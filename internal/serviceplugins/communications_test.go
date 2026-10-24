package serviceplugins

import "testing"

type communicationsFixture []string

func (p communicationsFixture) ReadyCapabilities() []string { return p }

func TestCommunicationsLocalRootBoundary(t *testing.T) {
	for _, e := range []Environment{{"android", 2000}, {"android", 10000}, {"linux", 0}, {"windows", -1}, {}} {
		r, err := newForEnvironment(e, LocalCommunications(communicationsFixture{"call", "audio", "sms"}))
		if err != nil || len(r.Descriptors()) != 0 {
			t.Fatalf("exposed outside Android root: %+v %v", e, err)
		}
	}
	for _, provider := range []CommunicationsProvider{nil, communicationsFixture{}, communicationsFixture{"unknown"}} {
		r, err := newForEnvironment(Environment{"android", 0}, LocalCommunications(provider))
		if err != nil || len(r.Descriptors()) != 0 {
			t.Fatalf("unready provider exposed: %v", err)
		}
	}
}
func TestCommunicationsCapabilitiesOwnedAndExact(t *testing.T) {
	input := communicationsFixture{"sms", "call", "sms", "unknown"}
	p := LocalCommunications(input)
	input[1] = "audio"
	r, err := newForEnvironment(Environment{"android", 0}, p)
	if err != nil {
		t.Fatal(err)
	}
	d := r.Descriptors()[0]
	if len(d.Tools) != 2 || d.Tools[0].Name != "communications_call" || d.Tools[1].Name != "communications_sms" {
		t.Fatalf("unexpected contributions: %+v", d)
	}
	d.Tools[0].Name = "mutated"
	if p.Describe().Tools[0].Name != "communications_call" {
		t.Fatal("descriptor aliased")
	}
	sink := &collector{}
	if err := r.Register(sink, map[string]bool{"*": true, "desktop": true}); err != nil || len(sink.values) != 0 {
		t.Fatal("implicit group grant")
	}
	if err := r.Register(sink, map[string]bool{"communications": true}); err != nil || len(sink.values) != 1 {
		t.Fatal("exact contribution missing")
	}
}
