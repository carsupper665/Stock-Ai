# Starts the Windows test package: Backend, LLM Provider, Agent, then the no-login middleware,
# which also serves the frontend. Works in Windows PowerShell 5.1 and PowerShell 7.
$ErrorActionPreference = 'Stop'
$root = $PSScriptRoot
$envFile = Join-Path $root '.env'
if (!(Test-Path -LiteralPath $envFile)) { throw 'Missing .env next to start.ps1; rebuild with scripts/build-win-test.sh' }
foreach ($line in Get-Content -LiteralPath $envFile) {
    $line = $line.Trim()
    if (!$line -or $line.StartsWith('#')) { continue }
    $parts = $line.Split('=', 2)
    if ($parts.Count -ne 2) { throw "Invalid .env line: $line" }
    $value = $parts[1].Trim()
    # *_FILE, *_PATH, *_DIR and WEB_DIR are relative to this folder.
    if (($parts[0] -match '_FILE$|_PATH$|_DIR$|^DB_DSN$') -and $value -and ![IO.Path]::IsPathRooted($value)) { $value = Join-Path $root $value }
    [Environment]::SetEnvironmentVariable($parts[0].Trim(), $value, 'Process')
}
foreach ($directory in @('data', 'logs')) { New-Item -ItemType Directory -Path (Join-Path $root $directory) -Force | Out-Null }
$pidFile = Join-Path $root 'data\pids.json'
$services = @(
    @{ Name = 'backend'; Address = $env:BACKEND_URL; Health = '/v1/health' },
    @{ Name = 'llm-provider-server'; Address = $env:LLM_SERVER_URL; Health = '/healthz' },
    @{ Name = 'agent-server'; Address = $env:AGENT_SERVER_URL; Health = '/health' },
    @{ Name = 'middleware'; Address = 'http://' + $env:MIDDLEWARE_ADDR; Health = '/healthz' }
)
foreach ($service in $services) {
    if (!(Test-Path -LiteralPath (Join-Path $root ($service.Name + '.exe')))) { throw "Missing $($service.Name).exe" }
    $port = ([Uri]$service.Address).Port
    if (Get-NetTCPConnection -LocalPort $port -State Listen -ErrorAction SilentlyContinue) { throw "Port $port is already in use. Run stop.ps1 or stop whatever is using it first." }
}
$started = [ordered]@{}
foreach ($service in $services) {
    # Only the middleware runs in Test mode; the internal services keep development mode.
    $env:STOCK_AI_ENVIRONMENT = $(if ($service.Name -eq 'middleware') { 'Test' } else { $null })
    $process = Start-Process -FilePath (Join-Path $root ($service.Name + '.exe')) -WorkingDirectory $root -WindowStyle Hidden -PassThru `
        -RedirectStandardOutput (Join-Path $root "logs\$($service.Name).out.log") -RedirectStandardError (Join-Path $root "logs\$($service.Name).err.log")
    $started[$service.Name] = $process.Id
    $started | ConvertTo-Json | Set-Content -LiteralPath $pidFile -Encoding UTF8
    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    $ready = $false
    while (!$ready -and [DateTime]::UtcNow -lt $deadline) {
        if ($process.HasExited) { throw "$($service.Name) exited; see logs\$($service.Name).err.log. Run stop.ps1 to stop the others." }
        try { $ready = (Invoke-WebRequest -Uri ($service.Address + $service.Health) -UseBasicParsing -TimeoutSec 2).StatusCode -eq 200 } catch { Start-Sleep -Milliseconds 300 }
    }
    if (!$ready) { throw "$($service.Name) did not become healthy in 30 seconds; see logs\$($service.Name).err.log. Run stop.ps1 to stop the others." }
    Write-Host "$($service.Name) ready (PID $($process.Id))"
}
$url = 'http://' + $env:MIDDLEWARE_ADDR + '/'
Write-Host "Open $url  (no login; stop everything with stop.ps1)"
Start-Process $url
