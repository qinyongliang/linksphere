package serviceplugins

import (
	"context"
	"encoding/json"
)

type callEventPublisherKey struct{}
type callEventPublisher struct {
	buffer  *CallEventBuffer
	binding DispatchBinding
}

// Invoke opts one owned call into this buffer. It still passes through the
// dispatcher's ordinary authorization, root/session and busy admission checks.
// Buffer closure suppresses later observations; only dispatcher Close cancels calls.
func (b *CallEventBuffer) Invoke(ctx context.Context, binding DispatchBinding, args json.RawMessage) (json.RawMessage, error) {
	if b == nil || b.dispatcher == nil || ctx == nil || ctx.Err() != nil || binding != b.dispatcher.binding {
		return nil, ErrDispatchDenied
	}
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed {
		return nil, ErrDispatchDenied
	}
	bound := context.WithValue(ctx, callEventPublisherKey{}, callEventPublisher{b, binding})
	return b.dispatcher.Invoke(bound, binding, "call", args)
}

// CallEventPublisher is for trusted local providers, not a remote registration API.
// No publisher is present on ordinary dispatcher invocations. The private key
// cannot be supplied in JSON/MCP arguments. Returned callbacks remain bounded by
// the original buffer's live binding and closure checks.
func CallEventPublisher(ctx context.Context) func(CallEvent) error {
	if ctx == nil {
		return nil
	}
	publisher, ok := ctx.Value(callEventPublisherKey{}).(callEventPublisher)
	if !ok || publisher.buffer == nil {
		return nil
	}
	return func(event CallEvent) error { return publisher.buffer.Append(publisher.binding, event) }
}
