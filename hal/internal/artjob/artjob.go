// Package artjob runs one-shot art-generation jobs: a frontier claude model
// ORCHESTRATES — it invokes agy (the Gemini image model) through
// `hal agy-run` to generate an asset and verifies what agy wrote. For a
// plain job that is the whole run; a transparent job continues strictly
// linearly — the orchestrator resumes the SAME agy conversation to repaint
// the background as a flat green/blue screen, then the engine keys that
// screen away with the selected remover, leaving a transparent PNG at the
// target path.
//
// This is the art pipeline ported off the retired autonomous pass loop: no
// task claiming, no routing, no breaker — one operator-triggered job, one
// pass row in the store (pipeline label "art"), evidence archived beside the
// pass log exactly like the old engine did.
package artjob

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/bus"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/chroma"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/domain"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/driver"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/statedir"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/store"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/turns"
)

// Pipeline is the passes-table label art jobs record under.
const Pipeline = "art"

// kv keys shared between art jobs, launch verification, and the dashboard's
// live art settings.
const (
	// KVArtAgyModel is the launch-verified agy model label art jobs hand to
	// agy (the first domain.ArtAgyModels entry agy actually offers).
	KVArtAgyModel = "art.agyModel"
	// KVArtAgyModels is the JSON list of every label the launch probe saw
	// (doctor/status display).
	KVArtAgyModels = "art.agyModels"
	// KVArtRemover is the legacy single-keyer live override (dashboard);
	// superseded by KVArtRemovers but still honored when only it is set.
	KVArtRemover = "art.remover"
	// KVArtRemovers is the live keyer-list override (dashboard settings
	// panel): a JSON string array, ordered, primary first. Empty/absent =
	// the [art] config value.
	KVArtRemovers = "art.removers"
)

// greenSubjectThreshold: when at least this fraction of the generated image
// is green-dominant, the subject "genuinely has lots of green" and the
// rescreen step uses a BLUE screen instead.
const greenSubjectThreshold = 0.25

// chromaTimeout bounds the keying step. The ffmpeg backend takes
// milliseconds; CorridorKey (always --device cuda — see RemoveCorridorKey)
// loads two ~400MB models per call and measured ~1 minute per image on real
// hardware (2026-07), so this is a generous ceiling for big assets and cold
// caches, not an expected duration.
const chromaTimeout = 15 * time.Minute

// chromaRemove is the keying entry point, a seam so tests can hold the run
// inside stage 3 (e.g. to cancel mid-keying) without a real backend.
var chromaRemove = chroma.Remove

// Config wires an ArtRunner.
type Config struct {
	RepoDir  string // the repository the asset lands in
	StateDir string // art's agent-facing state dir (task.json / progress.json)
	LogDir   string // per-job logs: iter-NNNNNN.log

	GoalPath   string // .hal/GOAL.md
	PromptsDir string // .hal/prompts ("" = none)

	SkipPermissions bool
	Timeout         time.Duration // per-orchestrator-invocation ceiling ([safety].wedge_minutes)

	// ArtRemovers is the configured green/blue-screen keyer list — ordered,
	// primary first; extra entries key comparison copies (kv overrides beat
	// it live). CorridorkeyDir is the CorridorKey checkout for that backend.
	ArtRemovers    []string
	CorridorkeyDir string

	// ExportDir mirrors each finished job's evidence files ("" = no export);
	// ExportHumanReadable additionally renders the transcript as markdown.
	ExportDir           string
	ExportHumanReadable bool
}

// ArtRunner executes art jobs. One at a time by convention (the caller holds
// the single-flight guard); the agy portion of each job holds the EXCLUSIVE
// agy slot regardless.
type ArtRunner struct {
	Store *store.Store
	Bus   *bus.Bus
	// AgyMu is the process-wide agy serialization lock: the conversation id
	// is recovered from agy's per-workdir last_conversations.json, a
	// whole-file-rewritten record that any concurrent agy -p instance races.
	// Art jobs take the WRITE lock across their whole multi-invocation
	// sequence; probes take the read lock. nil = unlocked.
	AgyMu *sync.RWMutex

	cfg Config
	drv driver.Driver // orchestrator driver; nil = claude, tests inject fake
}

// New builds an ArtRunner.
func New(st *store.Store, b *bus.Bus, agyMu *sync.RWMutex, cfg Config) *ArtRunner {
	return &ArtRunner{Store: st, Bus: b, AgyMu: agyMu, cfg: cfg}
}

// SetDriver injects the orchestrator driver — the seam tests use to
// substitute the fake driver for the real claude CLI.
func (r *ArtRunner) SetDriver(d driver.Driver) { r.drv = d }

// Job is one art-generation request.
type Job struct {
	// Description is the operator's ask, verbatim — what the asset should
	// depict (style, palette, subject). The first line doubles as the title.
	Description string
	// Target is the repo-relative asset path ("" = assets/art/<slug>.png).
	Target string
	// Transparent selects the art-gen-trans flow: rescreen + chroma key to a
	// transparent PNG.
	Transparent bool
}

// title derives the job's display title: the first non-empty line of the
// description, clipped.
func (j Job) title() string {
	for _, line := range strings.Split(j.Description, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			if r := []rune(line); len(r) > 80 {
				return string(r[:79]) + "…"
			}
			return line
		}
	}
	return "art asset"
}

// task builds the synthetic task the prompt/state files describe the job as.
func (j Job) task(passID int64) *domain.Task {
	typ := domain.ArtGenType
	if j.Transparent {
		typ = domain.ArtGenTransType
	}
	t := &domain.Task{
		ID:     fmt.Sprintf("art-job-%d", passID),
		Type:   typ,
		Title:  j.title(),
		Detail: j.Description,
		Origin: domain.OriginOperator,
	}
	if strings.TrimSpace(j.Target) != "" {
		t.Files = []string{strings.TrimSpace(j.Target)}
	}
	return t
}

// StartPass mints the job's pass row (pipeline "art", per-pipeline iteration
// counter preserved) so the caller can hand its id back before the run
// itself proceeds asynchronously.
func (r *ArtRunner) StartPass(ctx context.Context) (*domain.Pass, error) {
	return r.Store.StartPass(ctx, Pipeline)
}

// Run executes one art job against a pass row minted by StartPass. The error
// reports why the job failed; every outcome — success or failure — is also
// recorded on the pass row and the event bus.
func (r *ArtRunner) Run(ctx context.Context, pass *domain.Pass, job Job) error {
	trans := job.Transparent
	task := job.task(pass.ID)

	drv, caps, bundle, err := r.resolveArt(ctx)
	if err != nil {
		return r.failSetup(ctx, pass, err)
	}
	agyModel := r.artAgyModel(ctx)
	sessionID := ""
	if caps.Sessions {
		sessionID = uuid.NewString()
		r.patchPass(ctx, pass.ID, store.PassPatch{SessionID: &sessionID})
	}

	target, err := artTargetPath(task)
	if err != nil {
		return r.failSetup(ctx, pass, err)
	}
	targetAbs := filepath.Join(r.cfg.RepoDir, filepath.FromSlash(target))

	if err := statedir.Materialize(r.cfg.StateDir, task); err != nil {
		return r.failSetup(ctx, pass, err)
	}

	logPath := filepath.Join(r.cfg.LogDir, fmt.Sprintf("iter-%06d.log", pass.N))
	r.patchPass(ctx, pass.ID, store.PassPatch{LogPath: &logPath})
	logFile, err := r.openPassLog(logPath, pass, task, bundle, sessionID)
	if err != nil {
		return r.failSetup(ctx, pass, err)
	}
	// Registered before Close's defer so the export runs AFTER the log is
	// closed (and after the screen-png archive) — the mirror is complete.
	defer r.exportPass(ctx, pass, logPath)
	defer logFile.Close()

	r.setState(ctx, pass, domain.PassRunning)
	r.event(ctx, "pass.started", pass.ID, map[string]any{
		"n": pass.N, "task": task.Title, "art": true, "type": task.Type, "target": target,
		"agent": bundle.Agent, "model": bundle.Model, "agyModel": agyModel,
	})

	// Exclusive agy slot for the whole generate(+rescreen) sequence.
	unlockAgy := func() {}
	if r.AgyMu != nil {
		unlock, lerr := lockCtx(ctx, r.AgyMu)
		if lerr != nil {
			r.finishPass(ctx, pass, domain.PassFailed, domain.OutcomeFailed, domain.FailSetup, -1, "")
			return lerr
		}
		var once sync.Once
		unlockAgy = func() { once.Do(unlock) }
	}
	defer unlockAgy()

	// The pre-read that disambiguates "which conversation did OUR run
	// create": taken under the exclusive slot, so any change to the
	// workdir's entry between here and the post-read is ours.
	prevConv, _ := driver.AgyLastConversation(r.cfg.RepoDir)

	exitCode := -1
	fail := func(kind domain.FailKind, why string) error {
		r.event(ctx, "art.attempt_failed", pass.ID, map[string]any{"why": why, "target": target})
		r.finishPass(ctx, pass, domain.PassFailed, domain.OutcomeFailed, kind, exitCode, "")
		return fmt.Errorf("artjob: %s", why)
	}
	// blocked concludes the job cleanly on an agent self-report: the agent
	// ran fine — the job just cannot be done (agy missing, repeated refusals).
	blocked := func(progress domain.Progress) error {
		r.event(ctx, "art.blocked", pass.ID, map[string]any{
			"task": task.Title, "phase": progress.Phase, "note": progress.Note,
		})
		r.finishPass(ctx, pass, domain.PassDone, domain.PassOutcome(progress.Phase), domain.FailNone, exitCode, "")
		return fmt.Errorf("artjob: agent reported %s: %s", progress.Phase, progress.Note)
	}
	cancelled := func() error {
		// The job's ctx is already dead — the closing bookkeeping (pass row,
		// events) must still land, so it runs on a detached context.
		r.finishPass(context.WithoutCancel(ctx), pass, domain.PassFailed, domain.OutcomeFailed, domain.FailSetup, exitCode, "")
		return ctx.Err()
	}

	// Stage 1: generate the asset — the orchestrator directs agy.
	fmt.Fprintf(logFile, "art stage 1: generate %s (orchestrator %s, image model %s)\n", target, bundle.Model, agyModel)
	var timedOut bool
	var runErr error
	genPrompt := r.artGenPrompt(task, target, trans, r.artAgyCommand(agyModel, "", logPath+".agy.op.log"))
	exitCode, timedOut, runErr = r.spawn(ctx, pass, drv, caps, bundle, genPrompt, logPath, logFile, sessionID, "")
	if ctx.Err() != nil {
		return cancelled()
	}
	if err := r.artRunFailure(ctx, pass, fail, exitCode, timedOut, runErr); err != nil {
		return err
	}
	progress := statedir.ReadProgress(r.cfg.StateDir)
	switch progress.Phase {
	case "done":
	case "blocked", "reverted":
		return blocked(progress)
	default:
		return fail(domain.FailExit, "no final self-report from the generation step")
	}
	if _, err := os.Stat(targetAbs); err != nil {
		return fail(domain.FailExit, fmt.Sprintf("agent reported done but %s does not exist", target))
	}
	// The asset must come FROM agy: a done-report with no new conversation
	// record means the orchestrator fabricated (or copied) the file instead
	// of running agy from the repository root. -trans additionally needs the
	// id to resume the conversation for the rescreen.
	conv, cerr := driver.AgyLastConversation(r.cfg.RepoDir)
	if cerr != nil {
		return fail(domain.FailSetup, "agy conversation lookup: "+cerr.Error())
	}
	if conv == "" || conv == prevConv {
		return fail(domain.FailExit, "no new agy conversation recorded for this workdir — the image must be generated by agy (via `hal agy-run`, from the repository root)")
	}
	if err := r.artNormalize(ctx, pass, fail, logFile, targetAbs, target); err != nil {
		return err
	}
	r.event(ctx, "art.generated", pass.ID, map[string]any{"target": target, "conversation": conv})

	result := "generated " + target
	if !trans {
		archiveAgyTranscript(conv, logPath)
	} else {
		// Stage 2: rescreen — the orchestrator resumes the SAME agy
		// conversation (the id rides in the command template).
		key := chroma.KeyGreen
		if frac, ferr := chroma.FractionKeyish(targetAbs, chroma.KeyGreen); ferr == nil && frac > greenSubjectThreshold {
			key = chroma.KeyBlue // the sprite is genuinely green — blue screen keys cleaner
		}
		screen := screenPath(target)
		screenAbs := filepath.Join(r.cfg.RepoDir, filepath.FromSlash(screen))
		if err := statedir.WriteJSON(filepath.Join(r.cfg.StateDir, statedir.ProgressFile), domain.Progress{}); err != nil {
			return fail(domain.FailSetup, "reset progress.json: "+err.Error())
		}
		// The rescreen is a SECOND orchestrator run: it needs its own session
		// id (claude refuses a reused one) and its own op-log/transcript stem
		// so stage 1's evidence is not overwritten.
		sessionID2 := ""
		if caps.Sessions {
			sessionID2 = uuid.NewString()
			fmt.Fprintf(logFile, "rescreen session: %s\n", sessionID2)
		}
		rescreenLog := strings.TrimSuffix(logPath, ".log") + ".rescreen.log"
		fmt.Fprintf(logFile, "\nart stage 2: rescreen %s (agy conversation %s) -> %s\n", key, conv, screen)
		rescreenPrompt := r.artRescreenPrompt(target, screen, key, r.artAgyCommand(agyModel, conv, rescreenLog+".agy.op.log"))
		exitCode, timedOut, runErr = r.spawn(ctx, pass, drv, caps, bundle, rescreenPrompt, rescreenLog, logFile, sessionID2, "")
		if ctx.Err() != nil {
			return cancelled()
		}
		if err := r.artRunFailure(ctx, pass, fail, exitCode, timedOut, runErr); err != nil {
			return err
		}
		progress = statedir.ReadProgress(r.cfg.StateDir)
		switch progress.Phase {
		case "done":
		case "blocked", "reverted":
			return blocked(progress)
		default:
			return fail(domain.FailExit, "no final self-report from the rescreen step")
		}
		if _, err := os.Stat(screenAbs); err != nil {
			return fail(domain.FailExit, fmt.Sprintf("agent reported done but %s does not exist", screen))
		}
		// Archive AFTER the resume so the transcript holds both stages.
		archiveAgyTranscript(conv, logPath)
		unlockAgy() // agy work is over; verification and keying are local
		if err := r.artNormalize(ctx, pass, fail, logFile, screenAbs, screen); err != nil {
			return err
		}
		r.event(ctx, "art.rescreened", pass.ID, map[string]any{"screen": screen, "key": key.String()})

		// Stage 3: key the screen away — transparent PNG lands at target.
		// With more than one keyer selected the FIRST (primary) produces the
		// committed asset; the rest key the same screen into comparison files
		// beside the pass log (see compareKeyers).
		removers, ckDir := r.artRemovers(ctx)
		primary := removers[0]
		fmt.Fprintf(logFile, "art stage 3: remove %s screen via %s\n", key, primary)
		kctx, cancel := context.WithTimeout(ctx, chromaTimeout)
		kerr := chromaRemove(kctx, primary, ckDir, screenAbs, targetAbs, key)
		cancel()
		if ctx.Err() != nil {
			// Shutdown mid-keying, not a keying failure. (A chromaTimeout
			// expiry leaves ctx alive and stays a real failure.)
			return cancelled()
		}
		if kerr != nil {
			return fail(domain.FailExit, kerr.Error())
		}
		if verr := chroma.VerifyTransparency(targetAbs); verr != nil {
			return fail(domain.FailExit, verr.Error())
		}
		if len(removers) > 1 {
			r.compareKeyers(ctx, pass, logFile, logPath, screenAbs, targetAbs, target, removers, ckDir, key)
		}
		// The intermediate screen image is forensic material, not an asset:
		// archive it next to the pass log, out of the repository.
		_ = statedir.CopyFile(screenAbs, strings.TrimSuffix(logPath, ".log")+".screen.png")
		_ = os.Remove(screenAbs)
		r.event(ctx, "art.keyed", pass.ID, map[string]any{"target": target, "remover": primary, "key": key.String()})
		result = fmt.Sprintf("generated %s with a transparent background (%s screen keyed via %s)", target, key, primary)
		if len(removers) > 1 {
			result += fmt.Sprintf("; %d comparison keyings archived beside the pass log", len(removers)-1)
		}
	}

	if progress.Decisions != "" {
		fmt.Fprintf(logFile, "\n--- decisions ---\n%s\n", progress.Decisions)
	}
	sha := r.commitIfDirty(ctx, pass, task)
	_ = r.Store.AddCompletion(ctx, &domain.Completion{Pipeline: Pipeline, Title: task.Title, Result: result})
	r.event(ctx, "art.done", pass.ID, map[string]any{"title": task.Title, "result": result})
	r.finishPass(ctx, pass, domain.PassDone, domain.OutcomeDone, domain.FailNone, exitCode, sha)
	return nil
}

// artRunFailure funnels one orchestrator invocation's process-level failures
// (spawn error, wedge, nonzero exit) through the pass bookkeeping. nil when
// the invocation succeeded.
func (r *ArtRunner) artRunFailure(ctx context.Context, pass *domain.Pass, fail func(domain.FailKind, string) error, exitCode int, timedOut bool, runErr error) error {
	switch {
	case runErr != nil:
		return r.failSetup(ctx, pass, runErr)
	case timedOut:
		r.event(ctx, "wedge.killed", pass.ID, map[string]any{"timeoutMin": int(r.cfg.Timeout.Minutes())})
		return fail(domain.FailTimeout, "wedged")
	case exitCode != 0:
		return fail(domain.FailExit, fmt.Sprintf("agent exit %d", exitCode))
	}
	return nil
}

// artNormalize verifies that the image agy just wrote genuinely contains PNG
// bytes (the extension proves nothing — image models have emitted JPEG/WebP
// bytes under a .png name) and re-encodes it as a PNG in place when it does
// not. An undecodable file fails the job.
func (r *ArtRunner) artNormalize(ctx context.Context, pass *domain.Pass, fail func(domain.FailKind, string) error, logFile *os.File, abs, rel string) error {
	format, converted, err := chroma.EnsurePNG(abs)
	if err != nil {
		return fail(domain.FailExit, err.Error())
	}
	if converted {
		fmt.Fprintf(logFile, "art verify: %s held %s bytes — re-encoded as PNG\n", rel, format)
		r.event(ctx, "art.normalized", pass.ID, map[string]any{"path": rel, "from": format})
	}
	return nil
}

// resolveArt resolves the orchestrator: claude with a frontier model — art
// jobs are claude-orchestrated BY DEFINITION (the run invokes agy for the
// actual image generation via `hal agy-run`).
func (r *ArtRunner) resolveArt(ctx context.Context) (driver.Driver, driver.Capabilities, domain.Bundle, error) {
	drv := r.drv
	if drv == nil {
		var err error
		if drv, err = driver.New("claude"); err != nil {
			return nil, driver.Capabilities{}, domain.Bundle{}, err
		}
		r.drv = drv
	}
	caps, err := drv.Probe(ctx)
	if err != nil {
		return nil, driver.Capabilities{}, domain.Bundle{}, err
	}
	return drv, caps, domain.Bundle{Agent: "claude", Model: domain.ArtClaudeDefault}, nil
}

// artAgyModel resolves the agy image-model label the orchestrator is told to
// run: the launch-verified label (KVArtAgyModel), else the preferred default.
func (r *ArtRunner) artAgyModel(ctx context.Context) string {
	model, _ := r.Store.GetKV(ctx, KVArtAgyModel)
	if !domain.AllowedArtModel(model) {
		model = domain.ArtAgyModels[0]
	}
	return model
}

// artAgyCommand renders the exact agy invocation the orchestrator must use.
// It goes through `hal agy-run` (this very binary) — agy silently drops
// output and can hang when spawned from a shell with piped stdio, so a bare
// `agy` from the orchestrator's shell tool is never safe. conversation, when
// set, resumes that agy conversation (the -trans rescreen).
func (r *ArtRunner) artAgyCommand(agyModel, conversation, opLog string) string {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		exe = "hal"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\"%s\" agy-run", exe)
	if r.cfg.SkipPermissions {
		b.WriteString(" --dangerously-skip-permissions")
	}
	b.WriteString(" --print-timeout 30m")
	if conversation != "" {
		fmt.Fprintf(&b, " --conversation %s", conversation)
	}
	fmt.Fprintf(&b, " --model \"%s\" --log-file \"%s\" -p \"<your image prompt>\"", agyModel, opLog)
	return b.String()
}

// spawn runs one orchestrator invocation and streams/captures its output.
// conversationID resumes a previous runtime conversation ("" = fresh).
func (r *ArtRunner) spawn(ctx context.Context, pass *domain.Pass, drv driver.Driver, caps driver.Capabilities, bundle domain.Bundle, fullPrompt, logPath string, logFile *os.File, sessionID, conversationID string) (exitCode int, timedOut bool, err error) {
	plan, err := drv.Plan(driver.InvokeSpec{
		Prompt:          fullPrompt,
		Model:           bundle.Model,
		Effort:          bundle.Effort,
		SkipPermissions: r.cfg.SkipPermissions,
		OpLogPath:       logPath + ".op.log",
		SessionID:       sessionID,
		WorkDir:         r.cfg.RepoDir,
		ConversationID:  conversationID,
	})
	if err != nil {
		return -1, false, err
	}

	// Conversation-transcript runtimes key their transcript by a
	// runtime-minted id only discoverable AFTER the run, via the per-workdir
	// last_conversations.json record. Pre-read it so the post-run read can
	// tell whether OUR run minted a new entry.
	prevConv := ""
	if plan.TranscriptPath == "" && caps.ConversationTranscript && conversationID == "" {
		prevConv, _ = driver.AgyLastConversation(r.cfg.RepoDir)
	}

	res, err := turns.Exec(ctx, turns.ExecSpec{
		Plan:    plan,
		Prompt:  fullPrompt,
		Dir:     r.cfg.RepoDir,
		Timeout: r.cfg.Timeout,
		ExtraEnv: []string{
			"HAL_PASS_STATE_DIR=" + r.cfg.StateDir,
			"HAL_PASS_REPO_DIR=" + r.cfg.RepoDir,
			fmt.Sprintf("HAL_PASS_N=%d", pass.N),
		},
		LogFile: logFile,
		OnLine: func(line string) {
			r.Bus.PublishEphemeral(domain.Event{
				Type: "pass.log", Pipeline: Pipeline, Pass: pass.ID,
				TS: time.Now().UTC(), Payload: map[string]any{"line": line},
			})
		},
	})
	if err != nil {
		return -1, false, err
	}

	// Archive the runtime's own full transcript next to the pass log —
	// runtimes prune their session stores, this copy is ours. Best-effort.
	if plan.TranscriptPath != "" {
		_ = statedir.CopyFile(plan.TranscriptPath, turns.TranscriptArchivePath(logPath))
	} else if caps.ConversationTranscript {
		archiveConversationTranscript(r.cfg.RepoDir, conversationID, prevConv, logPath)
	}
	return res.ExitCode, res.TimedOut, nil
}

// archiveConversationTranscript archives a conversation-transcript runtime's
// (agy's) transcript next to the pass log. A resumed run knows its id up
// front; a fresh run discovers it from last_conversations.json, and only a
// NEW entry counts. Best-effort twice over: the record is rewritten WHOLE per
// agy run, and agy may prune its own brain dir.
func archiveConversationTranscript(workDir, conversationID, prevConv, logPath string) {
	conv := conversationID
	if conv == "" {
		cur, err := driver.AgyLastConversation(workDir)
		if err != nil || cur == "" || cur == prevConv {
			return
		}
		conv = cur
	}
	src, err := driver.AgyTranscriptPath(conv)
	if err != nil {
		return
	}
	_ = statedir.CopyFile(src, turns.TranscriptArchivePath(logPath))
}

// archiveAgyTranscript archives agy's own brain transcript for conversation
// conv beside the pass log as iter-NNNNNN.agy.transcript.jsonl — the
// orchestrator's claude session transcript is archived separately by spawn.
// Best-effort: agy owns its brain dir and may have pruned the conversation.
func archiveAgyTranscript(conv, logPath string) {
	src, err := driver.AgyTranscriptPath(conv)
	if err != nil {
		return
	}
	_ = statedir.CopyFile(src, strings.TrimSuffix(logPath, ".log")+".agy.transcript.jsonl")
}

// artRemovers resolves the effective green/blue-screen keyer list, ordered
// and deduped, primary first — never empty. Live kv overrides (dashboard
// settings, re-read EVERY use) beat the configured list: the JSON-array
// KVArtRemovers first, then the legacy single-value KVArtRemover, then
// config. Unknown names are dropped defensively; an all-invalid list falls
// back to the default backend.
func (r *ArtRunner) artRemovers(ctx context.Context) (removers []string, corridorkeyDir string) {
	var picked []string
	if raw, err := r.Store.GetKV(ctx, KVArtRemovers); err == nil && raw != "" {
		_ = json.Unmarshal([]byte(raw), &picked)
	}
	if len(picked) == 0 {
		if v, err := r.Store.GetKV(ctx, KVArtRemover); err == nil && v != "" {
			picked = []string{v}
		}
	}
	if len(picked) == 0 {
		picked = r.cfg.ArtRemovers
	}
	seen := map[string]bool{}
	for _, k := range picked {
		if k != "" && chroma.ValidRemover(k) && !seen[k] {
			seen[k] = true
			removers = append(removers, k)
		}
	}
	if len(removers) == 0 {
		removers = []string{chroma.Removers[0]}
	}
	return removers, r.cfg.CorridorkeyDir
}

// compareKeyers runs every non-primary keyer over the same screened
// intermediate, purely as evidence for the operator: each result (and a copy
// of the primary's) lands beside the pass log as "<stem>.keyed-<keyer>.png"
// so a human can compare backends file by file. Best-effort by design: a
// failure is logged and evented but never fails the job.
func (r *ArtRunner) compareKeyers(ctx context.Context, pass *domain.Pass, logFile *os.File, logPath, screenAbs, targetAbs, target string, removers []string, ckDir string, key chroma.Key) {
	stem := strings.TrimSuffix(logPath, ".log")
	results := make([]map[string]any, 0, len(removers))
	// The primary's output is already verified at the target; archive a copy
	// under the same naming scheme so the comparison set is complete.
	if err := statedir.CopyFile(targetAbs, stem+".keyed-"+removers[0]+".png"); err == nil {
		results = append(results, map[string]any{"keyer": removers[0], "ok": true, "primary": true})
	}
	for _, remover := range removers[1:] {
		if ctx.Err() != nil {
			return // shutdown — the job already has its asset
		}
		out := stem + ".keyed-" + remover + ".png"
		fmt.Fprintf(logFile, "art stage 3+: comparison keyer %s -> %s\n", remover, filepath.Base(out))
		kctx, cancel := context.WithTimeout(ctx, chromaTimeout)
		kerr := chromaRemove(kctx, remover, ckDir, screenAbs, out, key)
		cancel()
		if kerr == nil {
			if verr := chroma.VerifyTransparency(out); verr != nil {
				kerr = verr
			}
		}
		if kerr != nil {
			fmt.Fprintf(logFile, "art stage 3+: comparison keyer %s FAILED: %v\n", remover, kerr)
			results = append(results, map[string]any{"keyer": remover, "ok": false, "error": kerr.Error()})
			r.event(ctx, "art.keyer_compare_failed", pass.ID, map[string]any{
				"target": target, "keyer": remover, "error": kerr.Error(),
			})
			continue
		}
		results = append(results, map[string]any{"keyer": remover, "ok": true})
	}
	r.event(ctx, "art.keyer_compare", pass.ID, map[string]any{
		"target": target, "keyers": results, "files": filepath.Base(stem) + ".keyed-<keyer>.png",
	})
}

// artTargetPath derives the repo-relative asset path: the task's first files
// entry when given, else assets/art/<slug>.png. Task files are operator
// data — anything non-local is refused, never resolved. The extension is
// always .png: the flow only ever produces PNGs (the generation prompt says
// PNG, ffmpeg picks its encoder by extension, and JPEG cannot hold the alpha
// channel -trans exists for).
func artTargetPath(task *domain.Task) (string, error) {
	if len(task.Files) > 0 && strings.TrimSpace(task.Files[0]) != "" {
		// Backslashes are separators on EVERY platform: the engine (Windows)
		// and CI (Linux) must agree on what gets refused, and Linux's
		// filepath treats `C:\evil.png` as one harmless local file name.
		p := strings.ReplaceAll(strings.TrimSpace(task.Files[0]), `\`, "/")
		// Colons cover drive letters and NTFS alternate data streams; a
		// leading slash covers rooted and (post-replacement) UNC paths.
		if strings.ContainsRune(p, ':') || strings.HasPrefix(p, "/") ||
			!filepath.IsLocal(filepath.FromSlash(p)) {
			return "", fmt.Errorf("artjob: art target %q escapes the repository", task.Files[0])
		}
		p = path.Clean(p)
		// An asset has no business under .git (same guard family as the July
		// path-traversal hardening).
		for _, seg := range strings.Split(p, "/") {
			if strings.EqualFold(seg, ".git") {
				return "", fmt.Errorf("artjob: art target %q is inside .git", task.Files[0])
			}
		}
		if ext := path.Ext(p); !strings.EqualFold(ext, ".png") {
			p = strings.TrimSuffix(p, ext) + ".png"
		}
		return p, nil
	}
	return "assets/art/" + artSlug(task.Title) + ".png", nil
}

// artSlug turns a title into a filesystem-safe kebab-case stem.
func artSlug(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 60 {
		s = strings.Trim(s[:60], "-")
	}
	if s == "" {
		s = "asset"
	}
	return s
}

// screenPath is the intermediate rescreened image: "<stem>-screen.png".
func screenPath(target string) string {
	return strings.TrimSuffix(target, path.Ext(target)) + "-screen.png"
}
