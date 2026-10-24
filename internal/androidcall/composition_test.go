package call

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestLocalCompositionDisabledHasNoEffects(t *testing.T) {
	config := LocalProviderConfig{
		AuthorizeNumber: func(string) bool { panic("policy called") },
		CommandRunner:   func(context.Context, string, ...string) (string, error) { panic("command called") },
		Media: MediaResolver{
			Ready:   func() bool { panic("readiness called") },
			Resolve: func(context.Context, string) (MediaFunc, error) { panic("resolve called") },
		},
	}
	p, err := newLocalProvider(func() (string, int) { panic("environment called") }, config)
	if p != nil || err != nil {
		t.Fatalf("disabled composition: provider=%v err=%v", p, err)
	}
}

func localCompositionFixture() (LocalProviderConfig, *int, *int) {
	commands, resolves := new(int), new(int)
	return LocalProviderConfig{
		Enabled: true, Identity: providerIdentity, PollInterval: time.Millisecond,
		AuthorizeNumber: func(string) bool { return true },
		CommandRunner: func(_ context.Context, name string, args ...string) (string, error) {
			*commands++
			if name == "dumpsys" {
				if args[0] == "telephony.registry" {
					return "mVoiceRegState=0(IN_SERVICE)", nil
				}
				return "state: ACTIVE", nil
			}
			return "", nil
		},
		Media: MediaResolver{
			Ready: func() bool { return true },
			Resolve: func(context.Context, string) (MediaFunc, error) {
				*resolves++
				return func(context.Context) error { return nil }, nil
			},
		},
	}, commands, resolves
}

func TestLocalCompositionRejectsMissingDependencies(t *testing.T) {
	for _, missing := range []string{"policy", "runner", "ready", "resolve", "negative-interval"} {
		t.Run(missing, func(t *testing.T) {
			config, commands, resolves := localCompositionFixture()
			switch missing {
			case "policy":
				config.AuthorizeNumber = nil
			case "runner":
				config.CommandRunner = nil
			case "ready":
				config.Media.Ready = nil
			case "resolve":
				config.Media.Resolve = nil
			case "negative-interval":
				config.PollInterval = -time.Nanosecond
			}
			p, err := newLocalProvider(func() (string, int) { panic("invalid config reached environment") }, config)
			if p != nil || err == nil || *commands != 0 || *resolves != 0 {
				t.Fatal("unsafe dependencies accepted")
			}
		})
	}
}

func TestLocalCompositionKeepsEnvironmentAndReadinessGates(t *testing.T) {
	for _, env := range []func() (string, int){nil, func() (string, int) { return "linux", 0 }, func() (string, int) { return "android", 2000 }} {
		config, commands, resolves := localCompositionFixture()
		if p, err := newLocalProvider(env, config); p != nil || err == nil || *commands != 0 || *resolves != 0 {
			t.Fatal("invalid environment accepted")
		}
	}
	config, commands, resolves := localCompositionFixture()
	config.Media.Ready = func() bool { return false }
	if p, err := newLocalProvider(func() (string, int) { return "android", 0 }, config); p != nil || err == nil || *commands != 0 || *resolves != 0 {
		t.Fatal("unready provider accepted")
	}
}

func TestLocalCompositionMockInvocationAndPolicy(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		config, commands, resolves := localCompositionFixture()
		policyCalls := 0
		config.AuthorizeNumber = func(string) bool { policyCalls++; return allowed }
		p, err := newLocalProvider(func() (string, int) { return "android", 0 }, config)
		if err != nil {
			t.Fatal(err)
		}
		config.CommandRunner = nil // The factory owns a copy, not the caller's config.
		if capabilities := p.ReadyCapabilities(); len(capabilities) != 1 || capabilities[0] != "call" {
			t.Fatal(capabilities)
		}
		raw, err := p.Invoke(context.Background(), "call", validProviderArgs)
		if policyCalls != 1 || *resolves != 1 {
			t.Fatal("unexpected policy/resolver calls")
		}
		if !allowed {
			if err == nil || *commands != 0 {
				t.Fatal("denied destination executed")
			}
			continue
		}
		var result map[string]any
		if err != nil || json.Unmarshal(raw, &result) != nil || result["state"] != "completed" || *commands != 4 {
			t.Fatalf("mock invocation: %s %v", raw, err)
		}
		if _, exposed := result["number"]; exposed {
			t.Fatal("number exposed")
		}
	}
}

func TestLocalCompositionRejectsInvalidIdentityWithoutReadiness(t *testing.T) {
	for _, field := range []string{"target", "provider", "session", "generation"} {
		t.Run(field, func(t *testing.T) {
			config, _, _ := localCompositionFixture()
			switch field {
			case "target":
				config.Identity.TargetID = ""
			case "provider":
				config.Identity.ProviderID = " provider"
			case "session":
				config.Identity.SessionID = "session\n"
			case "generation":
				config.Identity.Generation = 0
			}
			config.Media.Ready = func() bool { panic("invalid identity reached readiness") }
			if p, err := newLocalProvider(func() (string, int) { return "android", 0 }, config); p != nil || err == nil {
				t.Fatal("invalid identity accepted")
			}
		})
	}
}

func TestLocalCompositionRechecksReadinessAndCancellation(t *testing.T) {
	config, commands, resolves := localCompositionFixture()
	ready := true
	config.Media.Ready = func() bool { return ready }
	p, err := newLocalProvider(func() (string, int) { return "android", 0 }, config)
	if err != nil {
		t.Fatal(err)
	}
	ready = false
	if _, err := p.Invoke(context.Background(), "call", validProviderArgs); err == nil {
		t.Fatal("unready invocation accepted")
	}
	ready = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Invoke(ctx, "call", validProviderArgs); err == nil {
		t.Fatal("cancelled invocation accepted")
	}
	if *commands != 0 || *resolves != 0 {
		t.Fatal("rejected invocation had effects")
	}
}
