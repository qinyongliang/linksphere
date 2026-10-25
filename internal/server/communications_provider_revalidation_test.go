package server

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/qinyongliang/linksphere/internal/store"
)

func TestCommunicationsRootSessionRechecksTarget(t *testing.T) {
	for _, change := range []string{"deleted", "agent", "organization", "owner-type", "type"} {
		t.Run(change, func(t *testing.T) {
			config, signer := rootConfigFixture(t)
			var changed atomic.Bool
			lookup := config.lookupTarget
			config.lookupTarget = func(ctx context.Context, id string) (store.SSHTarget, error) {
				target, err := lookup(ctx, id)
				if changed.Load() {
					switch change {
					case "deleted":
						return store.SSHTarget{}, errors.New("removed")
					case "agent":
						target.AgentID = "replacement-agent"
					case "organization":
						target.OwnerID = "another-organization"
					case "owner-type":
						target.OwnerType = "other"
					case "type":
						target.TargetType = "other"
					}
				}
				return target, err
			}
			a := rootAuthorityFixture(t, config)
			conn, _, err := rootHandshakeFixture(t, a, config, signer)
			if err != nil {
				t.Fatal(err)
			}
			m := providerSessionManagerFixture(t, a, 2)
			providerSessionRegisterFixture(t, m, rootCandidateFixture(conn))
			ctx := providerSessionContextFixture("actor")
			current, err := m.current(ctx, "target-fixture", "provider-fixture")
			if err != nil {
				t.Fatal(err)
			}
			_, lifetime, err := m.pin(ctx, current.Binding, current.AgentID)
			if err != nil {
				t.Fatal(err)
			}
			changed.Store(true)
			if _, _, err := m.pin(ctx, current.Binding, current.AgentID); err == nil {
				t.Fatal("changed target accepted")
			}
			if lifetime.Err() == nil {
				t.Fatal("denied binding remained live")
			}
			if communicationsChannelClosed(conn.CloseChan()) {
				t.Fatal("session denial closed dedicated transport")
			}
		})
	}
}

func TestCommunicationsSessionRevalidationCannotDeleteReplacement(t *testing.T) {
	for _, lateDenial := range []bool{true, false} {
		t.Run(map[bool]string{true: "denied", false: "allowed"}[lateDenial], func(t *testing.T) {
			old := newProviderSessionConnectionFixture()
			entered, release := make(chan struct{}), make(chan struct{})
			var block atomic.Bool
			v := providerSessionVerifierFixture(func(ctx context.Context, c communicationsProviderCandidate) error {
				if c.connection == old && block.Load() {
					close(entered)
					<-release
					if lateDenial {
						return errCommunicationsSessionDenied
					}
				}
				return nil
			})
			m := providerSessionManagerFixture(t, v, 2)
			providerSessionRegisterFixture(t, m, providerSessionCandidateFixture(old))
			block.Store(true)
			result := make(chan error, 1)
			go func() {
				_, err := m.current(providerSessionContextFixture("actor"), "target-fixture", "provider-fixture")
				result <- err
			}()
			providerSessionAwait(t, entered)
			fresh := providerSessionRegisterFixture(t, m, providerSessionCandidateFixture(newProviderSessionConnectionFixture()))
			close(release)
			if providerSessionAwait(t, result) == nil {
				t.Fatal("old validation survived replacement")
			}
			current, err := m.current(providerSessionContextFixture("actor"), "target-fixture", "provider-fixture")
			if err != nil || current.Binding.SessionID != fresh.identity.SessionID {
				t.Fatal("late denial removed replacement")
			}
		})
	}
}

func TestCommunicationsSessionRevalidationBoundAndCallerCancellation(t *testing.T) {
	entered := make(chan struct{})
	var block atomic.Bool
	v := providerSessionVerifierFixture(func(ctx context.Context, _ communicationsProviderCandidate) error {
		if block.Load() {
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	})
	m := providerSessionManagerFixture(t, v, 1)
	providerSessionRegisterFixture(t, m, providerSessionCandidateFixture(newProviderSessionConnectionFixture()))
	ctx, cancel := context.WithCancel(providerSessionContextFixture("actor"))
	defer cancel()
	block.Store(true)
	result := make(chan error, 1)
	go func() { _, err := m.current(ctx, "target-fixture", "provider-fixture"); result <- err }()
	providerSessionAwait(t, entered)
	if _, err := m.current(providerSessionContextFixture("actor"), "target-fixture", "provider-fixture"); err == nil {
		t.Fatal("validation budget exceeded")
	}
	if _, err := m.register(context.Background(), providerSessionCandidateFixture(newProviderSessionConnectionFixture())); err == nil {
		t.Fatal("register bypassed shared verification budget")
	}
	cancel()
	if providerSessionAwait(t, result) == nil {
		t.Fatal("cancelled read accepted")
	}
	block.Store(false)
	if _, err := m.current(providerSessionContextFixture("actor"), "target-fixture", "provider-fixture"); err != nil {
		t.Fatal("caller cancellation revoked healthy shared binding", err)
	}
}

func TestCommunicationsSessionRevalidationCancelledOnRevoke(t *testing.T) {
	entered := make(chan struct{})
	var block atomic.Bool
	v := providerSessionVerifierFixture(func(ctx context.Context, _ communicationsProviderCandidate) error {
		if block.Load() {
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	})
	m := providerSessionManagerFixture(t, v, 1)
	providerSessionRegisterFixture(t, m, providerSessionCandidateFixture(newProviderSessionConnectionFixture()))
	block.Store(true)
	result := make(chan error, 1)
	go func() {
		_, err := m.current(providerSessionContextFixture("actor"), "target-fixture", "provider-fixture")
		result <- err
	}()
	providerSessionAwait(t, entered)
	if !m.revoke("target-fixture", "provider-fixture") {
		t.Fatal("nothing revoked")
	}
	if providerSessionAwait(t, result) == nil {
		t.Fatal("revoked read accepted")
	}
}

func TestCommunicationsSessionUncooperativeRevalidationKeepsBudget(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var block atomic.Bool
	v := providerSessionVerifierFixture(func(context.Context, communicationsProviderCandidate) error {
		if block.Load() {
			close(entered)
			<-release
		}
		return nil
	})
	m := providerSessionManagerFixture(t, v, 1)
	providerSessionRegisterFixture(t, m, providerSessionCandidateFixture(newProviderSessionConnectionFixture()))
	block.Store(true)
	result := make(chan error, 1)
	go func() {
		_, err := m.current(providerSessionContextFixture("actor"), "target-fixture", "provider-fixture")
		result <- err
	}()
	providerSessionAwait(t, entered)
	m.revoke("target-fixture", "provider-fixture")
	if _, err := m.register(context.Background(), providerSessionCandidateFixture(newProviderSessionConnectionFixture())); err == nil {
		t.Fatal("running verifier stopped charging its budget")
	}
	block.Store(false)
	close(release)
	if providerSessionAwait(t, result) == nil {
		t.Fatal("revoked validation accepted")
	}
	providerSessionRegisterFixture(t, m, providerSessionCandidateFixture(newProviderSessionConnectionFixture()))
}

func TestCommunicationsSessionRevalidationCallbackCanCloseManager(t *testing.T) {
	var callback atomic.Bool
	var manager *communicationsProviderSessionManager
	v := providerSessionVerifierFixture(func(context.Context, communicationsProviderCandidate) error {
		if callback.Load() {
			manager.close()
		}
		return nil
	})
	manager = providerSessionManagerFixture(t, v, 1)
	providerSessionRegisterFixture(t, manager, providerSessionCandidateFixture(newProviderSessionConnectionFixture()))
	callback.Store(true)
	result := make(chan error, 1)
	go func() {
		_, err := manager.current(providerSessionContextFixture("actor"), "target-fixture", "provider-fixture")
		result <- err
	}()
	if providerSessionAwait(t, result) == nil {
		t.Fatal("closed manager accepted validation")
	}
}
