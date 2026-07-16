// Package fakeagent is the scripted stand-in for a real coding agent. The
// engine's fake driver execs the workshop binary (or a test binary) back
// into this entry point, letting every path of the pass state machine be
// driven deterministically: happy passes, blocked reports, crashes, auth
// failures, and wedges.
//
// Configuration comes from environment variables the engine sets on every
// agent child:
//
//	WORKSHOP_PASS_STATE_DIR  — the pipeline's agent-facing state dir
//	WORKSHOP_PASS_REPO_DIR   — the working directory (repo/worktree)
//	WORKSHOP_FAKE_SCENARIO   — path to a scenario JSON file (optional;
//	                           missing = the default happy behavior)
package fakeagent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/domain"
	"github.com/PurplePotassium/cosmic-agent-tools/workshop/internal/statedir"
)

// Scenario scripts one fake pass.
type Scenario struct {
	// Behavior: "happy" (default), "blocked", "reverted", "silent" (no
	// progress writes), "crash" (nonzero exit), "auth" (auth-looking
	// failure), "sleep" (wedge bait).
	Behavior string `json:"behavior"`

	SleepMs   int               `json:"sleepMs"`   // pre-exit sleep (sleep behavior: main wait)
	ExitCode  int               `json:"exitCode"`  // crash/auth exit code (default 1)
	Print     []string          `json:"print"`     // lines to emit on stdout
	Proposals []domain.Proposal `json:"proposals"` // written to proposals.json on happy passes
	WriteFile string            `json:"writeFile"` // repo-relative file to touch (default fake-work.txt)
	NoEdit    bool              `json:"noEdit"`    // happy pass without any repo edit
	// RawProposals, when set, is written to proposals.json VERBATIM instead
	// of Proposals — for tests that need a syntactically corrupt file.
	RawProposals string `json:"rawProposals"`
	// ArtEncode is the actual byte format the art scenario writes at the
	// asset paths ("" = png): "jpeg" simulates the image model mislabeling
	// its output (JPEG bytes under a .png name).
	ArtEncode string `json:"artEncode"`
}

// Prompt markers the art scenario keys on — they mirror the exact one-line
// path statements the engine's art prompts contain.
const (
	artTargetMarker = "TARGET PATH for the asset (relative to the repository root): "
	artScreenMarker = "SCREEN PATH — save the result as a PNG at EXACTLY this path (relative to the repository root): "
)

// artStage performs the stage the prompt asks for: write the asset (stage 1,
// neutral background) or the flat-green rescreen (stage 2), then record a
// fresh conversation id the way agy does.
func artStage(promptText, repoDir, encode string) error {
	if p := promptLineAfter(promptText, artScreenMarker); p != "" {
		if err := writeTestImage(filepath.Join(repoDir, p), color.NRGBA{G: 255, A: 255}, encode); err != nil {
			return err
		}
		return recordConversation(repoDir)
	}
	p := promptLineAfter(promptText, artTargetMarker)
	if p == "" {
		return fmt.Errorf("art scenario: prompt names no target path")
	}
	if err := writeTestImage(filepath.Join(repoDir, p), color.NRGBA{R: 90, G: 90, B: 90, A: 255}, encode); err != nil {
		return err
	}
	return recordConversation(repoDir)
}

// promptLineAfter returns the rest of the line following marker ("" if absent).
func promptLineAfter(text, marker string) string {
	i := strings.Index(text, marker)
	if i < 0 {
		return ""
	}
	rest := text[i+len(marker):]
	if j := strings.IndexByte(rest, '\n'); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(strings.TrimRight(rest, "\r"))
}

// writeTestImage writes a 64x64 image: bg fill with a centered red square
// (the "subject" the chroma keyer must preserve). encode picks the actual
// byte format ("" = png) regardless of the path's extension — how tests
// simulate a mislabeled asset.
func writeTestImage(path string, bg color.NRGBA, encode string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			if x >= 20 && x < 44 && y >= 20 && y < 44 {
				img.SetNRGBA(x, y, color.NRGBA{R: 200, A: 255})
			} else {
				img.SetNRGBA(x, y, bg)
			}
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	switch encode {
	case "", "png":
		return png.Encode(f, img)
	case "jpeg":
		return jpeg.Encode(f, img, nil)
	default:
		return fmt.Errorf("art scenario: unknown artEncode %q", encode)
	}
}

// recordConversation mimics agy's cache/last_conversations.json bookkeeping:
// the workdir's entry is replaced with a fresh id. Skipped unless the test
// harness points WORKSHOP_AGY_STATE_DIR somewhere.
func recordConversation(repoDir string) error {
	stateDir := os.Getenv("WORKSHOP_AGY_STATE_DIR")
	if stateDir == "" {
		return nil
	}
	path := filepath.Join(stateDir, "cache", "last_conversations.json")
	m := map[string]string{}
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &m)
	}
	m[repoDir] = fmt.Sprintf("conv-%d", time.Now().UnixNano())
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

// resolveConflicts rewrites files containing conflict markers with both
// sides' lines (markers stripped) and stages everything.
func resolveConflicts(repoDir string) error {
	err := filepath.WalkDir(repoDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if d != nil && d.IsDir() && d.Name() == ".git" {
				return filepath.SkipDir
			}
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Contains(data, []byte("<<<<<<< ")) {
			return nil //nolint:nilerr // unreadable or marker-free file: skip it
		}
		var out []string
		for _, line := range strings.Split(string(data), "\n") {
			// The middle marker is EXACTLY seven '='s alone on its line —
			// a prefix match would also eat setext underlines and similar
			// legitimate content, corrupting what the tests then verify.
			trimmed := strings.TrimRight(line, "\r")
			if strings.HasPrefix(trimmed, "<<<<<<< ") || trimmed == "=======" || strings.HasPrefix(trimmed, ">>>>>>> ") {
				continue
			}
			out = append(out, line)
		}
		return os.WriteFile(path, []byte(strings.Join(out, "\n")), 0o644)
	})
	if err != nil {
		return err
	}
	cmd := exec.Command("git", "add", "-A")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git add: %v: %s", err, out)
	}
	return nil
}

// Main runs one fake pass; returns the process exit code.
func Main() int {
	// A real agent consumes the prompt; drain stdin so the parent's write
	// never blocks — but only when stdin is actually a pipe. In blind
	// (own-console) mode stdin is the console and reading it would hang.
	// The art scenario parses the drained prompt for its target paths.
	var promptText string
	if info, err := os.Stdin.Stat(); err == nil && info.Mode()&os.ModeCharDevice == 0 {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, os.Stdin)
		promptText = buf.String()
	}

	stateDir := os.Getenv("WORKSHOP_PASS_STATE_DIR")
	repoDir := os.Getenv("WORKSHOP_PASS_REPO_DIR")
	if stateDir == "" || repoDir == "" {
		fmt.Fprintln(os.Stderr, "fakeagent: missing WORKSHOP_PASS_STATE_DIR / WORKSHOP_PASS_REPO_DIR")
		return 2
	}

	sc := Scenario{Behavior: "happy"}
	if p := os.Getenv("WORKSHOP_FAKE_SCENARIO"); p != "" {
		if err := statedir.ReadJSON(p, &sc); err != nil {
			fmt.Fprintf(os.Stderr, "fakeagent: scenario %s: %v\n", p, err)
			return 2
		}
		if sc.Behavior == "" {
			sc.Behavior = "happy"
		}
	}

	for _, line := range sc.Print {
		fmt.Println(line)
	}

	// What am I working on?
	var task domain.Task
	title := "(invented task)"
	if err := statedir.ReadJSON(filepath.Join(stateDir, statedir.TaskFile), &task); err == nil && task.Title != "" {
		title = task.Title
	}

	writeProgress := func(p domain.Progress) {
		p.Updated = time.Now().UTC().Format(time.RFC3339)
		_ = statedir.WriteJSON(filepath.Join(stateDir, statedir.ProgressFile), p)
	}

	switch sc.Behavior {
	case "silent":
		return 0
	case "crash":
		fmt.Println("something went wrong, giving up")
		if sc.ExitCode == 0 {
			sc.ExitCode = 1
		}
		return sc.ExitCode
	case "auth":
		fmt.Println("Error: unauthorized - credential expired, please sign-in again")
		if sc.ExitCode == 0 {
			sc.ExitCode = 1
		}
		return sc.ExitCode
	case "sleep":
		writeProgress(domain.Progress{Phase: "working", Task: title, Plan: "sleeping forever"})
		time.Sleep(time.Duration(sc.SleepMs) * time.Millisecond)
		return 0
	case "blocked", "reverted":
		writeProgress(domain.Progress{Phase: "working", Task: title, Plan: "attempting"})
		writeProgress(domain.Progress{Phase: sc.Behavior, Task: title, Note: "scripted " + sc.Behavior})
		return 0
	case "resolve":
		// Merge-conflict resolution: rewrite every conflicted file with a
		// merged body, stage it, report done. The engine verifies for real.
		writeProgress(domain.Progress{Phase: "working", Task: title, Plan: "resolving conflicts"})
		if err := resolveConflicts(repoDir); err != nil {
			fmt.Fprintln(os.Stderr, "fakeagent:", err)
			return 1
		}
		writeProgress(domain.Progress{Phase: "done", Task: title, Result: "combined both sides mechanically"})
		return 0
	case "art":
		// Art pass stand-in: honor whichever art-flow stage the prompt is —
		// stage 1 writes the generated asset at the TARGET PATH, stage 2 the
		// flat-green rescreen at the SCREEN PATH. Both record a fresh agy-style
		// conversation id (under WORKSHOP_AGY_STATE_DIR) like the real agy.
		writeProgress(domain.Progress{Phase: "working", Task: title, Plan: "generating art"})
		if err := artStage(promptText, repoDir, sc.ArtEncode); err != nil {
			fmt.Fprintln(os.Stderr, "fakeagent:", err)
			return 1
		}
		writeProgress(domain.Progress{Phase: "done", Task: title, Result: "art step complete"})
		return 0
	}

	// happy path
	writeProgress(domain.Progress{Phase: "working", Task: title, Plan: "one scripted increment"})
	if !sc.NoEdit {
		name := sc.WriteFile
		if name == "" {
			// Per-workdir default so parallel lanes don't artificially
			// collide on one filename.
			name = "fake-" + filepath.Base(repoDir) + ".txt"
		}
		path := filepath.Join(repoDir, name)
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Fprintln(os.Stderr, "fakeagent:", err)
			return 1
		}
		fmt.Fprintf(f, "%s @ %s\n", title, time.Now().UTC().Format(time.RFC3339Nano))
		f.Close()
	}
	if sc.RawProposals != "" {
		_ = os.WriteFile(filepath.Join(stateDir, statedir.ProposalsFile), []byte(sc.RawProposals), 0o644)
	} else if len(sc.Proposals) > 0 {
		_ = statedir.WriteJSON(filepath.Join(stateDir, statedir.ProposalsFile), sc.Proposals)
	}
	if sc.SleepMs > 0 {
		time.Sleep(time.Duration(sc.SleepMs) * time.Millisecond)
	}
	writeProgress(domain.Progress{Phase: "done", Task: title, Result: "completed: " + title})
	fmt.Println("fake pass complete:", title)
	return 0
}
