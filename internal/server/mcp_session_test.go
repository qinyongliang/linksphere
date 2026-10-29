package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/qinyongliang/linksphere/internal/protocol"
	"github.com/qinyongliang/linksphere/internal/store"
)

func mcpSessionTestClient(t *testing.T, app *App, user store.User) *mcp.ClientSession {
	t.Helper()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := app.newMCPServer(mcpActor{User: user, ToolGroups: map[string]bool{"session": true}}).Connect(context.Background(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "session-test"}, nil).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close(); _ = ss.Close() })
	return cs
}

func mcpSessionFixture(t *testing.T) (*App, store.User, store.SSHTarget) {
	t.Helper()
	app, user, target := desktopTestApp(t)
	enrollment, err := app.store.Repository().CreateAgentEnrollment(context.Background(), store.CreateAgentEnrollmentParams{
		OwnerType: target.OwnerType, OwnerID: target.OwnerID, TokenHash: codeHash("session-fixture"), Label: "session-fixture", CreatedBy: user.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := app.store.Repository().UpsertAgent(context.Background(), store.UpsertAgentParams{
		OwnerType: target.OwnerType, OwnerID: target.OwnerID, EnrollmentID: enrollment.ID, CurrentRuntimeID: target.AgentID,
	})
	if err != nil {
		t.Fatal(err)
	}
	target, err = app.store.Repository().UpdateSSHTarget(context.Background(), target.ID, store.UpdateSSHTargetParams{AgentID: agent.ID})
	if err != nil {
		t.Fatal(err)
	}
	return app, user, target
}

// This fixture isolates startup failures, including peers that never finish the
// shell handshake. Normal execution is covered with an embedded Agent below.
func fakeMCPShellAgent(t *testing.T, app *App, id, mode string) <-chan struct{} {
	t.Helper()
	a, b := net.Pipe()
	server, err := yamux.Server(a, nil)
	if err != nil {
		t.Fatal(err)
	}
	client, err := yamux.Client(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	app.registry.Register(id, server)
	done := make(chan struct{})
	go func() {
		defer close(done)
		stream, err := client.AcceptStream()
		if err != nil {
			return
		}
		defer stream.Close()
		reader := bufio.NewReader(stream)
		req, err := protocol.ReadJSONLine[protocol.StreamRequest](reader)
		if err != nil {
			return
		}
		if mode == "no-tcp-handshake" && req.Type != protocol.StreamTCP {
			t.Errorf("expected proxy TCP handshake, got %s", req.Type)
			return
		}
		if mode == "reject" {
			_ = protocol.WriteJSONLine(stream, protocol.StreamResponse{OK: false, Error: "shell unavailable"})
			return
		}
		if mode != "no-handshake" && mode != "no-tcp-handshake" {
			_ = protocol.WriteJSONLine(stream, protocol.StreamResponse{OK: true})
			if mode == "ready" {
				_ = protocol.WriteFrame(stream, protocol.Frame{Type: protocol.FrameStdout, Data: []byte("\x1b]633;A\a\x1b]633;P;Cwd=/example\a")})
			} else {
				_ = protocol.WriteFrame(stream, protocol.Frame{Type: protocol.FrameStdout, Data: []byte("unsupported shell prompt> ")})
			}
		}
		_, _ = io.Copy(io.Discard, reader)
	}()
	t.Cleanup(func() { server.Close(); client.Close(); app.registry.Unregister(id, server); <-done })
	return done
}

func decodeMCPSessionResult(t *testing.T, result *mcp.CallToolResult, output any) {
	t.Helper()
	if result.IsError {
		t.Fatalf("MCP tool failed: %+v", result.Content)
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, output); err != nil {
		t.Fatal(err)
	}
}

func TestMCPSessionLifecycleAndOwnership(t *testing.T) {
	app, user, target := mcpSessionFixture(t)
	done := fakeMCPShellAgent(t, app, target.AgentID, "ready")
	client := mcpSessionTestClient(t, app, user)
	tools, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"session_targets", "session_create", "session_close", "session_send_command", "session_list", "read", "edit", "write"} {
		if !mcpHasTool(tools, name) {
			t.Fatalf("session-only token missing tool %s", name)
		}
	}
	if mcpHasTool(tools, "target_create") || mcpHasTool(tools, "target_list") {
		t.Fatal("session tools exposed target management")
	}
	call := func(client *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	var targets mcpTargetListOutput
	decodeMCPSessionResult(t, call(client, "session_targets", map[string]any{}), &targets)
	if len(targets.Targets) != 1 || targets.Targets[0].Target.ID != target.ID {
		t.Fatalf("accessible target list = %+v", targets)
	}
	var created mcpSessionInfo
	decodeMCPSessionResult(t, call(client, "session_create", map[string]any{"target_id": target.ID}), &created)
	if created.ID == "" || !created.Managed || created.CurrentDirectory != "/example" || created.HeartbeatTimeoutS != 600 {
		t.Fatalf("created session = %+v", created)
	}
	session, err := app.terminalSessions.getForUser(user.ID, created.ID)
	if err != nil || len(session.clients) != 0 {
		t.Fatalf("session requires a browser: %v", err)
	}
	// Even a system administrator cannot use another actor's session identifier.
	outsider := mcpSessionTestClient(t, app, store.User{ID: "other-user", IsSystemAdmin: true})
	for _, name := range []string{"session_close", "session_screen", "session_interrupt", "session_send_command"} {
		if result := call(outsider, name, map[string]any{"session_id": created.ID, "command": "echo denied"}); !result.IsError {
			t.Fatalf("other actor used %s", name)
		}
	}
	var listed mcpSessionListOutput
	decodeMCPSessionResult(t, call(client, "session_list", map[string]any{}), &listed)
	if len(listed.Sessions) != 1 || !listed.Sessions[0].Managed {
		t.Fatalf("managed session absent from list: %+v", listed)
	}
	decodeMCPSessionResult(t, call(client, "session_close", map[string]any{"session_id": created.ID}), &mcpOK{})
	if _, err := app.terminalSessions.getForUser(user.ID, created.ID); err == nil {
		t.Fatal("closed session remains accessible")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("session_close did not release shell stream")
	}
}

func TestMCPSessionStartupFailureClosesResources(t *testing.T) {
	for _, mode := range []string{"reject", "no-prompt", "no-handshake"} {
		t.Run(mode, func(t *testing.T) {
			app, user, target := mcpSessionFixture(t)
			done := fakeMCPShellAgent(t, app, target.AgentID, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			if session, err := app.createMCPSession(ctx, user, target.ID); err == nil || session != nil {
				t.Fatalf("startup returned success for %s", mode)
			}
			if sessions := app.terminalSessions.listForUser(user.ID); len(sessions) != 0 {
				t.Fatalf("failed startup leaked sessions: %+v", sessions)
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("failed startup did not close transport")
			}
		})
	}
}

func TestMCPSessionStartupCancelsAgentProxyTCPHandshake(t *testing.T) {
	app, user, proxy := mcpSessionFixture(t)
	peerDone := fakeMCPShellAgent(t, app, proxy.AgentID, "no-tcp-handshake")
	target, err := app.store.Repository().CreateSSHTarget(context.Background(), store.CreateSSHTargetParams{
		OwnerType: proxy.OwnerType, OwnerID: proxy.OwnerID, Alias: "agent-proxy-ssh", TargetType: store.TargetDirect,
		Host: "192.0.2.12", Port: 22, RemoteUsername: "tester", AuthType: store.AuthPassword, ProxyTargetID: proxy.ID, CreatedBy: user.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if session, err := app.createMCPSession(ctx, user, target.ID); err == nil || session != nil {
		t.Fatalf("stalled Agent proxy handshake returned success: %v %v", session, err)
	}
	select {
	case <-peerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("canceled Agent proxy handshake did not close transport")
	}
	done := make(chan struct{})
	go func() { _ = app.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("App.Close waited on canceled Agent proxy TCP handshake")
	}
}

func TestMCPSessionTerminalPolicyGateAndRecheck(t *testing.T) {
	app, user, target := mcpSessionFixture(t)
	policy, err := app.store.Repository().CreateCommandPolicy(context.Background(), store.CreateCommandPolicyParams{
		OwnerType: target.OwnerType, OwnerID: target.OwnerID, Name: "terminal-gate", DefaultAction: store.DecisionAllow,
		AllowSSHInteractive: true, AllowWebTerminal: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.Repository().AttachPolicyToTarget(context.Background(), policy.ID, target.ID); err != nil {
		t.Fatal(err)
	}
	if session, err := app.createMCPSession(context.Background(), user, target.ID); err == nil || session != nil || !strings.Contains(err.Error(), "web terminal disabled") {
		t.Fatalf("terminal gate bypassed: session=%v err=%v", session, err)
	}
	session := app.terminalSessions.createSession("existing", user.ID, target, "", 80, 24, nil, true)
	defer session.close("")
	if err := app.authorizeMCPSessionCommand(context.Background(), user, session); err == nil {
		t.Fatal("existing managed session bypassed revoked terminal policy")
	}
	if _, err := app.mcpSessionForUser(context.Background(), user, session.id); err != nil {
		t.Fatalf("generic session lookup must leave capability checks to tools: %v", err)
	}
	client := mcpSessionTestClient(t, app, user)
	result, err := client.CallTool(context.Background(), &mcp.CallToolParams{Name: "session_send_command", Arguments: map[string]any{
		"session_id": session.id, "command": "echo forbidden",
	}})
	if err != nil {
		t.Fatal(err)
	}
	var denied mcpSessionCommandOutput
	decodeMCPSessionResult(t, result, &denied)
	if denied.Allowed || denied.ExitCode != 126 || !strings.Contains(denied.PolicyReason, "web terminal disabled") {
		t.Fatalf("revoked terminal capability returned wrong decision: %+v", denied)
	}
	logs, err := app.audit.Repository().ListCommandAuditLogs(context.Background(), store.AuditLogFilter{UserID: user.ID, TargetID: target.ID})
	if err != nil || len(logs.Logs) != 2 {
		t.Fatalf("denied startup or command audit missing: %+v %v", logs, err)
	}
	var commandAudited bool
	for _, entry := range logs.Logs {
		if entry.SessionID == session.id && entry.Command == "echo forbidden" && entry.RequestType == store.RequestExec && entry.PolicyDecision == store.DecisionDeny && entry.PolicyReason == denied.PolicyReason {
			commandAudited = true
		}
	}
	if !commandAudited {
		t.Fatal("revoked terminal capability denial has no session-linked command audit")
	}
}

func TestMCPSessionPreservesShellContextWithoutBrowser(t *testing.T) {
	app, user, target := mcpSessionFixture(t)
	attachTunnelAgent(t, app, target.AgentID)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := app.createMCPSession(ctx, user, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer session.close("")
	dir := t.TempDir()
	commands := []string{"cd '" + dir + "'", "export MCP_CONTEXT_TEST=persistent", "printf '%s' \"$MCP_CONTEXT_TEST\""}
	if runtime.GOOS == "windows" {
		if strings.Contains(strings.ToLower(protocol.DefaultShell()), "powershell") || strings.Contains(strings.ToLower(protocol.DefaultShell()), "pwsh") {
			commands = []string{"Set-Location '" + dir + "'", "$env:MCP_CONTEXT_TEST='persistent'", "Write-Output $env:MCP_CONTEXT_TEST"}
		} else {
			commands = []string{"cd /d \"" + dir + "\"", "set MCP_CONTEXT_TEST=persistent", "echo %MCP_CONTEXT_TEST%"}
		}
	}
	for index, command := range commands {
		run := app.runCommandInTerminalSession(ctx, session, command, terminalSessionCommandOptions{UserID: user.ID, RequireAIReview: true})
		if run.Err != nil || !run.Allowed || run.ExitCode != 0 {
			t.Fatalf("command %d failed: %+v", index, run)
		}
		if index == 2 && !strings.Contains(run.Output, "persistent") {
			t.Fatalf("environment lost across calls: %q", run.Output)
		}
	}
	if info := session.mcpInfo(); !strings.EqualFold(filepath.Clean(info.CurrentDirectory), filepath.Clean(dir)) {
		t.Fatalf("current directory not preserved: got %q", info.CurrentDirectory)
	}
	if len(session.clients) != 0 {
		t.Fatal("browserless session unexpectedly attached a web client")
	}
}

func TestAppCloseCancelsTerminalSessions(t *testing.T) {
	app, user, target := mcpSessionFixture(t)
	fakeMCPShellAgent(t, app, target.AgentID, "ready")
	if _, err := app.createMCPSession(context.Background(), user, target.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = app.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("App.Close waited on an open terminal session")
	}
}
