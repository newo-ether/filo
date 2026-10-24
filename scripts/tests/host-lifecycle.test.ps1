$ErrorActionPreference='Stop'
. (Join-Path $PSScriptRoot '..\windows\Host-Lifecycle.ps1')
function Assert($Value,$Message) { if(-not $Value){throw $Message} }
$fixture=Join-Path ([IO.Path]::GetTempPath()) ('filo-host-lifecycle-' + [guid]::NewGuid().ToString('N'))
$sid='S-1-5-21-1-2-3-1001'
$signals=[Collections.Generic.List[int]]::new()
$taskStops=0
$action=[pscustomobject]@{Execute='powershell.exe';Arguments=('-File fixture.ps1 -Role Host -StateDirectory "'+$fixture+'"')}
$process=[pscustomobject]@{ProcessId=700;ParentProcessId=600;Name='codex.exe';ExecutablePath='C:\Filo fixture\codex.exe';CreationDate='initial';CommandLine='app-server --listen ws://127.0.0.1:7436'}
$runtime=Join-Path $fixture 'program'
$supervisor='-File "'+(Join-Path $runtime 'scripts\windows\Run-Standalone.ps1')+'" -Role Host'
Assert (Test-FiloSupervisorCommandLine $supervisor $runtime) 'Fixed installed supervisor is recognized'
Assert (Test-FiloSupervisorCommandLine ($supervisor -replace '"','') $runtime) 'Unquoted supervisor path is recognized'
Assert (-not (Test-FiloSupervisorCommandLine ('-File "'+(Join-Path $fixture 'other\scripts\windows\Run-Standalone.ps1')+'" -Role Host') $runtime)) 'Foreign supervisor path is rejected'
Assert (-not (Test-FiloSupervisorCommandLine ($supervisor -replace 'Run-Standalone.ps1','Run-Standalone.ps1.evil') $runtime)) 'Suffixed script is rejected'
$hostCommand='-File "'+(Join-Path $runtime 'scripts\windows\Run-Standalone.ps1')+'" -Role Host -StateDirectory "'+$fixture+'"'
$parentCommand=$hostCommand
$parent=[pscustomobject]@{ProcessId=600;ExecutablePath=(Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe');CreationDate='parent-initial';CommandLine=$parentCommand}
$taskState='Running'
$ownerSid=$sid
$active=$false
function Get-ScheduledTask { [pscustomobject]@{State=$taskState;Actions=@($action)} }
function Get-NetTCPConnection { [pscustomobject]@{OwningProcess=700} }
function Get-CimInstance { param($ClassName,$Filter) if($Filter -eq 'ProcessId=700'){$script:process}else{$parent.CommandLine=$parentCommand;$parent} }
function Invoke-CimMethod { [pscustomobject]@{ReturnValue=0;Sid=$ownerSid} }
function Stop-ScheduledTask { $script:taskStops++ }
function Stop-Process {
    param($Id,$ErrorAction,[switch]$Force)
    if(-not $Force){throw 'SYSTEM cannot confirm another user process in a non-interactive installer'}
    $signals.Add($Id)
}
try {
    New-Item -ItemType Directory -Path $fixture | Out-Null
    Write-FiloJson (Join-Path $fixture 'launch.json') @{runtimeDirectory=$runtime;appServerUrl='ws://127.0.0.1:7436'}
    Write-FiloJson (Join-Path $fixture 'config.json') @{appServerUrl='ws://127.0.0.1:7436';bind='127.0.0.1';port=7437}
    $probe=Join-Path $fixture 'probe.ps1'
    [IO.File]::WriteAllText($probe,'Write-Output ''{"idle":true}''; $global:LASTEXITCODE=if($active){1}else{0}')
    foreach($case in @('original-desktop','wrong-account','active')) {
        $parentCommand=if($case -eq 'original-desktop'){'Original ChatGPT.exe'}else{$hostCommand}
        $ownerSid=if($case -eq 'wrong-account'){'S-1-5-21-9-9-9-1001'}else{$sid}
        $active=$case -eq 'active'
        $rejected=$false
        try{Get-FiloHostPlan $fixture $probe $sid | Out-Null}catch{$rejected=$true}
        Assert $rejected 'Unverified or active process must be preserved'
        Assert ($signals.Count -eq 0 -and $taskStops -eq 0) 'Preflight never signals a process/task'
    }
    $parentCommand=$hostCommand; $ownerSid=$sid; $active=$false
    Write-FiloJson (Join-Path $fixture 'launch.json') @{runtimeDirectory=$runtime;nodePath='C:\old\node.exe';codexPath='C:\native\codex.exe'}
    $plan=Get-FiloHostPlan $fixture $probe $sid
    Assert (@($plan).Count -eq 1) 'Probe output must not pollute the transaction plan'
    $process=[pscustomobject]@{ProcessId=700;ParentProcessId=600;Name='codex.exe';ExecutablePath='C:\Filo fixture\codex.exe';CreationDate='reused';CommandLine='app-server --listen ws://127.0.0.1:7436'}
    $rejected=$false
    try{Stop-FiloHost $plan}catch{$rejected=$true}
    Assert ($rejected -and $signals.Count -eq 0 -and $taskStops -eq 0) 'PID reuse is rejected before stopping a task'
    $process=$plan.Process
    $parent=[pscustomobject]@{ProcessId=600;ExecutablePath=$plan.Supervisor.ExecutablePath;CreationDate='parent-reused';CommandLine=$parentCommand}
    $rejected=$false
    try{Stop-FiloHost $plan}catch{$rejected=$true}
    Assert ($rejected -and $signals.Count -eq 0 -and $taskStops -eq 0) 'Supervisor PID reuse is rejected before stopping any task'
    $parent=$plan.Supervisor
    $taskState='Ready'
    $plan=Get-FiloHostPlan $fixture $probe $sid
    Assert $plan.WasRunning 'A detached live supervisor remains restorable when its scheduler entry is Ready'
    Stop-FiloHost $plan
    Assert ($signals.Count -eq 2 -and $signals[0] -eq 600 -and $signals[1] -eq 700 -and $taskStops -eq 1) 'Only the verified supervisor and idle child are retired, in that order'
    'PASS: original desktop/account/activity and child/supervisor PID identity protect native processes'
} finally {
    $safe=[IO.Path]::GetFullPath($fixture)
    if(-not $safe.StartsWith([IO.Path]::GetFullPath([IO.Path]::GetTempPath()),[StringComparison]::OrdinalIgnoreCase)){throw 'Unsafe cleanup'}
    if(Test-Path -LiteralPath $safe){Remove-Item -LiteralPath $safe -Recurse -Force}
}
