package engine

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

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/chroma"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/driver"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/prompt"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/statedir"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/store"
)

// kv keys shared between the engine (art passes), launch verification, and
// the dashboard's live art settings.
const (
	// KVArtAgyModel is the launch-verified agy model label art passes hand
	// to agy (the first domain.ArtAgyModels entry agy actually offers).
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
// caches, not an expected duration. On CPU it measured 2+ HOURS per image,
// which is why the backend refuses to run there at all.
const chromaTimeout = 15 * time.Minute

// chromaRemove is the keying entry point, a seam so tests can hold the pass
// inside stage 3 (e.g. to cancel the engine mid-keying) without a real
// backend.
var chromaRemove = chroma.Remove

// runArtPass executes an art-generation task: a frontier claude model (see
// domain.ArtClaudeModels) ORCHESTRATES — it invokes agy (the Gemini image
// model) through `workshop agy-run` to generate the asset, and verifies what
// agy wrote. For art-gen that is the whole pass; art-gen-trans continues
// strictly linearly — the orchestrator resumes the SAME agy conversation to
// repaint the background as a flat green/blue screen, then the engine keys
// that screen away with the selected remover, leaving a transparent PNG at
// the target path. Every image agy writes is byte-verified (and re-encoded
// when mislabeled) before downstream steps trust it — see artNormalize. A
// stage that leaves no NEW agy conversation record fails: the image must
// come from agy, never be fabricated by the orchestrator.
//
// The whole agy portion holds the EXCLUSIVE agy slot (WorkerConfig.AgyMu):
// the conversation id is recovered from agy's per-workdir
// last_conversations.json, a whole-file-rewritten record that any concurrent
// agy -p instance — including one under another pass's orchestrator — races.
// Ordinary claude passes are unaffected.
func (w *Worker) runArtPass(ctx context.Context, pass *domain.Pass, task *domain.Task, routed *resolved, sessionID string) (PassResult, error) {
	name := w.cfg.Pipeline.Name
	trans := task.Type == domain.ArtGenTransType

	res, err := w.resolveArt(ctx, routed)
	if err != nil {
		return w.failSetup(ctx, pass, task, err)
	}
	agyModel := w.artAgyModel(ctx, routed)
	// RunPass mints session ids from the ROUTED driver's capabilities; the
	// art pass may have swapped driver families (e.g. an agy-pinned task), so
	// mint one here when the orchestrator records sessions and routing didn't.
	if sessionID == "" && res.caps.Sessions {
		sessionID = uuid.NewString()
		w.patchPass(ctx, pass.ID, store.PassPatch{SessionID: &sessionID})
	}

	target, err := artTargetPath(task)
	if err != nil {
		return w.failSetup(ctx, pass, task, err)
	}
	targetAbs := filepath.Join(w.cfg.RepoDir, filepath.FromSlash(target))

	snapshot, _ := w.bl.Snapshot(ctx)
	completions, _ := w.st.ListCompletions(ctx, 10)
	if err := statedir.Materialize(w.cfg.StateDir, task, snapshot, completions); err != nil {
		return w.failSetup(ctx, pass, task, err)
	}

	logPath := filepath.Join(w.cfg.LogDir, fmt.Sprintf("iter-%06d.log", pass.N))
	w.patchPass(ctx, pass.ID, store.PassPatch{LogPath: &logPath})
	logFile, err := w.openPassLog(logPath, pass, task, res, prompt.Spice{}, "", sessionID)
	if err != nil {
		return w.failSetup(ctx, pass, task, err)
	}
	// Registered before Close's defer so the export runs AFTER the log is
	// closed (and after the screen-png archive) — the mirror is complete.
	defer w.exportPass(ctx, pass, logPath)
	defer logFile.Close()

	w.setState(ctx, pass, domain.PassRunning)
	w.event(ctx, "pass.started", name, pass.ID, map[string]any{
		"n": pass.N, "task": task.Title, "art": true, "type": task.Type, "target": target,
		"agent": res.bundle.Agent, "model": res.bundle.Model, "agyModel": agyModel,
	})

	// Exclusive agy slot for the whole generate(+rescreen) sequence.
	unlockAgy := func() {}
	if w.cfg.AgyMu != nil {
		unlock, lerr := lockCtx(ctx, w.cfg.AgyMu)
		if lerr != nil {
			_ = w.st.ReleaseTask(ctx, task.ID)
			w.finishPass(ctx, pass, domain.PassFailed, domain.OutcomeFailed, domain.FailSetup, -1, "")
			return PassRan, lerr
		}
		var once sync.Once
		unlockAgy = func() { once.Do(unlock) }
	}
	defer unlockAgy()

	// The pre-read that disambiguates "which conversation did OUR run
	// create": taken under the exclusive slot, so any change to the
	// workdir's entry between here and the post-read is ours.
	prevConv, _ := driver.AgyLastConversation(w.cfg.RepoDir)

	exitCode := -1
	fail := func(kind domain.FailKind, why string) (PassResult, error) {
		w.commitFailedWork(ctx, pass, task, res)
		w.failTask(ctx, task)
		w.event(ctx, "art.attempt_failed", name, pass.ID, map[string]any{"why": why, "target": target})
		w.finishPass(ctx, pass, domain.PassFailed, domain.OutcomeFailed, kind, exitCode, "")
		return w.bumpBreaker(ctx, pass)
	}
	// blocked concludes the pass cleanly on an agent self-report (blocked/
	// reverted): the agent ran fine — the task takes the failure, not the
	// breaker.
	blocked := func(progress domain.Progress) (PassResult, error) {
		w.failTask(ctx, task)
		w.event(ctx, "task.failed", name, pass.ID, map[string]any{
			"task": task.Title, "phase": progress.Phase, "note": progress.Note,
		})
		w.consecFails = 0
		w.consecNoReport = 0
		w.finishPass(ctx, pass, domain.PassDone, domain.PassOutcome(progress.Phase), domain.FailNone, exitCode, "")
		return PassRan, nil
	}

	// Stage 1: generate the asset — the orchestrator directs agy.
	fmt.Fprintf(logFile, "art stage 1: generate %s (orchestrator %s, image model %s)\n", target, res.bundle.Model, agyModel)
	var tail []string
	var timedOut bool
	var runErr error
	genPrompt := w.artGenPrompt(task, target, trans, w.artAgyCommand(agyModel, "", logPath+".agy.op.log"))
	exitCode, tail, timedOut, runErr = w.spawn(ctx, pass, res, genPrompt, w.cfg.RepoDir, logPath, logFile, sessionID, "")
	if ctx.Err() != nil {
		_ = w.st.ReleaseTask(ctx, task.ID)
		w.finishPass(ctx, pass, domain.PassFailed, domain.OutcomeFailed, domain.FailSetup, exitCode, "")
		return PassRan, ctx.Err()
	}
	if res2, err2, handled := w.artRunFailure(ctx, pass, res, fail, exitCode, tail, timedOut, runErr, task); handled {
		return res2, err2
	}
	progress := statedir.ReadProgress(w.cfg.StateDir)
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
	conv, cerr := driver.AgyLastConversation(w.cfg.RepoDir)
	if cerr != nil {
		return fail(domain.FailSetup, "agy conversation lookup: "+cerr.Error())
	}
	if conv == "" || conv == prevConv {
		return fail(domain.FailExit, "no new agy conversation recorded for this workdir — the image must be generated by agy (via `workshop agy-run`, from the repository root)")
	}
	if res2, err2, handled := w.artNormalize(ctx, pass, fail, logFile, targetAbs, target); handled {
		return res2, err2
	}
	w.event(ctx, "art.generated", name, pass.ID, map[string]any{"target": target, "conversation": conv})

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
		screenAbs := filepath.Join(w.cfg.RepoDir, filepath.FromSlash(screen))
		if err := statedir.WriteJSON(filepath.Join(w.cfg.StateDir, statedir.ProgressFile), domain.Progress{}); err != nil {
			return fail(domain.FailSetup, "reset progress.json: "+err.Error())
		}
		// The rescreen is a SECOND orchestrator run: it needs its own session
		// id (claude refuses a reused one) and its own op-log/transcript stem
		// so stage 1's evidence is not overwritten.
		sessionID2 := ""
		if res.caps.Sessions {
			sessionID2 = uuid.NewString()
			fmt.Fprintf(logFile, "rescreen session: %s\n", sessionID2)
		}
		rescreenLog := strings.TrimSuffix(logPath, ".log") + ".rescreen.log"
		fmt.Fprintf(logFile, "\nart stage 2: rescreen %s (agy conversation %s) -> %s\n", key, conv, screen)
		rescreenPrompt := w.artRescreenPrompt(target, screen, key, w.artAgyCommand(agyModel, conv, rescreenLog+".agy.op.log"))
		exitCode, tail, timedOut, runErr = w.spawn(ctx, pass, res, rescreenPrompt, w.cfg.RepoDir, rescreenLog, logFile, sessionID2, "")
		if ctx.Err() != nil {
			_ = w.st.ReleaseTask(ctx, task.ID)
			w.finishPass(ctx, pass, domain.PassFailed, domain.OutcomeFailed, domain.FailSetup, exitCode, "")
			return PassRan, ctx.Err()
		}
		if res2, err2, handled := w.artRunFailure(ctx, pass, res, fail, exitCode, tail, timedOut, runErr, task); handled {
			return res2, err2
		}
		progress = statedir.ReadProgress(w.cfg.StateDir)
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
		if res2, err2, handled := w.artNormalize(ctx, pass, fail, logFile, screenAbs, screen); handled {
			return res2, err2
		}
		w.event(ctx, "art.rescreened", name, pass.ID, map[string]any{"screen": screen, "key": key.String()})

		// Stage 3: key the screen away — transparent PNG lands at target.
		// With more than one keyer selected the FIRST (primary) produces the
		// committed asset; the rest key the same screen into comparison files
		// beside the pass log (see compareKeyers).
		removers, ckDir := w.artRemovers(ctx)
		primary := removers[0]
		fmt.Fprintf(logFile, "art stage 3: remove %s screen via %s\n", key, primary)
		kctx, cancel := context.WithTimeout(ctx, chromaTimeout)
		kerr := chromaRemove(kctx, primary, ckDir, screenAbs, targetAbs, key)
		cancel()
		if ctx.Err() != nil {
			// Engine shutdown mid-keying, not a keying failure: conclude the
			// same way as a cancellation during either agy stage — no task
			// attempt burned, nothing counted toward the breaker. (A
			// chromaTimeout expiry leaves ctx alive and stays a real failure.)
			_ = w.st.ReleaseTask(ctx, task.ID)
			w.finishPass(ctx, pass, domain.PassFailed, domain.OutcomeFailed, domain.FailSetup, exitCode, "")
			return PassRan, ctx.Err()
		}
		if kerr != nil {
			return fail(domain.FailExit, kerr.Error())
		}
		if verr := chroma.VerifyTransparency(targetAbs); verr != nil {
			return fail(domain.FailExit, verr.Error())
		}
		if len(removers) > 1 {
			w.compareKeyers(ctx, pass, logFile, logPath, screenAbs, targetAbs, target, removers, ckDir, key)
		}
		// The intermediate screen image is forensic material, not an asset:
		// archive it next to the pass log, out of the repository.
		_ = statedir.CopyFile(screenAbs, strings.TrimSuffix(logPath, ".log")+".screen.png")
		_ = os.Remove(screenAbs)
		w.event(ctx, "art.keyed", name, pass.ID, map[string]any{"target": target, "remover": primary, "key": key.String()})
		result = fmt.Sprintf("generated %s with a transparent background (%s screen keyed via %s)", target, key, primary)
		if len(removers) > 1 {
			result += fmt.Sprintf("; %d comparison keyings archived beside the pass log", len(removers)-1)
		}
	}

	if progress.Decisions != "" {
		fmt.Fprintf(logFile, "\n--- decisions ---\n%s\n", progress.Decisions)
	}
	sha := w.commitIfDirty(ctx, pass, task, res)
	if err := w.st.CompleteTask(ctx, task.ID, name, result); err == nil {
		w.event(ctx, "task.done", name, pass.ID, map[string]any{"task": task.ID, "title": task.Title})
	}
	w.consecFails = 0
	w.consecNoReport = 0
	w.finishPass(ctx, pass, domain.PassDone, domain.OutcomeDone, domain.FailNone, exitCode, sha)
	return PassRan, nil
}

// artRunFailure funnels one orchestrator invocation's process-level failures
// (spawn error, wedge, nonzero exit) through the pass bookkeeping. handled
// is false when the invocation succeeded.
func (w *Worker) artRunFailure(ctx context.Context, pass *domain.Pass, res *resolved, fail func(domain.FailKind, string) (PassResult, error), exitCode int, tail []string, timedOut bool, runErr error, task *domain.Task) (PassResult, error, bool) {
	switch {
	case runErr != nil:
		r, e := w.failSetup(ctx, pass, task, runErr)
		return r, e, true
	case timedOut:
		w.event(ctx, "wedge.killed", w.cfg.Pipeline.Name, pass.ID, map[string]any{"timeoutMin": int(w.cfg.Pipeline.PassTimeout.Minutes())})
		r, e := fail(domain.FailTimeout, "wedged")
		return r, e, true
	case exitCode != 0:
		// The orchestrator is claude: auth failures are detectable in the
		// captured output, and — like settlePass — they park the pipeline
		// instead of burning retries (auth never self-heals).
		if res.caps.AuthProbe && isAuthFailure(tail) {
			r, e := w.haltAuth(ctx, pass, task, res, exitCode)
			return r, e, true
		}
		r, e := fail(domain.FailExit, fmt.Sprintf("agent exit %d", exitCode))
		return r, e, true
	}
	return PassRan, nil, false
}

// artNormalize verifies that the image agy just wrote genuinely contains PNG
// bytes (the extension proves nothing — image models have emitted JPEG/WebP
// bytes under a .png name) and re-encodes it as a PNG in place when it does
// not. An undecodable file fails the pass; handled is false when the asset is
// fine (verbatim or after conversion). rel is the repo-relative path for
// logs/events, abs the on-disk location.
func (w *Worker) artNormalize(ctx context.Context, pass *domain.Pass, fail func(domain.FailKind, string) (PassResult, error), logFile *os.File, abs, rel string) (PassResult, error, bool) {
	format, converted, err := chroma.EnsurePNG(abs)
	if err != nil {
		r, e := fail(domain.FailExit, err.Error())
		return r, e, true
	}
	if converted {
		fmt.Fprintf(logFile, "art verify: %s held %s bytes — re-encoded as PNG\n", rel, format)
		w.event(ctx, "art.normalized", w.cfg.Pipeline.Name, pass.ID, map[string]any{
			"path": rel, "from": format,
		})
	}
	return PassRan, nil, false
}

// resolveArt forces an art pass onto claude with a frontier model. Routing
// may have produced anything (an agy pin, a pipeline's haiku bundle) — art
// passes are claude-orchestrated BY DEFINITION (the pass invokes agy for the
// actual image generation via `workshop agy-run`), so the agent is never
// negotiable. The model keeps whatever routing/pinning chose IF it belongs to
// one of the allowed frontier families (fable/opus, any version); anything
// weaker becomes domain.ArtClaudeDefault.
func (w *Worker) resolveArt(ctx context.Context, routed *resolved) (*resolved, error) {
	bundle := routed.bundle
	if !strings.EqualFold(bundle.Agent, "claude") {
		w.event(ctx, "art.route_forced", w.cfg.Pipeline.Name, 0, map[string]any{
			"routedAgent": bundle.Agent, "note": "art tasks always run on claude, which invokes agy for the image",
		})
		bundle = domain.Bundle{Agent: "claude", Effort: bundle.Effort}
	}
	if !domain.AllowedArtClaudeModel(bundle.Model) {
		bundle.Model = domain.ArtClaudeDefault
	}

	drv, ok := w.drivers["claude"]
	if !ok {
		var err error
		if drv, err = driver.New("claude"); err != nil {
			return nil, err
		}
		w.drivers["claude"] = drv
	}
	caps, err := drv.Probe(ctx)
	if err != nil {
		return nil, err
	}
	return &resolved{bundle: bundle, drv: drv, caps: caps, agyLockHeld: true}, nil
}

// artAgyModel resolves the agy image-model label the orchestrator is told to
// run: an operator pin/route to agy with an allowed label wins, else the
// launch-verified label (KVArtAgyModel), else the preferred default.
func (w *Worker) artAgyModel(ctx context.Context, routed *resolved) string {
	if strings.EqualFold(routed.bundle.Agent, "agy") && domain.AllowedArtModel(routed.bundle.Model) {
		return routed.bundle.Model
	}
	model, _ := w.st.GetKV(ctx, KVArtAgyModel)
	if !domain.AllowedArtModel(model) {
		model = domain.ArtAgyModels[0]
	}
	return model
}

// artAgyCommand renders the exact agy invocation the orchestrator must use.
// It goes through `workshop agy-run` (this very binary) — agy silently drops
// output and can hang when spawned from a shell with piped stdio, so a bare
// `agy` from the orchestrator's shell tool is never safe. conversation, when
// set, resumes that agy conversation (the -trans rescreen).
func (w *Worker) artAgyCommand(agyModel, conversation, opLog string) string {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		exe = "workshop"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\"%s\" agy-run", exe)
	if w.cfg.SkipPermissions {
		b.WriteString(" --dangerously-skip-permissions")
	}
	b.WriteString(" --print-timeout 30m")
	if conversation != "" {
		fmt.Fprintf(&b, " --conversation %s", conversation)
	}
	fmt.Fprintf(&b, " --model \"%s\" --log-file \"%s\" -p \"<your image prompt>\"", agyModel, opLog)
	return b.String()
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
// settings, re-read EVERY use so a mid-run change applies to the very next
// art pass) beat the configured list: the JSON-array KVArtRemovers first,
// then the legacy single-value KVArtRemover, then config. Unknown names are
// dropped defensively (a stale kv from an older/newer build must not fail
// passes); an all-invalid list falls back to the default backend.
func (w *Worker) artRemovers(ctx context.Context) (removers []string, corridorkeyDir string) {
	var picked []string
	if raw, err := w.st.GetKV(ctx, KVArtRemovers); err == nil && raw != "" {
		_ = json.Unmarshal([]byte(raw), &picked)
	}
	if len(picked) == 0 {
		if v, err := w.st.GetKV(ctx, KVArtRemover); err == nil && v != "" {
			picked = []string{v}
		}
	}
	if len(picked) == 0 {
		picked = w.cfg.ArtRemovers
	}
	seen := map[string]bool{}
	for _, r := range picked {
		if r != "" && chroma.ValidRemover(r) && !seen[r] {
			seen[r] = true
			removers = append(removers, r)
		}
	}
	if len(removers) == 0 {
		removers = []string{chroma.Removers[0]}
	}
	return removers, w.cfg.CorridorkeyDir
}

// compareKeyers runs every non-primary keyer of an art-gen-trans pass over
// the same screened intermediate, purely as evidence for the operator: each
// result (and a copy of the primary's) lands beside the pass log as
// "<stem>.keyed-<keyer>.png" — the [export] mirror picks the set up
// automatically — so a human can compare backends file by file and settle on
// the most effective one. Comparison keyers are best-effort by design: a
// failure is logged and evented but never fails the pass (the committed
// asset already exists), and an engine shutdown just stops the loop.
func (w *Worker) compareKeyers(ctx context.Context, pass *domain.Pass, logFile *os.File, logPath, screenAbs, targetAbs, target string, removers []string, ckDir string, key chroma.Key) {
	stem := strings.TrimSuffix(logPath, ".log")
	results := make([]map[string]any, 0, len(removers))
	// The primary's output is already verified at the target; archive a copy
	// under the same naming scheme so the comparison set is complete.
	if err := statedir.CopyFile(targetAbs, stem+".keyed-"+removers[0]+".png"); err == nil {
		results = append(results, map[string]any{"keyer": removers[0], "ok": true, "primary": true})
	}
	for _, remover := range removers[1:] {
		if ctx.Err() != nil {
			return // engine shutdown — the pass already has its asset
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
			w.event(ctx, "art.keyer_compare_failed", w.cfg.Pipeline.Name, pass.ID, map[string]any{
				"target": target, "keyer": remover, "error": kerr.Error(),
			})
			continue
		}
		results = append(results, map[string]any{"keyer": remover, "ok": true})
	}
	w.event(ctx, "art.keyer_compare", w.cfg.Pipeline.Name, pass.ID, map[string]any{
		"target": target, "keyers": results, "files": filepath.Base(stem) + ".keyed-<keyer>.png",
	})
}

// artTargetPath derives the repo-relative asset path: the task's first files
// entry when given, else assets/art/<slug>.png. Task files are operator/agent
// data — anything non-local is refused, never resolved. The extension is
// always .png: the flow only ever produces PNGs (the generation prompt says
// PNG, ffmpeg picks its encoder by extension, and JPEG cannot hold the alpha
// channel -trans exists for), so files: ["hero.jpg"] yields hero.png — a
// foreign extension honored verbatim either fails every keying verification
// (ffmpeg) or mislabels the asset (PNG bytes in a .jpg file).
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
			return "", fmt.Errorf("engine: art target %q escapes the repository", task.Files[0])
		}
		p = path.Clean(p)
		// Task files are operator/agent data: an asset has no business under
		// .git (same guard family as the July path-traversal hardening).
		for _, seg := range strings.Split(p, "/") {
			if strings.EqualFold(seg, ".git") {
				return "", fmt.Errorf("engine: art target %q is inside .git", task.Files[0])
			}
		}
		if ext := path.Ext(p); !strings.EqualFold(ext, ".png") {
			p = strings.TrimSuffix(p, ext) + ".png"
		}
		return p, nil
	}
	return "assets/art/" + artSlug(task.Title) + ".png", nil
}

// artSlug turns a task title into a filesystem-safe kebab-case stem.
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

// artGenPrompt composes the stage-1 (generation) prompt for the claude
// orchestrator. Deliberately NOT the coding contract: no verify command, no
// proposals, no invent framing — one image, produced by agy under the
// orchestrator's direction, is the whole deliverable. agyCmd is the exact
// `workshop agy-run` invocation to use. The operator's types/<type>.md
// fragment (style guides, palette rules) rides along when present.
func (w *Worker) artGenPrompt(task *domain.Task, target string, trans bool, agyCmd string) string {
	var b strings.Builder
	b.WriteString(`# Workshop art-generation pass

You are ONE pass of an autonomous art pipeline, acting as the ORCHESTRATOR.
Your ONLY deliverable this pass is ONE image asset, and the image itself MUST
be produced by the Antigravity CLI (agy) — the image-generation agent you
invoke from your shell. You direct agy and verify its output; you never draw,
compose, copy, or convert the asset yourself. This is not a coding task: do
not refactor anything, do not run builds or tests, and NEVER run git commit —
the engine commits.

Steps, in order:

1. The moment you start, OVERWRITE progress.json (absolute path in the
   mechanics section) with:
   { "phase": "working", "task": "<task title>", "plan": "<one line>", "updated": "<ISO-8601 UTC now>" }
2. Compose a self-contained image-generation prompt for agy from the task
   (and the GOAL/guidance sections below). It MUST tell agy to save the
   image as a PNG at EXACTLY the target path named in the mechanics section
   (relative to the repository root, creating parent directories as needed)
   and to write nothing else into the repository.`)
	if trans {
		b.WriteString(`
   It MUST ALSO demand: the subject composed on a PLAIN single-color
   background that contrasts with the subject (a later step replaces the
   background), and the WHOLE subject inside the frame with a small margin —
   it must not touch the image edges.`)
	}
	b.WriteString(`
3. Run agy from the repository root — your working directory; agy keys its
   conversation record on the directory it is launched from — EXACTLY like
   this, substituting your image prompt:

   ` + agyCmd + `

   Run it as ONE foreground shell command and wait for it to finish. NEVER
   invoke a bare "agy" (without a console it drops output and can hang) and
   never run two agy commands at once.
4. Verify the deliverable yourself: the file exists at the target path and
   holds a real image of the right subject (agy prints nothing to your
   shell — read the file; the --log-file above has agy's operational log if
   a run fails). A failed or empty run may be retried with a refined
   prompt: at most 3 agy runs this pass, then report blocked.
5. OVERWRITE progress.json with:
   { "phase": "done", "task": "<task title>", "result": "saved <target path>", "updated": "<ISO-8601 UTC now>" }

If you cannot produce the image (agy missing, not logged in, repeated
failures), OVERWRITE progress.json with phase "blocked" and a one-line note
instead, and change nothing in the repository.`)

	blocks := []string{b.String(), w.artMechanics(target)}
	if goal := readTrim(w.cfg.GoalPath); goal != "" {
		blocks = append(blocks, "## GOAL (project context — style, tone, subject matter)\n\n"+goal)
	}
	blocks = append(blocks, prompt.TaskBlock(task))
	if frag := w.fragment(filepath.Join("types", task.Type+".md")); frag != "" {
		blocks = append(blocks, "## GUIDANCE FOR THIS TASK TYPE\n\n"+frag)
	}
	return strings.Join(blocks, "\n\n")
}

// artMechanics is the art counterpart of prompt.Mechanics: paths only, no
// verify command (there is nothing to build).
func (w *Worker) artMechanics(target string) string {
	var b strings.Builder
	b.WriteString("## MECHANICS FOR THIS PASS\n\n")
	fmt.Fprintf(&b, "- progress.json (your self-report; OUTSIDE the repository): %s\n", filepath.Join(w.cfg.StateDir, statedir.ProgressFile))
	fmt.Fprintf(&b, "- Repository working directory: %s\n", w.cfg.RepoDir)
	fmt.Fprintf(&b, "- TARGET PATH for the asset (relative to the repository root): %s", filepath.FromSlash(target))
	return b.String()
}

// artRescreenPrompt composes the stage-2 prompt for the claude orchestrator:
// resume the SAME agy conversation (the --conversation flag rides in agyCmd)
// and have it repaint the background as a flat key color for chroma keying.
func (w *Worker) artRescreenPrompt(target, screen string, key chroma.Key, agyCmd string) string {
	colorName, hex := "green", "#00FF00"
	if key == chroma.KeyBlue {
		colorName, hex = "blue", "#0000FF"
	}
	var b strings.Builder
	b.WriteString("# Workshop art pass — background rescreen step\n\n")
	fmt.Fprintf(&b, "You are the orchestrator of an autonomous art pipeline. In the previous\nstep you had the Antigravity CLI (agy) generate an image asset, now saved at\n%s (relative to the repository root:\n%s).\n\n", filepath.FromSlash(target), w.cfg.RepoDir)
	fmt.Fprintf(&b, `Resume that SAME agy conversation — the --conversation flag in the command
below does exactly that; never start a fresh conversation — and direct agy to
recreate that exact image with its ENTIRE background repainted as one
uniform, flat, solid pure %s (%s) so it can be chroma-keyed. Your agy prompt
must demand:

- EVERY background pixel becomes exactly that flat color — no gradients, no
  shadows, no texture, no vignette.
- The subject stays EXACTLY as it is: same pose, style, colors, size, and
  position. Not restyled, and no %s bleeding into it.
- SCREEN PATH — save the result as a PNG at EXACTLY this path (relative to the repository root): %s
- Do not overwrite %s, and write nothing else into the repository.

Run agy from the repository root (your working directory) EXACTLY like this,
substituting your rescreen prompt — one foreground shell command at a time,
never a bare "agy", at most 3 runs:

   %s

Verify the screened image yourself (it exists at the screen path and shows
the same subject on the flat %s background), then OVERWRITE progress.json
(absolute path: %s) with:
{ "phase": "done", "result": "saved %s", "updated": "<ISO-8601 UTC now>" }

If you cannot do it, OVERWRITE progress.json with phase "blocked" and a
one-line note instead. NEVER run git commit.`,
		colorName, hex, colorName,
		filepath.FromSlash(screen), filepath.FromSlash(target),
		agyCmd, colorName,
		filepath.Join(w.cfg.StateDir, statedir.ProgressFile), filepath.FromSlash(screen))
	return b.String()
}
