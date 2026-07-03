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
| **[`workshop/`](workshop/)** | **Workshop** — the loop as a mature app: a single Go binary with an embedded dashboard. One pipeline by default; scales to multiple simultaneous pipelines in git worktrees behind a gated merge queue, with per-task-type model/effort routing and agent-resolved merge conflicts. | Go binary (Windows / macOS / Linux) | [`workshop/README.md`](workshop/README.md) |
| **[`ralph/`](ralph/)** | **Ralph loops + fleet orchestrator** — the original PowerShell scripts: the single loop, plus a fan-out that runs many loops in parallel worktrees behind a merge queue and a planner. | PowerShell (single loop also Bash) | [`ralph/README.md`](ralph/README.md) |
| **[`cosmo-canyon/`](cosmo-canyon/)** | **Cosmo Canyon** — a Claude Code–orchestrated take on the loop: a per-tick `claude -p` cycle (opus planner + hybrid `agy`/Claude worker + deterministic gate/commit) that builds a fresh browser game from a Ready-Spec set you author in an Asset Browser. The generated game lives in its own nested repo and is not tracked here. | Node standalone app (`server.mjs`, :7788) | [`cosmo-canyon/AGENTS.md`](cosmo-canyon/AGENTS.md) |
| **[`skills/`](skills/)** | **Agent skills** — self-contained guides an agent loads on demand (art direction, deep research, audit). Harness-agnostic drop-ins. | any agent harness | [Skills](#skills) below |

`workshop/` is the successor to the PowerShell tooling: `ralph/`'s fleet ideas (worktrees, merge
queue, bisect-on-red) and the original solo Workshop are both built into one binary, configured per
repo with a checked-in `.workshop/config.toml`. The `ralph/` scripts remain for reference and for
PowerShell-native workflows. `cosmo-canyon/` is a separate, self-contained take on the loop — building
a browser game from a spec set rather than grinding a backlog. `skills/` is independent of all of them.

**Requirements:** at least one coding-agent CLI on PATH and authenticated — **`claude`** (Claude
Code) and/or **`agy`** (Antigravity CLI, Gemini).

---

## `workshop/` — the loop as one binary

```
cd your-repo
workshop init      # optional — scaffolds .workshop/ (config.toml + GOAL.md, both versioned)
workshop           # dashboard opens; set the goal, queue tasks, press nothing — it's already running
```

- **One shared backlog + an exclusive backlog per pipeline**; pipelines claim their own tasks first,
  then type-matching shared tasks. Tasks can be typed (`art`, `code`, `audio`, …), pinned to an exact
  agent/model/effort, or auto-classified.
- **Task-type routing**: `[types.art] agent="agy"`, `[types.code] agent="claude" effort="high"`, etc.
- **Worktrees + merge queue** turn on automatically with a second pipeline: lanes land on your trunk
  through `--no-ff` merges gated by your verify command, with regressions bisected out. Conflicts
  become agent tasks (route `merge-conflict` to enable) resolved in ephemeral worktrees and verified
  by the engine before landing.
- **Drivers**: `claude` (streamed live) and `agy` (blind headless — self-report only; see
  [`workshop/AGENTS.md`](workshop/AGENTS.md)); more are pluggable.

→ **[`workshop/README.md`](workshop/README.md)**

## `ralph/` — fleet orchestrator (PowerShell)

The single Ralph loop and a **fleet** that fans it out: several loops at once, each in its own git
worktree on its own branch, hard-scoped to disjoint files, behind a Bors-style **refinery** (merge
queue) and a **planner** that keeps the backlog partitioned. Engine-agnostic lanes (Claude Code or
`agy`).

→ **[`ralph/README.md`](ralph/README.md)** · bring-up checklist [`SETUP.md`](ralph/SETUP.md) · fleet
rationale [`HYBRID.md`](ralph/HYBRID.md)

---

## Skills

Agent skills — self-contained guides an agent loads on demand. Drop one into your harness's skills
directory to make it available:

| skill | what it does |
|---|---|
| [`2d-game-art-direction`](skills/2d-game-art-direction) | Art-direction decision guide for 2D games — palette, value/contrast, composition, lighting, detail hierarchy, shape language, and the sketch→polish workflow. |
| [`giga-research`](skills/giga-research) | Multi-perspective deep research (STORM-inspired) — expert personas, question dedup, retrieval, cited synthesis. |
| [`giga-audit`](skills/giga-audit) | Multi-perspective audit of a plan, PR, or codebase — reviewer personas raise risks, each verified against the code, confirmed issues land in a severity-grouped report. |
| [`spark-research`](skills/spark-research) | Systematic framework for researching, verifying, backtesting, and vetting non-ML stock trading strategies for retail execution. |

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

Both tools run the same loop: a coding agent (`claude -p`, or `agy`) is invoked back-to-back on the
same prompt, each pass cold-starting from the repo state rather than a growing conversation. Because
every increment is committed and gated, the repo *is* the memory — the loop can run for hours without
the context drift that kills a single long session.

An optional **anti-circling** mode keeps a long run from looping on the same ideas: each pass injects
a random *persona* ("channel Edgar Allan Poe…") or a *recoding-decoding* word-stem — the technique
from [turso.tech/blog/edgar-allan-poe](https://turso.tech/blog/edgar-allan-poe). Both tools implement
it; the Workshop calls it `[spice]`.

---

## ⚠️ Unattended execution

By default the loops pass `--dangerously-skip-permissions`: agents edit, run, and delete files in
your repo on their own — across multiple worktrees at once, for as many iterations as you set. **Only
run this where you can fully revert via git.** Start bounded, watch the first rounds, and keep your
gate honest — a weak gate with fast agents corrupts the trunk silently.

- **workshop** — the server binds `127.0.0.1` only and mutations need a session token; the merge
  queue never force-pushes and skips rounds when you have uncommitted edits. Disable unattended mode
  with `[safety] skip_permissions = false`. See [`workshop/README.md`](workshop/README.md).
- **fleet** — the refinery never force-pushes and the trunk is a local branch. Disable per-loop with
  `-SkipPermissions:$false` / `--no-skip`. See [`ralph/README.md`](ralph/README.md).

---

## License

[MIT](LICENSE) © 2026 PurplePotassium.
