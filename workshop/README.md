# Workshop — the single-agent loop (a.k.a. "Solo Ralph")

The **Workshop** is the one-agent counterpart to the [fleet](../ralph). Instead of fanning many agents
across worktrees, it runs **one** coding agent (`claude -p` or Antigravity/Gemini `agy`) back-to-back on
the same prompt: each pass starts with a **fresh context**, reads your goal + an operator-curated
backlog, makes **one small verified increment**, commits it, and exits. The loop runs it again.

It ships with a **self-contained web UI** so you can watch and steer it live — edit the north-star goal,
queue/reorder tasks, switch the model for the next pass, and see what the current pass is doing — with
no Launcher, no framework, and no `node_modules`.

```
   GOAL.md  +  backlog.json   ──►   workshop.ps1 (one agent, fresh context)
   (you, via the UI)                  read → 1 increment → verify → commit → exit
        ▲                                              │
        └────────── completions.json / progress.json ◄─┘   (the UI polls these)
```

**Workshop vs. fleet** — no worktrees, lanes, refinery, planner, or trunk-merge. One agent, one branch,
one backlog. If you want parallel agents with a merge queue, use [`../ralph`](../ralph) instead.

---

## Quick start

1. **Edit `workshop.config.ps1`** — the only file with project-specifics:

   | knob | what |
   |---|---|
   | `Root` | absolute path to the repo the agent works in (the loop's git working dir) |
   | `Branch` | branch to commit onto (`''` = current branch; a dedicated one is recommended) |
   | `Agent` / `Model` | first-pass agent (`claude`/`agy`/`auto`) + model |
   | `Personas` / `Nouns` | anti-circling pools (`*-gamedev.txt` for games) |
   | `UiPort` | port the web UI listens on (default `4455`) |
   | `PreviewUrl` / `PreviewPath` | optional live project preview shown in the UI |

2. **Seed the operator files:**
   ```powershell
   Copy-Item PROMPT.example.md        PROMPT.md
   Copy-Item GOAL.example.md          GOAL.md
   Copy-Item backlog.example.json     backlog.json
   Copy-Item completions.example.json completions.json
   ```
   Then edit `PROMPT.md` (the bracketed `[…]` placeholders — your README, your verify command) and
   `GOAL.md` (your north star). The UI's "General Goal" box also writes `GOAL.md`.

3. **Run the UI** and open it:
   ```powershell
   node ui/server.js          # → http://localhost:4455
   ```
   Set the goal, add a few backlog tasks, pick a model, and hit **Start Loop**.

   Prefer the CLI? Skip the UI:
   ```powershell
   ./start-workshop.ps1                 # infinite (stop via stop-workshop.ps1)
   ./start-workshop.ps1 -Iterations 3   # bounded smoke run
   ./stop-workshop.ps1
   ```

---

## What's here

| file | what |
|---|---|
| `workshop.ps1` | the single-agent engine (the loop itself) |
| `start-workshop.ps1` / `stop-workshop.ps1` | launch the loop detached / kill it + the in-flight pass |
| `workshop-status.ps1` | one-shot JSON status the UI polls (process-tree liveness, not log mtime) |
| `workshop.config.ps1` | **the project knobs** |
| `ui/server.js` / `ui/index.html` | the zero-dependency web UI |
| `PROMPT.example.md` / `GOAL.example.md` | per-pass prompt + north-star templates (copy → `.md`) |
| `backlog.example.json` / `completions.example.json` | empty seeds (copy → `.json`) |
| `personas*.txt` / `nouns*.txt` | anti-circling pools |
| `AGENTS.md` | **read before touching agent/model wiring** — how the two drivers behave headless |

The live operator files (`PROMPT.md`, `GOAL.md`, `backlog.json`, `completions.json`, `agent.json`,
`progress.json`, `logs/`) are gitignored — they're your machine's state, not part of the tool.

---

## How the pieces talk

- **`agent.json`** `{agent,model}` is the live selection. The loop re-reads it at the **top of each
  pass** (`-AgentControlFile`), so the UI's model switch lands on the **next** pass with no restart.
  `auto` classifies the top backlog item per pass (light→agy, heavy→opus, else→sonnet).
- **`backlog.json`** is drained **top-first**; the agent removes the item it finished and appends to
  **`completions.json`**. Both use fresh read-modify-write so a UI edit mid-pass isn't clobbered.
- **`progress.json`** is the agent's self-report (`phase/task/plan/note`), written at pass start and
  end. It is the **only window into an `agy` pass**, whose stdout is uncapturable headless — see
  [`AGENTS.md`](AGENTS.md).
- **Liveness** comes from the **process tree + CPU + git dirty tree** (`workshop-status.ps1`), never the
  log mtime — a frozen log is usually mid-pass, not stopped.

---

## ⚠️ Unattended execution

By default the loop passes `--dangerously-skip-permissions`: the agent edits, runs, and deletes files in
`Root` on its own, for as many iterations as you set. **Only run this where you can fully revert via
git.** Start bounded (`-Iterations`), watch the first passes, and commit onto a branch you can reset.
Disable unattended mode with `-SkipPermissions:$false` (claude only).
