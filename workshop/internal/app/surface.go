package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/config"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/driver"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/gitx"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/statedir"
)

// Goal reads .workshop/GOAL.md ("" if absent).
func (a *App) Goal() string {
	data, err := os.ReadFile(config.GoalFile(a.RepoDir))
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(string(data), "\uFEFF")
}

// SetGoal writes .workshop/GOAL.md.
func (a *App) SetGoal(content string) error {
	path := config.GoalFile(a.RepoDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return statedir.WriteFileAtomic(path, []byte(content))
}

var fragmentRe = regexp.MustCompile(`^(project|base|types/[a-z0-9_-]+|pipelines/[a-z0-9_-]+)$`)

// Fragment reads a prompt fragment by its logical name ("project",
// "types/art", ...).
func (a *App) Fragment(name string) (string, error) {
	if !fragmentRe.MatchString(name) {
		return "", fmt.Errorf("invalid fragment name %q", name)
	}
	data, err := os.ReadFile(filepath.Join(config.PromptsDir(a.RepoDir), filepath.FromSlash(name)+".md"))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// SetFragment writes a prompt fragment.
func (a *App) SetFragment(name, content string) error {
	if !fragmentRe.MatchString(name) {
		return fmt.Errorf("invalid fragment name %q", name)
	}
	path := filepath.Join(config.PromptsDir(a.RepoDir), filepath.FromSlash(name)+".md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return statedir.WriteFileAtomic(path, []byte(content))
}

// SetPipelineDesired starts/stops one pipeline's loop live via the halt
// machinery: an unbounded worker parks on halt and resumes when cleared.
func (a *App) SetPipelineDesired(ctx context.Context, name string, running bool) error {
	found := false
	for _, p := range a.Res.Config.ResolvedPipelines() {
		if p.Name == name {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("no pipeline named %q", name)
	}
	if running {
		return a.Store.SetHalted(ctx, name, "") // clears operator/breaker/auth halts
	}
	return a.Store.SetHalted(ctx, name, "operator")
}

// SetPipelineBundle sets (or, for a zero bundle, clears) the live
// agent/model/effort override for a pipeline. Workers re-read it every pass,
// so it takes effect on the NEXT pass without a restart — the successor of
// the old tool's agent.json workflow.
func (a *App) SetPipelineBundle(ctx context.Context, name string, b domain.Bundle) error {
	found := false
	for _, p := range a.Res.Config.ResolvedPipelines() {
		if p.Name == name {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("no pipeline named %q", name)
	}
	if b.Agent != "" {
		if _, err := driver.New(b.Agent); err != nil {
			return err
		}
	}
	if !domain.ValidEffort(b.Effort) {
		return fmt.Errorf("effort %q is not one of %v", b.Effort, domain.Efforts)
	}
	if err := a.Store.SetPipelineBundle(ctx, name, b); err != nil {
		return err
	}
	a.Bus.Publish(ctx, domain.Event{Type: "pipeline.bundle", Pipeline: name, Payload: map[string]any{
		"agent": b.Agent, "model": b.Model, "effort": b.Effort, "cleared": b.IsZero(),
	}})
	return nil
}

// QueueLane is one lane's merge-queue view.
type QueueLane struct {
	Pipeline       string `json:"pipeline"`
	Branch         string `json:"branch"`
	Ahead          int    `json:"ahead"`
	Blocked        bool   `json:"blocked"`
	BlockedBy      string `json:"blockedBy,omitempty"`
	ProvenCulprit  bool   `json:"provenCulprit,omitempty"`
	ConflictTaskID string `json:"conflictTaskId,omitempty"`
}

// QueueState reports the merge queue (empty when worktrees are off).
func (a *App) QueueState(ctx context.Context) ([]QueueLane, error) {
	cfg := a.Res.Config
	if !cfg.WorktreesEnabled() {
		return nil, nil
	}
	trunk := cfg.Project.Trunk
	if trunk == "" {
		trunk, _ = gitx.CurrentBranch(ctx, a.RepoDir)
	}
	var lanes []QueueLane
	for _, p := range a.EnabledPipelines() {
		branch := cfg.Git.BranchPrefix + p.Name
		lane := QueueLane{Pipeline: p.Name, Branch: branch}
		if gitx.BranchExists(ctx, a.RepoDir, branch) && trunk != "" {
			lane.Ahead, _ = gitx.AheadCount(ctx, a.RepoDir, trunk, branch)
		}
		li, err := a.Store.GetIntegration(ctx, p.Name)
		if err == nil {
			lane.Blocked = li.Blocked
			lane.BlockedBy = li.BlockedBy
			lane.ProvenCulprit = li.ProvenCulprit
			lane.ConflictTaskID = li.ConflictTaskID
		}
		lanes = append(lanes, lane)
	}
	return lanes, nil
}

// ConfigView is the effective config plus provenance for the UI/CLI.
type ConfigView struct {
	Effective  config.Config     `json:"effective"`
	Provenance map[string]string `json:"provenance"`
	Warnings   []string          `json:"warnings,omitempty"`
	Worktrees  bool              `json:"worktreesEnabled"`
}

// ConfigSnapshot assembles the config view.
func (a *App) ConfigSnapshot() ConfigView {
	return ConfigView{
		Effective:  a.Res.Config,
		Provenance: a.Res.Provenance,
		Warnings:   a.Res.Warnings,
		Worktrees:  a.Res.Config.WorktreesEnabled(),
	}
}

// PassLog returns the captured log of a pass (header-only for blind drivers).
func (a *App) PassLog(ctx context.Context, id int64) (string, error) {
	p, err := a.Store.GetPass(ctx, id)
	if err != nil {
		return "", err
	}
	if p.LogPath == "" {
		return "", nil
	}
	data, err := os.ReadFile(p.LogPath)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

var _ = domain.MainBacklog
