. (Join-Path $PSScriptRoot 'Install-Layout.ps1')
. (Join-Path $PSScriptRoot 'Claude-Components.ps1')

function Invoke-FiloUninstall {
    param([string]$Bundle,[string]$Root,[string]$State,[string]$Worker,[string]$Sid)
    $program=Get-FiloProgramRoot
    $profiles=@(Get-CimInstance Win32_UserProfile | Where-Object { -not $_.Special -and $_.LocalPath } | ForEach-Object LocalPath)
    $componentPlan=$null
    if ($Worker -and (Test-Path -LiteralPath (Join-Path $Worker 'claude-components.json'))) {
        $receipt=Read-FiloClaudeComponentBytes (Join-Path $Worker 'claude-components.json') 8192
        $record=[Text.Encoding]::UTF8.GetString($receipt) | ConvertFrom-Json
        $componentPlan=New-FiloClaudeComponentPlan $Bundle $Worker ([string]$record.profile) $Sid @()
    }
    Stop-FiloInstalledResources $Root $program $profiles
    if ($componentPlan) { Install-FiloClaudeComponents $componentPlan }
    foreach ($profile in $profiles) {
        $marker=Join-Path $profile '.claude\skills\filo-observer\.filo-owner.json'
        if (-not (Test-Path -LiteralPath $marker)) { continue }
        $record=Get-Content -LiteralPath $marker -Raw | ConvertFrom-Json
        $ownedWorker=Get-FiloWorkerState $Root $profile ([string]$record.sid) ''
        $orphan=New-FiloClaudeComponentPlan $Bundle $ownedWorker $profile ([string]$record.sid) @()
        Install-FiloClaudeComponents $orphan
    }
    Remove-FiloFirewallRules
    Update-FiloMachinePath $program -Remove
    Remove-FiloLegacyDirectories $Root $profiles
    foreach ($directory in @($program,$Root)) {
        if (Test-Path -LiteralPath $directory) {
            $safe=Assert-FiloPrivatePath $directory (Split-Path -Parent $directory)
            Remove-Item -LiteralPath $safe -Recurse -Force
        }
    }
    Write-Output 'Filo uninstalled: service, tasks, firewall, PATH, program and private data removed.'
}
