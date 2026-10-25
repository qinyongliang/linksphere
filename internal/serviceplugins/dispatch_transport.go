package serviceplugins

import (
	"os"
	"runtime"
)

// NewProviderLocalDispatcher binds the existing local admission checks to an
// authenticated dedicated transport lifetime. The full actor/session binding
// MUST come from trusted host authentication, never an incoming wire envelope.
// This creates no handler, listener, registry entry or default authorizer.
func NewProviderLocalDispatcher(transport *ProviderTransport, binding DispatchBinding, provider DispatchProvider, authorizer DispatchAuthorizer) (*LocalDispatcher, error) {
	return newProviderLocalDispatcher(func() Environment { return Environment{goos: runtime.GOOS, uid: os.Geteuid()} }, transport, binding, provider, authorizer)
}

func validProviderTransport(transport *ProviderTransport, providerID string) bool {
	return transport != nil && transport.verified && !nilInterface(transport.raw) &&
		transport.lifetime != nil && transport.cancel != nil && transport.done != nil && transport.providerID != "" &&
		transport.providerID == providerID && transport.lifetime.Err() == nil
}

func newProviderLocalDispatcher(environment func() Environment, transport *ProviderTransport, binding DispatchBinding, provider DispatchProvider, authorizer DispatchAuthorizer) (*LocalDispatcher, error) {
	if !validProviderTransport(transport, binding.ProviderID) {
		return nil, ErrDispatchDenied
	}
	dispatcher, err := newLocalDispatcherWithLifetime(transport.lifetime, environment, binding, provider, authorizer)
	if err != nil {
		return nil, err
	}
	// Trusted callbacks may have closed the transport while the child was built.
	if !validProviderTransport(transport, binding.ProviderID) {
		dispatcher.Close()
		return nil, ErrDispatchDenied
	}
	return dispatcher, nil
}
