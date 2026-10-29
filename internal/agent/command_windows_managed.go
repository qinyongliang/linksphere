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
	manualContaminated := false
	var queuedInput []byte
	var query, queryOutput, queryID string
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
	var queryBase legacyWindowsPromptDecoder
	var result protocol.ShellCommandResult
	var lower, upper, threshold int64
	const manualStatusError = "command exit status is unknown after manual input; open a new session for reliable command execution"
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
	startQuery := func() {
		threshold = lower + (upper-lower+1)/2
		// A resize can replay result records from earlier thresholds too.
		// Match this exact query, independently of the command completion ID.
		queryID = active.ID + ":" + strconv.FormatInt(threshold, 10)
		query = windowsCommandStatusQuery(marker, queryID, threshold)
		queryOutput = ""
		queryBase = cloneWindowsPromptDecoder(&decoder)
		queryTimer = time.NewTimer(windowsCommandQueryTimeout)
		queryDeadline = queryTimer.C
		writeInput([]byte(query + "\r"))
	}
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
				} else if manualContaminated {
					failure = manualStatusError
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
					if active != nil && strings.Trim(string(frame.Data), "\x03") != "" {
						// Bytes can be either application stdin or a queued shell
						// command. Never send a query into that ambiguous input queue.
						manualContaminated = true
					}
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
				queryReady, resultBit := false, -1
				probe := cloneWindowsPromptDecoder(&queryBase)
				probe.onPrompt = func() { queryReady = true }
				probe.onResult = func(id, bit string) {
					if id == queryID && (bit == "0" || bit == "1") {
						resultBit, _ = strconv.Atoi(bit)
					}
				}
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
					if resultBit < 0 {
						result.Error = "CMD command exit status is unavailable"
					}
					if result.Error == "" {
						if resultBit == 1 {
							lower = threshold
						} else {
							upper = threshold - 1
						}
						if lower != upper {
							startQuery()
							continue
						}
						result.ExitCode = int(lower)
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
						if manualContaminated {
							if complete(protocol.ShellCommandResult{ID: active.ID, ExitCode: 255, Error: manualStatusError}) != nil {
								return 255
							}
							active = nil
						} else {
							result = protocol.ShellCommandResult{ID: active.ID, ExitCode: 255}
							lower, upper = -2147483648, 2147483647
							startQuery()
						}
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

// IF ERRORLEVEL reads CMD's actual signed status even when an environment
// variable shadows it. Binary search uses trusted numeric constants only;
// expanding an environment value here would turn data into executable syntax.
// IF and ECHO preserve both the native status and the persistent environment.
func windowsCommandStatusQuery(marker, id string, threshold int64) string {
	return "@if errorlevel " + strconv.FormatInt(threshold, 10) + " (echo " + marker + "R;1;" + id + legacyWindowsPromptEnd(marker) + ") else echo " + marker + "R;0;" + id + legacyWindowsPromptEnd(marker)
}

func cloneWindowsPromptDecoder(source *legacyWindowsPromptDecoder) legacyWindowsPromptDecoder {
	clone := *source
	clone.seenPrompts = make(map[[32]byte]struct{}, len(source.seenPrompts))
	for key := range source.seenPrompts {
		clone.seenPrompts[key] = struct{}{}
	}
	clone.promptOrder = append([][32]byte(nil), source.promptOrder...)
	return clone
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
