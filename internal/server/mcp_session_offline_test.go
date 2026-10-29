package server

import (
	"context"
	"errors"
	"strings"
	"testing"

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
