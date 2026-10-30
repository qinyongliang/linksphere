//go:build !windows

package agent

import (
	"bufio"
	"encoding/binary"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/qinyongliang/linksphere/internal/protocol"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

func (c *Client) handleCommand(stream io.ReadWriteCloser, reader *bufio.Reader, req protocol.StreamRequest) {
	cmd := agentShellCommand(c.cfg.Shell, req.Type)
	if req.Type == protocol.StreamExec {
		cmd = exec.Command(c.cfg.Shell, "-lc", req.Command)
	}
	cmd.Dir = c.cfg.Root
	cmd.Env = commandEnvironment(os.Environ(), c.cfg.Root)
	shellInterrupt := req.Type == protocol.StreamShell && shellBaseName(c.cfg.Shell) == "bash"
	if err := protocol.WriteJSONLine(stream, protocol.StreamResponse{OK: true, ShellCommandInterrupt: shellInterrupt}); err != nil {
		return
	}
	if req.Type == protocol.StreamShell && shellBaseName(c.cfg.Shell) == "bash" {
		if err := ensureAgentBashRC(); err != nil {
			_ = protocol.WriteFrame(stream, protocol.Frame{Type: protocol.FrameStderr, Data: []byte(err.Error())})
			_ = protocol.WriteFrame(stream, protocol.ExitFrame(255))
			return
		}
	}
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(req.Width), Rows: uint16(req.Height)})
	if err != nil {
		_ = protocol.WriteFrame(stream, protocol.Frame{Type: protocol.FrameStderr, Data: []byte(err.Error())})
		_ = protocol.WriteFrame(stream, protocol.ExitFrame(255))
		return
	}
	if req.Type == protocol.StreamShell {
		pollable, err := interruptiblePTY(ptmx)
		if err != nil {
			_ = cmd.Process.Kill()
			_ = ptmx.Close()
			_ = waitExitCode(cmd)
			_ = protocol.WriteFrame(stream, protocol.Frame{Type: protocol.FrameStderr, Data: []byte(err.Error())})
			_ = protocol.WriteFrame(stream, protocol.ExitFrame(255))
			return
		}
		_ = ptmx.Close()
		ptmx = pollable
	}
	defer ptmx.Close()
	var stop func()
	var finish func()
	waited := false
	if req.Type == protocol.StreamExec {
		var mu sync.Mutex
		finished := false
		finish = func() {
			mu.Lock()
			finished = true
			mu.Unlock()
		}
		stop = func() {
			mu.Lock()
			defer mu.Unlock()
			if finished {
				return
			}
			finished = true
			// A shell may put its child command in a separate foreground group.
			if pgid, err := unix.IoctlGetInt(int(ptmx.Fd()), unix.TIOCGPGRP); err == nil && pgid > 0 {
				_ = syscall.Kill(-pgid, syscall.SIGKILL)
			}
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		defer func() {
			if !waited {
				stop()
				_ = waitExitCode(cmd)
			}
			finish()
		}()
	} else if req.Type == protocol.StreamShell {
		var mu sync.Mutex
		finished := false
		finish = func() {
			mu.Lock()
			finished = true
			mu.Unlock()
		}
		stop = func() {
			mu.Lock()
			defer mu.Unlock()
			if finished {
				return
			}
			finished = true
			// StartWithSize gave this shell its own session and controlling
			// terminal. Freeze only this closing shell before its foreground
			// job dies, so it cannot resume a sourced tail between the kills.
			// The Process handle rejects a late stop after Wait reaped it.
			if err := cmd.Process.Signal(syscall.SIGSTOP); err == nil {
				if pgid, err := unix.IoctlGetInt(int(ptmx.Fd()), unix.TIOCGPGRP); err == nil && pgid > 1 && pgid != syscall.Getpgrp() {
					// A pipeline leader may already have exited while its group
					// still belongs to this terminal; Getsid then returns ESRCH.
					if sid, err := unix.Getsid(pgid); err == nil && sid == cmd.Process.Pid || err == unix.ESRCH {
						_ = syscall.Kill(-pgid, syscall.SIGKILL)
					}
				}
				_ = cmd.Process.Kill()
			}
			_ = ptmx.Close()
		}
		defer func() {
			if !waited {
				stop()
				_ = waitExitCode(cmd)
			}
			finish()
		}()
	}
	var interrupt func() error
	if shellInterrupt {
		interrupt = func() error {
			return interruptShell(cmd.Process, ptmx)
		}
	}
	go copyFramesToWriter(ptmx, reader, stop, interrupt)
	buf := make([]byte, 32*1024)
	for {
		n, err := ptmx.Read(buf)
		if n > 0 {
			if writeErr := protocol.WriteFrame(stream, protocol.Frame{Type: protocol.FrameStdout, Data: append([]byte(nil), buf[:n]...)}); writeErr != nil {
				return
			}
		}
		if err != nil {
			break
		}
	}
	code := waitExitCode(cmd)
	waited = true
	if finish != nil {
		finish()
	}
	_ = protocol.WriteFrame(stream, protocol.ExitFrame(code))
}

func interruptiblePTY(terminal *os.File) (*os.File, error) {
	raw, err := terminal.SyscallConn()
	if err != nil {
		return nil, err
	}
	descriptor := -1
	var duplicateErr error
	err = raw.Control(func(fd uintptr) {
		descriptor, duplicateErr = unix.FcntlInt(fd, unix.F_DUPFD_CLOEXEC, 0)
		if duplicateErr == nil {
			duplicateErr = unix.SetNonblock(descriptor, true)
		}
	})
	if err == nil {
		err = duplicateErr
	}
	if err != nil {
		if descriptor >= 0 {
			_ = unix.Close(descriptor)
		}
		return nil, err
	}
	// pty setup calls File.Fd, which makes its File permanently blocking.
	// A new File over our nonblocking duplicate can interrupt Read on Close;
	// subsequent Fd calls used for resize do not reset its explicit mode.
	return os.NewFile(uintptr(descriptor), terminal.Name()), nil
}

func agentShellCommand(shell string, streamType string) *exec.Cmd {
	if streamType != protocol.StreamShell {
		return exec.Command(shell)
	}
	if shellBaseName(shell) == "bash" {
		return exec.Command(shell, "--rcfile", agentBashRCPath(), "-i")
	}
	return exec.Command(shell, "-l")
}

func agentBashRCPath() string {
	return filepath.Join(os.TempDir(), "gosshd-agent-bashrc")
}

func ensureAgentBashRC() error {
	path := agentBashRCPath()
	return os.WriteFile(path, []byte(agentBashRC()), 0600)
}

func agentBashRC() string {
	return `if [ -r /etc/motd ]; then
  cat /etc/motd
fi
if [ -r /etc/profile ]; then
  . /etc/profile
fi
if [ -r "$HOME/.bash_profile" ]; then
  . "$HOME/.bash_profile"
elif [ -r "$HOME/.bash_login" ]; then
  . "$HOME/.bash_login"
elif [ -r "$HOME/.profile" ]; then
  . "$HOME/.profile"
fi
if [ -r "$HOME/.bashrc" ]; then
  . "$HOME/.bashrc"
fi
__gosshd_bash_source_supported=0
if { builtin source <(builtin printf ':'); } >/dev/null 2>&1; then
  __gosshd_bash_source_supported=1
fi
__gosshd_command_running=0
__gosshd_original_prompt_command=${PROMPT_COMMAND-}
__gosshd_preexec() {
  if [ "$__gosshd_command_running" = 0 ]; then
    __gosshd_command_running=1
    printf '\033]633;C\007'
  fi
}
__gosshd_precmd() {
  local __gosshd_rc=$?
  trap - DEBUG
  if [ "$__gosshd_command_running" = 1 ]; then
    printf '\033]633;D;%s\007' "$__gosshd_rc"
    __gosshd_command_running=0
  fi
  printf '\033]633;A\007'
  if [ -n "$__gosshd_original_prompt_command" ]; then
    eval "$__gosshd_original_prompt_command"
  fi
  if [ "$__gosshd_bash_source_supported" = 1 ]; then
    printf '\033]633;P;GosshdBashSource=1\007'
  fi
  case "$PWD" in
    *$'\e'*|*$'\a'*|*$'\r'*|*$'\n'*) ;;
    *) printf '\033]633;P;Cwd=%s\007' "$PWD" ;;
  esac
  trap '__gosshd_preexec' DEBUG
}
PROMPT_COMMAND='__gosshd_precmd'
export -n PROMPT_COMMAND 2>/dev/null || true
trap '__gosshd_preexec' DEBUG
`
}

func shellBaseName(shell string) string {
	base := filepath.Base(strings.TrimSpace(shell))
	return strings.TrimPrefix(base, "-")
}

type shellInterruptProcess interface {
	Signal(os.Signal) error
}

func interruptShell(process shellInterruptProcess, terminal io.Writer) error {
	// A foreground job can consume terminal SIGINT. Notify our own shell first
	// so it also stops a sourced command, then interrupt the foreground PTY job.
	if err := process.Signal(os.Interrupt); err != nil {
		return err
	}
	_, err := terminal.Write([]byte{3})
	return err
}

func copyFramesToWriter(w io.Writer, reader *bufio.Reader, onDisconnect func(), interrupt func() error) {
	defer func() {
		if onDisconnect != nil {
			onDisconnect()
		}
	}()
	for {
		frame, err := protocol.ReadFrame(reader)
		if err != nil {
			return
		}
		switch frame.Type {
		case protocol.FrameStdin:
			_, _ = w.Write(frame.Data)
		case protocol.FrameInterrupt:
			if len(frame.Data) == 0 && interrupt != nil {
				if err := interrupt(); err != nil {
					return
				}
			}
		case protocol.FrameResize:
			if len(frame.Data) == 8 {
				_ = pty.Setsize(w.(*os.File), &pty.Winsize{
					Cols: uint16(binary.BigEndian.Uint32(frame.Data[0:4])),
					Rows: uint16(binary.BigEndian.Uint32(frame.Data[4:8])),
				})
			}
		}
	}
}

func waitExitCode(cmd *exec.Cmd) int {
	if err := cmd.Wait(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			if status, ok := exit.Sys().(syscall.WaitStatus); ok {
				if status.Signaled() {
					return 128 + int(status.Signal())
				}
				return status.ExitStatus()
			}
		}
		return 255
	}
	return 0
}
