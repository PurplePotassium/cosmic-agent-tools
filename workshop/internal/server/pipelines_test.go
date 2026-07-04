package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/config"
)

// newTestServerForPipelines is newTestServer plus a real RepoDir and a
// sandboxed WORKSHOP_STATE_DIR: AddPipeline/DeletePipeline persist to
// config.OverridesFile(a.RepoDir), which is computed from the real state
// root unless overridden — without this a test would write into the
// operator's actual Workshop state directory.
func newTestServerForPipelines(t *testing.T) *Server {
	t.Helper()
	t.Setenv("WORKSHOP_STATE_DIR", t.TempDir())
	s, a := newTestServer(t)
	a.RepoDir = t.TempDir()
	return s
}

func doPipelineReq(t *testing.T, s *Server, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, "http://127.0.0.1"+path, reader)
	req.RemoteAddr = "127.0.0.1:5555"
	if token != "" {
		req.Header.Set("X-Workshop-Token", token)
	}
	rec := httptest.NewRecorder()
	s.handler().ServeHTTP(rec, req)
	return rec
}

func TestPostPipelineAddsLaneAlongsideImplicitMain(t *testing.T) {
	s := newTestServerForPipelines(t)

	rec := doPipelineReq(t, s, "POST", "/api/v1/pipelines", s.token, `{"name":"art","agent":"agy","model":"gemini-3-flash"}`)
	if rec.Code != 200 {
		t.Fatalf("add pipeline: got %d, body=%s", rec.Code, rec.Body.String())
	}
	pl := s.App.Res().Config.ResolvedPipelines()
	names := map[string]bool{}
	for _, p := range pl {
		names[p.Name] = true
	}
	if !names[config.DefaultPipelineName] || !names["art"] {
		t.Fatalf("pipelines after add = %+v, want main+art", pl)
	}
}

// TestPostPipelineWarnsNeedsRestart pins the operator-feedback contract for the
// silent-idle trap: a lane added through the dashboard only edits config, so the
// already-running engine won't back it until it relaunches. A successful add
// must publish pipeline.needs_restart (the dashboard's "halt to activate" hint);
// a rejected add must stay silent.
func TestPostPipelineWarnsNeedsRestart(t *testing.T) {
	s := newTestServerForPipelines(t)
	events, cancel := s.App.Bus.Subscribe()
	defer cancel()

	if rec := doPipelineReq(t, s, "POST", "/api/v1/pipelines", s.token, `{"name":"art"}`); rec.Code != 200 {
		t.Fatalf("add: got %d, body=%s", rec.Code, rec.Body.String())
	}
	if !drainHasEvent(events, "pipeline.needs_restart") {
		t.Fatal("a successful add did not publish pipeline.needs_restart")
	}

	// A rejected add mutates nothing, so it must not warn about a restart.
	if rec := doPipelineReq(t, s, "POST", "/api/v1/pipelines", s.token, `{"name":"bad name!"}`); rec.Code != 400 {
		t.Fatalf("bad name: got %d, want 400, body=%s", rec.Code, rec.Body.String())
	}
	if drainHasEvent(events, "pipeline.needs_restart") {
		t.Fatal("a rejected add must not publish pipeline.needs_restart")
	}
}

func TestPostPipelineRequiresToken(t *testing.T) {
	s := newTestServerForPipelines(t)
	rec := doPipelineReq(t, s, "POST", "/api/v1/pipelines", "", `{"name":"art"}`)
	if rec.Code != 403 {
		t.Fatalf("unauthorized add: got %d, want 403", rec.Code)
	}
	if pl := s.App.Res().Config.ResolvedPipelines(); len(pl) != 1 {
		t.Fatalf("unauthorized add must not mutate config, got %+v", pl)
	}
}

func TestPostPipelineRejectsBadName(t *testing.T) {
	s := newTestServerForPipelines(t)
	rec := doPipelineReq(t, s, "POST", "/api/v1/pipelines", s.token, `{"name":"bad name!"}`)
	if rec.Code != 400 {
		t.Fatalf("bad name: got %d, want 400, body=%s", rec.Code, rec.Body.String())
	}
}

func TestDeletePipelineRejectsMain(t *testing.T) {
	s := newTestServerForPipelines(t)
	rec := doPipelineReq(t, s, "DELETE", "/api/v1/pipelines/"+config.DefaultPipelineName, s.token, "")
	if rec.Code != 400 {
		t.Fatalf("delete main: got %d, want 400, body=%s", rec.Code, rec.Body.String())
	}
}

func TestAddThenDeletePipelineRoundTrip(t *testing.T) {
	s := newTestServerForPipelines(t)

	if rec := doPipelineReq(t, s, "POST", "/api/v1/pipelines", s.token, `{"name":"art"}`); rec.Code != 200 {
		t.Fatalf("add: got %d, body=%s", rec.Code, rec.Body.String())
	}
	if rec := doPipelineReq(t, s, "DELETE", "/api/v1/pipelines/art", s.token, ""); rec.Code != 200 {
		t.Fatalf("delete: got %d, body=%s", rec.Code, rec.Body.String())
	}
	pl := s.App.Res().Config.ResolvedPipelines()
	if len(pl) != 1 || pl[0].Name != config.DefaultPipelineName {
		t.Fatalf("pipelines after add+delete = %+v, want just main", pl)
	}
}
