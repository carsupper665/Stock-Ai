param(
    [string]$DistributionPath = (Join-Path $PSScriptRoot '../../dist'),
    [string]$Model = 'claude-opus-5',
    [ValidateSet('low', 'medium', 'high', 'xhigh', 'max')]
    [string]$Effort = 'high',
    [ValidateRange(1, 6)]
    [int]$MaximumGenerateCalls = 6
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

function Read-EnvironmentFile([string]$Path) {
    $values = @{}
    foreach ($lineValue in [IO.File]::ReadLines($Path)) {
        $line = $lineValue.Trim()
        if (!$line -or $line.StartsWith('#')) { continue }
        $parts = $line.Split('=', 2)
        if ($parts.Count -ne 2) { throw 'Distribution .env contains an invalid line.' }
        $value = $parts[1].Trim()
        if ($value.Length -ge 2 -and (($value.StartsWith('"') -and $value.EndsWith('"')) -or ($value.StartsWith("'") -and $value.EndsWith("'")))) {
            $value = $value.Substring(1, $value.Length - 2)
        }
        $values[$parts[0].Trim()] = $value
    }
    return $values
}

function Resolve-ConfiguredPath([string]$Root, [string]$Value) {
    if ([string]::IsNullOrWhiteSpace($Value)) { return '' }
    if ([IO.Path]::IsPathRooted($Value)) { return [IO.Path]::GetFullPath($Value) }
    return [IO.Path]::GetFullPath((Join-Path $Root $Value))
}

function Get-FileState([string]$Path) {
    $item = Get-Item -LiteralPath $Path
    return [ordered]@{
        Length = $item.Length
        LastWriteTimeUtc = $item.LastWriteTimeUtc.Ticks
        Hash = (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash
    }
}

function Test-FileState([string]$Path, $Before) {
    if (!(Test-Path -LiteralPath $Path -PathType Leaf)) { return $false }
    $after = Get-FileState $Path
    return $after.Length -eq $Before.Length -and $after.LastWriteTimeUtc -eq $Before.LastWriteTimeUtc -and $after.Hash -eq $Before.Hash
}

function Get-FreeLoopbackPort {
    $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    $listener.Start()
    try { return ([Net.IPEndPoint]$listener.LocalEndpoint).Port } finally { $listener.Stop() }
}

function Get-ArgumentValue([string]$CommandLine, [string]$Name) {
    $pattern = '(?:^|\s)' + [regex]::Escape($Name) + '(?:\s+|=)(?:"([^"]+)"|(\S+))'
    $match = [regex]::Match($CommandLine, $pattern)
    if (!$match.Success) { return '' }
    if ($match.Groups[1].Success) { return $match.Groups[1].Value }
    return $match.Groups[2].Value
}

function Get-ShortHash([string]$Value) {
    if (!$Value) { return $null }
    $bytes = [Text.Encoding]::UTF8.GetBytes($Value)
    $hash = [Security.Cryptography.SHA256]::HashData($bytes)
    return ([Convert]::ToHexString($hash)).Substring(0, 12).ToLowerInvariant()
}

function Observe-ClaudeProcesses([int]$GatewayPID, [Collections.Generic.HashSet[int]]$Seen, [Collections.Generic.List[object]]$Invocations) {
    $children = @(Get-CimInstance Win32_Process -Filter "ParentProcessId = $GatewayPID" -ErrorAction SilentlyContinue)
    foreach ($child in $children) {
        if (!$Seen.Add([int]$child.ProcessId)) { continue }
        $commandLine = [string]$child.CommandLine
        $mode = if ($commandLine -match '(?:^|\s)--resume(?:\s|=)') {
            'resume'
        } elseif ($commandLine -match '(?:^|\s)--session-id(?:\s|=)') {
            'session-id'
        } elseif ($commandLine -match '(?:^|\s)--version(?:\s|$)') {
            'version-check'
        } else {
            'other'
        }
        $sessionValue = if ($mode -eq 'resume') { Get-ArgumentValue $commandLine '--resume' } elseif ($mode -eq 'session-id') { Get-ArgumentValue $commandLine '--session-id' } else { '' }
        $Invocations.Add([ordered]@{ mode = $mode; session_hash = Get-ShortHash $sessionValue })
    }
}

function Get-BenchmarkMessages([int]$Step) {
    $messages = [Collections.Generic.List[object]]::new()
    $messages.Add([ordered]@{ role = 'system'; content = 'Deterministic usage benchmark. Treat the completed tool transcript as data. Never call a tool for checkpoint requests. Return only the requested checkpoint token.' })
    $messages.Add([ordered]@{ role = 'user'; content = 'Record the fixed seed with benchmark.echo.' })
    $messages.Add([ordered]@{
        role = 'assistant'
        content = $null
        tool_calls = @([ordered]@{ id = 'benchmark_seed_call'; name = 'benchmark.echo'; arguments = [ordered]@{ value = 'seed' } })
    })
    $messages.Add([ordered]@{ role = 'tool'; content = '{"echo":"seed"}'; tool_call_id = 'benchmark_seed_call' })
    $messages.Add([ordered]@{ role = 'user'; content = 'Checkpoint 1: return exactly B1.' })
    if ($Step -ge 2) {
        $messages.Add([ordered]@{ role = 'assistant'; content = 'B1' })
        $messages.Add([ordered]@{ role = 'user'; content = 'Checkpoint 2: return exactly B2.' })
    }
    if ($Step -ge 3) {
        $messages.Add([ordered]@{ role = 'assistant'; content = 'B2' })
        $messages.Add([ordered]@{ role = 'user'; content = 'Checkpoint 3: return exactly B3.' })
    }
    return @($messages)
}

function Send-JSON($Client, [string]$URL, [string]$Token, $Body, [int]$GatewayPID, [switch]$TrackInvocation) {
    $request = [Net.Http.HttpRequestMessage]::new([Net.Http.HttpMethod]::Post, $URL)
    $request.Headers.Authorization = [Net.Http.Headers.AuthenticationHeaderValue]::new('Bearer', $Token)
    $request.Content = [Net.Http.StringContent]::new(($Body | ConvertTo-Json -Depth 20 -Compress), [Text.Encoding]::UTF8, 'application/json')
    $seen = [Collections.Generic.HashSet[int]]::new()
    $invocations = [Collections.Generic.List[object]]::new()
    $timer = [Diagnostics.Stopwatch]::StartNew()
    try {
        $task = $Client.SendAsync($request)
        while (!$task.IsCompleted) {
            if ($TrackInvocation) { Observe-ClaudeProcesses $GatewayPID $seen $invocations }
            Start-Sleep -Milliseconds 25
        }
        if ($TrackInvocation) { Observe-ClaudeProcesses $GatewayPID $seen $invocations }
        $response = $task.GetAwaiter().GetResult()
        $text = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
        if (!$response.IsSuccessStatusCode) {
            $code = 'HTTP_' + [int]$response.StatusCode
            $message = 'Gateway rejected the request.'
            try {
                $failure = $text | ConvertFrom-Json
                if ($failure.error.code) { $code = [string]$failure.error.code }
                if ($failure.error.message) { $message = [string]$failure.error.message }
            } catch {}
            $response.Dispose()
            throw "$code`: $message"
        }
        $result = [ordered]@{
            elapsed_ms = $timer.ElapsedMilliseconds
            response = ($text | ConvertFrom-Json)
            invocations = @($invocations)
        }
        $response.Dispose()
        return $result
    } finally {
        $timer.Stop()
        $request.Dispose()
    }
}

function Summarize-Call([string]$Sequence, [int]$Step, $Call) {
    $response = $Call.response
    $content = if ($null -eq $response.content) { '' } else { [string]$response.content }
    return [ordered]@{
        sequence = $Sequence
        call = $Step
        elapsed_ms = $Call.elapsed_ms
        input_tokens = $response.usage.input_tokens
        output_tokens = $response.usage.output_tokens
        cached_tokens = $response.usage.cached_tokens
        num_turns = $response.num_turns
        total_cost_usd = $response.total_cost_usd
        finish_reason = $response.finish_reason
        content_length = $content.Length
        content_sha256_12 = Get-ShortHash $content
        invocation_metadata = @($Call.invocations | Where-Object { $_.mode -ne 'version-check' })
    }
}

function Sum-Field($Calls, [string]$Name) {
    $values = @($Calls | ForEach-Object { $_[$Name] } | Where-Object { $null -ne $_ })
    if ($values.Count -ne $Calls.Count) { return $null }
    return ($values | Measure-Object -Sum).Sum
}

$distribution = [IO.Path]::GetFullPath($DistributionPath)
$environmentFile = Join-Path $distribution '.env'
$gatewayExecutable = Join-Path $distribution 'llm-provider-server.exe'
$approvedTempRoot = 'C:\Users\admin\AppData\Local\Temp\opencode'
foreach ($requiredPath in @($environmentFile, $gatewayExecutable, $approvedTempRoot)) {
    if (!(Test-Path -LiteralPath $requiredPath)) { throw "Required benchmark path is missing: $requiredPath" }
}
$configured = Read-EnvironmentFile $environmentFile
$claudeExecutable = Resolve-ConfiguredPath $distribution ([string]$configured['LLM_SERVER_CLAUDE_EXECUTABLE'])
$claudeAuthFile = Resolve-ConfiguredPath $distribution ([string]$configured['LLM_SERVER_CLAUDE_AUTH_FILE'])
if (!(Test-Path -LiteralPath $claudeExecutable -PathType Leaf) -or !(Test-Path -LiteralPath $claudeAuthFile -PathType Leaf)) {
    throw 'Released Claude executable or configured local-login file is unavailable.'
}
$providerTimeout = [string]$configured['LLM_SERVER_PROVIDER_TIMEOUT']
if ([string]::IsNullOrWhiteSpace($providerTimeout)) { $providerTimeout = '180s' }
$timeoutMatch = [regex]::Match($providerTimeout, '^(\d+)(ms|s|m|h)$')
if (!$timeoutMatch.Success) { throw 'Configured Provider timeout is not a bounded simple duration.' }
$timeoutNumber = [int]$timeoutMatch.Groups[1].Value
$timeoutSeconds = switch ($timeoutMatch.Groups[2].Value) { 'ms' { [Math]::Ceiling($timeoutNumber / 1000) } 's' { $timeoutNumber } 'm' { $timeoutNumber * 60 } 'h' { $timeoutNumber * 3600 } }

$sourceStates = [ordered]@{
    env = Get-FileState $environmentFile
    provider_db = Get-FileState (Join-Path $distribution 'data/llm-provider.db')
    login = Get-FileState $claudeAuthFile
}
$runRoot = Join-Path $approvedTempRoot ('stock-ai-claude-usage-' + [Guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $runRoot
$gateway = $null
$client = $null
$attempted = 0

try {
    $port = Get-FreeLoopbackPort
    $adminToken = 'admin-' + [Guid]::NewGuid().ToString('N')
    $runtimeToken = 'runtime-' + [Guid]::NewGuid().ToString('N')
    $masterKey = [Convert]::ToBase64String([Security.Cryptography.RandomNumberGenerator]::GetBytes(32))
    $processInfo = [Diagnostics.ProcessStartInfo]::new()
    $processInfo.FileName = $gatewayExecutable
    $processInfo.WorkingDirectory = $runRoot
    $processInfo.UseShellExecute = $false
    $processInfo.CreateNoWindow = $true
    $processInfo.RedirectStandardOutput = $true
    $processInfo.RedirectStandardError = $true
    $processInfo.Environment.Clear()
    foreach ($name in @('SystemRoot', 'WINDIR', 'HTTPS_PROXY', 'HTTP_PROXY', 'NO_PROXY', 'SSL_CERT_FILE', 'SSL_CERT_DIR')) {
        $value = [Environment]::GetEnvironmentVariable($name)
        if ($null -ne $value) { $processInfo.Environment[$name] = $value }
    }
    $processInfo.Environment['TEMP'] = $runRoot
    $processInfo.Environment['TMP'] = $runRoot
    $processInfo.Environment['LLM_SERVER_ADDR'] = "127.0.0.1:$port"
    $processInfo.Environment['LLM_SERVER_DB_PATH'] = (Join-Path $runRoot 'benchmark.db')
    $processInfo.Environment['LLM_SERVER_ADMIN_TOKEN'] = $adminToken
    $processInfo.Environment['LLM_SERVER_RUNTIME_TOKEN'] = $runtimeToken
    $processInfo.Environment['LLM_SERVER_MASTER_KEY'] = $masterKey
    $processInfo.Environment['LLM_SERVER_PROVIDER_TIMEOUT'] = $providerTimeout
    $processInfo.Environment['LLM_SERVER_HARNESS_SESSION_TTL'] = '10m'
    $processInfo.Environment['LLM_SERVER_MODEL_CATALOG'] = '[]'
    $processInfo.Environment['LLM_SERVER_CLAUDE_EXECUTABLE'] = $claudeExecutable
    $processInfo.Environment['LLM_SERVER_CLAUDE_AUTH_FILE'] = $claudeAuthFile
    $gateway = [Diagnostics.Process]::Start($processInfo)
    $baseURL = "http://127.0.0.1:$port"
    $client = [Net.Http.HttpClient]::new()
    $client.Timeout = [TimeSpan]::FromSeconds($timeoutSeconds + 5)
    $readyDeadline = [DateTime]::UtcNow.AddSeconds(15)
    $ready = $false
    while (!$ready -and [DateTime]::UtcNow -lt $readyDeadline) {
        if ($gateway.HasExited) { throw 'Released Gateway exited during isolated startup.' }
        try {
            $health = $client.GetAsync($baseURL + '/healthz').GetAwaiter().GetResult()
            $ready = $health.IsSuccessStatusCode
            $health.Dispose()
        } catch { Start-Sleep -Milliseconds 100 }
    }
    if (!$ready) { throw 'Released Gateway did not become healthy within 15 seconds.' }

    $provider = [ordered]@{ id = 'claude-benchmark'; name = 'Claude benchmark'; type = 'claude_code'; base_url = ''; default_model = $Model; config = [ordered]@{ auth_mode = 'local_login' } }
    $null = Send-JSON $client ($baseURL + '/v1/providers') $adminToken $provider $gateway.Id
    $harnessRequest = [Net.Http.HttpRequestMessage]::new([Net.Http.HttpMethod]::Get, $baseURL + '/v1/harnesses')
    $harnessRequest.Headers.Authorization = [Net.Http.Headers.AuthenticationHeaderValue]::new('Bearer', $adminToken)
    $harnessResponse = $client.SendAsync($harnessRequest).GetAwaiter().GetResult()
    $harnessBody = $harnessResponse.Content.ReadAsStringAsync().GetAwaiter().GetResult() | ConvertFrom-Json
    $claudeHarness = @($harnessBody.harnesses | Where-Object { $_.type -eq 'claude_code' })[0]
    $harnessRequest.Dispose()
    $harnessResponse.Dispose()
    if (!$claudeHarness.executable_configured -or !$claudeHarness.local_login_configured) { throw 'Isolated Gateway did not accept the released Claude executable and login configuration.' }

    $tools = @([ordered]@{
        name = 'benchmark.echo'
        description = 'Return the supplied benchmark value unchanged.'
        input_schema = [ordered]@{ type = 'object'; properties = [ordered]@{ value = [ordered]@{ type = 'string' } }; required = @('value'); additionalProperties = $false }
    })
    $calls = [Collections.Generic.List[object]]::new()
    :sequenceLoop foreach ($sequence in @('cold', 'managed')) {
        $conversationID = if ($sequence -eq 'managed') { 'usage-benchmark/' + [Guid]::NewGuid().ToString('N') } else { $null }
        foreach ($step in 1..3) {
            if ($attempted -ge $MaximumGenerateCalls) { break sequenceLoop }
            $body = [ordered]@{
                provider = 'claude-benchmark'
                model = $Model
                messages = Get-BenchmarkMessages $step
                tools = $tools
                options = [ordered]@{ reasoning_effort = $Effort }
            }
            if ($conversationID) { $body['conversation_id'] = $conversationID }
            $attempted++
            $result = Send-JSON $client ($baseURL + '/v1/generate') $runtimeToken $body $gateway.Id -TrackInvocation
            $calls.Add((Summarize-Call $sequence $step $result))
        }
    }

    $coldCalls = @($calls | Where-Object { $_.sequence -eq 'cold' })
    $managedCalls = @($calls | Where-Object { $_.sequence -eq 'managed' })
    $managedModes = @($managedCalls | ForEach-Object { $_.invocation_metadata } | ForEach-Object { $_.mode })
    $managedHashes = @($managedCalls | ForEach-Object { $_.invocation_metadata } | ForEach-Object { $_.session_hash } | Where-Object { $null -ne $_ } | Select-Object -Unique)
    $resumeConfirmed = $managedCalls.Count -ge 2 -and $managedCalls[0].invocation_metadata.mode -contains 'session-id' -and
        @($managedCalls[1..($managedCalls.Count - 1)] | Where-Object { $_.invocation_metadata.mode -notcontains 'resume' }).Count -eq 0 -and
        @($managedModes | Where-Object { $_ -eq 'session-id' }).Count -eq 1 -and $managedHashes.Count -eq 1
    $report = [ordered]@{
        benchmark = 'released-gateway-claude-usage'
        released_gateway_sha256 = (Get-FileHash -LiteralPath $gatewayExecutable -Algorithm SHA256).Hash
        released_claude_sha256 = (Get-FileHash -LiteralPath $claudeExecutable -Algorithm SHA256).Hash
        harness_version = $claudeHarness.version
        model = $Model
        effort = $Effort
        provider_timeout = $providerTimeout
        requested_call_bound = $MaximumGenerateCalls
        real_calls_attempted = $attempted
        real_calls_completed = $calls.Count
        complete_two_by_three_design = $coldCalls.Count -eq 3 -and $managedCalls.Count -eq 3
        resume_confirmed = $resumeConfirmed
        calls = @($calls)
        totals = [ordered]@{
            cold = [ordered]@{
                elapsed_ms = Sum-Field $coldCalls 'elapsed_ms'
                input_tokens = Sum-Field $coldCalls 'input_tokens'
                output_tokens = Sum-Field $coldCalls 'output_tokens'
                cached_tokens = Sum-Field $coldCalls 'cached_tokens'
                num_turns_raw_sum = Sum-Field $coldCalls 'num_turns'
                total_cost_usd = Sum-Field $coldCalls 'total_cost_usd'
                cost_semantics = 'sum of independent cold CLI invocations'
            }
            managed = [ordered]@{
                elapsed_ms = Sum-Field $managedCalls 'elapsed_ms'
                input_tokens = Sum-Field $managedCalls 'input_tokens'
                output_tokens = Sum-Field $managedCalls 'output_tokens'
                cached_tokens = Sum-Field $managedCalls 'cached_tokens'
                num_turns_raw_sum = Sum-Field $managedCalls 'num_turns'
                total_cost_usd_raw_sum = $null
                last_reported_total_cost_usd = if ($managedCalls.Count) { $managedCalls[-1].total_cost_usd } else { $null }
                cost_semantics = 'not summed: Claude CLI may report resumed-session cumulative cost'
            }
        }
        caveats = @(
            'Cold calls ran before managed calls, so shared provider-side cache warmth was not randomized.',
            'Token and turn totals are sums of the raw fields returned per HTTP call; resumed CLI fields may themselves be cumulative.',
            'This small synthetic benchmark is not comparable to an earlier 140K-token workload.'
        )
        source_artifacts_unchanged = [ordered]@{
            env = Test-FileState $environmentFile $sourceStates.env
            provider_db = Test-FileState (Join-Path $distribution 'data/llm-provider.db') $sourceStates.provider_db
            login = Test-FileState $claudeAuthFile $sourceStates.login
        }
    }
    $report | ConvertTo-Json -Depth 20
} catch {
    [ordered]@{
        benchmark = 'released-gateway-claude-usage'
        real_calls_attempted = $attempted
        completed = $false
        sanitized_error = $_.Exception.Message
    } | ConvertTo-Json -Depth 5
    exit 1
} finally {
    if ($client) { $client.Dispose() }
    if ($gateway -and !$gateway.HasExited) {
        $gateway.Kill($true)
        $null = $gateway.WaitForExit(5000)
    }
    if ($gateway) { $gateway.Dispose() }
    if (Test-Path -LiteralPath $runRoot) { Remove-Item -LiteralPath $runRoot -Recurse -Force }
}
