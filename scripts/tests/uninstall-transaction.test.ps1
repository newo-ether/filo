$ErrorActionPreference='Stop'
. (Join-Path $PSScriptRoot '..\windows\Install-Support.ps1')
. (Join-Path $PSScriptRoot '..\windows\Uninstall-Transaction.ps1')
function Assert($Value,$Message) { if (-not $Value) { throw $Message } }
$fixture=Join-Path ([IO.Path]::GetTempPath()) ('filo-uninstall-' + [guid]::NewGuid().ToString('N'))
function Get-CimInstance {
    param($ClassName,$Filter)
    [pscustomobject]@{StartName='LocalSystem';PathName=(Join-Path $fixture 'nssm.exe');State=$global:FiloUninstallState}
}
function Get-ItemProperty { param($LiteralPath) [pscustomobject]@{AppDirectory=$fixture;Application='fixture-node.exe';AppParameters='fixture-node "fixture-args"'} }
function Get-ScheduledTask { param($TaskName,$ErrorAction) if($tasks.ContainsKey($TaskName)){[pscustomobject]@{State=$tasks[$TaskName]}} }
function Export-ScheduledTask { param($TaskName) 'original-'+$TaskName }
function Register-ScheduledTask {
    param($TaskName,$Xml,[switch]$Force)
    Assert ($Xml -eq ('original-'+$TaskName)) 'Exact original task registration is restored'
    $tasks[$TaskName]='Ready'
}
function Unregister-ScheduledTask {
    param($TaskName,[switch]$Confirm)
    $tasks.Remove($TaskName)
    if($failurePoint -eq 'task'){throw 'Injected unregister failure'}
}
function Start-ScheduledTask { param($TaskName) $tasks[$TaskName]='Running' }
function Stop-Service { param($Name) $global:FiloUninstallState='Stopped' }
function Start-Service { param($Name) $global:FiloUninstallState='Running' }
function Get-Service {
    param($Name,$ErrorAction)
    if($global:FiloUninstallService){$value=[pscustomobject]@{}; $value | Add-Member ScriptMethod WaitForStatus {}; $value}
}
function Get-FiloWorkerUpdatePlan { [pscustomobject]@{WasRunning=$wasRunning} }
function Request-FiloWorkerStop {
    $tasks['Filo-User-Agent']='Ready'
    if($failurePoint -eq 'worker'){throw 'Injected worker failure'}
}
function Get-FiloHostPlan {
    if($failurePoint -eq 'active'){throw 'Injected active auxiliary refusal'}
    [pscustomobject]@{WasRunning=$wasRunning}
}
function Stop-FiloHost { $tasks['Filo-Standalone-Host']='Ready' }
try {
    New-Item -ItemType Directory -Path (Join-Path $fixture 'scripts') -Force | Out-Null
    [IO.File]::WriteAllText((Join-Path $fixture 'scripts/system-service.ps1'),@'
param($Action,$RuntimeDirectory,$StateDirectory,$FiloPath,$WrapperPath,
$RestoreApplication,$RestoreAppDirectory,$RestoreAppParameters,[switch]$RestoreStart)
if($Action -eq 'Uninstall'){
    if($global:FiloUninstallFailure -eq 'service'){throw 'Injected service deletion failure'}
    $global:FiloUninstallService=$false
    if($global:FiloUninstallFailure -eq 'ambiguous'){throw 'Injected ambiguous deletion failure'}
    return}
$global:FiloUninstallService=$true
if($Action -eq 'Restore'){
    $global:FiloUninstallRestore=@($RestoreApplication,$RestoreAppDirectory,$RestoreAppParameters)
    if($RestoreStart){Start-Service -Name Filo}
    return}
Start-Service -Name Filo
'@)
    foreach($initialState in @('Stopped','Running','Paused','Stop Pending')) {
        $wasRunning=$initialState -in @('Running','Paused')
        foreach($failurePoint in @('none','worker','active','task','service','ambiguous')) {
            if(-not $wasRunning -and $failurePoint -eq 'worker'){continue}
            $global:FiloUninstallState=$initialState
            $global:FiloUninstallService=$true; $global:FiloUninstallFailure=$failurePoint; $global:FiloUninstallRestore=$null
            $tasks=@{'Filo-User-Agent'=$(if($wasRunning){'Running'}else{'Ready'});'Filo-Standalone-Host'=$(if($wasRunning){'Running'}else{'Ready'})}
            $failed=$false
            try { Invoke-FiloUninstall $fixture $fixture $fixture $fixture 'fixture-sid' | Out-Null }
            catch {$failed=$true; if($_.Exception.Message -notmatch 'Injected'){throw}}
            Assert ($failed -eq ($failurePoint -ne 'none')) "Expected uninstall result: $wasRunning/$failurePoint"
            Assert ($global:FiloUninstallService -eq $failed) 'Service removed only on successful uninstall'
            Assert ($tasks.Count -eq $(if($failed){2}else{0})) 'Original registrations survive every failed uninstall'
            if($failed){
                if($failurePoint -eq 'ambiguous') {
                    Assert ($global:FiloUninstallRestore[0] -eq 'fixture-node.exe') 'Rollback reinstates the recorded application'
                    Assert ($global:FiloUninstallRestore[1] -eq $fixture) 'Rollback reinstates the recorded directory'
                    Assert ($global:FiloUninstallRestore[2] -eq 'fixture-node "fixture-args"') 'Rollback reinstates the recorded parameters'
                }
                Assert ($global:FiloUninstallState -eq $(if($wasRunning){'Running'}else{'Stopped'})) "Original service run state survives: $wasRunning/$failurePoint/$global:FiloUninstallState"
                foreach($state in $tasks.Values){Assert ($state -eq $(if($wasRunning){'Running'}else{'Ready'})) 'Original task run state survives'}
            }
        }
    }
    'PASS: uninstall preserves registrations and running/stopped states at each failure boundary'
} finally {
    $safe=Assert-FiloPrivatePath $fixture ([IO.Path]::GetTempPath())
    if(Test-Path -LiteralPath $safe){Remove-Item -LiteralPath $safe -Recurse -Force}
    Remove-Variable FiloUninstallService,FiloUninstallFailure,FiloUninstallState,FiloUninstallRestore -Scope Global -ErrorAction SilentlyContinue
}
