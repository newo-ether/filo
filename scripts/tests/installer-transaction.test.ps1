$ErrorActionPreference='Stop'
. (Join-Path $PSScriptRoot '..\windows\Install-Support.ps1')
. (Join-Path $PSScriptRoot '..\windows\Install-Transaction.ps1')
function Assert($Value,$Message) { if (-not $Value) { throw $Message } }
$fixture=Join-Path ([IO.Path]::GetTempPath()) ('filo-transaction-' + [guid]::NewGuid().ToString('N'))
function Get-CimInstance {
    param($ClassName,$Filter)
    if ($ClassName -eq 'Win32_Service' -and $global:FiloFixtureService) {
        [pscustomobject]@{StartName='LocalSystem';PathName=(Join-Path $caseRoot 'nssm.exe');State=$global:FiloFixtureServiceState}
    } elseif ($ClassName -eq 'Win32_UserProfile') { [pscustomobject]@{SID=$client.Sid;Loaded=$true} }
}
function Get-ScheduledTask { param($TaskName,$ErrorAction) if($tasks.ContainsKey($TaskName)){[pscustomobject]@{State=$tasks[$TaskName]}} }
function Export-ScheduledTask { param($TaskName) 'original-'+$TaskName }
function Register-ScheduledTask { param($TaskName,$Xml,[switch]$Force) $tasks[$TaskName]='Ready'; $restored[$TaskName]=$Xml }
function Unregister-ScheduledTask { param($TaskName,[switch]$Confirm) $tasks.Remove($TaskName) }
function Start-ScheduledTask { param($TaskName) $tasks[$TaskName]='Running' }
function Stop-ScheduledTask { param($TaskName) $tasks[$TaskName]='Ready' }
function Stop-Process { throw 'Fixture must never signal any real process' }
function Stop-Service { param($Name) $global:FiloFixtureServiceState='Stopped' }
function Start-Service { param($Name) $global:FiloFixtureServiceState='Running' }
function Get-Service {
    param($Name,$ErrorAction)
    if($global:FiloFixtureService){ $value=[pscustomobject]@{Status=$global:FiloFixtureServiceState}; $value | Add-Member ScriptMethod WaitForStatus {}; $value }
}
function Get-ItemProperty { param($LiteralPath) [pscustomobject]@{AppDirectory=$runtime;Application=(Join-Path $runtime 'tools\filo.exe');AppParameters='system-service "prior-state"'} }
function Install-FiloRuntime { param($Bundle) $Bundle }
function Protect-FiloDirectory { param($Path,$UserSid) New-Item -ItemType Directory -Path $Path -Force | Out-Null }
function New-FiloTaskAction { param($Runtime,$State,$Role,$Workspace) $Role }
function Register-FiloUserTask {
    param($Name,$Action,$Sid)
    $tasks[$Name]='Ready'
    if($failurePoint -eq 'register' -and $Name -eq 'Filo-User-Agent'){throw 'Injected registration failure'}
}
function Wait-FiloEndpoint {
    param($Url,$TokenPath,$Mode,$Seconds)
    if($failurePoint -eq 'helper' -and $Mode -eq 'standalone'){throw 'Injected helper failure'}
    if($failurePoint -eq 'ready' -and $Mode -eq 'existing'){throw 'Injected readiness failure'}
}
function Get-FiloWorkerUpdatePlan {
    param($StateDirectory,$Revision)
    [pscustomobject]@{WasRunning=$true;Launch=[pscustomobject]@{workspaceDirectory=$caseRoot}}
}
function Request-FiloWorkerStop { $tasks['Filo-User-Agent']='Ready' }
function Get-FiloHostPlan { [pscustomobject]@{WasRunning=$true} }
function Stop-FiloHost { $tasks['Filo-Standalone-Host']='Ready' }
try {
    foreach($initialState in @('Absent','Stopped','Running','Paused','Stop Pending')) {
        $upgrade=$initialState -ne 'Absent'
        $shouldRun=$initialState -in @('Running','Paused')
        foreach($failurePoint in @('none','register','helper','service','ready')) {
            $caseRoot=Join-Path $fixture ($initialState+'-'+$failurePoint)
            $runtime=Join-Path $caseRoot 'runtime'; $state=Join-Path $caseRoot 'service'; $worker=Join-Path $caseRoot 'worker'
            New-Item -ItemType Directory -Path (Join-Path $runtime 'scripts'),(Join-Path $runtime 'tools'),$state,$worker -Force | Out-Null
            [IO.File]::WriteAllText((Join-Path $runtime 'tools/nssm.exe'),'fixture - never executed')
            [IO.File]::WriteAllText((Join-Path $runtime 'scripts/system-service.ps1'),@'
param($Action,$RuntimeDirectory,$StateDirectory,$FiloPath,$WrapperPath,
$RestoreApplication,$RestoreAppDirectory,$RestoreAppParameters,[switch]$RestoreStart)
if($Action -eq 'Uninstall'){$global:FiloFixtureService=$false;return}
if($Action -eq 'Install' -and $global:FiloFixtureFailure -eq 'service'){throw 'Injected service failure'}
$global:FiloFixtureService=$true
if($Action -eq 'Restore'){$global:FiloFixtureRestore=@($RestoreApplication,$RestoreAppDirectory,$RestoreAppParameters)}
if($Action -ne 'Restore' -or $RestoreStart){$global:FiloFixtureServiceState='Running'}
'@)
            $global:FiloFixtureService=$upgrade; $global:FiloFixtureServiceState=$initialState; $global:FiloFixtureFailure=$failurePoint
            $global:FiloFixtureRestore=$null
            $tasks=@{}; $restored=@{}
            $client=[pscustomobject]@{Sid='S-1-5-21-1-2-3-1001';Profile=$caseRoot;Name='Desktop-only fixture'}
            $paths=@((Join-Path $state 'config.json'),(Join-Path $worker 'config.json'),(Join-Path $worker 'launch.json'))
            $prior=@{}
            if($upgrade) {
                $prior[$paths[0]]='{"preserve":"prior state"}'
                $prior[$paths[1]]='{"appServerUrl":"ws://127.0.0.1:28436","bind":"127.0.0.1","port":28437}'
                $prior[$paths[2]]='{"appServerUrl":"ws://127.0.0.1:28436"}'
                foreach($file in $paths){[IO.File]::WriteAllText($file,$prior[$file])}
                $tasks['Filo-User-Agent']='Running'; $tasks['Filo-Standalone-Host']='Running'
            }
            $failed=$false
            try { Invoke-FiloInstall $runtime @{revision=('b'*40)} $caseRoot $state $worker $client '127.0.0.1' 23456 | Out-Null }
            catch { $failed=$true; if($_.Exception.Message -notmatch 'Injected'){throw} }
            Assert ($failed -eq ($failurePoint -ne 'none')) "Expected outcome: $upgrade/$failurePoint"
            if($failed) {
                Assert ($global:FiloFixtureService -eq $upgrade) 'Rollback preserves service existence'
                Assert ($tasks.Count -eq $(if($upgrade){2}else{0})) 'Rollback restores prior task registrations'
                foreach($file in $paths) {
                    if($upgrade){Assert ([IO.File]::ReadAllText($file) -eq $prior[$file]) 'Rollback preserves exact configuration bytes'}
                    else {Assert (-not(Test-Path -LiteralPath $file)) 'Failed first install leaves no partial config'}
                }
                if($upgrade){Assert ($global:FiloFixtureServiceState -eq $(if($shouldRun){'Running'}else{'Stopped'})) 'Rollback preserves running and stopping intent'}
                if($upgrade -and $failurePoint -in @('service','ready')) {
                    Assert ($global:FiloFixtureRestore[0] -eq (Join-Path $runtime 'tools\filo.exe')) 'Rollback reinstates the recorded application'
                    Assert ($global:FiloFixtureRestore[1] -eq $runtime) 'Rollback reinstates the recorded directory'
                    Assert ($global:FiloFixtureRestore[2] -eq 'system-service "prior-state"') 'Rollback reinstates the recorded parameters'
                    if($shouldRun){Assert ($global:FiloFixtureServiceState -eq 'Running') 'A recorded running service is started again'}
                }
            } else {
                Assert $global:FiloFixtureService 'Successful service installation'
                $launch=Get-Content -Raw -LiteralPath (Join-Path $worker 'launch.json') | ConvertFrom-Json
                Assert (-not $launch.PSObject.Properties['codexPath']) 'No persisted native executable path'
                Assert ($launch.desktopSid -eq $client.Sid) 'Selected desktop account persists'
                $helper=Get-Content -Raw -LiteralPath (Join-Path $worker 'config.json') | ConvertFrom-Json
                $gateway=Get-Content -Raw -LiteralPath (Join-Path $state 'config.json') | ConvertFrom-Json
                $nativePort=if($upgrade){28436}else{7436}
                $workerPort=if($upgrade){28437}else{7437}
                Assert ($launch.appServerUrl -eq "ws://127.0.0.1:$nativePort") 'Upgrade preserves native endpoint'
                Assert ($helper.port -eq $workerPort -and $gateway.workerUrl -eq "http://127.0.0.1:$workerPort") 'Gateway and helper retain one consistent configured endpoint'
            }
        }
    }
    'PASS: first install/update and registration/helper/service/readiness rollback preserve Filo state'
} finally {
    $safe=Assert-FiloPrivatePath $fixture ([IO.Path]::GetTempPath())
    if(Test-Path -LiteralPath $safe){Remove-Item -LiteralPath $safe -Recurse -Force}
    Remove-Variable FiloFixtureService,FiloFixtureServiceState,FiloFixtureFailure,FiloFixtureRestore -Scope Global -ErrorAction SilentlyContinue
}
