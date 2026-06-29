<#
.SYNOPSIS
  Refinery -- the integrator LOOP for parallel Ralph (a Bors-style merge queue, minus the foot-guns).
  Polls the lane branches, merges the ones with NEW commits into the trunk, gates the COMBINED result,
  and on a red gate BISECTS to drop the offending lane instead of corrupting the trunk.

  Discipline that keeps it safe (this is the whole point -- the easy version is what let other swarms
  auto-merge red into main and then force-push to recover):
    * NEVER force-pushes. Trunk is a local branch; worst case is "a lane didn't land", not "trunk broke".
    * Merge conflicts are ABORTED + flagged, never machine-resolved into a guessed state.
    * Every merge is --no-ff, so a regressing lane is rolled back with a single clean `reset HEAD^`.
    * A lane that conflicts or fails the gate is recorded by tip SHA and NOT retried until it ADVANCES
      (new commits), so a persistently-bad lane can't spin the loop forever.

  PROJECT KNOBS (Root, Base, GateDir, GateCmd) come from fleet.config.ps1; CLI params override.

.DESCRIPTION
  Owns the MAIN worktree ($Root): it checks out / resets $Base there, so that tree must be clean and
  no single-loop Ralph may run in it. The lanes run in their own <Root>-wt-* worktrees, untouched.

.EXAMPLE
  ./refinery.ps1                       # poll every 90s, forever
.EXAMPLE
  ./refinery.ps1 -IntervalSeconds 30 -Names systems,content
.EXAMPLE
  ./refinery.ps1 -Iterations 1         # single integration pass then exit (CI-style)
#>
[CmdletBinding()]
param(
  [string]$Base,                                      # trunk branch (default: fleet.config.ps1)
  [string]$Root,                                      # main worktree (default: fleet.config.ps1)
  [string[]]$Names,                                   # subset of lanes.txt (default = all)
  [string]$Manifest,                                  # default: lanes.txt next to this script
  [int]$IntervalSeconds = 90,                         # poll cadence between rounds
  [int]$Iterations = 0,                               # 0 = forever (Ctrl-C to stop)
  [string]$GateDir,                                   # cwd for the gate, relative to Root (default: config)
  [string]$GateCmd                                    # the integration gate (must exit 0 = PASS) (default: config)
)
$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8

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

$ralphDir = Join-Path $Root 'ralph'
if (-not $Manifest) { $Manifest = Join-Path $scriptDir 'lanes.txt' }

$laneNames = if ($Names) { $Names } else {
  Get-Content $Manifest | ForEach-Object { $_.Trim() } |
    Where-Object { $_ -and -not $_.StartsWith('#') } |
    ForEach-Object { ($_ -split '\s+')[0] }
}
if (-not $laneNames) { throw "no lanes (manifest empty or -Names matched nothing)" }

$stateFile = Join-Path $ralphDir '.refinery-state.json'   # lane -> last tip SHA we merged-or-dropped
$logDir    = Join-Path $ralphDir 'logs'
New-Item -ItemType Directory -Force -Path $logDir | Out-Null
$logFile = Join-Path $logDir ("refinery-{0}.log" -f (Get-Date -Format 'yyyy-MM-dd_HH-mm-ss'))

function Log([string]$msg) {
  $line = "[{0}] {1}" -f (Get-Date -Format 'HH:mm:ss'), $msg
  Write-Host $line
  Add-Content -Path $logFile -Value $line -Encoding utf8
}
function Load-State {
  if (Test-Path $stateFile) { Get-Content -Raw $stateFile | ConvertFrom-Json } else { [pscustomobject]@{} }
}
function Save-State($s) { $s | ConvertTo-Json -Depth 5 | Set-Content -Path $stateFile -Encoding utf8 }
function Get-Seen($s, $name) { if ($s.PSObject.Properties[$name]) { $s.$name } else { $null } }
function Set-Seen($s, $name, $val) {
  if ($s.PSObject.Properties[$name]) { $s.$name = $val }
  else { $s | Add-Member -NotePropertyName $name -NotePropertyValue $val }
}
# NOTE: call `git.exe`, NOT `git` -- PowerShell command resolution is case-insensitive and prefers a
# FUNCTION over an external command, so `& git` inside a function named `Git` recurses infinitely
# ("call depth overflow"). The .exe forces the external binary.
function Git { param([Parameter(ValueFromRemainingArguments)]$a) & git.exe -C $Root @a }
function Tip($branch) {
  $sha = Git rev-parse --verify --quiet "refs/heads/$branch"
  if ($LASTEXITCODE -ne 0) { return $null }
  $sha.Trim()
}
function Ahead($branch) {            # commits the lane has that $Base doesn't
  $n = Git rev-list --count "$Base..$branch"
  if ($LASTEXITCODE -ne 0) { return 0 }
  [int]($n.Trim())
}
function Short($sha) { if ($sha) { $sha.Substring(0, [Math]::Min(7, $sha.Length)) } else { '???????' } }
function Run-Gate {                  # returns the gate's exit code (0 = PASS)
  Push-Location (Join-Path $Root $GateDir)
  try { Invoke-Expression $GateCmd | Out-Host; return $LASTEXITCODE } finally { Pop-Location }
}

Log "refinery start -- base=$Base  lanes=$($laneNames -join ',')  interval=${IntervalSeconds}s  gate='$GateCmd' (in $GateDir)"

$round = 0
while ($Iterations -eq 0 -or $round -lt $Iterations) {
  $round++
  $state = Load-State

  # Defensive: a Ctrl-C'd prior round can leave an in-progress merge or stale worktree admin that blocks
  # the next checkout/merge. Clear both before touching anything.
  if (Test-Path (Join-Path $Root '.git\MERGE_HEAD')) { Git merge --abort 2>&1 | Out-Null; Log "cleared a stale in-progress merge from a prior round" }
  Git worktree prune 2>&1 | Out-Null
  Git checkout -q $Base

  # 1. which lanes have NEW commits since we last merged-or-dropped their tip?
  $pending = @()
  foreach ($name in $laneNames) {
    if ($state.blocked -and $state.blocked.PSObject.Properties[$name] -and $state.blocked.$name) {
      Log "  skipping blocked lane: $name (blocked by $($state.blocked.$name))"
      continue
    }
    $branch = "ralph-$name"
    $tip = Tip $branch
    if (-not $tip) { continue }                          # branch not created yet
    if ((Ahead $branch) -le 0) { continue }              # nothing ahead of trunk
    if ((Get-Seen $state $name) -eq $tip) { continue }   # already handled this exact tip
    $pending += [pscustomobject]@{ name = $name; branch = $branch; tip = $tip }
  }

  # Rotate merge order each round. When two lanes pass alone but regress COMBINED, the bisect below keeps
  # the earlier and drops the later -- so a fixed order would always punish the same lane. Rotating shares
  # that cost fairly across lanes over time. Deterministic (no RNG).
  if ($pending.Count -gt 1) {
    $shift = $round % $pending.Count
    if ($shift -gt 0) { $pending = @($pending[$shift..($pending.Count - 1)]) + @($pending[0..($shift - 1)]) }
  }

  if (-not $pending) {
    if ($IntervalSeconds -gt 0 -and ($Iterations -eq 0 -or $round -lt $Iterations)) { Start-Sleep -Seconds $IntervalSeconds }
    continue
  }

  Log "round $round -- pending: $($pending.name -join ', ')"
  $preMerge = (Git rev-parse HEAD).Trim()

  # 2. merge each pending lane (--no-ff so each is one rollback-able commit). Conflict => abort + flag.
  $mergedOk = @()
  foreach ($p in $pending) {
    Git merge --no-ff --no-edit $p.branch | Out-Host
    if ($LASTEXITCODE -ne 0) {
      Git merge --abort
      Set-Seen $state $p.name $p.tip      # flag this tip; don't retry until the lane advances
      Log "  CONFLICT $($p.name) @ $(Short $p.tip) -- aborted + flagged (resolve by hand or let the lane move on)"
    } else {
      $mergedOk += $p
    }
  }
  if (-not $mergedOk) { Save-State $state; if ($IntervalSeconds -gt 0){ Start-Sleep -Seconds $IntervalSeconds }; continue }

  # 3. gate the COMBINED stack.
  Log "  gating combined: $($mergedOk.name -join ', ')"
  if ((Run-Gate) -eq 0) {
    foreach ($p in $mergedOk) { Set-Seen $state $p.name $p.tip }
    Save-State $state
    Log "  INTEGRATION PASS -- landed: $($mergedOk.name -join ', ')  (trunk @ $(Short (Git rev-parse HEAD)))"
  } else {
    # 4. red: a lane (or a bad pair) regressed. Reset, re-apply survivors one at a time, drop the
    #    first that turns the gate red. Bail-safe -- we DROP, we never try to "fix" the merge.
    Log "  INTEGRATION RED -- bisecting (reset to $(Short $preMerge))"
    Git reset --hard $preMerge | Out-Null
    $kept = @()
    foreach ($p in $mergedOk) {
      Git merge --no-ff --no-edit $p.branch | Out-Host
      if ($LASTEXITCODE -ne 0) {
        Git merge --abort; Set-Seen $state $p.name $p.tip
        Log "    drop $($p.name): conflicts atop the kept set"
        continue
      }
      if ((Run-Gate) -eq 0) {
        $kept += $p; Set-Seen $state $p.name $p.tip
        Log "    keep $($p.name): green"
      } else {
        Git reset --hard HEAD^ | Out-Null         # peel off just this lane's merge commit
        Set-Seen $state $p.name $p.tip            # flag; retry only after the lane advances
        $keptNames = if ($kept) { ($kept.name -join ',') } else { '(base only)' }
        # Red on top of the kept set = either this lane is individually broken OR it INTERACTS with a
        # kept lane across a seam. Disjoint ownership should make the latter rare; when it isn't, it's a
        # planner job (split the seam), not something to silently auto-resolve. Flag it as such.
        Log "    DROP $($p.name) @ $(Short $p.tip): red on top of [$keptNames] -- broken OR cross-lane interaction; flagged for planner"
        if ($kept.Count -gt 0) {
          $blockingLane = ($kept.name -join '+')   # the integrated set it regressed against (best signal
                                                   # we have without a per-lane verify to pin the culprit)
          if (-not $state.blocked) {
            if ($state.PSObject.Properties['blocked']) {
              $state.blocked = [pscustomobject]@{}
            } else {
              $state | Add-Member -NotePropertyName 'blocked' -NotePropertyValue ([pscustomobject]@{});
            }
          }
          $ln = $p.name   # avoid `$state.blocked.$p.name` -- PS parses that as ($state.blocked).$p .name
          if ($state.blocked.PSObject.Properties[$ln]) {
            $state.blocked.$ln = $blockingLane
          } else {
            $state.blocked | Add-Member -NotePropertyName $ln -NotePropertyValue $blockingLane
          }
          Log "    BLOCKING LANE '$($p.name)' by '$blockingLane' to prevent oscillation until planner resolves."
        }
      }
    }
    Save-State $state
    Log "  bisect done -- landed: $(if ($kept) { ($kept.name -join ', ') } else { '(none)' })  trunk @ $(Short (Git rev-parse HEAD))"
  }

  if ($IntervalSeconds -gt 0 -and ($Iterations -eq 0 -or $round -lt $Iterations)) { Start-Sleep -Seconds $IntervalSeconds }
}

Log "refinery done ($round rounds). Log: $logFile"
