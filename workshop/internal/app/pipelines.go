package app

import (
	"fmt"
	"os"
	"strings"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/config"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/driver"
)

// AddPipeline appends a new pipeline definition to the runtime-overrides
// config layer and reloads it into the live config, so status/config reads
// see it right away. It does NOT start the pipeline's worker or worktree —
// the engine only builds workers at RunHeadless startup, so the new lane
// comes alive on the next `workshop up`/`run`.
func (a *App) AddPipeline(pc config.PipelineConfig) error {
	name := strings.ToLower(strings.TrimSpace(pc.Name))
	if !config.ValidPipelineName(name) {
		return fmt.Errorf("pipeline name %q must start with a letter/digit and contain only lowercase letters, digits, - or _", pc.Name)
	}
	pc.Name = name
	if pc.Agent == "" {
		pc.Agent = "claude"
	}
	if _, err := driver.New(pc.Agent); err != nil {
		return err
	}
	if !domain.ValidEffort(pc.Effort) {
		return fmt.Errorf("effort %q is not one of %v", pc.Effort, domain.Efforts)
	}

	// The overrides file holds the COMPLETE pipeline list, so this
	// read-modify-write must be serialized: two concurrent adds would both
	// read the same base list and the loser's pipeline would vanish.
	a.pipelineMu.Lock()
	defer a.pipelineMu.Unlock()
	pipelines := a.currentPipelineConfigs()
	for _, existing := range pipelines {
		if strings.EqualFold(existing.Name, name) {
			return fmt.Errorf("pipeline %q already exists", name)
		}
	}
	return a.persistPipelines(append(pipelines, pc))
}

// DeletePipeline drops a pipeline from the runtime-overrides config layer.
// "main" (config.DefaultPipelineName) — Workshop's always-present default
// lane — can never be deleted, and neither can the last remaining pipeline:
// there must always be at least one lane to do work.
func (a *App) DeletePipeline(name string) error {
	if strings.EqualFold(name, config.DefaultPipelineName) {
		return fmt.Errorf("the %q pipeline is Workshop's default lane and can't be deleted", config.DefaultPipelineName)
	}
	a.pipelineMu.Lock()
	defer a.pipelineMu.Unlock()
	pipelines := a.currentPipelineConfigs()
	out := make([]config.PipelineConfig, 0, len(pipelines))
	found := false
	for _, p := range pipelines {
		if strings.EqualFold(p.Name, name) {
			found = true
			continue
		}
		out = append(out, p)
	}
	if !found {
		return fmt.Errorf("no pipeline named %q", name)
	}
	if len(out) == 0 {
		return fmt.Errorf("can't delete the last pipeline — at least one must remain")
	}
	return a.persistPipelines(out)
}

// currentPipelineConfigs returns the effective pipelines as PipelineConfig
// entries, materializing the implicit "main" pipeline when no [[pipelines]]
// are configured yet. Without this, the first AddPipeline call would write
// only the new entry to the overrides file — which replaces the pipelines
// array wholesale (see config.WriteOverridePipelines) — silently deleting
// the implicit "main" lane it was standing in for.
func (a *App) currentPipelineConfigs() []config.PipelineConfig {
	cfg := a.Res().Config
	if len(cfg.Pipelines) > 0 {
		return append([]config.PipelineConfig{}, cfg.Pipelines...)
	}
	implicit := cfg.ResolvedPipelines()[0]
	return []config.PipelineConfig{{
		Name:   implicit.Name,
		Agent:  implicit.Bundle.Agent,
		Model:  implicit.Bundle.Model,
		Effort: implicit.Bundle.Effort,
	}}
}

// persistPipelines writes the full pipeline list to the runtime-overrides
// file and reloads it into the live config.
func (a *App) persistPipelines(pipelines []config.PipelineConfig) error {
	if err := config.WriteOverridePipelines(config.OverridesFile(a.RepoDir), pipelines); err != nil {
		return err
	}
	return a.reloadConfig()
}

// reloadConfig re-resolves layered config from disk (user/repo/override) so
// a config-file write made outside Open (AddPipeline, DeletePipeline) is
// visible to status/config reads without restarting the process.
func (a *App) reloadConfig() error {
	res, err := config.Load(config.UserConfigFile(), config.RepoConfigFile(a.RepoDir), config.OverridesFile(a.RepoDir), os.Getenv)
	if err != nil {
		return err
	}
	a.res.Store(res)
	return nil
}
