package agent

import (
	"io"

	"github.com/qinyongliang/linksphere/internal/egress"
	"github.com/qinyongliang/linksphere/internal/protocol"
)

// handleNetInfo reports the egress interfaces and addresses of this machine so the
// console can offer them as egress targets. It is read-only and never changes
// network state.
func (c *Client) handleNetInfo(stream io.ReadWriteCloser, req protocol.StreamRequest) {
	info := egress.Probe()
	response := protocol.StreamResponse{OK: true, NetInfo: &info}
	if req.Egress != nil {
		if err := req.Egress.Normalized().Validate(); err != nil {
			response.EgressError = err.Error()
		} else if _, err := egress.New(*req.Egress, egress.NewPools()); err != nil {
			response.EgressError = err.Error()
		}
	}
	_ = protocol.WriteJSONLine(stream, response)
}
