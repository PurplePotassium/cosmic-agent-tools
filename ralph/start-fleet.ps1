<#
.SYNOPSIS
  One-command launcher for the hybrid fleet: spins up every lane worker + the refinery (merge agent),
  each in its own window, and optionally the planner.

  This is the "let me test it" entry point. It calls ralph-fleet.ps1 (which creates the worktrees +
  per-lane PROMPT.md and launches a loop per lane) then starts refinery.ps1 in the MAIN worktree.

  PROJECT KNOBS (Root, Base, autostash tag) come from fleet.config.ps1; CLI params override.

.EXAMPLE
  ./start-fleet.ps1 -LaneIterations 3 -RefineryIterations 12 -RefineryInterval 45
      # BOUNDED TEST: each lane does 3 passes; the refinery polls 12 times @45s, then exits.

.EXAMPLE
  ./start-fleet.ps1 -WithPlanner
      # Open-ended run: lanes + refinery (60s) + a periodic planner. Ctrl-C each window to stop.

.NOTES
  Pre-reqs: the worker agent must be installed + authed (`claude` and/or `agy` on PATH), and your
  configured GATE must pass from a clean checkout. The main worktree must be clean (the refinery resets
  it). If using a subscription agent, quota is finite -- a bounded test (-LaneIterations) is the safe
  way to start.
#>
[CmdletBinding()]
param(
  [string]$Base,                          # trunk branch (default: fleet.config.ps1)
  [string]$Root,                          # main repo worktree (default: fleet.config.ps1)
  [string[]]$Names,                       # subset of lanes.txt (default = all)
  [int]$LaneIterations = 0,               # passes per lane (0 = infinite)
  [int]$RefineryIterations = 0,           # refinery rounds (0 = forever)
  [int]$RefineryInterval = 60,            # refinery poll cadence (s)
  [int]$LaunchStaggerSeconds = 8,         # gap between lane launches (auth/keyring de-collision)
  [switch]$WithPlanner,                   # also launch the planner loop
  [int]$PlannerSleepSeconds = 1800,       # planner cadence when -WithPlanner (default 30 min)
  [switch]$Hidden,                        # launch powershell processes hidden
  [switch]$Random,                        # launch planner with random personas/nouns
  [ValidateSet('claude','agy','')][string]$Agent = '',  # worker agent override
  [string]$Model = ''                     # worker model override
)
$ErrorActionPreference = 'Stop'

# --- resolve script dir + load project config ------------------------------------------------------
$scriptDir = $PSScriptRoot
if (-not $scriptDir -and $PSCommandPath) { $scriptDir = Split-Path -Parent $PSCommandPath }
if (-not $scriptDir) { $scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path }
. (Join-Path $scriptDir 'fleet.config.ps1')
$bk   = $PSBoundParameters.Keys
$Base = Resolve-FleetDefault 'Base' $Base $bk $FleetConfig.Base
$Root = Resolve-FleetDefault 'Root' $Root $bk $FleetConfig.Root
$stashTag = if ($FleetConfig.AutostashTag) { $FleetConfig.AutostashTag } else { 'FLEET_AUTOSTASH' }
$ralphDir = $scriptDir

# Safety: refuse to start if the main worktree is dirty outside ralph/ -- the refinery resets it and would lose work.
$dirtyLines = @(git -C $Root status --porcelain -uno)
$dirty = @($dirtyLines | Where-Object { $_.Trim() -and $_.Trim() -notmatch '^[MADRCU?\s]+\s+ralph/' })
if ($dirty) {
  Write-Host "Main worktree has uncommitted changes. Auto-stashing..." -ForegroundColor Yellow
  git -C $Root stash push -m $stashTag | Out-Host
  if ($LASTEXITCODE -ne 0) {
    Write-Error "Failed to auto-stash changes. Aborting."
  }
}

Write-Host "=== Launching fleet: lane workers + refinery$(if ($WithPlanner) {' + planner'}) ===" -ForegroundColor Cyan

# 1. lanes (creates worktrees + per-lane PROMPT.md, launches a window each)
$fleetArgs = @{ Base = $Base; Root = $Root; Launch = $true; LaunchStaggerSeconds = $LaunchStaggerSeconds; LaneIterations = $LaneIterations }
if ($Names) { $fleetArgs.Names = $Names }
if ($Hidden) { $fleetArgs.Hidden = $true }
if ($Agent) { $fleetArgs.Agent = $Agent }
if ($Model) { $fleetArgs.Model = $Model }
& (Join-Path $ralphDir 'ralph-fleet.ps1') @fleetArgs

# 2. refinery (the merge agent) -- owns the MAIN worktree, separate window
$refArgs = @('-NoExit', '-File', (Join-Path $ralphDir 'refinery.ps1'),
             '-Base', $Base, '-Root', $Root, '-IntervalSeconds', $RefineryInterval, '-Iterations', $RefineryIterations)
if ($Names) { $refArgs += @('-Names', ($Names -join ',')) }

$startRefArgs = @{
    FilePath = 'powershell'
    WorkingDirectory = $Root
    ArgumentList = $refArgs
}
if ($Hidden) { $startRefArgs.WindowStyle = 'Hidden' }
Start-Process @startRefArgs
Write-Host "launched refinery (poll ${RefineryInterval}s, iterations=$RefineryIterations, hidden=$Hidden)" -ForegroundColor Yellow

# 3. optional planner -- refills/repartitions the backlog
if ($WithPlanner) {
  $plannerList = @('-NoExit', '-File', (Join-Path $ralphDir 'plan.ps1'), '-Iterations', '0', '-SleepSeconds', $PlannerSleepSeconds,
                   '-Prompt', (Join-Path $ralphDir 'PLAN-PROMPT.md'), '-LogDir', (Join-Path $ralphDir 'logs'))
  if ($Random) { $plannerList += '-Random' }
  $startPlannerArgs = @{
      FilePath = 'powershell'
      WorkingDirectory = $Root
      ArgumentList = $plannerList
  }
  if ($Hidden) { $startPlannerArgs.WindowStyle = 'Hidden' }
  Start-Process @startPlannerArgs
  Write-Host "launched planner (every ${PlannerSleepSeconds}s, hidden=$Hidden, random=$Random)" -ForegroundColor Yellow
}

Write-Host ""
Write-Host "Up: lane windows + refinery$(if ($WithPlanner) {' + planner'}). Watch trunk go green:" -ForegroundColor Cyan
Write-Host "  git -C $Root log --oneline -15" -ForegroundColor DarkGray
Write-Host "  ./watch-fleet.ps1" -ForegroundColor DarkGray
Write-Host "Stop: ./stop-fleet.ps1  (or Ctrl-C each window)." -ForegroundColor DarkGray
