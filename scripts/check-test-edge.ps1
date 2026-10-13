[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)] [string] $AppHost,
    [Parameter(Mandatory = $true)] [string] $Issuer,
    [Parameter(Mandatory = $true)] [string] $EvidencePath,
    [Parameter(Mandatory = $true)] [switch] $FullPortScan
)

$ErrorActionPreference = 'Stop'
function Test-StableDnsHostname([string] $Name) {
    if ([string]::IsNullOrWhiteSpace($Name)) { return $false }
    $normalized = $Name.ToLowerInvariant()
    if ($normalized.EndsWith('..')) { return $false }
    if ($normalized.EndsWith('.')) { $normalized = $normalized.Substring(0, $normalized.Length - 1) }
    $parsedAddress = $null
    if ([System.Net.IPAddress]::TryParse($normalized, [ref] $parsedAddress)) { return $false }
    if ($normalized -eq 'localhost' -or $normalized -eq 'loopback' -or $normalized.EndsWith('.localhost') -or $normalized.EndsWith('.loopback') -or $normalized.EndsWith('.local')) { return $false }
    $labels = $normalized.Split('.')
    if ($labels.Count -lt 2 -or $normalized.Length -gt 253) { return $false }
    foreach ($label in $labels) {
        if ($label.Length -gt 63 -or $label -notmatch '^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$') { return $false }
    }
    return $true
}

$issuerUri = [Uri] $Issuer
$issuerHost = $issuerUri.DnsSafeHost
$issuerUriPort = if ($issuerUri.IsDefaultPort) { 443 } else { $issuerUri.Port }
if ($issuerUri.Scheme -ne 'https' -or $issuerUriPort -ne 443 -or $issuerUri.UserInfo -or -not (Test-StableDnsHostname $issuerHost) -or $issuerUri.AbsolutePath -ne '/realms/stock-ai-test' -or $issuerUri.Query -or $issuerUri.Fragment) {
    throw 'Issuer must be the exact HTTPS Test realm URL.'
}
if (-not (Test-StableDnsHostname $AppHost)) {
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
function Test-Tcp([string] $Address, [int] $Port) {
    $client = [System.Net.Sockets.TcpClient]::new()
    try {
        $task = $client.ConnectAsync($Address, $Port)
        return $task.Wait(3000) -and $client.Connected
    }
    catch { return $false }
    finally { $client.Dispose() }
}
function Get-OpenTcpPorts([string] $Address, [bool] $IPv6) {
    $arguments = @('-Pn', '-p-', '--open', '-oX', '-')
    if ($IPv6) { $arguments = @('-6') + $arguments }
    $arguments += $Address
    $xmlText = (& nmap @arguments) -join "`n"
    if ($LASTEXITCODE -ne 0) { throw 'nmap full TCP scan failed.' }
    $document = [xml] $xmlText
    $ports = @($document.nmaprun.host.ports.port | Where-Object { $_.state.state -eq 'open' } | ForEach-Object { [int] $_.portid } | Sort-Object -Unique)
    return [ordered]@{ address = $Address; open_tcp_ports = $ports }
}
function Get-TlsMetadata([string] $HostName, [string] $Address, [int] $Port) {
    $client = [System.Net.Sockets.TcpClient]::new()
    $stream = $null
    $certificate = $null
    try {
        $client.Connect($Address, $Port)
        $stream = [System.Net.Security.SslStream]::new($client.GetStream(), $false)
        $stream.AuthenticateAsClient($HostName)
        if (-not $stream.RemoteCertificate) { throw 'TLS peer did not provide a certificate.' }
        $certificate = [System.Security.Cryptography.X509Certificates.X509Certificate2]::new($stream.RemoteCertificate)
        return [ordered]@{
            address = $Address
            port = $Port
            validated_hostname = $HostName
            hostname_validation = 'passed by SslStream system validation'
            chain_validation = 'passed by SslStream system trust'
            certificate_issuer = $certificate.Issuer
            not_after_utc = $certificate.NotAfter.ToUniversalTime().ToString('o')
        }
    }
    finally {
        if ($certificate) { $certificate.Dispose() }
        if ($stream) { $stream.Dispose() }
        $client.Dispose()
    }
}

$appAddresses = @()
$idpAddresses = @()
$appTlsCertificates = @()
$idpTlsCertificates = @()
$appIPv4 = @()
$appIPv6 = @()
$idpIPv4 = @()
$idpIPv6 = @()
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

    $stage = 'Trusted app and IdP TLS certificate checks'
    foreach ($address in $appAddresses) {
        $appTlsCertificates += Get-TlsMetadata -HostName $AppHost -Address $address -Port 443
    }
    foreach ($address in $idpAddresses) {
        $idpTlsCertificates += Get-TlsMetadata -HostName $issuerUri.Host -Address $address -Port 443
    }
    Add-Check 'app TLS certificate trust and hostname' ($appTlsCertificates.Count -eq $appAddresses.Count) $appTlsCertificates
    Add-Check 'IdP TLS certificate trust and hostname' ($idpTlsCertificates.Count -eq $idpAddresses.Count) $idpTlsCertificates

    $stage = 'HTTP redirect and Test app TLS'
$http = Get-HttpResponse "http://$AppHost/"
$httpLocation = [Uri]::new([Uri] "http://$AppHost/", [string] $http.Headers.Location)
Add-Check 'HTTP redirects to HTTPS' ($http.StatusCode -eq 301 -and $httpLocation.Scheme -eq 'https' -and $httpLocation.Host -eq $AppHost) ([ordered]@{ status = $http.StatusCode; location_host = $httpLocation.Host; location_scheme = $httpLocation.Scheme })

$app = Get-HttpResponse "https://$AppHost/"
Add-Check 'HTTPS root requires login' ($app.StatusCode -eq 302 -and [string] $app.Headers.Location -eq '/oauth2/start') ([ordered]@{ status = $app.StatusCode; location = [string] $app.Headers.Location; tls_chain_and_hostname_validated = $true })
$badHost = Get-HttpResponse "https://$AppHost/" @{ Host = 'attacker.invalid' }
Add-Check 'unrecognized Host header is rejected' ($badHost.StatusCode -eq 421) ([ordered]@{ status = $badHost.StatusCode; supplied_host_recorded = $false })
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
foreach ($address in $appAddresses) {
    foreach ($port in @(80, 443)) {
        $open = Test-Tcp -Address $address -Port $port
        Add-Check "public edge port $port reachable at resolved address" $open ([ordered]@{ address = $address; port = $port; reachable = $open })
    }
    foreach ($port in @(4180, 8088, 7794, 8080, 8090)) {
        $open = Test-Tcp -Address $address -Port $port
        Add-Check "private port $port closed at resolved address" (-not $open) ([ordered]@{ address = $address; port = $port; reachable = $open })
    }
}
$stage = 'Nmap full IPv4/IPv6 TCP scan'
if (-not (Get-Command nmap -ErrorAction SilentlyContinue)) { throw 'nmap is required for the external full TCP port check.' }
$portScans = @()
foreach ($address in $appAddresses) {
    $isIPv6 = $address.Contains(':')
    $portScans += Get-OpenTcpPorts -Address $address -IPv6 $isIPv6
}
$expectedPorts = @(80, 443)
$unexpectedPorts = @($portScans | ForEach-Object { $_.open_tcp_ports | Where-Object { $_ -notin $expectedPorts } })
$missingAddressScans = @($portScans | Where-Object { @($_.open_tcp_ports | Where-Object { $_ -in $expectedPorts }).Count -ne $expectedPorts.Count } | ForEach-Object { $_.address })
Add-Check 'full TCP scan finds only intended edge ports at every resolved address' ($unexpectedPorts.Count -eq 0 -and $missingAddressScans.Count -eq 0 -and $portScans.Count -eq $appAddresses.Count) ([ordered]@{ address_scans = $portScans; addresses_missing_expected_ports = $missingAddressScans; expected = $expectedPorts })
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
    app_dns = [ordered]@{ a = $appIPv4; aaaa = $appIPv6 }
    issuer_dns = [ordered]@{ a = $idpIPv4; aaaa = $idpIPv6 }
    app_tls_certificates = $appTlsCertificates
    issuer_tls_certificates = $idpTlsCertificates
    failed_stage = $failedStage
    checks = @($checks)
    passed = (-not $probeFailed -and @($checks | Where-Object { -not $_.passed }).Count -eq 0)
}
$parent = Split-Path -Parent $EvidencePath
if (-not (Test-Path -LiteralPath $parent)) { New-Item -ItemType Directory -Path $parent | Out-Null }
$report | ConvertTo-Json -Depth 8 | Set-Content -LiteralPath $EvidencePath -Encoding utf8
if (-not $report.passed) { throw "One or more Test edge checks failed; sanitized evidence saved to $EvidencePath" }
Write-Output "Test edge checks passed; sanitized evidence saved to $EvidencePath"
