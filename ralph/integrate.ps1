<#
.SYNOPSIS
  One-shot: merge the parallel-Ralph lane branches back into the trunk and run the full gate.

  Append-only docs (DONE/NOTES/FEEL) auto-resolve via the `merge=union` driver (see SETUP.md). CODE
  seams can genuinely conflict -- this script merges one lane at a time and STOPS on the first conflict
  so you can resolve it by hand, then re-run. After all lanes merge cleanly it runs the configured GATE:
  if that FAILs, two lane changes combined badly -- the per-lane commits are clean, so bisect by merging
  one lane only. For a continuous auto-merging integrator use refinery.ps1 instead.

  PROJECT KNOBS (Root, Base, GateDir, GateCmd) come from fleet.config.ps1; CLI params override.

.EXAMPLE
  ./integrate.ps1
#>
[CmdletBinding()]
param(
  [string]$Base,                         # trunk branch (default: fleet.config.ps1)
  [string]$Root,                         # main worktree (default: fleet.config.ps1)
  [string[]]$Lanes,                      # default: all lanes in lanes.txt
  [string]$GateDir,                      # cwd for the gate, relative to Root (default: config)
  [string]$GateCmd                       # the gate (must exit 0 = PASS) (default: config)
)
$ErrorActionPreference = 'Stop'

# --- resolve script dir + load project config ------------------------------------------------------
$scriptDir = $PSScriptRoot
if (-not $scriptDir -and $PSCommandPath) { $scriptDir = Split-Path -Parent $PSCommandPath }
if (-not $scriptDir) { $scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path }
. (Join-Path $scriptDir 'fleet.config.ps1')
$bk      = $PSBoundParameters.Keys
$Base    = Resolve-FleetDefault 'Base'    $Base    $bk $FleetConfig.Base
$Root    = Resolve-FleetDefault 'Root'    $Root    $bk $FleetConfig.Root
$GateDir = Resolve-FleetDefault 'GateDir' $GateDir $bk $FleetConfig.GateDir
$GateCmd = Resolve-FleetDefault 'GateCmd' $GateCmd $bk $FleetConfig.GateCmd
if (-not $GateDir) { $GateDir = '.' }

# Default the lane list from the shared manifest so this one-shot stays in sync with ralph-fleet.ps1
# and refinery.ps1 (which read the same file).
if (-not $Lanes) {
  $manifest = Join-Path $scriptDir 'lanes.txt'
  $Lanes = Get-Content $manifest | ForEach-Object { $_.Trim() } |
    Where-Object { $_ -and -not $_.StartsWith('#') } |
    ForEach-Object { ($_ -split '\s+')[0] }
}

git -C $Root checkout $Base
foreach ($lane in $Lanes) {
  $branch = "ralph-$lane"
  Write-Host "=== merging $branch -> $Base ===" -ForegroundColor Cyan
  git -C $Root merge --no-edit $branch
  if ($LASTEXITCODE -ne 0) {
    Write-Host "CONFLICT merging $branch. Resolve the listed files, `git add` them, `git commit`," -ForegroundColor Red
    Write-Host "then re-run this script (already-merged lanes are no-ops)." -ForegroundColor Red
    git -C $Root --no-pager diff --name-only --diff-filter=U
    exit 1
  }
  Write-Host "  merged clean" -ForegroundColor Green
}

Write-Host "=== integration gate: $GateCmd  (in $GateDir) ===" -ForegroundColor Cyan
Push-Location (Join-Path $Root $GateDir)
try { Invoke-Expression $GateCmd; $gate = $LASTEXITCODE } finally { Pop-Location }

if ($gate -eq 0) {
  Write-Host "`nINTEGRATION PASS -- trunk is green." -ForegroundColor Green
} else {
  Write-Host "`nINTEGRATION FAIL -- lanes individually passed but COMBINED they regress." -ForegroundColor Red
  Write-Host "Bisect: reset trunk, merge ONE lane, gate; repeat to find the bad pair." -ForegroundColor Red
  exit 1
}
