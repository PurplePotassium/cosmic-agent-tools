package engine

import (
	"context"
	"errors"
	"testing"
)

// stubIntegrator drives the supervisor's drain seam: Loop idles until cancel,
// RunRound returns a scripted result.
type stubIntegrator struct {
	err    error
	rounds int
}

func (s *stubIntegrator) Loop(ctx context.Context) { <-ctx.Done() }

func (s *stubIntegrator) RunRound(context.Context) (int, error) {
	s.rounds++
	return 0, s.err
}

// A bounded run must surface an integrator drain failure out of Run (the W-6
// fix): committed lane work silently not landing while the CLI exits 0 is the
// worst outcome.
func TestBoundedRunSurfacesIntegratorDrainError(t *testing.T) {
	boom := errors.New("gate exploded")
	stub := &stubIntegrator{err: boom}

	err := NewSupervisor(nil, nil).Run(context.Background(), RunSpec{
		Iterations: 1,
		Integrator: stub,
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Run = %v, want the drain error surfaced", err)
	}
	if stub.rounds != 1 {
		t.Fatalf("RunRound ran %d times, want 1 (the first error stops the drain)", stub.rounds)
	}
}

// A clean drain (nothing lands) ends a bounded run with nil.
func TestBoundedRunCleanDrainReturnsNil(t *testing.T) {
	stub := &stubIntegrator{}
	err := NewSupervisor(nil, nil).Run(context.Background(), RunSpec{
		Iterations: 1,
		Integrator: stub,
	})
	if err != nil {
		t.Fatalf("Run = %v, want nil on a clean drain", err)
	}
	if stub.rounds != 1 {
		t.Fatalf("RunRound ran %d times, want 1 (0 landed ends the loop)", stub.rounds)
	}
}
