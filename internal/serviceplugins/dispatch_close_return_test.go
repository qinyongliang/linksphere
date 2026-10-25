package serviceplugins

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

// Test-only scheduling control: the owned session is genuinely cancelled,
// while its registered AfterFunc callback is held before forwarding cancellation.
// Hiding Value prevents context from bypassing this adapter via its private key.
type delayedSessionAfterFunc struct {
	context.Context
	entered  chan struct{}
	release  chan struct{}
	finished chan struct{}
}

func (c *delayedSessionAfterFunc) Value(any) any { return nil }
func (c *delayedSessionAfterFunc) AfterFunc(f func()) func() bool {
	return context.AfterFunc(c.Context, func() {
		close(c.entered)
		<-c.release
		defer close(c.finished)
		f()
	})
}

type closingReturnProvider struct {
	*diagProvider
	invoke func(context.Context) (json.RawMessage, error)
}

func (p *closingReturnProvider) Invoke(ctx context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) {
	return p.invoke(ctx)
}
func TestDispatcherRejectsSuccessBeforeCloseCancellationCallbackRuns(t *testing.T) {
	binding := diagBinding()
	provider, auth := newDiagProvider("call"), newDiagAuthorizer(binding)
	d := newDiagDispatcher(t, binding, provider, auth)
	delayed := &delayedSessionAfterFunc{Context: d.session, entered: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
	d.session = delayed
	var release sync.Once
	t.Cleanup(func() {
		d.Close()
		release.Do(func() { close(delayed.release) })
		select {
		case <-delayed.finished:
			t.Log("controlled AfterFunc cleanup completed")
		case <-time.After(2 * time.Second):
			t.Error("controlled callback cleanup did not finish")
		}
	})
	d.provider = &closingReturnProvider{diagProvider: provider, invoke: func(ctx context.Context) (json.RawMessage, error) {
		d.Close()
		select {
		case <-delayed.entered:
		case <-time.After(2 * time.Second):
			t.Fatal("cancellation callback was not registered")
		}
		if !d.closed.Load() || d.session.Err() != context.Canceled {
			t.Fatal("session was not synchronously closed")
		}
		if ctx.Err() != nil {
			t.Fatal("controlled invocation cancellation was not delayed")
		}
		t.Log("closed flag and session cancellation visible; invocation callback still gated")
		return json.RawMessage(`{"fixture":"must-not-return"}`), nil
	}}
	result, err := d.Invoke(diagContext(binding.ActorID), binding, "call", json.RawMessage(`{}`))
	if !errors.Is(err, ErrDispatchDenied) || len(result) != 0 {
		t.Fatalf("closed dispatcher returned success: result=%s err=%v", result, err)
	}
}
