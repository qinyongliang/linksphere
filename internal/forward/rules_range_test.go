package forward

import (
	"net"
	"testing"

	protocoltypes "github.com/qinyongliang/linksphere/internal/protocol/types"
)

// TestExpandPortRuleCoversRange proves one stored rule fans out into one listener
// per port while every listener keeps the same route grants.
func TestExpandPortRuleCoversRange(t *testing.T) {
	rule := protocoltypes.PortRule{ID: "seq", ListenHost: "0.0.0.0", ListenPort: 10000, ListenPortEnd: 10002, Transports: []string{"tcp"}}
	specs := ExpandPortRule(rule, map[string]string{"tcp": "seq:tcp"}, Location{ID: "seq"})
	if len(specs) != 3 {
		t.Fatalf("expected three listeners, got %d", len(specs))
	}
	for i, spec := range specs {
		if _, port, err := net.SplitHostPort(spec.ListenAddress); err != nil || port != []string{"10000", "10001", "10002"}[i] {
			t.Fatalf("unexpected listen address %q", spec.ListenAddress)
		}
		if spec.RouteIDs["tcp"] != "seq:tcp" {
			t.Fatalf("route grant lost: %+v", spec)
		}
	}
}

// TestExpandPortRuleSinglePort keeps a range-free rule at exactly one listener.
func TestExpandPortRuleSinglePort(t *testing.T) {
	rule := protocoltypes.PortRule{ID: "web", ListenPort: 8080, Transports: []string{"tcp"}}
	if specs := ExpandPortRule(rule, map[string]string{"tcp": "web:tcp"}, Location{}); len(specs) != 1 {
		t.Fatalf("expected one listener, got %d", len(specs))
	}
}

// TestCompactListenAddressesFoldsRanges keeps a large range readable in status.
func TestCompactListenAddressesFoldsRanges(t *testing.T) {
	got := compactListenAddresses([]string{"0.0.0.0:10000", "0.0.0.0:10001", "0.0.0.0:10002", "0.0.0.0:11000", "127.0.0.1:8080"})
	if got != "0.0.0.0:10000-10002, 0.0.0.0:11000, 127.0.0.1:8080" {
		t.Fatalf("unexpected compaction: %q", got)
	}
}
