package engine

import (
	"context"
	"errors"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	"github.com/gw1108/cosmic-agent-tools/workshop/internal/bus"
	"github.com/gw1108/cosmic-agent-tools/workshop/internal/store"
)

// RunSpec is one supervised run: N workers plus (in worktree mode) the
// integrator.
type RunSpec struct {
	Workers      []*Worker
	Integrator   *Integrator // nil in simple mode
	Iterations   int         // per pipeline; 0 = until stopped
	UntilDrained bool
}

// Supervisor runs the workers concurrently. One pipeline halting (auth,
// breaker) never stops the others; a context cancel stops everything.
type Supervisor struct {
	st  *store.Store
	bus *bus.Bus
}

// NewSupervisor builds a supervisor.
func NewSupervisor(st *store.Store, b *bus.Bus) *Supervisor {
	return &Supervisor{st: st, bus: b}
}

// Run executes the spec. Bounded runs (Iterations > 0 or UntilDrained) end
// when every worker finishes, then drain the merge queue; ErrHalted is
// returned if any pipeline halted. Unbounded runs end on ctx cancel.
func (s *Supervisor) Run(ctx context.Context, spec RunSpec) error {
	bounded := spec.Iterations > 0 || spec.UntilDrained
	var halted atomic.Int32

	// Workers get their own group so a halt (returned as nil) never
	// cancels siblings; only real errors do.
	g, gctx := errgroup.WithContext(ctx)
	for _, w := range spec.Workers {
		g.Go(func() error {
			err := w.Loop(gctx, spec.Iterations, spec.UntilDrained)
			switch {
			case errors.Is(err, ErrHalted):
				halted.Add(1)
				return nil
			case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
				return nil
			default:
				return err
			}
		})
	}

	// The integrator ticks alongside unbounded runs; bounded runs drain it
	// after the workers finish so committed lane work actually lands.
	integDone := make(chan struct{})
	var integCancel context.CancelFunc = func() {}
	if spec.Integrator != nil && !bounded {
		var integCtx context.Context
		integCtx, integCancel = context.WithCancel(ctx)
		go func() {
			defer close(integDone)
			spec.Integrator.Loop(integCtx)
		}()
	} else {
		close(integDone)
	}

	err := g.Wait()
	integCancel()
	<-integDone
	if err != nil {
		return err
	}

	if spec.Integrator != nil && bounded && ctx.Err() == nil {
		// Rounds until nothing more lands. Conflict tasks enqueued here
		// stay queued for the next run (a bounded run has no workers left
		// to resolve them).
		for i := 0; i < 10; i++ {
			landed, err := spec.Integrator.RunRound(ctx)
			if err != nil || landed == 0 {
				break
			}
		}
	}

	if halted.Load() > 0 {
		return ErrHalted
	}
	return ctx.Err()
}
