package serviceplugins

import (
	"encoding/json"
	"errors"
	"testing"
)

type fixturePlugin struct {
	description Descriptor
	rootOnly    bool
}

func (p fixturePlugin) Describe() Descriptor { return p.description }
func (p fixturePlugin) Available(e Environment) bool {
	return !p.rootOnly || (e.GOOS() == "android" && e.UID() == 0)
}
func fixture() fixturePlugin {
	return fixturePlugin{rootOnly: true, description: Descriptor{
		ID: "phone", Group: "phone",
		Permissions: []Permission{{ID: "call", Description: "Explicit call permission"}},
		Settings:    []Setting{{Key: "policy", SchemaJSON: json.RawMessage(`{"type":"object"}`)}},
		Tools:       []Tool{{Name: "phone_call", Permission: "call"}},
		Events:      []string{"call_changed"},
	}}
}

type collector struct {
	values []Descriptor
	err    error
}

func (c *collector) RegisterPlugin(d Descriptor) error { c.values = append(c.values, d); return c.err }

func TestRuntimeEligibility(t *testing.T) {
	for _, e := range []Environment{{"android", 2000}, {"android", 10000}, {"linux", 0}, {"windows", -1}, {}} {
		r, err := newForEnvironment(e, fixture())
		if err != nil || len(r.Descriptors()) != 0 {
			t.Fatalf("unexpected eligibility: %+v %v", e, err)
		}
	}
	r, err := newForEnvironment(Environment{"android", 0}, fixture())
	if err != nil || len(r.Descriptors()) != 1 {
		t.Fatalf("root service fixture: %v", err)
	}
}

func TestExactGroupsAndOwnedMetadata(t *testing.T) {
	p := fixture()
	r, err := newForEnvironment(Environment{"android", 0}, p)
	if err != nil {
		t.Fatal(err)
	}
	p.description.Settings[0].SchemaJSON[0] = '!'
	p.description.Tools[0].Name = "changed"
	d := r.Descriptors()
	d[0].Tools[0].Name = "changed_again"
	d[0].Settings[0].SchemaJSON[0] = '!'
	if r.Descriptors()[0].Tools[0].Name != "phone_call" || !json.Valid(r.Descriptors()[0].Settings[0].SchemaJSON) {
		t.Fatal("metadata aliased")
	}
	c := &collector{}
	if err := r.Register(c, map[string]bool{"*": true, "desktop": true}); err != nil || len(c.values) != 0 {
		t.Fatal("wildcard exposed plugin")
	}
	if err := r.Register(c, map[string]bool{"phone": true}); err != nil || len(c.values) != 1 {
		t.Fatal("exact group missing")
	}
	c.values[0].Settings[0].SchemaJSON[0] = '!'
	if !json.Valid(r.Descriptors()[0].Settings[0].SchemaJSON) {
		t.Fatal("registrar can mutate stored descriptor")
	}
	stop := errors.New("stop")
	if err := r.Register(&collector{err: stop}, map[string]bool{"phone": true}); !errors.Is(err, stop) {
		t.Fatal("registration failure hidden")
	}
}

func TestValidationEvenWhenUnavailable(t *testing.T) {
	for _, change := range []func(*fixturePlugin){
		func(p *fixturePlugin) { p.description.ID = "*" },
		func(p *fixturePlugin) { p.description.Group = "" },
		func(p *fixturePlugin) { p.description.Tools[0].Permission = "unknown" },
		func(p *fixturePlugin) { p.description.Settings[0].SchemaJSON = json.RawMessage("null") },
		func(p *fixturePlugin) { p.description.Events = append(p.description.Events, "call_changed") },
		func(p *fixturePlugin) {
			p.description.Permissions = append(p.description.Permissions, p.description.Permissions[0])
		},
	} {
		p := fixture()
		change(&p)
		if _, err := newForEnvironment(Environment{"linux", 0}, p); err == nil {
			t.Fatal("invalid descriptor accepted")
		}
	}
	if _, err := newForEnvironment(Environment{"linux", 0}, fixture(), fixture()); err == nil {
		t.Fatal("hidden duplicate accepted")
	}
	if _, err := newForEnvironment(Environment{}, nil); err == nil {
		t.Fatal("nil accepted")
	}
}

func TestTypedNilAndNilRegistry(t *testing.T) {
	var plugin *fixturePlugin
	if _, err := newForEnvironment(Environment{}, plugin); err == nil {
		t.Fatal("typed nil plugin accepted")
	}
	r, err := newForEnvironment(Environment{"android", 0}, fixture())
	if err != nil {
		t.Fatal(err)
	}
	var sink *collector
	if err := r.Register(sink, map[string]bool{"phone": true}); err == nil {
		t.Fatal("typed nil registrar accepted")
	}
	var missing *Registry
	if len(missing.Descriptors()) != 0 {
		t.Fatal("nil registry metadata")
	}
	if err := missing.Register(&collector{}, nil); err == nil {
		t.Fatal("nil registry registered")
	}
}

func TestDeterministicOrder(t *testing.T) {
	a, z := fixture(), fixture()
	a.description.ID, a.description.Group, a.description.Tools[0].Name = "a", "a", "a_call"
	z.description.ID, z.description.Group, z.description.Tools[0].Name = "z", "z", "z_call"
	r, err := newForEnvironment(Environment{"android", 0}, z, a)
	if err != nil {
		t.Fatal(err)
	}
	c := &collector{}
	if err := r.Register(c, map[string]bool{"a": true, "z": true}); err != nil {
		t.Fatal(err)
	}
	if len(c.values) != 2 || c.values[0].ID != "a" || c.values[1].ID != "z" {
		t.Fatal("nondeterministic registration")
	}
}
