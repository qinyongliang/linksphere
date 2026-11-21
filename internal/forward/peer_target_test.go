package forward

import (
	"bufio"
	"context"
	"io"
	"net"
	"testing"
	"time"
)

// TestPeerCarriesDynamicTarget proves a proxy route delivers the client-chosen
// destination to the exit machine instead of the route's fixed address.
func TestPeerCarriesDynamicTarget(t *testing.T) {
	// yamux needs a real duplex socket: net.Pipe is synchronous and deadlocks the
	// keep-alive writer.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	exitSide, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer exitSide.Close()
	entrySide, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer entrySide.Close()

	entryRace := make(chan *Peer, 1)
	exitRace := make(chan *Peer, 1)
	go func() {
		p, err := NewPeer(entrySide, bufio.NewReader(entrySide), true, nil)
		if err == nil {
			entryRace <- p
		}
	}()
	go func() {
		p, err := NewPeer(exitSide, bufio.NewReader(exitSide), false, nil)
		if err == nil {
			exitRace <- p
		}
	}()
	entry, exit := <-entryRace, <-exitRace
	if entry == nil || exit == nil {
		t.Fatal("peer handshake failed")
	}
	defer entry.Close()
	defer exit.Close()

	echo := echoServer(t)
	defer echo.Close()
	fixed := "127.0.0.1:9" // must never be dialled
	gotTarget := make(chan string, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go exit.Serve(ctx, func(ctx context.Context, id, target string) (net.Conn, error) {
		gotTarget <- target
		address := fixed
		if target != "" {
			address = target
		}
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "tcp", address)
	}, func(id string) bool { return id == "proxy:tcp" })

	conn, err := entry.DialTarget(ctx, "proxy:tcp", echo.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.WriteString(conn, "dynamic"); err != nil {
		t.Fatal(err)
	}
	echoed := make([]byte, len("dynamic"))
	if _, err := io.ReadFull(conn, echoed); err != nil || string(echoed) != "dynamic" {
		t.Fatalf("dynamic route round trip failed: %q %v", echoed, err)
	}
	if target := <-gotTarget; target != echo.Addr().String() {
		t.Fatalf("exit dialled %q instead of the client target", target)
	}
}
