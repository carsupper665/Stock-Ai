# $Only must not be named like the loop variable: PowerShell variable names are case-insensitive
# and the [string[]] constraint would then coerce each service hashtable to a string.
param([string[]]$Only = @('backend','llm-provider-server','agent-server','middleware','nginx'))
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
foreach ($key in @('LLM_SERVER_CODEX_EXECUTABLE','LLM_SERVER_CLAUDE_EXECUTABLE','LLM_SERVER_CODEX_AUTH_FILE','LLM_SERVER_CLAUDE_AUTH_FILE','RELEASE_TLS_CERT','RELEASE_TLS_KEY')) {
    $value = [Environment]::GetEnvironmentVariable($key, 'Process')
    if ($value -and ![IO.Path]::IsPathRooted($value)) { [Environment]::SetEnvironmentVariable($key, (Join-Path $root $value), 'Process') }
}
foreach ($directory in @('data', 'logs')) { New-Item -ItemType Directory -Path (Join-Path $root $directory) -Force | Out-Null }
$nginxDir = Join-Path $root 'nginx'
$nginxConfDir = Join-Path $nginxDir 'conf'
$nginxMainConf = Join-Path $nginxConfDir 'stock-ai-nginx.conf'
# RELEASE_APP_HOST is host[:port] as users open it; nginx needs the bare host name and the port separately.
$app = [Uri]('https://' + $env:RELEASE_APP_HOST + '/')
$services = @(
    @{ Name = 'backend'; Address = $env:BACKEND_URL; Health = '/v1/health'; Status = 200 },
    @{ Name = 'llm-provider-server'; Address = $env:LLM_SERVER_URL; Health = '/healthz'; Status = 200 },
    @{ Name = 'agent-server'; Address = $env:AGENT_SERVER_URL; Health = '/health'; Status = 200 },
    @{ Name = 'middleware'; Address = 'http://' + $env:MIDDLEWARE_ADDR; Health = '/healthz'; Status = 200 },
    @{ Name = 'nginx'; Address = 'https://127.0.0.1:' + $app.Port; Health = '/healthz'; Status = 200; Binary = (Join-Path $nginxDir 'nginx.exe')
       Arguments = @('-p', ('"' + $nginxDir.Replace('\', '/') + '/"'), '-c', ('"' + $nginxMainConf.Replace('\', '/') + '"')) }
)
$services = @($services | Where-Object { $_.Name -in $Only })
if (!$services.Count -or @($Only | Where-Object { $_ -notin $services.Name }).Count) { throw 'Unknown service name' }
foreach ($service in $services) {
    $binary = if ($service.Binary) { $service.Binary } else { Join-Path $root ($service.Name + '.exe') }
    if (!(Test-Path -LiteralPath $binary)) { throw "Missing $binary" }
    $uri = [Uri]$service.Address
    if (Get-NetTCPConnection -LocalPort $uri.Port -State Listen -ErrorAction SilentlyContinue) { throw "Port $($uri.Port) is already in use. Stop the old service before starting this distribution." }
}
foreach ($service in $services) {
    if ($service.Name -eq 'nginx') {
        # Fixed-string substitution of the shipped nginx templates; the Docker paths become local ones.
        $slash = { param([string]$path) $path.Replace('\', '/') }
        $logs = & $slash (Join-Path $root 'logs')
        $edgeConf = Join-Path $nginxConfDir 'stock-ai-edge.conf'
        $edgeLines = [IO.File]::ReadAllText((Join-Path $nginxConfDir 'release-edge.conf.template')) -split "`r?`n" | Where-Object { $_ -notlike '*/etc/nginx/release-extra/*' }
        $edge = ($edgeLines -join "`n").Replace('${RELEASE_APP_HOST}', $env:RELEASE_APP_HOST).Replace('${RELEASE_SERVER_NAME}', $app.Host).
            Replace('middleware:4180', $env:MIDDLEWARE_ADDR).Replace('listen 8443 ssl', "listen $($app.Port) ssl").
            Replace('/run/release-tls/fullchain.pem', ('"' + (& $slash $env:RELEASE_TLS_CERT) + '"')).Replace('/run/release-tls/privkey.pem', ('"' + (& $slash $env:RELEASE_TLS_KEY) + '"')).
            Replace('/usr/share/nginx/html', 'html')
        $main = [IO.File]::ReadAllText((Join-Path $nginxConfDir 'release-nginx.conf')).
            Replace('include /tmp/release-edge.conf;', ('include "' + (& $slash $edgeConf) + '";')).
            Replace('/dev/stdout', ('"' + $logs + '/nginx-access.log"')).Replace('/dev/stderr', ('"' + $logs + '/nginx-error.log"')).Replace('/tmp/', 'temp/')
        New-Item -ItemType Directory -Path (Join-Path $nginxDir 'temp') -Force | Out-Null
        [IO.File]::WriteAllText($edgeConf, $edge, [Text.UTF8Encoding]::new($false))
        [IO.File]::WriteAllText($nginxMainConf, $main, [Text.UTF8Encoding]::new($false))
    }
    # Only the middleware runs in Release mode; the other services keep development mode.
    $env:STOCK_AI_ENVIRONMENT = $(if ($service.Name -eq 'middleware') { 'Release' } else { $null })
    $startArguments = @{ FilePath = $(if ($service.Binary) { $service.Binary } else { Join-Path $root ($service.Name + '.exe') }); WorkingDirectory = $root; PassThru = $true }
    if ($service.Arguments) { $startArguments.ArgumentList = $service.Arguments }
    $process = Start-Process @startArguments
    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    $ready = $false
    while (!$ready -and [DateTime]::UtcNow -lt $deadline) {
        if ($process.HasExited) { throw "$($service.Name) exited. Inspect its console window or logs." }
        $request = @{ Uri = ($service.Address + $service.Health); TimeoutSec = 2 }
        if ($service.Name -eq 'nginx') { $request.SkipCertificateCheck = $true; $request.Headers = @{ Host = $env:RELEASE_APP_HOST } }
        try { $response = Invoke-WebRequest @request; $ready = $response.StatusCode -eq $service.Status } catch { Start-Sleep -Milliseconds 200 }
    }
    if (!$ready) { throw "$($service.Name) health check timed out; inspect its window. Previously started services remain running." }
    Write-Host "$($service.Name) ready (PID $($process.Id))"
}
Write-Host ('Open https://' + $env:RELEASE_APP_HOST + '/')
Write-Host 'Stop nginx with: nginx\nginx.exe -s stop -p nginx -c nginx\conf\stock-ai-nginx.conf (run from this folder).'
Write-Host 'Stop the other services with Ctrl+C in their windows (Agent first, then middleware, LLM Provider, Backend). Keep .env, secrets and data together.'
