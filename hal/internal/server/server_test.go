package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// guardReq runs one request through guardLoopback and returns the status.
func guardReq(t *testing.T, remoteAddr, host, origin string) int {
	t.Helper()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest("GET", "http://"+host+"/api/v1/status", nil)
	req.RemoteAddr = remoteAddr
	req.Host = host
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	guardLoopback(inner).ServeHTTP(rec, req)
	return rec.Code
}

func TestGuardLoopbackRejects(t *testing.T) {
	for _, tc := range []struct {
		name, remote, host, origin string
	}{
		{"non-loopback remote", "10.0.0.9:5555", "127.0.0.1:4455", ""},
		// DNS rebinding: loopback RemoteAddr, attacker's Host, no Origin.
		{"rebound host", "127.0.0.1:5555", "evil.example:4455", ""},
		{"rebound host with port", "127.0.0.1:5555", "localhost.evil.com:4455", ""},
		// Substring tricks that beat the old strings.Contains check.
		{"origin substring localhost", "127.0.0.1:5555", "127.0.0.1:4455", "http://localhost.evil.com"},
		{"origin substring ip", "127.0.0.1:5555", "127.0.0.1:4455", "http://127.0.0.1.evil.com"},
		{"origin in path", "127.0.0.1:5555", "127.0.0.1:4455", "http://evil.com/localhost"},
		{"plain cross-origin", "127.0.0.1:5555", "127.0.0.1:4455", "https://evil.com"},
		{"null origin", "127.0.0.1:5555", "127.0.0.1:4455", "null"},
		{"garbage origin", "127.0.0.1:5555", "127.0.0.1:4455", "::not a url::"},
	} {
		if code := guardReq(t, tc.remote, tc.host, tc.origin); code != http.StatusForbidden {
			t.Errorf("%s: got %d, want 403", tc.name, code)
		}
	}
}

func TestTokenHeaderOnly(t *testing.T) {
	s := &Server{token: "sekrit"}
	ok := httptest.NewRequest("POST", "http://127.0.0.1/api/v1/tasks", nil)
	ok.Header.Set("X-Hal-Token", "sekrit")
	if !s.authorized(ok) {
		t.Error("header token must authorize")
	}
	// The query channel is a leak vector (history, logs, Referer) and must
	// stay closed.
	viaQuery := httptest.NewRequest("POST", "http://127.0.0.1/api/v1/tasks?token=sekrit", nil)
	if s.authorized(viaQuery) {
		t.Error("query-param token must NOT authorize")
	}
	if s.authorized(httptest.NewRequest("POST", "http://127.0.0.1/api/v1/tasks", nil)) {
		t.Error("missing token must not authorize")
	}
}

func TestPostAttachmentRequiresToken(t *testing.T) {
	s, _ := newTestServer(t)
	req := httptest.NewRequest("POST", "http://127.0.0.1/api/v1/tasks/attachments", strings.NewReader(`{}`))
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	s.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unauthorized attachment upload: got %d, want 403", rec.Code)
	}
}

func TestPostAttachmentRejectsNonImageDataURL(t *testing.T) {
	s, _ := newTestServer(t)
	body := `{"name":"evil.txt","dataUrl":"data:text/plain;base64,aGk="}`
	req := httptest.NewRequest("POST", "http://127.0.0.1/api/v1/tasks/attachments", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:5555"
	req.Header.Set("X-Hal-Token", s.token)
	rec := httptest.NewRecorder()
	s.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("non-image data URL: got %d, want 400", rec.Code)
	}
}

// Read routes leak repo intelligence, so they're token-gated like mutations:
// no token → 403, header token → 200, and (for the SSE/img routes that can't
// set a header) a ?token= query parameter → 200.
func TestReadRoutesRequireToken(t *testing.T) {
	s, _ := newTestServer(t)
	do := func(url string, hdr bool) int {
		req := httptest.NewRequest("GET", url, nil)
		req.RemoteAddr = "127.0.0.1:5555"
		if hdr {
			req.Header.Set("X-Hal-Token", s.token)
		}
		rec := httptest.NewRecorder()
		s.handler().ServeHTTP(rec, req)
		return rec.Code
	}
	if code := do("http://127.0.0.1/api/v1/tasks", false); code != http.StatusForbidden {
		t.Fatalf("GET /tasks without token: got %d, want 403", code)
	}
	if code := do("http://127.0.0.1/api/v1/tasks", true); code != http.StatusOK {
		t.Fatalf("GET /tasks with header token: got %d, want 200", code)
	}
	if code := do("http://127.0.0.1/api/v1/tasks?token="+s.token, false); code != http.StatusOK {
		t.Fatalf("GET /tasks with query token: got %d, want 200", code)
	}
	// The static SPA shell stays ungated — the browser can't send the
	// fragment token on the initial navigation.
	if code := do("http://127.0.0.1/", false); code != http.StatusOK {
		t.Fatalf("GET / (SPA) without token: got %d, want 200", code)
	}
}

// authorizedRead accepts a query-param token (SSE/img); authorized (writes)
// still does not.
func TestAuthorizedReadAcceptsQueryToken(t *testing.T) {
	s := &Server{token: "sekrit"}
	q := httptest.NewRequest("GET", "http://127.0.0.1/api/v1/events?token=sekrit", nil)
	if !s.authorizedRead(q) {
		t.Error("read route must accept a query-param token")
	}
	if s.authorized(q) {
		t.Error("write auth must still reject a query-param token")
	}
}

// TestGetAttachmentRejectsPathTraversal calls the handler directly with a
// crafted PathValue (bypassing whatever ServeMux's own path cleaning would
// do) to pin down that getAttachment's own dir check refuses to escape
// <StateDir>/attachments, regardless of routing-layer behavior.
func TestGetAttachmentRejectsPathTraversal(t *testing.T) {
	s, a := newTestServer(t)
	if err := os.WriteFile(filepath.Join(a.StateDir, "secret.json"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "http://127.0.0.1/api/v1/attachments/x", nil)
	req.SetPathValue("name", "../secret.json")
	rec := httptest.NewRecorder()
	s.getAttachment(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("path traversal attempt: got %d, want 404", rec.Code)
	}
}
