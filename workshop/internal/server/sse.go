package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// sse streams broker events to one dashboard over Server-Sent Events. An optional
// ?projectId= narrows the stream to one project (events with no ProjectID — e.g.
// global notices — always pass through). A heartbeat comment keeps the connection
// alive through proxies/idle timeouts.
func (s *Server) sse(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	filter := r.URL.Query().Get("projectId")

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")

	ch, unsub := s.Broker.Subscribe()
	defer unsub()

	fmt.Fprint(w, "retry: 3000\n\n")
	flusher.Flush()

	ctx := r.Context()
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case ev, open := <-ch:
			if !open {
				return
			}
			if filter != "" && ev.ProjectID != "" && ev.ProjectID != filter {
				continue
			}
			data, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data)
			flusher.Flush()
		}
	}
}
