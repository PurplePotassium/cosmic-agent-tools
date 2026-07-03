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

	// replay streams every persisted event after `from`, in pages — a single
	// capped query would leave a permanent hole for clients more than one
	// page behind. Returns the last seq sent.
	replay := func(from int64) int64 {
		for {
			events, err := s.App.Store.EventsSince(r.Context(), from, 500)
			if err != nil {
				// Tell the client its history is incomplete instead of
				// silently serving live-only.
				fmt.Fprintf(w, ": replay-error %s\n\n", err)
				flusher.Flush()
				return from
			}
			for _, ev := range events {
				send(ev.Seq, ev.Type, ev)
				from = ev.Seq
			}
			if len(events) < 500 {
				return from
			}
		}
	}

	// Subscribe BEFORE replaying so nothing published in between is lost;
	// duplicates are filtered by seq.
	live, cancel := s.App.Bus.Subscribe()
	defer cancel()

	lastSent := replay(since)

	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.closing:
			// http.Server.Shutdown waits for handlers but never cancels
			// request contexts — without this, every shutdown stalls the
			// full timeout while a dashboard tab holds the stream open.
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
			if ev.Seq > lastSent+1 {
				// The bus drops events on a full subscriber buffer with the
				// promise that we re-sync from the store — keep it.
				lastSent = replay(lastSent)
				if ev.Seq <= lastSent {
					continue // the gap replay already delivered this one
				}
			}
			send(ev.Seq, ev.Type, ev)
			if ev.Seq > lastSent {
				lastSent = ev.Seq
			}
		}
	}
}
