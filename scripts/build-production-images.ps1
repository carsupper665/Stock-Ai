[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidatePattern('^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$')]
    [string]$Version,
    [string]$EvidenceDirectory
)

$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $true
Set-StrictMode -Version Latest

function New-EphemeralSecret {
    $bytes = [byte[]]::new(32)
    [Security.Cryptography.RandomNumberGenerator]::Fill($bytes)
    return [Convert]::ToBase64String($bytes)
}

$root = (Resolve-Path (Join-Path $PSScriptRoot '..')).ProviderPath
$gitSafeDirectory = $root.Replace('\', '/')
$builder = "stock-ai-evidence-$PID"
$builderCreated = $false
$trivyImage = 'aquasec/trivy:0.75.0@sha256:af6acf9a6b85dfe389a1941505c0ce9efef52a4719635e1a962f022a3d855daa'
$trivyCache = "stock-ai-trivy-cache-$PID"
$trivyCacheCreated = $false
Push-Location $root
try {
    if (& git -c "safe.directory=$gitSafeDirectory" status --porcelain=v1 --untracked-files=all) {
        throw 'Production images must be built from a clean checkout.'
    }
    $revision = (& git -c "safe.directory=$gitSafeDirectory" rev-parse HEAD).Trim()
    $tree = (& git -c "safe.directory=$gitSafeDirectory" rev-parse 'HEAD^{tree}').Trim()
    $sourceDateEpoch = (& git -c "safe.directory=$gitSafeDirectory" show -s --format=%ct HEAD).Trim()
    $buildDate = [DateTimeOffset]::FromUnixTimeSeconds([long]$sourceDateEpoch).UtcDateTime.ToString('yyyy-MM-ddTHH:mm:ssZ')
    if (!$EvidenceDirectory) {
        $EvidenceDirectory = Join-Path ([IO.Path]::GetTempPath()) "stock-ai-production-images-$revision"
    }
    $EvidenceDirectory = [IO.Path]::GetFullPath($EvidenceDirectory)
    if ($EvidenceDirectory.StartsWith($root + [IO.Path]::DirectorySeparatorChar, [StringComparison]::OrdinalIgnoreCase)) {
        throw 'EvidenceDirectory must be outside the source checkout so the clean-tree invariant remains meaningful.'
    }
    if (Test-Path $EvidenceDirectory) {
        throw "Evidence directory already exists: $EvidenceDirectory"
    }
    New-Item -ItemType Directory -Path $EvidenceDirectory | Out-Null

    $serverOS = (& docker version --format '{{.Server.Os}}').Trim()
    if ($serverOS -ne 'linux') {
        throw "Docker must be in Linux-container mode; server OS is '$serverOS'."
    }
    & docker buildx version | Out-Null
    & docker pull $trivyImage | Out-Null
    $trivyVersion = ((& docker run --rm $trivyImage --version) -join "`n")
    if ($trivyVersion -notmatch 'Version:\s+0\.75\.0') {
        throw "Pinned Trivy image reported an unexpected version: $trivyVersion"
    }
    & docker volume create $trivyCache | Out-Null
    $trivyCacheCreated = $true
    & docker buildx create --name $builder --driver docker-container --bootstrap | Out-Null
    $builderCreated = $true

    $agentAdmin = New-EphemeralSecret
    $backendUser = New-EphemeralSecret
    $llmRuntime = New-EphemeralSecret
    $llmAdmin = New-EphemeralSecret
    $services = @(
        @{ Name = 'backend'; Dockerfile = 'docker/backend.Dockerfile'; Memory = '512m'; CPUs = '1.0'; PIDs = 100; DataProbe = '/data/backend.db'; HealthURL = 'http://127.0.0.1:7794/v1/health'; Environment = [ordered]@{
            ALLOW_STATIC_USER_TOKEN = 'true'; USER_TOKEN = $backendUser; ACCOUNT_TOKEN_KEY = (New-EphemeralSecret)
        } },
        @{ Name = 'agent-server'; Dockerfile = 'docker/agent-server.Dockerfile'; Memory = '1g'; CPUs = '2.0'; PIDs = 200; DataProbe = '/data/agent-server.db'; HealthURL = 'http://127.0.0.1:8080/health'; Environment = [ordered]@{
            AGENT_SERVER_ADMIN_TOKEN = $agentAdmin; AGENT_SERVER_BACKEND_USER_TOKEN = $backendUser
            AGENT_SERVER_GATEWAY_RUNTIME_TOKEN = $llmRuntime; AGENT_SERVER_BACKEND_URL = 'http://backend.invalid'
            AGENT_SERVER_GATEWAY_URL = 'http://llm.invalid'
        } },
        @{ Name = 'llm-provider-server'; Dockerfile = 'docker/llm-provider-server.Dockerfile'; Memory = '512m'; CPUs = '1.0'; PIDs = 100; DataProbe = '/data/llm-provider.db'; HealthURL = 'http://127.0.0.1:8090/healthz'; Environment = [ordered]@{
            LLM_SERVER_ADMIN_TOKEN = $llmAdmin; LLM_SERVER_RUNTIME_TOKEN = $llmRuntime; LLM_SERVER_MASTER_KEY = (New-EphemeralSecret)
        } },
        @{ Name = 'console'; Dockerfile = 'docker/console.Dockerfile'; Memory = '128m'; CPUs = '0.5'; PIDs = 50; DataProbe = $null; HealthURL = 'http://127.0.0.1:8088/'; Environment = [ordered]@{
            AGENT_SERVER_ADMIN_TOKEN = $agentAdmin; LLM_SERVER_ADMIN_TOKEN = $llmAdmin; LLM_SERVER_RUNTIME_TOKEN = $llmRuntime
            BACKEND_URL = 'http://backend.invalid'; AGENT_SERVER_URL = 'http://agent.invalid'; LLM_SERVER_URL = 'http://llm.invalid'
            OIDC_ISSUER = 'https://identity.invalid/realms/acceptance'; OIDC_AUDIENCE = 'stock-ai-acceptance'
            OIDC_USER_SUBJECT = 'acceptance-user'; OIDC_JWKS_URL = 'https://identity.invalid/realms/acceptance/keys'
        } }
    )
    $images = [ordered]@{}
    foreach ($service in $services) {
        $name = $service.Name
        $tag = "stock-ai/${name}:$Version"
        $metadataPath = Join-Path $EvidenceDirectory "$name-build-metadata.json"
        $ociPath = Join-Path $EvidenceDirectory "$name-image.oci.tar"
        & docker buildx build --builder $builder --platform linux/amd64 --file $service.Dockerfile --tag $tag `
            --build-arg "BUILD_DATE=$buildDate" --build-arg "REVISION=$revision" `
            --build-arg "VERSION=$Version" --build-arg "SOURCE_DATE_EPOCH=$sourceDateEpoch" `
            --provenance=mode=max --sbom=true --metadata-file $metadataPath --output "type=oci,dest=$ociPath" .
        if ($LASTEXITCODE -ne 0) { throw "Image build failed: $name" }

        $rebuildTag = "stock-ai/${name}:$Version-rebuild"
        $rebuildMetadataPath = Join-Path $EvidenceDirectory "$name-rebuild-metadata.json"
        $rebuildOCIPath = Join-Path ([IO.Path]::GetTempPath()) "$name-rebuild-$PID.oci.tar"
        & docker buildx build --builder $builder --platform linux/amd64 --file $service.Dockerfile --tag $tag `
            --build-arg "BUILD_DATE=$buildDate" --build-arg "REVISION=$revision" `
            --build-arg "VERSION=$Version" --build-arg "SOURCE_DATE_EPOCH=$sourceDateEpoch" `
            --provenance=mode=max --sbom=true --metadata-file $rebuildMetadataPath --output "type=oci,dest=$rebuildOCIPath" .
        $ociHash = (Get-FileHash -Algorithm SHA256 $ociPath).Hash.ToLowerInvariant()
        $rebuildOCIHash = (Get-FileHash -Algorithm SHA256 $rebuildOCIPath).Hash.ToLowerInvariant()
        & docker buildx build --builder $builder --platform linux/amd64 --file $service.Dockerfile --tag $tag `
            --build-arg "BUILD_DATE=$buildDate" --build-arg "REVISION=$revision" `
            --build-arg "VERSION=$Version" --build-arg "SOURCE_DATE_EPOCH=$sourceDateEpoch" `
            --provenance=false --sbom=false --load .
        & docker buildx build --builder $builder --platform linux/amd64 --file $service.Dockerfile --tag $rebuildTag `
            --build-arg "BUILD_DATE=$buildDate" --build-arg "REVISION=$revision" `
            --build-arg "VERSION=$Version" --build-arg "SOURCE_DATE_EPOCH=$sourceDateEpoch" `
            --provenance=false --sbom=false --load .
        $imageID = (& docker image inspect --format '{{.Id}}' $tag).Trim()
        $rebuildImageID = (& docker image inspect --format '{{.Id}}' $rebuildTag).Trim()
        $firstBuild = Get-Content -Raw $metadataPath | ConvertFrom-Json
        $secondBuild = Get-Content -Raw $rebuildMetadataPath | ConvertFrom-Json
        $firstDigest = $firstBuild.'containerimage.digest'
        $secondDigest = $secondBuild.'containerimage.digest'
        $firstMaterials = $firstBuild.'buildx.build.provenance'.materials | ConvertTo-Json -Depth 8 -Compress
        $secondMaterials = $secondBuild.'buildx.build.provenance'.materials | ConvertTo-Json -Depth 8 -Compress
        Remove-Item -Force $rebuildOCIPath
        if (!$firstBuild.'buildx.build.provenance' -or !$secondBuild.'buildx.build.provenance' -or
            !$firstDigest -or !$secondDigest -or $imageID -ne $rebuildImageID -or $firstMaterials -ne $secondMaterials) {
            throw "$name same-input rebuild differs; promotion is blocked."
        }

        $inspectPath = Join-Path $EvidenceDirectory "$name-inspect.json"
        & docker image inspect $tag | Set-Content -Encoding utf8NoBOM $inspectPath
        $labelRevision = (& docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' $tag).Trim()
        if ($labelRevision -ne $revision) { throw "$name revision label does not match HEAD." }
        $user = (& docker image inspect --format '{{.Config.User}}' $tag).Trim()
        if ($user -ne '65532:65532') { throw "$name does not run as the fixed non-root identity." }
        $health = (& docker image inspect --format '{{json .Config.Healthcheck.Test}}' $tag).Trim()
        if (!$health -or $health -eq 'null' -or !$health.Contains($service.HealthURL)) {
            throw "$name does not have the expected image health check."
        }

        $container = "stock-ai-inventory-$name-$PID"
        $exportPath = Join-Path ([IO.Path]::GetTempPath()) "$container.tar"
        $containerCreated = $false
        try {
            & docker create --name $container $tag | Out-Null
            $containerCreated = $true
            & docker export --output $exportPath $container
            $inventory = @(& tar -tf $exportPath | Sort-Object -Unique)
            $inventory | Set-Content -Encoding utf8NoBOM (Join-Path $EvidenceDirectory "$name-files.txt")
            $forbidden = $inventory | Where-Object {
                $_ -match '(^|/)(\.env[^/]*|node_modules|[^/]+\.(go|c|cc|cpp|h|db|log))$' -or
                $_ -match '(^|/)(sh|ash|bash|busybox|apk|apt|apt-get|dpkg|curl|wget|go|gcc|node|npm|npx)$' -or
                $_ -match '(^|/)(auth|credentials|token|secret)[^/]*\.(json|txt|env)$'
            }
            if ($forbidden) { throw "$name contains forbidden runtime files: $($forbidden -join ', ')" }
            if ($inventory -notcontains 'etc/ssl/certs/ca-certificates.crt') {
                throw "$name does not contain the approved CA root bundle."
            }
        } finally {
            if ($containerCreated) {
                & docker rm -f $container | Out-Null
            }
            Remove-Item -Force -ErrorAction SilentlyContinue $exportPath
        }

        $sbomPath = Join-Path $EvidenceDirectory "$name-sbom.spdx.json"
        $sbomLogPath = Join-Path $EvidenceDirectory "$name-sbom.log"
        $previousNativePreference = $PSNativeCommandUseErrorActionPreference
        $PSNativeCommandUseErrorActionPreference = $false
        try {
            & docker run --rm --volume '/var/run/docker.sock:/var/run/docker.sock' `
                --volume "${trivyCache}:/root/.cache/trivy" $trivyImage image --quiet --scanners vuln `
                --format spdx-json $tag 2> $sbomLogPath | Set-Content -Encoding utf8NoBOM $sbomPath
            $sbomExitCode = $LASTEXITCODE
        } finally {
            $PSNativeCommandUseErrorActionPreference = $previousNativePreference
        }
        if ($sbomExitCode -ne 0 -or !(Test-Path $sbomPath) -or (Get-Item $sbomPath).Length -eq 0) {
            throw "Trivy SPDX SBOM generation failed: $name"
        }
        $scanPath = Join-Path $EvidenceDirectory "$name-vulnerabilities.sarif"
        $scanLogPath = Join-Path $EvidenceDirectory "$name-vulnerabilities.log"
        $previousNativePreference = $PSNativeCommandUseErrorActionPreference
        $PSNativeCommandUseErrorActionPreference = $false
        try {
            & docker run --rm --volume '/var/run/docker.sock:/var/run/docker.sock' `
                --volume "${trivyCache}:/root/.cache/trivy" $trivyImage image --quiet --scanners vuln `
                --severity HIGH,CRITICAL --exit-code 1 --format sarif $tag `
                2> $scanLogPath | Set-Content -Encoding utf8NoBOM $scanPath
            $scanExitCode = $LASTEXITCODE
        } finally {
            $PSNativeCommandUseErrorActionPreference = $previousNativePreference
        }
        if ($scanExitCode -ne 0) { throw "$name has a Critical/High vulnerability or its Trivy scan failed." }
        if (!(Test-Path $scanPath) -or (Get-Item $scanPath).Length -eq 0) { throw "Trivy SARIF generation failed: $name" }

        $runtimeContainer = "stock-ai-runtime-$name-$PID"
        $runtimeVolume = "stock-ai-runtime-$name-$PID"
        $containerCreated = $false
        $volumeCreated = $false
        $caCopy = Join-Path ([IO.Path]::GetTempPath()) "$runtimeContainer-ca-certificates.crt"
        $dataCopy = Join-Path ([IO.Path]::GetTempPath()) "$runtimeContainer-data-probe"
        try {
            $runArgs = @('run', '--detach', '--name', $runtimeContainer, '--read-only', '--tmpfs', '/tmp:rw,noexec,nosuid,size=16m',
                '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges:true', '--memory', $service.Memory,
                '--cpus', $service.CPUs, '--pids-limit', [string]$service.PIDs)
            if ($service.DataProbe) {
                & docker volume create $runtimeVolume | Out-Null
                $volumeCreated = $true
                $runArgs += @('--mount', "type=volume,src=$runtimeVolume,dst=/data")
            }
            foreach ($entry in $service.Environment.GetEnumerator()) {
                $runArgs += @('--env', "$($entry.Key)=$($entry.Value)")
            }
            $runArgs += $tag
            & docker @runArgs | Out-Null
            $containerCreated = $true

            $deadline = [DateTime]::UtcNow.AddSeconds(90)
            do {
                $running = (& docker inspect --format '{{.State.Running}}' $runtimeContainer).Trim()
                $healthStatus = (& docker inspect --format '{{.State.Health.Status}}' $runtimeContainer).Trim()
                if ($running -ne 'true' -or $healthStatus -eq 'unhealthy') {
                    throw "$name failed constrained runtime health acceptance."
                }
                if ($healthStatus -ne 'healthy') { Start-Sleep -Seconds 2 }
            } while ($healthStatus -ne 'healthy' -and [DateTime]::UtcNow -lt $deadline)
            if ($healthStatus -ne 'healthy') { throw "$name did not become healthy within 90 seconds." }

            $top = @(& docker top $runtimeContainer -eo user,pid,comm)
            if (($top -join "`n") -notmatch '(?m)^65532\s+') { throw "$name is not running as numeric user 65532." }
            $hostConfig = (& docker inspect --format '{{json .HostConfig}}' $runtimeContainer) | ConvertFrom-Json
            $mounts = @((& docker inspect --format '{{json .Mounts}}' $runtimeContainer) | ConvertFrom-Json)
            if (!$hostConfig.ReadonlyRootfs -or $hostConfig.Memory -le 0 -or $hostConfig.NanoCpus -le 0 -or $hostConfig.PidsLimit -le 0 -or
                $hostConfig.CapDrop -notcontains 'ALL' -or $hostConfig.SecurityOpt -notcontains 'no-new-privileges:true' -or
                !$hostConfig.Tmpfs.'/tmp') {
                throw "$name runtime constraints were not applied by Docker."
            }
            if ($service.DataProbe -and ($mounts.Destination -notcontains '/data')) {
                throw "$name does not have its declared /data writable mount."
            }
            & docker cp "${runtimeContainer}:/etc/ssl/certs/ca-certificates.crt" $caCopy
            $caInfo = Get-Item $caCopy
            if ($caInfo.Length -le 0) { throw "$name CA root bundle is empty." }
            $runtimeResult = [ordered]@{
                image_id = $imageID; health = $healthStatus; process = $top; user = '65532:65532'
                read_only_root = $hostConfig.ReadonlyRootfs; tmpfs = $hostConfig.Tmpfs.'/tmp'; memory_bytes = $hostConfig.Memory
                nano_cpus = $hostConfig.NanoCpus; pids = $hostConfig.PidsLimit; capabilities_dropped = $hostConfig.CapDrop
                security_options = $hostConfig.SecurityOpt; mounts = $mounts
                ca_bundle_sha256 = (Get-FileHash -Algorithm SHA256 $caCopy).Hash.ToLowerInvariant()
                ca_bundle_bytes = $caInfo.Length
            }
            if ($service.DataProbe) {
                & docker cp "${runtimeContainer}:$($service.DataProbe)" $dataCopy
                $dataInfo = Get-Item $dataCopy
                if ($dataInfo.Length -le 0) { throw "$name did not create writable state at $($service.DataProbe)." }
                $runtimeResult['data_path'] = $service.DataProbe
                $runtimeResult['data_bytes'] = $dataInfo.Length
                $runtimeResult['data_sha256'] = (Get-FileHash -Algorithm SHA256 $dataCopy).Hash.ToLowerInvariant()
            }
            $runtimeResult | ConvertTo-Json -Depth 6 | Set-Content -Encoding utf8NoBOM (Join-Path $EvidenceDirectory "$name-runtime.json")
            & docker logs $runtimeContainer 2>&1 | Set-Content -Encoding utf8NoBOM (Join-Path $EvidenceDirectory "$name-runtime.log")
        } finally {
            if ($containerCreated) {
                & docker stop --time 15 $runtimeContainer | Out-Null
                & docker rm -f $runtimeContainer | Out-Null
            }
            if ($volumeCreated) { & docker volume rm $runtimeVolume | Out-Null }
            Remove-Item -Force -ErrorAction SilentlyContinue $caCopy, $dataCopy
        }

        $images[$name] = [ordered]@{
            tag = $tag; image_id = $imageID; build_result_digest = $firstDigest; dockerfile = $service.Dockerfile
            oci_archive_sha256 = $ociHash; rebuild_tag = $rebuildTag; rebuild_image_id = $rebuildImageID
            rebuild_result_digest = $secondDigest; rebuild_oci_archive_sha256 = $rebuildOCIHash
        }
    }

    $inputs = @(
        '.dockerignore', 'docker/backend.Dockerfile', 'docker/agent-server.Dockerfile',
        'docker/llm-provider-server.Dockerfile', 'docker/console.Dockerfile', 'docker/healthcheck.go',
        'backend/go.mod', 'backend/go.sum', 'agent_server/go.mod', 'agent_server/go.sum',
        'llm_provider_server/go.mod', 'llm_provider_server/go.sum',
        'frontend/package.json', 'frontend/package-lock.json'
    )
    $inputHashes = [ordered]@{}
    foreach ($input in $inputs) {
        $inputHashes[$input] = (Get-FileHash -Algorithm SHA256 $input).Hash.ToLowerInvariant()
    }
    $metadata = [ordered]@{
        schema = 1
        revision = $revision
        source_tree = $tree
        version = $Version
        source_date_epoch = [long]$sourceDateEpoch
        build_date = $buildDate
        platform = 'linux/amd64'
        docker = (& docker version --format '{{json .}}' | ConvertFrom-Json)
        buildx = ((& docker buildx version) -join "`n")
        trivy_image = $trivyImage
        trivy_version = $trivyVersion
        images = $images
        inputs_sha256 = $inputHashes
    }
    $metadata | ConvertTo-Json -Depth 12 | Set-Content -Encoding utf8NoBOM (Join-Path $EvidenceDirectory 'provenance.json')
    [ordered]@{
        status = 'pass'
        revision = $revision
        image_count = $images.Count
        critical_high_findings = 0
        sbom = 'pass: SPDX JSON and BuildKit attestation present for every image'
        filesystem_policy = 'pass: forbidden files and tools absent from every flattened image'
        runtime_policy = 'pass: healthy as 65532:65532 with read-only root, bounded resources, CA roots, and declared writable state'
        reproducibility = 'pass: same-input image IDs and provenance materials match; per-invocation attestation envelope digests are recorded'
        images = $images
    } | ConvertTo-Json -Depth 8 | Set-Content -Encoding utf8NoBOM (Join-Path $EvidenceDirectory 'acceptance-summary.json')

    Get-ChildItem -File $EvidenceDirectory | Sort-Object Name | ForEach-Object {
        $hash = (Get-FileHash -Algorithm SHA256 $_.FullName).Hash.ToLowerInvariant()
        "$hash  $($_.Name)"
    } | Set-Content -Encoding ascii (Join-Path $EvidenceDirectory 'SHA256SUMS')

    if (& git -c "safe.directory=$gitSafeDirectory" status --porcelain=v1 --untracked-files=all) {
        throw 'The production build modified the source checkout.'
    }
    Write-Host "PASS: four production images built and scanned by pinned Trivy; evidence: $EvidenceDirectory"
} finally {
    if ($builderCreated) { & docker buildx rm $builder | Out-Null }
    if ($trivyCacheCreated) { & docker volume rm $trivyCache | Out-Null }
    Pop-Location
}
