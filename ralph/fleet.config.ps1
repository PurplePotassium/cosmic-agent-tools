# ============================================================================
#  FLEET CONFIG  -- edit these for YOUR project, then run the fleet.
#  Every fleet PowerShell script dot-sources this file for its DEFAULTS.
#  Any CLI param still overrides it (e.g. ./refinery.ps1 -Base other-branch).
#  See SETUP.md for the full walkthrough.
# ============================================================================
$FleetConfig = @{

  # Absolute path to your repo's MAIN git worktree. The trunk branch lives
  # here and the refinery owns this tree (checks out / resets Base in it).
  Root = 'C:\path\to\your\repo'

  # Trunk branch the lanes fork from and the refinery merges back into.
  # Create it first:  git branch fleet-trunk
  Base = 'fleet-trunk'

  # Working dir for the integration GATE, RELATIVE to Root (use '.' for repo
  # root). e.g. 'app' or 'game\prototype'.
  GateDir = '.'

  # The integration GATE command. MUST exit 0 on PASS, non-zero on FAIL.
  # This is the WHOLE safety story -- a faster/looser worker makes a strong
  # gate MORE important. Make it build + test + a smoke run.
  #   e.g. 'node test/sim.mjs'  |  'npm test'  |  'pytest -q'  |  'cargo test'
  GateCmd = 'echo SET-GateCmd-IN-fleet.config.ps1 && exit 1'

  # Which coding agent drives the lanes by default: 'claude' or 'agy'
  # (Antigravity/Gemini). Per-lane overrides live in lanes.txt.
  Agent = 'claude'

  # Anti-circling pools the fleet (lanes + planner) injects. Filenames are
  # resolved next to the scripts. Swap to 'personas-gamedev.txt' /
  # 'nouns-gamedev.txt' for a game project.
  Personas = 'personas.txt'
  Nouns    = 'nouns.txt'

  # git stash message used if start-fleet auto-stashes a dirty main tree.
  AutostashTag = 'FLEET_AUTOSTASH'
}

# --- resolution helper used by every fleet script ---------------------------
# Fills a param from $FleetConfig ONLY when the caller didn't pass it and it's
# still empty/default. $boundKeys = $PSBoundParameters.Keys from the caller.
function Resolve-FleetDefault {
  param([string]$Name, $Current, $boundKeys, $Default)
  if ($boundKeys -contains $Name) { return $Current }   # caller set it explicitly
  if ($null -ne $Current -and "$Current" -ne '') { return $Current }
  return $Default
}
