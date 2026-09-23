<#
.SYNOPSIS
Install Filo for the original Codex Desktop, without a separate CLI.
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
    [ValidateRange(1024,65535)][int]$Port = 7435
)
$ErrorActionPreference='Stop'
if ($ValidateOnly -and $Uninstall) { throw 'ValidateOnly cannot be combined with Uninstall; no resources changed' }
if (-not $BundleDirectory) { $BundleDirectory=Split-Path -Parent $PSScriptRoot }
. (Join-Path $PSScriptRoot 'windows\Client-Selection.ps1')
. (Join-Path $PSScriptRoot 'windows\Install-Support.ps1')
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
if ($admin) {
    $state=Assert-FiloPrivatePath (Join-Path $root 'service') $root
    $configPath=Join-Path $state 'config.json'
    $existing=if (Test-Path -LiteralPath $configPath) { Get-Content -Raw -LiteralPath $configPath | ConvertFrom-Json } else { $null }
}
$wrapper=Join-Path $root 'nssm.exe'
$mutex=$null; $ownsMutex=$false
if (-not $ValidateOnly) {
    if (-not $admin) { throw 'Run the Filo installer as administrator; discovery and validation do not need elevation' }
    $mutex=[Threading.Mutex]::new($false,'Global\Filo.Install')
    try { $ownsMutex=$mutex.WaitOne(0) } catch [Threading.AbandonedMutexException] { $ownsMutex=$true }
    if (-not $ownsMutex) { $mutex.Dispose(); throw 'Another Filo installation is already running' }
}
try {

if ($Uninstall) {
    if (-not $admin) { throw 'Run the Filo installer as administrator' }
    if (-not $existing) { Write-Output 'Filo has no installed configuration'; return }
    $profile=Get-ItemPropertyValue -LiteralPath "HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\ProfileList\$($existing.desktopSid)" -Name ProfileImagePath
    $worker=Get-FiloWorkerState $root $profile $existing.desktopSid $existing.workerTokenPath
    Invoke-FiloUninstall -Bundle $BundleDirectory -Root $root -State $state -Worker $worker -Sid $existing.desktopSid
    return
}

$detected=@(Get-FiloClients)
$selected=@(if ($Clients) { Resolve-FiloSelection $detected $Clients } else { Select-FiloClients $detected })
if (-not $selected.Count) { Write-Output 'Installation cancelled; no resources changed'; return }
$client=$selected[0]
if ($ValidateOnly -and -not $admin) {
    [pscustomobject]@{
        Client=$client.Name;Revision=$manifest.revision;SeparateCliRequired=$false
        Changed=$false;InstalledStateChecked=$false
        Detail='Bundle and client verified. Run validation as administrator to check the existing service configuration.'
    }
    return
}
if ($existing -and $existing.desktopSid -ne $client.Sid) { throw 'Installed Filo belongs to another account. Uninstall it before switching accounts.' }
$address=Get-FiloInstallBind $Bind $(if ($existing) { $existing.bind })
if ($existing -and -not $PSBoundParameters.ContainsKey('Port')) { $Port=$existing.port }
$worker=Get-FiloWorkerState $root $client.Profile $client.Sid $(if ($existing) { $existing.workerTokenPath })
if ($ValidateOnly) {
    [pscustomobject]@{Client=$client.Name;Revision=$manifest.revision;Bind=$address;Port=$Port;SeparateCliRequired=$false;Changed=$false;InstalledStateChecked=$true}
    return
}
if (-not $admin) { throw 'Run the Filo installer as administrator; discovery and validation do not need elevation' }
Invoke-FiloInstall -Bundle $BundleDirectory -Manifest $manifest -Root $root -State $state -Worker $worker -Client $client -Bind $address -Port $Port
} finally {
    if ($mutex) { if ($ownsMutex) { $mutex.ReleaseMutex() }; $mutex.Dispose() }
}
