package call

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func operationManager(hangup func()) *Manager {
	return NewManager(Config{PollInterval: time.Millisecond, CommandRunner: func(c context.Context, n string, a ...string) (string, error) {
		switch n {
		case "dumpsys":
			if a[0] == "telephony.registry" {
				return "mVoiceRegState=0(IN_SERVICE)", nil
			}
			return "state: ACTIVE", nil
		case "am":
			return "", nil
		case "input":
			hangup()
			return "", nil
		}
		return "", fmt.Errorf("unexpected fake command")
	}})
}
func waitSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("barrier not reached")
	}
}
func TestOwnedOperationRejectsInvalidLifetimeBeforeCommands(t *testing.T) {
	var calls atomic.Int32
	m := NewManager(Config{CommandRunner: func(context.Context, string, ...string) (string, error) { calls.Add(1); return "", nil }})
	media := func(context.Context) error { t.Error("unexpected media"); return nil }
	no := false
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, c := range []context.Context{nil, ctx} {
		if _, e := m.RunMediaContext(c, Request{Number: "12345"}, "test", media); e == nil {
			t.Fatal("accepted invalid context")
		}
	}
	if _, e := m.RunMediaContext(context.Background(), Request{Number: "12345", HangupAfterPlayback: &no}, "test", media); e == nil {
		t.Fatal("accepted unowned residual call")
	}
	if calls.Load() != 0 || m.Active() {
		t.Fatal("invalid admission had effects")
	}
}
func TestOwnedOperationWaitsForMediaExitAfterCancellation(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	cleaned := make(chan struct{})
	done := make(chan *Call, 1)
	var count atomic.Int32
	m := operationManager(func() {
		if count.Add(1) == 1 {
			close(cleaned)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		c, e := m.RunMediaContext(ctx, Request{Number: "12345"}, "test", func(context.Context) error { close(entered); <-release; return nil })
		if e != nil && c == nil {
			done <- nil
		} else {
			done <- c
		}
	}()
	waitSignal(t, entered)
	cancel()
	waitSignal(t, cleaned)
	if !m.Active() {
		t.Error("released while driver alive")
	}
	select {
	case <-done:
		t.Error("reported completion while driver alive")
	default:
	}
	if _, e := m.StartMedia(Request{Number: "12345"}, "second", func(context.Context) error { return nil }); e == nil {
		t.Error("second call admitted while driver alive")
	}
	close(release)
	select {
	case c := <-done:
		if c == nil || c.State != StateCancelled {
			t.Fatalf("final=%+v", c)
		}
	case <-time.After(time.Second):
		t.Fatal("did not finish after driver exit")
	}
	if m.Active() || count.Load() != 1 {
		t.Fatal("cleanup count/active mismatch")
	}
}
func TestOwnedOperationCompletionWaitsForHangup(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan *Call, 1)
	m := operationManager(func() { close(entered); <-release })
	go func() {
		c, _ := m.RunMediaContext(context.Background(), Request{Number: "12345"}, "test", func(context.Context) error { return nil })
		done <- c
	}()
	waitSignal(t, entered)
	if !m.Active() {
		t.Error("active released during hangup")
	}
	select {
	case <-done:
		t.Error("finished during hangup")
	default:
	}
	close(release)
	select {
	case c := <-done:
		if c.State != StateCompleted {
			t.Fatal(c.State)
		}
	case <-time.After(time.Second):
		t.Fatal("not completed")
	}
	if m.Active() {
		t.Fatal("active not released")
	}
}

func TestOwnedCleanupFailureQuarantinesManager(t *testing.T) {
	var attempts atomic.Int32
	m := NewManager(Config{PollInterval: time.Millisecond, CommandRunner: func(c context.Context, n string, a ...string) (string, error) {
		switch n {
		case "dumpsys":
			if a[0] == "telephony.registry" {
				return "mVoiceRegState=0(IN_SERVICE)", nil
			}
			return "state: ACTIVE", nil
		case "am":
			return "", nil
		case "input":
			attempts.Add(1)
			if _, ok := c.Deadline(); !ok {
				t.Error("cleanup missing deadline")
			}
			return "", fmt.Errorf("synthetic cleanup failure")
		}
		return "", fmt.Errorf("unexpected fake command")
	}})
	final, err := m.RunMediaContext(context.Background(), Request{Number: "12345"}, "test", func(context.Context) error { return nil })
	if err == nil || final.State != StateFailed || !m.Active() {
		t.Fatalf("failure not quarantined: %+v %v active=%v", final, err, m.Active())
	}
	if _, err = m.StartMedia(Request{Number: "12345"}, "next", func(context.Context) error { return nil }); err == nil {
		t.Fatal("quarantined manager accepted call")
	}
	m.Cancel(final.ID)
	if attempts.Load() != 1 {
		t.Fatal("cancel retried cleanup")
	}
}
func TestOwnedParentDeadlineBeforeDialDoesNotReachCommands(t *testing.T) {
	var dial atomic.Int32
	m := NewManager(Config{CommandRunner: func(c context.Context, n string, a ...string) (string, error) {
		if n == "dumpsys" && a[0] == "telephony.registry" {
			<-c.Done()
			return "", c.Err()
		}
		dial.Add(1)
		return "", nil
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	final, err := m.RunMediaContext(ctx, Request{Number: "12345"}, "test", func(context.Context) error { t.Error("media called"); return nil })
	if err != context.DeadlineExceeded || final.State != StateCancelled || dial.Load() != 0 || m.Active() {
		t.Fatalf("deadline classification/effects: %+v %v commands=%d", final, err, dial.Load())
	}
}
func TestOwnedLateActiveAfterCancelDoesNotStartMedia(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var media, hangups atomic.Int32
	m := NewManager(Config{CommandRunner: func(c context.Context, n string, a ...string) (string, error) {
		switch n {
		case "dumpsys":
			if a[0] == "telephony.registry" {
				return "mVoiceRegState=0(IN_SERVICE)", nil
			}
			cancel()
			return "state: ACTIVE", nil
		case "am":
			return "", nil
		case "input":
			hangups.Add(1)
			if c.Err() != nil {
				t.Error("cleanup reused canceled request")
			}
			return "", nil
		}
		return "", fmt.Errorf("unexpected")
	}})
	final, err := m.RunMediaContext(ctx, Request{Number: "12345"}, "test", func(context.Context) error { media.Add(1); return nil })
	if err != context.Canceled || final.State != StateCancelled || media.Load() != 0 || hangups.Load() != 1 {
		t.Fatalf("late active: %+v %v media=%d hangups=%d", final, err, media.Load(), hangups.Load())
	}
}
func TestOwnedCancellationDuringCleanupIsNotReportedAsSuccess(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan *Call, 1)
	errs := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := operationManager(func() { close(entered); <-release })
	go func() {
		c, e := m.RunMediaContext(ctx, Request{Number: "12345"}, "test", func(context.Context) error { return nil })
		done <- c
		errs <- e
	}()
	waitSignal(t, entered)
	cancel()
	close(release)
	select {
	case c := <-done:
		if c.State != StateCancelled {
			t.Fatal(c.State)
		}
	case <-time.After(time.Second):
		t.Fatal("not completed")
	}
	if e := <-errs; e != context.Canceled {
		t.Fatalf("error=%v", e)
	}
	if m.Active() {
		t.Fatal("successful cleanup should release slot")
	}
}
func TestOwnedLateSuccessDoesNotMaskPhaseDeadline(t *testing.T) {
	for _, phase := range []string{"answer", "media"} {
		t.Run(phase, func(t *testing.T) {
			var mediaCalls atomic.Int32
			m := NewManager(Config{PollInterval: time.Millisecond, CommandRunner: func(c context.Context, n string, a ...string) (string, error) {
				switch n {
				case "dumpsys":
					if a[0] == "telephony.registry" {
						return "mVoiceRegState=0(IN_SERVICE)", nil
					}
					if phase == "answer" {
						<-c.Done()
					}
					return "state: ACTIVE", nil
				case "am", "input":
					return "", nil
				}
				return "", fmt.Errorf("unexpected")
			}})
			call := &Call{ID: "synthetic", Number: "12345", State: StateDialing}
			m.calls[call.ID] = call
			m.active = call.ID
			m.runOwned(context.Background(), call, func(c context.Context) error { mediaCalls.Add(1); <-c.Done(); return nil }, 20*time.Millisecond, 20*time.Millisecond)
			if call.State != StateFailed {
				t.Fatalf("late success masked %s deadline: %+v", phase, call)
			}
			if phase == "answer" && mediaCalls.Load() != 0 {
				t.Fatal("media started after answer deadline")
			}
		})
	}
}
