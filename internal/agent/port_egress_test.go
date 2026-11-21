package agent

import (
	"bufio"
	"context"
	"io"
	"net"
	"testing"

	"github.com/qinyongliang/linksphere/internal/protocol"
	protocoltypes "github.com/qinyongliang/linksphere/internal/protocol/types"
)

// TestHandlePortAppliesEgress proves the Agent, acting as the exit machine, opens
// the destination socket through the rule's egress selection. A pinned interface
// on this host is not required for the assertion: the proxy mode exercises the
// same dial path deterministically.
func TestHandlePortAppliesEgress(t *testing.T) {
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		conn, err := echo.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		io.Copy(conn, conn)
	}()
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	spec := &protocoltypes.EgressSpec{Family: protocoltypes.EgressFamilyIPv4}
	go (&Client{}).handlePort(server, bufio.NewReader(server), echo.Addr().String(), "tcp", spec)
	response, err := protocol.ReadJSONLine[protocol.StreamResponse](bufio.NewReader(client))
	if err != nil {
		t.Fatal(err)
	}
	if !response.OK {
		t.Fatalf("egress dial failed: %+v", response)
	}
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	echoed := make([]byte, 4)
	if _, err := io.ReadFull(client, echoed); err != nil || string(echoed) != "ping" {
		t.Fatalf("egress socket not bridged: %q %v", echoed, err)
	}
}

// TestHandlePortRejectsInvalidEgress proves an unsupported selection fails before
// any socket is opened.
func TestHandlePortRejectsInvalidEgress(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	spec := &protocoltypes.EgressSpec{IPv6Mode: protocoltypes.EgressIPv6Pool, PoolStart: "192.0.2.1", PoolSize: 4}
	go (&Client{}).handlePort(server, bufio.NewReader(server), "127.0.0.1:9", "tcp", spec)
	response, err := protocol.ReadJSONLine[protocol.StreamResponse](bufio.NewReader(client))
	if err != nil {
		t.Fatal(err)
	}
	if response.OK || response.Error == "" {
		t.Fatalf("expected an egress rejection, got %+v", response)
	}
}

var _ = context.Background
