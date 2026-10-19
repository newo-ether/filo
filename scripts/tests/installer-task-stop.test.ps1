$ErrorActionPreference='Stop'
. (Join-Path $PSScriptRoot '..\windows\Host-Lifecycle.ps1')
. (Join-Path $PSScriptRoot '..\windows\Install-Transaction.ps1')
function Assert($Value,$Message) { if(-not $Value){throw $Message} }
$fixture=Join-Path ([IO.Path]::GetTempPath()) ('filo-candidate-stop-'+[guid]::NewGuid().ToString('N'))
$runtime=Join-Path $fixture 'runtime'; $state=Join-Path $fixture 'worker'; $sid='S-1-5-21-1-2-3-1001'
$signals=[Collections.Generic.List[int]]::new(); $events=[Collections.Generic.List[string]]::new()
$candidate=[pscustomobject]@{ProcessId=700;ExecutablePath='C:\Filo fixture\codex.exe';CreationDate='initial'}
$current=$candidate; $listener=$true; $refuse=$false; $fallback=$false
$gateway=[pscustomobject]@{ProcessId=7331;CreationDate='gateway';ExecutablePath=(Join-Path $runtime 'tools\filo.exe');CommandLine=('filo.exe standalone "'+$state+'"')}
$hostAction=[pscustomobject]@{Execute='FiloBackground.exe';Arguments=('-Role Host -StateDirectory "'+$state+'"')}
function Get-ScheduledTask {
    param($TaskName,$ErrorAction)
    if($TaskName -eq 'Filo-User-Agent'){[pscustomobject]@{State='Ready';Actions=@([pscustomobject]@{Execute='FiloBackground.exe';Arguments='-Role Gateway'})}}
    else{[pscustomobject]@{State='Running';Actions=@($hostAction)}}
}
function Get-NetTCPConnection { param($LocalAddress,$LocalPort,$State,$ErrorAction) if($script:listener){[pscustomobject]@{OwningProcess=7331}} }
function Get-CimInstance {
    param($ClassName,$Filter)
    if($Filter -eq 'ProcessId=7331'){if($script:listener){$script:gateway}}
    elseif($Filter -eq 'ProcessId=700'){$script:current}
    else{throw 'Rollback must not enumerate or signal original/private executor processes'}
}
function Invoke-RestMethod {
    param($Uri,$Method,$Headers,$TimeoutSec,[switch]$DisableKeepAlive)
    Assert ($Headers.Authorization -eq ('Bearer '+('a'*64))) 'Candidate shutdown is authenticated'
    if($Uri.EndsWith('/v1/info')){return [pscustomobject]@{supportsSafeShutdown=$true}}
    Assert ($Method -eq 'Post' -and $Uri.EndsWith('/v1/shutdown')) 'Candidate uses safe shutdown endpoint'
    $events.Add('shutdown')
    if($script:refuse){
        $exception=[Exception]::new('Native metadata host is active')
        $exception | Add-Member NoteProperty Response ([pscustomobject]@{StatusCode=409})
        throw $exception
    }
    if($script:fallback){throw 'Candidate still starting'}
    $script:listener=$false
    [pscustomobject]@{stopped=$true}
}
function Wait-FiloWorkerStopped {
    param($Plan)
    if($script:fallback){
        Assert (Test-Path -LiteralPath (Join-Path $state 'gateway.stop')) 'Private cancellation request must exist during wait'
        $script:listener=$false
    }
    Assert (-not $script:listener) 'Rollback must wait for the detached candidate port to exit'
    $events.Add('port-exited')
}
function Get-FiloHostPlan {
    param($StateDirectory,$FiloPath,$UserSid)
    Assert ($events.Contains('port-exited')) 'Host retirement follows verified gateway shutdown'
    Assert ($UserSid -eq $sid) 'Host ownership uses selected native account'
    [pscustomobject]@{Action=$hostAction;Process=$candidate;WasRunning=$true;Changed=$false}
}
function Stop-ScheduledTask { param($TaskName) Assert ($TaskName -eq 'Filo-Standalone-Host') 'Gateway is never force-stopped'; $events.Add('host-task') }
function Stop-Process {
    param($Id,$ErrorAction,[switch]$Force)
    Assert ($Force -and $Id -eq 700) 'Only the verified idle metadata host may be signalled'
    $signals.Add($Id)
}
try {
    New-Item -ItemType Directory -Path $state -Force | Out-Null
    Write-FiloJson (Join-Path $state 'launch.json') @{runtimeDirectory=$runtime;desktopSid=$sid;revision=('b'*40)}
    Write-FiloJson (Join-Path $state 'config.json') @{bind='127.0.0.1';port=28437}
    [IO.File]::WriteAllText((Join-Path $state 'token'),('a'*64))
    [IO.File]::WriteAllText((Join-Path $state 'gateway.pid'),'7331')
    Stop-FiloNewTasks $runtime $state $sid
    Assert ($signals.Count -eq 1 -and $signals[0] -eq 700) 'Only matching idle host is stopped; accepted private executors remain independent'
    Assert (($events -join ',') -eq 'shutdown,port-exited,host-task') 'Detached gateway exits before host/config restoration'

    $signals.Clear(); $events.Clear(); $listener=$true
    $current=[pscustomobject]@{ProcessId=700;ExecutablePath=$candidate.ExecutablePath;CreationDate='reused'}
    $rejected=$false
    try { Stop-FiloNewTasks $runtime $state $sid } catch { $rejected=$_.Exception.Message -match 'identity changed' }
    Assert ($rejected -and $signals.Count -eq 0) 'A reused host PID is preserved and rollback fails safely'

    $events.Clear(); $listener=$true; $current=$candidate; $refuse=$true
    $rejected=$false
    try { Stop-FiloNewTasks $runtime $state $sid } catch { $rejected=$_.Exception.Message -match 'active' }
    Assert ($rejected -and $signals.Count -eq 0 -and -not $events.Contains('host-task')) 'Active native protection has no kill fallback'
    Assert (-not (Test-Path -LiteralPath (Join-Path $state 'gateway.stop'))) '409 refusal cannot be bypassed with a cancellation file'

    $events.Clear(); $refuse=$false; $fallback=$true
    Stop-FiloNewTasks $runtime $state $sid
    Assert ($events.Contains('port-exited')) 'Starting candidate uses the same bounded shutdown fence'
    Assert (-not (Test-Path -LiteralPath (Join-Path $state 'gateway.stop'))) 'Private cancellation file is removed after wait'

    $events.Clear(); $listener=$true; $fallback=$false
    $previousGateway=$gateway
    $gateway=[pscustomobject]@{ProcessId=7331;CreationDate='reused';ExecutablePath=$previousGateway.ExecutablePath;CommandLine=$previousGateway.CommandLine}
    Wait-FiloCandidateExit $previousGateway
    Assert ($signals.Count -eq 1) 'Waiting for candidate exit never signals a reused gateway PID'
    $gateway=[pscustomobject]@{ExecutablePath='C:\Original\codex.exe';CommandLine='app-server'}
    $rejected=$false
    try { Stop-FiloNewTasks $runtime $state $sid } catch { $rejected=$_.Exception.Message -match 'unrecognized listener' }
    Assert ($rejected -and $events.Count -eq 0) 'Reused gateway lease pointing at an original backend is rejected before shutdown'
    'PASS: detached candidate graceful shutdown, exit ordering, active native fence and PID reuse protection'
} finally {
    if(Test-Path -LiteralPath $fixture){Remove-Item -LiteralPath $fixture -Recurse -Force}
}
