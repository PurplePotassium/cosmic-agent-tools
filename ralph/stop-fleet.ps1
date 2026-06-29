<#
.SYNOPSIS
  Stop the hybrid fleet: kill the agy workers + the PowerShell windows running the ralph loops
  (lane loops, refinery, planner, start-fleet). Targeted -- it only touches powershell processes whose
  command line references one of our scripts, so it won't kill unrelated shells or this launcher.

  Root + autostash tag come from fleet.config.ps1.

.EXAMPLE
  ./stop-fleet.ps1
#>
$ErrorActionPreference = 'SilentlyContinue'

$scriptDir = $PSScriptRoot
if (-not $scriptDir -and $PSCommandPath) { $scriptDir = Split-Path -Parent $PSCommandPath }
if (-not $scriptDir) { $scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path }
. (Join-Path $scriptDir 'fleet.config.ps1')
$Root     = if ($FleetConfig.Root) { $FleetConfig.Root } else { (Get-Location).Path }
$stashTag = if ($FleetConfig.AutostashTag) { $FleetConfig.AutostashTag } else { 'FLEET_AUTOSTASH' }

# 1. agy worker processes (Antigravity CLI), if any
$agy = Get-Process agy -ErrorAction SilentlyContinue
if ($agy) { $agy | Stop-Process -Force; Write-Host "stopped $($agy.Count) agy process(es)" -ForegroundColor Yellow }

# 2. the loop windows -- powershell.exe whose command line runs one of our ralph scripts
$mine = Get-CimInstance Win32_Process -Filter "Name = 'powershell.exe'" |
  Where-Object { $_.CommandLine -and $_.CommandLine -match '(ralph|refinery|plan|start-fleet)\.ps1' }
foreach ($p in $mine) {
  Stop-Process -Id $p.ProcessId -Force -ErrorAction SilentlyContinue
  Write-Host "stopped loop PID $($p.ProcessId)" -ForegroundColor Yellow
}

# 3. restore autostash if present
$stashes = git -C $Root stash list
if ($stashes) {
  $topStash = $stashes | Select-Object -First 1
  if ($topStash -match [regex]::Escape($stashTag)) {
    Write-Host "Restoring auto-stashed changes..." -ForegroundColor Yellow
    git -C $Root stash pop | Out-Host
  }
}

Write-Host "Fleet stopped ($($mine.Count) loop window(s)). Trunk + worktrees are left intact." -ForegroundColor Cyan
