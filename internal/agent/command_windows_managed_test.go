//go:build windows

package agent

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/qinyongliang/linksphere/internal/protocol"
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
			func(data []byte) { input <- string(data) }, func([]byte) {}, func() {}, exited, nil)
	}()
	frames := make(chan protocol.Frame, 16)
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
	if !strings.Contains(query, "%errorlevel%") {
		t.Fatalf("not a CMD query: %q", query)
	}
	if err := protocol.WriteFrame(peer, protocol.Frame{Type: protocol.FrameStdin, Data: []byte("echo manual\r")}); err != nil {
		t.Fatal(err)
	}
	result := testLegacyMarker + "R;7;" + command.ID + testLegacyEnd
	io.WriteString(outputWrite, query+"\nbackground output\r\n"+result+prompt("queried"))
	validation := <-input
	if !strings.Contains(validation, "@if errorlevel 7 if not errorlevel 8") {
		t.Fatalf("browser input raced status query: %q", validation)
	}
	io.WriteString(outputWrite, validation+"\n"+result+prompt("validated"))
	var output strings.Builder
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
			func([]byte) {}, func([]byte) {}, func() {}, exited, func() {
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
