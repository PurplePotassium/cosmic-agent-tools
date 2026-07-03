package app

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestEngineControlHaltRelaunchesParked pins the risky concurrency contract:
// Halt must cancel the current run (killing whatever it's doing), relaunch
// it parked (startStopped=true) so the loop stays alive for the operator to
// resume from, and must NOT surface that cancellation on Done() — only a
// real (non-Halt) exit should do that.
func TestEngineControlHaltRelaunchesParked(t *testing.T) {
	calls := make(chan bool, 4) // records startStopped per invocation
	run := func(ctx context.Context, startStopped bool) error {
		calls <- startStopped
		<-ctx.Done()
		return ctx.Err()
	}

	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	ctl := NewEngineControl(run)
	ctl.Start(parent, false)

	if got := <-calls; got != false {
		t.Fatalf("first call startStopped = %v, want false", got)
	}

	ctl.Halt()

	select {
	case got := <-calls:
		if !got {
			t.Fatalf("relaunch startStopped = %v, want true", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Halt did not relaunch the run loop")
	}

	select {
	case err := <-ctl.Done():
		t.Fatalf("Done() fired after Halt (err=%v); the loop must stay alive", err)
	case <-time.After(200 * time.Millisecond):
	}

	cancelParent()
	select {
	case err := <-ctl.Done():
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Done() err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Done() did not fire after the parent context was canceled")
	}
}

// TestEngineControlForwardsNonHaltExit: a loop that ends on its own (no Halt
// involved — e.g. iteration bound reached) must be reported on Done().
func TestEngineControlForwardsNonHaltExit(t *testing.T) {
	ctl := NewEngineControl(func(ctx context.Context, startStopped bool) error {
		return nil
	})
	ctl.Start(context.Background(), false)

	select {
	case err := <-ctl.Done():
		if err != nil {
			t.Fatalf("Done() err = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Done() did not fire for a natural loop exit")
	}
}
