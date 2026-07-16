package engine

import (
	"context"
	"errors"
	"image"
	"os"
	"path/filepath"
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

// artRig extends the fake-agent rig for art passes: the "agy" driver slot is
// pre-seeded with the fake driver (the real one would spawn agy.exe), and
// WORKSHOP_AGY_STATE_DIR points at a scratch dir the fake agent's
// conversation bookkeeping writes into.
func artRig(t *testing.T, sc fakeagent.Scenario) *testRig {
	t.Helper()
	r := newRig(t, sc, func(cfg *WorkerConfig) {
		cfg.AgyMu = &sync.RWMutex{}
	})
	t.Setenv("WORKSHOP_AGY_STATE_DIR", t.TempDir())
	r.worker.drivers["agy"] = driver.NewFake()
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

// Without a fresh conversation record the -trans flow cannot resume the agy
// session — the pass must fail rather than silently produce an opaque asset.
func TestArtGenTransFailsWithoutConversation(t *testing.T) {
	r := newRig(t, fakeagent.Scenario{Behavior: "art"}, nil)
	// No WORKSHOP_AGY_STATE_DIR: the fake agent records no conversation.
	t.Setenv("WORKSHOP_AGY_STATE_DIR", "")
	r.worker.drivers["agy"] = driver.NewFake()
	ctx := context.Background()
	task, err := r.st.AddTask(ctx, &domain.Task{Title: "hero", Type: domain.ArtGenTransType}, false)
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
		t.Fatal("task completed despite no resumable conversation")
	}
}
