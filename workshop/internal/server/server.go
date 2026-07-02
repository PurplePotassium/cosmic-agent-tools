// Package server is the local HTTP surface: REST API + (later) the embedded
// SPA. It binds 127.0.0.1 ONLY — this process spawns permission-skipping
// agents; it must never be reachable from the network. Mutating routes
// require the session token minted at startup.
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gw1108/cosmic-agent-tools/workshop/internal/app"
	"github.com/gw1108/cosmic-agent-tools/workshop/internal/statedir"
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
	App      *app.App
	OnStop   func() // invoked by POST /api/v1/server/stop
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

// Start listens on 127.0.0.1:port (port 0 = ephemeral) and writes
// server.json. Returns the bound port.
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
	mux.HandleFunc("GET /api/v1/status", func(w http.ResponseWriter, r *http.Request) {
		snap, err := s.App.Snapshot(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, snap)
	})
	mux.HandleFunc("POST /api/v1/server/stop", func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			http.Error(w, "bad or missing token", http.StatusForbidden)
			return
		}
		writeJSON(w, map[string]bool{"stopping": true})
		if s.OnStop != nil {
			go s.OnStop()
		}
	})
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, placeholderPage)
	})
	return guardLoopback(mux)
}

func (s *Server) authorized(r *http.Request) bool {
	tok := r.Header.Get("X-Workshop-Token")
	if tok == "" {
		tok = r.URL.Query().Get("token")
	}
	return tok == s.token
}

// guardLoopback rejects anything that isn't loopback-addressed, plus obvious
// cross-origin browser requests (drive-by localhost CSRF insurance).
func guardLoopback(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil || !net.ParseIP(host).IsLoopback() {
			http.Error(w, "loopback only", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			if !strings.Contains(origin, "127.0.0.1") && !strings.Contains(origin, "localhost") {
				http.Error(w, "cross-origin refused", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

const placeholderPage = `<!doctype html>
<meta charset="utf-8">
<title>Workshop</title>
<style>body{font-family:system-ui;background:#111;color:#ddd;display:grid;place-items:center;height:100vh;margin:0}div{max-width:40rem;text-align:center}</style>
<div>
  <h1>Workshop is running</h1>
  <p>The dashboard UI ships in a later phase. Meanwhile:</p>
  <p><code>GET /api/v1/status</code> — live status JSON<br>
  <code>workshop status</code> / <code>workshop stop</code> from a terminal</p>
</div>`
