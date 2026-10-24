package call

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func TestCancelWorkerOwnsHangupAndKeepsActiveUntilCleanup(t *testing.T) {
	mediaEntered := make(chan struct{}, 1)
	hangupEntered := make(chan struct{}, 1)
	releaseHangup := make(chan struct{})
	secondMedia := make(chan struct{})
	defer close(secondMedia)
	var hangups atomic.Int32
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
			if hangups.Add(1) == 1 {
				hangupEntered <- struct{}{}
				<-releaseHangup
			}
			return "", nil
		}
		return "", fmt.Errorf("unexpected fake command")
	}})
	first, e := m.StartMedia(Request{Number: "12345"}, "first", func(c context.Context) error { mediaEntered <- struct{}{}; <-c.Done(); return c.Err() })
	if e != nil {
		t.Fatal(e)
	}
	select {
	case <-mediaEntered:
	case <-time.After(time.Second):
		t.Fatal("media not entered")
	}
	for i := 0; i < 3; i++ {
		if _, ok := m.Cancel(first.ID); !ok {
			t.Fatal("missing call")
		}
	}
	select {
	case <-hangupEntered:
	case <-time.After(time.Second):
		t.Fatal("hangup not entered")
	}
	if !m.Active() {
		t.Fatal("active released before cleanup")
	}
	if _, e = m.StartMedia(Request{Number: "12345"}, "blocked", func(context.Context) error { return nil }); e == nil {
		t.Fatal("second admitted before cleanup")
	}
	close(releaseHangup)
	deadline := time.Now().Add(time.Second)
	for m.Active() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if m.Active() {
		t.Fatal("cleanup did not finish")
	}
	final, _ := m.Get(first.ID)
	if final.State != StateCancelled {
		t.Fatalf("first state=%s", final.State)
	}
	second, e := m.StartMedia(Request{Number: "12345"}, "second", func(c context.Context) error {
		select {
		case <-secondMedia:
			return nil
		case <-c.Done():
			return c.Err()
		}
	})
	if e != nil {
		t.Fatal(e)
	}
	m.Cancel(first.ID)
	again, _ := m.Get(first.ID)
	if !again.EndedAt.Equal(*final.EndedAt) {
		t.Fatal("terminal timestamp changed")
	}
	if hangups.Load() != 1 {
		t.Fatal("old cancel issued another hangup")
	}
	m.Cancel(second.ID)
	deadline = time.Now().Add(time.Second)
	for m.Active() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if m.Active() {
		t.Fatal("second did not finish")
	}
}
