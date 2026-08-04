package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/domain"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/driver"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/turns"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/export"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/proc"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/prompt"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/statedir"
)

// The self-evaluator: an on-demand, read-only forensics agent that answers
// "why did the hal do X?" from the evidence the engine already records —
// commit trailers, pass rows, pass logs, and archived session transcripts.
// It is one inquiry at a time, never touches the backlog, and never commits.

// ErrInquiryBusy is returned while a previous inquiry is still running.
var ErrInquiryBusy = errors.New("an inquiry is already running — wait for it or stop it")

// inquiryTimeout bounds one inquiry run; forensic greps through large
// transcripts take a while, but nothing legitimate takes this long.
const inquiryTimeout = 15 * time.Minute

// inquiryHistory caps the in-memory Q&A list (answers stay on disk as logs).
const inquiryHistory = 50

// inquiryAnswerCap bounds the captured answer; anything beyond it is truncated.
const inquiryAnswerCap = 1 << 20

// inquiryAllowedTools is the read-only tool grant for the evaluator agent.
// The inquiry deliberately does NOT use skip-permissions: this list is the
// enforcement of the contract's "read only" rule, not just a request.
const inquiryAllowedTools = "Read,Grep,Glob," +
	"Bash(git log:*),Bash(git show:*),Bash(git blame:*),Bash(git diff:*)," +
	"Bash(git grep:*),Bash(git rev-parse:*),Bash(git branch:*),Bash(git tag:*)"

// Inquiry is one self-evaluator question and its (eventual) answer.
type Inquiry struct {
	ID       int64     `json:"id"`
	Question string    `json:"question"`
	State    string    `json:"state"` // running | done | failed
	Auto     bool      `json:"auto,omitempty"` // fired by the engine (e.g. a breaker halt), not the operator
	Answer   string    `json:"answer,omitempty"`
	Error    string    `json:"error,omitempty"`
	Started  time.Time `json:"started"`
	Ended    time.Time `json:"ended,omitzero"`
}

// Inquiries returns the recorded inquiries, newest first.
func (a *App) Inquiries() []*Inquiry {
	a.inqMu.Lock()
	defer a.inqMu.Unlock()
	out := make([]*Inquiry, 0, len(a.inqList))
	for i := len(a.inqList) - 1; i >= 0; i-- {
		cp := *a.inqList[i]
		out = append(out, &cp)
	}
	return out
}

// StopInquiry cancels the running inquiry, if any.
func (a *App) StopInquiry() bool {
	a.inqMu.Lock()
	defer a.inqMu.Unlock()
	if a.inqCancel == nil {
		return false
	}
	a.inqCancel()
	return true
}

// AskInquiry starts the self-evaluator on a question and returns immediately;
// the answer streams over the bus ("inquiry.log" lines, then a persisted
// "inquiry.answered") and lands on the returned Inquiry's record. One at a
// time: a second ask while one runs returns ErrInquiryBusy.
//
// override lets the caller pick agent/model/effort for this one question
// (e.g. the operator's per-request choice in the UI); any field left empty
// falls through to the configured [types.inquiry] route, then the default.
func (a *App) AskInquiry(ctx context.Context, question string, override domain.Bundle) (*Inquiry, error) {
	return a.ask(ctx, question, override, false)
}

// ask is the shared body behind AskInquiry (operator-initiated) and
// autoInquire (engine-initiated). auto records who fired it so the dashboard
// can distinguish an auto-diagnosis from a question the operator typed.
func (a *App) ask(ctx context.Context, question string, override domain.Bundle, auto bool) (*Inquiry, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return nil, fmt.Errorf("an inquiry needs a question")
	}
	if len(question) > 4000 {
		return nil, fmt.Errorf("question too long (%d chars, max 4000)", len(question))
	}

	// Route: claude/high by default; an operator [types.inquiry] entry in
	// config.toml overrides agent/model/effort like any other type route;
	// a per-request override (if any) wins over both.
	bundle := override.Overlay(a.Res().Config.Types["inquiry"].Overlay(domain.Bundle{Agent: "claude", Effort: "high"}))
	drv, err := driver.New(bundle.Agent)
	if err != nil {
		return nil, err
	}
	caps, err := drv.Probe(ctx)
	if err != nil {
		return nil, err
	}
	if caps.Capture != driver.CaptureStreaming {
		return nil, fmt.Errorf("agent %q output is not capturable — the self-evaluator needs a streaming agent (claude)", bundle.Agent)
	}

	a.inqMu.Lock()
	defer a.inqMu.Unlock()
	if a.inqCancel != nil {
		return nil, ErrInquiryBusy
	}
	a.inqSeq++
	inq := &Inquiry{ID: a.inqSeq, Question: question, State: "running", Auto: auto, Started: time.Now().UTC()}
	a.inqList = append(a.inqList, inq)
	if len(a.inqList) > inquiryHistory {
		a.inqList = a.inqList[len(a.inqList)-inquiryHistory:]
	}
	// The run outlives the HTTP request that started it.
	runCtx, cancel := context.WithTimeout(context.Background(), inquiryTimeout)
	a.inqCancel = cancel

	a.Bus.Publish(ctx, domain.Event{Type: "inquiry.asked", Pipeline: "inquiry", Payload: map[string]any{
		"id": inq.ID, "question": clip(question, 200),
	}})

	go func() {
		defer cancel()
		answer, runErr := a.runInquiry(runCtx, inq.ID, question, drv, bundle, caps)

		a.inqMu.Lock()
		inq.Ended = time.Now().UTC()
		if runErr != nil {
			inq.State = "failed"
			inq.Error = runErr.Error()
		} else {
			inq.State = "done"
			inq.Answer = answer
		}
		a.inqCancel = nil
		a.inqMu.Unlock()

		a.Bus.Publish(context.Background(), domain.Event{
			Type: "inquiry.answered", Pipeline: "inquiry", Payload: map[string]any{
				"id": inq.ID, "ok": runErr == nil, "question": clip(question, 200),
			}})
	}()
	// Return a snapshot, not the live record: the goroutine above mutates inq
	// under inqMu, and callers (the HTTP handler) marshal the result unlocked.
	// Same copying pattern as Inquiries(). Taken while inqMu is still held.
	cp := *inq
	return &cp, nil
}

// runInquiry materializes evidence, composes the forensics prompt, and drives
// one read-only agent run, streaming its output to the bus and a log file.
func (a *App) runInquiry(ctx context.Context, id int64, question string, drv driver.Driver, bundle domain.Bundle, caps driver.Capabilities) (string, error) {
	evidencePath, err := a.writeInquiryEvidence(ctx)
	if err != nil {
		return "", fmt.Errorf("inquiry evidence: %w", err)
	}
	full := prompt.ComposeInquiry(prompt.InquiryInputs{
		Mechanics: prompt.InquiryMechanics(prompt.InquiryMechanicsInputs{
			RepoDir:      a.RepoDir,
			EvidencePath: evidencePath,
			LogsDir:      filepath.Join(a.StateDir, "logs"),
		}),
		Goal:     a.Goal(),
		Question: question,
	})

	logPath := filepath.Join(a.StateDir, "logs", "inquiry", fmt.Sprintf("inquiry-%06d.log", id))
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return "", err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	defer logFile.Close()
	fmt.Fprintf(logFile, "=== inquiry %d ===\nquestion: %s\nstarted : %s\n%s\n",
		id, question, time.Now().UTC().Format(time.RFC3339), strings.Repeat("-", 72))

	plan, err := drv.Plan(driver.InvokeSpec{
		Prompt: full,
		Model:  bundle.Model,
		Effort: bundle.Effort,
		// Read-only by construction: no skip-permissions; only the
		// allowlisted read tools are grantable without an operator present.
		SkipPermissions: false,
		ExtraArgs:       []string{"--allowedTools", inquiryAllowedTools},
		SessionID:       uuid.NewString(),
		WorkDir:         a.RepoDir,
	})
	if err != nil {
		return "", err
	}
	if plan.Mode != proc.Piped {
		return "", fmt.Errorf("agent %q cannot run piped — the self-evaluator needs captured output", drv.Name())
	}

	cmd := exec.CommandContext(ctx, plan.Exe, plan.Args...)
	cmd.Dir = a.RepoDir
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return proc.KillTree(cmd.Process.Pid)
		}
		return nil
	}
	cmd.WaitDelay = 15 * time.Second
	if plan.StdinPrompt {
		cmd.Stdin = strings.NewReader(full)
	}
	// stdout is the answer (streamed live); stderr goes to the log only.
	cmd.Stderr = logFile
	pr, pw, err := os.Pipe()
	if err != nil {
		return "", err
	}
	cmd.Stdout = pw
	proc.Configure(cmd, plan.Mode)

	var answer strings.Builder
	var scanErr error
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		sc := bufio.NewScanner(pr)
		// 4 MiB line cap: agents paste whole files into answers, and a line
		// over the cap makes Scan return false — which, unhandled, would stop
		// this reader and wedge the child on a full pipe until the timeout.
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			line := sc.Text()
			fmt.Fprintln(logFile, line)
			if answer.Len() < inquiryAnswerCap {
				answer.WriteString(line)
				answer.WriteByte('\n')
			}
			a.Bus.PublishEphemeral(domain.Event{
				Type: "inquiry.log", Pipeline: "inquiry", TS: time.Now().UTC(),
				Payload: map[string]any{"id": id, "line": line},
			})
		}
		if err := sc.Err(); err != nil {
			// Scan gave up (e.g. a line beyond even the raised cap). Keep
			// draining the pipe so the child can finish writing and exit,
			// and record why so the operator sees the real cause instead of
			// a 15-minute timeout.
			scanErr = err
			fmt.Fprintf(logFile, "[hal] reading agent output failed: %v (draining remainder)\n", err)
			_, _ = io.Copy(io.Discard, pr)
		}
	}()

	if err := cmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		return "", err
	}
	proc.Adopt(cmd.Process.Pid)
	defer proc.Finished(cmd.Process.Pid)
	pw.Close() // parent's write end; child holds its own dup
	waitErr := cmd.Wait()
	var readErr error
	select {
	case <-drained:
		readErr = scanErr // safe: the goroutine is done (close happens-before)
	case <-time.After(5 * time.Second):
	}
	pr.Close()

	// Archive the evaluator's own transcript beside its log, like any pass.
	if plan.TranscriptPath != "" {
		_ = statedir.CopyFile(plan.TranscriptPath, turns.TranscriptArchivePath(logPath))
	}
	// And mirror the evidence into the [export] folder, like any pass.
	// Best-effort: an unexportable inquiry is still a valid inquiry.
	if base, berr := a.ExportBase(); berr == nil && base != "" {
		_ = export.Pass(filepath.Join(base, "inquiry"), a.Res().Config.Export.HumanReadable, logPath)
	}

	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("inquiry timed out after %s", inquiryTimeout)
	}
	if ctx.Err() == context.Canceled {
		return "", fmt.Errorf("inquiry stopped by operator")
	}
	if exit := cmd.ProcessState.ExitCode(); waitErr != nil || exit != 0 {
		return "", fmt.Errorf("agent exited %d — see %s", exit, logPath)
	}
	if readErr != nil {
		return "", fmt.Errorf("reading agent output: %v — partial output in %s", readErr, logPath)
	}
	got := strings.TrimSpace(answer.String())
	if got == "" {
		return "", fmt.Errorf("agent produced no answer — see %s", logPath)
	}
	return got, nil
}

// inquiryPass is one pass's row in evidence.json: everything the evaluator
// needs to join a commit trailer to logs, transcripts, and intent.
type inquiryPass struct {
	Pass       int64     `json:"pass"` // the id commit trailers reference
	Pipeline   string    `json:"pipeline"`
	Iter       int       `json:"iter"`
	TaskID     string    `json:"taskId,omitempty"`
	TaskTitle  string    `json:"taskTitle,omitempty"`
	Spice      string    `json:"spice,omitempty"`
	Outcome    string    `json:"outcome,omitempty"`
	Failure    string    `json:"failure,omitempty"`
	CommitSHA  string    `json:"commitSha,omitempty"`
	SessionID  string    `json:"sessionId,omitempty"`
	LogPath    string    `json:"logPath,omitempty"`
	Transcript string    `json:"transcriptPath,omitempty"` // set only when the archive exists
	Started    time.Time `json:"started"`
	Ended      time.Time `json:"ended,omitzero"`
}

// writeInquiryEvidence materializes the recent-pass digest the inquiry prompt
// points at, and returns its path.
func (a *App) writeInquiryEvidence(ctx context.Context) (string, error) {
	passes, err := a.Store.RecentPasses(ctx, "", 200)
	if err != nil {
		return "", err
	}
	titles := map[string]string{}
	rows := make([]inquiryPass, 0, len(passes))
	for _, p := range passes {
		row := inquiryPass{
			Pass: p.ID, Pipeline: p.Pipeline, Iter: p.N, TaskID: p.TaskID,
			Spice: p.Spice, Outcome: string(p.Outcome), Failure: string(p.Failure),
			CommitSHA: p.CommitSHA, SessionID: p.SessionID, LogPath: p.LogPath,
			Started: p.Started, Ended: p.Ended,
		}
		if p.TaskID != "" {
			title, ok := titles[p.TaskID]
			if !ok {
				if t, terr := a.Store.GetTask(ctx, p.TaskID); terr == nil {
					title = t.Title
				}
				titles[p.TaskID] = title
			}
			row.TaskTitle = title
		}
		if p.LogPath != "" {
			if t := turns.TranscriptArchivePath(p.LogPath); fileExists(t) {
				row.Transcript = t
			}
		}
		rows = append(rows, row)
	}
	completions, err := a.Store.ListCompletions(ctx, 50)
	if err != nil {
		return "", err
	}
	path := filepath.Join(a.StateDir, "inquiry", "evidence.json")
	doc := map[string]any{
		"generated":   time.Now().UTC(),
		"note":        "join a commit's Hal-Pass trailer to `pass` here; transcriptPath is set only where an archived transcript exists",
		"passes":      rows,
		"completions": completions,
	}
	if err := statedir.WriteJSON(path, doc); err != nil {
		return "", err
	}
	return path, nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
