<#
.SYNOPSIS
  One-shot JSON status for the single-agent Workshop loop (for the web UI to poll).

.DESCRIPTION
  Liveness from the PROCESS TREE, not log mtimes (logs land only at pass END, so a frozen log is
  usually MID-PASS, not stopped). Real-time signals so the operator can SEE it working between pass
  boundaries: is the agent child computing right now (CPU rising) or waiting on the model, how many
  seconds into the pass, which files it has edited but not committed (dirty tree), the current task
  (top of backlog), and a feed of recent commits. Emits one compact JSON object.
#>
[CmdletBinding()]
param(
  [string]$Root   = '',
  [string]$LogDir = '',
  [int]$WedgeMinutes = 0
)
$ErrorActionPreference = 'SilentlyContinue'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8

# Resolve the script dir robustly ($PSScriptRoot comes through EMPTY under some -File launches), then
# derive paths + config from it.
$wsHome = $PSScriptRoot
if (-not $wsHome -and $PSCommandPath) { $wsHome = Split-Path -Parent $PSCommandPath }
if (-not $wsHome) { $wsHome = (Get-Location).Path }
. (Join-Path $wsHome 'workshop.config.ps1')
if (-not $Root)         { $Root = [string]$WorkshopConfig.Root }
if (-not $LogDir)       { $LogDir = Join-Path $wsHome 'logs' }
if ($WedgeMinutes -le 0){ $WedgeMinutes = [int]$WorkshopConfig.WedgeMinutes; if ($WedgeMinutes -le 0) { $WedgeMinutes = 20 } }

$backlogPath  = Join-Path $wsHome 'backlog.json'
$agentPath    = Join-Path $wsHome 'agent.json'
$progressPath = Join-Path $wsHome 'progress.json'

# 1. Loop wrapper + its in-flight agent child (the unattended flag is the loop's fingerprint). The
#    child is claude.exe (claude passes) OR agy.exe (Gemini passes) — match either so the Live Activity
#    panel works regardless of which agent is driving this pass.
$loops = @(Get-CimInstance Win32_Process -Filter "Name='powershell.exe'" |
  Where-Object { $_.CommandLine -match '[\\/]workshop\.ps1' })   # engine only; not start-/stop-workshop.ps1
$loopPids = $loops | ForEach-Object { $_.ProcessId }
$agentProc = @(Get-CimInstance Win32_Process -Filter "Name='claude.exe' OR Name='agy.exe'" |
  Where-Object { $_.CommandLine -match 'dangerously-skip-permissions' -and $loopPids -contains $_.ParentProcessId }) |
  Select-Object -First 1

$alive   = [bool]($loops.Count)
$loopPid = if ($loops.Count) { $loops[0].ProcessId } else { $null }

# Pass timing + "computing vs waiting" probe. CPU rising over a short sample = actively working; flat
# MAY just be waiting on the model (network-bound), so flat != wedged — the minutes check is.
$passSec = $null; $computing = $false; $wedged = $false
if ($agentProc) {
  $passSec = [int]((Get-Date) - $agentProc.CreationDate).TotalSeconds
  $p1 = Get-Process -Id $agentProc.ProcessId -ErrorAction SilentlyContinue
  if ($p1) {
    $cpu0 = [double]$p1.CPU
    Start-Sleep -Milliseconds 700
    $p2 = Get-Process -Id $agentProc.ProcessId -ErrorAction SilentlyContinue
    if ($p2) { $computing = (([double]$p2.CPU - $cpu0) -gt 0.05) }
  }
  $wedged = ($passSec -ge ($WedgeMinutes * 60))
}

# 2. Dirty tree — files the agent has edited THIS pass but not yet committed.
$dirty = @()
if ($Root -and (Test-Path $Root)) {
  Push-Location $Root
  try {
    $porc = git status --porcelain 2>$null
    if ($porc) {
      $dirty = @($porc -split "`n" | Where-Object { $_.Trim() } | ForEach-Object {
        $m = [regex]::Match($_, '^\s*\S+\s+(.+)$'); if ($m.Success) { $m.Groups[1].Value.Trim() } } |
        Select-Object -First 12)
    }
  } catch {}
  Pop-Location
}

# 3. Last completed pass + a short commit feed (every finished pass shows here).
$iter = $null; $sha = $null; $when = $null; $commits = @()
if ($Root -and (Test-Path $Root)) {
  Push-Location $Root
  $lastPass = (git log -1 --grep='^ralph iter' --format='%s|%h|%cd' --date=format:'%H:%M:%S' 2>$null)
  if ($lastPass -match '^ralph iter (\d+).*\|([0-9a-f]+)\|(.+)$') {
    $iter = [int]$Matches[1]; $sha = $Matches[2]; $when = $Matches[3].Trim()
  }
  $raw = git log -8 --format='%h|%s|%cd' --date=format:'%H:%M:%S' 2>$null
  if ($raw) {
    $commits = @($raw -split "`n" | Where-Object { $_.Trim() } | ForEach-Object {
      $parts = $_ -split '\|', 3
      [pscustomobject]@{ sha = $parts[0]; subject = $parts[1]; time = $parts[2] }
    })
  }
  Pop-Location
}

# 4. Current task = top of the backlog (what the agent works next / is working now).
$currentTask = $null; $backlogCount = 0
if (Test-Path $backlogPath) {
  try {
    # Two-step, NOT @(Get-Content | ConvertFrom-Json): PS5.1 ConvertFrom-Json emits a JSON array as ONE
    # object down the pipeline, so @() around the pipeline double-wraps it (count 1, [0]=the whole array).
    # Assign first, THEN @() to flatten a real array to itself.
    $bl = Get-Content -Raw $backlogPath | ConvertFrom-Json
    $bl = @($bl)
    $backlogCount = $bl.Count
    if ($bl.Count) { $currentTask = [string]$bl[0].title }
  } catch {}
}

# 5. Newest log tail (grows DURING a claude pass thanks to live streaming; empty for agy).
$log = Get-ChildItem $LogDir -Filter *.log | Sort-Object LastWriteTime -Descending | Select-Object -First 1
$tail = @(); $logName = $null; $logAgeSec = $null; $runningModel = $null
if ($log) {
  $logName = $log.Name
  $logAgeSec = [int]((Get-Date) - $log.LastWriteTime).TotalSeconds
  # Cast each line to a plain [string] — Get-Content strings carry ETS note-properties (PSPath, the
  # whole provider tree) that ConvertTo-Json -Depth would expand into MEGABYTES.
  $tail = @(Get-Content $log.FullName -Tail 22 | ForEach-Object { [string]$_ })
  $hdr = Get-Content $log.FullName -TotalCount 6 | Where-Object { $_ -match '^model\s*:' } | Select-Object -First 1
  if ($hdr) { $runningModel = ($hdr -replace '^model\s*:\s*', '').Trim() }
}

# Selected agent/model = what the NEXT iteration will use (loop re-reads agent.json each pass).
$selAgent = $null; $selModel = $null
if (Test-Path $agentPath) {
  try { $a = Get-Content -Raw $agentPath | ConvertFrom-Json; $selAgent = [string]$a.agent; $selModel = [string]$a.model } catch {}
}

# 6. Agent self-report ("offboard"). The ONLY window into an agy pass (uncapturable stdout). The agent
#    overwrites progress.json at pass START (phase=working + plan) and END (done/blocked/reverted).
$progress = $null; $progressAgeSec = $null
if (Test-Path $progressPath) {
  try {
    $progress = Get-Content -Raw $progressPath | ConvertFrom-Json
    $progressAgeSec = [int]((Get-Date) - (Get-Item $progressPath).LastWriteTime).TotalSeconds
  } catch {}
}

[pscustomobject]@{
  alive        = $alive
  loopPid      = $loopPid
  agentPid     = if ($agentProc) { $agentProc.ProcessId } else { $null }
  claudePid    = if ($agentProc) { $agentProc.ProcessId } else { $null }   # back-compat alias
  passSeconds  = $passSec
  computing    = $computing
  wedged       = $wedged
  dirtyFiles   = $dirty
  currentTask  = $currentTask
  backlogCount = $backlogCount
  selAgent     = $selAgent
  selModel     = $selModel
  runningModel = $runningModel
  lastIter     = $iter
  lastSha      = $sha
  lastWhen     = $when
  commits      = $commits
  logName      = $logName
  logAgeSec    = $logAgeSec
  logTail      = $tail
  progress     = $progress
  progressAgeSec = $progressAgeSec
} | ConvertTo-Json -Depth 6 -Compress
