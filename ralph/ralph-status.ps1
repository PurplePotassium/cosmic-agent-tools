<#
.SYNOPSIS
  One-shot "is the Ralph loop alive, and on which iteration?" -- the RELIABLE check.

.DESCRIPTION
  Do NOT judge ralph liveness from the log files. The per-iteration log is written only when a
  pass FINISHES (the loop captures claude's stdout to a variable and writes the file at the end),
  so a frozen newest-log / no-newer-log usually means MID-PASS, not stopped. Log mtimes are
  pass-END times, not start times. The authoritative liveness signal is the PROCESS TREE: a live
  `claude ... --dangerously-skip-permissions` process (that unattended flag is ralph's fingerprint;
  interactive claude sessions never set it) whose ancestor is the loop's PowerShell. This script
  checks that, cross-references the last `ralph iter N` commit (each completed pass commits one),
  and double-samples CPU so you can tell "actively working" from "possibly wedged".

  Root comes from fleet.config.ps1.

.EXAMPLE
  ./ralph-status.ps1
#>
[CmdletBinding()]
param(
  [string]$Root,
  [string]$LogDir,
  [int]   $WedgeMinutes = 20   # warn if a single pass has been running longer than this
)
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8

$scriptDir = $PSScriptRoot
if (-not $scriptDir -and $PSCommandPath) { $scriptDir = Split-Path -Parent $PSCommandPath }
if (-not $scriptDir) { $scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path }
. (Join-Path $scriptDir 'fleet.config.ps1')
$bk   = $PSBoundParameters.Keys
$Root = Resolve-FleetDefault 'Root' $Root $bk $FleetConfig.Root
if (-not $LogDir) { $LogDir = Join-Path $scriptDir 'logs' }

# 1. PROCESS -- ralph's claude carries the unattended flag; nothing interactive does.
$claudes = @(Get-CimInstance Win32_Process -Filter "Name='claude.exe'" -ErrorAction SilentlyContinue |
  Where-Object { $_.CommandLine -match 'dangerously-skip-permissions' })

function Get-PSAncestor($proc) {
  $cur = $proc
  for ($hop = 0; $hop -lt 6 -and $cur; $hop++) {
    $par = Get-CimInstance Win32_Process -Filter "ProcessId=$($cur.ParentProcessId)" -ErrorAction SilentlyContinue
    if (-not $par) { break }
    if ($par.Name -match 'powershell|pwsh') { return $par }
    $cur = $par
  }
  return $null
}

$live = foreach ($c in $claudes) {
  $ps   = Get-PSAncestor $c
  $proc = Get-Process -Id $c.ProcessId -ErrorAction SilentlyContinue
  [pscustomobject]@{
    PID_    = $c.ProcessId
    Started = $c.CreationDate
    Mins    = [int]((Get-Date) - $c.CreationDate).TotalMinutes
    CPU0    = if ($proc) { [double]$proc.CPU } else { 0 }
    PSPid   = if ($ps) { $ps.ProcessId } else { '?' }
  }
}
$live = @($live)

# 2. GIT -- each completed pass commits "ralph iter N: <date>". This is the true last-finished count
#    (survives the per-run log-numbering reset).
Push-Location $Root
$lastPass = (git log -1 --grep='^ralph iter' --format='%s|%cd' --date=format:'%H:%M:%S' 2>$null)
Pop-Location
$passN = $null; $passWhen = '?'
if ($lastPass -match '^ralph iter (\d+).*\|(.+)$') { $passN = [int]$Matches[1]; $passWhen = $Matches[2].Trim() }

# 3. LOG -- newest file is the last FINISHED pass; for content context only, NOT liveness.
$log = Get-ChildItem $LogDir -Filter *.log -ErrorAction SilentlyContinue | Sort-Object LastWriteTime -Descending | Select-Object -First 1

Write-Host "=== Ralph status ===" -ForegroundColor Cyan
if ($live.Count) {
  $l = $live | Sort-Object Started -Descending | Select-Object -First 1
  # progress probe: re-sample CPU after a moment. Rising = actively computing; flat MAY just be
  # waiting on the model (network-bound), so flat is not proof of a wedge -- the minutes check is.
  Start-Sleep -Milliseconds 1500
  $p2  = Get-Process -Id $l.PID_ -ErrorAction SilentlyContinue
  $dCPU = if ($p2) { [math]::Round(([double]$p2.CPU - $l.CPU0), 2) } else { 0 }
  $next = if ($passN) { $passN + 1 } else { '?' }
  Write-Host ("RUNNING  claude pid {0} (parent PS {1}), {2} min into this pass" -f $l.PID_, $l.PSPid, $l.Mins) -ForegroundColor Green
  Write-Host ("  last COMPLETED: ralph iter {0} at {1}  ->  currently on ~iter {2}" -f $passN, $passWhen, $next)
  Write-Host ("  CPU +{0}s over 1.5s ({1})" -f $dCPU, $(if ($dCPU -gt 0) { 'computing' } else { 'idle/0 -- may just be waiting on the model' }))
  if ($l.Mins -ge $WedgeMinutes) {
    Write-Host ("  WARNING: pass running {0} min (>= {1}). Heavy pass or wedged -- if CPU stays 0 and no commit lands, suspect a stuck child holding the pipe." -f $l.Mins, $WedgeMinutes) -ForegroundColor Yellow
  }
} else {
  Write-Host "NOT RUNNING  -- no 'claude --dangerously-skip-permissions' process found." -ForegroundColor Red
  if ($passN) { Write-Host ("  last COMPLETED: ralph iter {0} at {1}" -f $passN, $passWhen) }
}
if ($log) {
  $firstLine = (Get-Content $log.FullName -ErrorAction SilentlyContinue | Where-Object { $_.Trim() } | Select-Object -First 1)
  Write-Host ("  newest log (last FINISHED pass): {0} @ {1}" -f $log.Name, $log.LastWriteTime.ToString('HH:mm:ss')) -ForegroundColor DarkGray
  if ($firstLine) { Write-Host ("    {0}" -f $firstLine.Substring(0, [Math]::Min(100, $firstLine.Length))) -ForegroundColor DarkGray }
}
Write-Host "  (logs appear only when a pass ENDS; a frozen/absent log != stopped -- trust the process check above.)" -ForegroundColor DarkGray
