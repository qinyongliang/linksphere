package main

import (
	"flag"
	"log"
	"os"
	"runtime"

	"github.com/qinyongliang/linksphere/internal/agent"
	"github.com/qinyongliang/linksphere/internal/branding"
)

var version = "dev"

func parseAgentFlags(flags *flag.FlagSet, args []string) (agent.Config, error) {
	branding.SetUsage(flags, "LinkSphere Agent")
	var cfg agent.Config
	cfg.Version = version
	flags.StringVar(&cfg.Server, "server", "", "public LinkSphere server URL (required)")
	flags.StringVar(&cfg.EnrollmentToken, "enrollment-token", "", "owner-scoped LinkSphere enrollment token")
	flags.StringVar(&cfg.IDFile, "id-file", "", "path to stable local agent id file")
	flags.StringVar(&cfg.Shell, "shell", "", "shell executable; defaults to parent process shell, SHELL, or login shell")
	flags.StringVar(&cfg.Root, "root", "", "working directory; defaults to the current user's home directory")
	flags.StringVar(&cfg.SSHHost, "ssh-host", "", "public SSH host shown in connection hints")
	flags.StringVar(&cfg.SSHPort, "ssh-port", "", "public SSH port shown in connection hints")
	flags.StringVar(&cfg.AndroidIPv6RouterRefreshInterface, "android-ipv6-router-refresh-interface", "", "opt-in rooted Android workaround: refresh expiring IPv6 router advertisements on this interface")
	flags.BoolVar(&cfg.Desktop.Enabled, "desktop-enable", agent.DesktopEnabledByDefault(runtime.GOOS), "enable desktop RPC (default on Windows/macOS); requires a logged-in user desktop; Android opt-in needs unlocked shell or paired accessibility")
	flags.StringVar(&cfg.Desktop.HelperPath, "desktop-helper", "", "absolute native helper path (macOS/Windows); leave empty for embedded Android backend")
	flags.StringVar(&cfg.Desktop.AndroidBackend, "android-backend", "auto", "Android backend: auto, shell (root/adb) or accessibility (companion app)")
	flags.StringVar(&cfg.Desktop.AndroidBridgeTokenFile, "android-bridge-token-file", "", "private local pairing token file for Android companion")
	flags.BoolVar(&cfg.DesktopDisabled, "desktop-disable", false, "disable desktop RPC, including its default Windows/macOS capability")
	if err := flags.Parse(args); err != nil {
		return cfg, err
	}
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "desktop-enable" && !cfg.Desktop.Enabled {
			cfg.DesktopDisabled = true
		}
	})
	return cfg, nil
}

func main() {
	cfg, err := parseAgentFlags(flag.CommandLine, os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}

	if err := runAgent(cfg); err != nil {
		log.Fatal(err)
	}
}
