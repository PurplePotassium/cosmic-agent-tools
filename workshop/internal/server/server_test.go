package server

import (
	"net/http"
	"net/http/httptest"
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

func TestGuardLoopbackAllowsLocal(t *testing.T) {
	for _, tc := range []struct{ remote, host, origin string }{
		{"127.0.0.1:5555", "127.0.0.1:4455", ""},
		{"127.0.0.1:5555", "localhost:4455", ""},
		{"[::1]:5555", "[::1]:4455", ""},
		{"127.0.0.1:5555", "127.0.0.1:4455", "http://127.0.0.1:4455"},
		{"127.0.0.1:5555", "127.0.0.1:4455", "http://localhost:4455"},
		{"127.0.0.1:5555", "localhost:4455", "http://localhost:9999"}, // other local port ok
	} {
		if code := guardReq(t, tc.remote, tc.host, tc.origin); code != http.StatusOK {
			t.Errorf("remote=%s host=%s origin=%q: got %d, want 200", tc.remote, tc.host, tc.origin, code)
		}
	}
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
	ok.Header.Set("X-Workshop-Token", "sekrit")
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
