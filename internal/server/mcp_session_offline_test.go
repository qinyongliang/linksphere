package server

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/qinyongliang/linksphere/internal/store"
)

func TestMCPSessionOfflineAgentReportsTransportAndKeepsAudit(t *testing.T) {
	app, user, target := mcpSessionFixture(t)
	session, err := app.createMCPSession(context.Background(), user, target.ID)
	if session != nil || !errors.Is(err, ErrAgentOffline) || strings.Contains(err.Error(), "prompt integration") {
		t.Fatalf("offline Agent misreported as a shell failure: session=%v error=%v", session, err)
	}
	if sessions := app.terminalSessions.listForUser(user.ID); len(sessions) != 0 {
		t.Fatalf("failed startup leaked sessions: %+v", sessions)
	}
	app.backgroundWG.Wait()
	logs, err := app.audit.Repository().ListCommandAuditLogs(context.Background(), store.AuditLogFilter{UserID: user.ID, TargetID: target.ID})
	if err != nil || len(logs.Logs) != 1 {
		t.Fatalf("failed startup lost its audit: %+v %v", logs, err)
	}
	entry := logs.Logs[0]
	if entry.ExitCode == nil || *entry.ExitCode != 255 || entry.EndedAt == nil || entry.Command != "MCP terminal" {
		t.Fatalf("failed startup audit not finalized: %+v", entry)
	}
}

func TestMCPSessionOfflineAgentPreservesCallerCancellation(t *testing.T) {
	app, user, target := mcpSessionFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if session, err := app.createMCPSession(ctx, user, target.ID); session != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("caller cancellation replaced by offline state: session=%v error=%v", session, err)
	}
}

func TestMCPSessionCallerCancellationDuringShellClose(t *testing.T) {
	app, user, target := mcpSessionFixture(t)
	fakeMCPShellAgent(t, app, target.AgentID, "no-prompt")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := app.createMCPSession(ctx, user, target.ID)
		result <- err
	}()
	var session *terminalSession
	deadline := time.After(time.Second)
	for session == nil {
		app.terminalSessions.mu.Lock()
		for _, candidate := range app.terminalSessions.sessions {
			session = candidate
		}
		app.terminalSessions.mu.Unlock()
		if session == nil {
			select {
			case err := <-result:
				t.Fatalf("startup finished before cancellation: %v", err)
			case <-deadline:
				t.Fatal("startup session missing")
			case <-time.After(time.Millisecond):
			}
		}
	}
	// Hold the readiness lock beyond its polling interval, then let closure and
	// caller cancellation become observable together at the startup boundary.
	session.mu.Lock()
	time.Sleep(30 * time.Millisecond)
	cancel()
	session.closeLocked("")
	session.cancel()
	session.mu.Unlock()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("concurrent shell closure masked caller cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled startup did not finish")
	}
}
