package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pkg/sftp"
	"github.com/qinyongliang/linksphere/internal/store"
)

const mcpReadMaxLines = 2000
const mcpReadMaxBytes = 50 * 1024

// Serialize MCP read-modify-write operations, including sessions on the same file.
var mcpFileMutationMu sync.Mutex

type mcpFileReadInput struct {
	SessionID string `json:"session_id" jsonschema:"Session owning the target filesystem"`
	Path      string `json:"path" jsonschema:"Path to the UTF-8 text file (absolute or relative to the session working directory)"`
	Offset    int    `json:"offset,omitempty" jsonschema:"Line number to start reading from (1-indexed; defaults to 1)"`
	Limit     int    `json:"limit,omitempty" jsonschema:"Maximum number of lines to read (defaults to 2000; capped at 2000)"`
}

type mcpTextEdit struct {
	OldText string `json:"oldText" jsonschema:"Exact text to replace; must occur once in the original file and not overlap another edit"`
	NewText string `json:"newText" jsonschema:"Replacement text; may be empty to delete text"`
}

type mcpFileEditInput struct {
	SessionID string        `json:"session_id"`
	Path      string        `json:"path" jsonschema:"File path (absolute or relative to the session working directory)"`
	Edits     []mcpTextEdit `json:"edits" jsonschema:"One or more non-overlapping replacements matched against the original file"`
}

type mcpFileWriteInput struct {
	SessionID string `json:"session_id"`
	Path      string `json:"path" jsonschema:"File path (absolute or relative to the session working directory)"`
	Content   string `json:"content" jsonschema:"Complete UTF-8 text to write; creates or overwrites the file"`
}

type mcpFileReadOutput struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	Offset     int    `json:"offset"`
	TotalLines int    `json:"total_lines"`
	Truncated  bool   `json:"truncated"`
	NextOffset int    `json:"next_offset,omitempty"`
}

type mcpFileWriteOutput struct {
	Path         string `json:"path"`
	BytesWritten int    `json:"bytes_written"`
}

func (a *App) addMCPSessionFileTools(s *mcp.Server, actor store.User) {
	mcp.AddTool(s, &mcp.Tool{Name: "read", Description: "Read a UTF-8 text file on a session's target, over SSH or Agent on any supported OS. Prefer this over shell cat/sed. Paths may be absolute or relative to the session working directory. Returns at most 2000 lines / 50 KiB; continue with next_offset when truncated. Text files are limited to 2 MiB. Requires file download permission."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpFileReadInput) (*mcp.CallToolResult, mcpFileReadOutput, error) {
			var out mcpFileReadOutput
			if in.Offset < 0 || in.Limit < 0 {
				return nil, out, errors.New("offset and limit must be positive when provided")
			}
			err := a.withMCPSessionFile(ctx, actor, in.SessionID, in.Path, "read", func(_ context.Context, client *sftp.Client, filePath string) error {
				content, err := readMCPSessionText(client, filePath)
				if err != nil {
					return err
				}
				out, err = paginateMCPSessionText(content, in.Offset, in.Limit)
				out.Path = filePath
				return err
			})
			return nil, out, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "edit", Description: "Make precise edits to a UTF-8 text file on a session's target. Use edits:[{oldText,newText}] with exact unique text from read. Each edit matches the original file; repeated, missing, or overlapping matches fail before writing. Preserves CRLF line endings. Requires file download and upload permissions. Text files are limited to 2 MiB."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpFileEditInput) (*mcp.CallToolResult, mcpFileWriteOutput, error) {
			var out mcpFileWriteOutput
			err := a.withMCPSessionFile(ctx, actor, in.SessionID, in.Path, "edit", func(ctx context.Context, client *sftp.Client, filePath string) error {
				content, err := readMCPSessionText(client, filePath)
				if err != nil {
					return err
				}
				updated, err := applyMCPSessionEdits(content, in.Edits)
				if err != nil {
					return err
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := writeMCPSessionText(ctx, client, filePath, updated); err != nil {
					return err
				}
				out = mcpFileWriteOutput{Path: filePath, BytesWritten: len(updated)}
				return nil
			})
			return nil, out, err
		})
	mcp.AddTool(s, &mcp.Tool{Name: "write", Description: "Write UTF-8 text to a file on a session's target. Creates the file and parent directories if needed, overwrites an existing file. Use for new files or complete rewrites; prefer edit for precise changes. Requires file upload permission. Content is limited to 2 MiB."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpFileWriteInput) (*mcp.CallToolResult, mcpFileWriteOutput, error) {
			var out mcpFileWriteOutput
			if err := validateMCPSessionText(in.Content); err != nil {
				return nil, out, err
			}
			err := a.withMCPSessionFile(ctx, actor, in.SessionID, in.Path, "write", func(ctx context.Context, client *sftp.Client, filePath string) error {
				if err := client.MkdirAll(mcpSessionPathDir(filePath)); err != nil {
					return err
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := writeMCPSessionText(ctx, client, filePath, in.Content); err != nil {
					return err
				}
				out = mcpFileWriteOutput{Path: filePath, BytesWritten: len(in.Content)}
				return nil
			})
			return nil, out, err
		})
}

func (a *App) withMCPSessionFile(ctx context.Context, actor store.User, sessionID, inputPath, action string, operation func(context.Context, *sftp.Client, string) error) (opErr error) {
	session, err := a.mcpSessionForUser(ctx, actor, sessionID)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	stopSession := context.AfterFunc(session.ctx, cancel)
	defer cancel()
	defer stopSession()
	// Shell commands and file operations share cwd and cannot change it mid-operation.
	unlock, acquired := session.lockCommand(true)
	if !acquired {
		return errTerminalSessionBusy
	}
	defer unlock()
	session.mu.Lock()
	cwd, closed := session.currentDirectory, session.closed
	session.mu.Unlock()
	if closed {
		return errTerminalSessionClosed
	}
	if !mcpSessionPathIsAbsolute(inputPath) {
		cwd, err = session.waitForMCPDirectory(ctx)
		if err != nil {
			return err
		}
	}
	filePath, err := resolveMCPSessionPath(cwd, inputPath)
	if err != nil {
		return err
	}
	started := time.Now().UTC()
	sourceIP, ok := ctx.Value(mcpSessionSourceIPKey{}).(string)
	if !ok {
		sourceIP = session.sourceIP
	}
	target, decision, upload, download, err := a.targetSFTPAccess(ctx, session.target.ID, actor, sourceIP)
	if err != nil {
		return err
	}
	defer func() {
		code, reason := 0, decision.Reason
		if opErr != nil {
			code, reason = 255, opErr.Error()
		}
		if decision.Action == store.DecisionDeny {
			code = 126
		}
		ended := time.Now().UTC()
		_, _ = a.createAuditLog(context.WithoutCancel(ctx), store.CreateCommandAuditLogParams{
			UserID: actor.ID, TargetID: target.ID, OrganizationID: organizationIDForTarget(target),
			PublicKeyName: "MCP session", SessionID: session.id, Command: "sftp " + action + " " + filePath,
			RequestType: store.RequestSFTP, PolicyDecision: decision.Action, PolicyReason: reason,
			ExitCode: &code, StartedAt: started, EndedAt: &ended, RemoteAddress: sourceIP,
		})
	}()
	if decision.Action == store.DecisionDeny {
		return fmt.Errorf("file access denied: %s", decision.Reason)
	}
	if ((action == "read" || action == "edit") && !download) || ((action == "write" || action == "edit") && !upload) {
		decision.Action = store.DecisionDeny
		return errors.New("file operation is not allowed by upload/download policy")
	}
	if !sameMCPSessionFileTarget(session.target, target) {
		return errors.New("target connection changed; create a new session before using file tools")
	}
	if target.CredentialID != "" {
		credential, err := a.store.Repository().GetSSHCredential(ctx, target.CredentialID)
		if err != nil {
			return err
		}
		if credential.OwnerType != target.OwnerType || credential.OwnerID != target.OwnerID {
			return errOwnerAccess
		}
		if credential.UpdatedAt.After(session.startedAt) {
			return errors.New("target credential changed; create a new session before using file tools")
		}
		// Pin this call to the checked login, rather than resolving a mutable credential again.
		target.RemoteUsername, target.AuthType = credential.Username, credential.AuthType
		target.EncryptedSecret = credential.EncryptedSecret
		target.CredentialID = ""
	}
	if action != "read" {
		mcpFileMutationMu.Lock()
		defer mcpFileMutationMu.Unlock()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	client, closeClient, err := a.openSFTPClient(ctx, target)
	if err != nil {
		return err
	}
	var once sync.Once
	cleanup := func() { once.Do(closeClient) }
	stopIO := context.AfterFunc(ctx, cleanup)
	defer cleanup()
	defer stopIO()
	if err := operation(ctx, client, filePath); err != nil {
		return err
	}
	return ctx.Err()
}

func sameMCPSessionFileTarget(original, current store.SSHTarget) bool {
	return original.TargetType == current.TargetType && original.Host == current.Host && original.Port == current.Port &&
		original.RemoteUsername == current.RemoteUsername && original.AgentID == current.AgentID && original.ProxyTargetID == current.ProxyTargetID && original.CredentialID == current.CredentialID
}

func mcpSessionPathDir(filePath string) string {
	dir := pathpkg.Dir(filePath)
	if strings.HasPrefix(filePath, "//") {
		dir = "/" + dir
	}
	return dir
}

func mcpSessionPathIsAbsolute(input string) bool {
	input = strings.ReplaceAll(input, `\`, "/")
	return strings.HasPrefix(input, "/") || (len(input) >= 3 && input[1] == ':' && input[2] == '/' && ((input[0] >= 'A' && input[0] <= 'Z') || (input[0] >= 'a' && input[0] <= 'z')))
}

func resolveMCPSessionPath(cwd, input string) (string, error) {
	if strings.TrimSpace(input) == "" || strings.ContainsAny(input, "\x00\r\n\x1b") {
		return "", errors.New("a valid file path is required")
	}
	input = strings.ReplaceAll(input, `\`, "/")
	cwd = strings.ReplaceAll(cwd, `\`, "/")
	isDrive := func(p string) bool {
		return len(p) >= 2 && ((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z')) && p[1] == ':'
	}
	if isDrive(input) && !mcpSessionPathIsAbsolute(input) {
		return "", errors.New("drive-relative paths are ambiguous; use an absolute Windows path")
	}
	if !mcpSessionPathIsAbsolute(input) {
		if !mcpSessionPathIsAbsolute(cwd) {
			return "", errors.New("session working directory unavailable; use an absolute file path")
		}
		input = strings.TrimRight(cwd, "/") + "/" + input
	}
	unc := strings.HasPrefix(input, "//")
	clean := pathpkg.Clean(input)
	if unc {
		clean = "/" + clean
	}
	if clean == "/" || (isDrive(clean) && len(clean) <= 3) {
		return "", errors.New("file path must not be a filesystem root")
	}
	return cleanSFTPPath(clean), nil
}

func validateMCPSessionText(content string) error {
	if len(content) > webFileEditorMaxBytes {
		return errors.New("text exceeds the 2 MiB file limit")
	}
	if !utf8.ValidString(content) || strings.ContainsRune(content, 0) {
		return errors.New("file content must be UTF-8 text without NUL bytes")
	}
	return nil
}

func readMCPSessionText(client *sftp.Client, filePath string) (string, error) {
	file, err := client.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("path must be a regular text file")
	}
	if info.Size() > webFileEditorMaxBytes {
		return "", errors.New("text exceeds the 2 MiB file limit")
	}
	content, err := io.ReadAll(io.LimitReader(file, webFileEditorMaxBytes+1))
	if err != nil {
		return "", err
	}
	text := string(content)
	return text, validateMCPSessionText(text)
}

func writeMCPSessionText(ctx context.Context, client *sftp.Client, filePath, content string) error {
	info, err := client.Lstat(filePath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if info != nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("symlink destinations are not supported; use the resolved file path")
		}
		if !info.Mode().IsRegular() {
			return errors.New("path must be a regular text file")
		}
	}
	tempPath := strings.TrimRight(mcpSessionPathDir(filePath), "/") + "/.linksphere-mcp-" + uuid.NewString()
	file, err := client.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close(); _ = client.Remove(tempPath) }()
	if info != nil {
		if err := file.Chmod(info.Mode().Perm()); err != nil {
			return err
		}
	}
	_, err = io.WriteString(file, content)
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Never remove the original to accommodate a server lacking overwrite rename.
	if _, supported := client.HasExtension("posix-rename@openssh.com"); supported {
		return client.PosixRename(tempPath, filePath)
	}
	return client.Rename(tempPath, filePath)
}

func paginateMCPSessionText(content string, offset, limit int) (mcpFileReadOutput, error) {
	var out mcpFileReadOutput
	if offset < 0 || limit < 0 {
		return out, errors.New("offset and limit must be positive when provided")
	}
	if offset == 0 {
		offset = 1
	}
	if limit == 0 || limit > mcpReadMaxLines {
		limit = mcpReadMaxLines
	}
	lines := strings.SplitAfter(content, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	out.Offset, out.TotalLines = offset, len(lines)
	if offset > len(lines) {
		return out, nil
	}
	start := offset - 1
	end := start + min(limit, len(lines)-start)
	var result strings.Builder
	i := start
	for ; i < end; i++ {
		if result.Len()+len(lines[i]) > mcpReadMaxBytes {
			break
		}
		result.WriteString(lines[i])
	}
	if i == start && i < end {
		return out, errors.New("a single line exceeds the 50 KiB read limit")
	}
	out.Content, out.Truncated = result.String(), i < len(lines)
	if out.Truncated {
		out.NextOffset = i + 1
	}
	return out, nil
}

func applyMCPSessionEdits(content string, edits []mcpTextEdit) (string, error) {
	if err := validateMCPSessionText(content); err != nil {
		return "", err
	}
	if len(edits) == 0 {
		return "", errors.New("at least one edit is required")
	}
	crlf := strings.Contains(content, "\r\n")
	original := strings.ReplaceAll(content, "\r\n", "\n")
	type replacement struct {
		start, end int
		text       string
	}
	replacements := make([]replacement, 0, len(edits))
	for _, edit := range edits {
		if err := validateMCPSessionText(edit.OldText); err != nil {
			return "", err
		}
		if err := validateMCPSessionText(edit.NewText); err != nil {
			return "", err
		}
		old, updated := strings.ReplaceAll(edit.OldText, "\r\n", "\n"), strings.ReplaceAll(edit.NewText, "\r\n", "\n")
		if old == "" {
			return "", errors.New("oldText must not be empty")
		}
		index := strings.Index(original, old)
		if index < 0 {
			return "", errors.New("oldText was not found in the original file; read the file again")
		}
		if strings.LastIndex(original, old) != index {
			return "", errors.New("oldText matches more than once; include enough surrounding text to make it unique")
		}
		replacements = append(replacements, replacement{index, index + len(old), updated})
	}
	sort.Slice(replacements, func(i, j int) bool { return replacements[i].start < replacements[j].start })
	var out strings.Builder
	last := 0
	for _, edit := range replacements {
		if edit.start < last {
			return "", errors.New("edits must not overlap in the original file")
		}
		out.WriteString(original[last:edit.start])
		out.WriteString(edit.text)
		last = edit.end
		if out.Len() > webFileEditorMaxBytes {
			return "", errors.New("edited text exceeds the 2 MiB file limit")
		}
	}
	out.WriteString(original[last:])
	updated := out.String()
	if crlf {
		updated = strings.ReplaceAll(updated, "\n", "\r\n")
	}
	return updated, validateMCPSessionText(updated)
}
