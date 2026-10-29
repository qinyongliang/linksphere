//go:build windows

package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/qinyongliang/linksphere/internal/protocol"
	"golang.org/x/sys/windows"
)

const windowsCommandQueryTimeout = 5 * time.Second

// A single owner serializes PTY input, output and managed completions. In
// particular, a status query must never be queued behind a running application:
// that application could read it as its own stdin. Only the shell's next
// printable prompt authorizes sending the query to the same persistent CMD.
func serveWindowsPTYShell(stream io.ReadWriteCloser, reader *bufio.Reader, output io.Reader, shell, marker string, writeInput func([]byte), resize func([]byte), stop func(), exited <-chan int, drainOutput func()) int {
	type frameResult struct {
		frame protocol.Frame
		err   error
	}
	type outputResult struct {
		data []byte
		err  error
	}
	done := make(chan struct{})
	defer close(done)
	defer stop()
	frames := make(chan frameResult)
	chunks := make(chan outputResult)
	go func() {
		for {
			frame, err := protocol.ReadFrame(reader)
			select {
			case frames <- frameResult{frame, err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := output.Read(buf)
			select {
			case chunks <- outputResult{append([]byte(nil), buf[:n]...), err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()

	var active *protocol.ShellCommand
	var queuedInput []byte
	var query, queryOutput string
	var queryTimer *time.Timer
	var queryDeadline <-chan time.Time
	var exitTimer *time.Timer
	var exitDeadline <-chan time.Time
	var shellExitCode int
	shellExited := false
	defer func() {
		if queryTimer != nil {
			queryTimer.Stop()
		}
		if exitTimer != nil {
			exitTimer.Stop()
		}
	}()
	ready, prompts := false, 0
	decoder := legacyWindowsPromptDecoder{marker: marker, onPrompt: func() { prompts++ }, onResult: func(string, string) {}}
	var probe *legacyWindowsPromptDecoder
	var result protocol.ShellCommandResult
	queryReady, resultSeen := false, false
	validating, candidate := false, 0
	writeOutput := func(text string) error {
		if text == "" {
			return nil
		}
		return protocol.WriteFrame(stream, protocol.Frame{Type: protocol.FrameStdout, Data: []byte(text)})
	}
	complete := func(result protocol.ShellCommandResult) error {
		data, err := json.Marshal(result)
		if err != nil {
			return err
		}
		return protocol.WriteFrame(stream, protocol.Frame{Type: protocol.FrameCommandDone, Data: data})
	}
	markBusy := func() error { return writeOutput("\x1b]633;C\a") }
	for {
		select {
		case incoming := <-frames:
			if incoming.err != nil {
				return 255
			}
			frame := incoming.frame
			switch frame.Type {
			case protocol.FrameCommand:
				var command protocol.ShellCommand
				if json.Unmarshal(frame.Data, &command) != nil || command.ID == "" {
					return 255
				}
				failure := ""
				if _, err := uuid.Parse(command.ID); err != nil || len(command.Command) > 64*1024 || strings.TrimSpace(command.Command) == "" || strings.ContainsAny(command.Command, "\x00\r\n\x03\x04") {
					failure = "managed CMD commands must contain one command line"
				} else if !isCmdShell(shell) {
					failure = "shell does not support reliable command status"
				} else if !ready || active != nil {
					failure = "terminal shell is busy"
				}
				if failure != "" {
					if complete(protocol.ShellCommandResult{ID: command.ID, ExitCode: 255, Error: failure}) != nil {
						return 255
					}
					continue
				}
				active, ready = &command, false
				if markBusy() != nil {
					return 255
				}
				writeInput([]byte(command.Command + "\r"))
			case protocol.FrameStdin:
				if query != "" {
					// A browser keystroke arriving between prompt and query must
					// not change ERRORLEVEL or be consumed by the private query.
					if len(queuedInput)+len(frame.Data) > 64*1024 {
						return 255
					}
					queuedInput = append(queuedInput, frame.Data...)
					continue
				}
				if len(frame.Data) > 0 {
					ready = false
					if markBusy() != nil {
						return 255
					}
				}
				writeInput(frame.Data)
			case protocol.FrameResize:
				resize(frame.Data)
			}
		case chunk := <-chunks:
			if query != "" {
				queryOutput += string(chunk.data)
				if len(queryOutput) > 128*1024 {
					return 255
				}
				// Reparse the short query transcript after removing its complete
				// echo. A fragmented echo contains result-shaped records itself.
				queryReady, resultSeen = false, false
				result.ExitCode, result.Error = 255, ""
				probe = &legacyWindowsPromptDecoder{marker: marker, onResult: probe.onResult, onPrompt: probe.onPrompt}
				visible := filterWindowsQueryEcho(queryOutput, query)
				probe.decode(visible, chunk.err != nil)
				if queryReady {
					// Strip only the private command echo and result records.
					// Other output, including concurrent background output, survives.
					text := decoder.decode(visible, chunk.err != nil)
					if writeOutput(text) != nil {
						return 255
					}
					queryTimer.Stop()
					if !resultSeen {
						result.Error = "CMD command exit status is unavailable"
					}
					if !validating && result.Error == "" {
						candidate, validating = result.ExitCode, true
						query, queryOutput = windowsCommandValidationQuery(marker, active.ID, candidate), ""
						queryTimer = time.NewTimer(windowsCommandQueryTimeout)
						queryDeadline = queryTimer.C
						writeInput([]byte(query + "\r"))
						continue
					}
					if complete(result) != nil {
						return 255
					}
					queryDeadline = nil
					query, queryOutput, active, ready = "", "", nil, true
					if len(queuedInput) > 0 {
						ready = false
						if markBusy() != nil {
							return 255
						}
						writeInput(queuedInput)
						queuedInput = nil
					}
				}
			} else {
				prompts = 0
				if writeOutput(decoder.decode(string(chunk.data), chunk.err != nil)) != nil {
					return 255
				}
				if prompts > 0 {
					ready = true
					if active != nil {
						query = "@echo " + marker + "R;%errorlevel%;" + active.ID + legacyWindowsPromptEnd(marker)
						result = protocol.ShellCommandResult{ID: active.ID, ExitCode: 255}
						queryReady, resultSeen = false, false
						validating = false
						probe = &legacyWindowsPromptDecoder{marker: marker, onResult: func(id, code string) {
							if id != active.ID || code == "%errorlevel%" {
								return
							}
							resultSeen = true
							if value, err := strconv.ParseInt(code, 10, 32); err == nil && (!validating || int(value) == candidate) {
								result.ExitCode = int(value)
							} else {
								result.Error = "CMD command exit status is unavailable"
							}
						}, onPrompt: func() { queryReady = true }}
						queryTimer = time.NewTimer(windowsCommandQueryTimeout)
						queryDeadline = queryTimer.C
						writeInput([]byte(query + "\r"))
					}
				}
			}
			if chunk.err != nil {
				chunks = nil
				if shellExited {
					return shellExitCode
				}
			}
		case code := <-exited:
			if chunks == nil {
				return code
			}
			shellExitCode, shellExited, exited = code, true, nil
			// WinPTY closes its output after child exit. ConPTY needs its
			// console closed while output is still being drained, otherwise
			// the final rendered text can be lost or ClosePseudoConsole block.
			if drainOutput != nil {
				go drainOutput()
			}
			exitTimer = time.NewTimer(2 * time.Second)
			exitDeadline = exitTimer.C
		case <-exitDeadline:
			return shellExitCode
		case <-queryDeadline:
			_ = complete(protocol.ShellCommandResult{ID: active.ID, ExitCode: 255, Error: "CMD command status query timed out"})
			return 255
		}
	}
}

// ERRORLEVEL may be shadowed by an environment variable. IF ERRORLEVEL reads
// CMD's actual signed status; validate the expansion without changing either
// the status or that variable. IF and ECHO preserve the previous status.
func windowsCommandValidationQuery(marker, id string, code int) string {
	query := "@if errorlevel " + strconv.Itoa(code) + " "
	if code < 2147483647 {
		query += "if not errorlevel " + strconv.Itoa(code+1) + " "
	}
	return query + "echo " + marker + "R;" + strconv.Itoa(code) + ";" + id + legacyWindowsPromptEnd(marker)
}

// ConPTY/WinPTY can wrap the echoed input and insert cursor controls anywhere
// in it. Match normalized text but remove only its corresponding raw bytes.
func filterWindowsQueryEcho(raw, query string) string {
	for {
		text, positions := normalizedLegacyPrompt(raw)
		start := strings.Index(text, query)
		if start < 0 {
			return raw
		}
		end := positions[start+len(query)-1] + 1
		raw = raw[:positions[start]] + legacyPromptLayout(raw[positions[start]:end]) + raw[end:]
	}
}

func stopWindowsProcessTree(process windows.Handle) {
	if state, err := windows.WaitForSingleObject(process, 0); err != nil || state != uint32(windows.WAIT_TIMEOUT) {
		return
	}
	processID, err := windows.GetProcessId(process)
	if err == nil {
		kill := exec.Command("taskkill", "/PID", fmt.Sprint(processID), "/T", "/F")
		kill.SysProcAttr = hiddenWindowSysProcAttr()
		_ = kill.Run()
	}
	_ = windows.TerminateProcess(process, 255)
}
