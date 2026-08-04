package server

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/domain"
)

// collectSSE reads the SSE stream, returning the `id:` sequence numbers until
// it has count of them or `within` elapses (closing the body to unblock).
func collectSSE(t *testing.T, resp *http.Response, count int, within time.Duration) []int64 {
	t.Helper()
	out := make(chan []int64, 1)
	go func() {
		var seqs []int64
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 64*1024), 1<<20)
		for sc.Scan() {
			if line := sc.Text(); strings.HasPrefix(line, "id: ") {
				if v, err := strconv.ParseInt(strings.TrimPrefix(line, "id: "), 10, 64); err == nil {
					seqs = append(seqs, v)
					if len(seqs) >= count {
						break
					}
				}
			}
		}
		out <- seqs
	}()
	select {
	case seqs := <-out:
		return seqs
	case <-time.After(within):
		resp.Body.Close() // interrupt the blocked scan
		return <-out
	}
}

// openSSE opens an authenticated SSE stream against a running Server.
func openSSE(t *testing.T, base, token, query string) *http.Response {
	t.Helper()
	req, err := http.NewRequest("GET", base+"/api/v1/events"+query, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Hal-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("SSE status = %d, want 200", resp.StatusCode)
	}
	return resp
}

// startServer binds the Server on an ephemeral port over a real listener (not
// httptest) so s.Shutdown's SSE drain is exercised, and returns its base URL.
func startServer(t *testing.T, s *Server) string {
	t.Helper()
	port, err := s.Start(0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		s.Shutdown(ctx)
	})
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

// Replay must page past the 500-event query cap: publish more than a page,
// connect with since=0, and assert every event arrives once, in order. This
// pins the W-10/W-11 paged-replay behavior against regression over a real
// listener (a ResponseRecorder can't stream).
func TestSSEPagedReplayDeliversEveryEvent(t *testing.T) {
	s, a := newTestServer(t)
	ctx := context.Background()

	const n = 550 // > the 500-per-page replay cap
	for i := 0; i < n; i++ {
		a.Bus.Publish(ctx, domain.Event{Type: "test.tick", Pipeline: "main"})
	}

	base := startServer(t, s)
	resp := openSSE(t, base, s.token, "?since=0")
	defer resp.Body.Close()

	seqs := collectSSE(t, resp, n, 15*time.Second)
	if len(seqs) != n {
		t.Fatalf("replayed %d events, want %d", len(seqs), n)
	}
	for i, seq := range seqs {
		if seq != int64(i+1) {
			t.Fatalf("event %d has seq %d, want %d (out of order or gap)", i, seq, i+1)
		}
	}
}

// A client resuming from a sequence number (Last-Event-ID / ?since=) must get
// only the events after it, never a re-send of what it already has.
func TestSSEResumesFromSince(t *testing.T) {
	s, a := newTestServer(t)
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		a.Bus.Publish(ctx, domain.Event{Type: "test.tick"})
	}

	base := startServer(t, s)
	resp := openSSE(t, base, s.token, "?since=5")
	defer resp.Body.Close()

	seqs := collectSSE(t, resp, 5, 10*time.Second)
	if len(seqs) != 5 || seqs[0] != 6 || seqs[4] != 10 {
		t.Fatalf("resume from since=5 delivered %v, want 6..10", seqs)
	}
}

// A FRESH connection (no Last-Event-ID, no ?since=) must not replay the whole
// persisted log — only the most recent initialReplayEvents tail. Without this,
// every dashboard load machine-guns weeks of history (chimes, stale alert
// banners) into the UI.
func TestSSEFreshConnectionReplaysOnlyRecentTail(t *testing.T) {
	s, a := newTestServer(t)
	ctx := context.Background()

	const n = initialReplayEvents + 50
	for i := 0; i < n; i++ {
		a.Bus.Publish(ctx, domain.Event{Type: "test.tick", Pipeline: "main"})
	}

	base := startServer(t, s)
	resp := openSSE(t, base, s.token, "")
	defer resp.Body.Close()

	seqs := collectSSE(t, resp, initialReplayEvents, 10*time.Second)
	if len(seqs) != initialReplayEvents {
		t.Fatalf("fresh connection replayed %d events, want %d", len(seqs), initialReplayEvents)
	}
	if seqs[0] != n-initialReplayEvents+1 {
		t.Fatalf("fresh replay starts at seq %d, want %d (the recent tail, not the whole log)", seqs[0], n-initialReplayEvents+1)
	}
	if last := seqs[len(seqs)-1]; last != n {
		t.Fatalf("fresh replay ends at seq %d, want %d", last, n)
	}
}

// A RECONNECTING client (Last-Event-ID set) keeps the exact gap replay — the
// fresh-connection tail cap must not open holes in a resumed stream.
func TestSSELastEventIDStillReplaysFullGap(t *testing.T) {
	s, a := newTestServer(t)
	ctx := context.Background()

	const n = initialReplayEvents + 50 // gap wider than the fresh-connection tail
	for i := 0; i < n; i++ {
		a.Bus.Publish(ctx, domain.Event{Type: "test.tick", Pipeline: "main"})
	}

	base := startServer(t, s)
	req, err := http.NewRequest("GET", base+"/api/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Hal-Token", s.token)
	req.Header.Set("Last-Event-ID", "5")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("SSE status = %d, want 200", resp.StatusCode)
	}

	seqs := collectSSE(t, resp, n-5, 10*time.Second)
	if len(seqs) != n-5 || seqs[0] != 6 || seqs[len(seqs)-1] != int64(n) {
		t.Fatalf("resume from Last-Event-ID=5 delivered %d events (first=%v), want the full 6..%d gap",
			len(seqs), seqs, n)
	}
}

// Shutdown must not stall on an open SSE stream: the closing channel unblocks
// the handler, so Shutdown returns well under its deadline (pins W-11).
func TestSSEShutdownReturnsQuickly(t *testing.T) {
	s, _ := newTestServer(t)
	port, err := s.Start(0)
	if err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	resp := openSSE(t, base, s.token, "")
	defer resp.Body.Close()
	// Consume the initial retry hint so the handler is fully in its select loop.
	buf := make([]byte, 32)
	if _, err := resp.Body.Read(buf); err != nil {
		t.Fatalf("reading SSE preamble: %v", err)
	}

	done := make(chan struct{})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		s.Shutdown(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("Shutdown stalled on an open SSE stream — closing drain regressed")
	}
}
