param(
    [switch]$SkipFrontendBuild,
    [string]$CodexExecutable = '',
    [string]$ClaudeExecutable = '',
    [string]$ConfigurationFile = ''
)

$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
$dist = Join-Path $root 'dist'
$frontendOutput = Join-Path $root 'frontend/dist'
$buildRoot = Join-Path $root ('.distribution-build-' + [Guid]::NewGuid().ToString('N'))
$releaseStage = Join-Path $buildRoot 'release'
$initialEnv = Join-Path $buildRoot 'initial.env'
$backupRoot = Join-Path $root ('.distribution-backup-' + [Guid]::NewGuid().ToString('N'))
$distExisted = Test-Path -LiteralPath $dist

function Remove-Path([string]$Path) {
    if (Test-Path -LiteralPath $Path) {
        Remove-Item -LiteralPath $Path -Recurse -Force
    }
}

function Assert-File([string]$Path, [string]$Description) {
    if (!(Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "Missing staged $Description at $Path"
    }
}

function Assert-Unlocked([string]$Path) {
    if (!(Test-Path -LiteralPath $Path)) { return }

    $files = if (Test-Path -LiteralPath $Path -PathType Container) {
        @(Get-ChildItem -LiteralPath $Path -File -Recurse -Force)
    } else {
        @((Get-Item -LiteralPath $Path -Force))
    }
    foreach ($file in $files) {
        $stream = $null
        try {
            $stream = [IO.File]::Open($file.FullName, [IO.FileMode]::Open, [IO.FileAccess]::ReadWrite, [IO.FileShare]::None)
        } catch {
            throw "Release preflight failed: stop processes using $($file.FullName), then retry. No release artifacts were changed."
        } finally {
            if ($null -ne $stream) { $stream.Dispose() }
        }
    }
}

function Write-InitialEnvironment([string]$Path, [string]$Source, [bool]$IncludeCodex, [bool]$IncludeClaude) {
    if (!(Test-Path -LiteralPath $Source -PathType Leaf)) {
        throw "Configuration file not found: $Source"
    }

    $configuration = Get-Content -LiteralPath $Source -Raw | ConvertFrom-Json
    $values = @{}
    foreach ($entry in $configuration.configurations) {
        foreach ($argument in $entry.runtimeArgs) {
            foreach ($match in [regex]::Matches($argument, '\$env:([A-Z_]+)=''([^'']*)''')) {
                $values[$match.Groups[1].Value] = $match.Groups[2].Value
            }
        }
    }
    foreach ($required in @('USER_TOKEN','AGENT_SERVER_ADMIN_TOKEN','LLM_SERVER_ADMIN_TOKEN','LLM_SERVER_RUNTIME_TOKEN','LLM_SERVER_MASTER_KEY')) {
        if (!$values[$required]) { throw "Configuration missing $required; .env was not created" }
    }

    $lines = @(
        '# Initial local credentials copied from the explicitly selected configuration.',
        '# Preserve LLM_SERVER_MASTER_KEY when moving a database containing encrypted Provider tokens.',
        "USER_TOKEN=$($values.USER_TOKEN)", "USER_NAME=$($values.USER_NAME)",
        'PORT=17794', 'BACKEND_ADDR=127.0.0.1:17794', 'DB_DRIVER=sqlite', 'DB_DSN=data/backend.db', 'LOG_DIR=logs',
        'BACKEND_URL=http://127.0.0.1:17794',
        'LLM_SERVER_URL=http://127.0.0.1:18090', 'LLM_SERVER_ADDR=127.0.0.1:18090', 'LLM_SERVER_DB_PATH=data/llm-provider.db',
        "LLM_SERVER_ADMIN_TOKEN=$($values.LLM_SERVER_ADMIN_TOKEN)", "LLM_SERVER_RUNTIME_TOKEN=$($values.LLM_SERVER_RUNTIME_TOKEN)",
        "LLM_SERVER_MASTER_KEY=$($values.LLM_SERVER_MASTER_KEY)", 'LLM_SERVER_MODEL_CATALOG=[]', 'LLM_SERVER_PROVIDER_TIMEOUT=180s',
        'AGENT_SERVER_URL=http://127.0.0.1:18080', 'AGENT_SERVER_ADDR=127.0.0.1:18080', 'AGENT_SERVER_DB_PATH=data/agent-server.db',
        'AGENT_SERVER_HISTORY_DIR=data/agent-history', "AGENT_SERVER_ADMIN_TOKEN=$($values.AGENT_SERVER_ADMIN_TOKEN)",
        'AGENT_SERVER_BACKEND_URL=http://127.0.0.1:17794', "AGENT_SERVER_BACKEND_USER_TOKEN=$($values.USER_TOKEN)",
        'AGENT_SERVER_GATEWAY_URL=http://127.0.0.1:18090', "AGENT_SERVER_GATEWAY_RUNTIME_TOKEN=$($values.LLM_SERVER_RUNTIME_TOKEN)",
        'AGENT_SERVER_REQUEST_TIMEOUT=200s', 'AGENT_SERVER_TOOL_TIMEOUT=10s',
        'CONSOLE_ADDR=127.0.0.1:8088', 'CONSOLE_PUBLIC_DIR=public', 'CONSOLE_TLS_CERT=', 'CONSOLE_TLS_KEY='
    )
    if ($IncludeCodex) {
        $lines += 'LLM_SERVER_CODEX_EXECUTABLE=tools/codex/codex.exe'
        $lines += ('LLM_SERVER_CODEX_AUTH_FILE=' + (Join-Path $HOME '.codex/auth.json'))
    }
    if ($IncludeClaude) {
        $lines += 'LLM_SERVER_CLAUDE_EXECUTABLE=tools/claude/claude.exe'
        $lines += ('LLM_SERVER_CLAUDE_AUTH_FILE=' + (Join-Path $HOME '.claude/.credentials.json'))
    }
    [IO.File]::WriteAllLines($Path, $lines, [Text.UTF8Encoding]::new($false))
}

$managedPaths = @('backend.exe', 'console.exe', 'agent-server.exe', 'llm-provider-server.exe', 'public', 'start.ps1', 'README.md')
$codexRequested = ![string]::IsNullOrWhiteSpace($CodexExecutable)
$claudeRequested = ![string]::IsNullOrWhiteSpace($ClaudeExecutable)

try {
    if ($SkipFrontendBuild -and (
        !(Test-Path -LiteralPath $frontendOutput -PathType Container) -or
        @(Get-ChildItem -LiteralPath $frontendOutput -File -Recurse -Force).Count -eq 0
    )) {
        throw "-SkipFrontendBuild requires existing non-empty frontend output at $frontendOutput"
    }
    if ($codexRequested -and !(Test-Path -LiteralPath $CodexExecutable -PathType Leaf)) {
        throw "Requested Codex executable not found: $CodexExecutable"
    }
    if ($claudeRequested -and !(Test-Path -LiteralPath $ClaudeExecutable -PathType Leaf)) {
        throw "Requested Claude executable not found: $ClaudeExecutable"
    }

    New-Item -ItemType Directory -Path $releaseStage -Force | Out-Null

    foreach ($build in @(
        @('backend', '.', 'backend.exe'),
        @('backend', './cmd/console', 'console.exe'),
        @('agent_server', './cmd/agent-server', 'agent-server.exe'),
        @('llm_provider_server', './cmd/llm-provider-server', 'llm-provider-server.exe')
    )) {
        $compiled = Join-Path $releaseStage $build[2]
        & go -C (Join-Path $root $build[0]) build -trimpath -o $compiled $build[1]
        if ($LASTEXITCODE -ne 0) { throw "Build failed: $($build[2])" }
        Assert-File $compiled $build[2]
    }

    if (!$SkipFrontendBuild) {
        & npm --prefix (Join-Path $root 'frontend') run build
        if ($LASTEXITCODE -ne 0) { throw 'Frontend build failed' }
    }
    if (!(Test-Path -LiteralPath $frontendOutput -PathType Container) -or
        @(Get-ChildItem -LiteralPath $frontendOutput -File -Recurse -Force).Count -eq 0) {
        throw "Frontend output is missing or empty: $frontendOutput"
    }
    try {
        Copy-Item -LiteralPath $frontendOutput -Destination (Join-Path $releaseStage 'public') -Recurse -Force
    } catch {
        throw "Failed to stage frontend output: $($_.Exception.Message)"
    }

    Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'distribution-start.ps1') -Destination (Join-Path $releaseStage 'start.ps1') -Force
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'distribution-readme.md') -Destination (Join-Path $releaseStage 'README.md') -Force

    if ($codexRequested) {
        $codexStage = Join-Path $releaseStage 'tools/codex'
        New-Item -ItemType Directory -Path $codexStage -Force | Out-Null
        Copy-Item -Path (Join-Path (Split-Path $CodexExecutable -Parent) '*.exe') -Destination $codexStage -Force
        Assert-File (Join-Path $codexStage 'codex.exe') 'tools/codex/codex.exe'
        $managedPaths += 'tools/codex'
    }
    if ($claudeRequested) {
        $claudeStage = Join-Path $releaseStage 'tools/claude'
        New-Item -ItemType Directory -Path $claudeStage -Force | Out-Null
        Copy-Item -LiteralPath $ClaudeExecutable -Destination (Join-Path $claudeStage 'claude.exe') -Force
        Assert-File (Join-Path $claudeStage 'claude.exe') 'tools/claude/claude.exe'
        $managedPaths += 'tools/claude'
    }

    foreach ($required in @('backend.exe', 'console.exe', 'agent-server.exe', 'llm-provider-server.exe', 'start.ps1', 'README.md')) {
        Assert-File (Join-Path $releaseStage $required) $required
    }
    if (@(Get-ChildItem -LiteralPath (Join-Path $releaseStage 'public') -File -Recurse -Force).Count -eq 0) {
        throw 'Staged frontend is empty'
    }

    $envFile = Join-Path $dist '.env'
    if (!(Test-Path -LiteralPath $envFile -PathType Leaf)) {
        if ([string]::IsNullOrWhiteSpace($ConfigurationFile)) {
            $ConfigurationFile = Join-Path $root '.claude/launch.json'
        } elseif (![IO.Path]::IsPathRooted($ConfigurationFile)) {
            $ConfigurationFile = Join-Path $root $ConfigurationFile
        }
        Write-InitialEnvironment $initialEnv $ConfigurationFile $codexRequested $claudeRequested
    }

    foreach ($relativePath in $managedPaths) {
        Assert-Unlocked (Join-Path $dist $relativePath)
    }

    $backedUp = @()
    $published = @()
    $envPublished = $false
    try {
        New-Item -ItemType Directory -Path $dist -Force | Out-Null
        New-Item -ItemType Directory -Path $backupRoot -Force | Out-Null

        foreach ($relativePath in $managedPaths) {
            $destination = Join-Path $dist $relativePath
            if (!(Test-Path -LiteralPath $destination)) { continue }
            $backup = Join-Path $backupRoot $relativePath
            New-Item -ItemType Directory -Path (Split-Path $backup -Parent) -Force | Out-Null
            Move-Item -LiteralPath $destination -Destination $backup
            $backedUp += $relativePath
        }

        foreach ($relativePath in $managedPaths) {
            $source = Join-Path $releaseStage $relativePath
            $destination = Join-Path $dist $relativePath
            New-Item -ItemType Directory -Path (Split-Path $destination -Parent) -Force | Out-Null
            Move-Item -LiteralPath $source -Destination $destination
            $published += $relativePath
        }

        if ((Test-Path -LiteralPath $initialEnv -PathType Leaf) -and !(Test-Path -LiteralPath $envFile)) {
            Move-Item -LiteralPath $initialEnv -Destination $envFile
            $envPublished = $true
        }
    } catch {
        $publishError = $_
        $rollbackErrors = @()
        for ($index = $published.Count - 1; $index -ge 0; $index--) {
            try { Remove-Path (Join-Path $dist $published[$index]) } catch { $rollbackErrors += $_.Exception.Message }
        }
        if ($envPublished) {
            try { Remove-Path $envFile } catch { $rollbackErrors += $_.Exception.Message }
        }
        for ($index = $backedUp.Count - 1; $index -ge 0; $index--) {
            try {
                $relativePath = $backedUp[$index]
                $destination = Join-Path $dist $relativePath
                New-Item -ItemType Directory -Path (Split-Path $destination -Parent) -Force | Out-Null
                Move-Item -LiteralPath (Join-Path $backupRoot $relativePath) -Destination $destination
            } catch {
                $rollbackErrors += $_.Exception.Message
            }
        }
        if ($rollbackErrors.Count) {
            throw "Release publication failed: $($publishError.Exception.Message). Rollback needs manual recovery from $backupRoot`: $($rollbackErrors -join '; ')"
        }
        throw "Release publication failed and the previous release was restored: $($publishError.Exception.Message)"
    }

    Remove-Path $backupRoot
    Write-Host "Built $dist. Existing .env, data, History, and logs were not modified. Run dist/start.ps1."
} finally {
    Remove-Path $buildRoot
    if ((Test-Path -LiteralPath $backupRoot) -and !(Get-ChildItem -LiteralPath $backupRoot -Force)) {
        Remove-Path $backupRoot
    }
    if (!$distExisted -and (Test-Path -LiteralPath $dist -PathType Container) -and !(Get-ChildItem -LiteralPath $dist -Force)) {
        Remove-Path $dist
    }
}
