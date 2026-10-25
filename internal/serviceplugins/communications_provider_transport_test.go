package serviceplugins

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func providerTransportAndroidFixture() Environment { return Environment{goos: "android", uid: 0} }
func providerTransportSignerFixture(t *testing.T) ssh.Signer {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}
func providerTransportConfigFixture(t *testing.T) (ProviderTransportConfig, ssh.Signer) {
	host := providerTransportSignerFixture(t)
	return ProviderTransportConfig{Enabled: true, ProviderID: "fixture-provider", ServerKey: host.PublicKey(), ServiceSigner: providerTransportSignerFixture(t), HandshakeTimeout: time.Second}, host
}
func providerTransportAwait[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(3 * time.Second):
		t.Fatal("fixture operation did not finish")
		var zero T
		return zero
	}
}

type providerTransportConnFixture struct {
	net.Conn
	closed              atomic.Bool
	failSet             bool
	failClear           bool
	writeEntered        chan struct{}
	writeOnce           sync.Once
	deadlineCalls       atomic.Int32
	onClear             func()
	blockWrites         atomic.Bool
	blockedWriteEntered chan struct{}
	blockedOnce         sync.Once
	closedSignal        chan struct{}
	closedOnce          sync.Once
}

func (c *providerTransportConnFixture) Write(p []byte) (int, error) {
	if c.blockWrites.Load() {
		c.blockedOnce.Do(func() { close(c.blockedWriteEntered) })
		<-c.closedSignal
		return 0, net.ErrClosed
	}
	if c.writeEntered != nil {
		c.writeOnce.Do(func() { close(c.writeEntered) })
	}
	return c.Conn.Write(p)
}
func (c *providerTransportConnFixture) Close() error {
	c.closed.Store(true)
	if c.closedSignal != nil {
		c.closedOnce.Do(func() { close(c.closedSignal) })
	}
	return c.Conn.Close()
}
func (c *providerTransportConnFixture) SetDeadline(deadline time.Time) error {
	c.deadlineCalls.Add(1)
	if deadline.IsZero() && c.onClear != nil {
		c.onClear()
	}
	if c.failSet || (c.failClear && deadline.IsZero()) {
		return errors.New("fixture deadline failure")
	}
	return c.Conn.SetDeadline(deadline)
}
func providerTransportPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	client, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	server, err := listener.Accept()
	if err != nil {
		client.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close(); server.Close() })
	return client, server
}

type providerTransportServerResult struct {
	conn *ssh.ServerConn
	err  error
}

func providerTransportServer(t *testing.T, raw net.Conn, host ssh.Signer, service ssh.PublicKey) <-chan providerTransportServerResult {
	t.Helper()
	done := make(chan providerTransportServerResult, 1)
	go func() {
		config := &ssh.ServerConfig{PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if meta.User() != "fixture-provider" || !bytes.Equal(key.Marshal(), service.Marshal()) {
				return nil, errors.New("fixture authentication denied")
			}
			return nil, nil
		}}
		config.AddHostKey(host)
		conn, channels, requests, err := ssh.NewServerConn(raw, config)
		if conn != nil {
			go ssh.DiscardRequests(requests)
			go func() {
				for channel := range channels {
					_ = channel.Reject(ssh.Prohibited, "fixture rejects client channels")
				}
			}()
		}
		done <- providerTransportServerResult{conn, err}
	}()
	return done
}

func TestProviderTransportDisabledDoesNotTouchEnvironmentOrConnection(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	raw := &providerTransportConnFixture{Conn: a}
	result, err := newProviderTransport(func() Environment { t.Fatal("disabled environment callback"); return Environment{} }, nil, raw, ProviderTransportConfig{})
	if result != nil || err != nil || raw.closed.Load() {
		t.Fatal("disabled transport performed work")
	}
}
func TestProviderTransportEnvironmentAndConfigurationFailClosed(t *testing.T) {
	base, _ := providerTransportConfigFixture(t)
	for _, kind := range []string{"linux", "non-root", "context", "id", "pin", "signer", "timeout", "certificate"} {
		t.Run(kind, func(t *testing.T) {
			config := base
			env := providerTransportAndroidFixture
			ctx := context.Background()
			switch kind {
			case "linux":
				env = func() Environment { return Environment{goos: "linux", uid: 0} }
			case "non-root":
				env = func() Environment { return Environment{goos: "android", uid: 1000} }
			case "context":
				ctx = nil
			case "id":
				config.ProviderID = "*"
			case "pin":
				config.ServerKey = nil
			case "signer":
				config.ServiceSigner = nil
			case "timeout":
				config.HandshakeTimeout = 2 * time.Minute
			case "certificate":
				config.ServerKey = &ssh.Certificate{Key: base.ServerKey}
			}
			a, b := net.Pipe()
			defer b.Close()
			raw := &providerTransportConnFixture{Conn: a}
			transport, err := newProviderTransport(env, ctx, raw, config)
			if transport != nil || err == nil || !raw.closed.Load() {
				t.Fatal("invalid transport accepted or not closed")
			}
		})
	}
}
func TestProviderTransportPinsHostAndRejectsEveryIncomingChannel(t *testing.T) {
	config, host := providerTransportConfigFixture(t)
	raw, peer := providerTransportPair(t)
	serverResult := providerTransportServer(t, peer, host, config.ServiceSigner.PublicKey())
	ctx, cancel := context.WithCancel(context.Background())
	transport, err := newProviderTransport(providerTransportAndroidFixture, ctx, raw, config)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	server := providerTransportAwait(t, serverResult)
	if server.err != nil {
		t.Fatal(server.err)
	}
	defer server.conn.Close()
	cancel() // Admission context must not become the successful transport lifetime.
	select {
	case <-transport.Done():
		t.Fatal("handshake context retained as lifetime")
	default:
	}
	if channel, _, err := server.conn.OpenChannel("linksphere-communications-v1", nil); err == nil {
		channel.Close()
		t.Fatal("dispatch channel accepted")
	}
	accepted, _, err := server.conn.SendRequest("fixture-request", true, nil)
	if err != nil || accepted {
		t.Fatalf("global request accepted or failed unexpectedly: %v", err)
	}
	var closing sync.WaitGroup
	for i := 0; i < 8; i++ {
		closing.Add(1)
		go func() { defer closing.Done(); _ = transport.Close() }()
	}
	closing.Wait()
	providerTransportAwait(t, transport.Done())
	providerTransportAwait(t, transport.drained)
}
func TestProviderTransportWrongPinsAndServiceKeysFail(t *testing.T) {
	for _, kind := range []string{"host", "service"} {
		t.Run(kind, func(t *testing.T) {
			config, host := providerTransportConfigFixture(t)
			allowed := config.ServiceSigner.PublicKey()
			if kind == "host" {
				config.ServerKey = providerTransportSignerFixture(t).PublicKey()
			} else {
				config.ServiceSigner = providerTransportSignerFixture(t)
			}
			raw, peer := providerTransportPair(t)
			wrapped := &providerTransportConnFixture{Conn: raw}
			result := providerTransportServer(t, peer, host, allowed)
			transport, err := newProviderTransport(providerTransportAndroidFixture, context.Background(), wrapped, config)
			if transport != nil || err == nil || !wrapped.closed.Load() {
				t.Fatal("incorrect pin or service key accepted")
			}
			server := providerTransportAwait(t, result)
			if server.conn != nil {
				server.conn.Close()
			}
		})
	}
}
func TestProviderTransportHandshakeCancellationClosesOwnedConnection(t *testing.T) {
	config, _ := providerTransportConfigFixture(t)
	raw, peer := net.Pipe()
	defer peer.Close()
	entered := make(chan struct{})
	wrapped := &providerTransportConnFixture{Conn: raw, writeEntered: entered}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := newProviderTransport(providerTransportAndroidFixture, ctx, wrapped, config)
		done <- err
	}()
	providerTransportAwait(t, entered)
	cancel()
	if err := providerTransportAwait(t, done); err == nil || !wrapped.closed.Load() {
		t.Fatal("cancelled handshake survived")
	}
}
func TestProviderTransportDeadlineFailuresCloseOwnedConnection(t *testing.T) {
	config, host := providerTransportConfigFixture(t)
	a, b := net.Pipe()
	defer b.Close()
	first := &providerTransportConnFixture{Conn: a, failSet: true}
	if transport, err := newProviderTransport(providerTransportAndroidFixture, context.Background(), first, config); transport != nil || err == nil || !first.closed.Load() {
		t.Fatal("SetDeadline failure accepted")
	}
	raw, peer := providerTransportPair(t)
	clear := &providerTransportConnFixture{Conn: raw, failClear: true}
	result := providerTransportServer(t, peer, host, config.ServiceSigner.PublicKey())
	if transport, err := newProviderTransport(providerTransportAndroidFixture, context.Background(), clear, config); transport != nil || err == nil || !clear.closed.Load() {
		t.Fatal("clear deadline failure accepted")
	}
	server := providerTransportAwait(t, result)
	if server.conn != nil {
		server.conn.Close()
	}
}

type providerTransportWrappedKey struct{ ssh.PublicKey }
type providerTransportWrappedSigner struct {
	ssh.Signer
	public ssh.PublicKey
}

func (s providerTransportWrappedSigner) PublicKey() ssh.PublicKey { return s.public }

func TestProviderTransportWrappedCertificatesRejectedBeforeNetwork(t *testing.T) {
	config, host := providerTransportConfigFixture(t)
	certificate := &ssh.Certificate{Key: host.PublicKey(), CertType: ssh.HostCert, ValidBefore: ssh.CertTimeInfinity}
	if err := certificate.SignCert(rand.Reader, host); err != nil {
		t.Fatal(err)
	}
	wrapped := providerTransportWrappedKey{certificate}
	for _, kind := range []string{"host", "signer"} {
		t.Run(kind, func(t *testing.T) {
			current := config
			if kind == "host" {
				current.ServerKey = wrapped
			} else {
				current.ServiceSigner = providerTransportWrappedSigner{config.ServiceSigner, wrapped}
			}
			raw, peer := net.Pipe()
			defer peer.Close()
			observed := &providerTransportConnFixture{Conn: raw}
			transport, err := newProviderTransport(providerTransportAndroidFixture, context.Background(), observed, current)
			if transport != nil || err == nil || !observed.closed.Load() || observed.deadlineCalls.Load() != 0 {
				t.Fatal("wrapped certificate reached network admission")
			}
		})
	}
}
func TestProviderTransportTypedNilInputsRejected(t *testing.T) {
	config, _ := providerTransportConfigFixture(t)
	var noRaw *providerTransportConnFixture
	if transport, err := newProviderTransport(providerTransportAndroidFixture, context.Background(), noRaw, config); transport != nil || err == nil {
		t.Fatal("typed nil raw accepted")
	}
	var noKey *providerTransportWrappedKey
	var noSigner *providerTransportWrappedSigner
	for _, kind := range []string{"key", "signer", "signer-public"} {
		t.Run(kind, func(t *testing.T) {
			current := config
			switch kind {
			case "key":
				current.ServerKey = noKey
			case "signer":
				current.ServiceSigner = noSigner
			case "signer-public":
				current.ServiceSigner = providerTransportWrappedSigner{config.ServiceSigner, noKey}
			}
			raw, peer := net.Pipe()
			defer peer.Close()
			observed := &providerTransportConnFixture{Conn: raw}
			if transport, err := newProviderTransport(providerTransportAndroidFixture, context.Background(), observed, current); transport != nil || err == nil || !observed.closed.Load() {
				t.Fatal("typed nil key or signer accepted")
			}
		})
	}
}
func TestProviderTransportNaturalHandshakeTimeoutClosesBlockedWrite(t *testing.T) {
	config, _ := providerTransportConfigFixture(t)
	config.HandshakeTimeout = 80 * time.Millisecond
	raw, peer := net.Pipe()
	defer peer.Close()
	entered := make(chan struct{})
	observed := &providerTransportConnFixture{Conn: raw, writeEntered: entered}
	done := make(chan error, 1)
	go func() {
		_, err := newProviderTransport(providerTransportAndroidFixture, context.Background(), observed, config)
		done <- err
	}()
	providerTransportAwait(t, entered)
	if err := providerTransportAwait(t, done); err == nil || !observed.closed.Load() {
		t.Fatal("natural handshake timeout failed")
	}
}
func TestProviderTransportCancelDuringDeadlineClearRejectsSuccess(t *testing.T) {
	config, host := providerTransportConfigFixture(t)
	raw, peer := providerTransportPair(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observed := &providerTransportConnFixture{Conn: raw, onClear: cancel}
	result := providerTransportServer(t, peer, host, config.ServiceSigner.PublicKey())
	if transport, err := newProviderTransport(providerTransportAndroidFixture, ctx, observed, config); transport != nil || err == nil || !observed.closed.Load() {
		t.Fatal("cancelled deadline-clear boundary returned success")
	}
	server := providerTransportAwait(t, result)
	if server.conn != nil {
		server.conn.Close()
	}
}
func TestProviderTransportCloseUnblocksRejectedReplyAndCompletesDrain(t *testing.T) {
	config, host := providerTransportConfigFixture(t)
	raw, peer := providerTransportPair(t)
	observed := &providerTransportConnFixture{Conn: raw, closedSignal: make(chan struct{}), blockedWriteEntered: make(chan struct{})}
	result := providerTransportServer(t, peer, host, config.ServiceSigner.PublicKey())
	transport, err := newProviderTransport(providerTransportAndroidFixture, context.Background(), observed, config)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	server := providerTransportAwait(t, result)
	if server.err != nil {
		t.Fatal(server.err)
	}
	defer server.conn.Close()
	observed.blockWrites.Store(true)
	requestDone := make(chan error, 1)
	go func() { _, _, err := server.conn.SendRequest("blocked-rejection", true, nil); requestDone <- err }()
	providerTransportAwait(t, observed.blockedWriteEntered)
	_ = transport.Close()
	providerTransportAwait(t, transport.Done())
	providerTransportAwait(t, transport.drained)
	if err := providerTransportAwait(t, requestDone); err == nil {
		t.Fatal("blocked reply unexpectedly completed as normal")
	}
}
