. (Join-Path $PSScriptRoot 'Install-Support.ps1')
# Upgrade only the identified Filo auxiliary. Original desktop/backends are never targeted.
function Get-FiloHostPlan {
    param([string]$StateDirectory, [string]$FiloPath, [string]$UserSid)
    $task=Get-ScheduledTask -TaskName 'Filo-Standalone-Host' -ErrorAction SilentlyContinue
    if (-not $task) { return $null }
    $endpoint=(Get-FiloHelperEndpoints $StateDirectory).NativeUrl
    if ($endpoint -notmatch '^ws://127\.0\.0\.1:([0-9]+)$') { throw 'Unrecognized auxiliary endpoint' }
    $port=[int]$Matches[1]
    if ($port -lt 1024 -or $port -gt 65535) { throw 'Invalid auxiliary port' }
    $action=$task.Actions[0]
    if ([string]$action.Arguments -notmatch '-Role Host' -or
        ([string]$action.Arguments).IndexOf($StateDirectory,[StringComparison]::OrdinalIgnoreCase) -lt 0) {
        throw 'Existing host task is not the selected Filo auxiliary'
    }
    $peer=@(Get-NetTCPConnection -LocalAddress '127.0.0.1' -LocalPort $port -State Listen -ErrorAction SilentlyContinue)
    $process=$null
    if ($peer.Count) {
        if ($peer.Count -ne 1) { throw 'Ambiguous auxiliary listener' }
        $process=Get-CimInstance Win32_Process -Filter "ProcessId=$($peer[0].OwningProcess)"
        $parent=Get-CimInstance Win32_Process -Filter "ProcessId=$($process.ParentProcessId)"
        $owner=Invoke-CimMethod -InputObject $process -MethodName GetOwnerSid
        if ($owner.ReturnValue -ne 0 -or $owner.Sid -ne $UserSid -or
            $process.Name -ine 'codex.exe' -or
            ([string]$process.CommandLine).IndexOf($endpoint,[StringComparison]::OrdinalIgnoreCase) -lt 0 -or
            ([string]$parent.CommandLine).IndexOf($StateDirectory,[StringComparison]::OrdinalIgnoreCase) -lt 0 -or
            [string]$parent.CommandLine -notmatch '-Role Host') { throw 'Auxiliary ownership could not be verified' }
        & $FiloPath 'probe-host-idle' $endpoint | Out-Null
        if ($LASTEXITCODE -ne 0) { throw 'Native auxiliary is not verified idle; no process was stopped' }
    } elseif ($task.State -eq 'Running') { throw 'Auxiliary is still starting; retry after it settles' }
    [pscustomobject]@{Task=$task;Action=$action;Process=$process;WasRunning=($task.State -eq 'Running');Changed=$false}
}

function Stop-FiloHost {
    param($Plan)
    if (-not $Plan) { return }
    $task=Get-ScheduledTask -TaskName 'Filo-Standalone-Host'
    if ($task.Actions[0].Execute -ne $Plan.Action.Execute -or $task.Actions[0].Arguments -ne $Plan.Action.Arguments) {
        throw 'Auxiliary task changed during installation'
    }
    if ($Plan.Process) {
        $current=Get-CimInstance Win32_Process -Filter "ProcessId=$($Plan.Process.ProcessId)"
        if ($current -and ($current.CreationDate -ne $Plan.Process.CreationDate -or $current.ExecutablePath -ine $Plan.Process.ExecutablePath)) {
            throw 'Auxiliary process identity changed before shutdown'
        }
    }
    if ($Plan.WasRunning) { Stop-ScheduledTask -TaskName 'Filo-Standalone-Host' }
    if ($Plan.Process) {
        $current=Get-CimInstance Win32_Process -Filter "ProcessId=$($Plan.Process.ProcessId)"
        if ($current) {
            if ($current.CreationDate -ne $Plan.Process.CreationDate -or $current.ExecutablePath -ine $Plan.Process.ExecutablePath) {
                throw 'Auxiliary process identity changed during shutdown'
            }
            # This exact idle Filo child was verified above. Never kill by executable name/tree.
            Stop-Process -Id $current.ProcessId -Force -ErrorAction Stop
        }
    }
    $Plan.Changed=$true
}

function Restore-FiloHost {
    param($Plan)
    if ($Plan -and $Plan.Changed) {
        Set-ScheduledTask -TaskName 'Filo-Standalone-Host' -Action $Plan.Action | Out-Null
        if ($Plan.WasRunning) { Start-ScheduledTask -TaskName 'Filo-Standalone-Host' }
    }
}
