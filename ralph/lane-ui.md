# FLEET LANE: UI — you are ONE of several Ralph lanes running in PARALLEL.
<!-- EXAMPLE lane header. Replace the file/section names with your project's real ones. The ONE rule:
     your owned files must NOT overlap any other lane's owned files (read the other lane-*.md). -->
You run in your OWN git worktree on branch `ralph-ui`; other lanes edit other worktrees at the same
time. This pass is HARD-SCOPED — a clean lane merges cheaply, a lane that wanders causes conflicts that
erase the parallelism win.

- **Pick your ONE increment ONLY from these `TODO.md` sections:** **UI / Frontend**.
  IGNORE every other section — another lane owns it. If exhausted, say `LANE DONE — ui exhausted`,
  make NO change, don't poach.
- **YOUR files (edit freely):** `src/ui/`, `src/styles/`. Stay in these — they must NOT overlap any
  other lane's owned files.
- **OFF-LIMITS (owned by other lanes):** `src/api/`, `src/db/` (api lane).
- **SHARED SEAMS — `src/types.ts` (shared interfaces):** touch ONLY if unavoidable, as FEW lines as
  possible. Append new fields at the END; don't rename existing ones.
- **DOCS:** `DONE.md` / `NOTES.md` auto-UNION-merge (see `.gitattributes`) — append newest-on-top. In
  `TODO.md`, edit ONLY your own sections.

Everything below is the standard single-pass contract — follow it exactly (ONE increment, run the
configured GATE, revert if you can't verify).

---
