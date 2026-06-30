<#
.SYNOPSIS
  Start the single-agent Workshop loop DETACHED, driving the repo from workshop.config.ps1.

.DESCRIPTION
  One agent at a time, fresh context each pass, anti-circling on, draining the Workshop backlog
  toward GOAL.md. Unlike the fleet there are NO worktrees / lanes / refinery — just workshop.ps1.

  Reads its knobs from workshop.config.ps1 (Root / Agent / Model / pools). Spawns a hidden detached
  window with WorkingDirectory = Root so the loop's git add/commit land on your repo, and returns
  immediately (so a web UI's start call doesn't block on the loop).

.EXAMPLE
  ./start-workshop.ps1               # infinite (stop via stop-workshop.ps1)
  ./start-workshop.ps1 -Iterations 5 # bounded smoke run
#>
[CmdletBinding()]
param(
  [int]$Iterations = 0,                       # 0 = run forever
  [string]$Root = '',                          # override config Root (the repo the agent works in)
  [string]$Agent = '',                         # override first-pass agent (claude|agy|auto)
  [string]$Model = '',                         # override first-pass model
  [int]$SleepSeconds = 0
)
$ErrorActionPreference = 'Stop'
$ws  = $PSScriptRoot
if (-not $ws) { $ws = Split-Path -Parent $PSCommandPath }
. (Join-Path $ws 'workshop.config.ps1')

$bound  = $PSBoundParameters.Keys
$Root   = Resolve-WorkshopDefault 'Root'  $Root  $bound $WorkshopConfig.Root
$Agent  = Resolve-WorkshopDefault 'Agent' $Agent $bound $WorkshopConfig.Agent
$Model  = Resolve-WorkshopDefault 'Model' $Model $bound $WorkshopConfig.Model
if (-not $Root -or $Root -eq 'C:\path\to\your\repo' -or -not (Test-Path $Root)) {
  Write-Host "Set Root to your repo's absolute path in workshop.config.ps1 (currently: '$Root')." -ForegroundColor Red
  return
}

$engine = Join-Path $ws 'workshop.ps1'
$ctl    = Join-Path $ws 'agent.json'
$personas = Join-Path $ws ([string]$WorkshopConfig.Personas)
$nouns    = Join-Path $ws ([string]$WorkshopConfig.Nouns)

# Already running? Don't stack a second loop (two agents would race the same files + bookkeeping).
# Match the ENGINE only: `\workshop.ps1` (path-sep before). Plain 'workshop\.ps1' would also match the
# substring inside `start-workshop.ps1` / `stop-workshop.ps1`, false-positiving on this very script.
$existing = @(Get-CimInstance Win32_Process -Filter "Name='powershell.exe'" -ErrorAction SilentlyContinue |
  Where-Object { $_.CommandLine -match '[\\/]workshop\.ps1' })
if ($existing.Count) {
  Write-Host "Workshop loop already running (PID $($existing[0].ProcessId)). Stop it first." -ForegroundColor Yellow
  return
}

# agent.json is the live agent/model selection (UI-editable). The loop re-reads it every iteration
# (-AgentControlFile) so the operator can switch model for the NEXT pass with no restart. Seed it if
# missing; the initial -Agent/-Model below just sets the FIRST pass before the control file is read.
# MUST be BOM-free: Node JSON.parse (the UI server) chokes on a UTF-8 BOM, and Set-Content -Encoding
# utf8 writes one in PS5.1. Write raw bytes via UTF8Encoding($false).
if (-not (Test-Path $ctl)) {
  $seed = '{ "agent": "' + $Agent + '", "model": "' + $Model + '" }'
  [System.IO.File]::WriteAllText($ctl, $seed, (New-Object System.Text.UTF8Encoding($false)))
}
try {
  $sel = Get-Content -Raw -Path $ctl | ConvertFrom-Json
  if ($sel.agent) { $Agent = [string]$sel.agent }
  if ($sel.model) { $Model = [string]$sel.model }
} catch {}

$loopArgs = @(
  '-NoProfile','-ExecutionPolicy','Bypass','-File', $engine,
  '-Random',
  '-Prompt',           (Join-Path $ws 'PROMPT.md'),
  '-Personas',         $personas,
  '-Nouns',            $nouns,
  '-LogDir',           (Join-Path $ws 'logs'),
  '-Agent',            $Agent,
  '-Model',            $Model,
  '-AgentControlFile', $ctl
)
if ($Iterations -gt 0)   { $loopArgs += @('-Iterations', $Iterations) }
if ($SleepSeconds -gt 0) { $loopArgs += @('-SleepSeconds', $SleepSeconds) }

New-Item -ItemType Directory -Force -Path (Join-Path $ws 'logs') | Out-Null

# Optionally check out the configured Branch in Root first, so commits land where the operator wants.
$branch = [string]$WorkshopConfig.Branch
if ($branch) {
  try { git -C $Root rev-parse --verify $branch *> $null; if ($LASTEXITCODE -eq 0) { git -C $Root checkout $branch *> $null } } catch {}
}

# WorkingDirectory = Root so the loop's git add/commit (+ any Stop hook) land on your repo.
Start-Process powershell -ArgumentList $loopArgs -WindowStyle Hidden -WorkingDirectory $Root
Write-Host "Workshop loop launched (root $Root, agent $Agent, model $Model, $(if($Iterations){"$Iterations iters"}else{'infinite'}))." -ForegroundColor Green
