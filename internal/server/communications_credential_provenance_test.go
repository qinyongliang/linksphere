package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/qinyongliang/linksphere/internal/auth"
	"github.com/qinyongliang/linksphere/internal/store"
)

// These are baseline characterization tests, not endorsement of cookie-first
// precedence or evidence of communications authorization. All requests are
// in-memory and all credentials are non-secret fixtures in temporary SQLite.
func TestMCPCommunicationsCredentialPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name                            string
		client, loopback                bool
		cookie, bearer, wantUser        string
		wantAll, wantError, wantTouched bool
	}{
		{name: "no_credentials", wantError: true},
		{name: "cookie_only", cookie: "valid", wantUser: "cookie", wantAll: true},
		{name: "bearer_only", bearer: "valid", wantUser: "bearer", wantTouched: true},
		{name: "invalid_bearer_only", bearer: "invalid", wantError: true},
		{name: "invalid_cookie_only", cookie: "invalid", wantError: true},
		{name: "mixed_distinct_users_cookie_wins", cookie: "valid", bearer: "valid", wantUser: "cookie", wantAll: true},
		{name: "valid_cookie_invalid_bearer_ignored", cookie: "valid", bearer: "invalid", wantUser: "cookie", wantAll: true},
		{name: "invalid_cookie_falls_back_to_bearer", cookie: "invalid", bearer: "valid", wantUser: "bearer", wantTouched: true},
		{name: "expired_cookie_falls_back_to_bearer", cookie: "expired", bearer: "valid", wantUser: "bearer", wantTouched: true},
		{name: "client_loopback_no_credentials", client: true, loopback: true, wantUser: "client", wantAll: true},
		{name: "client_loopback_bearer_ignored", client: true, loopback: true, bearer: "valid", wantUser: "client", wantAll: true},
		{name: "client_loopback_cookie_and_bearer_ignored", client: true, loopback: true, cookie: "valid", bearer: "valid", wantUser: "client", wantAll: true},
		{name: "client_loopback_invalid_bearer_ignored", client: true, loopback: true, bearer: "invalid", wantUser: "client", wantAll: true},
		{name: "client_remote_no_credentials", client: true, wantError: true},
		{name: "client_remote_bearer", client: true, bearer: "valid", wantUser: "bearer", wantTouched: true},
		{name: "client_remote_cookie", client: true, cookie: "valid", wantUser: "cookie", wantAll: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st, err := store.Open(ctx, filepath.Join(t.TempDir(), "fixture.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := st.Close(); err != nil {
					t.Error(err)
				}
			})
			// Preinitialized store/auth intentionally skips ensureServices startup: the
			// client local agent must not start. Actual request-auth code is unmodified.
			app := &App{cfg: Config{ClientMode: tc.client, SessionCookieName: "diagnostic_session"}, store: st, auth: auth.NewService(st.Repository())}
			repo := st.Repository()
			users := map[string]store.User{}
			for _, name := range []string{"cookie", "bearer"} {
				u, err := repo.CreateUser(ctx, store.CreateUserParams{Email: name + "@example.test", DisplayName: name, PasswordHash: []byte("noncredential-fixture")})
				if err != nil {
					t.Fatal(err)
				}
				users[name] = u
			}
			const cookieValue = "diagnostic-cookie-value"
			expiry := time.Now().UTC().Add(time.Hour)
			if tc.cookie == "expired" {
				expiry = time.Now().UTC().Add(-time.Hour)
			}
			if _, err := repo.CreateSession(ctx, users["cookie"].ID, codeHash(cookieValue), expiry); err != nil {
				t.Fatal(err)
			}
			const bearerValue = "diagnostic-bearer-value"
			token, err := repo.CreateMCPToken(ctx, store.CreateMCPTokenParams{UserID: users["bearer"].ID, Name: "fixture", TokenHash: codeHash(bearerValue), ToolGroups: []string{"desktop"}})
			if err != nil {
				t.Fatal(err)
			}
			if token.LastUsedAt != nil {
				t.Fatal("fresh token unexpectedly touched")
			}
			req := httptest.NewRequest(http.MethodPost, "http://example.test/mcp", nil)
			req.RemoteAddr = "203.0.113.10:41234"
			if tc.loopback {
				req.RemoteAddr = "127.0.0.1:41234"
			}
			if tc.cookie != "" {
				value := cookieValue
				if tc.cookie == "invalid" {
					value = "unrecognized-cookie"
				}
				req.AddCookie(&http.Cookie{Name: app.sessionCookieName(), Value: value})
			}
			if tc.bearer != "" {
				value := bearerValue
				if tc.bearer == "invalid" {
					value = "unrecognized-bearer"
				}
				req.Header.Set("Authorization", "Bearer "+value)
			}
			actor, err := app.actorForMCPRequest(req)
			if tc.wantError {
				if err == nil {
					t.Fatal("expected authentication error")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if tc.wantUser == "client" {
					user, err := repo.EnsureClientUser(ctx)
					if err != nil {
						t.Fatal(err)
					}
					users["client"] = user
				}
				if actor.User.ID != users[tc.wantUser].ID {
					t.Fatalf("wrong credential selected: got user %q, want %q", actor.User.Email, tc.wantUser)
				}
				wantGroups := map[string]bool{"desktop": true}
				if tc.wantAll {
					wantGroups = allMCPToolGroups()
				}
				if !reflect.DeepEqual(actor.ToolGroups, wantGroups) {
					t.Fatalf("groups = %#v, want %#v", actor.ToolGroups, wantGroups)
				}
				if actor.Runtime.ClientMode != tc.client {
					t.Fatalf("client runtime = %v, want %v", actor.Runtime.ClientMode, tc.client)
				}
			}
			persisted, err := repo.GetMCPTokenByHash(ctx, codeHash(bearerValue))
			if err != nil {
				t.Fatal(err)
			}
			if got := persisted.LastUsedAt != nil; got != tc.wantTouched {
				t.Fatalf("bearer touched = %v, want %v", got, tc.wantTouched)
			}
		})
	}
}

func TestMCPCommunicationsRealSQLiteScopeNormalizationUnchanged(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "fixture.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	repo := st.Repository()
	user, err := repo.CreateUser(ctx, store.CreateUserParams{Email: "scope@example.test", DisplayName: "Scope", PasswordHash: []byte("noncredential-fixture")})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		input, want []string
	}{
		{"communications_only", []string{"communications"}, []string{"session"}},
		{"mixed_known_and_communications", []string{" Desktop ", "communications", "desktop"}, []string{"desktop"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hash := codeHash("fixture-" + tc.name)
			created, err := repo.CreateMCPToken(ctx, store.CreateMCPTokenParams{UserID: user.ID, Name: tc.name, TokenHash: hash, ToolGroups: tc.input})
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := repo.GetMCPTokenByHash(ctx, hash)
			if err != nil {
				t.Fatal(err)
			}
			for _, token := range []store.MCPToken{created, loaded} {
				if !reflect.DeepEqual(token.ToolGroups, tc.want) {
					t.Fatalf("groups = %#v, want %#v", token.ToolGroups, tc.want)
				}
			}
		})
	}
}
