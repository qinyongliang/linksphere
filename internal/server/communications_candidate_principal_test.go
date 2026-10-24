package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/qinyongliang/linksphere/internal/auth"
	"github.com/qinyongliang/linksphere/internal/store"
)

// Candidate-only actor tests. These do not inspect the downstream handler
// context or prove MCP transport propagation into a tool invocation.
func TestMCPCommunicationsCandidateBearerPrincipal(t *testing.T) {
	for _, tc := range []struct {
		name                             string
		cookie, client, loopback, bearer bool
		wantPrincipal                    bool
	}{
		{name: "bearer", bearer: true, wantPrincipal: true},
		{name: "cookie", cookie: true},
		{name: "mixed_cookie_and_bearer", cookie: true, bearer: true},
		{name: "loopback_client", client: true, loopback: true},
		{name: "loopback_client_and_bearer", client: true, loopback: true, bearer: true},
		{name: "remote_client_and_bearer", client: true, bearer: true, wantPrincipal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st, err := store.Open(ctx, filepath.Join(t.TempDir(), "fixture.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			repo := st.Repository()
			app := &App{cfg: Config{ClientMode: tc.client, SessionCookieName: "diagnostic_session"}, store: st, auth: auth.NewService(repo)}
			user, err := repo.CreateUser(ctx, store.CreateUserParams{Email: "principal@example.test", DisplayName: "Principal", PasswordHash: []byte("noncredential-fixture")})
			if err != nil {
				t.Fatal(err)
			}
			const raw = "diagnostic-distinct-raw-bearer"
			token, err := repo.CreateMCPToken(ctx, store.CreateMCPTokenParams{UserID: user.ID, Name: "fixture", TokenHash: codeHash(raw), ToolGroups: []string{"desktop"}})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "http://example.test/mcp", nil)
			req.RemoteAddr = "203.0.113.10:41234"
			if tc.loopback {
				req.RemoteAddr = "[::1]:41234"
			}
			if tc.cookie {
				if _, err := repo.CreateSession(ctx, user.ID, codeHash("fixture-cookie"), time.Now().UTC().Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
				req.AddCookie(&http.Cookie{Name: app.sessionCookieName(), Value: "fixture-cookie"})
			}
			if tc.bearer {
				req.Header.Set("Authorization", "Bearer "+raw)
			}
			actor, err := app.actorForMCPRequest(req)
			if err != nil {
				t.Fatal(err)
			}
			if got := actor.communications != nil; got != tc.wantPrincipal {
				t.Fatalf("principal present = %v, want %v", got, tc.wantPrincipal)
			}
			if tc.wantPrincipal {
				want := communicationsPrincipal{actorID: user.ID, tokenID: token.ID, tokenHash: sha256.Sum256([]byte(raw))}
				if *actor.communications != want {
					t.Fatal("principal is not bound to selected bearer user, token ID and SHA-256 digest")
				}
				// Changing the request afterwards cannot change the captured identity.
				req.Header.Set("Authorization", "Bearer replaced-fixture")
				if *actor.communications != want {
					t.Fatal("principal changed with request headers")
				}
				// Exact private value layout prevents introducing a retained raw credential
				// or request pointer without an explicit test review.
				typ := reflect.TypeOf(*actor.communications)
				if typ.NumField() != 3 {
					t.Fatalf("unexpected principal field count: %d", typ.NumField())
				}
				for i := 0; i < typ.NumField(); i++ {
					if typ.Field(i).IsExported() {
						t.Fatal("principal field unexpectedly exported")
					}
				}
			}
			typ := reflect.TypeOf(actor)
			field, ok := typ.FieldByName("communications")
			if !ok || field.IsExported() {
				t.Fatal("actor provenance must remain private")
			}
			encoded, err := json.Marshal(actor)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), raw) || strings.Contains(string(encoded), token.ID) || strings.Contains(string(encoded), "communications") {
				t.Fatal("serialized actor exposed private bearer provenance")
			}
		})
	}
}
