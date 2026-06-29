<#
.SYNOPSIS
  Live fleet dashboard. Refreshes every few seconds: per-lane iteration + ahead-count + last commit,
  whether each lane is alive, a tail of the most-recently-active agy log (what an agent is doing RIGHT
  NOW), and the refinery's latest activity. Read-only - safe to run alongside a live fleet.

  Root + Base come from fleet.config.ps1; lane names default to lanes.txt.

.EXAMPLE
  ./watch-fleet.ps1
  ./watch-fleet.ps1 -IntervalSeconds 3
#>
[CmdletBinding()]
param(
  [string]$Base,
  [string]$Root,
  [string[]]$Names,
  [int]$IntervalSeconds = 4
)
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8

$scriptDir = $PSScriptRoot
if (-not $scriptDir -and $PSCommandPath) { $scriptDir = Split-Path -Parent $PSCommandPath }
if (-not $scriptDir) { $scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path }
. (Join-Path $scriptDir 'fleet.config.ps1')
$bk   = $PSBoundParameters.Keys
$Base = Resolve-FleetDefault 'Base' $Base $bk $FleetConfig.Base
$Root = Resolve-FleetDefault 'Root' $Root $bk $FleetConfig.Root
if (-not $Names) {
  $manifest = Join-Path $scriptDir 'lanes.txt'
  $Names = Get-Content $manifest -ErrorAction SilentlyContinue | ForEach-Object { $_.Trim() } |
    Where-Object { $_ -and -not $_.StartsWith('#') } | ForEach-Object { ($_ -split '\s+')[0] }
}

try {
  while ($true) {
    Clear-Host
    $agy = @(Get-Process agy -ErrorAction SilentlyContinue).Count
    Write-Host ("=== FLEET  {0}   trunk {1}   agy running: {2} ===" -f (Get-Date -Format 'HH:mm:ss'), (git -C $Root rev-parse --short HEAD), $agy) -ForegroundColor Cyan
    Write-Host ""
    $procs = Get-CimInstance Win32_Process -Filter "Name='powershell.exe'" -ErrorAction SilentlyContinue
    foreach ($l in $Names) {
      $br = "ralph-$l"
      $ahead = git -C $Root rev-list --count "$Base..$br" 2>$null
      $agyCommits = @(git -C $Root log "$Base..$br" --oneline 2>$null | Select-String '\[agy\]').Count
      $last  = git -C $Root log -1 --format='%cr | %s' $br 2>$null
      $alive = [bool]($procs | Where-Object { $_.CommandLine -match "wt-$l\\ralph\\ralph.ps1" })
      $logs = Get-ChildItem "$Root-wt-$l\ralph\logs\iter-*.log" -ErrorAction SilentlyContinue | Sort-Object LastWriteTime
      $iter = if ($logs) { ($logs[-1].BaseName -split '-')[1] } else { '----' }
      $col = if ($alive) { 'Green' } else { 'DarkGray' }
      Write-Host ("  {0,-9} {1,-5} iter {2}  ahead {3,-3} agyCommits {4,-3} | {5}" -f $l, $(if($alive){'LIVE'}else{'idle'}), $iter, $ahead, $agyCommits, $last) -ForegroundColor $col
    }
    Write-Host ""
    Write-Host "--- newest agent activity (agy log) ---" -ForegroundColor Cyan
    $agyLog = Get-ChildItem "$Root-wt-*\ralph\logs\agy-*.log" -ErrorAction SilentlyContinue | Sort-Object LastWriteTime | Select-Object -Last 1
    if ($agyLog) {
      Write-Host ("  {0}  ({1}s ago)" -f $agyLog.Name, [int]((Get-Date) - $agyLog.LastWriteTime).TotalSeconds) -ForegroundColor DarkGray
      Get-Content $agyLog.FullName -Tail 10 -ErrorAction SilentlyContinue | ForEach-Object { "    $_" }
    } else { Write-Host "  (no agy logs yet -- claude lanes have none; check per-lane iter-*.log)" -ForegroundColor DarkGray }
    Write-Host ""
    Write-Host "--- refinery ---" -ForegroundColor Cyan
    $rl = Get-ChildItem "$Root\ralph\logs\refinery-*.log" -ErrorAction SilentlyContinue | Sort-Object LastWriteTime | Select-Object -Last 1
    if ($rl) { Get-Content $rl.FullName -Tail 6 -ErrorAction SilentlyContinue | ForEach-Object { "  $_" } } else { Write-Host "  (refinery not started)" -ForegroundColor DarkGray }
    Write-Host ""
    Write-Host "(Ctrl-C to stop watching; the fleet keeps running)" -ForegroundColor DarkGray
    Start-Sleep -Seconds $IntervalSeconds
  }
} finally { Write-Host "watch stopped." -ForegroundColor Cyan }
