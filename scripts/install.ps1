<#
.SYNOPSIS
Install Filo for explicitly selected native sources under one Windows account.
.DESCRIPTION
Uses the bundled Go service binary and the local service manager. The SYSTEM gateway
and ordinary-user auxiliaries never redirect desktop startup or control original
desktop processes.
#>
[CmdletBinding()]
param(
    [string]$BundleDirectory,
    [string[]]$Clients,
    [switch]$ListClients,
    [switch]$ValidateOnly,
    [switch]$Uninstall,
    [string]$Bind,
    [string]$CodexDaemonSocket,
    [ValidateRange(1024,65535)][int]$ClaudeBridgePort = 7438,
    [switch]$CopyClaudeMod,
    [string]$ClaudeModDirectory,
    [ValidateRange(1024,65535)][int]$Port = 7435
)
$ErrorActionPreference='Stop'
# Native remoting transports may serialize progress into a blocking stderr pipe.
$ProgressPreference='SilentlyContinue'
if ($PSBoundParameters.ContainsKey('Clients') -and
    (-not $Clients -or @($Clients | Where-Object { [string]::IsNullOrWhiteSpace($_) }).Count)) {
    throw 'Clients must contain at least one non-empty client ID. Use -ListClients to discover IDs.'
}
if ($ValidateOnly -and $Uninstall) { throw 'ValidateOnly cannot be combined with Uninstall; no resources changed' }
if (-not $BundleDirectory) { $BundleDirectory=Split-Path -Parent $PSScriptRoot }
. (Join-Path $PSScriptRoot 'windows\Client-Selection.ps1')
. (Join-Path $PSScriptRoot 'windows\Install-Support.ps1')
. (Join-Path $PSScriptRoot 'windows\Source-Selection.ps1')
. (Join-Path $PSScriptRoot 'windows\Installer-Tasks.ps1')
. (Join-Path $PSScriptRoot 'windows\Worker-Update.ps1')
. (Join-Path $PSScriptRoot 'windows\Host-Lifecycle.ps1')
. (Join-Path $PSScriptRoot 'windows\Install-Transaction.ps1')
. (Join-Path $PSScriptRoot 'windows\Uninstall-Transaction.ps1')
if ($ListClients) { @(Get-FiloClients) | Select-Object Id,Name,Supported,Detail; return }
if (-not (Test-Path -LiteralPath (Join-Path $BundleDirectory 'manifest.json'))) {
    throw 'Use a complete Filo Windows bundle. No services, tasks or native files were changed.'
}
$manifest=Get-FiloBundle $BundleDirectory
$root=Join-Path $env:ProgramData 'Filo'
$admin=Test-FiloInstallAdministrator
if (-not $ValidateOnly -and -not $admin) {
    throw 'Run the Filo installer as administrator; discovery and bundle/client validation do not need elevation'
}
$state=$null; $existing=$null
$wrapper=Join-Path $root 'nssm.exe'
$mutex=$null; $ownsMutex=$false
if ($admin) {
    $mutex=[Threading.Mutex]::new($false,'Global\Filo.Install')
    try { $ownsMutex=$mutex.WaitOne(0) } catch [Threading.AbandonedMutexException] { $ownsMutex=$true }
    if (-not $ownsMutex) { $mutex.Dispose(); throw 'Another Filo installation is already running' }
}
try {
if ($admin) {
    # Read installed identity and endpoints only while holding the installation lock.
    $state=Assert-FiloPrivatePath (Join-Path $root 'service') $root
    $configPath=Join-Path $state 'config.json'
    $existing=if (Test-Path -LiteralPath $configPath) { Get-Content -Raw -LiteralPath $configPath | ConvertFrom-Json } else { $null }
}

if ($Uninstall) {
    if (-not $admin) { throw 'Run the Filo installer as administrator' }
    if (-not $existing) { Write-Output 'Filo has no installed configuration'; return }
    $profile=Get-ItemPropertyValue -LiteralPath "HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\ProfileList\$($existing.desktopSid)" -Name ProfileImagePath
    $worker=Get-FiloWorkerState $root $profile $existing.desktopSid $existing.workerTokenPath
    Invoke-FiloUninstall -Bundle $BundleDirectory -Root $root -State $state -Worker $worker -Sid $existing.desktopSid
    return
}

$detected=@(Get-FiloClients)
$selected=@(if ($Clients) { Resolve-FiloSelection $detected $Clients }
    elseif ($existing) { Get-FiloSavedClients $existing }
    else { Select-FiloClients $detected })
if (-not $selected.Count) { Write-Output 'Installation cancelled; no resources changed'; return }
$client=$selected[0]
$sources=@($selected | ForEach-Object {
    if ($_.PSObject.Properties['Source']) { [string]$_.Source }
    elseif ($_.PSObject.Properties['Id']) { Get-FiloClientSource $_ } else { 'codex-desktop' }
} | Select-Object -Unique)
if ($CopyClaudeMod -and ($sources -notcontains 'claude-code' -or -not $ClaudeModDirectory)) {
    throw 'CopyClaudeMod requires the Claude source and an explicit user-owned ClaudeModDirectory'
}
if ($ClaudeModDirectory -and -not $CopyClaudeMod) { throw 'ClaudeModDirectory requires explicit CopyClaudeMod opt-in' }
if ($ValidateOnly -and -not $admin) {
    [pscustomobject]@{
        Client=$client.Name;Sources=$sources;Revision=$manifest.revision;SeparateCliRequired=$false
        Changed=$false;InstalledStateChecked=$false
        Detail='Bundle and client verified. Run validation as administrator to check the existing service configuration.'
    }
    return
}
if ($existing -and $existing.desktopSid -ne $client.Sid) { throw 'Installed Filo belongs to another account. Uninstall it before switching accounts.' }
if (-not $existing -and -not $Clients -and -not $ValidateOnly -and -not $PSBoundParameters.ContainsKey('CopyClaudeMod') -and
    $sources -contains 'claude-code') {
    $choice=Select-FiloClaudeModCopy $client.Profile $client.Sid
    if ($choice) { $CopyClaudeMod=$true; $ClaudeModDirectory=$choice }
}
$address=Get-FiloInstallBind $Bind $(if ($existing) { $existing.bind })
if ($existing -and -not $PSBoundParameters.ContainsKey('Port')) { $Port=$existing.port }
$worker=Get-FiloWorkerState $root $client.Profile $client.Sid $(if ($existing) { $existing.workerTokenPath })
$sourceOptions=Get-FiloSourceOptions $worker $sources $CodexDaemonSocket $PSBoundParameters.ContainsKey('CodexDaemonSocket') `
    $ClaudeBridgePort $PSBoundParameters.ContainsKey('ClaudeBridgePort') $Port
if ($CopyClaudeMod) { Assert-FiloClaudeModDestination $ClaudeModDirectory $client.Profile $client.Sid | Out-Null }
if ($ValidateOnly) {
    [pscustomobject]@{Client=$client.Name;Sources=$sources;Revision=$manifest.revision;Bind=$address;Port=$Port;SeparateCliRequired=$false;Changed=$false;InstalledStateChecked=$true}
    return
}
if (-not $admin) { throw 'Run the Filo installer as administrator; discovery and validation do not need elevation' }
Invoke-FiloInstall -Bundle $BundleDirectory -Manifest $manifest -Root $root -State $state -Worker $worker -Client $client -Bind $address -Port $Port -SourceOptions $sourceOptions
if ($CopyClaudeMod) { Copy-FiloClaudeMod $BundleDirectory $ClaudeModDirectory $client.Profile $client.Sid }
} finally {
    if ($mutex) { if ($ownsMutex) { $mutex.ReleaseMutex() }; $mutex.Dispose() }
}
