# Workshop code review — 2026-07-03

Scope: `workshop/` (~12.7k LOC Go). Every finding below was verified by reading
the cited code (line numbers are from the tree at commit `f0d3126`). Each entry
states **what** is wrong, **why** it is wrong, the **fix** judged best, and an
**autonomous verification** — a unit/integration/e2e test or CI/lint change an
agent can run without a human in the loop. Findings are grouped by subsystem and
ordered by severity within each group.

Severity legend:
- **P1** — corrupts state, panics, hangs, or silently loses work in normal operation.
- **P2** — wrong behavior under realistic (concurrent, failure, or adversarial) conditions.
- **P3** — hardening, correctness edge cases, and paper cuts.

---

## 1. Engine / merge queue (`internal/engine`)

### W-1 (P1) Conflict tasks pin a live, unvetted trunk SHA

**Where:** `internal/engine/integrator.go:359` (`handleConflict`), consumed at
`internal/engine/conflict.go:63`.

**What:** `handleConflict` runs from inside `RunRound`'s phase-1 merge loop
(`integrator.go:165-184`) and resolves `trunkTip` from the **live** trunk:

```go
trunkTip, err := gitx.RevParse(ctx, ig.cfg.RepoDir, ig.cfg.Trunk)
```

At that moment trunk already contains the `--no-ff` merges of every lane that
merged earlier in the same round — merges the gate has not yet vetted and that
`ResetHard(preMerge)` (`integrator.go:204`) may erase minutes later. That SHA is
stored as `MetaTrunkTip` (`integrator.go:394`) and the resolution pass merges it
into the resolution branch (`conflict.go:63`).

**Why it's wrong:** two concrete failure modes.
1. *Bisected-out work re-enters trunk.* Lane A merges, lane B conflicts and pins
   trunk+A. The gate then goes red and the bisect drops A as a proven culprit.
   B's resolution branch still contains A's merge; when the resolution lands
   through the queue, A's dropped commits ride back onto trunk without any lane
   being blamed for them.
2. *Unresolvable pin.* After `ResetHard(preMerge)`, the merge commits the pin
   points at are unreachable. Once git GC (or `PruneWorktrees`-adjacent
   housekeeping) collects them, `gitx.Merge(resolveDir, trunkTip)` in
   `conflict.go:63` fails, `failSetup` burns the task's attempts, and the lane
   parks with no way to succeed.

The codebase already documents this exact hazard: the `GreenRef` comment
(`integrator.go:36-42`) says lanes must sync from the green ref "never from
live trunk" for precisely these reasons. `handleConflict` violates its own
subsystem's invariant.

**Fix:** pin the last gate-proven state instead of live trunk. Resolve
`GreenRef` (fall back to `preMerge`, which `RunRound` already holds) and store
that as `MetaTrunkTip`. `preMerge` is threaded to `handleConflict` either as a
parameter or on `pendingLane`.

**Verify (autonomous):** integration test in `integrator_test.go` using the
existing real-git rig: three lanes where lane A lands a gate-red commit, lane B
conflicts with trunk after A merges in the same round. Assert
(a) the created conflict task's `MetaTrunkTip` equals the pre-round green SHA,
not a SHA containing A's merge (`git merge-base --is-ancestor <A-merge> <pin>`
must be false); (b) after the resolution task completes via the fake agent and
lands, `git log trunk` contains no commit authored by lane A. A second test:
run the round, `ResetHard` fires, then run `git rev-parse <MetaTrunkTip>^{commit}`
— it must still resolve (i.e. the pin is a ref the repo keeps reachable).

---

### W-2 (P1) Failure paths never clear `ConflictTaskID` — a lane can loop forever, minting unbounded conflict tasks

**Where:** `internal/engine/integrator.go:364-373` (skip-until-advanced),
`integrator.go:241-250` (bisect gate-red), `integrator.go:427-431` (`markSeen`);
gate check at `integrator.go:279` and `resolutionPending` at `:315-353`.

**What:** `collectPending` parks any lane with `ConflictTaskID != ""` and
re-offers its resolution branch when the task is done (`:279-284`).
`ConflictTaskID` is cleared only in `land()` (`:421`) and in
`resolutionPending`'s vanished/failed-task branches (`:319`, `:343`). Three
paths that take a resolution lane out of the round leave it set:

1. The resolution branch itself conflicts with (moved) trunk →
   `handleConflict`. Once `ConflictAttempts >= budget` the skip branch
   (`:364-373`) sets `LastSeenTip` and resets `ConflictAttempts = 0` — but
   `ConflictTaskID` still points at the **done** task.
2. The resolution branch fails the gate in the bisect → `:241-250` sets
   `Blocked = true` but leaves `ConflictTaskID`.
3. Conflict-in-combination → `markSeen` (`:224`, `:427-431`), same gap.

**Why it's wrong:** next round, `collectPending` sees `ConflictTaskID != ""`,
`resolutionPending` finds the task done and the branch still existing (none of
these paths deleted it) and re-offers it — **before ever consulting
`li.Blocked`** (`:323-338` checks task status only). Case 1 then re-conflicts
with `attempts == 0 < budget` and mints a brand-new conflict task
(`:383-404`): an infinite loop that creates tasks and burns agent passes
forever. Case 2 re-merges and re-runs the full gate (potentially many minutes)
on a proven-red branch every single round, forever.

**Fix:** make every terminal path for a resolution lane also retire the parked
state. Concretely: in `handleConflict`'s skip branch and in the bisect-red and
`markSeen` paths, when `lane.resolution` is true, delete the resolution branch
and clear `ConflictTaskID` (mirroring `resolutionPending`'s failed-task branch
`:339-349`), and have `resolutionPending` respect `li.Blocked` the same way
`collectPending` does for normal lanes (skip until the *lane* tip advances).
Do **not** reset `ConflictAttempts` to 0 in the skip branch while the same
lane tip is being retried — reset it only when `LastSeenTip` actually changes.

**Verify (autonomous):** integration test: lane conflicts → conflict task →
fake agent resolves → move trunk so the resolution branch re-conflicts → run N
rounds. Assert the total number of `merge-conflict` tasks ever created is
bounded by `ConflictRetryBudget` (query the store), and that after the budget
is spent the lane is parked (no new tasks, no merge attempts) until its tip
advances. Second test: force the resolution branch gate-red; run 3 rounds;
assert the gate command ran at most once for that branch (counting via a gate
script that appends to a file — the rig already uses script gates).

---

### W-3 (P1) `li, _ := GetIntegration(...)` — nil-pointer panic on any store error

**Where:** `internal/engine/integrator.go:241`, `:357`, `:413`, `:428`;
`internal/store/runs.go:210-211`.

**What:** `GetIntegration` returns `(nil, err)` for any error other than
`sql.ErrNoRows` (`runs.go:207-212`). Four call sites discard the error and
immediately write through the pointer (`li.LastSeenTip = ...`).

**Why it's wrong:** a transient `SQLITE_BUSY`, a cancelled context (the
supervisor cancels the integrator's context on shutdown, `supervisor.go:76`),
or an fs hiccup returns `nil, err` → panic in the integrator goroutine → the
whole process dies, possibly with trunk mid-merge. This is the worst possible
crash point in the program.

**Fix:** handle the error at all four sites: on error, emit an
`integration.error` event and return/skip the lane for this round (the state
write is best-effort bookkeeping; skipping a round is always safe here).
`collectPending` (`:272-275`) already shows the correct pattern.

**Verify (autonomous):** unit test with a store wrapper (or a closed `*sql.DB`)
that forces `GetIntegration` to error; call `land`/`markSeen`/`handleConflict`
and assert no panic (the test itself is the assertion — plus assert the
emitted event). Cheaper stopgap that catches the whole class: add
`nilness`/`nilerr` to `.golangci.yml` and run `golangci-lint` in CI (see W-33)
— `nilerr` flags exactly this `x, _ :=`-then-deref shape.

---

### W-4 (P1) A crash (or failed reset) between phase-1 merges and the gate reset silently blesses ungated commits on trunk

**Where:** `internal/engine/integrator.go:88-94` (round hygiene), `:142-144`
(GreenRef baseline), `:204` / `:238` (resets), `:262-266` (`recordGreen`),
`:293-299` (skip logic); `Loop` at `:68-77`.

**What:** round-start hygiene is `CleanStaleLock` + `MergeAbort` +
`PruneWorktrees` only. Trunk is never reconciled against `GreenRef`. If the
process dies (or `ResetHard` at `:204`/`:238` fails on lock contention — note
`Loop` at `:70` swallows every `RunRound` error unless the context is dead)
after some lanes merged but before the gate verdict was acted on, those
`--no-ff` merge commits stay on trunk.

**Why it's wrong:** on restart the affected lanes' `LastSeenTip` was never
updated, but `AheadCount(trunk, branch) == 0` (`:296-299`) so they are skipped
— their work is now *on trunk without ever having been gated*, invisibly. The
next green round calls `recordGreen` (`:194`/`:257`), advancing `GreenRef`
past the unvetted commits, and every lane then syncs them as "known green".
The gate — "the whole safety story" per the project's own README — has a hole
exactly across crashes, which for a self-hosting agent loop are routine.

**Fix:** at round start, if `GreenRef` exists and `trunk != GreenRef` and the
extra commits are workshop-made merge commits (subject `ws(...)`/merge of
`workshop/*` branches), reset trunk to `GreenRef` before proceeding (never
force-push; trunk is local — the lanes still hold the work and will re-merge
gated). If the extra commits are human commits, leave them and just re-baseline
after a gate run. Additionally: propagate `RunRound` errors in `Loop` to an
`integration.error` event instead of dropping them.

**Verify (autonomous):** integration test simulating the crash: run phase-1
merges manually with `gitx` against the rig repo (merge two lane branches into
trunk, do *not* reset), set `GreenRef` to the pre-merge SHA, then run
`RunRound`. Assert trunk equals `GreenRef` (+ any newly gated merges) after the
round and that the lanes subsequently land through the gate (their commits
appear exactly once). This also covers the `ResetHard`-failure path since the
recovery logic is the same.

---

### W-5 (P1) Auth-halt false positive: any failed pass whose output *mentions* auth permanently halts the pipeline

**Where:** `internal/engine/worker.go:48` (`authRe`), used at `:575`.

**What:** on any nonzero exit, the last ~400 lines of pass output are scanned
with

```go
var authRe = regexp.MustCompile(`(?i)\bauth(entication|...)?\b|credential|...`)
```

`\bauth\b` matches path segments (`internal/auth/handler.go`, `src/auth/`),
and `credential` is unanchored (`credentials_test.go`). The comment claims the
regex is narrow against "author"/"OAuth", but not against legitimate mentions.

**Why it's wrong:** a claude pass working on an auth module fails for an
unrelated reason (test failure, exit 1) with `/auth/` paths all over its
output → `isAuthFailure` → `SetHalted(HaltAuth)` (`:580`). Auth halts are
deliberately permanent ("an expired login never self-heals", AGENTS.md), so
one ordinary red pass on auth-flavored code takes the pipeline down until an
operator intervenes — the opposite of autonomous.

**Fix:** require an auth *failure* phrase, not an auth *word*. Match compound
signals: `(?i)(not (logged|signed) in|invalid api key|api key.*(invalid|expired)|(authentication|authorization) (failed|error|required)|please run .*(login|auth)|token (expired|invalid)|401 unauthorized)` — patterns taken
from real agent-CLI failure output, kept in one table with a comment per entry.
Additionally require the match within the last N lines *and* pair it with a
short output (auth failures die early; a 400-line test run that mentions auth
is not an auth failure — a cheap heuristic: only scan when total output < some
threshold or exit occurred < 60s in).

**Verify (autonomous):** table-driven unit test for `isAuthFailure` with
positive cases (real captured strings: Claude Code's "Invalid API key ·
Please run /login", 401 bodies) and negative cases (`FAIL internal/auth/...`,
`--- FAIL: TestCredentialStore`, diff hunks touching `auth.go`). Plus an
engine-level test: fake-agent scenario that exits 1 while printing
`internal/auth/handler_test.go` failures — assert the pipeline is *not*
halted and the breaker (not HaltAuth) advances.

---

### W-6 (P2) Worker `Loop` swallows claim/store errors; bounded runs report success on a broken store

**Where:** `internal/engine/worker.go:311-314` (claim error →
`(PassIdle, err)`), `:234-237` (only context errors propagate), `:249-252`
(bounded/`untilDrained` → `return nil` on idle).

**What:** `RunPass` returns `PassIdle` alongside the error when `Claim` fails.
`Loop` drops non-context errors, then treats the result as a normal idle: a
bounded run returns `nil` ("drained") even though the backlog was never read;
an unbounded worker silently spins on the idle poll. No event is emitted.

**Why it's wrong:** `workshop run --iterations 20` against a locked/corrupt DB
exits 0 having done nothing — for an unattended tool, indistinguishable from
success. (Same shape one level up: `supervisor.go:86-91` discards the bounded
drain's `RunRound` error, so committed lane work can silently not land while
the CLI exits 0.)

**Fix:** emit a `pass.error` event on the claim-failure path and count
consecutive store errors toward the breaker (a dead DB should halt, not spin).
In `supervisor.go`, surface the drain error into `Run`'s return.

**Verify (autonomous):** unit test: worker with a store whose `Claim` errors
(closed DB) → `Loop(ctx, 3, false)` must return a non-nil error (or halt), not
nil; assert a `pass.error` event was published. E2e-level: `workshop run` with
a deliberately corrupted `workshop.db` must exit nonzero (exit-code contract:
0 ok / 1 halted-failed).

---

### W-7 (P2) Conflict-resolution passes discard captured output — auth failures burn the whole retry budget

**Where:** `internal/engine/conflict.go:114`
(`exitCode, _, timedOut, runErr := w.spawn(...)`).

**What:** the normal pass path keeps the output tail and runs the auth scan
(`worker.go:575`); the conflict pass throws the tail away, so
`isAuthFailure` can never halt from a conflict pass.

**Why it's wrong:** an expired login during a routed merge-conflict task fails
`ConflictRetryBudget × MaxTaskAttempts` agent passes (plus breaker churn)
instead of halting on the first — exactly the "expired login must not burn
passes" rule AGENTS.md declares.

**Fix:** keep the tail and run the same `res.caps.AuthProbe && isAuthFailure`
check the worker path uses (extract that block into a helper shared by both).

**Verify (autonomous):** engine integration test: route `merge-conflict` to the
fake agent with `WORKSHOP_FAKE_SCENARIO=auth`; drive a conflict; assert the
pipeline halts with `HaltAuth` after one pass and the conflict task is
released, not failed.

---

## 2. Server / dashboard / app (`internal/server`, `internal/app`)

### W-8 (P1) Data race on `App.Res` — dashboard pipeline mutations vs. every reader

**Where:** write: `internal/app/pipelines.go:106` (`a.Res = res` in
`reloadConfig`, reached from `POST/DELETE /api/v1/pipelines`). Unsynchronized
readers include `internal/server/server.go:520` (`resolveBacklog`),
`internal/app/app.go` (`Snapshot`, `RunHeadless` re-reading `a.Res.Config` on
every Halt-relaunch via `enginectl.go:51`), and `ConfigSnapshot`.

**What:** a plain pointer write races plain pointer reads across goroutines —
a Go memory-model violation, not just a logic race.

**Why it's wrong:** `-race` flags it deterministically; in practice a
Halt-triggered engine relaunch can snapshot config mid-reload and build
workers from a half-visible view. Notably CI runs `-race` only on non-Windows
(`.github/workflows/ci.yml`), and unit tests never exercise the HTTP mutation
path concurrently — so the race is invisible today.

**Fix:** make `Res` an `atomic.Pointer[config.Resolved]` (readers call
`a.Res.Load()` once per operation and use that snapshot), and serialize
writers with a mutex in `reloadConfig`. The snapshot-per-operation discipline
also fixes torn multi-field reads.

**Verify (autonomous):** unit test that hammers `AddPipeline`/`DeletePipeline`
from N goroutines while M goroutines call `Snapshot`/`ConfigSnapshot`, run
under `-race` (it fails today, passes after). CI: add a Windows `-race` job or
at minimum keep this test in the Unix `-race` matrix leg (`go test -race
./internal/app/...`).

---

### W-9 (P2) Lost-update on the pipeline overrides file (two writers, no lock)

**Where:** `internal/app/pipelines.go:34`/`:51` (read current list →
`persistPipelines` rewrites whole array); `internal/config/overrides.go:18-37`
(read file → mutate map → atomic write; its own doc note at `:15-17` makes
callers pass the *complete* list, which maximizes the loss).

**What:** classic read-modify-write with no mutual exclusion, across both
goroutines (two HTTP requests) and processes (dashboard + CLI).

**Why it's wrong:** concurrent `POST /api/v1/pipelines` for A and B: both read
the same base list; whichever writes last silently erases the other's
pipeline. The write is atomic (temp+rename) but the *transaction* isn't.

**Fix:** in-process: the same mutex as W-8 around the read-modify-write.
Cross-process: an `O_CREATE|O_EXCL` lockfile (or `flock`-style lock via
`retrySharing`'s pattern) around `WriteOverridePipelines` — the file is tiny
and writes are rare, so a coarse lock is right.

**Verify (autonomous):** unit test: 10 goroutines each `AddPipeline` a unique
name; assert the final overrides file contains all 10. (Today this flakes to
< 10.) Run under `-race` as well.

---

### W-10 (P2) SSE stream silently loses events: bus drops promise a store re-sync that was never implemented, and replay caps at 500

**Where:** `internal/bus/bus.go:40-43` (drop on full 128-buffer, comment: "the
subscriber re-syncs from the store by sequence number");
`internal/server/sse.go:68-78` (only de-dups `ev.Seq <= lastSent`, never
detects `ev.Seq > lastSent+1`); `sse.go:52` (`EventsSince(ctx, since, 500)` —
no continuation loop, and its error is swallowed).

**What:** the contract between bus and consumer is half-built. Gap detection
and re-query don't exist; a reconnect that is > 500 events behind replays 500
then jumps to live, leaving a permanent hole.

**Why it's wrong:** the dashboard is the only window into an unattended
system. During a burst (merge round + several passes finishing), a slow tab
drops persisted events — a breaker trip or auth halt can simply never render.
No error, no recovery until a manual refresh.

**Fix:** in the SSE loop, when a live event arrives with
`ev.Seq > lastSent+1`, query `EventsSince(lastSent, ...)` and send the gap
first (loop until caught up), then the live event. In the initial replay, loop
`EventsSince` in pages until fewer than the page size return. Surface a
`replay-error` SSE comment event instead of swallowing the store error.

**Verify (autonomous):** httptest-based SSE test (none exists — see W-31):
publish 700 events, connect with `since=0`, read the stream, assert all 700
arrive in order. Second test: subscribe with a full channel (tiny
`subscriberBuffer` via a test hook, or publish 200 events while the client
reader sleeps), then assert every persisted seq is eventually received exactly
once.

---

### W-11 (P2) Graceful shutdown always eats the full 5s timeout while a dashboard tab is open

**Where:** `internal/server/sse.go:61-79` (select has no server-shutdown
signal); `internal/server/server.go:95-100` (`Shutdown`);
`cmd/workshop/main.go:197-201` (5s deadline).

**What:** `http.Server.Shutdown` waits for active handlers and does **not**
cancel in-flight request contexts. The SSE handler only exits on client
disconnect, so every shutdown with a connected dashboard blocks until the 5s
deadline, then the connection is torn down hard.

**Fix:** give the server a `BaseContext` derived from the app context, or call
`RegisterOnShutdown(func(){ ... })` to cancel a server-scoped context the SSE
select also watches. One extra `case <-s.closing:` in the loop.

**Verify (autonomous):** httptest test: open an SSE connection, call
`Shutdown` with a 3s-deadline context, assert it returns in well under 1s.
Also assert `workshop stop` latency in the e2e suite (< 2s wall) so the
regression stays caught at the binary level.

---

### W-12 (P2) `http.Server` has no timeouts; read endpoints have no auth at all

**Where:** `internal/server/server.go:83` (`&http.Server{Handler: ...}` —
zero timeouts); read routes at `:106-133` vs. token guard only on mutations
(`:136-158`).

**What/why:** (a) no `ReadHeaderTimeout`/`IdleTimeout` means any local process
can hold connections open indefinitely with partial headers (slowloris is
trivially local here; crashed clients linger forever). `WriteTimeout` must
stay 0 for SSE, but the other two are free. (b) `guardLoopback` checks the
peer is loopback, not *who* the peer is: on a shared machine, any other OS
user can read `/api/v1/runs/{id}/log` (full agent output), `/goal`, prompts,
and attachments. Mutations are token-gated; reads leak everything the
mutations protect.

**Fix:** `ReadHeaderTimeout: 10s, IdleTimeout: 120s` on the server. Require
the same `X-Workshop-Token` on read routes too — the dashboard already
carries the token (URL fragment → sessionStorage), so the only client change
is sending the header on GETs and the SSE URL (EventSource can't set headers;
accept the token via a query param *for the SSE route only* or a cookie set
by the first authorized request).

**Verify (autonomous):** server unit tests: GET `/api/v1/runs` without a token
→ 403; with token → 200. SSE with/without. Lint-level: a test asserting
`s.http.ReadHeaderTimeout != 0` is cheap insurance against regression.

---

### W-13 (P3) Miscellany in server/app, all verified

- `server.go:533` — token compared with `==`; use
  `subtle.ConstantTimeCompare`. *Verify:* trivially unit-testable; or accept
  as a one-line fix with the existing auth tests.
- `sse.go:52` — replay error silently swallowed (covered by W-10's fix).
- `internal/app/enginectl.go:62-70` — `Halt()` after the run loop already
  exited latches `stopping=true` forever and reports `{"halting":true}` for a
  no-op. *Fix:* check `e.cancel == nil`/loop-done state and return a
  "not running" result. *Verify:* unit test calling `Halt` after `Done()`
  fires; assert a subsequent `Start` still works and the API reports
  accurately.
- `internal/app/surface.go` (`PassLog`) + `server.go:506-512` — whole log file
  slurped into memory and written; a multi-hundred-MB pass log balloons the
  process. *Fix:* `http.ServeFile`/`io.Copy` with a size cap or tail
  semantics. *Verify:* unit test asserting the handler streams (allocations
  bounded) or at least caps: request a synthetic 100MB log, assert response
  truncates to the cap.

---

## 3. Process & driver layer (`internal/proc`, `internal/driver`)

### W-14 (P1) The claude capability probe can hang startup forever (grandchild holds the pipe; no `WaitDelay`; not under tree-kill)

**Where:** `internal/driver/claude.go:51-53`.

**What:** `exec.CommandContext(hctx, exe, "--help").CombinedOutput()` sets
neither `cmd.Cancel` nor `cmd.WaitDelay`. `CombinedOutput` returns on pipe
EOF, not process exit. On Windows, `claude` on PATH is typically an npm
`claude.cmd` shim: the 15s context kills the shim (cmd.exe), but the node.exe
child inherits the pipe write-end and keeps it open, so the internal copy
goroutine never sees EOF and `CombinedOutput` blocks forever. The probe child
is also not spawned through `proc.Configure`/`adopt`, so nothing can
tree-kill it.

**Why it's wrong:** one wedged `claude --help` (node update mid-flight, AV
scan, corrupted install) hangs `workshop`/`workshop run` at startup with no
timeout, no error, no dashboard.

**Fix:** set `cmd.WaitDelay = 2 * time.Second` (Go ≥1.20 semantics: after
Cancel, abandon the pipes) — a two-line fix. Better: route the probe through
the proc package so the shim's tree dies as one unit.

**Verify (autonomous):** unit test with `WORKSHOP_CLAUDE_BIN` pointed at a
test-built shim that spawns a grandchild which sleeps 60s while inheriting
stdout, parent exiting immediately (the fakeagent infra can host this
scenario). Assert `Probe(ctx)` returns within ~5s. Must run in the Windows CI
leg — this failure mode is Windows-shaped.

---

### W-15 (P2) `killTree` on Windows: "already exited" is reported as failure, and the Job-Object success path skips the taskkill backstop

**Where:** `internal/proc/proc_windows.go:112-116` and `:101-108`; contrast
`proc_unix.go:32` (forgives `ESRCH`).

**What:** (a) when the pid is already gone, `taskkill` exits 128
(`The process "N" not found.`) and `killTree` returns an error for a non-event
— the Unix path explicitly forgives the equivalent. (b) when
`TerminateJobObject` succeeds, the function returns immediately; a grandchild
spawned in the child's first milliseconds *before* `adopt`'s
`AssignProcessToJobObject` (`:68` — the race the comment at `:43-45` admits)
is outside the job and survives the "tree" kill.

**Why it's wrong:** (a) the wedge-kill path reports spurious kill failures
exactly when the agent exits at the deadline (a common race), polluting
alerts/events that operators and tests key on. (b) an instantly-spawned test
server or build daemon leaks past a wedge kill — the precise leak Job Objects
were adopted to prevent (AGENTS.md hard rule: "killed as a tree").

**Fix:** (a) parse taskkill's not-found case (exit code 128 / message match)
and return nil, mirroring ESRCH. (b) after a successful
`TerminateJobObject`, still run `taskkill /T /F` best-effort (ignore its
result) to sweep pre-adoption escapees — or close the race properly by
creating the process suspended (`CREATE_SUSPENDED`), assigning to the job,
then resuming.

**Verify (autonomous):** (a) unit test: spawn a short-lived process, wait for
exit, call `KillTree(pid)`, assert nil. (b) extend
`TestKillTreeKillsGrandchild` (`proc_test.go`) with an `Adopt`ed variant whose
child spawns the grandchild *immediately* and then exits — assert the
grandchild dies. Note the existing test never calls `Adopt` at all, so the
preferred Windows path currently has zero direct coverage (see W-31).

---

### W-16 (P2) agy Windows command-line length check undercounts quoting — passes prompts that then fail the real 32,767 limit blind

**Where:** `internal/driver/agy.go:85-94`
(`total += len(s) + 3 // separator + worst-case quoting`).

**What:** Go's Windows argv encoding escapes `"` as `\"` and doubles
backslash runs before quotes; a quote/backslash-dense arg (JSON, Windows
paths in GOAL.md) can nearly double in encoded length. `+3` per arg is not
worst-case; a ~17k-char raw prompt can encode past 32,767, pass this check,
and produce exactly the opaque blind spawn failure the guard exists to
prevent (agy failures are invisible — AGENTS.md).

**Fix:** measure the encoded length: build the command line the way the
runtime will (`syscall.EscapeArg` per arg, joined with spaces, plus the
quoted exe) and compare *that* against the cap.

**Verify (autonomous):** unit test: prompt of 12,000 `\"`-heavy chars — assert
`Plan` rejects it; same length of plain letters — assert it passes; and a
property-style assertion that `Plan`'s accept/reject decision matches
`len(encoded) <= cap` for a table of adversarial strings.

---

### W-17 (P3) Driver/proc paper cuts, all verified

- `internal/proc/proc_unix.go:23-24` — `os.Open(os.DevNull)` leaks one fd per
  Consoled spawn (exec.Cmd never closes caller-supplied files). Fix: leave
  `Stdin` nil (already means the null device). *Verify:* `golangci-lint`
  `bodyclose`-adjacent linters won't catch this; a unit test comparing fd
  counts before/after 100 spawns on Unix CI will.
- `internal/proc/proc_windows.go:124-134` — `alive` returns true for exit code
  259 collisions and for reused PIDs. Fix: prefer `WaitForSingleObject(h, 0)`
  == WAIT_TIMEOUT over the exit-code sentinel; PID-reuse needs a start-time
  check (`GetProcessTimes`) stored at spawn. *Verify:* unit test spawning a
  child that exits with code 259; `Alive` must go false.
- `internal/driver/claude.go:93-94` / `fake.go` — `WORKSHOP_*_BIN` env values
  used verbatim; a relative path resolves against `cmd.Dir` (the agent's
  worktree), so a previous pass committing `bin\claude.exe` gets *executed as
  the agent* next pass. Fix: `filepath.Abs` + stat at probe time; reject
  directories. *Verify:* unit test with a relative `WORKSHOP_CLAUDE_BIN` —
  probe must fail with an explicit error.
- `internal/fakeagent/fakeagent.go:62` — conflict resolution deletes any line
  *starting with* `=======`, eating setext underlines and similar content;
  can make merge-resolution tests pass on corrupted files. Fix: match marker
  lines exactly (`line == "======="`). *Verify:* fakeagent unit test with a
  conflicted file containing a legit `=========` underline; assert it
  survives.

---

## 4. Git layer (`internal/gitx`)

### W-18 (P1) Parsed git output uses `CombinedOutput` — stderr warnings corrupt plumbing results

**Where:** `internal/gitx/gitx.go:32-41` (`run` → `cmd.CombinedOutput()`),
feeding `RevParse` (`:112`), `CurrentBranch` (`:75`), `StatusPorcelain`,
`AheadCount`, `Worktrees`, etc.

**What:** any stderr output on a *successful* command lands in the parsed
result. Real triggers: `warning: refname 'x' is ambiguous.` (rev-parse still
exits 0 → garbage SHA fed to `UpdateRef`/`ResetHard`), fsmonitor/`unable to
access` warnings during `status --porcelain` (parsed as a changed path →
`IsDirty` true on a clean tree → spurious engine commits, or an integrator
round skipped as "human mid-edit"), advice lines before `rev-list --count`
(`AheadCount` atoi fails).

**Fix:** split the helper: `run` keeps CombinedOutput for imperative commands
(checkout, merge — where stderr belongs in the error); add `runOut` using
`cmd.Output()` with stderr captured into the `Error` wrapper (ExitError
already carries `Stderr`), and move every *parsed* call site onto it.

**Verify (autonomous):** unit tests in the existing real-git rig: create an
ambiguous ref (branch and tag both named `x`), assert `RevParse(dir, "x")`
returns a valid 40-hex SHA (regexp assert) — today it returns the warning
text. Add a `^[0-9a-f]{40}$` validation inside `RevParse` itself as
defense-in-depth so any future pollution fails loudly.

---

### W-19 (P2) No `--` separator before ref/branch/path arguments anywhere

**Where:** `gitx.go:90` (checkout), `:96`/`:106` (branch), `:112`
(rev-parse), `:280-285` (merge — ref appended last), `:346` (reset);
`worktree.go` add/remove.

**What:** names derived from config (`git.branch_prefix`, pipeline names) and
task data are passed positionally; a value starting with `-` is parsed as a
git option (`CheckoutBranch(dir, "--detach")` silently detaches HEAD;
`ResetHard(dir, "--keep")` changes semantics). Pipeline-name validation
(`config.pipelineNameRe`) narrows exposure but is itself only a warning (see
W-24), and branch_prefix is unvalidated.

**Fix:** add `--` before every name/ref argument where git supports it, and
reject leading-`-` values in `gitx` itself (cheap invariant at the boundary).

**Verify (autonomous):** unit tests: `CheckoutBranch(dir, "--detach")` must
return an error and leave HEAD attached; `ResetHard(dir, "--keep")` must
error. These are two-line tests in the existing rig.

---

### W-20 (P2) `StatusPorcelain` mishandles C-quoted (non-ASCII) paths

**Where:** `internal/gitx/gitx.go:126-134` — `strings.Trim(p, "\"")` strips
quotes but never unescapes `\303\274`-style octal; rename entries with quoted
halves also split wrong.

**Why it's wrong:** an agent creates `über.md` → every consumer of the
changed-path list (dirty checks, `.workshop/**` intent detection at
`integrator.go:101-108`, diff ingestion) operates on a filename that doesn't
exist. The intent-commit check misclassifying means a goal edit can block
integration rounds ("human mid-edit") indefinitely.

**Fix:** run status with `-c core.quotepath=off` (git then emits UTF-8 paths
verbatim) — one flag, no parser needed. Keep the trim as a fallback.

**Verify (autonomous):** rig unit test: create `über.md` and a
`ü1.md → ü2.md` rename, assert `StatusPorcelain` returns the exact on-disk
names (`os.Stat` each returned path must succeed).

---

### W-21 (P3) `normPath` lowercases on every platform

**Where:** `internal/gitx/worktree.go:73-79`.

**What:** correct on Windows, wrong on case-sensitive filesystems:
`FindWorktree` can match `/repo/Lane1` to `/repo/lane1` and "adopt" the wrong
directory's worktree. **Fix:** lowercase only when `runtime.GOOS == "windows"`
(or `darwin` for default HFS+ semantics — decide and comment). **Verify:**
Unix-only unit test creating two case-distinct sibling dirs; assert
`FindWorktree` doesn't cross-match (runs in the Linux CI leg).

---

## 5. Store / persistence (`internal/store`, `internal/statedir`)

### W-22 (P1) Schema version is written once and never advanced — the "newer DB" guard is dead code

**Where:** `internal/store/store.go:149-152`
(`ON CONFLICT(k) DO NOTHING`), guard at `:137-142`.

**What:** the stored `schema_version` is frozen at whatever version first
created the DB. When `schemaVersion` becomes 2 and a v1 DB is migrated
forward, the kv row still says 1 — so the refuse-newer-binary guard (the
comment's whole purpose: additive-only statements writing into a future
layout "could silently corrupt it") can never fire for any DB created before
the newest version.

**Fix:** `ON CONFLICT(k) DO UPDATE SET v = excluded.v` — and only after the
schema statements succeeded (current ordering is already correct).

**Verify (autonomous):** unit test: open a store (creates DB at version N),
close; flip a test seam (`schemaVersion` var or a store option) to N+1, open
again, close; read kv directly and assert `schema_version == N+1`; then
re-open with version N and assert `Open` returns the "created by a newer
workshop" error. This test is impossible to write today without the fix —
which is the point.

---

### W-23 (P2) Multi-writer store races: `AddTask` position, `CompleteTask` double-completion, backlog `Ingest` dedupe

**Where:** `internal/store/tasks.go:71-83` (SELECT MAX/MIN then separate
INSERT, no transaction — same in `MoveTask`); `tasks.go:207-209` (`UPDATE ...
SET status='done' WHERE id = ?` — no `AND status='claimed'` guard, so a
repeated call inserts duplicate completion rows); `internal/backlog/backlog.go`
(`Ingest` snapshot-then-insert TOCTOU across concurrent workers).

**Why it's wrong:** writers are *not* one process — `workshop task add` runs
concurrently with the engine and dashboard. Two adds can compute the same
position (both `--first` adds get `min-1024`, then order falls back to
creation time — the operator's "top" placement silently isn't); a retried
finalize double-counts completions in the digest agents read; two workers can
ingest the same proposed title.

**Fix:** wrap `AddTask`/`MoveTask` read+write in a transaction (modernc
sqlite serializes writers, so `BEGIN IMMEDIATE` is enough); add the status
guard to `CompleteTask` (`WHERE id=? AND status='claimed'`, treat 0 rows as
`ErrNotFound`-or-already-done); give `Ingest` a uniqueness check inside the
insert transaction (or a normalized-title unique index on open tasks).

**Verify (autonomous):** store unit tests: (a) 20 goroutines `AddTask(top)`
into one backlog; assert all 20 positions strictly ordered (no duplicates);
(b) call `CompleteTask` twice; assert one completion row; (c) two concurrent
`Ingest` of the same proposal; assert one task. All run under `-race`.

---

### W-24 (P2) Config validation never blocks, and safety numbers aren't validated at all

**Where:** `internal/config/load.go:102-104` (every `Validate()` error demoted
to a warning — directly contradicting `config.go:261-262` "Errors block
startup"); `Validate` (`config.go:263-314`) checks pipelines/types/worktrees
but no `[safety]` field.

**Why it's wrong:** for an unattended tool the config *is* the safety system.
`wedge_minutes = 0` (PassTimeout 0 — wedge protection off or instant-kill
depending on downstream zero handling), negative `breaker_failures`,
`max_concurrent = 0`, port 999999, reserved pipeline name — all boot fine
with at most a doctor WARN nobody reads.

**Fix:** split `Validate` into errors and warnings (two slices or a severity
field). Errors — invalid safety numbers, reserved/duplicate/malformed pipeline
names, invalid port — abort startup with exit code 2 (the documented config
exit code). Model-mismatch stays a warning (documented behavior). Also fix
`config.go:267-271`: the name regex is matched against the *lowercased* name,
so `MyPipe` passes but the raw mixed-case string is what lands in branch
names and exact-match SQL while `resolveBacklog` matches case-insensitively —
validate the raw name.

**Verify (autonomous):** config unit tests per bad key asserting `Load`
returns an error (not a warning); e2e: `workshop run` with
`wedge_minutes = 0` in the scaffolded config must exit 2. Regression test
that `[types.art] model=...` *without* an agent is either validated against
the resolved agent or rejected (see W-25).

---

### W-25 (P2) `[types.X]` with a model but no agent bypasses validation *and* leaks a foreign model onto the pipeline's agent

**Where:** `internal/config/config.go:299-303` (type models only checked
`if b.Agent != ""`); `internal/route/route.go:40-45` (merge strips base
model/effort only when `over.Agent` is set and differs).

**Why it's wrong:** `[types.art] model = "gemini-3-flash"` (agent omitted)
validates clean, then routes `gemini-3-flash` onto a claude pipeline — the
exact "some agents fail silently on a bad id" hazard the merge guard's own
comment warns about, and for agy it fails *blind* (AGENTS.md).

**Fix:** in `Validate`, when a type sets a model without an agent, validate
the model against every agent that could receive it (all pipelines claiming
that type) — or more simply, require `agent` whenever `model` is set on a
type (clear error message; config stays explicit).

**Verify (autonomous):** unit tests: config above must produce an
error/warning naming the mismatch; route-level test asserting the resolved
bundle for a claude pipeline + agent-less gemini-model type never yields
`{agent: claude, model: gemini-*}`.

---

### W-26 (P2) `workshop migrate` is non-idempotent and swallows write failures

**Where:** `cmd/workshop/migrate.go:93-110` (tasks re-imported wholesale — no
dedupe, no "already migrated" marker; old IDs discarded so re-runs mint fresh
ULIDs), `:120-137` (same for completions; also `TaskID: oc.ID` maps the old
*completion* record's id into `TaskID` — verify against the old schema, it
mislabels if those ids weren't task ids), `:57`/`:76` (`if err := Write...;
err == nil { copied(...) }` — on error *nothing* prints and exit code stays 0).

**Why it's wrong:** an interrupted or absent-mindedly repeated
`workshop migrate --from X` doubles the entire backlog and history; a failed
GOAL.md import looks identical to success.

**Fix:** write a `migrated-from-<hash>` marker into the kv table (or state
dir) and refuse a second run without `--force`; report and exit nonzero on
any write error; dedupe tasks by (title, created) on import as
belt-and-suspenders.

**Verify (autonomous):** unit/e2e test: run migrate twice against a fixture
dir; assert task count equals fixture count and second run exits nonzero with
"already migrated". Point `--from` at an unreadable dest (or read-only dir)
and assert nonzero exit.

---

### W-27 (P3) Persistence paper cuts, verified

- `internal/statedir/json.go:79-91` — `WriteFileAtomic` never fsyncs file or
  directory before rename: after power loss the rename can survive while data
  doesn't (zero-length `GOAL.md`/`task.json`). Fix: `tmp.Sync()` before
  close (dir-fsync is a no-op on Windows; guard by GOOS). *Verify:* can't
  test power loss autonomously — enforce by convention with a small unit test
  asserting `Sync` is called via an injectable FS seam, or accept and
  document.
- `internal/store/store.go:36-37` — DSN built by string concat; a repo path
  containing `#` or `?` (legal on Windows: `C:\dev\proj#2`) truncates the URI
  filename. Fix: `url.PathEscape` the path (keep `/`). *Verify:* unit test
  opening a store under a temp dir named `x#y?z`; assert the DB file appears
  *in that dir*.
- `cmd/workshop/task.go:48-65` — `parseMixed` infinite-loops on a bare `-`
  argument (the inner loop refuses to consume it, outer loop never
  terminates): `workshop task add fix - thing` spins at 100% CPU. Fix: treat
  a lone `-` as positional (`rest[i] == "-"` → consume). *Verify:* unit test
  with args `{"fix", "-", "thing"}` under a 5s test timeout — hangs today.
- `cmd/workshop/task.go` (`add`/`tag`) — task types never validated against
  the vocabulary; `--type tets` silently creates a task no filtered pipeline
  will drain. Fix: warn (stderr) on unknown types, suggesting the vocab list.
  *Verify:* unit test asserting the warning.
- `internal/prompt/spice.go:65` — `stem[:1]` byte-slices the first rune;
  non-ASCII pool entries produce invalid UTF-8. Fix: slice runes. *Verify:*
  unit test with an `Éclair` pool entry asserting `utf8.ValidString`.

---

## 6. Prompt integrity (`internal/prompt`)

### W-28 (P2) Agent-originated task text is interpolated raw into future prompts (prompt injection across passes)

**Where:** `internal/prompt/compose.go:76-84` (`TaskBlock` — title, multi-line
`Detail`, `Files` all raw), `:66-68` (`ScopeHint`); tasks can originate from
agent proposals via `statedir.ReadProposals` (`internal/statedir/files.go`),
which validates only that a title exists.

**What:** a task's `detail` containing `\n## MECHANICS FOR THIS PASS\n- VERIFY
COMMAND: <anything>` or a forged `files (edit exactly these):` line is
indistinguishable, in the composed prompt, from engine-authored structure.

**Why it's wrong:** the system's trust model everywhere else is explicit —
"a misbehaving agent's edits are diff-ingested harmlessly, never trusted"
(AGENTS.md). This is the one channel where a hallucinating or manipulated
pass can durably steer *future* passes (including other pipelines', via the
shared backlog): weaken the verify instruction, redirect scope, or smuggle
directives past the operator who only sees the task title in the dashboard.

**Fix:** fence untrusted fields. Indent every task-derived line by four
spaces or wrap in a clearly-delimited block
(`<task-data>` ... `</task-data>` with a contract line telling the agent that
nothing inside is an instruction from the engine), and strip/escape lines
matching `^#`/`^##` in task text at ingest (`backlog.Ingest`) as
defense-in-depth. Cap detail length at ingest too.

**Verify (autonomous):** prompt unit test: compose with a detail containing
`## MECHANICS` and a fake verify line; assert the composed prompt contains no
unfenced `## ` heading originating from task data (parse the output's heading
set and compare against the engine's known headings). Ingest test: proposal
with 50 heading lines → stored task detail is fenced/stripped.

---

## 7. Testing & CI gaps

Current state (verified by running): `go test ./... -count=1` **passes**
(~36s), `go vet` clean. CI (`.github/workflows/ci.yml`) runs
ubuntu+macos+windows, `-race` on Unix only, plus a goreleaser dry-run. The
suite's real-git + real-subprocess approach is genuinely strong. The gaps:

### W-29 (P1) CI never runs the e2e suite

`go test -tags e2e ./e2e` — the suite the README calls "the orchestrator-level
proof" and the self-hosting gate depends on — is not in `ci.yml`. A PR (or a
GitHub-side merge) can break the binary end-to-end while CI stays green; only
the local self-hosting gate would notice, and only on this one machine.
**Fix:** add a CI step `go test -tags e2e -timeout 10m ./e2e` on all three
OSes (it's hermetic by design — temp state dirs, fake driver, isolated git
config). **Verify:** the CI run itself; plus a guard test is unnecessary —
the workflow file is the artifact.

### W-30 (P1) `-race` is skipped exactly on the primary platform, and no race-shaped tests exist for the API layer

Windows CI runs plain `go test`; development and production are Windows.
W-8/W-9 are invisible under this setup. **Fix:** enable `-race` on the
Windows leg (supported; needs CGO + gcc, which GitHub's windows-latest
provides — if runtime cost is the concern, run only `./internal/app/...
./internal/server/... ./internal/store/...` under `-race` on Windows), and
add the concurrent-mutation tests from W-8/W-9/W-23 so `-race` has something
to bite on. **Verify:** CI matrix change + the new tests failing pre-fix,
passing post-fix.

### W-31 (P2) Zero-coverage zones that map one-to-one onto the bugs above

- **`cmd/workshop/` (~1,300 lines, no test file):** CLI dispatch, `task.go`
  (W-27's infinite loop lives here), `migrate.go` (W-26), `doctor.go`,
  `scaffold.go`. *Fix:* unit-test `parseMixed`, migrate idempotence, and pin
  parsing directly; the e2e suite covers dispatch.
- **`internal/server/sse.go`: no test touches the SSE endpoint at all** (all
  server tests use `httptest.NewRecorder`; SSE needs `httptest.NewServer`).
  W-10/W-11 are unfalsifiable until this exists. *Fix:* an `sse_test.go` with
  a real listener: replay, Last-Event-ID resume, gap re-sync, shutdown
  latency.
- **`internal/bus/`: no direct tests** for drop-on-full and
  subscribe/cancel/publish races. *Fix:* small unit suite incl. a `-race`
  hammer test.
- **`internal/engine/supervisor.go`: only e2e touches it.** The bounded-drain
  error swallow (W-6) hides here. *Fix:* supervisor unit test with a failing
  integrator stub.
- **Windows Job Object claims are untested** (`proc_test.go` never calls
  `Adopt` — see W-15). The AGENTS.md guarantees (grandchild-with-dead-parent,
  kill-on-close after crash) are asserted nowhere. *Fix:* the W-15 tests plus
  a kill-on-close test: spawn a helper process that itself spawns+adopts an
  agent tree then exits without cleanup; assert the tree dies.
- **Crash-recovery paths:** `CleanupOrphanPasses` (`store/runs.go`), stale
  engine-lock takeover (`app.go`), corrupted `workshop.db`, and syntactically
  invalid agent-written JSON (`proposals.json` garbage → `readProposals`)
  have zero tests — exactly the "process died mid-pass" story a self-hosting
  loop will eventually hit. *Fix:* fixture-based unit tests for each: a db
  file of random bytes must produce a clean error (not a panic) and exit 2;
  a half-written proposals.json must be skipped with an event.

### W-32 (P2) Timing-based flakiness in the suite

`integrator_test.go` (`clearBackoff` = bare `time.Sleep(5ms)` racing a 1ms
`RetryBackoff`), `halt_test.go` (50/200ms negative-assertion windows),
`enginectl_test.go` similarly. These pass on a fast dev box and will flake on
loaded CI runners — and for a self-hosting loop, a flaky gate is a corrupting
gate (red passes get retried and burn budget; worse, the loop may "fix" the
test). **Fix:** replace sleeps with injected clocks or event-driven waits
(poll-until with generous deadline, assert on state not time). **Verify:**
`go test -count=20 -race ./internal/engine/... ./internal/app/...` in a
nightly CI job (catching flakes autonomously instead of in production).

### W-33 (P3) golangci-lint exists but doesn't run in CI

`.golangci.yml` is checked in, the README documents the gap. Several findings
above (W-3's nilness, W-17's fd leak shape) are linter-catchable. **Fix:** add
the official golangci-lint action to `ci.yml`; add `nilness`, `nilerr`,
`contextcheck`, `sqlclosecheck` to the config. Keep it out of the self-hosting
gate initially (per the README's pre-commit rationale) — CI-only is where it
pays. **Verify:** the CI run; seed one deliberate nilerr in a scratch branch
to confirm the job actually fails.

---

## Suggested fix order

1. **W-3, W-14, W-27(parseMixed)** — panic/hang class, each a small diff.
2. **W-1, W-2, W-4** — merge-queue integrity; these are the product's core
   promise. Land W-31's integrator tests alongside.
3. **W-8, W-9 + W-30** — the data-race cluster and the CI change that keeps it
   fixed.
4. **W-5, W-7** — availability of the unattended loop (false halts / missed
   halts).
5. **W-18, W-19, W-22, W-23, W-24, W-25, W-26** — correctness hardening, each
   independently landable with its test.
6. **W-10–W-13, W-15–W-17, W-20, W-21, W-28, W-29, W-32, W-33** — as backlog
   tasks; every one above includes an autonomous verification an agent pass
   can implement and the gate can enforce.

Every item is scoped to be a single Workshop pass: one fix + its test(s), gate
green. The verifications deliberately avoid human judgment — they are unit,
integration, or e2e tests runnable by `go test`, CI matrix changes, or linter
rules, so the self-hosting loop can both apply and police them.
