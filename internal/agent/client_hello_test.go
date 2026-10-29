package agent

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hashicorp/yamux"
	"github.com/qinyongliang/linksphere/internal/protocol"
	"github.com/qinyongliang/linksphere/internal/relay"
)

func helloTimeoutClient(t *testing.T, address string) *Client {
	t.Helper()
	c, err := New(Config{Server: address, IDFile: filepath.Join(t.TempDir(), "agent.json"), Root: t.TempDir(), DisableAutoUpdate: true})
	if err != nil {
		t.Fatal(err)
	}
	c.helloTimeout = 100 * time.Millisecond
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// A successful HTTP upgrade does not mean the application acknowledged our
// enrollment. Neither a silent peer nor an unfinished JSON line may strand Run.
func TestClientHelloTimeout(t *testing.T) {
	for _, partial := range []bool{false, true} {
		name := "silent"
		if partial {
			name = "partial"
		}
		t.Run(name, func(t *testing.T) {
			upgrader := websocket.Upgrader{}
			serverDone := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(serverDone)
				ws, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				conn := relay.NewWSConn(ws)
				defer conn.Close()
				if _, err := protocol.ReadJSONLine[protocol.AgentHello](bufio.NewReader(conn)); err != nil {
					return
				}
				if partial {
					_, _ = conn.Write([]byte(`{"ok":true`))
				}
				var b [1]byte
				_, _ = conn.Read(b[:])
			}))
			defer srv.Close()
			c := helloTimeoutClient(t, srv.URL)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- c.runOnce(ctx) }()
			select {
			case err := <-done:
				var timeout net.Error
				if !errors.As(err, &timeout) || !timeout.Timeout() {
					t.Fatalf("expected acknowledgement timeout, got %v", err)
				}
			case <-time.After(800 * time.Millisecond):
				cancel()
				<-done
				t.Fatal("application hello remained blocked after its handshake budget")
			}
			select {
			case <-serverDone:
			case <-time.After(time.Second):
				t.Fatal("timed-out transport remained open")
			}
		})
	}
}

func TestClientHelloDeadlineClearedForEstablishedSession(t *testing.T) {
	upgrader := websocket.Upgrader{}
	serverDone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(serverDone)
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conn := relay.NewWSConn(ws)
		defer conn.Close()
		if _, err = protocol.ReadJSONLine[protocol.AgentHello](bufio.NewReader(conn)); err != nil {
			return
		}
		if protocol.WriteJSONLine(conn, protocol.StreamResponse{OK: true}) != nil {
			return
		}
		session, err := yamux.Server(conn, nil)
		if err != nil {
			return
		}
		defer session.Close()
		<-session.CloseChan()
	}))
	defer srv.Close()
	c := helloTimeoutClient(t, srv.URL)
	connected := make(chan struct{}, 1)
	c.cfg.OnConnectionState = func(online bool) {
		if online {
			connected <- struct{}{}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.runOnce(ctx) }()
	select {
	case <-connected:
	case <-ctx.Done():
		t.Fatal("session never established")
	}
	select {
	case err := <-done:
		t.Fatalf("hello deadline expired an established session: %v", err)
	case <-time.After(3 * c.helloTimeout):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("established session ignored cancellation")
	}
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("established transport remained open")
	}
}

func TestClientRunRetriesAfterStalledHello(t *testing.T) {
	var attempts atomic.Int32
	upgrader := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conn := relay.NewWSConn(ws)
		defer conn.Close()
		if _, err = protocol.ReadJSONLine[protocol.AgentHello](bufio.NewReader(conn)); err != nil {
			return
		}
		if attempts.Add(1) == 1 {
			var b [1]byte
			_, _ = conn.Read(b[:])
			return
		}
		if protocol.WriteJSONLine(conn, protocol.StreamResponse{OK: true}) != nil {
			return
		}
		session, err := yamux.Server(conn, nil)
		if err != nil {
			return
		}
		defer session.Close()
		<-session.CloseChan()
	}))
	defer srv.Close()
	c := helloTimeoutClient(t, srv.URL)
	connected := make(chan struct{}, 1)
	c.cfg.OnConnectionState = func(online bool) {
		if online {
			select {
			case connected <- struct{}{}:
			default:
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Run(ctx) }()
	select {
	case <-connected:
		if attempts.Load() < 2 {
			t.Fatal("expected a fresh transport after hello timeout")
		}
	case <-ctx.Done():
		err := <-done
		t.Fatalf("stalled hello prevented reconnect: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not stop")
	}
}
