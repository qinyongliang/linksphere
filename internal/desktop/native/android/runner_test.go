package android

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	mode := os.Getenv("GOSSHD_ANDROID_RUNNER_FIXTURE")
	if mode == "" {
		os.Exit(m.Run())
	}
	switch mode {
	case "overflow":
		fmt.Print(strings.Repeat("x", 1024))
	case "hang":
		time.Sleep(time.Hour)
	case "error":
		fmt.Fprintln(os.Stderr, "private UI text must not leave stderr")
		os.Exit(2)
	case "busy":
		fmt.Fprint(os.Stderr, "private UI text must not leave stderr\njava.lang.IllegalStateException: UiAuto")
		time.Sleep(20 * time.Millisecond)
		fmt.Fprintln(os.Stderr, "mationService already registered!")
		time.Sleep(time.Hour)
	case "busy-exit":
		fmt.Fprintln(os.Stderr, "java.lang.IllegalStateException: UiAutomationService already registered!")
		os.Exit(2)
	case "similar":
		fmt.Fprintln(os.Stderr, "java.lang.RuntimeException: UiAutomationService already registered!")
		os.Exit(2)
	case "busy-overflow":
		fmt.Fprint(os.Stderr, strings.Repeat("x", 4096))
		fmt.Fprintln(os.Stderr, "java.lang.IllegalStateException: UiAutomationService already registered!")
		os.Exit(2)
	case "busy-descendant":
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), "GOSSHD_ANDROID_RUNNER_FIXTURE=descendant")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(3)
		}
		for i := 0; i < 100; i++ {
			if _, err := os.Stat(os.Getenv("GOSSHD_ANDROID_RUNNER_CHILD_PID")); err == nil {
				fmt.Fprintln(os.Stderr, "java.lang.IllegalStateException: UiAutomationService already registered!")
				time.Sleep(time.Hour)
			}
			time.Sleep(10 * time.Millisecond)
		}
		_ = child.Process.Kill()
		_ = child.Wait()
		os.Exit(3)
	case "descendant":
		if err := os.WriteFile(os.Getenv("GOSSHD_ANDROID_RUNNER_CHILD_PID"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			os.Exit(3)
		}
		time.Sleep(time.Hour)
	}
	os.Exit(0)
}

func androidRunnerExecutable(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name)
	file := path
	if runtime.GOOS == "windows" {
		file += ".exe"
	}
	if err := os.WriteFile(file, data, 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

const androidBusyMessage = "Android accessibility connection is already in use by another automation; release it locally or use a configured accessibility backend"

func TestAndroidLocalRunnerStopsBusyUIAutomation(t *testing.T) {
	t.Setenv("GOSSHD_ANDROID_RUNNER_FIXTURE", "busy")
	name := androidRunnerExecutable(t, "uiautomator")
	started := time.Now()
	_, err := (LocalRunner{Timeout: 3 * time.Second}).Run(context.Background(), name, "dump")
	if err == nil || err.Error() != androidBusyMessage {
		t.Fatalf("busy UI automation diagnosis: %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("busy UI automation command was not stopped promptly")
	}
}

func TestAndroidUIAutomationStderrSplitMarkerAndLimit(t *testing.T) {
	cancels := 0
	w := &uiautomatorStderr{limitWriter: limitWriter{limit: 4096}, cancel: func() { cancels++ }}
	for _, b := range []byte("private UI text\njava.lang.IllegalStateException: UiAutomationService already registered!\n") {
		if n, err := w.Write([]byte{b}); n != 1 || err != nil {
			t.Fatalf("stderr write: %d, %v", n, err)
		}
	}
	if !w.busy || cancels != 1 {
		t.Fatalf("split marker: busy=%v, cancels=%d", w.busy, cancels)
	}
	_, _ = w.Write([]byte(strings.Repeat("x", 8192)))
	if w.buffer.Len() != 4096 || !w.overflow || cancels != 1 {
		t.Fatal("stderr limit or single cancellation was not preserved")
	}
}

func TestAndroidLocalRunnerBusyClassificationIsNarrow(t *testing.T) {
	for _, tc := range []struct {
		name, tool, operation, mode string
	}{
		{"other-tool", "dumpsys", "dump", "busy-exit"},
		{"other-operation", "uiautomator", "events", "busy-exit"},
		{"other-exception", "uiautomator", "dump", "similar"},
		{"beyond-stderr-limit", "uiautomator", "dump", "busy-overflow"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GOSSHD_ANDROID_RUNNER_FIXTURE", tc.mode)
			_, err := (LocalRunner{}).Run(context.Background(), androidRunnerExecutable(t, tc.tool), tc.operation)
			if err == nil || errors.Is(err, errUIAutomationBusy) || !strings.Contains(err.Error(), "failed:") {
				t.Fatalf("unrelated tool failure misclassified: %v", err)
			}
			if strings.Contains(err.Error(), "UiAutomationService") {
				t.Fatal("stderr escaped the runner")
			}
		})
	}
}

func TestAndroidLocalRunnerPreservesCallerCancellation(t *testing.T) {
	t.Setenv("GOSSHD_ANDROID_RUNNER_FIXTURE", "busy")
	name := androidRunnerExecutable(t, "uiautomator")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (LocalRunner{}).Run(ctx, name, "dump"); !errors.Is(err, context.Canceled) {
		t.Fatalf("caller cancellation: %v", err)
	}
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := (LocalRunner{}).Run(ctx, name, "dump"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("caller deadline: %v", err)
	}
	t.Setenv("GOSSHD_ANDROID_RUNNER_FIXTURE", "hang")
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	timer := time.AfterFunc(100*time.Millisecond, cancel)
	defer timer.Stop()
	if _, err := (LocalRunner{}).Run(ctx, name, "dump"); !errors.Is(err, context.Canceled) {
		t.Fatalf("running command cancellation: %v", err)
	}
}

func TestAndroidLocalRunnerBoundsCancellationAndDiagnostics(t *testing.T) {
	for _, mode := range []string{"overflow", "hang", "error"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("GOSSHD_ANDROID_RUNNER_FIXTURE", mode)
			timeout := 2 * time.Second
			if mode == "hang" {
				timeout = 100 * time.Millisecond
			}
			started := time.Now()
			_, err := (LocalRunner{Timeout: timeout, MaxOutput: 32}).Run(context.Background(), os.Args[0])
			if err == nil {
				t.Fatal("runner failure ignored")
			}
			if strings.Contains(err.Error(), "private UI text") {
				t.Fatal("native diagnostic data escaped")
			}
			if mode == "hang" && time.Since(started) > time.Second {
				t.Fatal("runner did not cancel")
			}
		})
	}
}
