package forward

import (
	"context"
	"errors"
	"net"
	"sort"
	"strconv"
	"strings"

	protocoltypes "github.com/qinyongliang/linksphere/internal/protocol/types"
)

// RuleSpec is one listen port of a multi-port forwarding configuration.
type RuleSpec struct {
	ID            string
	ListenAddress string
	Transports    []string
	// RouteIDs maps transport ("tcp"/"udp") to the immutable route grant the
	// destination socket is opened through.
	RouteIDs map[string]string
	// Destination labels events; the address is resolved from the route grant.
	Destination Location
	// Protocol selects the listener behaviour: empty or "tcp" streams bytes to
	// Destination, while "auto", "http" and "socks" serve proxy clients.
	Protocol string
}

// ExpandPortRule turns one stored rule into one listener spec per listen port.
// A rule without a range yields a single spec. Every port shares the rule's route
// grants, so the exit machine applies the same destination and egress selection
// (including pool allocation) whichever port the client used.
func ExpandPortRule(rule protocoltypes.PortRule, routeIDs map[string]string, destination Location) []RuleSpec {
	first, last := rule.PortRange()
	specs := make([]RuleSpec, 0, last-first+1)
	for port := first; port <= last; port++ {
		specs = append(specs, RuleSpec{
			ID:            rule.ID,
			ListenAddress: rule.ListenAddressAt(port),
			Transports:    rule.Transports,
			RouteIDs:      routeIDs,
			Destination:   destination,
			Protocol:      rule.NormalizedListenProtocol(),
		})
	}
	return specs
}

// StartRules binds every listen port of a multi-port configuration atomically: a
// single failed bind releases all previously bound ports. Each port keeps its own
// transports, route grants and counters.
func StartRules(parent context.Context, rules []RuleSpec, dial TargetDialFunc, logs EventRecorder, direct func(string) bool, listen func(string) (net.Listener, error)) (*Runtime, error) {
	if len(rules) == 0 {
		return nil, errors.New("port rules required")
	}
	if listen == nil {
		listen = func(address string) (net.Listener, error) { return net.Listen("tcp", address) }
	}
	r := &Runtime{runtimeCounters: newRuntimeCounters()}
	cleanup := func() {
		for _, child := range r.children {
			_ = child.Close()
		}
		r.children = nil
	}
	for _, rule := range rules {
		if rule.ListenAddress == "" {
			cleanup()
			return nil, errors.New("port rule listen address required")
		}
		selected := map[string]bool{}
		for _, network := range rule.Transports {
			if (network != "tcp" && network != "udp") || selected[network] {
				cleanup()
				return nil, errors.New("invalid port transports")
			}
			selected[network] = true
			if rule.RouteIDs[network] == "" {
				cleanup()
				return nil, errors.New("port rule route required")
			}
		}
		if len(selected) == 0 {
			cleanup()
			return nil, errors.New("port transports required")
		}
		address := rule.ListenAddress
		if selected["tcp"] {
			listener, err := listen(address)
			if err != nil {
				cleanup()
				return nil, err
			}
			route := rule.Destination
			route.ID = rule.RouteIDs["tcp"]
			var child *Runtime
			if isProxyMode(rule.Protocol) {
				// The listener speaks to clients: it sniffs SOCKS/HTTP and dials the
				// client's destination through the rule's dynamic route grant.
				child, err = startProxy(parent, listener, ProxySpec{RuleID: rule.ID, RouteID: rule.RouteIDs["tcp"], Mode: rule.Protocol, Destination: route}, dial, logs, direct, r.runtimeCounters)
			} else {
				child, err = start(parent, listener, nil, &route, func(ctx context.Context, id string) (net.Conn, error) { return dial(ctx, id, "") }, logs, direct, r.runtimeCounters)
			}
			if err != nil {
				_ = listener.Close()
				cleanup()
				return nil, err
			}
			r.children = append(r.children, child)
			address = listener.Addr().String()
		}
		if selected["udp"] {
			packet, err := net.ListenPacket("udp", address)
			if err != nil {
				cleanup()
				return nil, err
			}
			// UDP has no handshake to sniff: it always streams to Destination.
			route := rule.Destination
			route.ID = rule.RouteIDs["udp"]
			r.children = append(r.children, startUDP(parent, packet, route, func(ctx context.Context, id string) (net.Conn, error) { return dial(ctx, id, "") }, logs, direct, udpIdleTimeout, r.runtimeCounters))
		}
	}
	return r, nil
}

// RuleCount returns the number of bound listen ports, for status reporting.
func (r *Runtime) RuleCount() int { return len(r.children) }

// compactListenAddresses renders bound endpoints, folding consecutive ports of
// one host into a range so a 10000-11000 rule reports one compact endpoint.
func compactListenAddresses(values []string) string {
	type hostPorts struct {
		host  string
		ports []int
	}
	order := []string{}
	byHost := map[string]*hostPorts{}
	for _, value := range values {
		host, rawPort, err := net.SplitHostPort(value)
		if err != nil {
			continue
		}
		port, err := strconv.Atoi(rawPort)
		if err != nil {
			continue
		}
		entry := byHost[host]
		if entry == nil {
			entry = &hostPorts{host: host}
			byHost[host] = entry
			order = append(order, host)
		}
		entry.ports = append(entry.ports, port)
	}
	parts := make([]string, 0, len(order))
	for _, host := range order {
		ports := byHost[host].ports
		sort.Ints(ports)
		start, previous := ports[0], ports[0]
		flush := func(end int) {
			address := net.JoinHostPort(host, strconv.Itoa(start))
			if end != start {
				address = net.JoinHostPort(host, strconv.Itoa(start)+"-"+strconv.Itoa(end))
			}
			parts = append(parts, address)
		}
		for _, port := range ports[1:] {
			if port == previous || port == previous+1 {
				previous = port
				continue
			}
			flush(previous)
			start, previous = port, port
		}
		flush(previous)
	}
	return strings.Join(parts, ", ")
}

// dedupe keeps listen-address reporting stable for multi-port runtimes.
func dedupe(values []string) []string {
	seen := map[string]bool{}
	out := values[:0]
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
