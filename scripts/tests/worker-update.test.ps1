$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot '..\windows\Install-Support.ps1')
. (Join-Path $PSScriptRoot '..\windows\Worker-Update.ps1')
function Assert($Value, $Message) { if (-not $Value) { throw $Message } }
$fixture = Join-Path ([IO.Path]::GetTempPath()) ('filo-worker-update-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $fixture | Out-Null
$script:action = [pscustomobject]@{Execute='powershell.exe';Arguments='old-gateway';WorkingDirectory=$fixture}
$script:taskState = 'Running'; $script:capable = $false; $script:shutdowns = 0; $script:starts = 0
$script:listeners = @(); $script:gateway = $null
$script:taskNames = [Collections.Generic.List[string]]::new()
function Get-ScheduledTask { param($TaskName) $script:taskNames.Add($TaskName); return [pscustomobject]@{State=$script:taskState;Actions=@($script:action)} }
function New-ScheduledTaskAction { param($Execute,$Argument,$WorkingDirectory) return [pscustomobject]@{Execute=$Execute;Arguments=$Argument;WorkingDirectory=$WorkingDirectory} }
function Set-ScheduledTask { param($TaskName,$Action) $script:taskNames.Add($TaskName); $script:action=$Action }
function Start-ScheduledTask { param($TaskName) $script:taskNames.Add($TaskName); $script:starts++; $script:taskState='Running' }
function Get-NetTCPConnection { param($LocalAddress,$LocalPort,$State,$ErrorAction) return $script:listeners }
function Get-CimInstance { param($ClassName,$Filter) return $script:gateway }
function Wait-FiloWorkerStopped {
    param($Plan)
    Assert ($script:taskState -eq 'Ready') 'Worker still running'
    Assert ($Plan.Url -eq 'http://127.0.0.1:28437') 'Shutdown waits for the configured worker port'
}
function Wait-FiloEndpoint {
    param($Url,$TokenPath,$Mode)
    Assert ($script:taskState -eq 'Running') 'Worker not ready'
    Assert ($Url -eq 'http://127.0.0.1:28437') 'Readiness preserves the configured worker port'
}
function Invoke-RestMethod {
    param($Uri,$Method,$Headers,$TimeoutSec,[switch]$DisableKeepAlive)
    Assert ($Uri.StartsWith('http://127.0.0.1:28437/')) 'Worker calls use the configured endpoint'
    Assert ($Headers.Authorization -eq ('Bearer ' + ('a' * 64))) 'Authentication is required'
    if ($Uri.EndsWith('/v1/info')) { return [pscustomobject]@{supportsSafeShutdown=$script:capable} }
    Assert ($Method -eq 'Post') 'Shutdown requires POST'
    $script:shutdowns++; $script:taskState='Ready'; return [pscustomobject]@{stopped=$true}
}
try {
    $newRuntime=Join-Path $fixture 'new-runtime'
    New-Item -ItemType Directory -Path (Join-Path $newRuntime 'tools') | Out-Null
    [IO.File]::WriteAllText((Join-Path $newRuntime 'tools\FiloBackground.exe'),'fixture - never executed')
    [IO.File]::WriteAllText((Join-Path $fixture 'token'), ('a' * 64))
    Write-FiloJson (Join-Path $fixture 'config.json') @{bind='127.0.0.1';port=28437}
    Write-FiloJson (Join-Path $fixture 'launch.json') @{revision=('a' * 40);nodePath='old-node';runtimeDirectory='old-runtime';codexPath='original-native';workspaceDirectory=$fixture}
    $rejected=$false
    try { Get-FiloWorkerUpdatePlan $fixture ('b' * 40) | Out-Null } catch { $rejected=$true }
    Assert $rejected 'Legacy worker must fail before any service or native action'
    Assert ($script:shutdowns -eq 0 -and $script:starts -eq 0) 'Legacy rejection must not mutate'
    $script:capable=$true
    $plan=Get-FiloWorkerUpdatePlan $fixture ('b' * 40)
    Start-FiloWorkerUpdate $plan $newRuntime ('b' * 40)
    $next=Get-Content -LiteralPath (Join-Path $fixture 'launch.json') -Raw | ConvertFrom-Json
    Assert ($next.revision -eq ('b' * 40) -and $next.codexPath -eq 'original-native') 'Upgrade preserves native executable'
    Assert (-not $next.PSObject.Properties['nodePath']) 'Upgrade drops the retired Node entry point'
    Assert ((Get-FiloGatewayExpectation $next).Marker -eq 'standalone') 'The migrated record expects the Go gateway'
    Assert ($script:shutdowns -eq 1 -and $script:starts -eq 1) 'One graceful replacement'
    Assert ($script:action.Execute -eq (Join-Path $newRuntime 'tools\FiloBackground.exe') -and $script:action.Arguments.Contains('-Role Gateway')) 'New gateway uses the GUI entry in the staged runtime'
    Restore-FiloWorkerUpdate $plan
    $previous=Get-Content -LiteralPath (Join-Path $fixture 'launch.json') -Raw | ConvertFrom-Json
    Assert ($previous.revision -eq ('a' * 40) -and $script:action.Arguments -eq 'old-gateway') 'Rollback restores launch file and task action'
    Assert ($script:shutdowns -eq 2 -and $script:starts -eq 2) 'Rollback restarts the prior gateway once'
    Assert (@($script:taskNames | Where-Object { $_ -ne 'Filo-User-Agent' }).Count -eq 0) 'The native host task must never be changed'
    Assert ([IO.File]::ReadAllText((Join-Path $fixture 'token')) -eq ('a' * 64)) 'Credentials must remain identical'

    $oldNode = Join-Path $fixture 'old-node.exe'
    $oldRuntime = Join-Path $fixture 'old-runtime'
    Write-FiloJson (Join-Path $fixture 'launch.json') @{revision=('a' * 40);nodePath=$oldNode;runtimeDirectory=$oldRuntime;codexPath='original-native';workspaceDirectory=$fixture}
    [IO.File]::WriteAllText((Join-Path $fixture 'gateway.pid'), '7331')
    $script:taskState = 'Ready'
    $script:listeners = @([pscustomobject]@{OwningProcess=7331})
    $script:gateway = [pscustomobject]@{ExecutablePath=$oldNode;CommandLine=('"' + $oldNode + '" "' + (Join-Path $oldRuntime 'dist\packages\service\src\standalone.js') + '" "' + $fixture + '"')}
    $orphan = Get-FiloWorkerUpdatePlan $fixture ('b' * 40)
    Assert $orphan.WasRunning 'A fully identified orphan worker remains eligible for graceful replacement'
    Start-FiloWorkerUpdate $orphan $newRuntime ('b' * 40)
    Assert ($script:shutdowns -eq 3 -and $script:starts -eq 3) 'The identified orphan is shut down through its authenticated endpoint'
    Restore-FiloWorkerUpdate $orphan
    Assert ($script:shutdowns -eq 4 -and $script:starts -eq 4) 'Orphan rollback restarts the previous worker'

    $script:taskState = 'Ready'
    $script:listeners = @([pscustomobject]@{OwningProcess=7332})
    $rejected = $false
    try { Get-FiloWorkerUpdatePlan $fixture ('b' * 40) | Out-Null } catch { $rejected = $true }
    Assert $rejected 'A listener that does not match the private lease must be rejected'
    Assert ($script:shutdowns -eq 4 -and $script:starts -eq 4) 'Foreign-listener rejection must not mutate'

    Write-FiloJson (Join-Path $fixture 'launch.json') @{revision=('a' * 40);runtimeDirectory=$newRuntime;codexPath='original-native';workspaceDirectory=$fixture}
    [IO.File]::WriteAllText((Join-Path $fixture 'gateway.pid'), '7333')
    $script:taskState = 'Ready'
    $script:listeners = @([pscustomobject]@{OwningProcess=7333})
    $goGateway = Join-Path $newRuntime 'tools\filo.exe'
    $script:gateway = [pscustomobject]@{ExecutablePath=$goGateway;CommandLine=('"' + $goGateway + '" standalone "' + $fixture + '"')}
    $migrated = Get-FiloWorkerUpdatePlan $fixture ('b' * 40)
    Assert $migrated.WasRunning 'A Go gateway left by a stopped task is identified by its binary and role'
    Assert ($script:shutdowns -eq 4 -and $script:starts -eq 4) 'Identifying the Go gateway must not mutate'

    $script:listeners = @([pscustomobject]@{OwningProcess=7334})
    $rejected = $false
    try { Get-FiloWorkerUpdatePlan $fixture ('b' * 40) | Out-Null } catch { $rejected = $true }
    Assert $rejected 'A Go-shaped listener that does not match the private lease must be rejected'
    Assert ($script:shutdowns -eq 4 -and $script:starts -eq 4) 'Go-listener rejection must not mutate'
    Assert ($null -eq (Get-FiloWorkerUpdatePlan $fixture ('a' * 40))) 'Matching runtime is not restarted'
} finally {
    $safe=Assert-FiloPrivatePath $fixture ([IO.Path]::GetTempPath())
    Remove-Item -LiteralPath $safe -Recurse -Force
}
Write-Output 'PASS: legacy rejection, guarded user gateway upgrade and rollback preserve the original native host'
