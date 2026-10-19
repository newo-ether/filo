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
$events=[Collections.Generic.List[string]]::new()
$caseCount=0
function Start-ScheduledTask { param($TaskName) $events.Add('start-task:'+ $TaskName); $tasks[$TaskName]='Running' }
function Stop-ScheduledTask { param($TaskName) $tasks[$TaskName]='Ready' }
function Stop-Process { throw 'Fixture must never signal any real process' }
function Stop-Service { param($Name) $events.Add('stop-service'); $global:FiloFixtureServiceState='Stopped' }
function Start-Service { param($Name) $events.Add('start-service'); $global:FiloFixtureServiceState='Running' }
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
    if(($failurePoint -eq 'register-host-before' -and $Name -eq 'Filo-Standalone-Host') -or
        ($failurePoint -eq 'register-gateway-before' -and $Name -eq 'Filo-User-Agent')) { throw 'Injected registration failure before mutation' }
    $tasks[$Name]='Ready'
    if($failurePoint -eq 'register' -and $Name -eq 'Filo-User-Agent'){throw 'Injected registration failure'}
}
function Wait-FiloEndpoint {
    param($Url,$TokenPath,$Mode,$Seconds)
    if($failurePoint -in @('helper','shutdown') -and $Mode -eq 'standalone'){throw 'Injected helper failure'}
    if($failurePoint -eq 'ready' -and $Mode -eq 'existing'){throw 'Injected readiness failure'}
}
function Get-NetTCPConnection { @() }
function Get-FiloWorkerUpdatePlan {
    param($StateDirectory,$Revision)
    if($Revision -eq ''){
        [pscustomobject]@{WasRunning=($tasks['Filo-User-Agent'] -eq 'Running');StateDirectory=$StateDirectory;Candidate=$true}
    } else {
        $events.Add('worker-preflight')
        if($failurePoint -eq 'worker-preflight'){throw 'Injected worker preflight failure'}
        [pscustomobject]@{WasRunning=($tasks['Filo-User-Agent'] -eq 'Running');Launch=[pscustomobject]@{workspaceDirectory=$caseRoot};Candidate=$false}
    }
}
function Request-FiloWorkerStop {
    param($Plan)
    if($Plan.Candidate){
        Assert ([IO.File]::ReadAllText((Join-Path $worker 'launch.json')) -match 'runtimeDirectory') 'Candidate stops before prior config bytes are restored'
        if($failurePoint -eq 'shutdown'){throw 'Injected safe shutdown refusal'}
        $script:candidateStopped=$true
    } else {
        $events.Add('stop-worker')
        if($failurePoint -eq 'old-shutdown'){throw 'Injected original safe shutdown refusal'}
    }
    $tasks['Filo-User-Agent']='Ready'
    if(-not $Plan.Candidate -and $failurePoint -eq 'old-shutdown-after-stop'){throw 'Injected failure after original worker stopped'}
}
function Wait-FiloWorkerStopped { $script:candidateStopped=$true }
function Get-FiloHostPlan {
    $candidateConfig=Test-Path -LiteralPath (Join-Path $worker 'launch.json')
    if($candidateConfig){$candidateConfig=[IO.File]::ReadAllText((Join-Path $worker 'launch.json')) -match 'runtimeDirectory'}
    if(-not $candidateConfig){
        $events.Add('host-preflight')
        if($failurePoint -eq 'host-preflight'){throw 'Injected host preflight failure'}
        if($failurePoint -eq 'host-race' -and $events.Contains('stop-worker')){throw 'Injected late native activity'}
    }
    if($tasks.ContainsKey('Filo-Standalone-Host')){[pscustomobject]@{WasRunning=($tasks['Filo-Standalone-Host'] -eq 'Running')}}
}
function Stop-FiloHost { param($Plan) if($Plan){$events.Add('stop-host'); $tasks['Filo-Standalone-Host']='Ready'} }
try {
    foreach($initialState in @('Absent','Stopped','Running','Paused','Stop Pending')) {
        $upgrade=$initialState -ne 'Absent'
        $shouldRun=$initialState -in @('Running','Paused')
        $points=@('none','register-host-before','register-gateway-before','register','helper','service','ready','shutdown')
        if($upgrade){$points+=@('worker-preflight','host-preflight','old-shutdown','old-shutdown-after-stop','host-race')}
        foreach($failurePoint in $points) {
            $caseCount++
            $events.Clear()
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
            $tasks=@{}; $restored=@{}; $script:candidateStopped=$false
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
            $tokens=@((Join-Path $state 'token'),(Join-Path $worker 'token'))
            foreach($file in $tokens){[IO.File]::WriteAllText($file,('a'*64))}
            $tokenHashes=@{}; foreach($file in $tokens){$tokenHashes[$file]=(Get-FileHash -LiteralPath $file).Hash}
            $nativeState=Join-Path $caseRoot 'native-auth-and-task.fixture'
            [IO.File]::WriteAllText($nativeState,'Existing native account, credentials and task remain untouched')
            $nativeHash=(Get-FileHash -LiteralPath $nativeState).Hash
            $failed=$false
            try { Invoke-FiloInstall $runtime @{revision=('b'*40)} $caseRoot $state $worker $client '127.0.0.1' 23456 | Out-Null }
            catch { $failed=$true; if($_.Exception.Message -notmatch 'Injected'){throw} }
            Assert ($failed -eq ($failurePoint -ne 'none')) "Expected outcome: $upgrade/$failurePoint"
            foreach($file in $tokens){Assert ((Get-FileHash -LiteralPath $file).Hash -eq $tokenHashes[$file]) 'Existing credentials preserve exact bytes'}
            Assert ((Get-FileHash -LiteralPath $nativeState).Hash -eq $nativeHash) 'Native account and task data are untouched'
            if($failurePoint -in @('worker-preflight','host-preflight','old-shutdown','old-shutdown-after-stop','host-race')){
                Assert (-not $events.Contains('stop-service') -and -not $events.Contains('start-service')) 'Rejected update never interrupts the SYSTEM gateway'
                Assert (-not $events.Contains('stop-host')) 'Native host is never stopped on failed preflight or shutdown refusal'
                Assert ($restored.Count -eq 0 -and -not $script:candidateStopped) 'Rejected preflight never enters candidate rollback'
                foreach($file in $paths){Assert ([IO.File]::ReadAllText($file) -eq $prior[$file]) 'Rejected preflight preserves exact configuration bytes'}
                Assert ($tasks['Filo-User-Agent'] -eq 'Running' -and $tasks['Filo-Standalone-Host'] -eq 'Running') 'Rejected update preserves original task availability'
                continue
            }
            if($failurePoint -eq 'shutdown'){
                Assert $failed 'Failed safe shutdown must fail the transaction'
                Assert ($restored.Count -eq 0 -and -not $script:candidateStopped) 'Unsafe candidate prevents old task restoration'
                Assert ([IO.File]::ReadAllText((Join-Path $worker 'launch.json')) -match 'runtimeDirectory') 'Failed shutdown preserves matching candidate configuration'
                Assert ($tasks['Filo-User-Agent'] -eq 'Running') 'Active candidate cannot be force-stopped'
                continue
            }
            if($failed) {
                Assert ($script:candidateStopped -or -not $tasks.ContainsKey('Filo-User-Agent')) 'Candidate is confirmed stopped or was never registered'
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
    "PASS: $caseCount install/update cases cover preflight, admission race, partial registration, authenticated startup and rollback with preserved credentials/endpoints/account/tasks"
} finally {
    $safe=Assert-FiloPrivatePath $fixture ([IO.Path]::GetTempPath())
    if(Test-Path -LiteralPath $safe){Remove-Item -LiteralPath $safe -Recurse -Force}
    Remove-Variable FiloFixtureService,FiloFixtureServiceState,FiloFixtureFailure,FiloFixtureRestore -Scope Global -ErrorAction SilentlyContinue
}
