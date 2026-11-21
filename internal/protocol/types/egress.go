// Package types contains transport/configuration DTOs only. It must not depend
// on the server, Agent, persistence backend or forwarding data plane.
package types

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Egress modes. Direct opens sockets from the exit node itself; Proxy tunnels the
// outbound connection through an upstream SOCKS5/HTTP proxy reachable from the
// exit node.
const (
	EgressModeDirect = "direct"
	EgressModeProxy  = "proxy"
)

// Address families requested for the outbound connection.
const (
	EgressFamilyAuto = "auto"
	EgressFamilyIPv4 = "ipv4"
	EgressFamilyIPv6 = "ipv6"
	EgressFamilyBoth = "both"
)

// IPv6 source strategies.
const (
	EgressIPv6Host      = "host"
	EgressIPv6Interface = "interface"
	EgressIPv6Pool      = "pool"
)

// Pool allocation strategies.
const (
	EgressPoolRotate = "rotate"
	EgressPoolSticky = "sticky"
)

// Listen protocols. Auto sniffs the first bytes so one port serves SOCKS5,
// SOCKS4 and HTTP proxy clients (CONNECT for HTTPS) as well as raw TCP.
const (
	ListenProtocolAuto  = "auto"
	ListenProtocolTCP   = "tcp"
	ListenProtocolHTTP  = "http"
	ListenProtocolSOCKS = "socks"
)

// invalidNameChars rejects spaces, separators and control characters in an
// interface name without embedding escapes that differ per platform.
const invalidNameChars = " /\\\t\r\n\x00"

// EgressSpec selects how the exit node opens outbound sockets. The zero value is
// the default: host routing table, dual stack, no source binding.
type EgressSpec struct {
	Mode      string `json:"mode,omitempty"`
	Family    string `json:"family,omitempty"`
	Interface string `json:"interface,omitempty"`
	IPv6Mode  string `json:"ipv6_mode,omitempty"`
	PoolStart string `json:"pool_start,omitempty"`
	PoolSize  int    `json:"pool_size,omitempty"`
	PoolMode  string `json:"pool_mode,omitempty"`
	ProxyURL  string `json:"proxy_url,omitempty"`
}

// Normalized fills defaults so callers never branch on empty strings.
func (e EgressSpec) Normalized() EgressSpec {
	out := e
	if out.Mode == "" {
		out.Mode = EgressModeDirect
	}
	if out.Family == "" {
		out.Family = EgressFamilyAuto
	}
	if out.IPv6Mode == "" {
		out.IPv6Mode = EgressIPv6Host
	}
	if out.PoolMode == "" {
		out.PoolMode = EgressPoolRotate
	}
	out.Interface = strings.TrimSpace(out.Interface)
	out.PoolStart = strings.TrimSpace(out.PoolStart)
	out.ProxyURL = strings.TrimSpace(out.ProxyURL)
	return out
}

// IsDefault reports whether the spec means "host routing table, dual stack".
func (e EgressSpec) IsDefault() bool {
	n := e.Normalized()
	return n.Mode == EgressModeDirect && n.Family == EgressFamilyAuto && n.Interface == "" &&
		n.IPv6Mode == EgressIPv6Host && n.PoolStart == ""
}

// WantsIPv6 reports whether the rule asks for IPv6 egress at all.
func (e EgressSpec) WantsIPv6() bool {
	switch e.Normalized().Family {
	case EgressFamilyIPv6, EgressFamilyBoth:
		return true
	}
	return false
}

// Validate rejects configurations the data plane cannot honour. It is deliberately
// free of platform knowledge: unsupported combinations are reported by the exit
// node at runtime.
func (e EgressSpec) Validate() error {
	n := e.Normalized()
	switch n.Mode {
	case EgressModeDirect:
	case EgressModeProxy:
		if n.ProxyURL == "" {
			return errors.New("proxy egress requires proxy_url")
		}
		u, err := url.Parse(n.ProxyURL)
		if err != nil {
			return errors.New("invalid proxy_url")
		}
		switch strings.ToLower(u.Scheme) {
		case "socks5", "socks5h", "http", "https":
		default:
			return errors.New("proxy_url scheme must be socks5, socks5h, http or https")
		}
		if u.Host == "" {
			return errors.New("proxy_url requires a host")
		}
	default:
		return errors.New("egress mode must be direct or proxy")
	}
	if n.Mode == EgressModeProxy && (n.Interface != "" || n.PoolStart != "" || n.IPv6Mode != EgressIPv6Host) {
		return errors.New("proxy egress cannot combine with interface or IPv6 pool selection")
	}
	switch n.Family {
	case EgressFamilyAuto, EgressFamilyIPv4, EgressFamilyIPv6, EgressFamilyBoth:
	default:
		return errors.New("egress family must be auto, ipv4, ipv6 or both")
	}
	if len(n.Interface) > 64 || strings.ContainsAny(n.Interface, invalidNameChars) {
		return errors.New("invalid egress interface name")
	}
	switch n.IPv6Mode {
	case EgressIPv6Host, EgressIPv6Interface:
		if n.PoolStart != "" || n.PoolSize != 0 {
			return errors.New("IPv6 pool fields require ipv6_mode pool")
		}
	case EgressIPv6Pool:
		if n.Family == EgressFamilyIPv4 {
			return errors.New("IPv6 pool requires an IPv6 address family")
		}
		ip := net.ParseIP(n.PoolStart)
		if ip == nil || ip.To4() != nil {
			return errors.New("pool_start must be an IPv6 address")
		}
		if n.PoolSize < 1 || n.PoolSize > 65536 {
			return errors.New("pool_size must be between 1 and 65536")
		}
		switch n.PoolMode {
		case EgressPoolRotate, EgressPoolSticky:
		default:
			return errors.New("pool_mode must be rotate or sticky")
		}
	default:
		return errors.New("ipv6_mode must be host, interface or pool")
	}
	if n.IPv6Mode == EgressIPv6Interface && n.Family == EgressFamilyIPv4 {
		return errors.New("ipv6_mode interface requires an IPv6 address family")
	}
	return nil
}

// Key identifies one allocation pool on one exit node.
func (e EgressSpec) Key() string {
	n := e.Normalized()
	if n.IPv6Mode != EgressIPv6Pool {
		return ""
	}
	return n.PoolStart + "/" + n.PoolMode
}

// PortRule is one listen port of a port-forwarding configuration. A tunnel with
// several rules listens on several ports, each with its own transports,
// destination and egress selection.
type PortRule struct {
	ID              string      `json:"id"`
	Name            string      `json:"name,omitempty"`
	ListenHost      string      `json:"listen_host,omitempty"`
	ListenPort      int         `json:"listen_port"`
	// ListenPortEnd turns the rule into an inclusive port range such as
	// 10000-11000. Zero means the rule listens on ListenPort only.
	ListenPortEnd int `json:"listen_port_end,omitempty"`
	// Protocol selects how the listen port speaks. Empty or "auto" sniffs the
	// client bytes; "tcp" is a raw stream to Destination; "http" and "socks"
	// restrict the listener to that proxy protocol.
	Protocol        string      `json:"protocol,omitempty"`
	Transports      []string    `json:"transports,omitempty"`
	DestinationHost string      `json:"destination_host,omitempty"`
	DestinationPort int         `json:"destination_port,omitempty"`
	Egress          *EgressSpec `json:"egress,omitempty"`
}

// MaxRangePorts caps one rule's port range so a typo cannot try to bind the
// whole ephemeral range.
const MaxRangePorts = 1024

// NormalizedListenProtocol returns the effective listen protocol.
func (r PortRule) NormalizedListenProtocol() string {
	switch r.Protocol {
	case ListenProtocolTCP, ListenProtocolHTTP, ListenProtocolSOCKS:
		return r.Protocol
	}
	return ListenProtocolAuto
}

// ValidateProtocol rejects an unknown listen protocol.
func (r PortRule) ValidateProtocol() error {
	switch r.Protocol {
	case "", ListenProtocolAuto, ListenProtocolTCP, ListenProtocolHTTP, ListenProtocolSOCKS:
		return nil
	}
	return errors.New("listen protocol must be auto, tcp, http or socks")
}

// ProxiesClients reports whether the listener accepts client-chosen destinations
// (SOCKS/HTTP proxy) instead of always streaming to Destination.
func (r PortRule) ProxiesClients() bool {
	switch r.NormalizedListenProtocol() {
	case ListenProtocolAuto, ListenProtocolHTTP, ListenProtocolSOCKS:
		return true
	}
	return false
}

// PortRange returns the first and last listen port of the rule.
func (r PortRule) PortRange() (int, int) {
	first, last := r.ListenPort, r.ListenPort
	if r.ListenPortEnd > r.ListenPort {
		last = r.ListenPortEnd
	}
	return first, last
}

// PortCount returns how many listen ports the rule binds.
func (r PortRule) PortCount() int {
	first, last := r.PortRange()
	return last - first + 1
}

// ValidateRange checks the port range shape without touching the network.
func (r PortRule) ValidateRange() error {
	first, last := r.PortRange()
	if first < 1 || last > 65535 {
		return errors.New("listen ports must be between 1 and 65535")
	}
	if r.ListenPortEnd != 0 && r.ListenPortEnd < r.ListenPort {
		return errors.New("listen_port_end must be greater than or equal to listen_port")
	}
	if last-first+1 > MaxRangePorts {
		return errors.New("a listen port range may cover at most 1024 ports")
	}
	return nil
}

// ListenAddressAt returns the bind endpoint of one port inside the range.
func (r PortRule) ListenAddressAt(port int) string {
	host := r.ListenHost
	if host == "" {
		host = "0.0.0.0"
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// Address returns the destination endpoint of the rule.
func (r PortRule) Address() string {
	return net.JoinHostPort(r.DestinationHost, strconv.Itoa(r.DestinationPort))
}

// ListenAddress returns the bind endpoint of the rule.
func (r PortRule) ListenAddress() string {
	host := r.ListenHost
	if host == "" {
		host = "0.0.0.0"
	}
	return net.JoinHostPort(host, strconv.Itoa(r.ListenPort))
}

// EgressOrDefault never returns nil.
func (r PortRule) EgressOrDefault() EgressSpec {
	if r.Egress == nil {
		return EgressSpec{}.Normalized()
	}
	return r.Egress.Normalized()
}

// NetAddress is one address assigned to an interface on a machine.
type NetAddress struct {
	Address string `json:"address"`
	Prefix  int    `json:"prefix,omitempty"`
	Family  string `json:"family"`
	Scope   string `json:"scope,omitempty"`
}

// NetInterface describes an egress candidate on a machine.
type NetInterface struct {
	Name      string       `json:"name"`
	Up        bool         `json:"up"`
	Loopback  bool         `json:"loopback,omitempty"`
	Default4  bool         `json:"default4,omitempty"`
	Default6  bool         `json:"default6,omitempty"`
	MTU       int          `json:"mtu,omitempty"`
	Addresses []NetAddress `json:"addresses,omitempty"`
}

// NetInfo is the egress capability report of one machine.
type NetInfo struct {
	Hostname   string         `json:"hostname,omitempty"`
	Platform   string         `json:"platform,omitempty"`
	Default4   string         `json:"default4,omitempty"`
	Default6   string         `json:"default6,omitempty"`
	Interfaces []NetInterface `json:"interfaces"`
	// PoolSource reports how extra IPv6 addresses can be carried on this machine.
	PoolSource string `json:"pool_source,omitempty"`
}
