package forward

import (
	"context"
	"io"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// TestStartRulesRoutesEachPortIndependently proves a multi-port configuration
// keeps the ports isolated: each rule dials its own route grant and traffic on
// one port never reaches the other rule's destination.
func TestStartRulesRoutesEachPortIndependently(t *testing.T) {
	first, second := echoServer(t), echoServer(t)
	defer first.Close()
	defer second.Close()
	var firstDials, secondDials atomic.Int64
	r, err := StartRules(context.Background(), []RuleSpec{
		{ID: "a", ListenAddress: "127.0.0.1:0", Transports: []string{"tcp"}, RouteIDs: map[string]string{"tcp": "a:tcp"}},
		{ID: "b", ListenAddress: "127.0.0.1:0", Transports: []string{"tcp"}, RouteIDs: map[string]string{"tcp": "b:tcp"}},
	}, func(ctx context.Context, id, _ string) (net.Conn, error) {
		switch id {
		case "a:tcp":
			firstDials.Add(1)
			return DialNetwork(ctx, "tcp", first.Addr().String())
		case "b:tcp":
			secondDials.Add(1)
			return DialNetwork(ctx, "tcp", second.Addr().String())
		}
		t.Fatalf("unexpected route grant %q", id)
		return nil, net.ErrClosed
	}, &portEvents{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	addresses := splitAddresses(t, r.State().ListenAddress, 2)
	// Two ephemeral listeners can land on consecutive ports, which the status line
	// folds into one "host:a-b" range.
	firstEndpoint, secondEndpoint := expandAddress(t, addresses[0], 0), expandAddress(t, addresses[0], 1)
	roundTrip(t, firstEndpoint, "first")
	roundTrip(t, secondEndpoint, "second")
	if firstDials.Load() != 1 || secondDials.Load() != 1 {
		t.Fatalf("route isolation broken: %d / %d dials", firstDials.Load(), secondDials.Load())
	}
}

func echoServer(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); io.Copy(conn, conn) }()
		}
	}()
	return listener
}

// expandAddress resolves the index-th endpoint of a compact "host:a-b" range.
func expandAddress(t *testing.T, value string, index int) string {
	t.Helper()
	host, rawPort, err := net.SplitHostPort(value)
	if err != nil {
		t.Fatalf("bad listen address %q: %v", value, err)
	}
	startText, endText, isRange := strings.Cut(rawPort, "-")
	start, err := strconv.Atoi(startText)
	if err != nil {
		t.Fatalf("bad port %q: %v", rawPort, err)
	}
	if !isRange {
		return net.JoinHostPort(host, strconv.Itoa(start))
	}
	end, err := strconv.Atoi(endText)
	if err != nil || start+index > end {
		t.Fatalf("range %q has no index %d", value, index)
	}
	return net.JoinHostPort(host, strconv.Itoa(start+index))
}

func splitAddresses(t *testing.T, value string, want int) []string {
	t.Helper()
	var out []string
	for _, part := range splitOnComma(value) {
		if part != "" {
			out = append(out, part)
		}
	}
	// Consecutive ephemeral ports compact into one "host:a-b" range, which still
	// describes the wanted number of listeners.
	if len(out) == 1 && want > 1 {
		host, rawPort, err := net.SplitHostPort(out[0])
		if err == nil {
			if startText, endText, ok := strings.Cut(rawPort, "-"); ok {
				start, startErr := strconv.Atoi(startText)
				end, endErr := strconv.Atoi(endText)
				if startErr == nil && endErr == nil && end-start+1 == want {
					return out
				}
				_ = host
			}
		}
	}
	if len(out) != want {
		t.Fatalf("expected %d listen addresses, got %v", want, out)
	}
	return out
}

func splitOnComma(value string) []string {
	var out []string
	start := 0
	for i := 0; i < len(value); i++ {
		if value[i] == ',' {
			out = append(out, trimSpace(value[start:i]))
			start = i + 1
		}
	}
	return append(out, trimSpace(value[start:]))
}

func trimSpace(value string) string {
	for len(value) > 0 && value[0] == ' ' {
		value = value[1:]
	}
	for len(value) > 0 && value[len(value)-1] == ' ' {
		value = value[:len(value)-1]
	}
	return value
}

func roundTrip(t *testing.T, address, payload string) {
	t.Helper()
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	echo := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, echo); err != nil {
		t.Fatal(err)
	}
	if string(echo) != payload {
		t.Fatalf("round trip mismatch: %q", echo)
	}
}
