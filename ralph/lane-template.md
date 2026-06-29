# FLEET LANE: <NAME> — you are ONE of several Ralph lanes running in PARALLEL.
<!-- COPY this file to lane-<name>.md, fill every <...> slot, then add a row to lanes.txt. The ONE
     rule that keeps parallel Ralph from thrashing: give each lane a DISJOINT set of src/ files that
     no other lane owns. Read the other lane-*.md files and pick a partition that doesn't overlap. -->
You run in your OWN git worktree on branch `ralph-<name>`; other lanes edit other worktrees at the
same time. This pass is HARD-SCOPED — a clean lane merges cheaply, a lane that wanders causes
conflicts that erase the parallelism win.

- **Pick your ONE increment ONLY from these `TODO.md` sections:** **<sections this lane owns>**.
  IGNORE every other section — another lane owns it. If exhausted, say `LANE DONE — <name> exhausted`,
  make NO change, don't poach.
- **YOUR files (edit freely):** `<src/NN-foo.js>`, `<src/NN-bar.js>`. Stay in these — they must NOT
  overlap any other lane's owned files.
- **SHARED SEAMS — `<files multiple lanes occasionally touch, e.g. 08-main.js __rs block>`:** touch
  ONLY if unavoidable, as FEW lines as possible. Append new `__rs` getters at the END of the block.
- **DOCS:** `DONE.md` / `NOTES.md` / `FEEL-REVIEW.md` auto-UNION-merge (see `.gitattributes`) — append
  newest-on-top as usual. In `TODO.md`, edit ONLY your own sections.

Everything below is the standard single-pass contract — follow it exactly (ONE increment, run the
`node test/sim.mjs` gate, revert if you can't verify).

---
