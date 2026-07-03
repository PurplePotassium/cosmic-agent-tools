# Workshop

Autonomous coding-agent loops for any git repository, with a live dashboard.

One self-contained binary. Point it at a repo and it runs the **Ralph loop**:
a coding agent (Claude Code, Antigravity/Gemini, pluggable others) is invoked
back-to-back — each pass cold-starts with fresh context, reads your GOAL and
a curated backlog, makes ONE small verified increment, and exits; the engine
commits it and goes again. The repo is the memory, so it grinds for hours
without context drift.

Scale up when you want to: multiple **pipelines** work simultaneously, each
with its own agent/model/effort, each in its own **git worktree**, integrated
back to your trunk by a **gated merge queue** that bisects out regressions —
and merge conflicts can be resolved by an agent you route them to.

```
cd your-repo
workshop init      # optional — scaffolds .workshop/ (config + GOAL)
workshop           # dashboard opens (pipelines start stopped); set the goal, add tasks, press resume
```

---

## Install

- **From source** (Go 1.26+): `go install github.com/PurplePotassium/cosmic-agent-tools/workshop/cmd/workshop@latest`
- **Release binaries**: download from GitHub Releases and drop on your PATH
  (`%LOCALAPPDATA%\Programs\workshop\` is a good spot on Windows).

You also need at least one agent CLI on PATH and authenticated:
**`claude`** (Claude Code) and/or **`agy`** (Antigravity CLI / Gemini).
`workshop doctor` checks everything.

There is no Node, no npm, no scripts to copy: the dashboard is embedded in
the binary, and `go build ./cmd/workshop` is the entire build.

## The layout: intent vs. state

| where | what | versioned? |
|---|---|---|
| `<repo>/.workshop/` | `config.toml`, `GOAL.md`, `prompts/` fragments | **yes — commit it, share it with your team** |
| OS state dir¹ | task DB, pass logs, per-pipeline agent state, `server.json` | no — machine-local |

¹ `%LOCALAPPDATA%\workshop\projects\<name>-<hash>` on Windows; `workshop path` prints every resolved location.

The engine commits `.workshop/**` edits as their own `workshop: goal/config
update` commits, so goal history is project history.

## Commands

```
workshop            start server + dashboard; pipelines start STOPPED — resume to run (--start-running to auto-run; Ctrl+C exits)
workshop init       scaffold .workshop/  (--game for gamedev spice pools, --pipelines a,b)
workshop run        headless bounded run: --iterations N (default 3), --until-drained, --timeout 45m
workshop task       add | list | tag | pin | mv | rm     (see below)
workshop status     one-shot snapshot (--json)
workshop stop       stop the running server gracefully (--force kills a hung engine's process tree)
workshop doctor     environment health check (--json)
workshop path       print resolved dirs and config files
workshop migrate    import GOAL/PROMPT/backlog from the old PowerShell workshop (--from <dir>)
workshop version
```

Exit codes for `run`: `0` ok · `1` halted/failed/interrupted · `2` config error.

## Tasks and backlogs

There is one **shared backlog** plus an **exclusive backlog per pipeline**.
A pipeline always drains its own backlog first (any task type — explicit
assignment wins), then the shared backlog filtered by its `types` (unless
`drain_main = false`). No pipeline ever touches another pipeline's backlog.
Agents see ALL tasks across every backlog before proposing new ones, so
duplicates get filtered.

```
workshop task add "repaint the title screen" --type art          # shared backlog
workshop task add "fix the save crash" --backlog code --first    # code's own backlog, top
workshop task pin <id> claude:claude-opus-4-8:high               # this task, this bundle
workshop task tag <id> audio                                     # retype ('-' = re-classify)
workshop task mv <id> --to shared                                # move between backlogs
```

Untyped tasks are auto-classified (keyword heuristic against your configured
type vocabulary) so type-filtered pipelines can claim them.

## Configuration

Everything lives in `.workshop/config.toml`; **every key is optional** — an
absent/empty file runs one `main` pipeline (claude, all types, no worktrees).
Layering, lowest to highest: built-ins → user-global
(`%APPDATA%\workshop\config.toml`) → repo file → `WORKSHOP_*` env → CLI flags.

```toml
[project]
name   = "space-game"
trunk  = "main"              # branch pipelines fork from / merge into (default: current)
verify = "npm test"          # THE GATE. exit 0 = pass. strongly recommended.

[git]
worktrees = "auto"           # auto | on | off — auto turns worktrees on when >1 pipeline

[safety]
max_concurrent   = 2         # simultaneous agent passes
breaker_failures = 5         # consecutive failed passes -> pipeline halts
wedge_minutes    = 35        # in-flight pass older than this is KILLED (the old tool only
                             # flagged "wedged?"). Keep it above agy's 30m --print-timeout.
sleep_seconds    = 0         # pause between passes of one pipeline

[spice]                      # anti-circling: a persona/word-prime per pass
personas = "gamedev"         # general | gamedev | path/to/pool.txt
nouns    = "gamedev"

[server]                     # the `workshop up` / dashboard experience only
start_stopped = true         # come up with every pipeline stopped; resume from the dashboard
                             # (default true; `up --start-running` overrides for one launch)
open_browser  = true         # open the dashboard on launch (default true)

# ---- task-type routing: type -> {agent, model, effort} --------------------
# Precedence per task: pin > live dashboard override > this table > pipeline
# bundle. The types code/tests/docs/art/audio/merge-conflict are built in with
# EMPTY bundles — the classifier types tasks out of the box and agent-resolved
# merge conflicts are enabled by default; entries here override per type.
[types.code]
agent  = "claude"
model  = "claude-opus-4-8"
effort = "high"              # low|medium|high|xhigh|max — ignored if the agent has no effort knob

[types.art]
agent = "agy"                # blind headless: self-report only (see AGENTS.md)
model = "gemini-3-flash"     # agy model ids fail SILENTLY when wrong — don't guess

[types.merge-conflict]       # built-in route; override to pick a stronger resolver
agent  = "claude"
model  = "claude-opus-4-8"
effort = "high"

# model is validated against a curated family list per agent — claude:
# sonnet/fable/opus/haiku, agy: gemini — and just WARNS (never blocks) on a
# mismatch. Off-list on purpose (a proxy alias, a brand-new id)? Whitelist it:
# [agents.claude]
# extra_models = ["my-internal-proxy-model"]

# ---- pipelines: parallel worker lanes --------------------------------------
[[pipelines]]
name   = "code"
types  = ["code", "tests", "docs", "merge-conflict"]
agent  = "claude"
effort = "high"

[[pipelines]]
name       = "art"
types      = ["art", "audio"]
agent      = "agy"
model      = "gemini-3-flash"
invent     = false           # blind driver: only works operator-queued tasks
# drain_main = true          # default: also claim type-matching shared tasks
# extra_args = ["--flag"]    # raw args appended to every agent invocation
# mode = "drain"             # goal (default): invent idle work + accept agent
                             # proposals. discover: never invent, but still
                             # accept follow-ups proposed right after a task.
                             # drain: never invent, never accept proposals —
                             # the pipeline can only shrink the backlog.
                             # Set, mode overrides invent.
```

## How multi-pipeline integration works

With more than one pipeline (and `worktrees = "auto"`), each pipeline works
in a **sibling worktree** (`<repo>-wt-<name>`) on its own branch
(`workshop/<name>`), pulling the gated-green trunk in before each pass. The
**integrator** merges lane branches into your trunk `--no-ff` in rotation,
runs your `verify` command on the combined result, and **bisects out** the
first lane that turns it red (a lane red *alone on trunk* is a proven
culprit; red only in combination records the suspect set). Nothing is ever
force-pushed; a bad lane just doesn't land until its tip advances.

**Merge conflicts** are never machine-merged. If you route the
`merge-conflict` type, the conflict becomes a pinned-SHA task: an agent
reproduces the merge in an ephemeral worktree, resolves it semantically, and
the ENGINE verifies the result (no unmerged paths, no leftover markers, gate
green) before the resolution lands through the normal queue. After 2 failed
attempts the lane falls back to waiting for new commits.

If worktrees are off but several pipelines are enabled, passes serialize on
the shared tree — safe, just slower.

## Prompts

The ~90-line pass contract (read goal → one increment → verify → self-report)
is **built into the binary** and improves with upgrades. Customize per repo
with optional fragments under `.workshop/prompts/`:

- `project.md` — reading list, guardrails, conventions (every pass)
- `types/<type>.md` — e.g. `art.md` palette rules (passes working that type)
- `pipelines/<name>.md` — per-pipeline scope hints
- `base.md` — full contract replacement (you own the consequences)

## The dashboard

`workshop` opens `http://127.0.0.1:4455` (loopback ONLY; mutations need the
session token baked into the URL it opens). Three columns: goal + backlog
board (shared + per-pipeline sections) · pipeline cards (live log tail for
claude; the agent's own progress report for blind drivers, which is all that
exists) · merge queue, activity feed, and alert banners (auth halts, breaker
trips, wedge kills, suspected agy auth loss).

Pipelines come up **stopped** by default — each card shows a **resume** button
and nothing runs until you press it, so you can review the goal and backlog
first. Set `[server] start_stopped = false` (or launch `workshop up
--start-running`) to auto-run on launch instead.

Each pipeline card's ⚙ button switches agent/model/effort **live**: the
override is stored, applied from the next pass on (no restart), marked with ⚡
on the card, and cleared back to configured routing with one click. Per-task
pins still beat it.

## ⚠️ Unattended execution

Agents run with `--dangerously-skip-permissions` by default
(`[safety] skip_permissions = false` disables it): they edit, run, and delete
files on their own, for as many passes as you allow. **Only run where git can
fully revert you.** Start bounded (`workshop run`), watch the first passes,
and give `verify` real teeth — the gate is the whole safety story. The
server binds 127.0.0.1 and is never safe to expose.

## Migrating from the PowerShell workshop

```
workshop migrate --from C:\path\to\old\workshop
```

copies `GOAL.md`, your `PROMPT.md` edits (→ `prompts/project.md` — trim the
boilerplate, the contract is built in), and imports `backlog.json` /
`completions.json`. `workshop.config.ps1` knobs map onto `config.toml`
(`Root` → run in the repo; `Branch` → `project.trunk`; `WedgeMinutes` →
`safety.wedge_minutes`; `UiPort` → `server.port`; personas/nouns → `[spice]`;
`-AgentExtraArgs` → per-pipeline `extra_args`; `-SleepSeconds` →
`safety.sleep_seconds`). `agent.json` is superseded by the routing table,
per-task pins, and the dashboard's live model switch.

Behavior changes from the old tool worth knowing:

- **Wedge handling**: the old loop only showed "wedged?" in the UI; v2
  *kills* the pass at `wedge_minutes` and counts it as a failure.
- **`RALPH_PASS`** is not exported anymore. Repo-side hooks that keyed on it
  should use `WORKSHOP_PASS_N` (plus `WORKSHOP_PASS_STATE_DIR` /
  `WORKSHOP_PASS_REPO_DIR`), which every agent process gets.

## Development

```
cd workshop
go test ./...             # unit + integration (spawns real git repos + a scripted fake agent)
go test -tags e2e ./e2e   # end-to-end: builds the REAL binary, drives it against scaffolded repos
go build ./cmd/workshop
```

The `e2e` suite is the orchestrator-level proof: it builds `cmd/workshop`,
scaffolds throwaway git repos with `.workshop/` configs routed to the fake
driver, seeds backlogs through the CLI, runs bounded `workshop run`s (single
pipeline, and two worktree lanes through the merge queue), and asserts on the
resulting git history and `workshop status --json`. It is hermetic — temp
state dirs, temp git identity, no real agents — so it's safe to run while a
live workshop instance is using this machine.

### Self-hosting (workshop editing workshop)

This repo carries its own `.workshop/` so Workshop can work on itself. Rules
that keep that sane:

- The **running binary and the edited source are different artifacts** — a
  green gate proves the new source builds, tests, and drives end-to-end, but
  you are always running an older build. **Adopt in small batches**: every few
  landed commits, stop the instance, `go build ./cmd/workshop`, replace the
  binary on PATH, and smoke it on a scratch repo before trusting it.
- Never `go install` over a binary a running instance is using (Windows locks
  it); stop that instance first.
- Review `workshop:` config commits and any test-file diffs with extra
  suspicion — the loop edits the code AND the tests that define "passing".

The dashboard is buildless on purpose: vendored Preact+HTM as native ES
modules under `web/ui/`, embedded via `go:embed`. Agent-driver behavior facts
(especially agy's) live in [`AGENTS.md`](AGENTS.md) — read it before touching
driver wiring.
