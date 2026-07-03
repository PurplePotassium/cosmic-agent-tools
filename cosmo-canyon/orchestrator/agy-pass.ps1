# Cosmo Canyon - agy (free Gemini) worker dispatch. The PROVEN own-console recipe
# (shared-knowledge agy_cli_headless_gotchas section 6 / PLAN section 12).
#
# MUST run in its OWN console with NO stdout redirect: CC's Bash/PS tool redirects child
# stdout -> agy hangs at 0 CPU (upstream non-TTY drop, issue #76). So the dispatching tick
# launches THIS via `Start-Process powershell -File agy-pass.ps1` (no -RedirectStandardOutput).
# Inside here `& agy -p` inherits the real console. Verify agy's work by SIDE-EFFECT only
# (git diff vs BASE_SHA) - agy print stdout is uncapturable; --log-file is operational only.
#
# Writes this runner's pid to PidFile (.agy.pid) BEFORE calling agy (section 13.16): agy is in
# its own console, NOT a child of the claude tick, so killing the tick tree won't catch it - the
# supervisor / cc-stop / next-tick read .agy.pid to kill or refuse. Removed in finally on exit.
#
# ASCII-only (PS5.1 reads BOM-less .ps1 as ANSI; non-ASCII breaks the parse - FC gotcha).
[CmdletBinding()]
param(
  [Parameter(Mandatory = $true)][string]$PromptFile,
  [Parameter(Mandatory = $true)][string]$GameDir,
  [string]$LogFile = "",
  [string]$PidFile = "",
  [string]$Model = "gemini-3.5-flash",
  [string]$PrintTimeout = "30m"   # agy default is 5m and kills a real pass -> EXIT=1, no edit
)

$ErrorActionPreference = "Stop"
if ($PidFile) { Set-Content -LiteralPath $PidFile -Value "$PID" -NoNewline -Encoding ascii }
try {
  if (-not (Test-Path -LiteralPath $PromptFile)) { Write-Error "prompt file missing: $PromptFile"; exit 2 }
  $prompt = Get-Content -Raw -LiteralPath $PromptFile
  Set-Location -LiteralPath $GameDir
  $agyArgs = @("-p", $prompt, "--model", $Model, "--print-timeout", $PrintTimeout, "--dangerously-skip-permissions")
  if ($LogFile) { $agyArgs += @("--log-file", $LogFile) }
  # own console, NO redirect (the whole point). Verify by git side-effect, not this output.
  & agy @agyArgs
  exit $LASTEXITCODE
}
finally {
  if ($PidFile -and (Test-Path -LiteralPath $PidFile)) {
    Remove-Item -LiteralPath $PidFile -Force -ErrorAction SilentlyContinue
  }
}
