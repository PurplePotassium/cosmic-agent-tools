package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/app"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/bus"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/config"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/domain"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/driver"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/statedir"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/store"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/turns"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/workflow"
)

// wfStubDriver satisfies driver.Driver for the manager; never spawned.
type wfStubDriver struct{}

func (wfStubDriver) Name() string { return "stub" }
func (wfStubDriver) Probe(context.Context) (driver.Capabilities, error) {
	return driver.Capabilities{Interactive: true}, nil
}
func (wfStubDriver) Plan(driver.InvokeSpec) (driver.ExecPlan, error) {
	return driver.ExecPlan{}, fmt.Errorf("stub: never planned")
}

// wfStubRunner: the first `block` turns wait for cancellation; every turn
// after that instantly settles as done/asking.
type wfStubRunner struct {
	block chan struct{} // closed = stop blocking new turns
}

func (r *wfStubRunner) Run(ctx context.Context, _ driver.Driver, spec turns.TurnSpec, _ turns.Sink) (turns.TurnResult, error) {
	sessionID := spec.Resume
	if sessionID == "" {
		sessionID = spec.SessionID
	}
	select {
	case <-r.block:
	case <-ctx.Done():
		return turns.TurnResult{State: turns.TurnInterrupted, SessionID: sessionID, ExitCode: -1}, nil
	}
	for _, kv := range spec.ExtraEnv {
		if path, ok := strings.CutPrefix(kv, "HAL_WORKFLOW_STATUS_FILE="); ok {
			_ = statedir.WriteJSON(path, domain.WorkflowStatusFile{Phase: domain.PhaseAsking, Note: "?"})
		}
	}
	return turns.TurnResult{
		State: turns.TurnDone, SessionID: sessionID, FinalText: "what next?",
		Usage: &driver.TurnUsage{ResultText: "what next?", NumTurns: 1},
	}, nil
}

type wfFixture struct {
	s      *Server
	a      *app.App
	runner *wfStubRunner
}

func newWorkflowTestServer(t *testing.T) *wfFixture {
	t.Helper()
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.name", "t"}, {"config", "user.email", "t@t"},
		{"commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "hal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	b := bus.New(st)
	a := app.New(repo, t.TempDir(), &config.Result{Config: config.Default()}, st, b)
	runner := &wfStubRunner{block: make(chan struct{})}
	mgr := workflow.New(st, b, wfStubDriver{}, runner, workflow.Config{
		RepoDir: repo, StateDir: t.TempDir(), LogDir: t.TempDir(),
	})
	a.SetWorkflowManager(mgr)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = mgr.Run(ctx) }()
	// Wait for the manager to accept work.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := a.Store.ListWorkflows(ctx, true); err == nil {
			break
		}
	}
	s := New(a, func() {}, func() {})
	return &wfFixture{s: s, a: a, runner: runner}
}

func (f *wfFixture) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:5555"
	req.Header.Set("X-Hal-Token", f.s.Token())
	rec := httptest.NewRecorder()
	f.s.handler().ServeHTTP(rec, req)
	return rec
}

func (f *wfFixture) waitWorkflowStatus(t *testing.T, id string, want domain.WorkflowStatus) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		wf, err := f.a.Store.GetWorkflow(context.Background(), id)
		if err == nil && wf.Status == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", want)
}

func TestWorkflowEndpointsRequireToken(t *testing.T) {
	f := newWorkflowTestServer(t)
	req := httptest.NewRequest("POST", "http://127.0.0.1/api/v1/workflows", strings.NewReader(`{"title":"x"}`))
	req.RemoteAddr = "127.0.0.1:5555"
	rec := httptest.NewRecorder()
	f.s.handler().ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("tokenless create: %d, want 403", rec.Code)
	}
	req = httptest.NewRequest("GET", "http://127.0.0.1/api/v1/workflows", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	rec = httptest.NewRecorder()
	f.s.handler().ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatalf("tokenless list: %d, want 403", rec.Code)
	}
}

func TestWorkflowLifecycleOverHTTP(t *testing.T) {
	f := newWorkflowTestServer(t)

	// Create: the first turn blocks (the stub), so the workflow shows
	// turn-running.
	rec := f.do(t, "POST", "/api/v1/workflows", `{"title":"boss fight","text":"make the boss harder"}`)
	if rec.Code != 200 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var wf domain.Workflow
	if err := json.Unmarshal(rec.Body.Bytes(), &wf); err != nil {
		t.Fatal(err)
	}
	f.waitWorkflowStatus(t, wf.ID, domain.WorkflowTurnRunning)

	// A chat message during a turn without interrupt is a 409.
	rec = f.do(t, "POST", "/api/v1/workflows/"+wf.ID+"/messages", `{"text":"hello"}`)
	if rec.Code != 409 {
		t.Fatalf("message during turn: %d, want 409", rec.Code)
	}
	// With interrupt it kills the turn and queues the message.
	close(f.runner.block) // let follow-up turns settle instantly
	rec = f.do(t, "POST", "/api/v1/workflows/"+wf.ID+"/messages", `{"text":"different direction","interrupt":true}`)
	if rec.Code != 200 {
		t.Fatalf("interject: %d %s", rec.Code, rec.Body)
	}
	f.waitWorkflowStatus(t, wf.ID, domain.WorkflowAwaitingUser)

	// List + detail render the six-stage ladder.
	rec = f.do(t, "GET", "/api/v1/workflows", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), wf.ID) {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	rec = f.do(t, "GET", "/api/v1/workflows/"+wf.ID, "")
	if rec.Code != 200 {
		t.Fatalf("detail: %d", rec.Code)
	}
	var detail app.WorkflowDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Stages) != 6 || detail.Stages[0].Status != domain.StageActive {
		t.Fatalf("detail stages: %+v", detail.Stages)
	}

	// Messages replay.
	rec = f.do(t, "GET", "/api/v1/workflows/"+wf.ID+"/messages", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "different direction") {
		t.Fatalf("messages: %d %s", rec.Code, rec.Body)
	}

	// Approving a stage whose artifact doesn't exist is a 409.
	rec = f.do(t, "POST", "/api/v1/workflows/"+wf.ID+"/approve", `{"stage":"refine"}`)
	if rec.Code != 409 {
		t.Fatalf("approve without artifact: %d, want 409", rec.Code)
	}
	// Approving the WRONG stage is a 409 too.
	rec = f.do(t, "POST", "/api/v1/workflows/"+wf.ID+"/approve", `{"stage":"design"}`)
	if rec.Code != 409 {
		t.Fatalf("approve wrong stage: %d, want 409", rec.Code)
	}

	// Artifact editor round-trip: PUT (creates), GET, conflicting PUT.
	rec = f.do(t, "PUT", "/api/v1/workflows/"+wf.ID+"/artifacts/refine", `{"content":"# Q\nhand-written"}`)
	if rec.Code != 200 {
		t.Fatalf("artifact put: %d %s", rec.Code, rec.Body)
	}
	var art app.WorkflowArtifact
	_ = json.Unmarshal(rec.Body.Bytes(), &art)
	rec = f.do(t, "GET", "/api/v1/workflows/"+wf.ID+"/artifacts/refine", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "hand-written") {
		t.Fatalf("artifact get: %d %s", rec.Code, rec.Body)
	}
	rec = f.do(t, "PUT", "/api/v1/workflows/"+wf.ID+"/artifacts/refine", `{"content":"clobber","baseHash":"deadbeef00000000"}`)
	if rec.Code != 409 {
		t.Fatalf("stale-hash put: %d, want 409", rec.Code)
	}
	rec = f.do(t, "PUT", "/api/v1/workflows/"+wf.ID+"/artifacts/refine",
		fmt.Sprintf(`{"content":"# Q v2","baseHash":%q}`, art.Hash))
	if rec.Code != 200 {
		t.Fatalf("fresh-hash put: %d %s", rec.Code, rec.Body)
	}

	// With the artifact on disk, approve works and advances the stage.
	rec = f.do(t, "POST", "/api/v1/workflows/"+wf.ID+"/approve", `{"stage":"refine","note":"ok"}`)
	if rec.Code != 200 {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body)
	}
	f.waitWorkflowStatus(t, wf.ID, domain.WorkflowAwaitingUser) // research opened + stub turn settled

	// Bad stage names are 400s.
	rec = f.do(t, "GET", "/api/v1/workflows/"+wf.ID+"/artifacts/nonsense", "")
	if rec.Code != 400 {
		t.Fatalf("bad stage: %d, want 400", rec.Code)
	}
	// Unknown workflow is a 404.
	rec = f.do(t, "GET", "/api/v1/workflows/nope", "")
	if rec.Code != 404 {
		t.Fatalf("missing workflow: %d, want 404", rec.Code)
	}

	// Abandon terminates.
	rec = f.do(t, "POST", "/api/v1/workflows/"+wf.ID+"/abandon", "")
	if rec.Code != 200 {
		t.Fatalf("abandon: %d %s", rec.Code, rec.Body)
	}
	f.waitWorkflowStatus(t, wf.ID, domain.WorkflowAbandoned)
}

// The intake validate checkbox rides the create body: omitted means the
// default (validate runs); only an explicit false turns it off.
func TestCreateWorkflowValidateFlag(t *testing.T) {
	f := newWorkflowTestServer(t)
	close(f.runner.block) // opening turns settle instantly

	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"omitted", `{"title":"a","text":"x"}`, false},
		{"checked", `{"title":"b","text":"x","validate":true}`, false},
		{"unchecked", `{"title":"c","text":"x","validate":false}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.do(t, "POST", "/api/v1/workflows", tc.body)
			if rec.Code != 200 {
				t.Fatalf("create: %d %s", rec.Code, rec.Body)
			}
			var wf domain.Workflow
			if err := json.Unmarshal(rec.Body.Bytes(), &wf); err != nil {
				t.Fatal(err)
			}
			if wf.SkipValidate != tc.want {
				t.Fatalf("SkipValidate = %v, want %v", wf.SkipValidate, tc.want)
			}
			// And it survives the round trip through the store.
			got, err := f.a.Store.GetWorkflow(context.Background(), wf.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.SkipValidate != tc.want {
				t.Fatalf("persisted SkipValidate = %v, want %v", got.SkipValidate, tc.want)
			}
		})
	}
}
