package engine

import (
	"context"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// An empty (or whitespace-only) verify command is vacuously green with no
// output — a project without a gate must never be reported red.
func TestRunGateEmptyCommandIsGreen(t *testing.T) {
	for _, cmd := range []string{"", "   ", "\t\n"} {
		green, out := runGate(context.Background(), t.TempDir(), cmd, 0)
		if !green || out != "" {
			t.Fatalf("runGate(%q) = green=%v out=%q, want green with no output", cmd, green, out)
		}
	}
}

// exitCmd exits with the given code in either shell runGate spawns.
func exitCmd(code int) string {
	if runtime.GOOS == "windows" {
		return "exit /b " + strconv.Itoa(code)
	}
	return "exit " + strconv.Itoa(code)
}

// A command that exits 0 is green; a non-zero exit is red. This is the
// green/red decision every integration and resolution gate rides on.
func TestRunGateExitCodeDecidesGreen(t *testing.T) {
	if green, out := runGate(context.Background(), t.TempDir(), exitCmd(0), time.Minute); !green {
		t.Fatalf("exit 0: green=%v out=%q, want green", green, out)
	}
	if green, _ := runGate(context.Background(), t.TempDir(), exitCmd(1), time.Minute); green {
		t.Fatal("exit 1: want red")
	}
}

// A gate that outruns its timeout is killed and reported red with a marker —
// a hung build must never wedge the integrator or a resolution pass.
func TestRunGateTimeoutKillsAndReportsRed(t *testing.T) {
	sleep := "sleep 10"
	if runtime.GOOS == "windows" {
		sleep = "ping -n 11 127.0.0.1 >nul"
	}
	start := time.Now()
	green, out := runGate(context.Background(), t.TempDir(), sleep, 150*time.Millisecond)
	if green {
		t.Fatal("timed-out gate reported green")
	}
	if !strings.Contains(out, "gate timed out") {
		t.Fatalf("output missing timeout marker: %q", out)
	}
	// Must return promptly (WaitDelay is 10s); a leaked wait would hang here.
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Fatalf("runGate took %v after a 150ms timeout — process not killed", elapsed)
	}
}

// Long gate output is truncated to a bounded tail with a marker, so a noisy
// build can't flood the event that carries the failure snippet.
func TestRunGateTruncatesLongOutput(t *testing.T) {
	line := strings.Repeat("a", 4500)
	green, out := runGate(context.Background(), t.TempDir(), "echo "+line, time.Minute)
	if !green {
		t.Fatalf("echo gate reported red: %q", out)
	}
	if !strings.HasPrefix(out, "...\n") {
		t.Fatalf("truncated output missing marker prefix: %.20q", out)
	}
	if len(out) != len("...\n")+4000 {
		t.Fatalf("truncated output len=%d, want %d", len(out), len("...\n")+4000)
	}
}
