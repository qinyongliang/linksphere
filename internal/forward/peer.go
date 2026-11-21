package forward

import (
	"bufio"
	"context"
	"errors"
	"github.com/hashicorp/yamux"
	"github.com/qinyongliang/linksphere/internal/tunnel"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

// Peer multiplexes new upstream connections over one established negotiated path.
// The exit's dial function resolves immutable route IDs, never caller addresses.
type Peer struct {
	Session    *yamux.Session
	Stable     *tunnel.Conn
	negotiator *tunnel.Negotiator
}

func NewPeer(stream io.ReadWriteCloser, reader *bufio.Reader, entry bool, servers []string) (*Peer, error) {
	stable := tunnel.NewConn(&tunnel.Relay{Reader: reader, Writer: stream, Closer: stream})
	stable.KeepDirectOnRelayLoss()
	n := tunnel.NewNegotiator(stable, entry, servers)
	go stable.Run()
	go n.Run()
	cfg := yamux.DefaultConfig()
	cfg.LogOutput = io.Discard
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 5 * time.Second
	cfg.ConnectionWriteTimeout = 10 * time.Second
	var session *yamux.Session
	var err error
	if entry {
		session, err = yamux.Client(stable, cfg)
	} else {
		session, err = yamux.Server(stable, cfg)
	}
	if err != nil {
		n.Close()
		stable.Close()
		return nil, err
	}
	p := &Peer{Session: session, Stable: stable, negotiator: n}
	go func() { <-session.CloseChan(); n.Close(); stable.Close() }()
	return p, nil
}
func (p *Peer) Close() error {
	err := p.Session.Close()
	p.negotiator.Close()
	_ = p.Stable.Close()
	return err
}
func (p *Peer) Snapshot() tunnel.PeerInfo { return p.negotiator.Snapshot() }
func (p *Peer) Alive() bool {
	select {
	case <-p.Session.CloseChan():
		return false
	default:
		return true
	}
}
func (p *Peer) Dial(ctx context.Context, id string) (net.Conn, error) {
	return p.dial(ctx, id, "")
}

// DialTarget opens a stream for a dynamic route: the destination socket is
// chosen per connection by a proxy listener on the entry machine.
func (p *Peer) DialTarget(ctx context.Context, id, target string) (net.Conn, error) {
	return p.dial(ctx, id, target)
}

func (p *Peer) dial(ctx context.Context, id, target string) (net.Conn, error) {
	if len(id) > 128 || len(target) > 512 || strings.ContainsAny(target, "\r\n") {
		return nil, errors.New("invalid route ID or target")
	}
	s, err := p.Session.OpenStream()
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { s.Close() })
	defer stop()
	_ = s.SetDeadline(time.Now().Add(10 * time.Second))
	// Route IDs and dynamic targets are newline-free by validation.
	if _, err = io.WriteString(s, id+"\n"); err != nil {
		s.Close()
		return nil, err
	}
	if target != "" {
		if _, err = io.WriteString(s, target+"\n"); err != nil {
			s.Close()
			return nil, err
		}
	}
	r := bufio.NewReaderSize(s, 256)
	answer, err := r.ReadString('\n')
	if err != nil || answer != "ok\n" {
		s.Close()
		return nil, errors.New("upstream route unavailable")
	}
	_ = s.SetDeadline(time.Time{})
	return &peerConn{Stream: s, reader: r}, nil
}
// TargetDialFunc opens the destination socket. target is empty for static routes
// and carries the client-requested destination for dynamic proxy routes.
type TargetDialFunc func(ctx context.Context, id, target string) (net.Conn, error)

// Serve accepts upstream connection streams. dynamic reports which route IDs may
// carry a client-chosen destination on the line after the route ID.
func (p *Peer) Serve(ctx context.Context, dial TargetDialFunc, dynamic func(string) bool) {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer wg.Wait()
	defer cancel()
	stop := context.AfterFunc(ctx, func() { p.Close() })
	defer stop()
	for {
		s, err := p.Session.AcceptStream()
		if err != nil {
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer s.Close()
			_ = s.SetDeadline(time.Now().Add(10 * time.Second))
			r := bufio.NewReaderSize(s, 256)
			raw, err := r.ReadSlice('\n')
			id := string(raw)
			if err != nil || len(id) > 129 {
				return
			}
			routeID := id[:len(id)-1]
			target := ""
			if dynamic != nil && dynamic(routeID) {
				rawTarget, err := r.ReadSlice('\n')
				if err != nil || len(rawTarget) > 513 {
					return
				}
				target = string(rawTarget[:len(rawTarget)-1])
			}
			conn, err := dial(ctx, routeID, target)
			if err != nil {
				io.WriteString(s, "error\n")
				return
			}
			defer conn.Close()
			closeOnCancel := context.AfterFunc(ctx, func() { conn.Close(); s.Close() })
			defer closeOnCancel()
			if _, err := io.WriteString(s, "ok\n"); err != nil {
				return
			}
			_ = s.SetDeadline(time.Time{})
			done := make(chan struct{})
			go func() {
				_, _ = io.Copy(conn, r)
				if cw, ok := conn.(interface{ CloseWrite() error }); ok {
					_ = cw.CloseWrite()
				} else {
					conn.Close()
				}
				close(done)
			}()
			_, _ = io.Copy(s, conn)
			_ = s.Close()
			<-done
		}()
	}
}

type peerConn struct {
	*yamux.Stream
	reader *bufio.Reader
}

func (c *peerConn) Read(b []byte) (int, error) { return c.reader.Read(b) }
func (c *peerConn) CloseWrite() error          { return c.Stream.Close() }
func (c *peerConn) Close() error               { _ = c.Stream.SetReadDeadline(time.Now()); return c.Stream.Close() }
