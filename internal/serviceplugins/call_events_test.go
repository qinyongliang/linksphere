package serviceplugins

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"unsafe"
)

func eventFixture(t *testing.T, capacity int) (*CallEventBuffer, *LocalDispatcher, *diagProvider, *diagAuthorizer) {
	t.Helper()
	binding := diagBinding()
	p := newDiagProvider("call")
	a := newDiagAuthorizer(binding)
	d, e := newLocalDispatcher(func() Environment { return Environment{goos: "android", uid: 0} }, binding, p, a)
	if e != nil {
		t.Fatal(e)
	}
	b, e := newCallEventBuffer(d, capacity)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { b.Close(); d.Close() })
	return b, d, p, a
}
func (p *diagProvider) EventCapabilities() []string { return []string{"call"} }

func observedEvent() CallEvent { return CallEvent{CallID: "call_123", State: "playing", UpdatedAt: 1} }
func TestCallEventsBoundedGapCursorAndOwnedCopies(t *testing.T) {
	b, _, _, _ := eventFixture(t, 2)
	for i := 0; i < 5; i++ {
		if e := b.Append(diagBinding(), observedEvent()); e != nil {
			t.Fatal(e)
		}
	}
	page, e := b.Read(diagContext("actor-1"), diagBinding(), CallEventCursor{}, 1)
	if e != nil {
		t.Fatal(e)
	}
	if page.Lost != 3 || len(page.Events) != 1 || page.Events[0].Sequence != 4 || page.Next.Sequence != 4 {
		t.Fatalf("bad page %+v", page)
	}
	page.Events[0].State = "mutated"
	next, e := b.Read(diagContext("actor-1"), diagBinding(), page.Next, 2)
	if e != nil || len(next.Events) != 1 || next.Events[0].Sequence != 5 || next.Events[0].State != "playing" {
		t.Fatalf("bad next %+v %v", next, e)
	}
	for _, cursor := range []CallEventCursor{{StreamID: page.Next.StreamID, Sequence: 6}, {StreamID: "other", Sequence: 4}, {Sequence: 1}} {
		if _, e = b.Read(diagContext("actor-1"), diagBinding(), cursor, 1); e == nil {
			t.Fatal("invalid cursor accepted")
		}
	}
	other, _, _, _ := eventFixture(t, 2)
	if _, e = other.Read(diagContext("actor-1"), diagBinding(), page.Next, 1); e == nil {
		t.Fatal("old stream cursor accepted")
	}
	raw, _ := json.Marshal(page)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	if len(fields) != 3 {
		t.Fatal("unexpected page fields")
	}
	var eventFields map[string]json.RawMessage
	data, _ := json.Marshal(page.Events[0])
	_ = json.Unmarshal(data, &eventFields)
	if len(eventFields) != 4 {
		t.Fatal("unexpected event fields")
	}
}
func TestCallEventsBusyReadDoesNotAdmitSecondCall(t *testing.T) {
	b, d, p, a := eventFixture(t, 2)
	_ = b.Append(diagBinding(), observedEvent())
	p.mu.Lock()
	p.capabilities = nil
	p.mu.Unlock()
	if _, e := b.Read(diagContext("actor-1"), diagBinding(), CallEventCursor{}, 2); e != nil {
		t.Fatal(e)
	}
	if e := d.Check(diagContext("actor-1"), diagBinding(), "call"); e == nil {
		t.Fatal("busy command admitted")
	}
	a.setRevoked(true)
	if page, e := b.Read(diagContext("actor-1"), diagBinding(), CallEventCursor{}, 2); e == nil || len(page.Events) != 0 {
		t.Fatal("revoked read exposed data")
	}
}
func TestCallEventsBindingsCancellationAndClose(t *testing.T) {
	b, d, _, _ := eventFixture(t, 2)
	_ = b.Append(diagBinding(), observedEvent())
	for _, change := range []func(*DispatchBinding){func(v *DispatchBinding) { v.ActorID = "other" }, func(v *DispatchBinding) { v.TargetID = "other" }, func(v *DispatchBinding) { v.ProviderID = "other" }, func(v *DispatchBinding) { v.SessionID = "other" }, func(v *DispatchBinding) { v.Generation++ }} {
		v := diagBinding()
		change(&v)
		if b.Append(v, observedEvent()) == nil {
			t.Fatal("wrong publisher binding")
		}
		if _, e := b.Read(diagContext("actor-1"), v, CallEventCursor{}, 2); e == nil {
			t.Fatal("wrong reader binding")
		}
	}
	if _, e := b.Read(context.Background(), diagBinding(), CallEventCursor{}, 2); e == nil {
		t.Fatal("strings alone authorized")
	}
	ctx, cancel := context.WithCancel(diagContext("actor-1"))
	cancel()
	if _, e := b.Read(ctx, diagBinding(), CallEventCursor{}, 2); e == nil {
		t.Fatal("cancelled read")
	}
	d.Close()
	if b.Append(diagBinding(), observedEvent()) == nil {
		t.Fatal("append after session close")
	}
	if _, e := b.Read(diagContext("actor-1"), diagBinding(), CallEventCursor{}, 2); e == nil {
		t.Fatal("read after session close")
	}
	b.Close()
	if len(b.entries) != 0 {
		t.Fatal("not cleared")
	}
	if b.Append(diagBinding(), observedEvent()) == nil {
		t.Fatal("append after buffer close")
	}
}

type eventAuthorizerFunc func(context.Context, DispatchBinding, string) error

func (f eventAuthorizerFunc) Authorize(c context.Context, b DispatchBinding, o string) error {
	return f(c, b, o)
}
func TestCallEventsFinalAuthorizationRecheck(t *testing.T) {
	b, d, _, _ := eventFixture(t, 2)
	_ = b.Append(diagBinding(), observedEvent())
	var count atomic.Int32
	d.authorizer = eventAuthorizerFunc(func(context.Context, DispatchBinding, string) error {
		if count.Add(1) == 2 {
			return errors.New("revoked")
		}
		return nil
	})
	page, e := b.Read(diagContext("actor-1"), diagBinding(), CallEventCursor{}, 2)
	if e == nil || len(page.Events) != 0 || count.Load() != 2 {
		t.Fatalf("missing final check %+v %v", page, e)
	}
}
func TestCallEventsInvalidInputAndOverflow(t *testing.T) {
	b, _, _, _ := eventFixture(t, 2)
	for _, e := range []CallEvent{{CallID: "../path", State: "playing", UpdatedAt: 1}, {CallID: "ok", State: "raw error", UpdatedAt: 1}, {CallID: "ok", State: "playing"}, {Sequence: 1, CallID: "ok", State: "playing", UpdatedAt: 1}} {
		if b.Append(diagBinding(), e) == nil {
			t.Fatal("bad event")
		}
	}
	b.mu.Lock()
	b.sequence = math.MaxUint64
	b.mu.Unlock()
	if b.Append(diagBinding(), observedEvent()) == nil {
		t.Fatal("wrapped sequence")
	}
	var d LocalDispatcher
	if d.CheckEvents(context.Background(), diagBinding(), "call") == nil {
		t.Fatal("zero dispatcher")
	}
	for _, limit := range []int{0, 129} {
		if _, e := b.Read(diagContext("actor-1"), diagBinding(), CallEventCursor{}, limit); e == nil {
			t.Fatal("invalid limit")
		}
	}
}
func TestCallEventsConcurrentAppendReadAndClose(t *testing.T) {
	b, _, _, _ := eventFixture(t, CallEventCapacity)
	var workers sync.WaitGroup
	for i := 0; i < 4; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := 0; j < 200; j++ {
				_ = b.Append(diagBinding(), observedEvent())
				page, e := b.Read(diagContext("actor-1"), diagBinding(), CallEventCursor{}, 128)
				if e == nil {
					for k := 1; k < len(page.Events); k++ {
						if page.Events[k].Sequence != page.Events[k-1].Sequence+1 {
							t.Error("nonconsecutive snapshot")
						}
					}
				}
			}
		}()
	}
	workers.Add(1)
	go func() { defer workers.Done(); b.Close() }()
	workers.Wait()
}

func TestCallEventsOwnShortStringStorage(t *testing.T) {
	b, _, _, _ := eventFixture(t, 2)
	backing := strings.Repeat("x", 1024*1024) + "call_123playing"
	id := backing[len(backing)-15 : len(backing)-7]
	state := backing[len(backing)-7:]
	if e := b.Append(diagBinding(), CallEvent{CallID: id, State: state, UpdatedAt: 1}); e != nil {
		t.Fatal(e)
	}
	page, e := b.Read(diagContext("actor-1"), diagBinding(), CallEventCursor{}, 2)
	if e != nil {
		t.Fatal(e)
	}
	got := page.Events[0]
	if got.CallID != id || got.State != state {
		t.Fatal("value changed")
	}
	if unsafe.StringData(got.CallID) == unsafe.StringData(id) || unsafe.StringData(got.State) == unsafe.StringData(state) {
		t.Fatal("ring retained producer substring storage")
	}
}
