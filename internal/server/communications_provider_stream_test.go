package server

import (
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestCommunicationsRootStreamDefaultsAndAuthority(t *testing.T) {
	config, signer := rootConfigFixture(t)
	a := rootAuthorityFixture(t, config)
	if a.streamOpener(false) != nil {
		t.Fatal("stream enabled by default")
	}
	c, _, err := rootHandshakeFixture(t, a, config, signer)
	if err != nil {
		t.Fatal(err)
	}
	other := rootAuthorityFixture(t, config)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if s, err := other.streamOpener(true)(ctx, c); s != nil || err == nil {
		t.Fatal("foreign authority accepted")
	}
	if s, err := a.streamOpener(true)(context.Background(), c); s != nil || err == nil {
		t.Fatal("unbounded context accepted")
	}
}

func TestCommunicationsRootStreamRoundTripAndCleanup(t *testing.T) {
	config, signer := rootConfigFixture(t)
	a := rootAuthorityFixture(t, config)
	c, client, err := rootHandshakeFixture(t, a, config, signer)
	if err != nil {
		t.Fatal(err)
	}
	incoming := client.HandleChannelOpen(communicationsProviderChannel)
	peerDone := make(chan error, 1)
	go func() {
		n := <-incoming
		channel, requests, err := n.Accept()
		if err != nil {
			peerDone <- err
			return
		}
		go ssh.DiscardRequests(requests)
		_, err = io.Copy(channel, channel)
		_ = channel.Close()
		peerDone <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream, err := a.streamOpener(true)(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	deadline, _ := ctx.Deadline()
	if err := stream.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if second, err := a.streamOpener(true)(ctx, c); second != nil || err == nil {
		t.Fatal("second inflight channel accepted")
	}
	if _, err = stream.Write([]byte("fixture")); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 7)
	if _, err = io.ReadFull(stream, b); err != nil || string(b) != "fixture" {
		t.Fatalf("round trip %q: %v", b, err)
	}
	if err = stream.Close(); err != nil {
		t.Fatal(err)
	}
	if err = stream.Close(); err != nil {
		t.Fatal(err)
	}
	if err = stream.SetDeadline(deadline); err == nil {
		t.Fatal("closed stream accepted deadline")
	}
	if err = providerSessionAwait(t, peerDone); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	busy := c.exchangeLease != nil
	a.mu.Unlock()
	if busy {
		t.Fatal("cleanup retained slot")
	}
	// The old operation cancellation must not invalidate the idle live connection.
	cancel()
	if communicationsChannelClosed(c.done) {
		t.Fatal("completed operation invalidated transport")
	}
}

func TestCommunicationsRootStreamOpenCancellationInvalidatesTransport(t *testing.T) {
	config, signer := rootConfigFixture(t)
	a := rootAuthorityFixture(t, config)
	c, client, err := rootHandshakeFixture(t, a, config, signer)
	if err != nil {
		t.Fatal(err)
	}
	incoming := client.HandleChannelOpen(communicationsProviderChannel)
	pending := make(chan struct{})
	go func() { <-incoming; close(pending) }() // Deliberately never accept or reject.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := a.streamOpener(true)(ctx, c); done <- err }()
	providerSessionAwait(t, pending)
	cancel()
	if err := providerSessionAwait(t, done); err == nil {
		t.Fatal("cancelled open succeeded")
	}
	if !communicationsChannelClosed(c.done) {
		t.Fatal("cancelled pinned transport remained valid")
	}
}

func TestCommunicationsRootStreamReuseIgnoresOldLease(t *testing.T) {
	config, signer := rootConfigFixture(t)
	a := rootAuthorityFixture(t, config)
	c, client, err := rootHandshakeFixture(t, a, config, signer)
	if err != nil {
		t.Fatal(err)
	}
	incoming := client.HandleChannelOpen(communicationsProviderChannel)
	peers := make(chan error, 2)
	go func() {
		for i := 0; i < 2; i++ {
			n := <-incoming
			ch, req, err := n.Accept()
			if err != nil {
				peers <- err
				continue
			}
			go ssh.DiscardRequests(req)
			go func() { _, err := io.Copy(ch, ch); _ = ch.Close(); peers <- err }()
		}
	}()
	firstContext, firstCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer firstCancel()
	first, err := a.streamOpener(true)(firstContext, c)
	if err != nil {
		t.Fatal(err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	secondContext, secondCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer secondCancel()
	second, err := a.streamOpener(true)(secondContext, c)
	if err != nil {
		t.Fatal(err)
	}
	firstCancel()
	first.(*communicationsRootStream).abortTransport()
	if communicationsChannelClosed(c.done) {
		t.Fatal("old lease invalidated new channel")
	}
	if _, err = second.Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 2)
	if _, err = io.ReadFull(second, b); err != nil || string(b) != "ok" {
		t.Fatalf("reused stream: %q %v", b, err)
	}
	if err = second.Close(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = providerSessionAwait(t, peers); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCommunicationsRootStreamCloseStallRemainsBounded(t *testing.T) {
	config, signer := rootConfigFixture(t)
	a := rootAuthorityFixture(t, config)
	c, client, gate, err := rootStreamGatedHandshake(t, a, config, signer)
	if err != nil {
		t.Fatal(err)
	}
	incoming := client.HandleChannelOpen(communicationsProviderChannel)
	accepted := make(chan ssh.Channel, 1)
	go func() {
		n := <-incoming
		ch, requests, err := n.Accept()
		if err != nil {
			accepted <- nil
			return
		}
		go ssh.DiscardRequests(requests)
		accepted <- ch
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream, err := a.streamOpener(true)(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	peer := providerSessionAwait(t, accepted)
	if peer == nil {
		t.Fatal("peer accept failed")
	}
	defer peer.Close()
	// The SSH library automatically acknowledges peer close. Stall the actual
	// transport write instead; deadline must unblock the close write and drain.
	gate.blockWrites.Store(true)
	if err = stream.SetDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- stream.Close() }()
	if err = providerSessionAwait(t, done); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil {
		t.Fatal("narrowed deadline failed; cleanup required original context expiry")
	}
	if !communicationsChannelClosed(c.done) {
		t.Fatal("stalled cleanup did not invalidate dedicated transport")
	}
	a.mu.Lock()
	busy := c.exchangeLease != nil
	a.mu.Unlock()
	if busy {
		t.Fatal("stalled cleanup retained lease")
	}
}

// A transport fixture whose blocked Write is released only by actual Close.
// This avoids pretending an application-level unclosed ssh.Channel suppresses ACK.
type rootStreamWriteGate struct {
	net.Conn
	blockWrites atomic.Bool
	closed      chan struct{}
	once        sync.Once
}

func (g *rootStreamWriteGate) Write(p []byte) (int, error) {
	if g.blockWrites.Load() {
		<-g.closed
		return 0, net.ErrClosed
	}
	return g.Conn.Write(p)
}
func (g *rootStreamWriteGate) Close() error {
	g.once.Do(func() { close(g.closed) })
	return g.Conn.Close()
}
func rootStreamGatedHandshake(t *testing.T, a *communicationsRootAuthority, config communicationsRootConfig, signer ssh.Signer) (*communicationsRootConnection, *ssh.Client, *rootStreamWriteGate, error) {
	t.Helper()
	rawClient, rawServer := rootSocketPairFixture(t)
	gate := &rootStreamWriteGate{Conn: rawServer, closed: make(chan struct{})}
	t.Cleanup(func() { _ = gate.Close() })
	accepted := make(chan rootAcceptResult, 1)
	go func() {
		c, err := a.accept(context.Background(), gate, rootGrantTuple(config.grants[0]))
		accepted <- rootAcceptResult{c, err}
	}()
	_ = rawClient.SetDeadline(time.Now().Add(2 * time.Second))
	conn, channels, requests, err := ssh.NewClientConn(rawClient, "fixture.invalid:22", &ssh.ClientConfig{User: config.grants[0].providerID, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.FixedHostKey(config.hostSigner.PublicKey())})
	acceptedResult := providerSessionAwait(t, accepted)
	if err != nil {
		return nil, nil, gate, err
	}
	if acceptedResult.err != nil {
		_ = conn.Close()
		return nil, nil, gate, acceptedResult.err
	}
	_ = rawClient.SetDeadline(time.Time{})
	client := ssh.NewClient(conn, channels, requests)
	t.Cleanup(func() { _ = client.Close(); acceptedResult.connection.close() })
	return acceptedResult.connection, client, gate, nil
}
