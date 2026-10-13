[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)] [string] $AppHost,
    [Parameter(Mandatory = $true)] [string] $Issuer,
    [Parameter(Mandatory = $true)] [string] $EvidencePath,
    [Parameter(Mandatory = $true)] [switch] $FullPortScan
)

$ErrorActionPreference = 'Stop'
$issuerUri = [Uri] $Issuer
if ($issuerUri.Scheme -ne 'https' -or $issuerUri.AbsolutePath -ne '/realms/stock-ai-test' -or $issuerUri.Query -or $issuerUri.Fragment) {
    throw 'Issuer must be the exact HTTPS Test realm URL.'
}
if ($AppHost -notmatch '^[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?$' -or $AppHost -match '\.\.|localhost|127\.0\.0\.1|\[::1\]') {
    throw 'AppHost must be a stable non-local DNS hostname.'
}
if (-not $FullPortScan) { throw 'Run the independent external probe with -FullPortScan.' }

$checks = [System.Collections.Generic.List[object]]::new()
function Add-Check([string] $Name, [bool] $Passed, [object] $Details) {
    $checks.Add([ordered]@{ name = $Name; passed = $Passed; details = $Details })
}
function Get-HttpResponse([string] $Uri, [hashtable] $Headers = @{}) {
    Invoke-WebRequest -Uri $Uri -Headers $Headers -MaximumRedirection 0 -SkipHttpErrorCheck -TimeoutSec 15
}
function Test-Tcp([string] $HostName, [int] $Port) {
    $client = [System.Net.Sockets.TcpClient]::new()
    try {
        $task = $client.ConnectAsync($HostName, $Port)
        return $task.Wait(3000) -and $client.Connected
    }
    catch { return $false }
    finally { $client.Dispose() }
}
function Get-OpenTcpPorts([string] $HostName, [bool] $IPv6) {
    $arguments = @('-Pn', '-p-', '--open', '-oX', '-')
    if ($IPv6) { $arguments = @('-6') + $arguments }
    $arguments += $HostName
    $xmlText = (& nmap @arguments) -join "`n"
    if ($LASTEXITCODE -ne 0) { throw 'nmap full TCP scan failed.' }
    $document = [xml] $xmlText
    return @($document.nmaprun.host.ports.port | Where-Object { $_.state.state -eq 'open' } | ForEach-Object { [int] $_.portid } | Sort-Object -Unique)
}

$appAddresses = @()
$idpAddresses = @()
$probeFailed = $false
$failedStage = $null
try {
    $stage = 'Test app and IdP DNS'
    $appIPv4 = @(Resolve-DnsName -Name $AppHost -Type A -ErrorAction SilentlyContinue | ForEach-Object { [string] $_.IPAddress } | Where-Object { $_ } | Sort-Object -Unique)
    $appIPv6 = @(Resolve-DnsName -Name $AppHost -Type AAAA -ErrorAction SilentlyContinue | ForEach-Object { [string] $_.IPAddress } | Where-Object { $_ } | Sort-Object -Unique)
    $appAddresses = @($appIPv4 + $appIPv6)
    if ($appAddresses.Count -eq 0) { throw 'No Test app A or AAAA records were returned.' }
    $idpIPv4 = @(Resolve-DnsName -Name $issuerUri.Host -Type A -ErrorAction SilentlyContinue | ForEach-Object { [string] $_.IPAddress } | Where-Object { $_ } | Sort-Object -Unique)
    $idpIPv6 = @(Resolve-DnsName -Name $issuerUri.Host -Type AAAA -ErrorAction SilentlyContinue | ForEach-Object { [string] $_.IPAddress } | Where-Object { $_ } | Sort-Object -Unique)
    $idpAddresses = @($idpIPv4 + $idpIPv6)
    if ($idpAddresses.Count -eq 0) { throw 'No Test IdP A or AAAA records were returned.' }
    Add-Check 'app DNS A and AAAA records' $true $appAddresses
    Add-Check 'IdP DNS A and AAAA records' $true $idpAddresses

    $stage = 'HTTP redirect and Test app TLS'
$http = Get-HttpResponse "http://$AppHost/"
$httpLocation = [Uri]::new([Uri] "http://$AppHost/", [string] $http.Headers.Location)
Add-Check 'HTTP redirects to HTTPS' ($http.StatusCode -eq 301 -and $httpLocation.Scheme -eq 'https' -and $httpLocation.Host -eq $AppHost) ([ordered]@{ status = $http.StatusCode; location_host = $httpLocation.Host; location_scheme = $httpLocation.Scheme })

$app = Get-HttpResponse "https://$AppHost/"
Add-Check 'HTTPS root requires login' ($app.StatusCode -eq 302 -and [string] $app.Headers.Location -eq '/oauth2/start') ([ordered]@{ status = $app.StatusCode; location = [string] $app.Headers.Location; tls_chain_and_hostname_validated = $true })
$health = Get-HttpResponse "https://$AppHost/healthz"
Add-Check 'public edge health route' ($health.StatusCode -eq 200 -and [string] $health.Content -match '^ok') ([ordered]@{ status = $health.StatusCode })
$asset = Get-HttpResponse "https://$AppHost/assets/app.js"
Add-Check 'anonymous asset route does not require OIDC' ($asset.StatusCode -notin @(302, 401)) ([ordered]@{ status = $asset.StatusCode })

foreach ($path in @('/v1/health', '/agent/health', '/llm/healthz')) {
    $response = Get-HttpResponse "https://$AppHost$path"
    Add-Check "anonymous API route $path returns 401" ($response.StatusCode -eq 401) ([ordered]@{ status = $response.StatusCode })
}

$accountHeaders = @{ Authorization = 'Bearer deliberately-invalid-test-account-token' }
$account = Get-HttpResponse "https://$AppHost/v1/account" $accountHeaders
$accountReachedConsole = $account.StatusCode -eq 401 -and [string] $account.Headers['Content-Type'] -like 'application/json*'
Add-Check 'Account bearer reaches Console Account validation' $accountReachedConsole ([ordered]@{ status = $account.StatusCode; content_type = [string] $account.Headers['Content-Type']; token_recorded = $false })

foreach ($path in @('/agent/api/v1/sessions', '/llm/v1/providers')) {
    $response = Get-HttpResponse "https://$AppHost$path" $accountHeaders
    Add-Check "Account bearer is rejected at edge for $path" ($response.StatusCode -eq 403) ([ordered]@{ status = $response.StatusCode; token_recorded = $false })
}

$spoofHeaders = @{
    'X-Auth-Request-Subject' = 'spoofed-subject'
    'X-Auth-Request-User' = 'spoofed-user'
    'X-Auth-Request-Email' = 'spoofed@example.invalid'
    'X-Auth-Request-Access-Token' = 'deliberately-invalid'
    'X-Forwarded-For' = '192.0.2.123'
    'X-Forwarded-Proto' = 'http'
    'X-Forwarded-Host' = 'attacker.invalid'
    Forwarded = 'for=192.0.2.123;proto=http;host=attacker.invalid'
}
$spoof = Get-HttpResponse "https://$AppHost/agent/api/v1/sessions" $spoofHeaders
Add-Check 'spoofed identity and forwarding headers do not authenticate' ($spoof.StatusCode -eq 401) ([ordered]@{ status = $spoof.StatusCode; spoof_values_recorded = $false })
$authRoute = Get-HttpResponse "https://$AppHost/oauth2/auth"
Add-Check 'Gateway auth subrequest endpoint is private' ($authRoute.StatusCode -eq 404) ([ordered]@{ status = $authRoute.StatusCode })
$internalRoute = Get-HttpResponse "https://$AppHost/_oauth2_auth"
Add-Check 'Nginx internal auth location is private' ($internalRoute.StatusCode -eq 404) ([ordered]@{ status = $internalRoute.StatusCode })
$requiredHeaders = @{
    'Strict-Transport-Security' = 'max-age=31536000'
    'Content-Security-Policy' = "default-src 'self'"
    'X-Content-Type-Options' = 'nosniff'
    'Referrer-Policy' = 'same-origin'
    'X-Frame-Options' = 'DENY'
}
foreach ($name in $requiredHeaders.Keys) {
    $value = [string] $app.Headers[$name]
    Add-Check "header $name" ($value -like "*$($requiredHeaders[$name])*" ) $value
}

$stage = 'Test OIDC client redirect and cookie contract'
$start = Get-HttpResponse "https://$AppHost/oauth2/start"
$auth = [Uri] [string] $start.Headers.Location
$query = [System.Web.HttpUtility]::ParseQueryString($auth.Query)
$redirect = [Uri] [string] $query['redirect_uri']
$expectedRedirect = "https://$AppHost/oauth2/callback"
$startOK = $start.StatusCode -eq 302 -and $auth.Scheme -eq 'https' -and $auth.Host -eq $issuerUri.Host -and $auth.AbsolutePath.StartsWith($issuerUri.AbsolutePath + '/') -and $query['client_id'] -eq 'stock-ai-test' -and $query['state'] -and $query['nonce'] -and $redirect.AbsoluteUri -eq $expectedRedirect
Add-Check 'OIDC authorization redirect contract' $startOK ([ordered]@{ status = $start.StatusCode; authorization_host = $auth.Host; authorization_path = $auth.AbsolutePath; client_id = $query['client_id']; redirect_uri = $redirect.AbsoluteUri; state_present = [bool] $query['state']; nonce_present = [bool] $query['nonce'] })
$transactionCookie = [string] $start.Headers['Set-Cookie']
$cookieOK = $transactionCookie -match '__Secure-stock-ai-test-login=' -and $transactionCookie -match '(?i)Secure' -and $transactionCookie -match '(?i)HttpOnly' -and $transactionCookie -match '(?i)SameSite=Lax' -and $transactionCookie -match '(?i)Path=/oauth2/callback' -and $transactionCookie -notmatch '(?i)Domain='
Add-Check 'OIDC transaction cookie scope and flags' $cookieOK ([ordered]@{ secure = $transactionCookie -match '(?i)Secure'; http_only = $transactionCookie -match '(?i)HttpOnly'; same_site_lax = $transactionCookie -match '(?i)SameSite=Lax'; callback_path = $transactionCookie -match '(?i)Path=/oauth2/callback'; domain_attribute_absent = $transactionCookie -notmatch '(?i)Domain=' })
$loginStatuses = @()
for ($attempt = 0; $attempt -lt 12; $attempt++) {
    $loginStatuses += (Get-HttpResponse "https://$AppHost/oauth2/start").StatusCode
}
Add-Check 'login route rate limit enforced' ($loginStatuses -contains 429) ([ordered]@{ status_codes = $loginStatuses; rate_limited = ($loginStatuses -contains 429) })
$oversize = [byte[]]::new((1MB) + 1)
$bodyLimit = Invoke-WebRequest -Uri "https://$AppHost/oauth2/logout" -Method Post -Body $oversize -ContentType 'application/octet-stream' -MaximumRedirection 0 -SkipHttpErrorCheck -TimeoutSec 15
Add-Check 'request body size limit enforced' ($bodyLimit.StatusCode -eq 413) ([ordered]@{ status = $bodyLimit.StatusCode; request_bytes = $oversize.Length })

$stage = 'Test issuer discovery and JWKS HTTPS'
$discovery = Invoke-RestMethod -Uri ($Issuer.TrimEnd('/') + '/.well-known/openid-configuration') -TimeoutSec 15
$jwksUri = [Uri] [string] $discovery.jwks_uri
$jwksOK = $discovery.issuer -ceq $Issuer.TrimEnd('/') -and $jwksUri.Scheme -eq 'https' -and $jwksUri.Host -eq $issuerUri.Host -and $jwksUri.AbsolutePath.StartsWith($issuerUri.AbsolutePath + '/')
if (-not $jwksOK) { throw 'Test IdP discovery returned a mismatched issuer or JWKS URL.' }
$jwks = Invoke-RestMethod -Uri $jwksUri.AbsoluteUri -TimeoutSec 15
Add-Check 'Test issuer and JWKS reachable with trusted HTTPS' (@($jwks.keys).Count -gt 0) ([ordered]@{ issuer = $discovery.issuer; jwks_host = $jwksUri.Host; jwks_key_count = @($jwks.keys).Count; tls_chain_and_hostname_validated = $true })

$stage = 'Test host external TCP ports'
foreach ($port in @(80, 443)) {
    $open = Test-Tcp $AppHost $port
    Add-Check "public edge port $port reachable" $open ([ordered]@{ host = $AppHost; port = $port; reachable = $open })
}
foreach ($port in @(4180, 8088, 7794, 8080, 8090)) {
    $open = Test-Tcp $AppHost $port
    Add-Check "private port $port closed" (-not $open) ([ordered]@{ host = $AppHost; port = $port; reachable = $open })
}
$stage = 'Nmap full IPv4/IPv6 TCP scan'
if (-not (Get-Command nmap -ErrorAction SilentlyContinue)) { throw 'nmap is required for the external full TCP port check.' }
    $openPorts = @()
    if ($appIPv4.Count -gt 0) { $openPorts += @(Get-OpenTcpPorts -HostName $AppHost -IPv6 $false) }
    if ($appIPv6.Count -gt 0) { $openPorts += @(Get-OpenTcpPorts -HostName $AppHost -IPv6 $true) }
    $openPorts = @($openPorts | Sort-Object -Unique)
$expectedPorts = @(80, 443)
$unexpectedPorts = @($openPorts | Where-Object { $_ -notin $expectedPorts })
$missingPorts = @($expectedPorts | Where-Object { $_ -notin $openPorts })
    Add-Check 'full TCP scan finds only intended edge ports' ($unexpectedPorts.Count -eq 0 -and $missingPorts.Count -eq 0) ([ordered]@{ open_tcp_ports = $openPorts; scanned_families = @($(if ($appIPv4.Count -gt 0) { 'IPv4' }; if ($appIPv6.Count -gt 0) { 'IPv6' })); expected = $expectedPorts })
}
catch {
    $probeFailed = $true
    $failedStage = $stage
    $checks.Add([ordered]@{ name = 'probe execution'; passed = $false; details = 'A required operation failed; the stage identifies the unavailable Test dependency without retaining response content.' })
}

$report = [ordered]@{
    schema = 'stock-ai.test-edge-check.v1'
    checked_at_utc = [DateTime]::UtcNow.ToString('o')
    vantage_host = [Environment]::MachineName
    app_host = $AppHost
    issuer = $Issuer.TrimEnd('/')
    app_dns_a = $appAddresses
    issuer_dns_a = $idpAddresses
    failed_stage = $failedStage
    checks = @($checks)
    passed = (-not $probeFailed -and @($checks | Where-Object { -not $_.passed }).Count -eq 0)
}
$parent = Split-Path -Parent $EvidencePath
if (-not (Test-Path -LiteralPath $parent)) { New-Item -ItemType Directory -Path $parent | Out-Null }
$report | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $EvidencePath -Encoding utf8
if (-not $report.passed) { throw "One or more Test edge checks failed; sanitized evidence saved to $EvidencePath" }
Write-Output "Test edge checks passed; sanitized evidence saved to $EvidencePath"
