package server

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// Only explicitly created local test servers may be contacted. Public release
// fallback URLs are rejected before any DNS lookup or socket connection.
type offlineReleaseTransport struct {
	base    http.RoundTripper
	allowed map[string]bool
	blocked atomic.Int64
}

func (g *offlineReleaseTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !g.allowed[r.URL.Scheme+"://"+r.URL.Host] {
		g.blocked.Add(1)
		return nil, fmt.Errorf("external release request blocked by offline test fixture")
	}
	return g.base.RoundTrip(r)
}
func restrictReleaseHTTPToFixtures(t *testing.T, servers ...*httptest.Server) {
	t.Helper()
	old := http.DefaultTransport
	base := &http.Transport{} // no environment proxy or external resolver for fixture IPs
	allowed := map[string]bool{}
	for _, server := range servers {
		allowed[server.URL] = true
	}
	guard := &offlineReleaseTransport{base: base, allowed: allowed}
	http.DefaultTransport = guard
	t.Cleanup(func() {
		http.DefaultTransport = old
		base.CloseIdleConnections()
		if n := guard.blocked.Load(); n != 0 {
			t.Errorf("release test attempted %d non-fixture requests", n)
		}
	})
}

func TestReleaseProxyURLsKeepConfiguredFirstAndDeduplicate(t *testing.T) {
	app := NewApp(Config{ReleaseProxyURL: " https://custom.example/, https://ghfast.top/ "})
	got := app.proxyReleaseURLs("https://github.com/owner/repo/file")
	want := []string{
		"https://custom.example/https://github.com/owner/repo/file",
		"https://ghfast.top/https://github.com/owner/repo/file",
		"https://fastgit.cc/https://github.com/owner/repo/file",
		"https://gh.dpik.top/https://github.com/owner/repo/file",
		"https://github.tbap.top/https://github.com/owner/repo/file",
		"https://cdn.gh-proxy.com/https://github.com/owner/repo/file",
	}
	if len(got) != len(want) {
		t.Fatalf("proxy URL count mismatch: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("proxy URL %d mismatch: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestAgentReleaseURLUsesRawAgentAsset(t *testing.T) {
	app := NewApp(Config{Version: "v1.2.3"})
	got := app.agentReleaseURL("linux", "amd64", "gosshd-agent")
	want := "https://github.com/qinyongliang/linksphere/releases/download/v1.2.3/gosshd-agent-v1.2.3-linux-amd64"
	if got != want {
		t.Fatalf("agent release url mismatch:\n got: %s\nwant: %s", got, want)
	}
}

func TestAgentReleaseURLUsesWindowsExtension(t *testing.T) {
	app := NewApp(Config{Version: "v1.2.3"})
	got := app.agentReleaseURL("windows", "arm64", "gosshd-agent.exe")
	want := "https://github.com/qinyongliang/linksphere/releases/download/v1.2.3/gosshd-agent-v1.2.3-windows-arm64.exe"
	if got != want {
		t.Fatalf("agent release url mismatch:\n got: %s\nwant: %s", got, want)
	}
}

func TestAgentCachePathDefaultsToVersionedTempDir(t *testing.T) {
	app := NewApp(Config{Version: "v1.2.3"})
	got := app.agentCachePath("linux", "amd64", "gosshd-agent")
	want := filepath.Join("gosshd-agent-cache", "v1.2.3", "linux", "amd64", "gosshd-agent")
	if filepath.ToSlash(got) == filepath.ToSlash(want) {
		t.Fatalf("expected absolute temp cache path, got relative %s", got)
	}
	if !strings.HasSuffix(filepath.ToSlash(got), filepath.ToSlash(want)) {
		t.Fatalf("cache path mismatch:\n got: %s\nwant suffix: %s", got, want)
	}
}

func TestAgentCachePathUsesVersionUnderConfiguredRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache")
	app := NewApp(Config{Version: "v1.2.3", AgentCachePath: root})
	got := app.agentCachePath("linux", "amd64", "gosshd-agent")
	want := filepath.Join(root, "v1.2.3", "linux", "amd64", "gosshd-agent")
	if got != want {
		t.Fatalf("cache path mismatch:\n got: %s\nwant: %s", got, want)
	}
}

func TestDownloadWinPTYServesCachedZip(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache")
	app := NewApp(Config{Version: "v1.2.3", AgentCachePath: root})
	cachePath := filepath.Join(root, "winpty", winPTYVersion, winPTYZipName)
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("winpty-zip")
	if err := os.WriteFile(cachePath, body, 0o644); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/download/winpty/windows/amd64", nil)
	rec := httptest.NewRecorder()
	app.downloadWinPTY(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != string(body) {
		t.Fatalf("body mismatch: %q", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/download/winpty/windows/amd64.sha256", nil)
	rec = httptest.NewRecorder()
	app.downloadWinPTY(rec, req)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != winPTYZipSHA {
		t.Fatalf("sha response mismatch: code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestEnsureAgentBinaryFallsBackToProxyChecksum(t *testing.T) {
	agentBytes := []byte("agent-binary")
	agentSHA := fmt.Sprintf("%x", sha256.Sum256(agentBytes))
	assetName := "gosshd-agent-v1.2.3-linux-amd64"

	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "direct unavailable", http.StatusInternalServerError)
	}))
	t.Cleanup(direct.Close)

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/checksums.txt"):
			_, _ = fmt.Fprintf(w, "%s  %s\n", agentSHA, assetName)
		case strings.Contains(r.URL.Path, assetName):
			_, _ = w.Write(agentBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(proxy.Close)
	restrictReleaseHTTPToFixtures(t, direct, proxy)

	cacheRoot := filepath.Join(t.TempDir(), "cache")
	app := NewApp(Config{
		Version:         "v1.2.3",
		ReleaseBaseURL:  direct.URL,
		ReleaseProxyURL: proxy.URL,
		AgentCachePath:  cacheRoot,
	})

	path, err := app.ensureAgentBinary("linux", "amd64", "gosshd-agent")
	if err != nil {
		t.Fatalf("ensureAgentBinary failed: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cached agent: %v", err)
	}
	if string(got) != string(agentBytes) {
		t.Fatalf("cached agent mismatch: %q", got)
	}
}

func TestAgentDownloadSHA256FallsBackToReleaseChecksum(t *testing.T) {
	agentBytes := []byte("agent-binary")
	agentSHA := fmt.Sprintf("%x", sha256.Sum256(agentBytes))
	assetName := "gosshd-agent-v1.2.3-linux-amd64"

	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "direct unavailable", http.StatusInternalServerError)
	}))
	t.Cleanup(direct.Close)

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/checksums.txt") {
			_, _ = fmt.Fprintf(w, "%s  %s\n", agentSHA, assetName)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(proxy.Close)
	restrictReleaseHTTPToFixtures(t, direct, proxy)

	app := NewApp(Config{
		Version:         "v1.2.3",
		ReleaseBaseURL:  direct.URL,
		ReleaseProxyURL: proxy.URL,
		AgentCachePath:  filepath.Join(t.TempDir(), "cache"),
	})

	got, err := app.agentDownloadSHA256("linux", "amd64")
	if err != nil {
		t.Fatalf("agentDownloadSHA256 failed: %v", err)
	}
	if got != agentSHA {
		t.Fatalf("sha mismatch: got %s want %s", got, agentSHA)
	}
}

func TestOfflineReleaseTransportRejectsExternalBeforeDispatch(t *testing.T) {
	guard := &offlineReleaseTransport{allowed: map[string]bool{}}
	request := httptest.NewRequest(http.MethodGet, "https://blocked.example/checksums.txt", nil)
	if _, err := guard.RoundTrip(request); err == nil || guard.blocked.Load() != 1 {
		t.Fatal("external request not blocked")
	}
	// base is nil: any attempt to dispatch would panic, so this proves no transport I/O.
}
