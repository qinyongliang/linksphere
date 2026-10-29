//go:build windows

package desktop

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The fixture and helper run on a private, never activated desktop. This also
// runs on disconnected CI sessions without locking or switching the real one.
func TestWindowsNativeInputDesktopAvailability(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	helper, cleanup, err := platformCommand(ctx, Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	compiler := filepath.Join(os.Getenv("SystemRoot"), "Microsoft.NET", "Framework64", "v4.0.30319", "csc.exe")
	if _, err := os.Stat(compiler); err != nil {
		compiler = filepath.Join(os.Getenv("SystemRoot"), "Microsoft.NET", "Framework", "v4.0.30319", "csc.exe")
	}
	fixture := filepath.Join(t.TempDir(), "InputDesktopTest.exe")
	cmd := exec.CommandContext(ctx, compiler, "/nologo", "/target:exe", "/main:InputDesktopTest", "/r:System.Windows.Forms.dll", "/r:System.Drawing.dll", "/r:System.Web.Extensions.dll", "/out:"+fixture, filepath.Join("native", "windows", "InputDesktopTest.cs"), filepath.Join("native", "windows", "Fixture.cs"))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("input desktop fixture compile: %v %s", err, out)
	}
	cmd = exec.CommandContext(ctx, fixture, helper.Path)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("input desktop availability: %v %s", err, out)
	}
	if strings.Contains(string(out), "Session 0 fixture skipped") {
		t.Skip(string(out))
	}
	if !strings.Contains(string(out), "non-input desktop status, foreground guards and background fixture passed") {
		t.Fatalf("input desktop fixture did not complete: %s", out)
	}
	t.Log(strings.TrimSpace(string(out)))
}

func TestWindowsNonInteractiveStatusPreservesBackgroundService(t *testing.T) {
	svc := NewService(Adapter{
		Authorize: func(context.Context, string, string, bool) error { return nil },
		Dial: func(context.Context, string) (io.ReadWriteCloser, error) {
			client, native := net.Pipe()
			go func() {
				defer native.Close()
				_ = writeResponse(native, Response{ID: "init", OK: true})
				scan := bufio.NewScanner(native)
				for scan.Scan() {
					var req Request
					_ = json.Unmarshal(scan.Bytes(), &req)
					var result any = map[string]any{"ok": true, "operation_method": "uia.value"}
					switch req.Method {
					case "status":
						result = map[string]any{"interactive": false, "background_supported": true, "permissions": map[string]bool{"screen_capture": false, "accessibility": true}}
					case "get_window_state":
						result = State{SnapshotID: "inactive", Window: *req.Params.Window, Bounds: Bounds{Width: 100, Height: 100}}
					}
					body, _ := json.Marshal(result)
					_ = writeResponse(native, Response{ID: req.ID, OK: true, Result: body})
				}
			}()
			return client, nil
		},
	})
	defer svc.Close()
	ctx := context.Background()
	info, _, err := svc.Open(ctx, "actor", "target")
	if err != nil {
		t.Fatal(err)
	}
	window := &Window{ID: "10", App: "20"}
	screenshot := false
	if _, err := svc.Call(ctx, "actor", info.ID, Request{Method: "get_window_state", Params: Params{Mode: "background", Window: window, IncludeScreenshot: &screenshot}}); err != nil {
		t.Fatal(err)
	}
	index := 0
	if _, err := svc.Call(ctx, "actor", info.ID, Request{Method: "set_value", Params: Params{Mode: "background", Window: window, SnapshotID: "inactive", ElementIndex: &index, Text: "sample"}}); err != nil {
		t.Fatalf("interactive=false blocked available background input: %v", err)
	}
}
