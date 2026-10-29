//go:build linux

package android

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestAndroidLocalRunnerBusyStopsOwnedProcessGroup(t *testing.T) {
	t.Setenv("GOSSHD_ANDROID_RUNNER_FIXTURE", "busy-descendant")
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	t.Setenv("GOSSHD_ANDROID_RUNNER_CHILD_PID", pidFile)
	started := time.Now()
	_, err := (LocalRunner{Timeout: 3 * time.Second}).Run(context.Background(), androidRunnerExecutable(t, "uiautomator"), "dump")
	data, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatalf("descendant never started: %v", readErr)
	}
	pid, parseErr := strconv.Atoi(string(data))
	if parseErr != nil || pid <= 1 {
		t.Fatalf("invalid descendant pid: %q", data)
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	if err == nil || err.Error() != androidBusyMessage || time.Since(started) > time.Second {
		t.Fatalf("busy process group was not promptly diagnosed/stopped: %v", err)
	}
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		stat, statErr := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if os.IsNotExist(statErr) {
			return
		}
		// A killed orphan may remain a zombie until init reaps it.
		if i := strings.LastIndexByte(string(stat), ')'); i >= 0 && len(stat) > i+2 && stat[i+2] == 'Z' {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("owned descendant survived busy command cancellation")
}
