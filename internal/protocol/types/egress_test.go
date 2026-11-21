package types

import "testing"

func TestPortRangeValidation(t *testing.T) {
	valid := PortRule{ListenPort: 10000, ListenPortEnd: 11000}
	if first, last := valid.PortRange(); first != 10000 || last != 11000 {
		t.Fatalf("range %d-%d", first, last)
	}
	if valid.PortCount() != 1001 {
		t.Fatalf("count %d", valid.PortCount())
	}
	if err := valid.ValidateRange(); err != nil {
		t.Fatalf("valid range rejected: %v", err)
	}
	if err := (PortRule{ListenPort: 10000, ListenPortEnd: 9999}).ValidateRange(); err == nil {
		t.Fatal("inverted range accepted")
	}
	if err := (PortRule{ListenPort: 10000, ListenPortEnd: 10000 + MaxRangePorts}).ValidateRange(); err == nil {
		t.Fatal("oversized range accepted")
	}
	if err := (PortRule{ListenPort: 0}).ValidateRange(); err == nil {
		t.Fatal("zero port accepted")
	}
	if first, last := (PortRule{ListenPort: 8080}).PortRange(); first != 8080 || last != 8080 {
		t.Fatalf("single port range %d-%d", first, last)
	}
}
