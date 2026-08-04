package artjob

import (
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/bus"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/chroma"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/domain"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/driver"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/fakeagent"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/gitx"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/statedir"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/store"
)

// The test binary doubles as the fake agent.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "_fake-agent" {
		os.Exit(fakeagent.Main())
	}
	os.Exit(m.Run())
}

func TestArtTargetPath(t *testing.T) {
	cases := []struct {
		name  string
		task  domain.Task
		want  string
		isErr bool
	}{
		{"files entry", domain.Task{Files: []string{"art/hero.png"}}, "art/hero.png", false},
		{"extension added", domain.Task{Files: []string{"art/hero"}}, "art/hero.png", false},
		{"foreign extension replaced", domain.Task{Files: []string{"art/hero.jpg"}}, "art/hero.png", false},
		{"uppercase png kept", domain.Task{Files: []string{"art/HERO.PNG"}}, "art/HERO.PNG", false},
		{"title slug", domain.Task{Title: "Draw the Hero Sprite!"}, "assets/art/draw-the-hero-sprite.png", false},
		{"empty title", domain.Task{}, "assets/art/asset.png", false},
		{"escape rejected", domain.Task{Files: []string{"../outside.png"}}, "", true},
		{"absolute rejected", domain.Task{Files: []string{`C:\evil.png`}}, "", true},
		{"dot-git rejected", domain.Task{Files: []string{".git/hooks/x.png"}}, "", true},
		{"nested dot-git rejected", domain.Task{Files: []string{"a/.GIT/x.png"}}, "", true},
	}
	for _, c := range cases {
		got, err := artTargetPath(&c.task)
		if c.isErr {
			if err == nil {
				t.Errorf("%s: want error, got %q", c.name, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: got %q, %v; want %q", c.name, got, err, c.want)
		}
	}
}

func TestScreenPath(t *testing.T) {
	if got := screenPath("assets/art/hero.png"); got != "assets/art/hero-screen.png" {
		t.Fatalf("screenPath = %q", got)
	}
}

func TestJobTitle(t *testing.T) {
	if got := (Job{Description: "  \nDraw a hero\nwith details"}).title(); got != "Draw a hero" {
		t.Fatalf("title = %q", got)
	}
	if got := (Job{}).title(); got != "art asset" {
		t.Fatalf("empty title = %q", got)
	}
}

type testRig struct {
	t      *testing.T
	repo   string
	state  string
	st     *store.Store
	runner *ArtRunner
	agyMu  *sync.RWMutex
	cfg    Config
}

// newRig builds a hermetic ArtRunner: a scratch git repo, a scratch store,
// the fake driver in the orchestrator slot (the real one would spawn
// claude.exe), HAL_AGY_STATE_DIR pointed at a scratch dir the fake
// agent's agy-style conversation bookkeeping writes into, and a hermetic
// keying stub (every real backend shells out).
func newRig(t *testing.T, sc fakeagent.Scenario, tweaks ...func(cfg *Config)) *testRig {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.name", "Test")
	git("config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-q", "-m", "initial")

	stateRoot := t.TempDir()
	goalPath := filepath.Join(stateRoot, "GOAL.md")
	if err := os.WriteFile(goalPath, []byte("Test goal.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	scenarioPath := filepath.Join(stateRoot, "scenario.json")
	if err := statedir.WriteJSON(scenarioPath, sc); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HAL_FAKE_BIN", os.Args[0])
	t.Setenv("HAL_FAKE_SCENARIO", scenarioPath)
	t.Setenv("HAL_AGY_STATE_DIR", t.TempDir())

	st, err := store.Open(filepath.Join(stateRoot, "hal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	cfg := Config{
		RepoDir:  repo,
		StateDir: filepath.Join(stateRoot, "state", Pipeline),
		LogDir:   filepath.Join(stateRoot, "logs", Pipeline),
		GoalPath: goalPath,
		Timeout:  2 * time.Minute,
	}
	for _, tweak := range tweaks {
		tweak(&cfg)
	}
	agyMu := &sync.RWMutex{}
	runner := New(st, bus.New(st), agyMu, cfg)
	runner.SetDriver(driver.NewFake())

	orig := chromaRemove
	chromaRemove = func(_ context.Context, _, _, in, out string, key chroma.Key) error {
		return testKeyImage(in, out, key)
	}
	t.Cleanup(func() { chromaRemove = orig })

	return &testRig{t: t, repo: repo, state: stateRoot, st: st, runner: runner, agyMu: agyMu, cfg: cfg}
}

// run executes one job through the pass-row lifecycle and returns the run
// error plus the settled pass row.
func (r *testRig) run(ctx context.Context, job Job) (error, *domain.Pass) {
	r.t.Helper()
	pass, err := r.runner.StartPass(ctx)
	if err != nil {
		r.t.Fatal(err)
	}
	runErr := r.runner.Run(ctx, pass, job)
	got, err := r.st.GetPass(context.Background(), pass.ID)
	if err != nil {
		r.t.Fatal(err)
	}
	return runErr, got
}

func (r *testRig) commits() []gitx.Commit {
	r.t.Helper()
	commits, err := gitx.RecentCommits(context.Background(), r.repo, 20)
	if err != nil {
		r.t.Fatal(err)
	}
	return commits
}

// testKeyImage is the hermetic stand-in for a keying backend: pixels whose
// key channel clearly dominates go transparent, everything else stays opaque
// — enough for chroma.VerifyTransparency on the fake agent's flat screens.
// The real backends are exercised in internal/chroma.
func testKeyImage(inPath, outPath string, key chroma.Key) error {
	f, err := os.Open(inPath)
	if err != nil {
		return err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return err
	}
	b := img.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r16, g16, b16, _ := img.At(x, y).RGBA()
			r8, g8, b8 := int(r16>>8), int(g16>>8), int(b16>>8)
			screen := g8 > r8+40 && g8 > b8+40
			if key == chroma.KeyBlue {
				screen = b8 > r8+40 && b8 > g8+40
			}
			a := uint8(255)
			if screen {
				a = 0
			}
			out.SetNRGBA(x-b.Min.X, y-b.Min.Y, color.NRGBA{R: uint8(r8), G: uint8(g8), B: uint8(b8), A: a})
		}
	}
	o, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer o.Close()
	return png.Encode(o, out)
}

func TestArtGenJob(t *testing.T) {
	r := newRig(t, fakeagent.Scenario{Behavior: "art"})
	ctx := context.Background()

	err, pass := r.run(ctx, Job{Description: "Draw a cosmic hero"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if pass.Outcome != domain.OutcomeDone {
		t.Fatalf("pass outcome = %s; want done", pass.Outcome)
	}

	target := filepath.Join(r.repo, "assets", "art", "draw-a-cosmic-hero.png")
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("generated asset missing: %v", err)
	}
	if commits := r.commits(); len(commits) < 2 {
		t.Fatalf("expected the art job to commit; got %d commits", len(commits))
	} else if !strings.HasPrefix(commits[0].Subject, "ws(art) iter 1 [claude]") {
		t.Fatalf("commit subject = %q", commits[0].Subject)
	}
	comps, _ := r.st.ListCompletions(ctx, 5)
	if len(comps) != 1 || comps[0].Pipeline != Pipeline {
		t.Fatalf("completions: %+v", comps)
	}
}

func TestArtGenTransJob(t *testing.T) {
	r := newRig(t, fakeagent.Scenario{Behavior: "art"})
	ctx := context.Background()

	err, pass := r.run(ctx, Job{Description: "hero cutout", Target: "assets/hero.png", Transparent: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if pass.Outcome != domain.OutcomeDone {
		t.Fatalf("pass outcome = %s; want done", pass.Outcome)
	}

	target := filepath.Join(r.repo, "assets", "hero.png")
	if err := chroma.VerifyTransparency(target); err != nil {
		t.Fatalf("final asset is not transparent: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.repo, "assets", "hero-screen.png")); !os.IsNotExist(err) {
		t.Fatalf("screen intermediate should be removed from the repo (err=%v)", err)
	}
	// The exclusive agy slot must be free again after the job.
	if !r.agyMu.TryLock() {
		t.Fatal("AgyMu still held after the job")
	}
	r.agyMu.Unlock()
}

// An agy-shaped run has no ExecPlan.TranscriptPath (the conversation id is
// runtime-minted): the runner must recover the id post-run and archive the
// runtime's brain transcript beside the pass log — and, with [export]
// configured, mirror the evidence (plus the rendered markdown) into the
// export folder.
func TestArtJobArchivesAndExportsTranscript(t *testing.T) {
	exportDir := filepath.Join(t.TempDir(), "audit", "art")
	r := newRig(t, fakeagent.Scenario{Behavior: "art"}, func(cfg *Config) {
		cfg.ExportDir = exportDir
		cfg.ExportHumanReadable = true
	})
	ctx := context.Background()
	if err, _ := r.run(ctx, Job{Description: "audited hero"}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	archive, err := os.ReadFile(filepath.Join(r.cfg.LogDir, "iter-000001.transcript.jsonl"))
	if err != nil {
		t.Fatalf("agy transcript not archived beside the pass log: %v", err)
	}
	if !strings.Contains(string(archive), "fake thinking") {
		t.Fatalf("archived transcript lacks the runtime's content:\n%s", archive)
	}
	for _, name := range []string{
		"iter-000001.log", "iter-000001.transcript.jsonl", "iter-000001.transcript.md",
	} {
		if _, err := os.Stat(filepath.Join(exportDir, name)); err != nil {
			t.Errorf("%s not exported: %v", name, err)
		}
	}
	md, err := os.ReadFile(filepath.Join(exportDir, "iter-000001.transcript.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(md), "> fake thinking") {
		t.Fatalf("rendered transcript lacks the thinking blockquote:\n%s", md)
	}
}

// The image model sometimes writes JPEG (or WebP…) bytes under the requested
// .png name. The job must detect the real format and re-encode, so the
// committed asset is a genuine PNG.
func TestArtGenJobNormalizesMislabeledBytes(t *testing.T) {
	r := newRig(t, fakeagent.Scenario{Behavior: "art", ArtEncode: "jpeg"})
	ctx := context.Background()

	err, pass := r.run(ctx, Job{Description: "mislabeled hero"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if pass.Outcome != domain.OutcomeDone {
		t.Fatalf("pass outcome = %s; want done", pass.Outcome)
	}

	target := filepath.Join(r.repo, "assets", "art", "mislabeled-hero.png")
	f, err := os.Open(target)
	if err != nil {
		t.Fatalf("generated asset missing: %v", err)
	}
	defer f.Close()
	_, format, err := image.Decode(f)
	if err != nil || format != "png" {
		t.Fatalf("asset decodes as %q, %v; want png", format, err)
	}
}

// Same for -trans: the mislabeled screen image must be normalized before
// keying, and the final keyed asset must still verify as transparent.
func TestArtGenTransJobNormalizesMislabeledBytes(t *testing.T) {
	r := newRig(t, fakeagent.Scenario{Behavior: "art", ArtEncode: "jpeg"})
	ctx := context.Background()

	err, pass := r.run(ctx, Job{Description: "hero cutout", Target: "assets/hero.png", Transparent: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if pass.Outcome != domain.OutcomeDone {
		t.Fatalf("pass outcome = %s; want done", pass.Outcome)
	}
	if err := chroma.VerifyTransparency(filepath.Join(r.repo, "assets", "hero.png")); err != nil {
		t.Fatalf("final asset is not transparent: %v", err)
	}
}

// With several keyers selected (kv override, primary first) the job must
// commit the PRIMARY's output at the target and archive one comparison image
// per keyer beside the pass log — the operator's side-by-side evidence.
func TestArtGenTransMultiKeyerComparison(t *testing.T) {
	r := newRig(t, fakeagent.Scenario{Behavior: "art"})
	ctx := context.Background()
	if err := r.st.SetKV(ctx, KVArtRemovers, `["ffmpeg","corridorkey"]`); err != nil {
		t.Fatal(err)
	}
	var calls []string
	orig := chromaRemove
	// Hermetic stub: record which backend was asked for, key via the test keyer.
	chromaRemove = func(kctx context.Context, remover, ckDir, in, out string, key chroma.Key) error {
		calls = append(calls, remover)
		return testKeyImage(in, out, key)
	}
	t.Cleanup(func() { chromaRemove = orig })

	if err, _ := r.run(ctx, Job{Description: "hero cutout", Target: "assets/hero.png", Transparent: true}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if want := []string{"ffmpeg", "corridorkey"}; len(calls) != 2 || calls[0] != want[0] || calls[1] != want[1] {
		t.Fatalf("keyer invocations = %v; want %v (primary first, then comparisons)", calls, want)
	}
	if err := chroma.VerifyTransparency(filepath.Join(r.repo, "assets", "hero.png")); err != nil {
		t.Fatalf("final asset is not transparent: %v", err)
	}
	for _, k := range []string{"ffmpeg", "corridorkey"} {
		cmp := filepath.Join(r.cfg.LogDir, "iter-000001.keyed-"+k+".png")
		if err := chroma.VerifyTransparency(cmp); err != nil {
			t.Errorf("comparison image for %s missing/not keyed: %v", k, err)
		}
	}
	if _, err := os.Stat(filepath.Join(r.repo, "assets", "hero-screen.png")); !os.IsNotExist(err) {
		t.Fatalf("screen intermediate should be removed from the repo (err=%v)", err)
	}
	// Comparison images are forensic material and must never enter the repo.
	if _, err := os.Stat(filepath.Join(r.repo, "assets", "hero.keyed-corridorkey.png")); !os.IsNotExist(err) {
		t.Fatalf("comparison image leaked into the repo (err=%v)", err)
	}
}

// A comparison keyer failing (missing backend, bad checkout, …) must never
// fail the job: the committed asset already exists — the comparison is
// evidence, not the deliverable.
func TestArtGenTransComparisonKeyerFailureTolerated(t *testing.T) {
	r := newRig(t, fakeagent.Scenario{Behavior: "art"})
	ctx := context.Background()
	if err := r.st.SetKV(ctx, KVArtRemovers, `["ffmpeg","corridorkey"]`); err != nil {
		t.Fatal(err)
	}
	orig := chromaRemove
	chromaRemove = func(kctx context.Context, remover, ckDir, in, out string, key chroma.Key) error {
		if remover == "corridorkey" {
			return errors.New("corridorkey CLI not found")
		}
		return testKeyImage(in, out, key)
	}
	t.Cleanup(func() { chromaRemove = orig })

	err, pass := r.run(ctx, Job{Description: "hero cutout", Target: "assets/hero.png", Transparent: true})
	if err != nil {
		t.Fatalf("Run failed despite the failed comparison keyer: %v", err)
	}
	if pass.Outcome != domain.OutcomeDone {
		t.Fatalf("pass outcome = %s; want done despite the failed comparison keyer", pass.Outcome)
	}
	if err := chroma.VerifyTransparency(filepath.Join(r.cfg.LogDir, "iter-000001.keyed-ffmpeg.png")); err != nil {
		t.Errorf("primary comparison copy missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.cfg.LogDir, "iter-000001.keyed-corridorkey.png")); !os.IsNotExist(err) {
		t.Fatalf("failed comparison keyer should leave no image (err=%v)", err)
	}
}

// artRemovers precedence: JSON-list kv > legacy single kv > config > default,
// with unknown names dropped defensively.
func TestArtRemoversResolution(t *testing.T) {
	r := newRig(t, fakeagent.Scenario{Behavior: "art"})
	ctx := context.Background()
	assertRemovers := func(want ...string) {
		t.Helper()
		got, _ := r.runner.artRemovers(ctx)
		if len(got) != len(want) {
			t.Fatalf("artRemovers = %v; want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("artRemovers = %v; want %v", got, want)
			}
		}
	}
	assertRemovers("ffmpeg") // nothing configured -> default
	r.runner.cfg.ArtRemovers = []string{"corridorkey", "ffmpeg"}
	assertRemovers("corridorkey", "ffmpeg") // config list
	if err := r.st.SetKV(ctx, KVArtRemover, "corridorkey"); err != nil {
		t.Fatal(err)
	}
	assertRemovers("corridorkey") // legacy single kv beats config
	if err := r.st.SetKV(ctx, KVArtRemovers, `["ffmpeg","corridorkey"]`); err != nil {
		t.Fatal(err)
	}
	assertRemovers("ffmpeg", "corridorkey") // list kv beats everything
	if err := r.st.SetKV(ctx, KVArtRemovers, `["photoshop"]`); err != nil {
		t.Fatal(err)
	}
	assertRemovers("ffmpeg") // garbage entries dropped -> default fallback
}

// A shutdown while the keyer runs must conclude like a shutdown during
// either agy stage — job over, context error surfaced — NOT like a keying
// failure.
func TestArtGenTransShutdownDuringKeying(t *testing.T) {
	r := newRig(t, fakeagent.Scenario{Behavior: "art"})
	ctx := context.Background()

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	orig := chromaRemove
	chromaRemove = func(kctx context.Context, remover, ckDir, in, out string, key chroma.Key) error {
		cancel()      // the operator stops the engine mid-keying…
		<-kctx.Done() // …which kills the keyer through its context
		return kctx.Err()
	}
	t.Cleanup(func() { chromaRemove = orig })

	pass, err := r.runner.StartPass(ctx)
	if err != nil {
		t.Fatal(err)
	}
	runErr := r.runner.Run(runCtx, pass, Job{Description: "hero cutout", Target: "assets/hero.png", Transparent: true})
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("Run err = %v; want context.Canceled", runErr)
	}
	got, err := r.st.GetPass(ctx, pass.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Failure != domain.FailSetup || got.Outcome != domain.OutcomeFailed {
		t.Fatalf("cancelled job settled wrong: failure=%s outcome=%s", got.Failure, got.Outcome)
	}
}

// Without a fresh agy conversation record the job must fail — for BOTH art
// flows: the asset must come from agy (a done-report with no record means the
// orchestrator fabricated it), and -trans additionally cannot resume the
// session for the rescreen.
func TestArtJobFailsWithoutConversation(t *testing.T) {
	for _, trans := range []bool{false, true} {
		name := "art-gen"
		if trans {
			name = "art-gen-trans"
		}
		t.Run(name, func(t *testing.T) {
			r := newRig(t, fakeagent.Scenario{Behavior: "art"})
			// No HAL_AGY_STATE_DIR: the fake agent records no conversation.
			t.Setenv("HAL_AGY_STATE_DIR", "")
			ctx := context.Background()

			err, pass := r.run(ctx, Job{Description: "hero", Transparent: trans})
			if err == nil {
				t.Fatal("job succeeded despite no agy conversation record")
			}
			if pass.Outcome != domain.OutcomeFailed {
				t.Fatalf("pass outcome = %s; want failed", pass.Outcome)
			}
		})
	}
}

// artAgyModel resolves the image label agy is handed: the launch-verified kv
// when it names an allowed label, else the preferred default.
func TestArtAgyModelResolution(t *testing.T) {
	r := newRig(t, fakeagent.Scenario{Behavior: "art"})
	ctx := context.Background()

	if got := r.runner.artAgyModel(ctx); got != domain.ArtAgyModels[0] {
		t.Errorf("artAgyModel(default) = %q; want %q", got, domain.ArtAgyModels[0])
	}
	if err := r.st.SetKV(ctx, KVArtAgyModel, "Gemini 3.5 Flash (High)"); err != nil {
		t.Fatal(err)
	}
	if got := r.runner.artAgyModel(ctx); got != "Gemini 3.5 Flash (High)" {
		t.Errorf("artAgyModel(kv) = %q; want the verified label", got)
	}
	// A stale/garbage kv is ignored.
	if err := r.st.SetKV(ctx, KVArtAgyModel, "gemini 3 pro"); err != nil {
		t.Fatal(err)
	}
	if got := r.runner.artAgyModel(ctx); got != domain.ArtAgyModels[0] {
		t.Errorf("artAgyModel(garbage kv) = %q; want %q", got, domain.ArtAgyModels[0])
	}
}

// A blocked self-report concludes the pass cleanly (outcome blocked, no
// failure kind) and surfaces the note in the returned error.
func TestArtJobBlockedSelfReport(t *testing.T) {
	r := newRig(t, fakeagent.Scenario{Behavior: "blocked"})
	ctx := context.Background()

	err, pass := r.run(ctx, Job{Description: "impossible hero"})
	if err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("Run err = %v; want a blocked report", err)
	}
	if pass.Outcome != domain.OutcomeBlocked || pass.Failure != domain.FailNone {
		t.Fatalf("pass settled wrong: outcome=%s failure=%s", pass.Outcome, pass.Failure)
	}
}
