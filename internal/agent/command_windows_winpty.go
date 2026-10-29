//go:build windows && (amd64 || 386)

package agent

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/qinyongliang/linksphere/internal/protocol"

	winpty "github.com/iamacarpet/go-winpty"
	"golang.org/x/sys/windows"
)

func (c *Client) handleWinPTYShell(stream io.ReadWriteCloser, reader *bufio.Reader, req protocol.StreamRequest) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("winpty unavailable: %v", recovered)
		}
	}()

	exePath, err := os.Executable()
	if err != nil {
		return err
	}
	marker := "__GOSSHD_" + strings.ReplaceAll(uuid.NewString(), "-", "") + "__"
	cols, rows := terminalSize(req)
	pty, err := winpty.OpenWithOptions(winpty.Options{
		DLLPrefix:   filepath.Dir(exePath),
		Command:     windowsCommandLineWithArgs(c.cfg.Shell, windowsLegacyInteractiveShellArgs(c.cfg.Shell, marker)),
		Dir:         c.cfg.Root,
		Env:         os.Environ(),
		Flags:       winpty.WINPTY_FLAG_ALLOW_CURPROC_DESKTOP_CREATION,
		InitialCols: uint32(cols),
		InitialRows: uint32(rows),
	})
	if err != nil {
		return err
	}
	defer pty.Close()

	if err := protocol.WriteJSONLine(stream, protocol.StreamResponse{OK: true, ShellCommandStatus: isCmdShell(c.cfg.Shell)}); err != nil {
		return err
	}

	exited := make(chan int, 1)
	processDone := make(chan struct{})
	go func() {
		defer close(processDone)
		exited <- waitHandleExitCode(pty.GetProcHandle())
	}()
	processID, _ := windows.GetProcessId(windows.Handle(pty.GetProcHandle()))
	stop := sync.OnceFunc(func() {
		stopWindowsProcessTree(windows.Handle(pty.GetProcHandle()))
		_ = pty.StdIn.Close()
		_ = pty.StdOut.Close()
	})
	code := serveWindowsPTYShell(stream, reader, pty.StdOut, c.cfg.Shell, marker,
		func(data []byte) { writeWinPTYInput(pty.StdIn, data) },
		func() { _ = interruptConsoleProcess(processID) },
		func(data []byte) {
			if len(data) == 8 {
				width := uint32(data[0])<<24 | uint32(data[1])<<16 | uint32(data[2])<<8 | uint32(data[3])
				height := uint32(data[4])<<24 | uint32(data[5])<<16 | uint32(data[6])<<8 | uint32(data[7])
				pty.SetSize(width, height)
			}
		}, stop, exited, nil)
	<-processDone
	_ = protocol.WriteFrame(stream, protocol.ExitFrame(code))
	return nil
}

func writeWinPTYInput(w io.Writer, data []byte) {
	for len(data) > 0 {
		special := -1
		for i, b := range data {
			if b == 0x04 {
				special = i
				break
			}
		}
		if special < 0 {
			_, _ = w.Write(data)
			return
		}
		if special > 0 {
			_, _ = w.Write(data[:special])
		}
		_, _ = w.Write([]byte("exit\r\n"))
		data = data[special+1:]
	}
}

func terminalSize(req protocol.StreamRequest) (int, int) {
	cols, rows := req.Width, req.Height
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	return cols, rows
}
