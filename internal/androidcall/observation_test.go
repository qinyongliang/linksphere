package call

import (
	"bytes"
	"context"
	"log"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestOwnedObservationsOrderedAndAfterCleanup(t *testing.T) {
	var cleaned atomic.Bool
	manager := operationManager(func() { cleaned.Store(true) })
	var events []CallObservation
	result, err := manager.RunMediaContextObserved(context.Background(), Request{Number: "12345"}, "asset", func(context.Context) error { return nil }, func(e CallObservation) {
		if !manager.Active() {
			t.Error("observer ran after busy release")
		}
		if e.State == StateCompleted && !cleaned.Load() {
			t.Error("terminal before cleanup")
		}
		events = append(events, e)
	})
	if err != nil {
		t.Fatal(err)
	}
	var states []State
	for _, e := range events {
		if e.ID != result.ID || e.UpdatedAt.IsZero() {
			t.Fatal("bad observation")
		}
		states = append(states, e.State)
	}
	if !reflect.DeepEqual(states, []State{StateDialing, StateWaitingForAnswer, StatePlaying, StateCompleted}) {
		t.Fatalf("states %v", states)
	}
	if reflect.TypeOf(CallObservation{}).NumField() != 3 {
		t.Fatal("unexpected observation payload")
	}
}
func TestOwnedObserverPanicCannotSkipCleanup(t *testing.T) {
	var cleaned atomic.Int32
	manager := operationManager(func() { cleaned.Add(1) })
	var output bytes.Buffer
	manager.config.Logger = log.New(&output, "", 0)
	result, err := manager.RunMediaContextObserved(context.Background(), Request{Number: "12345"}, "asset", func(context.Context) error { return nil }, func(CallObservation) { panic("untrusted error text must not be logged") })
	if strings.Contains(output.String(), "untrusted error text") {
		t.Fatal("observer panic leaked")
	}
	if err != nil || result.State != StateCompleted || cleaned.Load() != 1 || manager.Active() {
		t.Fatalf("observer changed lifecycle %v %v", result, err)
	}
}
func TestOwnedObserverIsOperationScopedAndWaitsForCancelledMedia(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	manager := operationManager(func() {})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var states []State
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = manager.RunMediaContextObserved(ctx, Request{Number: "12345"}, "asset", func(context.Context) error { close(entered); <-release; return nil }, func(e CallObservation) { mu.Lock(); states = append(states, e.State); mu.Unlock() })
	}()
	waitSignal(t, entered)
	cancel()
	mu.Lock()
	for _, s := range states {
		if isTerminal(s) {
			t.Error("terminal before media exit")
		}
	}
	mu.Unlock()
	if !manager.Active() {
		t.Fatal("busy released early")
	}
	close(release)
	waitSignal(t, done)
	mu.Lock()
	if states[len(states)-1] != StateCancelled {
		t.Error("missing cancelled terminal")
	}
	count := len(states)
	mu.Unlock()
	_, err := manager.RunMediaContext(context.Background(), Request{Number: "12345"}, "asset", func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(states) != count {
		t.Fatal("observer leaked into next call")
	}
}
