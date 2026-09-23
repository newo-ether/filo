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
function Get-FiloClients { $global:FiloEntryClients }
function Resolve-FiloSelection { param($Clients,$Ids) $Clients }
function Select-FiloClients { param($Clients) $Clients }
'@)
    [IO.File]::WriteAllText((Join-Path $modules 'Install-Support.ps1'),@'
function Test-FiloInstallAdministrator { $global:FiloEntryAdmin }
function Get-FiloBundle { @{revision=('a'*40)} }
function Assert-FiloPrivatePath { param($Path,$Root) if(-not $global:FiloEntryAdmin){throw 'Protected state must not be read without elevation'}; Join-Path $global:FiloEntryRoot 'state' }
function Get-FiloWorkerState { Join-Path $global:FiloEntryRoot 'worker' }
'@)
    [IO.File]::WriteAllText((Join-Path $modules 'Installer-Tasks.ps1'),"function Get-FiloInstallBind { '127.0.0.1' }")
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
    'PASS: real installer entry handles zero and one discovered client in strict mode'
} finally {
    $safe=Assert-FiloPrivatePath $fixture ([IO.Path]::GetTempPath())
    if(Test-Path -LiteralPath $safe){Remove-Item -LiteralPath $safe -Recurse -Force}
    Remove-Variable FiloEntryRoot,FiloEntryClients,FiloEntryAdmin -Scope Global -ErrorAction SilentlyContinue
}
