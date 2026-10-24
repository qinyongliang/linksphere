package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// CommunicationsAccess stores explicit consent, not an authenticated identity.
// Callers must independently reload actor/target visibility and authenticate the
// granting administrator. These methods do not expose an API or activate a device.
type CommunicationsAccess struct {
	UserID    string     `json:"user_id"`
	TargetID  string     `json:"target_id"`
	Operation string     `json:"operation"`
	Allowed   bool       `json:"allowed"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	GrantedBy string     `json:"granted_by"`
	UpdatedAt time.Time  `json:"updated_at"`
}

func validCommunicationsOperation(operation string) bool {
	return operation == "call" || operation == "audio" || operation == "sms"
}

func validCommunicationsID(value string) bool {
	return value != "" && len(value) <= 256 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}

// Effective checks this stored grant only. It is not a substitute for live actor,
// target, token, provider, session generation or local Android root checks.
func (a CommunicationsAccess) Effective(now time.Time) bool {
	return !now.IsZero() && validCommunicationsID(a.UserID) && validCommunicationsID(a.TargetID) &&
		validCommunicationsOperation(a.Operation) && validCommunicationsID(a.GrantedBy) &&
		!a.UpdatedAt.IsZero() && a.Allowed && (a.ExpiresAt == nil || now.Before(*a.ExpiresAt))
}

// GetCommunicationsAccess performs a fresh read; there is deliberately no grant cache.
// An absent row denies. Malformed persisted timestamps fail closed with an error.
func (r *Repository) GetCommunicationsAccess(ctx context.Context, user, target, operation string) (CommunicationsAccess, error) {
	a := CommunicationsAccess{UserID: user, TargetID: target, Operation: operation}
	if !validCommunicationsID(user) || !validCommunicationsID(target) || !validCommunicationsOperation(operation) {
		return a, errors.New("invalid communications grant identity or operation")
	}
	var allowed int
	var expiry sql.NullString
	var updated string
	err := r.db.QueryRowContext(ctx, `SELECT allowed,expires_at,granted_by,updated_at FROM communications_access WHERE user_id=? AND target_id=? AND operation=?`, user, target, operation).Scan(&allowed, &expiry, &a.GrantedBy, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return a, nil
	}
	if err != nil {
		return a, err
	}
	a.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil || a.UpdatedAt.IsZero() || !validCommunicationsID(a.GrantedBy) || (allowed != 0 && allowed != 1) {
		return a, errors.New("invalid stored communications grant")
	}
	if expiry.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, expiry.String)
		if parseErr != nil || value.IsZero() {
			return a, errors.New("invalid stored communications grant expiry")
		}
		a.ExpiresAt = &value
	}
	a.Allowed = allowed == 1
	return a, nil
}

// SetCommunicationsAccess is a persistence primitive for a future authenticated
// grant-management adapter. It must never be directly bound to untrusted JSON.
// Revocation writes an explicit deny row and clears expiry; no device I/O occurs.
func (r *Repository) SetCommunicationsAccess(ctx context.Context, a CommunicationsAccess) error {
	if !validCommunicationsID(a.UserID) || !validCommunicationsID(a.TargetID) || !validCommunicationsID(a.GrantedBy) || !validCommunicationsOperation(a.Operation) {
		return errors.New("invalid communications grant identity or operation")
	}
	now := time.Now().UTC()
	var expiry any
	if a.Allowed && a.ExpiresAt != nil {
		if !a.ExpiresAt.After(now) {
			return errors.New("communications grant expiry must be in the future")
		}
		expiry = a.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := r.db.ExecContext(ctx, `INSERT INTO communications_access(user_id,target_id,operation,allowed,expires_at,granted_by,updated_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(user_id,target_id,operation) DO UPDATE SET allowed=excluded.allowed,expires_at=excluded.expires_at,granted_by=excluded.granted_by,updated_at=excluded.updated_at`, a.UserID, a.TargetID, a.Operation, boolInt(a.Allowed), expiry, a.GrantedBy, now.Format(time.RFC3339Nano))
	return err
}
