# Workshop code review — 2026-07-03 — deferrals & decision rationale

Companion to `code-review-2026-07-03.md`. Records, for the implementation pass
that resolved W-12b/W-13r/W-17r(part)/W-23r/W-27r/W-31r(part)/W-32/W-33:

1. **what was deliberately left undone** and why, and
2. **the judgment calls made on the items that *were* fixed** — the alternatives
   considered and why the chosen shape won — so a later pass doesn't re-litigate
   settled decisions or assume a gap was an oversight.

---

## Part 1 — Deferrals (still open)

### D-1. W-17r — the PID-reuse start-time check

**Deferred.** Only the exit-259 half of W-17r was done (the Windows `alive`
now uses `WaitForSingleObject(h,0)==WAIT_TIMEOUT` instead of trusting the
`GetExitCodeProcess` 259 sentinel).

**Why deferred:** the PID-reuse fix is structurally larger than the exit-code
fix. It requires capturing a process's creation time at spawn
(`GetProcessTimes`) and comparing it at probe, which means:

- threading a start-time value through the `proc` public API (`Alive` today
  takes only a PID), and
- **changing the engine-lock file format** — `app.EngineLock` persists only the
  PID, so a start-time would need a new field plus back-compat handling for
  lock files written by older binaries.

That is a migration-touching change disproportionate to a P3, and the review
itself notes PID reuse "is not deterministically testable." The exit-259 bug
was the actual observed correctness defect (a process that genuinely exits 259
was reported alive forever); PID reuse is a narrow race with lower impact.

**When picked up:** the start-time capture/compare gets a unit test against the
current process (`GetProcessTimes(self)` twice → stable non-zero creation time);
the reuse scenario stays untestable and is covered by construction.

**Correction (2026-07-03, later review pass):** the "migration-touching"
premise is wrong against the current tree — `app.engineLock` *already*
persists `Started time.Time` alongside the PID, and `server.ServerInfo` does
too. Since the original process wrote its lock *after* it started, any process
whose OS creation time is later than the recorded `Started` is a reuse
impostor — no spawn-side capture, no format change, zero-`Started` files fall
back to the PID-only check. Also under-weighted: `workshop stop --force`
`KillTree`s the lock/server PID, so reuse can kill an innocent process tree —
this is effectively a P2, not a P3. Concrete plan now in
`code-review-2026-07-03.md` (W-17r-pid).

### D-2. W-31r — remaining zero-coverage zones

**Partially done.** The SSE-over-a-real-listener suite (paged replay >500
events, `since=`/Last-Event-ID resume, shutdown-drain) and the `internal/bus`
unit tests landed — the review flagged the SSE suite as the highest-value gap.

**Deferred:** the other three zones, each its own scoped pass:

- **Windows Job Object guarantees** — `proc_test.go` still never calls `Adopt`,
  so `TerminateJobObject` + the post-terminate taskkill sweep are untested.
  Needs an `Adopt`ed grandchild test and a kill-on-close test.
- **Crash-recovery paths** — `CleanupOrphanPasses`, stale engine-lock takeover,
  corrupted `workshop.db` (random bytes → clean error + exit 2, not a panic),
  invalid agent-written `proposals.json`.
- **Supervisor bounded-drain error propagation** (the W-6 fix) — a
  failing-integrator stub asserting `Run` returns the error.

**Why deferred:** pure test-authoring breadth with no production change riding
on it; bounded and low-risk to add later. The SSE suite was prioritized because
it pins the already-landed W-10/W-11 fixes against regression.

**Correction (2026-07-03, later review pass):** "no production change riding
on it" is not quite true for two of the zones. The invalid-`proposals.json`
"skipped with an event" behavior does not exist — `statedir.ReadProposals`
swallows the parse error and returns nil silently, so surfacing it needs a
small production change (error return + a `proposals.invalid` event from the
worker). And the supervisor drain test needs a seam: `RunSpec.Integrator` is a
concrete `*Integrator`, to be narrowed to a two-method interface. Concrete
per-zone test shapes now in `code-review-2026-07-03.md` (W-31r).

### D-3. W-33 — the `contextcheck` linter (dropped from the requested set)

**Not enabled**, though the review listed it alongside `nilerr` /
`sqlclosecheck` / `nilness` (all of which *were* enabled).

**Why:** run against this tree, `contextcheck`'s only findings are correct code:

- `app/inquiry.go` `AskInquiry` + its goroutine's `Bus.Publish(context.Background())`
  — the inquiry runs async and **must outlive** the HTTP request that started
  it, so a detached context is intentional, not a bug.
- `server/sse.go` replay closure — a **false positive**: the closure already
  captures and threads `r.Context()`.

Enabling it would force `//nolint` annotations on correct code without catching
a single real defect, and `contextcheck` is known-noisy on legitimate
detached-context patterns (a recurring friction for the self-hosting loop). The
rationale is recorded inline in `.golangci.yml`. `nilerr`/`sqlclosecheck` earn
their keep — their current hits are intentional and got `//nolint` with reasons,
so the linters still catch *future* regressions.

---

## Part 2 — Decision rationale on the items that were fixed

### W-23r — `Ingest` dedupe: transaction, not a unique index or a mutex

Three options were weighed:

- **Normalized-title partial unique index** (a review suggestion) — rejected:
  it would forbid *any* two open/claimed tasks with the same normalized title,
  but `AddTask` deliberately lets operators create duplicate-titled tasks. A
  global constraint would break that and make `AddTask` fail. It also needs a
  stored `norm_title` column backfilled from Go (`normTitle` collapses
  whitespace, which SQLite can't express cleanly).
- **In-process mutex around `Ingest`** — rejected as papering over: it closes
  the specific ingest-vs-ingest race but isn't DB-level.
- **One transaction around snapshot+inserts** (chosen) — the store runs
  `SetMaxOpenConns(1)`, so an open transaction holds the sole connection and a
  second `Ingest`'s `BeginTx` blocks in Go's pool until the first commits. That
  makes snapshot-then-insert atomic against concurrent ingests with **no schema
  change** and full fidelity to `normTitle` (dedupe stays in Go). Implemented as
  `Store.WithTx` + a `TaskTx` handle; `AddTask`/`ListTasks` were refactored onto
  a shared `dbtx` interface so they run against the DB or a tx unchanged.

Side effect (an improvement): on a mid-loop error `Ingest` now returns
`nil, err` after rollback, instead of the old partial `added` slice pointing at
rows that had already been individually committed.

### W-23r — `MoveTask` subquery excludes the moved row

The folded `SELECT COALESCE(MAX(position)+?,?) … WHERE … AND id != ?` excludes
the task itself so its own current position can't skew the max on a
move-to-bottom **within the same backlog** (where the row is already present).
Mirrors the shape `AddTask` uses, where the not-yet-inserted row is excluded by
construction.

### W-13r — tail-cap, not `http.ServeFile`; 2 MiB; clean line boundary

- **Tail-cap over `http.ServeFile`:** the dashboard only renders the log tail,
  so returning the last N bytes bounds memory *and* matches what the UI shows.
  `ServeFile` would stream the whole file (and add range-request surface) for no
  UI benefit.
- **2 MiB** (`maxPassLogTail`): generous for the rendered tail, tiny for RSS.
- **Cut on the first newline after the seek point:** starting the tail on a
  clean line boundary also guarantees a multi-byte UTF-8 rune is never split.
- A one-line banner marks the elision so the tail isn't mistaken for the whole.

### W-12b — query-param token (not a cookie); which routes; `pingServer`

- **Query param for SSE + attachments, header everywhere else** — `EventSource`
  and `<img src>` can't set a header. A cookie was the alternative but the
  existing design deliberately avoids cookies ("URLs leak"); the token still
  never lands in a navigable/address-bar URL (it rides an EventSource/img
  request), so the query channel doesn't leak the way a page URL would.
  Writes stay **header-only** (`authorized` unchanged); only reads get the
  `?token=` fallback (`authorizedRead`).
- **SPA shell stays ungated** — the browser can't send the fragment token on
  the initial navigation to `/`, and the static assets carry no secrets.
- **`pingServer` now authenticates** — gating `/status` would otherwise break
  the CLI's liveness/already-running detection (`up`, `status`, `doctor`); all
  three call sites already have the running instance's token from `server.json`.
- Flagged for the user: if a **cookie** (set once on first authorized request)
  is preferred over the query param, it's a localized swap.

### W-27r — the two non-obvious calls

- **fsync: accept+document, no injectable-FS seam.** `WriteFileAtomic` now
  `tmp.Sync()`s before close; dir-fsync is skipped (no-op on Windows, and the
  atomic rename already orders the replace). An injectable FS just to assert
  "Sync was called" was judged over-engineering for a one-line durability fix.
- **DSN: percent-encode per `/`-segment.** `#`/`?`/`%` are legal in
  Windows/Unix paths but truncate a `file:` URI filename. Each `/`-separated
  segment is `url.PathEscape`d (separators and the drive colon kept); SQLite
  percent-decodes the filename back. Verified a DB opens and writes under a dir
  named `x#y%z`.

### W-32 — which waits are actually flaky (and which were left alone)

Fixed the three that couple test timing to production/async timing:

- `integrator_test.clearBackoff` `Sleep(5ms)` racing a 1ms `RetryBackoff` →
  poll the task's `not_before` until it has passed (asserts on state).
  `RetryBackoff` can't be zeroed — `<=0` defaults to 1 minute.
- `enginectl_test` 200ms negative window → non-blocking `Done()` check.
  Observing the relaunch on `calls` is a **happens-before** for the swallow (the
  relaunch goroutine is spawned only from inside the `if swallow` branch).
- `server/halt_test` 50ms negative window → non-blocking check. The unauthorized
  request returns synchronously via the guard **before** any `go OnHalt()` is
  spawned.

**Left alone on purpose:** `inquiry_test.settle()` (already a poll-until-state
with a 15s deadline — the endorsed pattern; its 25ms `Sleep` is just the poll
interval) and the several `time.After(2*time.Second)` cases (those are generous
**failure deadlines** for positive events, not flaky windows).

### W-17r — `alive` uses `WaitForSingleObject`, not the exit code

A running process's handle is unsignaled (`WAIT_TIMEOUT`); an exited one signals
(`WAIT_OBJECT_0`) — **including** a process that genuinely exits with code 259,
which `GetExitCodeProcess` couldn't distinguish from `STILL_ACTIVE`. Needs
`SYNCHRONIZE` on the `OpenProcess` handle alongside `QUERY_LIMITED_INFORMATION`.
Tested with a helper that exits 259, kept openable across exit (Go holds the
handle) so the exited-handle path is actually exercised.
