package server

import (
	"context"
	"strings"
	"time"
)

type communicationsRequestLifetimeKey struct{}

// The SDK intentionally detaches HTTP cancellation for resumable MCP tools.
// Carry a separate, private lifetime only for bounded communications calls. It
// contains no request, credential or arbitrary request-context values.
func withCommunicationsRequestLifetime(ctx context.Context) (context.Context, func()) {
	var lifetime context.Context
	var cancel context.CancelFunc
	if deadline, ok := ctx.Deadline(); ok {
		lifetime, cancel = context.WithDeadline(context.Background(), deadline)
	} else {
		lifetime, cancel = context.WithCancel(context.Background())
	}
	stop := context.AfterFunc(ctx, cancel)
	if ctx.Err() != nil {
		cancel()
	}
	return context.WithValue(ctx, communicationsRequestLifetimeKey{}, lifetime), func() { stop(); cancel() }
}

func communicationsRequestLifetime(ctx context.Context) context.Context {
	lifetime, _ := ctx.Value(communicationsRequestLifetimeKey{}).(context.Context)
	return lifetime
}

// Worker context owns only the authenticated principal and bounded cancellation.
// Ordinary MCP tools never call this function and retain their SDK semantics.
func communicationsInvocationContext(ctx context.Context, principal communicationsPrincipal, maximum time.Duration) (context.Context, context.CancelFunc, func()) {
	principal.actorID, principal.tokenID = strings.Clone(principal.actorID), strings.Clone(principal.tokenID)
	base := context.WithValue(context.Background(), communicationsPrincipalKey{}, principal)
	deadline := time.Now().Add(maximum)
	lifetime := communicationsRequestLifetime(ctx)
	for _, parent := range []context.Context{ctx, lifetime} {
		if parent != nil {
			if d, ok := parent.Deadline(); ok && d.Before(deadline) {
				deadline = d
			}
		}
	}
	call, cancel := context.WithDeadline(base, deadline)
	stops := []func() bool{context.AfterFunc(ctx, cancel)}
	if lifetime != nil {
		stops = append(stops, context.AfterFunc(lifetime, cancel))
	}
	if ctx.Err() != nil || lifetime != nil && lifetime.Err() != nil {
		cancel()
	}
	return call, cancel, func() {
		for _, stop := range stops {
			stop()
		}
		cancel()
	}
}
