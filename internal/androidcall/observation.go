package call

import (
	"context"
	"time"
)

// CallObservation excludes number, media identity/path and raw error details.
// It describes owned runtime state, not carrier confirmation or persisted history.
type CallObservation struct {
	ID        string
	State     State
	UpdatedAt time.Time
}
type callObserverKey struct{}

// RunMediaContextObserved is opt-in trusted local observation. Nil preserves the
// ordinary owned operation. A slow observer holds the owned operation busy; no
// detached queue is created. Observer failure must not prevent device cleanup.
func (manager *Manager) RunMediaContextObserved(ctx context.Context, request Request, mediaID string, media MediaFunc, observer func(CallObservation)) (*Call, error) {
	if ctx != nil && observer != nil {
		ctx = context.WithValue(ctx, callObserverKey{}, observer)
	}
	return manager.RunMediaContext(ctx, request, mediaID, media)
}
func (manager *Manager) observeOwned(ctx context.Context, call *Call) {
	observer, ok := ctx.Value(callObserverKey{}).(func(CallObservation))
	if !ok || observer == nil {
		return
	}
	manager.mu.Lock()
	snapshot := CallObservation{ID: call.ID, State: call.State, UpdatedAt: call.UpdatedAt}
	manager.mu.Unlock()
	// Trusted observers run outside manager.mu, so a bounded event-buffer append
	// cannot deadlock state reads. A panic is isolated without logging call data.
	func() {
		defer func() {
			if recover() != nil {
				manager.config.Logger.Printf("call observer failed")
			}
		}()
		observer(snapshot)
	}()
}
