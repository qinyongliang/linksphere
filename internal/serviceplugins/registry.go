// Package serviceplugins provides code-registered capability metadata.
// Metadata availability is not an execution authorization decision.
package serviceplugins

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"runtime"
	"sort"
)

// Environment is a local process observation, never decoded from an AgentHello.
type Environment struct {
	goos string
	uid  int
}

func (e Environment) GOOS() string { return e.goos }
func (e Environment) UID() int     { return e.uid }

type Permission struct {
	ID          string
	Description string
}

type Setting struct {
	Key string
	// SchemaJSON is syntax-checked object metadata. The receiving adapter must
	// compile it against its supported JSON Schema dialect before exposing it.
	SchemaJSON json.RawMessage
}

type Tool struct {
	Name       string
	Permission string
}

type Descriptor struct {
	ID          string
	Group       string
	Permissions []Permission
	Settings    []Setting
	Tools       []Tool
	// Events are namespaced by this descriptor ID, never routed by name alone.
	Events []string
}

// Plugin is supplied by trusted built-in code, not a remotely loaded module.
type Plugin interface {
	Describe() Descriptor
	Available(Environment) bool
}

// Registrar adapters may contribute settings, permission UI, MCP schemas and
// event routing from one descriptor. Execution must separately authorize actor,
// target, provider and operation on every call.
type Registrar interface {
	RegisterPlugin(Descriptor) error
}

type Registry struct {
	descriptors []Descriptor
}

var identifier = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// New observes this service's OS and UID. A caller cannot enable root plugins
// by submitting a platform or privilege flag over the network.
func New(plugins ...Plugin) (*Registry, error) {
	return newForEnvironment(Environment{goos: runtime.GOOS, uid: os.Geteuid()}, plugins...)
}

func newForEnvironment(env Environment, plugins ...Plugin) (*Registry, error) {
	r := &Registry{}
	ids, groups, tools, permissions, settings := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, plugin := range plugins {
		if nilInterface(plugin) {
			return nil, fmt.Errorf("nil service plugin")
		}
		d := copyDescriptor(plugin.Describe())
		if !identifier.MatchString(d.ID) || !identifier.MatchString(d.Group) || ids[d.ID] || groups[d.Group] {
			return nil, fmt.Errorf("invalid or duplicate plugin identity")
		}
		ids[d.ID], groups[d.Group] = true, true
		localPermissions := map[string]bool{}
		for _, p := range d.Permissions {
			key := d.ID + "." + p.ID
			if !identifier.MatchString(p.ID) || p.Description == "" || permissions[key] {
				return nil, fmt.Errorf("invalid or duplicate plugin permission")
			}
			permissions[key], localPermissions[p.ID] = true, true
		}
		for _, s := range d.Settings {
			var schema map[string]json.RawMessage
			key := d.ID + "." + s.Key
			if !identifier.MatchString(s.Key) || settings[key] || json.Unmarshal(s.SchemaJSON, &schema) != nil || schema == nil {
				return nil, fmt.Errorf("invalid or duplicate plugin setting")
			}
			settings[key] = true
		}
		for _, t := range d.Tools {
			if !identifier.MatchString(t.Name) || tools[t.Name] || !localPermissions[t.Permission] {
				return nil, fmt.Errorf("invalid tool or missing explicit permission")
			}
			tools[t.Name] = true
		}
		events := map[string]bool{}
		for _, event := range d.Events {
			if !identifier.MatchString(event) || events[event] {
				return nil, fmt.Errorf("invalid or duplicate plugin event")
			}
			events[event] = true
		}
		// Validate every descriptor, including unavailable plugins, so a future
		// runtime change cannot reveal a hidden naming collision.
		if plugin.Available(env) {
			r.descriptors = append(r.descriptors, d)
		}
	}
	sort.Slice(r.descriptors, func(i, j int) bool { return r.descriptors[i].ID < r.descriptors[j].ID })
	return r, nil
}

// Descriptors returns an owned snapshot.
func (r *Registry) Descriptors() []Descriptor {
	if r == nil {
		return nil
	}
	out := make([]Descriptor, len(r.descriptors))
	for i, d := range r.descriptors {
		out[i] = copyDescriptor(d)
	}
	return out
}

// Register contributes metadata only for exact granted groups. "*" is not a
// group, and this method cannot grant access to a device or execute an action.
// The filter applies to the whole descriptor, not only its MCP tools. Do not
// reuse token-specific grants when constructing global settings/event handlers.
// Registration is not transactional: on error, discard the partially built sink
// before exposing it. A failed registration is not a successful server startup.
func (r *Registry) Register(sink Registrar, granted map[string]bool) error {
	if r == nil || nilInterface(sink) {
		return fmt.Errorf("plugin registrar required")
	}
	for _, d := range r.descriptors {
		if granted[d.Group] {
			if err := sink.RegisterPlugin(copyDescriptor(d)); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyDescriptor(d Descriptor) Descriptor {
	d.Permissions = append([]Permission(nil), d.Permissions...)
	d.Tools = append([]Tool(nil), d.Tools...)
	d.Events = append([]string(nil), d.Events...)
	settings := make([]Setting, len(d.Settings))
	for i, setting := range d.Settings {
		settings[i] = Setting{Key: setting.Key, SchemaJSON: append(json.RawMessage(nil), setting.SchemaJSON...)}
	}
	d.Settings = settings
	return d
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}
