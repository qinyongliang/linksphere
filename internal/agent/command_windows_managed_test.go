//go:build windows

package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/qinyongliang/linksphere/internal/protocol"
	"golang.org/x/sys/windows"
)

func TestWindowsManagedCommandWaitsForPromptAndQueuesManualInput(t *testing.T) {
	agentConn, peer := net.Pipe()
	defer agentConn.Close()
	defer peer.Close()
	_ = peer.SetDeadline(time.Now().Add(5 * time.Second))
	outputRead, outputWrite := io.Pipe()
	defer outputRead.Close()
	defer outputWrite.Close()
	input := make(chan string, 8)
	exited := make(chan int, 1)
	done := make(chan int, 1)
	go func() {
		done <- serveWindowsPTYShell(agentConn, bufio.NewReader(agentConn), outputRead, "cmd.exe", testLegacyMarker,
			func(data []byte) { input <- string(data) }, func() {}, func([]byte) {}, func() {}, exited, nil)
	}()
	frames := make(chan protocol.Frame, 128)
	go func() {
		defer close(frames)
		for {
			frame, err := protocol.ReadFrame(peer)
			if err != nil {
				return
			}
			frames <- frame
		}
	}()
	prompt := func(stamp string) string { return testLegacyMarker + "0;" + stamp + ";C:\\work" + testLegacyEnd }
	if _, err := io.WriteString(outputWrite, prompt("initial")); err != nil {
		t.Fatal(err)
	}
	<-frames
	command := protocol.ShellCommand{ID: uuid.NewString(), Command: "native-command"}
	data, _ := json.Marshal(command)
	if err := protocol.WriteFrame(peer, protocol.Frame{Type: protocol.FrameCommand, Data: data}); err != nil {
		t.Fatal(err)
	}
	if got := <-input; got != "native-command\r" {
		t.Fatalf("user command input: %q", got)
	}
	io.WriteString(outputWrite, "foreground output\r\n")
	select {
	case query := <-input:
		t.Fatalf("query could be consumed by interactive program: %q", query)
	case <-time.After(20 * time.Millisecond):
	}
	io.WriteString(outputWrite, prompt("user-finished"))
	query := <-input
	var output strings.Builder
	// A query probe must retain the initial/finished prompt history. A resize
	// replay is neither a new shell prompt nor a missing-result completion.
	io.WriteString(outputWrite, "\x1b[H"+prompt("initial")+"\x1b[?25h")
	redrawWait := time.NewTimer(20 * time.Millisecond)
redraw:
	for {
		select {
		case frame := <-frames:
			if frame.Type == protocol.FrameCommandDone {
				t.Fatalf("historical redraw completed a query before its result: %s", frame.Data)
			}
			output.Write(frame.Data)
		case <-redrawWait.C:
			break redraw
		}
	}
	if err := protocol.WriteFrame(peer, protocol.Frame{Type: protocol.FrameStdin, Data: []byte("echo manual\r")}); err != nil {
		t.Fatal(err)
	}
	thresholdPattern := regexp.MustCompile(`^@if errorlevel (-?\d+) `)
	for i := 0; i < 32; i++ {
		match := thresholdPattern.FindStringSubmatch(query)
		if len(match) != 2 || strings.Contains(query, "%") {
			t.Fatalf("status query contains nonconstant CMD syntax: %q", query)
		}
		threshold, _ := strconv.ParseInt(match[1], 10, 64)
		bit := "0"
		if 7 >= threshold {
			bit = "1"
		}
		result := testLegacyMarker + "R;" + bit + ";" + command.ID + ":" + match[1] + testLegacyEnd
		// Result records from a previous threshold must not replace this bit,
		// even if they arrive after its genuine result during a redraw.
		stale := testLegacyMarker + "R;" + map[string]string{"0": "1", "1": "0"}[bit] + ";" + command.ID + ":stale" + testLegacyEnd
		io.WriteString(outputWrite, query+"\nbackground output\r\n"+result+stale+prompt(fmt.Sprintf("query-%d", i)))
		if i < 31 {
			query = <-input
		}
	}
	for {
		frame, ok := <-frames
		if !ok {
			t.Fatal("shell stream closed before command completion")
		}
		if frame.Type == protocol.FrameStdout {
			output.Write(frame.Data)
		}
		if frame.Type == protocol.FrameCommandDone {
			var completion protocol.ShellCommandResult
			if json.Unmarshal(frame.Data, &completion) != nil || completion.ID != command.ID || completion.ExitCode != 7 || completion.Error != "" {
				t.Fatalf("completion: %s", frame.Data)
			}
			break
		}
	}
	if got := <-input; got != "echo manual\r" {
		t.Fatalf("queued browser input changed: %q", got)
	}
	if !strings.Contains(output.String(), "foreground output") || !strings.Contains(output.String(), "background output") || strings.Contains(output.String(), testLegacyMarker) || strings.Contains(output.String(), "@if errorlevel") {
		t.Fatalf("filtered output: %q", output.String())
	}
	exited <- 0
	outputWrite.Close()
	if code := <-done; code != 0 {
		t.Fatalf("shell exit: %d", code)
	}
}

func TestWindowsManagedShellDrainsOutputAfterProcessExit(t *testing.T) {
	agentConn, peer := net.Pipe()
	defer agentConn.Close()
	defer peer.Close()
	_ = peer.SetDeadline(time.Now().Add(5 * time.Second))
	outputRead, outputWrite := io.Pipe()
	defer outputRead.Close()
	defer outputWrite.Close()
	exited := make(chan int, 1)
	exited <- 0
	done := make(chan int, 1)
	go func() {
		done <- serveWindowsPTYShell(agentConn, bufio.NewReader(agentConn), outputRead, "cmd.exe", testLegacyMarker,
			func([]byte) {}, func() {}, func([]byte) {}, func() {}, exited, func() {
				io.WriteString(outputWrite, "final rendered output")
				outputWrite.Close()
			})
	}()
	frame, err := protocol.ReadFrame(peer)
	if err != nil || string(frame.Data) != "final rendered output" {
		t.Fatalf("final output lost: %v %v", frame, err)
	}
	if code := <-done; code != 0 {
		t.Fatalf("shell exit: %d", code)
	}
}

func TestWindowsManagedCommandSurvivesQueryWindowResize(t *testing.T) {
	if err := windows.NewLazySystemDLL("kernel32.dll").NewProc("CreatePseudoConsole").Find(); err != nil {
		t.Skip("ConPTY is not available")
	}
	client, err := New(Config{Server: "http://agent.example.test:8880", Shell: "cmd.exe", Root: t.TempDir(), IDFile: filepath.Join(t.TempDir(), "agent.json")})
	if err != nil {
		t.Fatal(err)
	}
	agentConn, peer := net.Pipe()
	defer agentConn.Close()
	defer peer.Close()
	_ = peer.SetDeadline(time.Now().Add(15 * time.Second))
	done := make(chan error, 1)
	go func() {
		done <- client.handleConPTYShell(agentConn, bufio.NewReader(agentConn), protocol.StreamRequest{Type: protocol.StreamShell, Width: 120, Height: 24})
	}()
	t.Cleanup(func() {
		_ = peer.SetDeadline(time.Now().Add(3 * time.Second))
		go io.Copy(io.Discard, peer)
		_ = protocol.WriteFrame(peer, protocol.Frame{Type: protocol.FrameStdin, Data: []byte("exit\r")})
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("ConPTY shell did not stop")
		}
	})
	reader := bufio.NewReader(peer)
	response, err := protocol.ReadJSONLine[protocol.StreamResponse](reader)
	if err != nil || !response.ShellCommandStatus {
		t.Fatalf("ConPTY capability: %+v %v", response, err)
	}
	readFramesUntil(t, reader, "\x1b]633;A\a")
	command := protocol.ShellCommand{ID: uuid.NewString(), Command: "cmd.exe /d /c exit 7"}
	data, _ := json.Marshal(command)
	if err := protocol.WriteFrame(peer, protocol.Frame{Type: protocol.FrameCommand, Data: data}); err != nil {
		t.Fatal(err)
	}
	// The first post-command prompt starts the private status-query window.
	readFramesUntil(t, reader, "\x1b]633;A\a")
	if err := protocol.WriteFrame(peer, protocol.Frame{Type: protocol.FrameResize, Data: []byte{0, 0, 0, 40, 0, 0, 0, 24}}); err != nil {
		t.Fatal(err)
	}
	for {
		frame, err := protocol.ReadFrame(reader)
		if err != nil {
			t.Fatal(err)
		}
		if frame.Type == protocol.FrameCommandDone {
			var result protocol.ShellCommandResult
			if json.Unmarshal(frame.Data, &result) != nil || result.ID != command.ID || result.ExitCode != 7 || result.Error != "" {
				t.Fatalf("resize corrupted status query: %s", frame.Data)
			}
			break
		}
		if frame.Type == protocol.FrameExit {
			t.Fatalf("shell exited during status query: %d", protocol.ExitCode(frame))
		}
	}
}

func TestWindowsManagedShellBlockedInputDisconnectStopsAndJoinsWriter(t *testing.T) {
	for _, trigger := range []string{"disconnect", "interrupt", "overflow"} {
		t.Run(trigger, func(t *testing.T) {
			agentConn, peer := net.Pipe()
			defer agentConn.Close()
			defer peer.Close()
			outputRead, outputWrite := io.Pipe()
			defer outputRead.Close()
			defer outputWrite.Close()
			inputRead, inputWrite, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			entered, writeReturned, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{})
			interrupted := make(chan struct{})
			interrupt := sync.OnceFunc(func() { close(interrupted) })
			stop := sync.OnceFunc(func() {
				inputRead.Close()
				inputWrite.Close()
				close(stopped)
			})
			defer stop()
			done := make(chan int, 1)
			go func() {
				done <- serveWindowsPTYShell(agentConn, bufio.NewReader(agentConn), outputRead, "cmd.exe", testLegacyMarker,
					func(data []byte) { close(entered); inputWrite.Write(data); close(writeReturned) }, interrupt, func([]byte) {}, stop, make(chan int), nil)
			}()
			errors := make(chan string, 8)
			go func() {
				for {
					frame, err := protocol.ReadFrame(peer)
					if err != nil {
						return
					}
					if frame.Type == protocol.FrameStderr {
						errors <- string(frame.Data)
					}
				}
			}()
			if err := protocol.WriteFrame(peer, protocol.Frame{Type: protocol.FrameStdin, Data: make([]byte, 64*1024)}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("input write was not attempted")
			}
			select {
			case <-writeReturned:
				t.Fatal("Windows pipe did not block")
			case <-time.After(30 * time.Millisecond):
			}
			switch trigger {
			case "disconnect":
				peer.Close()
			case "interrupt":
				if err := protocol.WriteFrame(peer, protocol.Frame{Type: protocol.FrameStdin, Data: []byte{0x03}}); err != nil {
					t.Fatal(err)
				}
			case "overflow":
				for _, size := range []int{64 * 1024, 1} {
					if err := protocol.WriteFrame(peer, protocol.Frame{Type: protocol.FrameStdin, Data: make([]byte, size)}); err != nil {
						t.Fatal(err)
					}
				}
			}
			if trigger != "disconnect" {
				select {
				case message := <-errors:
					if !strings.Contains(message, "session closed") {
						t.Errorf("input failure was silent: %q", message)
					}
				case <-time.After(300 * time.Millisecond):
					t.Error("input cancellation/overflow did not report its failure")
				}
			}
			select {
			case <-stopped:
			case <-time.After(300 * time.Millisecond):
				t.Errorf("%s did not stop a blocked Windows input write", trigger)
			}
			stop() // Also release the pipe if the regression fails.
			select {
			case <-done:
				if trigger == "interrupt" {
					select {
					case <-interrupted:
						t.Error("blocked paste must close without a native interrupt")
					default:
					}
				}
				select {
				case <-writeReturned:
				default:
					t.Error("controller returned without joining its input writer")
				}
			case <-time.After(time.Second):
				t.Error("controller did not finish after pipe release")
			}
		})
	}
}

func TestWindowsSynchronousPipeCloseUnblocksInputWrite(t *testing.T) {
	var readHandle, writeHandle windows.Handle
	if err := windows.CreatePipe(&readHandle, &writeHandle, nil, 0); err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(readHandle)
	input := os.NewFile(uintptr(writeHandle), "synchronous-input")
	defer input.Close()
	writeReturned := make(chan struct{})
	go func() {
		input.Write(make([]byte, 64*1024))
		close(writeReturned)
	}()
	select {
	case <-writeReturned:
		t.Fatal("synchronous Windows pipe did not block")
	case <-time.After(30 * time.Millisecond):
	}
	closeReturned := make(chan struct{})
	go func() { input.Close(); close(closeReturned) }()
	select {
	case <-writeReturned:
	case <-time.After(300 * time.Millisecond):
		t.Error("closing synchronous PTY input did not cancel its write")
		windows.CloseHandle(readHandle)
	}
	select {
	case <-closeReturned:
	case <-time.After(time.Second):
		t.Error("input close did not finish")
	}
}

func TestWindowsManagedShellStopsPendingPasteBeforeNativeInterrupt(t *testing.T) {
	agentConn, peer := net.Pipe()
	defer agentConn.Close()
	defer peer.Close()
	_ = peer.SetDeadline(time.Now().Add(3 * time.Second))
	outputRead, outputWrite := io.Pipe()
	defer outputRead.Close()
	defer outputWrite.Close()
	firstEntered, firstReturned := make(chan struct{}), make(chan struct{})
	releaseFirst := make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseFirst) })
	defer release()
	secondAttempted, interrupted := make(chan struct{}), make(chan struct{})
	interrupt := func() {
		close(interrupted)
		// A real console interrupt can release an application's backpressure.
		// Keep this callback open long enough for an unsafe writer to resume.
		release()
		select {
		case <-secondAttempted:
		case <-time.After(300 * time.Millisecond):
		}
	}
	done := make(chan int, 1)
	go func() {
		done <- serveWindowsPTYShell(agentConn, bufio.NewReader(agentConn), outputRead, "cmd.exe", testLegacyMarker,
			func(data []byte) {
				if string(data) == "first" {
					close(firstEntered)
					<-releaseFirst
					close(firstReturned)
				} else if string(data) == "second" {
					close(secondAttempted)
				}
			}, interrupt, func([]byte) {}, release, make(chan int), nil)
	}()
	go func() {
		for {
			if _, err := protocol.ReadFrame(peer); err != nil {
				return
			}
		}
	}()
	if err := protocol.WriteFrame(peer, protocol.Frame{Type: protocol.FrameStdin, Data: []byte("first")}); err != nil {
		t.Fatal(err)
	}
	<-firstEntered
	for _, data := range [][]byte{[]byte("second"), {0x03}} {
		if err := protocol.WriteFrame(peer, protocol.Frame{Type: protocol.FrameStdin, Data: data}); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("pending paste cancellation did not stop the session")
	}
	select {
	case <-secondAttempted:
		t.Error("queued paste resumed after cancellation")
	default:
	}
	select {
	case <-interrupted:
		t.Error("forced-close cancellation invoked native interrupt before teardown")
	default:
	}
	select {
	case <-firstReturned:
	default:
		t.Error("pending input writer was not joined")
	}
}
