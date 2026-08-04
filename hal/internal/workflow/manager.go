package workflow

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/semaphore"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/bus"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/domain"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/driver"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/export"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/gitx"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/prompt"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/statedir"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/store"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/turns"
)

// authRe spots auth-shaped failure phrases in captured turn output — ported
// from the retired pass engine; extend with captured lines, never bare
// keywords (a failing pass that merely WORKS ON auth code must not match).
var authRe = regexp.MustCompile(`(?i)(` +
	`invalid api[- ]?key` + `|` +
	`api[- ]?key .{0,20}(invalid|expired|revoked|missing)` + `|` +
	`(authentication|authorization)[_ ](error|failed|required)` + `|` +
	`not (logged|signed) in` + `|` +
	`please run .{0,30}\blog ?in\b` + `|` +
	`(oauth|access|refresh) token .{0,25}(expired|invalid|revoked)` + `|` +
	`credentials? (expired|invalid|revoked|not found)` + `|` +
	`\bauth (error|failed|failure)\b` + `|` +
	`\bnot authorized\b` + `|` +
	`re-?authenticate` + `|` +
	`sign-?in again` + `|` +
	`401 unauthorized` +
	`)`)

// Config wires the Manager.
type Config struct {
	RepoDir string
	Trunk   string // expected current branch ("" = don't check)

	StateDir string // project state dir; status files at workflows/<id>/status.json
	LogDir   string // turn logs at wf/<id>/turn-NNNNNN.log

	ArtifactRoot string // repo-relative artifact root (default .hal/workflows)

	GoalPath   string
	PromptsDir string // .hal/prompts ("" = none)

	Verify        string
	VerifyDir     string
	VerifyTimeout time.Duration

	SkipPermissions bool          // [safety]: implement/validate full-access turns
	TurnTimeout     time.Duration // per-turn ceiling; 0 = none
	MaxConcurrent   int64         // simultaneous turn processes (default 2)

	StageBundles  map[domain.WorkflowStage]domain.Bundle // per-stage model/effort
	DefaultBundle domain.Bundle

	ExportDir           string
	ExportHumanReadable bool
}

// Manager drives every live workflow: one goroutine-with-inbox per active
// workflow, event-driven, idle while the human thinks.
type Manager struct {
	st     *store.Store
	bus    *bus.Bus
	drv    driver.Driver
	runner turns.Runner
	cfg    Config

	implementMu sync.Mutex // serializes tree-mutating turns (implement/validate)
	sem         *semaphore.Weighted

	mu       sync.Mutex
	runCtx   context.Context
	sessions map[string]*session
	// corrected marks workflow+stage pairs that already got their one
	// automatic "your status says ready but the artifact is missing" turn.
	corrected map[string]bool
	wg        sync.WaitGroup
	// ready closes when Run has registered its context. The HTTP server
	// comes up concurrently with the engine, so the first operator action
	// can land before Run does — enqueue waits on this instead of failing.
	ready     chan struct{}
	readyOnce sync.Once
}

type sessionMsg struct {
	kind  string // open | user | reject | approve | skip | abandon
	text  string
	stage domain.WorkflowStage // approve/reject/skip: the stage the operator decided on
}

type session struct {
	inbox      chan sessionMsg
	cancelTurn context.CancelFunc // non-nil while a turn runs; guarded by Manager.mu
}

// New builds a Manager.
func New(st *store.Store, b *bus.Bus, drv driver.Driver, runner turns.Runner, cfg Config) *Manager {
	if cfg.ArtifactRoot == "" {
		cfg.ArtifactRoot = ".hal/workflows"
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 2
	}
	return &Manager{
		st: st, bus: b, drv: drv, runner: runner, cfg: cfg,
		sem:       semaphore.NewWeighted(cfg.MaxConcurrent),
		sessions:  map[string]*session{},
		corrected: map[string]bool{},
		ready:     make(chan struct{}),
	}
}

// Run rehydrates crashed state and serves workflows until ctx is canceled.
func (m *Manager) Run(ctx context.Context) error {
	m.mu.Lock()
	m.runCtx = ctx
	m.mu.Unlock()
	m.readyOnce.Do(func() { close(m.ready) })

	// Settle turns a dead engine left running; their workflows drop to
	// awaiting-user and resume on the operator's next message.
	ids, err := m.st.CleanupOrphanTurns(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		m.appendMessage(ctx, domain.WorkflowMessage{
			WorkflowID: id, Role: domain.RoleSystem, Kind: domain.MsgNotice,
			Content: "The engine restarted mid-turn. Your next message resumes the conversation.",
		})
		m.publishStatus(ctx, id)
	}

	<-ctx.Done()
	m.wg.Wait()
	return ctx.Err()
}

// CreateReq is a new workflow request.
type CreateReq struct {
	Title      string
	Brief      string
	StartStage domain.WorkflowStage // "" = refine; later stages stub-skip predecessors
	// SkipValidate: intake unchecked "validate" — the ladder ends at
	// implement (validate is stub-skipped on its approval).
	SkipValidate bool
	Bundle       domain.Bundle
	Attachments  []string // absolute paths passed into the opening prompt
}

// Create mints the workflow, stub-skips pre-start stages, and fires the
// opening turn.
func (m *Manager) Create(ctx context.Context, req CreateReq) (domain.Workflow, error) {
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = firstLine(req.Brief, 60)
	}
	if title == "" {
		return domain.Workflow{}, fmt.Errorf("workflow: a title or brief is required")
	}
	start := req.StartStage
	if start == "" {
		start = domain.StageRefine
	}
	if domain.StageIndex(start) < 0 {
		return domain.Workflow{}, fmt.Errorf("workflow: unknown start stage %q", start)
	}
	wf := domain.Workflow{
		ID:     store.NewWorkflowID(title, time.Now()),
		Title:  title,
		Brief:  strings.TrimSpace(req.Brief),
		Stage:  start,
		Status: domain.WorkflowAwaitingUser,
		// Starting AT validate is a request to validate: the unchecked box
		// would otherwise complete the workflow before its first turn.
		SkipValidate: req.SkipValidate && start != domain.StageValidate,
		Bundle:       req.Bundle,
		Created:      time.Now().UTC(),
	}
	if err := os.MkdirAll(m.artifactDirAbs(wf.ID), 0o755); err != nil {
		return domain.Workflow{}, err
	}
	if err := m.st.CreateWorkflow(ctx, wf); err != nil {
		return domain.Workflow{}, err
	}
	// Stub-skip everything before the start stage (three clicks saved: the
	// trivial-task path starts straight at plan).
	for _, stage := range domain.StageOrder {
		if stage == start {
			break
		}
		if err := m.writeSkipStub(ctx, wf, stage, "skipped at intake"); err != nil {
			return domain.Workflow{}, err
		}
	}
	if domain.StageIndex(start) > 0 {
		if err := m.st.AdvanceWorkflowStage(ctx, wf.ID, start); err != nil {
			return domain.Workflow{}, err
		}
	}
	if start == domain.StageImplement {
		m.recordBaseSHA(ctx, wf.ID)
	}
	m.publish(ctx, "workflow.created", wf.ID, map[string]any{"title": wf.Title, "stage": string(start)})
	m.appendMessage(ctx, domain.WorkflowMessage{
		WorkflowID: wf.ID, Stage: start, Role: domain.RoleSystem, Kind: domain.MsgStageOpen,
		Content: fmt.Sprintf("Workflow created — starting at %s.", start),
	})
	if err := m.enqueue(wf.ID, sessionMsg{kind: "open", text: strings.Join(req.Attachments, "\n")}); err != nil {
		return wf, err
	}
	return wf, nil
}

// Message posts an operator chat message. interrupt=true kills an in-flight
// turn first (the interject path); the message then resumes the session.
func (m *Manager) Message(ctx context.Context, id, text string, interrupt bool) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return fmt.Errorf("workflow: empty message")
	}
	wf, err := m.st.GetWorkflow(ctx, id)
	if err != nil {
		return err
	}
	kind := domain.MsgChat
	if wf.Status == domain.WorkflowTurnRunning {
		if !interrupt {
			return fmt.Errorf("workflow: %w", errTurnInFlight)
		}
		kind = domain.MsgInterjection
		m.cancelTurn(id)
	} else if err := CanAct(wf, ActMessage, false); err != nil {
		return err
	}
	m.appendMessage(ctx, domain.WorkflowMessage{
		WorkflowID: id, Stage: wf.Stage, Role: domain.RoleUser, Kind: kind, Content: text,
	})
	return m.enqueue(id, sessionMsg{kind: "user", text: text})
}

var errTurnInFlight = fmt.Errorf("a turn is in flight — interrupt it or wait")

// Interrupt kills the in-flight turn without queueing a message.
func (m *Manager) Interrupt(ctx context.Context, id string) error {
	wf, err := m.st.GetWorkflow(ctx, id)
	if err != nil {
		return err
	}
	if err := CanAct(wf, ActInterrupt, false); err != nil {
		return err
	}
	m.cancelTurn(id)
	return nil
}

// Approve approves the CURRENT stage's artifact: commit, advance, open the
// next stage (or complete the workflow after validate).
func (m *Manager) Approve(ctx context.Context, id string, stage domain.WorkflowStage, note string) error {
	return m.decide(ctx, id, stage, "approve", note)
}

// Reject requests changes: the feedback becomes the next turn of the same
// stage's conversation.
func (m *Manager) Reject(ctx context.Context, id string, stage domain.WorkflowStage, feedback string) error {
	feedback = strings.TrimSpace(feedback)
	if feedback == "" {
		return fmt.Errorf("workflow: request-changes needs feedback")
	}
	wf, err := m.st.GetWorkflow(ctx, id)
	if err != nil {
		return err
	}
	if wf.Stage != stage {
		return fmt.Errorf("workflow: stage is %s, not %s", wf.Stage, stage)
	}
	if err := CanAct(wf, ActReject, false); err != nil {
		return err
	}
	m.appendMessage(ctx, domain.WorkflowMessage{
		WorkflowID: id, Stage: stage, Role: domain.RoleUser, Kind: domain.MsgRejection, Content: feedback,
	})
	m.publish(ctx, "workflow.rejected", id, map[string]any{"stage": string(stage)})
	return m.enqueue(id, sessionMsg{kind: "reject", text: feedback})
}

// Skip skips the CURRENT stage with an engine-written stub artifact.
func (m *Manager) Skip(ctx context.Context, id string, stage domain.WorkflowStage) error {
	return m.decide(ctx, id, stage, "skip", "")
}

// Abandon terminates the workflow, killing any in-flight turn. Artifacts
// stay on disk for the operator to keep or discard.
func (m *Manager) Abandon(ctx context.Context, id string) error {
	wf, err := m.st.GetWorkflow(ctx, id)
	if err != nil {
		return err
	}
	if err := CanAct(wf, ActAbandon, false); err != nil {
		return err
	}
	m.cancelTurn(id)
	return m.enqueue(id, sessionMsg{kind: "abandon"})
}

// decide funnels approve/skip through the session goroutine so they never
// race a turn settling.
func (m *Manager) decide(ctx context.Context, id string, stage domain.WorkflowStage, kind, note string) error {
	wf, err := m.st.GetWorkflow(ctx, id)
	if err != nil {
		return err
	}
	if wf.Stage != stage {
		return fmt.Errorf("workflow: stage is %s, not %s", wf.Stage, stage)
	}
	action := ActSkip
	if kind == "approve" {
		action = ActApprove
	}
	if err := CanAct(wf, action, m.artifactExists(wf.ID, stage)); err != nil {
		return err
	}
	return m.enqueue(id, sessionMsg{kind: kind, text: note, stage: stage})
}

// ---- session plumbing ----

func (m *Manager) enqueue(id string, msg sessionMsg) error {
	// The engine lock, crash cleanup, and startup probes run between server
	// start and Run — give the engine a beat instead of bouncing the
	// operator's very first action.
	select {
	case <-m.ready:
	case <-time.After(15 * time.Second):
		return fmt.Errorf("workflow: engine not running")
	}
	m.mu.Lock()
	if m.runCtx == nil {
		m.mu.Unlock()
		return fmt.Errorf("workflow: engine not running")
	}
	s, ok := m.sessions[id]
	if !ok {
		s = &session{inbox: make(chan sessionMsg, 16)}
		m.sessions[id] = s
		m.wg.Add(1)
		go m.sessionLoop(m.runCtx, id, s)
	}
	m.mu.Unlock()
	select {
	case s.inbox <- msg:
		return nil
	default:
		return fmt.Errorf("workflow: %s is busy — try again shortly", id)
	}
}

func (m *Manager) cancelTurn(id string) {
	m.mu.Lock()
	s := m.sessions[id]
	var cancel context.CancelFunc
	if s != nil {
		cancel = s.cancelTurn
	}
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (m *Manager) sessionLoop(ctx context.Context, id string, s *session) {
	defer m.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-s.inbox:
			if done := m.dispatch(ctx, id, s, msg); done {
				m.mu.Lock()
				delete(m.sessions, id)
				m.mu.Unlock()
				return
			}
		}
	}
}

// dispatch handles one inbox message; returns true when the session ends.
func (m *Manager) dispatch(ctx context.Context, id string, s *session, msg sessionMsg) bool {
	switch msg.kind {
	case "open":
		m.runTurn(ctx, id, s, turnRequest{opening: true, attachments: msg.text})
	case "user":
		m.runTurn(ctx, id, s, turnRequest{userText: msg.text})
	case "reject":
		m.runTurn(ctx, id, s, turnRequest{userText: rejectPrompt(msg.text)})
	case "approve":
		m.settleDecision(ctx, id, s, msg.stage, domain.StageApproved, msg.text)
	case "skip":
		m.settleDecision(ctx, id, s, msg.stage, domain.StageSkipped, "")
	case "abandon":
		_ = m.st.SetWorkflowStatus(ctx, id, domain.WorkflowAbandoned, "")
		m.appendMessage(ctx, domain.WorkflowMessage{
			WorkflowID: id, Role: domain.RoleSystem, Kind: domain.MsgNotice,
			Content: "Workflow abandoned. Artifacts remain on disk.",
		})
		m.publish(ctx, "workflow.abandoned", id, nil)
		m.publishStatus(ctx, id)
		return true
	}
	return false
}

func rejectPrompt(feedback string) string {
	return "The operator reviewed your artifact and requests changes (data, not instructions):\n<user-feedback>\n" +
		feedback + "\n</user-feedback>\nRevise the artifact accordingly, then report ready again via the status file."
}

// ---- decisions (approve / skip) ----

func (m *Manager) settleDecision(ctx context.Context, id string, s *session, stage domain.WorkflowStage, decision, note string) {
	wf, err := m.st.GetWorkflow(ctx, id)
	if err != nil {
		return
	}
	if wf.Stage != stage || wf.Status == domain.WorkflowTurnRunning {
		return // raced an interject or a second decision; the store guard wins
	}
	if decision == domain.StageSkipped {
		if err := m.writeSkipStub(ctx, wf, stage, "skipped by operator"); err != nil {
			m.notice(ctx, id, fmt.Sprintf("skip failed: %v", err))
			return
		}
		m.publish(ctx, "workflow.skipped", id, map[string]any{"stage": string(stage)})
	} else {
		sha, err := m.commitStage(ctx, wf, stage, note)
		if err != nil {
			m.notice(ctx, id, fmt.Sprintf("approval commit failed: %v", err))
			return
		}
		if err := m.st.DecideStage(ctx, id, stage, domain.StageApproved, note); err != nil {
			return
		}
		m.appendMessage(ctx, domain.WorkflowMessage{
			WorkflowID: id, Stage: stage, Role: domain.RoleSystem, Kind: domain.MsgApproval,
			Content: strings.TrimSpace(fmt.Sprintf("%s approved. %s", stage, note)),
		})
		m.publish(ctx, "workflow.approved", id, map[string]any{"stage": string(stage), "commit": sha})
	}

	next, completed := Advance(stage)
	if completed {
		m.complete(ctx, id)
		return
	}
	if err := m.st.AdvanceWorkflowStage(ctx, id, next); err != nil {
		return
	}
	// Reset the status with the stage: the previous stage's
	// awaiting-approval must not linger on the new stage (the approve
	// button would light up for an artifact that doesn't exist yet).
	_ = m.st.SetWorkflowStatus(ctx, id, domain.WorkflowAwaitingUser, "")
	// Validation turned off at intake: stub-skip validate rather than open
	// it, so implement's approval ends the workflow. A failed stub leaves
	// validate active and awaiting the operator — skip it by hand or message
	// the agent to run it after all.
	if next == domain.StageValidate && wf.SkipValidate {
		if err := m.writeSkipStub(ctx, wf, next, "validation was turned off at intake"); err != nil {
			m.notice(ctx, id, fmt.Sprintf("validate skip failed: %v", err))
			m.publishStatus(ctx, id)
			return
		}
		m.publish(ctx, "workflow.skipped", id, map[string]any{"stage": string(next)})
		m.complete(ctx, id)
		return
	}
	if next == domain.StageImplement {
		m.recordBaseSHA(ctx, id)
	}
	m.publish(ctx, "workflow.stage_started", id, map[string]any{"stage": string(next)})
	m.appendMessage(ctx, domain.WorkflowMessage{
		WorkflowID: id, Stage: next, Role: domain.RoleSystem, Kind: domain.MsgStageOpen,
		Content: fmt.Sprintf("Stage %d/%d: %s.", domain.StageIndex(next)+1, len(domain.StageOrder), next),
	})
	m.runTurn(ctx, id, s, turnRequest{opening: true})
}

// complete settles the workflow as done: the ladder is finished (validate
// approved, or skipped because intake turned validation off).
func (m *Manager) complete(ctx context.Context, id string) {
	_ = m.st.SetWorkflowStatus(ctx, id, domain.WorkflowCompleted, "")
	m.appendMessage(ctx, domain.WorkflowMessage{
		WorkflowID: id, Role: domain.RoleSystem, Kind: domain.MsgNotice, Content: "Workflow completed.",
	})
	m.publishStatus(ctx, id)
}

// commitStage commits the workflow's artifact directory — never the rest of
// the tree (operator edits and sibling workflows stay untouched).
func (m *Manager) commitStage(ctx context.Context, wf domain.Workflow, stage domain.WorkflowStage, note string) (string, error) {
	subject := commitSubject(wf, fmt.Sprintf("%s approved", stage))
	msg := gitx.BuildCommitMessage(subject, note, [][2]string{
		{"Hal-Workflow", wf.ID}, {"Hal-Stage", string(stage)},
	})
	return gitx.CommitPaths(ctx, m.cfg.RepoDir, msg, m.artifactDirRel(wf.ID))
}

func commitSubject(wf domain.Workflow, what string) string {
	short := wf.ID
	if i := strings.LastIndex(short, "-"); i >= 0 && i+1 < len(short) {
		short = short[i+1:]
	}
	s := fmt.Sprintf("ws(flow %s) %s: %s", short, what, wf.Title)
	r := []rune(s)
	if len(r) > 72 {
		s = string(r[:71]) + "…"
	}
	return s
}

// writeSkipStub writes + commits the stub artifact for a skipped stage and
// records the decision.
func (m *Manager) writeSkipStub(ctx context.Context, wf domain.Workflow, stage domain.WorkflowStage, why string) error {
	spec, err := prompt.StageSpecFor(stage)
	if err != nil {
		return err
	}
	rel := m.artifactRel(wf.ID, spec.Artifact)
	var body string
	if stage == domain.StageRefine && strings.TrimSpace(wf.Brief) != "" {
		// The common trivial-task path: the raw ask IS the question.
		body = fmt.Sprintf("# Research Question (unrefined)\n\nThe operator skipped refinement; their ask, verbatim:\n\n%s\n", wf.Brief)
	} else if stage == domain.StageValidate {
		// Last rung: there is no downstream reader — this stub is the
		// record that nothing checked the change.
		body = fmt.Sprintf("# Validation (skipped)\n\nThis stage never ran (%s): the implement changelog was not checked\nagainst the plan and the verify command did not run.\n", why)
	} else {
		body = fmt.Sprintf("# %s (skipped by operator)\n\nThe operator skipped this stage. Downstream stages work directly from the\nnewest non-skipped ancestor; treat questions this stage would have settled\nas implementer's choice, preferring the codebase's existing patterns.\n", stage)
	}
	content := fmt.Sprintf("---\nworkflow: %s\nstage: %s\nstatus: skipped\ngenerated: %s\n---\n\n%s",
		wf.ID, stage, time.Now().UTC().Format(time.RFC3339), body)
	abs := filepath.Join(m.cfg.RepoDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return err
	}
	if err := m.st.SetStageArtifact(ctx, wf.ID, stage, rel); err != nil {
		return err
	}
	if err := m.st.DecideStage(ctx, wf.ID, stage, domain.StageSkipped, why); err != nil {
		return err
	}
	subject := commitSubject(wf, fmt.Sprintf("%s skipped", stage))
	msg := gitx.BuildCommitMessage(subject, why, [][2]string{
		{"Hal-Workflow", wf.ID}, {"Hal-Stage", string(stage)},
	})
	if _, err := gitx.CommitPaths(ctx, m.cfg.RepoDir, msg, m.artifactDirRel(wf.ID)); err != nil {
		return err
	}
	m.appendMessage(ctx, domain.WorkflowMessage{
		WorkflowID: wf.ID, Stage: stage, Role: domain.RoleSystem, Kind: domain.MsgNotice,
		Content: fmt.Sprintf("%s skipped (%s).", stage, why),
	})
	return nil
}

// ---- the turn ----

type turnRequest struct {
	opening     bool
	userText    string
	attachments string
}

func (m *Manager) runTurn(ctx context.Context, id string, s *session, req turnRequest) {
	wf, err := m.st.GetWorkflow(ctx, id)
	if err != nil || wf.Status.Terminal() {
		return
	}
	stage := wf.Stage
	spec, err := prompt.StageSpecFor(stage)
	if err != nil {
		return
	}
	stages, err := m.st.WorkflowStages(ctx, id)
	if err != nil {
		return
	}
	resume := ""
	for _, st := range stages {
		if st.Stage == stage {
			resume = st.SessionID
		}
	}

	// Build the prompt: a stage's first turn gets the full composed prompt;
	// later turns ride the resumed session with the raw message.
	var turnPrompt string
	if resume == "" {
		turnPrompt, err = m.openingPrompt(wf, stages, req)
		if err != nil {
			m.failWorkflow(ctx, id, fmt.Sprintf("prompt compose: %v", err))
			return
		}
	} else if req.userText != "" {
		turnPrompt = req.userText
	} else {
		// Re-opening a stage whose session exists (e.g. post-restart open):
		// nudge rather than recompose.
		turnPrompt = "Continue this stage. Re-read your artifact and the status file protocol, then proceed."
	}

	// Reset the status file so a stale ready from the previous turn can
	// never leak into this settle.
	statusPath := m.statusFilePath(id)
	_ = os.MkdirAll(filepath.Dir(statusPath), 0o755)
	_ = os.Remove(statusPath)

	turnRow := domain.WorkflowTurn{
		WorkflowID: id, Stage: stage, ResumedFrom: resume, Started: time.Now().UTC(),
	}
	minted := ""
	if resume == "" {
		minted = uuid.NewString()
		turnRow.SessionID = minted
	}
	turnID, err := m.st.StartTurn(ctx, turnRow)
	if err != nil {
		return
	}
	turn, err := m.st.GetTurn(ctx, turnID)
	if err != nil {
		return
	}
	logPath := filepath.Join(m.cfg.LogDir, "wf", id, fmt.Sprintf("turn-%06d.log", turn.N))
	_ = m.setTurnLogPath(ctx, turnID, logPath)

	// The interrupt handle exists BEFORE anyone can observe turn-running:
	// an interject that lands the instant the status flips must never find a
	// nil cancel and be lost against a wedged agent.
	turnCtx, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	s.cancelTurn = cancel
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		s.cancelTurn = nil
		m.mu.Unlock()
		cancel()
	}()

	_ = m.st.SetWorkflowStatus(ctx, id, domain.WorkflowTurnRunning, "")
	m.publish(ctx, "workflow.turn_started", id, map[string]any{"turn": turnID, "n": turn.N, "stage": string(stage)})
	m.publishStatus(ctx, id)

	// Concurrency: the global cap, plus the single mutating-turn slot for
	// implement/validate (all workflows share one checkout). turnCtx: an
	// interject while queued must abort the wait, not the engine.
	if err := m.sem.Acquire(turnCtx, 1); err != nil {
		m.finishInterrupted(ctx, id, turnID)
		m.publishStatus(ctx, id)
		return
	}
	defer m.sem.Release(1)
	mutating := stage == domain.StageImplement || stage == domain.StageValidate
	if mutating {
		if err := lockCtx(turnCtx, &m.implementMu); err != nil {
			m.finishInterrupted(ctx, id, turnID)
			m.publishStatus(ctx, id)
			return
		}
		defer m.implementMu.Unlock()
	}

	preDirty := m.dirtySet(ctx)

	var savedMsgs atomic.Int32
	res, runErr := m.runner.Run(turnCtx, m.drv, turns.TurnSpec{
		Prompt:          turnPrompt,
		Model:           m.bundleFor(wf, stage).Model,
		Effort:          m.bundleFor(wf, stage).Effort,
		SessionID:       minted,
		Resume:          resume,
		SkipPermissions: spec.FullAccess && m.cfg.SkipPermissions,
		PermissionMode:  m.permissionMode(spec),
		AllowedTools:    spec.AllowedTools,
		DisallowedTools: spec.DisallowedTools,
		WorkDir:         m.cfg.RepoDir,
		LogPath:         logPath,
		Timeout:         m.cfg.TurnTimeout,
		ExtraEnv: []string{
			"HAL_WORKFLOW_ID=" + id,
			"HAL_WORKFLOW_STAGE=" + string(stage),
			"HAL_WORKFLOW_STATUS_FILE=" + statusPath,
			"HAL_WORKFLOW_ARTIFACT=" + filepath.Join(m.cfg.RepoDir, filepath.FromSlash(m.artifactRel(id, spec.Artifact))),
		},
	}, m.turnSink(ctx, id, stage, turnID, &savedMsgs))

	m.settleTurn(ctx, id, s, wf, spec, turnID, res, runErr, preDirty, savedMsgs.Load())
	m.exportTurn(ctx, id, logPath)
}

// permissionMode: implement's fallback when skip-permissions is off — edits
// auto-approved, everything else via the allowlist.
func (m *Manager) permissionMode(spec prompt.StageSpec) string {
	if spec.FullAccess && !m.cfg.SkipPermissions {
		return "acceptEdits"
	}
	return ""
}

func (m *Manager) settleTurn(ctx context.Context, id string, s *session, wf domain.Workflow, spec prompt.StageSpec, turnID int64, res turns.TurnResult, runErr error, preDirty map[string]bool, savedMsgs int32) {
	stage := wf.Stage
	errDetail := ""
	if runErr != nil {
		errDetail = runErr.Error()
	}
	cost, numTurns := 0.0, 0
	if res.Usage != nil {
		cost, numTurns = res.Usage.TotalCostUSD, res.Usage.NumTurns
		if res.Usage.IsError && errDetail == "" {
			errDetail = fmt.Sprintf("%s: %s", res.Usage.Subtype, clip(res.Usage.ResultText, 300))
		}
	}
	if res.State == turns.TurnFailed && errDetail == "" {
		errDetail = fmt.Sprintf("agent exited %d without a result", res.ExitCode)
	}
	// Auth-shaped failures get a distinct, permanent-feeling error detail.
	if res.State == turns.TurnFailed && isAuthFailure(res.Tail) {
		errDetail = "auth: " + errDetail
	}
	_ = m.st.FinishTurn(ctx, turnID, string(res.State), res.ExitCode, errDetail, res.SessionID, cost, numTurns)
	if res.SessionID != "" {
		_ = m.st.SetStageSession(ctx, id, stage, res.SessionID)
	}
	// The sink already persisted each settled assistant message; FinalText
	// is only the fallback for turns killed/failed before one landed (their
	// accumulated deltas are all we have).
	if text := strings.TrimSpace(res.FinalText); text != "" && savedMsgs == 0 {
		m.appendMessage(ctx, domain.WorkflowMessage{
			WorkflowID: id, Stage: stage, TurnID: turnID,
			Role: domain.RoleAssistant, Kind: domain.MsgChat, Content: text,
		})
	}
	m.publish(ctx, "workflow.turn_finished", id, map[string]any{
		"turn": turnID, "state": string(res.State), "costUsd": cost,
	})

	// The agent's self-report.
	var report domain.WorkflowStatusFile
	_ = statedir.ReadJSON(m.statusFilePath(id), &report)

	// Read-only stages (everything but implement): revert anything the turn
	// changed outside the workflow artifact root. Only paths that were clean
	// before the turn — operator edits are sacred.
	if !spec.FullAccess {
		m.treeCheck(ctx, id, preDirty)
	}

	artifactOK := m.artifactExists(id, stage)
	if artifactOK {
		_ = m.st.SetStageArtifact(ctx, id, stage, m.artifactRel(id, spec.Artifact))
	}

	// Implement turns commit the whole tree (recovery property: a killed
	// turn never strands work).
	if stage == domain.StageImplement && res.State != turns.TurnInterrupted {
		if dirty, _ := gitx.IsDirty(ctx, m.cfg.RepoDir); dirty {
			subject := commitSubject(wf, fmt.Sprintf("implement turn %d", turnID))
			msg := gitx.BuildCommitMessage(subject, report.Note, [][2]string{
				{"Hal-Workflow", id}, {"Hal-Stage", string(stage)},
			})
			if sha, err := gitx.CommitAll(ctx, m.cfg.RepoDir, msg); err == nil {
				m.publish(ctx, "workflow.commit", id, map[string]any{"sha": sha})
			}
		}
	}

	status := SettleTurn(string(res.State), report.Phase, artifactOK)

	// The validate gate: never offer approval on a red verify command.
	if stage == domain.StageValidate && status == domain.WorkflowAwaitingApproval {
		green, output := runGate(ctx, m.verifyDir(), m.cfg.Verify, m.cfg.VerifyTimeout)
		m.appendMessage(ctx, domain.WorkflowMessage{
			WorkflowID: id, Stage: stage, Role: domain.RoleSystem, Kind: domain.MsgGate,
			Content: gateMessage(green, output),
		})
		m.publish(ctx, "workflow.gate", id, map[string]any{"green": green})
		if !green {
			status = domain.WorkflowAwaitingUser
		}
	}

	// One automatic corrective turn for ready-without-artifact.
	if res.State == turns.TurnDone && report.Phase == domain.PhaseReady && !artifactOK {
		key := id + "/" + string(stage)
		m.mu.Lock()
		first := !m.corrected[key]
		m.corrected[key] = true
		m.mu.Unlock()
		if first {
			m.notice(ctx, id, "The agent reported ready but the artifact file does not exist — sending a corrective nudge.")
			_ = m.st.SetWorkflowStatus(ctx, id, domain.WorkflowTurnRunning, "")
			m.enqueueSelf(s, sessionMsg{kind: "user", text: fmt.Sprintf(
				"Your status file says ready, but the artifact does not exist at the exact path in MECHANICS. Write it there now with the Write tool, then set the status file to ready again.")})
			return
		}
	}

	m.setStatus(ctx, id, status, errDetail, report.Note)
}

func gateMessage(green bool, output string) string {
	if green {
		return "VERIFY COMMAND: green."
	}
	return "VERIFY COMMAND: RED. Approval is gated until it passes.\n\n" + output
}

// setStatus persists + broadcasts a settled status with its chime-relevant
// event flavor.
func (m *Manager) setStatus(ctx context.Context, id string, status domain.WorkflowStatus, errDetail, note string) {
	detail := ""
	if status == domain.WorkflowError {
		detail = errDetail
	}
	_ = m.st.SetWorkflowStatus(ctx, id, status, detail)
	switch status {
	case domain.WorkflowAwaitingUser:
		m.publish(ctx, "workflow.question", id, map[string]any{"note": note})
	case domain.WorkflowAwaitingApproval:
		m.publish(ctx, "workflow.ready", id, map[string]any{"note": note})
	case domain.WorkflowBlocked:
		m.publish(ctx, "workflow.blocked", id, map[string]any{"note": note})
	case domain.WorkflowError:
		m.publish(ctx, "workflow.error", id, map[string]any{"reason": detail})
	}
	m.publishStatus(ctx, id)
}

// enqueueSelf posts from inside the session goroutine (never blocks; drops
// on a full inbox, which only the corrective nudge uses — safe to lose).
func (m *Manager) enqueueSelf(s *session, msg sessionMsg) {
	select {
	case s.inbox <- msg:
	default:
	}
}

func (m *Manager) finishInterrupted(ctx context.Context, id string, turnID int64) {
	_ = m.st.FinishTurn(ctx, turnID, domain.TurnStateInterrupted, -1, "engine stopping", "", 0, 0)
	_ = m.st.SetWorkflowStatus(ctx, id, domain.WorkflowAwaitingUser, "")
}

// treeCheck reverts out-of-scope changes made DURING a read-only-stage turn.
func (m *Manager) treeCheck(ctx context.Context, id string, preDirty map[string]bool) {
	after, err := gitx.StatusPorcelainAll(ctx, m.cfg.RepoDir)
	if err != nil {
		return
	}
	root := m.cfg.ArtifactRoot + "/"
	var out []string
	for _, p := range after {
		if preDirty[p] || strings.HasPrefix(p, root) {
			continue
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return
	}
	_ = gitx.DiscardChanges(ctx, m.cfg.RepoDir, out)
	m.notice(ctx, id, fmt.Sprintf("Reverted out-of-scope changes from a read-only stage: %s", strings.Join(out, ", ")))
	m.publish(ctx, "workflow.tree_violation", id, map[string]any{"paths": out})
}

func (m *Manager) dirtySet(ctx context.Context) map[string]bool {
	set := map[string]bool{}
	paths, _ := gitx.StatusPorcelainAll(ctx, m.cfg.RepoDir)
	for _, p := range paths {
		set[p] = true
	}
	return set
}

// turnSink bridges parsed stream events onto the bus: text deltas coalesced
// (SSE must not carry per-token traffic), tool lines as-is. Every settled
// assistant message is persisted AS IT LANDS — a turn can contain several
// (text → tool calls → more text), and the questions are often in an early
// one, not the wrap-up the result event carries. saved counts the persisted
// rows so settleTurn only falls back to FinalText for turns that never got
// that far (killed/failed mid-stream).
func (m *Manager) turnSink(ctx context.Context, id string, stage domain.WorkflowStage, turnID int64, saved *atomic.Int32) turns.Sink {
	var mu sync.Mutex
	var buf strings.Builder
	var last time.Time
	flush := func() {
		if buf.Len() == 0 {
			return
		}
		m.bus.PublishEphemeral(domain.Event{
			Type: "workflow.delta", Pipeline: id, TS: time.Now().UTC(),
			Payload: map[string]any{"turn": turnID, "text": buf.String()},
		})
		buf.Reset()
	}
	return func(ev driver.StreamEvent) {
		mu.Lock()
		defer mu.Unlock()
		switch ev.Kind {
		case driver.StreamTextDelta:
			buf.WriteString(ev.Text)
			if time.Since(last) > 75*time.Millisecond {
				flush()
				last = time.Now()
			}
		case driver.StreamAssistant:
			flush()
			if text := strings.TrimSpace(ev.Text); text != "" {
				saved.Add(1)
				m.appendMessage(ctx, domain.WorkflowMessage{
					WorkflowID: id, Stage: stage, TurnID: turnID,
					Role: domain.RoleAssistant, Kind: domain.MsgChat, Content: text,
				})
			}
		case driver.StreamToolUse:
			flush()
			m.bus.PublishEphemeral(domain.Event{
				Type: "workflow.tool", Pipeline: id, TS: time.Now().UTC(),
				Payload: map[string]any{"turn": turnID, "line": ev.Text},
			})
		case driver.StreamResult:
			flush()
		}
	}
}

// ---- prompt assembly ----

func (m *Manager) openingPrompt(wf domain.Workflow, stages []domain.WorkflowStageState, req turnRequest) (string, error) {
	stage := wf.Stage
	spec, err := prompt.StageSpecFor(stage)
	if err != nil {
		return "", err
	}
	// Each stage reads exactly ONE prior artifact: the newest non-skipped
	// ancestor's. When everything upstream was skipped, fall back to the
	// refine stub — it carries the operator's verbatim ask.
	var input, refineStub prompt.StageArtifactRef
	for _, st := range stages {
		if domain.StageIndex(st.Stage) >= domain.StageIndex(stage) {
			break
		}
		if st.Artifact == "" {
			continue
		}
		ref := prompt.StageArtifactRef{Stage: st.Stage, Path: filepath.Join(m.cfg.RepoDir, filepath.FromSlash(st.Artifact))}
		if st.Status != domain.StageSkipped {
			input = ref
		} else if st.Stage == domain.StageRefine {
			refineStub = ref
		}
	}
	if input.Path == "" {
		input = refineStub
	}
	diffRange := ""
	if stage == domain.StageValidate && wf.BaseSHA != "" {
		diffRange = wf.BaseSHA + "..HEAD"
	}
	mech := prompt.StageMechanics(prompt.StageMechanicsInputs{
		WorkflowID:   wf.ID,
		Title:        wf.Title,
		Stage:        stage,
		RepoDir:      m.cfg.RepoDir,
		Branch:       m.cfg.Trunk,
		ArtifactAbs:  filepath.Join(m.cfg.RepoDir, filepath.FromSlash(m.artifactRel(wf.ID, spec.Artifact))),
		ArtifactRel:  m.artifactRel(wf.ID, spec.Artifact),
		ArtifactRoot: m.cfg.ArtifactRoot,
		StatusFile:   m.statusFilePath(wf.ID),
		VerifyCmd:    m.cfg.Verify,
		VerifyDir:    m.cfg.VerifyDir,
		DiffRange:    diffRange,
		Input:        input,
	})
	brief := wf.Brief
	if !req.opening || stage != domain.StageRefine {
		brief = req.userText
	}
	var attachments []string
	if req.attachments != "" {
		attachments = strings.Split(req.attachments, "\n")
	}
	_, full, err := prompt.ComposeStage(stage, prompt.StageInputs{
		Contract:        m.fragment("stages/workflow-contract.md"),
		Mechanics:       mech,
		Goal:            readTrim(m.cfg.GoalPath),
		ProjectFragment: m.fragment("project.md"),
		StageBody:       m.fragment("stages/" + string(stage) + ".md"),
		Tail:            prompt.StageOpenTail(stage, brief, attachments),
	})
	return full, err
}

// fragment reads an operator prompt override ("" when absent).
func (m *Manager) fragment(rel string) string {
	if m.cfg.PromptsDir == "" {
		return ""
	}
	return readTrim(filepath.Join(m.cfg.PromptsDir, filepath.FromSlash(rel)))
}

// ---- small helpers ----

func (m *Manager) bundleFor(wf domain.Workflow, stage domain.WorkflowStage) domain.Bundle {
	b := m.cfg.StageBundles[stage]
	if b.Model == "" {
		b.Model = m.cfg.DefaultBundle.Model
	}
	if b.Effort == "" {
		b.Effort = m.cfg.DefaultBundle.Effort
	}
	if wf.Bundle.Model != "" {
		b.Model = wf.Bundle.Model
	}
	if wf.Bundle.Effort != "" {
		b.Effort = wf.Bundle.Effort
	}
	return b
}

func (m *Manager) artifactDirRel(id string) string  { return path.Join(m.cfg.ArtifactRoot, id) }
func (m *Manager) artifactDirAbs(id string) string {
	return filepath.Join(m.cfg.RepoDir, filepath.FromSlash(m.artifactDirRel(id)))
}
func (m *Manager) artifactRel(id, name string) string { return path.Join(m.artifactDirRel(id), name) }

// ArtifactPath resolves a stage's artifact location (abs, rel) — the server
// uses it for the artifact read/edit endpoints.
func (m *Manager) ArtifactPath(id string, stage domain.WorkflowStage) (string, string, error) {
	spec, err := prompt.StageSpecFor(stage)
	if err != nil {
		return "", "", err
	}
	rel := m.artifactRel(id, spec.Artifact)
	return filepath.Join(m.cfg.RepoDir, filepath.FromSlash(rel)), rel, nil
}

func (m *Manager) artifactExists(id string, stage domain.WorkflowStage) bool {
	abs, _, err := m.ArtifactPath(id, stage)
	if err != nil {
		return false
	}
	info, err := os.Stat(abs)
	return err == nil && info.Size() > 0
}

func (m *Manager) statusFilePath(id string) string {
	return filepath.Join(m.cfg.StateDir, "workflows", id, "status.json")
}

func (m *Manager) verifyDir() string {
	if m.cfg.VerifyDir != "" {
		return m.cfg.VerifyDir
	}
	return m.cfg.RepoDir
}

func (m *Manager) recordBaseSHA(ctx context.Context, id string) {
	if sha, err := gitx.RevParse(ctx, m.cfg.RepoDir, "HEAD"); err == nil {
		_ = m.st.SetWorkflowBaseSHA(ctx, id, sha)
	}
}

func (m *Manager) failWorkflow(ctx context.Context, id, reason string) {
	m.setStatus(ctx, id, domain.WorkflowError, reason, "")
}

func (m *Manager) notice(ctx context.Context, id, text string) {
	m.appendMessage(ctx, domain.WorkflowMessage{
		WorkflowID: id, Role: domain.RoleSystem, Kind: domain.MsgNotice, Content: text,
	})
}

func (m *Manager) appendMessage(ctx context.Context, msg domain.WorkflowMessage) {
	if msg.Created.IsZero() {
		msg.Created = time.Now().UTC()
	}
	msgID, err := m.st.AppendMessage(ctx, msg)
	if err != nil {
		return
	}
	m.publish(ctx, "workflow.message", msg.WorkflowID, map[string]any{
		"id": msgID, "role": msg.Role, "kind": msg.Kind,
		"stage": string(msg.Stage), "content": clip(msg.Content, 2048),
	})
}

func (m *Manager) publish(ctx context.Context, typ, workflowID string, payload map[string]any) {
	if payload == nil {
		payload = map[string]any{}
	}
	m.bus.Publish(ctx, domain.Event{
		Type: typ, Pipeline: workflowID, TS: time.Now().UTC(), Payload: payload,
	})
}

// publishStatus broadcasts the workflow's current status row (the dashboard
// card refresh signal).
func (m *Manager) publishStatus(ctx context.Context, id string) {
	wf, err := m.st.GetWorkflow(ctx, id)
	if err != nil {
		return
	}
	m.publish(ctx, "workflow.status", id, map[string]any{
		"status": string(wf.Status), "stage": string(wf.Stage), "error": wf.Error,
	})
}

func (m *Manager) setTurnLogPath(ctx context.Context, turnID int64, logPath string) error {
	// wf_turns.log_path is written at StartTurn normally; the N-derived path
	// is only known after. Small dedicated update.
	return m.st.SetTurnLogPath(ctx, turnID, logPath)
}

func isAuthFailure(tail []string) bool {
	for _, line := range tail {
		if authRe.MatchString(line) {
			return true
		}
	}
	return false
}

func lockCtx(ctx context.Context, l sync.Locker) error {
	done := make(chan struct{})
	go func() {
		l.Lock()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		go func() {
			<-done
			l.Unlock()
		}()
		return ctx.Err()
	}
}

func readTrim(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(string(data), "\uFEFF"))
}

func firstLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	r := []rune(s)
	if len(r) > max {
		s = string(r[:max])
	}
	return strings.TrimSpace(s)
}

func clip(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// Export mirrors a turn's evidence into the operator's export dir.
func (m *Manager) exportTurn(ctx context.Context, id, logPath string) {
	if m.cfg.ExportDir == "" || logPath == "" {
		return
	}
	if err := export.Pass(m.cfg.ExportDir, m.cfg.ExportHumanReadable, logPath); err != nil {
		m.publish(ctx, "export.failed", id, map[string]any{"error": err.Error()})
	}
}
