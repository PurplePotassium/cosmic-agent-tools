package server

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// TestPostPipelineRelaunchesEngine pins the auto-activation contract: a
// successful add fires OnHalt (the same cancel+relaunch the halt button uses)
// so RunHeadless rebuilds workers from current config and the new lane comes
// alive without a manual restart. A rejected add must not touch the engine.
func TestPostPipelineRelaunchesEngine(t *testing.T) {
	s := newTestServerForPipelines(t)
	halted := make(chan struct{}, 1)
	s.OnHalt = func() { halted <- struct{}{} }

	if rec := doPipelineReq(t, s, "POST", "/api/v1/pipelines", s.token, `{"name":"art"}`); rec.Code != 200 {
		t.Fatalf("add: got %d, body=%s", rec.Code, rec.Body.String())
	}
	select {
	case <-halted:
	case <-time.After(2 * time.Second):
		t.Fatal("a successful add did not relaunch the engine (OnHalt never fired)")
	}

	// A rejected add mutates nothing, so it must not relaunch the engine.
	if rec := doPipelineReq(t, s, "POST", "/api/v1/pipelines", s.token, `{"name":"bad name!"}`); rec.Code != 400 {
		t.Fatalf("bad name: got %d, want 400, body=%s", rec.Code, rec.Body.String())
	}
	select {
	case <-halted:
		t.Fatal("a rejected add must not relaunch the engine")
	case <-time.After(200 * time.Millisecond):
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

// TestDeletePipelineRelaunchesEngine pins the delete-side of the config/engine
// split: removing a lane only edits config, so without a relaunch the deleted
// lane's worker keeps running with no dashboard card to control it. A
// successful delete must publish pipeline.needs_restart (action=removed) and
// fire OnHalt, exactly like postPipeline; a rejected delete must do neither.
func TestDeletePipelineRelaunchesEngine(t *testing.T) {
	s := newTestServerForPipelines(t)
	halted := make(chan struct{}, 1)
	s.OnHalt = func() { halted <- struct{}{} }
	events, cancel := s.App.Bus.Subscribe()
	defer cancel()

	if rec := doPipelineReq(t, s, "POST", "/api/v1/pipelines", s.token, `{"name":"art"}`); rec.Code != 200 {
		t.Fatalf("add: got %d, body=%s", rec.Code, rec.Body.String())
	}
	<-halted // consume the add's relaunch
	for len(events) > 0 {
		<-events // drain the add's events
	}

	if rec := doPipelineReq(t, s, "DELETE", "/api/v1/pipelines/art", s.token, ""); rec.Code != 200 {
		t.Fatalf("delete: got %d, body=%s", rec.Code, rec.Body.String())
	}
	found := false
	for len(events) > 0 {
		ev := <-events
		if ev.Type == "pipeline.needs_restart" {
			found = true
			if ev.Pipeline != "art" {
				t.Fatalf("needs_restart pipeline = %q, want %q", ev.Pipeline, "art")
			}
			if action, _ := ev.Payload["action"].(string); action != "removed" {
				t.Fatalf("needs_restart action = %q, want %q", action, "removed")
			}
		}
	}
	if !found {
		t.Fatal("a successful delete did not publish pipeline.needs_restart")
	}
	select {
	case <-halted:
	case <-time.After(2 * time.Second):
		t.Fatal("a successful delete did not relaunch the engine (OnHalt never fired)")
	}

	// A rejected delete mutates nothing, so it must neither warn nor relaunch.
	if rec := doPipelineReq(t, s, "DELETE", "/api/v1/pipelines/nope", s.token, ""); rec.Code != 400 {
		t.Fatalf("delete unknown: got %d, want 400, body=%s", rec.Code, rec.Body.String())
	}
	if drainHasEvent(events, "pipeline.needs_restart") {
		t.Fatal("a rejected delete must not publish pipeline.needs_restart")
	}
	select {
	case <-halted:
		t.Fatal("a rejected delete must not relaunch the engine")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestDeletePipelineRejectsMain(t *testing.T) {
	s := newTestServerForPipelines(t)
	rec := doPipelineReq(t, s, "DELETE", "/api/v1/pipelines/"+config.DefaultPipelineName, s.token, "")
	if rec.Code != 400 {
		t.Fatalf("delete main: got %d, want 400, body=%s", rec.Code, rec.Body.String())
	}
}

// TestPatchPipelinePersonality pins the dashboard's live personality-override
// endpoint: PATCH .../pipelines/{name} {"personality":...} mirrors bundle/mode
// — a valid selector reaches the store immediately (next-pass, no restart),
// an invalid one is rejected with 400 and leaves the store untouched, and ""
// clears an active override back to the configured selector.
func TestPatchPipelinePersonality(t *testing.T) {
	s := newTestServerForPipelines(t)
	ctx := context.Background()
	name := config.DefaultPipelineName

	if rec := doPipelineReq(t, s, "PATCH", "/api/v1/pipelines/"+name, s.token, `{"personality":"bogus-personality"}`); rec.Code != 400 {
		t.Fatalf("bogus personality: got %d, want 400, body=%s", rec.Code, rec.Body.String())
	}
	if got, err := s.App.Store.PipelinePersonality(ctx, name); err != nil || got != "" {
		t.Fatalf("rejected patch left an override behind: %q err=%v", got, err)
	}

	if rec := doPipelineReq(t, s, "PATCH", "/api/v1/pipelines/"+name, s.token, `{"personality":"random"}`); rec.Code != 200 {
		t.Fatalf("valid personality: got %d, body=%s", rec.Code, rec.Body.String())
	}
	if got, err := s.App.Store.PipelinePersonality(ctx, name); err != nil || got != "random" {
		t.Fatalf("stored personality = %q err=%v, want %q", got, err, "random")
	}

	if rec := doPipelineReq(t, s, "PATCH", "/api/v1/pipelines/"+name, s.token, `{"personality":""}`); rec.Code != 200 {
		t.Fatalf("clear personality: got %d, body=%s", rec.Code, rec.Body.String())
	}
	if got, err := s.App.Store.PipelinePersonality(ctx, name); err != nil || got != "" {
		t.Fatalf("clearing personality: stored = %q err=%v, want empty", got, err)
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
