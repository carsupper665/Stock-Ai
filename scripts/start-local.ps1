param([switch]$NoBuild)
$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
$configPath = Join-Path $root '.claude/launch.json'
if (!(Test-Path -LiteralPath $configPath)) { throw 'Local service configuration .claude/launch.json is required.' }
$configuration = Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json
$output = Join-Path ([System.IO.Path]::GetTempPath()) 'stock-ai-local'
New-Item -ItemType Directory -Path $output -Force | Out-Null
$services = @(
    @{ Name = 'backend'; Directory = 'backend'; Package = '.'; Port = 7794 },
    @{ Name = 'llm-server'; Directory = 'llm_provider_server'; Package = './cmd/llm-provider-server'; Port = 8090 },
    @{ Name = 'agent-server'; Directory = 'agent_server'; Package = './cmd/agent-server'; Port = 8080 }
)
foreach ($service in $services) {
    $existing = Get-NetTCPConnection -State Listen -LocalPort $service.Port -ErrorAction SilentlyContinue
    if ($existing) { Write-Host "$($service.Name): already listening on $($service.Port); left running"; continue }
    $directory = Join-Path $root $service.Directory
    $binary = Join-Path $output "$($service.Name).exe"
    if (!$NoBuild -or !(Test-Path -LiteralPath $binary)) {
        & go -C $directory build -o $binary $service.Package
        if ($LASTEXITCODE -ne 0) { throw "Build failed: $($service.Name)" }
    }
    $entry = $configuration.configurations | Where-Object name -EQ $service.Name
    if (!$entry) { throw "Missing local configuration: $($service.Name)" }
    $command = $entry.runtimeArgs[-1]
    $saved = @{}
    try {
        # Read only literal environment assignments; never evaluate shell configuration.
        foreach ($match in [regex]::Matches($command, '\$env:([A-Z_]+)=''([^'']*)''')) {
            $key = $match.Groups[1].Value
            $saved[$key] = [Environment]::GetEnvironmentVariable($key, 'Process')
            [Environment]::SetEnvironmentVariable($key, $match.Groups[2].Value, 'Process')
        }
        $process = Start-Process -FilePath $binary -WorkingDirectory $directory -PassThru -WindowStyle Hidden `
            -RedirectStandardOutput (Join-Path $output "$($service.Name).out.log") `
            -RedirectStandardError (Join-Path $output "$($service.Name).err.log")
    } finally {
        foreach ($key in $saved.Keys) {
            if ($null -eq $saved[$key]) { Remove-Item -LiteralPath "Env:$key" -ErrorAction SilentlyContinue }
            else { [Environment]::SetEnvironmentVariable($key, $saved[$key], 'Process') }
        }
    }
    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    do {
        if ($process.HasExited) { throw "$($service.Name) exited; inspect $output/$($service.Name).err.log" }
        $ready = Get-NetTCPConnection -State Listen -LocalPort $service.Port -ErrorAction SilentlyContinue
        if (!$ready) { Start-Sleep -Milliseconds 200 }
    } while (!$ready -and [DateTime]::UtcNow -lt $deadline)
    if (!$ready) { throw "$($service.Name) readiness timed out; inspect $output" }
    Write-Host "$($service.Name): http://127.0.0.1:$($service.Port) (PID $($process.Id))"
}
if (!(Get-NetTCPConnection -State Listen -LocalPort 5173 -ErrorAction SilentlyContinue)) {
    $vite = Join-Path $root 'frontend/node_modules/vite/bin/vite.js'
    $node = (Get-Command node).Source
    $process = Start-Process -FilePath $node -ArgumentList @('"' + $vite + '"', '--host', '127.0.0.1', '--port', '5173', '--strictPort') `
        -WorkingDirectory (Join-Path $root 'frontend') -PassThru -WindowStyle Hidden `
        -RedirectStandardOutput (Join-Path $output 'frontend.out.log') -RedirectStandardError (Join-Path $output 'frontend.err.log')
    Write-Host "frontend starting (PID $($process.Id))"
}
Write-Host "Open http://127.0.0.1:5173 ; local logs: $output"
