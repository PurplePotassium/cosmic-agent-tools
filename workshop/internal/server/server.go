// Package server exposes the Workshop over HTTP: a chi REST API (project CRUD,
// start/stop, goal/prompt/backlog/completions/agent edits, history) and an SSE /events
// broker that PUSHES status/log/commit/progress (replacing the old 2s poll). It also
// serves the embedded React SPA. Bind to 127.0.0.1 only — the server spawns agent
// commands and must never be exposed without auth.
package server

import (
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/events"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/store"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/supervisor"
)

// Deps are the collaborators the server needs.
type Deps struct {
	Store   *store.Store
	Sup     *supervisor.Supervisor
	Broker  *events.Broker
	Log     *slog.Logger
	BaseDir string // OS state base dir (for new-project state dirs)
	UIPort  int    // for the /api/config echo
	Dist    fs.FS  // embedded SPA (web/dist)
}

// Server is the HTTP layer.
type Server struct {
	Deps
}

// New builds a Server.
func New(d Deps) *Server { return &Server{Deps: d} }

// Handler builds the chi router.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)

	r.Route("/api", func(r chi.Router) {
		r.Get("/config", s.getConfig)
		r.Get("/projects", s.listProjects)
		r.Post("/projects", s.createProject)
		r.Post("/detect", s.detectProject)

		r.Route("/projects/{id}", func(r chi.Router) {
			r.Get("/", s.getProject)
			r.Patch("/", s.updateProject)
			r.Delete("/", s.deleteProject)
			r.Post("/start", s.startProject)
			r.Post("/stop", s.stopProject)
			r.Get("/status", s.getStatus)
			r.Get("/goal", s.getGoal)
			r.Post("/goal", s.postGoal)
			r.Get("/prompt", s.getPrompt)
			r.Post("/prompt", s.postPrompt)
			r.Get("/backlog", s.getBacklog)
			r.Post("/backlog", s.postBacklog)
			r.Post("/backlog/delete", s.deleteBacklog)
			r.Post("/backlog/reorder", s.reorderBacklog)
			r.Get("/completions", s.getCompletions)
			r.Get("/agent", s.getAgent)
			r.Post("/agent", s.postAgent)
			r.Get("/progress", s.getProgress)
			r.Get("/iterations", s.getIterations)
		})
	})

	// SSE stream (query ?projectId= optionally filters).
	r.Get("/events", s.sse)

	// Embedded SPA with history-fallback to index.html.
	r.Handle("/*", s.spa())
	return r
}

// getConfig echoes server-level info + the agent menu the UI renders.
func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"uiPort":  s.UIPort,
		"options": AgentOptions,
	})
}

// --- small helpers -------------------------------------------------------------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(v)
}

// project loads the {id} path param or writes 404 and returns (nil,false).
func (s *Server) project(w http.ResponseWriter, r *http.Request) (*store.Project, bool) {
	id := chi.URLParam(r, "id")
	p, err := s.Store.GetProject(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "project not found")
		return nil, false
	}
	return p, true
}
