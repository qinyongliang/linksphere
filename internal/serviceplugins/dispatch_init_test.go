package serviceplugins

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestUninitializedDispatcherFailsClosed(t *testing.T) {
	for name, dispatcher := range map[string]*LocalDispatcher{"nil": nil, "zero": {}} {
		t.Run(name, func(t *testing.T) {
			for i := 0; i < 2; i++ {
				if err := dispatcher.Check(context.Background(), DispatchBinding{}, "call"); !errors.Is(err, ErrDispatchDenied) {
					t.Fatalf("check: %v", err)
				}
				if err := dispatcher.CheckEvents(context.Background(), DispatchBinding{}, "call"); !errors.Is(err, ErrDispatchDenied) {
					t.Fatalf("events: %v", err)
				}
				if result, err := dispatcher.Invoke(context.Background(), DispatchBinding{}, "call", []byte(`{}`)); !errors.Is(err, ErrDispatchDenied) || result != nil {
					t.Fatalf("invoke: %s %v", result, err)
				}
				dispatcher.Close()
			}
		})
	}
}

func TestZeroDispatcherConcurrentCloseAndInvoke(t *testing.T) {
	var dispatcher LocalDispatcher
	var workers sync.WaitGroup
	for i := 0; i < 16; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := 0; j < 100; j++ {
				dispatcher.Close()
				if _, err := dispatcher.Invoke(context.Background(), DispatchBinding{}, "call", []byte(`{}`)); !errors.Is(err, ErrDispatchDenied) {
					t.Errorf("unexpected admission: %v", err)
				}
			}
		}()
	}
	workers.Wait()
	if !dispatcher.closed.Load() {
		t.Fatal("zero dispatcher must remain closed")
	}
}
