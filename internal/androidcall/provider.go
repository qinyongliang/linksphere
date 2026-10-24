package call

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/qinyongliang/linksphere/internal/serviceplugins"
	"io"
	"os"
	"regexp"
	"runtime"
	"strings"
)

var ErrProviderUnavailable = errors.New("local call provider unavailable")
var mediaIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// MediaResolver is trusted local initialization, never a remote request. Ready
// must be an I/O-free readiness observation; Resolve maps an ID, never a caller path. It must enforce media access for the
// authenticated context and this provider target/session; resolution must not play
// media or enable listeners. Stub constructor success is not readiness.
type MediaResolver struct {
	Ready   func() bool
	Resolve func(context.Context, string) (MediaFunc, error)
}

// Provider supplies only synchronous call/start. It installs no handlers and
// does not authorize callers: use the trusted LocalDispatcher admission boundary.
type Provider struct {
	manager     *Manager
	media       MediaResolver
	identity    serviceplugins.DispatchProviderIdentity
	environment func() (string, int)
}

func NewProvider(identity serviceplugins.DispatchProviderIdentity, manager *Manager, media MediaResolver) (*Provider, error) {
	return newProvider(func() (string, int) { return runtime.GOOS, os.Geteuid() }, identity, manager, media)
}
func newProvider(environment func() (string, int), identity serviceplugins.DispatchProviderIdentity, manager *Manager, media MediaResolver) (*Provider, error) {
	if environment == nil || manager == nil || media.Ready == nil || media.Resolve == nil || identity.Generation == 0 {
		return nil, ErrProviderUnavailable
	}
	for _, s := range []string{identity.TargetID, identity.ProviderID, identity.SessionID} {
		if s == "" || len(s) > 256 || strings.TrimSpace(s) != s || strings.ContainsAny(s, "\x00\r\n") {
			return nil, ErrProviderUnavailable
		}
	}
	p := &Provider{manager: manager, media: media, identity: identity, environment: environment}
	if !p.ready() {
		return nil, ErrProviderUnavailable
	}
	return p, nil
}
func (p *Provider) ready() bool {
	if p == nil || p.environment == nil || p.manager == nil || p.media.Ready == nil || p.media.Resolve == nil {
		return false
	}
	platform, uid := p.environment()
	return platform == "android" && uid == 0 && !p.manager.Active() && p.media.Ready()
}
func (p *Provider) Identity() serviceplugins.DispatchProviderIdentity {
	if p == nil {
		return serviceplugins.DispatchProviderIdentity{}
	}
	return p.identity
}
func (p *Provider) ReadyCapabilities() []string {
	if !p.ready() {
		return nil
	}
	return []string{"call"}
}

// EventCapabilities does not imply idle command readiness or register a stream.
// A busy initialized root provider can still report its owned call observations.
func (p *Provider) EventCapabilities() []string {
	if p == nil || p.manager == nil || p.environment == nil {
		return nil
	}
	platform, uid := p.environment()
	if platform != "android" || uid != 0 {
		return nil
	}
	return []string{"call"}
}

type providerRequest struct {
	Action string `json:"action"`
	Request
}

func decodeProviderRequest(raw json.RawMessage) (providerRequest, error) {
	var request providerRequest
	if len(raw) == 0 || len(raw) > 64*1024 {
		return request, ErrProviderUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return request, ErrProviderUnavailable
	}
	fields := map[string]json.RawMessage{}
	allowed := map[string]bool{"action": true, "number": true, "audio_id": true, "answer_timeout_seconds": true, "max_call_seconds": true, "hangup_after_playback": true}
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || !allowed[name] {
			return request, ErrProviderUnavailable
		}
		if _, exists := fields[name]; exists {
			return request, ErrProviderUnavailable
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return request, ErrProviderUnavailable
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return request, ErrProviderUnavailable
		}
		fields[name] = value
	}
	if _, err = decoder.Token(); err != nil {
		return request, ErrProviderUnavailable
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return request, ErrProviderUnavailable
	}
	normalized, err := json.Marshal(fields)
	if err != nil || json.Unmarshal(normalized, &request) != nil {
		return request, ErrProviderUnavailable
	}
	if request.Action != "start" || !mediaIDPattern.MatchString(request.AudioID) || strings.Contains(request.AudioID, "..") {
		return request, ErrProviderUnavailable
	}
	if !ValidNumber(request.Number) || request.HangupAfterPlayback != nil && !*request.HangupAfterPlayback {
		return request, ErrProviderUnavailable
	}
	if request.AnswerTimeoutSecs != 0 && (request.AnswerTimeoutSecs < 5 || request.AnswerTimeoutSecs > 180) || request.MaxCallSecs != 0 && (request.MaxCallSecs < 5 || request.MaxCallSecs > 1800) {
		return request, ErrProviderUnavailable
	}
	return request, nil
}
func (p *Provider) Invoke(ctx context.Context, operation string, args json.RawMessage) (json.RawMessage, error) {
	if ctx == nil || ctx.Err() != nil || operation != "call" || !p.ready() {
		return nil, ErrProviderUnavailable
	}
	request, err := decodeProviderRequest(args)
	if err != nil {
		return nil, err
	}
	media, err := p.media.Resolve(ctx, request.AudioID)
	if err != nil || media == nil || ctx.Err() != nil || !p.ready() {
		return nil, ErrProviderUnavailable
	}
	var observer func(CallObservation)
	if publish := serviceplugins.CallEventPublisher(ctx); publish != nil {
		observer = func(event CallObservation) {
			_ = publish(serviceplugins.CallEvent{CallID: event.ID, State: string(event.State), UpdatedAt: event.UpdatedAt.UnixMilli()})
		}
	}
	final, err := p.manager.RunMediaContextObserved(ctx, request.Request, request.AudioID, media, observer)
	if err != nil {
		return nil, ErrProviderUnavailable
	}
	return json.Marshal(map[string]any{"id": final.ID, "state": final.State, "audio_id": final.AudioID, "created_at": final.CreatedAt, "updated_at": final.UpdatedAt, "answered_at": final.AnsweredAt, "ended_at": final.EndedAt})
}

var _ serviceplugins.DispatchProvider = (*Provider)(nil)
