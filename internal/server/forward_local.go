package server

import (
	"context"
	"errors"
	"io"
	"net"

	"github.com/qinyongliang/linksphere/internal/egress"
	"github.com/qinyongliang/linksphere/internal/forward"
	"github.com/qinyongliang/linksphere/internal/protocol"
	protocoltypes "github.com/qinyongliang/linksphere/internal/protocol/types"
	"github.com/qinyongliang/linksphere/internal/store"
)

// serverDial grants one route per transport. When the bastion is the exit machine
// it opens the destination socket itself, so that route's egress selection is
// applied here; Agent and SSH exits apply their own selection on the far side.
func (m *edgeForwardManager) serverDial(ctx context.Context, targetID string, routes []protocol.ForwardRoute) (forward.TargetDialFunc, []io.Closer, error) {
	allowed := map[string]protocol.ForwardRoute{}
	for _, r := range routes {
		if r.Network != "tcp" && r.Network != "udp" {
			return nil, nil, errors.New("invalid route network")
		}
		allowed[r.ID] = r
	}
	base := forward.DialNetwork
	egressDials := map[string]egress.Dialer{}
	var closers []io.Closer
	if targetID != "" {
		target, err := m.app.store.Repository().GetSSHTarget(ctx, targetID)
		if err != nil {
			return nil, nil, err
		}
		if target.TargetType == store.TargetAgent {
			// An Agent exit applies the rule egress itself; the stream carries it.
			// Keyed by route ID because two rules may share a destination yet
			// differ in interface or pool selection.
			agentBase := func(ctx context.Context, network, address string, spec *protocoltypes.EgressSpec) (net.Conn, error) {
				session, err := m.app.registry.Get(target.AgentID)
				if err != nil {
					return nil, err
				}
				return dialTunnelAgentEgress(ctx, session, address, network, spec)
			}
			return func(ctx context.Context, id, target string) (net.Conn, error) {
				route, ok := allowed[id]
				if !ok {
					return nil, errors.New("unauthorized route")
				}
				address, err := routeAddress(route, target)
				if err != nil {
					return nil, err
				}
				return agentBase(ctx, route.Network, address, route.Egress)
			}, closers, nil
		} else {
			for _, r := range routes {
				if r.Network == "udp" {
					return nil, nil, errors.New("SSH endpoints do not support UDP forwarding")
				}
			}
			client, err := m.app.openTargetSSHClient(ctx, target)
			if err != nil {
				return nil, nil, err
			}
			closers = append(closers, client)
			base = func(ctx context.Context, network, address string) (net.Conn, error) {
				return client.DialContext(ctx, network, address)
			}
		}
	} else {
		for _, r := range routes {
			dialer, err := egress.FromRoute(r.Egress, m.pools)
			if err != nil {
				for _, c := range closers {
					_ = c.Close()
				}
				return nil, nil, err
			}
			egressDials[r.ID] = dialer
			closers = append(closers, dialer)
		}
	}
	return func(ctx context.Context, id, target string) (net.Conn, error) {
		route, ok := allowed[id]
		if !ok {
			return nil, errors.New("unauthorized route")
		}
		address, err := routeAddress(route, target)
		if err != nil {
			return nil, err
		}
		if dialer := egressDials[id]; dialer != nil {
			return dialer.DialContext(ctx, route.Network, address)
		}
		return base(ctx, route.Network, address)
	}, closers, nil
}

// routeAddress resolves the destination of one dial. A proxy listener supplies a
// per-connection target, which is only accepted from a dynamic route grant.
func routeAddress(route protocol.ForwardRoute, target string) (string, error) {
	if target == "" {
		return route.Address, nil
	}
	if !route.Dynamic {
		return "", errors.New("route does not accept a client destination")
	}
	return target, nil
}
func (m *edgeForwardManager) ensureServerRun(t store.Tunnel) {
	m.mu.Lock()
	old := m.runs[t.ID]
	if old != nil && old.revision == t.Revision && old.err == "" && old.runtime != nil && old.runtime.Alive() {
		m.mu.Unlock()
		return
	}
	delete(m.runs, t.ID)
	m.mu.Unlock()
	if old != nil {
		old.close()
	}
	r := &forwardServerRun{revision: t.Revision}
	ctx, cancel := context.WithCancel(m.ctx)
	if t.ExpiresAt != nil {
		cancel()
		ctx, cancel = context.WithDeadline(m.ctx, *t.ExpiresAt)
	}
	r.cancel = cancel
	start := func() error {
		logs, err := m.logStore(t.ID)
		if err != nil {
			return err
		}
		listen := func(address string) (net.Listener, error) { return net.Listen("tcp", address) }
		if t.EntryTargetID != "" {
			target, e := m.app.store.Repository().GetSSHTarget(ctx, t.EntryTargetID)
			if e != nil {
				return e
			}
			client, e := m.app.openTargetSSHClient(ctx, target)
			if e != nil {
				return e
			}
			r.closers = append(r.closers, client)
			listen = func(address string) (net.Listener, error) { return client.Listen("tcp", address) }
		}
		dials := map[string]forward.TargetDialFunc{}
		groups := map[string][]protocol.ForwardRoute{}
		if t.Protocol == "http" {
			if t.HTTP == nil {
				return errors.New("HTTP configuration required")
			}
			for _, s := range t.HTTP.Servers {
				for _, l := range s.Locations {
					groups[l.ExitTargetID] = append(groups[l.ExitTargetID], protocol.ForwardRoute{ID: l.ID, Network: "tcp", Address: l.Address(), Egress: l.Egress})
				}
			}
		} else {
			for _, rule := range t.Rules() {
				for _, network := range ruleTransports(rule) {
					// A proxy listener chooses the destination per connection, so its
					// TCP grant is dynamic; UDP always streams to the rule destination.
					groups[t.ExitTargetID] = append(groups[t.ExitTargetID], protocol.ForwardRoute{ID: portRuleRouteID(rule.ID, network), Network: network, Address: rule.Address(), Egress: rule.Egress, Dynamic: network == "tcp" && rule.ProxiesClients()})
				}
			}
		}
		for target, routes := range groups {
			dial, closers, e := m.serverDial(ctx, target, routes)
			if e != nil {
				return e
			}
			r.closers = append(r.closers, closers...)
			for _, route := range routes {
				dials[route.ID] = dial
			}
		}
		dialTarget := func(ctx context.Context, id, target string) (net.Conn, error) {
			fn := dials[id]
			if fn == nil {
				return nil, errors.New("route unavailable")
			}
			return fn(ctx, id, target)
		}
		dial := func(ctx context.Context, id string) (net.Conn, error) { return dialTarget(ctx, id, "") }
		if t.Protocol == "http" {
			r.runtime, err = forward.StartHTTP(ctx, t.HTTP, listen, dial, logs, nil)
			return err
		}
		rules := t.Rules()
		if len(rules) == 0 {
			return errors.New("port rules required")
		}
		specs := make([]forward.RuleSpec, 0, len(rules))
		for _, rule := range rules {
			transports := ruleTransports(rule)
			routeIDs := map[string]string{}
			for _, network := range transports {
				routeIDs[network] = portRuleRouteID(rule.ID, network)
			}
			destination := forward.Location{ID: rule.ID, ExitTargetID: t.ExitTargetID, Host: rule.DestinationHost, Port: rule.DestinationPort, Egress: rule.Egress}
			// A port range binds one listener per port; they share the rule's
			// route grants and therefore its egress selection.
			specs = append(specs, forward.ExpandPortRule(rule, routeIDs, destination)...)
		}
		// StartRules binds every listen port atomically, so a multi-port
		// configuration never serves a partial port set.
		r.runtime, err = forward.StartRules(ctx, specs, dialTarget, logs.TrafficRecorder(), nil, listen)
		return err
	}
	if err := start(); err != nil {
		r.err = err.Error()
		r.close()
	}
	m.mu.Lock()
	m.runs[t.ID] = r
	m.mu.Unlock()
}
