package serviceplugins

import (
	"encoding/json"
	"testing"
)

type identityObservationProvider struct {
	*diagProvider
	observations int
}

func (p *identityObservationProvider) Identity() DispatchProviderIdentity {
	p.observations++
	return p.diagProvider.Identity()
}

func TestLocalDispatcherRejectsMalformedIdentityEncoding(t *testing.T) {
	for _, field := range []string{"actor", "target", "provider", "session"} {
		t.Run(field, func(t *testing.T) {
			binding := diagBinding()
			malformed := string([]byte{'i', 'd', 0xff})
			switch field {
			case "actor":
				binding.ActorID = malformed
			case "target":
				binding.TargetID = malformed
			case "provider":
				binding.ProviderID = malformed
			case "session":
				binding.SessionID = malformed
			}
			provider := newDiagProvider("call")
			provider.identity = diagIdentityFor(binding)
			observedProvider := &identityObservationProvider{diagProvider: provider}
			observations := 0
			dispatcher, err := newLocalDispatcher(func() Environment { observations++; return Environment{goos: "android", uid: 0} }, binding, observedProvider, newDiagAuthorizer(binding))
			if dispatcher != nil {
				dispatcher.Close()
			}
			if err == nil || dispatcher != nil || observations != 0 || observedProvider.observations != 0 {
				t.Fatal("malformed identity reached local enrollment callbacks")
			}
		})
	}
}

func TestLocalDispatcherPreservesValidUnicodeIdentity(t *testing.T) {
	binding := DispatchBinding{ActorID: "用户🌾", TargetID: "设备一", ProviderID: "本地服务", SessionID: "会话一", Generation: 1}
	provider := newDiagProvider("call")
	provider.identity = diagIdentityFor(binding)
	dispatcher := newDiagDispatcher(t, binding, provider, newDiagAuthorizer(binding))
	defer dispatcher.Close()
	result, err := dispatcher.Invoke(diagContext(binding.ActorID), binding, "call", json.RawMessage(`{}`))
	if err != nil || string(result) != `{"ok":true}` {
		t.Fatalf("valid Unicode was rejected: %s %v", result, err)
	}
}
