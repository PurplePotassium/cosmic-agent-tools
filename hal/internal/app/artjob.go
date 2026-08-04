package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/artjob"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/chroma"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/config"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/statedir"
)

// ErrArtJobBusy is returned while a previous art job is still running: the
// whole agy sequence holds the exclusive agy slot, so stacking jobs would
// only queue them anyway — reject the second one visibly instead.
var ErrArtJobBusy = errors.New("an art job is already running — wait for it to finish")

// ArtJobReq is one dashboard/API art-generation request.
type ArtJobReq struct {
	// Prompt describes the asset (subject, style, palette); its first line
	// doubles as the job title.
	Prompt string `json:"prompt"`
	// Target is the repo-relative asset path ("" = assets/art/<slug>.png).
	Target string `json:"target"`
	// Transparent selects the rescreen + chroma-key flow (transparent PNG).
	Transparent bool `json:"transparent"`
}

// RunArtJob starts one art-generation job in the background and returns the
// pass id recording it (pipeline "art" in the passes table — the dashboard's
// activity view and run-log routes pick it up like any pass). Single-flight:
// a second job while one runs returns ErrArtJobBusy.
func (a *App) RunArtJob(ctx context.Context, req ArtJobReq) (int64, error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return 0, fmt.Errorf("an art job needs a prompt")
	}
	if !a.artJobRunning.CompareAndSwap(false, true) {
		return 0, ErrArtJobBusy
	}
	runner := artjob.New(a.Store, a.Bus, &a.agyMu, a.artJobConfig())
	if a.artDrv != nil {
		runner.SetDriver(a.artDrv)
	}
	pass, err := runner.StartPass(ctx)
	if err != nil {
		a.artJobRunning.Store(false)
		return 0, err
	}
	job := artjob.Job{Description: req.Prompt, Target: req.Target, Transparent: req.Transparent}
	// The job outlives the HTTP request that kicked it: run detached. Each
	// orchestrator invocation is bounded by [safety].wedge_minutes and the
	// keying step by its own timeout, so the goroutine cannot hang forever.
	go func() {
		defer a.artJobRunning.Store(false)
		_ = runner.Run(context.WithoutCancel(ctx), pass, job)
	}()
	return pass.ID, nil
}

// artJobConfig assembles the ArtRunner wiring from the resolved config.
func (a *App) artJobConfig() artjob.Config {
	cfg := a.Res().Config
	exportDir := ""
	if base, err := a.ExportBase(); err == nil && base != "" {
		exportDir = filepath.Join(base, artjob.Pipeline)
	}
	return artjob.Config{
		RepoDir:             a.RepoDir,
		StateDir:            statedir.PipelineDir(a.StateDir, artjob.Pipeline),
		LogDir:              filepath.Join(a.StateDir, "logs", artjob.Pipeline),
		GoalPath:            config.GoalFile(a.RepoDir),
		PromptsDir:          config.PromptsDir(a.RepoDir),
		SkipPermissions:     cfg.Safety.SkipPermissions,
		Timeout:             time.Duration(cfg.Safety.WedgeMinutes) * time.Minute,
		ArtRemovers:         cfg.ArtKeyers(),
		CorridorkeyDir:      chroma.DiscoverCorridorKey(cfg.Art.CorridorkeyDir),
		ExportDir:           exportDir,
		ExportHumanReadable: cfg.Export.HumanReadable,
	}
}
