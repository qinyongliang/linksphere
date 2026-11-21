package egress

import (
	"bufio"
	"context"
	"encoding/binary"
	"net"
	"testing"

	protocoltypes "github.com/qinyongliang/linksphere/internal/protocol/types"
)

func TestPoolRotateStaysInsideRange(t *testing.T) {
	p := newPool(protocoltypes.EgressSpec{PoolStart: "2001:db8:1::100", PoolSize: 8, PoolMode: protocoltypes.EgressPoolRotate})
	base := binary.BigEndian.Uint64(net.ParseIP("2001:db8:1::100").To16()[8:])
	seen := map[int]bool{}
	for i := 0; i < 64; i++ {
		alloc := p.Next()
		if alloc.Index < 0 || alloc.Index > 7 {
			t.Fatalf("index %d outside pool", alloc.Index)
		}
		got := binary.BigEndian.Uint64(alloc.IP[8:])
		if got < base || got >= base+8 {
			t.Fatalf("address outside pool: %s", alloc.IP)
		}
		seen[alloc.Index] = true
	}
	if len(seen) < 2 {
		t.Fatalf("rotate did not vary the address: %v", seen)
	}
}

func TestPoolStickyAdvancesAndWraps(t *testing.T) {
	p := newPool(protocoltypes.EgressSpec{PoolStart: "2001:db8:1::", PoolSize: 4, PoolMode: protocoltypes.EgressPoolSticky})
	last := p.Next().Index
	for i := 0; i < 20; i++ {
		next := p.Next()
		if next.Index < 0 || next.Index > 3 {
			t.Fatalf("sticky index outside pool: %d", next.Index)
		}
		last = next.Index
	}
	if last < 0 || last > 3 {
		t.Fatalf("sticky wrapped incorrectly: %d", last)
	}
}

func TestPoolsReuseSequenceByKey(t *testing.T) {
	pools := NewPools()
	spec := protocoltypes.EgressSpec{PoolStart: "2001:db8:1::", PoolSize: 4, IPv6Mode: protocoltypes.EgressIPv6Pool}
	if pools.For(spec.Key(), spec) != pools.For(spec.Key(), spec) {
		t.Fatal("same pool key must reuse one sequence")
	}
}

func TestNewRejectsUnsupportedCombinations(t *testing.T) {
	cases := []protocoltypes.EgressSpec{
		{Mode: "tunnel"},
		{Mode: protocoltypes.EgressModeProxy, Interface: "eth0", ProxyURL: "socks5://127.0.0.1:1080"},
		{IPv6Mode: protocoltypes.EgressIPv6Pool, Family: protocoltypes.EgressFamilyIPv4, PoolStart: "2001:db8::1", PoolSize: 4},
		{IPv6Mode: protocoltypes.EgressIPv6Pool, PoolStart: "192.0.2.1", PoolSize: 4},
		{IPv6Mode: protocoltypes.EgressIPv6Pool, PoolStart: "2001:db8::1", PoolSize: 0},
		{Mode: protocoltypes.EgressModeProxy, ProxyURL: "ftp://example.com:21"},
	}
	for _, spec := range cases {
		if _, err := New(spec, NewPools()); err == nil {
			t.Fatalf("expected rejection for %+v", spec)
		}
	}
}

func TestParseProxyURLDefaults(t *testing.T) {
	p, err := parseProxyURL("socks5://user:pass@proxy.example:1080")
	if err != nil {
		t.Fatal(err)
	}
	if p.address != "proxy.example:1080" || p.username != "user" || p.password != "pass" {
		t.Fatalf("unexpected endpoint: %+v", p)
	}
	p, err = parseProxyURL("http://proxy.example")
	if err != nil || p.address != "proxy.example:80" {
		t.Fatalf("default http port: %+v %v", p, err)
	}
}

// TestProxyDialerSOCKS5Handshake drives a fake SOCKS5 proxy to verify the
// request bytes and that the established tunnel is returned to the caller.
func TestProxyDialerSOCKS5Handshake(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		greeting := make([]byte, 3)
		if _, err := reader.Read(greeting); err != nil {
			return
		}
		conn.Write([]byte{0x05, 0x00})
		head := make([]byte, 5)
		if _, err := reader.Read(head); err != nil {
			return
		}
		rest := make([]byte, int(head[4])+2)
		reader.Read(rest)
		conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 127, 0, 0, 1, 0, 80})
		buf := make([]byte, 4)
		reader.Read(buf)
		conn.Write([]byte("pong"))
	}()
	dialer, err := New(protocoltypes.EgressSpec{Mode: protocoltypes.EgressModeProxy, ProxyURL: "socks5://" + listener.Addr().String()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := dialer.DialContext(context.Background(), "tcp", "127.0.0.1:80")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Write([]byte("ping"))
	echo := make([]byte, 4)
	if _, err := conn.Read(echo); err != nil || string(echo) != "pong" {
		t.Fatalf("tunnel not bridged: %q %v", echo, err)
	}
	<-done
}

func TestProbeReportsInterfaces(t *testing.T) {
	info := Probe()
	if len(info.Interfaces) == 0 {
		t.Fatal("expected at least one interface")
	}
	for _, iface := range info.Interfaces {
		if iface.Name == "" {
			t.Fatal("interface name must not be empty")
		}
	}
}
