package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shorty/shorty/internal/config"
	"github.com/shorty/shorty/internal/shortcode"
	"github.com/shorty/shorty/internal/store"
)

const testToken = "test-operator-token-0123456789"

func newTestServer(t *testing.T, modify func(*config.Config)) (*Server, *httptest.Server, *bytes.Buffer) {
	t.Helper()
	cfg := config.Default()
	cfg.DBPath = filepath.Join(t.TempDir(), "test.db")
	cfg.BearerToken = testToken
	cfg.RateLimitRPS = 100
	cfg.RateLimitBurst = 100
	if modify != nil {
		modify(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("invalid test config: %v", err)
	}

	st, err := store.Open(context.Background(), cfg.DBPath, cfg.CodeLength, cfg.Retention)
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	srv := New(cfg, st, logger)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	return srv, ts, &logBuf
}

func noRedirectClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func TestServer_Healthz(t *testing.T) {
	_, ts, _ := newTestServer(t, nil)
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if strings.TrimSpace(string(body)) != `{"status":"ok"}` {
		t.Fatalf("unexpected healthz body: %s", body)
	}
}

func TestServer_AuthorizationAndRedactedLogs(t *testing.T) {
	_, ts, logBuf := newTestServer(t, nil)

	// Missing auth -> 401
	resp, err := http.Post(ts.URL+"/api/v1/links", "application/json", strings.NewReader(`{"url":"https://example.com/secret?token=supersecret"}`))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("got status %d, want 401", resp.StatusCode)
	}

	// Wrong auth -> 401
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/links", strings.NewReader(`{"url":"https://example.com/secret?token=supersecret"}`))
	req.Header.Set("Authorization", "Bearer wrong-token")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("got status %d, want 401", resp2.StatusCode)
	}

	// Valid auth -> 201
	req3, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/links", strings.NewReader(`{"url":"https://example.com/secret?token=supersecret"}`))
	req3.Header.Set("Authorization", "Bearer "+testToken)
	resp3, err := http.DefaultClient.Do(req3)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp3.Body.Close()
	if resp3.StatusCode != http.StatusCreated {
		t.Fatalf("got status %d, want 201", resp3.StatusCode)
	}

	logs := logBuf.String()
	if strings.Contains(logs, testToken) {
		t.Fatal("logs leaked bearer token")
	}
	if strings.Contains(logs, "supersecret") {
		t.Fatal("logs leaked destination query string")
	}
}

func TestServer_RejectsMaliciousURLsAndOpenRedirects(t *testing.T) {
	_, ts, _ := newTestServer(t, func(c *config.Config) {
		c.Mode = config.ModePublic
	})

	badURLs := []string{
		"javascript:alert(1)",
		"data:text/html,<script>alert(1)</script>",
		"file:///etc/passwd",
		"https://user:pass@example.com/",
		"https://evil.com\\@example.com",
		"//evil.com",
		"https://example.com:99999/",
		"https://example.com/\r\nX-Injected: true",
	}

	for _, u := range badURLs {
		payload, _ := json.Marshal(map[string]string{"url": u})
		resp, err := http.Post(ts.URL+"/api/v1/links", "application/json", bytes.NewReader(payload))
		if err != nil {
			t.Fatalf("POST failed for %q: %v", u, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("URL %q got status %d, want 400", u, resp.StatusCode)
		}
	}

	// Create one valid link.
	resp, err := http.Post(ts.URL+"/api/v1/links", "application/json", strings.NewReader(`{"url":"https://example.com/safe"}`))
	if err != nil {
		t.Fatalf("POST valid failed: %v", err)
	}
	var created createResponse
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	resp.Body.Close()

	// Attempt open-redirect via query parameter on resolve endpoint: must ignore query params.
	client := noRedirectClient()
	rResp, err := client.Get(ts.URL + "/" + created.Code + "?url=https://evil.example&next=https://evil.example&redirect=https://evil.example")
	if err != nil {
		t.Fatalf("GET resolve failed: %v", err)
	}
	rResp.Body.Close()
	if rResp.StatusCode != http.StatusFound {
		t.Fatalf("got status %d, want 302", rResp.StatusCode)
	}
	if loc := rResp.Header.Get("Location"); loc != "https://example.com/safe" {
		t.Fatalf("Location header %q != stored destination", loc)
	}

	// Unknown code with query param -> 404, never redirects to query param.
	uResp, err := client.Get(ts.URL + "/NotExist123?url=https://evil.example")
	if err != nil {
		t.Fatalf("GET unknown failed: %v", err)
	}
	uResp.Body.Close()
	if uResp.StatusCode != http.StatusNotFound {
		t.Fatalf("got status %d, want 404", uResp.StatusCode)
	}
}

func TestServer_RateLimitAndOversizedBody(t *testing.T) {
	_, ts, _ := newTestServer(t, func(c *config.Config) {
		c.Mode = config.ModePublic
		c.RateLimitRPS = 0.5
		c.RateLimitBurst = 2
		c.MaxBodyBytes = 256
	})

	// Oversized body -> 413
	bigBody := `{"url":"https://example.com/` + strings.Repeat("a", 400) + `"}`
	resp, err := http.Post(ts.URL+"/api/v1/links", "application/json", strings.NewReader(bigBody))
	if err != nil {
		t.Fatalf("POST oversized failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body got %d, want 413", resp.StatusCode)
	}

	// First request counted against burst (2nd remaining), 3rd should be 429.
	resp2, _ := http.Post(ts.URL+"/api/v1/links", "application/json", strings.NewReader(`{"url":"https://example.com/1"}`))
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusCreated {
		t.Fatalf("2nd request got %d, want 201", resp2.StatusCode)
	}

	resp3, _ := http.Post(ts.URL+"/api/v1/links", "application/json", strings.NewReader(`{"url":"https://example.com/2"}`))
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("3rd request got %d, want 429", resp3.StatusCode)
	}
}

func TestServer_InterstitialAnd307AndDelete(t *testing.T) {
	_, ts, _ := newTestServer(t, func(c *config.Config) {
		c.Interstitial = true
		c.RedirectStatus = 307
	})

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/links", strings.NewReader(`{"url":"https://example.com/page?x=1"}`))
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	var created createResponse
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()

	// Interstitial renders 200 HTML with host and safe link.
	getResp, err := http.Get(ts.URL + "/" + created.Code)
	if err != nil {
		t.Fatalf("GET interstitial failed: %v", err)
	}
	body, _ := io.ReadAll(getResp.Body)
	getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("interstitial got %d, want 200", getResp.StatusCode)
	}
	if !strings.Contains(string(body), "example.com") || !strings.Contains(string(body), `rel="noopener noreferrer"`) {
		t.Fatalf("interstitial missing expected content: %s", body)
	}

	// Delete requires auth and removes link.
	delReq, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/v1/links/"+created.Code, nil)
	delReq.Header.Set("Authorization", "Bearer "+testToken)
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatalf("DELETE failed: %v", err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE got %d, want 204", delResp.StatusCode)
	}

	afterDel, _ := http.Get(ts.URL + "/" + created.Code)
	afterDel.Body.Close()
	if afterDel.StatusCode != http.StatusNotFound {
		t.Fatalf("GET after delete got %d, want 404", afterDel.StatusCode)
	}
}

func TestServer_RequestTimeout(t *testing.T) {
	srv, _, _ := newTestServer(t, func(c *config.Config) {
		c.RequestTimeout = 100 * time.Millisecond
	})

	// Verify timeout middleware sets a deadline on the request context.
	var hasDeadline bool
	wrapped := srv.timeoutMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hasDeadline = r.Context().Deadline()
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	wrapped.ServeHTTP(rec, req)

	if !hasDeadline {
		t.Fatal("expected request context to have deadline set by timeoutMiddleware")
	}
}

func FuzzRouting(f *testing.F) {
	f.Add("GET", "/healthz", "")
	f.Add("GET", "/AbCdEfGh123", "")
	f.Add("GET", "/AbCdEfGh123?url=https://evil.com", "")
	f.Add("GET", "/../../etc/passwd", "")
	f.Add("POST", "/api/v1/links", `{"url":"https://example.com"}`)
	f.Add("POST", "/api/v1/links", `{"url":"javascript:alert(1)"}`)
	f.Add("DELETE", "/api/v1/links/AbCdEfGh123", "")

	cfg := config.Default()
	cfg.Mode = config.ModePublic
	cfg.RateLimitRPS = 1000
	cfg.RateLimitBurst = 10000
	cfg.DBPath = filepath.Join(f.TempDir(), "fuzz.db")
	st, err := store.Open(context.Background(), cfg.DBPath, shortcode.DefaultLength, 0)
	if err != nil {
		f.Fatalf("store.Open failed: %v", err)
	}
	defer st.Close()
	srv := New(cfg, st, nil)
	h := srv.Handler()

	f.Fuzz(func(t *testing.T, method, path, body string) {
		if method != http.MethodGet && method != http.MethodPost && method != http.MethodDelete {
			method = http.MethodGet
		}
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		req, err := http.NewRequest(method, "http://shorty.test"+path, strings.NewReader(body))
		if err != nil {
			return
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if loc := rec.Header().Get("Location"); loc != "" {
			if !strings.HasPrefix(loc, "http://") && !strings.HasPrefix(loc, "https://") {
				t.Fatalf("unexpected Location header %q", loc)
			}
		}
	})
}
