package modelcheck

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/config"
)

func writeRaw(path, body string) error { return os.WriteFile(path, []byte(body), 0o600) }

// fakeVerifier records every probe so tests can assert what was NOT spawned —
// the cache's whole purpose is that a hit costs no subprocess (and no turn).
type fakeVerifier struct {
	version    string
	versionErr error
	ok         map[string]bool
	err        map[string]error
	probed     []string
}

func (f *fakeVerifier) Version(context.Context) (string, error) {
	return f.version, f.versionErr
}

func (f *fakeVerifier) VerifyModel(_ context.Context, model string) (bool, error) {
	f.probed = append(f.probed, model)
	if err, bad := f.err[model]; bad {
		return false, err
	}
	return f.ok[model], nil
}

func refs(models ...string) []config.ModelRef {
	out := make([]config.ModelRef, 0, len(models))
	for _, m := range models {
		out = append(out, config.ModelRef{Where: "workflow.stages.design", Agent: "claude", Model: m})
	}
	return out
}

func TestVerifyCachesAcrossCalls(t *testing.T) {
	dir := t.TempDir()
	f := &fakeVerifier{version: "2.1.225", ok: map[string]bool{"claude-opus-5": true}}

	first := Verify(t.Context(), f, dir, refs("claude-opus-5"))
	if len(first) != 1 || !first[0].OK || first[0].Cached {
		t.Fatalf("first pass: %+v, want a fresh OK verdict", first)
	}
	if len(f.probed) != 1 {
		t.Fatalf("first pass probed %v, want exactly one", f.probed)
	}

	second := Verify(t.Context(), f, dir, refs("claude-opus-5"))
	if len(second) != 1 || !second[0].OK || !second[0].Cached {
		t.Fatalf("second pass: %+v, want a cached OK verdict", second)
	}
	if len(f.probed) != 1 {
		t.Fatalf("second pass re-probed (%v) — a cache hit must spawn nothing", f.probed)
	}
}

// A negative verdict is cached too: re-probing a known-bad id every startup
// would be pure latency, and the answer cannot change without a new CLI.
func TestVerifyCachesNegativeVerdict(t *testing.T) {
	dir := t.TempDir()
	f := &fakeVerifier{version: "2.1.225", ok: map[string]bool{"claude-nope-5": false}}

	Verify(t.Context(), f, dir, refs("claude-nope-5"))
	got := Verify(t.Context(), f, dir, refs("claude-nope-5"))
	if len(got) != 1 || got[0].OK || !got[0].Cached {
		t.Fatalf("second pass: %+v, want a cached negative verdict", got)
	}
	if len(f.probed) != 1 {
		t.Fatalf("re-probed a cached negative: %v", f.probed)
	}
	if bad := Invalid(got); len(bad) != 1 {
		t.Fatalf("Invalid() = %+v, want the one rejected id", bad)
	}
}

// A CLI upgrade can add models and change what an account may reach, so every
// verdict recorded under the old version is dropped rather than aged out.
func TestVerifyInvalidatesCacheOnCLIUpgrade(t *testing.T) {
	dir := t.TempDir()
	f := &fakeVerifier{version: "2.1.225", ok: map[string]bool{"claude-opus-5": false}}
	Verify(t.Context(), f, dir, refs("claude-opus-5"))

	// Same id, newer CLI, now serving it.
	f.version = "2.2.0"
	f.ok["claude-opus-5"] = true
	got := Verify(t.Context(), f, dir, refs("claude-opus-5"))
	if len(got) != 1 || !got[0].OK || got[0].Cached {
		t.Fatalf("after upgrade: %+v, want a fresh OK verdict", got)
	}
	if len(f.probed) != 2 {
		t.Fatalf("probed %v, want a re-probe after the version change", f.probed)
	}
}

// A probe that could not run (offline, dead auth) is not evidence about the
// model. It must never be cached, or one flaky startup poisons every later
// one, and it must never be reported as a rejection.
func TestVerifyDoesNotCacheUnknown(t *testing.T) {
	dir := t.TempDir()
	f := &fakeVerifier{
		version: "2.1.225",
		err:     map[string]error{"claude-opus-5": errors.New("claude not found on PATH")},
	}

	got := Verify(t.Context(), f, dir, refs("claude-opus-5"))
	if len(got) != 1 || !got[0].Unknown || got[0].OK {
		t.Fatalf("probe failure: %+v, want Unknown", got)
	}
	if bad := Invalid(got); len(bad) != 0 {
		t.Fatalf("Invalid() = %+v — an unverifiable model must never block startup", bad)
	}

	// It recovers on the next run rather than being stuck at Unknown.
	f.err = nil
	f.ok = map[string]bool{"claude-opus-5": true}
	if got := Verify(t.Context(), f, dir, refs("claude-opus-5")); !got[0].OK || got[0].Cached {
		t.Fatalf("after recovery: %+v, want a fresh OK verdict", got)
	}
}

// An unreadable version keeps the existing cache usable — re-probing (and
// re-billing) every id because `claude --version` hiccuped is the wrong trade.
func TestVerifyKeepsCacheWhenVersionUnreadable(t *testing.T) {
	dir := t.TempDir()
	f := &fakeVerifier{version: "2.1.225", ok: map[string]bool{"claude-opus-5": true}}
	Verify(t.Context(), f, dir, refs("claude-opus-5"))

	f.versionErr = errors.New("exec: not found")
	got := Verify(t.Context(), f, dir, refs("claude-opus-5"))
	if len(got) != 1 || !got[0].Cached || !got[0].OK {
		t.Fatalf("version unreadable: %+v, want the cached verdict", got)
	}
	if len(f.probed) != 1 {
		t.Fatalf("re-probed after a version read failure: %v", f.probed)
	}
}

// A corrupt cache costs a re-probe, never a startup failure.
func TestLoadToleratesCorruptCache(t *testing.T) {
	dir := t.TempDir()
	if err := writeRaw(Path(dir), "{not json"); err != nil {
		t.Fatal(err)
	}
	if c := Load(dir); c.Models == nil || len(c.Models) != 0 {
		t.Fatalf("Load() on corrupt cache = %+v, want an empty usable cache", c)
	}
}

func TestVerifyNoRefsDoesNothing(t *testing.T) {
	f := &fakeVerifier{version: "2.1.225"}
	if got := Verify(t.Context(), f, t.TempDir(), nil); got != nil {
		t.Fatalf("Verify(nil) = %+v, want nil", got)
	}
	if len(f.probed) != 0 {
		t.Fatalf("probed %v with no refs", f.probed)
	}
}

func TestSkip(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	for name, m := range map[string]map[string]string{
		"explicit 1":   {"HAL_SKIP_MODEL_VERIFY": "1"},
		"true":         {"HAL_SKIP_MODEL_VERIFY": "TRUE"},
		"padded yes":   {"HAL_SKIP_MODEL_VERIFY": "  yes  "},
		"fake harness": {"HAL_FAKE_BIN": "/tmp/fake"},
	} {
		if !Skip(env(m)) {
			t.Errorf("%s: Skip = false, want true", name)
		}
	}
	// "0" must not skip — same trap the agy verification comment calls out.
	for name, m := range map[string]map[string]string{
		"unset": {},
		"zero":  {"HAL_SKIP_MODEL_VERIFY": "0"},
		"false": {"HAL_SKIP_MODEL_VERIFY": "false"},
		"empty": {"HAL_SKIP_MODEL_VERIFY": ""},
	} {
		if Skip(env(m)) {
			t.Errorf("%s: Skip = true, want false", name)
		}
	}
}

// The blocking error must name every offending key and the escape hatch — it
// is the only thing an operator sees when startup refuses.
func TestInvalidModelErrorMessage(t *testing.T) {
	err := &InvalidModelError{Results: []Result{
		{Ref: config.ModelRef{Where: "workflow.stages.design", Agent: "claude", Model: "claude-sonnnet-5"}},
	}}
	msg := err.Error()
	for _, want := range []string{"workflow.stages.design", "claude-sonnnet-5", "HAL_SKIP_MODEL_VERIFY"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message missing %q: %s", want, msg)
		}
	}
}
