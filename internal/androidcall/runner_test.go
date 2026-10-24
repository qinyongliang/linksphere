package call

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestPerManagerRunnerCapturesAllDeviceCommands(t *testing.T) {
	var mu sync.Mutex
	var commands []string
	finished := make(chan struct{}, 1)
	m := NewManager(Config{PollInterval: time.Millisecond, CommandRunner: func(ctx context.Context, n string, a ...string) (string, error) {
		mu.Lock()
		commands = append(commands, fmt.Sprint(append([]string{n}, a...)))
		mu.Unlock()
		switch n {
		case "dumpsys":
			if a[0] == "telephony.registry" {
				return "mVoiceRegState=0(IN_SERVICE)", nil
			}
			return "state: ACTIVE", nil
		case "am":
			return "", nil
		case "input":
			finished <- struct{}{}
			return "", nil
		default:
			return "", fmt.Errorf("unexpected fake command %s", n)
		}
	}})
	c, err := m.StartMedia(Request{Number: "12345"}, "synthetic", func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("fake hangup not reached")
	}
	deadline := time.Now().Add(time.Second)
	for m.Active() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if m.Active() {
		t.Fatal("worker did not complete")
	}
	got, ok := m.Get(c.ID)
	if !ok || got.State != StateCompleted {
		t.Fatalf("final=%+v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(commands) != 4 {
		t.Fatalf("commands=%v", commands)
	}
	want := []string{"[dumpsys telephony.registry]", "[am start --user 0 -a android.intent.action.CALL -d tel:12345]", "[dumpsys telecom]", "[input keyevent KEYCODE_ENDCALL]"}
	for i := range want {
		if commands[i] != want[i] {
			t.Fatalf("commands=%v", commands)
		}
	}
}
func TestRunnerIsPerManager(t *testing.T) {
	a := NewManager(Config{CommandRunner: func(context.Context, string, ...string) (string, error) { return "a", nil }})
	b := NewManager(Config{CommandRunner: func(context.Context, string, ...string) (string, error) { return "b", nil }})
	x, _ := a.runCommand(context.Background(), "synthetic")
	y, _ := b.runCommand(context.Background(), "synthetic")
	if x != "a" || y != "b" {
		t.Fatal("runner cross-contamination")
	}
}
