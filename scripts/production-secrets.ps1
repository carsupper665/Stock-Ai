param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('Generate', 'Validate', 'Scan')]
    [string]$Command,
    [Parameter(Mandatory = $true)]
    [ValidateSet('Test', 'Release')]
    [string]$Environment,
    [Parameter(Mandatory = $true)]
    [string]$Directory,
    [string]$OtherDirectory = '',
    [string]$BackendDatabase = '',
    [string]$ProviderDatabase = '',
    [string[]]$Artifact = @()
)

$ErrorActionPreference = 'Stop'
$tool = Join-Path $PSScriptRoot 'production_secrets.py'
$python = Get-Command 'py.exe' -ErrorAction SilentlyContinue
if ($python) {
    $pythonArgs = @('-3', $tool)
} else {
    $python = Get-Command 'python.exe' -ErrorAction Stop
    $pythonArgs = @($tool)
}

$arguments = @($Command.ToLowerInvariant(), '--environment', $Environment, '--directory', $Directory)

function Assert-RestrictedAcl([string]$Root) {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $allowed = @($identity.User.Value, 'S-1-5-18', 'S-1-5-32-544')
    $manifestPath = Join-Path $Root 'production-config.json'
    if (!(Test-Path -LiteralPath $manifestPath -PathType Leaf)) {
        throw "ACL validation failed: missing production-config.json at $Root"
    }
    $manifest = Get-Content -LiteralPath $manifestPath -Raw | ConvertFrom-Json
    $paths = @($Root, $manifestPath)
    foreach ($property in $manifest.secret_files.PSObject.Properties) {
        $paths += Join-Path $Root ([string]$property.Value)
    }
    foreach ($path in $paths) {
        $acl = Get-Acl -LiteralPath $path
        foreach ($entry in $acl.Access) {
            $sid = $entry.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value
            if ($entry.IsInherited -or $entry.AccessControlType -ne 'Allow' -or $sid -notin $allowed) {
                throw "ACL validation failed for $path; inherited or unauthorized access is present."
            }
        }
    }
    Write-Host "ACL valid; source=$Root"
}
switch ($Command) {
    'Generate' {
        if ([string]::IsNullOrWhiteSpace($BackendDatabase) -or [string]::IsNullOrWhiteSpace($ProviderDatabase)) {
            throw 'Generate requires -BackendDatabase and -ProviderDatabase.'
        }
        $arguments += @('--backend-database', $BackendDatabase, '--provider-database', $ProviderDatabase)
    }
    'Validate' {
        if (![string]::IsNullOrWhiteSpace($OtherDirectory)) {
            $arguments += @('--other-directory', $OtherDirectory)
        }
    }
    'Scan' {
        if ($Artifact.Count -eq 0) { throw 'Scan requires at least one -Artifact path.' }
        foreach ($path in $Artifact) { $arguments += @('--artifact', $path) }
    }
}

& $python.Source @pythonArgs @arguments
if ($LASTEXITCODE -ne 0) {
    throw "$Command failed. No service was started and no secret value was printed."
}
Assert-RestrictedAcl -Root $Directory
