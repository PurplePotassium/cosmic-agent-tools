#!/usr/bin/env bash
# Game-dev flavored random Ralph loop. Same engine as ralph.sh but --random is
# always on and the persona/noun pools are the game-dev sets (artists, designers,
# playtesters, players, memelords, ...). Any ralph.sh flag passes straight through.
#
#   ./ralph/ralph-gamedev.sh -n 30
#   ./ralph/ralph-gamedev.sh            # infinite (Ctrl-C to stop)
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec "$DIR/ralph.sh" --random \
  --personas "$DIR/personas-gamedev.txt" \
  --nouns    "$DIR/nouns-gamedev.txt" \
  "$@"
