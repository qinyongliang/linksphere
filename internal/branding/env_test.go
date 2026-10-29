package branding

import "testing"

func TestLookupEnvironmentAliases(t *testing.T) {
	for _, tc := range []struct {
		label       string
		values      map[string]string
		value, name string
		ok          bool
	}{
		{"legacy", map[string]string{"GOSSHD_SSH_HOST": "old.example"}, "old.example", "GOSSHD_SSH_HOST", true},
		{"canonical", map[string]string{"LINKSPHERE_SSH_HOST": "new.example"}, "new.example", "LINKSPHERE_SSH_HOST", true},
		{"canonical wins", map[string]string{"LINKSPHERE_SSH_HOST": "new.example", "GOSSHD_SSH_HOST": "old.example"}, "new.example", "LINKSPHERE_SSH_HOST", true},
		{"blank canonical falls back", map[string]string{"LINKSPHERE_SSH_HOST": "  ", "GOSSHD_SSH_HOST": "old.example"}, "old.example", "GOSSHD_SSH_HOST", true},
		{"absent", map[string]string{}, "", "", false},
	} {
		t.Run(tc.label, func(t *testing.T) {
			v, n, ok := Lookup(func(k string) (string, bool) { v, ok := tc.values[k]; return v, ok }, "SSH_HOST")
			if v != tc.value || n != tc.name || ok != tc.ok {
				t.Fatalf("unexpected resolution: %q %q %v", v, n, ok)
			}
		})
	}
}

func TestIdentityAliasConflict(t *testing.T) {
	values := map[string]string{"LINKSPHERE_SECRET_KEY": "new-test-value", "GOSSHD_SECRET_KEY": "old-test-value"}
	lookup := func(k string) (string, bool) { v, ok := values[k]; return v, ok }
	value, err := ConsistentValue(lookup, "SECRET_KEY")
	if err == nil || value != "" || err.Error() != "LINKSPHERE_SECRET_KEY and GOSSHD_SECRET_KEY disagree" {
		t.Fatal("conflicting identity aliases must fail without exposing values")
	}
	values["LINKSPHERE_SECRET_KEY"] = "old-test-value"
	value, err = ConsistentValue(lookup, "SECRET_KEY")
	if err != nil || value != "old-test-value" {
		t.Fatal("same key aliases must preserve old identity")
	}
	delete(values, "LINKSPHERE_SECRET_KEY")
	value, err = ConsistentValue(lookup, "SECRET_KEY")
	if err != nil || value != "old-test-value" {
		t.Fatal("legacy-only key must remain readable")
	}
}
