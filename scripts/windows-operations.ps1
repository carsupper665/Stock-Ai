[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('Install', 'Preflight', 'Start', 'Status', 'Stop', 'Update', 'Rollback', 'Diagnose')]
    [string]$Action,
    [Parameter(Mandatory = $true)]
    [string]$EnvFile,
    [string]$ComposeFile = (Join-Path $PSScriptRoot '..\deploy\compose\compose.yaml'),
    [string]$EvidenceDirectory = (Join-Path $PSScriptRoot '..\operator-evidence'),
    [string]$PromotionManifest,
    [string]$RollbackMetadata,
    [int]$HealthTimeoutSeconds = 180
)

$ErrorActionPreference = 'Stop'
$script:Services = @('backend', 'llm-provider-server', 'agent-server', 'console')
$script:ImageVariables = [ordered]@{
    backend = 'BACKEND_IMAGE'
    'llm-provider-server' = 'LLM_PROVIDER_SERVER_IMAGE'
    'agent-server' = 'AGENT_SERVER_IMAGE'
    console = 'CONSOLE_IMAGE'
}
$script:DigestRequiredEnvironments = @('Release')
$script:ImmutableImageReferenceRegex = '^[^\s@]+@(?<digest>sha256:[0-9a-f]{64})$'
$script:ChecksumLinePrefixRegex = '^([0-9a-fA-F]{64})\s+\*?'
$script:ComposePath = (Resolve-Path -LiteralPath $ComposeFile -ErrorAction Stop).ProviderPath
$script:EnvPath = (Resolve-Path -LiteralPath $EnvFile -ErrorAction Stop).ProviderPath
$script:EvidencePath = [IO.Path]::GetFullPath($EvidenceDirectory)
$script:Docker = Get-Command 'docker.exe' -ErrorAction SilentlyContinue
if (!$script:Docker) { $script:Docker = Get-Command 'docker' -ErrorAction SilentlyContinue }
if (!$script:Docker) { throw 'Docker CLI was not found. Install Docker Desktop and retry.' }

function Invoke-Docker([string[]]$Arguments, [switch]$AllowFailure) {
    $output = & $script:Docker.Source @Arguments 2>&1
    $code = $LASTEXITCODE
    if ($code -ne 0 -and !$AllowFailure) {
        throw "Docker command failed (exit $code): docker $($Arguments -join ' ')"
    }
    return [pscustomobject]@{ ExitCode = $code; Output = @($output) }
}

function Invoke-Compose([string[]]$Arguments, [switch]$AllowFailure) {
    $prefix = @('compose', '--env-file', $script:EnvPath, '-f', $script:ComposePath)
    return Invoke-Docker ($prefix + $Arguments) -AllowFailure:$AllowFailure
}

function Read-EnvironmentFile {
    $values = @{}
    foreach ($line in [IO.File]::ReadAllLines($script:EnvPath)) {
        $trimmed = $line.Trim()
        if (!$trimmed -or $trimmed.StartsWith('#')) { continue }
        $index = $trimmed.IndexOf('=')
        if ($index -lt 1) { throw 'Compose env file contains an invalid assignment.' }
        $key = $trimmed.Substring(0, $index).Trim()
        $value = $trimmed.Substring($index + 1).Trim()
        if ($value.Length -ge 2 -and (($value.StartsWith('"') -and $value.EndsWith('"')) -or ($value.StartsWith("'") -and $value.EndsWith("'")))) {
            $value = $value.Substring(1, $value.Length - 2)
        }
        $values[$key] = $value
    }
    return $values
}

function Get-ComposeConfig {
    $result = Invoke-Compose @('config', '--format', 'json')
    $json = ($result.Output -join "`n") | ConvertFrom-Json
    $actualServices = @($json.services.PSObject.Properties.Name | Sort-Object) -join ','
    $expectedServices = @($script:Services | Sort-Object) -join ','
    if ($actualServices -ne $expectedServices) {
        throw 'Compose definition does not match the isolated four-service application stack.'
    }
    return $json
}

function Assert-IsolatedCompose([object]$Config, [switch]$RequireDigests) {
    foreach ($name in $script:Services) {
        $service = $Config.services.$name
        if ($service.ports -or $service.network_mode) { throw "$name has an unsupported host port or network mode." }
        if ($service.build) { throw "$name uses a local build; only promoted images are allowed." }
        if ($RequireDigests -and $service.image -notmatch $script:ImmutableImageReferenceRegex) { throw "$name image is not pinned to an immutable SHA256 digest." }
    }
    foreach ($name in @('backend', 'llm-provider-server', 'agent-server')) {
        if ($Config.services.$name.networks.PSObject.Properties.Name -contains 'edge') { throw "$name unexpectedly joins the public edge network." }
    }
}

function Assert-EnvironmentCompose([object]$Config, [string]$Environment, [switch]$RequireDigests) {
    if ($Environment -notin @('Test', 'Release')) { throw 'STOCK_AI_ENVIRONMENT must be Test or Release.' }
    $digestRequired = ($script:DigestRequiredEnvironments -contains $Environment) -or $RequireDigests
    if ($digestRequired) { Assert-IsolatedCompose $Config -RequireDigests }
    else { Assert-IsolatedCompose $Config }
}

function Get-ConfiguredImageDigest([string]$ImageReference) {
    $match = [regex]::Match($ImageReference, $script:ImmutableImageReferenceRegex)
    if ($match.Success) { return $match.Groups['digest'].Value }
    return $null
}

function Get-ContainerId([string]$Service) {
    $result = Invoke-Compose @('ps', '--all', '-q', $Service)
    $id = ($result.Output -join "`n").Trim()
    if (!$id) { return $null }
    return $id
}

function Get-Container([string]$Service) {
    $id = Get-ContainerId $Service
    if (!$id) { return $null }
    $result = Invoke-Docker @('inspect', '--size', '--format', '{{json .}}', $id)
    return (($result.Output -join "`n") | ConvertFrom-Json)
}

function Wait-ForHealth {
    $deadline = [DateTime]::UtcNow.AddSeconds($HealthTimeoutSeconds)
    $last = @{}
    while ([DateTime]::UtcNow -lt $deadline) {
        $pending = @()
        foreach ($service in $script:Services) {
            $container = Get-Container $service
            if (!$container) { $last[$service] = 'missing'; $pending += $service; continue }
            $state = $container.State
            $health = if ($state.Health) { $state.Health.Status } else { 'no-healthcheck' }
            $last[$service] = "$($state.Status)/$health"
            if ($state.Status -ne 'running' -or $health -ne 'healthy') { $pending += $service }
        }
        if (!$pending.Count) { return }
        Start-Sleep -Seconds 2
    }
    $detail = ($script:Services | ForEach-Object { "$_=$($last[$_])" }) -join ', '
    $diagnostics = Join-Path $script:EvidencePath 'diagnostics.json'
    try { Write-Diagnostics $diagnostics } catch { }
    throw "Health wait failed for $detail. Diagnostic location: $diagnostics"
}

function Write-Diagnostics([string]$Destination) {
    if (!(Test-Path -LiteralPath (Split-Path -Parent $Destination))) {
        New-Item -ItemType Directory -Path (Split-Path -Parent $Destination) -Force | Out-Null
    }
    $items = @()
    foreach ($service in $script:Services) {
        $container = Get-Container $service
        if (!$container) { $items += [ordered]@{ service = $service; state = 'missing' }; continue }
        $state = $container.State
        $items += [ordered]@{
            service = $service
            container_id = $container.Id.Substring(0, 12)
            state = $state.Status
            health = if ($state.Health) { $state.Health.Status } else { 'none' }
            exit_code = $state.ExitCode
            error_present = [bool]$state.Error
            restart_count = $container.RestartCount
        }
    }
    [ordered]@{ captured_at_utc = [DateTime]::UtcNow.ToString('o'); services = $items } |
        ConvertTo-Json -Depth 6 | Set-Content -LiteralPath $Destination -Encoding utf8NoBOM
}

function Get-Promotion([string]$Path) {
    if (!$Path) { throw 'Update requires -PromotionManifest.' }
    $manifestPath = (Resolve-Path -LiteralPath $Path -ErrorAction Stop).ProviderPath
    $manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
    if ($manifest.status -ne 'promoted' -or !$manifest.images) { throw 'Promotion manifest must have status=promoted and an images map.' }
    $expected = @($script:Services | Sort-Object) -join ','
    $actual = @($manifest.images.PSObject.Properties.Name | Sort-Object) -join ','
    if ($actual -ne $expected) { throw 'Promotion manifest must contain exactly the four application services.' }
    $promoted = [ordered]@{}
    foreach ($service in $script:Services) {
        $reference = [string]$manifest.images.$service
        if ($reference -notmatch $script:ImmutableImageReferenceRegex) { throw "Promotion entry for $service is not an immutable image digest." }
        $promoted[$service] = $reference
    }
    $manifestHash = (Get-FileHash -LiteralPath $manifestPath -Algorithm SHA256).Hash.ToLowerInvariant()
    $sidecar = Join-Path (Split-Path -Parent $manifestPath) 'SHA256SUMS'
    if (!(Test-Path -LiteralPath $sidecar)) { throw 'Promotion manifest checksum file SHA256SUMS is missing.' }
    $leaf = [IO.Path]::GetFileName($manifestPath)
    $verified = $false
    foreach ($line in [IO.File]::ReadAllLines($sidecar)) {
        if ($line -match ($script:ChecksumLinePrefixRegex + [regex]::Escape($leaf) + '$')) {
            $verified = $Matches[1].ToLowerInvariant() -eq $manifestHash
        }
    }
    if (!$verified) { throw 'Promotion manifest does not match its SHA256SUMS checksum.' }
    return [pscustomobject]@{ Path = $manifestPath; Hash = $manifestHash; Images = $promoted }
}

function Set-ImageAssignments([string]$Destination, [Collections.IDictionary]$Images) {
    $lines = [Collections.Generic.List[string]]::new()
    foreach ($line in [IO.File]::ReadAllLines($script:EnvPath)) { $lines.Add($line) }
    foreach ($service in $script:Services) {
        $key = $script:ImageVariables[$service]
        $found = $false
        for ($i = 0; $i -lt $lines.Count; $i++) {
            if ($lines[$i] -match ('^\s*' + [regex]::Escape($key) + '\s*=')) {
                $lines[$i] = "$key=$($Images[$service])"
                $found = $true
            }
        }
        if (!$found) { throw "Compose env file is missing $key; refusing partial update." }
    }
    $temporary = "$Destination.tmp"
    [IO.File]::WriteAllLines($temporary, $lines, [Text.UTF8Encoding]::new($false))
    Move-Item -LiteralPath $temporary -Destination $Destination -Force
}

function Get-ImageMap([object]$Config) {
    $images = [ordered]@{}
    foreach ($service in $script:Services) { $images[$service] = [string]$Config.services.$service.image }
    return $images
}

function Test-Preflight {
    $issues = [Collections.Generic.List[string]]::new()
    $windows = Get-CimInstance Win32_OperatingSystem
    if ($windows.Caption -notmatch 'Windows 10|Windows 11|Windows Server 2022|Windows Server 2025') { $issues.Add('Unsupported Windows version.') }
    if ($windows.Caption -match 'Windows 10' -and [int]$windows.BuildNumber -lt 19045) { $issues.Add('Windows 10 build 19045 or later is required.') }
    if ($windows.Caption -match 'Windows 11' -and [int]$windows.BuildNumber -lt 22000) { $issues.Add('Windows 11 build 22000 or later is required.') }
    $computer = Get-CimInstance Win32_ComputerSystem
    $memoryGiB = [math]::Round($computer.TotalPhysicalMemory / 1GB, 1)
    $systemDrive = Get-CimInstance Win32_LogicalDisk -Filter "DeviceID='$($env:SystemDrive)'"
    if ($memoryGiB -lt 8) { $issues.Add('At least 8 GiB physical memory is required.') }
    if (!$systemDrive -or $systemDrive.FreeSpace -lt 20GB) { $issues.Add('At least 20 GiB free on the system drive is required.') }
    $cpu = Get-CimInstance Win32_Processor | Select-Object -First 1
    if ($cpu.VirtualizationFirmwareEnabled -ne $true) { $issues.Add('CPU virtualization is disabled in firmware or unavailable.') }
    $dockerInfo = Invoke-Docker @('info', '--format', '{{.OSType}}|{{.ServerVersion}}') -AllowFailure
    $engine = ($dockerInfo.Output -join '').Trim()
    if ($dockerInfo.ExitCode -ne 0) { $issues.Add('Docker Engine is not reachable.') }
    elseif (!$engine.StartsWith('linux|')) { $issues.Add('Docker must be in Linux-container mode.') }
    elseif ($engine -match '^linux\|([0-9]+(?:\.[0-9]+){1,2})$' -and [version]$Matches[1] -lt [version]'24.0') { $issues.Add('Docker Engine 24.0 or later is required.') }
    if (!(Test-Path -LiteralPath $script:ComposePath -PathType Leaf)) { $issues.Add('Compose file is missing.') }
    if (!(Test-Path -LiteralPath $script:EnvPath -PathType Leaf)) { $issues.Add('Compose env file is missing.') }
    $values = Read-EnvironmentFile
    $config = $null
    try { $config = Get-ComposeConfig }
    catch { $issues.Add("Compose configuration could not be resolved ($($_.Exception.GetType().Name)).") }
    if ($config) {
        try { Assert-EnvironmentCompose $config ([string]$values['STOCK_AI_ENVIRONMENT']) }
        catch { $issues.Add($_.Exception.Message) }
    }
    $namespace = [string]$values['STOCK_AI_SECRET_NAMESPACE']
    $secretsDirectory = [string]$values['STOCK_AI_SECRETS_DIR']
    if (!$namespace -or !$secretsDirectory -or !(Test-Path -LiteralPath $secretsDirectory -PathType Container)) {
        $issues.Add('Configured secrets directory or namespace is missing.')
    } else {
        foreach ($secret in @('backend_user_token', 'backend_account_token_key', 'agent_admin_token', 'llm_admin_token', 'llm_runtime_token', 'llm_master_key')) {
            if (!(Test-Path -LiteralPath (Join-Path $secretsDirectory "${namespace}_${secret}") -PathType Leaf)) {
                $issues.Add("A required secret file is missing: ${namespace}_${secret}.")
            }
        }
    }
    $ports = @(@(80, 443) + @($values['STOCK_AI_PUBLIC_PORTS'] -split ',' | Where-Object { $_ }) | Select-Object -Unique)
    foreach ($port in $ports) {
        if ($port -notmatch '^\d{1,5}$' -or [int]$port -lt 1 -or [int]$port -gt 65535) { $issues.Add('STOCK_AI_PUBLIC_PORTS contains an invalid port.'); continue }
        if (Get-NetTCPConnection -LocalPort ([int]$port) -State Listen -ErrorAction SilentlyContinue) { $issues.Add("Configured public port $port is already in use.") }
    }
    [ordered]@{
        checked_at_utc = [DateTime]::UtcNow.ToString('o')
        windows = @{ caption = $windows.Caption; build = $windows.BuildNumber }
        docker_linux_mode = $engine.StartsWith('linux|')
        memory_gib = $memoryGiB
        free_system_drive_gib = if ($systemDrive) { [math]::Round($systemDrive.FreeSpace / 1GB, 1) } else { 0 }
        issues = @($issues)
        passed = ($issues.Count -eq 0)
    } | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath (Join-Path $script:EvidencePath 'preflight.json') -Encoding utf8NoBOM
    if ($issues.Count) { throw "Preflight failed: $($issues -join ' '); details: $(Join-Path $script:EvidencePath 'preflight.json')" }
}

function Get-SafeStatus {
    $config = Get-ComposeConfig
    $values = Read-EnvironmentFile
    Assert-EnvironmentCompose $config ([string]$values['STOCK_AI_ENVIRONMENT'])
    $services = @()
    foreach ($service in $script:Services) {
        $container = Get-Container $service
        if (!$container) { $services += [ordered]@{ service = $service; container_id = $null; state = 'missing'; health = 'unknown'; digest = $null; image_reference = [string]$config.services.$service.image; restart_count = 0; disk_bytes = 0; volume_names = @() }; continue }
        $health = if ($container.State.Health) { $container.State.Health.Status } else { 'none' }
        $imageReference = [string]$config.services.$service.image
        $volumes = @($container.Mounts | Where-Object { $_.Type -eq 'volume' } | ForEach-Object { $_.Name } | Sort-Object -Unique)
        $services += [ordered]@{
            service = $service
            container_id = $container.Id.Substring(0, 12)
            state = $container.State.Status
            health = $health
            digest = Get-ConfiguredImageDigest $imageReference
            image_reference = $imageReference
            restart_count = $container.RestartCount
            disk_bytes = [int64]$container.SizeRw
            volume_names = $volumes
        }
    }
    $issuer = [string]$values['OIDC_ISSUER']
    $issuerReachable = $false
    if ($issuer -match '^https://') {
        try { $null = Invoke-WebRequest -Uri $issuer -Method Get -TimeoutSec 8; $issuerReachable = $true } catch { $issuerReachable = $false }
    }
    $certificates = @()
    $secretDirectory = [string]$values['STOCK_AI_SECRETS_DIR']
    $certificateFiles = @()
    if ($secretDirectory -and (Test-Path -LiteralPath $secretDirectory -PathType Container)) {
        $certificateFiles += @(Get-ChildItem -LiteralPath $secretDirectory -File -ErrorAction SilentlyContinue | Where-Object { $_.Extension -in @('.crt', '.cer', '.pem') })
    }
    foreach ($certificatePath in @($values['STOCK_AI_CERTIFICATE_FILES'] -split ';' | Where-Object { $_ })) {
        if (Test-Path -LiteralPath $certificatePath -PathType Leaf) { $certificateFiles += Get-Item -LiteralPath $certificatePath }
    }
    foreach ($file in $certificateFiles | Sort-Object FullName -Unique) {
            try {
                $cert = [Security.Cryptography.X509Certificates.X509Certificate2]::new($file.FullName)
                $certificates += [ordered]@{ file = $file.Name; expires_utc = $cert.NotAfter.ToUniversalTime().ToString('o'); days_remaining = [math]::Floor(($cert.NotAfter.ToUniversalTime() - [DateTime]::UtcNow).TotalDays) }
            } catch { }
    }
    $backup = $null
    $backupDirectory = [string]$values['STOCK_AI_BACKUP_DIR']
    if ($backupDirectory -and (Test-Path -LiteralPath $backupDirectory -PathType Container)) {
        $backup = Get-ChildItem -LiteralPath $backupDirectory -File -ErrorAction SilentlyContinue | Sort-Object LastWriteTimeUtc -Descending | Select-Object -First 1
    }
    [ordered]@{
        checked_at_utc = [DateTime]::UtcNow.ToString('o')
        services = $services
        docker_disk_usage = @((Invoke-Docker @('system', 'df', '--format', '{{json .}}') -AllowFailure).Output)
        idp = @{ reachable = $issuerReachable; issuer_host = if ($issuer) { ([Uri]$issuer).Host } else { $null } }
        certificates = $certificates
        backup = if ($backup) { @{ available = $true; file = $backup.Name; modified_utc = $backup.LastWriteTimeUtc.ToString('o'); age_hours = [math]::Round(([DateTime]::UtcNow - $backup.LastWriteTimeUtc).TotalHours, 1) } } else { @{ available = $false } }
    }
}

if (!(Test-Path -LiteralPath $script:EvidencePath)) { New-Item -ItemType Directory -Path $script:EvidencePath -Force | Out-Null }
switch ($Action) {
    'Install' { Test-Preflight; Write-Host "Operator workflow validated; report=$(Join-Path $script:EvidencePath 'preflight.json')" }
    'Preflight' { Test-Preflight; Write-Host "Preflight passed; report=$(Join-Path $script:EvidencePath 'preflight.json')" }
    'Start' {
        Test-Preflight
        $values = Read-EnvironmentFile
        $config = Get-ComposeConfig; Assert-EnvironmentCompose $config ([string]$values['STOCK_AI_ENVIRONMENT'])
        $result = Invoke-Compose @('up', '-d', '--wait', '--wait-timeout', "$HealthTimeoutSeconds") -AllowFailure
        if ($result.ExitCode -ne 0) {
            try { Write-Diagnostics (Join-Path $script:EvidencePath 'diagnostics.json') } catch { }
            throw "Compose start failed; inspect $(Join-Path $script:EvidencePath 'diagnostics.json')"
        }
        Wait-ForHealth
        Write-Host 'Application services are healthy.'
    }
    'Status' {
        $status = Get-SafeStatus
        $destination = Join-Path $script:EvidencePath 'status.json'
        $status | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $destination -Encoding utf8NoBOM
        Write-Host "Sanitized status report: $destination"
    }
    'Stop' {
        foreach ($service in @('console', 'agent-server', 'llm-provider-server', 'backend')) {
            $result = Invoke-Compose @('stop', '--timeout', '30', $service) -AllowFailure
            if ($result.ExitCode -ne 0) { throw "Graceful stop failed for $service." }
        }
        $running = @()
        foreach ($service in $script:Services) { $container = Get-Container $service; if ($container -and $container.State.Status -eq 'running') { $running += $service } }
        if ($running.Count) { throw "Services remain running after stop: $($running -join ', ')" }
        Write-Host 'All application containers stopped in dependency-safe order.'
    }
    'Update' {
        $promotion = Get-Promotion $PromotionManifest
        $current = Get-ComposeConfig; Assert-IsolatedCompose $current -RequireDigests
        $currentImages = Get-ImageMap $current
        $backupImages = [ordered]@{}
        foreach ($service in $script:Services) { $backupImages[$service] = $currentImages[$service] }
        $rollback = [ordered]@{
            schema = 1
            environment = [string](Read-EnvironmentFile)['STOCK_AI_ENVIRONMENT']
            created_at_utc = [DateTime]::UtcNow.ToString('o')
            compose_file_sha256 = (Get-FileHash -LiteralPath $script:ComposePath -Algorithm SHA256).Hash.ToLowerInvariant()
            previous_env_sha256 = (Get-FileHash -LiteralPath $script:EnvPath -Algorithm SHA256).Hash.ToLowerInvariant()
            previous_images = $backupImages
            updated_images = $promotion.Images
            promotion_manifest_sha256 = $promotion.Hash
        }
        $rollbackPath = Join-Path $script:EvidencePath ("rollback-{0}.json" -f [DateTime]::UtcNow.ToString('yyyyMMddTHHmmssZ'))
        $rollback | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath $rollbackPath -Encoding utf8NoBOM
        Set-ImageAssignments $script:EnvPath $promotion.Images
        try {
            $updated = Get-ComposeConfig; Assert-EnvironmentCompose $updated ([string]$rollback.environment) -RequireDigests
            foreach ($service in $script:Services) { if ($updated.services.$service.image -ne $promotion.Images[$service]) { throw "Updated Compose image mismatch for $service." } }
            $result = Invoke-Compose @('up', '-d', '--wait', '--wait-timeout', "$HealthTimeoutSeconds") -AllowFailure
            if ($result.ExitCode -ne 0) { throw 'Compose update did not reach healthy state.' }
            Wait-ForHealth
        } catch {
            Set-ImageAssignments $script:EnvPath $backupImages
            $rollbackResult = Invoke-Compose @('up', '-d', '--wait', '--wait-timeout', "$HealthTimeoutSeconds") -AllowFailure
            if ($rollbackResult.ExitCode -eq 0) {
                try { Wait-ForHealth } catch { }
            }
            throw "Update failed; previous image references were applied for recovery. Rollback metadata: $rollbackPath. $($_.Exception.Message)"
        }
        Write-Host "Update healthy; rollback metadata=$rollbackPath"
    }
    'Rollback' {
        if (!$RollbackMetadata) { throw 'Rollback requires -RollbackMetadata.' }
        $metadata = Get-Content -LiteralPath (Resolve-Path -LiteralPath $RollbackMetadata).ProviderPath -Raw | ConvertFrom-Json
        if ($metadata.schema -ne 1 -or !$metadata.previous_images) { throw 'Rollback metadata schema is invalid.' }
        $values = Read-EnvironmentFile
        if ($metadata.environment -ne $values['STOCK_AI_ENVIRONMENT']) { throw 'Rollback metadata belongs to a different environment.' }
        if ($metadata.compose_file_sha256 -ne (Get-FileHash -LiteralPath $script:ComposePath -Algorithm SHA256).Hash.ToLowerInvariant()) { throw 'Rollback metadata was created for a different Compose definition.' }
        $images = @{}
        foreach ($service in $script:Services) {
            $image = [string]$metadata.previous_images.$service
            if ($image -notmatch '@sha256:[0-9a-f]{64}$') { throw "Rollback digest for $service is invalid." }
            $images[$service] = $image
        }
        Set-ImageAssignments $script:EnvPath $images
        $result = Invoke-Compose @('up', '-d', '--wait', '--wait-timeout', "$HealthTimeoutSeconds") -AllowFailure
        if ($result.ExitCode -ne 0) { throw "Rollback failed; metadata: $RollbackMetadata" }
        Wait-ForHealth
        Write-Host "Rollback healthy using metadata: $RollbackMetadata"
    }
    'Diagnose' {
        $destination = Join-Path $script:EvidencePath 'diagnostics.json'
        Write-Diagnostics $destination
        Write-Host "Sanitized diagnostics: $destination"
    }
}
