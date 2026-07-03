// Package server is the local HTTP surface: REST API + SSE + the embedded
// dashboard. It binds 127.0.0.1 ONLY — this process spawns
// permission-skipping agents; it must never be reachable from the network.
// Mutating routes require the session token minted at startup (loopback CSRF
// insurance), and cross-origin browser requests are refused outright.
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/app"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/driver"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/statedir"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/store"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/web"
)

// ServerInfo is persisted to <stateDir>/server.json so CLI subcommands can
// find (and authenticate to) a running instance.
type ServerInfo struct {
	PID     int       `json:"pid"`
	Port    int       `json:"port"`
	Token   string    `json:"token"`
	Started time.Time `json:"started"`
}

// InfoPath returns the server.json path for a state dir.
func InfoPath(stateDir string) string { return filepath.Join(stateDir, "server.json") }

// ReadInfo loads server.json if present.
func ReadInfo(stateDir string) (*ServerInfo, error) {
	var si ServerInfo
	if err := statedir.ReadJSON(InfoPath(stateDir), &si); err != nil {
		return nil, err
	}
	return &si, nil
}

// Server hosts the API for one project.
type Server struct {
	App    *app.App
	OnStop func()

	token    string
	listener net.Listener
	http     *http.Server
}

// New builds the server (not yet listening).
func New(a *app.App, onStop func()) *Server {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return &Server{App: a, OnStop: onStop, token: hex.EncodeToString(buf)}
}

// Token returns the session token (embedded in the URL the CLI opens).
func (s *Server) Token() string { return s.token }

// Start listens on 127.0.0.1:port (0 = ephemeral) and writes server.json.
func (s *Server) Start(port int) (int, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return 0, err
	}
	s.listener = ln
	bound := ln.Addr().(*net.TCPAddr).Port
	s.http = &http.Server{Handler: s.handler()}
	go func() { _ = s.http.Serve(ln) }()

	info := ServerInfo{PID: os.Getpid(), Port: bound, Token: s.token, Started: time.Now().UTC()}
	if err := statedir.WriteJSON(InfoPath(s.App.StateDir), info); err != nil {
		_ = s.http.Close()
		return 0, err
	}
	return bound, nil
}

// Shutdown stops the HTTP server and removes server.json.
func (s *Server) Shutdown(ctx context.Context) {
	if s.http != nil {
		_ = s.http.Shutdown(ctx)
	}
	_ = os.Remove(InfoPath(s.App.StateDir))
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()

	// --- read routes ---
	mux.HandleFunc("GET /api/v1/status", s.getStatus)
	mux.HandleFunc("GET /api/v1/config", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, s.App.ConfigSnapshot())
	})
	mux.HandleFunc("GET /api/v1/goal", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"goal": s.App.Goal()})
	})
	mux.HandleFunc("GET /api/v1/prompts/{frag...}", func(w http.ResponseWriter, r *http.Request) {
		body, err := s.App.Fragment(r.PathValue("frag"))
		if err != nil {
			httpErr(w, err, http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]string{"content": body})
	})
	mux.HandleFunc("GET /api/v1/tasks", s.getTasks)
	mux.HandleFunc("GET /api/v1/queue", func(w http.ResponseWriter, r *http.Request) {
		lanes, err := s.App.QueueState(r.Context())
		if err != nil {
			httpErr(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, lanes)
	})
	mux.HandleFunc("GET /api/v1/runs", s.getRuns)
	mux.HandleFunc("GET /api/v1/runs/{id}/log", s.getRunLog)
	mux.HandleFunc("GET /api/v1/events", s.getEvents) // SSE

	// --- mutating routes (token-gated) ---
	guard := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !s.authorized(r) {
				httpErr(w, fmt.Errorf("bad or missing token"), http.StatusForbidden)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("PUT /api/v1/goal", guard(s.putGoal))
	mux.HandleFunc("PUT /api/v1/prompts/{frag...}", guard(s.putFragment))
	mux.HandleFunc("POST /api/v1/tasks", guard(s.postTask))
	mux.HandleFunc("PATCH /api/v1/tasks/{id}", guard(s.patchTask))
	mux.HandleFunc("DELETE /api/v1/tasks/{id}", guard(s.deleteTask))
	mux.HandleFunc("POST /api/v1/tasks/reorder", guard(s.reorderTasks))
	mux.HandleFunc("PATCH /api/v1/pipelines/{name}", guard(s.patchPipeline))
	mux.HandleFunc("POST /api/v1/server/stop", guard(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]bool{"stopping": true})
		if s.OnStop != nil {
			go s.OnStop()
		}
	}))

	// --- static SPA ---
	mux.HandleFunc("GET /", s.serveUI)

	return guardLoopback(mux)
}

func (s *Server) getStatus(w http.ResponseWriter, r *http.Request) {
	snap, err := s.App.Snapshot(r.Context())
	if err != nil {
		httpErr(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, snap)
}

func (s *Server) getTasks(w http.ResponseWriter, r *http.Request) {
	filter := store.TaskFilter{}
	if r.URL.Query().Get("all") == "" {
		filter.Statuses = []domain.TaskStatus{domain.TaskOpen, domain.TaskClaimed, domain.TaskStuck}
	}
	tasks, err := s.App.Store.ListTasks(r.Context(), filter)
	if err != nil {
		httpErr(w, err, http.StatusInternalServerError)
		return
	}
	if tasks == nil {
		tasks = []*domain.Task{}
	}
	writeJSON(w, tasks)
}

type taskBody struct {
	Title   *string        `json:"title"`
	Detail  *string        `json:"detail"`
	Type    *string        `json:"type"`
	Backlog *string        `json:"backlog"` // "shared" or pipeline name
	Pin     *domain.Bundle `json:"pin"`
	First   bool           `json:"first"`
}

// validatePin rejects pins to unknown agents or efforts at the API boundary —
// a task pinned to a typo'd agent would otherwise burn its retry attempts
// failing pass setup.
func validatePin(pin *domain.Bundle) error {
	if pin == nil {
		return nil
	}
	if pin.Agent != "" {
		if _, err := driver.New(pin.Agent); err != nil {
			return err
		}
	}
	if !domain.ValidEffort(pin.Effort) {
		return fmt.Errorf("effort %q is not one of %v", pin.Effort, domain.Efforts)
	}
	return nil
}

func (s *Server) postTask(w http.ResponseWriter, r *http.Request) {
	var body taskBody
	if err := readBody(r, &body); err != nil || body.Title == nil || strings.TrimSpace(*body.Title) == "" {
		httpErr(w, fmt.Errorf("a task needs a title"), http.StatusBadRequest)
		return
	}
	task := &domain.Task{Title: strings.TrimSpace(*body.Title)}
	if body.Detail != nil {
		task.Detail = *body.Detail
	}
	if body.Type != nil {
		task.Type = strings.ToLower(*body.Type)
	}
	if body.Pin != nil {
		if err := validatePin(body.Pin); err != nil {
			httpErr(w, err, http.StatusBadRequest)
			return
		}
		task.Pin = *body.Pin
	}
	if body.Backlog != nil {
		backlog, err := s.resolveBacklog(*body.Backlog)
		if err != nil {
			httpErr(w, err, http.StatusBadRequest)
			return
		}
		task.Backlog = backlog
	}
	added, err := s.App.Store.AddTask(r.Context(), task, body.First)
	if err != nil {
		httpErr(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, added)
}

func (s *Server) patchTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body taskBody
	if err := readBody(r, &body); err != nil {
		httpErr(w, err, http.StatusBadRequest)
		return
	}
	if err := validatePin(body.Pin); err != nil {
		httpErr(w, err, http.StatusBadRequest)
		return
	}
	if body.Backlog != nil {
		backlog, err := s.resolveBacklog(*body.Backlog)
		if err != nil {
			httpErr(w, err, http.StatusBadRequest)
			return
		}
		if _, err := s.App.Store.MoveTask(r.Context(), id, backlog); err != nil {
			httpErr(w, err, statusFor(err))
			return
		}
	}
	patch := store.TaskPatch{Title: body.Title, Detail: body.Detail, Pin: body.Pin}
	if body.Type != nil {
		lower := strings.ToLower(*body.Type)
		patch.Type = &lower
	}
	task, err := s.App.Store.UpdateTask(r.Context(), id, patch)
	if err != nil {
		httpErr(w, err, statusFor(err))
		return
	}
	writeJSON(w, task)
}

func (s *Server) deleteTask(w http.ResponseWriter, r *http.Request) {
	if err := s.App.Store.DeleteTask(r.Context(), r.PathValue("id")); err != nil {
		httpErr(w, err, statusFor(err))
		return
	}
	writeJSON(w, map[string]bool{"deleted": true})
}

func (s *Server) reorderTasks(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Backlog string   `json:"backlog"`
		IDs     []string `json:"ids"`
	}
	if err := readBody(r, &body); err != nil {
		httpErr(w, err, http.StatusBadRequest)
		return
	}
	backlog, err := s.resolveBacklog(body.Backlog)
	if err != nil {
		httpErr(w, err, http.StatusBadRequest)
		return
	}
	if err := s.App.Store.ReorderBacklog(r.Context(), backlog, body.IDs); err != nil {
		httpErr(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) patchPipeline(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Desired *string        `json:"desired"` // "running" | "stopped"
		Bundle  *domain.Bundle `json:"bundle"`  // live agent/model/effort override; {} clears
	}
	if err := readBody(r, &body); err != nil || (body.Desired == nil && body.Bundle == nil) {
		httpErr(w, fmt.Errorf(`body needs {"desired":"running"|"stopped"} and/or {"bundle":{agent,model,effort}}`), http.StatusBadRequest)
		return
	}
	name := r.PathValue("name")
	if body.Bundle != nil {
		if err := s.App.SetPipelineBundle(r.Context(), name, *body.Bundle); err != nil {
			httpErr(w, err, http.StatusBadRequest)
			return
		}
	}
	if body.Desired != nil {
		running := *body.Desired == "running"
		if err := s.App.SetPipelineDesired(r.Context(), name, running); err != nil {
			httpErr(w, err, http.StatusBadRequest)
			return
		}
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) putGoal(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Goal *string `json:"goal"`
	}
	if err := readBody(r, &body); err != nil || body.Goal == nil {
		httpErr(w, fmt.Errorf(`body needs {"goal":"..."}`), http.StatusBadRequest)
		return
	}
	if err := s.App.SetGoal(*body.Goal); err != nil {
		httpErr(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) putFragment(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Content *string `json:"content"`
	}
	if err := readBody(r, &body); err != nil || body.Content == nil {
		httpErr(w, fmt.Errorf(`body needs {"content":"..."}`), http.StatusBadRequest)
		return
	}
	if err := s.App.SetFragment(r.PathValue("frag"), *body.Content); err != nil {
		httpErr(w, err, http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) getRuns(w http.ResponseWriter, r *http.Request) {
	limit := 30
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	passes, err := s.App.Store.RecentPasses(r.Context(), r.URL.Query().Get("pipeline"), limit)
	if err != nil {
		httpErr(w, err, http.StatusInternalServerError)
		return
	}
	if passes == nil {
		passes = []*domain.Pass{}
	}
	writeJSON(w, passes)
}

func (s *Server) getRunLog(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		httpErr(w, err, http.StatusBadRequest)
		return
	}
	log, err := s.App.PassLog(r.Context(), id)
	if err != nil {
		httpErr(w, err, statusFor(err))
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, log)
}

func (s *Server) resolveBacklog(name string) (string, error) {
	switch strings.ToLower(name) {
	case "", statedir.SharedLabel:
		return domain.MainBacklog, nil
	}
	for _, p := range s.App.Res.Config.ResolvedPipelines() {
		if strings.EqualFold(p.Name, name) {
			return p.Name, nil
		}
	}
	return "", fmt.Errorf("no pipeline named %q", name)
}

// authorized accepts the session token from the header only. It is
// deliberately NOT read from a query parameter: URLs leak (history, logs,
// Referer), and the dashboard already carries the token via the URL fragment
// into sessionStorage, so a query channel buys nothing.
func (s *Server) authorized(r *http.Request) bool {
	return r.Header.Get("X-Workshop-Token") == s.token
}

// serveUI serves the embedded dashboard with history fallback.
func (s *Server) serveUI(w http.ResponseWriter, r *http.Request) {
	dist := web.Dist()
	if dist == nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, placeholderPage)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	data, err := dist.ReadFile(path)
	if err != nil {
		// SPA history fallback.
		data, err = dist.ReadFile("index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		path = "index.html"
	}
	w.Header().Set("Content-Type", contentType(path))
	_, _ = w.Write(data)
}

func contentType(path string) string {
	switch filepath.Ext(path) {
	case ".html":
		return "text/html; charset=utf-8"
	case ".js", ".mjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".json", ".map":
		return "application/json"
	default:
		return "application/octet-stream"
	}
}

// guardLoopback rejects anything that isn't loopback-addressed, anything
// whose Host header names a non-loopback host, and cross-origin browser
// requests. The Host check is the DNS-rebinding defense: a page at evil.com
// whose DNS answer is 127.0.0.1 reaches us with a loopback RemoteAddr and NO
// Origin header (the browser considers it same-origin) — the Host header is
// the only tell. Origin matching is by exact parsed hostname, never
// substring ("localhost.evil.com" must not pass).
func guardLoopback(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil || !net.ParseIP(host).IsLoopback() {
			http.Error(w, "loopback only", http.StatusForbidden)
			return
		}
		if !loopbackHostname(hostnameOf(r.Host)) {
			http.Error(w, "bad host", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || !loopbackHostname(u.Hostname()) {
				http.Error(w, "cross-origin refused", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// hostnameOf strips an optional port (and IPv6 brackets) from a host:port.
func hostnameOf(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return strings.Trim(hostport, "[]")
}

// loopbackHostname reports whether h names this machine's loopback:
// "localhost" or a literal loopback IP. Nothing else qualifies.
func loopbackHostname(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func readBody(r *http.Request, v any) error {
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return fmt.Errorf("empty body")
	}
	return json.Unmarshal(data, v)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func httpErr(w http.ResponseWriter, err error, code int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func statusFor(err error) int {
	if err == store.ErrNotFound {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}

const placeholderPage = `<!doctype html>
<meta charset="utf-8">
<title>Workshop</title>
<style>body{font-family:system-ui;background:#111;color:#ddd;display:grid;place-items:center;height:100vh;margin:0}div{max-width:40rem;text-align:center}</style>
<div>
  <h1>Workshop is running</h1>
  <p>No dashboard is embedded in this build. The API is live:</p>
  <p><code>GET /api/v1/status</code> · <code>workshop status</code> · <code>workshop stop</code></p>
</div>`
