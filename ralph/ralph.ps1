<#
.SYNOPSIS
  Ralph loop — run a coding agent (claude -p) back-to-back on the same prompt,
  fresh context each iteration, until the work is done or N iterations elapse.

  The "Ralph Wiggum" technique: a dumb while-loop around a smart agent. Each pass
  starts cold (no memory of the last), reads the repo + prompt, makes one
  increment of progress, commits/leaves notes, and exits. The loop runs it again.

.DESCRIPTION
  -Random adds the blog's anti-circling trick (turso.tech/blog/edgar-allan-poe):
  long-running agents stop exploring and loop on the same ideas. Inject semantic
  tension each iteration so the model breaks out of the rut. Two modes, picked at
  random per iteration:
    * persona       — "channel the mindset of <random persona>" (Edgar Allan Poe, ...)
    * recode-decode — Recoding-Decoding: a priming noun at the START
                      ("Related to FOOD:") + a diverting word-stem at the END ("Pas")
                      that the model resolves creatively.

.EXAMPLE
  # plain Ralph, 20 iterations against ralph/PROMPT.md
  ./ralph/ralph.ps1 -Iterations 20

.EXAMPLE
  # improved Ralph with per-iteration randomness, run until you Ctrl-C
  ./ralph/ralph.ps1 -Random

.NOTES
  -SkipPermissions passes --dangerously-skip-permissions so the agent runs
  unattended. Only do this in a repo you trust and can revert (this workspace
  auto-commits). Read the warning printed at startup.
#>
[CmdletBinding()]
param(
  [string]$Prompt = '',                          # prompt file (default: resolved next to this script)
  [int]$Iterations = 0,                           # 0 = run forever (Ctrl-C to stop)
  [switch]$Random,                                # enable per-iteration prompt randomness
  [int]$SleepSeconds = 0,                          # pause between iterations (0 = none)
  # Default to Sonnet: most passes are small in-file increments and the run is gated by the hard
  # `node test/sim.mjs` PASS + directed asserts, so accuracy is protected by the harness, not the
  # model — Sonnet is ~2-3x faster tokens. Pass -Model claude-opus-4-8 for known-hard categories.
  [string]$Model = '',                            # model id; '' = the agent's own default (claude->Sonnet)
  # Which coding agent drives each pass. 'claude' = Claude Code (`claude -p`, prompt over stdin, commits
  # via the workspace Stop hook). 'agy' = Antigravity CLI (Gemini; `agy -p <prompt> --dangerously-skip-permissions`) — much faster
  # for implementation. agy does NOT trigger the Claude Stop hook, so this loop commits agy passes itself.
  [ValidateSet('claude','agy')][string]$Agent = 'claude',
  [string[]]$AgentExtraArgs = @(),                # raw extra args appended to the agent invocation
  [string]$SyncBase = '',                         # fleet passes the trunk branch here: each pass first
                                                  # merges it into this lane so the worker sees the latest
                                                  # planner re-partition + other lanes' integrated work
  [switch]$SkipPermissions = $true,               # run unattended (claude only; see .NOTES)
  [string]$Personas = '',                        # persona pool (default: <scriptdir>/personas.txt)
  [string]$Nouns    = '',                        # priming-noun pool (default: <scriptdir>/nouns.txt)
  [string]$LogDir = ''                           # log dir (default: <scriptdir>/logs)
)

$ErrorActionPreference = 'Stop'

# Resolve script-relative defaults ROBUSTLY. $PSScriptRoot comes through EMPTY under some launch
# mechanisms (certain Start-Process / -Command invocations), which would turn "$PSScriptRoot/PROMPT.md"
# into a bogus "/PROMPT.md" (and "/logs", "/personas.txt", ...). Fall back to $PSCommandPath, then to the
# fleet's worktree layout (CWD\ralph\), then CWD.
$ralphHome = $PSScriptRoot
if (-not $ralphHome -and $PSCommandPath)               { $ralphHome = Split-Path -Parent $PSCommandPath }
if (-not $ralphHome -and $MyInvocation.MyCommand.Path) { $ralphHome = Split-Path -Parent $MyInvocation.MyCommand.Path }
if (-not $ralphHome -or -not (Test-Path (Join-Path $ralphHome 'PROMPT.md'))) {
  if      (Test-Path (Join-Path (Get-Location) 'ralph\PROMPT.md')) { $ralphHome = Join-Path (Get-Location) 'ralph' }
  elseif  (-not $ralphHome)                                        { $ralphHome = (Get-Location).Path }
}
if (-not $Prompt)   { $Prompt   = Join-Path $ralphHome 'PROMPT.md' }
if (-not $Personas) { $Personas = Join-Path $ralphHome 'personas.txt' }
if (-not $Nouns)    { $Nouns    = Join-Path $ralphHome 'nouns.txt' }
if (-not $LogDir)   { $LogDir   = Join-Path $ralphHome 'logs' }

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

# Random pools (one item per line, '#'/blank lines ignored)
function Read-Pool([string]$file) {
  if (Test-Path $file) {
    Get-Content $file | Where-Object { $_.Trim() -and -not $_.Trim().StartsWith('#') }
  } else { @() }
}
# NOTE: don't reuse the [string]-typed param names ($Personas/$Nouns) for these
# arrays — PS var names are case-insensitive, so $personas would inherit the
# param's [string] constraint and coerce the array to one space-joined string.
$personaPool = Read-Pool $Personas
$nounPool    = Read-Pool $Nouns

# Lane-scope check helper. `git status` reports paths relative to the WORKTREE ROOT (e.g.
# realm-survivors/prototype/src/NN.js) but lane headers list short entries (src/NN.js), so an exact
# compare never matches — match by suffix instead.
function Test-PathSuffix([string]$file, $list) {
  foreach ($e in $list) { if ($file -ieq $e -or $file.EndsWith("/$e")) { return $true } }
  return $false
}

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

$banner = if ($Random) { "Ralph loop (RANDOM / anti-circling)" } else { "Ralph loop" }
Write-Host "=== $banner ===" -ForegroundColor Cyan
Write-Host "prompt : $Prompt"
Write-Host "iters  : $(if ($Iterations -eq 0) {'infinite (Ctrl-C to stop)'} else {$Iterations})"
if ($SkipPermissions) {
  Write-Host "WARNING: --dangerously-skip-permissions is ON. The agent runs unattended with full tool access in this repo. Make sure you can revert its changes." -ForegroundColor Yellow
}

# --- agent driver: how this loop invokes the coding agent each pass ------------------------------
# claude takes the prompt over STDIN (dodges arg-length limits) and the workspace Stop hook commits.
# agy (Antigravity/Gemini) takes the prompt as a -p ARG + --dangerously-skip-permissions, and does NOT fire the
# Stop hook — so the loop commits its increment itself (see the auto-commit fallback below).
switch ($Agent) {
  'claude' {
    $agentExe  = 'claude'
    $agentMode = 'stdin'
    $agentArgs = @('-p')
    $m = if ($Model) { $Model } else { 'claude-sonnet-4-6' }
    $agentArgs += @('--model', $m)
    if ($SkipPermissions) { $agentArgs += '--dangerously-skip-permissions' }
  }
  'agy' {
    # Antigravity CLI (Gemini), authenticated via your Antigravity SUBSCRIPTION (Pro/Ultra quota, NOT a
    # metered API key — agy ignores GEMINI_API_KEY). Flags verified against agy 1.0.13:
    #   -p <prompt>                      single non-interactive pass, prints the response
    #   --dangerously-skip-permissions   auto-approve all tool requests (unattended; same name as claude)
    #   --print-timeout <dur>            print-mode wait — DEFAULT 5m would cut a real increment short,
    #                                    so we raise it; a pass that legitimately needs >30m is too big.
    #   --model <id>                     optional; omit to use the account default (see `agy models`)
    # Do NOT add --sandbox (it restricts the terminal and would block `node test/sim.mjs`). Override any
    # of this via -AgentExtraArgs without editing here.
    $agentExe  = 'agy'
    $agentMode = 'arg'                                 # prompt passed as a -p argument, not stdin
    $agentArgs = @('--dangerously-skip-permissions', '--print-timeout', '30m')
    if ($Model) { $agentArgs += @('--model', $Model) }
  }
}
$agentArgs += $AgentExtraArgs

if ($Agent -eq 'agy') {
  # Ensure agy is findable. A parent shell launched BEFORE agy's installer ran — or the web launcher's
  # node process, or a spawned-from-stale-PATH window — can inherit a PATH without agy's bin dir, so the
  # `& agy` calls below would fail "command not found" and the lane dies. Refresh PATH from the registry
  # (where the installer recorded it) if agy isn't already resolvable.
  if (-not (Get-Command agy -ErrorAction SilentlyContinue)) {
    $env:Path = [Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [Environment]::GetEnvironmentVariable('Path','User')
  }
  Write-Host "Running auth pre-flight check..." -ForegroundColor Cyan
  # NOTE: `$x = & nativecmd 2>&1` is a terminating error under $ErrorActionPreference='Stop' the moment
  # the command writes ANYTHING to stderr (even on exit 0) — and `agy models` does. So wrap it: catch
  # that, judge by exit code, and WARN rather than throw. A real auth failure still surfaces in the pass
  # (which has its own auth-fail handling); a noisy-but-fine pre-flight must NOT kill the whole lane.
  $code = 0
  try { $null = & $agentExe models 2>&1; $code = $LASTEXITCODE }
  catch { $code = if ($LASTEXITCODE) { $LASTEXITCODE } else { 0 } }   # stderr-as-error: not a real failure
  if ($code -ne 0) {
    Write-Warning "agy pre-flight: 'agy models' exited $code. If passes fail with auth errors, run 'agy auth login'. Continuing."
  } else {
    Write-Host "Auth pre-flight passed." -ForegroundColor Green
  }
}

$i = 0
$consecutiveFails = 0   # circuit breaker: bail after N back-to-back agent failures
while ($Iterations -eq 0 -or $i -lt $Iterations) {
  $i++
  $stamp = Get-Date -Format 'yyyy-MM-dd_HH-mm-ss'

  # Pull the latest trunk into this lane (fleet mode) so the worker sees the current backlog/lane-header
  # and builds on the INTEGRATED game, not a stale fork — this is what makes the planner's re-partition
  # actually reach the worker. Trunk is gated-green by the refinery, so it won't import a regression.
  # Only merge when trunk is genuinely ahead (avoids churn) and the tree is clean; conflict => abort + skip.
  if ($SyncBase) {
    $curBranch = (git rev-parse --abbrev-ref HEAD 2>$null)
    if ($curBranch) { $curBranch = $curBranch.Trim() }
    if ($curBranch -and $curBranch -ne $SyncBase -and $curBranch -match '^ralph-' -and -not (git status --porcelain)) {
      $baseAhead = (git rev-list --count "$curBranch..$SyncBase" 2>$null)
      if ($baseAhead -and ([int]$baseAhead.Trim()) -gt 0) {
        git merge --no-edit $SyncBase 2>&1 | Out-Null
        if ($LASTEXITCODE -ne 0) {
          git merge --abort 2>&1 | Out-Null
          Write-Host "sync: '$SyncBase' merge conflicted -- skipped (lane retries next pass)." -ForegroundColor Yellow
        } else {
          Write-Host "sync: pulled $($baseAhead.Trim()) new '$SyncBase' commit(s) into $curBranch" -ForegroundColor DarkGray
        }
      }
    }
  }

  $iterPrompt = $basePrompt
  $modeLabel = 'plain'
  if ($Random) {
    $spice = Get-Spice
    # Append the anti-circling spice AFTER the (byte-identical) base prompt instead of prepending it,
    # so the long stable preamble stays a cacheable prefix across passes (prompt-cache hit on -p)
    # while the per-iteration tension still lands. The persona/recode framing reads fine trailing.
    $iterPrompt = $basePrompt + "`n`n---`n" + $spice.prefix + $spice.suffix
    $modeLabel = $spice.mode
  }

  $iterLabel = ("iteration {0}{1} [{2}] {3}" -f $i, $(if ($Iterations) {"/$Iterations"} else {''}), $modeLabel, $stamp)
  Write-Host ""
  Write-Host "--- $iterLabel ---" -ForegroundColor Green

  $log = Join-Path $LogDir ("iter-{0:0000}-{1}.log" -f $i, $stamp)
  # Self-describing UTF-8 header so a log read on its own says which pass/mode/model it was — the raw
  # claude stdout alone doesn't. (We also write the log OURSELVES as UTF-8: PS5.1 Tee-Object writes
  # UTF-16LE, which is why old logs open as spaced-out garbage with a BOM.)
  @(
    "=== $iterLabel ===",
    "model  : $Model",
    "spice  : $modeLabel  (anti-circling lens for THIS pass, not the task)",
    "prompt : $Prompt",
    ("-" * 72),
    ""
  ) | Set-Content -Path $log -Encoding utf8

  # Tag this pass so the auto-commit Stop hook (claude) makes a SEPARATE per-pass commit (bisectable
  # history for unattended runs) instead of amending one rolling checkpoint. Plain value only.
  $env:RALPH_PASS = "iter $i"
  # Run the agent; stream to console live AND capture the output, then append it to the log as UTF-8
  # (Tee-Object can't write UTF-8 in PS5.1). agy can drop final stdout when piped (non-TTY) — that's
  # why the change is also documented from git below, so an empty capture still leaves a useful log.
  if ($agentMode -eq 'stdin') {
    $iterPrompt | & $agentExe @agentArgs 2>&1 | Tee-Object -Variable passOut | Out-Host
  } else {
    # agy print mode MISBEHAVES when its stdout is a pipe/redirect (non-TTY): it drops output. So DON'T
    # pipe agy: let it inherit the window's real CONSOLE/TTY so its progress shows LIVE in the lane
    # window. For a TAILABLE real-time record, point agy's own --log-file at our logs dir.
    $agyLog = Join-Path $LogDir ("agy-{0:0000}-{1}.log" -f $i, $stamp)
    & $agentExe @agentArgs '-p' $iterPrompt --log-file $agyLog
    $passOut = @("(agy output is LIVE in the lane window; tailable agy log: $agyLog)")
  }
  Add-Content -Path $log -Value $passOut -Encoding utf8

  if ($LASTEXITCODE -ne 0) {
    # Narrow keywords so a normal pass mentioning "token"/"login" doesn't read as an auth failure.
    $isAuthFail = $false
    foreach ($line in $passOut) {
      if ($line -match 'auth|credential|sign-?in|re-?authenticate|keyring|unauthorized|\b401\b') { $isAuthFail = $true; break }
    }
    if ($isAuthFail) {
      [Console]::Beep(440, 500)
      Write-Host 'CRITICAL: agy auth failure - re-authenticate; the loop cannot fix this.' -ForegroundColor Red
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

  # File Path Validation & Auto-commit
  $wt = (Get-Location).Path
  $statusLines = (git -C $wt status --porcelain)
  if ($statusLines) {
    # Check if we are running in a scoped lane branch (ralph-<lane>)
    $branch = (git -C $wt rev-parse --abbrev-ref HEAD).Trim()
    if ($branch -match '^ralph-(.+)$') {
      $laneName = $Matches[1]
      $laneHeaderFile = Join-Path $ralphHome "lane-$laneName.md"
      if (Test-Path $laneHeaderFile) {
        $headerContent = Get-Content -Raw -Path $laneHeaderFile
        # Match BOTH "YOUR file (edit freely):" (single-file lanes) and "YOUR files ..." (multi-file),
        # and "SHARED SEAM"/"SHARED SEAMS". Matching only the PLURAL left $ownedFiles EMPTY for the
        # single-file lanes (enemies/art/audio), so EVERY legit edit read as unauthorized.
        $ownedLine = $headerContent -split "`n" | Where-Object { $_ -match 'YOUR files?\s*\(edit freely\)' } | Select-Object -First 1
        $seamsLine = $headerContent -split "`n" | Where-Object { $_ -match 'SHARED SEAMS?' } | Select-Object -First 1

        $ownedFiles = @()
        if ($ownedLine) { foreach ($m in [regex]::Matches($ownedLine, '`src/[^`]+`')) { $ownedFiles += $m.Value.Trim('`').Trim().Replace('\', '/') } }
        $seamFiles = @()
        if ($seamsLine) { foreach ($m in [regex]::Matches($seamsLine, '`src/[^`]+`')) { $seamFiles += $m.Value.Trim('`').Trim().Replace('\', '/') } }

        # Always allowed: union-merged trail docs, ralph tooling, generated assets, and TEST helpers (the
        # PROMPT tells workers to verify via test/sim.mjs and may add directed checks under test/).
        $allowedSuffixes = @('DONE.md', 'NOTES.md', 'FEEL-REVIEW.md', 'TODO.md')

        $invalidFiles = @()
        foreach ($line in $statusLines) {
          if ($line -match '^\s*[MADRCU?]{1,2}\s+(.+)$') {
            $fileStr = $Matches[1].Trim()
            $file = if ($fileStr -match '->\s+(.+)$') { $Matches[1].Trim().Replace('\', '/') } else { $fileStr.Replace('\', '/') }
            if (Test-PathSuffix $file $allowedSuffixes) { continue }
            if ($file -match '(^|/)(ralph|assets|test)/') { continue }
            if (Test-PathSuffix $file $ownedFiles) { continue }
            if (Test-PathSuffix $file $seamFiles) { Write-Host "  seam touch: $file" -ForegroundColor DarkYellow; continue }
            $invalidFiles += $file
          }
        }

        # Out-of-scope edits: WARN, never destroy. The previous code did `reset --hard` + `clean -fd` +
        # throw here, which DISCARDED real agent work and killed the lane (and an empty/mis-parsed owned
        # list flagged everything). The refinery is the real guard: if an out-of-scope edit causes a
        # cross-lane MERGE conflict it aborts + flags that lane. A flagged merge beats lost work.
        if ($invalidFiles.Count -gt 0) {
          Write-Host "NOTE: lane '$laneName' touched out-of-scope files (committing anyway; refinery catches real conflicts): $($invalidFiles -join ', ')" -ForegroundColor Yellow
        }
      }
    }

    # Commit after validation passes
    git -C $wt add -A 2>&1 | Out-Null
    git -C $wt commit -q -m "ralph $($env:RALPH_PASS) [$Agent] $stamp" 2>&1 | Out-Null
  }

  # Record what the pass actually changed
  try {
    $stat = (git -C $wt show --stat --format='commit %h  %s' HEAD 2>&1) -join "`n"
    Add-Content -Path $log -Value @('', ('-' * 72), '--- files changed this pass ---', $stat) -Encoding utf8
  } catch {}

  if ($SleepSeconds -gt 0 -and ($Iterations -eq 0 -or $i -lt $Iterations)) {
    Start-Sleep -Seconds $SleepSeconds
  }
}

Write-Host ""
Write-Host "=== Ralph loop done ($i iterations). Logs in $LogDir ===" -ForegroundColor Cyan
