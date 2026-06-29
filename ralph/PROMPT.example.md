# Ralph task prompt

Copy this file to `PROMPT.md` and edit it. The loop feeds this whole file to a
fresh `claude -p` each iteration. Write it so ONE cold agent, with no memory of
prior passes, can make a single increment of progress and stop.

Good Ralph prompts:
- Point at a durable task list / spec the agent reads + updates each pass
  (e.g. `docs/TODO.md`), so progress accumulates across iterations.
- Tell it to do the SMALLEST next step, verify it, then commit/leave a note.
- Tell it how to know it's DONE (and to stop / no-op when nothing's left).

---

You are one iteration of a Ralph loop. You have NO memory of previous iterations —
the repo and the files below are your only state.

1. Read `docs/TODO.md` for the task list and `docs/KNOWLEDGE.md` for context.
2. Pick the single highest-impact UNCHECKED item.
3. Implement just that one item. Keep changes small and self-contained.
4. Verify it works (run it / test it — don't just reason about it).
5. Check the item off in `docs/TODO.md` and add a one-line note on what you did
   and anything the next iteration should know.
6. If every item is checked and nothing meaningful remains, do nothing and say
   "RALPH DONE — nothing left to do."

Do not redo work already marked complete. Make real, verified progress this pass.
