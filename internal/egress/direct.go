package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	protocoltypes "github.com/qinyongliang/linksphere/internal/protocol/types"
)

type directDialer struct {
	spec protocoltypes.EgressSpec
	pool *pool
}

func (d *directDialer) Spec() protocoltypes.EgressSpec { return d.spec }
func (d *directDialer) Close() error                   { return nil }

// DialContext opens the socket from the exit machine. Pool allocation consumes
// one address per connection: rotate picks a random member, sticky advances the
// sequence from the previous address by a random increment.
func (d *directDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" && network != "udp" {
		return nil, errors.New("unsupported forward network")
	}
	dialNetwork := network
	source := d.sourceAddress()
	switch d.spec.Family {
	case protocoltypes.EgressFamilyIPv4:
		dialNetwork = network + "4"
	case protocoltypes.EgressFamilyIPv6:
		dialNetwork = network + "6"
	}
	// A pinned IPv6 source is only meaningful on an IPv6 socket, so an explicit
	// source overrides "auto": the operator asked for IPv6 egress.
	if source != nil && source.To4() == nil && dialNetwork == network {
		dialNetwork = network + "6"
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	if source != nil {
		if network == "udp" {
			dialer.LocalAddr = &net.UDPAddr{IP: source}
		} else {
			dialer.LocalAddr = &net.TCPAddr{IP: source}
		}
	}
	// A pinned source address already selects the interface; also binding the
	// device breaks PPPoE links, where the route is pinned to the ppp device.
	device := d.spec.Interface
	if d.spec.IPv6Mode == protocoltypes.EgressIPv6Interface {
		device = ""
	}
	control, err := socketControl(device, d.spec.IPv6Mode == protocoltypes.EgressIPv6Pool)
	if err != nil {
		return nil, err
	}
	dialer.Control = control
	conn, err := dialer.DialContext(ctx, dialNetwork, address)
	if err != nil {
		return nil, describeDialError(err, d.spec)
	}
	if network == "udp" {
		return &datagramConn{Conn: conn}, nil
	}
	return conn, nil
}

func (d *directDialer) sourceAddress() net.IP {
	switch d.spec.IPv6Mode {
	case protocoltypes.EgressIPv6Pool:
		if d.pool == nil {
			return nil
		}
		return d.pool.Next().IP
	case protocoltypes.EgressIPv6Interface:
		addresses, err := interfaceIPv6(d.spec.Interface)
		if err != nil || len(addresses) == 0 {
			return nil
		}
		return addresses[0]
	}
	return nil
}

// interfaceIPv6 lists global unicast addresses of one interface.
func interfaceIPv6(name string) ([]net.IP, error) {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return nil, err
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return nil, err
	}
	var out []net.IP
	for _, addr := range addrs {
		ip, _, err := net.ParseCIDR(addr.String())
		if err != nil || ip.To4() != nil || !isGlobalIPv6(ip) {
			continue
		}
		out = append(out, ip)
	}
	return out, nil
}

func isGlobalIPv6(ip net.IP) bool {
	if ip == nil || ip.To4() != nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	return true
}

// describeDialError adds the egress context an operator needs to fix the rule.
func describeDialError(err error, spec protocoltypes.EgressSpec) error {
	message := err.Error()
	switch {
	case strings.Contains(message, "cannot assign requested address"):
		return fmt.Errorf("egress %s unavailable: %w (check the interface or pool prefix on the exit machine)", spec.Normalized().IPv6Mode, err)
	case strings.Contains(message, "network is unreachable"):
		return fmt.Errorf("egress family %s unreachable from the exit machine: %w", spec.Normalized().Family, err)
	}
	return err
}
