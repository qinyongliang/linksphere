package server

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/qinyongliang/linksphere/internal/protocol"
	protocoltypes "github.com/qinyongliang/linksphere/internal/protocol/types"
	"github.com/qinyongliang/linksphere/internal/store"
)

type apiTargetNetInfo struct {
	Supported bool                   `json:"supported"`
	Reason    string                 `json:"reason,omitempty"`
	NetInfo   *protocoltypes.NetInfo `json:"net_info,omitempty"`
	// EgressError reports a rejected egress probe selection on the Agent.
	EgressError string `json:"egress_error,omitempty"`
}

// targetNetInfo asks the Agent behind a target for its egress interfaces. The
// probe is read-only and never changes network state; it exists so an operator or
// assistant can pick an interface, address family or pool before saving a rule.
func (a *App) targetNetInfo(ctx context.Context, target store.SSHTarget, egress *protocoltypes.EgressSpec) (apiTargetNetInfo, error) {
	agentID, err := a.forwardAgentID(ctx, target.ID)
	if err != nil {
		return apiTargetNetInfo{}, err
	}
	if agentID == "" {
		return apiTargetNetInfo{Reason: "egress interface discovery requires an Agent endpoint"}, nil
	}
	session, err := a.registry.Get(agentID)
	if err != nil {
		return apiTargetNetInfo{}, err
	}
	stream, err := session.OpenStream()
	if err != nil {
		return apiTargetNetInfo{}, err
	}
	defer stream.Close()
	stop := context.AfterFunc(ctx, func() { _ = stream.Close() })
	defer stop()
	_ = stream.SetDeadline(time.Now().Add(15 * time.Second))
	if err := protocol.WriteJSONLine(stream, protocol.StreamRequest{Type: protocol.StreamNetInfo, Egress: egress}); err != nil {
		return apiTargetNetInfo{}, err
	}
	response, err := protocol.ReadJSONLine[protocol.StreamResponse](bufio.NewReader(io.LimitReader(stream, 4<<20)))
	if err != nil {
		return apiTargetNetInfo{}, err
	}
	if !response.OK {
		if response.Error == "" {
			return apiTargetNetInfo{}, errors.New("network information unavailable")
		}
		return apiTargetNetInfo{}, errors.New(response.Error)
	}
	if response.NetInfo == nil {
		return apiTargetNetInfo{}, errors.New("network information unavailable")
	}
	return apiTargetNetInfo{Supported: true, NetInfo: response.NetInfo, EgressError: response.EgressError}, nil
}

// handleTargetNetInfo reports the egress capability of one machine. Machines
// reachable only over SSH answer supported=false: interface and pool selection is
// an Agent capability.
func (a *App) handleTargetNetInfo(w http.ResponseWriter, r *http.Request, user store.User) {
	target, err := a.targetForUser(r.Context(), r.PathValue("id"), user)
	if err != nil {
		writeOwnerError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	out, err := a.targetNetInfo(ctx, target, nil)
	if err != nil {
		if errors.Is(err, ErrAgentOffline) || strings.Contains(err.Error(), "offline") {
			writeError(w, http.StatusBadGateway, "Agent offline: "+err.Error())
			return
		}
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}
