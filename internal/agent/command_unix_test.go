//go:build !windows

package agent

import (
	"bufio"
	"bytes"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qinyongliang/linksphere/internal/protocol"
)

func TestAgentBashRCDoesNotExportPromptHook(t *testing.T) {
	if script := agentBashRC(); !strings.Contains(script, "export -n PROMPT_COMMAND") {
		t.Fatalf("agent bashrc should not export the prompt hook to tmux child shells: %q", script)
	}
}

func TestExecDisconnectKillsCommandAndItsChildren(t *testing.T) {
	root := t.TempDir()
	clientConn, agentConn := net.Pipe()
	defer clientConn.Close()
	defer agentConn.Close()
	client := &Client{cfg: Config{Shell: "/bin/sh", Root: root}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		client.handleCommand(agentConn, bufio.NewReader(agentConn), protocol.StreamRequest{
			Type: protocol.StreamExec, Width: 80, Height: 24,
			Command: "trap '' HUP INT TERM; sleep 30 & echo ready; wait; touch completed",
		})
	}()
	_ = clientConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReader(clientConn)
	response, err := protocol.ReadJSONLine[protocol.StreamResponse](reader)
	if err != nil || !response.OK || response.ShellCommandInterrupt {
		t.Fatalf("start command: %+v, %v", response, err)
	}
	for {
		frame, err := protocol.ReadFrame(reader)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(frame.Data), "ready") {
			break
		}
	}
	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("disconnected command or child still holds the PTY open")
	}
	if _, err := os.Stat(filepath.Join(root, "completed")); !os.IsNotExist(err) {
		t.Fatalf("stopped command continued executing: %v", err)
	}
}

func TestAgentBashDirectoryIntegrationReportsInitialHomeAndCD(t *testing.T) {
	shell, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	root := t.TempDir()
	work := filepath.Join(root, "work;中文")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", root)
	t.Setenv("SHELL", shell)
	clientConn, agentConn := net.Pipe()
	defer clientConn.Close()
	defer agentConn.Close()
	client := &Client{cfg: Config{Shell: shell, Root: root}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		client.handleCommand(agentConn, bufio.NewReader(agentConn), protocol.StreamRequest{Type: protocol.StreamShell, Width: 80, Height: 24})
	}()
	_ = clientConn.SetDeadline(time.Now().Add(10 * time.Second))
	reader := bufio.NewReader(clientConn)
	response, err := protocol.ReadJSONLine[protocol.StreamResponse](reader)
	if err != nil || !response.OK || !response.ShellCommandInterrupt {
		t.Fatalf("start shell: %+v, %v", response, err)
	}
	waitDirectory := func(want string) {
		t.Helper()
		var output strings.Builder
		for !strings.Contains(output.String(), "\x1b]633;P;Cwd="+want+"\a") {
			frame, err := protocol.ReadFrame(reader)
			if err != nil {
				t.Fatalf("waiting for cwd %q: %v\n%s", want, err, output.String())
			}
			output.Write(frame.Data)
		}
		capability := strings.LastIndex(output.String(), "\x1b]633;P;GosshdBashSource=1\a")
		directory := strings.LastIndex(output.String(), "\x1b]633;P;Cwd=")
		if capability < 0 || directory < capability {
			t.Fatalf("prompt cwd preceded Bash multiline capability: %q", output.String())
		}
	}
	waitDirectory(root)
	if err := protocol.WriteFrame(clientConn, protocol.Frame{Type: protocol.FrameStdin, Data: []byte("cd 'work;中文'\r")}); err != nil {
		t.Fatal(err)
	}
	waitDirectory(work)
	if err := protocol.WriteFrame(clientConn, protocol.Frame{Type: protocol.FrameStdin, Data: []byte("exit\r")}); err != nil {
		t.Fatal(err)
	}
	for {
		frame, err := protocol.ReadFrame(reader)
		if err != nil {
			t.Fatal(err)
		}
		if frame.Type == protocol.FrameExit {
			break
		}
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shell did not exit")
	}
}

type testShellInterruptProcess struct {
	signal func(os.Signal) error
}

func (p testShellInterruptProcess) Signal(signal os.Signal) error { return p.signal(signal) }

type testShellInterruptWriter struct {
	write func([]byte) (int, error)
}

func (w testShellInterruptWriter) Write(data []byte) (int, error) { return w.write(data) }

func TestShellInterruptPrecedesTerminalAndQueuedInput(t *testing.T) {
	var frames bytes.Buffer
	for _, frame := range []protocol.Frame{{Type: protocol.FrameInterrupt}, {Type: protocol.FrameStdin, Data: []byte("next command\r")}} {
		if err := protocol.WriteFrame(&frames, frame); err != nil {
			t.Fatal(err)
		}
	}
	var events []string
	process := testShellInterruptProcess{signal: func(signal os.Signal) error {
		if signal != os.Interrupt {
			t.Fatalf("unexpected shell signal: %v", signal)
		}
		events = append(events, "shell")
		return nil
	}}
	terminal := testShellInterruptWriter{write: func(data []byte) (int, error) {
		events = append(events, string(data))
		return len(data), nil
	}}
	copyFramesToWriter(terminal, bufio.NewReader(&frames), nil, func() error { return interruptShell(process, terminal) })
	if got := strings.Join(events, "|"); got != "shell|\x03|next command\r" {
		t.Fatalf("interrupt was reordered with queued terminal input: %q", got)
	}
}

func TestShellInterruptRejectsPayloadAndUnnegotiatedControl(t *testing.T) {
	for _, supported := range []bool{false, true} {
		var frames, output bytes.Buffer
		for _, frame := range []protocol.Frame{{Type: protocol.FrameInterrupt, Data: []byte("pid=123;signal=KILL")}, {Type: protocol.FrameStdin, Data: []byte("ordinary\x03")}} {
			if err := protocol.WriteFrame(&frames, frame); err != nil {
				t.Fatal(err)
			}
		}
		calls := 0
		var interrupt func() error
		if supported {
			interrupt = func() error { calls++; return nil }
		}
		copyFramesToWriter(&output, bufio.NewReader(&frames), nil, interrupt)
		if calls != 0 || output.String() != "ordinary\x03" {
			t.Fatalf("payload or ordinary input invoked negotiated control: calls=%d, output=%q", calls, output.String())
		}
	}
	var frames, output bytes.Buffer
	_ = protocol.WriteFrame(&frames, protocol.Frame{Type: protocol.FrameInterrupt})
	copyFramesToWriter(&output, bufio.NewReader(&frames), nil, nil)
	if output.Len() != 0 {
		t.Fatal("unnegotiated control wrote terminal input")
	}
}

func TestShellInterruptDoesNotWriteAfterProcessWasReaped(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	var terminal bytes.Buffer
	if err := interruptShell(cmd.Process, &terminal); !errors.Is(err, os.ErrProcessDone) || terminal.Len() != 0 {
		t.Fatalf("reaped shell received terminal input: err=%v, input=%q", err, terminal.String())
	}
}

func TestShellInterruptFailureStopsQueuedInput(t *testing.T) {
	var frames, terminal bytes.Buffer
	_ = protocol.WriteFrame(&frames, protocol.Frame{Type: protocol.FrameInterrupt})
	_ = protocol.WriteFrame(&frames, protocol.Frame{Type: protocol.FrameStdin, Data: []byte("late command\r")})
	disconnected := false
	copyFramesToWriter(&terminal, bufio.NewReader(&frames), func() { disconnected = true }, func() error { return os.ErrProcessDone })
	if !disconnected || terminal.Len() != 0 {
		t.Fatalf("dead shell accepted queued input: disconnected=%v, terminal=%q", disconnected, terminal.String())
	}
}

func TestBashShellDisconnectSettlesOwnedProcess(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// Bound a failing baseline without sending a signal to an observed PID.
	t.Setenv("TMOUT", "3")
	root := t.TempDir()
	clientConn, agentConn := net.Pipe()
	defer clientConn.Close()
	defer agentConn.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		client := &Client{cfg: Config{Shell: "/bin/bash", Root: root}}
		client.handleCommand(agentConn, bufio.NewReader(agentConn), protocol.StreamRequest{Type: protocol.StreamShell, Width: 80, Height: 24})
	}()
	_ = clientConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReader(clientConn)
	response, err := protocol.ReadJSONLine[protocol.StreamResponse](reader)
	if err != nil || !response.OK {
		t.Fatalf("start Bash: %+v, %v", response, err)
	}
	var output strings.Builder
	for !strings.Contains(output.String(), "\x1b]633;P;Cwd="+root+"\a") {
		frame, err := protocol.ReadFrame(reader)
		if err != nil {
			t.Fatal(err)
		}
		output.Write(frame.Data)
	}
	// Drain the last prompt so a write failure cannot hide an idle-reader leak.
	for {
		_ = clientConn.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
		_, err := protocol.ReadFrame(reader)
		if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	_ = clientConn.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("closed shell stream left its owned Bash process waiting on the PTY")
		select {
		case <-done:
		case <-time.After(4 * time.Second):
			t.Fatal("bounded baseline shell did not exit")
		}
	}
}

func TestNonBashShellDoesNotNegotiateInterrupt(t *testing.T) {
	clientConn, agentConn := net.Pipe()
	defer clientConn.Close()
	defer agentConn.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		client := &Client{cfg: Config{Shell: "/bin/sh", Root: t.TempDir()}}
		client.handleCommand(agentConn, bufio.NewReader(agentConn), protocol.StreamRequest{Type: protocol.StreamShell, Width: 80, Height: 24})
	}()
	_ = clientConn.SetDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReader(clientConn)
	response, err := protocol.ReadJSONLine[protocol.StreamResponse](reader)
	if err != nil || !response.OK || response.ShellCommandInterrupt {
		t.Fatalf("non-Bash shell negotiated interruption: %+v, %v", response, err)
	}
	if err := protocol.WriteFrame(clientConn, protocol.Frame{Type: protocol.FrameInterrupt}); err != nil {
		t.Fatal(err)
	}
	if err := protocol.WriteFrame(clientConn, protocol.Frame{Type: protocol.FrameStdin, Data: []byte("exit 0\r")}); err != nil {
		t.Fatal(err)
	}
	for {
		frame, err := protocol.ReadFrame(reader)
		if err != nil {
			t.Fatal(err)
		}
		if frame.Type == protocol.FrameExit {
			if code := protocol.ExitCode(frame); code != 0 {
				t.Fatalf("unsupported interrupt changed shell exit: %d", code)
			}
			break
		}
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("non-Bash shell did not settle")
	}
}
