// Package bus fans out engine events to live subscribers (SSE, CLI watch)
// while persisting every event to the store for replay. The engine never
// blocks on a slow consumer: full subscriber buffers drop events, and the
// subscriber re-syncs from the store by sequence number.
package bus

import (
	"context"
	"sync"

	"github.com/gw1108/cosmic-agent-tools/workshop/internal/domain"
	"github.com/gw1108/cosmic-agent-tools/workshop/internal/store"
)

const subscriberBuffer = 128

// Bus is the process-wide event hub.
type Bus struct {
	st *store.Store

	mu     sync.Mutex
	nextID int
	subs   map[int]chan domain.Event
}

// New builds a bus that sinks events into st.
func New(st *store.Store) *Bus {
	return &Bus{st: st, subs: map[int]chan domain.Event{}}
}

// Publish persists the event (assigning its seq) and fans it out. Errors from
// the sink are swallowed after best effort — an event must never fail a pass.
func (b *Bus) Publish(ctx context.Context, ev domain.Event) domain.Event {
	if seq, err := b.st.AppendEvent(ctx, &ev); err == nil {
		ev.Seq = seq
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default: // full buffer: drop; subscriber replays from the store
		}
	}
	return ev
}

// PublishEphemeral fans an event out to live subscribers WITHOUT persisting
// it. High-volume streams (agent log lines) use this: the pass log file is
// their durable home, not the events table.
func (b *Bus) PublishEphemeral(ev domain.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// Subscribe returns a live event channel and a cancel func. Use
// store.EventsSince to backfill before (or after gaps in) the live stream.
func (b *Bus) Subscribe() (<-chan domain.Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.nextID
	b.nextID++
	ch := make(chan domain.Event, subscriberBuffer)
	b.subs[id] = ch
	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if c, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(c)
		}
	}
}
