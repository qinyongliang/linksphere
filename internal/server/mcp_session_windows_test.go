//go:build windows

package server

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/qinyongliang/linksphere/internal/agent"
)

func TestMCPSessionCmdPreservesContextAndReturnsOutput(t *testing.T) {
	app, user, target := mcpSessionFixture(t)
	a, b := net.Pipe()
	server, err := yamux.Server(a, nil)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := yamux.Client(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	realAgent, err := agent.NewEmbedded(agent.Config{Shell: "cmd.exe", Root: "."}, target.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	app.registry.RegisterWithInfo(target.AgentID, server, AgentRegistryInfo{GOOS: "windows", GOARCH: "amd64"})
	done := make(chan struct{})
	go func() { defer close(done); _ = realAgent.ServeSession(ctx, peer) }()
	t.Cleanup(func() { cancel(); server.Close(); peer.Close(); <-done; _ = realAgent.Close() })
	session, err := app.createMCPSession(ctx, user, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer session.close("")
	for _, command := range []string{"set MCP_CONTEXT_TEST=persistent", "echo %MCP_CONTEXT_TEST%", "ping -n 3 127.0.0.1 >nul & echo command-finished"} {
		run := app.runCommandInTerminalSession(ctx, session, command, terminalSessionCommandOptions{UserID: user.ID})
		if run.Err != nil || !run.Allowed || run.ExitCode != 0 {
			t.Fatalf("command failed: %+v", run)
		}
		if strings.HasPrefix(command, "echo ") && !strings.Contains(run.Output, "persistent") {
			t.Fatalf("command output missing: %q", run.Output)
		}
		if strings.HasPrefix(command, "ping ") && !strings.Contains(run.Output, "command-finished") {
			t.Fatalf("command finished before output: %q", run.Output)
		}
	}
}
