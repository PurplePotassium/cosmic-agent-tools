package server

import (
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/app"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/bus"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/config"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/store"
)

// newTestServer builds a Server around a throwaway store — no git repo
// needed since the routes under test (halt, pause-after) never touch RepoDir.
func newTestServer(t *testing.T) (*Server, *app.App) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "workshop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	a := app.New("", t.TempDir(), &config.Result{Config: config.Default()}, st, bus.New(st))
	s := New(a, func() {}, func() {})
	return s, a
}

func TestServerHaltRequiresToken(t *testing.T) {
	s, _ := newTestServer(t)
	halted := make(chan struct{}, 1)
	s.OnHalt = func() { halted <- struct{}{} }

	req := httptest.NewRequest("POST", "http://127.0.0.1/api/v1/server/halt", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	s.handler().ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("unauthorized halt: got %d, want 403", rec.Code)
	}
	select {
	case <-halted:
		t.Fatal("OnHalt must not fire without a valid token")
	case <-time.After(50 * time.Millisecond):
	}

	req = httptest.NewRequest("POST", "http://127.0.0.1/api/v1/server/halt", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	req.Header.Set("X-Workshop-Token", s.token)
	rec = httptest.NewRecorder()
	s.handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("authorized halt: got %d, want 200", rec.Code)
	}
	select {
	case <-halted:
	case <-time.After(2 * time.Second):
		t.Fatal("OnHalt must fire for an authorized request")
	}
}

func TestServerPauseAfterHaltsEveryEnabledPipeline(t *testing.T) {
	s, a := newTestServer(t)

	req := httptest.NewRequest("POST", "http://127.0.0.1/api/v1/server/pause-after", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	req.Header.Set("X-Workshop-Token", s.token)
	rec := httptest.NewRecorder()
	s.handler().ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("pause-after: got %d, want 200: %s", rec.Code, rec.Body.String())
	}

	for _, p := range a.EnabledPipelines() {
		reason, err := a.Store.HaltedReason(req.Context(), p.Name)
		if err != nil {
			t.Fatal(err)
		}
		if reason != "operator" {
			t.Fatalf("pipeline %s halted reason = %q, want %q", p.Name, reason, "operator")
		}
	}
}

func TestServerPauseAfterRequiresToken(t *testing.T) {
	s, _ := newTestServer(t)
	req := httptest.NewRequest("POST", "http://127.0.0.1/api/v1/server/pause-after", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	s.handler().ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("unauthorized pause-after: got %d, want 403", rec.Code)
	}
}
