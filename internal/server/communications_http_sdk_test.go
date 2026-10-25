package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/qinyongliang/linksphere/internal/auth"
	"github.com/qinyongliang/linksphere/internal/store"
)

// Exercise the real request-auth wrapper and SDK HTTP dispatch, without opening
// a listener, registering production communications tools, or calling a provider.
func TestCommunicationsPrincipalReachesHTTPMCPTool(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "fixture.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	repo := st.Repository()
	app := &App{cfg: Config{SessionCookieName: "fixture_session"}, store: st, auth: auth.NewService(repo)}
	user, err := repo.CreateUser(ctx, store.CreateUserParams{Email: "sdk@example.test", DisplayName: "SDK", PasswordHash: []byte("noncredential-fixture")})
	if err != nil {
		t.Fatal(err)
	}
	const raw = "synthetic-sdk-bearer"
	token, err := repo.CreateMCPToken(ctx, store.CreateMCPTokenParams{UserID: user.ID, Name: "fixture", TokenHash: codeHash(raw), ToolGroups: []string{"desktop"}})
	if err != nil {
		t.Fatal(err)
	}
	want := communicationsPrincipal{actorID: user.ID, tokenID: token.ID, tokenHash: sha256.Sum256([]byte(raw))}
	seen := make(chan communicationsPrincipal, 1)
	handler := app.mcpHandlerWithServer(func(actor mcpActor) *mcp.Server {
		server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
		mcp.AddTool(server, &mcp.Tool{Name: "capture_principal", Description: "Synthetic context probe"}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, mcpOK, error) {
			p, _ := ctx.Value(communicationsPrincipalKey{}).(communicationsPrincipal)
			seen <- p
			return nil, mcpOK{OK: true}, nil
		})
		return server
	})
	request := httptest.NewRequest(http.MethodPost, "http://example.test/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"capture_principal","arguments":{}}}`))
	request.RemoteAddr = "203.0.113.10:41234"
	request.Header.Set("Authorization", "Bearer "+raw)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-03-26")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("HTTP status %d", recorder.Code)
	}
	var response struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Error) > 0 {
		t.Fatal("SDK returned JSON-RPC error")
	}
	select {
	case got := <-seen:
		if got != want {
			t.Fatal("tool context did not preserve authenticated bearer identity")
		}
	default:
		t.Fatal("tool was not dispatched")
	}
}

func TestCommunicationsHTTPMCPMasksNonBearerPrincipal(t *testing.T) {
	for _, mode := range []string{"cookie", "loopback"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			st, err := store.Open(ctx, filepath.Join(t.TempDir(), "fixture.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			repo := st.Repository()
			app := &App{cfg: Config{SessionCookieName: "fixture_session", ClientMode: mode == "loopback"}, store: st, auth: auth.NewService(repo)}
			user, err := repo.CreateUser(ctx, store.CreateUserParams{Email: "cookie@example.test", DisplayName: "Cookie", PasswordHash: []byte("noncredential-fixture")})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = repo.CreateSession(ctx, user.ID, codeHash("synthetic-cookie"), time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			stale := communicationsPrincipal{actorID: "stale", tokenID: "stale-token", tokenHash: sha256.Sum256([]byte("synthetic-stale"))}
			seen := make(chan communicationsPrincipal, 1)
			handler := app.mcpHandlerWithServer(func(mcpActor) *mcp.Server {
				server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
				mcp.AddTool(server, &mcp.Tool{Name: "capture_principal"}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, mcpOK, error) {
					p, _ := ctx.Value(communicationsPrincipalKey{}).(communicationsPrincipal)
					seen <- p
					return nil, mcpOK{OK: true}, nil
				})
				return server
			})
			request := sdkPrincipalRequest()
			request = request.WithContext(context.WithValue(ctx, communicationsPrincipalKey{}, stale))
			request.Header.Set("Authorization", "Bearer unrecognized-synthetic-bearer")
			if mode == "cookie" {
				request.AddCookie(&http.Cookie{Name: "fixture_session", Value: "synthetic-cookie"})
			} else {
				request.RemoteAddr = "127.0.0.1:41234"
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status %d", recorder.Code)
			}
			select {
			case got := <-seen:
				if got != (communicationsPrincipal{}) {
					t.Fatal("non-bearer request inherited communications principal")
				}
			default:
				t.Fatal("tool not dispatched")
			}
		})
	}
}

func sdkPrincipalRequest() *http.Request {
	request := httptest.NewRequest(http.MethodPost, "http://example.test/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"capture_principal","arguments":{}}}`))
	request.RemoteAddr = "203.0.113.10:41234"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("MCP-Protocol-Version", "2025-03-26")
	return request
}

func TestCommunicationsHTTPMCPConcurrentIdentityAndCancellation(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "fixture.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	repo := st.Repository()
	app := &App{cfg: Config{SessionCookieName: "fixture_session"}, store: st, auth: auth.NewService(repo)}
	wants := map[string]communicationsPrincipal{}
	for _, raw := range []string{"synthetic-alice", "synthetic-bob"} {
		user, err := repo.CreateUser(ctx, store.CreateUserParams{Email: raw + "@example.test", DisplayName: raw, PasswordHash: []byte("noncredential-fixture")})
		if err != nil {
			t.Fatal(err)
		}
		token, err := repo.CreateMCPToken(ctx, store.CreateMCPTokenParams{UserID: user.ID, Name: "fixture", TokenHash: codeHash(raw), ToolGroups: []string{"desktop"}})
		if err != nil {
			t.Fatal(err)
		}
		wants[raw] = communicationsPrincipal{actorID: user.ID, tokenID: token.ID, tokenHash: sha256.Sum256([]byte(raw))}
	}
	errorsFound := make(chan string, 32)
	entered := make(chan struct{}, 1)
	cancelSeen := make(chan error, 1)
	type waitKey struct{}
	handler := app.mcpHandlerWithServer(func(actor mcpActor) *mcp.Server {
		server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
		mcp.AddTool(server, &mcp.Tool{Name: "capture_principal"}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, mcpOK, error) {
			got, _ := ctx.Value(communicationsPrincipalKey{}).(communicationsPrincipal)
			if actor.communications == nil || got != *actor.communications {
				errorsFound <- "request actor differs from tool context"
			}
			if ctx.Value(waitKey{}) == true {
				call, _, release := communicationsInvocationContext(ctx, got, time.Minute)
				defer release()
				entered <- struct{}{}
				<-call.Done()
				cancelSeen <- call.Err()
				return nil, mcpOK{}, call.Err()
			}
			return nil, mcpOK{OK: true}, nil
		})
		return server
	})
	var workers sync.WaitGroup
	for i := 0; i < 24; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			raw := []string{"synthetic-alice", "synthetic-bob"}[i%2]
			request := sdkPrincipalRequest()
			request.Header.Set("Authorization", "Bearer "+raw)
			// A foreign principal on the incoming context must never survive bearer authentication.
			request = request.WithContext(context.WithValue(ctx, communicationsPrincipalKey{}, wants[[]string{"synthetic-bob", "synthetic-alice"}[i%2]]))
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				errorsFound <- "unexpected concurrent HTTP status"
			}
		}(i)
	}
	workers.Wait()
	select {
	case message := <-errorsFound:
		t.Fatal(message)
	default:
	}
	request := sdkPrincipalRequest()
	request.Header.Set("Authorization", "Bearer synthetic-alice")
	cancelCtx, cancel := context.WithCancel(context.WithValue(ctx, waitKey{}, true))
	defer cancel()
	request = request.WithContext(cancelCtx)
	finished := make(chan struct{})
	go func() { defer close(finished); handler.ServeHTTP(httptest.NewRecorder(), request) }()
	select {
	case <-entered:
		cancel()
	case <-time.After(5 * time.Second):
		t.Fatal("tool did not receive request context")
	}
	select {
	case err := <-cancelSeen:
		if err != context.Canceled {
			t.Fatal("request cancellation not preserved")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("tool context was not canceled")
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled handler did not return")
	}
}
