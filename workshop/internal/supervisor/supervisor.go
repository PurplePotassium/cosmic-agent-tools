// Package supervisor owns the set of running project loops. Single-project today is
// just the N=1 path: each ACTIVE project gets its own goroutine + context.Context, and
// stopping it cancels that context, which kills the in-flight agent process subtree.
// A configurable cap bounds concurrent projects. There are NO worktrees — each loop
// runs in its project's own working directory, so concurrent agents never contend on
// the same files. That is the whole multi-agent/multi-goal model.
package supervisor

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/assets"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/engine"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/events"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/store"
)

// Supervisor multiplexes engine loops across projects.
type Supervisor struct {
	store  *store.Store
	broker *events.Broker
	log    *slog.Logger
	cfg    engine.Config
	max    int

	mu    sync.Mutex
	loops map[string]*handle
}

type handle struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// New creates a supervisor. maxConcurrent caps how many project loops may run at once.
func New(st *store.Store, br *events.Broker, log *slog.Logger, cfg engine.Config, maxConcurrent int) *Supervisor {
	if maxConcurrent <= 0 {
		maxConcurrent = 4
	}
	return &Supervisor{
		store:  st,
		broker: br,
		log:    log,
		cfg:    cfg,
		max:    maxConcurrent,
		loops:  map[string]*handle{},
	}
}

// IsRunning reports whether a project's loop is active.
func (s *Supervisor) IsRunning(projectID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.loops[projectID]
	return ok
}

// Running returns the ids of all active loops.
func (s *Supervisor) Running() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.loops))
	for id := range s.loops {
		out = append(out, id)
	}
	return out
}

// Start launches a project's loop. iterations overrides the default (0 = infinite).
// Returns an error if already running or the concurrency cap is reached.
func (s *Supervisor) Start(projectID string, iterations int) error {
	s.mu.Lock()
	if _, ok := s.loops[projectID]; ok {
		s.mu.Unlock()
		return fmt.Errorf("project %s already running", projectID)
	}
	if len(s.loops) >= s.max {
		s.mu.Unlock()
		return fmt.Errorf("concurrency cap reached (%d projects running)", s.max)
	}

	proj, err := s.store.GetProject(projectID)
	if err != nil {
		s.mu.Unlock()
		return err
	}

	cfg := s.cfg
	cfg.Iterations = iterations
	personas := assets.Personas(cfg.PersonaFlavor)
	nouns := assets.Nouns(cfg.PersonaFlavor)
	eng := engine.New(s.store, s.broker, s.log, proj, cfg, personas, nouns)

	ctx, cancel := context.WithCancel(context.Background())
	h := &handle{cancel: cancel, done: make(chan struct{})}
	s.loops[projectID] = h
	s.mu.Unlock()

	_ = s.store.SetProjectStatus(projectID, store.StatusRunning)
	s.broker.Emit(events.TypeStatus, projectID, map[string]any{"alive": true})
	s.broker.Emit(events.TypeProject, projectID, map[string]any{"status": store.StatusRunning})
	s.log.Info("project loop started", "project", proj.Name, "iterations", iterations)

	go func() {
		defer close(h.done)
		runErr := eng.Run(ctx)

		s.mu.Lock()
		delete(s.loops, projectID)
		s.mu.Unlock()

		status := store.StatusStopped
		if runErr != nil {
			status = store.StatusError
			s.log.Error("project loop stopped with error", "project", proj.Name, "err", runErr)
		} else {
			s.log.Info("project loop stopped", "project", proj.Name)
		}
		_ = s.store.SetProjectStatus(projectID, status)
		s.broker.Emit(events.TypeStatus, projectID, map[string]any{"alive": false})
		s.broker.Emit(events.TypeProject, projectID, map[string]any{"status": status})
	}()
	return nil
}

// Stop cancels a project's loop (killing the in-flight agent) and waits for it to end.
func (s *Supervisor) Stop(projectID string) error {
	s.mu.Lock()
	h, ok := s.loops[projectID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("project %s is not running", projectID)
	}
	h.cancel()
	<-h.done
	return nil
}

// Wait blocks until a project's loop ends on its own (used by bounded --iterations
// runs). Returns immediately if the loop already finished / never started.
func (s *Supervisor) Wait(projectID string) {
	s.mu.Lock()
	h, ok := s.loops[projectID]
	s.mu.Unlock()
	if ok {
		<-h.done
	}
}

// StopAll cancels every loop and waits (graceful shutdown on SIGINT).
func (s *Supervisor) StopAll() {
	s.mu.Lock()
	handles := make([]*handle, 0, len(s.loops))
	for _, h := range s.loops {
		h.cancel()
		handles = append(handles, h)
	}
	s.mu.Unlock()
	for _, h := range handles {
		<-h.done
	}
}
