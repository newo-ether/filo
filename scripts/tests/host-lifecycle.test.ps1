$ErrorActionPreference='Stop'
. (Join-Path $PSScriptRoot '..\windows\Host-Lifecycle.ps1')
function Assert($Value,$Message) { if(-not $Value){throw $Message} }
$fixture=Join-Path ([IO.Path]::GetTempPath()) ('filo-host-lifecycle-' + [guid]::NewGuid().ToString('N'))
$sid='S-1-5-21-1-2-3-1001'
$signals=[Collections.Generic.List[int]]::new()
$taskStops=0
$action=[pscustomobject]@{Execute='powershell.exe';Arguments=('-File fixture.ps1 -Role Host -StateDirectory "'+$fixture+'"')}
$process=[pscustomobject]@{ProcessId=700;ParentProcessId=600;Name='codex.exe';ExecutablePath='C:\Filo fixture\codex.exe';CreationDate='initial';CommandLine='app-server --listen ws://127.0.0.1:7436'}
$parentCommand=$action.Arguments
$ownerSid=$sid
$active=$false
function Get-ScheduledTask { [pscustomobject]@{State='Running';Actions=@($action)} }
function Get-NetTCPConnection { [pscustomobject]@{OwningProcess=700} }
function Get-CimInstance { param($ClassName,$Filter) if($Filter -eq 'ProcessId=700'){$script:process}else{[pscustomobject]@{CommandLine=$parentCommand}} }
function Invoke-CimMethod { [pscustomobject]@{ReturnValue=0;Sid=$ownerSid} }
function Stop-ScheduledTask { $script:taskStops++ }
function Stop-Process {
    param($Id,$ErrorAction,[switch]$Force)
    if(-not $Force){throw 'SYSTEM cannot confirm another user process in a non-interactive installer'}
    $signals.Add($Id)
}
try {
    New-Item -ItemType Directory -Path $fixture | Out-Null
    [IO.File]::WriteAllText((Join-Path $fixture 'launch.json'),'{"appServerUrl":"ws://127.0.0.1:7436"}')
    Write-FiloJson (Join-Path $fixture 'config.json') @{appServerUrl='ws://127.0.0.1:7436';bind='127.0.0.1';port=7437}
    $probe=Join-Path $fixture 'probe.ps1'
    [IO.File]::WriteAllText($probe,'Write-Output ''{"idle":true}''; $global:LASTEXITCODE=if($active){1}else{0}')
    foreach($case in @('original-desktop','wrong-account','active')) {
        $parentCommand=if($case -eq 'original-desktop'){'Original ChatGPT.exe'}else{$action.Arguments}
        $ownerSid=if($case -eq 'wrong-account'){'S-1-5-21-9-9-9-1001'}else{$sid}
        $active=$case -eq 'active'
        $rejected=$false
        try{Get-FiloHostPlan $fixture $probe $sid | Out-Null}catch{$rejected=$true}
        Assert $rejected 'Unverified or active process must be preserved'
        Assert ($signals.Count -eq 0 -and $taskStops -eq 0) 'Preflight never signals a process/task'
    }
    $parentCommand=$action.Arguments; $ownerSid=$sid; $active=$false
    Write-FiloJson (Join-Path $fixture 'launch.json') @{nodePath='C:\old\node.exe';codexPath='C:\native\codex.exe'}
    $plan=Get-FiloHostPlan $fixture $probe $sid
    Assert (@($plan).Count -eq 1) 'Probe output must not pollute the transaction plan'
    $process=[pscustomobject]@{ProcessId=700;ParentProcessId=600;Name='codex.exe';ExecutablePath='C:\Filo fixture\codex.exe';CreationDate='reused';CommandLine='app-server --listen ws://127.0.0.1:7436'}
    $rejected=$false
    try{Stop-FiloHost $plan}catch{$rejected=$true}
    Assert ($rejected -and $signals.Count -eq 0 -and $taskStops -eq 0) 'PID reuse is rejected before stopping a task'
    $process=$plan.Process
    Stop-FiloHost $plan
    Assert ($signals.Count -eq 1 -and $signals[0] -eq 700 -and $taskStops -eq 1) 'Only the identified idle Filo child is retired'
    'PASS: original desktop/account/activity/PID identity protects native processes'
} finally {
    $safe=[IO.Path]::GetFullPath($fixture)
    if(-not $safe.StartsWith([IO.Path]::GetFullPath([IO.Path]::GetTempPath()),[StringComparison]::OrdinalIgnoreCase)){throw 'Unsafe cleanup'}
    if(Test-Path -LiteralPath $safe){Remove-Item -LiteralPath $safe -Recurse -Force}
}
