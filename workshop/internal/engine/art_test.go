package engine

import (
	"context"
	"errors"
	"image"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/chroma"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/driver"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/fakeagent"
)

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

// artRig extends the fake-agent rig for art passes: the "claude" driver slot
// (art passes force the claude orchestrator) is pre-seeded with the fake
// driver (the real one would spawn claude.exe), and WORKSHOP_AGY_STATE_DIR
// points at a scratch dir the fake agent's agy-style conversation bookkeeping
// writes into.
func artRig(t *testing.T, sc fakeagent.Scenario, tweaks ...func(cfg *WorkerConfig)) *testRig {
	t.Helper()
	r := newRig(t, sc, func(cfg *WorkerConfig) {
		cfg.AgyMu = &sync.RWMutex{}
		for _, tweak := range tweaks {
			tweak(cfg)
		}
	})
	t.Setenv("WORKSHOP_AGY_STATE_DIR", t.TempDir())
	r.worker.drivers["claude"] = driver.NewFake()
	return r
}

func TestArtGenPass(t *testing.T) {
	r := artRig(t, fakeagent.Scenario{Behavior: "art"})
	ctx := context.Background()
	task, err := r.st.AddTask(ctx, &domain.Task{Title: "Draw a cosmic hero", Type: domain.ArtGenType}, false)
	if err != nil {
		t.Fatal(err)
	}

	res, err := r.worker.RunPass(ctx)
	if err != nil || res != PassRan {
		t.Fatalf("RunPass = %v, %v; want PassRan, nil", res, err)
	}

	target := filepath.Join(r.repo, "assets", "art", "draw-a-cosmic-hero.png")
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("generated asset missing: %v", err)
	}
	got, err := r.st.GetTask(ctx, task.ID)
	if err != nil || got.Status != domain.TaskDone {
		t.Fatalf("task status = %v, %v; want done", got.Status, err)
	}
	if commits := r.commits(); len(commits) < 2 {
		t.Fatalf("expected the art pass to commit; got %d commits", len(commits))
	}
}

func TestArtGenTransPass(t *testing.T) {
	r := artRig(t, fakeagent.Scenario{Behavior: "art"})
	ctx := context.Background()
	task, err := r.st.AddTask(ctx, &domain.Task{
		Title: "hero cutout", Type: domain.ArtGenTransType, Files: []string{"assets/hero.png"},
	}, false)
	if err != nil {
		t.Fatal(err)
	}

	res, err := r.worker.RunPass(ctx)
	if err != nil || res != PassRan {
		t.Fatalf("RunPass = %v, %v; want PassRan, nil", res, err)
	}

	target := filepath.Join(r.repo, "assets", "hero.png")
	if err := chroma.VerifyTransparency(target); err != nil {
		t.Fatalf("final asset is not transparent: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.repo, "assets", "hero-screen.png")); !os.IsNotExist(err) {
		t.Fatalf("screen intermediate should be removed from the repo (err=%v)", err)
	}
	got, err := r.st.GetTask(ctx, task.ID)
	if err != nil || got.Status != domain.TaskDone {
		t.Fatalf("task status = %v, %v; want done", got.Status, err)
	}
	// The exclusive agy slot must be free again after the pass.
	if !r.cfg.AgyMu.TryLock() {
		t.Fatal("AgyMu still held after the pass")
	}
	r.cfg.AgyMu.Unlock()
}

// An agy-shaped pass has no ExecPlan.TranscriptPath (the conversation id is
// runtime-minted): the engine must recover the id post-run and archive the
// runtime's brain transcript beside the pass log — and, with [export]
// configured, mirror the evidence (plus the rendered markdown) into the
// export folder.
func TestArtPassArchivesAndExportsTranscript(t *testing.T) {
	exportDir := filepath.Join(t.TempDir(), "audit", "art")
	r := artRig(t, fakeagent.Scenario{Behavior: "art"}, func(cfg *WorkerConfig) {
		cfg.ExportDir = exportDir
		cfg.ExportHumanReadable = true
	})
	ctx := context.Background()
	if _, err := r.st.AddTask(ctx, &domain.Task{Title: "audited hero", Type: domain.ArtGenType}, false); err != nil {
		t.Fatal(err)
	}
	if res, err := r.worker.RunPass(ctx); err != nil || res != PassRan {
		t.Fatalf("RunPass = %v, %v; want PassRan, nil", res, err)
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
// .png name. The pass must detect the real format and re-encode, so the
// committed asset is a genuine PNG.
func TestArtGenPassNormalizesMislabeledBytes(t *testing.T) {
	r := artRig(t, fakeagent.Scenario{Behavior: "art", ArtEncode: "jpeg"})
	ctx := context.Background()
	task, err := r.st.AddTask(ctx, &domain.Task{Title: "mislabeled hero", Type: domain.ArtGenType}, false)
	if err != nil {
		t.Fatal(err)
	}

	res, err := r.worker.RunPass(ctx)
	if err != nil || res != PassRan {
		t.Fatalf("RunPass = %v, %v; want PassRan, nil", res, err)
	}
	got, err := r.st.GetTask(ctx, task.ID)
	if err != nil || got.Status != domain.TaskDone {
		t.Fatalf("task status = %v, %v; want done", got.Status, err)
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
func TestArtGenTransPassNormalizesMislabeledBytes(t *testing.T) {
	r := artRig(t, fakeagent.Scenario{Behavior: "art", ArtEncode: "jpeg"})
	ctx := context.Background()
	task, err := r.st.AddTask(ctx, &domain.Task{
		Title: "hero cutout", Type: domain.ArtGenTransType, Files: []string{"assets/hero.png"},
	}, false)
	if err != nil {
		t.Fatal(err)
	}

	res, err := r.worker.RunPass(ctx)
	if err != nil || res != PassRan {
		t.Fatalf("RunPass = %v, %v; want PassRan, nil", res, err)
	}
	got, err := r.st.GetTask(ctx, task.ID)
	if err != nil || got.Status != domain.TaskDone {
		t.Fatalf("task status = %v, %v; want done", got.Status, err)
	}
	if err := chroma.VerifyTransparency(filepath.Join(r.repo, "assets", "hero.png")); err != nil {
		t.Fatalf("final asset is not transparent: %v", err)
	}
}

// With several keyers selected (kv override, primary first) the pass must
// commit the PRIMARY's output at the target and archive one comparison image
// per keyer beside the pass log — the operator's side-by-side evidence.
func TestArtGenTransMultiKeyerComparison(t *testing.T) {
	r := artRig(t, fakeagent.Scenario{Behavior: "art"})
	ctx := context.Background()
	if err := r.st.SetKV(ctx, KVArtRemovers, `["builtin","ffmpeg","corridorkey"]`); err != nil {
		t.Fatal(err)
	}
	var calls []string
	orig := chromaRemove
	// Hermetic stub: record which backend was asked for, key via builtin.
	chromaRemove = func(kctx context.Context, remover, ckDir, in, out string, key chroma.Key) error {
		calls = append(calls, remover)
		return chroma.RemoveBuiltin(in, out, key)
	}
	t.Cleanup(func() { chromaRemove = orig })

	task, err := r.st.AddTask(ctx, &domain.Task{
		Title: "hero cutout", Type: domain.ArtGenTransType, Files: []string{"assets/hero.png"},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := r.worker.RunPass(ctx); err != nil || res != PassRan {
		t.Fatalf("RunPass = %v, %v; want PassRan, nil", res, err)
	}

	if want := []string{"builtin", "ffmpeg", "corridorkey"}; len(calls) != 3 || calls[0] != want[0] || calls[1] != want[1] || calls[2] != want[2] {
		t.Fatalf("keyer invocations = %v; want %v (primary first, then comparisons)", calls, want)
	}
	if err := chroma.VerifyTransparency(filepath.Join(r.repo, "assets", "hero.png")); err != nil {
		t.Fatalf("final asset is not transparent: %v", err)
	}
	for _, k := range []string{"builtin", "ffmpeg", "corridorkey"} {
		cmp := filepath.Join(r.cfg.LogDir, "iter-000001.keyed-"+k+".png")
		if err := chroma.VerifyTransparency(cmp); err != nil {
			t.Errorf("comparison image for %s missing/not keyed: %v", k, err)
		}
	}
	if _, err := os.Stat(filepath.Join(r.repo, "assets", "hero-screen.png")); !os.IsNotExist(err) {
		t.Fatalf("screen intermediate should be removed from the repo (err=%v)", err)
	}
	// Comparison images are forensic material and must never enter the repo.
	if _, err := os.Stat(filepath.Join(r.repo, "assets", "hero.keyed-ffmpeg.png")); !os.IsNotExist(err) {
		t.Fatalf("comparison image leaked into the repo (err=%v)", err)
	}
	if got, err := r.st.GetTask(ctx, task.ID); err != nil || got.Status != domain.TaskDone {
		t.Fatalf("task status = %v, %v; want done", got.Status, err)
	}
}

// A comparison keyer failing (missing backend, bad checkout, …) must never
// fail the pass: the committed asset already exists — the comparison is
// evidence, not the deliverable.
func TestArtGenTransComparisonKeyerFailureTolerated(t *testing.T) {
	r := artRig(t, fakeagent.Scenario{Behavior: "art"})
	ctx := context.Background()
	if err := r.st.SetKV(ctx, KVArtRemovers, `["builtin","corridorkey"]`); err != nil {
		t.Fatal(err)
	}
	orig := chromaRemove
	chromaRemove = func(kctx context.Context, remover, ckDir, in, out string, key chroma.Key) error {
		if remover == "corridorkey" {
			return errors.New("corridorkey CLI not found")
		}
		return chroma.RemoveBuiltin(in, out, key)
	}
	t.Cleanup(func() { chromaRemove = orig })

	task, err := r.st.AddTask(ctx, &domain.Task{
		Title: "hero cutout", Type: domain.ArtGenTransType, Files: []string{"assets/hero.png"},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := r.worker.RunPass(ctx); err != nil || res != PassRan {
		t.Fatalf("RunPass = %v, %v; want PassRan, nil", res, err)
	}
	if got, err := r.st.GetTask(ctx, task.ID); err != nil || got.Status != domain.TaskDone {
		t.Fatalf("task status = %v, %v; want done despite the failed comparison keyer", got.Status, err)
	}
	if r.worker.consecFails != 0 {
		t.Fatalf("comparison failure counted toward the breaker (consecFails=%d)", r.worker.consecFails)
	}
	if err := chroma.VerifyTransparency(filepath.Join(r.cfg.LogDir, "iter-000001.keyed-builtin.png")); err != nil {
		t.Errorf("primary comparison copy missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.cfg.LogDir, "iter-000001.keyed-corridorkey.png")); !os.IsNotExist(err) {
		t.Fatalf("failed comparison keyer should leave no image (err=%v)", err)
	}
}

// artRemovers precedence: JSON-list kv > legacy single kv > config > default,
// with unknown names dropped defensively.
func TestArtRemoversResolution(t *testing.T) {
	r := artRig(t, fakeagent.Scenario{Behavior: "art"})
	ctx := context.Background()
	assertRemovers := func(want ...string) {
		t.Helper()
		got, _ := r.worker.artRemovers(ctx)
		if len(got) != len(want) {
			t.Fatalf("artRemovers = %v; want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("artRemovers = %v; want %v", got, want)
			}
		}
	}
	assertRemovers("builtin") // nothing configured -> default
	r.worker.cfg.ArtRemovers = []string{"ffmpeg", "builtin"}
	assertRemovers("ffmpeg", "builtin") // config list
	if err := r.st.SetKV(ctx, KVArtRemover, "corridorkey"); err != nil {
		t.Fatal(err)
	}
	assertRemovers("corridorkey") // legacy single kv beats config
	if err := r.st.SetKV(ctx, KVArtRemovers, `["builtin","ffmpeg"]`); err != nil {
		t.Fatal(err)
	}
	assertRemovers("builtin", "ffmpeg") // list kv beats everything
	if err := r.st.SetKV(ctx, KVArtRemovers, `["photoshop"]`); err != nil {
		t.Fatal(err)
	}
	assertRemovers("builtin") // garbage entries dropped -> default fallback
}

// An engine shutdown while the keyer runs must conclude like a shutdown
// during either agy stage — pass over, claim back — NOT like a keying
// failure: cancellation must never burn a task attempt or count toward the
// breaker (a breaker trip persists as a halt across restarts).
func TestArtGenTransShutdownDuringKeying(t *testing.T) {
	r := artRig(t, fakeagent.Scenario{Behavior: "art"})
	ctx := context.Background()
	task, err := r.st.AddTask(ctx, &domain.Task{
		Title: "hero cutout", Type: domain.ArtGenTransType, Files: []string{"assets/hero.png"},
	}, false)
	if err != nil {
		t.Fatal(err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	orig := chromaRemove
	chromaRemove = func(kctx context.Context, remover, ckDir, in, out string, key chroma.Key) error {
		cancel()      // the operator stops the engine mid-keying…
		<-kctx.Done() // …which kills the keyer through its context
		return kctx.Err()
	}
	t.Cleanup(func() { chromaRemove = orig })

	res, err := r.worker.RunPass(runCtx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunPass err = %v; want context.Canceled", err)
	}
	if res != PassRan {
		t.Fatalf("RunPass res = %v; want PassRan", res)
	}
	if r.worker.consecFails != 0 {
		t.Fatalf("shutdown counted toward the breaker (consecFails=%d)", r.worker.consecFails)
	}
	got, err := r.st.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Attempts != 0 || got.Status == domain.TaskStuck {
		t.Fatalf("shutdown burned a task attempt (attempts=%d status=%s)", got.Attempts, got.Status)
	}
}

// Without a fresh agy conversation record the pass must fail — for BOTH art
// types: the asset must come from agy (a done-report with no record means the
// orchestrator fabricated it), and -trans additionally cannot resume the
// session for the rescreen.
func TestArtPassFailsWithoutConversation(t *testing.T) {
	for _, typ := range []string{domain.ArtGenType, domain.ArtGenTransType} {
		t.Run(typ, func(t *testing.T) {
			r := newRig(t, fakeagent.Scenario{Behavior: "art"}, nil)
			// No WORKSHOP_AGY_STATE_DIR: the fake agent records no conversation.
			t.Setenv("WORKSHOP_AGY_STATE_DIR", "")
			r.worker.drivers["claude"] = driver.NewFake()
			ctx := context.Background()
			task, err := r.st.AddTask(ctx, &domain.Task{Title: "hero", Type: typ}, false)
			if err != nil {
				t.Fatal(err)
			}

			if res, _ := r.worker.RunPass(ctx); res != PassRan {
				t.Fatalf("RunPass = %v; want PassRan", res)
			}
			got, err := r.st.GetTask(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status == domain.TaskDone {
				t.Fatal("task completed despite no agy conversation record")
			}
		})
	}
}

// resolveArt must force every art pass onto claude with a frontier model —
// keeping an allowed routed choice, overriding anything else — and
// artAgyModel must resolve the image label agy is handed (pin > verified kv >
// default).
func TestResolveArtForcesFrontierClaude(t *testing.T) {
	r := artRig(t, fakeagent.Scenario{Behavior: "art"})
	// The claude slot holds the fake driver (artRig): resolveArt probes it
	// instead of a real claude binary.
	ctx := context.Background()
	cases := []struct {
		routed    domain.Bundle
		wantModel string
	}{
		{domain.Bundle{Agent: "agy", Model: "Gemini 3.1 Pro (High)"}, domain.ArtClaudeDefault},
		{domain.Bundle{Agent: "claude", Model: "claude-sonnet-5"}, domain.ArtClaudeDefault},
		{domain.Bundle{Agent: "claude", Model: "claude-opus-4-8"}, "claude-opus-4-8"},
		{domain.Bundle{Agent: "claude", Model: "claude-fable-5"}, "claude-fable-5"},
		{domain.Bundle{}, domain.ArtClaudeDefault},
	}
	for _, c := range cases {
		res, err := r.worker.resolveArt(ctx, &resolved{bundle: c.routed})
		if err != nil {
			t.Fatalf("resolveArt(%+v): %v", c.routed, err)
		}
		if res.bundle.Agent != "claude" || res.bundle.Model != c.wantModel {
			t.Errorf("resolveArt(%+v) = %s/%s; want claude/%s", c.routed, res.bundle.Agent, res.bundle.Model, c.wantModel)
		}
		if !res.agyLockHeld {
			t.Errorf("resolveArt(%+v): agyLockHeld not set", c.routed)
		}
	}

	// artAgyModel: an agy pin with an allowed label wins…
	pin := &resolved{bundle: domain.Bundle{Agent: "agy", Model: "Gemini 3.5 Flash (High)"}}
	if got := r.worker.artAgyModel(ctx, pin); got != "Gemini 3.5 Flash (High)" {
		t.Errorf("artAgyModel(pinned) = %q; want the pinned label", got)
	}
	// …else the launch-verified kv…
	if err := r.st.SetKV(ctx, KVArtAgyModel, "Gemini 3.5 Flash (High)"); err != nil {
		t.Fatal(err)
	}
	if got := r.worker.artAgyModel(ctx, &resolved{bundle: domain.Bundle{Agent: "claude"}}); got != "Gemini 3.5 Flash (High)" {
		t.Errorf("artAgyModel(kv) = %q; want the verified label", got)
	}
	// …else the preferred default (a stale/garbage kv is ignored).
	if err := r.st.SetKV(ctx, KVArtAgyModel, "gemini 3 pro"); err != nil {
		t.Fatal(err)
	}
	if got := r.worker.artAgyModel(ctx, &resolved{bundle: domain.Bundle{}}); got != domain.ArtAgyModels[0] {
		t.Errorf("artAgyModel(default) = %q; want %q", got, domain.ArtAgyModels[0])
	}
}
