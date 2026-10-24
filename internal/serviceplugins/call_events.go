package serviceplugins

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"math"
	"regexp"
	"strings"
	"sync"
)

const CallEventCapacity = 128

var callEventID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// CallEvent is deliberately closed: no number, media path, raw error or payload.
// These are volatile observations, not proof of physical call/cleanup completion.
type CallEvent struct {
	Sequence  uint64 `json:"seq"`
	CallID    string `json:"call_id"`
	State     string `json:"state"`
	UpdatedAt int64  `json:"updated_at"`
}

// StreamID is a cursor namespace, never a credential or an authorization grant.
type CallEventCursor struct {
	StreamID string `json:"stream_id"`
	Sequence uint64 `json:"seq"`
}
type CallEventPage struct {
	Events []CallEvent     `json:"events"`
	Next   CallEventCursor `json:"next"`
	Lost   uint64          `json:"lost"`
}

// CallEventBuffer belongs to one full trusted binding, including its actor.
// Construction installs no handler/listener. The local owner must Close it at
// session teardown; closing the dispatcher also denies all subsequent reads/writes.
// It is not yet wired to the Manager transition sink or a remote transport.
type CallEventBuffer struct {
	mu         sync.Mutex
	dispatcher *LocalDispatcher
	stream     string
	entries    []CallEvent
	sequence   uint64
	closed     bool
}

func NewCallEventBuffer(d *LocalDispatcher) (*CallEventBuffer, error) {
	return newCallEventBuffer(d, CallEventCapacity)
}
func newCallEventBuffer(d *LocalDispatcher, capacity int) (*CallEventBuffer, error) {
	if d == nil || !d.live(d.binding) || !supportsCallEvents(d) || capacity < 1 || capacity > CallEventCapacity {
		return nil, ErrDispatchDenied
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	return &CallEventBuffer{dispatcher: d, stream: hex.EncodeToString(nonce[:]), entries: make([]CallEvent, capacity)}, nil
}
func supportsCallEvents(d *LocalDispatcher) bool {
	if d == nil {
		return false
	}
	events, ok := d.provider.(DispatchEventProvider)
	if !ok || nilInterface(events) {
		return false
	}
	for _, capability := range events.EventCapabilities() {
		if capability == "call" {
			return true
		}
	}
	return false
}

func validCallEvent(e CallEvent) bool {
	if e.Sequence != 0 || e.UpdatedAt <= 0 || !callEventID.MatchString(e.CallID) {
		return false
	}
	switch e.State {
	case "dialing", "waiting_for_answer", "playing", "completed", "failed", "cancelled":
		return true
	}
	return false
}

// Append is a trusted in-process publisher seam, never a remote input handler.
// The publisher must obtain binding from the owned operation, not user arguments.
func (b *CallEventBuffer) Append(binding DispatchBinding, e CallEvent) error {
	if b == nil || b.dispatcher == nil || !b.dispatcher.live(binding) || !supportsCallEvents(b.dispatcher) || !validCallEvent(e) {
		return ErrDispatchDenied
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.dispatcher.closed.Load() || b.sequence == math.MaxUint64 || len(b.entries) == 0 {
		return ErrDispatchDenied
	}
	// Validated short substrings may still reference a producer's large backing
	// string. Retain independent bounded strings, not that backing allocation.
	e.CallID = strings.Clone(e.CallID)
	e.State = strings.Clone(e.State)
	b.sequence++
	e.Sequence = b.sequence
	b.entries[(b.sequence-1)%uint64(len(b.entries))] = e
	return nil
}
func (b *CallEventBuffer) Read(ctx context.Context, binding DispatchBinding, cursor CallEventCursor, limit int) (CallEventPage, error) {
	empty := CallEventPage{}
	if b == nil || b.dispatcher == nil || limit < 1 || limit > CallEventCapacity {
		return empty, ErrDispatchDenied
	}
	if err := b.dispatcher.CheckEvents(ctx, binding, "call"); err != nil {
		return empty, err
	}
	page, err := b.snapshot(cursor, limit)
	if err != nil {
		return empty, err
	}
	// No authorizer/provider callback executes while holding the ring mutex.
	if err = b.dispatcher.CheckEvents(ctx, binding, "call"); err != nil {
		return empty, err
	}
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed || b.dispatcher.closed.Load() || ctx.Err() != nil {
		return empty, ErrDispatchDenied
	}
	return page, nil
}
func (b *CallEventBuffer) snapshot(cursor CallEventCursor, limit int) (CallEventPage, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || len(b.entries) == 0 || cursor.Sequence > b.sequence ||
		(cursor.StreamID != b.stream && !(cursor.StreamID == "" && cursor.Sequence == 0)) {
		return CallEventPage{}, ErrDispatchDenied
	}
	page := CallEventPage{Events: make([]CallEvent, 0, limit), Next: CallEventCursor{StreamID: b.stream, Sequence: cursor.Sequence}}
	oldest := uint64(1)
	if b.sequence > uint64(len(b.entries)) {
		oldest = b.sequence - uint64(len(b.entries)) + 1
	}
	after := cursor.Sequence
	if after < oldest-1 {
		page.Lost = oldest - 1 - after
		after = oldest - 1
		page.Next.Sequence = after
	}
	for after < b.sequence && len(page.Events) < limit {
		after++
		page.Events = append(page.Events, b.entries[(after-1)%uint64(len(b.entries))])
		page.Next.Sequence = after
	}
	return page, nil
}
func (b *CallEventBuffer) Close() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	clear(b.entries)
	b.entries = nil
}
