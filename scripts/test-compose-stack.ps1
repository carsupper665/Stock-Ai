[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('Exercise', 'BeforeDockerRestart', 'AfterDockerRestart')]
    [string]$Phase,
    [Parameter(Mandatory = $true)]
    [string]$EnvFile,
    [Parameter(Mandatory = $true)]
    [string]$EvidenceDirectory,
    [string]$ComposeFile = (Join-Path $PSScriptRoot '..\deploy\compose\compose.yaml'),
    [int]$HealthTimeoutSeconds = 180
)

$ErrorActionPreference = 'Stop'
$composePath = (Resolve-Path -LiteralPath $ComposeFile).ProviderPath
$envPath = (Resolve-Path -LiteralPath $EnvFile).ProviderPath
$evidencePath = [IO.Path]::GetFullPath($EvidenceDirectory)
if (!(Test-Path -LiteralPath $evidencePath)) {
    New-Item -ItemType Directory -Path $evidencePath | Out-Null
}

function Invoke-Docker([string[]]$Arguments) {
    $output = & docker @Arguments 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "docker command failed: docker $($Arguments -join ' ')`n$output"
    }
    return $output
}

function Invoke-Compose([string[]]$Arguments) {
    $allArguments = @('compose', '--env-file', $envPath, '-f', $composePath) + $Arguments
    return Invoke-Docker $allArguments
}

function Get-ServiceContainer([string]$Service) {
    $identifier = (Invoke-Compose @('ps', '--all', '-q', $Service) | Out-String).Trim()
    if ([string]::IsNullOrWhiteSpace($identifier)) {
        throw "$Service has no container"
    }
    return $identifier
}

function Wait-Healthy {
    $deadline = [DateTime]::UtcNow.AddSeconds($HealthTimeoutSeconds)
    $services = @('backend', 'llm-provider-server', 'agent-server', 'console')
    while ([DateTime]::UtcNow -lt $deadline) {
        $pending = @()
        foreach ($service in $services) {
            $identifier = Get-ServiceContainer $service
            $status = (Invoke-Docker @('inspect', '--format', '{{.State.Status}}/{{.State.Health.Status}}', $identifier) | Out-String).Trim()
            if ($status -ne 'running/healthy') {
                $pending += "$service=$status"
            }
        }
        if ($pending.Count -eq 0) { return }
        Start-Sleep -Seconds 2
    }
    throw "health timeout: $($pending -join ', ')"
}

function Assert-NetworkBoundary {
    foreach ($service in @('backend', 'llm-provider-server', 'agent-server', 'console')) {
        $identifier = Get-ServiceContainer $service
        $bindings = (Invoke-Docker @('inspect', '--format', '{{json .HostConfig.PortBindings}}', $identifier) | Out-String).Trim()
        if ($bindings -ne 'null' -and $bindings -ne '{}') {
            throw "$service unexpectedly publishes a host port: $bindings"
        }
        $networks = (Invoke-Docker @('inspect', '--format', '{{json .NetworkSettings.Networks}}', $identifier) | Out-String).Trim() | ConvertFrom-Json
        $names = @($networks.PSObject.Properties.Name)
        $application = $names | Where-Object { $_ -like '*_application' }
        $edge = $names | Where-Object { $_ -like '*_edge' }
        if ($application.Count -ne 1) { throw "$service is not on exactly one application network" }
        if ($service -eq 'console') {
            if ($edge.Count -ne 1) { throw 'Console is not on exactly one designated edge network' }
        } elseif ($edge.Count -ne 0) {
            throw "$service unexpectedly joins the edge network"
        }
    }
}

function Get-PythonCommand {
    $launcher = Get-Command 'py.exe' -ErrorAction SilentlyContinue
    if ($launcher) { return @{ Command = $launcher.Source; Prefix = @('-3') } }
    $python = Get-Command 'python.exe' -ErrorAction SilentlyContinue
    if ($python) { return @{ Command = $python.Source; Prefix = @() } }
    throw 'Python 3 is required to run SQLite integrity_check.'
}

function Save-StoppedState([string]$Name) {
    $destination = Join-Path $evidencePath $Name
    if (Test-Path -LiteralPath $destination) {
        throw "evidence destination already exists: $destination"
    }
    New-Item -ItemType Directory -Path $destination | Out-Null
    $databaseFiles = @{
        backend = 'backend.db'
        'agent-server' = 'agent-server.db'
        'llm-provider-server' = 'llm-provider.db'
    }
    $arguments = @()
    foreach ($entry in $databaseFiles.GetEnumerator()) {
        $identifier = Get-ServiceContainer $entry.Key
        $target = Join-Path $destination $entry.Value
        Invoke-Docker @('cp', "${identifier}:/data/$($entry.Value)", $target) | Out-Null
        $arguments += @('--database', "$($entry.Key)=$target")
    }
    $agent = Get-ServiceContainer 'agent-server'
    $history = Join-Path $destination 'history'
    Invoke-Docker @('cp', "${agent}:/data/history", $history) | Out-Null
    $historyEvidence = @(
        Get-ChildItem -LiteralPath $history -File -Recurse | Sort-Object FullName | ForEach-Object {
            [ordered]@{
                relative_path = [IO.Path]::GetRelativePath($history, $_.FullName)
                sha256 = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
            }
        }
    )
    ConvertTo-Json -InputObject @($historyEvidence) -Depth 4 | Set-Content -LiteralPath (Join-Path $destination 'history.json') -Encoding utf8NoBOM

    $python = Get-PythonCommand
    $tool = Join-Path $PSScriptRoot 'compose_sqlite_evidence.py'
    $fingerprint = Join-Path $destination 'sqlite.json'
    $pythonArguments = @($python.Prefix) + @($tool) + $arguments + @('--output', $fingerprint)
    & $python.Command @pythonArguments | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "SQLite evidence failed for $Name" }
    return $destination
}

function Assert-StateEqual([string]$Expected, [string]$Actual) {
    foreach ($file in @('sqlite.json', 'history.json')) {
        $left = (Get-Content -LiteralPath (Join-Path $Expected $file) -Raw).Trim()
        $right = (Get-Content -LiteralPath (Join-Path $Actual $file) -Raw).Trim()
        if ($left -ne $right) { throw "persistent logical state changed: $file" }
    }
}

function Start-Stack {
    Invoke-Compose @('up', '-d', '--wait', '--wait-timeout', "$HealthTimeoutSeconds") | Out-Null
    Wait-Healthy
    Assert-NetworkBoundary
}

Invoke-Compose @('config', '--quiet') | Out-Null
$server = (Invoke-Docker @('version', '--format', '{{.Server.Os}} {{.Server.Version}}') | Out-String).Trim()
if (!$server.StartsWith('linux ')) { throw "Docker must be in Linux-container mode; found $server" }

switch ($Phase) {
    'Exercise' {
        Start-Stack
        $crashes = @()
        foreach ($service in @('agent-server', 'llm-provider-server', 'backend')) {
            $before = Get-ServiceContainer $service
            Invoke-Docker @('kill', '--signal', 'KILL', $before) | Out-Null
            Invoke-Compose @('up', '-d', $service) | Out-Null
            Wait-Healthy
            $after = Get-ServiceContainer $service
            $crashes += [ordered]@{ service = $service; before = $before; after = $after; recovered = $true }
        }
        $crashes | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath (Join-Path $evidencePath 'crash-recovery.json') -Encoding utf8NoBOM

        Invoke-Compose @('stop') | Out-Null
        $baseline = Save-StoppedState 'before-compose-restart'
        Invoke-Compose @('start') | Out-Null
        Wait-Healthy
        Invoke-Compose @('restart') | Out-Null
        Wait-Healthy
        Invoke-Compose @('stop') | Out-Null
        $afterRestart = Save-StoppedState 'after-compose-restart'
        Assert-StateEqual $baseline $afterRestart

        Invoke-Compose @('start') | Out-Null
        Wait-Healthy
        Invoke-Compose @('stop') | Out-Null
        $afterStart = Save-StoppedState 'after-stop-start'
        Assert-StateEqual $baseline $afterStart

        Invoke-Compose @('down', '--remove-orphans') | Out-Null
        Start-Stack
        Invoke-Compose @('stop') | Out-Null
        $afterDownUp = Save-StoppedState 'after-down-up'
        Assert-StateEqual $baseline $afterDownUp
        Invoke-Compose @('start') | Out-Null
        Wait-Healthy
    }
    'BeforeDockerRestart' {
        Start-Stack
        Invoke-Compose @('stop') | Out-Null
        Save-StoppedState 'before-docker-restart' | Out-Null
        Invoke-Compose @('start') | Out-Null
        Wait-Healthy
        [ordered]@{
            recorded_at_utc = [DateTime]::UtcNow.ToString('o')
            docker_server = $server
            instruction = 'Restart Docker Desktop now, then run AfterDockerRestart with the same arguments.'
        } | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $evidencePath 'docker-restart-pending.json') -Encoding utf8NoBOM
    }
    'AfterDockerRestart' {
        $baseline = Join-Path $evidencePath 'before-docker-restart'
        if (!(Test-Path -LiteralPath $baseline -PathType Container)) {
            throw 'BeforeDockerRestart evidence is missing.'
        }
        Wait-Healthy
        Assert-NetworkBoundary
        Invoke-Compose @('stop') | Out-Null
        $afterRestart = Save-StoppedState 'after-docker-restart'
        Assert-StateEqual $baseline $afterRestart
        Invoke-Compose @('start') | Out-Null
        Wait-Healthy
        Remove-Item -LiteralPath (Join-Path $evidencePath 'docker-restart-pending.json')
    }
}

[ordered]@{
    phase = $Phase
    completed_at_utc = [DateTime]::UtcNow.ToString('o')
    compose_file = $composePath
    docker_server = $server
    healthy = $true
    host_ports_absent = $true
    sqlite_integrity = if ($Phase -eq 'BeforeDockerRestart') { 'recorded' } else { 'ok' }
} | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $evidencePath "$($Phase.ToLowerInvariant())-summary.json") -Encoding utf8NoBOM

Write-Host "Compose acceptance phase passed: $Phase; evidence=$evidencePath"
