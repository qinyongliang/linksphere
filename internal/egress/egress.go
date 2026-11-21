// Package egress opens outbound sockets from an exit machine with an explicit
// egress selection: default host routing, a pinned interface, an IPv6 source
// allocation pool, or an upstream SOCKS5/HTTP CONNECT proxy.
//
// Nothing in this package listens on a port. It is used by the machine that
// opens the destination socket, which is the exit side of a forward.
package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"

	protocoltypes "github.com/qinyongliang/linksphere/internal/protocol/types"
)

// Dialer opens outbound connections for one egress selection.
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
	Spec() protocoltypes.EgressSpec
	Close() error
}

// New builds a dialer for spec. The pool argument carries the process-wide IPv6
// allocator; it may be nil when the spec does not select a pool.
func New(spec protocoltypes.EgressSpec, pools *Pools) (Dialer, error) {
	n := spec.Normalized()
	if err := n.Validate(); err != nil {
		return nil, err
	}
	if n.Mode == protocoltypes.EgressModeProxy {
		p, err := parseProxyURL(n.ProxyURL)
		if err != nil {
			return nil, err
		}
		return &proxyDialer{spec: n, proxy: p}, nil
	}
	d := &directDialer{spec: n}
	if n.IPv6Mode == protocoltypes.EgressIPv6Pool {
		if pools == nil {
			return nil, errors.New("IPv6 pool allocator required")
		}
		d.pool = pools.For(n.Key(), n)
	}
	return d, nil
}

// FromRoute builds a dialer from a route selection, defaulting to plain dialing.
func FromRoute(spec *protocoltypes.EgressSpec, pools *Pools) (Dialer, error) {
	if spec == nil {
		return &directDialer{spec: protocoltypes.EgressSpec{}.Normalized()}, nil
	}
	return New(*spec, pools)
}

// DialNetwork applies an immutable route grant through an egress dialer.
func DialNetwork(ctx context.Context, d Dialer, network, address string) (net.Conn, error) {
	return d.DialContext(ctx, network, address)
}

// proxyEndpoint is a parsed upstream proxy.
type proxyEndpoint struct {
	url      *url.URL
	scheme   string
	address  string
	username string
	password string
}

func parseProxyURL(raw string) (proxyEndpoint, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return proxyEndpoint{}, errors.New("invalid proxy_url")
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "socks5", "socks5h", "http", "https":
	default:
		return proxyEndpoint{}, fmt.Errorf("unsupported proxy scheme %q", u.Scheme)
	}
	port := u.Port()
	if port == "" {
		switch scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			port = "1080"
		}
	}
	p := proxyEndpoint{url: u, scheme: scheme, address: net.JoinHostPort(u.Hostname(), port)}
	if u.User != nil {
		p.username = u.User.Username()
		p.password, _ = u.User.Password()
	}
	return p, nil
}

// Pools keeps one IPv6 allocation sequence per (exit machine, pool key).
type Pools struct {
	mu    sync.Mutex
	byKey map[string]*pool
}

// NewPools returns an allocator registry.
func NewPools() *Pools { return &Pools{byKey: map[string]*pool{}} }

// For returns the sequence for a pool key, creating it on first use.
func (p *Pools) For(key string, spec protocoltypes.EgressSpec) *pool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if existing := p.byKey[key]; existing != nil {
		return existing
	}
	created := newPool(spec)
	p.byKey[key] = created
	return created
}
