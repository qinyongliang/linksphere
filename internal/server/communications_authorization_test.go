package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/qinyongliang/linksphere/internal/serviceplugins"
	"github.com/qinyongliang/linksphere/internal/store"
)

type communicationsAuthFixture struct {
	token   store.MCPToken
	user    store.User
	target  store.SSHTarget
	member  store.OrganizationMember
	grant   store.CommunicationsAccess
	visible bool
	fail    string
	reads   int
	onGrant func()
}

func (f *communicationsAuthFixture) fault(s string) error {
	if f.fail == s {
		return errors.New("fixture failure")
	}
	return nil
}
func (f *communicationsAuthFixture) GetMCPTokenByHash(_ context.Context, hash []byte) (store.MCPToken, error) {
	if len(hash) != 32 || hash[0] != 1 {
		return store.MCPToken{}, errors.New("wrong digest query")
	}
	f.reads++
	return f.token, f.fault("token")
}
func (f *communicationsAuthFixture) GetUser(_ context.Context, user string) (store.User, error) {
	if user != "actor" {
		return store.User{}, errors.New("wrong user query")
	}
	return f.user, f.fault("user")
}
func (f *communicationsAuthFixture) GetSSHTarget(_ context.Context, target string) (store.SSHTarget, error) {
	if target != "target" {
		return store.SSHTarget{}, errors.New("wrong target query")
	}
	return f.target, f.fault("target")
}
func (f *communicationsAuthFixture) GetOrganizationMember(_ context.Context, org, user string) (store.OrganizationMember, error) {
	if org != "org" || user != "actor" {
		return store.OrganizationMember{}, errors.New("wrong membership query")
	}
	return f.member, f.fault("member")
}
func (f *communicationsAuthFixture) UserCanAccessTarget(_ context.Context, user, target string) (bool, error) {
	if user != "actor" || target != "target" {
		return false, errors.New("wrong visibility query")
	}
	return f.visible, f.fault("visibility")
}
func (f *communicationsAuthFixture) GetCommunicationsAccess(_ context.Context, user, target, op string) (store.CommunicationsAccess, error) {
	if user != "actor" || target != "target" || (op != "call" && op != "audio" && op != "sms") {
		return store.CommunicationsAccess{}, errors.New("wrong consent query")
	}
	if f.onGrant != nil {
		f.onGrant()
	}
	return f.grant, f.fault("grant")
}

func TestCommunicationsAuthorizationGate(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for _, name := range []string{"allow", "allow audio", "allow sms", "revoke token", "revoke scope", "disable actor", "owner visible", "member org mismatch", "cancel during grant", "replace provider", "missing principal", "actor mismatch", "empty hash", "token owner", "token identity", "wildcard", "scope case", "scope removed", "disabled", "user mismatch", "target mismatch", "not agent", "missing agent", "personal target", "membership", "member identity", "visibility", "grant missing", "grant actor", "grant target", "grant operation", "expiry boundary", "zero clock", "generation replaced", "agent remapped", "agent replaced during lookup", "expiry during binding", "binding error", "canceled", "unknown operation", "admin no consent", "admin wildcard", "admin explicit", "token error", "user error", "target error", "member error", "visibility error", "grant error"} {
		t.Run(name, func(t *testing.T) {
			f := &communicationsAuthFixture{token: store.MCPToken{ID: "token", UserID: "actor", ToolGroups: []string{"communications"}}, user: store.User{ID: "actor"}, target: store.SSHTarget{ID: "target", TargetType: store.TargetAgent, AgentID: "agent", OwnerType: store.OwnerOrganization, OwnerID: "org"}, member: store.OrganizationMember{OrganizationID: "org", UserID: "actor", Role: "member"}, visible: true, grant: store.CommunicationsAccess{UserID: "actor", TargetID: "target", Operation: "call", Allowed: true, GrantedBy: "admin", UpdatedAt: now}}
			p := communicationsPrincipal{actorID: "actor", tokenID: "token", tokenHash: [32]byte{1}}
			binding := serviceplugins.DispatchBinding{ActorID: "actor", TargetID: "target", ProviderID: "provider", SessionID: "session", Generation: 1}
			current := communicationsProviderSession{Binding: binding, AgentID: "agent"}
			clock := now
			op := "call"
			bindingError := false
			bindingCalls := 0
			a := &communicationsAuthorizer{repo: f, now: func() time.Time { return clock }, currentBinding: func(context.Context) (communicationsProviderSession, error) {
				bindingCalls++
				if name == "expiry during binding" && bindingCalls == 2 {
					clock = now.Add(time.Minute)
				}
				if bindingError {
					return current, errors.New("closed")
				}
				return current, nil
			}}
			switch name {
			case "allow audio":
				op = "audio"
				f.grant.Operation = op
			case "allow sms":
				op = "sms"
				f.grant.Operation = op
			case "owner visible":
				f.member.Role = store.RoleOwner
				f.visible = false
			case "member org mismatch":
				f.member.OrganizationID = "other"
			case "replace provider":
				f.onGrant = func() { current.Binding.ProviderID = "other" }
			case "actor mismatch":
				p.actorID = "other"
			case "empty hash":
				p.tokenHash = [32]byte{}
			case "token owner":
				f.token.UserID = "other"
			case "token identity":
				f.token.ID = "other"
			case "wildcard", "admin wildcard":
				f.token.ToolGroups = []string{"*"}
			case "scope case":
				f.token.ToolGroups = []string{"Communications"}
			case "scope removed":
				f.token.ToolGroups = nil
			case "disabled":
				f.user.DisabledAt = &now
			case "user mismatch":
				f.user.ID = "other"
			case "target mismatch":
				f.target.ID = "other"
			case "not agent":
				f.target.TargetType = "ssh"
			case "missing agent":
				f.target.AgentID = ""
			case "personal target":
				f.target.OwnerType = "user"
			case "membership":
				f.fail = "member"
			case "member identity":
				f.member.UserID = "other"
			case "visibility":
				f.visible = false
			case "grant missing", "admin no consent":
				f.grant.Allowed = false
			case "grant actor":
				f.grant.UserID = "other"
			case "grant target":
				f.grant.TargetID = "other"
			case "grant operation":
				f.grant.Operation = "sms"
			case "expiry boundary":
				f.grant.ExpiresAt = &now
			case "zero clock":
				clock = time.Time{}
			case "generation replaced":
				f.onGrant = func() { current.Binding.Generation++ }
			case "agent replaced during lookup":
				f.onGrant = func() { current.AgentID = "replacement" }
			case "agent remapped":
				f.target.AgentID = "replacement"
			case "expiry during binding":
				expiry := now.Add(time.Second)
				f.grant.ExpiresAt = &expiry
			case "binding error":
				bindingError = true
			case "unknown operation":
				op = "event"
			case "token error":
				f.fail = "token"
			case "user error":
				f.fail = "user"
			case "target error":
				f.fail = "target"
			case "member error":
				f.fail = "member"
			case "visibility error":
				f.fail = "visibility"
			case "grant error":
				f.fail = "grant"
			}
			if name == "admin no consent" || name == "admin wildcard" || name == "admin explicit" {
				f.user.IsSystemAdmin = true
				f.fail = "member"
				f.visible = false
			}
			ctx := context.WithValue(context.Background(), communicationsPrincipalKey{}, p)
			if name == "missing principal" {
				ctx = context.Background()
			}
			if name == "cancel during grant" {
				c, cancel := context.WithCancel(ctx)
				defer cancel()
				ctx = c
				f.onGrant = cancel
			}
			if name == "canceled" {
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			}
			err := a.Authorize(ctx, binding, op)
			allowed := name == "allow" || name == "admin explicit" || name == "allow audio" || name == "allow sms" || name == "owner visible" || name == "revoke token" || name == "revoke scope" || name == "disable actor"
			if allowed && err != nil {
				t.Fatal(err)
			}
			if !allowed && !errors.Is(err, errCommunicationsDenied) {
				t.Fatalf("expected deny, got %v", err)
			}
			if allowed {
				switch name {
				case "revoke token":
					f.fail = "token"
				case "revoke scope":
					f.token.ToolGroups = nil
				case "disable actor":
					f.user.DisabledAt = &now
				default:
					f.grant.Allowed = false
				}
				if a.Authorize(ctx, binding, op) == nil {
					t.Fatal("revocation not reloaded")
				}
				if f.reads != 2 {
					t.Fatalf("credential reads=%d", f.reads)
				}
			}
		})
	}
}

func TestCommunicationsAuthorizationProductionScopeRemainsUnavailable(t *testing.T) {
	app, user, target := desktopTestApp(t)
	// This is a disposable database fixture, not a real credential or scope grant.
	digest := [32]byte{9}
	token, err := app.store.Repository().CreateMCPToken(context.Background(), store.CreateMCPTokenParams{UserID: user.ID, Name: "fixture", TokenHash: digest[:], ToolGroups: []string{"communications", "*"}})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := app.store.Repository().GetMCPTokenByHash(context.Background(), digest[:])
	if err != nil || loaded.ID != token.ID {
		t.Fatalf("fixture token: %v", err)
	}
	for _, group := range loaded.ToolGroups {
		if group == "communications" || group == "*" {
			t.Fatal("production token acquired communications scope")
		}
	}
	binding := serviceplugins.DispatchBinding{ActorID: user.ID, TargetID: target.ID, ProviderID: "fixture", SessionID: "fixture", Generation: 1}
	gate := &communicationsAuthorizer{repo: app.store.Repository(), now: time.Now, currentBinding: func(context.Context) (communicationsProviderSession, error) {
		return communicationsProviderSession{Binding: binding, AgentID: target.AgentID}, nil
	}}
	ctx := context.WithValue(context.Background(), communicationsPrincipalKey{}, communicationsPrincipal{actorID: user.ID, tokenID: token.ID, tokenHash: digest})
	if gate.Authorize(ctx, binding, "call") == nil {
		t.Fatal("real repository unexpectedly admitted normalized token")
	}
	// Existing token normalization must not be broadened by the admission seam.
	groups := allMCPToolGroups()
	if groups["communications"] {
		t.Fatal("communications exposed by cookie defaults")
	}
	var nilGate *communicationsAuthorizer
	if nilGate.Authorize(context.Background(), serviceplugins.DispatchBinding{}, "call") == nil {
		t.Fatal("nil gate allowed")
	}
}
