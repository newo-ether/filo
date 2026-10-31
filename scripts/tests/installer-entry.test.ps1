$ErrorActionPreference='Stop'
. (Join-Path $PSScriptRoot '..\windows\Install-Support.ps1')
$fixture=Join-Path ([IO.Path]::GetTempPath()) ('filo-entry-'+[guid]::NewGuid().ToString('N'))
function Assert($condition,$message){if(-not $condition){throw $message}}
try {
    $scripts=Join-Path $fixture 'scripts'
    $modules=Join-Path $scripts 'windows'
    New-Item -ItemType Directory -Path $modules -Force | Out-Null
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot '..\install.ps1') -Destination $scripts
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot '..\install-entry.ps1') -Destination (Join-Path $fixture 'Install-Filo.ps1')
    [IO.File]::WriteAllText((Join-Path $fixture 'manifest.json'),'fixture')
    # The actual entrypoint consumes fixture discovery/prerequisites, never OS mutations.
    [IO.File]::WriteAllText((Join-Path $modules 'Client-Selection.ps1'),@'
Set-StrictMode -Version 2
function Get-FiloClients { if($ProgressPreference -ne 'SilentlyContinue'){throw 'Installer progress can block native remoting'}; $global:FiloEntryClients }
function Resolve-FiloSelection { param($Clients,$Ids) $Clients }
function Select-FiloClients { param($Clients) $Clients }
'@)
    [IO.File]::WriteAllText((Join-Path $modules 'Install-Support.ps1'),@'
function Test-FiloInstallAdministrator { $global:FiloEntryAdmin }
function Get-FiloBundle { @{revision=('a'*40)} }
function Assert-FiloPrivatePath { param($Path,$Root) if(-not $global:FiloEntryAdmin){throw 'Protected state must not be read without elevation'}; Join-Path $global:FiloEntryRoot 'state' }
function Get-FiloWorkerState { Join-Path $global:FiloEntryRoot 'worker' }
'@)
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot '..\windows\Source-Selection.ps1') -Destination $modules
    [IO.File]::WriteAllText((Join-Path $modules 'Source-Selection.ps1'), @'
. (Join-Path $PSScriptRoot 'Source-Selection-real.ps1')
function Get-FiloSourceOptions { param($Worker,$Sources,$Socket,$SocketSpecified,$BridgePort,$BridgeSpecified,$PublicPort) [pscustomobject]@{Sources=$Sources;CodexDaemonSocket='';ClaudeBridgePort=7438} }
'@)
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot '..\windows\Source-Selection.ps1') -Destination (Join-Path $modules 'Source-Selection-real.ps1')
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot '..\windows\Installer-Tasks.ps1') -Destination $modules
    foreach($name in @('Worker-Update','Host-Lifecycle','Uninstall-Transaction')) {
        [IO.File]::WriteAllText((Join-Path $modules ($name+'.ps1')), '# No OS interaction in entry fixture')
    }
    [IO.File]::WriteAllText((Join-Path $modules 'Install-Transaction.ps1'),"function Invoke-FiloInstall { throw 'Validation must never mutate an installation' }")
    $global:FiloEntryRoot=$fixture
    foreach($privileged in @($false,$true)) {
    $global:FiloEntryAdmin=$privileged
    foreach($count in @(0,1)) {
        $global:FiloEntryClients=@(if($count){[pscustomobject]@{Name='Desktop only';Sid='fixture';Profile=$fixture}})
        foreach($unattended in @($false,$true)) {
            # Exercise the actual packaged wrapper with hashtable-bound switches.
            # The old @args wrapper forwarded True as the positional bundle path.
            $arguments=@{ValidateOnly=$true;Uninstall=$false}
            if($unattended){$arguments.Clients=@('codex')}
            $result=& (Join-Path $fixture 'Install-Filo.ps1') @arguments
            if($count){
                Assert ($result.Changed -eq $false -and $result.Client -eq 'Desktop only') 'Single selection validates without mutation'
                Assert ($result.InstalledStateChecked -eq $privileged) 'Validation must disclose its scope'
            }
            else {Assert ($result -match 'cancelled') 'Empty selection settles without mutation'}
        }
    }
    }
    $global:FiloEntryAdmin=$false
    $blocked=$false
    try { & (Join-Path $scripts 'install.ps1') -BundleDirectory $fixture -Clients codex }
    catch { $blocked=$_.Exception.Message -match 'as administrator' }
    Assert $blocked 'Non-admin installation must fail readably before reading private state'
    $global:FiloEntryAdmin=$true
    $statePath=Join-Path $fixture 'state'
    New-Item -ItemType Directory -Path $statePath -Force | Out-Null
    $savedSid='S-1-5-21-1-2-3-1001'
    [IO.File]::WriteAllText((Join-Path $statePath 'config.json'),(@{
        layoutVersion=1;desktopSid=$savedSid; sources=@('codex-cli','claude-code'); bind='127.0.0.1';port=7435;workerTokenPath='fixture'
    } | ConvertTo-Json))
    function Get-ItemPropertyValue { param($LiteralPath,$Name) $fixture }
    $global:FiloEntryClients=@()
    $preserved=& (Join-Path $fixture 'Install-Filo.ps1') -ValidateOnly
    Assert (($preserved.Sources -join ',') -eq 'codex-cli,claude-code') 'Upgrade without source options preserves saved sources even while native clients are unavailable'
    Assert ($preserved.Bind -eq '127.0.0.1') 'Default upgrade preserves the existing public bind'
    $wildcard=& (Join-Path $fixture 'Install-Filo.ps1') -ValidateOnly -Bind '0.0.0.0'
    Assert ($wildcard.Bind -eq '0.0.0.0') 'Packaged entry forwards an explicit wildcard bind through normal upgrade validation'
    Remove-Item -LiteralPath (Join-Path $statePath 'config.json')
    'PASS: real installer entry handles zero/one sources, validation and saved-source upgrade preservation'
} finally {
    $safe=Assert-FiloPrivatePath $fixture ([IO.Path]::GetTempPath())
    if(Test-Path -LiteralPath $safe){Remove-Item -LiteralPath $safe -Recurse -Force}
    Remove-Variable FiloEntryRoot,FiloEntryClients,FiloEntryAdmin -Scope Global -ErrorAction SilentlyContinue
}
