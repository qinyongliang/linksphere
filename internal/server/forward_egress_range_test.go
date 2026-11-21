package server

import (
	"context"
	"testing"

	protocoltypes "github.com/qinyongliang/linksphere/internal/protocol/types"
	"github.com/qinyongliang/linksphere/internal/store"
)

func rangeRule(id string, first, last int) protocoltypes.PortRule {
	return protocoltypes.PortRule{ID: id, ListenHost: "0.0.0.0", ListenPort: first, ListenPortEnd: last, Transports: []string{"tcp"}, DestinationHost: "127.0.0.1", DestinationPort: 80}
}

// TestValidatePortRulesRanges covers the inclusive range contract and the
// overlap rule shared by ranges and single ports.
func TestValidatePortRulesRanges(t *testing.T) {
	ok := []protocoltypes.PortRule{rangeRule("range", 10000, 10500), {ID: "single", ListenHost: "0.0.0.0", ListenPort: 8080, Transports: []string{"tcp"}, DestinationHost: "127.0.0.1", DestinationPort: 80}}
	if err := validatePortRules(ok); err != nil {
		t.Fatalf("valid range rejected: %v", err)
	}
	cases := map[string][]protocoltypes.PortRule{
		"overlapping ranges": {rangeRule("a", 10000, 10100), rangeRule("b", 10100, 10200)},
		"range and port":     {rangeRule("a", 10000, 10100), {ID: "b", ListenHost: "0.0.0.0", ListenPort: 10050, Transports: []string{"tcp"}, DestinationHost: "127.0.0.1", DestinationPort: 80}},
		"inverted":           {rangeRule("a", 10100, 10000)},
		"oversized":          {rangeRule("a", 10000, 10000+protocoltypes.MaxRangePorts)},
	}
	for name, rules := range cases {
		if err := validatePortRules(rules); err == nil {
			t.Fatalf("%s: expected rejection", name)
		}
	}
}

// TestPortRulePlanPreservesRange proves a range survives planning; the Agent
// expands it into one listener per port.
func TestPortRulePlanPreservesRange(t *testing.T) {
	app, u, o := tunnelFixture(t)
	attachForwardTestAgent(t, app, "range-entry")
	attachForwardTestAgent(t, app, "range-exit")
	entry := tunnelAgentTarget(t, app, u, o, "range-entry")
	exit := tunnelAgentTarget(t, app, u, o, "range-exit")
	config := store.TunnelConfig{Protocol: "port", Name: "range", EntryTargetID: entry.ID, ExitTargetID: exit.ID, PortRules: []protocoltypes.PortRule{rangeRule("seq", 10000, 10010)}}
	tun, err := app.store.Repository().SaveTunnel(context.Background(), store.Tunnel{TunnelConfig: config, OrganizationID: o.ID, CreatedBy: u.ID})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := app.forwards.build(tun)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.deployment.PortRules) != 1 {
		t.Fatalf("rules not planned: %+v", plan.deployment.PortRules)
	}
	if got := plan.deployment.PortRules[0].ListenPortEnd; got != 10010 {
		t.Fatalf("range end lost in plan: %d", got)
	}
	if len(plan.deployment.Routes) != 1 {
		t.Fatalf("a range shares one route grant per transport, got %d", len(plan.deployment.Routes))
	}
}

// TestLegacyConfigProjectsSingleRuleWithoutRange keeps old configurations intact.
func TestLegacyConfigProjectsSingleRuleWithoutRange(t *testing.T) {
	rules := store.TunnelConfig{Protocol: "port", ListenHost: "127.0.0.1", ListenPort: 8080, Transports: []string{"tcp"}, DestinationHost: "127.0.0.1", DestinationPort: 80}.Rules()
	if len(rules) != 1 || rules[0].ListenPortEnd != 0 {
		t.Fatalf("unexpected projection: %+v", rules)
	}
}
