package serviceplugins

import (
	"context"
	"encoding/json"
	"testing"
)

type publishingProvider struct {
	*diagProvider
	last func(CallEvent) error
}

func (p *publishingProvider) Invoke(ctx context.Context, operation string, args json.RawMessage) (json.RawMessage, error) {
	p.last = CallEventPublisher(ctx)
	if p.last != nil {
		if err := p.last(observedEvent()); err != nil {
			return nil, err
		}
	}
	return json.RawMessage(`{"ok":true}`), nil
}
func TestCallEventInvocationUsesAuthorizedBoundPublisherOnly(t *testing.T) {
	binding := diagBinding()
	p := &publishingProvider{diagProvider: newDiagProvider("call")}
	a := newDiagAuthorizer(binding)
	d, e := newLocalDispatcher(func() Environment { return Environment{goos: "android", uid: 0} }, binding, p, a)
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close()
	b, e := NewCallEventBuffer(d)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	ctx := diagContext(binding.ActorID)
	if _, e = d.Invoke(ctx, binding, "call", json.RawMessage(`{"action":"start"}`)); e != nil {
		t.Fatal(e)
	}
	if p.last != nil {
		t.Fatal("ordinary invocation got publisher")
	}
	if _, e = b.Invoke(context.Background(), binding, json.RawMessage(`{"action":"start"}`)); e == nil {
		t.Fatal("unauthenticated invoke")
	}
	if _, e = b.Invoke(ctx, binding, json.RawMessage(`{"action":"start","actor":"forged"}`)); e != nil {
		t.Fatal(e)
	}
	page, e := b.Read(ctx, binding, CallEventCursor{}, 128)
	if e != nil || len(page.Events) != 1 {
		t.Fatalf("page %+v err %v", page, e)
	}
	publish := p.last
	b.Close()
	if publish(observedEvent()) == nil {
		t.Fatal("late publisher resurrected buffer")
	}
	if _, e = b.Invoke(ctx, binding, json.RawMessage(`{}`)); e == nil {
		t.Fatal("closed buffer invoke")
	}
	if CallEventPublisher(nil) != nil || CallEventPublisher(context.Background()) != nil {
		t.Fatal("unbound publisher")
	}
}
