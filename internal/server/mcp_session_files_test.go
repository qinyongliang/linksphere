package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pkg/sftp"
	"github.com/qinyongliang/linksphere/internal/store"
)

func TestMCPSessionFilesOverSSHAndAgent(t *testing.T) {
	for _, transport := range []string{"agent", "ssh"} {
		t.Run(transport, func(t *testing.T) {
			srv, httpClient, app := newAPITestServer(t)
			defer srv.Close()
			postJSON(t, httpClient, srv.URL+"/api/auth/login", map[string]string{"email": "admin", "password": "admin-pass"}, http.StatusOK, nil)
			ctx := context.Background()
			user, _ := app.store.Repository().GetUserByEmail(ctx, "admin")
			org, _ := app.store.Repository().GetPersonalOrganizationForUser(ctx, user.ID)
			attachTunnelAgent(t, app, "mcp-file-agent")
			target := uploadAgentTarget(t, app, user, org, "mcp-file-agent")
			if transport == "ssh" {
				address, closeServer := startTestSFTPServer(t, testSFTPModeSubsystem)
				defer closeServer()
				target = tunnelSSHTarget(t, app, user, org, "mcp-files", address, "")
			}
			root := t.TempDir()
			terminal := app.terminalSessions.create("mcp-file-session", user.ID, target, "127.0.0.1", 80, 24, nil)
			defer terminal.close("")
			terminal.mu.Lock()
			terminal.currentDirectory = filepath.ToSlash(root)
			terminal.mu.Unlock()
			var token apiCreateMCPTokenResponse
			postJSON(t, httpClient, srv.URL+"/api/mcp-tokens", map[string]string{"name": "files"}, http.StatusCreated, &token)
			client := mcp.NewClient(&mcp.Implementation{Name: "file-test"}, nil)
			connection, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerRoundTripper{token: token.TokenValue}}, DisableStandaloneSSE: true}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			call := func(name string, args map[string]any, wantError bool) *mcp.CallToolResult {
				t.Helper()
				args["session_id"] = terminal.id
				result, err := connection.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
				if err != nil || result == nil || result.IsError != wantError {
					data, _ := json.Marshal(result)
					t.Fatalf("%s = %s, %v; error wanted %v", name, data, err, wantError)
				}
				return result
			}
			tools, err := connection.ListTools(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"read", "edit", "write"} {
				if !mcpHasTool(tools, name) || !mcpToolRequires(tools, name, "session_id") || !mcpToolRequires(tools, name, "path") {
					t.Fatalf("missing file tool schema: %s", name)
				}
			}
			call("write", map[string]any{"path": "nested/example.txt", "content": "first\r\nsecond\r\nthird\r\n"}, false)
			read := call("read", map[string]any{"path": `nested\example.txt`, "offset": 2, "limit": 1}, false)
			encoded, _ := json.Marshal(read.StructuredContent)
			var page mcpFileReadOutput
			if err := json.Unmarshal(encoded, &page); err != nil || page.Content != "second\r\n" || page.NextOffset != 3 {
				t.Fatalf("read page: %s, %v", encoded, err)
			}
			call("edit", map[string]any{"path": "nested/example.txt", "edits": []map[string]string{{"oldText": "first", "newText": "one"}, {"oldText": "third", "newText": "last"}}}, false)
			path := filepath.Join(root, "nested", "example.txt")
			want := "one\r\nsecond\r\nlast\r\n"
			assertFile := func() {
				t.Helper()
				data, err := os.ReadFile(path)
				if err != nil || string(data) != want {
					t.Fatalf("file = %q, %v; want %q", data, err, want)
				}
			}
			assertFile()
			// Validate the whole edit batch before the first replacement writes.
			call("edit", map[string]any{"path": path, "edits": []map[string]string{{"oldText": "one", "newText": "changed"}, {"oldText": "missing", "newText": "bad"}}}, true)
			assertFile()
			// A canceled staged write must leave the original intact.
			err = app.withMCPSessionFile(ctx, user, terminal.id, path, "write", func(ctx context.Context, client *sftp.Client, filePath string) error {
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				return writeMCPSessionText(canceled, client, filePath, "must not replace original")
			})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled commit: %v", err)
			}
			assertFile()
			temps, _ := filepath.Glob(filepath.Join(root, "nested", ".linksphere-mcp-*"))
			if len(temps) != 0 {
				t.Fatalf("staged files were not removed: %v", temps)
			}
			terminal.commandMu.Lock()
			call("read", map[string]any{"path": path}, true)
			terminal.commandMu.Unlock()
			wantAudits := 8
			for _, name := range []string{"existing-link", "dangling-link"} {
				link := filepath.Join(root, name)
				destination := path
				if name == "dangling-link" {
					destination = filepath.Join(root, "missing-file")
				}
				if err := os.Symlink(destination, link); err != nil {
					t.Logf("symlink case unavailable on this host: %v", err)
					continue
				}
				call("write", map[string]any{"path": link, "content": "must not replace link"}, true)
				info, err := os.Lstat(link)
				if err != nil || info.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("symlink was replaced: %v", err)
				}
				wantAudits++
				assertFile()
			}
			attachAllowSFTPPolicyForTargetAccess(t, app, org.ID, target.ID, false, true)
			call("read", map[string]any{"path": path}, false)
			call("write", map[string]any{"path": path, "content": "denied"}, true)
			call("edit", map[string]any{"path": path, "edits": []map[string]string{{"oldText": "one", "newText": "denied"}}}, true)
			assertFile()
			logs, err := app.audit.Repository().ListCommandAuditLogs(ctx, store.AuditLogFilter{TargetID: target.ID, RequestType: store.RequestSFTP})
			if err != nil || len(logs.Logs) != wantAudits {
				t.Fatalf("file audit count = %d, %v", len(logs.Logs), err)
			}
			for _, log := range logs.Logs {
				if log.SessionID != terminal.id || log.PublicKeyName != "MCP session" {
					t.Fatalf("file audit detached from session: %+v", log)
				}
			}
			// A different user's session id must not authorize a filesystem operation.
			other := app.terminalSessions.create("other-file-session", "other-user", target, "127.0.0.1", 80, 24, nil)
			defer other.close("")
			denied, err := connection.CallTool(ctx, &mcp.CallToolParams{Name: "read", Arguments: map[string]any{"session_id": other.id, "path": path}})
			if err != nil || denied == nil || !denied.IsError {
				t.Fatalf("another user's file session was accepted: %+v, %v", denied, err)
			}
			if _, err := app.store.Repository().UpdateSSHTarget(ctx, target.ID, store.UpdateSSHTargetParams{Host: "192.0.2.99"}); err != nil {
				t.Fatal(err)
			}
			changed := call("read", map[string]any{"path": path}, true)
			changedJSON, _ := json.Marshal(changed)
			if !strings.Contains(string(changedJSON), "target connection changed") {
				t.Fatalf("target update bypassed session binding: %s", changedJSON)
			}
			assertFile()
		})
	}
}

func TestMCPSessionFilePaths(t *testing.T) {
	for _, tc := range []struct{ cwd, path, want string }{
		{"/srv/app", "../config/app.json", "/srv/config/app.json"},
		{`C:\work\app`, `..\config\app.json`, "/C:/work/config/app.json"},
		{"", `D:\data\file.txt`, "/D:/data/file.txt"},
		{"", "/etc/example.conf", "/etc/example.conf"},
		{"", `\\example.invalid\share\file.txt`, "//example.invalid/share/file.txt"},
	} {
		got, err := resolveMCPSessionPath(tc.cwd, tc.path)
		if err != nil || got != tc.want {
			t.Errorf("resolve(%q, %q) = %q, %v; want %q", tc.cwd, tc.path, got, err, tc.want)
		}
	}
	for _, tc := range []struct{ cwd, path string }{{"", "relative.txt"}, {"/srv", ""}, {"/srv", "bad\x00path"}, {"/srv", "C:relative.txt"}} {
		if _, err := resolveMCPSessionPath(tc.cwd, tc.path); err == nil {
			t.Errorf("accepted invalid/ambiguous path %q in %q", tc.path, tc.cwd)
		}
	}
}

func TestMCPSessionFileEdit(t *testing.T) {
	got, err := applyMCPSessionEdits("first\r\nsecond\r\nthird\r\n", []mcpTextEdit{{OldText: "third", NewText: "last"}, {OldText: "first\nsecond", NewText: "one\ntwo"}})
	if err != nil || got != "one\r\ntwo\r\nlast\r\n" {
		t.Fatalf("edit = %q, %v", got, err)
	}
	for _, tc := range []struct {
		name, content string
		edits         []mcpTextEdit
	}{
		{"empty batch", "abc", nil},
		{"empty match", "abc", []mcpTextEdit{{OldText: "", NewText: "x"}}},
		{"missing match", "abc", []mcpTextEdit{{OldText: "x", NewText: "y"}}},
		{"repeated match", "abc abc", []mcpTextEdit{{OldText: "abc", NewText: "x"}}},
		{"overlapping occurrences", "aaa", []mcpTextEdit{{OldText: "aa", NewText: "x"}}},
		{"overlapping edits", "abcd", []mcpTextEdit{{OldText: "abc", NewText: "x"}, {OldText: "bc", NewText: "y"}}},
		{"invalid UTF8", "abc", []mcpTextEdit{{OldText: "abc", NewText: "\xff"}}},
		{"binary", "a\x00b", []mcpTextEdit{{OldText: "a", NewText: "x"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := applyMCPSessionEdits(tc.content, tc.edits); err == nil {
				t.Fatal("expected edit error")
			}
		})
	}
}

func TestMCPSessionFileReadPagination(t *testing.T) {
	result, err := paginateMCPSessionText("first\nsecond\nthird\n", 2, 1)
	if err != nil || result.Content != "second\n" || result.Offset != 2 || result.NextOffset != 3 || !result.Truncated || result.TotalLines != 3 {
		t.Fatalf("unexpected page: %+v, %v", result, err)
	}
	end, err := paginateMCPSessionText("first\n", 2, 0)
	if err != nil || end.Content != "" || end.Truncated {
		t.Fatalf("EOF = %+v, %v", end, err)
	}
	for _, tc := range []struct{ offset, limit int }{{-1, 0}, {1, -1}} {
		if _, err := paginateMCPSessionText("x", tc.offset, tc.limit); err == nil {
			t.Fatal("accepted negative pagination")
		}
	}
	bounded, err := paginateMCPSessionText(strings.Repeat("line\n", 3000), 0, 0)
	if err != nil || bounded.NextOffset != 2001 || !bounded.Truncated {
		t.Fatalf("unbounded page: %+v, %v", bounded, err)
	}
}
