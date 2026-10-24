# Stops the Windows test package in dependency order: Agent first, Backend last.
$ErrorActionPreference = 'Stop'
$pidFile = Join-Path $PSScriptRoot 'data\pids.json'
if (!(Test-Path -LiteralPath $pidFile)) { Write-Host 'Nothing to stop (no data\pids.json).'; return }
$started = Get-Content -LiteralPath $pidFile -Raw | ConvertFrom-Json
foreach ($name in @('agent-server', 'middleware', 'llm-provider-server', 'backend')) {
    $id = $started.$name
    if (!$id) { continue }
    $process = Get-Process -Id $id -ErrorAction SilentlyContinue
    if ($process -and $process.Path -and $process.Path.StartsWith($PSScriptRoot, [StringComparison]::OrdinalIgnoreCase)) {
        Stop-Process -Id $id
        $process.WaitForExit(10000) | Out-Null
        Write-Host "$name stopped (PID $id)"
    }
}
Remove-Item -LiteralPath $pidFile
