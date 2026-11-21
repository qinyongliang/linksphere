package server

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/qinyongliang/linksphere/internal/protocol"
	protocoltypes "github.com/qinyongliang/linksphere/internal/protocol/types"
	"github.com/qinyongliang/linksphere/internal/store"
)

// egressRule builds one port rule with an optional egress selection.
func egressRule(id string, listenPort int, transports []string, egress *protocoltypes.EgressSpec) protocoltypes.PortRule {
	return protocoltypes.PortRule{ID: id, Name: id, ListenHost: "127.0.0.1", ListenPort: listenPort, Transports: transports, DestinationHost: "127.0.0.1", DestinationPort: 8080, Egress: egress}
}

// routesByID indexes a deployment's routes for assertions.
func routesByID(d protocol.ForwardDeployment) map[string]protocol.ForwardRoute {
	out := map[string]protocol.ForwardRoute{}
	for _, r := range d.Routes {
		out[r.ID] = r
	}
	return out
}

func TestPortRulePlanEmitsRulesRoutesAndEgress(t *testing.T) {
	app, u, o := tunnelFixture(t)
	attachForwardTestAgent(t, app, "plan-entry")
	attachForwardTestAgent(t, app, "plan-exit")
	entry := tunnelAgentTarget(t, app, u, o, "plan-entry")
	exit := tunnelAgentTarget(t, app, u, o, "plan-exit")

	iface := &protocoltypes.EgressSpec{Interface: "eth0", Family: protocoltypes.EgressFamilyBoth}
	pool := &protocoltypes.EgressSpec{Family: protocoltypes.EgressFamilyIPv6, IPv6Mode: protocoltypes.EgressIPv6Pool, PoolStart: "2001:db8::10", PoolSize: 16, PoolMode: protocoltypes.EgressPoolRotate}
	proxy := &protocoltypes.EgressSpec{Mode: protocoltypes.EgressModeProxy, ProxyURL: "socks5://user:pass@192.0.2.10:1080"}
	config := store.TunnelConfig{Protocol: "port", Name: "multi-port egress", EntryTargetID: entry.ID, ExitTargetID: exit.ID, PortRules: []protocoltypes.PortRule{
		egressRule("web", freeTunnelPort(t), []string{"tcp"}, iface),
		egressRule("dns", freeTunnelPort(t), []string{"tcp", "udp"}, pool),
		egressRule("legacy", freeTunnelPort(t), []string{"tcp"}, nil),
	}}
	tun, err := app.store.Repository().SaveTunnel(context.Background(), store.Tunnel{TunnelConfig: config, OrganizationID: o.ID, CreatedBy: u.ID})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := app.forwards.build(tun)
	if err != nil {
		t.Fatal(err)
	}
	d := plan.deployment
	if len(d.PortRules) != 3 || d.PortRules[0].ID != "web" || d.PortRules[1].ID != "dns" || d.PortRules[2].ID != "legacy" {
		t.Fatalf("port rules not projected: %+v", d.PortRules)
	}
	if d.PortRules[0].Egress == nil || d.PortRules[0].Egress.Interface != "eth0" {
		t.Fatalf("rule egress lost: %+v", d.PortRules[0])
	}
	// The legacy pair still describes the first rule for old Agents.
	if d.ListenAddress != d.PortRules[0].ListenAddress() || d.Destination == nil || d.Destination.Address() != d.PortRules[0].Address() {
		t.Fatalf("legacy projection broken: %s %+v", d.ListenAddress, d.Destination)
	}
	routes := routesByID(d)
	if len(routes) != 4 {
		t.Fatalf("expected one route per rule x transport, got %d: %+v", len(routes), d.Routes)
	}
	for _, id := range []string{"web:tcp", "dns:tcp", "dns:udp", "legacy:tcp"} {
		if _, ok := routes[id]; !ok {
			t.Fatalf("missing route %q in %+v", id, d.Routes)
		}
	}
	if got := routes["web:tcp"].Egress; got == nil || got.Interface != "eth0" || got.Family != protocoltypes.EgressFamilyBoth {
		t.Fatalf("interface egress not passed through: %+v", got)
	}
	if got := routes["dns:udp"].Egress; got == nil || got.IPv6Mode != protocoltypes.EgressIPv6Pool || got.PoolStart != "2001:db8::10" || got.PoolSize != 16 {
		t.Fatalf("pool egress not passed through: %+v", got)
	}
	if routes["dns:udp"].Network != "udp" || routes["dns:udp"].Address != config.PortRules[1].Address() {
		t.Fatalf("route shape wrong: %+v", routes["dns:udp"])
	}
	if routes["legacy:tcp"].Egress != nil {
		t.Fatalf("default egress must stay empty: %+v", routes["legacy:tcp"])
	}
	// All three rules share one exit machine, so one link carries every route.
	if len(plan.links) != 1 {
		t.Fatalf("expected a single shared link, got %d", len(plan.links))
	}
	for linkID, spec := range plan.links {
		if len(spec.peer.Routes) != 4 {
			t.Fatalf("shared link %s lost routes: %+v", linkID, spec.peer.Routes)
		}
		if spec.peer.ConfigID != tun.ID || spec.peer.Revision != tun.Revision {
			t.Fatalf("link spec identity wrong: %+v", spec.peer)
		}
		for _, r := range spec.peer.Routes {
			if r.LinkID != "" {
				t.Fatalf("peer route must not nest links: %+v", r)
			}
			if r.ID == "web:tcp" && (r.Egress == nil || r.Egress.Interface != "eth0") {
				t.Fatalf("peer route lost egress: %+v", r)
			}
		}
	}
	for _, r := range d.Routes {
		if r.LinkID == "" {
			t.Fatalf("remote exit route missing link: %+v", r)
		}
	}
	_ = proxy
}

func TestPortRulePlanSameAgentExitDialsLocally(t *testing.T) {
	app, u, o := tunnelFixture(t)
	attachForwardTestAgent(t, app, "local-entry")
	entry := tunnelAgentTarget(t, app, u, o, "local-entry")
	config := store.TunnelConfig{Protocol: "port", Name: "same agent", EntryTargetID: entry.ID, ExitTargetID: entry.ID, PortRules: []protocoltypes.PortRule{
		egressRule("a", freeTunnelPort(t), []string{"tcp"}, nil),
		egressRule("b", freeTunnelPort(t), []string{"tcp"}, nil),
	}}
	tun, err := app.store.Repository().SaveTunnel(context.Background(), store.Tunnel{TunnelConfig: config, OrganizationID: o.ID, CreatedBy: u.ID})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := app.forwards.build(tun)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.links) != 0 {
		t.Fatalf("same-Agent exit must not create a link: %+v", plan.links)
	}
	if len(plan.deployment.Routes) != 2 {
		t.Fatalf("routes missing: %+v", plan.deployment.Routes)
	}
	for _, r := range plan.deployment.Routes {
		if r.LinkID != "" {
			t.Fatalf("local route must not carry a link: %+v", r)
		}
	}
}

func TestLegacySinglePortConfigStillProducesOneRoute(t *testing.T) {
	app, u, o := tunnelFixture(t)
	attachForwardTestAgent(t, app, "legacy-entry")
	attachForwardTestAgent(t, app, "legacy-exit")
	entry := tunnelAgentTarget(t, app, u, o, "legacy-entry")
	exit := tunnelAgentTarget(t, app, u, o, "legacy-exit")
	config := store.TunnelConfig{Protocol: "port", Transports: []string{"tcp"}, Name: "legacy", EntryTargetID: entry.ID, ExitTargetID: exit.ID, ListenHost: "127.0.0.1", ListenPort: freeTunnelPort(t), DestinationHost: "127.0.0.1", DestinationPort: 8080}
	tun, err := app.store.Repository().SaveTunnel(context.Background(), store.Tunnel{TunnelConfig: config, OrganizationID: o.ID, CreatedBy: u.ID})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := app.forwards.build(tun)
	if err != nil {
		t.Fatal(err)
	}
	d := plan.deployment
	if len(d.PortRules) != 1 || d.PortRules[0].ID != "port" {
		t.Fatalf("legacy rule projection wrong: %+v", d.PortRules)
	}
	if len(d.Routes) != 1 {
		t.Fatalf("legacy config must produce one route: %+v", d.Routes)
	}
	route := d.Routes[0]
	if route.Network != "tcp" || route.Address != net.JoinHostPort(config.DestinationHost, itoa(config.DestinationPort)) {
		t.Fatalf("legacy route wrong: %+v", route)
	}
	if d.ListenAddress != "127.0.0.1:"+itoa(config.ListenPort) {
		t.Fatalf("legacy listen address lost: %s", d.ListenAddress)
	}
	if len(plan.links) != 1 {
		t.Fatalf("legacy cross-Agent config must keep one link: %+v", plan.links)
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var digits []byte
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	return string(digits)
}

func TestValidateTunnelPortRulesAndEgress(t *testing.T) {
	app, u, o := tunnelFixture(t)
	attachForwardTestAgent(t, app, "egress-exit")
	agentExit := tunnelAgentTarget(t, app, u, o, "egress-exit")
	sshExit := tunnelSSHTarget(t, app, u, o, "egress-ssh", tunnelTestSSH(t), "")
	valid := func() store.TunnelConfig {
		return store.TunnelConfig{Protocol: "port", Name: "rules", ExitTargetID: agentExit.ID, PortRules: []protocoltypes.PortRule{
			egressRule("a", freeTunnelPort(t), []string{"tcp"}, nil),
			egressRule("b", freeTunnelPort(t), []string{"tcp", "udp"}, nil),
		}}
	}
	if err := app.validateTunnel(context.Background(), u, o.ID, valid()); err != nil {
		t.Fatalf("rejected valid multi-port config: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*store.TunnelConfig)
	}{
		{"empty rules", func(c *store.TunnelConfig) { c.PortRules = nil }},
		{"too many rules", func(c *store.TunnelConfig) {
			c.PortRules = nil
			for i := 0; i < 65; i++ {
				c.PortRules = append(c.PortRules, egressRule("r"+itoa(i), 1000+i, []string{"tcp"}, nil))
			}
		}},
		{"duplicate id", func(c *store.TunnelConfig) { c.PortRules[1].ID = c.PortRules[0].ID }},
		{"empty id", func(c *store.TunnelConfig) { c.PortRules[0].ID = " " }},
		{"duplicate listen port", func(c *store.TunnelConfig) {
			c.PortRules[1].ListenHost = c.PortRules[0].ListenHost
			c.PortRules[1].ListenPort = c.PortRules[0].ListenPort
		}},
		{"non-ip listen host", func(c *store.TunnelConfig) { c.PortRules[0].ListenHost = "localhost" }},
		{"bad listen port", func(c *store.TunnelConfig) { c.PortRules[0].ListenPort = 70000 }},
		{"bad destination port", func(c *store.TunnelConfig) { c.PortRules[0].DestinationPort = 0 }},
		{"bad destination host", func(c *store.TunnelConfig) { c.PortRules[0].DestinationHost = "bad host" }},
		{"empty transports", func(c *store.TunnelConfig) { c.PortRules[0].Transports = nil }},
		{"bad transport", func(c *store.TunnelConfig) { c.PortRules[0].Transports = []string{"sctp"} }},
		{"duplicate transport", func(c *store.TunnelConfig) { c.PortRules[0].Transports = []string{"tcp", "tcp"} }},
		{"invalid egress", func(c *store.TunnelConfig) {
			c.PortRules[0].Egress = &protocoltypes.EgressSpec{Mode: protocoltypes.EgressModeProxy}
		}},
		{"invalid egress pool", func(c *store.TunnelConfig) {
			c.PortRules[0].Egress = &protocoltypes.EgressSpec{Family: protocoltypes.EgressFamilyIPv6, IPv6Mode: protocoltypes.EgressIPv6Pool, PoolStart: "192.0.2.1", PoolSize: 4}
		}},
	}
	for _, tc := range cases {
		config := valid()
		tc.mutate(&config)
		if err := app.validateTunnel(context.Background(), u, o.ID, config); err == nil {
			t.Fatalf("%s: accepted invalid port rules", tc.name)
		}
	}
	// Advanced egress needs an Agent exit: an SSH exit is rejected, the bastion is
	// allowed because it applies the selection itself.
	ssh := valid()
	ssh.ExitTargetID = sshExit.ID
	ssh.PortRules[1].Transports = []string{"tcp"}
	ssh.PortRules[0].Egress = &protocoltypes.EgressSpec{Interface: "eth0"}
	err := app.validateTunnel(context.Background(), u, o.ID, ssh)
	if err == nil || !strings.Contains(err.Error(), "egress interface/pool selection requires an Agent exit machine") {
		t.Fatalf("accepted SSH exit with advanced egress: %v", err)
	}
	bastion := valid()
	bastion.ExitTargetID = ""
	bastion.PortRules[0].Egress = &protocoltypes.EgressSpec{Interface: "eth0"}
	if err := app.validateTunnel(context.Background(), u, o.ID, bastion); err != nil {
		t.Fatalf("rejected bastion exit with egress: %v", err)
	}
	// The same egress on an Agent exit is accepted.
	agent := valid()
	agent.PortRules[0].Egress = &protocoltypes.EgressSpec{Interface: "eth0"}
	if err := app.validateTunnel(context.Background(), u, o.ID, agent); err != nil {
		t.Fatalf("rejected Agent exit with egress: %v", err)
	}
	// UDP still cannot cross an SSH endpoint.
	udp := valid()
	udp.ExitTargetID = sshExit.ID
	udp.PortRules[0].Transports = []string{"udp"}
	if err := app.validateTunnel(context.Background(), u, o.ID, udp); err == nil {
		t.Fatal("accepted UDP SSH exit in multi-port mode")
	}
}
func TestTargetNetInfoRequiresAgentEndpoint(t *testing.T) {
	app, u, o := tunnelFixture(t)
	ctx := context.Background()
	attachForwardTestAgent(t, app, "netinfo-agent")
	agentTarget := tunnelAgentTarget(t, app, u, o, "netinfo-agent")
	sshTarget := tunnelSSHTarget(t, app, u, o, "netinfo-ssh", tunnelTestSSH(t), "")

	out, err := app.targetNetInfo(ctx, sshTarget, nil)
	if err != nil {
		t.Fatalf("SSH target probe failed: %v", err)
	}
	if out.Supported || out.Reason != "egress interface discovery requires an Agent endpoint" {
		t.Fatalf("SSH target must report unsupported: %+v", out)
	}
	probe, err := app.targetNetInfo(ctx, agentTarget, nil)
	if err != nil {
		t.Fatalf("Agent probe failed: %v", err)
	}
	if !probe.Supported || probe.NetInfo == nil || probe.NetInfo.Platform == "" || len(probe.NetInfo.Interfaces) == 0 {
		t.Fatalf("Agent must report interfaces: %+v", probe)
	}
	// A rejected egress selection is reported alongside the capability report.
	bad := protocoltypes.EgressSpec{Mode: protocoltypes.EgressModeProxy}
	rejected, err := app.targetNetInfo(ctx, agentTarget, &bad)
	if err != nil {
		t.Fatalf("Agent probe with invalid egress failed: %v", err)
	}
	if rejected.EgressError == "" {
		t.Fatalf("invalid egress selection must be reported: %+v", rejected)
	}
}
func TestMultiPortBastionExitListensOnEveryRule(t *testing.T) {
	app, u, o := tunnelFixture(t)
	first := tunnelEcho(t)
	second := tunnelEcho(t)
	firstPort := freeTunnelPort(t)
	secondPort := freeTunnelPort(t)
	config := store.TunnelConfig{Protocol: "port", Name: "multi listen", PortRules: []protocoltypes.PortRule{
		{ID: "one", ListenHost: "127.0.0.1", ListenPort: firstPort, Transports: []string{"tcp"}, DestinationHost: "127.0.0.1", DestinationPort: first.Addr().(*net.TCPAddr).Port},
		{ID: "two", ListenHost: "127.0.0.1", ListenPort: secondPort, Transports: []string{"tcp"}, DestinationHost: "127.0.0.1", DestinationPort: second.Addr().(*net.TCPAddr).Port, Egress: &protocoltypes.EgressSpec{Family: protocoltypes.EgressFamilyIPv4}},
	}}
	view, err := app.saveManagedTunnel(context.Background(), u, store.Tunnel{OrganizationID: o.ID, CreatedBy: u.ID, TunnelConfig: config})
	if err != nil {
		t.Fatal(err)
	}
	enabled, err := app.setManagedTunnelEnabled(context.Background(), u, view.Tunnel, true)
	if err != nil {
		t.Fatal(err)
	}
	v := enabled.Tunnel
	waitForward(t, app, v, func(s tunnelStatus) bool { return s.Status == "running" && s.Error == "" })
	for _, port := range []int{firstPort, secondPort} {
		c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", itoa(port)), 5*time.Second)
		if err != nil {
			t.Fatalf("rule port %d not listening: %v", port, err)
		}
		roundtripTunnel(t, c)
		c.Close()
	}
	if _, err := app.setManagedTunnelEnabled(context.Background(), u, v, false); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		blocked := 0
		for _, port := range []int{firstPort, secondPort} {
			if c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", itoa(port)), 200*time.Millisecond); err != nil {
				blocked++
			} else {
				c.Close()
			}
		}
		if blocked == 2 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("multi-port listeners not released after stop")
}
func TestTunnelAPIMultiPortRulesRoundTrip(t *testing.T) {
	srv, admin, app := newAPITestServer(t)
	defer srv.Close()
	postJSON(t, admin, srv.URL+"/api/auth/login", map[string]string{"email": "admin", "password": "admin-pass"}, 200, nil)
	u, err := app.store.Repository().GetUserByEmail(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	org, err := app.store.Repository().GetPersonalOrganizationForUser(context.Background(), u.ID)
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{
		"protocol":        "port",
		"organization_id": org.ID,
		"name":            "multi-port API",
		"port_rules": []any{
			map[string]any{"id": "web", "listen_host": "127.0.0.1", "listen_port": freeTunnelPort(t), "transports": []string{"tcp"}, "destination_host": "127.0.0.1", "destination_port": 8080, "egress": map[string]any{"interface": "eth0"}},
			map[string]any{"id": "dns", "listen_host": "127.0.0.1", "listen_port": freeTunnelPort(t), "transports": []string{"tcp", "udp"}, "destination_host": "127.0.0.1", "destination_port": 53},
		},
	}
	var result struct {
		Tunnel apiTunnel `json:"tunnel"`
	}
	// listen_port is not required when port_rules is present.
	postJSON(t, admin, srv.URL+"/api/tunnels", body, 200, &result)
	if len(result.Tunnel.PortRules) != 2 || result.Tunnel.PortRules[0].ID != "web" || result.Tunnel.PortRules[0].Egress == nil || result.Tunnel.PortRules[0].Egress.Interface != "eth0" {
		t.Fatalf("port rules not persisted: %+v", result.Tunnel.PortRules)
	}
	if result.Tunnel.ListenPort != result.Tunnel.PortRules[0].ListenPort {
		t.Fatalf("legacy listen port not mirrored: %+v", result.Tunnel.TunnelConfig)
	}
	// A duplicate listen endpoint is rejected.
	body["port_rules"] = []any{
		map[string]any{"id": "one", "listen_host": "127.0.0.1", "listen_port": 9101, "transports": []string{"tcp"}, "destination_host": "127.0.0.1", "destination_port": 80},
		map[string]any{"id": "two", "listen_host": "127.0.0.1", "listen_port": 9101, "transports": []string{"tcp"}, "destination_host": "127.0.0.1", "destination_port": 81},
	}
	postJSON(t, admin, srv.URL+"/api/tunnels", body, 400, nil)
	// An invalid egress spec is rejected.
	body["port_rules"] = []any{
		map[string]any{"id": "one", "listen_host": "127.0.0.1", "listen_port": 9102, "transports": []string{"tcp"}, "destination_host": "127.0.0.1", "destination_port": 80, "egress": map[string]any{"mode": "proxy"}},
	}
	postJSON(t, admin, srv.URL+"/api/tunnels", body, 400, nil)
}
