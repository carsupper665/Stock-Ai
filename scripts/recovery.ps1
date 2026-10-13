[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('Backup', 'Verify', 'Restore', 'Prune', 'InstallSchedule')]
    [string]$Action,
    [Parameter(Mandatory = $true)] [string]$EnvFile,
    [string]$ComposeFile = (Join-Path $PSScriptRoot '..\deploy\compose\compose.yaml'),
    [string]$BackupDirectory,
    [string]$BackupFile,
    [string]$OffHostVerificationMarker,
    [string]$RestoreDirectory,
    [string]$CertificateThumbprint,
    [string]$ScheduledTaskUser,
    [int]$KeepDaily = 14,
    [int]$KeepMonthly = 12
)

$ErrorActionPreference = 'Stop'
Import-Module (Join-Path $PSScriptRoot 'recovery-policy.psm1') -Force
$Services = @('backend', 'llm-provider-server', 'agent-server', 'console')
$Volumes = [ordered]@{
    backend = 'backend_state'
    'llm-provider-server' = 'llm_state'
    'agent-server' = 'agent_state'
}
$Secrets = @('backend_user_token', 'backend_account_token_key', 'agent_admin_token', 'llm_admin_token', 'llm_runtime_token', 'llm_master_key')
$DockerCommand = Get-Command 'docker.exe' -ErrorAction SilentlyContinue
if (!$DockerCommand) { $DockerCommand = Get-Command 'docker' -ErrorAction SilentlyContinue }
if (!$DockerCommand) { throw 'Docker CLI is unavailable.' }
$EnvPath = (Resolve-Path -LiteralPath $EnvFile).ProviderPath
$ComposePath = (Resolve-Path -LiteralPath $ComposeFile).ProviderPath
$Values = @{}
foreach ($line in [IO.File]::ReadAllLines($EnvPath)) {
    $line = $line.Trim()
    if (!$line -or $line.StartsWith('#')) { continue }
    $separator = $line.IndexOf('=')
    if ($separator -lt 1) { throw 'Compose env file has an invalid assignment.' }
    $Values[$line.Substring(0, $separator).Trim()] = $line.Substring($separator + 1).Trim().Trim('"').Trim("'")
}
if (!$BackupDirectory) { $BackupDirectory = [string]$Values['STOCK_AI_BACKUP_DIR'] }
if (!$BackupDirectory) { throw 'Set STOCK_AI_BACKUP_DIR or -BackupDirectory.' }
$BackupDirectory = [IO.Path]::GetFullPath($BackupDirectory)
$SecretsDirectory = [IO.Path]::GetFullPath([string]$Values['STOCK_AI_SECRETS_DIR'])
$Namespace = [string]$Values['STOCK_AI_SECRET_NAMESPACE']
$Stack = [string]$Values['STOCK_AI_INSTANCE']
if (!$Namespace -or !$Stack -or !(Test-Path -LiteralPath $SecretsDirectory -PathType Container)) { throw 'Secret namespace, stack name, or secret directory is invalid.' }
if ($Action -eq 'InstallSchedule') {
    if (!$ScheduledTaskUser) { throw 'InstallSchedule requires -ScheduledTaskUser.' }
    $RecoveryCredential = Get-Credential -UserName $ScheduledTaskUser -Message 'Enter the dedicated backup account password for Task Scheduler.'
    $scheduledSid = ([Security.Principal.NTAccount]::new($RecoveryCredential.UserName)).Translate([Security.Principal.SecurityIdentifier]).Value
    $operatorSid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    Assert-RecoveryScheduledAccount -ScheduledSid $scheduledSid -OperatorSid $operatorSid
}
$BackupDirectory = Initialize-RecoveryBackupDirectory -Path $BackupDirectory
$LogPath = Join-Path $BackupDirectory ("recovery-{0}.log" -f [DateTime]::UtcNow.ToString('yyyyMMdd'))

function Log-Recovery([string]$Message) {
    $line = "{0} {1}" -f [DateTime]::UtcNow.ToString('o'), $Message
    Add-Content -LiteralPath $LogPath -Value $line -Encoding utf8
}

function Invoke-Docker([string[]]$Arguments, [switch]$AllowFailure) {
    $output = & $DockerCommand.Source @Arguments 2>&1
    $code = $LASTEXITCODE
    if ($code -ne 0 -and !$AllowFailure) { throw "docker $($Arguments -join ' ') failed (exit $code)." }
    return [pscustomobject]@{ Code = $code; Output = @($output) }
}

function Invoke-Compose([string[]]$Arguments, [switch]$AllowFailure) {
    Invoke-Docker (@('compose', '--env-file', $EnvPath, '-f', $ComposePath) + $Arguments) -AllowFailure:$AllowFailure
}

function Get-Certificate([switch]$PrivateKey) {
    if (!$CertificateThumbprint) { throw 'A certificate thumbprint is required.' }
    $certificate = Get-Item -LiteralPath "Cert:\LocalMachine\My\$($CertificateThumbprint.Replace(' ', ''))" -ErrorAction Stop
    if (!$certificate.HasPrivateKey -and $PrivateKey) { throw 'Restore certificate does not have its private key.' }
    $rsa = [Security.Cryptography.X509Certificates.RSACertificateExtensions]::GetRSAPublicKey($certificate)
    if (!$rsa) { throw 'Recovery certificate must use RSA.' }
    $rsa.Dispose()
    return $certificate
}

function Protect-Archive([string]$Archive, [string]$Destination) {
    $certificate = Get-Certificate
    $key = [byte[]]::new(32); $nonce = [byte[]]::new(12); $tag = [byte[]]::new(16)
    [Security.Cryptography.RandomNumberGenerator]::Fill($key)
    [Security.Cryptography.RandomNumberGenerator]::Fill($nonce)
    $plain = [IO.File]::ReadAllBytes($Archive)
    $cipher = [byte[]]::new($plain.Length)
    $aes = [Security.Cryptography.AesGcm]::new($key, $tag.Length)
    try { $aes.Encrypt($nonce, $plain, $cipher, $tag) } finally { $aes.Dispose(); [Array]::Clear($plain, 0, $plain.Length) }
    $rsa = [Security.Cryptography.X509Certificates.RSACertificateExtensions]::GetRSAPublicKey($certificate)
    try { $wrappedKey = $rsa.Encrypt($key, [Security.Cryptography.RSAEncryptionPadding]::OaepSHA256) } finally { $rsa.Dispose() }
    [Array]::Clear($key, 0, $key.Length)
    $envelope = [ordered]@{
        schema = 1
        created_at_utc = [DateTime]::UtcNow.ToString('o')
        certificate_thumbprint = $certificate.Thumbprint
        nonce = [Convert]::ToBase64String($nonce)
        tag = [Convert]::ToBase64String($tag)
        wrapped_key = [Convert]::ToBase64String($wrappedKey)
        ciphertext = [Convert]::ToBase64String($cipher)
    } | ConvertTo-Json -Compress
    [IO.File]::WriteAllText($Destination, $envelope, [Text.UTF8Encoding]::new($false))
    $hash = (Get-FileHash -LiteralPath $Destination -Algorithm SHA256).Hash.ToLowerInvariant()
    [IO.File]::WriteAllText("$Destination.sha256", "$hash  $([IO.Path]::GetFileName($Destination))`n", [Text.UTF8Encoding]::new($false))
}

function Unprotect-Archive([string]$Source, [string]$Destination) {
    $hash = (Get-FileHash -LiteralPath $Source -Algorithm SHA256).Hash.ToLowerInvariant()
    $sidecar = [IO.File]::ReadAllText("$Source.sha256").Trim()
    if ($sidecar -ne "$hash  $([IO.Path]::GetFileName($Source))") { throw 'Encrypted backup checksum failed.' }
    $envelope = Get-Content -LiteralPath $Source -Raw | ConvertFrom-Json
    if ($envelope.schema -ne 1) { throw 'Unsupported backup envelope schema.' }
    if ($envelope.certificate_thumbprint -ne $CertificateThumbprint.Replace(' ', '')) { throw 'Backup belongs to a different recovery certificate.' }
    $certificate = Get-Certificate -PrivateKey
    $rsa = [Security.Cryptography.X509Certificates.RSACertificateExtensions]::GetRSAPrivateKey($certificate)
    try { $key = $rsa.Decrypt([Convert]::FromBase64String($envelope.wrapped_key), [Security.Cryptography.RSAEncryptionPadding]::OaepSHA256) } finally { $rsa.Dispose() }
    $cipher = [Convert]::FromBase64String($envelope.ciphertext)
    $plain = [byte[]]::new($cipher.Length)
    $aes = [Security.Cryptography.AesGcm]::new($key, 16)
    try { $aes.Decrypt([Convert]::FromBase64String($envelope.nonce), $cipher, [Convert]::FromBase64String($envelope.tag), $plain) }
    finally { $aes.Dispose(); [Array]::Clear($key, 0, $key.Length) }
    [IO.File]::WriteAllBytes($Destination, $plain)
    [Array]::Clear($plain, 0, $plain.Length)
}

function Get-Inventory([string]$Root) {
    $items = @{}
    foreach ($file in Get-ChildItem -LiteralPath $Root -File -Recurse) {
        $relative = [IO.Path]::GetRelativePath($Root, $file.FullName).Replace('\', '/')
        if ($relative -eq 'manifest.json') { continue }
        $items[$relative] = (Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
    }
    return $items
}

function Test-Inventory([string]$Root, [object]$Manifest) {
    $actual = Get-Inventory $Root
    if ($Manifest.files.PSObject.Properties.Count -ne $actual.Count) { throw 'Backup content inventory does not match its manifest.' }
    foreach ($path in $Manifest.files.PSObject.Properties.Name) {
        if (!$actual.ContainsKey($path) -or $actual[$path] -cne $Manifest.files.$path) { throw "Backup checksum failed for $path." }
    }
    foreach ($name in @('backend/backend.db', 'llm-provider-server/llm-provider.db', 'agent-server/agent-server.db')) {
        if (!(Test-Path -LiteralPath (Join-Path $Root $name) -PathType Leaf)) { throw "Backup is missing required database: $name" }
    }
    if (!(Test-Path -LiteralPath (Join-Path $Root 'agent-server/history') -PathType Container)) { throw 'Backup is missing Agent History.' }
    if (!(Test-Path -LiteralPath (Join-Path $Root 'secrets/llm_master_key') -PathType Leaf)) { throw 'Backup is missing the matching LLM master key.' }
}

function Get-Containers {
    $composeInvoker = { param([string[]]$ComposeArguments) Invoke-Compose $ComposeArguments }
    return Get-RecoveryContainerIds -Services $Services -ComposeInvoker $composeInvoker
}

function Test-EmptyVolumes([object]$Containers, [string]$Scratch) {
    foreach ($service in $Volumes.Keys) {
        $destination = Join-Path $Scratch "empty-$service"
        New-Item -ItemType Directory -Path $destination | Out-Null
        Invoke-Docker @('cp', "$($Containers[$service]):/data/.", $destination) | Out-Null
        Assert-RecoveryVolumeScaffolding -Service $service -Path $destination
    }
}

function Invoke-Backup {
    $started = [DateTime]::UtcNow
    $wasRunning = @((Invoke-Compose @('ps', '--status', 'running', '-q')).Output | Where-Object { $_.Trim() }).Count -gt 0
    $stage = Join-Path $BackupDirectory ('.staging-' + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $stage | Out-Null
    Set-RecoveryDirectoryAcl -Path $stage
    $archive = "$stage.zip"
    try {
        Invoke-Compose @('stop', '--timeout', '30') | Out-Null
        $containers = Get-Containers
        foreach ($service in $Volumes.Keys) {
            $target = Join-Path $stage $service
            New-Item -ItemType Directory -Path $target | Out-Null
            Invoke-Docker @('cp', "$($containers[$service]):/data/.", $target) | Out-Null
        }
        New-Item -ItemType Directory -Path (Join-Path $stage 'config'), (Join-Path $stage 'secrets') -Force | Out-Null
        Copy-Item -LiteralPath $ComposePath -Destination (Join-Path $stage 'config/compose.yaml')
        Copy-Item -LiteralPath $EnvPath -Destination (Join-Path $stage 'config/compose.env')
        foreach ($secret in $Secrets) {
            $source = Join-Path $SecretsDirectory "${Namespace}_${secret}"
            if (!(Test-Path -LiteralPath $source -PathType Leaf)) { throw "Required secret is missing: $secret" }
            Copy-Item -LiteralPath $source -Destination (Join-Path $stage "secrets/$secret")
        }
        $config = (Invoke-Compose @('config', '--format', 'json')).Output -join "`n" | ConvertFrom-Json
        $manifest = [ordered]@{
            schema = 1
            created_at_utc = $started.ToString('o')
            environment = $Values['STOCK_AI_ENVIRONMENT']
            stack = $Stack
            volumes = @($Volumes.Values)
            images = $null
            files = $null
        }
        $images = [ordered]@{}
        foreach ($service in $Services) { $images[$service] = [string]$config.services.$service.image }
        $manifest.images = $images
        $manifest.files = Get-Inventory $stage
        $manifest | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath (Join-Path $stage 'manifest.json') -Encoding utf8NoBOM
        [IO.Compression.ZipFile]::CreateFromDirectory($stage, $archive, [IO.Compression.CompressionLevel]::Optimal, $false)
        $name = '{0}-{1}.sai-backup' -f $Stack, $started.ToString('yyyyMMddTHHmmssZ')
        $destination = Join-Path $BackupDirectory $name
        Protect-Archive $archive $destination
        Log-Recovery "BACKUP_OK file=$name bytes=$((Get-Item $destination).Length) duration_seconds=$([math]::Round(([DateTime]::UtcNow-$started).TotalSeconds,1))"
        Write-Host "Encrypted backup created: $destination"
    } finally {
        if ($wasRunning) { Invoke-Compose @('up', '-d', '--wait', '--wait-timeout', '180') | Out-Null }
        if (Test-Path -LiteralPath $stage) { Remove-Item -LiteralPath $stage -Recurse -Force }
        if (Test-Path -LiteralPath $archive) { Remove-Item -LiteralPath $archive -Force }
    }
}

function Invoke-Verify {
    if (!$BackupFile) { throw 'Verify requires -BackupFile.' }
    $temp = Join-Path $env:TEMP ('stock-ai-verify-' + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $temp | Out-Null
    try {
        Set-RecoveryDirectoryAcl -Path $temp
        $zip = Join-Path $temp 'backup.zip'
        Unprotect-Archive $BackupFile $zip
        $expanded = Join-Path $temp 'contents'
        [IO.Compression.ZipFile]::ExtractToDirectory($zip, $expanded)
        $manifest = Get-Content -LiteralPath (Join-Path $expanded 'manifest.json') -Raw | ConvertFrom-Json
        Test-Inventory $expanded $manifest
        Write-Host "Backup verified: $($manifest.created_at_utc) environment=$($manifest.environment)"
    } finally { Remove-Item -LiteralPath $temp -Recurse -Force }
}

function Invoke-Restore {
    if (!$BackupFile -or !$RestoreDirectory) { throw 'Restore requires -BackupFile and -RestoreDirectory.' }
    $started = [DateTime]::UtcNow
    $temp = Join-Path $env:TEMP ('stock-ai-restore-' + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $temp | Out-Null
    try {
        Set-RecoveryDirectoryAcl -Path $temp
        $zip = Join-Path $temp 'backup.zip'; Unprotect-Archive $BackupFile $zip
        $expanded = Join-Path $temp 'contents'; [IO.Compression.ZipFile]::ExtractToDirectory($zip, $expanded)
        $manifest = Get-Content -LiteralPath (Join-Path $expanded 'manifest.json') -Raw | ConvertFrom-Json
        Test-Inventory $expanded $manifest
        if ($manifest.stack -ne $Stack -or $manifest.environment -ne $Values['STOCK_AI_ENVIRONMENT']) { throw 'Backup belongs to a different stack or environment.' }
        if ((Test-Path -LiteralPath $RestoreDirectory) -and (Get-ChildItem -LiteralPath $RestoreDirectory -Force | Select-Object -First 1)) { throw 'Restore directory must be empty.' }
        $archivedCompose = Join-Path $expanded 'config/compose.yaml'
        $archivedEnv = Join-Path $expanded 'config/compose.env'
        $archivedConfig = (Invoke-Docker @('compose', '--env-file', $archivedEnv, '-f', $archivedCompose, 'config', '--format', 'json')).Output -join "`n" | ConvertFrom-Json
        $actualServices = @($archivedConfig.services.PSObject.Properties.Name | Sort-Object) -join ','
        $expectedServices = @($Services | Sort-Object) -join ','
        if ($actualServices -cne $expectedServices) { throw 'Backup Compose definition has an unexpected service set.' }
        foreach ($service in $Services) {
            if ([string]$archivedConfig.services.$service.image -cne [string]$manifest.images.$service) { throw "Archived image reference mismatch for $service." }
        }
        Invoke-Compose @('stop', '--timeout', '30') | Out-Null
        Invoke-Compose @('create') | Out-Null
        $containers = Get-Containers; Test-EmptyVolumes $containers $temp
        foreach ($service in $Volumes.Keys) { Invoke-Docker @('cp', "$(Join-Path $expanded $service)/.", "$($containers[$service]):/data/") | Out-Null }
        $dockerInvoker = { param([string[]]$DockerArguments) Invoke-Docker $DockerArguments | Out-Null }
        foreach ($service in $Volumes.Keys) {
            Invoke-RecoveryVolumeOwnership -VolumeName "${Stack}_$($Volumes[$service])" -DockerInvoker $dockerInvoker
        }
        New-Item -ItemType Directory -Path $RestoreDirectory -Force | Out-Null
        Set-RecoveryDirectoryAcl -Path $RestoreDirectory
        $restoredSecrets = Join-Path $RestoreDirectory 'secrets'; New-Item -ItemType Directory -Path $restoredSecrets -Force | Out-Null
        Set-RecoveryDirectoryAcl -Path $restoredSecrets
        Copy-Item -LiteralPath (Join-Path $expanded 'config/compose.yaml') -Destination (Join-Path $RestoreDirectory 'compose.yaml') -Force
        foreach ($secret in $Secrets) { Copy-Item -LiteralPath (Join-Path $expanded "secrets/$secret") -Destination (Join-Path $restoredSecrets "${Namespace}_${secret}") -Force }
        $restoredEnv = [Collections.Generic.List[string]]::new()
        foreach ($line in [IO.File]::ReadAllLines((Join-Path $expanded 'config/compose.env'))) {
            if ($line -match '^\s*STOCK_AI_SECRETS_DIR\s*=') { $line = "STOCK_AI_SECRETS_DIR=$($restoredSecrets.Replace('\','/'))" }
            $restoredEnv.Add($line)
        }
        [IO.File]::WriteAllLines((Join-Path $RestoreDirectory 'compose.env'), $restoredEnv, [Text.UTF8Encoding]::new($false))
        Log-Recovery "RESTORE_OK file=$([IO.Path]::GetFileName($BackupFile)) duration_seconds=$([math]::Round(([DateTime]::UtcNow-$started).TotalSeconds,1))"
        Write-Host "State restored to empty volumes. Review restored configuration, then start and validate services: $RestoreDirectory"
    } finally { Remove-Item -LiteralPath $temp -Recurse -Force }
}

function Invoke-Prune {
    if ($KeepDaily -lt 1 -or $KeepMonthly -lt 1) { throw 'Retention counts must be positive.' }
    if (!$OffHostVerificationMarker -or !(Test-Path -LiteralPath $OffHostVerificationMarker -PathType Leaf)) { throw 'Prune requires a recent off-host verification marker.' }
    $marker = Get-Content -LiteralPath $OffHostVerificationMarker -Raw | ConvertFrom-Json
    if (!$marker.backup_file -or !$marker.sha256 -or !$marker.verified_at_utc) { throw 'Off-host verification marker is incomplete.' }
    $verifiedAt = [DateTime]::Parse([string]$marker.verified_at_utc).ToUniversalTime()
    $verifiedBackup = Join-Path $BackupDirectory ([IO.Path]::GetFileName([string]$marker.backup_file))
    if ($verifiedAt -gt [DateTime]::UtcNow.AddMinutes(5) -or [DateTime]::UtcNow - $verifiedAt -gt [TimeSpan]::FromDays(7) -or !(Test-Path -LiteralPath $verifiedBackup -PathType Leaf)) { throw 'Off-host verification is older than 7 days or its local backup is missing.' }
    if ([string]$marker.sha256 -notmatch '^[0-9a-fA-F]{64}$') { throw 'Off-host verification SHA-256 is invalid.' }
    $verifiedHash = (Get-FileHash -LiteralPath $verifiedBackup -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($verifiedHash -cne ([string]$marker.sha256).ToLowerInvariant()) { throw 'Off-host verification marker does not match the encrypted backup.' }
    $files = @(Get-ChildItem -LiteralPath $BackupDirectory -Filter '*.sai-backup' -File | Sort-Object Name -Descending)
    $keep = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
    $days = [Collections.Generic.HashSet[string]]::new()
    foreach ($file in $files) {
        if ($file.Name -match '-(?<date>\d{8})T' -and $days.Add($Matches.date)) { $null = $keep.Add($file.FullName) }
        if ($days.Count -ge $KeepDaily) { break }
    }
    $months = [Collections.Generic.HashSet[string]]::new()
    foreach ($file in $files) {
        if ($file.Name -notmatch '-(?<year>\d{4})(?<month>\d{2})\d{2}T') { continue }
        $month = "$($Matches.year)$($Matches.month)"
        if ($months.Add($month)) { $null = $keep.Add($file.FullName) }
        if ($months.Count -ge $KeepMonthly) { break }
    }
    foreach ($file in $files) {
        if (!$keep.Contains($file.FullName)) { Remove-Item -LiteralPath $file.FullName, "$($file.FullName).sha256" -Force }
    }
    $logCutoff = [DateTime]::UtcNow.AddDays(-90)
    $oldLogs = @(Get-ChildItem -LiteralPath $BackupDirectory -Filter 'recovery-????????.log' -File | Where-Object { $_.LastWriteTimeUtc -lt $logCutoff })
    foreach ($log in $oldLogs) { Remove-Item -LiteralPath $log.FullName -Force }
    Log-Recovery "PRUNE_OK retained=$($keep.Count) removed=$($files.Count-$keep.Count) logs_removed=$($oldLogs.Count)"
}

function Install-RecoverySchedule {
    if (!$CertificateThumbprint) { throw 'InstallSchedule requires -CertificateThumbprint.' }
    $credential = $RecoveryCredential
    $common = "-NoProfile -NonInteractive -ExecutionPolicy Bypass -File `"$PSCommandPath`" -EnvFile `"$EnvPath`" -ComposeFile `"$ComposePath`""
    $backupAction = New-ScheduledTaskAction -Execute 'pwsh.exe' -Argument "$common -Action Backup -CertificateThumbprint `"$CertificateThumbprint`""
    $markerPath = Join-Path $BackupDirectory 'offhost-verification.json'
    $pruneAction = New-ScheduledTaskAction -Execute 'pwsh.exe' -Argument "$common -Action Prune -OffHostVerificationMarker `"$markerPath`""
    $backupTrigger = New-ScheduledTaskTrigger -Daily -At '2:00AM'
    $pruneTrigger = New-ScheduledTaskTrigger -Weekly -DaysOfWeek Sunday -At '4:00AM'
    $settings = New-ScheduledTaskSettingsSet -StartWhenAvailable -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 15) -MultipleInstances IgnoreNew
    $principalUser = $credential.UserName
    $password = $credential.GetNetworkCredential().Password
    Register-ScheduledTask -TaskName 'Stock AI encrypted backup' -Action $backupAction -Trigger $backupTrigger -Settings $settings -User $principalUser -Password $password -RunLevel Limited -Force | Out-Null
    Register-ScheduledTask -TaskName 'Stock AI backup retention' -Action $pruneAction -Trigger $pruneTrigger -Settings $settings -User $principalUser -Password $password -RunLevel Limited -Force | Out-Null
    $password = $null
    Log-Recovery "SCHEDULE_OK user=$principalUser daily=02:00 weekly_prune=Sunday@04:00"
    Write-Host 'Daily encrypted backup and weekly retention tasks registered. Check Task Scheduler history and recovery-YYYYMMDD.log for failures.'
}

try {
    switch ($Action) {
        'Backup' { Invoke-Backup }
        'Verify' { Invoke-Verify }
        'Restore' { Invoke-Restore }
        'Prune' { Invoke-Prune }
        'InstallSchedule' { Install-RecoverySchedule }
    }
} catch {
    try { Log-Recovery "FAIL action=$Action error_type=$($_.Exception.GetType().Name)" } catch { }
    [Console]::Error.WriteLine("Recovery $Action failed. See $LogPath and the scheduled task's Last Run Result.")
    exit 1
}
