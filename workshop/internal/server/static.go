package server

import (
	"io"
	"net/http"
	"strings"
)

// spa serves the embedded React build. Dist is rooted at the build output (index.html
// at top). Unknown paths fall back to index.html so client-side routes work on reload.
// If no build is embedded (Dist nil / empty), it serves a minimal placeholder so the
// binary still runs and the API is usable.
func (s *Server) spa() http.Handler {
	if s.Dist == nil {
		return http.HandlerFunc(placeholder)
	}
	fileServer := http.FileServer(http.FS(s.Dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if f, err := s.Dist.Open(p); err == nil {
			f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}
		// SPA history fallback → index.html
		idx, err := s.Dist.Open("index.html")
		if err != nil {
			placeholder(w, r)
			return
		}
		defer idx.Close()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.Copy(w, idx)
	})
}

func placeholder(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, `<!doctype html><html><head><meta charset="utf-8">
<title>Workshop</title></head><body style="font:14px system-ui;background:#0b1120;color:#e2e8f0;padding:2rem">
<h1>🔨 Workshop</h1>
<p>The API is running, but no web UI is embedded in this build.</p>
<p>Build the frontend with <code>npm --prefix web install &amp;&amp; npm --prefix web run build</code>, then rebuild the binary.</p>
</body></html>`)
}
