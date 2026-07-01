package server

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/events"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/store"
)

// --- GOAL.md / PROMPT.md (user-edited files in the state dir) -------------------

func (s *Server) readStateFile(w http.ResponseWriter, r *http.Request, name, field string) {
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	b, err := os.ReadFile(filepath.Join(p.StateDir, name))
	if err != nil {
		b = nil
	}
	writeJSON(w, http.StatusOK, map[string]string{field: string(b)})
}

func (s *Server) writeStateFile(w http.ResponseWriter, r *http.Request, name, field string) {
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	var body map[string]string
	if err := readJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad body")
		return
	}
	val, has := body[field]
	if !has {
		writeErr(w, http.StatusBadRequest, field+" is required")
		return
	}
	if err := os.WriteFile(filepath.Join(p.StateDir, name), []byte(val), 0o644); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) getGoal(w http.ResponseWriter, r *http.Request) {
	s.readStateFile(w, r, "GOAL.md", "goal")
}
func (s *Server) postGoal(w http.ResponseWriter, r *http.Request) {
	s.writeStateFile(w, r, "GOAL.md", "goal")
}
func (s *Server) getPrompt(w http.ResponseWriter, r *http.Request) {
	s.readStateFile(w, r, "PROMPT.md", "prompt")
}
func (s *Server) postPrompt(w http.ResponseWriter, r *http.Request) {
	s.writeStateFile(w, r, "PROMPT.md", "prompt")
}

// --- backlog (DB-backed; the engine materializes/reconciles the JSON file) ------

func (s *Server) getBacklog(w http.ResponseWriter, r *http.Request) {
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	items, err := s.Store.ListBacklog(p.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) postBacklog(w http.ResponseWriter, r *http.Request) {
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	var req struct {
		Title  string `json:"title"`
		Detail string `json:"detail"`
		Top    bool   `json:"top"`
	}
	if err := readJSON(r, &req); err != nil || req.Title == "" {
		writeErr(w, http.StatusBadRequest, "title is required")
		return
	}
	item, err := s.Store.AddBacklog(p.ID, store.BacklogItem{Title: req.Title, Detail: req.Detail}, req.Top)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "item": item})
}

func (s *Server) deleteBacklog(w http.ResponseWriter, r *http.Request) {
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := readJSON(r, &req); err != nil || req.ID == "" {
		writeErr(w, http.StatusBadRequest, "id is required")
		return
	}
	if err := s.Store.DeleteBacklogItem(p.ID, req.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) reorderBacklog(w http.ResponseWriter, r *http.Request) {
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "ids is required")
		return
	}
	if err := s.Store.ReorderBacklog(p.ID, req.IDs); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- completions / progress / iterations (read-only views) ----------------------

func (s *Server) getCompletions(w http.ResponseWriter, r *http.Request) {
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	items, err := s.Store.ListCompletions(p.ID, 100)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) getProgress(w http.ResponseWriter, r *http.Request) {
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	prog, _ := s.Store.GetProgress(p.ID)
	writeJSON(w, http.StatusOK, prog)
}

func (s *Server) getIterations(w http.ResponseWriter, r *http.Request) {
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	its, err := s.Store.ListIterations(p.ID, 50)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, its)
}

// --- live agent/model selection -------------------------------------------------

func (s *Server) getAgent(w http.ResponseWriter, r *http.Request) {
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"agent":   p.Agent,
		"model":   p.Model,
		"id":      agentIDFor(p.Agent, p.Model),
		"options": AgentOptions,
	})
}

func (s *Server) postAgent(w http.ResponseWriter, r *http.Request) {
	p, ok := s.project(w, r)
	if !ok {
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad body")
		return
	}
	opt, valid := agentOptionByID(req.ID)
	if !valid {
		writeErr(w, http.StatusBadRequest, "unknown agent option: "+req.ID)
		return
	}
	if err := s.Store.SetProjectAgent(p.ID, opt.Agent, opt.Model); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Broker.Emit(events.TypeStatus, p.ID, map[string]any{"selAgent": opt.Agent, "selModel": opt.Model})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": opt.ID, "agent": opt.Agent, "model": opt.Model})
}
