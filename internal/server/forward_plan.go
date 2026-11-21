package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"

	"github.com/qinyongliang/linksphere/internal/forward"
	"github.com/qinyongliang/linksphere/internal/protocol"
	protocoltypes "github.com/qinyongliang/linksphere/internal/protocol/types"
	"github.com/qinyongliang/linksphere/internal/store"
)

func (a *App) forwardAgentID(ctx context.Context, id string) (string, error) {
	if id == "" {
		return "", nil
	}
	for depth := 0; depth < 4; depth++ {
		t, err := a.store.Repository().GetSSHTarget(ctx, id)
		if err != nil {
			return "", err
		}
		if t.TargetType == store.TargetAgent {
			return t.AgentID, nil
		}
		if t.ProxyTargetID == "" {
			return "", nil
		}
		id = t.ProxyTargetID
	}
	return "", errors.New("SSH chain too deep")
}
func (a *App) forwardHops(ctx context.Context, id string) ([]protocol.TunnelSSHHop, error) {
	if id == "" {
		return nil, nil
	}
	t, err := a.store.Repository().GetSSHTarget(ctx, id)
	if err != nil {
		return nil, err
	}
	if t.TargetType == store.TargetAgent {
		return nil, nil
	}
	ep, err := a.tunnelAgentEndpoint(ctx, id)
	if err != nil {
		return nil, err
	}
	if ep == nil {
		return nil, nil
	}
	return ep.hops, nil
}

// portRuleRouteID names the immutable route grant of one transport of one rule.
// Every rule keeps its own grant so transports, destination and egress stay
// independent.
func portRuleRouteID(ruleID, network string) string { return ruleID + ":" + network }

// ruleTransports returns the transports a rule listens on, defaulting to TCP.
func ruleTransports(rule protocoltypes.PortRule) []string {
	if len(rule.Transports) == 0 {
		return []string{"tcp"}
	}
	return rule.Transports
}

func (m *edgeForwardManager) build(t store.Tunnel) (forwardPlan, error) {
	entry, err := m.app.forwardAgentID(m.ctx, t.EntryTargetID)
	if err != nil {
		return forwardPlan{}, err
	}
	d := protocol.ForwardDeployment{ID: t.ID, Revision: t.Revision, Protocol: t.Protocol, Transports: t.Transports, ListenAddress: net.JoinHostPort(t.ListenHost, strconv.Itoa(t.ListenPort)), HTTP: t.HTTP}
	if t.ExpiresAt != nil {
		d.ExpiresAt = t.ExpiresAt.UnixMilli()
	}
	p := forwardPlan{deployment: d, entryAgent: entry, links: map[string]forwardLinkSpec{}}
	if entry != "" {
		if p.deployment.ListenHops, err = m.app.forwardHops(m.ctx, t.EntryTargetID); err != nil {
			return p, err
		}
	}
	if t.Protocol == "http" {
		if err := m.planHTTP(&p); err != nil {
			return p, err
		}
		return p, nil
	}
	if err := m.planPort(&p, t); err != nil {
		return p, err
	}
	return p, nil
}

// planHTTP maps every location onto one TCP route. The location egress travels
// with the route so the exit machine opens the upstream socket through it.
func (m *edgeForwardManager) planHTTP(p *forwardPlan) error {
	if p.deployment.HTTP == nil {
		return errors.New("HTTP configuration required")
	}
	for _, s := range p.deployment.HTTP.Servers {
		for _, location := range s.Locations {
			route := protocol.ForwardRoute{ID: location.ID, Network: "tcp", Address: location.Address(), Egress: location.Egress}
			if err := m.planRoute(p, location.ExitTargetID, &route); err != nil {
				return err
			}
			p.deployment.Routes = append(p.deployment.Routes, route)
		}
	}
	return nil
}

// planPort projects the stored configuration onto its effective port rules. Every
// rule is emitted in PortRules and contributes one route per transport; the legacy
// ListenAddress/Destination pair still describes the first rule for old Agents.
func (m *edgeForwardManager) planPort(p *forwardPlan, t store.Tunnel) error {
	rules := t.Rules()
	if len(rules) == 0 {
		return errors.New("port rules required")
	}
	p.deployment.PortRules = append([]protocoltypes.PortRule(nil), rules...)
	first := rules[0]
	p.deployment.ListenAddress = first.ListenAddress()
	p.deployment.Transports = ruleTransports(first)
	destination := forward.Location{ID: first.ID, ExitTargetID: t.ExitTargetID, Host: first.DestinationHost, Port: first.DestinationPort, Egress: first.Egress}
	p.deployment.Destination = &destination
	for _, rule := range rules {
		for _, network := range ruleTransports(rule) {
			// A proxy listener ("auto"/"http"/"socks") picks the destination per
			// connection, so its TCP grant is dynamic; UDP always streams to the
			// rule destination.
			route := protocol.ForwardRoute{ID: portRuleRouteID(rule.ID, network), Network: network, Address: rule.Address(), Egress: rule.Egress, Dynamic: network == "tcp" && rule.ProxiesClients()}
			if err := m.planRoute(p, t.ExitTargetID, &route); err != nil {
				return err
			}
			p.deployment.Routes = append(p.deployment.Routes, route)
		}
	}
	return nil
}

// planRoute attaches one route grant to the machine that opens the destination
// socket. An entry Agent that is also the exit dials locally, while a remote exit
// needs a peer link: every rule that shares an exit target reuses that one link so
// its peer carries all those routes.
func (m *edgeForwardManager) planRoute(p *forwardPlan, exitTargetID string, route *protocol.ForwardRoute) error {
	exit, err := m.app.forwardAgentID(m.ctx, exitTargetID)
	if err != nil {
		return err
	}
	if p.entryAgent != "" && p.entryAgent == exit {
		route.Hops, err = m.app.forwardHops(m.ctx, exitTargetID)
		return err
	}
	if p.entryAgent == "" {
		// The bastion listens and dials the exit itself.
		return nil
	}
	linkID := fmt.Sprintf("%s:%d:%s", p.deployment.ID, p.deployment.Revision, exitTargetID)
	spec, ok := p.links[linkID]
	if !ok {
		spec = forwardLinkSpec{peer: protocol.ForwardPeer{LinkID: linkID, ConfigID: p.deployment.ID, Revision: p.deployment.Revision, ExpiresAt: p.deployment.ExpiresAt}, exitAgent: exit, targetID: exitTargetID}
	}
	route.LinkID = linkID
	// The exit must know the grant is dynamic: a proxy listener's client picks the
	// destination, and without this flag the exit rejects every client target.
	exitRoute := protocol.ForwardRoute{ID: route.ID, Network: route.Network, Address: route.Address, Egress: route.Egress, Dynamic: route.Dynamic}
	if exit != "" {
		if exitRoute.Hops, err = m.app.forwardHops(m.ctx, exitTargetID); err != nil {
			return err
		}
	}
	spec.peer.Routes = append(spec.peer.Routes, exitRoute)
	p.links[linkID] = spec
	return nil
}
