param([string]$TemporaryParent = [IO.Path]::GetTempPath())

$ErrorActionPreference = 'Stop'
$workspace = Split-Path (Split-Path $PSScriptRoot -Parent) -Parent
$sourceBuildScript = Join-Path $workspace 'scripts/build-distribution.ps1'
$testRoot = Join-Path $TemporaryParent ('stock-ai-distribution-test-' + [Guid]::NewGuid().ToString('N'))
$passed = 0

function Assert-True([bool]$Condition, [string]$Message) {
    if (!$Condition) { throw "Assertion failed: $Message" }
}

function Write-Text([string]$Path, [string]$Text) {
    $parent = Split-Path $Path -Parent
    if ($parent) { [IO.Directory]::CreateDirectory($parent) | Out-Null }
    [IO.File]::WriteAllText($Path, $Text, [Text.UTF8Encoding]::new($false))
}

function New-LaunchJson([string]$UserToken) {
    $assignments = "`$env:USER_TOKEN='$UserToken'; `$env:USER_NAME='Fixture'; `$env:AGENT_SERVER_ADMIN_TOKEN='fixture-agent-token-0123456789abcdefgh'; `$env:LLM_SERVER_ADMIN_TOKEN='fixture-admin-token-0123456789abcdefghi'; `$env:LLM_SERVER_RUNTIME_TOKEN='fixture-runtime-token-0123456789abcdefg'; `$env:LLM_SERVER_MASTER_KEY='fixture-master-key'"
    return (@{ configurations = @(@{ runtimeArgs = @($assignments) }) } | ConvertTo-Json -Depth 5)
}

function New-NginxArchive([string]$Path) {
    $stage = Join-Path $testRoot 'nginx-archive-stage'
    Write-Text (Join-Path $stage 'nginx-1.30.6/nginx.exe') 'fake nginx'
    Write-Text (Join-Path $stage 'nginx-1.30.6/conf/nginx.conf') 'default nginx conf'
    Write-Text (Join-Path $stage 'nginx-1.30.6/html/index.html') 'default nginx page'
    Compress-Archive -Path (Join-Path $stage 'nginx-1.30.6') -DestinationPath $Path
}

function New-Fixture([string]$Name, [switch]$WithoutEnvironment) {
    $root = Join-Path $testRoot $Name
    foreach ($directory in @('scripts', 'middleware/cmd/middleware', 'deploy/nginx', 'agent_server/cmd/agent-server', 'llm_provider_server/cmd/llm-provider-server', 'frontend', 'fake-bin', 'fake-state')) {
        [IO.Directory]::CreateDirectory((Join-Path $root $directory)) | Out-Null
    }
    Copy-Item -LiteralPath $sourceBuildScript -Destination (Join-Path $root 'scripts/build-distribution.ps1')
    Write-Text (Join-Path $root 'scripts/distribution-start.ps1') 'synthetic start script'
    Write-Text (Join-Path $root 'scripts/distribution-readme.md') 'synthetic distribution readme'
    Write-Text (Join-Path $root 'deploy/nginx/release-edge.conf.template') 'synthetic edge template'
    Write-Text (Join-Path $root 'deploy/nginx/release-nginx.conf') 'synthetic nginx main'
    Write-Text (Join-Path $root 'synthetic-launch.json') (New-LaunchJson 'fixture-user-token-0123456789abcdefghij')
    Write-Text (Join-Path $root 'short-launch.json') (New-LaunchJson 'too-short')

    $fakeGo = @'
$ErrorActionPreference = 'Stop'
$counterFile = Join-Path $env:FAKE_STATE 'go-count.txt'
$count = if (Test-Path -LiteralPath $counterFile) { [int][IO.File]::ReadAllText($counterFile) + 1 } else { 1 }
[IO.File]::WriteAllText($counterFile, [string]$count)
if ($env:FAKE_GO_FAIL_ON -and $count -eq [int]$env:FAKE_GO_FAIL_ON) { exit 31 }
$outputIndex = [Array]::IndexOf($args, '-o')
if ($outputIndex -lt 0 -or $outputIndex + 1 -ge $args.Count) { exit 32 }
$output = $args[$outputIndex + 1]
[IO.Directory]::CreateDirectory((Split-Path $output -Parent)) | Out-Null
[IO.File]::WriteAllText($output, "new-go-artifact-$count", [Text.UTF8Encoding]::new($false))
'@
    $fakeNpm = @'
$ErrorActionPreference = 'Stop'
if ($env:FAKE_NPM_FAIL -eq '1') { exit 41 }
$prefixIndex = [Array]::IndexOf($args, '--prefix')
if ($prefixIndex -lt 0 -or $prefixIndex + 1 -ge $args.Count) { exit 42 }
if ($env:FAKE_NPM_NO_OUTPUT -eq '1') { exit 0 }
$output = Join-Path $args[$prefixIndex + 1] 'dist/index.html'
[IO.Directory]::CreateDirectory((Split-Path $output -Parent)) | Out-Null
[IO.File]::WriteAllText($output, 'new frontend', [Text.UTF8Encoding]::new($false))
'@
    Write-Text (Join-Path $root 'fake-bin/fake-go.ps1') $fakeGo
    Write-Text (Join-Path $root 'fake-bin/fake-npm.ps1') $fakeNpm
    Write-Text (Join-Path $root 'fake-bin/go.cmd') '@pwsh -NoProfile -File "%~dp0fake-go.ps1" %*'
    Write-Text (Join-Path $root 'fake-bin/npm.cmd') '@pwsh -NoProfile -File "%~dp0fake-npm.ps1" %*'

    $dist = Join-Path $root 'dist'
    foreach ($relativePath in @('nginx/html', 'tools/codex', 'tools/claude', 'data/agent-history', 'logs')) {
        [IO.Directory]::CreateDirectory((Join-Path $dist $relativePath)) | Out-Null
    }
    foreach ($file in @('backend.exe', 'middleware.exe', 'agent-server.exe', 'llm-provider-server.exe', 'nginx/nginx.exe', 'start.ps1', 'README.md')) {
        Write-Text (Join-Path $dist $file) "old-$file"
    }
    Write-Text (Join-Path $dist 'nginx/html/index.html') 'old frontend'
    Write-Text (Join-Path $dist 'tools/codex/bin/codex.exe') 'old codex'
    Write-Text (Join-Path $dist 'tools/claude/claude.exe') 'old claude'
    [IO.File]::WriteAllBytes((Join-Path $dist 'data/backend.db'), [byte[]](0, 255, 13, 10, 7, 8, 9))
    [IO.File]::WriteAllBytes((Join-Path $dist 'data/agent-history/history.bin'), [byte[]](9, 8, 0, 1, 2))
    [IO.File]::WriteAllBytes((Join-Path $dist 'logs/backend.log'), [byte[]](3, 2, 1, 0, 255))
    if (!$WithoutEnvironment) {
        [IO.File]::WriteAllBytes((Join-Path $dist '.env'), [byte[]](85, 83, 69, 82, 95, 84, 79, 75, 69, 78, 61, 111, 108, 100, 13, 10, 88, 61, 255))
    }
    return $root
}

function Get-PersistentHashes([string]$Root) {
    $dist = Join-Path $Root 'dist'
    $result = @{}
    foreach ($relativePath in @('.env', 'data/backend.db', 'data/agent-history/history.bin', 'logs/backend.log')) {
        $path = Join-Path $dist $relativePath
        if (Test-Path -LiteralPath $path) { $result[$relativePath] = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash }
    }
    return $result
}

function Get-ReleaseHashes([string]$Root) {
    $dist = Join-Path $Root 'dist'
    $result = @{}
    foreach ($relativePath in @('backend.exe', 'middleware.exe', 'agent-server.exe', 'llm-provider-server.exe', 'nginx/nginx.exe', 'nginx/html/index.html', 'start.ps1', 'README.md', 'tools/codex/bin/codex.exe', 'tools/claude/claude.exe')) {
        $path = Join-Path $dist $relativePath
        if (Test-Path -LiteralPath $path -PathType Leaf) { $result[$relativePath] = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash }
    }
    return $result
}

function Assert-Hashes([hashtable]$Expected, [hashtable]$Actual, [string]$Description) {
    Assert-True ($Expected.Count -eq $Actual.Count) "$Description file count changed"
    foreach ($key in $Expected.Keys) {
        Assert-True ($Actual.ContainsKey($key)) "$Description lost $key"
        Assert-True ($Expected[$key] -eq $Actual[$key]) "$Description changed $key"
    }
}

function Invoke-Packaging([string]$Root, [string[]]$Arguments) {
    $savedPath = $env:PATH
    $savedState = $env:FAKE_STATE
    try {
        $env:PATH = (Join-Path $Root 'fake-bin') + [IO.Path]::PathSeparator + $savedPath
        $env:FAKE_STATE = Join-Path $Root 'fake-state'
        $output = & pwsh -NoProfile -File (Join-Path $Root 'scripts/build-distribution.ps1') @Arguments 2>&1 | Out-String
        return @{ ExitCode = $LASTEXITCODE; Output = $output }
    } finally {
        $env:PATH = $savedPath
        $env:FAKE_STATE = $savedState
    }
}

function Invoke-FailureCase([string]$Name, [scriptblock]$Prepare, [string[]]$Arguments, [string]$ExpectedOutput, [switch]$WithoutEnvironment) {
    $root = New-Fixture $Name -WithoutEnvironment:$WithoutEnvironment
    $releaseBefore = Get-ReleaseHashes $root
    $persistentBefore = Get-PersistentHashes $root
    $resource = & $Prepare $root
    try {
        $result = Invoke-Packaging $root $Arguments
    } finally {
        if ($null -ne $resource -and $resource -is [IDisposable]) { $resource.Dispose() }
    }
    Assert-True ($result.ExitCode -ne 0) "$Name unexpectedly succeeded"
    Assert-True ($result.Output -match [regex]::Escape($ExpectedOutput)) "$Name did not report '$ExpectedOutput': $($result.Output)"
    Assert-Hashes $releaseBefore (Get-ReleaseHashes $root) "$Name release"
    Assert-Hashes $persistentBefore (Get-PersistentHashes $root) "$Name persistent state"
    if ($WithoutEnvironment) { Assert-True (!(Test-Path -LiteralPath (Join-Path $root 'dist/.env'))) "$Name published initial .env on failure" }
    $script:passed++
    Write-Host "PASS $Name"
}

if (!(Test-Path -LiteralPath $TemporaryParent -PathType Container)) {
    throw "Temporary parent does not exist: $TemporaryParent"
}
[IO.Directory]::CreateDirectory($testRoot) | Out-Null

try {
    $nginxArchive = Join-Path $testRoot 'nginx-fixture.zip'
    New-NginxArchive $nginxArchive
    $nginxHash = (Get-FileHash -LiteralPath $nginxArchive -Algorithm SHA256).Hash.ToLowerInvariant()
    $nginxArgs = @('-NginxArchive', $nginxArchive, '-NginxSha256', $nginxHash)
    $configArgs = @('-ConfigurationFile', 'synthetic-launch.json') + $nginxArgs

    $env:FAKE_GO_FAIL_ON = '3'
    Invoke-FailureCase 'third Go build failure' {} $configArgs 'Build failed: agent-server.exe'
    $env:FAKE_GO_FAIL_ON = $null

    $env:FAKE_NPM_FAIL = '1'
    Invoke-FailureCase 'nginx archive hash mismatch' {} (@('-ConfigurationFile', 'synthetic-launch.json', '-NginxArchive', $nginxArchive, '-NginxSha256', ('0' * 64))) 'Nginx archive SHA-256 mismatch'

    Invoke-FailureCase 'short launch token' {
        param($root)
        Write-Text (Join-Path $root 'frontend/dist/index.html') 'new frontend'
    } (@('-SkipFrontendBuild', '-ConfigurationFile', 'short-launch.json') + $nginxArgs) 'USER_TOKEN must be at least 32 bytes' -WithoutEnvironment

    Invoke-FailureCase 'frontend build failure' {} $configArgs 'Frontend build failed'
    $env:FAKE_NPM_FAIL = $null

    Invoke-FailureCase 'missing skipped frontend output' {} (@('-SkipFrontendBuild') + $configArgs) '-SkipFrontendBuild requires existing non-empty frontend output'

    Invoke-FailureCase 'explicit missing CLI' {} (@('-CodexExecutable', 'missing-codex.exe') + $configArgs) 'Requested Codex executable not found'

    Invoke-FailureCase 'frontend copy failure' {
        param($root)
        $source = Join-Path $root 'frontend/dist/index.html'
        Write-Text $source 'locked frontend source'
        [IO.File]::Open($source, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::None)
    } (@('-SkipFrontendBuild') + $configArgs) 'Failed to stage frontend output'

    Invoke-FailureCase 'locked destination preflight' {
        param($root)
        Write-Text (Join-Path $root 'frontend/dist/index.html') 'new frontend'
        [IO.File]::Open((Join-Path $root 'dist/backend.exe'), [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::None)
    } (@('-SkipFrontendBuild') + $configArgs) 'Release preflight failed' -WithoutEnvironment

    $successRoot = New-Fixture 'successful release'
    Write-Text (Join-Path $successRoot 'frontend/dist/index.html') 'new frontend'
    Write-Text (Join-Path $successRoot 'cli/bin/codex.exe') 'new codex'
    Write-Text (Join-Path $successRoot 'cli/bin/codex-code-mode-host.exe') 'tool host'
    Write-Text (Join-Path $successRoot 'cli/codex-package.json') '{"layoutVersion":1}'
    Write-Text (Join-Path $successRoot 'cli/codex-resources/fixture.txt') 'package resource'
    Write-Text (Join-Path $successRoot 'cli/bin/codex-helper.exe') 'new codex helper'
    Write-Text (Join-Path $successRoot 'cli/claude.exe') 'new claude'
    $oldRelease = Get-ReleaseHashes $successRoot
    $persistentBefore = Get-PersistentHashes $successRoot
    $success = Invoke-Packaging $successRoot (@(
        '-SkipFrontendBuild', '-ConfigurationFile', 'synthetic-launch.json',
        '-CodexExecutable', (Join-Path $successRoot 'cli/bin/codex.exe'),
        '-ClaudeExecutable', (Join-Path $successRoot 'cli/claude.exe')
    ) + $nginxArgs)
    Assert-True ($success.ExitCode -eq 0) "successful release failed: $($success.Output)"
    Assert-Hashes $persistentBefore (Get-PersistentHashes $successRoot) 'successful release persistent state'
    $newRelease = Get-ReleaseHashes $successRoot
    foreach ($key in @('backend.exe', 'middleware.exe', 'agent-server.exe', 'llm-provider-server.exe', 'nginx/nginx.exe', 'nginx/html/index.html', 'start.ps1', 'README.md', 'tools/codex/bin/codex.exe', 'tools/claude/claude.exe')) {
        Assert-True ($oldRelease[$key] -ne $newRelease[$key]) "successful release did not replace $key"
    }
    Assert-True (([IO.File]::ReadAllText((Join-Path $successRoot 'dist/nginx/html/index.html'))) -eq 'new frontend') 'successful release frontend content is wrong'
    foreach ($removed in @('console.exe', 'public')) {
        Assert-True (!(Test-Path -LiteralPath (Join-Path $successRoot "dist/$removed"))) "successful release contains $removed"
    }
    Assert-True (([IO.File]::ReadAllText((Join-Path $successRoot 'dist/nginx/conf/release-edge.conf.template'))) -eq 'synthetic edge template') 'successful release edge template was not copied verbatim'
    Assert-True (([IO.File]::ReadAllText((Join-Path $successRoot 'dist/nginx/conf/release-nginx.conf'))) -eq 'synthetic nginx main') 'successful release nginx main config was not copied verbatim'
    Assert-True (!(Test-Path -LiteralPath (Join-Path $successRoot 'dist/secrets'))) 'successful release with an existing .env generated secrets'
    Assert-True (Test-Path -LiteralPath (Join-Path $successRoot 'dist/tools/codex/bin/codex-helper.exe')) 'successful release omitted Codex companion executable'
    Assert-True (Test-Path -LiteralPath (Join-Path $successRoot 'dist/tools/codex/bin/codex-code-mode-host.exe')) 'successful release omitted Codex tool host'
    Assert-True (Test-Path -LiteralPath (Join-Path $successRoot 'dist/tools/codex/codex-resources/fixture.txt')) 'successful release omitted Codex package resources'
    $passed++
    Write-Host 'PASS successful release'

    $rollbackRoot = New-Fixture 'publication rollback'
    Write-Text (Join-Path $rollbackRoot 'frontend/dist/index.html') 'new frontend'
    Write-Text (Join-Path $rollbackRoot 'cli/bin/codex.exe') 'new codex'
    Write-Text (Join-Path $rollbackRoot 'cli/bin/codex-code-mode-host.exe') 'tool host'
    Write-Text (Join-Path $rollbackRoot 'cli/codex-package.json') '{"layoutVersion":1}'
    Write-Text (Join-Path $rollbackRoot 'cli/codex-resources/fixture.txt') 'package resource'
    Remove-Item -LiteralPath (Join-Path $rollbackRoot 'dist/tools') -Recurse -Force
    Write-Text (Join-Path $rollbackRoot 'dist/tools') 'old blocking tools file'
    $rollbackRelease = Get-ReleaseHashes $rollbackRoot
    $rollbackRelease['tools'] = (Get-FileHash -LiteralPath (Join-Path $rollbackRoot 'dist/tools') -Algorithm SHA256).Hash
    $rollbackPersistent = Get-PersistentHashes $rollbackRoot
    $rollback = Invoke-Packaging $rollbackRoot @(
        '-SkipFrontendBuild', '-ConfigurationFile', 'synthetic-launch.json',
        '-CodexExecutable', (Join-Path $rollbackRoot 'cli/bin/codex.exe')
    )
    Assert-True ($rollback.ExitCode -ne 0) 'publication rollback unexpectedly succeeded'
    Assert-True ($rollback.Output -match 'previous release was restored') "publication rollback did not report restoration: $($rollback.Output)"
    $rollbackAfter = Get-ReleaseHashes $rollbackRoot
    $rollbackAfter['tools'] = (Get-FileHash -LiteralPath (Join-Path $rollbackRoot 'dist/tools') -Algorithm SHA256).Hash
    Assert-Hashes $rollbackRelease $rollbackAfter 'publication rollback release'
    Assert-Hashes $rollbackPersistent (Get-PersistentHashes $rollbackRoot) 'publication rollback persistent state'
    $passed++
    Write-Host 'PASS publication rollback'

    $initialRoot = New-Fixture 'initial environment' -WithoutEnvironment
    Write-Text (Join-Path $initialRoot 'frontend/dist/index.html') 'new frontend'
    $initial = Invoke-Packaging $initialRoot (@('-SkipFrontendBuild') + $configArgs)
    Assert-True ($initial.ExitCode -eq 0) "initial environment release failed: $($initial.Output)"
    $initialEnv = [IO.File]::ReadAllLines((Join-Path $initialRoot 'dist/.env'))
    $initialEnvText = [IO.File]::ReadAllText((Join-Path $initialRoot 'dist/.env'))
    Assert-True ($initialEnv -contains 'USER_TOKEN_FILE=secrets/stock-ai-release_backend_middleware_token') 'initial environment omitted USER_TOKEN_FILE'
    Assert-True ($initialEnv -contains 'AGENT_SERVICE_TOKEN_FILE=secrets/stock-ai-release_backend_user_token') 'initial environment omitted AGENT_SERVICE_TOKEN_FILE'
    Assert-True ($initialEnv -contains 'AGENT_SERVER_BACKEND_USER_TOKEN_FILE=secrets/stock-ai-release_backend_user_token') 'Agent does not use the Agent-only backend token'
    Assert-True ($initialEnv -contains 'MIDDLEWARE_ADDR=127.0.0.1:4180') 'initial environment omitted MIDDLEWARE_ADDR'
    Assert-True ($initialEnv -contains 'OIDC_COOKIE_NAME=__Host-stock-ai-release-session') 'initial environment omitted OIDC_COOKIE_NAME'
    Assert-True ($initialEnvText -notmatch 'CONSOLE_') 'initial environment still contains CONSOLE_ settings'
    $secretsDirectory = Join-Path $initialRoot 'dist/secrets'
    $expectedSecrets = @{
        backend_middleware_token = 'fixture-user-token-0123456789abcdefghij'
        agent_admin_token = 'fixture-agent-token-0123456789abcdefgh'
        llm_admin_token = 'fixture-admin-token-0123456789abcdefghi'
        llm_runtime_token = 'fixture-runtime-token-0123456789abcdefg'
        llm_master_key = 'fixture-master-key'
    }
    foreach ($name in $expectedSecrets.Keys) {
        $secretFile = Join-Path $secretsDirectory "stock-ai-release_$name"
        Assert-True (Test-Path -LiteralPath $secretFile -PathType Leaf) "secret file $name is missing"
        Assert-True (([IO.File]::ReadAllText($secretFile)) -eq $expectedSecrets[$name]) "secret file $name does not hold the launch configuration value"
    }
    $agentToken = [IO.File]::ReadAllText((Join-Path $secretsDirectory 'stock-ai-release_backend_user_token'))
    Assert-True ($agentToken.Length -ge 32) 'generated backend_user_token is shorter than 32 bytes'
    Assert-True ($agentToken -ne $expectedSecrets.backend_middleware_token) 'backend_user_token equals backend_middleware_token'
    $cookieKey = [IO.File]::ReadAllText((Join-Path $secretsDirectory 'stock-ai-release_oidc_cookie_key'))
    $cookieKeyBytes = [Convert]::FromBase64String($cookieKey)
    Assert-True ($cookieKeyBytes.Length -eq 32 -and [Convert]::ToBase64String($cookieKeyBytes) -eq $cookieKey) 'oidc_cookie_key is not canonical base64 of 32 bytes'
    Assert-True (!(Test-Path -LiteralPath (Join-Path $secretsDirectory 'stock-ai-release_oidc_client_secret'))) 'build generated oidc_client_secret'
    foreach ($secretValue in @($expectedSecrets.Values) + @($agentToken, $cookieKey)) {
        Assert-True (!$initialEnvText.Contains($secretValue)) '.env contains a secret value'
    }
    foreach ($artifact in @('middleware.exe', 'nginx/nginx.exe')) {
        Assert-True (Test-Path -LiteralPath (Join-Path $initialRoot "dist/$artifact") -PathType Leaf) "initial release omitted $artifact"
    }
    Assert-True ($initialEnv -notcontains 'LLM_SERVER_CODEX_EXECUTABLE=tools/codex/bin/codex.exe') 'initial environment claimed an unrequested Codex CLI'
    Assert-True (([IO.File]::ReadAllText((Join-Path $initialRoot 'dist/tools/codex/bin/codex.exe'))) -eq 'old codex') 'unrequested Codex tool was changed'
    Assert-True (([IO.File]::ReadAllText((Join-Path $initialRoot 'dist/tools/claude/claude.exe'))) -eq 'old claude') 'unrequested Claude tool was changed'
    $passed++
    Write-Host 'PASS initial environment after successful release'

    Write-Host "All $passed distribution packaging tests passed."
} finally {
    $env:FAKE_GO_FAIL_ON = $null
    $env:FAKE_NPM_FAIL = $null
    $env:FAKE_NPM_NO_OUTPUT = $null
    if (Test-Path -LiteralPath $testRoot) { Remove-Item -LiteralPath $testRoot -Recurse -Force }
}
