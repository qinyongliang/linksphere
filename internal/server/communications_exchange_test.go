package server

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qinyongliang/linksphere/internal/protocol"
	"github.com/qinyongliang/linksphere/internal/serviceplugins"
)

type exchangeAuthorizerFixture func(context.Context, serviceplugins.DispatchBinding, string) error

func (f exchangeAuthorizerFixture) Authorize(c context.Context, b serviceplugins.DispatchBinding, o string) error {
	return f(c, b, o)
}
func exchangeFixture(t *testing.T, open communicationsStreamOpener, auth exchangeAuthorizerFixture, timeout time.Duration) (*communicationsExchange, serviceplugins.DispatchBinding, *providerSessionConnectionFixture) {
	t.Helper()
	m := providerSessionManagerFixture(t, providerSessionVerifierFixture(providerSessionAllow), 2)
	c := newProviderSessionConnectionFixture()
	r := providerSessionRegisterFixture(t, m, providerSessionCandidateFixture(c))
	b := serviceplugins.DispatchBinding{ActorID: "actor", TargetID: r.identity.TargetID, ProviderID: r.identity.ProviderID, SessionID: r.identity.SessionID, Generation: r.identity.Generation}
	if auth == nil {
		auth = func(context.Context, serviceplugins.DispatchBinding, string) error { return nil }
	}
	e, err := newCommunicationsExchange(m, auth, open, 1, timeout)
	if err != nil {
		t.Fatal(err)
	}
	return e, b, c
}
func pipeExchangeFixture(t *testing.T, mutate func(*protocol.CommunicationsEnvelope), seen chan<- protocol.CommunicationsEnvelope) communicationsStreamOpener {
	return func(context.Context, communicationsProviderConnection) (communicationsExchangeStream, error) {
		client, server := net.Pipe()
		t.Cleanup(func() { server.Close() })
		go func() {
			defer server.Close()
			request, err := protocol.ReadCommunicationsEnvelope(server, "request")
			if err != nil {
				return
			}
			if seen != nil {
				seen <- request
			}
			reply := request
			reply.Kind = "reply"
			reply.Arguments = nil
			reply.Result = json.RawMessage(`{"ok":true}`)
			if mutate != nil {
				mutate(&reply)
			}
			_ = protocol.WriteCommunicationsEnvelope(server, reply, "reply")
		}()
		return client, nil
	}
}
func awaitExchangeSlot(t *testing.T, e *communicationsExchange) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for len(e.slots) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(e.slots) != 0 {
		t.Fatal("slot not released after real completion")
	}
}
func TestCommunicationsExchangeSuccessAndMinimalContext(t *testing.T) {
	seen := make(chan protocol.CommunicationsEnvelope, 1)
	pipe := pipeExchangeFixture(t, nil, seen)
	var pinned communicationsProviderConnection
	type key struct{}
	open := func(ctx context.Context, c communicationsProviderConnection) (communicationsExchangeStream, error) {
		if ctx.Value(key{}) != nil {
			t.Error("retained arbitrary caller context")
		}
		if _, ok := communicationsSessionPrincipal(ctx); !ok {
			t.Error("lost private principal")
		}
		pinned = c
		return pipe(ctx, c)
	}
	var checks atomic.Int32
	e, b, c := exchangeFixture(t, open, func(context.Context, serviceplugins.DispatchBinding, string) error { checks.Add(1); return nil }, time.Second)
	result, err := e.invoke(context.WithValue(providerSessionContextFixture("actor"), key{}, make([]byte, 1024)), b, "agent-fixture", "call", json.RawMessage(`{"fixture":true}`))
	if err != nil || string(result) != `{"ok":true}` {
		t.Fatalf("result %s error %v", result, err)
	}
	if pinned != c || checks.Load() != 3 {
		t.Fatal("wrong pinned connection or reauthorization count")
	}
	request := <-seen
	if request.RequestID == "" || request.Generation != b.Generation {
		t.Fatal("missing internal correlation")
	}
	awaitExchangeSlot(t, e)
	if communicationsChannelClosed(c.done) {
		t.Fatal("closed parent connection")
	}
}
func TestCommunicationsExchangeCorrelationMismatch(t *testing.T) {
	cases := map[string]func(*protocol.CommunicationsEnvelope){"request": func(e *protocol.CommunicationsEnvelope) { e.RequestID = "other" }, "target": func(e *protocol.CommunicationsEnvelope) { e.TargetID = "other" }, "provider": func(e *protocol.CommunicationsEnvelope) { e.ProviderID = "other" }, "session": func(e *protocol.CommunicationsEnvelope) { e.SessionID = "other" }, "generation": func(e *protocol.CommunicationsEnvelope) { e.Generation++ }, "operation": func(e *protocol.CommunicationsEnvelope) { e.Operation = "sms" }}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			e, b, _ := exchangeFixture(t, pipeExchangeFixture(t, change, nil), nil, time.Second)
			r, err := e.invoke(providerSessionContextFixture("actor"), b, "agent-fixture", "call", json.RawMessage(`{}`))
			if err == nil || r != nil {
				t.Fatal("mismatched reply accepted")
			}
			awaitExchangeSlot(t, e)
		})
	}
}
func TestCommunicationsExchangeAuthorizationAtEveryBoundary(t *testing.T) {
	for _, deny := range []int32{1, 2, 3} {
		t.Run(string(rune('0'+deny)), func(t *testing.T) {
			var checks atomic.Int32
			var opens atomic.Int32
			pipe := pipeExchangeFixture(t, nil, nil)
			e, b, _ := exchangeFixture(t, func(c context.Context, p communicationsProviderConnection) (communicationsExchangeStream, error) {
				opens.Add(1)
				return pipe(c, p)
			}, func(context.Context, serviceplugins.DispatchBinding, string) error {
				if checks.Add(1) == deny {
					return errors.New("private authorization detail")
				}
				return nil
			}, time.Second)
			r, err := e.invoke(providerSessionContextFixture("actor"), b, "agent-fixture", "call", json.RawMessage(`{}`))
			if !errors.Is(err, errCommunicationsExchangeDenied) || r != nil {
				t.Fatal("authorization failure was exposed or ignored")
			}
			if deny == 1 && opens.Load() != 0 {
				t.Fatal("opened before authorization")
			}
			awaitExchangeSlot(t, e)
		})
	}
}

type lateExchangeStream struct {
	writes      atomic.Int32
	closes      atomic.Int32
	closeGate   <-chan struct{}
	deadlineErr error
}

func (s *lateExchangeStream) Read([]byte) (int, error)    { return 0, io.EOF }
func (s *lateExchangeStream) Write(p []byte) (int, error) { s.writes.Add(1); return len(p), nil }
func (s *lateExchangeStream) Close() error {
	s.closes.Add(1)
	if s.closeGate != nil {
		<-s.closeGate
	}
	return nil
}
func (s *lateExchangeStream) SetDeadline(time.Time) error { return s.deadlineErr }
func TestCommunicationsExchangeLateOpenerRetainsSlotAndNeverWrites(t *testing.T) {
	gate := make(chan struct{})
	started := make(chan struct{})
	stream := &lateExchangeStream{}
	e, b, c := exchangeFixture(t, func(context.Context, communicationsProviderConnection) (communicationsExchangeStream, error) {
		close(started)
		<-gate
		return stream, nil
	}, nil, 25*time.Millisecond)
	done := make(chan error, 1)
	args := json.RawMessage(`{"safe":true}`)
	go func() {
		_, err := e.invoke(providerSessionContextFixture("actor"), b, "agent-fixture", "call", args)
		done <- err
	}()
	providerSessionAwait(t, started)
	if err := providerSessionAwait(t, done); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	for i := range args {
		args[i] = 'x'
	}
	if _, err := e.invoke(providerSessionContextFixture("actor"), b, "agent-fixture", "call", json.RawMessage(`{}`)); !errors.Is(err, errCommunicationsExchangeDenied) {
		t.Fatal("timed out worker released capacity too early")
	}
	close(gate)
	awaitExchangeSlot(t, e)
	if stream.writes.Load() != 0 || stream.closes.Load() != 1 || communicationsChannelClosed(c.done) {
		t.Fatal("late stream lifecycle violated")
	}
}
func TestCommunicationsExchangeBlockedCloseRetainsSlotNotCaller(t *testing.T) {
	gate := make(chan struct{})
	stream := &lateExchangeStream{closeGate: gate, deadlineErr: errors.New("private transport detail")}
	e, b, _ := exchangeFixture(t, func(context.Context, communicationsProviderConnection) (communicationsExchangeStream, error) {
		return stream, nil
	}, nil, 25*time.Millisecond)
	done := make(chan error, 1)
	go func() {
		_, err := e.invoke(providerSessionContextFixture("actor"), b, "agent-fixture", "call", json.RawMessage(`{}`))
		done <- err
	}()
	if err := providerSessionAwait(t, done); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if len(e.slots) != 1 || stream.writes.Load() != 0 {
		t.Fatal("cleanup did not retain capacity")
	}
	close(gate)
	awaitExchangeSlot(t, e)
	if stream.closes.Load() != 1 {
		t.Fatal("multiple close calls")
	}
}

func TestCommunicationsExchangeInvalidInputsFailBeforeOpen(t *testing.T) {
	var opens atomic.Int32
	open := func(context.Context, communicationsProviderConnection) (communicationsExchangeStream, error) {
		opens.Add(1)
		return nil, errors.New("not expected")
	}
	e, b, _ := exchangeFixture(t, open, nil, time.Second)
	cancelled, cancel := context.WithCancel(providerSessionContextFixture("actor"))
	cancel()
	cases := []struct {
		ctx  context.Context
		op   string
		args json.RawMessage
	}{
		{nil, "call", json.RawMessage(`{}`)}, {cancelled, "call", json.RawMessage(`{}`)},
		{context.Background(), "call", json.RawMessage(`{}`)}, {providerSessionContextFixture("other"), "call", json.RawMessage(`{}`)},
		{providerSessionContextFixture("actor"), "events", json.RawMessage(`{}`)},
		{providerSessionContextFixture("actor"), "call", json.RawMessage(`[]`)}, {providerSessionContextFixture("actor"), "call", json.RawMessage(`null`)},
		{providerSessionContextFixture("actor"), "call", make([]byte, 65537)},
	}
	for _, c := range cases {
		if r, err := e.invoke(c.ctx, b, "agent-fixture", c.op, c.args); err == nil || r != nil {
			t.Fatal("invalid invocation accepted")
		}
	}
	if opens.Load() != 0 {
		t.Fatal("invalid inputs opened a stream")
	}
	awaitExchangeSlot(t, e)
	var zero communicationsExchange
	var absent *communicationsExchange
	for _, candidate := range []*communicationsExchange{&zero, absent} {
		if _, err := candidate.invoke(providerSessionContextFixture("actor"), b, "agent-fixture", "call", json.RawMessage(`{}`)); err == nil {
			t.Fatal("uninitialized adapter accepted")
		}
	}
	for _, maximum := range []int{0, 65} {
		if _, err := newCommunicationsExchange(e.sessions, e.authorizer, open, maximum, time.Second); err == nil {
			t.Fatal("invalid capacity")
		}
	}
	for _, timeout := range []time.Duration{0, 6 * time.Minute} {
		if _, err := newCommunicationsExchange(e.sessions, e.authorizer, open, 1, timeout); err == nil {
			t.Fatal("invalid deadline")
		}
	}
	var nilAuth exchangeAuthorizerFixture
	if _, err := newCommunicationsExchange(e.sessions, nilAuth, open, 1, time.Second); err == nil {
		t.Fatal("typed nil authorizer accepted")
	}
}

func TestCommunicationsExchangeRevocationAtBoundaries(t *testing.T) {
	for _, boundary := range []string{"before-open", "during-open", "before-write", "after-reply", "disconnect"} {
		t.Run(boundary, func(t *testing.T) {
			var e *communicationsExchange
			var b serviceplugins.DispatchBinding
			var connection *providerSessionConnectionFixture
			var checks atomic.Int32
			var opens atomic.Int32
			pipe := pipeExchangeFixture(t, nil, nil)
			auth := exchangeAuthorizerFixture(func(context.Context, serviceplugins.DispatchBinding, string) error {
				n := checks.Add(1)
				if (boundary == "before-open" && n == 1) || (boundary == "before-write" && n == 2) || (boundary == "after-reply" && n == 3) {
					e.sessions.revoke(b.TargetID, b.ProviderID)
				}
				return nil
			})
			open := func(ctx context.Context, c communicationsProviderConnection) (communicationsExchangeStream, error) {
				opens.Add(1)
				if boundary == "during-open" {
					e.sessions.revoke(b.TargetID, b.ProviderID)
				}
				if boundary == "disconnect" {
					connection.close()
				}
				return pipe(ctx, c)
			}
			e, b, connection = exchangeFixture(t, open, auth, time.Second)
			result, err := e.invoke(providerSessionContextFixture("actor"), b, "agent-fixture", "call", json.RawMessage(`{}`))
			if err == nil || result != nil {
				t.Fatal("revoked exchange accepted")
			}
			if boundary == "before-open" && opens.Load() != 0 {
				t.Fatal("opened revoked binding")
			}
			awaitExchangeSlot(t, e)
		})
	}
}

func TestCommunicationsExchangeBlockingAuthorizationRetainsCapacity(t *testing.T) {
	gate := make(chan struct{})
	entered := make(chan struct{})
	var opens atomic.Int32
	e, b, _ := exchangeFixture(t, func(context.Context, communicationsProviderConnection) (communicationsExchangeStream, error) {
		opens.Add(1)
		return &lateExchangeStream{}, nil
	}, func(context.Context, serviceplugins.DispatchBinding, string) error {
		close(entered)
		<-gate
		return nil
	}, 25*time.Millisecond)
	done := make(chan error, 1)
	go func() {
		_, err := e.invoke(providerSessionContextFixture("actor"), b, "agent-fixture", "call", json.RawMessage(`{}`))
		done <- err
	}()
	providerSessionAwait(t, entered)
	if err := providerSessionAwait(t, done); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if len(e.slots) != 1 {
		t.Fatal("released blocked authorizer")
	}
	close(gate)
	awaitExchangeSlot(t, e)
	if opens.Load() != 0 {
		t.Fatal("late authorization opened stream")
	}
}

type blockingDeadlineStream struct {
	lateExchangeStream
	gate    <-chan struct{}
	entered chan struct{}
}

func (s *blockingDeadlineStream) SetDeadline(time.Time) error { close(s.entered); <-s.gate; return nil }
func TestCommunicationsExchangeBlockingDeadlineRetainsCapacity(t *testing.T) {
	gate := make(chan struct{})
	defer func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	}()
	stream := &blockingDeadlineStream{gate: gate, entered: make(chan struct{})}
	e, b, _ := exchangeFixture(t, func(context.Context, communicationsProviderConnection) (communicationsExchangeStream, error) {
		return stream, nil
	}, nil, time.Second)
	ctx, cancel := context.WithCancel(providerSessionContextFixture("actor"))
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := e.invoke(ctx, b, "agent-fixture", "call", json.RawMessage(`{}`)); done <- err }()
	providerSessionAwait(t, stream.entered)
	cancel()
	if err := providerSessionAwait(t, done); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(e.slots) != 1 {
		t.Fatal("released blocked deadline callback")
	}
	close(gate)
	awaitExchangeSlot(t, e)
	if stream.writes.Load() != 0 || stream.closes.Load() != 1 {
		t.Fatal("late deadline caused write or duplicate close")
	}
}
func TestCommunicationsExchangeOpenerErrorOwnsReturnedStream(t *testing.T) {
	stream := &lateExchangeStream{}
	e, b, _ := exchangeFixture(t, func(context.Context, communicationsProviderConnection) (communicationsExchangeStream, error) {
		return stream, errors.New("sensitive transport detail")
	}, nil, time.Second)
	_, err := e.invoke(providerSessionContextFixture("actor"), b, "agent-fixture", "call", json.RawMessage(`{}`))
	if !errors.Is(err, errCommunicationsExchangeDenied) {
		t.Fatal("raw opener error exposed")
	}
	awaitExchangeSlot(t, e)
	if stream.closes.Load() != 1 || stream.writes.Load() != 0 {
		t.Fatal("error stream not owned")
	}
	var typedNil *lateExchangeStream
	e.open = func(context.Context, communicationsProviderConnection) (communicationsExchangeStream, error) {
		return typedNil, nil
	}
	if _, err = e.invoke(providerSessionContextFixture("actor"), b, "agent-fixture", "call", json.RawMessage(`{}`)); err == nil {
		t.Fatal("typed nil stream accepted")
	}
	awaitExchangeSlot(t, e)
}

type exchangeDeadlineRecorder struct {
	lateExchangeStream
	deadline time.Time
}

func (s *exchangeDeadlineRecorder) SetDeadline(d time.Time) error {
	s.deadline = d
	return errors.New("stop fixture before write")
}
func TestCommunicationsExchangeUsesAbsoluteEarlierCallerDeadline(t *testing.T) {
	stream := &exchangeDeadlineRecorder{}
	e, b, _ := exchangeFixture(t, func(context.Context, communicationsProviderConnection) (communicationsExchangeStream, error) {
		return stream, nil
	}, nil, time.Second)
	deadline := time.Now().Add(100 * time.Millisecond)
	ctx, cancel := context.WithDeadline(providerSessionContextFixture("actor"), deadline)
	defer cancel()
	_, _ = e.invoke(ctx, b, "agent-fixture", "call", json.RawMessage(`{}`))
	awaitExchangeSlot(t, e)
	if !stream.deadline.Equal(deadline) {
		t.Fatal("caller deadline shifted or extended")
	}
}
func TestCommunicationsExchangeBoundsIdentifiersBeforeCallbacks(t *testing.T) {
	var checks atomic.Int32
	e, b, _ := exchangeFixture(t, func(context.Context, communicationsProviderConnection) (communicationsExchangeStream, error) {
		t.Error("opened invalid binding")
		return nil, io.EOF
	}, func(context.Context, serviceplugins.DispatchBinding, string) error { checks.Add(1); return nil }, time.Second)
	for _, field := range []string{"actor", "target", "provider", "session", "generation", "agent"} {
		bad := b
		agent := "agent-fixture"
		long := string(make([]byte, 1024*1024))
		switch field {
		case "actor":
			bad.ActorID = long
		case "target":
			bad.TargetID = long
		case "provider":
			bad.ProviderID = long
		case "session":
			bad.SessionID = long
		case "generation":
			bad.Generation = 0
		case "agent":
			agent = long
		}
		if _, err := e.invoke(providerSessionContextFixture("actor"), bad, agent, "call", json.RawMessage(`{}`)); err == nil {
			t.Fatal("invalid binding accepted")
		}
	}
	if checks.Load() != 0 {
		t.Fatal("invalid identifiers reached callback")
	}
}
func TestCommunicationsExchangeReplacementDuringOpenNeverUsesReplacement(t *testing.T) {
	old := newProviderSessionConnectionFixture()
	replacement := newProviderSessionConnectionFixture()
	m := providerSessionManagerFixture(t, providerSessionVerifierFixture(providerSessionAllow), 2)
	r := providerSessionRegisterFixture(t, m, providerSessionCandidateFixture(old))
	b := serviceplugins.DispatchBinding{ActorID: "actor", TargetID: r.identity.TargetID, ProviderID: r.identity.ProviderID, SessionID: r.identity.SessionID, Generation: r.identity.Generation}
	stream := &lateExchangeStream{}
	var opens atomic.Int32
	e, err := newCommunicationsExchange(m, exchangeAuthorizerFixture(func(context.Context, serviceplugins.DispatchBinding, string) error { return nil }), func(ctx context.Context, c communicationsProviderConnection) (communicationsExchangeStream, error) {
		opens.Add(1)
		if c != old {
			t.Error("opener received replacement connection")
		}
		if _, err := m.register(context.Background(), providerSessionCandidateFixture(replacement)); err != nil {
			t.Error(err)
		}
		return stream, nil
	}, 1, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := e.invoke(providerSessionContextFixture("actor"), b, "agent-fixture", "call", json.RawMessage(`{}`))
	if err == nil || result != nil {
		t.Fatal("replacement accepted old exchange")
	}
	awaitExchangeSlot(t, e)
	if opens.Load() != 1 || stream.writes.Load() != 0 || stream.closes.Load() != 1 {
		t.Fatal("replacement rerouted or late write")
	}
	if communicationsChannelClosed(old.done) || communicationsChannelClosed(replacement.done) {
		t.Fatal("closed parent transport")
	}
}

func TestCommunicationsExchangeRejectsInvalidWireReplies(t *testing.T) {
	for _, kind := range []string{"version", "direction", "malformed", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			open := func(context.Context, communicationsProviderConnection) (communicationsExchangeStream, error) {
				client, server := net.Pipe()
				t.Cleanup(func() { server.Close() })
				go func() {
					defer server.Close()
					request, err := protocol.ReadCommunicationsEnvelope(server, "request")
					if err != nil {
						return
					}
					request.Kind = "reply"
					request.Arguments = nil
					request.Result = json.RawMessage(`{}`)
					if kind == "version" {
						request.Version++
					}
					if kind == "direction" {
						request.Kind = "request"
					}
					body, _ := json.Marshal(request)
					if kind == "malformed" {
						body = []byte(`{`)
					}
					var header [4]byte
					length := len(body)
					if kind == "oversized" {
						length = 1024*1024 + 8192 + 1
					}
					binary.BigEndian.PutUint32(header[:], uint32(length))
					_, _ = server.Write(header[:])
					if kind != "oversized" {
						_, _ = server.Write(body)
					}
				}()
				return client, nil
			}
			e, b, _ := exchangeFixture(t, open, nil, time.Second)
			r, err := e.invoke(providerSessionContextFixture("actor"), b, "agent-fixture", "call", json.RawMessage(`{}`))
			if err == nil || r != nil {
				t.Fatal("invalid wire reply accepted")
			}
			awaitExchangeSlot(t, e)
		})
	}
}

type failingExchangeWriter struct {
	lateExchangeStream
	zero bool
}

func (s *failingExchangeWriter) Write([]byte) (int, error) {
	s.writes.Add(1)
	if s.zero {
		return 0, nil
	}
	return 1, errors.New("private partial transmission detail")
}
func TestCommunicationsExchangeWriteFailureNeverRetries(t *testing.T) {
	for _, zero := range []bool{false, true} {
		stream := &failingExchangeWriter{zero: zero}
		var opens atomic.Int32
		e, b, _ := exchangeFixture(t, func(context.Context, communicationsProviderConnection) (communicationsExchangeStream, error) {
			opens.Add(1)
			return stream, nil
		}, nil, time.Second)
		r, err := e.invoke(providerSessionContextFixture("actor"), b, "agent-fixture", "call", json.RawMessage(`{}`))
		if !errors.Is(err, errCommunicationsExchangeDenied) || r != nil {
			t.Fatal("failed write accepted or exposed")
		}
		awaitExchangeSlot(t, e)
		if opens.Load() != 1 || stream.writes.Load() != 1 || stream.closes.Load() != 1 {
			t.Fatal("partial/zero write retried")
		}
	}
}
func TestCommunicationsExchangeCallerCancelAtReplyNeverReturnsValue(t *testing.T) {
	for i := 0; i < 20; i++ {
		ctx, cancel := context.WithCancel(providerSessionContextFixture("actor"))
		var checks atomic.Int32
		e, b, _ := exchangeFixture(t, pipeExchangeFixture(t, nil, nil), func(context.Context, serviceplugins.DispatchBinding, string) error {
			if checks.Add(1) == 3 {
				cancel()
			}
			return nil
		}, time.Second)
		r, err := e.invoke(ctx, b, "agent-fixture", "call", json.RawMessage(`{}`))
		cancel()
		if !errors.Is(err, context.Canceled) || r != nil {
			t.Fatal("canceled caller received success")
		}
		awaitExchangeSlot(t, e)
	}
}
