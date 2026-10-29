package agent

import (
	"context"
	"log"
	"strconv"
	"strings"
	"time"
)

// Only refresh an existing RA default route. Static routes and absent networks
// belong to the OS; this workaround never creates routes or enables an interface.
func expiringRouterAdvertisement(output, iface string) bool {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "default" {
			continue
		}
		attributes := make(map[string]string)
		for i := 1; i+1 < len(fields); i++ {
			switch fields[i] {
			case "dev", "proto", "expires":
				attributes[fields[i]] = fields[i+1]
			}
		}
		if attributes["dev"] != iface || attributes["proto"] != "ra" {
			continue
		}
		value := attributes["expires"]
		if !strings.HasSuffix(value, "sec") {
			continue
		}
		seconds, err := strconv.ParseUint(strings.TrimSuffix(value, "sec"), 10, 32)
		if err == nil && seconds > 0 && seconds <= 120 {
			return true
		}
	}
	return false
}

func startRouterRefresh(ctx context.Context, iface string) func() {
	if iface == "" {
		return func() {}
	}
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runRouterRefresh(workerCtx, iface, 30*time.Second, time.Minute, readIPv6Routes, solicitIPv6Router)
	}()
	return func() { cancel(); <-done }
}

func runRouterRefresh(ctx context.Context, iface string, interval, cooldown time.Duration, routes func(context.Context) (string, error), solicit func(string) error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var lastAttempt time.Time
	var lastError string
	for {
		if ctx.Err() != nil {
			return
		}
		output, err := routes(ctx)
		if err == nil && ctx.Err() == nil && expiringRouterAdvertisement(output, iface) && (lastAttempt.IsZero() || time.Since(lastAttempt) >= cooldown) {
			lastAttempt = time.Now()
			err = solicit(iface)
			if err == nil {
				log.Printf("Android IPv6 router refresh: solicitation sent on %q", iface)
			}
		}
		if err != nil && ctx.Err() == nil {
			if message := err.Error(); message != lastError {
				log.Printf("Android IPv6 router refresh unavailable: %s", message)
				lastError = message
			}
		} else if err == nil {
			lastError = ""
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
