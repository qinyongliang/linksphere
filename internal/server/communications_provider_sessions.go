package server

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/qinyongliang/linksphere/internal/serviceplugins"
)

var errCommunicationsSessionDenied = errors.New("communications provider session denied")

// This seam is deliberately not wired to AgentHello, an HTTP route, or an MCP tool.
// A normal Agent enrollment token or self-reported Android/root metadata is not proof.
type communicationsProviderConnection interface{ CloseChan() <-chan struct{} }
type communicationsProviderCandidate struct {
	agentID, targetID, providerID string
	connection                    communicationsProviderConnection
}
type communicationsProviderVerifier interface {
	verifyProvider(context.Context, communicationsProviderCandidate) error
}
type communicationsProviderScope struct{ targetID, providerID string }
type communicationsProviderAttempt struct{ cancel context.CancelFunc }
type communicationsProviderEntry struct {
	agentID          string
	identity         serviceplugins.DispatchProviderIdentity
	connection       communicationsProviderConnection
	connectionClosed <-chan struct{}
	lifetime         context.Context
	cancel           context.CancelFunc
}
type communicationsProviderRegistration struct {
	agentID  string
	identity serviceplugins.DispatchProviderIdentity
}

// The host must supply a real trusted verifier before registration is possible.
// Entries pin the exact connection, not a lookup that may resolve to a replacement.
// Cancellation owns only this binding; ordinary Agent connections are never closed.
type communicationsProviderSessionManager struct {
	mu                  sync.Mutex
	verifier            communicationsProviderVerifier
	maximum             int
	activeVerifications int
	generation          uint64
	closed              bool
	entries             map[communicationsProviderScope]*communicationsProviderEntry
	pending             map[communicationsProviderScope]*communicationsProviderAttempt
	newID               func() (string, error)
}

func newCommunicationsProviderSessionManager(verifier communicationsProviderVerifier, maximum int) (*communicationsProviderSessionManager, error) {
	if maximum < 1 {
		return nil, errCommunicationsSessionDenied
	}
	return &communicationsProviderSessionManager{verifier: verifier, maximum: maximum,
		entries: make(map[communicationsProviderScope]*communicationsProviderEntry), pending: make(map[communicationsProviderScope]*communicationsProviderAttempt),
		newID: func() (string, error) { id, err := uuid.NewRandom(); return id.String(), err }}, nil
}
func nilCommunicationsValue(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	}
	return false
}
func communicationsIdentifier(value string) bool {
	return value != "" && len(value) <= 256 && utf8.ValidString(value) && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}
func communicationsChannelClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
func (m *communicationsProviderSessionManager) register(ctx context.Context, c communicationsProviderCandidate) (communicationsProviderRegistration, error) {
	denied := communicationsProviderRegistration{}
	if m == nil || ctx == nil || ctx.Err() != nil || nilCommunicationsValue(m.verifier) || nilCommunicationsValue(c.connection) ||
		!communicationsIdentifier(c.agentID) || !communicationsIdentifier(c.targetID) || !communicationsIdentifier(c.providerID) {
		return denied, errCommunicationsSessionDenied
	}
	closed := c.connection.CloseChan()
	if closed == nil || communicationsChannelClosed(closed) {
		return denied, errCommunicationsSessionDenied
	}
	scope := communicationsProviderScope{c.targetID, c.providerID}
	verification, cancel := context.WithCancel(ctx)
	attempt := &communicationsProviderAttempt{cancel: cancel}
	m.mu.Lock()
	used := len(m.entries)
	for key := range m.pending {
		if m.entries[key] == nil {
			used++
		}
	}
	_, occupied := m.entries[scope]
	if m.closed || m.entries == nil || m.pending == nil || m.newID == nil || m.maximum < 1 || m.pending[scope] != nil || m.activeVerifications >= m.maximum || (!occupied && used >= m.maximum) {
		m.mu.Unlock()
		cancel()
		return denied, errCommunicationsSessionDenied
	}
	// A changed Agent association requires explicit revoke followed by fresh verification.
	if old := m.entries[scope]; old != nil && old.agentID != c.agentID {
		m.mu.Unlock()
		cancel()
		return denied, errCommunicationsSessionDenied
	}
	m.pending[scope] = attempt
	m.activeVerifications++
	m.mu.Unlock()
	watcherDone := make(chan struct{})
	defer func() {
		cancel()
		<-watcherDone
		m.mu.Lock()
		if m.pending[scope] == attempt {
			delete(m.pending, scope)
		}
		m.activeVerifications--
		m.mu.Unlock()
	}()
	// At most maximum verification watchers exist. Disconnect cancels verification,
	// but the active slot remains charged until the verifier actually returns.
	go func() {
		defer close(watcherDone)
		select {
		case <-closed:
			cancel()
		case <-verification.Done():
		}
	}()
	// No manager lock is held across trusted host callbacks or entropy acquisition.
	if err := m.verifier.verifyProvider(verification, c); err != nil || verification.Err() != nil || communicationsChannelClosed(closed) {
		return denied, errCommunicationsSessionDenied
	}
	id, err := m.newID()
	if err != nil || !communicationsIdentifier(id) {
		return denied, errCommunicationsSessionDenied
	}
	lifetime, stop := context.WithCancel(context.Background())
	entry := &communicationsProviderEntry{agentID: c.agentID, connection: c.connection, connectionClosed: closed, lifetime: lifetime, cancel: stop}
	m.mu.Lock()
	if m.closed || m.pending[scope] != attempt || verification.Err() != nil || communicationsChannelClosed(closed) || m.generation == ^uint64(0) {
		m.mu.Unlock()
		stop()
		return denied, errCommunicationsSessionDenied
	}
	old := m.entries[scope]
	if old != nil && old.agentID != c.agentID {
		m.mu.Unlock()
		stop()
		return denied, errCommunicationsSessionDenied
	}
	m.generation++
	entry.identity = serviceplugins.DispatchProviderIdentity{TargetID: c.targetID, ProviderID: c.providerID, SessionID: id, Generation: m.generation}
	// These lifetimes are our own standard Background-derived cancel contexts.
	// Cancel the old binding before publishing its replacement; never close the Agent transport.
	if old != nil {
		old.cancel()
	}
	m.entries[scope] = entry
	delete(m.pending, scope)
	m.mu.Unlock()
	go func() {
		select {
		case <-closed:
		case <-lifetime.Done():
		}
		m.remove(entry)
	}()
	return communicationsProviderRegistration{agentID: entry.agentID, identity: entry.identity}, nil
}
func (m *communicationsProviderSessionManager) remove(entry *communicationsProviderEntry) {
	scope := communicationsProviderScope{entry.identity.TargetID, entry.identity.ProviderID}
	m.mu.Lock()
	entry.cancel()
	if m.entries[scope] == entry {
		delete(m.entries, scope)
	}
	m.mu.Unlock()
}
func communicationsSessionPrincipal(ctx context.Context) (communicationsPrincipal, bool) {
	if ctx == nil || ctx.Err() != nil {
		return communicationsPrincipal{}, false
	}
	p, ok := ctx.Value(communicationsPrincipalKey{}).(communicationsPrincipal)
	return p, ok && communicationsIdentifier(p.actorID) && communicationsIdentifier(p.tokenID) && p.tokenHash != ([32]byte{})
}

// current is a freshness snapshot, not authorization. The repository authorizer is still required.
func (m *communicationsProviderSessionManager) current(ctx context.Context, targetID, providerID string) (communicationsProviderSession, error) {
	denied := communicationsProviderSession{}
	p, ok := communicationsSessionPrincipal(ctx)
	if m == nil || !ok || !communicationsIdentifier(targetID) || !communicationsIdentifier(providerID) {
		return denied, errCommunicationsSessionDenied
	}
	scope := communicationsProviderScope{targetID, providerID}
	m.mu.Lock()
	entry := m.entries[scope]
	if m.closed || entry == nil || nilCommunicationsValue(m.verifier) || m.activeVerifications >= m.maximum {
		m.mu.Unlock()
		return denied, errCommunicationsSessionDenied
	}
	if entry.lifetime.Err() != nil || communicationsChannelClosed(entry.connectionClosed) {
		entry.cancel()
		delete(m.entries, scope)
		m.mu.Unlock()
		return denied, errCommunicationsSessionDenied
	}
	id := entry.identity
	agent := entry.agentID
	m.activeVerifications++
	m.mu.Unlock()
	// A registration proves only its original target snapshot. Recheck the exact
	// pinned transport before exposing a fresh session; never hold the manager
	// lock across the host verifier. Keep the admission slot until it returns.
	defer func() {
		m.mu.Lock()
		m.activeVerifications--
		m.mu.Unlock()
	}()
	verification, cancel := context.WithCancel(ctx)
	defer cancel()
	stopLifetime := context.AfterFunc(entry.lifetime, cancel)
	defer stopLifetime()
	err := m.verifier.verifyProvider(verification, communicationsProviderCandidate{
		agentID: agent, targetID: id.TargetID, providerID: id.ProviderID, connection: entry.connection,
	})
	verificationErr := verification.Err()
	callerErr := ctx.Err()
	m.mu.Lock()
	unchanged := !m.closed && m.entries[scope] == entry && entry.lifetime.Err() == nil && !communicationsChannelClosed(entry.connectionClosed)
	// A cancelled caller does not revoke a shared healthy binding. A completed
	// denial does invalidate that old binding, never a replacement registration.
	if err != nil && callerErr == nil && m.entries[scope] == entry {
		entry.cancel()
		delete(m.entries, scope)
	}
	m.mu.Unlock()
	if err != nil || verificationErr != nil || ctx.Err() != nil || !unchanged {
		return denied, errCommunicationsSessionDenied
	}
	return communicationsProviderSession{AgentID: agent, Binding: serviceplugins.DispatchBinding{ActorID: p.actorID, TargetID: id.TargetID, ProviderID: id.ProviderID, SessionID: id.SessionID, Generation: id.Generation}}, nil
}

// pin never resolves AgentID through the ordinary registry. Future adapters must combine the
// returned lifetime with request cancellation and reauthorize immediately before sending.
func (m *communicationsProviderSessionManager) pin(ctx context.Context, binding serviceplugins.DispatchBinding, agentID string) (communicationsProviderConnection, context.Context, error) {
	current, err := m.current(ctx, binding.TargetID, binding.ProviderID)
	if err != nil || current.Binding != binding || current.AgentID != agentID {
		return nil, nil, errCommunicationsSessionDenied
	}
	scope := communicationsProviderScope{binding.TargetID, binding.ProviderID}
	m.mu.Lock()
	entry := m.entries[scope]
	if m.closed || entry == nil || entry.agentID != agentID || entry.identity.SessionID != binding.SessionID || entry.identity.Generation != binding.Generation || entry.lifetime.Err() != nil || communicationsChannelClosed(entry.connectionClosed) {
		m.mu.Unlock()
		return nil, nil, errCommunicationsSessionDenied
	}
	connection, lifetime := entry.connection, entry.lifetime
	m.mu.Unlock()
	// Caller context methods may be implemented by host adapters; never invoke them under our lock.
	if ctx.Err() != nil {
		return nil, nil, errCommunicationsSessionDenied
	}
	return connection, lifetime, nil
}
func (m *communicationsProviderSessionManager) revoke(targetID, providerID string) bool {
	if m == nil {
		return false
	}
	scope := communicationsProviderScope{targetID, providerID}
	m.mu.Lock()
	entry := m.entries[scope]
	attempt := m.pending[scope]
	if entry != nil {
		entry.cancel()
	}
	delete(m.entries, scope)
	delete(m.pending, scope)
	m.mu.Unlock()
	if attempt != nil {
		attempt.cancel()
	}
	return entry != nil || attempt != nil
}
func (m *communicationsProviderSessionManager) close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	for _, entry := range m.entries {
		entry.cancel()
	}
	pending := m.pending
	m.entries = make(map[communicationsProviderScope]*communicationsProviderEntry)
	m.pending = make(map[communicationsProviderScope]*communicationsProviderAttempt)
	m.mu.Unlock()
	for _, attempt := range pending {
		attempt.cancel()
	}
}
