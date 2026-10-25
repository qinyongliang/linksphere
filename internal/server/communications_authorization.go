package server

import (
	"context"
	"errors"
	"time"

	"github.com/qinyongliang/linksphere/internal/serviceplugins"
	"github.com/qinyongliang/linksphere/internal/store"
)

var errCommunicationsDenied = errors.New("communications authorization denied")

// This private principal must be created by an explicit authenticated request
// adapter, never from tool arguments, AgentHello or client-mode loopback login.
// The MCP bearer branch supplies provenance; no communications MCP tool is registered. Only the digest is
// retained, not a credential or request object.
type communicationsPrincipal struct {
	actorID   string
	tokenID   string
	tokenHash [32]byte
}
type communicationsPrincipalKey struct{}

type communicationsAuthorizationRepository interface {
	GetMCPTokenByHash(context.Context, []byte) (store.MCPToken, error)
	GetUser(context.Context, string) (store.User, error)
	GetSSHTarget(context.Context, string) (store.SSHTarget, error)
	GetOrganizationMember(context.Context, string, string) (store.OrganizationMember, error)
	UserCanAccessTarget(context.Context, string, string) (bool, error)
	GetCommunicationsAccess(context.Context, string, string, string) (store.CommunicationsAccess, error)
}

// communicationsAuthorizer is an unregistered admission seam. The binding
// callback must consult a trusted provider-session manager, NOT self-reported
// platform metadata. The snapshot must include the authenticated Agent association;
// a target remapped to a different Agent invalidates the old snapshot. Central consent does not prove local Android UID 0; the
// local dispatcher independently checks that before invoking its provider.
// Persisted MCP scopes currently exclude communications, so production tokens
// cannot pass this gate. Do not broaden that allowlist merely to enable it.
type communicationsProviderSession struct {
	Binding serviceplugins.DispatchBinding
	AgentID string
}

type communicationsAuthorizer struct {
	repo           communicationsAuthorizationRepository
	currentBinding func(context.Context) (communicationsProviderSession, error)
	now            func() time.Time
}

var _ serviceplugins.DispatchAuthorizer = (*communicationsAuthorizer)(nil)

func (a *communicationsAuthorizer) Authorize(ctx context.Context, binding serviceplugins.DispatchBinding, operation string) error {
	if a == nil || nilCommunicationsValue(a.repo) || a.currentBinding == nil || a.now == nil || ctx == nil || ctx.Err() != nil {
		return errCommunicationsDenied
	}
	if operation != "call" && operation != "audio" && operation != "sms" {
		return errCommunicationsDenied
	}
	p, ok := ctx.Value(communicationsPrincipalKey{}).(communicationsPrincipal)
	if !ok || p.actorID == "" || p.tokenID == "" || p.tokenHash == ([32]byte{}) || p.actorID != binding.ActorID || binding.TargetID == "" || binding.ProviderID == "" || binding.SessionID == "" || binding.Generation == 0 {
		return errCommunicationsDenied
	}
	current, err := a.currentBinding(ctx)
	if err != nil || current.Binding != binding || current.AgentID == "" {
		return errCommunicationsDenied
	}
	token, err := a.repo.GetMCPTokenByHash(ctx, p.tokenHash[:])
	if err != nil || token.ID != p.tokenID || token.UserID != p.actorID {
		return errCommunicationsDenied
	}
	explicit := false
	for _, group := range token.ToolGroups {
		if group == "communications" {
			explicit = true
		}
	}
	if !explicit {
		return errCommunicationsDenied
	}
	user, err := a.repo.GetUser(ctx, p.actorID)
	if err != nil || user.ID != p.actorID || user.DisabledAt != nil {
		return errCommunicationsDenied
	}
	target, err := a.repo.GetSSHTarget(ctx, binding.TargetID)
	if err != nil || target.ID != binding.TargetID || target.TargetType != store.TargetAgent || target.AgentID == "" || target.OwnerType != store.OwnerOrganization || target.OwnerID == "" || target.AgentID != current.AgentID {
		return errCommunicationsDenied
	}
	if !user.IsSystemAdmin {
		member, memberErr := a.repo.GetOrganizationMember(ctx, target.OwnerID, user.ID)
		if memberErr != nil || member.UserID != user.ID || member.OrganizationID != target.OwnerID {
			return errCommunicationsDenied
		}
		if member.Role != store.RoleOwner && member.Role != store.RoleAdmin {
			visible, visibilityErr := a.repo.UserCanAccessTarget(ctx, user.ID, target.ID)
			if visibilityErr != nil || !visible {
				return errCommunicationsDenied
			}
		}
	}
	grant, err := a.repo.GetCommunicationsAccess(ctx, user.ID, target.ID, operation)
	now := a.now()
	if err != nil || grant.UserID != user.ID || grant.TargetID != target.ID || grant.Operation != operation || !grant.Effective(now) {
		return errCommunicationsDenied
	}
	current, err = a.currentBinding(ctx)
	if err != nil || current.Binding != binding || current.AgentID != target.AgentID || ctx.Err() != nil || !grant.Effective(a.now()) {
		return errCommunicationsDenied
	}
	return nil
}
