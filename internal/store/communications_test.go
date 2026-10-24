package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestCommunicationsAccessPersistenceIsolationAndRevocation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "main.db")
	st, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { st.Close() }()
	r := st.Repository()
	user, err := r.CreateUser(ctx, CreateUserParams{Email: "communications@example.test", DisplayName: "Fixture", PasswordHash: []byte("hash")})
	if err != nil {
		t.Fatal(err)
	}
	other, err := r.CreateUser(ctx, CreateUserParams{Email: "other@example.test", DisplayName: "Other", PasswordHash: []byte("hash")})
	if err != nil {
		t.Fatal(err)
	}
	org, err := r.CreateOrganization(ctx, CreateOrganizationParams{Name: "Fixture", Slug: "communications", OwnerUserID: user.ID})
	if err != nil {
		t.Fatal(err)
	}
	target, err := r.CreateSSHTarget(ctx, CreateSSHTargetParams{OwnerType: OwnerOrganization, OwnerID: org.ID, Alias: "phone", TargetType: TargetAgent, AgentID: "test-agent", CreatedBy: user.ID})
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.CreateSSHTarget(ctx, CreateSSHTargetParams{OwnerType: OwnerOrganization, OwnerID: org.ID, Alias: "other-phone", TargetType: TargetAgent, AgentID: "other-test-agent", CreatedBy: user.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"call", "audio", "sms"} {
		a, e := r.GetCommunicationsAccess(ctx, user.ID, target.ID, op)
		if e != nil || a.Allowed || a.Effective(time.Now()) {
			t.Fatalf("default grant: %+v %v", a, e)
		}
	}
	expires := time.Now().Add(time.Hour)
	a := CommunicationsAccess{UserID: user.ID, TargetID: target.ID, Operation: "sms", Allowed: true, ExpiresAt: &expires, GrantedBy: user.ID}
	if err = r.SetCommunicationsAccess(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, err := r.GetCommunicationsAccess(ctx, user.ID, target.ID, "sms")
	if err != nil || !got.Effective(time.Now()) {
		t.Fatalf("grant missing: %+v %v", got, err)
	}
	if got.Effective(time.Time{}) || got.Effective(expires) || got.Effective(expires.Add(time.Second)) {
		t.Fatal("expired grant allowed")
	}
	for _, key := range [][3]string{{user.ID, target.ID, "call"}, {user.ID, target.ID, "audio"}, {other.ID, target.ID, "sms"}, {user.ID, second.ID, "sms"}} {
		g, e := r.GetCommunicationsAccess(ctx, key[0], key[1], key[2])
		if e != nil || g.Effective(time.Now()) {
			t.Fatalf("grant leaked: %+v %v", g, e)
		}
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	r = st.Repository()
	got, err = r.GetCommunicationsAccess(ctx, user.ID, target.ID, "sms")
	if err != nil || !got.Effective(time.Now()) {
		t.Fatal("restart lost grant", err)
	}
	// Expired input must never prevent revocation of a previously live grant.
	past := time.Now().Add(-time.Hour)
	a.Allowed = false
	a.ExpiresAt = &past
	if err = r.SetCommunicationsAccess(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, err = r.GetCommunicationsAccess(ctx, user.ID, target.ID, "sms")
	if err != nil || got.Allowed || got.ExpiresAt != nil || got.Effective(time.Now()) {
		t.Fatalf("revoke failed: %+v %v", got, err)
	}
	// A grant cannot outlive its granting identity, or name a nonexistent one.
	a.Allowed = true
	a.ExpiresAt = nil
	a.GrantedBy = "missing-grantor"
	if err = r.SetCommunicationsAccess(ctx, a); err == nil {
		t.Fatal("accepted nonexistent grantor")
	}
	a.GrantedBy = other.ID
	if err = r.SetCommunicationsAccess(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err = r.db.ExecContext(ctx, "DELETE FROM users WHERE id=?", other.ID); err != nil {
		t.Fatal(err)
	}
	got, err = r.GetCommunicationsAccess(ctx, user.ID, target.ID, "sms")
	if err != nil || got.Effective(time.Now()) {
		t.Fatal("deleted grantor retained consent", err)
	}
	a.GrantedBy = user.ID
	a.Allowed = false
	if err = r.SetCommunicationsAccess(ctx, a); err != nil {
		t.Fatal(err)
	}
	// Corrupt persistent rows must not turn into grants with no expiry.
	for _, column := range []string{"expires_at", "updated_at"} {
		if _, err = r.db.ExecContext(ctx, "UPDATE communications_access SET allowed=1,"+column+"='not-a-time' WHERE user_id=?", user.ID); err != nil {
			t.Fatal(err)
		}
		got, err = r.GetCommunicationsAccess(ctx, user.ID, target.ID, "sms")
		if err == nil || got.Effective(time.Now()) {
			t.Fatalf("corrupt %s accepted", column)
		}
		a.Allowed = false
		a.ExpiresAt = nil
		if err = r.SetCommunicationsAccess(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = r.db.ExecContext(ctx, "INSERT INTO communications_access(user_id,target_id,operation,allowed,granted_by,updated_at) VALUES(?,?,?,?,?,?)", user.ID, target.ID, "*", 1, user.ID, time.Now().Format(time.RFC3339Nano)); err == nil {
		t.Fatal("DB accepted wildcard operation")
	}
	if _, err = r.db.ExecContext(ctx, "UPDATE communications_access SET allowed=2 WHERE user_id=?", user.ID); err == nil {
		t.Fatal("DB accepted nonboolean allow")
	}
}

func TestCommunicationsAccessRejectsInvalidInput(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "main.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	r := st.Repository()
	for _, op := range []string{"", "*", "communications", "SMS", " sms", "event", "permissions"} {
		if _, e := r.GetCommunicationsAccess(ctx, "user", "target", op); e == nil {
			t.Fatalf("accepted op %q", op)
		}
		if e := r.SetCommunicationsAccess(ctx, CommunicationsAccess{UserID: "user", TargetID: "target", Operation: op, GrantedBy: "actor"}); e == nil {
			t.Fatalf("stored op %q", op)
		}
	}
	past := time.Now().Add(-time.Minute)
	if e := r.SetCommunicationsAccess(ctx, CommunicationsAccess{UserID: "user", TargetID: "target", Operation: "sms", Allowed: true, GrantedBy: "actor", ExpiresAt: &past}); e == nil {
		t.Fatal("accepted expired grant")
	}
	for _, id := range []string{"", " user", "user\n", "user\x00"} {
		if _, e := r.GetCommunicationsAccess(ctx, id, "target", "sms"); e == nil {
			t.Fatal("accepted invalid identity")
		}
	}
	if g := normalizeMCPToolGroups(nil); len(g) != 1 || g[0] != "session" {
		t.Fatal("default token scopes changed", g)
	}
	for _, g := range normalizeMCPToolGroups([]string{"communications"}) {
		if g == "communications" {
			t.Fatal("unfinished communications tools exposed")
		}
	}
}

func TestCommunicationsMigrationFromVersionEightDefaultsDenied(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "upgrade.db")
	st, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	// Construct a version-eight fixture by removing only this new empty migration.
	if _, err = st.Repository().db.ExecContext(ctx, "DROP TABLE communications_access; DELETE FROM schema_migrations WHERE version=9"); err != nil {
		t.Fatal(err)
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var count int
	if err = st.Repository().db.QueryRowContext(ctx, "SELECT COUNT(*) FROM communications_access").Scan(&count); err != nil || count != 0 {
		t.Fatal("upgrade generated consent", count, err)
	}
	var name string
	if err = st.Repository().db.QueryRowContext(ctx, "SELECT name FROM schema_migrations WHERE version=9").Scan(&name); err != nil || name != "0009_communications_access.sql" {
		t.Fatal(name, err)
	}
}
