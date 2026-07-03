# Goal

Improve **Workshop itself** (`workshop/` in this repo): a single Go binary that
runs autonomous coding-agent loops with an embedded dashboard.

Every increment must keep the full gate green — `go build`, the unit and
integration suites, and the `e2e` suite that builds the real binary and drives
it end-to-end against scaffolded repos. The gate is the definition of done;
never trade gate integrity for a green pass.

Priorities, in order:

1. Correctness of the engine: pass state machine, merge queue, worktree lanes,
   process lifecycle (wedge kills, Job Objects), driver behavior.
2. Test coverage where it's thinnest: `internal/server` guards, `internal/bus`,
   `internal/app`, `supervisor`, `cmd/workshop`.
3. Operator experience: dashboard, CLI ergonomics, actionable errors.

Work ONLY on tasks from the backlog. Do not invent new directions.
