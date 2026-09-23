$ErrorActionPreference='Stop'
. (Join-Path $PSScriptRoot '..\windows\Install-Transaction.ps1')
function Assert($Value,$Message) { if(-not $Value){throw $Message} }
$runtime='C:\Filo fixture\runtime'
$state='C:\Filo fixture\state'
$signals=[Collections.Generic.List[int]]::new()
$taskStops=[Collections.Generic.List[string]]::new()
$candidate=[pscustomobject]@{ProcessId=700;ParentProcessId=600;Name='codex.exe';ExecutablePath='C:\Filo fixture\codex.exe';CreationDate='initial';CommandLine='app-server'}
$original=[pscustomobject]@{ProcessId=701;ParentProcessId=601;Name='codex.exe';ExecutablePath='C:\Original\codex.exe';CreationDate='original';CommandLine='app-server'}
$runner=[pscustomobject]@{ProcessId=600;ParentProcessId=500;Name='powershell.exe';CommandLine=('-File '+$runtime+'\run.ps1 -Role Host -StateDirectory '+$state)}
$all=@($runner,$candidate,$original)
$current=$candidate
function Get-CimInstance {
    param($ClassName,$Filter)
    if($Filter -eq 'ProcessId=700'){$script:current}
    elseif($Filter){throw 'Unexpected process identity lookup'}
    else{$script:all}
}
function Get-ScheduledTask { [pscustomobject]@{State='Running'} }
function Stop-ScheduledTask { param($TaskName) $taskStops.Add($TaskName) }
function Stop-Process {
    param($Id,$ErrorAction,[switch]$Force)
    Assert $Force 'SYSTEM rollback must not prompt for an owned user child'
    Assert ($Id -eq 700) 'Rollback must never signal the original backend'
    $signals.Add($Id)
}
Stop-FiloNewTasks $runtime $state
Assert ($signals.Count -eq 1 -and $signals[0] -eq 700) 'Only the matching candidate child is stopped'
Assert ($taskStops.Count -eq 2) 'Both Filo candidate tasks are stopped'
$signals.Clear()
$current=[pscustomobject]@{ProcessId=700;ExecutablePath=$candidate.ExecutablePath;CreationDate='reused'}
Stop-FiloNewTasks $runtime $state
Assert ($signals.Count -eq 0) 'A reused candidate PID is preserved'
'PASS: SYSTEM candidate cleanup is non-interactive and preserves original/reused processes'
