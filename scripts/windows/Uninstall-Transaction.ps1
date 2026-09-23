. (Join-Path $PSScriptRoot 'Service-State.ps1')

function Invoke-FiloUninstall {
    param([string]$Bundle,[string]$Root,[string]$State,[string]$Worker,[string]$Sid)
    $wrapper=Join-Path $Root 'nssm.exe'
    $serviceScript=Join-Path $Bundle 'scripts\system-service.ps1'
    $service=Get-CimInstance Win32_Service -Filter "Name='Filo'"
    if ($service -and ($service.StartName -ne 'LocalSystem' -or $service.PathName.Trim('"') -ine $wrapper)) {
        throw 'Existing service identity is not the Filo SYSTEM gateway; it was preserved'
    }
    $parameters=if ($service) { Get-ItemProperty -LiteralPath 'HKLM:\SYSTEM\CurrentControlSet\Services\Filo\Parameters' }
    $serviceWasRunning=Test-FiloServiceShouldRun $service
    $snapshots=@{}
    foreach ($name in @('Filo-User-Agent','Filo-Standalone-Host')) {
        $task=Get-ScheduledTask -TaskName $name -ErrorAction SilentlyContinue
        if ($task) { $snapshots[$name]=Export-ScheduledTask -TaskName $name }
    }
    $workerPlan=$null; $hostPlan=$null; $tasksChanged=$false; $serviceChanged=$false
    try {
        if ($service -and $service.State -ne 'Stopped') {
            Stop-Service -Name Filo
            (Get-Service -Name Filo).WaitForStatus('Stopped',[TimeSpan]::FromSeconds(20))
        }
        $workerPlan=Get-FiloWorkerUpdatePlan $Worker ('0' * 40)
        if ($workerPlan -and $workerPlan.WasRunning) { Request-FiloWorkerStop $workerPlan }
        $hostPlan=Get-FiloHostPlan $Worker (Join-Path $Bundle 'tools\filo.exe') $Sid
        Stop-FiloHost $hostPlan
        $tasksChanged=$true
        foreach ($name in $snapshots.Keys) { Unregister-ScheduledTask -TaskName $name -Confirm:$false }
        # Delete the service last: no fallible task mutation follows successful deletion.
        $serviceChanged=$true
        & $serviceScript -Action Uninstall -StateDirectory $State -WrapperPath $wrapper
        Write-Output 'Filo removed. Original Codex and retained user data are unchanged.'
    } catch {
        $failure=$_
        $rollbackErrors=[Collections.Generic.List[string]]::new()
        if ($tasksChanged) {
            foreach ($name in $snapshots.Keys) {
                try { Register-ScheduledTask -TaskName $name -Xml $snapshots[$name] -Force | Out-Null }
                catch { $rollbackErrors.Add("Restore ${name}: $($_.Exception.Message)") }
            }
        }
        foreach ($entry in @(@{Name='Filo-Standalone-Host';Plan=$hostPlan},@{Name='Filo-User-Agent';Plan=$workerPlan})) {
            if ($entry.Plan -and $entry.Plan.WasRunning) {
                try { Start-ScheduledTask -TaskName $entry.Name }
                catch { $rollbackErrors.Add("Restart $($entry.Name): $($_.Exception.Message)") }
            }
        }
        try {
            if ($serviceChanged -and $service -and -not (Get-Service -Name Filo -ErrorAction SilentlyContinue)) {
                # The removed registration may predate this release, so recreate the
                # service and reapply the recorded values instead of rebuilding them.
                $restore=@{Action='Restore';StateDirectory=$State;WrapperPath=$wrapper;
                    RestoreApplication=$parameters.Application;
                    RestoreAppDirectory=$parameters.AppDirectory;
                    RestoreAppParameters=$parameters.AppParameters}
                if ($serviceWasRunning) { $restore.RestoreStart=$true }
                & $serviceScript @restore
            } elseif ($serviceWasRunning) { Start-Service -Name Filo }
        } catch { $rollbackErrors.Add("Restore service: $($_.Exception.Message)") }
        if ($rollbackErrors.Count) { throw "$($failure.Exception.Message). Rollback needs attention: $($rollbackErrors -join '; ')" }
        throw $failure
    }
}
