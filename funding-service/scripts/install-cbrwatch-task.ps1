# Installs the background `cbrwatch` latency tracker without requiring admin rights.
# It builds the binary, drops a hidden launcher into the user's Startup folder (so
# it auto-starts at every logon), and starts it immediately. cbrwatch sleeps
# outside the 16:00-19:00 MSK window, so it is cheap to keep running.
#
# Run:    powershell -ExecutionPolicy Bypass -File install-cbrwatch-task.ps1
# Stop:   Get-Process cbrwatch -ErrorAction SilentlyContinue | Stop-Process
# Remove: Remove-Item "$([Environment]::GetFolderPath('Startup'))\cbrwatch.vbs"
#
# Note: a Scheduled Task would also work but requires an elevated (admin) shell;
# the Startup-folder approach is equivalent for an always-logged-in dev machine.

$ErrorActionPreference = 'Stop'

$root    = Split-Path -Parent $PSScriptRoot          # ...\funding-service
$backend = Join-Path $root 'backend'
$bin     = Join-Path $root 'logs\bin'
$exe     = Join-Path $bin 'cbrwatch.exe'

New-Item -ItemType Directory -Force -Path $bin | Out-Null

Write-Host "Building cbrwatch.exe..."
Push-Location $backend
try {
    & go build -o $exe ./cmd/cbrwatch
    if ($LASTEXITCODE -ne 0) { throw "go build failed with exit code $LASTEXITCODE" }
} finally {
    Pop-Location
}
Write-Host "Built: $exe"

# Hidden VBS launcher in the Startup folder (window style 0 = hidden).
$startup = [Environment]::GetFolderPath('Startup')
$vbs     = Join-Path $startup 'cbrwatch.vbs'
$vbsBody = "Set s = CreateObject(""WScript.Shell"")" + "`r`n" +
           "s.Run """"""$exe"""""", 0, False"
Set-Content -Path $vbs -Value $vbsBody -Encoding ascii
Write-Host "Startup launcher: $vbs"

# Start now if not already running.
$running = Get-Process cbrwatch -ErrorAction SilentlyContinue
if ($running) {
    Write-Host "cbrwatch already running (PID $($running.Id))."
} else {
    & wscript.exe $vbs
    Start-Sleep -Seconds 2
    $now = Get-Process cbrwatch -ErrorAction SilentlyContinue
    if ($now) { Write-Host "Started cbrwatch (PID $($now.Id))." }
    else { Write-Host "WARNING: cbrwatch did not start; launch manually: $exe" }
}

$logsPath = Join-Path $root 'logs'
Write-Host ""
Write-Host "Done. Logs: $logsPath"
Write-Host "  cbrwatch-summary.md      - human/agent-readable breakdown"
Write-Host "  cbrwatch-YYYY-MM-DD.jsonl - raw events"
