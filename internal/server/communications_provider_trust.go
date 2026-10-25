package server

import (
	"bytes"
	"context"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/qinyongliang/linksphere/internal/store"
	"golang.org/x/crypto/ssh"
)

// A grant is supplied by trusted startup configuration, never AgentHello or the
// ordinary user key store. It attests a deployed service key, not remote UID.
// Updating this immutable trust set requires closing the authority and bindings.
type communicationsRootGrant struct {
	agentID, targetID, providerID, organizationID, grantID string
	version                                                uint64
	publicKey                                              ssh.PublicKey
}
type communicationsRootTuple struct{ agentID, targetID, providerID string }
type communicationsRootConfig struct {
	enabled          bool
	hostSigner       ssh.Signer
	grants           []communicationsRootGrant
	maximum          int
	handshakeTimeout time.Duration
	// Must check the current organization-owned Agent target and exact Agent ID.
	lookupTarget func(context.Context, string) (store.SSHTarget, error)
}
type communicationsRootAuthority struct {
	mu           sync.Mutex
	closed       bool
	signer       ssh.Signer
	grants       map[communicationsRootTuple]communicationsRootGrant
	maximum      int
	timeout      time.Duration
	lookupTarget func(context.Context, string) (store.SSHTarget, error)
	active       map[*communicationsRootConnection]struct{}
}
type communicationsRootConnection struct {
	authority   *communicationsRootAuthority
	grant       communicationsRootGrant
	raw         net.Conn
	done        chan struct{}
	invalidated sync.Once
	// Set only after a complete signature-authenticated SSH handshake. Access is
	// under authority.mu, and membership is also required by the verifier.
	authenticated bool
	sshConnection *ssh.ServerConn
	exchangeLease *communicationsRootStream
}

func (c *communicationsRootConnection) CloseChan() <-chan struct{} { return c.done }
func (c *communicationsRootConnection) invalidate()                { c.invalidated.Do(func() { close(c.done) }) }

// close invalidates admission immediately; capacity remains charged until exit.
func (c *communicationsRootConnection) close() { c.invalidate(); c.raw.Close() }
func rootGrantTuple(g communicationsRootGrant) communicationsRootTuple {
	return communicationsRootTuple{g.agentID, g.targetID, g.providerID}
}
func rootTrustIdentifier(s string) bool {
	return communicationsIdentifier(s) && !strings.ContainsAny(s, "*?\t")
}
func newCommunicationsRootAuthority(c communicationsRootConfig) (*communicationsRootAuthority, error) {
	if !c.enabled {
		return nil, nil
	}
	if nilCommunicationsValue(c.hostSigner) || c.maximum < 1 || c.handshakeTimeout <= 0 || c.handshakeTimeout > time.Minute || c.lookupTarget == nil || len(c.grants) == 0 {
		return nil, errCommunicationsSessionDenied
	}
	a := &communicationsRootAuthority{signer: c.hostSigner, maximum: c.maximum, timeout: c.handshakeTimeout, lookupTarget: c.lookupTarget,
		grants: make(map[communicationsRootTuple]communicationsRootGrant), active: make(map[*communicationsRootConnection]struct{})}
	for _, g := range c.grants {
		if !rootTrustIdentifier(g.agentID) || !rootTrustIdentifier(g.targetID) || !rootTrustIdentifier(g.providerID) || !rootTrustIdentifier(g.organizationID) || !rootTrustIdentifier(g.grantID) || g.version == 0 || nilCommunicationsValue(g.publicKey) {
			return nil, errCommunicationsSessionDenied
		}
		key, err := ssh.ParsePublicKey(append([]byte(nil), g.publicKey.Marshal()...))
		if err != nil {
			return nil, errCommunicationsSessionDenied
		}
		if _, certificate := key.(*ssh.Certificate); certificate {
			return nil, errCommunicationsSessionDenied
		}
		g.publicKey = key // Do not retain caller-owned key buffers or the input slice.
		tuple := rootGrantTuple(g)
		if _, exists := a.grants[tuple]; exists {
			return nil, errCommunicationsSessionDenied
		}
		a.grants[tuple] = g
	}
	return a, nil
}

// accept owns raw, including on rejection. It must receive a dedicated provider
// transport, never an ordinary Agent transport. It opens no listener, creates no
// keys, and registers no provider. The active budget includes unfinished
// handshakes AND authenticated connections that have not yet been registered.
func (a *communicationsRootAuthority) accept(ctx context.Context, raw net.Conn, tuple communicationsRootTuple) (*communicationsRootConnection, error) {
	if nilCommunicationsValue(raw) {
		return nil, errCommunicationsSessionDenied
	}
	if a == nil || ctx == nil || ctx.Err() != nil {
		raw.Close()
		return nil, errCommunicationsSessionDenied
	}
	a.mu.Lock()
	grant, found := a.grants[tuple]
	if a.closed || !found || len(a.active) >= a.maximum {
		a.mu.Unlock()
		raw.Close()
		return nil, errCommunicationsSessionDenied
	}
	c := &communicationsRootConnection{authority: a, grant: grant, raw: raw, done: make(chan struct{})}
	a.active[c] = struct{}{}
	a.mu.Unlock()
	sshOwnsCleanup := false
	defer func() {
		if !sshOwnsCleanup {
			raw.Close()
			a.release(c)
		}
	}()
	handshake, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	deadline, _ := handshake.Deadline()
	if err := raw.SetDeadline(deadline); err != nil {
		return nil, errCommunicationsSessionDenied
	}
	watcherStop, watcherDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-handshake.Done():
			raw.Close()
		case <-watcherStop:
		}
	}()
	config := &ssh.ServerConfig{MaxAuthTries: 1, PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if meta.User() != grant.providerID || !bytes.Equal(key.Marshal(), grant.publicKey.Marshal()) {
			return nil, errCommunicationsSessionDenied
		}
		// This callback may be an unsigned probe. Never publish provenance here.
		return nil, nil
	}}
	config.AddHostKey(a.signer)
	conn, channels, requests, err := ssh.NewServerConn(raw, config)
	if conn != nil {
		sshOwnsCleanup = true
		a.trackSSH(c, conn, channels, requests)
	}
	close(watcherStop)
	<-watcherDone
	if err != nil || handshake.Err() != nil {
		if conn != nil {
			c.close()
		}
		return nil, errCommunicationsSessionDenied
	}
	if err := raw.SetDeadline(time.Time{}); err != nil {
		c.close()
		return nil, errCommunicationsSessionDenied
	}
	cancelled := ctx.Err() != nil
	a.mu.Lock()
	if a.closed || cancelled || communicationsChannelClosed(c.done) {
		a.mu.Unlock()
		c.close()
		return nil, errCommunicationsSessionDenied
	}
	c.sshConnection = conn
	c.authenticated = true
	a.mu.Unlock()
	return c, nil
}

func (a *communicationsRootAuthority) trackSSH(c *communicationsRootConnection, conn *ssh.ServerConn, channels <-chan ssh.NewChannel, requests <-chan *ssh.Request) {
	// Identity-only foundation: no command, forwarding, media or exchange channels
	// are enabled. Later exchange wiring must preserve this exact transport pin.
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for channels != nil || requests != nil {
			select {
			case channel, ok := <-channels:
				if !ok {
					channels = nil
				} else {
					channel.Reject(ssh.Prohibited, "provider exchange disabled")
				}
			case request, ok := <-requests:
				if !ok {
					requests = nil
				} else if request.WantReply {
					request.Reply(false, nil)
				}
			}
		}
	}()
	go func() { conn.Wait(); c.invalidate(); conn.Close(); <-drained; a.release(c) }()
}
func (a *communicationsRootAuthority) release(c *communicationsRootConnection) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, present := a.active[c]; present {
		delete(a.active, c)
		c.invalidate()
	}
}
func (a *communicationsRootAuthority) close() {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.closed = true
	connections := make([]net.Conn, 0, len(a.active))
	for c := range a.active {
		c.invalidate()
		connections = append(connections, c.raw)
	}
	a.mu.Unlock()
	for _, conn := range connections {
		conn.Close()
	}
}
func (a *communicationsRootAuthority) verifyProvider(ctx context.Context, candidate communicationsProviderCandidate) error {
	c, ok := candidate.connection.(*communicationsRootConnection)
	if a == nil || !ok || c == nil || c.authority != a || ctx == nil || ctx.Err() != nil ||
		candidate.agentID != c.grant.agentID || candidate.targetID != c.grant.targetID || candidate.providerID != c.grant.providerID {
		return errCommunicationsSessionDenied
	}
	a.mu.Lock()
	_, active := a.active[c]
	valid := active && !a.closed && c.authenticated
	a.mu.Unlock()
	if !valid || communicationsChannelClosed(c.done) {
		return errCommunicationsSessionDenied
	}
	target, err := a.lookupTarget(ctx, c.grant.targetID)
	if err != nil || ctx.Err() != nil || target.ID != c.grant.targetID || target.TargetType != store.TargetAgent || target.AgentID != c.grant.agentID || target.OwnerType != store.OwnerOrganization || target.OwnerID != c.grant.organizationID {
		return errCommunicationsSessionDenied
	}
	a.mu.Lock()
	_, active = a.active[c]
	valid = active && !a.closed && c.authenticated
	a.mu.Unlock()
	if !valid || communicationsChannelClosed(c.done) {
		return errCommunicationsSessionDenied
	}
	return nil
}
