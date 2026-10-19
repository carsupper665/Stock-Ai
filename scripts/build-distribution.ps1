param(
    [switch]$SkipFrontendBuild,
    [string]$CodexExecutable = '',
    [string]$ClaudeExecutable = '',
    [string]$ConfigurationFile = '',
    [string]$NginxArchive = '',
    [string]$NginxSha256 = 'c6ee35edb0a861063323f709192ae50d1eefaf2ea7d00678bd2b22d8542dc61e'
)

$ErrorActionPreference = 'Stop'
$root = Split-Path $PSScriptRoot -Parent
$dist = Join-Path $root 'dist'
$frontendOutput = Join-Path $root 'frontend/dist'
$buildRoot = Join-Path $root ('.distribution-build-' + [Guid]::NewGuid().ToString('N'))
$releaseStage = Join-Path $buildRoot 'release'
$initialEnv = Join-Path $buildRoot 'initial.env'
$initialSecrets = Join-Path $buildRoot 'initial-secrets'
$nginxVersion = '1.30.6'
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

function New-RandomBytes([int]$Count) {
    $bytes = [byte[]]::new($Count)
    [Security.Cryptography.RandomNumberGenerator]::Fill($bytes)
    return $bytes
}

function Write-InitialEnvironment([string]$Path, [string]$SecretsPath, [string]$Source, [bool]$IncludeCodex, [bool]$IncludeClaude) {
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
    # The middleware (Release mode) rejects tokens shorter than 32 bytes; fail here instead of at start.
    foreach ($token in @('USER_TOKEN','AGENT_SERVER_ADMIN_TOKEN','LLM_SERVER_ADMIN_TOKEN','LLM_SERVER_RUNTIME_TOKEN')) {
        if ($values[$token].Length -lt 32) { throw "$token must be at least 32 bytes for the middleware; .env was not created" }
    }

    $secrets = [ordered]@{
        backend_middleware_token = $values.USER_TOKEN
        agent_admin_token = $values.AGENT_SERVER_ADMIN_TOKEN
        llm_admin_token = $values.LLM_SERVER_ADMIN_TOKEN
        llm_runtime_token = $values.LLM_SERVER_RUNTIME_TOKEN
        llm_master_key = $values.LLM_SERVER_MASTER_KEY
        backend_user_token = [Convert]::ToHexString((New-RandomBytes 32)).ToLowerInvariant()
        oidc_cookie_key = [Convert]::ToBase64String((New-RandomBytes 32))
    }
    New-Item -ItemType Directory -Path $SecretsPath -Force | Out-Null
    foreach ($name in $secrets.Keys) {
        [IO.File]::WriteAllText((Join-Path $SecretsPath "stock-ai-release_$name"), $secrets[$name], [Text.UTF8Encoding]::new($false))
    }
    $secretFile = { param($name) "secrets/stock-ai-release_$name" }

    $lines = @(
        '# Non-secret settings. Secrets live in secrets/stock-ai-release_<name>; put oidc_client_secret there yourself.',
        '# Preserve llm_master_key when moving a database containing encrypted Provider tokens.',
        "USER_TOKEN_FILE=$(& $secretFile 'backend_middleware_token')", "AGENT_SERVICE_TOKEN_FILE=$(& $secretFile 'backend_user_token')", "USER_NAME=$($values.USER_NAME)",
        'PORT=17794', 'BACKEND_ADDR=127.0.0.1:17794', 'DB_DRIVER=sqlite', 'DB_DSN=data/backend.db', 'LOG_DIR=logs',
        'BACKEND_URL=http://127.0.0.1:17794',
        'LLM_SERVER_URL=http://127.0.0.1:18090', 'LLM_SERVER_ADDR=127.0.0.1:18090', 'LLM_SERVER_DB_PATH=data/llm-provider.db',
        "LLM_SERVER_ADMIN_TOKEN_FILE=$(& $secretFile 'llm_admin_token')", "LLM_SERVER_RUNTIME_TOKEN_FILE=$(& $secretFile 'llm_runtime_token')",
        "LLM_SERVER_MASTER_KEY_FILE=$(& $secretFile 'llm_master_key')", 'LLM_SERVER_MODEL_CATALOG=[]', 'LLM_SERVER_PROVIDER_TIMEOUT=180s',
        'AGENT_SERVER_URL=http://127.0.0.1:18080', 'AGENT_SERVER_ADDR=127.0.0.1:18080', 'AGENT_SERVER_DB_PATH=data/agent-server.db',
        'AGENT_SERVER_HISTORY_DIR=data/agent-history', "AGENT_SERVER_ADMIN_TOKEN_FILE=$(& $secretFile 'agent_admin_token')",
        'AGENT_SERVER_BACKEND_URL=http://127.0.0.1:17794', "AGENT_SERVER_BACKEND_USER_TOKEN_FILE=$(& $secretFile 'backend_user_token')",
        'AGENT_SERVER_GATEWAY_URL=http://127.0.0.1:18090', "AGENT_SERVER_GATEWAY_RUNTIME_TOKEN_FILE=$(& $secretFile 'llm_runtime_token')",
        'AGENT_SERVER_REQUEST_TIMEOUT=200s', 'AGENT_SERVER_TOOL_TIMEOUT=10s',
        '# Middleware (OIDC). Fill these in with your IdP values; see README.md.',
        'MIDDLEWARE_ADDR=127.0.0.1:4180',
        'OIDC_ISSUER=https://idp.example.com/realms/stock-ai',
        'OIDC_AUTHORIZATION_URL=https://idp.example.com/realms/stock-ai/protocol/openid-connect/auth',
        'OIDC_TOKEN_URL=https://idp.example.com/realms/stock-ai/protocol/openid-connect/token',
        'OIDC_JWKS_URL=https://idp.example.com/realms/stock-ai/protocol/openid-connect/certs',
        'OIDC_END_SESSION_URL=https://idp.example.com/realms/stock-ai/protocol/openid-connect/logout',
        'OIDC_CLIENT_ID=stock-ai', 'OIDC_USER_SUBJECT=', 'OIDC_AUDIENCE=stock-ai-api',
        'OIDC_REDIRECT_URI=https://app.example.com/oauth2/callback', 'OIDC_POST_LOGOUT_URI=https://app.example.com/',
        'OIDC_COOKIE_NAME=__Host-stock-ai-release-session',
        "OIDC_CLIENT_SECRET_FILE=$(& $secretFile 'oidc_client_secret')", "OIDC_COOKIE_KEY_FILE=$(& $secretFile 'oidc_cookie_key')",
        "BACKEND_USER_TOKEN_FILE=$(& $secretFile 'backend_middleware_token')",
        '# nginx: your domain and TLS certificate.',
        'RELEASE_APP_HOST=app.example.com', 'RELEASE_TLS_CERT=tls/fullchain.pem', 'RELEASE_TLS_KEY=tls/privkey.pem'
    )
    if ($IncludeCodex) {
        $lines += 'LLM_SERVER_CODEX_EXECUTABLE=tools/codex/bin/codex.exe'
        $lines += ('LLM_SERVER_CODEX_AUTH_FILE=' + (Join-Path $HOME '.codex/auth.json'))
    }
    if ($IncludeClaude) {
        $lines += 'LLM_SERVER_CLAUDE_EXECUTABLE=tools/claude/claude.exe'
        $lines += ('LLM_SERVER_CLAUDE_AUTH_FILE=' + (Join-Path $HOME '.claude/.credentials.json'))
    }
    [IO.File]::WriteAllLines($Path, $lines, [Text.UTF8Encoding]::new($false))
}

$managedPaths = @('backend.exe', 'middleware.exe', 'agent-server.exe', 'llm-provider-server.exe', 'nginx', 'start.ps1', 'README.md')
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
    if ($codexRequested) {
        $codexBinDirectory = Split-Path $CodexExecutable -Parent
        if ((Split-Path $codexBinDirectory -Leaf) -ne 'bin') { throw 'Select bin/codex.exe from the complete official Codex package' }
        $codexPackageRoot = Split-Path $codexBinDirectory -Parent
        Assert-File (Join-Path $codexBinDirectory 'codex-code-mode-host.exe') 'Codex tool host'
        Assert-File (Join-Path $codexPackageRoot 'codex-package.json') 'Codex package metadata'
    }
    if ($claudeRequested -and !(Test-Path -LiteralPath $ClaudeExecutable -PathType Leaf)) {
        throw "Requested Claude executable not found: $ClaudeExecutable"
    }

    New-Item -ItemType Directory -Path $releaseStage -Force | Out-Null

    $nginxZip = $NginxArchive
    if ([string]::IsNullOrWhiteSpace($nginxZip)) {
        $nginxZip = Join-Path $buildRoot "nginx-$nginxVersion.zip"
        Invoke-WebRequest -Uri "https://nginx.org/download/nginx-$nginxVersion.zip" -OutFile $nginxZip
    } elseif (!(Test-Path -LiteralPath $nginxZip -PathType Leaf)) {
        throw "Requested nginx archive not found: $nginxZip"
    }
    if ((Get-FileHash -LiteralPath $nginxZip -Algorithm SHA256).Hash -ne $NginxSha256) {
        throw "Nginx archive SHA-256 mismatch for $nginxZip; expected $NginxSha256"
    }

    foreach ($build in @(
        @('backend', '.', 'backend.exe'),
        @('middleware', './cmd/middleware', 'middleware.exe'),
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
    $nginxExtract = Join-Path $buildRoot 'nginx-extract'
    Expand-Archive -LiteralPath $nginxZip -DestinationPath $nginxExtract
    $nginxStage = Join-Path $releaseStage 'nginx'
    Move-Item -LiteralPath (Join-Path $nginxExtract "nginx-$nginxVersion") -Destination $nginxStage
    Assert-File (Join-Path $nginxStage 'nginx.exe') 'nginx/nginx.exe'
    Remove-Path (Join-Path $nginxStage 'html')
    try {
        Copy-Item -LiteralPath $frontendOutput -Destination (Join-Path $nginxStage 'html') -Recurse -Force
    } catch {
        throw "Failed to stage frontend output: $($_.Exception.Message)"
    }
    foreach ($template in @('release-edge.conf.template', 'release-nginx.conf')) {
        Copy-Item -LiteralPath (Join-Path $root "deploy/nginx/$template") -Destination (Join-Path $nginxStage "conf/$template") -Force
    }

    Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'distribution-start.ps1') -Destination (Join-Path $releaseStage 'start.ps1') -Force
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'distribution-readme.md') -Destination (Join-Path $releaseStage 'README.md') -Force

    if ($codexRequested) {
        $codexStage = Join-Path $releaseStage 'tools/codex'
        New-Item -ItemType Directory -Path $codexStage -Force | Out-Null
        Get-ChildItem -LiteralPath $codexPackageRoot -Force | Copy-Item -Destination $codexStage -Recurse -Force
        Assert-File (Join-Path $codexStage 'bin/codex.exe') 'tools/codex/bin/codex.exe'
        Assert-File (Join-Path $codexStage 'bin/codex-code-mode-host.exe') 'Codex tool host'
        $managedPaths += 'tools/codex'
    }
    if ($claudeRequested) {
        $claudeStage = Join-Path $releaseStage 'tools/claude'
        New-Item -ItemType Directory -Path $claudeStage -Force | Out-Null
        Copy-Item -LiteralPath $ClaudeExecutable -Destination (Join-Path $claudeStage 'claude.exe') -Force
        Assert-File (Join-Path $claudeStage 'claude.exe') 'tools/claude/claude.exe'
        $managedPaths += 'tools/claude'
    }

    foreach ($required in @('backend.exe', 'middleware.exe', 'agent-server.exe', 'llm-provider-server.exe', 'nginx/nginx.exe', 'nginx/conf/release-edge.conf.template', 'nginx/conf/release-nginx.conf', 'start.ps1', 'README.md')) {
        Assert-File (Join-Path $releaseStage $required) $required
    }
    if (@(Get-ChildItem -LiteralPath (Join-Path $nginxStage 'html') -File -Recurse -Force).Count -eq 0) {
        throw 'Staged frontend is empty'
    }

    $envFile = Join-Path $dist '.env'
    if (!(Test-Path -LiteralPath $envFile -PathType Leaf)) {
        if ([string]::IsNullOrWhiteSpace($ConfigurationFile)) {
            $ConfigurationFile = Join-Path $root '.claude/launch.json'
        } elseif (![IO.Path]::IsPathRooted($ConfigurationFile)) {
            $ConfigurationFile = Join-Path $root $ConfigurationFile
        }
        if (Test-Path -LiteralPath (Join-Path $dist 'secrets')) {
            throw "$dist/secrets exists but .env is missing; restore .env or remove secrets, then retry"
        }
        Write-InitialEnvironment $initialEnv $initialSecrets $ConfigurationFile $codexRequested $claudeRequested
    }

    foreach ($relativePath in $managedPaths) {
        Assert-Unlocked (Join-Path $dist $relativePath)
    }

    $backedUp = @()
    $published = @()
    $envPublished = $false
    $secretsPublished = $false
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
            Move-Item -LiteralPath $initialSecrets -Destination (Join-Path $dist 'secrets')
            $secretsPublished = $true
        }
    } catch {
        $publishError = $_
        $rollbackErrors = @()
        for ($index = $published.Count - 1; $index -ge 0; $index--) {
            try { Remove-Path (Join-Path $dist $published[$index]) } catch { $rollbackErrors += $_.Exception.Message }
        }
        if ($secretsPublished) {
            try { Remove-Path (Join-Path $dist 'secrets') } catch { $rollbackErrors += $_.Exception.Message }
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
    Write-Host "Built $dist. Existing .env, secrets, data, History, and logs were not modified. Run dist/start.ps1."
} finally {
    Remove-Path $buildRoot
    if ((Test-Path -LiteralPath $backupRoot) -and !(Get-ChildItem -LiteralPath $backupRoot -Force)) {
        Remove-Path $backupRoot
    }
    if (!$distExisted -and (Test-Path -LiteralPath $dist -PathType Container) -and !(Get-ChildItem -LiteralPath $dist -Force)) {
        Remove-Path $dist
    }
}
