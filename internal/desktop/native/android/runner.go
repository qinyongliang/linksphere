package android

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"
)

const commandTimeout = 20 * time.Second
const maxOutputBytes = 24 << 20

var errUIAutomationBusy = errors.New("Android accessibility connection is already in use by another automation; release it locally or use a configured accessibility backend")

// LocalRunner executes argv directly; it never invokes a shell or evaluates UI text.
type LocalRunner struct {
	Timeout   time.Duration
	MaxOutput int
}

func (r LocalRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = commandTimeout
	}
	limit := r.MaxOutput
	if limit <= 0 {
		limit = maxOutputBytes
	}
	callerCtx := ctx
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	prepareCommand(cmd)
	cmd.WaitDelay = time.Second
	stdout := &limitWriter{limit: limit}
	cmd.Stdout = stdout
	stderr := &uiautomatorStderr{limitWriter: limitWriter{limit: 4096}}
	if filepath.Base(name) == "uiautomator" && len(args) > 0 && args[0] == "dump" {
		stderr.cancel = cancel
	}
	cmd.Stderr = stderr
	// Run waits for stderr copying before we inspect busy, so the writer and
	// this goroutine never access its state concurrently.
	if err := cmd.Run(); err != nil || stderr.busy {
		if callerCtx.Err() != nil {
			return nil, callerCtx.Err()
		}
		if stderr.busy {
			return nil, errUIAutomationBusy
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Device diagnostics can contain screen data or typed text. Return only the
		// fixed command name and exit status, never stdout/stderr.
		return nil, fmt.Errorf("Android tool %s failed: %w", name, err)
	}
	if stdout.overflow {
		return nil, errors.New("Android command output exceeds limit")
	}
	return stdout.buffer.Bytes(), nil
}

type uiautomatorStderr struct {
	limitWriter
	cancel context.CancelFunc
	busy   bool
}

func (w *uiautomatorStderr) Write(data []byte) (int, error) {
	n, err := w.limitWriter.Write(data)
	if w.cancel != nil && !w.busy && bytes.Contains(w.buffer.Bytes(), []byte("java.lang.IllegalStateException: UiAutomationService already registered")) {
		w.busy = true
		// Cancel only this command; prepareCommand also stops its private process
		// group on Android. Never interfere with the existing automation owner.
		w.cancel()
	}
	return n, err
}

type limitWriter struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (w *limitWriter) Write(data []byte) (int, error) {
	n := min(len(data), max(0, w.limit-w.buffer.Len()))
	w.buffer.Write(data[:n])
	if n < len(data) {
		w.overflow = true
	}
	return len(data), nil
}
