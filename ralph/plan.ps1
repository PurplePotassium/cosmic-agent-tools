<#
.SYNOPSIS
  Planner -- the "smart, whole-codebase" half of the hybrid loop. Fast workers (Gemini/agy or a cheap
  model) are good at IMPLEMENTING but weaker at holding the whole codebase in mind; a strong model is
  the opposite. So planning stays here: this runs `claude -p` against PLAN-PROMPT.md to keep `TODO.md`
  full, high-impact, and (critically) carved into per-lane sections whose open items touch DISJOINT
  files -- which is what keeps the refinery from drowning in merge conflicts.

  Runs on $Base in the MAIN worktree. It self-commits its TODO/lane edits each pass (so it works with
  OR without a workspace auto-commit-on-Stop hook). Run it BETWEEN fan-out cycles (lanes paused) or on
  a slow cadence; lanes pick up the new backlog when they next sync $Base.

.EXAMPLE
  ./plan.ps1                 # one planning pass, then exit
.EXAMPLE
  ./plan.ps1 -Iterations 0 -SleepSeconds 1800   # re-plan every 30 min, forever
#>
[CmdletBinding()]
param(
  [string]$Prompt = '',                       # planner prompt (default: PLAN-PROMPT.md next to script)
  [int]$Iterations = 1,                       # planning passes; 0 = forever
  [int]$SleepSeconds = 0,
  [string]$Model = 'claude-opus-4-8',         # planning wants the strongest whole-codebase model
  [switch]$SkipPermissions = $true,
  [string]$LogDir = '',
  [string]$Personas = '',                     # default: config pool, next to script
  [string]$Nouns = '',
  [switch]$Random
)
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding           = [System.Text.Encoding]::UTF8

# --- resolve script dir + load project config ------------------------------------------------------
$scriptDir = $PSScriptRoot
if (-not $scriptDir -and $PSCommandPath) { $scriptDir = Split-Path -Parent $PSCommandPath }
if (-not $scriptDir) { $scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path }
if (-not $scriptDir) { $scriptDir = Join-Path $PWD 'ralph' }
$cfgFile = Join-Path $scriptDir 'fleet.config.ps1'
if (Test-Path $cfgFile) { . $cfgFile }
$poolPersonas = if ($FleetConfig.Personas) { $FleetConfig.Personas } else { 'personas.txt' }
$poolNouns    = if ($FleetConfig.Nouns)    { $FleetConfig.Nouns }    else { 'nouns.txt' }

if (-not $Prompt)   { $Prompt   = Join-Path $scriptDir 'PLAN-PROMPT.md' }
if (-not $LogDir)   { $LogDir   = Join-Path $scriptDir 'logs' }
if (-not $Personas) { $Personas = Join-Path $scriptDir $poolPersonas }
if (-not $Nouns)    { $Nouns    = Join-Path $scriptDir $poolNouns }

if (-not (Test-Path $Prompt)) { Write-Error "planner prompt not found: $Prompt  (copy PLAN-PROMPT.example.md -> PLAN-PROMPT.md and edit it for your lanes)" }
$ralphHome = Split-Path $Prompt
$basePrompt = (Get-Content -Raw -Path $Prompt).Trim()
New-Item -ItemType Directory -Force -Path $LogDir | Out-Null

# Don't plan while the refinery is mid-merge in THIS worktree -- both edit TODO.md / the main tree and a
# concurrent reset/checkout would clobber the planner's edits (or vice-versa). Bail clearly, don't race.
$gitDir = git rev-parse --git-dir 2>$null
if ($gitDir -and (Test-Path (Join-Path $gitDir 'MERGE_HEAD'))) {
  Write-Error "A merge is in progress in this worktree (refinery running here?). Wait for it to finish, or run the planner from a clean worktree, before planning."
}

$claudeArgs = @('-p', '--model', $Model)
if ($SkipPermissions) { $claudeArgs += '--dangerously-skip-permissions' }

Write-Host "=== Planner (claude $Model) ===" -ForegroundColor Cyan
$i = 0
while ($Iterations -eq 0 -or $i -lt $Iterations) {
  $i++
  $stamp = Get-Date -Format 'yyyy-MM-dd_HH-mm-ss'
  $log = Join-Path $LogDir ("plan-{0:0000}-{1}.log" -f $i, $stamp)
  Write-Host "--- plan pass $i  $stamp ---" -ForegroundColor Green
  "=== plan pass $i  $stamp  (model $Model) ===" | Set-Content -Path $log -Encoding utf8

  $stateFile = Join-Path $ralphHome '.refinery-state.json'
  $blocksInfo = ""
  if (Test-Path $stateFile) {
    $state = Get-Content -Raw $stateFile | ConvertFrom-Json
    if ($state.blocked -and ($state.blocked | Get-Member -MemberType NoteProperty)) {
      $blocks = @()
      foreach ($prop in ($state.blocked | Get-Member -MemberType NoteProperty)) {
        $lane = $prop.Name
        $blocker = $state.blocked.$lane
        $blocks += "  - Lane '$lane' is BLOCKED by '$blocker' due to a cross-lane integration/gate failure when combined."
      }
      $blocksInfo = "`n`nCRITICAL: The following cross-lane conflicts were detected by the refinery. Please adjust the tasks/files for these lanes to resolve the conflict, then they will be unblocked:`n" + ($blocks -join "`n")
      Write-Host "Detected active lane blocks: $($blocks -join ', ')" -ForegroundColor Yellow
    }
  }

  $spiceInfo = ""
  if ($Random) {
    $pRaw = if (Test-Path $Personas) { Get-Content $Personas } else { @() }
    $nRaw = if (Test-Path $Nouns)    { Get-Content $Nouns }    else { @() }
    $spices = @()
    $pRaw | ForEach-Object { $l = $_.Trim(); if ($l -and -not $l.StartsWith('#')) { $spices += @{ type='persona'; text=$l } } }
    $nRaw | ForEach-Object { $l = $_.Trim(); if ($l -and -not $l.StartsWith('#')) { $spices += @{ type='noun';    text=$l } } }
    if ($spices.Count -gt 0) {
      $picked = $spices | Get-Random
      $spiceInfo = "`n`nANTI-CIRCLING CONTEXT FOR PLANNING: When generating new backlog TODOs, you must slant the designs/themes/ideas using this lens/concept: '$($picked.text)' ($($picked.type)). Make sure some of the new tasks reflect this flavor."
      Write-Host "Injecting planning spice: $($picked.text) ($($picked.type))" -ForegroundColor Yellow
      "spice  : $($picked.text) ($($picked.type))" | Add-Content -Path $log -Encoding utf8
    }
  }

  $headBefore = (git rev-parse HEAD 2>$null)
  $iterPrompt = $basePrompt + $blocksInfo + $spiceInfo
  $env:RALPH_PASS = "plan $i"
  $iterPrompt | & claude @claudeArgs 2>&1 | Tee-Object -Variable out | Out-Host
  $planExit = $LASTEXITCODE                      # capture BEFORE the next git call overwrites it
  Add-Content -Path $log -Value $out -Encoding utf8

  # Self-commit the planner's edits so it works WITHOUT a workspace Stop hook. If a Stop hook already
  # committed, this no-ops (clean tree). Only commits when the planner actually changed files.
  if ($planExit -eq 0 -and (git status --porcelain)) {
    git add -A 2>&1 | Out-Null
    git commit -q -m "plan pass $i $stamp" 2>&1 | Out-Null
  }
  $headAfter = (git rev-parse HEAD 2>$null)

  # Clear lane blocks ONLY if the planner actually committed a re-partition (HEAD moved). Clearing on a
  # no-op pass would unblock lanes that nothing was done for -> the refinery re-merges, re-fails, and
  # re-blocks them (thrash). No commit = nothing resolved = leave the blocks standing.
  if ($planExit -eq 0 -and $headBefore -and $headAfter -and ($headAfter -ne $headBefore) -and (Test-Path $stateFile)) {
    $state = Get-Content -Raw $stateFile | ConvertFrom-Json
    if ($state.blocked -and ($state.blocked | Get-Member -MemberType NoteProperty)) {
      $state.blocked = [pscustomobject]@{}
      $state | ConvertTo-Json -Depth 5 | Set-Content -Path $stateFile -Encoding utf8
      Write-Host "Cleared active lane blocks (planner committed a re-partition)." -ForegroundColor Green
    }
  } elseif ($planExit -eq 0 -and $headBefore -eq $headAfter) {
    Write-Host "Planner made no commit this pass -- leaving lane blocks in place." -ForegroundColor DarkGray
  }

  if ($SleepSeconds -gt 0 -and ($Iterations -eq 0 -or $i -lt $Iterations)) { Start-Sleep -Seconds $SleepSeconds }
}
Write-Host "=== Planner done ($i passes) ===" -ForegroundColor Cyan
