<#
.SYNOPSIS
  Parallel Ralph -- spawn an ARBITRARY number of category-scoped Ralph loops, each in its own git
  worktree + branch, all at once. The fan-out is data-driven: the lane list lives in `lanes.txt`, so
  adding a lane is one line there + a `lane-<name>.md` header (copy `lane-template.md`), no edit here.

  Single-loop Ralph is serial (read repo -> 1 increment -> commit -> repeat). This fans it out: each
  lane gets its own worktree + branch + a category-scoped PROMPT, so several loops make progress
  simultaneously without stepping on each other. Each lane is still strictly 1-increment-per-pass with
  clean commits, so bisectability is preserved. Run `refinery.ps1` (loop) or `integrate.ps1` (one-shot)
  to merge the lane branches back into the trunk under a gate.

  Two conflict sources are pre-handled:
    * append-only docs (DONE/NOTES/FEEL) -> a `merge=union` .gitattributes entry (see SETUP.md).
    * the base PROMPT.md hardcodes your repo path -> this script rewrites it to each worktree root
      so a lane edits ITS OWN tree, not the trunk.

  PROJECT KNOBS (Root, Base, default Agent, pools) come from fleet.config.ps1; CLI params override.

.EXAMPLE
  # create every worktree in lanes.txt but DON'T launch yet (inspect first)
  ./ralph-fleet.ps1

.EXAMPLE
  # create AND launch a loop per lane, each in its own window (per-lane model from lanes.txt)
  ./ralph-fleet.ps1 -Launch

.EXAMPLE
  # only the api + ui lanes
  ./ralph-fleet.ps1 -Launch -Names api,ui

.NOTES
  STOP any single-loop Ralph first -- otherwise it keeps editing the trunk while lanes run, doubling
  work. The trunk branch is the integration target, not a lane, and refinery.ps1 owns the main worktree.
#>
[CmdletBinding()]
param(
  [string]$Base,                            # trunk branch (default: fleet.config.ps1)
  [string]$Root,                            # main repo worktree (default: fleet.config.ps1)
  [string[]]$Names,                         # subset of lanes.txt to act on (default = all)
  [switch]$Launch,                          # also start a loop per lane (else just create worktrees)
  [string]$Model = '',                      # override or fallback model ('' = agent default)
  [int]$LaunchStaggerSeconds = 8,           # gap between launching lanes (avoids a thundering-herd auth/keyring hit)
  [int]$LaneIterations = 0,                 # passes per lane (0 = infinite); set e.g. 3 for a bounded test
  [string]$Manifest,                        # lane manifest (default: lanes.txt next to this script)
  [switch]$Hidden,                          # launch powershell processes hidden
  [ValidateSet('claude','agy','')][string]$Agent = ''  # worker agent override
)
$ErrorActionPreference = 'Stop'
# Read/write everything as UTF-8 so the em-dashes etc. in the headers + base prompt survive into each
# lane's PROMPT.md (PS5.1 otherwise decodes source files as cp1252 and bakes in mojibake).
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8

# --- resolve script dir + load project config ------------------------------------------------------
$scriptDir = $PSScriptRoot
if (-not $scriptDir -and $PSCommandPath) { $scriptDir = Split-Path -Parent $PSCommandPath }
if (-not $scriptDir) { $scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path }
. (Join-Path $scriptDir 'fleet.config.ps1')
$bk   = $PSBoundParameters.Keys
$Base = Resolve-FleetDefault 'Base' $Base $bk $FleetConfig.Base
$Root = Resolve-FleetDefault 'Root' $Root $bk $FleetConfig.Root

$ralphDir = Join-Path $Root 'ralph'
if (-not $Manifest) { $Manifest = Join-Path $scriptDir 'lanes.txt' }
$poolPersonas = if ($FleetConfig.Personas) { $FleetConfig.Personas } else { 'personas.txt' }
$poolNouns    = if ($FleetConfig.Nouns)    { $FleetConfig.Nouns }    else { 'nouns.txt' }

# --- parse lanes.txt: "<name> [agent=..] [model=..] [header=..]"  (# comments / blanks ignored) -----
function Read-Lanes([string]$path, [string]$overrideAgent, [string]$overrideModel) {
  if (-not (Test-Path $path)) { throw "lane manifest not found: $path" }
  Get-Content $path | ForEach-Object {
    $line = $_.Trim()
    if (-not $line -or $line.StartsWith('#')) { return }
    $tok = $line -split '\s+'
    $name = $tok[0]
    $kv = @{}
    if ($tok.Count -gt 1) { foreach ($t in $tok[1..($tok.Count - 1)]) { if ($t -match '^(\w+)=(.+)$') { $kv[$matches[1]] = $matches[2] } } }

    $defaultAgent = if ($FleetConfig.Agent) { $FleetConfig.Agent } else { 'claude' }
    $rowAgent = if ($overrideAgent) { $overrideAgent } elseif ($kv.ContainsKey('agent')) { $kv['agent'] } else { $defaultAgent }

    $rowModel = ''
    if ($overrideModel) {
      $rowModel = $overrideModel
    } elseif (-not $overrideAgent) {
      $rowModel = if ($kv.ContainsKey('model')) { $kv['model'] } else { '' }
    }

    [pscustomobject]@{
      name   = $name
      agent  = $rowAgent
      model  = $rowModel
      header = if ($kv.ContainsKey('header')) { $kv['header'] } else { "lane-$name.md" }
    }
  }
}
$lanes = @(Read-Lanes $Manifest $Agent $Model)
if ($Names) { $lanes = @($lanes | Where-Object { $Names -contains $_.name }) }
if (-not $lanes) { throw "no lanes selected (manifest empty, or -Names matched nothing)" }

$basePromptFile = Join-Path $ralphDir 'PROMPT.md'
if (-not (Test-Path $basePromptFile)) { throw "base prompt not found: $basePromptFile  (copy PROMPT.example.md -> PROMPT.md and edit it for your project)" }
$basePrompt = Get-Content -Raw -Encoding utf8 -Path $basePromptFile   # canonical single-loop prompt
git -C $Root worktree prune | Out-Null                                  # drop stale admin for worktrees whose
                                                                        # dirs were deleted, so add/checkout won't error
$existing   = (git -C $Root worktree list) -join "`n"

foreach ($lane in $lanes) {
  $branch = "ralph-$($lane.name)"
  $path   = "$Root-wt-$($lane.name)"        # e.g. C:\repo-wt-ui
  $modelLabel = if ($lane.model) { $lane.model } else { '(agent default)' }
  Write-Host "=== lane: $($lane.name)  branch: $branch  dir: $path  agent: $($lane.agent)  model: $modelLabel ===" -ForegroundColor Cyan

  # 1. worktree (create the branch off the trunk if this is the first time). Normalise slashes: `git
  #    worktree list` prints forward slashes, $path uses backslashes -- a raw match would miss existing
  #    worktrees and try to re-add them (fatal).
  if (($existing -replace '\\', '/') -match [regex]::Escape(($path -replace '\\', '/'))) {
    Write-Host "  worktree exists -- reusing" -ForegroundColor DarkGray
  } else {
    if (git -C $Root branch --list $branch) {
      git -C $Root worktree add $path $branch | Out-Null
    } else {
      git -C $Root worktree add -b $branch $path $Base | Out-Null
    }
    Write-Host "  worktree created" -ForegroundColor Green
  }

  # 2. build this lane's PROMPT.md INSIDE its worktree: scoping header + base prompt with every
  #    repo-root path rewritten to the worktree root (so the lane edits its own tree).
  $headerPath = Join-Path $ralphDir $lane.header
  if (-not (Test-Path $headerPath)) {
    throw "lane '$($lane.name)' header not found: $headerPath  (copy lane-template.md and scope its files/TODO sections)"
  }
  $header    = Get-Content -Raw -Encoding utf8 -Path $headerPath
  $rewritten = $basePrompt.Replace($Root, $path)   # literal path rewrite -> lane edits ITS OWN tree
  $laneRalph = Join-Path $path 'ralph'
  New-Item -ItemType Directory -Force -Path $laneRalph | Out-Null
  Set-Content -Path (Join-Path $laneRalph 'PROMPT.md') -Value ($header + "`n`n" + $rewritten) -Encoding utf8
  Write-Host "  PROMPT.md written (paths -> $path)" -ForegroundColor Green

  # 3. optionally launch the loop in its own window, CWD = the worktree (so the agent scopes there).
  if ($Launch) {
    # Pass script-relative paths EXPLICITLY (absolute) so the lane never depends on $PSScriptRoot, which
    # comes through empty under some launch mechanisms -- that would yield a bogus "/PROMPT.md" AND empty
    # persona/noun pools (every pass falling back to the default = no anti-circling variety).
    $laneArgs = @('-NoExit', '-File', (Join-Path $laneRalph 'ralph.ps1'),
                  '-Prompt',   (Join-Path $laneRalph 'PROMPT.md'),
                  '-LogDir',   (Join-Path $laneRalph 'logs'),
                  '-Personas', (Join-Path $laneRalph $poolPersonas),
                  '-Nouns',    (Join-Path $laneRalph $poolNouns),
                  '-Random', '-Agent', $lane.agent, '-SyncBase', $Base)
    if ($lane.model)            { $laneArgs += @('-Model', $lane.model) }
    if ($LaneIterations -gt 0)  { $laneArgs += @('-Iterations', $LaneIterations) }
    $startProcArgs = @{
        FilePath = 'powershell'
        WorkingDirectory = $path
        ArgumentList = $laneArgs
    }
    if ($Hidden) { $startProcArgs.WindowStyle = 'Hidden' }
    Start-Process @startProcArgs
    Write-Host "  launched loop (agent=$($lane.agent), model=$modelLabel, -Random, hidden=$Hidden)" -ForegroundColor Yellow
    # Stagger the next launch: N agy lanes hitting the Windows Credential Manager / token refresh at the
    # same instant can race. A few seconds apart side-steps it (and is the cheap mitigation we control).
    if ($LaunchStaggerSeconds -gt 0) { Start-Sleep -Seconds $LaunchStaggerSeconds }
  }
}

Write-Host ""
Write-Host "Lanes: $($lanes.name -join ', ')" -ForegroundColor Cyan
if (-not $Launch) { Write-Host "Launch all:  ./ralph-fleet.ps1 -Launch" -ForegroundColor Cyan }
Write-Host "Integrate (loop):  ./refinery.ps1     one-shot:  ./integrate.ps1" -ForegroundColor Cyan
