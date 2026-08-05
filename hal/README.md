# Hal

coding-agent workflows for any git repository, with a live dashboard.

One self-contained binary. Point it at a repo and each unit of work becomes a
**workflow**: a live Claude Code conversation that moves through five fixed,
reviewable stages — **refine → research → design → plan → implement**.
The agent asks questions, you steer it in chat, and each stage produces a
markdown artifact. The default-on **auto-approve ready stages** checkbox
commits and advances completed stages automatically; clarification questions,
blockers, errors, missing artifacts, and red validation gates still pause for
you. Turn it off to review and approve (or skip) every stage manually. Approved
artifacts are committed with the work, so every
decision trail is project history. Validation is a separate **validation
run**: one validate conversation that sweeps every implemented-but-
unvalidated workflow at once — triggered by the dashboard's Validate button
or automatically when an implementation lands and the engine is idle.

```
cd your-repo
hal init      # optional — scaffolds .hal/ (config + GOAL + .claude/agents)
hal           # dashboard opens; type what you want built and press Start
```

---

## Install

- **From source** (Go 1.26+): `go install github.com/PurplePotassium/cosmic-agent-tools/hal/cmd/hal@latest`
- **From source again**: `go install ./cmd/hal # from this project path`
- **Release binaries**: download from GitHub Releases and drop on your PATH
  (`%LOCALAPPDATA%\Programs\hal\` is a good spot on Windows).

You need **`claude`** (Claude Code) on PATH and authenticated — it is the
interactive driver every workflow turn runs on. **`agy`** (Antigravity CLI /
Gemini) is optional, used only by art jobs. `hal doctor` checks
everything.

There is no Node, no npm, no scripts to copy: the dashboard is embedded in
the binary, and `go build ./cmd/hal` is the entire build.

## The layout: intent vs. state

| where | what | versioned? |
|---|---|---|
| `<repo>/.hal/` | `config.toml`, `GOAL.md`, `prompts/` fragments, `workflows/<id>/` stage artifacts | **yes — commit it, share it with your team** |
| OS state dir¹ | workflow DB (conversations, turns, messages), turn logs, `server.json` | no — machine-local |

¹ `%LOCALAPPDATA%\hal\projects\<name>-<hash>` on Windows; `hal path` prints every resolved location.

Stage approvals commit the workflow's artifact folder as their own
`ws(flow …) <stage> approved` commits (with `Hal-Workflow` /
`Hal-Stage` trailers), so the decision record travels with the code.

## Commands

```
hal            start server + dashboard + workflow engine (default; Ctrl+C exits)
hal init       scaffold .hal/ (config.toml + GOAL.md + .claude/agents)
hal task       ideas inbox: add | list | rm
hal status     one-shot snapshot: open ideas + live workflows (--json)
hal stop       stop the running server gracefully (--force kills a hung engine)
hal doctor     environment health check (--json)
hal bug        write a self-contained bug report (env, git, config, status; --logs adds the last art/inquiry log)
hal agy-run    run agy with the given args in a hidden console (agy drops output on
                    pipes); how art-job orchestrators invoke it — exits with agy's code
hal path       print resolved dirs and config files
hal migrate    import GOAL/PROMPT/backlog from the old PowerShell hal (--from <dir>)
hal version
```

## The workflow model

Every workflow is one live conversation with fixed, interruptible checkpoints:

1. **refine** — the agent interprets your raw ask conversationally (no repo
   access) and distills it into a research question (`01-question.md`).
2. **research** — read-only codebase (and, when asked, web) investigation →
   `02-research.md`.
3. **design** — approaches + tradeoffs + a recommendation → `03-design.md`.
4. **plan** — a checkbox implementation plan → `04-plan.md`.
5. **implement** — the only task stage with write access to the repo; checks
   the plan off in place and writes a changelog of every change (plus the
   checks validation must run) → `05-implementation.md`. Approving it
   completes the workflow and queues the implementation for validation.

Each stage reads exactly ONE input artifact — its predecessor's output
(skips resolve to the newest non-skipped ancestor).

**Validation runs.** Validate is no longer a per-workflow stage. A
validation run is a single validate conversation that covers EVERY completed,
implemented workflow no run has checked yet. It verifies each changelog's
claims against the code, runs your `verify` command (a red verify gates
approval) and the `agent_play/` smoke test, and — with full write access —
**auto-fixes issues whose solution is obvious** (in the implementations, the
agent-play harness, the smoke scenario, or the verify wiring); anything
needing a real decision lands in the report instead. A run triggers when:

- you click **Validate** on the dashboard (always allowed — with nothing
  pending it runs as a health check of the verify command and the smoke
  harness), or
- an implement approval completes a workflow while no other stage is
  executing and **auto-validate on completion** (the persisted checkbox
  beside the Validate button, on by default) is checked.

Approving a run's report stamps every covered workflow validated and
**archives its `.hal/workflows/<id>/` folder to the OS recycle bin**, with
the deletion committed — the artifacts stay recoverable in git history and
the bin while the working tree stays tidy.

Mechanics that keep this honest:

- **Chat steering.** Between turns you can answer, redirect, or use the
  steering-hint chips. While a turn is running, sending a message
  **interjects**: the turn process is killed and your message steers the
  resumed session (`--resume` keeps full context — see
  [docs/interactive-driver.md](docs/interactive-driver.md) for the probed
  per-turn contract).
- **Approval gates.** Each stage ends with review-and-approve in the
  dashboard: approve (commits the artifact and opens the next stage), request
  changes (your feedback becomes the next turn), or skip (a stub artifact
  records the skip). Intake can also start at a later stage — everything
  before it is stub-skipped. With **auto-approve ready stages** checked (the
  default), the engine uses that same approval path as soon as the agent
  reports a final artifact; it never auto-advances an `asking`, `blocked`, or
  `error` state, a missing artifact, or a red validation gate.
- **Artifacts live in the repo** under `.hal/workflows/<id>/`, editable
  from the dashboard (conflict-checked) or your editor.
- **Tool scoping + tree check.** Stages before implement run with a
  read-only tool surface, and any file they change outside their artifact
  folder is reverted after the turn (`workflow.tree_violation`). Implement
  and validation-run turns have write access and commit the whole tree after
  each turn so a killed turn never strands work.
- **Statuses.** `turn-running` → agent working; `awaiting-user` → it asked
  you something; `awaiting-approval` → artifact ready for review; `blocked` →
  the implement plan/reality mismatch hard-stop; `error` → the turn failed
  (any message retries via resume).

## The ideas inbox

Not every thought deserves a workflow yet. The **ideas** tab (and `hal
task add`) is a lightweight parking lot: title + detail + pasted screenshots.
One click (**→ workflow**) promotes an idea — its text seeds the workflow
brief and the idea closes with a pointer at it.

## Configuration

Everything lives in `.hal/config.toml`; **every key is optional** — an
absent/empty file works out of the box. Layering, lowest to highest:
built-ins → user-global (`%APPDATA%\hal\config.toml`) → repo file →
`HAL_*` env → CLI flags.

```toml
[project]
name   = "space-game"
trunk  = "main"              # the branch workflows work on (checked at start; default: current)
verify = "npm test"          # THE GATE. exit 0 = pass. validation-run approval is gated on it.
# verify_dir = "client"      # where verify runs (default: repo root)

[safety]
skip_permissions = true      # implement/validate turns bypass the agent CLI's
                             # permission prompts (see ⚠️ below); false = Claude's
                             # acceptEdits mode + explicit tool allowlist
max_concurrent   = 2         # simultaneous agent turn processes across workflows
wedge_minutes    = 35        # bounds one ART JOB invocation; keep above agy's
                             # 30m --print-timeout (workflow turns use turn_minutes)

[workflow]
turn_minutes = 20            # per-turn ceiling — never counts waiting on you
artifact_dir = ".hal/workflows"   # repo-relative artifact root
# Per-stage model/effort (the agent is always claude); a per-workflow ⚙
# override from the dashboard beats these.
# [workflow.stages.research]
# model  = "claude-opus-4-8"
# effort = "high"
# [workflow.stages.implement]
# effort = "max"

[server]                     # the dashboard experience
open_browser = true          # open the dashboard on launch (default true)
# port = 4455

# ---- art jobs: generated image assets --------------------------------------
# The dashboard's "generate art" card runs one job at a time: a frontier
# claude orchestrator (fable preferred, opus allowed) invokes agy (the Gemini
# image model) through `hal agy-run` (agy drops output/hangs on piped
# stdio, so it gets a hidden console), verifies what agy wrote, and may
# refine + retry. The asset lands at the target path (default
# assets/art/<slug>.png). "transparent" adds a second, same-conversation agy
# step that repaints the background as a flat green screen (blue when the
# subject is green-heavy); the engine then keys the screen away, leaving a
# transparent PNG.
# Every image agy writes is byte-verified: a file whose bytes are actually
# JPEG/WebP/GIF/BMP/TIFF is re-encoded as a real PNG in place (event
# "art.normalized"); undecodable bytes fail the job; a job that leaves no NEW
# agy conversation record fails outright (the image must come from agy, never
# be fabricated by the orchestrator).
# The image model is agy's, preferring "Gemini 3.1 Pro (High)" and falling
# back to "Gemini 3.5 Flash (High)" — launch verifies which of those agy
# actually offers (quota-free probe). agy labels must be EXACT: "gemini 3
# pro" is rejected.
[art]                        # transparent-job green/blue-screen removal
remover = "ffmpeg"           # ffmpeg (colorkey+despill; needs ffmpeg on
                             #   PATH — `winget install ffmpeg`; default)
                             # | corridorkey (neural keyer — the CorridorKey
                             #   checkout at corridorkey_dir; invoked with
                             #   --device cuda, NEVER CPU: a CPU-only torch
                             #   venv measured 2+ hours per image, so provision
                             #   it with `uv sync --extra cuda`)
                             # Switchable LIVE from the dashboard settings
                             # panel (⚙ → keyers) — applies to the next job.
# removers = ["ffmpeg", "corridorkey"]  # multi-keyer comparison mode (beats `remover`):
                             # every listed backend keys each screen; the FIRST
                             # entry's output becomes the committed asset, the
                             # rest are archived beside the job log (and
                             # mirrored by [export]) as iter-NNNNNN.keyed-<keyer>.png
                             # so a human can compare files and settle on the
                             # most effective keyer.
# corridorkey_dir = 'C:\GameDev\CorridorKey'   # HAL_CORRIDORKEY env also works

[export]                     # audit trail: mirror evidence to a folder you choose
dir = 'C:\audits\space-game' # default "" = off. Every finished workflow turn and
                             # art job mirrors its raw stream log and the agent
                             # runtime's FULL transcript (prompt as sent, thinking,
                             # response, tool calls) there, plus transparent jobs'
                             # screened intermediate (.screen.png) and keyer
                             # comparisons. Must be OUTSIDE the repo (implement
                             # turns commit anything dirty, so exported evidence
                             # inside would land in project history — the engine
                             # refuses to start). Relative = repo-root-relative
                             # (and therefore refused); pick an absolute path.
human_readable = false       # true also renders each transcript as markdown

# model is validated against a curated family list per agent — claude:
# sonnet/fable/opus/haiku, codex: gpt-5.6-sol/terra/luna, agy: gemini — and just WARNS (never blocks) on a
# mismatch. Off-list on purpose (a proxy alias, a brand-new id)? Whitelist it:
# [agents.claude]
# extra_models = ["my-internal-proxy-model"]

# The self-evaluator's route (see "Ask why" below):
# [types.inquiry]
# model  = "claude-opus-4-8"
# effort = "xhigh"
```

Pass-loop era keys (`[[pipelines]]`, `[spice]`, `[git]` worktrees, breaker/
iteration knobs, …) are parsed tolerantly and ignored with a deprecation
warning — an old config never bricks the CLI.

## Prompts

The stage contracts (conversation protocol, status-file discipline, artifact
shapes) are **built into the binary** and improve with upgrades. Customize
per repo with optional fragments under `.hal/prompts/`:

- `project.md` — reading list, guardrails, conventions (injected into every
  stage prompt; editable from the dashboard's goal tab)
- `stages/<stage>.md` — replace one stage's instructions
- `stages/workflow-contract.md` — replace the shared contract (you own the
  consequences)

`hal init` also seeds `.claude/agents/` with the locator/analyzer
sub-agent definitions the research/design prompts call by name.

In a repo that carries the `agent_play/` toolkit, `hal init` additionally
seeds `agent_play/agent_play.config.json` with an example `smoke` entry —
the level select / setup a validation run plays as its quick agent
playthrough. An existing toolkit config is never touched (even with
`--force`); if it lacks a `smoke` entry, init prints the entry to merge.

## The dashboard

`hal` opens `http://127.0.0.1:4455` (loopback ONLY; mutations need the
session token baked into the URL it opens). Left column tabs:

- **workflows** — the intake box ("What should we build / fix / understand?",
  with an advanced row for start-stage and model/effort) and the live
  workflow list: per-card stage stepper (✓ approved · ● active · ○ pending ·
  ⊘ skipped), status badge, and a "waiting Xm" age while a workflow sits on
  your move.
- **ideas** — the inbox: add (with pasted images), delete, promote.
- **goal** — `GOAL.md` and the `project.md` prompt fragment.
- **eval** — the fixed Goal.md self-evaluation questions.

Selecting a workflow opens the detail: a **chat pane** (live-streamed
assistant text, collapsed one-line tool calls, steering hints, interject
while a turn runs) beside the **artifact pane** (stage tabs, sanitized
markdown rendering with checkbox progress on the plan, raw/edit with
conflict-checked save, the implement diffstat, and the approve / request
changes / skip / abandon bar). The tab title badges `(N) Hal` and an
optional chime fires when a workflow needs you.

The right column (when no workflow is open) holds the activity feed, recent
commits, the **generate art** card (see `[art]` above), and **Ask why**.

### The self-evaluator ("ask why")

The **Ask why** card answers questions about what the hal already did —
"why are the coin pickups so big?". It launches a one-shot **read-only**
forensics agent — no skip-permissions; only read tools and read-only git
commands are allowlisted — that works the evidence trail the engine keeps
anyway: commit trailers, turn logs, and each turn's archived **full session
transcript** (`turn-NNNNNN.transcript.jsonl` — the exact prompt, every tool
call, the model's reasoning; Claude Code prunes its own copies after ~30
days, the archive is permanent). Answers stream into the card live. One
inquiry runs at a time; route it with `[types.inquiry]`.

## ⚠️ Unattended execution

Implement and validation-run turns run with Claude Code's permission bypass
by default (`[safety] skip_permissions = true`); set it to `false` for
acceptEdits mode plus an explicit tool allowlist instead. The earlier stages
are read-only by tool policy, backed by the engine's post-turn tree check.
Agents can still edit, run, and delete files during implement — **only run
where git can fully revert you**, review before approving, and give `verify`
real teeth. The server binds 127.0.0.1 and is never safe to expose.

## Development

```
cd hal
go test ./...             # unit + integration (spawns real git repos + a scripted fake agent)
go test -tags e2e ./e2e   # end-to-end: builds the REAL binary, drives it against scaffolded repos
go build ./cmd/hal
go vet ./...               # also runs in CI
golangci-lint run ./...    # static analysis — see .golangci.yml (install: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
```

No TypeScript tooling is set up: the project has no TS/JS build step by
design (`web/ui/` is vendored, buildless ES modules — see below), so there's
nothing for an ESLint/tsc-style linter to check. When editing the UI,
remember a single syntax error kills the whole app — `node --check` each
touched file (copy to `.mjs` first so it parses as a module).

A pre-commit hook (`hal/githooks/pre-commit`, runs `golangci-lint`) is
available but **not enabled by default**: this repo self-hosts Hal, and
the engine's automated commits must never be blocked by a local tool that
might be missing or briefly noisy. Opt in per clone if you want it on your
own commits:

```
git config core.hooksPath hal/githooks
```

CI (`.github/workflows/ci.yml`, outside `hal/`) doesn't run
`golangci-lint` yet — wiring that in is a follow-up for a human or a pass
scoped to touch the workflow file.

The `e2e` suite is the orchestrator-level proof: it builds `cmd/hal`,
scaffolds throwaway git repos with `.hal/` configs, boots `hal up`
with workflow turns routed to the scripted fake agent
(`HAL_WORKFLOW_AGENT=fake`), and drives a full workflow refine →
implement → completed plus the auto-opened validation run over REST —
approvals, artifacts, commit trailers, message history, the archive step,
and the interject path. It is hermetic — temp state dirs,
temp git identity, no real agents — so it's safe to run while a live
hal instance is using this machine.

### Self-hosting (hal editing hal)

This repo carries its own `.hal/` so Hal can work on itself. Rules
that keep that sane:

- The **running binary and the edited source are different artifacts** — a
  green gate proves the new source builds, tests, and drives end-to-end, but
  you are always running an older build. **Adopt in small batches**: every few
  landed workflows, stop the instance, `go build ./cmd/hal`, replace the
  binary on PATH, and smoke it on a scratch repo before trusting it.
- Never `go install` over a binary a running instance is using (Windows locks
  it); stop that instance first.
- Review `hal:` config commits and any test-file diffs with extra
  suspicion — the agent edits the code AND the tests that define "passing".

The dashboard is buildless on purpose: vendored Preact+HTM as native ES
modules under `web/ui/`, embedded via `go:embed`. Agent-driver behavior facts
(especially agy's) live in [`AGENTS.md`](AGENTS.md) — read it before touching
driver wiring; the interactive claude turn contract is probed and documented
in [`docs/interactive-driver.md`](docs/interactive-driver.md).
