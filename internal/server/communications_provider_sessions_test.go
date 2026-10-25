package server

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type providerSessionConnectionFixture struct {
	done chan struct{}
	once sync.Once
}

func newProviderSessionConnectionFixture() *providerSessionConnectionFixture {
	return &providerSessionConnectionFixture{done: make(chan struct{})}
}
func (c *providerSessionConnectionFixture) CloseChan() <-chan struct{} { return c.done }
func (c *providerSessionConnectionFixture) close()                     { c.once.Do(func() { close(c.done) }) }

type providerSessionVerifierFixture func(context.Context, communicationsProviderCandidate) error

func (f providerSessionVerifierFixture) verifyProvider(ctx context.Context, c communicationsProviderCandidate) error {
	return f(ctx, c)
}
func providerSessionManagerFixture(t *testing.T, v communicationsProviderVerifier, max int) *communicationsProviderSessionManager {
	t.Helper()
	m, e := newCommunicationsProviderSessionManager(v, max)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(m.close)
	return m
}
func providerSessionAllow(context.Context, communicationsProviderCandidate) error { return nil }
func providerSessionCandidateFixture(c communicationsProviderConnection) communicationsProviderCandidate {
	return communicationsProviderCandidate{agentID: "agent-fixture", targetID: "target-fixture", providerID: "provider-fixture", connection: c}
}
func providerSessionContextFixture(actor string) context.Context {
	return context.WithValue(context.Background(), communicationsPrincipalKey{}, communicationsPrincipal{actorID: actor, tokenID: "token-fixture", tokenHash: [32]byte{1}})
}
func providerSessionAwait[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(2 * time.Second):
		t.Fatal("fixture timed out")
		var zero T
		return zero
	}
}
func providerSessionRegisterFixture(t *testing.T, m *communicationsProviderSessionManager, c communicationsProviderCandidate) communicationsProviderRegistration {
	t.Helper()
	r, e := m.register(context.Background(), c)
	if e != nil {
		t.Fatal(e)
	}
	return r
}

func TestCommunicationsProviderSessionsDefaultDeny(t *testing.T) {
	for _, v := range []communicationsProviderVerifier{nil, providerSessionVerifierFixture(nil)} {
		m := providerSessionManagerFixture(t, v, 2)
		if _, e := m.register(context.Background(), providerSessionCandidateFixture(newProviderSessionConnectionFixture())); !errors.Is(e, errCommunicationsSessionDenied) {
			t.Fatalf("missing verifier accepted: %v", e)
		}
	}
	if _, e := newCommunicationsProviderSessionManager(nil, 0); e == nil {
		t.Fatal("zero capacity accepted")
	}
}
func TestCommunicationsProviderSessionsInvalidInputsNeverVerify(t *testing.T) {
	var calls atomic.Int32
	v := providerSessionVerifierFixture(func(context.Context, communicationsProviderCandidate) error { calls.Add(1); return nil })
	for _, kind := range []string{"agent", "target", "provider", "agent-utf8", "target-utf8", "provider-utf8", "long", "nil-connection", "typed-nil", "nil-channel", "closed", "cancelled", "nil-context", "uninitialized"} {
		t.Run(kind, func(t *testing.T) {
			m := providerSessionManagerFixture(t, v, 2)
			c := providerSessionCandidateFixture(newProviderSessionConnectionFixture())
			ctx := context.Background()
			switch kind {
			case "agent":
				c.agentID = ""
			case "target":
				c.targetID = " bad"
			case "provider":
				c.providerID = "bad\n"
			case "agent-utf8":
				c.agentID = "bad\xff"
			case "target-utf8":
				c.targetID = "bad\xff"
			case "provider-utf8":
				c.providerID = "bad\xff"
			case "long":
				c.providerID = strings.Repeat("x", 257)
			case "nil-connection":
				c.connection = nil
			case "typed-nil":
				c.connection = (*providerSessionConnectionFixture)(nil)
			case "nil-channel":
				c.connection = &providerSessionConnectionFixture{}
			case "closed":
				c.connection.(*providerSessionConnectionFixture).close()
			case "cancelled":
				x, cancel := context.WithCancel(ctx)
				cancel()
				ctx = x
			case "nil-context":
				ctx = nil
			case "uninitialized":
				m.pending = nil
			}
			if _, e := m.register(ctx, c); !errors.Is(e, errCommunicationsSessionDenied) {
				t.Fatalf("accepted invalid %s", kind)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("verifier called %d times", calls.Load())
	}
}
func TestCommunicationsProviderSessionsPrincipalAndPin(t *testing.T) {
	m := providerSessionManagerFixture(t, providerSessionVerifierFixture(providerSessionAllow), 2)
	c := newProviderSessionConnectionFixture()
	registration := providerSessionRegisterFixture(t, m, providerSessionCandidateFixture(c))
	if registration.identity.SessionID == "" || registration.identity.Generation == 0 {
		t.Fatal("server identity missing")
	}
	if _, e := m.current(context.Background(), "target-fixture", "provider-fixture"); e == nil {
		t.Fatal("anonymous current accepted")
	}
	for _, actor := range []string{"alice", "bob"} {
		ctx := providerSessionContextFixture(actor)
		current, e := m.current(ctx, "target-fixture", "provider-fixture")
		if e != nil || current.Binding.ActorID != actor || current.AgentID != "agent-fixture" {
			t.Fatalf("wrong principal snapshot: %+v %v", current, e)
		}
		pinned, life, e := m.pin(ctx, current.Binding, current.AgentID)
		if e != nil || pinned != c || life.Err() != nil {
			t.Fatal("pin not live/exact")
		}
		if _, _, e = m.pin(providerSessionContextFixture("other"), current.Binding, current.AgentID); e == nil {
			t.Fatal("cross-actor binding accepted")
		}
	}
}
func TestCommunicationsProviderSessionsReplacementPinsAndLateClose(t *testing.T) {
	m := providerSessionManagerFixture(t, providerSessionVerifierFixture(providerSessionAllow), 1)
	oldConn := newProviderSessionConnectionFixture()
	old := providerSessionRegisterFixture(t, m, providerSessionCandidateFixture(oldConn))
	ctx := providerSessionContextFixture("actor")
	current, _ := m.current(ctx, "target-fixture", "provider-fixture")
	_, oldLife, _ := m.pin(ctx, current.Binding, current.AgentID)
	m.mu.Lock()
	oldEntry := m.entries[communicationsProviderScope{"target-fixture", "provider-fixture"}]
	m.mu.Unlock()
	nextConn := newProviderSessionConnectionFixture()
	next := providerSessionRegisterFixture(t, m, providerSessionCandidateFixture(nextConn))
	if next.identity.Generation <= old.identity.Generation || next.identity.SessionID == old.identity.SessionID {
		t.Fatal("server generation not advanced")
	}
	if oldLife.Err() == nil {
		t.Fatal("old binding not cancelled")
	}
	if _, _, e := m.pin(ctx, current.Binding, current.AgentID); e == nil {
		t.Fatal("old binding survived replacement")
	}
	oldConn.close()
	m.remove(oldEntry)
	fresh, e := m.current(ctx, "target-fixture", "provider-fixture")
	if e != nil || fresh.Binding.SessionID != next.identity.SessionID {
		t.Fatal("late old cleanup removed replacement")
	}
	pinned, _, e := m.pin(ctx, fresh.Binding, fresh.AgentID)
	if e != nil || pinned != nextConn {
		t.Fatal("replacement not pinned")
	}
}
func TestCommunicationsProviderSessionsWrongBindingDenied(t *testing.T) {
	m := providerSessionManagerFixture(t, providerSessionVerifierFixture(providerSessionAllow), 2)
	providerSessionRegisterFixture(t, m, providerSessionCandidateFixture(newProviderSessionConnectionFixture()))
	ctx := providerSessionContextFixture("actor")
	current, _ := m.current(ctx, "target-fixture", "provider-fixture")
	for _, kind := range []string{"target", "provider", "session", "generation", "actor", "agent"} {
		b := current.Binding
		agent := current.AgentID
		switch kind {
		case "target":
			b.TargetID = "other"
		case "provider":
			b.ProviderID = "other"
		case "session":
			b.SessionID = "other"
		case "generation":
			b.Generation++
		case "actor":
			b.ActorID = "other"
		case "agent":
			agent = "other"
		}
		if _, _, e := m.pin(ctx, b, agent); e == nil {
			t.Fatalf("accepted wrong %s", kind)
		}
	}
}
func TestCommunicationsProviderSessionsRevokeDoesNotCloseAgentConnection(t *testing.T) {
	m := providerSessionManagerFixture(t, providerSessionVerifierFixture(providerSessionAllow), 2)
	c := newProviderSessionConnectionFixture()
	providerSessionRegisterFixture(t, m, providerSessionCandidateFixture(c))
	ctx := providerSessionContextFixture("actor")
	current, _ := m.current(ctx, "target-fixture", "provider-fixture")
	_, life, _ := m.pin(ctx, current.Binding, current.AgentID)
	if !m.revoke("target-fixture", "provider-fixture") || life.Err() == nil {
		t.Fatal("revoke failed")
	}
	if communicationsChannelClosed(c.done) {
		t.Fatal("ordinary agent connection was closed")
	}
	if _, e := m.current(ctx, "target-fixture", "provider-fixture"); e == nil {
		t.Fatal("revoked session current")
	}
}
func TestCommunicationsProviderSessionsCloseCancelsOnlyOwnedBindings(t *testing.T) {
	m := providerSessionManagerFixture(t, providerSessionVerifierFixture(providerSessionAllow), 2)
	c := newProviderSessionConnectionFixture()
	providerSessionRegisterFixture(t, m, providerSessionCandidateFixture(c))
	ctx := providerSessionContextFixture("actor")
	current, _ := m.current(ctx, "target-fixture", "provider-fixture")
	_, life, _ := m.pin(ctx, current.Binding, current.AgentID)
	m.close()
	m.close()
	if life.Err() == nil || communicationsChannelClosed(c.done) {
		t.Fatal("close ownership mismatch")
	}
	if _, e := m.register(context.Background(), providerSessionCandidateFixture(c)); e == nil {
		t.Fatal("register after close")
	}
}
func TestCommunicationsProviderSessionsConnectionCloseInvalidatesImmediately(t *testing.T) {
	m := providerSessionManagerFixture(t, providerSessionVerifierFixture(providerSessionAllow), 2)
	c := newProviderSessionConnectionFixture()
	providerSessionRegisterFixture(t, m, providerSessionCandidateFixture(c))
	c.close()
	if _, e := m.current(providerSessionContextFixture("actor"), "target-fixture", "provider-fixture"); e == nil {
		t.Fatal("closed connection remains current")
	}
}
func TestCommunicationsProviderSessionsCallerLifetimeDoesNotOwnRegistration(t *testing.T) {
	m := providerSessionManagerFixture(t, providerSessionVerifierFixture(providerSessionAllow), 2)
	ctx, cancel := context.WithCancel(context.Background())
	if _, e := m.register(ctx, providerSessionCandidateFixture(newProviderSessionConnectionFixture())); e != nil {
		t.Fatal(e)
	}
	cancel()
	if _, e := m.current(providerSessionContextFixture("actor"), "target-fixture", "provider-fixture"); e != nil {
		t.Fatal("ended registration request killed live binding")
	}
}
func TestCommunicationsProviderSessionsFailedReplacementKeepsPrevious(t *testing.T) {
	var deny atomic.Bool
	m := providerSessionManagerFixture(t, providerSessionVerifierFixture(func(context.Context, communicationsProviderCandidate) error {
		if deny.Load() {
			return errors.New("fixture denial")
		}
		return nil
	}), 2)
	old := providerSessionRegisterFixture(t, m, providerSessionCandidateFixture(newProviderSessionConnectionFixture()))
	deny.Store(true)
	if _, e := m.register(context.Background(), providerSessionCandidateFixture(newProviderSessionConnectionFixture())); e == nil {
		t.Fatal("denied verifier accepted")
	}
	current, e := m.current(providerSessionContextFixture("actor"), "target-fixture", "provider-fixture")
	if e != nil || current.Binding.SessionID != old.identity.SessionID {
		t.Fatal("failed replacement lost previous binding")
	}
}
func TestCommunicationsProviderSessionsAgentRebindRequiresRevoke(t *testing.T) {
	m := providerSessionManagerFixture(t, providerSessionVerifierFixture(providerSessionAllow), 2)
	providerSessionRegisterFixture(t, m, providerSessionCandidateFixture(newProviderSessionConnectionFixture()))
	c := providerSessionCandidateFixture(newProviderSessionConnectionFixture())
	c.agentID = "other-agent"
	if _, e := m.register(context.Background(), c); e == nil {
		t.Fatal("silent agent reassociation")
	}
	m.revoke(c.targetID, c.providerID)
	providerSessionRegisterFixture(t, m, c)
}
func TestCommunicationsProviderSessionsCapacityAndReplacement(t *testing.T) {
	m := providerSessionManagerFixture(t, providerSessionVerifierFixture(providerSessionAllow), 1)
	providerSessionRegisterFixture(t, m, providerSessionCandidateFixture(newProviderSessionConnectionFixture()))
	c := providerSessionCandidateFixture(newProviderSessionConnectionFixture())
	c.targetID = "other-target"
	if _, e := m.register(context.Background(), c); e == nil {
		t.Fatal("capacity exceeded")
	}
	providerSessionRegisterFixture(t, m, providerSessionCandidateFixture(newProviderSessionConnectionFixture()))
}
func TestCommunicationsProviderSessionsPendingRevokeRejectsLateVerification(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	v := providerSessionVerifierFixture(func(context.Context, communicationsProviderCandidate) error {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		return nil
	})
	m := providerSessionManagerFixture(t, v, 2)
	result := make(chan error, 1)
	go func() {
		_, e := m.register(context.Background(), providerSessionCandidateFixture(newProviderSessionConnectionFixture()))
		result <- e
	}()
	providerSessionAwait(t, started)
	if !m.revoke("target-fixture", "provider-fixture") {
		t.Fatal("pending revoke missing")
	}
	fresh := providerSessionRegisterFixture(t, m, providerSessionCandidateFixture(newProviderSessionConnectionFixture()))
	close(release)
	if e := providerSessionAwait(t, result); !errors.Is(e, errCommunicationsSessionDenied) {
		t.Fatal("late verifier committed")
	}
	current, e := m.current(providerSessionContextFixture("actor"), "target-fixture", "provider-fixture")
	if e != nil || current.Binding.SessionID != fresh.identity.SessionID {
		t.Fatal("late verifier changed replacement")
	}
}
func TestCommunicationsProviderSessionsPendingBoundAndClose(t *testing.T) {
	started := make(chan struct{})
	v := providerSessionVerifierFixture(func(ctx context.Context, _ communicationsProviderCandidate) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	m := providerSessionManagerFixture(t, v, 1)
	result := make(chan error, 1)
	go func() {
		_, e := m.register(context.Background(), providerSessionCandidateFixture(newProviderSessionConnectionFixture()))
		result <- e
	}()
	providerSessionAwait(t, started)
	c := providerSessionCandidateFixture(newProviderSessionConnectionFixture())
	c.targetID = "other-target"
	if _, e := m.register(context.Background(), c); e == nil {
		t.Fatal("pending work exceeded limit")
	}
	m.close()
	if e := providerSessionAwait(t, result); !errors.Is(e, errCommunicationsSessionDenied) {
		t.Fatal("pending close accepted")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.activeVerifications != 0 || len(m.pending) != 0 || len(m.entries) != 0 {
		t.Fatal("verification bookkeeping retained")
	}
}
func TestCommunicationsProviderSessionsEntropyFailureAndGenerationWrapDeny(t *testing.T) {
	for _, kind := range []string{"entropy", "invalid-id", "utf8-id", "wrap"} {
		t.Run(kind, func(t *testing.T) {
			m := providerSessionManagerFixture(t, providerSessionVerifierFixture(providerSessionAllow), 1)
			switch kind {
			case "entropy":
				m.newID = func() (string, error) { return "", errors.New("entropy fixture") }
			case "invalid-id":
				m.newID = func() (string, error) { return " ", nil }
			case "utf8-id":
				m.newID = func() (string, error) { return "bad\xff", nil }
			case "wrap":
				m.generation = ^uint64(0)
			}
			if _, e := m.register(context.Background(), providerSessionCandidateFixture(newProviderSessionConnectionFixture())); e == nil {
				t.Fatal("invalid session identity accepted")
			}
			m.mu.Lock()
			defer m.mu.Unlock()
			if len(m.entries) != 0 || len(m.pending) != 0 || m.activeVerifications != 0 {
				t.Fatal("failed registration retained")
			}
		})
	}
}
func TestCommunicationsProviderSessionsVerifierCallbackCanCloseManager(t *testing.T) {
	var m *communicationsProviderSessionManager
	v := providerSessionVerifierFixture(func(context.Context, communicationsProviderCandidate) error { m.close(); return nil })
	m = providerSessionManagerFixture(t, v, 1)
	result := make(chan error, 1)
	go func() {
		_, e := m.register(context.Background(), providerSessionCandidateFixture(newProviderSessionConnectionFixture()))
		result <- e
	}()
	if e := providerSessionAwait(t, result); !errors.Is(e, errCommunicationsSessionDenied) {
		t.Fatal("closed manager accepted verifier")
	}
}

func TestCommunicationsProviderSessionsDisconnectedVerificationCancels(t *testing.T) {
	started := make(chan struct{})
	v := providerSessionVerifierFixture(func(ctx context.Context, _ communicationsProviderCandidate) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	m := providerSessionManagerFixture(t, v, 1)
	c := newProviderSessionConnectionFixture()
	result := make(chan error, 1)
	go func() { _, e := m.register(context.Background(), providerSessionCandidateFixture(c)); result <- e }()
	providerSessionAwait(t, started)
	c.close()
	if e := providerSessionAwait(t, result); !errors.Is(e, errCommunicationsSessionDenied) {
		t.Fatal("disconnected verification committed")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.activeVerifications != 0 || len(m.pending) != 0 {
		t.Fatal("verification not released after return")
	}
}
func TestCommunicationsProviderSessionsIgnoredCancellationKeepsCapacity(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	v := providerSessionVerifierFixture(func(context.Context, communicationsProviderCandidate) error {
		calls.Add(1)
		close(started)
		<-release
		return nil
	})
	m := providerSessionManagerFixture(t, v, 1)
	result := make(chan error, 1)
	go func() {
		_, e := m.register(context.Background(), providerSessionCandidateFixture(newProviderSessionConnectionFixture()))
		result <- e
	}()
	providerSessionAwait(t, started)
	m.revoke("target-fixture", "provider-fixture")
	if _, e := m.register(context.Background(), providerSessionCandidateFixture(newProviderSessionConnectionFixture())); e == nil {
		t.Fatal("cancelled but still running verifier freed capacity")
	}
	if calls.Load() != 1 {
		t.Fatal("extra verifier invoked")
	}
	close(release)
	if e := providerSessionAwait(t, result); !errors.Is(e, errCommunicationsSessionDenied) {
		t.Fatal("late cancelled verifier accepted")
	}
}
func TestCommunicationsProviderSessionsReplacementCancelsBeforePublication(t *testing.T) {
	m := providerSessionManagerFixture(t, providerSessionVerifierFixture(providerSessionAllow), 1)
	providerSessionRegisterFixture(t, m, providerSessionCandidateFixture(newProviderSessionConnectionFixture()))
	scope := communicationsProviderScope{"target-fixture", "provider-fixture"}
	m.mu.Lock()
	old := m.entries[scope]
	originalCancel := old.cancel
	var once sync.Once
	var ordered atomic.Bool
	old.cancel = func() { once.Do(func() { ordered.Store(m.entries[scope] == old) }); originalCancel() }
	m.mu.Unlock()
	providerSessionRegisterFixture(t, m, providerSessionCandidateFixture(newProviderSessionConnectionFixture()))
	if !ordered.Load() {
		t.Fatal("replacement published before old lifetime cancellation")
	}
}

type providerSessionErrContextFixture struct {
	context.Context
	calls   atomic.Int32
	onThird func()
}

func (c *providerSessionErrContextFixture) Err() error {
	if c.calls.Add(1) == 3 {
		c.onThird()
		return context.Canceled
	}
	return nil
}
func TestCommunicationsProviderSessionsCallerContextCallbackOutsidePinLock(t *testing.T) {
	m := providerSessionManagerFixture(t, providerSessionVerifierFixture(providerSessionAllow), 1)
	providerSessionRegisterFixture(t, m, providerSessionCandidateFixture(newProviderSessionConnectionFixture()))
	base := providerSessionContextFixture("actor")
	current, e := m.current(base, "target-fixture", "provider-fixture")
	if e != nil {
		t.Fatal(e)
	}
	ctx := &providerSessionErrContextFixture{Context: base, onThird: m.close}
	result := make(chan error, 1)
	go func() { _, _, e := m.pin(ctx, current.Binding, current.AgentID); result <- e }()
	if e := providerSessionAwait(t, result); !errors.Is(e, errCommunicationsSessionDenied) {
		t.Fatal("cancelled caller context accepted")
	}
}

func TestCommunicationsProviderSessionsUnicodeAndInvalidPrincipal(t *testing.T) {
	m := providerSessionManagerFixture(t, providerSessionVerifierFixture(providerSessionAllow), 1)
	c := providerSessionCandidateFixture(newProviderSessionConnectionFixture())
	c.agentID, c.targetID, c.providerID = "设备", "目标", "服务"
	original := providerSessionRegisterFixture(t, m, c)
	valid := providerSessionContextFixture("用户")
	current, err := m.current(valid, c.targetID, c.providerID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.pin(valid, current.Binding, c.agentID); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"actor", "token"} {
		t.Run(field, func(t *testing.T) {
			p := communicationsPrincipal{actorID: "用户", tokenID: "token-fixture", tokenHash: [32]byte{1}}
			if field == "actor" {
				p.actorID = "bad\xff"
			} else {
				p.tokenID = "bad\xff"
			}
			ctx := context.WithValue(context.Background(), communicationsPrincipalKey{}, p)
			if _, err := m.current(ctx, c.targetID, c.providerID); err == nil {
				t.Fatal("invalid principal accepted")
			}
			if _, _, err := m.pin(ctx, current.Binding, c.agentID); err == nil {
				t.Fatal("invalid principal pinned")
			}
		})
	}
	m.newID = func() (string, error) { return "bad\xff", nil }
	replacement := c
	replacement.connection = newProviderSessionConnectionFixture()
	if _, err := m.register(context.Background(), replacement); err == nil {
		t.Fatal("invalid replacement session accepted")
	}
	after, err := m.current(valid, c.targetID, c.providerID)
	if err != nil || after.Binding.SessionID != original.identity.SessionID {
		t.Fatal("old binding replaced on invalid new identity")
	}
	pinned, life, err := m.pin(valid, current.Binding, c.agentID)
	if err != nil || pinned != c.connection || life.Err() != nil {
		t.Fatal("old connection invalidated")
	}
}
