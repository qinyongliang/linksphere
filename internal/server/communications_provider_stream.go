package server

import (
	"context"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const communicationsProviderChannel = "linksphere-communications-v1"

// Unregistered and opt-in. SSH possession is not Android UID attestation.
// Ordinary Agent transports and inbound provider channels remain unsupported.
func (a *communicationsRootAuthority) streamOpener(enabled bool) communicationsStreamOpener {
	if !enabled || a == nil {
		return nil
	}
	return func(ctx context.Context, connection communicationsProviderConnection) (communicationsExchangeStream, error) {
		c, ok := connection.(*communicationsRootConnection)
		if !ok || c == nil || c.authority != a || ctx == nil || ctx.Err() != nil {
			return nil, errCommunicationsExchangeDenied
		}
		deadline, bounded := ctx.Deadline()
		if !bounded || !deadline.After(time.Now()) || time.Until(deadline) > 5*time.Minute {
			return nil, errCommunicationsExchangeDenied
		}
		stream := &communicationsRootStream{connection: c, deadline: deadline, closed: make(chan struct{})}
		a.mu.Lock()
		_, active := a.active[c]
		if !active || a.closed || !c.authenticated || c.sshConnection == nil || c.exchangeLease != nil || communicationsChannelClosed(c.done) {
			a.mu.Unlock()
			return nil, errCommunicationsExchangeDenied
		}
		c.exchangeLease = stream
		transport := c.sshConnection
		a.mu.Unlock()
		stream.stopCancel = context.AfterFunc(ctx, stream.abortTransport)
		channel, requests, err := transport.OpenChannel(communicationsProviderChannel, nil)
		if err != nil {
			stream.release()
			return nil, errCommunicationsExchangeDenied
		}
		stream.channel = channel
		stream.drained = make(chan struct{})
		go func() {
			defer close(stream.drained)
			for request := range requests {
				if request.WantReply {
					_ = request.Reply(false, nil)
				}
			}
		}()
		if ctx.Err() != nil || communicationsChannelClosed(c.done) {
			_ = stream.Close()
			return nil, errCommunicationsExchangeDenied
		}
		return stream, nil
	}
}

type communicationsRootStream struct {
	connection *communicationsRootConnection
	channel    ssh.Channel
	drained    chan struct{}
	stopCancel func() bool
	mu         sync.Mutex
	deadline   time.Time
	timer      *time.Timer
	once       sync.Once
	closed     chan struct{}
}

func (s *communicationsRootStream) Read(p []byte) (int, error)  { return s.channel.Read(p) }
func (s *communicationsRootStream) Write(p []byte) (int, error) { return s.channel.Write(p) }

// A narrower deadline is allowed; extending or clearing the invocation budget is not.
func (s *communicationsRootStream) SetDeadline(deadline time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if deadline.IsZero() || deadline.After(s.deadline) || communicationsChannelClosed(s.closed) {
		return errCommunicationsExchangeDenied
	}
	s.deadline = deadline
	if s.timer != nil {
		s.timer.Stop()
	}
	s.timer = time.AfterFunc(time.Until(deadline), s.abortTransport)
	return nil
}

// Compare the exact lease while locked and invalidate before unlocking: an old
// timeout can never close a later operation on a successfully reused transport.
func (s *communicationsRootStream) abortTransport() {
	c := s.connection
	a := c.authority
	a.mu.Lock()
	current := c.exchangeLease == s
	if current {
		c.invalidate()
	}
	a.mu.Unlock()
	if current {
		_ = c.raw.Close()
	}
}

func (s *communicationsRootStream) release() {
	c := s.connection
	c.authority.mu.Lock()
	if c.exchangeLease == s {
		c.exchangeLease = nil
	}
	c.authority.mu.Unlock()
	if s.stopCancel != nil {
		s.stopCancel()
	}
	s.mu.Lock()
	if s.timer != nil {
		s.timer.Stop()
	}
	s.mu.Unlock()
}

func (s *communicationsRootStream) Close() error {
	s.once.Do(func() {
		// Keep cancellation/deadline armed until the peer's channel cleanup finishes.
		_ = s.channel.Close()
		<-s.drained
		close(s.closed)
		s.release()
	})
	return nil
}
