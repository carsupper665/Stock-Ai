BeforeAll {
    Import-Module (Join-Path $PSScriptRoot '..\recovery-policy.psm1') -Force
}

Describe 'Recovery volume scaffolding checks' {
    BeforeEach {
        $fixture = Join-Path $TestDrive ([Guid]::NewGuid().ToString('N'))
        New-Item -ItemType Directory -Path $fixture | Out-Null
    }

    It 'allows only the image-owned empty backend logs directory' {
        New-Item -ItemType Directory -Path (Join-Path $fixture 'logs') | Out-Null
        { Assert-RecoveryVolumeScaffolding -Service backend -Path $fixture } | Should -Not -Throw
    }

    It 'allows only the image-owned empty Agent history directory' {
        New-Item -ItemType Directory -Path (Join-Path $fixture 'history') | Out-Null
        { Assert-RecoveryVolumeScaffolding -Service agent-server -Path $fixture } | Should -Not -Throw
    }

    It 'allows an entirely empty LLM Provider volume' {
        { Assert-RecoveryVolumeScaffolding -Service llm-provider-server -Path $fixture } | Should -Not -Throw
    }

    It 'rejects files in an otherwise allowed logs scaffold' {
        $logs = Join-Path $fixture 'logs'
        New-Item -ItemType Directory -Path $logs | Out-Null
        Set-Content -LiteralPath (Join-Path $logs 'backend.log') -Value 'existing data'
        { Assert-RecoveryVolumeScaffolding -Service backend -Path $fixture } | Should -Throw '*backend.log*'
    }

    It 'rejects database files, history files, and unknown directories' {
        foreach ($entry in @(
            @{ service = 'backend'; path = 'backend.db'; directory = $false },
            @{ service = 'agent-server'; path = 'history/run.json'; directory = $false },
            @{ service = 'backend'; path = 'unexpected'; directory = $true },
            @{ service = 'llm-provider-server'; path = 'logs'; directory = $true }
        )) {
            $root = Join-Path $TestDrive ([Guid]::NewGuid().ToString('N'))
            New-Item -ItemType Directory -Path $root | Out-Null
            $path = Join-Path $root $entry.path
            if ($entry.directory) { New-Item -ItemType Directory -Path $path -Force | Out-Null }
            else {
                New-Item -ItemType Directory -Path (Split-Path $path) -Force | Out-Null
                Set-Content -LiteralPath $path -Value 'existing data'
            }
            { Assert-RecoveryVolumeScaffolding -Service $entry.service -Path $root } | Should -Throw
        }
    }
}

Describe 'Recovery ACL and restored ownership policy' {
    It 'sets and verifies the protected recovery directory DACL before use' {
        $directory = Join-Path $TestDrive ([Guid]::NewGuid().ToString('N'))
        Set-RecoveryDirectoryAcl -Path $directory
        { Assert-RecoveryDirectoryAcl -Path $directory } | Should -Not -Throw
        $acl = Get-Acl -LiteralPath $directory
        $acl.AreAccessRulesProtected | Should -BeTrue
        $acl.GetOwner([Security.Principal.SecurityIdentifier]).Value | Should -Be ([Security.Principal.WindowsIdentity]::GetCurrent().User.Value)
    }

    It 'rejects an unexpected principal in the recovery directory DACL' {
        $directory = Join-Path $TestDrive ([Guid]::NewGuid().ToString('N'))
        Set-RecoveryDirectoryAcl -Path $directory
        $acl = Get-Acl -LiteralPath $directory
        $everyone = [Security.Principal.SecurityIdentifier]::new('S-1-1-0')
        $rule = [Security.AccessControl.FileSystemAccessRule]::new($everyone, [Security.AccessControl.FileSystemRights]::Read, [Security.AccessControl.AccessControlType]::Allow)
        $acl.AddAccessRule($rule)
        Set-Acl -LiteralPath $directory -AclObject $acl
        { Assert-RecoveryDirectoryAcl -Path $directory } | Should -Throw '*unexpected entries*'
    }

    It 'chowns every data volume to 65532 and verifies all entries read-only' {
        $script:dockerCalls = [Collections.Generic.List[object]]::new()
        $docker = { param([string[]]$DockerArguments) $script:dockerCalls.Add($DockerArguments) }
        Invoke-RecoveryVolumeOwnership -VolumeName 'stock-ai-test_agent_state' -DockerInvoker $docker
        $script:dockerCalls.Count | Should -Be 2
        ($script:dockerCalls[0] -join ' ') | Should -Match 'golang:1\.27\.2-alpine3\.23@sha256:[0-9a-f]{64}'
        ($script:dockerCalls[0] -join ' ') | Should -Match '--network none.*--read-only.*--cap-drop ALL.*--cap-add CHOWN.*--cap-add DAC_OVERRIDE'
        ($script:dockerCalls[0] -join ' ') | Should -Match 'chown -R 65532:65532 /data'
        ($script:dockerCalls[1] -join ' ') | Should -Match 'readonly'
        ($script:dockerCalls[1] -join ' ') | Should -Match '--cap-add DAC_OVERRIDE'
        ($script:dockerCalls[1] -join ' ') | Should -Match '! -uid 65532 -o ! -gid 65532'
    }

    It 'fails closed when chown or ownership verification fails' {
        foreach ($failAt in @(1, 2)) {
            $script:dockerCalls = 0
            $failOnCall = $failAt
            $docker = {
                param([string[]]$DockerArguments)
                $script:dockerCalls++
                if ($script:dockerCalls -eq $failOnCall) { throw 'ownership operation failed' }
            }.GetNewClosure()
            { Invoke-RecoveryVolumeOwnership -VolumeName 'stock-ai-test_backend_state' -DockerInvoker $docker } | Should -Throw 'ownership operation failed'
            $script:dockerCalls | Should -Be $failAt
        }
    }

    It 'rejects volume names that could alter Docker mount options' {
        $docker = { param([string[]]$DockerArguments) throw 'Docker must not be called' }
        { Invoke-RecoveryVolumeOwnership -VolumeName 'safe,volume=other' -DockerInvoker $docker } | Should -Throw
    }
}
