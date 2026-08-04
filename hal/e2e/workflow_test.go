//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This file drives the interactive workflow engine end-to-end: the REAL
// binary's `up` boots the server + workflow Manager, workflow turns exec the
// binary back into the scripted fake agent (HAL_WORKFLOW_AGENT=fake +
// HAL_FAKE_BIN), and everything is asserted over the REST surface plus
// the resulting git history.

// envVal reads one KEY=value out of the rig's environment.
func envVal(r *rig, key string) string {
	for _, kv := range r.env {
		if strings.HasPrefix(kv, key+"=") {
			return strings.TrimPrefix(kv, key+"=")
		}
	}
	return ""
}

// startUp boots `hal up` on the rig and returns the API base URL, the
// session token, and a stop func. The caller's deferred stop kills the
// process tree via context cancel.
func startUp(t *testing.T, r *rig) (base, token string, stop func()) {
	t.Helper()
	stateRoot := envVal(r, "HAL_STATE_DIR")
	if stateRoot == "" {
		t.Fatal("rig env missing HAL_STATE_DIR")
	}
	ctx, cancel := context.WithCancel(context.Background())
	up := exec.CommandContext(ctx, exePath, "up", "--no-open", "--port", strconv.Itoa(freePort(t)))
	up.Dir = r.repo
	up.Env = r.env
	if err := up.Start(); err != nil {
		cancel()
		t.Fatalf("start up: %v", err)
	}
	si := waitServerJSON(t, stateRoot)
	return "http://127.0.0.1:" + strconv.Itoa(si.Port), si.Token, func() {
		cancel()
		_ = up.Wait()
	}
}

// wfReq is one authenticated JSON request; it returns status code + body.
func wfReq(t *testing.T, client *http.Client, method, url, token string, body any) (int, []byte) {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Hal-Token", token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.Bytes()
}

// mustReq asserts a 200 and unmarshals the response into out (nil = discard).
func mustReq(t *testing.T, client *http.Client, method, url, token string, body, out any) {
	t.Helper()
	code, raw := wfReq(t, client, method, url, token, body)
	if code != 200 {
		t.Fatalf("%s %s: code=%d body=%s", method, url, code, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s %s: bad json: %v\n%s", method, url, err, raw)
		}
	}
}

// REST view models — Go's default (capitalized) field names on the wire;
// unmarshal matches case-insensitively.
type wfRow struct {
	ID     string
	Title  string
	Stage  string
	Status string
	Error  string
}

type wfStageRow struct {
	Stage          string
	Status         string
	Artifact       string
	ArtifactExists bool `json:"artifactExists"`
}

type wfTurnRow struct {
	ID    int64
	N     int
	State string
}

type wfDetail struct {
	Workflow wfRow        `json:"workflow"`
	Stages   []wfStageRow `json:"stages"`
	Turns    []wfTurnRow  `json:"turns"`
}

type wfMsg struct {
	ID      int64
	Stage   string
	Role    string
	Kind    string
	Content string
}

// waitDetail polls the workflow detail until ok(detail) or the deadline.
func waitDetail(t *testing.T, client *http.Client, base, token, id string, timeout time.Duration, desc string, ok func(wfDetail) bool) wfDetail {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last wfDetail
	for time.Now().Before(deadline) {
		mustReq(t, client, "GET", base+"/api/v1/workflows/"+id, token, nil, &last)
		if ok(last) {
			return last
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; last: stage=%s status=%s error=%q turns=%d",
		desc, last.Workflow.Stage, last.Workflow.Status, last.Workflow.Error, len(last.Turns))
	return last
}

// fullRunScenario scripts one workflow through all six stages: refine asks a
// question first (turn 1), then every stage lands its artifact and reports
// ready. Raw string: the \n escapes belong to the JSON, not to Go.
const fullRunScenario = `{"behavior":"interactive","turns":[
{"deltas":["Let me think about the ask… "],"final":"Before I write the research question: what exactly should the widget do?","status":{"phase":"asking","note":"clarify scope"}},
{"final":"Refined. The research question artifact is written.","artifact":"# Research Question\n\nWhat is the smallest widget that satisfies the ask?\n","status":{"phase":"ready","note":"question ready"}},
{"final":"Research complete; artifact written.","artifact":"# Research\n\nFindings about the codebase.\n","status":{"phase":"ready","note":"research ready"}},
{"final":"Design written.","artifact":"# Design\n\nApproach A wins.\n","status":{"phase":"ready","note":"design ready"}},
{"final":"Plan written.","artifact":"# Plan\n\n- [ ] step one\n- [ ] step two\n","status":{"phase":"ready","note":"plan ready"}},
{"final":"Implementation done.","artifact":"# Implementation\n\n- [x] step one\n- [x] step two\n","status":{"phase":"ready","note":"implemented"}},
{"final":"Validation done.","artifact":"# Validation\n\nAll checks green.\n","status":{"phase":"ready","note":"validated"}}
]}`

// interactiveRig scaffolds a rig whose workflow turns run the scripted fake
// agent: the scenario file is rewritten with the interactive script and the
// turn counter gets its own directory.
func interactiveRig(t *testing.T, scenario string) *rig {
	t.Helper()
	r := newRig(t, `
[project]
name   = "e2e-workflow"
trunk  = "main"
verify = "git log -1 --oneline"
`)
	scenarioPath := envVal(r, "HAL_FAKE_SCENARIO")
	if scenarioPath == "" {
		t.Fatal("rig env missing HAL_FAKE_SCENARIO")
	}
	if err := os.WriteFile(scenarioPath, []byte(scenario), 0o644); err != nil {
		t.Fatal(err)
	}
	turnsDir := filepath.Join(filepath.Dir(scenarioPath), "turns")
	if err := os.MkdirAll(turnsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	r.env = append(r.env,
		"HAL_WORKFLOW_AGENT=fake",
		"HAL_FAKE_TURNS_DIR="+turnsDir,
	)
	return r
}

// TestWorkflowFullRun drives one workflow refine → validate → completed over
// REST: question turn, answer, ready+artifact per stage, approval per stage,
// then asserts the stage rows, the artifact files, the approval commits (with
// Hal-Workflow trailers), and the persisted message history.
func TestWorkflowFullRun(t *testing.T) {
	r := interactiveRig(t, fullRunScenario)
	base, token, stop := startUp(t, r)
	defer stop()
	client := &http.Client{Timeout: 10 * time.Second}

	var wf wfRow
	mustReq(t, client, "POST", base+"/api/v1/workflows", token,
		map[string]any{"title": "Widget", "text": "Build the widget.\n\nKeep it small."}, &wf)
	if wf.ID == "" || wf.Stage != "refine" {
		t.Fatalf("created workflow = %+v", wf)
	}

	// Turn 1: the refine agent asks a question → awaiting-user.
	waitDetail(t, client, base, token, wf.ID, 60*time.Second, "refine question (awaiting-user)", func(d wfDetail) bool {
		return d.Workflow.Status == "awaiting-user" && len(d.Turns) == 1
	})

	// The answer resumes the session; turn 2 writes the artifact and reports
	// ready → awaiting-approval.
	mustReq(t, client, "POST", base+"/api/v1/workflows/"+wf.ID+"/messages", token,
		map[string]any{"text": "It should frobnicate quietly."}, nil)
	d := waitDetail(t, client, base, token, wf.ID, 60*time.Second, "refine ready (awaiting-approval)", func(d wfDetail) bool {
		return d.Workflow.Status == "awaiting-approval" && d.Workflow.Stage == "refine"
	})
	for _, st := range d.Stages {
		if st.Stage == "refine" && !st.ArtifactExists {
			t.Fatalf("refine artifact missing from stage row: %+v", st)
		}
	}

	// A message during awaiting-approval is fine; approving mid-turn is not —
	// exercise the 409 by approving a stage that is not current.
	if code, _ := wfReq(t, client, "POST", base+"/api/v1/workflows/"+wf.ID+"/approve", token,
		map[string]any{"stage": "design"}); code != 409 {
		t.Fatalf("approve of a non-current stage: code=%d, want 409", code)
	}

	// Approve each stage; every next stage runs one ready+artifact turn.
	order := []string{"refine", "research", "design", "plan", "implement", "validate"}
	for i, stage := range order {
		mustReq(t, client, "POST", base+"/api/v1/workflows/"+wf.ID+"/approve", token,
			map[string]any{"stage": stage, "note": "looks good"}, nil)
		if i == len(order)-1 {
			waitDetail(t, client, base, token, wf.ID, 60*time.Second, "workflow completed", func(d wfDetail) bool {
				return d.Workflow.Status == "completed"
			})
			break
		}
		next := order[i+1]
		waitDetail(t, client, base, token, wf.ID, 60*time.Second, next+" ready (awaiting-approval)", func(d wfDetail) bool {
			return d.Workflow.Stage == next && d.Workflow.Status == "awaiting-approval"
		})
	}

	// Six approved stage rows.
	mustReq(t, client, "GET", base+"/api/v1/workflows/"+wf.ID, token, nil, &d)
	if len(d.Stages) != 6 {
		t.Fatalf("stage rows = %d, want 6", len(d.Stages))
	}
	for _, st := range d.Stages {
		if st.Status != "approved" {
			t.Fatalf("stage %s = %s, want approved", st.Stage, st.Status)
		}
	}

	// Artifact files on disk under .hal/workflows/<id>/.
	for _, name := range []string{
		"01-question.md", "02-research.md", "03-design.md",
		"04-plan.md", "05-implementation.md", "06-validation.md",
	} {
		p := filepath.Join(r.repo, ".hal", "workflows", wf.ID, name)
		if info, err := os.Stat(p); err != nil || info.Size() == 0 {
			t.Fatalf("artifact %s missing or empty: %v", p, err)
		}
	}

	// Approval commits with the Hal-Workflow trailer — one per stage.
	log := r.git("log", "--format=%B")
	if got := strings.Count(log, "Hal-Workflow: "+wf.ID); got < 6 {
		t.Fatalf("Hal-Workflow trailers = %d, want >= 6\n%s", got, log)
	}
	if !strings.Contains(log, "refine approved") || !strings.Contains(log, "validate approved") {
		t.Fatalf("approval commit subjects missing:\n%s", log)
	}

	// The message replay carries the whole conversation: the operator's
	// answer, assistant rows, the validate gate, and the approvals.
	var msgs []wfMsg
	mustReq(t, client, "GET", base+"/api/v1/workflows/"+wf.ID+"/messages?limit=500", token, nil, &msgs)
	var haveUser, haveAssistant, haveGate, haveApproval bool
	for _, m := range msgs {
		switch {
		case m.Role == "user" && strings.Contains(m.Content, "frobnicate"):
			haveUser = true
		case m.Role == "assistant" && strings.Contains(m.Content, "research question"):
			haveAssistant = true
		case m.Kind == "gate" && strings.Contains(m.Content, "VERIFY COMMAND"):
			haveGate = true
		case m.Kind == "approval":
			haveApproval = true
		}
	}
	if !haveUser || !haveAssistant || !haveGate || !haveApproval {
		t.Fatalf("message replay incomplete: user=%v assistant=%v gate=%v approval=%v (%d messages)",
			haveUser, haveAssistant, haveGate, haveApproval, len(msgs))
	}

	// The GET /workflows list surfaces the terminal row with ?all=1.
	var list []wfRow
	mustReq(t, client, "GET", base+"/api/v1/workflows?all=1", token, nil, &list)
	found := false
	for _, row := range list {
		if row.ID == wf.ID && row.Status == "completed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("completed workflow not in ?all=1 list: %+v", list)
	}
}

// interjectScenario: turn 1 lingers after its result (20s) so the operator
// can interject; turn 2 consumes the queued interjection.
const interjectScenario = `{"behavior":"interactive","turns":[
{"deltas":["Working on it… "],"final":"Slow first response.","status":{"phase":"asking","note":"slow"},"sleepMs":20000},
{"final":"Got the interjection - adjusting course.","status":{"phase":"asking","note":"adjusted"}}
]}`

// TestWorkflowInterject: a message with interrupt=true kills the in-flight
// turn (state interrupted) and the queued message drives the next turn.
func TestWorkflowInterject(t *testing.T) {
	r := interactiveRig(t, interjectScenario)
	base, token, stop := startUp(t, r)
	defer stop()
	client := &http.Client{Timeout: 10 * time.Second}

	var wf wfRow
	mustReq(t, client, "POST", base+"/api/v1/workflows", token,
		map[string]any{"title": "Interject", "text": "Long-running first turn."}, &wf)

	// Wait for the turn to be visibly in flight, then give the fake a beat to
	// enter its post-result sleep.
	waitDetail(t, client, base, token, wf.ID, 30*time.Second, "turn in flight", func(d wfDetail) bool {
		return d.Workflow.Status == "turn-running" && len(d.Turns) == 1
	})
	time.Sleep(1500 * time.Millisecond)

	// A plain message while the turn runs is a 409 …
	if code, _ := wfReq(t, client, "POST", base+"/api/v1/workflows/"+wf.ID+"/messages", token,
		map[string]any{"text": "no interrupt"}); code != 409 {
		t.Fatalf("message during turn without interrupt: code=%d, want 409", code)
	}
	// … the interject flavor kills the turn and queues the message.
	mustReq(t, client, "POST", base+"/api/v1/workflows/"+wf.ID+"/messages", token,
		map[string]any{"text": "Stop - take the other approach.", "interrupt": true}, nil)

	d := waitDetail(t, client, base, token, wf.ID, 60*time.Second, "interjection consumed", func(d wfDetail) bool {
		return d.Workflow.Status == "awaiting-user" && len(d.Turns) == 2 &&
			d.Turns[0].State == "interrupted" && d.Turns[1].State == "done"
	})
	_ = d

	// The interjection is persisted as a user row and the next assistant row
	// follows it.
	var msgs []wfMsg
	mustReq(t, client, "GET", base+"/api/v1/workflows/"+wf.ID+"/messages?limit=500", token, nil, &msgs)
	interjectionAt := int64(-1)
	for _, m := range msgs {
		if m.Role == "user" && m.Kind == "interjection" && strings.Contains(m.Content, "other approach") {
			interjectionAt = m.ID
		}
	}
	if interjectionAt < 0 {
		t.Fatalf("no interjection message row:\n%+v", msgs)
	}
	answered := false
	for _, m := range msgs {
		if m.Role == "assistant" && m.ID > interjectionAt {
			answered = true
		}
	}
	if !answered {
		t.Fatalf("no assistant reply after the interjection:\n%+v", msgs)
	}
}
