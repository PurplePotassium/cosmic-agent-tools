# cosmic-agent-tools

Reusable tooling for running coding agents autonomously, for long stretches, without a human at the
keyboard.

Two tools live here, both built on the same "Ralph Wiggum" loop (a dumb `while` loop around a smart
coding agent):

- the **fleet orchestrator** (`ralph/`) — that loop scaled out across many parallel lanes with a merge
  queue, for grinding a big backlog fast;
- the **Workshop** (`workshop/`) — a single agent with a live web UI, for steering one agent on one
  branch toward a goal you curate by hand.

---

## What it is

A single **Ralph loop** runs a coding agent (`claude -p`, or Antigravity/Gemini `agy`) back-to-back on
the same prompt. Each pass starts with a **fresh context**, reads the repo + your prompt, makes ONE
small verified increment, commits it, and exits. The loop runs it again. Progress accumulates in the
repo (and a `TODO.md`), not in the agent's memory — so it grinds on a task for hours without drifting.

The **fleet** fans that out. Several loops run at once, each in its own git worktree on its own branch,
each hard-scoped to a disjoint set of files. A **refinery** (a Bors-style merge queue) polls the lane
branches, merges the ones that advanced, runs your gate on the combined result, and **bisects out
anything that regresses** instead of corrupting the trunk. A **planner** keeps the backlog full and
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
- **Bail-safe by design.** The refinery never force-pushes; conflicts are aborted + flagged, every
  merge is `--no-ff` so a bad lane peels off with one `reset HEAD^`, and a flagged lane isn't retried
  until it advances. The opposite of the "auto-merge red into main, then force-push to recover" trap.
- **Engine-agnostic.** Lanes can be driven by Claude Code or `agy` (or a mix) — the refinery merges
  commits and doesn't care who produced them. The merge/plan side runs on Claude Code.

The anti-circling trick (optional `-Random`) keeps a long run from looping on the same ideas: each pass
injects a random *persona* ("channel Edgar Allan Poe…") or a *recoding-decoding* word-stem, the
technique from [turso.tech/blog/edgar-allan-poe](https://turso.tech/blog/edgar-allan-poe).

---

## Install into a project

The tool lives in this repo's `ralph/` folder. Drop that folder into the repo you want agents to work on:

```bash
git clone https://github.com/PurplePotassium/cosmic-agent-tools
cp -r cosmic-agent-tools/ralph /path/to/your-project/ralph
```
Windows PowerShell:
```powershell
git clone https://github.com/PurplePotassium/cosmic-agent-tools
Copy-Item -Recurse cosmic-agent-tools\ralph C:\path\to\your-project\ralph
```

Then follow **[`ralph/SETUP.md`](ralph/SETUP.md)** — the bring-up checklist. In short:

1. `git branch fleet-trunk` (the fleet never touches `main` directly).
2. Edit `ralph/fleet.config.ps1` — the only file with project-specifics:
   | knob | what |
   |---|---|
   | `Root` | absolute path to your repo |
   | `Base` | trunk branch (from step 1) |
   | `GateDir` / `GateCmd` | where + how to run your gate (build + tests; must exit 0 on pass) |
   | `Agent` | default lane agent (`claude` or `agy`) |
3. `copy ralph\PROMPT.example.md ralph\PROMPT.md` and write your task.
4. Define lanes in `ralph\lanes.txt` + a `lane-<name>.md` per lane (worked examples included).

Then run:
```powershell
.\ralph\start-fleet.ps1 -LaneIterations 3 -RefineryIterations 12   # bounded test run
.\ralph\start-fleet.ps1 -WithPlanner                               # open-ended fleet
.\ralph\watch-fleet.ps1                                            # live dashboard
.\ralph\ralph.ps1 -Random                                         # just the single loop, no fan-out
```

Tip: copy `ralph/repo-root-AGENTS.md` / `repo-root-CLAUDE.md` to your project root so any agent that
opens the project discovers the fleet on its own.

---

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
| `SETUP.md` / `README.md` / `HYBRID.md` | setup checklist / single-loop docs / full fleet rationale |
| `lanes.txt`, `lane-*.md`, `*.example.md` | lane manifest + scoping headers + prompt templates |
| `personas*.txt`, `nouns*.txt` | anti-circling pools |

---

## Workshop (`workshop/`)

The **single-agent** counterpart to the fleet — "Solo Ralph." One agent at a time (no worktrees, lanes,
refinery, or planner), fresh context each pass, draining an **operator-curated backlog** toward a
north-star `GOAL.md`. It ships a **self-contained, zero-dependency web UI** to watch and steer it live:
edit the goal, queue/reorder tasks, switch the model for the next pass, and see what the current pass is
doing.

Use the Workshop when you want to **hand-curate** what one agent works on next (and watch it); use the
fleet when you want **many** agents grinding a partitioned backlog in parallel.

```powershell
cd workshop
# edit workshop.config.ps1 (Root = your repo, UiPort, ...)
Copy-Item PROMPT.example.md PROMPT.md ; Copy-Item GOAL.example.md GOAL.md
Copy-Item backlog.example.json backlog.json ; Copy-Item completions.example.json completions.json
node ui/server.js            # → http://localhost:4455  (Start the loop from the UI)
```

Full walkthrough + the agent-driver caveats: **[`workshop/README.md`](workshop/README.md)** and
**[`workshop/AGENTS.md`](workshop/AGENTS.md)**.

---

## Skills (`skills/`)

Agent skills — self-contained guides an agent loads on demand. Drop one into your harness's skills
directory to make it available:

| skill | what it does |
|---|---|
| [`2d-game-art-direction`](skills/2d-game-art-direction) | Art-direction decision guide for 2D games — palette, value/contrast, composition, lighting, detail hierarchy, shape language, and the sketch→polish workflow. Static look & readability (for motion, use a separate animation skill). |
| [`giga-research`](skills/giga-research) | Multi-perspective deep research (STORM-inspired) — generate expert personas, formulate & dedupe questions, retrieve sources, then synthesize a structured, fully cited report. |
| [`giga-audit`](skills/giga-audit) | Multi-perspective audit of a plan, PR, or codebase — expert reviewer personas raise risks, each is verified against the actual code, and confirmed issues land in a severity-grouped report with evidence + mitigations. |

Both giga skills are harness-agnostic — they describe capabilities (search / ranged read / file-write) and name per-harness tools only as examples, so they work under Claude Code, Antigravity, or any agent harness.

Install (Claude Code example — adjust the path for your harness):
```bash
cp -r cosmic-agent-tools/skills/2d-game-art-direction ~/.claude/skills/
```
Windows PowerShell:
```powershell
Copy-Item -Recurse cosmic-agent-tools\skills\2d-game-art-direction $env:USERPROFILE\.claude\skills\
```

---

## Platform

The fleet (fan-out / refinery / planner) is **Windows + PowerShell 5.1+**. A Bash flavor of the *single*
loop (`ralph/ralph.sh`) is included; the fan-out is PowerShell-only.

You also need at least one coding-agent CLI on PATH and authenticated:
- **`claude`** (Claude Code) — for the refinery + planner, and optionally lanes.
- **`agy`** (Antigravity CLI, Gemini) — optional, for fast implementation lanes; uses your subscription
  quota, not a metered API key. Setup details in `ralph/HYBRID.md`.

---

## ⚠️ Unattended execution

By default the loops pass `--dangerously-skip-permissions`: agents edit, run, and delete files in your
repo on their own, across multiple worktrees at once, for as many iterations as you set. **Only run this
where you can fully revert via git.** Start bounded (`-LaneIterations`), watch the first rounds, and keep
your gate honest — a weak gate with fast agents corrupts the trunk silently. The refinery never
force-pushes and the trunk is a local branch, so the worst case is "a lane didn't land," not "trunk
broke" — but the worktrees and trunk *are* mutated. Disable unattended mode on the single loop with
`-SkipPermissions:$false` (PowerShell) / `--no-skip` (Bash).

---

## License

[MIT](LICENSE) © 2026 PurplePotassium.
