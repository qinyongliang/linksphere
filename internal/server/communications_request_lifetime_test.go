package server

import (
	"context"
	"testing"
	"time"
)

func TestCommunicationsRequestLifetimeSurvivesSDKDetach(t *testing.T) {
	type unrelatedKey struct{}
	request, cancel := context.WithCancel(context.WithValue(context.Background(), unrelatedKey{}, "not-for-worker"))
	adapted, release := withCommunicationsRequestLifetime(request)
	defer release()
	detached := context.WithoutCancel(adapted) // Same relevant cancellation behavior as the SDK's notDone wrapper.
	principal := communicationsPrincipal{actorID: "actor", tokenID: "token"}
	call, _, finish := communicationsInvocationContext(detached, principal, time.Minute)
	defer finish()
	if call.Value(unrelatedKey{}) != nil {
		t.Fatal("worker retained unrelated request values")
	}
	if call.Value(communicationsRequestLifetimeKey{}) != nil {
		t.Fatal("worker retained HTTP lifetime context")
	}
	if got, _ := call.Value(communicationsPrincipalKey{}).(communicationsPrincipal); got != principal {
		t.Fatal("worker lost principal")
	}
	cancel()
	select {
	case <-call.Done():
		if call.Err() != context.Canceled {
			t.Fatal("wrong cancellation")
		}
	case <-time.After(time.Second):
		t.Fatal("request cancellation not bridged")
	}
	if detached.Err() != nil {
		t.Fatal("ordinary SDK semantics unexpectedly changed")
	}
}
func TestCommunicationsRequestLifetimePreservesEarlierDeadline(t *testing.T) {
	deadline := time.Now().Add(time.Minute)
	request, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	adapted, release := withCommunicationsRequestLifetime(request)
	defer release()
	call, _, finish := communicationsInvocationContext(context.WithoutCancel(adapted), communicationsPrincipal{}, 5*time.Minute)
	defer finish()
	if got, ok := call.Deadline(); !ok || !got.Equal(deadline) {
		t.Fatal("HTTP deadline was lost")
	}
	shorter, _, finishShort := communicationsInvocationContext(adapted, communicationsPrincipal{}, time.Second)
	defer finishShort()
	if got, _ := shorter.Deadline(); !got.Before(deadline) {
		t.Fatal("exchange maximum was extended")
	}
}
func TestCommunicationsRequestLifetimeAlreadyCanceledAndRelease(t *testing.T) {
	request, cancel := context.WithCancel(context.Background())
	cancel()
	adapted, release := withCommunicationsRequestLifetime(request)
	call, _, finish := communicationsInvocationContext(context.WithoutCancel(adapted), communicationsPrincipal{}, time.Minute)
	if call.Err() == nil {
		t.Fatal("already-canceled request allowed to start")
	}
	finish()
	release()
	release()
	adapted, release = withCommunicationsRequestLifetime(context.Background())
	call, _, finish = communicationsInvocationContext(context.WithoutCancel(adapted), communicationsPrincipal{}, time.Minute)
	defer finish()
	release()
	select {
	case <-call.Done():
	case <-time.After(time.Second):
		t.Fatal("HTTP return did not close communications lifetime")
	}
}
