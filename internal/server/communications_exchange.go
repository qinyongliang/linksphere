package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/qinyongliang/linksphere/internal/protocol"
	"github.com/qinyongliang/linksphere/internal/serviceplugins"
)

var errCommunicationsExchangeDenied = errors.New("communications exchange denied")

type communicationsExchangeStream interface {
	io.ReadWriteCloser
	SetDeadline(time.Time) error
}
type communicationsStreamOpener func(context.Context, communicationsProviderConnection) (communicationsExchangeStream, error)
type communicationsExchangeResult struct {
	value json.RawMessage
	err   error
}

// Private and unregistered: neither construction nor invocation installs a listener,
// trusts AgentHello metadata, or enables any ordinary Agent/MCP capability.
type communicationsExchange struct {
	sessions   *communicationsProviderSessionManager
	authorizer serviceplugins.DispatchAuthorizer
	open       communicationsStreamOpener
	slots      chan struct{}
	timeout    time.Duration
}

func newCommunicationsExchange(sessions *communicationsProviderSessionManager, authorizer serviceplugins.DispatchAuthorizer, open communicationsStreamOpener, maximum int, timeout time.Duration) (*communicationsExchange, error) {
	if sessions == nil || nilCommunicationsValue(authorizer) || open == nil || maximum < 1 || maximum > 64 || timeout <= 0 || timeout > 5*time.Minute {
		return nil, errCommunicationsExchangeDenied
	}
	return &communicationsExchange{sessions: sessions, authorizer: authorizer, open: open, slots: make(chan struct{}, maximum), timeout: timeout}, nil
}

// invoke owns a bounded copy before starting work. The slot belongs to the worker,
// not the waiting caller: an uncooperative opener cannot create unbounded workers.
func (e *communicationsExchange) invoke(ctx context.Context, binding serviceplugins.DispatchBinding, agentID, operation string, args json.RawMessage) (json.RawMessage, error) {
	if e == nil || e.sessions == nil || nilCommunicationsValue(e.authorizer) || e.open == nil || e.timeout <= 0 || ctx == nil || ctx.Err() != nil || len(args) == 0 || len(args) > 64*1024 || (operation != "call" && operation != "audio" && operation != "sms") {
		return nil, errCommunicationsExchangeDenied
	}
	if binding.Generation == 0 {
		return nil, errCommunicationsExchangeDenied
	}
	for _, identifier := range []string{binding.ActorID, binding.TargetID, binding.ProviderID, binding.SessionID, agentID} {
		if !communicationsIdentifier(identifier) || !utf8.ValidString(identifier) {
			return nil, errCommunicationsExchangeDenied
		}
	}
	select {
	case e.slots <- struct{}{}:
	default:
		return nil, errCommunicationsExchangeDenied
	}
	// Own short strings as well as bytes; substrings can otherwise retain large request buffers.
	binding = serviceplugins.DispatchBinding{
		ActorID: strings.Clone(binding.ActorID), TargetID: strings.Clone(binding.TargetID),
		ProviderID: strings.Clone(binding.ProviderID), SessionID: strings.Clone(binding.SessionID),
		Generation: binding.Generation,
	}
	agentID, operation = strings.Clone(agentID), strings.Clone(operation)
	owned := append(json.RawMessage(nil), args...)
	trimmed := bytes.TrimSpace(owned)
	if len(trimmed) == 0 || trimmed[0] != '{' || !utf8.Valid(owned) || !json.Valid(owned) {
		<-e.slots
		return nil, errCommunicationsExchangeDenied
	}
	principal, valid := communicationsSessionPrincipal(ctx)
	if !valid {
		<-e.slots
		return nil, errCommunicationsExchangeDenied
	}
	call, cancel, release := communicationsInvocationContext(ctx, principal, e.timeout)
	defer release()
	if call.Err() != nil {
		<-e.slots
		return nil, call.Err()
	}
	result := make(chan communicationsExchangeResult, 1)
	go func() {
		defer func() { <-e.slots }()
		value, err := e.exchange(call, cancel, binding, agentID, operation, owned)
		result <- communicationsExchangeResult{value, err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-call.Done():
		return nil, call.Err()
	case response := <-result:
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if call.Err() != nil {
			return nil, call.Err()
		}
		if response.err != nil {
			return nil, errCommunicationsExchangeDenied
		}
		return response.value, nil
	}
}

func (e *communicationsExchange) authorizeAndPin(ctx context.Context, binding serviceplugins.DispatchBinding, agentID, operation string) (communicationsProviderConnection, context.Context, error) {
	if ctx.Err() != nil {
		return nil, nil, ctx.Err()
	}
	if err := e.authorizer.Authorize(ctx, binding, operation); err != nil {
		return nil, nil, errCommunicationsExchangeDenied
	}
	return e.sessions.pin(ctx, binding, agentID)
}

func (e *communicationsExchange) exchange(ctx context.Context, cancel context.CancelFunc, binding serviceplugins.DispatchBinding, agentID, operation string, args json.RawMessage) (json.RawMessage, error) {
	connection, lifetime, err := e.authorizeAndPin(ctx, binding, agentID, operation)
	if err != nil {
		return nil, err
	}
	stopBinding := context.AfterFunc(lifetime, cancel)
	defer stopBinding()
	stream, err := e.open(ctx, connection)
	if err != nil {
		if !nilCommunicationsValue(stream) {
			_ = stream.Close()
		}
		return nil, err
	}
	if nilCommunicationsValue(stream) {
		return nil, errCommunicationsExchangeDenied
	}
	var closeOnce sync.Once
	closeStream := func() { closeOnce.Do(func() { _ = stream.Close() }) }
	stopClose := context.AfterFunc(ctx, closeStream)
	defer func() { stopClose(); closeStream() }()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil, errCommunicationsExchangeDenied
	}
	if err = stream.SetDeadline(deadline); err != nil {
		return nil, err
	}
	id, err := uuid.NewRandom()
	if err != nil {
		return nil, err
	}
	request := protocol.CommunicationsEnvelope{Version: protocol.CommunicationsVersion, Kind: "request", RequestID: id.String(), TargetID: binding.TargetID, ProviderID: binding.ProviderID, SessionID: binding.SessionID, Generation: binding.Generation, Operation: operation, Arguments: args}
	if _, _, err = e.authorizeAndPin(ctx, binding, agentID, operation); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err = protocol.WriteCommunicationsEnvelope(stream, request, "request"); err != nil {
		return nil, err
	}
	reply, err := protocol.ReadCommunicationsEnvelope(stream, "reply")
	if err != nil {
		return nil, err
	}
	if !communicationsReplyMatches(request, reply) {
		return nil, errCommunicationsExchangeDenied
	}
	if _, _, err = e.authorizeAndPin(ctx, binding, agentID, operation); err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return append(json.RawMessage(nil), reply.Result...), nil
}

func communicationsReplyMatches(request, reply protocol.CommunicationsEnvelope) bool {
	return reply.Version == request.Version && reply.RequestID == request.RequestID && reply.TargetID == request.TargetID && reply.ProviderID == request.ProviderID && reply.SessionID == request.SessionID && reply.Generation == request.Generation && reply.Operation == request.Operation
}
