package call

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type testPlayer struct{}

func (testPlayer) Play(context.Context, string) error { return nil }

func TestManagerRejectsNumberBeforeDialing(t *testing.T) {
	manager := NewManager(Config{
		Player: testPlayer{},
		AuthorizeNumber: func(number string) bool {
			return strings.HasPrefix(number, "138")
		},
	})

	_, err := manager.StartMedia(Request{Number: "10010"}, "test", func(context.Context) error {
		t.Fatal("media must not start for a blocked number")
		return nil
	})
	if err == nil || err.Error() != "number is not allowed by phone policy" {
		t.Fatalf("unexpected error: %v", err)
	}
	if manager.Active() {
		t.Fatal("blocked call must not become active")
	}
}

func TestPruneCacheRemovesOldestCall(t *testing.T) {
	manager := NewManager(Config{})
	base := time.Now().UTC()
	for index := 0; index < maxCachedCalls; index++ {
		id := string(rune('a' + index))
		manager.calls[id] = &Call{ID: id, CreatedAt: base.Add(time.Duration(index) * time.Second)}
	}
	manager.pruneCache()
	if len(manager.calls) != maxCachedCalls-1 {
		t.Fatalf("cache length=%d, want %d", len(manager.calls), maxCachedCalls-1)
	}
	if _, found := manager.calls[string(rune('a'))]; found {
		t.Fatal("oldest call was not removed")
	}
}

func TestVoiceServicePatterns(t *testing.T) {
	tests := []struct {
		name  string
		input string
		known bool
		ready bool
	}{
		{name: "single SIM ready", input: "mVoiceRegState=0(IN_SERVICE)", known: true, ready: true},
		{name: "dual SIM one ready", input: "mVoiceRegState=1(OUT_OF_SERVICE)\nmVoiceRegState=0(IN_SERVICE)", known: true, ready: true},
		{name: "out of service", input: "mVoiceRegState=1(OUT_OF_SERVICE)", known: true, ready: false},
		{name: "unsupported output", input: "permission denied", known: false, ready: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := voiceStatePattern.MatchString(test.input); got != test.known {
				t.Fatalf("known=%v, want %v", got, test.known)
			}
			if got := voiceReadyPattern.MatchString(test.input); got != test.ready {
				t.Fatalf("ready=%v, want %v", got, test.ready)
			}
		})
	}
}

func TestRunMediaHonorsDeadlineWhenDriverBlocks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	blocked := make(chan struct{})
	err := runMedia(ctx, func(context.Context) error {
		<-blocked
		return nil
	})
	close(blocked)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runMedia error=%v, want deadline exceeded", err)
	}
}

func TestLiveTelecomState(t *testing.T) {
	tests := []struct {
		name  string
		input string
		state string
		found bool
	}{
		{
			name:  "Mokee active call list",
			input: "CallsManager:\n  mCalls:\n    [TC@5, ACTIVE, com.android.phone, tel:10010, A]\nAnalytics:\n  SET_ACTIVE",
			state: "ACTIVE", found: true,
		},
		{
			name:  "Mokee dialing call list",
			input: "CallsManager:\n  mCalls:\n    [TC@6, DIALING, com.android.phone, tel:10010, A]\nAnalytics:",
			state: "DIALING", found: true,
		},
		{
			name:  "state field",
			input: "CallsManager:\n  state: ACTIVE\nAnalytics:",
			state: "ACTIVE", found: true,
		},
		{
			name:  "historical active event ignored",
			input: "CallsManager:\n  mCalls:\nAnalytics:\n  SET_ACTIVE\n  state: ACTIVE",
			found: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state, found := liveTelecomState(test.input)
			if state != test.state || found != test.found {
				t.Fatalf("state=%q found=%v, want state=%q found=%v", state, found, test.state, test.found)
			}
		})
	}
}
