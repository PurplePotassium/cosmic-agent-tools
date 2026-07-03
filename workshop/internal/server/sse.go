package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// getEvents is the SSE stream: replay from Last-Event-ID (the store is the
// durable log), then live from the bus. Log lines arrive as ephemeral
// events (seq 0) — they replay from pass log files, not from here.
func (s *Server) getEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpErr(w, fmt.Errorf("streaming unsupported"), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, "retry: 3000\n\n")
	flusher.Flush()

	since := int64(0)
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		since, _ = strconv.ParseInt(v, 10, 64)
	} else if v := r.URL.Query().Get("since"); v != "" {
		since, _ = strconv.ParseInt(v, 10, 64)
	}

	send := func(seq int64, typ string, payload any) bool {
		data, err := json.Marshal(payload)
		if err != nil {
			return true
		}
		if seq > 0 {
			fmt.Fprintf(w, "id: %d\n", seq)
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ, data)
		flusher.Flush()
		return true
	}

	// Subscribe BEFORE replaying so nothing published in between is lost;
	// duplicates are filtered by seq.
	live, cancel := s.App.Bus.Subscribe()
	defer cancel()

	lastSent := since
	if events, err := s.App.Store.EventsSince(r.Context(), since, 500); err == nil {
		for _, ev := range events {
			send(ev.Seq, ev.Type, ev)
			lastSent = ev.Seq
		}
	}

	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case ev, ok := <-live:
			if !ok {
				return
			}
			if ev.Seq != 0 && ev.Seq <= lastSent {
				continue // already replayed
			}
			send(ev.Seq, ev.Type, ev)
			if ev.Seq > lastSent {
				lastSent = ev.Seq
			}
		}
	}
}
