package call

import (
	"context"
	"os"
	"runtime"
	"time"

	"github.com/qinyongliang/linksphere/internal/serviceplugins"
)

// LocalProviderConfig is supplied by trusted local initialization, never a request.
// Enabling this component does not register a listener or prove a remote identity.
type LocalProviderConfig struct {
	Enabled         bool
	Identity        serviceplugins.DispatchProviderIdentity
	AuthorizeNumber func(string) bool
	CommandRunner   func(context.Context, string, ...string) (string, error)
	Media           MediaResolver
	PollInterval    time.Duration
}

// NewLocalProvider is disabled by default. Enabled providers require explicit
// dependencies so this entry point cannot fall back to unrestricted OS commands.
// The caller must still use a trusted LocalDispatcher for invocation admission.
func NewLocalProvider(config LocalProviderConfig) (*Provider, error) {
	return newLocalProvider(func() (string, int) { return runtime.GOOS, os.Geteuid() }, config)
}

func newLocalProvider(environment func() (string, int), config LocalProviderConfig) (*Provider, error) {
	if !config.Enabled {
		return nil, nil
	}
	if environment == nil || config.PollInterval < 0 || config.AuthorizeNumber == nil || config.CommandRunner == nil ||
		config.Media.Ready == nil || config.Media.Resolve == nil {
		return nil, ErrProviderUnavailable
	}
	manager := NewManager(Config{
		AuthorizeNumber: config.AuthorizeNumber,
		CommandRunner:   config.CommandRunner,
		PollInterval:    config.PollInterval,
	})
	return newProvider(environment, config.Identity, manager, config.Media)
}
