# Ralph loops + fleet orchestrator (standalone)

A dumb `while` loop around a smart agent. Each iteration runs a coding agent (`claude -p`, or `agy`)
with a **fresh context**, reads the repo + your prompt, makes one increment of progress, leaves notes,
commits, and exits. The loop runs it again. Named after the "Ralph Wiggum" technique — let a coding
agent grind on a task autonomously for a long time.

## Install into a project

This package is **project-agnostic** — drop this `ralph/` folder into the repo you want agents to work on:

```bash
git clone https://github.com/PurplePotassium/cosmic-agent-tools
cp -r cosmic-agent-tools/ralph /path/to/your-project/ralph
```
Windows PowerShell:
```powershell
git clone https://github.com/PurplePotassium/cosmic-agent-tools
Copy-Item -Recurse cosmic-agent-tools\ralph C:\path\to\your-project\ralph
```

New here? Read **[SETUP.md](SETUP.md)** next — it's the bring-up checklist (config + prompt + lanes). The
single knob file is `fleet.config.ps1`. Tip: copy `repo-root-AGENTS.md` / `repo-root-CLAUDE.md` to your
project root so any agent that opens the project discovers the loop on its own.

> **Scaling past one loop?** Jump to **[the fleet](#scaling-past-one-loop-the-fleet)** below — many lanes
> run in parallel worktrees, a **refinery** merges their branches under a gate, and a **planner** keeps
> the backlog file-disjoint.

Two flavors of the single loop:

- **Plain Ralph** — same prompt every pass.
- **Improved Ralph (`--random` / `-Random`)** — injects per-iteration randomness so the agent stops
  circling on extended runs. From [turso.tech/blog/edgar-allan-poe](https://turso.tech/blog/edgar-allan-poe):
  a long-running agent "stops exploring new paths." Their fix added *semantic tension*. This loop
  implements two modes, chosen at random each pass:
  - **persona** — "channel the mindset of `<random persona>`" (Edgar Allan Poe, a paranoid security
    auditor, a speedrunner hunting glitches, …). The blog found a stuck agent found 3 new bugs in 5 min
    after being told to think like Poe.
  - **recode-decode** — *Recoding-Decoding*: a priming noun at the **start** (`Related to FOOD:`) + a
    diverting word-stem at the **end** (`Pas`) that the model resolves creatively. Pools live in
    `personas.txt` and `nouns.txt` — edit freely.

- **Game-dev Ralph (`ralph-gamedev.ps1` / `ralph-gamedev.sh`)** — same engine, `-Random` always on,
  but the pools are swapped for game-dev sets (`personas-gamedev.txt` / `nouns-gamedev.txt`: pixel-art
  leads, systems designers, brutal QA playtesters, …; boss fights, loot tables, hitboxes, juice…).

Point any loop at custom pools with `-Personas`/`-Nouns` (PS) or `--personas`/`--nouns` (bash).

## Setup (single loop)

1. `copy PROMPT.example.md PROMPT.md` and edit it for your task. Write it so one cold agent can make a
   single verified increment and stop. Point it at a durable task list (e.g. `TODO.md`) so progress
   accumulates across passes.

> **Watch for category-circling.** The persona/recode randomness fights *semantic* circling (the agent
> rephrasing the same idea), but NOT *work-category* circling. On a multi-category backlog a cold loop
> reliably drains the **cheapest, lowest-risk section** and starves the high-value ones — each pass
> independently picks the safe win. Counter it in the PROMPT, not the pools: add an explicit **priority
> + rotation rule** ("bias toward sections X/Y; if the last ~3 done-log entries share a `##` section,
> pick a different one").

## Run

PowerShell (Windows, primary):

```powershell
.\ralph.ps1 -Iterations 20            # plain Ralph, 20 passes
.\ralph.ps1 -Random                   # improved Ralph, infinite (Ctrl-C)
.\ralph.ps1 -Random -Iterations 50 -Model claude-opus-4-8
.\ralph-gamedev.ps1 -Iterations 30    # game-dev personas (random always on)
```

Bash (Git Bash / WSL — single loop only):

```bash
./ralph.sh -n 20                       # plain Ralph, 20 passes
./ralph.sh --random                    # improved Ralph, infinite (Ctrl-C)
./ralph.sh --random -n 50 -m claude-opus-4-8
```

Each pass is logged to `logs/iter-NNNN-<timestamp>.log`. **The log file is written when the pass
FINISHES, not live** — so a frozen/absent newest log means a pass is in progress, NOT that the loop
died (see below). `iter-NNNN` numbering RESETS each run. The loop self-commits each dirty pass as
`ralph iter N [<agent>] <timestamp>`, so history is bisectable regardless of agent.

## Is it running? (don't trust the logs)

```powershell
.\ralph-status.ps1     # alive? on which iter? actively computing?
```

Judging liveness from log files gives WRONG answers, because logs land only at pass end and commits
land only at pass boundaries (minutes apart) — a silent gap is normal mid-pass. The reliable signal is
the **process tree**: a live `claude … --dangerously-skip-permissions` (that flag is ralph's
fingerprint; interactive claude never sets it) whose ancestor is the loop's PowerShell.
`ralph-status.ps1` checks that, reports the last `ralph iter N` commit, and double-samples CPU.

## Flags (single loop)

| PowerShell | bash | meaning |
|---|---|---|
| `-Prompt <path>` | `-p <path>` | prompt file (default `PROMPT.md` next to the script) |
| `-Iterations <n>` | `-n <n>` | passes; `0` = forever (default) |
| `-Random` | `--random` | per-iteration anti-circling randomness |
| `-Personas <path>` | `--personas <path>` | persona pool file (default `personas.txt`) |
| `-Nouns <path>` | `--nouns <path>` | recode-decode noun pool (default `nouns.txt`) |
| `-SleepSeconds <n>` | `-s <n>` | pause between passes (PS default 0; bash default 2) |
| `-Model <id>` | `-m <id>` | model override |
| `-Agent claude\|agy` | — | which coding-agent CLI drives each pass (PS only) |
| `-SkipPermissions:$false` | `--no-skip` | disable unattended mode (prompt for perms) |

## Scaling past one loop: the fleet

The **fleet** fans the single loop out. Several loops run at once, each in its own git worktree on its
own branch, each hard-scoped to a disjoint set of files. A **refinery** (a Bors-style merge queue) polls
the lane branches, merges the ones that advanced, runs your gate on the combined result, and **bisects
out anything that regresses** instead of corrupting the trunk. A **planner** keeps the backlog full and
carved into per-lane sections whose open items touch non-overlapping files, so the lanes never collide.

```
  planner ──► TODO.md on trunk ──► lane: api      lane: ui     lane: docs   (parallel worktrees)
 (strong model)                      src/api/      src/ui/      docs/        each: 1 increment → gate → commit
                                         └─────────────┼─────────────┘
                                                       ▼
                                                  refinery  ── poll → merge → gate → bisect-on-red
                                                       ▼
                                                  trunk (always green)
```

Why it holds together:
- **The gate is the whole safety story.** Your build/test command gates both each lane's own pass and
  the refinery's merge. Nothing lands on the trunk that turns it red.
- **Disjoint file ownership beats conflict resolution.** Lanes own non-overlapping files; the planner
  enforces the partition and the refinery flags whatever slips through. No machine-guessed merges.
- **Bail-safe by design.** The refinery never force-pushes; conflicts are aborted + flagged, every merge
  is `--no-ff` so a bad lane peels off with one `reset HEAD^`, and a flagged lane isn't retried until it
  advances. The opposite of the "auto-merge red into main, then force-push to recover" trap.
- **Engine-agnostic.** Lanes can be driven by Claude Code or `agy` (or a mix) — the refinery merges
  commits and doesn't care who produced them. The merge/plan side runs on Claude Code.

Bring-up is the **[SETUP.md](SETUP.md)** checklist (config + trunk branch + prompt + lanes); the
load-bearing rationale, run order, monitoring, and concurrency hardening are in
**[HYBRID.md](HYBRID.md)**. In short:

```powershell
.\start-fleet.ps1 -LaneIterations 3 -RefineryIterations 12   # bounded test run (caps spend)
.\start-fleet.ps1 -WithPlanner                               # open-ended fleet
.\watch-fleet.ps1                                            # live dashboard
```

## What's in `ralph/`

| | |
|---|---|
| `ralph.ps1` / `ralph.sh` | the single Ralph loop (PowerShell / Bash) |
| `ralph-gamedev.*` | same loop, game-dev persona/noun pools, `-Random` always on |
| `ralph-fleet.ps1` | spawn one loop per lane, each in its own worktree |
| `refinery.ps1` | the merge queue: poll → merge → gate → bisect-on-red |
| `plan.ps1` | the planner: keep `TODO.md` full + file-disjoint |
| `integrate.ps1` | one-shot merge of all lanes (instead of the refinery loop) |
| `start-fleet.ps1` / `stop-fleet.ps1` | bring the whole fleet up / down |
| `watch-fleet.ps1` / `ralph-status.ps1` | live dashboard / reliable liveness check |
| `fleet.config.ps1` | **the project knobs** |
| `SETUP.md` / `HYBRID.md` | setup checklist / full fleet rationale |
| `lanes.txt`, `lane-*.md`, `*.example.md` | lane manifest + scoping headers + prompt templates |
| `personas*.txt`, `nouns*.txt` | anti-circling pools |
| `repo-root-AGENTS.md` / `repo-root-CLAUDE.md` | drop-in root files so agents discover the loop |

## ⚠️ For agents testing this loop

`PROMPT.md` is the user's REAL task file and is **gitignored — destroying it is unrecoverable**. NEVER
`copy`/`Write`/`rm` against `PROMPT.md` to smoke-test. Point the loop at a throwaway prompt instead:

```powershell
.\ralph.ps1 -Random -Prompt "$env:TEMP\ralph-test.md" -Iterations 2 -SkipPermissions:$false
```
```bash
./ralph.sh --random -p /tmp/ralph-test.md -n 2 --no-skip
```

## ⚠️ Unattended execution

By default the loop passes `--dangerously-skip-permissions` so the agent runs without stopping to ask.
That means it can edit, run, and delete things in this repo on its own, for as many iterations as you
set. Only run it where you can revert (use git as your undo). Disable with `-SkipPermissions:$false`
(PS) / `--no-skip` (bash) to require approval each tool use.
