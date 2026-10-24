package serviceplugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	diagArgLimit    = 64 * 1024
	diagResultLimit = 1024 * 1024
)

type diagPrincipalKey struct{}

func diagContext(actor string) context.Context {
	return context.WithValue(context.Background(), diagPrincipalKey{}, actor)
}

func diagBinding() DispatchBinding {
	return DispatchBinding{ActorID: "actor-1", TargetID: "target-1", ProviderID: "provider-1", SessionID: "session-1", Generation: 1}
}

type diagEnvironment struct {
	mu  sync.RWMutex
	os  string
	uid int
}

func (e *diagEnvironment) get() Environment {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return Environment{goos: e.os, uid: e.uid}
}
func (e *diagEnvironment) set(goos string, uid int) {
	e.mu.Lock()
	e.os, e.uid = goos, uid
	e.mu.Unlock()
}

type diagProvider struct {
	mu           sync.Mutex
	identity     DispatchProviderIdentity
	capabilities []string
	calls        []string
	args         [][]byte
	ctxs         []context.Context
	result       json.RawMessage
	err          error
	mutateArgs   bool
	started      chan struct{}
	startOnce    sync.Once
}

func newDiagProvider(caps ...string) *diagProvider {
	return &diagProvider{identity: diagIdentityFor(diagBinding()), capabilities: append([]string(nil), caps...), result: json.RawMessage(`{"ok":true}`)}
}
func diagIdentityFor(binding DispatchBinding) DispatchProviderIdentity {
	return DispatchProviderIdentity{TargetID: binding.TargetID, ProviderID: binding.ProviderID, SessionID: binding.SessionID, Generation: binding.Generation}
}
func (p *diagProvider) Identity() DispatchProviderIdentity {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.identity
}
func (p *diagProvider) setIdentity(identity DispatchProviderIdentity) {
	p.mu.Lock()
	p.identity = identity
	p.mu.Unlock()
}
func (p *diagProvider) ReadyCapabilities() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.capabilities...)
}
func (p *diagProvider) setCapabilities(caps ...string) {
	p.mu.Lock()
	p.capabilities = append([]string(nil), caps...)
	p.mu.Unlock()
}
func (p *diagProvider) Invoke(ctx context.Context, operation string, args json.RawMessage) (json.RawMessage, error) {
	p.mu.Lock()
	p.calls = append(p.calls, operation)
	p.args = append(p.args, append([]byte(nil), args...))
	p.ctxs = append(p.ctxs, ctx)
	result, err, mutate, started := p.result, p.err, p.mutateArgs, p.started
	p.mu.Unlock()
	if started != nil {
		p.startOnce.Do(func() { close(started) })
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if mutate && len(args) > 0 {
		args[0] = '!'
	}
	return result, err
}
func (p *diagProvider) snapshot() (calls []string, args [][]byte, ctxs []context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()
	calls = append([]string(nil), p.calls...)
	args = make([][]byte, len(p.args))
	for i := range p.args {
		args[i] = append([]byte(nil), p.args[i]...)
	}
	return calls, args, append([]context.Context(nil), p.ctxs...)
}

type diagAuthorizer struct {
	mu      sync.Mutex
	binding DispatchBinding
	revoked bool
	calls   int
	block   <-chan struct{}
}

func newDiagAuthorizer(binding DispatchBinding) *diagAuthorizer {
	return &diagAuthorizer{binding: binding}
}
func (a *diagAuthorizer) Authorize(ctx context.Context, binding DispatchBinding, operation string) error {
	a.mu.Lock()
	a.calls++
	revoked, expected, block := a.revoked, a.binding, a.block
	a.mu.Unlock()
	if actor, ok := ctx.Value(diagPrincipalKey{}).(string); !ok || actor != binding.ActorID {
		return errors.New("missing or mismatched authenticated principal")
	}
	if binding != expected || revoked {
		return errors.New("no live exact actor/target/provider/session grant")
	}
	if operation != "call" && operation != "audio" && operation != "sms" {
		return errors.New("operation not granted")
	}
	if block != nil {
		<-block
	}
	return nil
}
func (a *diagAuthorizer) setRevoked(revoked bool) {
	a.mu.Lock()
	a.revoked = revoked
	a.mu.Unlock()
}
func (a *diagAuthorizer) setBlock(block <-chan struct{}) {
	a.mu.Lock()
	a.block = block
	a.mu.Unlock()
}
func (a *diagAuthorizer) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

func newDiagDispatcher(t *testing.T, binding DispatchBinding, provider *diagProvider, auth *diagAuthorizer) *LocalDispatcher {
	t.Helper()
	d, err := newLocalDispatcher(func() Environment { return Environment{goos: "android", uid: 0} }, binding, provider, auth)
	if err != nil {
		t.Fatalf("newLocalDispatcher: %v", err)
	}
	t.Cleanup(d.Close)
	return d
}

func TestDispatchConstructorAndroidRootOnly(t *testing.T) {
	binding := diagBinding()
	cases := []struct {
		name string
		os   string
		uid  int
		ok   bool
	}{
		{"android uid zero", "android", 0, true},
		{"android non-root", "android", 2000, false},
		{"linux uid zero", "linux", 0, false},
		{"linux non-root", "linux", 1000, false},
		{"windows uid zero", "windows", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := newLocalDispatcher(func() Environment { return Environment{goos: tc.os, uid: tc.uid} }, binding, newDiagProvider("call"), newDiagAuthorizer(binding))
			if tc.ok && err != nil {
				t.Fatalf("expected constructor success: %v", err)
			}
			if !tc.ok && (err == nil || d != nil) {
				t.Fatalf("expected rejection, got dispatcher=%v err=%v", d, err)
			}
			if d != nil {
				d.Close()
			}
		})
	}
}

func TestDispatchConstructorRejectsInvalidBindingAndNilDependencies(t *testing.T) {
	binding := diagBinding()
	bad := []DispatchBinding{}
	b := binding
	b.ActorID = ""
	bad = append(bad, b)
	b = binding
	b.TargetID = strings.Repeat("t", 257)
	bad = append(bad, b)
	b = binding
	b.ProviderID = " provider-1"
	bad = append(bad, b)
	b = binding
	b.SessionID = "session\n1"
	bad = append(bad, b)
	b = binding
	b.Generation = 0
	bad = append(bad, b)
	for i, invalid := range bad {
		if _, err := newLocalDispatcher(func() Environment { return Environment{goos: "android", uid: 0} }, invalid, newDiagProvider("call"), newDiagAuthorizer(binding)); err == nil {
			t.Errorf("invalid binding case %d was accepted", i)
		}
	}
	if _, err := newLocalDispatcher(nil, binding, newDiagProvider("call"), newDiagAuthorizer(binding)); err == nil {
		t.Error("nil environment accepted")
	}
	if _, err := newLocalDispatcher(func() Environment { return Environment{goos: "android", uid: 0} }, binding, nil, newDiagAuthorizer(binding)); err == nil {
		t.Error("nil provider accepted")
	}
	var typedNil *diagProvider
	if _, err := newLocalDispatcher(func() Environment { return Environment{goos: "android", uid: 0} }, binding, typedNil, newDiagAuthorizer(binding)); err == nil {
		t.Error("typed nil provider accepted")
	}
	if _, err := newLocalDispatcher(func() Environment { return Environment{goos: "android", uid: 0} }, binding, newDiagProvider("call"), nil); err == nil {
		t.Error("nil authorizer accepted")
	}
}

func TestDispatchConstructorRejectsMismatchedProviderIdentity(t *testing.T) {
	binding := diagBinding()
	cases := map[string]func(*DispatchProviderIdentity){
		"target":     func(id *DispatchProviderIdentity) { id.TargetID = "wrong-target" },
		"provider":   func(id *DispatchProviderIdentity) { id.ProviderID = "wrong-provider" },
		"session":    func(id *DispatchProviderIdentity) { id.SessionID = "wrong-session" },
		"generation": func(id *DispatchProviderIdentity) { id.Generation++ },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			provider := newDiagProvider("call")
			identity := diagIdentityFor(binding)
			mutate(&identity)
			provider.setIdentity(identity)
			d, err := newLocalDispatcher(func() Environment { return Environment{goos: "android", uid: 0} }, binding, provider, newDiagAuthorizer(binding))
			if err == nil || d != nil {
				if d != nil {
					d.Close()
				}
				t.Fatalf("mismatched provider identity was accepted: dispatcher=%v err=%v", d, err)
			}
		})
	}
}

func TestDispatchRejectsProviderIdentityChangesAfterCreation(t *testing.T) {
	binding := diagBinding()
	mutations := map[string]func(*DispatchProviderIdentity){
		"target":     func(id *DispatchProviderIdentity) { id.TargetID = "wrong-target" },
		"provider":   func(id *DispatchProviderIdentity) { id.ProviderID = "wrong-provider" },
		"session":    func(id *DispatchProviderIdentity) { id.SessionID = "wrong-session" },
		"generation": func(id *DispatchProviderIdentity) { id.Generation++ },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			provider, auth := newDiagProvider("call"), newDiagAuthorizer(binding)
			d := newDiagDispatcher(t, binding, provider, auth)
			if _, err := d.Invoke(diagContext(binding.ActorID), binding, "call", json.RawMessage(`{}`)); err != nil {
				t.Fatalf("initial call failed: %v", err)
			}
			identity := diagIdentityFor(binding)
			mutate(&identity)
			provider.setIdentity(identity)
			if _, err := d.Invoke(diagContext(binding.ActorID), binding, "call", json.RawMessage(`{}`)); !errors.Is(err, ErrDispatchDenied) {
				t.Fatalf("changed provider identity was accepted: %v", err)
			}
			calls, _, _ := provider.snapshot()
			if len(calls) != 1 {
				t.Fatalf("identity-changed request reached provider: %v", calls)
			}
		})
	}
}

func TestDispatchRejectsUnauthenticatedAndMismatchedPrincipal(t *testing.T) {
	binding := diagBinding()
	provider, auth := newDiagProvider("call"), newDiagAuthorizer(binding)
	d := newDiagDispatcher(t, binding, provider, auth)
	for _, ctx := range []context.Context{context.Background(), diagContext("other-actor")} {
		if _, err := d.Invoke(ctx, binding, "call", json.RawMessage(`{"test":true}`)); !errors.Is(err, ErrDispatchDenied) {
			t.Errorf("unauthenticated/mismatched actor result: %v", err)
		}
	}
	calls, _, _ := provider.snapshot()
	if len(calls) != 0 {
		t.Fatalf("provider invoked for invalid principal: %v", calls)
	}
}

func TestDispatchBindingMismatchDeniedBeforeProvider(t *testing.T) {
	binding := diagBinding()
	provider, auth := newDiagProvider("call"), newDiagAuthorizer(binding)
	d := newDiagDispatcher(t, binding, provider, auth)
	mutations := map[string]func(*DispatchBinding){
		"actor":      func(b *DispatchBinding) { b.ActorID = "other-actor" },
		"target":     func(b *DispatchBinding) { b.TargetID = "other-target" },
		"provider":   func(b *DispatchBinding) { b.ProviderID = "other-provider" },
		"session":    func(b *DispatchBinding) { b.SessionID = "other-session" },
		"generation": func(b *DispatchBinding) { b.Generation++ },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			bad := binding
			mutate(&bad)
			if _, err := d.Invoke(diagContext(binding.ActorID), bad, "call", json.RawMessage(`{"x":1}`)); !errors.Is(err, ErrDispatchDenied) {
				t.Fatalf("mismatched binding accepted: %v", err)
			}
		})
	}
	calls, _, _ := provider.snapshot()
	if len(calls) != 0 {
		t.Fatalf("provider invoked for binding mismatch: %v", calls)
	}
	if auth.count() != 0 {
		t.Fatalf("authorizer called for mismatched binding: %d", auth.count())
	}
}

func TestDispatchRechecksRevocationAndCapabilityEveryCall(t *testing.T) {
	binding := diagBinding()
	provider, auth := newDiagProvider("call"), newDiagAuthorizer(binding)
	d := newDiagDispatcher(t, binding, provider, auth)
	if _, err := d.Invoke(diagContext(binding.ActorID), binding, "call", json.RawMessage(`{"n":1}`)); err != nil {
		t.Fatal(err)
	}
	auth.setRevoked(true)
	if _, err := d.Invoke(diagContext(binding.ActorID), binding, "call", json.RawMessage(`{"n":2}`)); !errors.Is(err, ErrDispatchDenied) {
		t.Fatalf("revoked permission not rejected: %v", err)
	}
	auth.setRevoked(false)
	provider.setCapabilities()
	if _, err := d.Invoke(diagContext(binding.ActorID), binding, "call", json.RawMessage(`{"n":3}`)); !errors.Is(err, ErrDispatchDenied) {
		t.Fatalf("retracted provider capability not rejected: %v", err)
	}
	calls, _, _ := provider.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected exactly first invocation, got %v", calls)
	}
	if auth.count() != 2 {
		t.Fatalf("expected authorization recheck on first and revoked call, got %d", auth.count())
	}
}

func TestDispatchRootEnvironmentRecheckedOnEveryCall(t *testing.T) {
	binding := diagBinding()
	provider, auth := newDiagProvider("call"), newDiagAuthorizer(binding)
	env := &diagEnvironment{os: "android", uid: 0}
	d, err := newLocalDispatcher(env.get, binding, provider, auth)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Invoke(diagContext(binding.ActorID), binding, "call", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	for _, state := range []struct {
		os  string
		uid int
	}{{"android", 2000}, {"linux", 0}, {"linux", 2000}} {
		env.set(state.os, state.uid)
		if _, err := d.Invoke(diagContext(binding.ActorID), binding, "call", json.RawMessage(`{}`)); !errors.Is(err, ErrDispatchDenied) {
			t.Errorf("dispatch admitted after environment changed to %s uid %d: %v", state.os, state.uid, err)
		}
	}
	calls, _, _ := provider.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected one call before environment changed, got %d", len(calls))
	}
}

func TestDispatchOnlyAllowsCallAudioSMSAndRejectsEventPermissionMCP(t *testing.T) {
	binding := diagBinding()
	provider, auth := newDiagProvider("call", "audio", "sms"), newDiagAuthorizer(binding)
	d := newDiagDispatcher(t, binding, provider, auth)
	for _, op := range []string{"call", "audio", "sms"} {
		if _, err := d.Invoke(diagContext(binding.ActorID), binding, op, json.RawMessage(`{"fixture":true}`)); err != nil {
			t.Errorf("supported operation %q denied: %v", op, err)
		}
	}
	for _, op := range []string{"event", "permission", "mcp", "call_changed", "audio_changed", "sms_changed"} {
		if err := d.Check(diagContext(binding.ActorID), binding, op); !errors.Is(err, ErrDispatchDenied) {
			t.Errorf("unsupported operation %q admitted: %v", op, err)
		}
	}
	calls, _, _ := provider.snapshot()
	if len(calls) != 3 {
		t.Fatalf("unsupported event/permission/MCP check invoked provider: %v", calls)
	}
}

func TestDispatchInputValidationAndCopyBoundaries(t *testing.T) {
	binding := diagBinding()
	provider, auth := newDiagProvider("call"), newDiagAuthorizer(binding)
	provider.mutateArgs = true
	d := newDiagDispatcher(t, binding, provider, auth)
	cases := []struct {
		name string
		args json.RawMessage
		ok   bool
	}{
		{"empty", nil, false},
		{"malformed", json.RawMessage(`{"x":`), false},
		{"non-object array", json.RawMessage(`[1]`), false},
		{"non-object scalar", json.RawMessage(`true`), false},
		{"small object", json.RawMessage(`{"small":true}`), true},
		{"exact limit object", json.RawMessage(`{"x":"` + strings.Repeat("a", diagArgLimit-8) + `"}`), true},
		{"over limit object", json.RawMessage(`{"x":"` + strings.Repeat("a", diagArgLimit-7) + `"}`), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := append([]byte(nil), tc.args...)
			_, err := d.Invoke(diagContext(binding.ActorID), binding, "call", tc.args)
			if tc.ok && err != nil {
				t.Fatalf("valid input rejected: %v", err)
			}
			if !tc.ok && !errors.Is(err, ErrDispatchDenied) {
				t.Fatalf("invalid input not rejected at admission: %v", err)
			}
			if string(tc.args) != string(before) {
				t.Fatal("caller-owned argument buffer was modified by provider")
			}
		})
	}
	calls, args, _ := provider.snapshot()
	if len(calls) != 2 {
		t.Fatalf("only the exact-limit and small valid objects should reach provider; got %d calls", len(calls))
	}
	for _, got := range args {
		if len(got) == 0 || got[0] != '{' {
			t.Fatalf("provider did not receive object-shaped owned args: first bytes %q", got[:min(len(got), 8)])
		}
	}
}

func TestDispatchResultValidationAndCopy(t *testing.T) {
	binding := diagBinding()
	provider, auth := newDiagProvider("call"), newDiagAuthorizer(binding)
	d := newDiagDispatcher(t, binding, provider, auth)
	provider.result = json.RawMessage(`{"result":"ok"}`)
	got, err := d.Invoke(diagContext(binding.ActorID), binding, "call", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	provider.result[2] = '!'
	if string(got) != `{"result":"ok"}` {
		t.Fatalf("result aliases provider buffer: %s", got)
	}
	for _, tc := range []struct {
		name string
		wire json.RawMessage
	}{
		{"malformed", json.RawMessage(`{bad}`)},
		{"over limit", json.RawMessage(`{"x":"` + strings.Repeat("a", diagResultLimit) + `"}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider.result = tc.wire
			if _, err := d.Invoke(diagContext(binding.ActorID), binding, "call", json.RawMessage(`{}`)); err == nil {
				t.Fatal("invalid provider result accepted")
			}
		})
	}
}

func TestDispatchCloseCancelsInFlightAndRejectsLaterCalls(t *testing.T) {
	binding := diagBinding()
	provider, auth := newDiagProvider("call"), newDiagAuthorizer(binding)
	provider.started = make(chan struct{})
	d := newDiagDispatcher(t, binding, provider, auth)
	result := make(chan error, 1)
	go func() {
		_, err := d.Invoke(diagContext(binding.ActorID), binding, "call", json.RawMessage(`{}`))
		result <- err
	}()
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("provider was not entered")
	}
	d.Close()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("in-flight invocation succeeded after close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not cancel provider context")
	}
	if _, err := d.Invoke(diagContext(binding.ActorID), binding, "call", json.RawMessage(`{}`)); !errors.Is(err, ErrDispatchDenied) {
		t.Fatalf("closed dispatcher accepted invocation: %v", err)
	}
	_, _, ctxs := provider.snapshot()
	if len(ctxs) != 1 || ctxs[0].Err() == nil {
		t.Fatal("provider's call context was not cancelled")
	}
}

func TestDispatchCloseDuringAuthorizationDoesNotEnterProvider(t *testing.T) {
	binding := diagBinding()
	provider, auth := newDiagProvider("call"), newDiagAuthorizer(binding)
	release := make(chan struct{})
	auth.setBlock(release)
	d := newDiagDispatcher(t, binding, provider, auth)
	result := make(chan error, 1)
	go func() {
		_, err := d.Invoke(diagContext(binding.ActorID), binding, "call", json.RawMessage(`{}`))
		result <- err
	}()
	deadline := time.Now().Add(2 * time.Second)
	for auth.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if auth.count() == 0 {
		t.Fatal("authorization was not reached")
	}
	d.Close()
	close(release)
	select {
	case err := <-result:
		if !errors.Is(err, ErrDispatchDenied) {
			t.Fatalf("authorization-close race returned %v, want denied", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("invoke did not return")
	}
	calls, _, _ := provider.snapshot()
	if len(calls) != 0 {
		t.Fatalf("provider entered after close raced authorization: %v", calls)
	}
}

func TestDispatchRechecksRootAndProviderIdentityAfterAuthorization(t *testing.T) {
	binding := diagBinding()
	cases := map[string]func(*diagEnvironment, *diagProvider){
		"root uid changed": func(env *diagEnvironment, _ *diagProvider) { env.set("android", 2000) },
		"os changed":       func(env *diagEnvironment, _ *diagProvider) { env.set("linux", 0) },
		"provider target changed": func(_ *diagEnvironment, p *diagProvider) {
			id := diagIdentityFor(binding)
			id.TargetID = "wrong-target"
			p.setIdentity(id)
		},
		"provider id changed": func(_ *diagEnvironment, p *diagProvider) {
			id := diagIdentityFor(binding)
			id.ProviderID = "wrong-provider"
			p.setIdentity(id)
		},
		"provider session changed": func(_ *diagEnvironment, p *diagProvider) {
			id := diagIdentityFor(binding)
			id.SessionID = "wrong-session"
			p.setIdentity(id)
		},
		"provider generation changed": func(_ *diagEnvironment, p *diagProvider) {
			id := diagIdentityFor(binding)
			id.Generation++
			p.setIdentity(id)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			provider, auth := newDiagProvider("call"), newDiagAuthorizer(binding)
			env := &diagEnvironment{os: "android", uid: 0}
			release := make(chan struct{})
			auth.setBlock(release)
			d, err := newLocalDispatcher(env.get, binding, provider, auth)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			result := make(chan error, 1)
			go func() {
				_, invokeErr := d.Invoke(diagContext(binding.ActorID), binding, "call", json.RawMessage(`{}`))
				result <- invokeErr
			}()
			deadline := time.Now().Add(2 * time.Second)
			for auth.count() == 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if auth.count() == 0 {
				close(release)
				t.Fatal("authorization was not reached")
			}
			mutate(env, provider)
			close(release)
			select {
			case invokeErr := <-result:
				if !errors.Is(invokeErr, ErrDispatchDenied) {
					t.Fatalf("post-authorization environment/identity change returned %v", invokeErr)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("invoke did not return after authorization resumed")
			}
			calls, _, _ := provider.snapshot()
			if len(calls) != 0 {
				t.Fatalf("provider invoked after environment/identity changed during authorization: %v", calls)
			}
		})
	}
}

func TestDispatchReconnectClosesOldGeneration(t *testing.T) {
	oldBinding := diagBinding()
	oldProvider, oldAuth := newDiagProvider("call"), newDiagAuthorizer(oldBinding)
	old := newDiagDispatcher(t, oldBinding, oldProvider, oldAuth)
	if _, err := old.Invoke(diagContext(oldBinding.ActorID), oldBinding, "call", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	newBinding := oldBinding
	newBinding.SessionID = "session-2"
	newBinding.Generation++
	newProvider, newAuth := newDiagProvider("call"), newDiagAuthorizer(newBinding)
	newProvider.setIdentity(diagIdentityFor(newBinding))
	old.Close() // The owning session lifecycle must do this before replacing a handle.
	newDispatcher := newDiagDispatcher(t, newBinding, newProvider, newAuth)
	if _, err := old.Invoke(diagContext(oldBinding.ActorID), oldBinding, "call", json.RawMessage(`{}`)); !errors.Is(err, ErrDispatchDenied) {
		t.Fatalf("old generation remained usable after its owner closed it: %v", err)
	}
	if _, err := newDispatcher.Invoke(diagContext(newBinding.ActorID), newBinding, "call", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("new generation rejected: %v", err)
	}
	oldCalls, _, _ := oldProvider.snapshot()
	newCalls, _, _ := newProvider.snapshot()
	if len(oldCalls) != 1 || len(newCalls) != 1 {
		t.Fatalf("unexpected old/new provider calls: %d/%d", len(oldCalls), len(newCalls))
	}
}

func TestDispatchConcurrentCallsAndLiveRevocation(t *testing.T) {
	binding := diagBinding()
	provider, auth := newDiagProvider("call"), newDiagAuthorizer(binding)
	d := newDiagDispatcher(t, binding, provider, auth)
	var failed atomic.Bool
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 80; j++ {
				_, err := d.Invoke(diagContext(binding.ActorID), binding, "call", json.RawMessage(`{"n":1}`))
				if err != nil && !errors.Is(err, ErrDispatchDenied) {
					failed.Store(true)
				}
			}
		}()
	}
	for i := 0; i < 30; i++ {
		auth.setRevoked(i%2 == 0)
		provider.setCapabilities("call")
		time.Sleep(time.Millisecond)
	}
	auth.setRevoked(true)
	wg.Wait()
	if failed.Load() {
		t.Fatal("unexpected concurrent dispatch error")
	}
	if _, err := d.Invoke(diagContext(binding.ActorID), binding, "call", json.RawMessage(`{}`)); !errors.Is(err, ErrDispatchDenied) {
		t.Fatalf("final revoked request admitted: %v", err)
	}
}

func TestDispatchMCPInMemoryExactGroupAndSameAdmission(t *testing.T) {
	binding := diagBinding()
	provider, auth := newDiagProvider("call"), newDiagAuthorizer(binding)
	d := newDiagDispatcher(t, binding, provider, auth)
	ctx := context.Background()

	connect := func(grants map[string]bool) (*mcp.ClientSession, func()) {
		t.Helper()
		server := mcp.NewServer(&mcp.Implementation{Name: "dispatch-diagnostics"}, nil)
		if grants["communications"] { // Exact group only: no wildcard expansion.
			mcp.AddTool[map[string]any, map[string]any](server, &mcp.Tool{Name: "communications_call"},
				func(callCtx context.Context, _ *mcp.CallToolRequest, input map[string]any) (*mcp.CallToolResult, map[string]any, error) {
					args, err := json.Marshal(input)
					if err != nil {
						return nil, nil, err
					}
					result, err := d.Invoke(callCtx, binding, "call", args)
					if err != nil {
						return nil, nil, err
					}
					var output map[string]any
					if err = json.Unmarshal(result, &output); err != nil {
						return nil, nil, err
					}
					return nil, output, nil
				})
		}
		server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
			return func(callCtx context.Context, method string, req mcp.Request) (mcp.Result, error) {
				return next(diagContext(binding.ActorID), method, req)
			}
		})
		st, ct := mcp.NewInMemoryTransports()
		serverSession, err := server.Connect(ctx, st, nil)
		if err != nil {
			t.Fatalf("server connect: %v", err)
		}
		client := mcp.NewClient(&mcp.Implementation{Name: "dispatch-diagnostics-client"}, nil)
		clientSession, err := client.Connect(ctx, ct, nil)
		if err != nil {
			_ = serverSession.Close()
			t.Fatalf("client connect: %v", err)
		}
		return clientSession, func() { _ = clientSession.Close(); _ = serverSession.Close() }
	}

	wildcardClient, closeWildcard := connect(map[string]bool{"*": true})
	tools, err := wildcardClient.ListTools(ctx, nil)
	if err != nil {
		closeWildcard()
		t.Fatal(err)
	}
	if len(tools.Tools) != 0 {
		closeWildcard()
		t.Fatalf("wildcard grant exposed communications tool: %+v", tools.Tools)
	}
	if _, err = wildcardClient.CallTool(ctx, &mcp.CallToolParams{Name: "communications_call", Arguments: map[string]any{"fixture": true}}); err == nil {
		closeWildcard()
		t.Fatal("wildcard grant called absent communications tool")
	}
	closeWildcard()

	exactClient, closeExact := connect(map[string]bool{"communications": true})
	defer closeExact()
	tools, err = exactClient.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "communications_call" {
		t.Fatalf("exact communications group did not expose expected tool: %+v", tools.Tools)
	}
	result, err := exactClient.CallTool(ctx, &mcp.CallToolParams{Name: "communications_call", Arguments: map[string]any{"fixture": true}})
	if err != nil || result.IsError {
		t.Fatalf("MCP tool did not route through dispatch admission: result=%+v err=%v", result, err)
	}
	if auth.count() != 1 {
		t.Fatalf("MCP call bypassed or duplicated live admission, auth count=%d", auth.count())
	}
	providerCalls, _, _ := provider.snapshot()
	if len(providerCalls) != 1 || providerCalls[0] != "call" {
		t.Fatalf("MCP call did not reach the fake provider through Invoke: %v", providerCalls)
	}
	auth.setRevoked(true)
	result, err = exactClient.CallTool(ctx, &mcp.CallToolParams{Name: "communications_call", Arguments: map[string]any{"fixture": true}})
	if err != nil || result == nil || !result.IsError {
		t.Fatalf("revoked MCP request was not returned as tool error: result=%+v err=%v", result, err)
	}
	providerCalls, _, _ = provider.snapshot()
	if len(providerCalls) != 1 {
		t.Fatalf("revoked MCP request reached fake provider: %v", providerCalls)
	}
}

func TestDispatchProviderErrorIsReturnedWithoutRetry(t *testing.T) {
	binding := diagBinding()
	provider, auth := newDiagProvider("sms"), newDiagAuthorizer(binding)
	provider.err = fmt.Errorf("fake provider rejected request")
	d := newDiagDispatcher(t, binding, provider, auth)
	if _, err := d.Invoke(diagContext(binding.ActorID), binding, "sms", json.RawMessage(`{"fixture":true}`)); err == nil || !strings.Contains(err.Error(), "fake provider rejected") {
		t.Fatalf("provider error not preserved: %v", err)
	}
	calls, _, _ := provider.snapshot()
	if len(calls) != 1 {
		t.Fatalf("provider error caused retry: %d calls", len(calls))
	}
}
