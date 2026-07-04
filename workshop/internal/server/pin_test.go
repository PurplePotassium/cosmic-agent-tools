package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
)

// drainHasEvent consumes every event currently buffered on ch and reports
// whether any carried the given type. Publish is synchronous onto a buffered
// channel, so a pin route's warning is already readable once its response
// returns — no sleep needed.
func drainHasEvent(ch <-chan domain.Event, typ string) bool {
	found := false
	for {
		select {
		case ev := <-ch:
			if ev.Type == typ {
				found = true
			}
		default:
			return found
		}
	}
}

// postTaskPin POSTs a task carrying pin and returns the response status.
func postTaskPin(t *testing.T, s *Server, pin string) int {
	t.Helper()
	body := `{"title":"pinned","pin":` + pin + `}`
	req := httptest.NewRequest("POST", "http://127.0.0.1/api/v1/tasks", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:5555"
	req.Header.Set("X-Workshop-Token", s.token)
	rec := httptest.NewRecorder()
	s.handler().ServeHTTP(rec, req)
	return rec.Code
}

// TestPinWarnsUnknownModel pins the operator-feedback contract at the task-pin
// boundary: a pin whose model is off-family for its agent still lands (warn,
// never block, matching SetPipelineBundle) but publishes a non-blocking
// driver.model_unknown warning. A curated model — or a model with no explicit
// agent to check against — stays silent.
func TestPinWarnsUnknownModel(t *testing.T) {
	s, a := newTestServer(t)
	events, cancel := a.Bus.Subscribe()
	defer cancel()

	// claude agent + a non-claude model id: off-family, so it must warn yet
	// still add the task (2xx).
	if code := postTaskPin(t, s, `{"agent":"claude","model":"gpt-4o-mini"}`); code != 200 {
		t.Fatalf("off-family pin model must warn, not block: got %d", code)
	}
	if !drainHasEvent(events, "driver.model_unknown") {
		t.Fatal("an off-family pin model did not publish driver.model_unknown")
	}

	// A curated claude model id must NOT warn.
	if code := postTaskPin(t, s, `{"agent":"claude","model":"claude-opus-4-8"}`); code != 200 {
		t.Fatalf("curated pin model: got %d, want 200", code)
	}
	if drainHasEvent(events, "driver.model_unknown") {
		t.Fatal("a curated claude model must not warn")
	}

	// No explicit agent: the running pipeline's agent isn't known here, so the
	// check is a no-op — an unusual model id must not spuriously warn.
	if code := postTaskPin(t, s, `{"model":"gpt-4o-mini"}`); code != 200 {
		t.Fatalf("agentless pin: got %d, want 200", code)
	}
	if drainHasEvent(events, "driver.model_unknown") {
		t.Fatal("an agentless pin must not warn — no agent family to check against")
	}
}

// TestPinBadAgentBlocksWithoutWarning keeps the block-vs-warn split honest: an
// unknown agent is still a hard 400 (it would burn retry attempts), and a
// rejected pin never emits the model warning.
func TestPinBadAgentBlocksWithoutWarning(t *testing.T) {
	s, a := newTestServer(t)
	events, cancel := a.Bus.Subscribe()
	defer cancel()

	if code := postTaskPin(t, s, `{"agent":"bogus-agent","model":"gpt-4o-mini"}`); code != 400 {
		t.Fatalf("unknown pin agent must block: got %d, want 400", code)
	}
	if drainHasEvent(events, "driver.model_unknown") {
		t.Fatal("a blocked pin must not publish a model warning")
	}
}
