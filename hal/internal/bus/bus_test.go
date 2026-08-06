package bus

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/domain"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/store"
)

func newTestBus(t *testing.T) (*Bus, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "hal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st), st
}

// The engine must never block on a slow consumer: a subscriber that never
// drains has its buffer dropped on overflow, but every event still lands in
// the store so it can re-sync by sequence number.
func TestPublishDropsOnFullBufferButPersistsAll(t *testing.T) {
	b, st := newTestBus(t)
	ctx := context.Background()
	_, cancel := b.Subscribe() // deliberately never drained
	defer cancel()

	const n = subscriberBuffer + 50
	for i := 0; i < n; i++ {
		b.Publish(ctx, domain.Event{Type: "flood"})
	}
	evs, err := st.EventsSince(ctx, 0, n+10)
	if err != nil || len(evs) != n {
		t.Fatalf("persisted %d events, want %d despite drops (err=%v)", len(evs), n, err)
	}
}

// Cancel closes the subscriber channel and unregisters it, so a later publish
// neither reaches it nor panics on the closed channel; cancel is idempotent.
func TestSubscribeCancelClosesChannel(t *testing.T) {
	b, _ := newTestBus(t)
	ctx := context.Background()
	ch, cancel := b.Subscribe()

	cancel()
	if _, ok := <-ch; ok {
		t.Fatal("channel should be closed after cancel")
	}
	// A publish after cancel must not panic (no send on the closed channel).
	b.Publish(ctx, domain.Event{Type: "after-cancel"})
	cancel() // double cancel is a no-op
}

// PublishEphemeral fans out to live subscribers without persisting — the
// durable home for high-volume log lines is the pass log file, not the store.
func TestPublishEphemeralDoesNotPersist(t *testing.T) {
	b, st := newTestBus(t)
	ch, cancel := b.Subscribe()
	defer cancel()

	b.PublishEphemeral(domain.Event{Type: "pass.log"})
	select {
	case got := <-ch:
		if got.Type != "pass.log" {
			t.Fatalf("got %q, want pass.log", got.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ephemeral event not fanned out")
	}
	evs, err := st.EventsSince(context.Background(), 0, 10)
	if err != nil || len(evs) != 0 {
		t.Fatalf("ephemeral event was persisted: %+v (err=%v)", evs, err)
	}
}
