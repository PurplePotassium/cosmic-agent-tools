package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// artReq runs one authorized request against the test server and returns the
// recorder.
func artReq(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rd *strings.Reader
	if body == "" {
		rd = strings.NewReader("")
	} else {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "http://127.0.0.1"+path, rd)
	req.RemoteAddr = "127.0.0.1:5555"
	req.Header.Set("X-Hal-Token", s.token)
	rec := httptest.NewRecorder()
	s.handler().ServeHTTP(rec, req)
	return rec
}

type artResp struct {
	Removers   []string `json:"removers"`
	Keyers     []string `json:"keyers"`
	Remover    string   `json:"remover"`
	Override   []string `json:"override"`
	Configured []string `json:"configured"`
}

func decodeArt(t *testing.T, rec *httptest.ResponseRecorder) artResp {
	t.Helper()
	var a artResp
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil {
		t.Fatalf("bad art response %q: %v", rec.Body.String(), err)
	}
	return a
}

func TestArtKeyersRoundTrip(t *testing.T) {
	s, _ := newTestServer(t)

	// Invalid and duplicate keyers are rejected at the API boundary.
	if rec := artReq(t, s, "PUT", "/api/v1/art/keyers", `{"keyers":["photoshop"]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown keyer: got %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if rec := artReq(t, s, "PUT", "/api/v1/art/keyers", `{"keyers":["ffmpeg","ffmpeg"]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("duplicate keyer: got %d, want 400: %s", rec.Code, rec.Body.String())
	}

	// A valid ordered list becomes the live override, primary first.
	rec := artReq(t, s, "PUT", "/api/v1/art/keyers", `{"keyers":["corridorkey","ffmpeg"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("set keyers: got %d: %s", rec.Code, rec.Body.String())
	}
	a := decodeArt(t, rec)
	if len(a.Keyers) != 2 || a.Keyers[0] != "corridorkey" || a.Keyers[1] != "ffmpeg" || a.Remover != "corridorkey" {
		t.Fatalf("after set: keyers=%v remover=%q; want [corridorkey ffmpeg] / corridorkey", a.Keyers, a.Remover)
	}
	if len(a.Override) != 2 {
		t.Fatalf("override = %v; want the applied list", a.Override)
	}

	// GET reflects the same view.
	a = decodeArt(t, artReq(t, s, "GET", "/api/v1/art", ""))
	if len(a.Keyers) != 2 || a.Keyers[0] != "corridorkey" {
		t.Fatalf("GET after set: keyers=%v", a.Keyers)
	}

	// The legacy single-keyer route still works and replaces the list.
	a = decodeArt(t, artReq(t, s, "PUT", "/api/v1/art/remover", `{"remover":"corridorkey"}`))
	if len(a.Keyers) != 1 || a.Keyers[0] != "corridorkey" {
		t.Fatalf("legacy route: keyers=%v; want [corridorkey]", a.Keyers)
	}

	// An empty list clears the override back to config.
	a = decodeArt(t, artReq(t, s, "PUT", "/api/v1/art/keyers", `{"keyers":[]}`))
	if a.Override != nil {
		t.Fatalf("override after clear = %v; want none", a.Override)
	}
	if len(a.Keyers) != 1 || a.Keyers[0] != a.Configured[0] {
		t.Fatalf("after clear: keyers=%v configured=%v; want config value", a.Keyers, a.Configured)
	}
}

func TestEnvEndpoint(t *testing.T) {
	// Deterministic tool discovery: the driver override envs accept any
	// executable file, and the probes' --version/--help spawns of a Go test
	// binary fail fast on unknown flags (version just comes back empty).
	t.Setenv("HAL_CLAUDE_BIN", os.Args[0])
	t.Setenv("HAL_AGY_BIN", os.Args[0])
	s, a := newTestServer(t)

	// Reads are token-gated.
	noToken := httptest.NewRequest("GET", "http://127.0.0.1/api/v1/env", nil)
	noToken.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	s.handler().ServeHTTP(rec, noToken)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("env without token: got %d, want 403", rec.Code)
	}

	rec = artReq(t, s, "GET", "/api/v1/env", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("env: got %d: %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Tools []struct {
			Name    string `json:"name"`
			Present bool   `json:"present"`
		} `json:"tools"`
		Paths struct {
			StateDir string `json:"stateDir"`
		} `json:"paths"`
		Export struct {
			Enabled bool `json:"enabled"`
		} `json:"export"`
		Server *struct {
			Version string `json:"version"`
			PID     int    `json:"pid"`
		} `json:"server"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("bad env response %q: %v", rec.Body.String(), err)
	}
	got := map[string]bool{}
	for _, tool := range env.Tools {
		got[tool.Name] = tool.Present
	}
	for _, name := range []string{"claude", "agy", "ffmpeg", "corridorkey", "git"} {
		if _, ok := got[name]; !ok {
			t.Errorf("env tools missing %q (have %v)", name, got)
		}
	}
	if !got["claude"] || !got["agy"] {
		t.Errorf("claude/agy should be present via HAL_*_BIN overrides: %v", got)
	}
	if env.Paths.StateDir != a.StateDir {
		t.Errorf("paths.stateDir = %q; want %q", env.Paths.StateDir, a.StateDir)
	}
	if env.Export.Enabled {
		t.Error("export should be disabled by default")
	}
	if env.Server == nil || env.Server.Version != "dev" || env.Server.PID != os.Getpid() {
		t.Errorf("server info = %+v; want dev version and this pid", env.Server)
	}
}
