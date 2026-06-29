# Ralph loops + fleet orchestrator (standalone)

A dumb `while` loop around a smart agent. Each iteration runs a coding agent (`claude -p`, or `agy`)
with a **fresh context**, reads the repo + your prompt, makes one increment of progress, leaves notes,
commits, and exits. The loop runs it again. Named after the "Ralph Wiggum" technique — let a coding
agent grind on a task autonomously for a long time.

This package is **project-agnostic**. New here? Read **[SETUP.md](SETUP.md)** first — it's the bring-up
checklist (config + prompt + lanes). The single knob file is `fleet.config.ps1`.

> **Scaling past one loop?** See **[HYBRID.md](HYBRID.md)** — many lanes run in parallel worktrees,
> a **refinery** merges their branches under a gate, and a **planner** keeps the backlog file-disjoint.
> Lanes are declared in `lanes.txt`; fan-out via `ralph-fleet.ps1`; one-command launch via
> `start-fleet.ps1`.

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
