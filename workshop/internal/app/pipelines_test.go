package app

import (
	"fmt"
	"sync"
	"testing"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/config"
)

// Concurrent pipeline mutations against concurrent config reads: the adds
// must all survive (the overrides file is a whole-list write, so an unlocked
// read-modify-write loses entries), and the Res pointer handoff must be
// race-clean (run under -race).
func TestConcurrentPipelineMutationsLoseNothing(t *testing.T) {
	a := newTestAppForPipelines(t)

	const n = 8
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := a.AddPipeline(config.PipelineConfig{Name: fmt.Sprintf("lane%d", i)}); err != nil {
				t.Errorf("add lane%d: %v", i, err)
			}
		}(i)
		wg.Add(1)
		go func() { // racing readers of the live config snapshot
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = a.Res().Config.ResolvedPipelines()
				_ = a.currentPipelineConfigs()
			}
		}()
	}
	wg.Wait()

	pl := a.Res().Config.ResolvedPipelines()
	if len(pl) != n+1 { // implicit main + n adds
		t.Fatalf("pipelines after %d concurrent adds = %d, want %d: %+v", n, len(pl), n+1, pl)
	}
}

// newTestAppForPipelines is newTestApp plus a sandboxed WORKSHOP_STATE_DIR:
// AddPipeline/DeletePipeline write config.OverridesFile(a.RepoDir), which is
// computed independently of a.StateDir (it's keyed off the real state root),
// so without this a test would write into the operator's real state dir.
func newTestAppForPipelines(t *testing.T) *App {
	t.Helper()
	t.Setenv("WORKSHOP_STATE_DIR", t.TempDir())
	return newTestApp(t, initRepo(t))
}

func TestAddPipelineThenDeletePipeline(t *testing.T) {
	a := newTestAppForPipelines(t)

	if err := a.AddPipeline(config.PipelineConfig{Name: "art", Agent: "agy", Model: "gemini-3-flash"}); err != nil {
		t.Fatal(err)
	}
	pl := a.Res().Config.ResolvedPipelines()
	if len(pl) != 2 {
		t.Fatalf("pipelines after add = %d, want 2 (implicit main + art): %+v", len(pl), pl)
	}
	names := map[string]bool{}
	for _, p := range pl {
		names[p.Name] = true
	}
	if !names[config.DefaultPipelineName] || !names["art"] {
		t.Fatalf("expected main+art, got %+v", pl)
	}

	if err := a.DeletePipeline("art"); err != nil {
		t.Fatal(err)
	}
	pl = a.Res().Config.ResolvedPipelines()
	if len(pl) != 1 || pl[0].Name != config.DefaultPipelineName {
		t.Fatalf("pipelines after delete = %+v, want just main", pl)
	}
}

func TestAddPipelineValidation(t *testing.T) {
	a := newTestAppForPipelines(t)

	if err := a.AddPipeline(config.PipelineConfig{Name: "bad name"}); err == nil {
		t.Fatal("invalid name: want error, got nil")
	}
	if err := a.AddPipeline(config.PipelineConfig{Name: "art", Agent: "bogus-agent"}); err == nil {
		t.Fatal("unrecognized agent: want error, got nil")
	}
	if err := a.AddPipeline(config.PipelineConfig{Name: "art", Effort: "bogus-effort"}); err == nil {
		t.Fatal("unrecognized effort: want error, got nil")
	}
	if err := a.AddPipeline(config.PipelineConfig{Name: config.DefaultPipelineName}); err == nil {
		t.Fatal("duplicate name (implicit main): want error, got nil")
	}
	if pl := a.Res().Config.ResolvedPipelines(); len(pl) != 1 {
		t.Fatalf("rejected AddPipeline calls left pipelines = %+v, want just the implicit main", pl)
	}
}

func TestDeletePipelineProtectsMain(t *testing.T) {
	a := newTestAppForPipelines(t)

	if err := a.DeletePipeline(config.DefaultPipelineName); err == nil {
		t.Fatal("deleting main: want error, got nil")
	}
	if err := a.DeletePipeline("no-such-pipeline"); err == nil {
		t.Fatal("deleting unknown pipeline: want error, got nil")
	}
	if err := a.AddPipeline(config.PipelineConfig{Name: "art"}); err != nil {
		t.Fatal(err)
	}
	// main stays protected even once other lanes exist alongside it.
	if err := a.DeletePipeline(config.DefaultPipelineName); err == nil {
		t.Fatal("deleting main with other lanes present: want error, got nil")
	}
}

// TestDeletePipelineProtectsLastLane covers configs that never had a "main"
// pipeline at all (a repo with only custom-named [[pipelines]] entries) —
// the main-name guard doesn't apply there, so the last-lane count guard is
// what stops the backlog from ending up with zero lanes to do work.
func TestDeletePipelineProtectsLastLane(t *testing.T) {
	a := newTestAppForPipelines(t)
	a.Res().Config.Pipelines = []config.PipelineConfig{{Name: "solo", Agent: "claude"}}

	if err := a.DeletePipeline("solo"); err == nil {
		t.Fatal("deleting the last remaining pipeline: want error, got nil")
	}
}
