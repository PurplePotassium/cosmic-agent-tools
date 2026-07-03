package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
)

// The auth scan gates a PERMANENT halt, so its false-positive cost is a dead
// pipeline: output that merely mentions auth-flavored code must never match,
// while real agent-CLI failure phrases always must.
func TestIsAuthFailurePhrases(t *testing.T) {
	real := []string{
		"Error: unauthorized - credential expired, please sign-in again", // fakeagent
		"Invalid API key · Please run /login",                            // claude console
		`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`,
		"OAuth token has expired. Please run `claude login`.",
		"You are not logged in. Run claude login to authenticate.",
		"HTTP 401 Unauthorized",
		"API key is invalid — regenerate it in the console",
	}
	for _, line := range real {
		if !isAuthFailure([]string{line}) {
			t.Errorf("real auth failure not detected: %q", line)
		}
	}

	chatter := []string{
		"--- FAIL: TestCredentialStore (0.03s)",
		"internal/auth/handler_test.go:42: want 401, got 200",
		"FAIL github.com/x/y/internal/auth 0.5s",
		"updating credentials_test.go with new fixtures",
		"expected the 401 handler to redirect to /login-page", // mentions, not a failure
		"Author identity unknown",
		"git: 'sign-off' is not a git command",
		"+ func TestUnauthorizedAccess(t *testing.T) {",
		"reworked the keyring integration tests",
		"docs: document the token refresh flow",
	}
	for _, line := range chatter {
		if isAuthFailure([]string{line}) {
			t.Errorf("chatter tripped the auth halt: %q", line)
		}
	}
}

// An expired login during a conflict-resolution pass must halt the pipeline
// on the FIRST pass — not burn ConflictRetryBudget × MaxTaskAttempts blind
// retries (the resolution path used to discard the output tail entirely).
func TestConflictPassAuthFailureHalts(t *testing.T) {
	r := newQueueRig(t, []string{"solo"}, "", true)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(r.repo, "README.md"), []byte("trunk side\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.git(r.repo, "commit", "-q", "-am", "trunk edit")
	r.laneCommit(0, "README.md", "lane side\n", "lane edit")
	if _, err := r.ig.RunRound(ctx); err != nil {
		t.Fatal(err)
	}
	li, _ := r.st.GetIntegration(ctx, "solo")
	if li.ConflictTaskID == "" {
		t.Fatal("no conflict task")
	}

	worker := r.conflictWorker(t, "auth")
	res, err := worker.RunPass(ctx)
	if err != nil || res != PassHalted {
		t.Fatalf("auth failure in conflict pass: res=%v err=%v, want PassHalted", res, err)
	}
	if reason, _ := r.st.HaltedReason(ctx, "fixer"); reason != HaltAuth {
		t.Fatalf("halt reason %q, want %q", reason, HaltAuth)
	}
	// The task is released (auth is not the task's fault), not failed.
	task, _ := r.st.GetTask(ctx, li.ConflictTaskID)
	if task.Status != domain.TaskOpen || task.Attempts != 0 {
		t.Fatalf("task after auth halt: %+v, want released/open", task)
	}
}
