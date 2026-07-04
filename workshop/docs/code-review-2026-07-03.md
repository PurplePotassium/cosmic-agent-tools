# Workshop code review — 2026-07-03 — PENDING items only

Scope: `workshop/` code review of commit `f0d3126`. This file tracks only the
findings **not yet fixed**. Resolved items (W-1..W-11, W-14..W-16, W-18..W-22,
W-24..W-26, W-29, W-30, and parts of W-12/W-13/W-17/W-23/W-27) were removed —
see commits `0105d1f` and `0196c14` (merged in `49520f7`) and git history of
this file for their full write-ups.

A later pass resolved **W-12b, W-13r, W-23r, W-27r, W-32, W-33** in full and
**W-17r / W-31r** in part. What each of those did:

- **W-12b** — read routes are token-gated now (`guardRead` + `authorizedRead`,
  header or `?token=` for the SSE/`<img>` GETs that can't set a header); the
  dashboard sends the token on every read and the SSE/attachment URLs carry it
  as a query param. Covered by server unit tests and an e2e
  (`TestDashboardLoadsAndStreams`) driving the real binary.
- **W-13r** — `PassLog` now tails a large log to its last 2 MiB on a clean line
  boundary with a banner, instead of slurping it whole (`TestPassLogTailCaps`).
- **W-23r** — `MoveTask` folds its edge position into the UPDATE; `Ingest`
  snapshots-and-inserts inside one `WithTx` transaction (the single store
  connection serializes concurrent ingests). Concurrency tests added.
- **W-27r** — all four paper cuts: `WriteFileAtomic` fsyncs before rename;
  the SQLite DSN percent-encodes the path; `task add/tag` warn on an unknown
  type; `wordStem` slices by rune.
- **W-32** — the three timing-coupled test waits are now deterministic
  (poll-until-state / non-blocking checks after a happens-before point).
- **W-33** — `golangci-lint` runs in CI (`.github/workflows/ci.yml`) with
  `nilerr` + `sqlclosecheck` + `govet/nilness` added; validated clean.
- **W-17r** — the Windows `alive` exit-259 bug is fixed (WaitForSingleObject),
  and `WORKSHOP_FAKE_BIN` got the absolutize+stat guard. **Still open:** the
  PID-reuse start-time check (below).
- **W-31r** — the SSE-over-a-real-listener suite (paged replay, since-resume,
  shutdown-drain) and the `internal/bus` unit tests landed. **Still open:** the
  other zero-coverage zones (below).

Each remaining entry: **what** is wrong, **why**, the **fix** judged best, and
an **autonomous verification**. Line numbers are from `f0d3126` and may have
drifted; symbol names are stable.

Severity: **P2** — wrong behavior under realistic conditions. **P3** —
hardening and paper cuts.

---

## W-17r-pid (P3→P2) `alive` on Windows can still be fooled by PID reuse

**Where:** `internal/proc/proc_windows.go` (`alive`), `internal/app/app.go`
(`acquireEngineLock`, `ReadEngineLock`), `cmd/workshop/main.go` (`cmdStop`).

**What:** the exit-259 misreport is fixed, but a reused PID still makes
`alive` true for an *unrelated* process. Severity is higher than first judged:
`workshop stop --force` calls `KillTree` on the lock/server PID
(`main.go` `forceKill`), so a reused PID doesn't just make takeover *defer* to
a stranger — it can **kill an innocent process tree**.

**Correction to the original estimate:** no lock-format change is needed.
`engineLock` already persists `Started time.Time` (`app.go`) and `ServerInfo`
already persists `Started` (`server.go`). The invariant: the original process
wrote the file *after* it started, so its OS creation time ≤ `Started`; any
process whose creation time is *later* than `Started` is a PID-reuse impostor.
No spawn-side capture, no migration — old files with a zero `Started` just
fall back to the PID-only check.

**Fix:** add `proc.AliveSince(pid int, started time.Time) bool` — alive AND
(creation time unknown OR ≤ `started` + ~2s margin). Windows: same
`OpenProcess` + `WaitForSingleObject` as `alive`, plus `GetProcessTimes`
creation time. Linux: `/proc/<pid>/stat` field 22 + btime; other Unix: fall
back to `alive`. Switch the three probe sites: `acquireEngineLock`
(`ReadEngineLock` returns the `Started` too), `cmdStop`'s engine-lock branch,
and `cmdStop`'s `si.PID` branch (use `si.Started`).

**Verify (autonomous):** deterministic without real PID reuse:
`AliveSince(os.Getpid(), time.Now())` → true;
`AliveSince(os.Getpid(), time.Unix(1, 0))` → false (creation after the
recorded start simulates reuse exactly); spawned-sleeper live/killed cases;
`GetProcessTimes(self)` twice → stable non-zero creation time.

---

## W-31r (P2) Remaining zero-coverage zones

The SSE real-listener suite and `internal/bus` tests are done. Still open,
with the concrete shape each test takes:

- **Windows Job Object guarantees:** `proc_test.go` never calls `Adopt`.
  - *Adopted-grandchild:* start the existing `spawner` helper, `Adopt` it,
    let it exit (breaking the parent-PID chain), then `KillTree` — the
    grandchild must die via `TerminateJobObject`, asserted with the
    `tasklist` ground truth (`isAlive`).
  - *Kill-on-close:* start a `sleeper`, `Adopt` it, call `Finished` (closes
    the job handle) — `KILL_ON_JOB_CLOSE` must reap it. This pins the
    crash-cleanup guarantee (OS closing our handles kills the tree)
    in-process, no crash simulation needed.
- **Crash-recovery paths:**
  - *`CleanupOrphanPasses`:* store test — `StartPass`, call it, assert the
    running row is closed.
  - *Stale engine-lock takeover:* `enginectl_test.go` is `package app`, so
    `acquireEngineLock` is callable directly. Dead-PID lock → takeover
    succeeds and the lock holds our PID; live-sleeper-PID lock → refused.
    After W-17r-pid lands, add the reuse case: live PID + `Started` earlier
    than its creation time → takeover.
  - *Corrupted `workshop.db`:* random bytes at the path → `store.Open`
    returns a clean error (migration fails), no panic. The CLI's generic
    open-error path already exits 2.
  - *Invalid `proposals.json`:* **needs a small production change first** —
    `statedir.ReadProposals` currently swallows the parse error and returns
    nil silently (`files.go`), so the "skipped with an event" behavior this
    review described does not exist yet. Return the error (or an ok bool) and
    have the worker publish a `proposals.invalid` event before proceeding
    with none; then a worker-level test asserts the event.
- **Supervisor bounded-drain error propagation** (the W-6 fix): needs a
  seam — `RunSpec.Integrator` is a concrete `*Integrator`. Narrow it to a
  two-method interface (`Loop`, `RunRound`); call sites don't change. Then a
  bounded `Run` with zero workers and a failing `RunRound` stub must return
  the wrapped drain error.

---

## Suggested order

1. **Supervisor drain seam + test** — smallest, pins W-6.
2. **Store crash-recovery tests** — `CleanupOrphanPasses`, corrupted-db.
3. **Windows Job Object tests** — adopted-grandchild + kill-on-close.
4. **W-17r-pid `AliveSince`** + the engine-lock takeover tests (they
   interlock: the reuse case needs the new probe).
5. **`ReadProposals` error surfacing** + the invalid-proposals event test
   (the one item with a production change riding on it).

Every item is scoped to a single Workshop pass: one fix + its autonomous
verification, gate green.
