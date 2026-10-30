//go:build !windows

package server

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/qinyongliang/linksphere/internal/agent"
	"github.com/qinyongliang/linksphere/internal/store"
)

func TestMCPSessionMultilineWaitsForFinalOutputAndExitCode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app, user, target := mcpSessionFixture(t)
	attachTunnelAgent(t, app, target.AgentID, agent.Config{Shell: "/bin/bash"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := app.createMCPSession(ctx, user, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer session.close("")
	client := mcpSessionTestClient(t, app, user)
	command := ":\nsleep 0.25\nprintf '%s%s\\n' multiline _completed\n(exit 7)"
	started := time.Now()
	result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "session_send_command", Arguments: map[string]any{
		"session_id": session.id, "command": command,
	}})
	if err != nil {
		t.Fatal(err)
	}
	var output mcpSessionCommandOutput
	decodeMCPSessionResult(t, result, &output)
	t.Logf("MCP command returned after %v, allowed=%v, exit_code=%d, tail_seen=%v", time.Since(started), output.Allowed, output.ExitCode, strings.Contains(output.Output, "multiline_completed"))
	if !output.Allowed || output.ExitCode != 7 || !strings.Contains(output.Output, "multiline_completed") {
		t.Fatalf("multiline command completed before final output/status: %+v", output)
	}
}

func TestMCPSessionMultilinePreservesStateAndLiteralSyntax(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app, user, target := mcpSessionFixture(t)
	attachTunnelAgent(t, app, target.AgentID, agent.Config{Shell: "/bin/bash"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := app.createMCPSession(ctx, user, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer session.close("")
	client := mcpSessionTestClient(t, app, user)
	directory := filepath.Join(t.TempDir(), "work ' quoted")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	steps := []struct {
		command string
		output  string
		code    int
	}{
		{"cd " + quote(directory) + "\nexport MCP_MULTILINE_TEST='value with \"quotes\" and $literal'", "", 0},
		{"printf '%s\\n' \"$PWD\" \"$MCP_MULTILINE_TEST\"", directory + "\r\nvalue with \"quotes\" and $literal", 0},
		{"cat <<'LITERAL_EOF' > literal.txt\nheredoc ' \" $HOME `printf not-expanded`\nLITERAL_EOF\nprintf '%s%s\\n' quoted _tail", "quoted_tail", 0},
		{"export MCP_MULTILINE_CRLF=kept\r\nprintf '%s%s\\n' crlf _tail", "crlf_tail", 0},
		{"printf '%s\\n' \"$MCP_MULTILINE_CRLF\"", "kept", 0},
		{"export MCP_MULTILINE_RETURN=before_return\nreturn 7\nprintf '%s%s\\n' unexpected _return_tail", "", 7},
		{"printf '%s\\n' \"$MCP_MULTILINE_RETURN\"", "before_return", 0},
		{":\nprintf 'unterminated", "", 2},
		{"printf '%s%s\\n' after _syntax_error", "after_syntax_error", 0},
	}
	for _, step := range steps {
		result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "session_send_command", Arguments: map[string]any{
			"session_id": session.id, "command": step.command,
		}})
		if err != nil {
			t.Fatal(err)
		}
		var output mcpSessionCommandOutput
		decodeMCPSessionResult(t, result, &output)
		if !output.Allowed || output.ExitCode != step.code || !strings.Contains(output.Output, step.output) {
			t.Fatalf("command %q lost shell state or literal syntax: %+v", step.command, output)
		}
		if strings.Contains(output.Output, "unexpected_return_tail") {
			t.Fatal("top-level return did not end the sourced multiline command")
		}
	}
	if info := session.mcpInfo(); info.CurrentDirectory != directory {
		t.Fatalf("multiline cd did not update directory: %q", info.CurrentDirectory)
	}
	if content, err := os.ReadFile(filepath.Join(directory, "literal.txt")); err != nil || string(content) != "heredoc ' \" $HOME `printf not-expanded`\n" {
		t.Fatalf("heredoc did not preserve literal contents: %q, %v", content, err)
	}
}

func TestMCPSessionMultilineKeepsTerminalStdinAndTopLevelState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app, user, target := mcpSessionFixture(t)
	attachTunnelAgent(t, app, target.AgentID, agent.Config{Shell: "/bin/bash"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := app.createMCPSession(ctx, user, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer session.close("")
	if result := session.trySendCommand(ctx, "set -- first 'second word'"); result.Err != nil || result.Result.ExitCode != 0 {
		t.Fatalf("set shell positional parameters: %+v", result)
	}
	done := make(chan terminalCommandAttempt, 1)
	command := "declare MCP_MULTILINE_DECLARED=declared\nfalse\nexport MCP_MULTILINE_AFTER_FALSE=reached\nprintf '%s%s\\n' stdin _ready\nread -r MCP_MULTILINE_INPUT\nprintf '%s%s\\n' input _received"
	go func() { done <- session.trySendCommand(ctx, command) }()
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(session.currentScreen(), "stdin_ready") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(session.currentScreen(), "stdin_ready") {
		t.Fatal("ordinary nonzero intermediate command stopped multiline execution")
	}
	select {
	case result := <-done:
		t.Fatalf("read consumed the command stream instead of terminal stdin: %+v", result)
	default:
	}
	if err := session.writeInput("typed value\r"); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.Err != nil || result.Result.ExitCode != 0 || !strings.Contains(result.Result.Output, "input_received") {
			t.Fatalf("multiline terminal input: %+v", result)
		}
	case <-ctx.Done():
		t.Fatal("multiline read did not receive terminal input")
	}
	command = "printf '%s\\n' \"$MCP_MULTILINE_DECLARED\" \"$MCP_MULTILINE_AFTER_FALSE\" \"$MCP_MULTILINE_INPUT\" \"$1\" \"$2\""
	result := session.trySendCommand(ctx, command)
	if result.Err != nil || result.Result.ExitCode != 0 || !strings.Contains(result.Result.Output, "declared\r\nreached\r\ntyped value\r\nfirst\r\nsecond word") {
		t.Fatalf("multiline command changed top-level state or positional parameters: %+v", result)
	}
}

func TestMCPSessionMultilineSourceProbeFailsClosed(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Simulate an unavailable source/process-substitution backend without
	// changing /dev/fd or other host resources.
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), []byte("builtin() { if [ \"$1\" = source ]; then printf probe-output; printf probe-error >&2; return 1; fi; command builtin \"$@\"; }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	app, user, target := mcpSessionFixture(t)
	attachTunnelAgent(t, app, target.AgentID, agent.Config{Shell: "/bin/bash"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := app.createMCPSession(ctx, user, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer session.close("")
	session.mu.Lock()
	startupOutput := session.output.String()
	session.mu.Unlock()
	if strings.Contains(startupOutput, "probe-output") || strings.Contains(startupOutput, "probe-error") {
		t.Fatalf("source availability probe leaked startup output: %q", startupOutput)
	}
	writes := 0
	session.mu.Lock()
	input := session.input
	session.input = terminalInputFunc(func(data []byte) (int, error) { writes++; return input.Write(data) })
	session.mu.Unlock()
	command := ":\r\nprintf '%s%s\\n' unexpected _probe_tail"
	result, err := mcpSessionTestClient(t, app, user).CallTool(ctx, &mcp.CallToolParams{Name: "session_send_command", Arguments: map[string]any{"session_id": session.id, "command": command}})
	if err != nil {
		t.Fatal(err)
	}
	var toolError strings.Builder
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			toolError.WriteString(text.Text)
		}
	}
	if !result.IsError || !strings.Contains(toolError.String(), "bundled Bash") || writes != 0 {
		t.Fatalf("failed availability probe allowed multiline input: %+v, writes=%d", result, writes)
	}
	logs, err := app.audit.Repository().ListCommandAuditLogs(ctx, store.AuditLogFilter{UserID: user.ID, TargetID: target.ID})
	if err != nil {
		t.Fatal(err)
	}
	commandAudits := 0
	for _, entry := range logs.Logs {
		if entry.SessionID != session.id || entry.RequestType != store.RequestExec {
			continue
		}
		commandAudits++
		code := -1
		if entry.ExitCode != nil {
			code = *entry.ExitCode
		}
		if code != 255 || entry.Command != command || entry.PolicyDecision != store.DecisionAllow || entry.PolicyReason != "no policy" || entry.EndedAt == nil {
			t.Fatalf("rejected multiline audit must preserve the original command/policy and record exit 255: exit=%d, audit=%+v", code, entry)
		}
	}
	if commandAudits != 1 {
		t.Fatalf("failed multiline request audit count = %d, want 1", commandAudits)
	}
	if result := session.trySendCommand(ctx, "printf '%s%s\\n' one _line"); result.Err != nil || result.Result.ExitCode != 0 || !strings.Contains(result.Result.Output, "one_line") || writes != 1 {
		t.Fatalf("failed multiline probe blocked ordinary commands: %+v, writes=%d", result, writes)
	}
}

func TestMCPSessionMultilineCancellationStopsChildAndRemainingLines(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app, user, target := mcpSessionFixture(t)
	attachTunnelAgent(t, app, target.AgentID, agent.Config{Shell: "/bin/bash"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := app.createMCPSession(ctx, user, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer session.close("")
	pidPath := filepath.Join(t.TempDir(), "shell.pid")
	if result := session.trySendCommand(ctx, "printf '%s' \"$$\" > '"+pidPath+"'"); result.Err != nil || result.Result.ExitCode != 0 {
		t.Fatalf("record session shell pid: %+v", result)
	}
	pidData, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	shellPID, err := strconv.Atoi(string(pidData))
	if err != nil || shellPID <= 0 {
		t.Fatalf("invalid shell pid %q: %v", pidData, err)
	}
	command := ":\nprintf '%s%s\\n' multiline _started\nsleep 30\nprintf '%s%s\\n' unexpected _tail"
	commandCtx, cancelCommand := context.WithCancel(ctx)
	defer cancelCommand()
	done := make(chan terminalCommandAttempt, 1)
	go func() { done <- session.trySendCommand(commandCtx, command) }()
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(session.currentScreen(), "multiline_started") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(session.currentScreen(), "multiline_started") {
		t.Fatal("multiline child did not start")
	}
	pid := 0
	for pid == 0 {
		processes, err := exec.Command("ps", "-eo", "pid=,ppid=,comm=").Output()
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(processes), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 3 && fields[1] == strconv.Itoa(shellPID) && filepath.Base(fields[2]) == "sleep" {
				pid, _ = strconv.Atoi(fields[0])
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("session did not start its foreground sleep child")
		}
		time.Sleep(10 * time.Millisecond)
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	cancelCommand()
	select {
	case result := <-done:
		if !errors.Is(result.Err, context.Canceled) || result.Result.ExitCode != 130 {
			t.Fatalf("multiline cancellation: %+v", result)
		}
	case <-ctx.Done():
		t.Fatal("multiline cancellation did not finish")
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("cancelled command left its child running: %v", err)
	}
	if strings.Contains(session.currentScreen(), "unexpected_tail") {
		t.Fatal("cancelled multiline command executed remaining lines")
	}
	if result := session.trySendCommand(ctx, "printf '%s%s\\n' next _command"); result.Err != nil || result.Result.ExitCode != 0 || !strings.Contains(result.Result.Output, "next_command") {
		t.Fatalf("cancelled session could not run next command: %+v", result)
	}
}

func TestMCPSessionMultilineInterruptsChildThatHandlesSIGINT(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	app, user, target := mcpSessionFixture(t)
	attachTunnelAgent(t, app, target.AgentID, agent.Config{Shell: "/bin/bash"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := app.createMCPSession(ctx, user, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer session.close("")
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	shellPIDPath := filepath.Join(home, "shell.pid")
	if result := session.trySendCommand(ctx, "printf '%s' \"$$\" > "+quote(shellPIDPath)); result.Err != nil || result.Result.ExitCode != 0 {
		t.Fatalf("record owned shell pid: %+v", result)
	}
	shellPID, err := os.ReadFile(shellPIDPath)
	if err != nil {
		t.Fatal(err)
	}
	childPath, childPIDPath, handledPath := filepath.Join(home, "child.sh"), filepath.Join(home, "child.pid"), filepath.Join(home, "handled")
	handler := "printf handled > " + quote(handledPath) + "; exit 0"
	child := "trap " + quote(handler) + " INT\nprintf '%s' \"$$\" > " + quote(childPIDPath) + "\nsleep 5\n"
	if err := os.WriteFile(childPath, []byte(child), 0600); err != nil {
		t.Fatal(err)
	}
	client := mcpSessionTestClient(t, app, user)
	var commandResult *mcp.CallToolResult
	var commandError error
	done := make(chan struct{})
	go func() {
		defer close(done)
		commandResult, commandError = client.CallTool(ctx, &mcp.CallToolParams{Name: "session_send_command", Arguments: map[string]any{
			"session_id": session.id, "command": ":\n/bin/bash " + quote(childPath) + "\nprintf '%s%s\\n' unexpected _handled_tail",
		}})
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if data, err := os.ReadFile(childPIDPath); err == nil && len(data) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("owned signal-handling child did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "session_interrupt", Arguments: map[string]any{"session_id": session.id}}); err != nil || result.IsError {
		t.Fatalf("interrupt owned command: %+v, %v", result, err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("interrupted signal-handling child did not finish")
	}
	if commandError != nil {
		t.Fatal(commandError)
	}
	var output mcpSessionCommandOutput
	decodeMCPSessionResult(t, commandResult, &output)
	if !output.Allowed || output.ExitCode != 130 || strings.Contains(output.Output, "unexpected_handled_tail") {
		t.Fatalf("interrupted script continued after its child handled SIGINT: %+v", output)
	}
	if data, err := os.ReadFile(handledPath); err != nil || string(data) != "handled" {
		t.Fatalf("child did not handle SIGINT before exiting zero: %q, %v", data, err)
	}
	childPID, err := os.ReadFile(childPIDPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(childPID))
	if err != nil || pid <= 1 {
		t.Fatalf("invalid owned child pid %q: %v", childPID, err)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("interrupted signal-handling child remains: %v", err)
	}
	if result := session.trySendCommand(ctx, "printf '%s' \"$$\" > "+quote(shellPIDPath)+"; printf '%s%s\\n' handled _next"); result.Err != nil || result.Result.ExitCode != 0 || !strings.Contains(result.Result.Output, "handled_next") {
		t.Fatalf("interrupted session could not be reused: %+v", result)
	}
	if after, err := os.ReadFile(shellPIDPath); err != nil || string(after) != string(shellPID) {
		t.Fatalf("interruption replaced the persistent shell: before=%q, after=%q, err=%v", shellPID, after, err)
	}
}

func TestMCPSessionMultilineLegacyAgentInterruptFailsBeforeInput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app, user, target := mcpSessionFixture(t)
	attachTunnelAgent(t, app, target.AgentID, agent.Config{Shell: "/bin/bash"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := app.createMCPSession(ctx, user, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer session.close("")
	session.setAgentCommandInterrupt(false)
	session.mu.Lock()
	writer := session.input.(*agentSessionInput)
	session.mu.Unlock()
	writes := 0
	stream := writer.stream
	writer.stream = terminalInputFunc(func(data []byte) (int, error) { writes++; return stream.Write(data) })
	command := ":\r\nprintf '%s%s\\n' unexpected _legacy_tail"
	result, err := mcpSessionTestClient(t, app, user).CallTool(ctx, &mcp.CallToolParams{Name: "session_send_command", Arguments: map[string]any{"session_id": session.id, "command": command}})
	if err != nil || !result.IsError || writes != 0 {
		t.Fatalf("legacy Agent received unsupported multiline input: result=%+v, err=%v, writes=%d", result, err, writes)
	}
	logs, err := app.audit.Repository().ListCommandAuditLogs(ctx, store.AuditLogFilter{UserID: user.ID, TargetID: target.ID})
	if err != nil {
		t.Fatal(err)
	}
	commands := 0
	for _, entry := range logs.Logs {
		if entry.SessionID == session.id && entry.RequestType == store.RequestExec {
			commands++
			if entry.ExitCode == nil || *entry.ExitCode != 255 || entry.Command != command || entry.PolicyDecision != store.DecisionAllow || entry.PolicyReason != "no policy" || entry.EndedAt == nil {
				t.Fatalf("legacy Agent refusal lost command/audit outcome: %+v", entry)
			}
		}
	}
	if commands != 1 {
		t.Fatalf("legacy Agent refusal audit count=%d", commands)
	}
	if result := session.trySendCommand(ctx, "printf '%s%s\\n' legacy _single_line"); result.Err != nil || result.Result.ExitCode != 0 || !strings.Contains(result.Result.Output, "legacy_single_line") || writes == 0 {
		t.Fatalf("legacy Agent lost single-line support: %+v, writes=%d", result, writes)
	}
}
