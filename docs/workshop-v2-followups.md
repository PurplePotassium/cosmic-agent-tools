# Workshop v2 follow-ups — harvest from the PR #1 / PR #3 comparative review

**Date:** 2026-07-02
**Decision:** PR #3 (`user/gewan/workshop-v2`) is the go-forward implementation. PR #1
(`user/gewan/go_workshop`) will be closed. This document preserves everything from the
comparative review that remains actionable: features from PR #1 worth porting, regressions
in v2 to restore, defects both branches share, and v2's own must-fix list.

Items marked **(v)** were hand-verified in the code during the review; unmarked items come
from the deep review passes and should be re-confirmed at fix time. File references are
against the two PR branches as of 2026-07-02.

---

## A. Features from PR #1 worth porting into v2

| # | Item | Priority | Notes |
|---|------|----------|-------|
| A1 | **Live per-pass agent/model switching + UI selector** | High | PR #1 re-reads agent/model from the DB every pass (`engine.go:124-135`) and the dashboard has a model selector, matching the old `agent.json` workflow. v2 loads config once at startup and its UI has no model selector at all — changing models requires editing `config.toml` and restarting. Daily-use regression vs both the old tool and PR #1. Per-task pins exist in v2 but are CLI-only. |
| A2 | **Out-of-the-box auto-routing** | High | PR #1 ports the old keyword classifier verbatim (`internal/agent/autoroute.go`) and it works with zero config. v2's `[types.*]` routing table does nothing until the operator hand-writes routing config — ship a default routing table equivalent to the old curated agent/model combos. |
| A3 | **(v) Correct module path** | Must-fix pre-merge | PR #1 declares `module github.com/PurplePotassium/cosmic-agent-tools/workshop`; v2 declares `github.com/gw1108/...` (the fork, not the canonical repo). `go install` by the canonical path breaks after merge. One-line fix + import rewrite. |
| A4 | **(v) macOS in the CI matrix** | Medium | PR #1 tests ubuntu/macos/windows; v2 tests ubuntu/windows only. Relevant because v2's Unix PID-liveness fallback is weakest on darwin (kill(pid,0) only, no start-time check). |
| A5 | **Release packaging polish** | Medium | PR #1's goreleaser ships archives + deb/rpm (nfpm, with git declared as a dependency) + Homebrew tap + Scoop bucket + `--version` ldflags. v2's goreleaser is more minimal. Caveat from both branches: publishers reference tap/bucket repos and a `HOMEBREW_TAP_GITHUB_TOKEN` that don't exist yet — create them before the first tag, and add a `goreleaser --snapshot` check to CI (neither branch has one). |
| A6 | **Dashboard liveness affordances** | Low | PR #1's 3s status ticker pushes elapsed-time, dirty-file list, and a "computing vs waiting on model" hint over SSE. v2 dropped the dirty-file feed and the computing probe. Nice-to-have for operator confidence during long passes. |
| A7 | **Reconcile test pattern** | Reference | PR #1's `TestReconcilePreservesConcurrentEdits` pins the "UI edits survive an agent pass" invariant for its membership-diff reconcile. If v2 ever grows UI-vs-agent edit races on its statedir projections, this is the invariant to test. |

## B. Regressions in v2 (vs the old PowerShell tool) to restore or explicitly drop

| # | Item | Priority | Notes |
|---|------|----------|-------|
| B1 | **(v) Failed/wedged passes commit half-done work — ungated on trunk in default mode** | Critical | `worker.go:538,570` commit on failure ("keep pass boundaries bisectable"). Defensible in worktree mode where the gate protects trunk; in the **default single-pipeline simple mode there is no gate**, so broken trees land directly on the user's branch. The old loop skipped the commit on failure. Fix: skip commit on failure in simple mode, or make the rationale mode-conditional. |
| B2 | **(v) Preview pane gone, knob left behind** | Medium | `project.preview` (config.go:34) is defined/defaulted but referenced nowhere — dead config. The old UI served `PreviewUrl`/`PreviewPath` (incl. a repo-relative static dir) in an iframe. Implement or delete the knob. Same for `server.update_check` (config.go:82) — dead. |
| B3 | **`-AgentExtraArgs` / `-SleepSeconds` unwired** | Medium | `ExtraArgs` and `SleepBetween` are plumbed through `WorkerConfig` (worker.go:64,78) but nothing in app.go/config populates them. Both rewrites have this same dead plumbing. Wire to config.toml keys. |
| B4 | **`workshop stop` has no force fallback** | Medium | It only asks a responsive server politely via `server.json`. The old `stop-workshop.ps1` force-killed the tree by fingerprint. A hung engine is currently unkillable by the tool itself. Add a `--force` path (pid from server.json → KillTree). |
| B5 | **Old dedicated-branch mode gone** | Low | The old `Branch` knob committed onto a reviewable branch with no merge queue. v2 offers only "straight to trunk" (simple) or "full worktrees + queue". A middle mode may be worth restoring for solo review workflows. |
| B6 | **(v) `RALPH_PASS` env var never set** | Verify | The old tool exported it for repo-side hooks. Absent from v2's Go code (grep-confirmed). If nothing depends on it, document the removal; otherwise restore. |
| B7 | **Custom persona/noun pool files** | Verify | Old tool accepted arbitrary pool file paths; v2 embeds two flavors (general/gamedev). Check whether `.workshop/prompts/` fragments cover the use case; if not, add a pool-file knob. |
| B8 | **Wedge semantics changed: old flagged, new kills** | Doc-only | Old tool showed "wedged?" in the UI; v2 kills the pass at `wedge_minutes`. Intentional improvement, but document it — and see D7 for the broken default. |

## C. Shared defects (identical in both branches — fix once in v2)

| # | Item | Priority | Notes |
|---|------|----------|-------|
| C1 | **(v) `authRe` matches "author"** | High | `(?i)auth|credential|sign-?in|...` (worker.go:46) substring-matches "author"/"OAuth" anywhere in a failed pass's output tail → pipeline permanently halts as "auth failure" (e.g. git's "Author identity unknown"). Both branches' tests check "token"/"login" but not "author". Fix with word boundaries/anchoring + a regression test. Inherited verbatim from the old script. |
| C2 | **No Windows Job Object** | Medium | `taskkill /T` walks parent-PID chains, so a grandchild whose intermediate parent exited (npm→node chains) survives the kill (proc_windows.go). A Job Object with kill-on-close also fixes the orphan-on-crash case. |
| C3 | **agy prompt as one argv element** | Medium | The full composed prompt (contract + GOAL + fragments + task) is a single command-line argument (driver/agy.go:80). Windows caps the command line at 32,767 chars — large GOAL/fragments fail the spawn opaquely. claude avoids this via stdin; agy needs a file-based prompt or truncation guard. |
| C4 | **(v) `Scanner.Err()` unchecked in the drain loop** | Low (v2) | An over-long line (>1MB buffer cap, worker.go:478) silently ends output capture — the tail and auth scan go blind for the rest of the pass. In v2 the wedge timeout + 5s drain timeout bound the damage (verified worker.go:505-516); in PR #1 the same flaw could hang a pass forever. Log a "capture truncated" marker on scanner error. |

## D. v2's own must-fix list (from the deep review of PR #3)

### Critical / major — gate multi-pipeline mode on these

| # | Item | Where |
|---|------|-------|
| D1 | **Worker trunk-sync races the integrator.** Workers merge the *live* trunk into their lanes while the integrator may be mid-round (trunk holds unvetted combined merges for the whole gate duration, then may `reset --hard`). A worker syncing in that window absorbs later-bisected-out gate-failing commits into its lane and then gets blamed as "proven culprit". Fix direction: sync a last-known-green ref (recorded after each green gate), never live trunk. Untested path. | integrator.go:139-211, worker.go:273-283 |
| D2 | **Livelock: conflicted lane can never advance (default config).** With no `[types.merge-conflict]` route, a lane conflicting with trunk skips at the trunk-sync step *before ever claiming*, so its tip never advances and skip-until-advanced never releases. One event per pass, no halt, no alert. | worker.go:276-282, integrator.go:310-320 |
| D3 | **Livelock: conflicted lane can't claim its own resolution task.** The lane's worker returns idle at the sync conflict before claiming, so unless another pipeline handles type `merge-conflict`, the resolution task sits open forever. | worker.go:273-283, conflict path |
| D4 | **Setup-failure spin loop.** A task pinned to an unknown agent (never validated at `task pin` or the API) releases its claim without attempts++ or breaker credit → the worker re-claims it in a zero-sleep tight loop forever, growing passes/events unboundedly. Fix: validate agent names at pin/API time + backoff + count attempts. | worker.go:296-302,667-673; task.go:106; server.go:207 |
| D5 | **`CleanupOrphanPasses` clobbers live passes.** It runs on every `app.Open`, so any CLI invocation (`status`, `task add`) during a live pass marks the in-flight pass row failed in the shared DB. Scope it to the engine's own startup, or check server.json liveness first. | app/app.go:71 |
| D6 | **No already-running guard on `workshop run`.** Two invocations (or `run` + `up`) put two workers on the same worktree/branch. The old start script explicitly refused to stack loops. Reuse the server.json/pid singleton check. | cmd/workshop/main.go:107-145 |
| D7 | **`wedge_minutes` default (20) < agy `--print-timeout` (30m).** Default config kills healthy agy passes and can trip the breaker on a working pipeline; AGENTS.md itself says wedge must exceed the print timeout. Default ≥35m, or per-driver wedge defaults. | config.go:96 vs driver/agy.go:72 |
| D8 | **Integrator swallows non-conflict merge failures.** `continue` with no event on e.g. missing git identity or lock contention — a lane silently never lands; in the bisect loop `markSeen` additionally strands the lane's committed work until a new commit appears. Emit events; add identity fallback to `Merge` (CommitAll already has one). | integrator.go:139-142,177-183 |
| D9 | **(v) Module path** — see A3. | go.mod |

### Minor — queue behind the above

- **(v) Origin check is substring-based** (`strings.Contains(origin, "localhost")` passes `localhost.evil.com`) and there is no Host-header check, so DNS-rebinding pages can read the unauthenticated GET APIs (status, tasks, config with absolute paths, logs, SSE). Mutations stay token-gated. Exact-match origin allowlist + Host check. — server.go:438-441
- Session token also accepted as `?token=` query param (leak channel via history/logs); the fragment/sessionStorage path makes it unnecessary. — server.go:381-383
- `.workshop/**` auto-commit happens **before** the trunk checkout, so goal/config commits land on whatever branch the operator has checked out; the round also switches the operator's checkout to trunk every 45s. — integrator.go:92-117
- `AddWorktree` adopts an existing worktree dir without verifying its checked-out branch → stale worktree from a different `branch_prefix` commits to the wrong branch while the integrator watches `workshop/<name>`. — gitx/worktree.go:84-96
- `schema_version` is written but never read on open — no forward/backward guard for schema changes. No pruning for events/passes tables (unbounded growth). — store.go:50,134-137
- State is keyed by a hash of the repo's absolute path — moving/renaming the repo silently orphans the whole DB with no hint. — config/paths.go:74-93
- `doctor` fix-text references `workshop config validate`, which doesn't exist. — doctor.go:59
- `up`'s already-running branch ignores `server.open_browser=false` / `WORKSHOP_NO_OPEN` (honors only `--no-open`). — main.go:168-171
- Windows `CREATE_NEW_CONSOLE` for agy doesn't give it a console stdout (Go sets NUL handles for nil stdio); no-hang holds, but agy console behavior is unverified. — proc_windows.go:22-26
- Test gaps: zero tests for `internal/server` (token/loopback/origin guards, SSE replay), `internal/bus`, `internal/app` (RunHeadless, TreeMu mode, worktree setup), `supervisor`, and all of `cmd/workshop`; nothing tests D1 or D2; CI runs `-race` on Linux only (pure-Go SQLite would allow it on Windows too).

---

## What was checked and found fine (don't re-litigate)

- v2's task claiming is race-free by construction (atomic `UPDATE...RETURNING`, single-conn pool) and proven by a 16-goroutine race test.
- v2's `migrate` correctly imports GOAL.md, PROMPT.md, backlog.json (order + dates preserved), completions.json; `agent.json`/`config.ps1` are intentionally manual with a documented mapping. Encoding/BOM handling is solid. (An earlier review claim of UTF-16 failures did not survive verification.)
- v2's security posture is a large upgrade: loopback-only bind, per-session mutation token via URL fragment, no CORS surface, embedded-FS serving, `server.json` 0600. (PR #1 had **no** auth mechanism at all — the decisive disqualifier.)
- v2's engine test harness (scripted fake agent covering happy/blocked/crash/auth/silent/wedge/blind paths, real multi-worktree integrator tests, torn-read hammer, grandchild-kill test) is genuinely strong.
- PR #1 claims that were **refuted** during verification, recorded so they don't resurface: no CORS-wildcard middleware existed (there was simply no auth at all); `deleteProject` never called `os.RemoveAll`; repo paths *were* validated at registration; its diff-reconcile *did* preserve concurrent UI edits (tested).
