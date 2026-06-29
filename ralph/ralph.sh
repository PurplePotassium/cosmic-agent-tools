#!/usr/bin/env bash
# Ralph loop — run `claude -p` back-to-back on the same prompt, fresh context each
# pass, until the work is done or N iterations elapse. The "Ralph Wiggum" technique:
# a dumb while-loop around a smart agent.
#
#   --random  adds the blog's anti-circling trick (turso.tech/blog/edgar-allan-poe):
#             long runs stop exploring, so inject semantic tension each iteration.
#             Per-pass it randomly picks one of:
#               persona       — "channel the mindset of <persona>"
#               recode-decode — priming noun at START ("Related to FOOD:") +
#                               diverting word-stem at END ("Pas")
#
# Usage:
#   ./ralph/ralph.sh [-p PROMPT] [-n ITERS] [--random] [-s SLEEP] [-m MODEL] [--no-skip]
#   ./ralph/ralph.sh -n 20                 # plain Ralph, 20 passes
#   ./ralph/ralph.sh --random              # improved Ralph, infinite (Ctrl-C to stop)
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROMPT="$DIR/PROMPT.md"
ITERS=0            # 0 = forever
RANDOMIZE=0
SLEEP=2
MODEL=""
SKIP=1             # --dangerously-skip-permissions (unattended); --no-skip to disable
PERSONAS_FILE="$DIR/personas.txt"   # swap for a themed pool
NOUNS_FILE="$DIR/nouns.txt"
LOGDIR="$DIR/logs"

while [[ $# -gt 0 ]]; do
  case "$1" in
    -p|--prompt) PROMPT="$2"; shift 2;;
    -n|--iterations) ITERS="$2"; shift 2;;
    --random) RANDOMIZE=1; shift;;
    -s|--sleep) SLEEP="$2"; shift 2;;
    -m|--model) MODEL="$2"; shift 2;;
    --personas) PERSONAS_FILE="$2"; shift 2;;
    --nouns) NOUNS_FILE="$2"; shift 2;;
    --no-skip) SKIP=0; shift;;
    -h|--help) sed -n '2,20p' "$0"; exit 0;;
    *) echo "unknown arg: $1" >&2; exit 2;;
  esac
done

[[ -f "$PROMPT" ]] || { echo "Prompt file not found: $PROMPT (copy PROMPT.example.md -> PROMPT.md)" >&2; exit 1; }
BASE="$(cat "$PROMPT")"
mkdir -p "$LOGDIR"

# read a pool file: non-blank, non-comment lines
read_pool() { [[ -f "$1" ]] && grep -vE '^\s*(#|$)' "$1" || true; }
mapfile -t PERSONAS < <(read_pool "$PERSONAS_FILE")
mapfile -t NOUNS    < <(read_pool "$NOUNS_FILE")
pick() { local arr=("$@"); echo "${arr[RANDOM % ${#arr[@]}]}"; }

ARGS=(-p)
[[ -n "$MODEL" ]] && ARGS+=(--model "$MODEL")
[[ "$SKIP" -eq 1 ]] && ARGS+=(--dangerously-skip-permissions)

echo "=== Ralph loop$([[ $RANDOMIZE -eq 1 ]] && echo ' (RANDOM / anti-circling)') ==="
echo "prompt : $PROMPT"
echo "iters  : $([[ $ITERS -eq 0 ]] && echo 'infinite (Ctrl-C to stop)' || echo "$ITERS")"
[[ "$SKIP" -eq 1 ]] && echo "WARNING: --dangerously-skip-permissions ON. Agent runs unattended w/ full tool access. Be able to revert."

i=0
while [[ $ITERS -eq 0 || $i -lt $ITERS ]]; do
  i=$((i+1))
  STAMP="$(date '+%Y-%m-%d_%H-%M-%S')"
  P="$BASE"; MODE="plain"
  if [[ $RANDOMIZE -eq 1 ]]; then
    if (( RANDOM % 2 == 0 )) && [[ ${#PERSONAS[@]} -gt 0 ]]; then
      persona="$(pick "${PERSONAS[@]}")"
      P="For this iteration only, channel the mindset of ${persona}. Bring that distinctive way of seeing to the task below — a lens to break you out of repeating earlier ideas, not a change to the goal.

$BASE"
      MODE="persona:$persona"
    else
      noun="$(pick "${NOUNS[@]:-food}")"
      src="$(pick "${NOUNS[@]:-pasta}")"
      len=$(( (RANDOM % 3) + 2 )); (( len > ${#src} )) && len=${#src}
      stem="${src:0:len}"; stem="$(tr '[:lower:]' '[:upper:]' <<< "${stem:0:1}")${stem:1}"
      P="Related to ${noun^^}: ${BASE} ${stem}"
      MODE="recode:$noun/$stem"
    fi
  fi

  LABEL="iteration $i$([[ $ITERS -ne 0 ]] && echo "/$ITERS") [$MODE] $STAMP"
  echo ""
  echo "--- $LABEL ---"
  LOG="$LOGDIR/iter-$(printf '%04d' "$i")-$STAMP.log"
  # Self-describing header so a log read on its own says which pass/mode/model it was.
  { echo "=== $LABEL ==="; echo "model  : ${MODEL:-default}";
    echo "spice  : $MODE  (anti-circling lens for THIS pass, not the task)";
    echo "prompt : $PROMPT"; printf '%.0s-' {1..72}; echo; echo; } > "$LOG"
  # tag this pass so the auto-commit Stop hook makes a SEPARATE per-pass commit (bisectable
  # history for unattended runs) instead of amending one rolling checkpoint. Plain value only
  # (no persona text) so it's safe inside the hook's commit message.
  export RALPH_PASS="iter $i"
  printf '%s' "$P" | claude "${ARGS[@]}" 2>&1 | tee -a "$LOG"
  # what the pass changed — the Stop hook has committed by now; file list shows which TODO moved.
  { echo; printf '%.0s-' {1..72}; echo; echo "--- files changed this pass ---";
    git -C "$DIR" show --stat --format='commit %h  %s' HEAD 2>&1 || true; } >> "$LOG"

  if [[ "$SLEEP" -gt 0 && ( $ITERS -eq 0 || $i -lt $ITERS ) ]]; then sleep "$SLEEP"; fi
done

echo ""
echo "=== Ralph loop done ($i iterations). Logs in $LOGDIR ==="
