package agent

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/qinyongliang/linksphere/internal/desktop"
)

func TestDesktopDefaultConfiguration(t *testing.T) {
	tests := []struct {
		name, goos string
		cfg        Config
		env        map[string]string
		enabled    bool
		helper     string
		wantError  bool
	}{
		{name: "windows default", goos: "windows", enabled: true},
		{name: "mac default", goos: "darwin", enabled: true},
		{name: "linux default", goos: "linux"},
		{name: "android default", goos: "android"},
		{name: "explicit opt out", goos: "windows", cfg: Config{DesktopDisabled: true}},
		{name: "opt out wins explicit enable", goos: "darwin", cfg: Config{DesktopDisabled: true, Desktop: desktop.Config{Enabled: true}}},
		{name: "canonical enable wins", goos: "linux", env: map[string]string{"LINKSPHERE_DESKTOP_ENABLE": "true", "GOSSHD_DESKTOP_ENABLE": "false"}, enabled: true},
		{name: "legacy disable still wins", goos: "windows", env: map[string]string{"LINKSPHERE_DESKTOP_ENABLE": "true", "LINKSPHERE_DESKTOP_DISABLE": "false", "GOSSHD_DESKTOP_DISABLE": "true"}},
		{name: "canonical disable wins", goos: "windows", env: map[string]string{"LINKSPHERE_DESKTOP_DISABLE": "true", "GOSSHD_DESKTOP_DISABLE": "false"}},
		{name: "canonical helper wins", goos: "darwin", env: map[string]string{"LINKSPHERE_DESKTOP_HELPER": " /Applications/New.app/helper ", "GOSSHD_DESKTOP_HELPER": "/Applications/Old.app/helper"}, enabled: true, helper: "/Applications/New.app/helper"},
		{name: "invalid canonical enable rejected", goos: "windows", env: map[string]string{"LINKSPHERE_DESKTOP_ENABLE": "invalid", "GOSSHD_DESKTOP_ENABLE": "true"}, wantError: true},
		{name: "legacy environment off", goos: "windows", env: map[string]string{"GOSSHD_DESKTOP_ENABLE": "0"}},
		{name: "legacy environment on", goos: "darwin", env: map[string]string{"GOSSHD_DESKTOP_ENABLE": "1"}, enabled: true},
		{name: "environment opt out", goos: "darwin", env: map[string]string{"GOSSHD_DESKTOP_DISABLE": "true"}},
		{name: "opt out wins environment enable", goos: "windows", cfg: Config{DesktopDisabled: true}, env: map[string]string{"GOSSHD_DESKTOP_ENABLE": "1", "GOSSHD_DESKTOP_DISABLE": "0"}},
		{name: "empty environment uses default", goos: "windows", env: map[string]string{"GOSSHD_DESKTOP_ENABLE": ""}, enabled: true},
		{name: "helper environment", goos: "darwin", env: map[string]string{"GOSSHD_DESKTOP_HELPER": " /Applications/Example.app/Contents/MacOS/helper "}, enabled: true, helper: "/Applications/Example.app/Contents/MacOS/helper"},
		{name: "explicit helper preserved", goos: "darwin", cfg: Config{Desktop: desktop.Config{HelperPath: "/Applications/Explicit.app/Contents/MacOS/helper"}}, env: map[string]string{"GOSSHD_DESKTOP_HELPER": "/Applications/Other.app/Contents/MacOS/helper"}, enabled: true, helper: "/Applications/Explicit.app/Contents/MacOS/helper"},
		{name: "invalid environment enable", goos: "windows", env: map[string]string{"GOSSHD_DESKTOP_ENABLE": "invalid"}, wantError: true},
		{name: "invalid environment disable", goos: "darwin", env: map[string]string{"GOSSHD_DESKTOP_DISABLE": "invalid"}, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := desktopConfig(tt.cfg, tt.goos, func(name string) (string, bool) { value, ok := tt.env[name]; return value, ok })
			if (err != nil) != tt.wantError {
				t.Fatalf("configuration error: %v", err)
			}
			if err != nil {
				return
			}
			if cfg.Desktop.Enabled != tt.enabled || cfg.Desktop.HelperPath != tt.helper {
				t.Fatalf("unexpected desktop configuration: %+v", cfg.Desktop)
			}
		})
	}
}

func TestDesktopDefaultsAppliedByBothAgentConstructors(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "opt out"}[disabled], func(t *testing.T) {
			t.Setenv("LINKSPHERE_DESKTOP_ENABLE", "")
			t.Setenv("LINKSPHERE_DESKTOP_DISABLE", "")
			t.Setenv("LINKSPHERE_DESKTOP_HELPER", "")
			t.Setenv("GOSSHD_DESKTOP_ENABLE", "")
			t.Setenv("GOSSHD_DESKTOP_DISABLE", "")
			t.Setenv("GOSSHD_DESKTOP_HELPER", "")
			dir := t.TempDir()
			cfg := Config{Server: "https://bastion.example.test", IDFile: filepath.Join(dir, "agent-id.json"), Root: dir, DesktopDisabled: disabled}
			remote, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer remote.Close()
			embedded, err := NewEmbedded(cfg, "desktop-config-test")
			if err != nil {
				t.Fatal(err)
			}
			defer embedded.Close()
			want := DesktopEnabledByDefault(runtime.GOOS) && !disabled
			for _, client := range []*Client{remote, embedded} {
				if client.cfg.Desktop.Enabled != want {
					t.Fatalf("constructor desktop enabled=%v; want %v", client.cfg.Desktop.Enabled, want)
				}
			}
		})
	}
}
