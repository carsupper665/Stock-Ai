# $Only must not be named like the loop variable: PowerShell variable names are case-insensitive
# and the [string[]] constraint would then coerce each service hashtable to a string.
param([string[]]$Only = @('backend','llm-provider-server','agent-server','console'))
$ErrorActionPreference = 'Stop'
$root = $PSScriptRoot
$envFile = Join-Path $root '.env'
if (!(Test-Path -LiteralPath $envFile)) { throw 'Missing .env next to start.ps1' }
foreach ($line in Get-Content -LiteralPath $envFile) {
    $line = $line.Trim()
    if (!$line -or $line.StartsWith('#')) { continue }
    $parts = $line.Split('=', 2)
    if ($parts.Count -ne 2) { throw 'Invalid .env line' }
    $value = $parts[1].Trim()
    if ($value.Length -ge 2 -and (($value.StartsWith('"') -and $value.EndsWith('"')) -or ($value.StartsWith("'") -and $value.EndsWith("'")))) { $value = $value.Substring(1, $value.Length-2) }
    [Environment]::SetEnvironmentVariable($parts[0].Trim(), $value, 'Process')
}
foreach ($key in @('LLM_SERVER_CODEX_EXECUTABLE','LLM_SERVER_CLAUDE_EXECUTABLE','LLM_SERVER_CODEX_AUTH_FILE','LLM_SERVER_CLAUDE_AUTH_FILE')) {
    $value = [Environment]::GetEnvironmentVariable($key, 'Process')
    if ($value -and ![IO.Path]::IsPathRooted($value)) { [Environment]::SetEnvironmentVariable($key, (Join-Path $root $value), 'Process') }
}
foreach ($directory in @('data', 'logs')) { New-Item -ItemType Directory -Path (Join-Path $root $directory) -Force | Out-Null }
$services = @(
    @{ Name = 'backend'; Address = $env:BACKEND_URL; Health = '/v1/health' },
    @{ Name = 'llm-provider-server'; Address = $env:LLM_SERVER_URL; Health = '/healthz' },
    @{ Name = 'agent-server'; Address = $env:AGENT_SERVER_URL; Health = '/health' },
    @{ Name = 'console'; Address = $(if ($env:CONSOLE_TLS_CERT) { 'https://localhost:' + $env:CONSOLE_ADDR.Split(':')[-1] } else { 'http://127.0.0.1:' + $env:CONSOLE_ADDR.Split(':')[-1] }); Health = '/' }
)
$services = @($services | Where-Object { $_.Name -in $Only })
if (!$services.Count -or @($Only | Where-Object { $_ -notin $services.Name }).Count) { throw 'Unknown service name' }
foreach ($service in $services) {
    $binary = Join-Path $root ($service.Name + '.exe')
    if (!(Test-Path -LiteralPath $binary)) { throw "Missing $binary" }
    $uri = [Uri]$service.Address
    if (Get-NetTCPConnection -LocalPort $uri.Port -State Listen -ErrorAction SilentlyContinue) { throw "Port $($uri.Port) is already in use. Stop the old service before starting this distribution." }
}
foreach ($service in $services) {
    $process = Start-Process -FilePath (Join-Path $root ($service.Name + '.exe')) -WorkingDirectory $root -PassThru
    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    $ready = $false
    while (!$ready -and [DateTime]::UtcNow -lt $deadline) {
        if ($process.HasExited) { throw "$($service.Name) exited. Inspect its console window or logs." }
        try { $response = Invoke-WebRequest -Uri ($service.Address + $service.Health) -TimeoutSec 2; $ready = $response.StatusCode -eq 200 } catch { Start-Sleep -Milliseconds 200 }
    }
    if (!$ready) { throw "$($service.Name) health check timed out; inspect its window. Previously started services remain running." }
    Write-Host "$($service.Name) ready (PID $($process.Id))"
}
Write-Host ('Open ' + $services[-1].Address)
Write-Host 'Stop with Ctrl+C in the service windows (Agent first, then Gateway/Backend). Keep .env and data together.'
