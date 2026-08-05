package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/config"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/domain"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/driver"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/gitx"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/turns"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/workflow"
)

// WorkflowManager returns the lazily-built workflow Manager.
func (a *App) WorkflowManager() *workflow.Manager {
	a.wfMu.Lock()
	defer a.wfMu.Unlock()
	if a.wfManager == nil {
		a.wfManager = workflow.New(a.Store, a.Bus, workflowDriver(), turns.ProcRunner{}, a.workflowConfig())
	}
	return a.wfManager
}

// workflowDriver picks the interactive turn driver: claude in production.
// HAL_WORKFLOW_AGENT is a test seam — the e2e suite sets it to "fake"
// so workflow turns exec the scripted fake agent instead of the claude CLI.
func workflowDriver() driver.Driver {
	if name := os.Getenv("HAL_WORKFLOW_AGENT"); name != "" {
		d, err := driver.New(name)
		if err == nil {
			return d
		}
		fmt.Fprintf(os.Stderr, "hal: HAL_WORKFLOW_AGENT: %v — falling back to claude\n", err)
	}
	return driver.NewClaude()
}

// SetWorkflowManager injects a pre-built Manager — the seam server tests use
// to substitute a scripted turn runner for the real claude CLI.
func (a *App) SetWorkflowManager(m *workflow.Manager) {
	a.wfMu.Lock()
	defer a.wfMu.Unlock()
	a.wfManager = m
}

func (a *App) workflowConfig() workflow.Config {
	cfg := a.Res().Config
	// Per-stage model/effort overrides from [workflow.stages.<stage>]; the
	// agent is always claude (the interactive driver). Load validated the
	// stage names.
	stages := make(map[domain.WorkflowStage]domain.Bundle, len(cfg.Workflow.Stages))
	for name, b := range cfg.Workflow.Stages {
		stages[domain.WorkflowStage(name)] = domain.Bundle{Agent: "claude", Model: b.Model, Effort: b.Effort}
	}
	wcfg := workflow.Config{
		RepoDir:             a.RepoDir,
		Trunk:               cfg.Project.Trunk,
		StateDir:            a.StateDir,
		LogDir:              filepath.Join(a.StateDir, "logs"),
		ArtifactRoot:        cfg.Workflow.ArtifactDir,
		GoalPath:            config.GoalFile(a.RepoDir),
		PromptsDir:          filepath.Join(a.RepoDir, config.RepoConfigDir, "prompts"),
		Verify:              cfg.Project.Verify,
		VerifyDir:           a.verifyDirAbs(cfg.Project.VerifyDir),
		SkipPermissions:     cfg.Safety.SkipPermissions,
		TurnTimeout:         time.Duration(cfg.Workflow.TurnMinutes) * time.Minute,
		MaxConcurrent:       int64(cfg.Safety.MaxConcurrent),
		StageBundles:        stages,
		ExportDir:           cfg.Export.Dir,
		ExportHumanReadable: cfg.Export.HumanReadable,
	}
	// Test seam (e2e): validated artifact folders are hard-deleted instead
	// of landing in the developer's real recycle bin.
	if os.Getenv("HAL_WORKFLOW_ARCHIVE") == "delete" {
		wcfg.Archive = os.RemoveAll
	}
	return wcfg
}

func (a *App) verifyDirAbs(rel string) string {
	if rel == "" {
		return ""
	}
	if filepath.IsAbs(rel) {
		return rel
	}
	return filepath.Join(a.RepoDir, rel)
}

// RunWorkflows is the interactive engine body behind `hal up`: the
// engine singleton lock, crash cleanup, table pruning, then the workflow
// Manager until ctx cancels. The successor of RunHeadless's pass loop.
func (a *App) RunWorkflows(ctx context.Context) error {
	release, err := a.acquireEngineLock()
	if err != nil {
		return err
	}
	defer release()

	// Close rows a crashed engine left open — passes (art/inquiry) and
	// workflow turns both; liveness must derive from truth.
	_, _ = a.Store.CleanupOrphanPasses(ctx)
	_ = a.Store.Prune(ctx, 20000, 5000)
	_ = a.Store.PruneWorkflows(ctx, 200, 2000)

	// Verify agy's art models in the background: art jobs read the verified
	// label from the kv store per job, so the result applies the moment the
	// probe lands — no need to hold up engine start on it. The probe shares
	// the app-wide agy lock (it is an agy run like any other).
	a.VerifyArtModelsAsync(ctx, &a.agyMu)

	// The workflow engine shares one checkout; verify we're on the trunk the
	// operator configured (or record the current branch as trunk).
	mgr := a.WorkflowManager()
	if cfg := a.Res().Config; cfg.Project.Trunk != "" {
		branch, err := gitx.CurrentBranch(ctx, a.RepoDir)
		if err == nil && branch != cfg.Project.Trunk {
			return fmt.Errorf("repo is on branch %q but project.trunk is %q — check out the trunk (or update config) before running workflows", branch, cfg.Project.Trunk)
		}
	}
	return mgr.Run(ctx)
}

// WorkflowDetail is the dashboard's per-workflow view model.
type WorkflowDetail struct {
	Workflow domain.Workflow             `json:"workflow"`
	Stages   []WorkflowStageDetail       `json:"stages"`
	Turns    []domain.WorkflowTurn       `json:"turns,omitempty"`
	Messages []domain.WorkflowMessage    `json:"messages,omitempty"`
}

// WorkflowStageDetail decorates a stage row with artifact existence (the
// approve/skip button gates).
type WorkflowStageDetail struct {
	domain.WorkflowStageState
	ArtifactExists bool `json:"artifactExists"`
}

// WorkflowDetail assembles the detail view: stages with artifact existence,
// plus optionally turns and the paged message backlog.
func (a *App) WorkflowDetail(ctx context.Context, id string, withTurns bool, afterMsg int64, msgLimit int) (WorkflowDetail, error) {
	wf, err := a.Store.GetWorkflow(ctx, id)
	if err != nil {
		return WorkflowDetail{}, err
	}
	stages, err := a.Store.WorkflowStages(ctx, id)
	if err != nil {
		return WorkflowDetail{}, err
	}
	detail := WorkflowDetail{Workflow: wf}
	for _, st := range stages {
		exists := false
		if st.Artifact != "" {
			info, err := os.Stat(filepath.Join(a.RepoDir, filepath.FromSlash(st.Artifact)))
			exists = err == nil && info.Size() > 0
		} else if abs, _, aerr := a.WorkflowManager().ArtifactPath(id, st.Stage); aerr == nil {
			info, err := os.Stat(abs)
			exists = err == nil && info.Size() > 0
		}
		detail.Stages = append(detail.Stages, WorkflowStageDetail{WorkflowStageState: st, ArtifactExists: exists})
	}
	if withTurns {
		if detail.Turns, err = a.Store.ListTurns(ctx, id); err != nil {
			return WorkflowDetail{}, err
		}
	}
	if msgLimit != 0 {
		if detail.Messages, err = a.Store.ListMessages(ctx, id, afterMsg, msgLimit); err != nil {
			return WorkflowDetail{}, err
		}
	}
	return detail, nil
}

// WorkflowArtifact is one artifact read: content plus the hash concurrent
// editors compare-and-swap on.
type WorkflowArtifact struct {
	Path    string `json:"path"` // repo-relative
	Content string `json:"content"`
	Hash    string `json:"hash"`
	Mtime   int64  `json:"mtime"`
}

// ReadWorkflowArtifact reads a stage's artifact fresh from disk.
func (a *App) ReadWorkflowArtifact(id string, stage domain.WorkflowStage) (WorkflowArtifact, error) {
	abs, rel, err := a.WorkflowManager().ArtifactPath(id, stage)
	if err != nil {
		return WorkflowArtifact{}, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return WorkflowArtifact{}, err
	}
	info, _ := os.Stat(abs)
	mtime := int64(0)
	if info != nil {
		mtime = info.ModTime().UnixMilli()
	}
	return WorkflowArtifact{Path: rel, Content: string(data), Hash: hashContent(data), Mtime: mtime}, nil
}

// WriteWorkflowArtifact saves a dashboard edit. baseHash is the hash the
// editor loaded; a mismatch means someone (agent or IDE) changed the file
// since — the caller gets a conflict instead of a silent clobber. An empty
// baseHash writes unconditionally (the file may not exist yet).
func (a *App) WriteWorkflowArtifact(ctx context.Context, id string, stage domain.WorkflowStage, content, baseHash string) (WorkflowArtifact, error) {
	abs, rel, err := a.WorkflowManager().ArtifactPath(id, stage)
	if err != nil {
		return WorkflowArtifact{}, err
	}
	if baseHash != "" {
		if cur, err := os.ReadFile(abs); err == nil && hashContent(cur) != baseHash {
			return WorkflowArtifact{}, ErrArtifactConflict
		}
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return WorkflowArtifact{}, err
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return WorkflowArtifact{}, err
	}
	_ = a.Store.SetStageArtifact(ctx, id, stage, rel)
	return WorkflowArtifact{Path: rel, Content: content, Hash: hashContent([]byte(content)), Mtime: time.Now().UnixMilli()}, nil
}

// ErrArtifactConflict: the artifact changed under the editor.
var ErrArtifactConflict = fmt.Errorf("artifact changed on disk since it was loaded — reload and reapply your edit")

// ValidationStatus is the dashboard's validation card view model: the
// persisted auto-validate toggle, the implementations waiting for a run,
// and the live run if one exists.
type ValidationStatus struct {
	AutoValidate bool              `json:"autoValidate"`
	Pending      []domain.Workflow `json:"pending"`
	Active       *domain.Workflow  `json:"active,omitempty"`
}

// ValidationState assembles the validation card's view model.
func (a *App) ValidationState(ctx context.Context) (ValidationStatus, error) {
	st := ValidationStatus{AutoValidate: a.WorkflowManager().AutoValidate(ctx)}
	pending, err := a.Store.ListValidationPending(ctx)
	if err != nil {
		return st, err
	}
	st.Pending = pending
	if run, err := a.Store.ActiveValidationRun(ctx); err == nil {
		st.Active = &run
	}
	return st, nil
}

// WorkflowDiff returns the implement diffstat (base..HEAD).
func (a *App) WorkflowDiff(ctx context.Context, id string) (string, error) {
	wf, err := a.Store.GetWorkflow(ctx, id)
	if err != nil {
		return "", err
	}
	if wf.BaseSHA == "" {
		return "", fmt.Errorf("workflow has no implementation diff yet")
	}
	return gitx.DiffStat(ctx, a.RepoDir, wf.BaseSHA, "HEAD")
}

func hashContent(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}
