# Workshop — a single-agent coding loop as one installable binary

The **Workshop** runs **one** coding agent (`claude -p` or Antigravity/Gemini `agy`)
back-to-back on the same prompt: each pass starts with a **fresh context**, reads your
north-star goal + an operator-curated backlog, makes **one small verified increment**,
commits it, and exits. The loop runs it again. The "Ralph Wiggum" technique: a dumb
while-loop around a smart agent.

It ships as **one self-contained Go binary** with an embedded web dashboard — the
reference silhouette is **Syncthing**: Go backend + embedded UI + single binary, all
local. You install nothing else: run `workshop` in any git repo, a browser opens to a
live dashboard, and the loop runs.

```
  workshop            ← single binary; brew / scoop / curl install, no runtime needed
     │  starts, opens http://127.0.0.1:4455
     ▼
  Local service (Go: net/http + chi)
    REST /api/*   commands: project CRUD, start/stop, backlog/goal/prompt, model
    SSE  /events  PUSH log/progress/commit/status, tagged by projectId (no polling)
    Supervisor → one Engine loop per ACTIVE project (goroutine + context; no worktrees)
    Store (modernc SQLite, pure-Go) + per-project state dir OUTSIDE the repo tree
    Embedded React SPA (web/dist via go:embed)
```

**Workshop vs. the fleet** ([`../ralph`](../ralph)) — no worktrees, lanes, refinery,
planner, or trunk-merge. One agent, one working directory, one backlog. Multiple agents
run only by running **multiple projects**, each bound to a **distinct working directory**
— so concurrent agents never contend on the same files. "Project" is the first-class unit.

---

## Install

Once a release is cut (see [Distribution](#distribution)):

```sh
brew install PurplePotassium/tap/workshop      # macOS / Linux
scoop bucket add pp https://github.com/PurplePotassium/scoop-bucket; scoop install workshop   # Windows
```

Or grab a binary from GitHub Releases, or build from source (below).

---

## Quick start

```sh
cd /path/to/your/repo      # any git repo
workshop                   # opens http://127.0.0.1:4455
```

On first launch Workshop:
- detects the current directory as a **project** (reusing it on later runs),
- scaffolds `GOAL.md` + `PROMPT.md` in a **per-project state dir outside your repo**
  (so the loop's `git add -A` never commits Workshop's own state),
- opens the dashboard.

In the dashboard: set the **goal**, add a few **backlog** tasks, pick a **model**, and
hit **Start**. Watch the live log, commit feed, and the agent's current-pass self-report.

Prefer a bounded smoke run from the CLI (no browser)?

```sh
workshop --iterations 2    # drive the detected project 2 passes, then exit
```

Key flags (all optional — zero-config works): `--port`, `--repo`, `--agent`, `--model`,
`--branch`, `--personas gamedev|plain`, `--sleep`, `--max-concurrent`, `--open=false`,
`--base-dir`. Settings also load from `WORKSHOP_*` env vars and an optional
`<base-dir>/workshop.json`. `workshop --version` prints build info.

### Where state lives

Everything Workshop writes lives in an OS data dir keyed to each repo — **never in your
repo tree**:

| OS | base dir |
|---|---|
| Windows | `%LOCALAPPDATA%\workshop` |
| macOS | `~/Library/Application Support/workshop` |
| Linux | `$XDG_STATE_HOME/workshop` (or `~/.local/state/workshop`) |

`<base>/workshop.db` is the SQLite registry (projects, backlog, completions, run/iteration
history). `<base>/projects/<slug>/` holds each project's `GOAL.md`, `PROMPT.md`, `logs/`,
and the materialized `backlog.json` / `completions.json` / `progress.json` the agent reads.

---

## How the pieces talk

- **The engine materializes → reconciles.** Before each pass it writes `backlog.json` /
  `completions.json` from the DB (the agent's file-based contract); after the pass it reads
  them back with a **diff**, so the agent's edits (drain the top item, append follow-ups)
  and any UI edit that landed in the DB **mid-pass** are both preserved. This replaces the
  old JSON read-modify-write races.
- **Live selection is re-read each pass.** Switching the model in the UI applies to the
  **next** pass — the in-flight pass keeps its model, no restart. `auto` classifies the top
  backlog item per pass: light/presentation → agy, heavy/structural → claude/opus, else
  claude/sonnet.
- **`progress.json`** is the agent's self-report (`phase/task/plan/note`), the **only
  window into an `agy` pass** (whose stdout is uncapturable headless — see
  [`AGENTS.md`](AGENTS.md)).
- **SSE pushes everything.** Log lines, commits, progress, and status ticks stream over
  `/events` — the dashboard never polls.
- **Anti-circling** injects a persona or recoding-decoding lens each pass so a long run
  keeps exploring instead of looping on the same idea.

---

## Build from source

Requires Go 1.26+ and Node 20+ (Node only to *build* the embedded UI).

```sh
cd workshop
npm --prefix web ci && npm --prefix web run build   # → web/dist (embedded)
go build -o workshop ./cmd/workshop
go test ./...
```

The module layout:

```
cmd/workshop/       CLI entry: flags, config, detect project, start server, open browser
internal/supervisor one engine loop per active project; bounded concurrency; start/stop
internal/engine     per-project loop (the port of the old workshop.ps1)
internal/agent      claude/agy driver abstraction + auto-routing classifier
internal/project    project model + state-dir/slug derivation + scaffold
internal/server     chi REST + SSE broker; serves the embedded SPA
internal/store      modernc.org/sqlite registry + history
internal/gitx       git CLI wrapper (commit, status, feed, stale-lock cleanup)
internal/procctl    cross-platform process-group kill (build-tagged)
internal/events     SSE fan-out broker
internal/config     layered config (defaults → file → env → flags)
internal/assets     embedded default GOAL/PROMPT templates + persona/noun pools
web/                Vite + React source → web/dist (embedded via go:embed)
```

---

## Distribution

`goreleaser` (see [`../.goreleaser.yaml`](../.goreleaser.yaml)) builds every platform from
one Linux CI job (all deps are pure-Go, so `CGO_ENABLED=0` cross-compiles cleanly) and
publishes on a `v*` git tag via [`release.yml`](../.github/workflows/release.yml):
cross-platform archives, `.deb`/`.rpm`, a Homebrew tap, and a Scoop bucket. **No Docker
image** — the tool needs the *host's* repo, agent CLIs, and local credentials, so a
container adds friction without fitting the local-first model.

To enable the Homebrew/Scoop publishers, create `PurplePotassium/homebrew-tap` and
`PurplePotassium/scoop-bucket` repos and add a `HOMEBREW_TAP_GITHUB_TOKEN` secret (a PAT
with `repo` scope).

---

## ⚠️ Unattended execution

By default the loop passes `--dangerously-skip-permissions`: the agent edits, runs, and
deletes files in your repo on its own. **Only run this where you can fully revert via git.**
Start bounded (`--iterations`), watch the first passes, and commit onto a branch you can
reset (`--branch`). The server binds `127.0.0.1` only — it spawns agent commands and must
never be exposed without auth.
