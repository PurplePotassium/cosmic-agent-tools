package engine

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/bus"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/store"
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

// drainStub reports work landing for the first landsFor rounds (landsFor < 0
// means it never drains), then reports an empty round.
type drainStub struct {
	landsFor int
	rounds   int
}

func (d *drainStub) Loop(ctx context.Context) { <-ctx.Done() }

func (d *drainStub) RunRound(context.Context) (int, error) {
	d.rounds++
	if d.landsFor < 0 || d.rounds <= d.landsFor {
		return 1, nil
	}
	return 0, nil
}

// A drain that lands work across several rounds before settling still ends the
// bounded run with nil, exactly at the round it first reports empty — the
// multi-round path the single-round tests never exercise.
func TestBoundedRunDrainsAcrossRounds(t *testing.T) {
	stub := &drainStub{landsFor: 3}
	err := NewSupervisor(nil, nil).Run(context.Background(), RunSpec{
		Iterations: 1,
		Integrator: stub,
	})
	if err != nil {
		t.Fatalf("Run = %v, want nil once the queue settles", err)
	}
	if stub.rounds != 4 {
		t.Fatalf("RunRound ran %d times, want 4 (3 landing + 1 empty stops it)", stub.rounds)
	}
}

// A queue that keeps landing past the round cap must NOT exit 0 silently: the
// supervisor caps the drain and publishes integration.drain_incomplete so the
// operator knows lane work is still queued and a re-run is needed.
func TestBoundedRunCapPublishesDrainIncomplete(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "workshop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	b := bus.New(st)

	stub := &drainStub{landsFor: -1} // never drains
	if err := NewSupervisor(st, b).Run(context.Background(), RunSpec{
		Iterations: 1,
		Integrator: stub,
	}); err != nil {
		t.Fatalf("Run = %v, want nil (the cap is surfaced via event, not error)", err)
	}
	if stub.rounds != maxDrainRounds {
		t.Fatalf("RunRound ran %d times, want the %d-round cap", stub.rounds, maxDrainRounds)
	}

	evs, err := st.EventsSince(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, ev := range evs {
		if ev.Type == "integration.drain_incomplete" {
			found = true
			if got := ev.Payload["rounds"]; got != int64(maxDrainRounds) && got != float64(maxDrainRounds) {
				t.Fatalf("drain_incomplete rounds = %v, want %d", got, maxDrainRounds)
			}
		}
	}
	if !found {
		t.Fatalf("no integration.drain_incomplete event published; got %d events", len(evs))
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
