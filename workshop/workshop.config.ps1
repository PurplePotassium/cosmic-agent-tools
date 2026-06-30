# ============================================================================
#  WORKSHOP CONFIG  -- edit these for YOUR project, then run the Workshop.
#  Every workshop PowerShell script dot-sources this file for its DEFAULTS.
#  Any CLI param still overrides it (e.g. ./start-workshop.ps1 -Iterations 5).
#  See README.md for the full walkthrough.
#
#  The Workshop is the SINGLE-agent counterpart to the fleet (../ralph): one
#  agent at a time, fresh context each pass, draining an operator-curated
#  backlog toward GOAL.md. No worktrees / lanes / refinery / planner.
# ============================================================================
$WorkshopConfig = @{

  # Absolute path to the repo the agent works in. This is the loop's WORKING
  # DIRECTORY, so its `git add`/`git commit` (and any Stop hook) land here.
  Root = 'C:\path\to\your\repo'

  # Branch the loop commits onto. Leave '' to use whatever branch Root is on.
  # Recommended: a dedicated branch you can review/revert (e.g. 'workshop').
  Branch = ''

  # Default agent/model for the FIRST pass. After launch the live selection is
  # agent.json (UI-editable, re-read each pass). Agent: 'claude' | 'agy' |
  # 'auto'. 'auto' classifies the top backlog item per pass (see workshop.ps1
  # Resolve-AutoSelection). Model '' = the agent's own default (claude->Sonnet).
  Agent = 'claude'
  Model = 'claude-sonnet-4-6'

  # Anti-circling pools injected each pass (-Random is always on for the
  # Workshop). Filenames resolve next to the scripts. Swap to the plain
  # personas.txt / nouns.txt for a non-game project.
  Personas = 'personas-gamedev.txt'
  Nouns    = 'nouns-gamedev.txt'

  # Port the standalone Workshop web UI (ui/server.js) listens on.
  UiPort = 4455

  # OPTIONAL: a live preview of the project shown in the UI's right column
  # (e.g. your dev server, or a static page served by the UI). Leave '' to hide
  # the preview pane entirely. If set to a path under Root it is mounted at
  # /preview/; if set to an http(s) URL it is embedded as-is.
  #   PreviewUrl  = 'http://localhost:8761/index.html'   # external dev server
  #   PreviewPath = 'prototype'                          # static dir under Root
  PreviewUrl  = ''
  PreviewPath = ''

  # How long (minutes) a single pass may run before the status UI flags it
  # 'wedged'. A legitimate increment rarely exceeds this.
  WedgeMinutes = 20
}

# --- resolution helper used by every workshop script ------------------------
# Fills a param from $WorkshopConfig ONLY when the caller didn't pass it and
# it's still empty/default. $boundKeys = $PSBoundParameters.Keys from the caller.
function Resolve-WorkshopDefault {
  param([string]$Name, $Current, $boundKeys, $Default)
  if ($boundKeys -contains $Name) { return $Current }   # caller set it explicitly
  if ($null -ne $Current -and "$Current" -ne '') { return $Current }
  return $Default
}
