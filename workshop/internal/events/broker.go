// Package events is the SSE fan-out broker: the engine PUBLISHES status/log/commit/
// progress events (tagged by projectId) and the server's /events handler SUBSCRIBES
// one channel per connected dashboard. This replaces the old 2s status poll — the
// browser is pushed to, and one dashboard can watch several projects at once.
package events

import (
	"sync"
	"time"
)

// Event types pushed over SSE.
const (
	TypeStatus    = "status"    // loop/project status changed
	TypeLog       = "log"       // a live agent log line (claude passes)
	TypeCommit    = "commit"    // a pass committed
	TypeProgress  = "progress"  // the agent's self-report changed
	TypeIteration = "iteration" // a pass started/ended
	TypeProject   = "project"   // project list changed (created/updated/deleted)
)

// Event is one server→client message. ProjectID scopes it so a dashboard can filter.
type Event struct {
	Type      string    `json:"type"`
	ProjectID string    `json:"projectId,omitempty"`
	Data      any       `json:"data,omitempty"`
	Time      time.Time `json:"time"`
}

// Broker fans events out to every subscribed channel. Slow subscribers drop events
// rather than block the publisher (a dashboard that can't keep up just misses a tick;
// the next full status refreshes it).
type Broker struct {
	mu   sync.RWMutex
	subs map[chan Event]struct{}
}

// New creates an empty broker.
func New() *Broker { return &Broker{subs: map[chan Event]struct{}{}} }

// Subscribe returns a buffered channel of events plus an unsubscribe func the caller
// must invoke (e.g. on client disconnect) to free the subscription.
func (b *Broker) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 128)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
		b.mu.Unlock()
	}
}

// Publish sends an event to all subscribers, non-blocking (drops on a full channel).
func (b *Broker) Publish(ev Event) {
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs {
		select {
		case ch <- ev:
		default: // subscriber is behind — drop this event for it
		}
	}
}

// Emit is a convenience wrapper for Publish.
func (b *Broker) Emit(typ, projectID string, data any) {
	b.Publish(Event{Type: typ, ProjectID: projectID, Data: data})
}
