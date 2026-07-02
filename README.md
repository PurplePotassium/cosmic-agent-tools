# cosmic-agent-tools

A mono-repo of tools and skills for running coding agents **autonomously** — unattended, for long
stretches, without a human at the keyboard.

Everything here is built on one core idea: the **"Ralph Wiggum" loop** — a dumb `while` loop around a
smart coding agent. Each pass starts with a **fresh context**, reads the repo + your prompt, makes ONE
small verified increment, commits it, and exits. The loop runs it again. Progress accumulates in the
repo (and a task list), not in the agent's memory — so it grinds on a task for hours without drifting.

---

## What's in this repo

| directory | what it is | platform | start here |
|---|---|---|---|
| **[`ralph/`](ralph/)** | **Ralph loops + fleet orchestrator** — the single loop, plus a fan-out that runs many loops in parallel git worktrees behind a merge queue and a planner. For grinding a big, partitioned backlog fast. | PowerShell (single loop also Bash) | [`ralph/README.md`](ralph/README.md) |
| **[`workshop/`](workshop/)** | **Workshop — "Solo Ralph"** — one agent on one branch with a live web dashboard, draining a backlog you curate by hand toward a north-star goal. A single self-contained Go binary. | Go binary (Windows / macOS / Linux) | [`workshop/README.md`](workshop/README.md) |
| **[`skills/`](skills/)** | **Agent skills** — self-contained guides an agent loads on demand (art direction, deep research, audit). Harness-agnostic drop-ins. | any agent harness | [Skills](#skills) below |

`ralph/` and `workshop/` are two takes on the same loop: **ralph** fans it out across **many** agents
grinding a partitioned backlog in parallel; **workshop** keeps **one** agent on a hand-curated backlog
you watch live. `skills/` is independent of both — drop-in guides for any agent harness.

**Requirements (both tools):** at least one coding-agent CLI on PATH and authenticated —
**`claude`** (Claude Code) and/or **`agy`** (Antigravity CLI, Gemini). The ralph refinery + planner run
on Claude Code; implementation lanes and the Workshop can use either.

---

## `ralph/` — fleet orchestrator

The single Ralph loop and a **fleet** that fans it out. Several loops run at once, each in its own git
worktree on its own branch, each hard-scoped to a disjoint set of files. A **refinery** (a Bors-style
merge queue) polls the lane branches, merges the ones that advanced, runs your gate on the combined
result, and **bisects out anything that regresses** instead of corrupting the trunk. A **planner** keeps
the backlog carved into per-lane sections whose open items touch non-overlapping files, so lanes never
collide. Engine-agnostic — lanes run on Claude Code or `agy`, or a mix; the refinery merges commits and
doesn't care who produced them.

Use the fleet when you want **many** agents grinding a partitioned backlog in parallel.

→ **[`ralph/README.md`](ralph/README.md)** · bring-up checklist [`SETUP.md`](ralph/SETUP.md) · full
fleet rationale [`HYBRID.md`](ralph/HYBRID.md)

<<<<<<< Updated upstream
The **single-agent** counterpart to the fleet — "Solo Ralph." One agent at a time (no worktrees, lanes,
refinery, or planner), fresh context each pass, draining an **operator-curated backlog** toward a
north-star `GOAL.md`. It ships a **self-contained, zero-dependency web UI** to watch and steer it live:
edit the goal, queue/reorder tasks, switch the model for the next pass, and see what the current pass is
doing.
=======
## `workshop/` — single-agent loop with a live UI
>>>>>>> Stashed changes

The **single-agent** counterpart to the fleet. One agent at a time (no worktrees, lanes, refinery, or
planner), fresh context each pass, draining an **operator-curated backlog** toward a north-star `GOAL.md`.
It ships as **one cross-platform Go binary** with an embedded web UI (Syncthing-style: Go backend +
baked-in React SPA, all local). Run `workshop` in any git repo and a browser opens to a live dashboard:
edit the goal/prompt, queue/reorder tasks, switch the model for the next pass, and watch the current pass
over SSE.

Use the Workshop when you want to **hand-curate** what one agent works on next — and watch it.

```powershell
cd workshop
# edit workshop.config.ps1 (Root = your repo, UiPort, ...)
Copy-Item PROMPT.example.md PROMPT.md ; Copy-Item GOAL.example.md GOAL.md
Copy-Item backlog.example.json backlog.json ; Copy-Item completions.example.json completions.json
node ui/server.js            # → http://localhost:4455  (Start the loop from the UI)
```

<<<<<<< Updated upstream
Full walkthrough + the agent-driver caveats: **[`workshop/README.md`](workshop/README.md)** and
**[`workshop/AGENTS.md`](workshop/AGENTS.md)**.
=======
→ **[`workshop/README.md`](workshop/README.md)** · agent-driver caveats [`workshop/AGENTS.md`](workshop/AGENTS.md)
>>>>>>> Stashed changes

---

## Skills

Agent skills — self-contained guides an agent loads on demand. Drop one into your harness's skills
directory to make it available:

| skill | what it does |
|---|---|
| [`2d-game-art-direction`](skills/2d-game-art-direction) | Art-direction decision guide for 2D games — palette, value/contrast, composition, lighting, detail hierarchy, shape language, and the sketch→polish workflow. Static look & readability (for motion, use a separate animation skill). |
| [`giga-research`](skills/giga-research) | Multi-perspective deep research (STORM-inspired) — generate expert personas, formulate & dedupe questions, retrieve sources, then synthesize a structured, fully cited report. |
| [`giga-audit`](skills/giga-audit) | Multi-perspective audit of a plan, PR, or codebase — expert reviewer personas raise risks, each is verified against the actual code, and confirmed issues land in a severity-grouped report with evidence + mitigations. |

Both giga skills are harness-agnostic — they describe capabilities (search / ranged read / file-write)
and name per-harness tools only as examples, so they work under Claude Code, Antigravity, or any agent
harness.

Install (Claude Code example — adjust the path for your harness):
```bash
cp -r cosmic-agent-tools/skills/2d-game-art-direction ~/.claude/skills/
```
Windows PowerShell:
```powershell
Copy-Item -Recurse cosmic-agent-tools\skills\2d-game-art-direction $env:USERPROFILE\.claude\skills\
```

---

## The shared idea — the Ralph Wiggum loop

Both tools run the same loop: a coding agent (`claude -p`, or `agy`) is invoked back-to-back on the same
prompt, each pass cold-starting from the repo state rather than a growing conversation. Because every
increment is committed and gated, the repo *is* the memory — the loop can run for hours without the
context drift that kills a single long session.

An optional **anti-circling** mode keeps a long run from looping on the same ideas: each pass injects a
random *persona* ("channel Edgar Allan Poe…") or a *recoding-decoding* word-stem — the technique from
[turso.tech/blog/edgar-allan-poe](https://turso.tech/blog/edgar-allan-poe). Both `ralph/` and `workshop/`
implement it; see their READMEs for the pools and flags.

---

## ⚠️ Unattended execution

By default the loops pass `--dangerously-skip-permissions`: agents edit, run, and delete files in your
repo on their own — in the fleet's case across multiple worktrees at once, for as many iterations as you
set. **Only run this where you can fully revert via git.** Start bounded (a small iteration count), watch
the first rounds, and keep your gate honest — a weak gate with fast agents corrupts the trunk silently.
Each tool documents how to disable unattended mode and how far the blast radius reaches:

- **fleet** — the refinery never force-pushes and the trunk is a local branch, so the worst case is "a
  lane didn't land," not "trunk broke." Disable per-loop with `-SkipPermissions:$false` / `--no-skip`.
  See [`ralph/README.md`](ralph/README.md).
- **workshop** — the server binds `127.0.0.1` only (it spawns agent commands — never expose it without
  auth); commit onto a branch you can reset (`--branch`). See [`workshop/README.md`](workshop/README.md).

---

## License

[MIT](LICENSE) © 2026 PurplePotassium.
