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
)

func TestMCPSessionCloseStopsOwnedForegroundChild(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	app, user, target := mcpSessionFixture(t)
	attachTunnelAgent(t, app, target.AgentID, agent.Config{Shell: "/bin/bash"})
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	session, err := app.createMCPSession(ctx, user, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer session.close("")
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	shellPIDPath, childPIDPath, tailPath := filepath.Join(home, "shell.pid"), filepath.Join(home, "child.pid"), filepath.Join(home, "closed-tail")
	if result := session.trySendCommand(ctx, "printf '%s' \"$$\" > "+quote(shellPIDPath)); result.Err != nil || result.Result.ExitCode != 0 {
		t.Fatalf("record owned shell: %+v", result)
	}
	session.resizeTo(111, 37)
	if result := session.trySendCommand(ctx, "stty size"); result.Err != nil || result.Result.ExitCode != 0 || !strings.Contains(result.Result.Output, "37 111") {
		t.Fatalf("owned PTY resize before close: %+v", result)
	}
	childPath := filepath.Join(home, "foreground.sh")
	// The bound makes the failing baseline settle without signalling a PID
	// obtained by a process search. A terminal close must also stop a child
	// that inherited an ignored hangup signal.
	child := "trap '' HUP\nprintf '%s' \"$$\" > " + quote(childPIDPath) + "\nexec sleep 4\n"
	if err := os.WriteFile(childPath, []byte(child), 0600); err != nil {
		t.Fatal(err)
	}
	client := mcpSessionTestClient(t, app, user)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = client.CallTool(ctx, &mcp.CallToolParams{Name: "session_send_command", Arguments: map[string]any{
			"session_id": session.id, "command": ":\n/bin/sh " + quote(childPath) + "\nprintf stopped > " + quote(tailPath),
		}})
	}()
	readPID := func(path string) int {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		pid, err := strconv.Atoi(string(data))
		if err != nil || pid <= 1 {
			t.Fatalf("invalid owned process PID %q: %v", data, err)
		}
		return pid
	}
	var childPID int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(childPIDPath); err == nil && len(data) > 0 {
			childPID = readPID(childPIDPath)
			command, err := exec.Command("ps", "-p", strconv.Itoa(childPID), "-o", "comm=").Output()
			if err == nil && filepath.Base(strings.TrimSpace(string(command))) == "sleep" {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID == 0 || time.Now().After(deadline) {
		t.Fatal("owned foreground sleep did not start")
	}
	shellPID := readPID(shellPIDPath)
	started := time.Now()
	result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "session_close", Arguments: map[string]any{"session_id": session.id}})
	if err != nil || result.IsError {
		t.Fatalf("close active session: %+v, %v", result, err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("closed session command request did not settle")
	}
	gone := func(pid int) bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) }
	deadline = started.Add(time.Second)
	for time.Now().Before(deadline) && (!gone(shellPID) || !gone(childPID)) {
		time.Sleep(10 * time.Millisecond)
	}
	if time.Since(started) > time.Second {
		t.Errorf("active session close exceeded its one-second lifecycle bound: %v", time.Since(started))
	}
	if !gone(shellPID) || !gone(childPID) {
		t.Errorf("API close left owned processes after %v: shell_gone=%v child_gone=%v", time.Since(started), gone(shellPID), gone(childPID))
		// Allow the four-second fixture to end and release its PTY on RED.
		deadline = time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) && (!gone(shellPID) || !gone(childPID)) {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if _, err := os.Stat(tailPath); !os.IsNotExist(err) {
		t.Error("closed session executed the remaining command")
	}
	// Closing one shell must leave its Agent available to another session.
	next, err := app.createMCPSession(ctx, user, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer next.close("")
	if result := next.trySendCommand(ctx, "printf '%s%s\\n' agent _alive"); result.Err != nil || result.Result.ExitCode != 0 || !strings.Contains(result.Result.Output, "agent_alive") {
		t.Fatalf("closing the owned session stopped its Agent: %+v", result)
	}
}
