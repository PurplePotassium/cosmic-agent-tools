package server

import (
	"net/http"
	"path/filepath"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/events"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/gitx"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/project"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/store"
)

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	ps, err := s.Store.ListProjects()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if ps == nil {
		ps = []*store.Project{}
	}
	// annotate running flag from the supervisor (authoritative live state)
	type projView struct {
		*store.Project
		Running bool `json:"running"`
	}
	out := make([]projView, 0, len(ps))
	for _, p := range ps {
		out = append(out, projView{Project: p, Running: s.Sup.IsRunning(p.ID)})
	}
	writeJSON(w, http.StatusOK, out)
}

type createReq struct {
	RepoPath string `json:"repoPath"`
	Branch   string `json:"branch"`
	AgentID  string `json:"agentId"`
	Preview  string `json:"preview"`
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var req createReq
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad body")
		return
	}
	if req.RepoPath == "" {
		writeErr(w, http.StatusBadRequest, "repoPath is required")
		return
	}
	abs, _ := filepath.Abs(req.RepoPath)
	if !gitx.IsRepo(abs) {
		writeErr(w, http.StatusBadRequest, "not a git repository: "+abs)
		return
	}
	ag, model := "claude", ""
	if opt, ok := agentOptionByID(req.AgentID); ok {
		ag, model = opt.Agent, opt.Model
	}
	p := project.New(s.BaseDir, abs, req.Branch, ag, model, req.Preview)

	// Reuse an existing project for the same repo rather than erroring on the unique path.
	if existing, err := s.Store.GetProject(p.ID); err == nil {
		writeJSON(w, http.StatusOK, existing)
		return
	}
	if err := project.Scaffold(p); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.Store.CreateProject(p); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Broker.Emit(events.TypeProject, p.ID, map[string]any{"status": "created"})
	writeJSON(w, http.StatusOK, p)
}

// detectProject reports whether the given repo path is already a registered project.
func (s *Server) detectProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepoPath string `json:"repoPath"`
	}
	if err := readJSON(r, &req); err != nil || req.RepoPath == "" {
		writeErr(w, http.StatusBadRequest, "repoPath is required")
		return
	}
	abs, _ := filepath.Abs(req.RepoPath)
	id := project.Slug(abs)
	p, err := s.Store.GetProject(id)
	writeJSON(w, http.StatusOK, map[string]any{
		"repoPath": abs,
		"isRepo":   gitx.IsRepo(abs),
		"exists":   err == nil,
		"project":  p,
	})
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) updateProject(w http.ResponseWriter, r *http.Request) {
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	var req struct {
		Name    *string `json:"name"`
		Branch  *string `json:"branch"`
		Preview *string `json:"preview"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad body")
		return
	}
	if req.Name != nil {
		p.Name = *req.Name
	}
	if req.Branch != nil {
		p.Branch = *req.Branch
	}
	if req.Preview != nil {
		p.Preview = *req.Preview
	}
	if err := s.Store.UpdateProject(p); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Broker.Emit(events.TypeProject, p.ID, map[string]any{"status": "updated"})
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	if s.Sup.IsRunning(p.ID) {
		writeErr(w, http.StatusConflict, "stop the loop before deleting")
		return
	}
	if err := s.Store.DeleteProject(p.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Broker.Emit(events.TypeProject, p.ID, map[string]any{"status": "deleted"})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) startProject(w http.ResponseWriter, r *http.Request) {
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	var req struct {
		Iterations int `json:"iterations"`
	}
	_ = readJSON(r, &req)
	if err := s.Sup.Start(p.ID, req.Iterations); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) stopProject(w http.ResponseWriter, r *http.Request) {
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	if err := s.Sup.Stop(p.ID); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// getStatus assembles the same shape the old workshop-status.ps1 emitted, computed
// on demand (liveness from the supervisor, timing from the running iteration, dirty
// tree + commit feed from git, task from the backlog, self-report from the DB).
func (s *Server) getStatus(w http.ResponseWriter, r *http.Request) {
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	alive := s.Sup.IsRunning(p.ID)

	var passSeconds *int
	var runningModel string
	if it, err := s.Store.RunningIteration(p.ID); err == nil && it != nil {
		sec := int(time.Since(it.Started).Seconds())
		passSeconds = &sec
		runningModel = it.Model
	}

	var lastIter *int
	var lastSha, lastWhen string
	if it, err := s.Store.LastPassIteration(p.ID); err == nil && it != nil {
		lastIter = &it.Num
		lastSha = it.CommitSHA
		if it.Ended != nil {
			lastWhen = it.Ended.Local().Format("15:04:05")
		}
	}

	backlog, _ := s.Store.ListBacklog(p.ID)
	currentTask := ""
	if len(backlog) > 0 {
		currentTask = backlog[0].Title
	}
	prog, _ := s.Store.GetProgress(p.ID)
	progAge := progressAgeSeconds(prog.Updated)

	writeJSON(w, http.StatusOK, map[string]any{
		"alive":          alive,
		"passSeconds":    passSeconds,
		"runningModel":   runningModel,
		"selAgent":       p.Agent,
		"selModel":       p.Model,
		"dirtyFiles":     gitx.DirtyFiles(p.RepoPath, 12),
		"currentTask":    currentTask,
		"backlogCount":   len(backlog),
		"lastIter":       lastIter,
		"lastSha":        lastSha,
		"lastWhen":       lastWhen,
		"commits":        gitx.Commits(p.RepoPath, 8),
		"progress":       prog,
		"progressAgeSec": progAge,
	})
}

// progressAgeSeconds parses an ISO-8601 updated time and returns seconds since, or nil.
func progressAgeSeconds(updated string) *int {
	if updated == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, updated)
	if err != nil {
		return nil
	}
	sec := int(time.Since(t).Seconds())
	if sec < 0 {
		sec = 0
	}
	return &sec
}
