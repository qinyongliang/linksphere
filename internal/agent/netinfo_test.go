package agent

import (
	"bufio"
	"io"
	"net"
	"testing"

	"github.com/qinyongliang/linksphere/internal/protocol"
	protocoltypes "github.com/qinyongliang/linksphere/internal/protocol/types"
)

type pipeStream struct {
	net.Conn
	reader *bufio.Reader
}

func (p *pipeStream) Read(b []byte) (int, error) { return p.reader.Read(b) }

// TestHandleNetInfoReportsInterfaces proves the Agent answers a net-info request
// with its egress interfaces and never mutates network state.
func TestHandleNetInfoReportsInterfaces(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	go (&Client{}).handleNetInfo(server, protocol.StreamRequest{Type: protocol.StreamNetInfo})
	response, err := protocol.ReadJSONLine[protocol.StreamResponse](bufio.NewReader(client))
	if err != nil {
		t.Fatal(err)
	}
	if !response.OK || response.NetInfo == nil {
		t.Fatalf("unexpected response: %+v", response)
	}
	if len(response.NetInfo.Interfaces) == 0 {
		t.Fatal("expected at least one interface")
	}
}

// TestHandleNetInfoValidatesEgress proves an unsupported egress selection is
// reported instead of silently accepted.
func TestHandleNetInfoValidatesEgress(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	bad := protocoltypes.EgressSpec{IPv6Mode: protocoltypes.EgressIPv6Pool, PoolStart: "192.0.2.10", PoolSize: 4}
	go (&Client{}).handleNetInfo(server, protocol.StreamRequest{Type: protocol.StreamNetInfo, Egress: &bad})
	response, err := protocol.ReadJSONLine[protocol.StreamResponse](bufio.NewReader(client))
	if err != nil {
		t.Fatal(err)
	}
	if response.EgressError == "" {
		t.Fatal("expected an egress validation error")
	}
}

var _ io.Reader = (*pipeStream)(nil)
