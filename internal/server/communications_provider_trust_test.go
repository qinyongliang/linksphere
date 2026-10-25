package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qinyongliang/linksphere/internal/store"
	"golang.org/x/crypto/ssh"
)

func rootSignerFixture(t *testing.T) ssh.Signer {
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
func rootConfigFixture(t *testing.T) (communicationsRootConfig, ssh.Signer) {
	t.Helper()
	signer := rootSignerFixture(t)
	return communicationsRootConfig{enabled: true, hostSigner: rootSignerFixture(t), maximum: 2, handshakeTimeout: time.Second,
		grants: []communicationsRootGrant{{agentID: "agent-fixture", targetID: "target-fixture", providerID: "provider-fixture", organizationID: "org-fixture", grantID: "grant-fixture", version: 1, publicKey: signer.PublicKey()}},
		lookupTarget: func(context.Context, string) (store.SSHTarget, error) {
			return store.SSHTarget{ID: "target-fixture", TargetType: store.TargetAgent, AgentID: "agent-fixture", OwnerType: store.OwnerOrganization, OwnerID: "org-fixture"}, nil
		}}, signer
}
func rootAuthorityFixture(t *testing.T, config communicationsRootConfig) *communicationsRootAuthority {
	t.Helper()
	a, err := newCommunicationsRootAuthority(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.close)
	return a
}
func rootSocketPairFixture(t *testing.T) (net.Conn, net.Conn) {
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

type rootAcceptResult struct {
	connection *communicationsRootConnection
	err        error
}

func rootHandshakeFixture(t *testing.T, a *communicationsRootAuthority, config communicationsRootConfig, signer ssh.Signer) (*communicationsRootConnection, *ssh.Client, error) {
	t.Helper()
	return rootHandshakeContextFixture(t, context.Background(), a, config, signer)
}
func rootHandshakeContextFixture(t *testing.T, ctx context.Context, a *communicationsRootAuthority, config communicationsRootConfig, signer ssh.Signer) (*communicationsRootConnection, *ssh.Client, error) {
	t.Helper()
	rawClient, rawServer := rootSocketPairFixture(t)
	done := make(chan rootAcceptResult, 1)
	go func() {
		c, err := a.accept(ctx, rawServer, rootGrantTuple(config.grants[0]))
		done <- rootAcceptResult{c, err}
	}()
	rawClient.SetDeadline(time.Now().Add(2 * time.Second))
	conn, channels, requests, clientErr := ssh.NewClientConn(rawClient, "fixture.invalid:22", &ssh.ClientConfig{User: config.grants[0].providerID,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: ssh.FixedHostKey(config.hostSigner.PublicKey())})
	result := providerSessionAwait(t, done)
	if clientErr != nil {
		if result.err == nil || result.connection != nil {
			t.Fatal("failed client authentication produced provenance")
		}
		return nil, nil, clientErr
	}
	if result.err != nil {
		conn.Close()
		t.Fatal(result.err)
	}
	rawClient.SetDeadline(time.Time{})
	client := ssh.NewClient(conn, channels, requests)
	t.Cleanup(func() { client.Close(); result.connection.close() })
	return result.connection, client, nil
}
func rootCandidateFixture(c *communicationsRootConnection) communicationsProviderCandidate {
	return communicationsProviderCandidate{agentID: c.grant.agentID, targetID: c.grant.targetID, providerID: c.grant.providerID, connection: c}
}
func TestCommunicationsRootDisabledAndInvalidConfiguration(t *testing.T) {
	if a, err := newCommunicationsRootAuthority(communicationsRootConfig{}); a != nil || err != nil {
		t.Fatal("disabled authority initialized")
	}
	for _, kind := range []string{"signer", "grants", "checker", "capacity", "timeout", "wildcard", "version", "duplicate", "key"} {
		t.Run(kind, func(t *testing.T) {
			c, _ := rootConfigFixture(t)
			switch kind {
			case "signer":
				c.hostSigner = nil
			case "grants":
				c.grants = nil
			case "checker":
				c.lookupTarget = nil
			case "capacity":
				c.maximum = 0
			case "timeout":
				c.handshakeTimeout = 0
			case "wildcard":
				c.grants[0].targetID = "*"
			case "version":
				c.grants[0].version = 0
			case "duplicate":
				c.grants = append(c.grants, c.grants[0])
			case "key":
				c.grants[0].publicKey = nil
			}
			if a, err := newCommunicationsRootAuthority(c); a != nil || err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}
func TestCommunicationsRootHandshakePinsIdentityAndRejectsExchange(t *testing.T) {
	config, signer := rootConfigFixture(t)
	a := rootAuthorityFixture(t, config)
	c, client, err := rootHandshakeFixture(t, a, config, signer)
	if err != nil {
		t.Fatal(err)
	}
	candidate := rootCandidateFixture(c)
	if err := a.verifyProvider(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	manager := providerSessionManagerFixture(t, a, 2)
	registration := providerSessionRegisterFixture(t, manager, candidate)
	if registration.agentID != config.grants[0].agentID {
		t.Fatal("wrong identity")
	}
	if _, _, err := client.OpenChannel("session", nil); err == nil {
		t.Fatal("identity foundation enabled command channel")
	}
	candidate.targetID = "different-target"
	if a.verifyProvider(context.Background(), candidate) == nil {
		t.Fatal("identity transferred across target")
	}
	candidate = rootCandidateFixture(c)
	other := rootAuthorityFixture(t, config)
	if other.verifyProvider(context.Background(), candidate) == nil {
		t.Fatal("identity transferred across authority")
	}
	a.close()
	if !communicationsChannelClosed(c.done) {
		t.Fatal("close did not synchronously invalidate provenance")
	}
	if a.verifyProvider(context.Background(), candidate) == nil {
		t.Fatal("closed authority verified")
	}
	if _, err := manager.current(providerSessionContextFixture("actor"), c.grant.targetID, c.grant.providerID); err == nil {
		t.Fatal("closed provider retained current binding")
	}
}

type rootProbeOnlySigner struct {
	public ssh.PublicKey
	signs  atomic.Int32
}

func (s *rootProbeOnlySigner) PublicKey() ssh.PublicKey { return s.public }
func (s *rootProbeOnlySigner) Sign(io.Reader, []byte) (*ssh.Signature, error) {
	s.signs.Add(1)
	return nil, errors.New("fixture has no private key")
}
func TestCommunicationsRootRequiresPrivateKeyProof(t *testing.T) {
	config, signer := rootConfigFixture(t)
	a := rootAuthorityFixture(t, config)
	probe := &rootProbeOnlySigner{public: signer.PublicKey()}
	if _, _, err := rootHandshakeFixture(t, a, config, probe); err == nil {
		t.Fatal("unsigned probe authenticated")
	}
	if probe.signs.Load() == 0 {
		t.Fatal("fixture never reached accepted public-key probe")
	}
	a.mu.Lock()
	active := len(a.active)
	a.mu.Unlock()
	if active != 0 {
		t.Fatal("failed handshake leaked capacity")
	}
	if _, _, err := rootHandshakeFixture(t, a, config, rootSignerFixture(t)); err == nil {
		t.Fatal("ordinary unapproved key authenticated")
	}
}
func TestCommunicationsRootOrdinaryConnectionCannotVerify(t *testing.T) {
	config, _ := rootConfigFixture(t)
	a := rootAuthorityFixture(t, config)
	if a.verifyProvider(context.Background(), providerSessionCandidateFixture(newProviderSessionConnectionFixture())) == nil {
		t.Fatal("ordinary connection trusted")
	}
	fake := &communicationsRootConnection{authority: a, grant: config.grants[0], done: make(chan struct{}), authenticated: true}
	if a.verifyProvider(context.Background(), rootCandidateFixture(fake)) == nil {
		t.Fatal("unowned wrapper trusted")
	}
}
func TestCommunicationsRootTargetOrganizationCheck(t *testing.T) {
	config, signer := rootConfigFixture(t)
	var denied atomic.Bool
	lookup := config.lookupTarget
	config.lookupTarget = func(ctx context.Context, id string) (store.SSHTarget, error) {
		target, err := lookup(ctx, id)
		if denied.Load() {
			target.OwnerID = "different-org"
		}
		return target, err
	}
	a := rootAuthorityFixture(t, config)
	c, _, err := rootHandshakeFixture(t, a, config, signer)
	if err != nil {
		t.Fatal(err)
	}
	denied.Store(true)
	if a.verifyProvider(context.Background(), rootCandidateFixture(c)) == nil {
		t.Fatal("changed target accepted at registration")
	}
}
func TestCommunicationsRootHandshakeCancellationReleasesCapacity(t *testing.T) {
	config, _ := rootConfigFixture(t)
	config.maximum = 1
	a := rootAuthorityFixture(t, config)
	_, raw := rootSocketPairFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan rootAcceptResult, 1)
	go func() {
		c, err := a.accept(ctx, raw, rootGrantTuple(config.grants[0]))
		done <- rootAcceptResult{c, err}
	}()
	// Observe the actual reservation, not a fixed scheduling delay.
	deadline := time.After(time.Second)
	for {
		a.mu.Lock()
		n := len(a.active)
		a.mu.Unlock()
		if n == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("handshake not admitted")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	_, extra := rootSocketPairFixture(t)
	if c, err := a.accept(context.Background(), extra, rootGrantTuple(config.grants[0])); c != nil || err == nil {
		t.Fatal("capacity exceeded")
	}
	cancel()
	result := providerSessionAwait(t, done)
	if result.err == nil || result.connection != nil {
		t.Fatal("cancelled handshake authenticated")
	}
	a.mu.Lock()
	n := len(a.active)
	a.mu.Unlock()
	if n != 0 {
		t.Fatal("cancelled handshake leaked capacity")
	}
}

func TestCommunicationsRootSuccessfulConnectionOutlivesCaller(t *testing.T) {
	config, signer := rootConfigFixture(t)
	config.maximum = 1
	a := rootAuthorityFixture(t, config)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c, _, err := rootHandshakeContextFixture(t, ctx, a, config, signer)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if communicationsChannelClosed(c.done) || a.verifyProvider(context.Background(), rootCandidateFixture(c)) != nil {
		t.Fatal("caller cancellation killed established identity")
	}
	_, extra := rootSocketPairFixture(t)
	if c, err := a.accept(context.Background(), extra, rootGrantTuple(config.grants[0])); c != nil || err == nil {
		t.Fatal("unregistered established connection escaped capacity")
	}
	c.close()
	if !communicationsChannelClosed(c.done) {
		t.Fatal("explicit close did not immediately invalidate")
	}
	c.close()
}
func TestCommunicationsRootTimeoutNeverPublishes(t *testing.T) {
	config, _ := rootConfigFixture(t)
	config.handshakeTimeout = 50 * time.Millisecond
	a := rootAuthorityFixture(t, config)
	_, raw := rootSocketPairFixture(t)
	done := make(chan rootAcceptResult, 1)
	go func() {
		c, err := a.accept(context.Background(), raw, rootGrantTuple(config.grants[0]))
		done <- rootAcceptResult{c, err}
	}()
	result := providerSessionAwait(t, done)
	if result.err == nil || result.connection != nil {
		t.Fatal("incomplete handshake accepted")
	}
	a.mu.Lock()
	n := len(a.active)
	a.mu.Unlock()
	if n != 0 {
		t.Fatal("timed out handshake retained slot after exit")
	}
}
func TestCommunicationsRootLookupMayCloseAuthority(t *testing.T) {
	config, signer := rootConfigFixture(t)
	lookup := config.lookupTarget
	var authority *communicationsRootAuthority
	config.lookupTarget = func(ctx context.Context, id string) (store.SSHTarget, error) {
		authority.close()
		return lookup(ctx, id)
	}
	authority = rootAuthorityFixture(t, config)
	c, _, err := rootHandshakeFixture(t, authority, config, signer)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- authority.verifyProvider(context.Background(), rootCandidateFixture(c)) }()
	if providerSessionAwait(t, done) == nil {
		t.Fatal("callback-closed authority accepted")
	}
}

type rootMutableKeyFixture struct {
	ssh.PublicKey
	encoded []byte
}

func (k *rootMutableKeyFixture) Marshal() []byte { return k.encoded }
func TestCommunicationsRootTrustCopiesCallerKeyBuffer(t *testing.T) {
	config, signer := rootConfigFixture(t)
	replacement := rootSignerFixture(t)
	mutable := &rootMutableKeyFixture{PublicKey: signer.PublicKey(), encoded: append([]byte(nil), signer.PublicKey().Marshal()...)}
	config.grants[0].publicKey = mutable
	a := rootAuthorityFixture(t, config)
	copy(mutable.encoded, replacement.PublicKey().Marshal())
	c, _, err := rootHandshakeFixture(t, a, config, signer)
	if err != nil {
		t.Fatal("caller mutation changed approved key:", err)
	}
	if a.verifyProvider(context.Background(), rootCandidateFixture(c)) != nil {
		t.Fatal("original key no longer trusted")
	}
	if _, _, err := rootHandshakeFixture(t, a, config, replacement); err == nil {
		t.Fatal("caller mutation installed replacement key")
	}
}
