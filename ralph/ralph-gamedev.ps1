<#
.SYNOPSIS
  Game-dev flavored random Ralph loop. Same engine as ralph.ps1 but -Random is
  always on and the persona/noun pools are the game-dev sets (artists, designers,
  playtesters, players, memelords, ...). Use it to grind on a browser game while
  the agent keeps switching creative lenses so it doesn't circle.

.EXAMPLE
  ./ralph/ralph-gamedev.ps1 -Iterations 30
  ./ralph/ralph-gamedev.ps1                 # infinite (Ctrl-C to stop)

  Any ralph.ps1 flag passes straight through, e.g. -Model, -Prompt, -SleepSeconds.
#>

& "$PSScriptRoot/ralph.ps1" -Random `
  -Personas "$PSScriptRoot/personas-gamedev.txt" `
  -Nouns    "$PSScriptRoot/nouns-gamedev.txt" `
  @args
