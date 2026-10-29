//go:build windows

package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
	"github.com/qinyongliang/linksphere/internal/agent"
	"github.com/qinyongliang/linksphere/internal/store"
	"golang.org/x/sys/windows"
)

func windowsMCPSession(t *testing.T) (*App, store.User, *terminalSession, context.Context) {
	t.Helper()
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
	realAgent, err := agent.NewEmbedded(agent.Config{Shell: "cmd.exe", Root: t.TempDir(), IDFile: filepath.Join(t.TempDir(), "agent.json")}, target.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	app.registry.RegisterWithInfo(target.AgentID, server, AgentRegistryInfo{GOOS: "windows", GOARCH: "amd64"})
	done := make(chan struct{})
	go func() { defer close(done); _ = realAgent.ServeSession(ctx, peer) }()
	t.Cleanup(func() { cancel(); server.Close(); peer.Close(); <-done; _ = realAgent.Close() })
	session, err := app.createMCPSession(ctx, user, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.close("") })
	return app, user, session, ctx
}

func TestMCPSessionCmdPreservesContextAndReturnsOutput(t *testing.T) {
	app, user, session, ctx := windowsMCPSession(t)
	directory := filepath.Join(t.TempDir(), "项目 with spaces")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		command string
		code    int
		output  string
	}{
		{"cmd.exe /d /c exit 7", 7, ""},
		{"echo %ERRORLEVEL%", 7, "7"},
		// Successful CMD builtins preserve the previous native status. The
		// Agent must not reset it to make those commands appear successful.
		{"set MCP_CONTEXT_TEST=persistent", 7, ""},
		{"echo %MCP_CONTEXT_TEST%", 7, "persistent"},
		{"cmd.exe /d /c exit 0", 0, ""},
		{"cmd.exe /d /c exit -7", -7, ""},
		{"cmd.exe /d /c exit -2147483648", -2147483648, ""},
		{"cmd.exe /d /c exit 2147483647", 2147483647, ""},
		{"cmd.exe /d /c exit 0", 0, ""},
		{"__linksphere_nonexistent_command__", 9009, ""},
		{"cmd.exe /d /c exit 0", 0, ""},
		{`cd /d "` + directory + `"`, 0, ""},
		{"cd", 0, directory},
		{"echo 中文输出", 0, "中文输出"},
		{"ping -n 3 127.0.0.1 >nul & echo command-finished", 0, "command-finished"},
	}
	for _, step := range steps {
		run := app.runCommandInTerminalSession(ctx, session, step.command, terminalSessionCommandOptions{UserID: user.ID})
		if run.Err != nil || !run.Allowed || run.ExitCode != step.code {
			t.Fatalf("%s: %+v", step.command, run)
		}
		if step.output != "" && !strings.Contains(run.Output, step.output) {
			t.Fatalf("%s output missing %q: %q", step.command, step.output, run.Output)
		}
		if strings.Contains(run.Output, "__GOSSHD_") || strings.Contains(run.Output, "@if errorlevel") {
			t.Fatalf("private query leaked: %q", run.Output)
		}
	}
	for i := 0; i < 20; i++ {
		code := i % 8
		run := app.runCommandInTerminalSession(ctx, session, fmt.Sprintf("cmd.exe /d /c exit %d", code), terminalSessionCommandOptions{UserID: user.ID})
		if run.Err != nil || run.ExitCode != code || strings.Contains(run.Output, "__GOSSHD_") {
			t.Fatalf("rapid command %d: %+v", i, run)
		}
	}
}

func TestMCPSessionCmdWaitsForInteractiveInput(t *testing.T) {
	app, user, session, ctx := windowsMCPSession(t)
	done := make(chan terminalSessionCommandRun, 1)
	go func() {
		done <- app.runCommandInTerminalSession(ctx, session, "set /p MCP_REPLY=reply:", terminalSessionCommandOptions{UserID: user.ID})
	}()
	select {
	case result := <-done:
		t.Fatalf("interactive command consumed an unsolicited query: %+v", result)
	case <-time.After(150 * time.Millisecond):
	}
	if err := session.writeInput("answer\r"); err != nil {
		t.Fatal(err)
	}
	if run := <-done; run.Err == nil || run.ExitCode != 255 || !strings.Contains(run.Err.Error(), "open a new session") {
		t.Fatalf("interactive input must make command status explicitly unknown: %+v", run)
	}
	run := app.runCommandInTerminalSession(ctx, session, "echo %MCP_REPLY%", terminalSessionCommandOptions{UserID: user.ID})
	if run.Err == nil || !strings.Contains(run.Err.Error(), "open a new session") {
		t.Fatalf("ambiguous shell accepted another managed command: %+v", run)
	}
	if err := session.writeInput("echo %MCP_REPLY%\r"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for strings.Count(session.currentScreen(), "answer") < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if strings.Count(session.currentScreen(), "answer") < 2 {
		t.Fatalf("manual interactive input/output stopped working: %q", session.currentScreen())
	}
	freshApp, freshUser, freshSession, freshCtx := windowsMCPSession(t)
	fresh := freshApp.runCommandInTerminalSession(freshCtx, freshSession, "cmd.exe /d /c exit 7", terminalSessionCommandOptions{UserID: freshUser.ID})
	if fresh.Err != nil || fresh.ExitCode != 7 {
		t.Fatalf("new session did not restore reliable commands: %+v", fresh)
	}
}

func TestMCPSessionCmdResizeDoesNotCompleteRunningCommand(t *testing.T) {
	app, user, session, ctx := windowsMCPSession(t)
	done := make(chan terminalSessionCommandRun, 1)
	go func() {
		done <- app.runCommandInTerminalSession(ctx, session, "ping -n 3 127.0.0.1 >nul & echo delayed-finished", terminalSessionCommandOptions{UserID: user.ID})
	}()
	time.Sleep(150 * time.Millisecond)
	session.resizeTo(40, 24)
	select {
	case run := <-done:
		t.Fatalf("screen redraw completed a running command: %+v", run)
	case <-time.After(300 * time.Millisecond):
	}
	run := <-done
	if run.Err != nil || run.ExitCode != 0 || !strings.Contains(run.Output, "delayed-finished") || strings.Contains(run.Output, "__GOSSHD_") {
		t.Fatalf("resized command completion: %+v", run)
	}
}

func TestMCPSessionCmdReadsNativeStatusDespiteShadowedVariable(t *testing.T) {
	app, user, session, ctx := windowsMCPSession(t)
	if run := app.runCommandInTerminalSession(ctx, session, "set ERRORLEVEL=0", terminalSessionCommandOptions{UserID: user.ID}); run.Err != nil {
		t.Fatal(run.Err)
	}
	run := app.runCommandInTerminalSession(ctx, session, "cmd.exe /d /c exit 7", terminalSessionCommandOptions{UserID: user.ID})
	if run.Err != nil || run.ExitCode != 7 {
		t.Fatalf("shadow variable replaced native status: %+v", run)
	}
}

func TestMCPSessionCmdStatusQueryDoesNotExecuteEnvironmentValue(t *testing.T) {
	app, user, session, ctx := windowsMCPSession(t)
	run := func(command string) terminalSessionCommandRun {
		result := app.runCommandInTerminalSession(ctx, session, command, terminalSessionCommandOptions{UserID: user.ID})
		if result.Err != nil {
			t.Fatalf("%s: %+v", command, result)
		}
		return result
	}
	escape := strings.NewReplacer("^", "^^", "&", "^&", "|", "^|", "<", "^<", ">", "^>", `"`, `^"`, "(", "^(", ")", "^)")
	for _, value := range []string{`0&set MCP_UNEXPECTED=changed&rem ignored`, `0"|set MCP_UNEXPECTED=changed|rem "`, `0">nul&set MCP_UNEXPECTED=changed&rem "`} {
		run("set MCP_UNEXPECTED=")
		// Escape the assignment so the complete hostile value is data in the
		// persistent CMD environment, including quotes and redirection syntax.
		run("set ERRORLEVEL=" + escape.Replace(value))
		if result := run("cmd.exe /d /c exit 7"); result.ExitCode != 7 {
			t.Fatalf("environment data replaced native exit status: %+v", result)
		}
		if valueOutput := run("set ERRORLEVEL"); !strings.Contains(valueOutput.Output, value) {
			t.Fatalf("status query changed the environment value: %q", valueOutput.Output)
		}
		run("set ERRORLEVEL=")
		observed := run("echo %MCP_UNEXPECTED%")
		if strings.Contains(observed.Output, "changed") {
			t.Fatalf("status query executed environment data: %+v", observed)
		}
	}
}

func TestMCPSessionCmdManualInputCannotReportAnotherCommandsStatus(t *testing.T) {
	app, user, session, ctx := windowsMCPSession(t)
	done := make(chan terminalSessionCommandRun, 1)
	go func() {
		done <- app.runCommandInTerminalSession(ctx, session, "ping -n 2 127.0.0.1 >nul & cmd.exe /d /c exit 7", terminalSessionCommandOptions{UserID: user.ID})
	}()
	time.Sleep(150 * time.Millisecond)
	if err := session.writeInput("cmd.exe /d /c exit 0\r"); err != nil {
		t.Fatal(err)
	}
	result := <-done
	if result.Err == nil || result.ExitCode != 255 {
		t.Fatalf("manual command contaminated managed status: %+v", result)
	}
}

// The same test executable is a real native child of the persistent CMD. Its
// PID lets cancellation/disconnect checks verify that no command survives.
func TestMCPSessionWindowsCommandChild(t *testing.T) {
	if os.Getenv("LINKSPHERE_MCP_CHILD_TEST") != "1" {
		t.Skip("subprocess helper")
	}
	fmt.Printf("mcp-child-pid:%d\n", os.Getpid())
	for {
		time.Sleep(time.Second)
	}
}

func TestMCPSessionCmdCancellationAndDisconnectStopChild(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		t.Run(fmt.Sprintf("disconnect-%v", disconnect), func(t *testing.T) {
			app, user, session, ctx := windowsMCPSession(t)
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			commandCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			done := make(chan terminalSessionCommandRun, 1)
			go func() {
				command := `set LINKSPHERE_MCP_CHILD_TEST=1& "` + exe + `" -test.run=^TestMCPSessionWindowsCommandChild$`
				done <- app.runCommandInTerminalSession(commandCtx, session, command, terminalSessionCommandOptions{UserID: user.ID})
			}()
			pattern := regexp.MustCompile(`mcp-child-pid:(\d+)`)
			var pid uint32
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				if match := pattern.FindStringSubmatch(session.currentScreen()); len(match) == 2 {
					value, _ := strconv.ParseUint(match[1], 10, 32)
					pid = uint32(value)
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if pid == 0 {
				t.Fatal("native child did not start")
			}
			process, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
			if err != nil {
				t.Fatal(err)
			}
			defer windows.CloseHandle(process)
			if disconnect {
				session.close("")
			} else {
				cancel()
			}
			select {
			case run := <-done:
				if run.Err == nil || (!disconnect && !errors.Is(run.Err, context.Canceled)) {
					t.Fatalf("canceled call: %+v", run)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("canceled call did not return")
			}
			if state, err := windows.WaitForSingleObject(process, 3000); err != nil || state != windows.WAIT_OBJECT_0 {
				t.Fatalf("native child survived: state=%d err=%v", state, err)
			}
			if !disconnect {
				run := app.runCommandInTerminalSession(ctx, session, "cmd.exe /d /c exit 0", terminalSessionCommandOptions{UserID: user.ID})
				if run.Err != nil || run.ExitCode != 0 {
					t.Fatalf("shell did not recover after cancellation: %+v", run)
				}
			}
		})
	}
}
