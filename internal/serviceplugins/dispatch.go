package serviceplugins

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
)

var ErrDispatchDenied = errors.New("local communications dispatch denied")

// DispatchBinding must come from trusted local enrollment/authentication, never
// from an AgentHello root claim. This module does not authenticate remote agents.
type DispatchBinding struct {
	ActorID    string
	TargetID   string
	ProviderID string
	SessionID  string
	Generation uint64
}

// DispatchProviderIdentity identifies the trusted local adapter connection.
type DispatchProviderIdentity struct {
	TargetID   string
	ProviderID string
	SessionID  string
	Generation uint64
}

// DispatchProvider is a local initialized adapter. Identity is a local observation,
// not remote input. Implementations must honor cancellation and validate operation
// arguments before any side effect.
type DispatchProvider interface {
	Identity() DispatchProviderIdentity
	CommunicationsProvider
	Invoke(context.Context, string, json.RawMessage) (json.RawMessage, error)
}

// DispatchEventProvider declares supported event categories independently of
// command readiness. Absence fails closed; it does not register an event handler.
type DispatchEventProvider interface {
	EventCapabilities() []string
}

// DispatchAuthorizer must verify the authenticated principal in ctx, the live
// session/generation, exact permission and target on EVERY invocation.
// A caller-supplied DispatchBinding alone is not authentication or a grant.
type DispatchAuthorizer interface {
	Authorize(context.Context, DispatchBinding, string) error
}

// LocalDispatcher is an admission seam, not a registered RPC/MCP/OS handler.
// No provider or network listener is installed by constructing it.
type LocalDispatcher struct {
	binding     DispatchBinding
	provider    DispatchProvider
	authorizer  DispatchAuthorizer
	environment func() Environment
	session     context.Context
	cancel      context.CancelFunc
	closed      atomic.Bool
}

func NewLocalDispatcher(binding DispatchBinding, provider DispatchProvider, authorizer DispatchAuthorizer) (*LocalDispatcher, error) {
	return newLocalDispatcher(func() Environment { return Environment{goos: runtime.GOOS, uid: os.Geteuid()} }, binding, provider, authorizer)
}

func newLocalDispatcher(environment func() Environment, binding DispatchBinding, provider DispatchProvider, authorizer DispatchAuthorizer) (*LocalDispatcher, error) {
	return newLocalDispatcherWithLifetime(context.Background(), environment, binding, provider, authorizer)
}

func newLocalDispatcherWithLifetime(parent context.Context, environment func() Environment, binding DispatchBinding, provider DispatchProvider, authorizer DispatchAuthorizer) (*LocalDispatcher, error) {
	if parent == nil || parent.Err() != nil {
		return nil, ErrDispatchDenied
	}

	if environment == nil || nilInterface(provider) || nilInterface(authorizer) || !validDispatchBinding(binding) {
		return nil, ErrDispatchDenied
	}
	env := environment()
	if env.GOOS() != "android" || env.UID() != 0 {
		return nil, ErrDispatchDenied
	}
	if !matchesProvider(binding, provider.Identity()) {
		return nil, ErrDispatchDenied
	}
	session, cancel := context.WithCancel(parent)
	if session.Err() != nil {
		cancel()
		return nil, ErrDispatchDenied
	}
	return &LocalDispatcher{binding: binding, provider: provider, authorizer: authorizer, environment: environment, session: session, cancel: cancel}, nil
}

func matchesProvider(binding DispatchBinding, provider DispatchProviderIdentity) bool {
	return binding.TargetID == provider.TargetID && binding.ProviderID == provider.ProviderID &&
		binding.SessionID == provider.SessionID && binding.Generation == provider.Generation
}

func validDispatchBinding(binding DispatchBinding) bool {
	if binding.Generation == 0 {
		return false
	}
	for _, value := range []string{binding.ActorID, binding.TargetID, binding.ProviderID, binding.SessionID} {
		if value == "" || len(value) > 256 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\x00\r\n") {
			return false
		}
	}
	return true
}

// Check permits command/MCP adapters to recheck their originating call/audio/SMS
// capability and command readiness. Event readers must use CheckEvents. It does not authorize a generic event channel or grant permissions,
// and must not be used as a cached authorization.
func (d *LocalDispatcher) Check(ctx context.Context, binding DispatchBinding, operation string) error {
	return d.check(ctx, binding, operation, true)
}

// CheckEvents reauthorizes reading volatile events for an originating capability.
// Unlike command admission, an active/busy provider may still expose its events.
// It grants no command permission and does not register an event endpoint.
func (d *LocalDispatcher) CheckEvents(ctx context.Context, binding DispatchBinding, operation string) error {
	return d.check(ctx, binding, operation, false)
}

func (d *LocalDispatcher) check(ctx context.Context, binding DispatchBinding, operation string, requireReady bool) error {
	if ctx == nil || ctx.Err() != nil || !d.live(binding) {
		return ErrDispatchDenied
	}

	if operation != "call" && operation != "audio" && operation != "sms" {
		return ErrDispatchDenied
	}

	var capabilities []string
	if requireReady {
		capabilities = d.provider.ReadyCapabilities()
	} else {
		events, ok := d.provider.(DispatchEventProvider)
		if !ok || nilInterface(events) {
			return ErrDispatchDenied
		}
		capabilities = events.EventCapabilities()
	}
	supported := false
	for _, capability := range capabilities {
		if capability == operation {
			supported = true
			break
		}
	}
	if !supported {
		return ErrDispatchDenied
	}
	if err := d.authorizer.Authorize(ctx, d.binding, operation); err != nil {
		return ErrDispatchDenied
	}
	if !d.live(binding) || ctx.Err() != nil {
		return ErrDispatchDenied
	}
	return nil
}

// live checks trusted local state; it never authenticates caller strings.
func (d *LocalDispatcher) live(binding DispatchBinding) bool {
	if d == nil || d.session == nil || d.cancel == nil || d.environment == nil || nilInterface(d.provider) || nilInterface(d.authorizer) || d.closed.Load() || d.session.Err() != nil || binding != d.binding || !validDispatchBinding(binding) {
		return false
	}
	env := d.environment()
	matches := env.GOOS() == "android" && env.UID() == 0 && matchesProvider(binding, d.provider.Identity())
	// Trusted callbacks may block or close this dispatcher. Recheck the local
	// lifecycle after they return; this is not atomic external revocation.
	return matches && !d.closed.Load() && d.session.Err() == nil
}

// Invoke accepts only a bounded JSON object. Both argument and result buffers
// are copied at this boundary; provider transient allocations are not bounded here.
func (d *LocalDispatcher) Invoke(ctx context.Context, binding DispatchBinding, operation string, args json.RawMessage) (json.RawMessage, error) {
	if d == nil || d.session == nil || d.cancel == nil || ctx == nil || ctx.Err() != nil || d.closed.Load() || d.session.Err() != nil || binding != d.binding || len(args) == 0 || len(args) > 64*1024 {
		return nil, ErrDispatchDenied
	}
	owned := append(json.RawMessage(nil), args...)
	trimmed := bytes.TrimSpace(owned)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return nil, ErrDispatchDenied
	}
	call, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(d.session, cancel)
	defer func() { stop(); cancel() }()
	if err := d.Check(call, binding, operation); err != nil {
		return nil, err
	}
	result, err := d.provider.Invoke(call, operation, owned)
	if err != nil {
		return nil, err
	}
	if call.Err() != nil {
		return nil, call.Err()
	}
	if len(result) > 1024*1024 || !json.Valid(result) {
		return nil, errors.New("invalid local communications result")
	}
	ownedResult := append(json.RawMessage(nil), result...)
	// Session cancellation reaches call asynchronously. Check the owning lifecycle
	// after copying too, so a completed Close cannot return a successful result.
	if call.Err() != nil {
		return nil, call.Err()
	}
	if d.closed.Load() || d.session.Err() != nil {
		return nil, ErrDispatchDenied
	}
	return ownedResult, nil
}

// Close rejects new admission and cancels ongoing provider contexts. It cannot
// undo a provider side effect that was already dispatched. Reconnect constructs
// a new dispatcher with a new trusted session generation; old handles stay closed.
func (d *LocalDispatcher) Close() {
	if d != nil && d.closed.CompareAndSwap(false, true) && d.cancel != nil {
		d.cancel()
	}
}
