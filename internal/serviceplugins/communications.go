package serviceplugins

// CommunicationsProvider is implemented by built-in local device providers.
// It must not be populated from AgentHello or remote configuration. A capability
// is advertised only after its adapter is ready; this interface performs no I/O.
type CommunicationsProvider interface {
	ReadyCapabilities() []string
}

// LocalCommunications describes adapters in the current root service process.
// Construct it after local provider initialization, never in the central server
// on behalf of a remote device. It does not install handlers or grant operations.
func LocalCommunications(provider CommunicationsProvider) Plugin {
	p := &communicationsPlugin{}
	if nilInterface(provider) {
		return p
	}
	ready := map[string]bool{}
	for _, capability := range provider.ReadyCapabilities() {
		ready[capability] = true
	}
	p.descriptor = Descriptor{ID: "communications", Group: "communications"}
	for _, c := range []struct{ id, description, tool, event string }{
		{"call", "Control explicitly authorized calls", "communications_call", "call_changed"},
		{"audio", "Use an explicitly authorized audio session", "communications_audio", "audio_changed"},
		{"sms", "Send an explicitly authorized SMS", "communications_sms", "sms_changed"},
	} {
		if !ready[c.id] {
			continue
		}
		p.descriptor.Permissions = append(p.descriptor.Permissions, Permission{ID: c.id, Description: c.description})
		p.descriptor.Tools = append(p.descriptor.Tools, Tool{Name: c.tool, Permission: c.id})
		p.descriptor.Events = append(p.descriptor.Events, c.event)
	}
	return p
}

type communicationsPlugin struct{ descriptor Descriptor }

func (p *communicationsPlugin) Describe() Descriptor {
	if p.descriptor.ID == "" {
		return Descriptor{ID: "communications", Group: "communications"}
	}
	return copyDescriptor(p.descriptor)
}
func (p *communicationsPlugin) Available(e Environment) bool {
	return e.GOOS() == "android" && e.UID() == 0 && len(p.descriptor.Tools) > 0
}
