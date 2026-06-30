<#
.SYNOPSIS
  Stop the single-agent Workshop loop and its in-flight agent pass.

.DESCRIPTION
  Kills (1) the loop's powershell whose cmdline references workshop.ps1, and (2) any unattended
  claude.exe / agy.exe child it spawned (the --dangerously-skip-permissions fingerprint, parented by
  that loop). Scoped to the Workshop engine so it never touches a fleet loop.
#>
[CmdletBinding()]
param()
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8

# Match the ENGINE only: `\workshop.ps1` (path-sep before), NOT the substring inside this script's own
# name (start-/stop-workshop.ps1) — otherwise stop would try to kill itself.
$loops = @(Get-CimInstance Win32_Process -Filter "Name='powershell.exe'" -ErrorAction SilentlyContinue |
  Where-Object { $_.CommandLine -match '[\\/]workshop\.ps1' })

$loopPids = $loops | ForEach-Object { $_.ProcessId }

# agent children of those loops (kill the in-flight pass too, not just the wrapper).
$agents = @(Get-CimInstance Win32_Process -Filter "Name='claude.exe' OR Name='agy.exe'" -ErrorAction SilentlyContinue |
  Where-Object { $_.CommandLine -match 'dangerously-skip-permissions' -and $loopPids -contains $_.ParentProcessId })

$killed = 0
foreach ($c in $agents) { try { Stop-Process -Id $c.ProcessId -Force -ErrorAction Stop; $killed++ } catch {} }
foreach ($l in $loops)  { try { Stop-Process -Id $l.ProcessId -Force -ErrorAction Stop; $killed++ } catch {} }

if ($killed) { Write-Host "Stopped Workshop loop ($killed process(es))." -ForegroundColor Green }
else         { Write-Host "No Workshop loop running." -ForegroundColor Yellow }
