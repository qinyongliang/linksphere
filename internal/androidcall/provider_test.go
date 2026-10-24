package call

import (
	"context"
	"encoding/json"
	"github.com/qinyongliang/linksphere/internal/serviceplugins"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var providerIdentity = serviceplugins.DispatchProviderIdentity{TargetID: "target", ProviderID: "provider", SessionID: "session", Generation: 1}
var validProviderArgs = json.RawMessage(`{"action":"start","number":"12345","audio_id":"audio-1"}`)

func providerFixture(t *testing.T) (*Provider, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	commands, resolves := new(atomic.Int32), new(atomic.Int32)
	m := NewManager(Config{PollInterval: time.Millisecond, CommandRunner: func(c context.Context, n string, a ...string) (string, error) {
		commands.Add(1)
		if n == "dumpsys" {
			if a[0] == "telephony.registry" {
				return "mVoiceRegState=0(IN_SERVICE)", nil
			}
			return "state: ACTIVE", nil
		}
		return "", nil
	}})
	p, e := newProvider(func() (string, int) { return "android", 0 }, providerIdentity, m, MediaResolver{Ready: func() bool { return true }, Resolve: func(context.Context, string) (MediaFunc, error) {
		resolves.Add(1)
		return func(context.Context) error { return nil }, nil
	}})
	if e != nil {
		t.Fatal(e)
	}
	return p, commands, resolves
}
func TestProviderAdmissionAndCapabilities(t *testing.T) {
	p, _, _ := providerFixture(t)
	if got := p.ReadyCapabilities(); len(got) != 1 || got[0] != "call" {
		t.Fatal(got)
	}
	id := p.Identity()
	id.TargetID = "mutated"
	if p.Identity().TargetID != "target" {
		t.Fatal("identity mutable")
	}
	for _, env := range []func() (string, int){func() (string, int) { return "linux", 0 }, func() (string, int) { return "android", 2000 }} {
		if _, e := newProvider(env, providerIdentity, p.manager, p.media); e == nil {
			t.Fatal("unsupported env accepted")
		}
	}
	if _, e := newProvider(p.environment, providerIdentity, p.manager, MediaResolver{}); e == nil {
		t.Fatal("nil resolver accepted")
	}
	bad := providerIdentity
	bad.SessionID = ""
	if _, e := newProvider(p.environment, bad, p.manager, p.media); e == nil {
		t.Fatal("invalid identity accepted")
	}
}
func TestProviderStrictArgumentsHaveNoCommandsOrResolution(t *testing.T) {
	p, commands, resolves := providerFixture(t)
	cases := []string{`null`, `[]`, `{}`, `{"action":"start","number":"12345","audio_id":"a","extra":1}`, `{"action":"start","number":"12345","audio_id":"a","number":"999"}`, string(validProviderArgs) + ` true`, `{"action":"start","number":"12345","audio_id":"../a"}`, `{"action":"start","number":"12345","audio_id":"a","hangup_after_playback":false}`, `{"action":"start","number":"12345","audio_id":"a","answer_timeout_seconds":-1}`, `{"action":"start","number":12345,"audio_id":"a"}`, strings.Repeat(" ", 65537)}
	for _, raw := range cases {
		if _, e := p.Invoke(context.Background(), "call", json.RawMessage(raw)); e == nil {
			t.Fatal("invalid accepted", raw)
		}
	}
	for _, op := range []string{"audio", "sms", "call_changed"} {
		if _, e := p.Invoke(context.Background(), op, validProviderArgs); e == nil {
			t.Fatal(op)
		}
	}
	if commands.Load() != 0 || resolves.Load() != 0 {
		t.Fatal("invalid request had effects")
	}
}
func TestProviderSuccessReturnsOnlyWhitelistedCurrentResult(t *testing.T) {
	p, commands, resolves := providerFixture(t)
	raw, e := p.Invoke(context.Background(), "call", validProviderArgs)
	if e != nil {
		t.Fatal(e)
	}
	var result map[string]any
	if json.Unmarshal(raw, &result) != nil || result["state"] != "completed" || result["audio_id"] != "audio-1" {
		t.Fatal(string(raw))
	}
	for _, key := range []string{"number", "error", "TargetID", "token"} {
		if _, ok := result[key]; ok {
			t.Fatal("unexpected result field", key)
		}
	}
	if commands.Load() != 4 || resolves.Load() != 1 {
		t.Fatal("fake flow mismatch")
	}
}
func TestProviderRechecksCancellationAndEnvironmentAfterResolve(t *testing.T) {
	for _, mode := range []string{"cancel", "environment", "nil"} {
		t.Run(mode, func(t *testing.T) {
			p, commands, _ := providerFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			platform := "android"
			p.environment = func() (string, int) { return platform, 0 }
			p.media.Resolve = func(context.Context, string) (MediaFunc, error) {
				if mode == "cancel" {
					cancel()
				}
				if mode == "environment" {
					platform = "linux"
				}
				if mode == "nil" {
					return nil, nil
				}
				return func(context.Context) error { return nil }, nil
			}
			if _, e := p.Invoke(ctx, "call", validProviderArgs); e == nil {
				t.Fatal("late invalidation accepted")
			}
			if commands.Load() != 0 {
				t.Fatal("late invalidation reached device commands")
			}
		})
	}
}
func TestProviderBusyAndQuarantineFailClosed(t *testing.T) {
	p, commands, _ := providerFixture(t)
	p.manager.mu.Lock()
	p.manager.active = "quarantined"
	p.manager.blocked = true
	p.manager.mu.Unlock()
	if len(p.ReadyCapabilities()) != 0 {
		t.Fatal("advertised busy provider")
	}
	if _, e := p.Invoke(context.Background(), "call", validProviderArgs); e == nil {
		t.Fatal("busy accepted")
	}
	if commands.Load() != 0 {
		t.Fatal("busy commands")
	}
}
func TestProviderConcurrentStartAndCancellation(t *testing.T) {
	p, _, _ := providerFixture(t)
	entered := make(chan struct{})
	p.media.Resolve = func(context.Context, string) (MediaFunc, error) {
		return func(c context.Context) error { close(entered); <-c.Done(); return c.Err() }, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, e := p.Invoke(ctx, "call", validProviderArgs); done <- e }()
	waitSignal(t, entered)
	if _, e := p.Invoke(context.Background(), "call", validProviderArgs); e == nil {
		t.Fatal("concurrent call admitted")
	}
	cancel()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("cancel reported success")
		}
	case <-time.After(time.Second):
		t.Fatal("not canceled")
	}
	if p.manager.Active() {
		t.Fatal("slot retained after confirmed fake cleanup")
	}
}
func TestProviderExplicitNullIsNotAnOmittedDefault(t *testing.T) {
	for _, field := range []string{"action", "number", "audio_id", "answer_timeout_seconds", "max_call_seconds", "hangup_after_playback"} {
		t.Run(field, func(t *testing.T) {
			p, commands, resolves := providerFixture(t)
			fields := map[string]any{"action": "start", "number": "12345", "audio_id": "audio-1"}
			fields[field] = nil
			raw, _ := json.Marshal(fields)
			if _, e := p.Invoke(context.Background(), "call", raw); e == nil {
				t.Fatal("explicit null accepted")
			}
			if commands.Load() != 0 || resolves.Load() != 0 {
				t.Fatal("null had effects")
			}
		})
	}
}
