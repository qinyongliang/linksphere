package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/qinyongliang/linksphere/internal/store"
)

const mcpSessionStartupTimeout = 15 * time.Second

type mcpSessionSourceIPKey struct{}

type mcpSessionCreateInput struct {
	TargetID string `json:"target_id"`
}

type mcpTerminalAccessDeniedError struct {
	reason string
}

func (e *mcpTerminalAccessDeniedError) Error() string {
	return "interactive terminal denied: " + e.reason
}

func (a *App) addMCPSessionLifecycleTools(s *mcp.Server, actor store.User) {
	mcp.AddTool(s, &mcp.Tool{Name: "session_targets", Description: "List accessible SSH targets for creating an independent terminal session. Opening a session also requires terminal policy access."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ mcpSessionListInput) (*mcp.CallToolResult, mcpTargetListOutput, error) {
			if err := a.ensureServices(ctx); err != nil {
				return nil, mcpTargetListOutput{}, err
			}
			targets, err := a.mcpListTargets(ctx, actor, actor.ID, mcpTargetListInput{})
			if err != nil {
				return nil, mcpTargetListOutput{}, err
			}
			out := mcpTargetListOutput{Targets: []mcpTargetInfo{}}
			for _, target := range targets {
				out.Targets = append(out.Targets, mcpTargetInfo{Target: apiTargetFromStore(target)})
			}
			return nil, out, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "session_create", Description: "Open a new MCP-managed persistent terminal on an accessible target, without a web terminal. Waits for shell integration readiness; working directory and environment persist across calls. Idle sessions expire after ten minutes."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpSessionCreateInput) (*mcp.CallToolResult, mcpSessionInfo, error) {
			session, err := a.createMCPSession(ctx, actor, in.TargetID)
			if err != nil {
				return nil, mcpSessionInfo{}, err
			}
			return nil, session.mcpInfo(), nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "session_close", Description: "Close one of your terminal sessions and release its shell and transport resources."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in mcpSessionInput) (*mcp.CallToolResult, mcpOK, error) {
			if err := a.ensureServices(ctx); err != nil {
				return nil, mcpOK{}, err
			}
			session, err := a.terminalSessions.getForUser(actor.ID, in.SessionID)
			if err != nil {
				return nil, mcpOK{}, err
			}
			session.close("MCP client closed")
			a.terminalSessions.remove(session.id)
			return nil, mcpOK{OK: true}, nil
		})
}

func (a *App) createMCPSession(ctx context.Context, actor store.User, targetID string) (*terminalSession, error) {
	if !a.terminalSessions.startBackground(&a.backgroundWG) {
		return nil, errTerminalSessionClosed
	}
	defer a.backgroundWG.Done()
	target, err := a.targetForUser(ctx, targetID, actor)
	if err != nil {
		return nil, err
	}
	sourceIP, _ := ctx.Value(mcpSessionSourceIPKey{}).(string)
	// MCP uses the same controlled terminal channel as web collaboration. The
	// separate SSH interactive capability permits an unfiltered SSH shell.
	decision, err := a.bastion.EvaluateAccess(ctx, actor.ID, target.ID, store.RequestWebTerminal, sourceIP)
	if err != nil {
		return nil, err
	}
	startedAt := time.Now().UTC()
	sessionID := newAuditSessionID()
	params := store.CreateCommandAuditLogParams{
		UserID: actor.ID, TargetID: target.ID, OrganizationID: organizationIDForTarget(target),
		PublicKeyName: "MCP session", SessionID: sessionID, Command: "MCP terminal",
		RequestType: store.RequestWebTerminal, PolicyDecision: decision.Action,
		PolicyReason: decision.Reason, StartedAt: startedAt, RemoteAddress: sourceIP,
	}
	if decision.Action == store.DecisionDeny {
		code := 126
		params.ExitCode, params.EndedAt = &code, &startedAt
		_, _ = a.createAuditLog(context.WithoutCancel(ctx), params)
		return nil, fmt.Errorf("interactive terminal denied: %s", decision.Reason)
	}
	recorder, err := newTerminalRecorder(a.auditRecordingsPath, sessionID, 120, 32, target)
	if err != nil {
		return nil, fmt.Errorf("terminal recording unavailable: %w", err)
	}
	auditLog, err := a.createAuditLog(ctx, params)
	if err != nil {
		_, _ = recorder.Close()
		return nil, err
	}
	session := a.terminalSessions.createSession(sessionID, actor.ID, target, sourceIP, 120, 32, recorder, true)
	session.auditLogID = auditLog.ID
	if session.ctx.Err() != nil {
		a.completeShellAuditAsync(recorder, auditLog.ID, 255, time.Now().UTC())
		return nil, errTerminalSessionClosed
	}
	a.backgroundWG.Add(1)
	go func() {
		defer a.backgroundWG.Done()
		exitCode := a.webTerminalOnTarget(session)
		session.close("")
		a.terminalSessions.remove(session.id)
		a.completeShellAuditAsync(recorder, auditLog.ID, exitCode, time.Now().UTC())
	}()
	if err := session.waitForMCPReady(ctx); err != nil {
		session.close("shell startup failed")
		a.terminalSessions.remove(session.id)
		// A failed Agent transport closes the shell startup waiter too. Preserve
		// the connection failure instead of suggesting its prompt hook is broken.
		// Caller cancellation still takes precedence over a concurrent disconnect.
		if ctx.Err() == nil && target.TargetType == store.TargetAgent && a.registry != nil {
			if _, connectionErr := a.registry.Get(target.AgentID); errors.Is(connectionErr, ErrAgentOffline) {
				return nil, fmt.Errorf("terminal startup failed: %w", connectionErr)
			}
		}
		return nil, err
	}
	return session, nil
}

func (s *terminalSession) waitForMCPReady(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, mcpSessionStartupTimeout)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("terminal shell integration startup: %w", err)
		}
		s.mu.Lock()
		ready, closed := s.input != nil && s.promptReady && !s.directoryPending && !s.shellBusy, s.closed
		s.mu.Unlock()
		if closed {
			return errors.New("terminal shell exited before prompt integration was ready")
		}
		if ready {
			return nil
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
		case <-s.ctx.Done():
			return errors.New("terminal shell exited before prompt integration was ready")
		}
	}
}

// Callers hold commandMu so a subsequent command cannot change cwd while the
// operation resolves its path. Prompt hooks may emit cwd after command completion.
func (s *terminalSession) waitForMCPDirectory(ctx context.Context) (string, error) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		s.mu.Lock()
		cwd, ready, closed := s.currentDirectory, !s.directoryPending && !s.shellBusy, s.closed
		s.mu.Unlock()
		if closed {
			return "", errTerminalSessionClosed
		}
		if ready {
			return cwd, nil
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
		case <-s.ctx.Done():
			return "", errTerminalSessionClosed
		}
	}
}

// Recheck visibility against current repository state, rather than the snapshot
// saved when the session was created. Individual tools check their capability.
func (a *App) mcpSessionForUser(ctx context.Context, actor store.User, sessionID string) (*terminalSession, error) {
	if err := a.ensureServices(ctx); err != nil {
		return nil, err
	}
	session, err := a.terminalSessions.getForUser(actor.ID, sessionID)
	if err != nil {
		return nil, err
	}
	user, err := a.store.Repository().GetUser(ctx, actor.ID)
	if err != nil || user.DisabledAt != nil {
		return nil, errors.New("session account is unavailable")
	}
	_, err = a.targetForUser(ctx, session.target.ID, user)
	if err != nil {
		return nil, err
	}
	return session, nil
}

func (a *App) authorizeMCPSessionCommand(ctx context.Context, actor store.User, session *terminalSession) error {
	if session.managed {
		sourceIP, ok := ctx.Value(mcpSessionSourceIPKey{}).(string)
		if !ok {
			sourceIP = session.sourceIP
		}
		decision, err := a.bastion.EvaluateAccess(ctx, actor.ID, session.target.ID, store.RequestWebTerminal, sourceIP)
		if err != nil {
			return err
		}
		if decision.Action == store.DecisionDeny {
			return &mcpTerminalAccessDeniedError{reason: decision.Reason}
		}
	}
	return nil
}

func (s *terminalSession) mcpInfo() mcpSessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return mcpSessionInfoFromTerminal(terminalSessionInfo{
		ID: s.id, TargetID: s.target.ID, TargetName: s.target.Name, TargetAlias: s.target.Alias,
		Endpoint: targetEndpointForStore(s.target), StartedAt: s.startedAt, LastHeartbeat: s.lastHeartbeat,
		ShellBusy: s.shellBusy, Managed: s.managed, CurrentDirectory: s.currentDirectory, LastActivity: s.lastActivity,
	})
}

func mcpSessionInfoFromTerminal(item terminalSessionInfo) mcpSessionInfo {
	lastHeartbeat, timeout := item.LastHeartbeat, terminalSessionHeartbeatTimeout
	if item.Managed {
		lastHeartbeat, timeout = item.LastActivity, terminalSessionDetachedTimeout
	}
	return mcpSessionInfo{
		ID: item.ID, TargetID: item.TargetID, TargetName: item.TargetName, TargetAlias: item.TargetAlias,
		Endpoint: item.Endpoint, StartedAt: item.StartedAt.Format(time.RFC3339), LastHeartbeatAt: lastHeartbeat.Format(time.RFC3339),
		HeartbeatTimeoutS: int(timeout.Seconds()), ShellBusy: item.ShellBusy, Managed: item.Managed, CurrentDirectory: item.CurrentDirectory,
	}
}
