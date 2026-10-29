package agent

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRouterRefreshOnlyForExpiringExistingRADefault(t *testing.T) {
	for _, test := range []struct {
		name, route string
		want        bool
	}{
		{"near expiry", "default via 2001:db8::1 dev wlan-test table wlan-test proto ra expires 120sec", true},
		{"last second", "default dev wlan-test proto ra expires 1sec", true},
		{"healthy lifetime", "default dev wlan-test proto ra expires 121sec", false},
		{"static", "default dev wlan-test proto static expires 10sec", false},
		{"another interface", "default dev other-test proto ra expires 10sec", false},
		{"expired", "default dev wlan-test proto ra expires 0sec", false},
		{"permanent", "default dev wlan-test proto ra", false},
		{"prefix route", "2001:db8::/64 dev wlan-test proto ra expires 10sec", false},
		{"malformed lifetime", "default dev wlan-test proto ra expires -1sec", false},
		{"overflow", "default dev wlan-test proto ra expires 18446744073709551616sec", false},
		{"absent", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := expiringRouterAdvertisement(test.route, "wlan-test"); got != test.want {
				t.Fatalf("refresh=%v, want %v", got, test.want)
			}
		})
	}
}

func TestRouterRefreshThrottlesFailuresAndStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reads := make(chan struct{}, 16)
	var sends atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		runRouterRefresh(ctx, "wlan-test", time.Millisecond, time.Minute, func(context.Context) (string, error) {
			select {
			case reads <- struct{}{}:
			default:
			}
			return "default dev wlan-test proto ra expires 30sec", nil
		}, func(name string) error {
			if name != "wlan-test" {
				t.Errorf("wrong interface %q", name)
			}
			sends.Add(1)
			return errors.New("raw socket unavailable")
		})
	}()
	for i := 0; i < 5; i++ {
		select {
		case <-reads:
		case <-time.After(time.Second):
			t.Fatal("refresh worker stalled")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("refresh worker did not stop")
	}
	if sends.Load() != 1 {
		t.Fatalf("failed solicitation retried without cooldown: %d", sends.Load())
	}
}

func TestRouterRefreshDoesNotSendOnRouteReadFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var sends int
	runRouterRefresh(ctx, "wlan-test", time.Second, time.Minute, func(context.Context) (string, error) {
		cancel()
		return "", errors.New("ip command unavailable")
	}, func(string) error { sends++; return nil })
	if sends != 0 {
		t.Fatal("route lookup failure triggered network traffic")
	}
}

func TestRouterRefreshOptInRequiresSupportedPlatform(t *testing.T) {
	if err := validateRouterRefresh(""); err != nil {
		t.Fatalf("default configuration changed: %v", err)
	}
	if runtime.GOOS != "android" {
		if _, err := New(Config{AndroidIPv6RouterRefreshInterface: "wlan-test"}); err == nil || !strings.Contains(err.Error(), "rooted Android") {
			t.Fatalf("unsupported opt-in silently accepted: %v", err)
		}
	}
}
