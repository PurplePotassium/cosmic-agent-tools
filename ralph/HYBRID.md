# Parallel-Ralph fleet: many workers + a merge/plan integrator

A Gastown-style agent fleet. Optionally split across two harnesses by what each is best at:

- **Implementation → fast workers** — many lanes run in parallel, each in its own git worktree/branch,
  each making ONE verified increment per pass against a hard file scope. Drive these with `claude` or,
  for speed, Antigravity/Gemini (`agy`).
- **Merging → Claude Code (`refinery.ps1`)** — a Bors-style merge queue that integrates lane branches
  under a gate and bisects out anything that regresses. Whole-codebase reasoning a safe merge needs.
- **Planning → Claude Code (`plan.ps1`)** — keeps `TODO.md` full and carved into per-lane, file-disjoint
  sections so the workers never collide.

Workers and integrator are **decoupled through git** — the refinery merges commits and doesn't care
which agent produced them. That's what lets a mixed fleet (claude + agy lanes) cooperate. All project
specifics (trunk branch, gate, paths) live in `fleet.config.ps1` — see [SETUP.md](SETUP.md).

```
            ┌─────────────── Claude Code ───────────────┐
 plan.ps1   │ PLANNER  → shapes TODO.md into disjoint    │
 (Opus)     │            per-lane sections               │
            └───────────────────┬───────────────────────┘
                                 │ TODO.md on trunk (Base)
        ┌────────────────────────┼────────────────────────┐   ← lanes (claude and/or agy)
        ▼                        ▼                         ▼
       api                      ui                       docs        (each owns DISJOINT files)
   src/api, src/db         src/ui, src/styles        docs/           (shared seam: src/types.ts)
        │                        │                         │   (each: 1 increment → gate → commit)
        └────────────────────────┼─────────────────────────┘
                                 ▼
                          refinery.ps1  ← Claude Code, MAIN worktree (Root)
                          poll → merge → gate (your GateCmd) → bisect-on-red
                                 ▼
                          trunk: Base  (green)
```

## One-time setup
1. **Do the [SETUP.md](SETUP.md) checklist** (config, trunk branch, PROMPT, lanes, .gitattributes).
2. **Pick your lane agent(s).** `claude` works out of the box if Claude Code is installed + authed.
3. **(Optional) Antigravity/Gemini for fast lanes.** Install the Antigravity CLI so `agy` is on PATH
   and **sign in to your subscription** — agy draws on your **Pro/Ultra account quota**, NOT metered
   API tokens. There is no `agy auth login` subcommand in 1.0.13; sign in via the Antigravity desktop
   app or the first interactive `agy` run (it shares the session). **Do NOT set `GEMINI_API_KEY`** —
   agy ignores it. Verify:
   ```powershell
   agy models                                          # lists your account's models → proves auth
   agy -p "reply PONG" --dangerously-skip-permissions  # headless pass on the subscription
   ```
   Flags (verified against **agy 1.0.13**, confirm with `agy --help`): prompt `-p`, auto-approve
   `--dangerously-skip-permissions`, model `--model <id>` (omit for account default; see `agy models`),
   and **`--print-timeout`** (default **5m** — the driver raises it to 30m so a real increment isn't
   killed mid-pass). Don't use `--sandbox` (it can block the gate). Set a lane to agy via
   `<name> agent=agy model=<id>` in `lanes.txt`. After install, **open a fresh terminal** so PATH
   updates are visible.

   **Quota reality:** a parallel fleet of unattended agents is token-hungry by design. Subscription
   quota is finite — running several lanes 24/7 can exhaust it fast. Start with one or two lanes and
   watch usage before scaling N.

## Run order
**One command (recommended)** — brings up all lane workers + the refinery, each in its own window:
```powershell
# BOUNDED TEST: 3 passes per lane; refinery polls 12×@45s then exits. Safe first run (caps spend).
.\start-fleet.ps1 -LaneIterations 3 -RefineryIterations 12 -RefineryInterval 45

# OPEN-ENDED: lanes + refinery (+ planner every 30 min). Ctrl-C each window (or stop-fleet) to stop.
.\start-fleet.ps1 -WithPlanner
```
**Or step by step:**
```powershell
.\plan.ps1                         # (optional) Claude refills/repartitions the backlog first
.\ralph-fleet.ps1 -Launch          # worker loops, one window each
.\refinery.ps1 -IntervalSeconds 60 # SEPARATE window: the Claude merge loop (owns main worktree)
```
Stop with `.\stop-fleet.ps1` or Ctrl-C in each window. `integrate.ps1` is the one-shot merge. Single
lane: `ralph-fleet.ps1 -Launch -Names api`. `start-fleet.ps1` auto-stashes a dirty main worktree (the
refinery resets it) and restores it on `stop-fleet.ps1`.

### Driving an agy worker from the Antigravity IDE instead of the loop
The loop (`ralph.ps1 -Agent agy`) is the unattended path. You can also open a lane worktree
(`<Root>-wt-<name>`) in the Antigravity IDE and point its agent at `ralph/PROMPT.md`. In that case
**commit each increment yourself** (the loop's auto-commit only fires when the loop runs the agent).
The refinery picks up your commits either way.

## How it fits together (the load-bearing details)
- **Per-lane PROMPT.md** = lane scoping header (`lane-<name>.md`) + the base `PROMPT.md` with every
  `Root` path rewritten to the lane's worktree, so a lane edits ITS OWN tree. `ralph-fleet.ps1` writes
  it; it's gitignored.
- **Commits.** Claude lanes commit via the workspace Stop hook if you have one; either way `ralph.ps1`
  self-commits each dirty pass, so every pass is one clean, bisectable commit — what the refinery merges.
- **The gate is the whole safety story.** Your `GateCmd` (fleet.config.ps1) gates both the lane's own
  pass and the refinery's merge. Faster, less-careful workers make a strong gate MORE important, not
  less — keep it green and keep it meaningful.
- **Disjoint file ownership > conflict resolution.** Each `lane-<name>.md` hard-assigns files. Two
  lanes editing the same file is the failure mode; the planner enforces the partition and the refinery
  bisects out whatever slips through. Adding lanes means partitioning files, not raising N.
- **Refinery is bail-safe.** Never force-pushes; conflicts are `git merge --abort` + flagged (not
  machine-resolved); `--no-ff` so a regressing lane peels off with one `reset HEAD^`; a flagged lane
  isn't retried until it advances (state in `.refinery-state.json`). Deliberately the opposite of the
  "auto-merge red into main, then force-push to recover" failure other swarms hit.

## Real-time monitoring
```powershell
.\watch-fleet.ps1     # live dashboard: per-lane iter / ahead / agy-commit count + last commit,
                      # newest agent-log tail, and refinery state (refreshes ~4s, read-only)
```
- agy writes each pass's activity to `<Root>-wt-<lane>\ralph\logs\agy-NNNN-*.log` (via `--log-file`);
  claude lanes log to `iter-NNNN-*.log` in the same dir.
- Quick pulse: `git -C <Root> log --oneline -15` shows lane `[<agent>]` commits + refinery
  `Merge 'ralph-<lane>' into <Base>`; tail `<Root>\ralph\logs\refinery-*.log`.
- Healthy run: lane commits `ralph iter N [<agent>]` → refinery `INTEGRATION PASS -- landed: <lane>`
  → trunk advances → your gate stays green.

## Model + reasoning (agy note)
Set the worker model per lane in `lanes.txt` (`<name> agent=agy model=gemini-3.5-flash`). agy 1.0.13's
CLI has **no reasoning/effort flag** (only `--model`); change models by editing `lanes.txt` and
relaunching.

## Troubleshooting
- **Lanes run but produce ZERO commits.** The post-pass lane-scope check WARNS on out-of-scope edits,
  it does NOT revert them (an earlier version reverted real work on an empty allow-list). Trust the
  commit / `files changed` footer, not captured stdout (agy can drop piped stdout while still editing).
- **Lanes sit idle right after launch** = threw at startup. Common with agy: not on the inherited PATH
  (you launched from a shell predating the install — the lane self-refreshes PATH from the registry;
  a FRESH terminal also fixes it); or an auth pre-flight stderr-as-error (now caught + warned).
- **Spice stuck on `[recode:food/Past]`** = the persona/noun pools didn't load (empty `$PSScriptRoot`).
  The fleet passes pools explicitly; relaunch.
- **Lane edits a file it doesn't own → refinery conflicts.** Tighten that `lane-<name>.md` scope and
  re-run the planner; the offending commit stays flagged until the lane advances past it.
- **Lanes see new planning automatically.** The planner writes `TODO.md` (and re-partitioned
  `lane-*.md`) on trunk; each lane loop merges trunk into its branch at the START of every pass
  (`ralph.ps1 -SyncBase <Base>`, set by the fleet), so the worker picks up the latest backlog + other
  lanes' integrated work before it runs. Merge only happens when trunk is genuinely ahead and the tree
  is clean; a conflict aborts and retries next pass.
- **Is a lane alive?** Logs/commits land only at pass boundaries; a mid-pass gap is normal. Check the
  process tree, not the logs (`ralph-status.ps1`).

## Concurrency hardening (known races + how they're handled)
- **Lane file scope.** `ralph.ps1` runs a post-pass check: it extracts the owned/seam files from
  `lane-<name>.md` and compares against `git status --porcelain`. Out-of-scope edits are WARNED and
  committed anyway (the refinery is the real guard — it abort+flags any cross-lane MERGE conflict). It
  deliberately does NOT `reset --hard` (that discarded real work in an earlier version).
- **Conflict oscillation & blockers.** When two lanes pass individually but fail combined, the refinery
  keeps the earlier and records the later under a `blocked` property in `.refinery-state.json` (value =
  the integrated set it regressed against). Kept lanes land; the blocked lane is SKIPPED until cleared,
  so it can't oscillate. The planner reads the blocks, injects them into its prompt to re-partition the
  offending lanes' files/sections, and — only if it actually COMMITS a re-partition (HEAD moved) —
  clears them (clearing on a no-op pass would just re-thrash). The re-partition reaches the blocked
  worker via the trunk→lane sync. Caveat: without a per-lane verify, the blocker field is the
  integrated set — a hint, not a proof of the culprit.
- **Auth pre-flight & watchdog.** N agy lanes hitting Windows Credential Manager / token refresh at the
  same instant can race. `ralph-fleet.ps1 -Launch` staggers launches (`-LaunchStaggerSeconds`, default
  8s). An auth pre-flight (`agy models` on loop startup) + a loop watchdog detect auth/token failure
  exit codes, alert, and halt to prevent runaway loops on expired sessions.
- **Stale worktree / merge state.** A Ctrl-C'd run can leave an in-progress merge or orphaned worktree
  admin that blocks the next checkout. The refinery clears `MERGE_HEAD` + runs `git worktree prune` at
  the top of every round; `ralph-fleet.ps1` prunes before creating worktrees. If a branch is still
  "already checked out" somewhere: `git worktree list` then `git worktree remove <path>`.
- **Append docs DON'T conflict.** Mark `DONE.md`/`NOTES.md`/etc. `merge=union` in `.gitattributes`
  (see `gitattributes.sample`) — concurrent appends from different lanes auto-concatenate (order may
  interleave; fine for a log). Don't put structured (delete-able) content in these.
- **TODO.md is not a live race.** Lanes read their OWN worktree's copy, so there's no shared-file race
  with the planner. Real risks: (a) a TODO.md *merge* conflict if a lane and the planner edit the same
  lines — avoided by lanes editing only their own section; the refinery abort+flags any that slip; and
  (b) the planner and refinery both mutating the MAIN worktree at once — `plan.ps1` refuses to run while
  a merge is in progress there. Don't run the planner and refinery in the same worktree simultaneously.
