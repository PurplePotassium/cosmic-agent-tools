# Workshop code review — 2026-07-03 — PENDING items only

Scope: `workshop/` code review of commit `f0d3126`. This file tracks only the
findings **not yet fixed**. Resolved items (W-1..W-11, W-14..W-16, W-18..W-22,
W-24..W-26, W-29, W-30, and parts of W-12/W-13/W-17/W-23/W-27) were removed —
see commits `0105d1f` and `0196c14` (merged in `49520f7`) and git history of
this file for their full write-ups.

Each entry: **what** is wrong, **why**, the **fix** judged best, and an
**autonomous verification** (unit/integration/e2e test or CI/lint change — no
human steps). Line numbers are from `f0d3126` and may have drifted slightly;
symbol names are stable.

Severity: **P2** — wrong behavior under realistic conditions. **P3** —
hardening and paper cuts.

---

## W-12b (P2) Read endpoints have no auth — any local OS user can read repo intelligence

**Where:** `internal/server/server.go` read routes (`GET /api/v1/status`,
`/goal`, `/prompts/*`, `/runs/{id}/log`, `/attachments/{name}`, `/events`).

**What:** `guardLoopback` checks the peer is loopback, not *who* the peer is.
On a shared machine, another OS user's process can connect to
`127.0.0.1:<port>` and read full agent pass logs, GOAL.md, prompts, and
pasted screenshots. Mutations are token-gated; reads leak everything the
mutations protect. (The header timeouts and constant-time token compare from
W-12a/W-13 are already fixed.)

**Fix:** require the same `X-Workshop-Token` on read routes. The dashboard
already carries the token (URL fragment → sessionStorage), so the client
change is sending the header on GETs — **this touches `web/ui/` JS**, which is
why it was deferred. EventSource can't set headers; accept the token via a
query parameter *for the SSE route only* (or a cookie set by the first
authorized request).

**Verify (autonomous):** server unit tests: GET `/api/v1/runs` without token →
403; with token → 200; SSE with/without. An e2e assertion that the dashboard
still loads and streams (fake driver, real binary) so the JS change is proven,
not assumed.

---

## W-13r (P3) `PassLog`/`getRunLog` slurp the whole log into memory

**Where:** `internal/app/surface.go` (`PassLog` → `os.ReadFile(p.LogPath)`),
served by `internal/server/server.go` (`getRunLog`).

**What/why:** a multi-hundred-MB agent pass log balloons the process on one
dashboard click; no streaming or size cap.

**Fix:** stream with `http.ServeFile` (it also gets range requests for free),
or cap to a tail (last N MB) since the dashboard only renders the tail anyway.

**Verify (autonomous):** handler test writing a synthetic 100 MB log file;
assert the response is served without the process RSS growing by the file
size (or assert the tail-cap semantics: response size ≤ cap, content is the
file's tail).

---

## W-17r (P3) `alive` on Windows misreports on exit code 259 and PID reuse

**Where:** `internal/proc/proc_windows.go` (`alive`).

**What:** `GetExitCodeProcess` returning 259 (`STILL_ACTIVE`) is trusted — a
process that genuinely *exits* with code 259 (crashed .NET/test hosts do) is
reported alive forever; separately, a reused PID makes `alive` true for an
unrelated process, so a wedge-watchdog can wait on a stranger.

**Fix:** prefer `WaitForSingleObject(h, 0) == WAIT_TIMEOUT` over the exit-code
sentinel. PID reuse needs a start-time check (`GetProcessTimes` captured at
spawn, compared at probe) threaded through the `proc` API where `Alive` is
used for the engine lock.

**Verify (autonomous):** Windows unit test spawning a helper that exits with
code 259; `Alive(pid)` must go false once it exits. (Runs in the Windows CI
leg.) PID-reuse is not deterministically testable; the start-time check gets a
unit test against the current process.

Also still open from W-17: `WORKSHOP_FAKE_BIN` (test driver) is used verbatim
without the absolutize+stat guard that `WORKSHOP_CLAUDE_BIN` now has — apply
the same treatment in `internal/driver/fake.go` for symmetry.

---

## W-23r (P2) Remaining store read-modify-write races: `MoveTask` and backlog `Ingest`

**Where:** `internal/store/tasks.go` (`MoveTask` — SELECT MIN/MAX then
separate UPDATE, same pattern `AddTask` had before it was fixed);
`internal/backlog/backlog.go` (`Ingest` — snapshot-then-insert dedupe).

**What/why:** `MoveTask` racing a concurrent add/move can compute a stale
edge position, silently breaking the operator's "move to top" intent.
Two workers finishing passes concurrently can both snapshot the backlog
before either inserts, admitting duplicate same-titled agent proposals.

**Fix:** `MoveTask`: fold the edge computation into the UPDATE statement with
a scalar subquery (exactly the shape `AddTask` now uses). `Ingest`: enforce
uniqueness in the insert itself — a normalized-title partial unique index on
open tasks, or an `INSERT ... WHERE NOT EXISTS` per proposal inside one
statement.

**Verify (autonomous):** store test: N goroutines `MoveTask(top)` + adds;
assert strict position ordering, no duplicates. Backlog test: two concurrent
`Ingest` calls with the same proposal title; assert exactly one task exists.

---

## W-27r (P3) Persistence/CLI paper cuts still open

1. **`WriteFileAtomic` never fsyncs** (`internal/statedir/json.go`): after
   power loss the rename can survive while data doesn't (zero-length
   GOAL.md). *Fix:* `tmp.Sync()` before close (skip dir-fsync on Windows).
   *Verify:* injectable-FS seam asserting Sync is called, or accept+document.
2. **SQLite DSN not URL-escaped** (`internal/store/store.go` `Open`): a repo
   path containing `#`/`?`/`%` (legal on Windows) truncates the URI filename.
   *Fix:* percent-encode the path (keep `/`). *Verify:* unit test opening a
   store under a temp dir named `x#y`; assert the DB file lands in that dir.
3. **Task types never validated against the vocabulary**
   (`cmd/workshop/task.go` add/tag): `--type tets` silently creates a task no
   filtered pipeline will drain. *Fix:* warn on stderr with the known-vocab
   list. *Verify:* CLI unit test asserting the warning text.
4. **`spice.go` byte-slices the first rune** (`wordStem`):
   `strings.ToUpper(stem[:1])` on a non-ASCII pool entry produces invalid
   UTF-8. *Fix:* slice runes. *Verify:* unit test with an "Éclair" pool entry
   asserting `utf8.ValidString`.

---

## W-31r (P2) Zero-coverage zones still open

- **SSE over a real listener:** the gap re-sync, paged replay, Last-Event-ID
  resume, and shutdown-drain behavior (all implemented in `sse.go` +
  `server.go`) still have **no test** — they need `httptest.NewServer`, not
  `NewRecorder`. This is the highest-value missing suite: it pins W-10/W-11
  against regression. Test shape: publish >500 events, connect with
  `since=0`, assert all arrive in order; publish while the client sleeps to
  force bus drops, assert store re-sync fills the gap; open a stream, call
  `Shutdown` with a 3s deadline, assert it returns <1s.
- **Windows Job Object guarantees:** `proc_test.go` never calls `Adopt`, so
  `TerminateJobObject` and the new post-terminate taskkill sweep are
  untested. Add an `Adopt`ed variant of the grandchild test whose child
  spawns the grandchild immediately and exits, plus a kill-on-close test
  (helper process adopts a tree then dies without cleanup).
- **Crash-recovery paths:** `CleanupOrphanPasses`, stale engine-lock
  takeover, corrupted `workshop.db` (random bytes → clean error + exit 2,
  not a panic), and syntactically invalid agent-written `proposals.json`
  (skipped with an event) have no tests.
- **`internal/bus`:** drop-on-full and subscribe/cancel/publish still have no
  direct unit tests (only the `-race` CI leg exercises them incidentally).
- **Supervisor bounded-drain error propagation** (fixed in W-6) has no direct
  unit test — a failing-integrator stub asserting `Run` returns the error.

---

## W-32 (P2) Timing-based flakiness in the suite

**Where:** `integrator_test.go` (`clearBackoff` = bare `time.Sleep(5ms)`
racing a 1ms `RetryBackoff`), `halt_test.go` (50/200ms negative-assertion
windows), `enginectl_test.go` (similar).

**Why:** these pass on a fast dev box and will flake on loaded CI runners —
and for a self-hosting loop, a flaky gate is a corrupting gate (red passes
burn budget; worse, the loop may "fix" the test).

**Fix:** replace sleeps with injected clocks or event-driven waits
(poll-until-state with a generous deadline; assert on state, never elapsed
time).

**Verify (autonomous):** nightly CI job: `go test -count=20 -race
./internal/engine/... ./internal/app/...` — catches flakes before they cost a
production pass.

---

## W-33 (P3) golangci-lint exists but doesn't run in CI

**Where:** `.golangci.yml` is checked in; `.github/workflows/ci.yml` runs only
`go vet`.

**Fix:** add the official golangci-lint action to `ci.yml`; add `nilness`,
`nilerr`, `contextcheck`, `sqlclosecheck` to the config (the `nilerr` class
would have caught the W-3 panics mechanically). Keep it out of the
self-hosting gate initially (per the README's pre-commit rationale) — CI-only.

**Verify (autonomous):** the CI run itself; seed one deliberate `x, _ :=`
nil-deref on a scratch branch to confirm the job actually fails.

---

## Suggested order

1. **W-31r (SSE suite)** — pins the already-landed W-10/W-11 fixes before
   anything regresses them.
2. **W-23r** — same-shape fix as the landed AddTask change; small diff.
3. **W-12b** — the one remaining P2 security gap; needs a careful dashboard
   JS change with an e2e check.
4. **W-32, W-33** — gate hygiene.
5. **W-13r, W-17r, W-27r** — paper cuts, one pass each.

Every item is scoped to a single Workshop pass: one fix + its autonomous
verification, gate green.
