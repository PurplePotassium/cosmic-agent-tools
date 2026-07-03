package app

import (
	"context"
	"errors"
	"sync"
)

// EngineControl supervises a restartable engine run loop (RunHeadless) so
// the HTTP layer can hard-stop every in-flight pass right now and bring the
// loop back up parked, without tearing down the process that hosts the
// dashboard's HTTP server. Before this existed, the only way to stop running
// models was to cancel the process-level context, which also killed the
// server (see cmd/workshop's "stop server" flow).
type EngineControl struct {
	run func(ctx context.Context, startStopped bool) error

	mu       sync.Mutex
	cancel   context.CancelFunc
	live     bool // a run loop is currently executing
	stopping bool // true while a Halt()-triggered relaunch is in flight
	done     chan error
}

// NewEngineControl wraps a run function shaped like App.RunHeadless
// (iterations/untilDrained already bound by the caller) so this package
// stays testable without spinning up real git repos or agents.
func NewEngineControl(run func(ctx context.Context, startStopped bool) error) *EngineControl {
	return &EngineControl{run: run, done: make(chan error, 1)}
}

// Start launches the engine loop as a child of parent. Call once; Halt owns
// relaunching after that.
func (e *EngineControl) Start(parent context.Context, startStopped bool) {
	e.launch(parent, startStopped)
}

func (e *EngineControl) launch(parent context.Context, startStopped bool) {
	runCtx, cancel := context.WithCancel(parent)
	e.mu.Lock()
	e.cancel = cancel
	e.live = true
	e.mu.Unlock()
	go func() {
		err := e.run(runCtx, startStopped)
		e.mu.Lock()
		swallow := e.stopping && errors.Is(err, context.Canceled)
		e.stopping = false
		e.live = false
		e.mu.Unlock()
		if swallow {
			// Come back up parked: no pipeline claims work until the
			// operator resumes it (dashboard's per-pipeline "resume").
			e.launch(parent, true)
			return
		}
		e.done <- err
	}()
}

// Halt kills every in-flight pass right now (the run loop's context
// cancellation propagates into each spawned agent's cmd.Cancel, which
// KillTrees the process) and relaunches the loop parked. The engine — and
// whatever HTTP server sits in front of it — stays alive throughout.
func (e *EngineControl) Halt() {
	e.mu.Lock()
	if !e.live {
		// The loop already exited (iteration bound reached): cancelling a
		// dead context does nothing, and latching stopping=true here would
		// stick forever — the dashboard's halt/resume would silently no-op.
		e.mu.Unlock()
		return
	}
	e.stopping = true
	cancel := e.cancel
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Done reports the engine loop's terminal (non-Halt) exit: iteration bound
// reached, a pipeline halted itself in bounded mode, a real error, or the
// parent context (real process shutdown) was canceled.
func (e *EngineControl) Done() <-chan error { return e.done }
