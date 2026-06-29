# Fleet Orchestrator — standalone setup

A reusable **parallel-Ralph fleet**: many coding-agent loops grind on one repo in parallel, each in
its own git worktree with a hard file scope, while a **refinery** (Bors-style merge queue) integrates
their branches under a gate and a **planner** keeps the backlog carved into non-overlapping lanes.
Engine-agnostic — drive lanes with Claude Code (`claude`) and/or Antigravity/Gemini (`agy`).

Read `README.md` (single-loop Ralph + flags) and `HYBRID.md` (the full fleet rationale) for depth.
This file is the **bring-up checklist** for a NEW project.

---

## 0. Prerequisites
- **Windows + PowerShell 5.1+** (the fleet scripts are PowerShell). A Bash flavor of the *single* loop
  is included (`ralph.sh`), but the fan-out/refinery/planner are PowerShell-only.
- **git** on PATH, and your project is a git repo.
- **At least one coding-agent CLI** on PATH and authenticated:
  - `claude` (Claude Code) — for the refinery + planner, and optionally lanes.
  - `agy` (Antigravity CLI, Gemini) — optional, for fast implementation lanes. Signs in via your
    Antigravity subscription; do NOT set `GEMINI_API_KEY`. See HYBRID.md §"One-time setup".

## 1. Drop the package into your repo
Unzip so these files live at **`<your-repo>\ralph\`** (the scripts assume that folder name):

```
<your-repo>\ralph\  ← all the .ps1/.sh/.md/.txt files from this zip
```

Then **copy the repo-root pointer up to your repo root** so any agent that opens this project discovers
the fleet on its own (skip if you'll always tell the agent "read ralph/SETUP.md" yourself):
```powershell
copy ralph\repo-root-AGENTS.md AGENTS.md     # Antigravity reads this
copy ralph\repo-root-CLAUDE.md CLAUDE.md     # Claude Code reads this
# already have an AGENTS.md / CLAUDE.md? append the block from the pointer file instead of overwriting.
```

Commit them (lanes are git worktrees and need `ralph/` present on the trunk):
```powershell
git add ralph AGENTS.md CLAUDE.md && git commit -m "add fleet orchestrator"
```

## 2. Create the trunk branch
The fleet never touches `main` directly. Lanes fork from a trunk; the refinery merges back into it.
```powershell
git branch fleet-trunk        # or any name; put it in fleet.config.ps1 below
```

## 3. Edit `ralph\fleet.config.ps1` — the 5 knobs
This is the ONLY file with project-specifics. Set:
| key | what | example |
|---|---|---|
| `Root` | absolute path to your repo's main worktree | `'C:\dev\myapp'` |
| `Base` | trunk branch from step 2 | `'fleet-trunk'` |
| `GateDir` | dir (relative to Root) where the gate runs | `'.'` or `'app'` |
| `GateCmd` | **the gate** — build+test, MUST exit 0 on pass | `'npm test'` |
| `Agent` | default lane agent | `'claude'` or `'agy'` |

The **gate is the entire safety story** — the refinery only lands changes that keep it green, and it
bisects out whatever turns it red. Make it real: build + tests + ideally a smoke run. A weak gate with
fast agents corrupts the trunk silently.

## 4. Write the task prompt — `ralph\PROMPT.md`
```powershell
copy ralph\PROMPT.example.md ralph\PROMPT.md   # then edit
```
Write it so ONE cold agent (no memory of prior passes) makes a single verified increment and stops.
Point it at a durable `TODO.md` so progress accumulates. **`PROMPT.md` is gitignored** — it's per-repo.

## 5. Define your lanes — `ralph\lanes.txt` + `lane-<name>.md`
Edit `lanes.txt` (one lane per line). For each lane, copy `lane-template.md` to `lane-<name>.md` and
fill in the owned files + TODO sections. **THE ONE RULE: lanes must own DISJOINT files** — two lanes
editing the same file is the failure mode. `lane-api.md` / `lane-ui.md` are worked examples.

Pick lane count to match your file partition, not ambition — a small codebase realistically supports
~3–5 clean lanes. Each lane needs its own non-overlapping slice of the source tree.

## 6. (Recommended) union-merge the trail docs
So parallel appends to log docs don't conflict, copy the lines from `gitattributes.sample` into your
repo-root `.gitattributes` (adjust paths). Commit it on the trunk.

## 7. (Optional) the planner — `ralph\PLAN-PROMPT.md`
```powershell
copy ralph\PLAN-PROMPT.example.md ralph\PLAN-PROMPT.md   # edit the lane list + read-first paths
```
The planner (strong model) keeps `TODO.md` full and file-disjoint. It self-commits its edits, so it
works with or without a Claude-Code auto-commit-on-Stop hook. Skip it for a short bounded run.

---

## Run

**Smoke-test the single loop first** (cheap, proves the agent + gate work) — point it at a THROWAWAY
prompt so you don't touch your real `PROMPT.md`:
```powershell
.\ralph\ralph.ps1 -Prompt "$env:TEMP\ralph-test.md" -Iterations 1 -SkipPermissions:$false
```

**Bounded fleet test** (safe first real run — caps spend):
```powershell
.\ralph\start-fleet.ps1 -LaneIterations 3 -RefineryIterations 12 -RefineryInterval 45
```

**Open-ended fleet** (lanes + refinery + planner every 30 min; Ctrl-C / stop-fleet to stop):
```powershell
.\ralph\start-fleet.ps1 -WithPlanner
```

**Monitor / control:**
```powershell
.\ralph\watch-fleet.ps1      # live dashboard (read-only)
.\ralph\ralph-status.ps1     # is a loop alive + on which iter (process-based, reliable)
.\ralph\stop-fleet.ps1       # kill all fleet windows + workers, restore autostash
```

Step-by-step (instead of start-fleet): `plan.ps1` → `ralph-fleet.ps1 -Launch` → `refinery.ps1` in a
separate window. One-shot merge instead of the loop: `integrate.ps1`.

---

## ⚠️ Unattended execution — read this
By default every loop passes `--dangerously-skip-permissions`: the agents edit, run, and delete in
your repo on their own for as many iterations as you set, across multiple worktrees at once. Only run
this in a repo you can fully revert. The refinery never force-pushes and the trunk is a local branch,
so the worst case is "a lane didn't land," not "trunk broke" — but the lane worktrees and your trunk
*are* mutated. Start bounded (`-LaneIterations`), watch the first rounds, and keep your gate honest.

Disable unattended mode on the single loop with `-SkipPermissions:$false` (PS) / `--no-skip` (bash).

## What's project-specific vs reusable
- **Reusable, don't edit:** all `.ps1`/`.sh` engine scripts, `personas*.txt`, `nouns*.txt`,
  `lane-template.md`, the `.example.md` files, `repo-root-*.md` pointers.
- **Yours to edit:** `fleet.config.ps1`, `PROMPT.md`, `PLAN-PROMPT.md`, `lanes.txt`, your `lane-*.md`
  headers, your repo `.gitattributes`.

## Cleanup (remove the fleet's worktrees/branches when done)
```powershell
git worktree list                          # see the <repo>-wt-* lane worktrees
git worktree remove <repo>-wt-<lane>       # per lane (add --force if dirty)
git branch -D ralph-<lane>                 # delete the lane branch
```
