package call

import (
	"context"
	"github.com/qinyongliang/linksphere/internal/serviceplugins"
	"testing"
)

func TestProviderRejectsMalformedIdentityBeforeReadiness(t *testing.T) {
	for _, field := range []string{"target", "provider", "session"} {
		t.Run(field, func(t *testing.T) {
			identity := providerIdentity
			malformed := string([]byte{'i', 'd', 0xff})
			switch field {
			case "target":
				identity.TargetID = malformed
			case "provider":
				identity.ProviderID = malformed
			case "session":
				identity.SessionID = malformed
			}
			environment, readiness, commands, resolves := 0, 0, 0, 0
			manager := NewManager(Config{CommandRunner: func(context.Context, string, ...string) (string, error) { commands++; return "", nil }})
			media := MediaResolver{Ready: func() bool { readiness++; return true }, Resolve: func(context.Context, string) (MediaFunc, error) { resolves++; return nil, nil }}
			provider, err := newProvider(func() (string, int) { environment++; return "android", 0 }, identity, manager, media)
			if err == nil || provider != nil || environment != 0 || readiness != 0 || commands != 0 || resolves != 0 {
				t.Fatal("malformed provider identity reached readiness or command callbacks")
			}
		})
	}
}

func TestProviderAcceptsWellFormedUnicodeIdentity(t *testing.T) {
	identity := serviceplugins.DispatchProviderIdentity{TargetID: "设备🌾", ProviderID: "服务\uFFFD", SessionID: "会话一", Generation: 1}
	commands, resolves := 0, 0
	manager := NewManager(Config{CommandRunner: func(context.Context, string, ...string) (string, error) { commands++; return "", nil }})
	media := MediaResolver{Ready: func() bool { return true }, Resolve: func(context.Context, string) (MediaFunc, error) { resolves++; return nil, nil }}
	provider, err := newProvider(func() (string, int) { return "android", 0 }, identity, manager, media)
	if err != nil || provider == nil || provider.Identity() != identity {
		t.Fatalf("valid Unicode identity rejected: %v", err)
	}
	if commands != 0 || resolves != 0 {
		t.Fatal("construction performed an operation")
	}
}
