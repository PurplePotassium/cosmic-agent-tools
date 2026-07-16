package engine

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

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
	// KVArtAgyModel is the launch-verified agy model label art passes run
	// (the first domain.ArtAgyModels entry agy actually offers).
	KVArtAgyModel = "art.agyModel"
	// KVArtAgyModels is the JSON list of every label the launch probe saw
	// (doctor/status display).
	KVArtAgyModels = "art.agyModels"
	// KVArtRemover is the live green/blue-screen remover override
	// (dashboard); empty/absent = the [art].remover config value.
	KVArtRemover = "art.remover"
)

// greenSubjectThreshold: when at least this fraction of the generated image
// is green-dominant, the subject "genuinely has lots of green" and the
// rescreen step uses a BLUE screen instead.
const greenSubjectThreshold = 0.25

// chromaTimeout bounds the keying step. The builtin/ffmpeg backends take
// milliseconds; CorridorKey on a CPU-only torch install loads two ~400MB
// models per call and then infers on CPU — measured at 30+ minutes for a
// single image on real hardware (2026-07), so the budget is generous. The
// exclusive agy slot is already released by this point, so a slow keying
// blocks only its own pipeline.
const chromaTimeout = 45 * time.Minute

// runArtPass executes an art-generation task: agy (Gemini image model)
// generates the asset. For art-gen that is the whole pass; art-gen-trans
// continues strictly linearly — resume the SAME agy conversation to repaint
// the background as a flat green/blue screen, then key that screen away with
// the selected remover, leaving a transparent PNG at the target path.
//
// The whole agy portion holds the EXCLUSIVE agy slot (WorkerConfig.AgyMu):
// the conversation id is recovered from agy's per-workdir
// last_conversations.json, a whole-file-rewritten record that any concurrent
// agy -p instance races. claude passes are unaffected.
func (w *Worker) runArtPass(ctx context.Context, pass *domain.Pass, task *domain.Task, routed *resolved, sessionID string) (PassResult, error) {
	name := w.cfg.Pipeline.Name
	trans := task.Type == domain.ArtGenTransType

	res, err := w.resolveArt(ctx, routed)
	if err != nil {
		return w.failSetup(ctx, pass, task, err)
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
	defer logFile.Close()

	w.setState(ctx, pass, domain.PassRunning)
	w.event(ctx, "pass.started", name, pass.ID, map[string]any{
		"n": pass.N, "task": task.Title, "art": true, "type": task.Type, "target": target,
		"agent": res.bundle.Agent, "model": res.bundle.Model,
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

	// Stage 1: generate the asset.
	fmt.Fprintf(logFile, "art stage 1: generate %s (model %s)\n", target, res.bundle.Model)
	var timedOut bool
	var runErr error
	exitCode, _, timedOut, runErr = w.spawn(ctx, pass, res, w.artGenPrompt(task, target, trans), w.cfg.RepoDir, logPath, logFile, sessionID, "")
	if ctx.Err() != nil {
		_ = w.st.ReleaseTask(ctx, task.ID)
		w.finishPass(ctx, pass, domain.PassFailed, domain.OutcomeFailed, domain.FailSetup, exitCode, "")
		return PassRan, ctx.Err()
	}
	if res2, err2, handled := w.artRunFailure(ctx, pass, res, fail, exitCode, timedOut, runErr, task); handled {
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
	w.event(ctx, "art.generated", name, pass.ID, map[string]any{"target": target})

	result := "generated " + target
	if trans {
		// Stage 2: rescreen, continuing the SAME agy conversation.
		conv, cerr := driver.AgyLastConversation(w.cfg.RepoDir)
		if cerr != nil {
			return fail(domain.FailSetup, "agy conversation lookup: "+cerr.Error())
		}
		if conv == "" || conv == prevConv {
			return fail(domain.FailSetup, "cannot identify the agy conversation to resume (no new last_conversations.json entry for this workdir)")
		}
		key := chroma.KeyGreen
		if frac, ferr := chroma.FractionKeyish(targetAbs, chroma.KeyGreen); ferr == nil && frac > greenSubjectThreshold {
			key = chroma.KeyBlue // the sprite is genuinely green — blue screen keys cleaner
		}
		screen := screenPath(target)
		screenAbs := filepath.Join(w.cfg.RepoDir, filepath.FromSlash(screen))
		if err := statedir.WriteJSON(filepath.Join(w.cfg.StateDir, statedir.ProgressFile), domain.Progress{}); err != nil {
			return fail(domain.FailSetup, "reset progress.json: "+err.Error())
		}
		fmt.Fprintf(logFile, "\nart stage 2: rescreen %s (conversation %s) -> %s\n", key, conv, screen)
		exitCode, _, timedOut, runErr = w.spawn(ctx, pass, res, w.artRescreenPrompt(target, screen, key), w.cfg.RepoDir, logPath, logFile, sessionID, conv)
		if ctx.Err() != nil {
			_ = w.st.ReleaseTask(ctx, task.ID)
			w.finishPass(ctx, pass, domain.PassFailed, domain.OutcomeFailed, domain.FailSetup, exitCode, "")
			return PassRan, ctx.Err()
		}
		if res2, err2, handled := w.artRunFailure(ctx, pass, res, fail, exitCode, timedOut, runErr, task); handled {
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
		unlockAgy() // agy work is over; keying is local
		w.event(ctx, "art.rescreened", name, pass.ID, map[string]any{"screen": screen, "key": key.String()})

		// Stage 3: key the screen away — transparent PNG lands at target.
		remover, ckDir := w.artRemover(ctx)
		fmt.Fprintf(logFile, "art stage 3: remove %s screen via %s\n", key, remover)
		kctx, cancel := context.WithTimeout(ctx, chromaTimeout)
		kerr := chroma.Remove(kctx, remover, ckDir, screenAbs, targetAbs, key)
		cancel()
		if kerr != nil {
			return fail(domain.FailExit, kerr.Error())
		}
		if verr := chroma.VerifyTransparency(targetAbs); verr != nil {
			return fail(domain.FailExit, verr.Error())
		}
		// The intermediate screen image is forensic material, not an asset:
		// archive it next to the pass log, out of the repository.
		_ = statedir.CopyFile(screenAbs, strings.TrimSuffix(logPath, ".log")+".screen.png")
		_ = os.Remove(screenAbs)
		w.event(ctx, "art.keyed", name, pass.ID, map[string]any{"target": target, "remover": remover, "key": key.String()})
		result = fmt.Sprintf("generated %s with a transparent background (%s screen keyed via %s)", target, key, remover)
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

// artRunFailure funnels one agy invocation's process-level failures
// (spawn error, wedge, nonzero exit) through the pass bookkeeping. handled
// is false when the invocation succeeded.
func (w *Worker) artRunFailure(ctx context.Context, pass *domain.Pass, res *resolved, fail func(domain.FailKind, string) (PassResult, error), exitCode int, timedOut bool, runErr error, task *domain.Task) (PassResult, error, bool) {
	switch {
	case runErr != nil:
		r, e := w.failSetup(ctx, pass, task, runErr)
		return r, e, true
	case timedOut:
		w.event(ctx, "wedge.killed", w.cfg.Pipeline.Name, pass.ID, map[string]any{"timeoutMin": int(w.cfg.Pipeline.PassTimeout.Minutes())})
		r, e := fail(domain.FailTimeout, "wedged")
		return r, e, true
	case exitCode != 0:
		// agy is blind: a failure with no progress start-write smells like
		// dead auth or a rejected model id — same heuristic as settlePass.
		if res.caps.Capture == driver.CaptureNone && statedir.ReadProgress(w.cfg.StateDir).Phase == "" {
			w.consecNoReport++
			if w.consecNoReport == w.cfg.SuspectAuthAfter {
				w.event(ctx, "auth.suspected", w.cfg.Pipeline.Name, pass.ID, map[string]any{
					"agent": res.drv.Name(),
					"note": fmt.Sprintf("%d consecutive failures with no self-report — check auth interactively (run `%s` once) and verify the model id",
						w.consecNoReport, res.drv.Name()),
				})
			}
		}
		r, e := fail(domain.FailExit, fmt.Sprintf("agent exit %d", exitCode))
		return r, e, true
	}
	return PassRan, nil, false
}

// resolveArt forces an art pass onto agy with an allowed art model. Routing
// may have produced anything (a pipeline's claude bundle, a foreign pin) —
// art passes are agy-only BY DEFINITION, so the agent is never negotiable.
// The model keeps whatever routing/pinning chose IF it is one of the allowed
// labels; otherwise the launch-verified label (KVArtAgyModel), else the
// preferred default.
func (w *Worker) resolveArt(ctx context.Context, routed *resolved) (*resolved, error) {
	bundle := routed.bundle
	if !strings.EqualFold(bundle.Agent, "agy") {
		w.event(ctx, "art.route_forced", w.cfg.Pipeline.Name, 0, map[string]any{
			"routedAgent": bundle.Agent, "note": "art tasks always run on agy",
		})
		bundle = domain.Bundle{Agent: "agy"}
	}
	if !domain.AllowedArtModel(bundle.Model) {
		model, _ := w.st.GetKV(ctx, KVArtAgyModel)
		if !domain.AllowedArtModel(model) {
			model = domain.ArtAgyModels[0]
		}
		bundle.Model = model
	}
	bundle.Effort = "" // agy has no effort flag

	drv, ok := w.drivers["agy"]
	if !ok {
		var err error
		if drv, err = driver.New("agy"); err != nil {
			return nil, err
		}
		w.drivers["agy"] = drv
	}
	caps, err := drv.Probe(ctx)
	if err != nil {
		return nil, err
	}
	return &resolved{bundle: bundle, drv: drv, caps: caps, agyLockHeld: true}, nil
}

// artRemover resolves the effective green/blue-screen remover: the live kv
// override (dashboard setting, re-read EVERY use so a mid-run change applies
// to the very next art pass) over the configured default.
func (w *Worker) artRemover(ctx context.Context) (remover, corridorkeyDir string) {
	remover = w.cfg.ArtRemover
	if v, err := w.st.GetKV(ctx, KVArtRemover); err == nil && v != "" {
		remover = v
	}
	if remover == "" {
		remover = chroma.Removers[0]
	}
	return remover, w.cfg.CorridorkeyDir
}

// artTargetPath derives the repo-relative asset path: the task's first files
// entry when given, else assets/art/<slug>.png. Task files are operator/agent
// data — anything non-local is refused, never resolved.
func artTargetPath(task *domain.Task) (string, error) {
	if len(task.Files) > 0 && strings.TrimSpace(task.Files[0]) != "" {
		p := filepath.ToSlash(strings.TrimSpace(task.Files[0]))
		if !filepath.IsLocal(filepath.FromSlash(p)) {
			return "", fmt.Errorf("engine: art target %q escapes the repository", task.Files[0])
		}
		p = path.Clean(p)
		if path.Ext(p) == "" {
			p += ".png"
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

// artGenPrompt composes the stage-1 (generation) prompt. Deliberately NOT the
// coding contract: no verify command, no proposals, no invent framing — one
// image is the whole deliverable. The operator's types/<type>.md fragment
// (style guides, palette rules) rides along when present.
func (w *Worker) artGenPrompt(task *domain.Task, target string, trans bool) string {
	var b strings.Builder
	b.WriteString(`# Workshop art-generation pass

You are ONE pass of an autonomous art pipeline. Your ONLY deliverable this
pass is ONE image asset produced with your image generation tool. This is not
a coding task: do not refactor anything, do not run builds or tests, and
NEVER run git commit — the engine commits.

Steps, in order:

1. The moment you start, OVERWRITE progress.json (absolute path in the
   mechanics section) with:
   { "phase": "working", "task": "<task title>", "plan": "<one line>", "updated": "<ISO-8601 UTC now>" }
2. Generate the image the task describes.`)
	if trans {
		b.WriteString(`
   Compose the subject on a PLAIN single-color background that contrasts
   with the subject (a later step replaces the background). Keep the whole
   subject inside the frame with a small margin — it must not touch the
   image edges.`)
	}
	b.WriteString(`
3. Save it as a PNG at EXACTLY the target path named in the mechanics
   section, creating parent directories as needed. Write nothing else into
   the repository.
4. OVERWRITE progress.json with:
   { "phase": "done", "task": "<task title>", "result": "saved <target path>", "updated": "<ISO-8601 UTC now>" }

If you cannot produce the image, OVERWRITE progress.json with phase
"blocked" and a one-line note instead, and change nothing in the repository.`)

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

// artRescreenPrompt composes the stage-2 prompt sent into the RESUMED agy
// conversation: repaint the background as a flat key color for chroma keying.
func (w *Worker) artRescreenPrompt(target, screen string, key chroma.Key) string {
	colorName, hex := "green", "#00FF00"
	if key == chroma.KeyBlue {
		colorName, hex = "blue", "#0000FF"
	}
	var b strings.Builder
	b.WriteString("# Workshop art pass — background rescreen step\n\n")
	fmt.Fprintf(&b, "You are continuing the conversation in which you just generated an image\nasset and saved it at %s (relative to the repository root:\n%s).\n\n", filepath.FromSlash(target), w.cfg.RepoDir)
	fmt.Fprintf(&b, `Recreate that exact image with its ENTIRE background repainted as one
uniform, flat, solid pure %s (%s) so it can be chroma-keyed:

- EVERY background pixel becomes exactly that flat color — no gradients, no
  shadows, no texture, no vignette.
- The subject stays EXACTLY as it is: same pose, style, colors, size, and
  position. Do not restyle it and do not let %s bleed into it.

SCREEN PATH — save the result as a PNG at EXACTLY this path (relative to the repository root): %s
Do not overwrite %s. Write nothing else into the repository, and NEVER run
git commit.

Then OVERWRITE progress.json (absolute path: %s) with:
{ "phase": "done", "result": "saved %s", "updated": "<ISO-8601 UTC now>" }

If you cannot do it, OVERWRITE progress.json with phase "blocked" and a
one-line note instead.`,
		colorName, hex, colorName,
		filepath.FromSlash(screen), filepath.FromSlash(target),
		filepath.Join(w.cfg.StateDir, statedir.ProgressFile), filepath.FromSlash(screen))
	return b.String()
}
