package serviceplugins

import (
	"context"
	"encoding/json"
	"net"
	"sync"
	"sync/atomic"
	"testing"
)

// Pure lifetime fixture; SSH authentication is separately exercised below and in transport tests.
func dispatcherTransportFixture(t *testing.T, id string) *ProviderTransport {
	t.Helper()
	raw, peer := net.Pipe()
	lifetime, cancel := context.WithCancel(context.Background())
	transport := &ProviderTransport{raw: raw, lifetime: lifetime, cancel: cancel, providerID: id, verified: true, done: make(chan struct{}), drained: make(chan struct{})}
	t.Cleanup(func() { transport.Close(); peer.Close() })
	return transport
}

type dispatcherTransportAuth func(context.Context, DispatchBinding, string) error

func (f dispatcherTransportAuth) Authorize(ctx context.Context, b DispatchBinding, operation string) error {
	return f(ctx, b, operation)
}
func dispatcherForTransport(t *testing.T, transport *ProviderTransport, provider DispatchProvider, authorizer DispatchAuthorizer) *LocalDispatcher {
	t.Helper()
	d, err := newProviderLocalDispatcher(providerTransportAndroidFixture, transport, diagBinding(), provider, authorizer)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	return d
}
func TestProviderDispatcherRejectsUnverifiedClosedAndMismatchedTransport(t *testing.T) {
	for _, kind := range []string{"nil", "zero", "unverified", "closed", "provider", "authorizer", "typed-authorizer"} {
		t.Run(kind, func(t *testing.T) {
			transport := dispatcherTransportFixture(t, diagBinding().ProviderID)
			var auth DispatchAuthorizer = newDiagAuthorizer(diagBinding())
			switch kind {
			case "nil":
				transport = nil
			case "zero":
				transport = &ProviderTransport{}
			case "unverified":
				transport.verified = false
			case "closed":
				transport.Close()
			case "provider":
				transport.providerID = "other"
			case "authorizer":
				auth = nil
			case "typed-authorizer":
				var missing *diagAuthorizer
				auth = missing
			}
			provider := newDiagProvider("call")
			if d, err := newProviderLocalDispatcher(providerTransportAndroidFixture, transport, diagBinding(), provider, auth); d != nil || err == nil {
				t.Fatal("invalid transport binding accepted")
			}
			calls, _, _ := provider.snapshot()
			if len(calls) != 0 {
				t.Fatal("rejected constructor invoked provider")
			}
		})
	}
}
func TestProviderDispatcherAuthorizationCloseRevokesAllAdmissionForms(t *testing.T) {
	for _, mode := range []string{"check", "events", "invoke"} {
		t.Run(mode, func(t *testing.T) {
			transport := dispatcherTransportFixture(t, diagBinding().ProviderID)
			provider := newDiagProvider("call")
			base := newDiagAuthorizer(diagBinding())
			authorizer := dispatcherTransportAuth(func(ctx context.Context, b DispatchBinding, operation string) error {
				transport.Close()
				return base.Authorize(ctx, b, operation)
			})
			d := dispatcherForTransport(t, transport, provider, authorizer)
			ctx := diagContext(diagBinding().ActorID)
			var err error
			switch mode {
			case "check":
				err = d.Check(ctx, diagBinding(), "call")
			case "events":
				err = d.CheckEvents(ctx, diagBinding(), "call")
			case "invoke":
				_, err = d.Invoke(ctx, diagBinding(), "call", json.RawMessage(`{}`))
			}
			if err == nil {
				t.Fatal("closed transport admitted operation")
			}
			calls, _, _ := provider.snapshot()
			if len(calls) != 0 {
				t.Fatal("provider invoked after authorization closed transport")
			}
		})
	}
}

type dispatcherIdentityClose struct {
	*diagProvider
	close func()
}

func (p *dispatcherIdentityClose) Identity() DispatchProviderIdentity {
	p.close()
	return p.diagProvider.Identity()
}
func TestProviderDispatcherConstructorCallbacksCannotReturnCanceledChild(t *testing.T) {
	for _, kind := range []string{"environment", "identity"} {
		t.Run(kind, func(t *testing.T) {
			transport := dispatcherTransportFixture(t, diagBinding().ProviderID)
			env := providerTransportAndroidFixture
			var provider DispatchProvider = newDiagProvider("call")
			if kind == "environment" {
				env = func() Environment { transport.Close(); return providerTransportAndroidFixture() }
			} else {
				provider = &dispatcherIdentityClose{newDiagProvider("call"), func() { transport.Close() }}
			}
			if d, err := newProviderLocalDispatcher(env, transport, diagBinding(), provider, newDiagAuthorizer(diagBinding())); d != nil || err == nil {
				t.Fatal("canceled constructor returned child")
			}
		})
	}
}
func TestProviderDispatcherChildClosePreservesSiblingButTransportCloseRevokesBoth(t *testing.T) {
	transport := dispatcherTransportFixture(t, diagBinding().ProviderID)
	auth := newDiagAuthorizer(diagBinding())
	first := dispatcherForTransport(t, transport, newDiagProvider("call"), auth)
	second := dispatcherForTransport(t, transport, newDiagProvider("call"), auth)
	ctx := diagContext(diagBinding().ActorID)
	first.Close()
	if first.Check(ctx, diagBinding(), "call") == nil {
		t.Fatal("closed child admitted")
	}
	if err := second.Check(ctx, diagBinding(), "call"); err != nil {
		t.Fatal("sibling was revoked", err)
	}
	transport.Close()
	if first.session.Err() == nil || second.session.Err() == nil {
		t.Fatal("parent cancellation was not synchronous")
	}
	if second.Check(ctx, diagBinding(), "call") == nil {
		t.Fatal("transport close did not revoke sibling")
	}
}

type dispatcherIgnoringCancel struct {
	*diagProvider
	entered chan context.Context
	release chan struct{}
}

func (p *dispatcherIgnoringCancel) Invoke(ctx context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) {
	p.entered <- ctx
	<-p.release
	return json.RawMessage(`{"ok":true}`), nil
}
func TestProviderDispatcherLateProviderResultCannotSurviveTransportClose(t *testing.T) {
	transport := dispatcherTransportFixture(t, diagBinding().ProviderID)
	provider := &dispatcherIgnoringCancel{newDiagProvider("call"), make(chan context.Context, 1), make(chan struct{})}
	d := dispatcherForTransport(t, transport, provider, newDiagAuthorizer(diagBinding()))
	done := make(chan error, 1)
	go func() {
		_, err := d.Invoke(diagContext(diagBinding().ActorID), diagBinding(), "call", json.RawMessage(`{}`))
		done <- err
	}()
	call := providerTransportAwait(t, provider.entered)
	transport.Close()
	providerTransportAwait(t, call.Done())
	close(provider.release)
	if err := providerTransportAwait(t, done); err == nil {
		t.Fatal("late successful result escaped revoked lifetime")
	}
}

type dispatcherEventClose struct {
	*diagProvider
	transport    *ProviderTransport
	closeOnEvent atomic.Bool
}

func (p *dispatcherEventClose) EventCapabilities() []string {
	if p.closeOnEvent.Load() {
		p.transport.Close()
	}
	return []string{"call"}
}
func TestProviderDispatcherEventPublisherRechecksParentAfterCapabilities(t *testing.T) {
	transport := dispatcherTransportFixture(t, diagBinding().ProviderID)
	provider := &dispatcherEventClose{diagProvider: newDiagProvider("call"), transport: transport}
	d := dispatcherForTransport(t, transport, provider, newDiagAuthorizer(diagBinding()))
	buffer, err := NewCallEventBuffer(d)
	if err != nil {
		t.Fatal(err)
	}
	defer buffer.Close()
	provider.closeOnEvent.Store(true)
	if err = buffer.Append(diagBinding(), CallEvent{CallID: "fixture-call", State: "dialing", UpdatedAt: 1}); err == nil {
		t.Fatal("event appended after capability callback closed parent")
	}
	if _, err = buffer.Read(diagContext(diagBinding().ActorID), diagBinding(), CallEventCursor{}, 1); err == nil {
		t.Fatal("event read after parent closed")
	}
}
func TestProviderDispatcherUsesActualVerifiedSSHTransportIdentity(t *testing.T) {
	config, host := providerTransportConfigFixture(t)
	raw, peer := providerTransportPair(t)
	serverResult := providerTransportServer(t, peer, host, config.ServiceSigner.PublicKey())
	transport, err := newProviderTransport(providerTransportAndroidFixture, context.Background(), raw, config)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	server := providerTransportAwait(t, serverResult)
	if server.err != nil {
		t.Fatal(server.err)
	}
	defer server.conn.Close()
	binding := diagBinding()
	binding.ProviderID = config.ProviderID
	provider := newDiagProvider("call")
	provider.setIdentity(diagIdentityFor(binding))
	d, err := newProviderLocalDispatcher(providerTransportAndroidFixture, transport, binding, provider, newDiagAuthorizer(binding))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err = d.Check(diagContext(binding.ActorID), binding, "call"); err != nil {
		t.Fatal(err)
	}
	server.conn.Close()
	providerTransportAwait(t, transport.Done())
	if d.session.Err() == nil || d.Check(diagContext(binding.ActorID), binding, "call") == nil {
		t.Fatal("peer disconnect did not revoke dispatcher")
	}
}

func TestProviderDispatcherDoneWaitsForLifetimeCancellationCompletion(t *testing.T) {
	transport := dispatcherTransportFixture(t, diagBinding().ProviderID)
	child := dispatcherForTransport(t, transport, newDiagProvider("call"), newDiagAuthorizer(diagBinding()))
	originalCancel := transport.cancel
	entered, release, closed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	// Hold the synchronous cancel phase open after its context work. Done must
	// not advertise completion until that phase has actually returned.
	transport.cancel = func() { originalCancel(); close(entered); <-release }
	go func() { transport.Close(); close(closed) }()
	providerTransportAwait(t, entered)
	select {
	case <-transport.Done():
		t.Fatal("Done published before synchronous cancel returned")
	default:
	}
	unblock()
	providerTransportAwait(t, transport.Done())
	providerTransportAwait(t, closed)
	if child.session.Err() == nil {
		t.Fatal("Done published before child was canceled")
	}
}
