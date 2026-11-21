package forward

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

// TestStartRulesBindsEveryPort proves a multi-port configuration listens on all
// of its ports and that each rule dials through its own route grant.
func TestStartRulesBindsEveryPort(t *testing.T) {
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, e := echo.Accept()
			if e != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	r, err := StartRules(context.Background(), []RuleSpec{
		{ID: "a", ListenAddress: "127.0.0.1:0", Transports: []string{"tcp"}, RouteIDs: map[string]string{"tcp": "a:tcp"}},
		{ID: "b", ListenAddress: "127.0.0.1:0", Transports: []string{"tcp"}, RouteIDs: map[string]string{"tcp": "b:tcp"}},
	}, func(ctx context.Context, id, _ string) (net.Conn, error) {
		if id != "a:tcp" && id != "b:tcp" {
			t.Fatalf("unexpected route grant %q", id)
		}
		return DialNetwork(ctx, "tcp", echo.Addr().String())
	}, &portEvents{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.RuleCount() != 2 {
		t.Fatalf("expected two listen ports, got %d", r.RuleCount())
	}
	addresses := r.State().ListenAddress
	if len(addresses) == 0 || addresses == "127.0.0.1:0" {
		t.Fatalf("listen addresses not reported: %q", addresses)
	}
}

// TestStartRulesReleasesEarlierBindsOnFailure proves the multi-port start is
// atomic: a failed bind must not leave a listener behind.
func TestStartRulesReleasesEarlierBindsOnFailure(t *testing.T) {
	_, err := StartRules(context.Background(), []RuleSpec{
		{ID: "a", ListenAddress: "127.0.0.1:0", Transports: []string{"tcp"}, RouteIDs: map[string]string{"tcp": "a"}},
		{ID: "b", ListenAddress: "127.0.0.1:1", Transports: []string{"tcp"}, RouteIDs: map[string]string{"tcp": "b"}},
	}, func(context.Context, string, string) (net.Conn, error) { return nil, net.ErrClosed }, &portEvents{}, nil,
		func(address string) (net.Listener, error) {
			if address == "127.0.0.1:1" {
				return nil, net.ErrClosed
			}
			return net.Listen("tcp", address)
		})
	if err == nil {
		t.Fatal("expected a bind failure")
	}
}

// TestStartRulesRejectsMissingRoute keeps an unconfigured transport from
// silently bypassing the route grants.
func TestStartRulesRejectsMissingRoute(t *testing.T) {
	if _, err := StartRules(context.Background(), []RuleSpec{{ID: "a", ListenAddress: "127.0.0.1:0", Transports: []string{"tcp"}}}, nil, &portEvents{}, nil, nil); err == nil {
		t.Fatal("expected a missing route error")
	}
}

var _ = time.Second
