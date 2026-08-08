// Package modelcheck verifies that the claude model ids a project's config
// names can actually be run by the installed Claude Code, and caches each
// verdict so the probe is paid for once per id per CLI version.
//
// Why verify at all: hal passes `--model <id>` through verbatim
// (driver.(*Claude).Plan) and config.Validate only prefix-matches the family
// (domain.ClaudeModels), warn-not-block. So "claude-sonnnet-5" and a real id
// the account cannot reach both sail through config load and fail later, mid
// turn, as a wasted workflow stage. Asking the CLI up front turns that into a
// startup error naming the config key to edit.
//
// Why cache: driver.(*Claude).VerifyModel is quota-free for a BAD id (claude
// rejects it before the prompt reaches the API) but costs one minimal turn for
// a GOOD one. Caching keeps steady-state startup free — a hit needs no
// subprocess at all.
package modelcheck

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/config"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/statedir"
)

// FileName is the verdict cache inside a project's state dir. Human-readable
// on purpose: an operator debugging a blocked startup can read it, and
// deleting it is the documented way to force a re-probe.
const FileName = "claude-models.json"

// Verdict is one model id's cached answer.
type Verdict struct {
	OK bool      `json:"ok"`
	At time.Time `json:"at"`
}

// Cache is the on-disk file. CLIVersion scopes every verdict in it: which
// models exist, and which an account may reach, both move with Claude Code
// releases, so verdicts recorded under another version are discarded wholesale
// rather than aged out.
type Cache struct {
	CLIVersion string             `json:"cliVersion"`
	Models     map[string]Verdict `json:"models"`
}

// Path returns the cache file for a project state dir.
func Path(stateDir string) string { return filepath.Join(stateDir, FileName) }

// Load reads the cache, returning an empty one for any problem. A cache miss
// only costs a re-probe, so a missing/corrupt/partial file must never be an
// error the caller has to handle.
func Load(stateDir string) Cache {
	var c Cache
	if err := statedir.ReadJSON(Path(stateDir), &c); err != nil {
		return Cache{Models: map[string]Verdict{}}
	}
	if c.Models == nil {
		c.Models = map[string]Verdict{}
	}
	return c
}

// Save atomically replaces the cache file.
func Save(stateDir string, c Cache) error { return statedir.WriteJSON(Path(stateDir), c) }

// Verifier is the slice of driver.(*Claude) this package needs, so tests can
// substitute a fake without spawning a real CLI.
type Verifier interface {
	VerifyModel(ctx context.Context, model string) (bool, error)
	Version(ctx context.Context) (string, error)
}

// Result is one model id's outcome.
type Result struct {
	Ref config.ModelRef
	// OK is meaningful only when Unknown is false.
	OK bool
	// Unknown means the probe could not be carried out (claude missing, dead
	// auth, timeout). It is NOT evidence against the model: callers warn, and
	// must never block on it, or an offline laptop cannot start hal.
	Unknown bool
	// Cached is true when the verdict came from disk — no subprocess ran.
	Cached bool
	Detail string
}

// InvalidModelError reports config-named model ids the installed Claude Code
// refuses to run. app.Open returns it so `hal doctor` can render it as its own
// check (errors.As) instead of a misleading "repository" failure.
type InvalidModelError struct{ Results []Result }

func (e *InvalidModelError) Error() string {
	var b strings.Builder
	b.WriteString("configured model id(s) the installed Claude Code will not run:")
	for _, r := range e.Results {
		fmt.Fprintf(&b, "\n  %s = %q — it may not exist, or this account has no access to it", r.Ref.Where, r.Ref.Model)
	}
	b.WriteString("\nfix the id, or set HAL_SKIP_MODEL_VERIFY=1 to start without verifying")
	return b.String()
}

// Skip reports whether model verification is disabled: HAL_SKIP_MODEL_VERIFY
// truthy, or a fake-agent harness (HAL_FAKE_BIN), whose stub binary has no
// model list to verify against. Mirrors HAL_SKIP_AGY_VERIFY's semantics —
// notably that "0" must NOT skip.
func Skip(getenv func(string) string) bool {
	if getenv("HAL_FAKE_BIN") != "" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(getenv("HAL_SKIP_MODEL_VERIFY"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// Verify resolves every ref against the cache, probing only on a miss, and
// persists any newly learned verdicts. Results are returned in refs order.
//
// Unknown verdicts are deliberately not cached: a probe that failed because
// the laptop was offline must not persist as a verdict about the model.
func Verify(ctx context.Context, v Verifier, stateDir string, refs []config.ModelRef) []Result {
	if len(refs) == 0 {
		return nil
	}
	cache := Load(stateDir)
	// A version we cannot read leaves the cache scoped to whatever it already
	// claims — better a stale hit than re-probing (and re-billing) every start.
	if version, err := v.Version(ctx); err == nil && version != cache.CLIVersion {
		cache = Cache{CLIVersion: version, Models: map[string]Verdict{}}
	}

	dirty := false
	out := make([]Result, 0, len(refs))
	for _, ref := range refs {
		if vd, hit := cache.Models[ref.Model]; hit {
			out = append(out, Result{Ref: ref, OK: vd.OK, Cached: true})
			continue
		}
		ok, err := v.VerifyModel(ctx, ref.Model)
		if err != nil {
			out = append(out, Result{Ref: ref, Unknown: true, Detail: err.Error()})
			continue
		}
		cache.Models[ref.Model] = Verdict{OK: ok, At: time.Now().UTC()}
		dirty = true
		out = append(out, Result{Ref: ref, OK: ok})
	}
	if dirty {
		// A cache that cannot be written costs re-probes, not correctness.
		_ = Save(stateDir, cache)
	}
	return out
}

// Invalid filters results down to definitive negatives — the ones worth
// blocking on. Unknowns are excluded by construction.
func Invalid(results []Result) []Result {
	var bad []Result
	for _, r := range results {
		if !r.Unknown && !r.OK {
			bad = append(bad, r)
		}
	}
	return bad
}
