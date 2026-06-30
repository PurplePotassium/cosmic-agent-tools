<#
.SYNOPSIS
  Workshop loop — a SINGLE coding agent (claude -p / agy) run back-to-back on the
  same prompt, fresh context each pass, draining an operator-curated backlog
  toward a north-star GOAL.md, until the work is done or N iterations elapse.

  The "Ralph Wiggum" technique: a dumb while-loop around a smart agent. Each pass
  starts cold (no memory of the last), reads the repo + prompt, makes ONE small
  verified increment, commits it, and exits. The loop runs it again.

  This is the SINGLE-agent engine — the counterpart to the fleet's ralph.ps1.
  There are NO worktrees, lanes, refinery, planner, or trunk-merge here; one
  agent works one branch. Workshop-specific behavior (the live -AgentControlFile
  agent/model switch + per-pass auto-routing) lives here and nowhere in the fleet.

.DESCRIPTION
  -Random adds the blog's anti-circling trick (turso.tech/blog/edgar-allan-poe):
  long-running agents stop exploring and loop on the same ideas. Inject semantic
  tension each iteration so the model breaks out of the rut (persona OR
  recoding-decoding). The Workshop runs with -Random on by default.

.EXAMPLE
  # run forever against workshop/PROMPT.md (stop via stop-workshop.ps1)
  ./workshop.ps1 -Random

.EXAMPLE
  # bounded smoke run, 3 passes
  ./workshop.ps1 -Random -Iterations 3

.NOTES
  -SkipPermissions passes --dangerously-skip-permissions so the agent runs
  unattended with full tool access in the target repo. Only do this where you can
  fully revert via git. Read the warning printed at startup.
#>
[CmdletBinding()]
param(
  [string]$Prompt = '',                          # prompt file (default: <scriptdir>/PROMPT.md)
  [int]$Iterations = 0,                           # 0 = run forever (Ctrl-C / stop-workshop.ps1)
  [switch]$Random,                                # per-iteration anti-circling randomness
  [int]$SleepSeconds = 0,                          # pause between iterations (0 = none)
  [string]$Model = '',                            # model id; '' = the agent's own default
  # Which coding agent drives each pass. 'claude' = Claude Code (`claude -p`, prompt over stdin).
  # 'agy' = Antigravity CLI (Gemini; `agy -p <prompt> --dangerously-skip-permissions`) — faster, but
  # uncapturable headless (see AGENTS.md). Neither relies on a Stop hook here: this loop commits each
  # pass itself. 'auto' = classify the top backlog item per pass into a concrete agent/model.
  [ValidateSet('claude','agy','auto')][string]$Agent = 'claude',
  [string[]]$AgentExtraArgs = @(),                # raw extra args appended to the agent invocation
  [switch]$SkipPermissions = $true,               # run unattended (see .NOTES)
  [string]$Personas = '',                        # persona pool (default: <scriptdir>/personas.txt)
  [string]$Nouns    = '',                        # priming-noun pool (default: <scriptdir>/nouns.txt)
  [string]$LogDir = '',                          # log dir (default: <scriptdir>/logs)
  [string]$AgentControlFile = ''                 # optional JSON {agent,model} re-read at the TOP of
                                                 # each iteration so the operator (the Workshop UI)
                                                 # can switch agent/model for the NEXT pass with no
                                                 # restart. Unset = fixed at launch.
)

$ErrorActionPreference = 'Stop'

# Resolve script-relative defaults ROBUSTLY. $PSScriptRoot comes through EMPTY under some launch
# mechanisms (certain Start-Process / -Command invocations), which would turn "$PSScriptRoot/PROMPT.md"
# into a bogus "/PROMPT.md" (and "/logs", "/personas.txt", ...). Fall back to $PSCommandPath, then CWD.
$wsHome = $PSScriptRoot
if (-not $wsHome -and $PSCommandPath)               { $wsHome = Split-Path -Parent $PSCommandPath }
if (-not $wsHome -and $MyInvocation.MyCommand.Path) { $wsHome = Split-Path -Parent $MyInvocation.MyCommand.Path }
if (-not $wsHome) { $wsHome = (Get-Location).Path }
if (-not $Prompt)   { $Prompt   = Join-Path $wsHome 'PROMPT.md' }
if (-not $Personas) { $Personas = Join-Path $wsHome 'personas.txt' }
if (-not $Nouns)    { $Nouns    = Join-Path $wsHome 'nouns.txt' }
if (-not $LogDir)   { $LogDir   = Join-Path $wsHome 'logs' }

# claude emits UTF-8 (✅ → emoji, —, →). PS5.1 otherwise decodes its stdout as the console's OEM/
# cp1252 codepage, mangling those to Γ£à / ΓÇö / ΓåÆ in BOTH the console AND the log. Force UTF-8 on
# the console decode + on what we send to the agent over stdin so multibyte chars survive intact.
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
$OutputEncoding           = [System.Text.Encoding]::UTF8

if (-not (Test-Path $Prompt)) {
  Write-Error "Prompt file not found: $Prompt  (copy PROMPT.example.md to PROMPT.md and edit it)"
}
$basePrompt = (Get-Content -Raw -Path $Prompt).Trim()
New-Item -ItemType Directory -Force -Path $LogDir | Out-Null

# The agent's working directory is the TARGET repo (Root), but the operator state files this prompt
# refers to (GOAL.md, backlog.json, completions.json, progress.json) live next to PROMPT.md — the
# Workshop dir. Inject their absolute location as a CONSTANT header (stable across passes => stays a
# cacheable prompt prefix) so the agent reads/writes them at the right path regardless of cwd.
$promptDir = Split-Path -Parent $Prompt
if (-not $promptDir) { $promptDir = $wsHome }
$stateHeader = @"
## Workshop state directory (read this first)
Your working directory is the target repo. The operator state files this prompt refers to —
GOAL.md, backlog.json, completions.json, and progress.json — live in this folder instead:
  $promptDir
Always read and write those four files at that absolute path, not relative to your working directory.

"@

# Random pools (one item per line, '#'/blank lines ignored)
function Read-Pool([string]$file) {
  if (Test-Path $file) {
    Get-Content $file | Where-Object { $_.Trim() -and -not $_.Trim().StartsWith('#') }
  } else { @() }
}
# NOTE: don't reuse the [string]-typed param names ($Personas/$Nouns) for these arrays — PS var names
# are case-insensitive, so $personas would inherit the param's [string] constraint and coerce the array
# to one space-joined string.
$personaPool = Read-Pool $Personas
$nounPool    = Read-Pool $Nouns

# Build the per-iteration randomness wrapper (blog: persona OR recoding-decoding).
function Get-Spice {
  $mode = Get-Random -InputObject @('persona','recode')
  if ($mode -eq 'persona' -and $personaPool.Count) {
    $p = Get-Random -InputObject $personaPool
    return @{
      mode   = "persona:$p"
      prefix = "For this iteration only, channel the mindset of $p. Bring that distinctive way of seeing to the task below - it is a lens to break you out of repeating earlier ideas, not a change to the goal.`n`n"
      suffix = ""
    }
  }
  # recoding-decoding: priming noun up front + diverting word-stem at the end
  $noun = if ($nounPool.Count) { Get-Random -InputObject $nounPool } else { 'food' }
  $stemSource = if ($nounPool.Count) { Get-Random -InputObject $nounPool } else { 'pasta' }
  $stemLen = Get-Random -Minimum 2 -Maximum ([Math]::Min(4, $stemSource.Length) + 1)
  $stem = $stemSource.Substring(0, $stemLen)
  $stem = $stem.Substring(0,1).ToUpper() + $stem.Substring(1)
  return @{
    mode   = "recode:$noun/$stem"
    prefix = "Related to $($noun.ToUpper()): "
    suffix = " $stem"
  }
}

Write-Host "=== Workshop loop (single agent / anti-circling) ===" -ForegroundColor Cyan
Write-Host "prompt : $Prompt"
Write-Host "iters  : $(if ($Iterations -eq 0) {'infinite (Ctrl-C to stop)'} else {$Iterations})"
if ($SkipPermissions) {
  Write-Host "WARNING: --dangerously-skip-permissions is ON. The agent runs unattended with full tool access in this repo. Make sure you can revert its changes." -ForegroundColor Yellow
}

# --- agent driver: how this loop invokes the coding agent each pass ------------------------------
# claude takes the prompt over STDIN (dodges arg-length limits). agy (Antigravity/Gemini) takes the
# prompt as a -p ARG + --dangerously-skip-permissions. Neither fires a Stop hook here, so the loop
# commits its increment itself (see the auto-commit below).
function Resolve-AgentDriver([string]$ag, [string]$md) {
  $exe = ''; $mode = ''; $aArgs = @()
  switch ($ag) {
    'claude' {
      $exe = 'claude'; $mode = 'stdin'; $aArgs = @('-p')
      $m = if ($md) { $md } else { 'claude-sonnet-4-6' }
      $aArgs += @('--model', $m)
      if ($SkipPermissions) { $aArgs += '--dangerously-skip-permissions' }
    }
    'agy' {
      # Antigravity CLI (Gemini), authenticated via your Antigravity SUBSCRIPTION (Pro/Ultra quota,
      # NOT a metered API key — agy ignores GEMINI_API_KEY). Flags verified against agy 1.0.13:
      #   -p <prompt>                      single non-interactive pass, prints the response
      #   --dangerously-skip-permissions   auto-approve all tool requests (unattended)
      #   --print-timeout <dur>            print-mode wait — DEFAULT 5m would cut a real increment
      #                                    short, so we raise it; a pass needing >30m is too big.
      #   --model <id>                     optional; omit to use the account default (see `agy models`)
      # Do NOT add --sandbox (it would block your verify command). Override any of this via
      # -AgentExtraArgs without editing here.
      $exe = 'agy'; $mode = 'arg'                       # prompt passed as a -p argument, not stdin
      $aArgs = @('--dangerously-skip-permissions', '--print-timeout', '30m')
      if ($md) { $aArgs += @('--model', $md) }
    }
    default {
      # Unknown/unresolved (e.g. 'auto' before classification) — fall back to a safe gated default so
      # an invocation can never fire with an empty exe. The control-file block re-resolves to the real
      # pick before this driver is actually used.
      $exe = 'claude'; $mode = 'stdin'; $aArgs = @('-p', '--model', 'claude-sonnet-4-6')
      if ($SkipPermissions) { $aArgs += '--dangerously-skip-permissions' }
    }
  }
  $aArgs += $AgentExtraArgs

  if ($ag -eq 'agy') {
    # Ensure agy is findable. A parent shell launched BEFORE agy's installer ran — or the web UI's node
    # process, or a spawned-from-stale-PATH window — can inherit a PATH without agy's bin dir, so `& agy`
    # would fail "command not found". Refresh PATH from the registry (where the installer recorded it).
    if (-not (Get-Command agy -ErrorAction SilentlyContinue)) {
      $env:Path = [Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [Environment]::GetEnvironmentVariable('Path','User')
    }
    if (Get-Command agy -ErrorAction SilentlyContinue) {
      $exe = (Get-Command agy).Source
    } else {
      foreach ($p in @(
          (Join-Path $env:LOCALAPPDATA 'agy\bin\agy.exe'),
          (Join-Path $env:USERPROFILE 'AppData\Local\agy\bin\agy.exe'))) {
        if (Test-Path $p) { $exe = $p; break }
      }
    }
  }
  return @{ Exe = $exe; Mode = $mode; Args = $aArgs }
}

# --- auto model routing ---------------------------------------------------------------------------
# When the operator picks "Auto" in the UI, agent.json holds {agent:'auto',model:'auto'}. Each pass we
# classify the TOP backlog item (title+detail keywords) into one of three real combos:
#   * light presentation work (art/audio/juice/tuning/copy)        -> agy / gemini-3.5-flash  (fastest)
#   * heavy/structural work (refactor/reorg/architecture/save/AI)  -> claude / opus-4-8        (deepest)
#   * everything else (content/systems/features/mechanics)         -> claude / sonnet-4-6      (gated default)
# Empty/unreadable backlog -> sonnet (safe). Backlog path = sibling backlog.json of the control file.
function Resolve-AutoSelection([string]$ctlFile) {
  $light  = 'juice|particle|screenshake|\bshake\b|telegraph|\bfeel\b|polish|\bart\b|sprite|palette|\bcolou?r\b|\baudio\b|\bsound\b|\bsfx\b|music|\btween|easing|\bcopy\b|tooltip|label|\btext\b|wording|cosmetic|\bvfx\b'
  $heavy  = 'refactor|re-?organ|reorganize|restructure|re-?architect|architecture|\bsplit(ting)?\b|\bmodulari|save/load|serializ|persist|netcode|multiplayer|pathfind|state machine|overhaul|migrat|\bredesign\b|\brework\b|boss ai|complex'
  $title = ''; $detail = ''
  try {
    $blPath = Join-Path (Split-Path -Parent $ctlFile) 'backlog.json'
    if (Test-Path $blPath) {
      $bl = Get-Content -Raw -Path $blPath | ConvertFrom-Json
      if ($bl -and $bl.Count -gt 0) { $title = [string]$bl[0].title; $detail = [string]$bl[0].detail }
    }
  } catch {}
  $txt = ("$title $detail").ToLower()
  if ($txt.Trim() -and $txt -match $heavy) { return @{ agent = 'claude'; model = 'claude-opus-4-8';   reason = 'heavy/structural' } }
  if ($txt.Trim() -and $txt -match $light) { return @{ agent = 'agy';    model = 'gemini-3.5-flash';  reason = 'light/presentation' } }
  return @{ agent = 'claude'; model = 'claude-sonnet-4-6'; reason = 'default/systems' }
}

$driver    = Resolve-AgentDriver $Agent $Model
$agentExe  = $driver.Exe
$agentMode = $driver.Mode
$agentArgs = $driver.Args

$i = 0
$consecutiveFails = 0   # circuit breaker: bail after N back-to-back agent failures
while ($Iterations -eq 0 -or $i -lt $Iterations) {
  $i++
  $stamp = Get-Date -Format 'yyyy-MM-dd_HH-mm-ss'

  # Per-iteration agent/model switch. If a control file is set, re-read it at the TOP of each pass so
  # the operator can change agent/model for the NEXT iteration from the UI without restarting the loop
  # (the in-flight pass keeps its model; the change lands next time around). Re-resolve the driver only
  # when the selection actually changed (agy's PATH/exe lookup is non-trivial). Malformed file = ignore.
  if ($AgentControlFile -and (Test-Path $AgentControlFile)) {
    try {
      $sel = Get-Content -Raw -Path $AgentControlFile | ConvertFrom-Json
      $rawAg = if ($sel.agent) { [string]$sel.agent } else { $Agent }
      $rawMd = if ($sel.model) { [string]$sel.model } else { $Model }
      # 'auto' on either field => classify THIS pass's top backlog item into a concrete combo. Re-run
      # every pass (the raw 'auto' stays in the file) so each item gets the model that fits it.
      if ($rawAg -eq 'auto' -or $rawMd -eq 'auto') {
        $auto = Resolve-AutoSelection $AgentControlFile
        $newAg = $auto.agent; $newMd = $auto.model
        Write-Host ("auto-route: top backlog item -> {0}/{1} ({2})" -f $newAg, $newMd, $auto.reason) -ForegroundColor DarkCyan
      } else {
        $newAg = $rawAg; $newMd = $rawMd
      }
      if ($newAg -ne $Agent -or $newMd -ne $Model) {
        Write-Host ("agent/model switch: {0}/{1} -> {2}/{3}" -f $Agent, $Model, $newAg, $newMd) -ForegroundColor Cyan
        $Agent = $newAg; $Model = $newMd
        $driver = Resolve-AgentDriver $Agent $Model
        $agentExe = $driver.Exe; $agentMode = $driver.Mode; $agentArgs = $driver.Args
      }
    } catch {}
  }

  # --- stale lock cleanup --------------------------------------------------------------------
  # A crashed agent (or git subprocess) can leave an orphaned index.lock that blocks ALL subsequent
  # git operations in this worktree. Detect + remove it. Only delete if the lock's owning process is
  # genuinely gone (age > 60s heuristic — a live git add/merge/commit taking > 60s is implausible here).
  $wt = (Get-Location).Path
  $gitDir = (git -C $wt rev-parse --git-dir 2>$null)
  if ($gitDir) {
    $lockFile = Join-Path $gitDir 'index.lock'
    if (Test-Path $lockFile) {
      $lockAge = (Get-Date) - (Get-Item $lockFile).CreationTime
      if ($lockAge.TotalSeconds -gt 60) {
        Remove-Item $lockFile -Force -ErrorAction SilentlyContinue
        Write-Host "cleaned stale index.lock (age: $([int]$lockAge.TotalSeconds)s)" -ForegroundColor Yellow
      }
    }
  }

  $iterPrompt = $stateHeader + $basePrompt
  $modeLabel = 'plain'
  if ($Random) {
    $spice = Get-Spice
    # Append the anti-circling spice AFTER the (byte-identical) base prompt instead of prepending it,
    # so the long stable preamble (state header + base prompt) stays a cacheable prefix across passes
    # (prompt-cache hit on -p) while the per-iteration tension still lands. The framing reads fine trailing.
    $iterPrompt = $stateHeader + $basePrompt + "`n`n---`n" + $spice.prefix + $spice.suffix
    $modeLabel = $spice.mode
  }

  $iterLabel = ("iteration {0}{1} [{2}] {3}" -f $i, $(if ($Iterations) {"/$Iterations"} else {''}), $modeLabel, $stamp)
  Write-Host ""
  Write-Host "--- $iterLabel ---" -ForegroundColor Green

  $log = Join-Path $LogDir ("iter-{0:0000}-{1}.log" -f $i, $stamp)
  # Self-describing UTF-8 header so a log read on its own says which pass/mode/model it was. (We write
  # the log OURSELVES as UTF-8: PS5.1 Tee-Object writes UTF-16LE, which opens as spaced-out garbage.)
  @(
    "=== $iterLabel ===",
    "model  : $Model",
    "spice  : $modeLabel  (anti-circling lens for THIS pass, not the task)",
    "prompt : $Prompt",
    ("-" * 72),
    ""
  ) | Set-Content -Path $log -Encoding utf8

  # Tag this pass for any Stop hook in the target repo that wants a per-pass commit subject.
  $env:RALPH_PASS = "iter $i"
  if ($agentMode -eq 'stdin') {
    # Stream claude's output to the log LIVE (per-line UTF-8 append) AND to the console, so a tail of
    # the iter log shows real-time progress MID-pass (the Workshop UI watches this). Still capture
    # passOut for the auth-fail scan + exit handling. Add-Content (not Tee-Object) keeps UTF-8 in PS5.1.
    $passOut = New-Object 'System.Collections.Generic.List[string]'
    $iterPrompt | & $agentExe @agentArgs 2>&1 | ForEach-Object {
      $line = [string]$_
      $passOut.Add($line)
      Add-Content -Path $log -Value $line -Encoding utf8
      Write-Host $line
    }
  } else {
    # agy print mode MISBEHAVES when its stdout is a pipe/redirect (non-TTY): it drops output. So DON'T
    # pipe agy — let it inherit the window's real CONSOLE/TTY. For a tailable record, point agy's own
    # --log-file at our logs dir. (--log-file is agy's OPERATIONAL log, not the model response text.)
    $agyLog = Join-Path $LogDir ("agy-{0:0000}-{1}.log" -f $i, $stamp)
    & $agentExe @agentArgs '-p' $iterPrompt --log-file $agyLog
    $passOut = @("(agy output is LIVE in the loop window; tailable agy log: $agyLog)")
    if (Test-Path $agyLog) {
      try { $passOut += @(Get-Content -Path $agyLog -Tail 200 -ErrorAction SilentlyContinue) } catch {}
    }
    Add-Content -Path $log -Value $passOut -Encoding utf8
  }

  if ($LASTEXITCODE -ne 0) {
    # Narrow keywords so a normal pass mentioning "token"/"login" doesn't read as an auth failure.
    $isAuthFail = $false
    foreach ($line in $passOut) {
      if ($line -match 'auth|credential|sign-?in|re-?authenticate|keyring|unauthorized|\b401\b') { $isAuthFail = $true; break }
    }
    if ($isAuthFail) {
      [Console]::Beep(440, 500)
      Write-Host 'CRITICAL: agent auth failure - re-authenticate; the loop cannot fix this.' -ForegroundColor Red
      throw "Agent auth failure, exit code $LASTEXITCODE."  # auth will not self-heal: stop for the human
    }
    # Non-auth failure: don't let one transient error kill an unattended loop. Skip this pass's commit
    # and continue — but bail if the agent fails repeatedly (something is genuinely wrong).
    $consecutiveFails++
    Write-Host "pass ${i}: agent exited $LASTEXITCODE (non-auth) -- skipping commit, continuing ($consecutiveFails in a row)." -ForegroundColor Yellow
    if ($consecutiveFails -ge 5) { throw "5 consecutive agent failures -- stopping loop; check the agent/repo." }
    if ($SleepSeconds -gt 0 -and ($Iterations -eq 0 -or $i -lt $Iterations)) { Start-Sleep -Seconds $SleepSeconds }
    continue
  }
  $consecutiveFails = 0

  # Auto-commit whatever the pass changed — wrapped in try/catch because git can write to stderr
  # (warnings, lock notices, merge advice) which under $ErrorActionPreference='Stop' would terminate
  # the whole script. A failed commit is recoverable (next pass retries); a dead loop is not.
  $wt = (Get-Location).Path
  try {
    $statusLines = @(git -C $wt status --porcelain 2>&1)
  } catch {
    Write-Host "git status failed (stale lock?): $_ -- skipping commit, continuing" -ForegroundColor Yellow
    $statusLines = @()
  }
  if ($statusLines) {
    try {
      $null = git -C $wt add -A 2>&1
      $null = git -C $wt commit -q -m "ralph $($env:RALPH_PASS) [$Agent] $stamp" 2>&1
    } catch {
      Write-Host "git commit failed: $_ -- changes staged but not committed; next pass retries" -ForegroundColor Yellow
    }
  }

  # Record what the pass actually changed.
  try {
    $stat = (git -C $wt show --stat --format='commit %h  %s' HEAD 2>&1) -join "`n"
    Add-Content -Path $log -Value @('', ('-' * 72), '--- files changed this pass ---', $stat) -Encoding utf8
  } catch {}

  if ($SleepSeconds -gt 0 -and ($Iterations -eq 0 -or $i -lt $Iterations)) {
    Start-Sleep -Seconds $SleepSeconds
  }
}

Write-Host ""
Write-Host "=== Workshop loop done ($i iterations). Logs in $LogDir ===" -ForegroundColor Cyan
