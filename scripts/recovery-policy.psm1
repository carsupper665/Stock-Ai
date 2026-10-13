$script:RecoveryOwnershipHelperImage = 'golang:1.27.2-alpine3.23@sha256:9e45f0eb4a63ed37ad6e604950407558b7c738e34e015ae77a5d4ad369cde079'

function Assert-RecoveryVolumeScaffolding {
    param(
        [Parameter(Mandatory = $true)] [ValidateSet('backend', 'llm-provider-server', 'agent-server')] [string]$Service,
        [Parameter(Mandatory = $true)] [string]$Path
    )

    $allowedDirectories = switch ($Service) {
        'backend' { @('logs') }
        'agent-server' { @('history') }
        default { @() }
    }
    foreach ($entry in Get-ChildItem -LiteralPath $Path -Force -Recurse) {
        $relative = [IO.Path]::GetRelativePath($Path, $entry.FullName).Replace('\', '/')
        if (!$entry.PSIsContainer -or $entry.Attributes.HasFlag([IO.FileAttributes]::ReparsePoint) -or $relative -notin $allowedDirectories) {
            throw "Restore target for $Service contains non-empty or unknown data ($relative); refusing to overwrite user data."
        }
    }
}

function Set-RecoveryDirectoryAcl {
    param([Parameter(Mandatory = $true)] [string]$Path)

    $fullPath = [IO.Path]::GetFullPath($Path)
    if (!(Test-Path -LiteralPath $fullPath)) { New-Item -ItemType Directory -Path $fullPath -Force | Out-Null }
    $directory = Get-Item -LiteralPath $fullPath -Force
    if (!$directory.PSIsContainer -or $directory.Attributes.HasFlag([IO.FileAttributes]::ReparsePoint)) { throw 'Recovery path must be a real directory, not a file or reparse point.' }

    $expectedSids = @(
        [Security.Principal.WindowsIdentity]::GetCurrent().User.Value,
        'S-1-5-18',
        'S-1-5-32-544'
    ) | Sort-Object -Unique
    $acl = [Security.AccessControl.DirectorySecurity]::new()
    $acl.SetAccessRuleProtection($true, $false)
    $acl.SetOwner([Security.Principal.SecurityIdentifier]::new([Security.Principal.WindowsIdentity]::GetCurrent().User.Value))
    $inheritance = [Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [Security.AccessControl.InheritanceFlags]::ObjectInherit
    $rights = [Security.AccessControl.FileSystemRights]::FullControl
    foreach ($sidText in $expectedSids) {
        $sid = [Security.Principal.SecurityIdentifier]::new($sidText)
        $rule = [Security.AccessControl.FileSystemAccessRule]::new($sid, $rights, $inheritance, [Security.AccessControl.PropagationFlags]::None, [Security.AccessControl.AccessControlType]::Allow)
        $acl.AddAccessRule($rule)
    }
    Set-Acl -LiteralPath $fullPath -AclObject $acl
    Assert-RecoveryDirectoryAcl -Path $fullPath -ExpectedSids $expectedSids
}

function Assert-RecoveryDirectoryAcl {
    param(
        [Parameter(Mandatory = $true)] [string]$Path,
        [string[]]$ExpectedSids
    )

    if (!$ExpectedSids) {
        $ExpectedSids = @(
            [Security.Principal.WindowsIdentity]::GetCurrent().User.Value,
            'S-1-5-18',
            'S-1-5-32-544'
        )
    }
    $acl = Get-Acl -LiteralPath $Path
    if (!$acl.AreAccessRulesProtected) { throw 'Recovery directory ACL still inherits access rules.' }
    $ownerSid = $acl.GetOwner([Security.Principal.SecurityIdentifier]).Value
    if ($ownerSid -cne [Security.Principal.WindowsIdentity]::GetCurrent().User.Value) { throw 'Recovery directory owner must be the current operator.' }
    $actual = @($acl.Access)
    if ($actual.Count -ne $ExpectedSids.Count) { throw 'Recovery directory ACL contains unexpected entries.' }
    $expected = @($ExpectedSids | Sort-Object -Unique)
    foreach ($rule in $actual) {
        $sid = $rule.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value
        if ($sid -notin $expected -or $rule.AccessControlType -ne [Security.AccessControl.AccessControlType]::Allow -or $rule.FileSystemRights -ne [Security.AccessControl.FileSystemRights]::FullControl -or $rule.IsInherited) {
            throw 'Recovery directory ACL is not restricted to the operator, SYSTEM, and local Administrators.'
        }
    }
    $actualSids = @($actual | ForEach-Object { $_.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value } | Sort-Object -Unique)
    if (($actualSids -join ',') -cne ($expected -join ',')) {
        throw 'Recovery directory ACL does not contain the exact expected principals.'
    }
}

function Invoke-RecoveryVolumeOwnership {
    param(
        [Parameter(Mandatory = $true)] [ValidatePattern('^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$')] [string]$VolumeName,
        [Parameter(Mandatory = $true)] [scriptblock]$DockerInvoker
    )

    $mount = "type=volume,source=$VolumeName,target=/data"
    $chown = @('run', '--rm', '--network', 'none', '--read-only', '--cap-drop', 'ALL', '--cap-add', 'CHOWN', '--cap-add', 'DAC_OVERRIDE', '--security-opt', 'no-new-privileges:true', '--user', '0:0', '--mount', $mount, '--entrypoint', '/bin/sh', $script:RecoveryOwnershipHelperImage, '-ec', 'links=$(find /data -xdev -type l -print -quit) || exit 1; test -z "$links" && chown -R 65532:65532 /data')
    $verifyMount = "type=volume,source=$VolumeName,target=/data,readonly"
    $verify = @('run', '--rm', '--network', 'none', '--read-only', '--cap-drop', 'ALL', '--cap-add', 'DAC_OVERRIDE', '--security-opt', 'no-new-privileges:true', '--user', '0:0', '--mount', $verifyMount, '--entrypoint', '/bin/sh', $script:RecoveryOwnershipHelperImage, '-ec', 'bad=$(find /data -xdev \( ! -uid 65532 -o ! -gid 65532 \) -print -quit) || exit 1; test -z "$bad"')
    & $DockerInvoker -DockerArguments $chown
    & $DockerInvoker -DockerArguments $verify
}

Export-ModuleMember -Function Assert-RecoveryVolumeScaffolding, Set-RecoveryDirectoryAcl, Assert-RecoveryDirectoryAcl, Invoke-RecoveryVolumeOwnership
