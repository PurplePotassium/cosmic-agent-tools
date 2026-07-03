package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
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
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/store"
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

var imageDataURLRe = regexp.MustCompile(`^data:image/(png|jpe?g|gif|webp);base64,(.+)$`)

// attachmentNameRe strips everything but a conservative filename charset —
// the value comes straight from a browser File object, so treat it as
// untrusted path input.
var attachmentNameRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// SaveAttachment decodes a pasted/attached image (a data: URL, as produced by
// FileReader.readAsDataURL) and writes it under <StateDir>/attachments with
// a random-prefixed name. It returns the absolute path so a task's detail
// can reference it directly — the next pass's Read tool can open an image
// by path, no separate image-serving route needed.
func (a *App) SaveAttachment(name, dataURL string) (string, error) {
	m := imageDataURLRe.FindStringSubmatch(dataURL)
	if m == nil {
		return "", fmt.Errorf("attachment must be a base64 image data URL (png/jpeg/gif/webp)")
	}
	raw, err := base64.StdEncoding.DecodeString(m[2])
	if err != nil {
		return "", fmt.Errorf("invalid base64 attachment data: %w", err)
	}
	dir := filepath.Join(a.StateDir, "attachments")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	base := attachmentNameRe.ReplaceAllString(strings.TrimSuffix(filepath.Base(name), filepath.Ext(name)), "-")
	if base == "" {
		base = "image"
	}
	if len(base) > 40 {
		base = base[:40]
	}
	prefix := make([]byte, 8)
	if _, err := rand.Read(prefix); err != nil {
		return "", err
	}
	ext := strings.ToLower(m[1])
	if ext == "jpg" {
		ext = "jpeg"
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%s.%s", hex.EncodeToString(prefix), base, ext))
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// attachmentRefRe matches the markdown image lines SaveAttachment's callers
// write into a task's detail, e.g. ![name](<StateDir>/attachments/xxx.png).
var attachmentRefRe = regexp.MustCompile(`!\[[^\]]*\]\(([^)]+)\)`)

// attachmentPaths extracts the attachment file paths referenced by detail
// that live under <StateDir>/attachments (SaveAttachment's directory);
// references to anything else are ignored.
func (a *App) attachmentPaths(detail string) map[string]bool {
	dir := filepath.Join(a.StateDir, "attachments")
	paths := make(map[string]bool)
	for _, m := range attachmentRefRe.FindAllStringSubmatch(detail, -1) {
		path := filepath.Clean(m[1])
		if filepath.Dir(path) == dir {
			paths[path] = true
		}
	}
	return paths
}

// DeleteTask removes a task and best-effort unlinks any attachment files its
// detail text references. An attachment's only owner is that markdown line —
// once the task is gone the file would otherwise sit under
// <StateDir>/attachments forever, so failures to remove it are logged and
// swallowed rather than failing the delete.
func (a *App) DeleteTask(ctx context.Context, id string) error {
	t, err := a.Store.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if err := a.Store.DeleteTask(ctx, id); err != nil {
		return err
	}
	for path := range a.attachmentPaths(t.Detail) {
		_ = os.Remove(path)
	}
	return nil
}

// UpdateTask patches a task and best-effort unlinks any attachment files that
// were referenced by its old detail but are no longer referenced by the new
// one — the same leak DeleteTask guards against, but triggered by an edit
// (e.g. a dashboard PATCH) that drops or replaces an image reference instead
// of removing the whole task.
func (a *App) UpdateTask(ctx context.Context, id string, p store.TaskPatch) (*domain.Task, error) {
	var before map[string]bool
	if p.Detail != nil {
		if old, err := a.Store.GetTask(ctx, id); err == nil {
			before = a.attachmentPaths(old.Detail)
		}
	}
	t, err := a.Store.UpdateTask(ctx, id, p)
	if err != nil {
		return nil, err
	}
	if p.Detail != nil {
		after := a.attachmentPaths(*p.Detail)
		for path := range before {
			if !after[path] {
				_ = os.Remove(path)
			}
		}
	}
	return t, nil
}

// SetPipelineDesired starts/stops one pipeline's loop live via the halt
// machinery: an unbounded worker parks on halt and resumes when cleared.
func (a *App) SetPipelineDesired(ctx context.Context, name string, running bool) error {
	found := false
	for _, p := range a.Res().Config.ResolvedPipelines() {
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

// PauseAfter stops every enabled pipeline from claiming new work — the bulk
// form of SetPipelineDesired(name, false) — while letting whatever each
// pipeline is currently running finish untouched. It is the "pause-after"
// dashboard action's counterpart to Halt's immediate kill.
func (a *App) PauseAfter(ctx context.Context) error {
	for _, p := range a.EnabledPipelines() {
		if err := a.Store.SetHalted(ctx, p.Name, "operator"); err != nil {
			return err
		}
	}
	return nil
}

// SetPipelineBundle sets (or, for a zero bundle, clears) the live
// agent/model/effort override for a pipeline. Workers re-read it every pass,
// so it takes effect on the NEXT pass without a restart — the successor of
// the old tool's agent.json workflow.
func (a *App) SetPipelineBundle(ctx context.Context, name string, b domain.Bundle) error {
	found := false
	for _, p := range a.Res().Config.ResolvedPipelines() {
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

// SetPipelineMode sets (or, for "", clears) the live goal/discover/drain
// mode override for a pipeline. Workers re-read it every pass, so it takes
// effect on the NEXT pass without a restart or a config edit.
func (a *App) SetPipelineMode(ctx context.Context, name, mode string) error {
	found := false
	for _, p := range a.Res().Config.ResolvedPipelines() {
		if p.Name == name {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("no pipeline named %q", name)
	}
	if !domain.ValidMode(mode) {
		return fmt.Errorf("mode %q is not one of %v", mode, domain.Modes)
	}
	if err := a.Store.SetPipelineMode(ctx, name, mode); err != nil {
		return err
	}
	a.Bus.Publish(ctx, domain.Event{Type: "pipeline.mode", Pipeline: name, Payload: map[string]any{
		"mode": mode, "cleared": mode == "",
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
	cfg := a.Res().Config
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
		Effective:  a.Res().Config,
		Provenance: a.Res().Provenance,
		Warnings:   a.Res().Warnings,
		Worktrees:  a.Res().Config.WorktreesEnabled(),
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
