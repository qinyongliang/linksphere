package server

import (
	"context"
	"testing"
)

func TestCommunicationsRevalidationPanicReleasesAdmissionAndContext(t *testing.T) {
	panicNow := false
	var observed context.Context
	verifier := providerSessionVerifierFixture(func(ctx context.Context, _ communicationsProviderCandidate) error {
		if panicNow {
			observed = ctx
			panic("trusted verifier failure")
		}
		return nil
	})
	manager := providerSessionManagerFixture(t, verifier, 1)
	registration := providerSessionRegisterFixture(t, manager, providerSessionCandidateFixture(newProviderSessionConnectionFixture()))
	panicNow = true
	got := func() (value any) {
		defer func() { value = recover() }()
		_, _ = manager.current(providerSessionContextFixture("actor"), "target-fixture", "provider-fixture")
		return nil
	}()
	if got != "trusted verifier failure" {
		t.Fatalf("panic changed: %v", got)
	}
	if observed == nil || observed.Err() != context.Canceled {
		t.Fatal("failed verification context remained live")
	}
	manager.mu.Lock()
	active := manager.activeVerifications
	manager.mu.Unlock()
	if active != 0 {
		t.Fatalf("panic leaked %d verification slots", active)
	}
	panicNow = false
	current, err := manager.current(providerSessionContextFixture("actor"), "target-fixture", "provider-fixture")
	if err != nil || current.Binding.SessionID != registration.identity.SessionID {
		t.Fatal("host recovery could not reuse healthy session", err)
	}
	providerSessionRegisterFixture(t, manager, providerSessionCandidateFixture(newProviderSessionConnectionFixture()))
}
