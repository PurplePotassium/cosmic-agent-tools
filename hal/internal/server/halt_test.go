package server

import (
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/app"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/bus"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/config"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/store"
)

// newTestServer builds a Server around a throwaway store — no git repo
// needed since the routes under test never touch RepoDir.
func newTestServer(t *testing.T) (*Server, *app.App) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "hal.db"))
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
	// ServeHTTP has already returned: the token guard rejects with 403 before
	// the handler body ever spawns `go OnHalt()`, so a non-blocking check is
	// deterministic — nothing is in flight that could still fire OnHalt.
	select {
	case <-halted:
		t.Fatal("OnHalt must not fire without a valid token")
	default:
	}

	req = httptest.NewRequest("POST", "http://127.0.0.1/api/v1/server/halt", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	req.Header.Set("X-Hal-Token", s.token)
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

