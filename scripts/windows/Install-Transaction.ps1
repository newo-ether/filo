. (Join-Path $PSScriptRoot 'Service-State.ps1')

function Stop-FiloNewTasks {
    param([string]$Runtime,[string]$State)
    $processes=@(Get-CimInstance Win32_Process)
    $runners=@($processes | Where-Object {
        ([string]$_.CommandLine).IndexOf($Runtime,[StringComparison]::OrdinalIgnoreCase) -ge 0 -and
        ([string]$_.CommandLine).IndexOf($State,[StringComparison]::OrdinalIgnoreCase) -ge 0 -and
        [string]$_.CommandLine -match '-Role Host' })
    $runnerIds=@($runners | ForEach-Object { $_.ProcessId })
    $owned=@($processes | Where-Object { $_.Name -ieq 'codex.exe' -and $_.ParentProcessId -in $runnerIds })
    foreach ($name in @('Filo-User-Agent','Filo-Standalone-Host')) {
        $task=Get-ScheduledTask -TaskName $name -ErrorAction SilentlyContinue
        if ($task -and $task.State -eq 'Running') { Stop-ScheduledTask -TaskName $name }
    }
    foreach ($process in $owned) {
        $current=Get-CimInstance Win32_Process -Filter "ProcessId=$($process.ProcessId)"
        if ($current -and $current.CreationDate -eq $process.CreationDate -and $current.ExecutablePath -ieq $process.ExecutablePath) {
            Stop-Process -Id $current.ProcessId -Force -ErrorAction Stop
        }
    }
}

function Invoke-FiloInstall {
    param([string]$Bundle,$Manifest,[string]$Root,[string]$State,[string]$Worker,$Client,[string]$Bind,[int]$Port)
    Write-Host '1/4  Verify Filo and preserve the current installation' -ForegroundColor Cyan
    $configPath=Join-Path $State 'config.json'
    $endpoints=Get-FiloHelperEndpoints $Worker
    $wrapper=Join-Path $Root 'nssm.exe'
    $service=Get-CimInstance Win32_Service -Filter "Name='Filo'"
    if ($service -and ($service.StartName -ne 'LocalSystem' -or $service.PathName.Trim('"') -ine $wrapper)) {
        throw 'Existing service identity is not the Filo SYSTEM gateway; it was preserved'
    }
    $previousParameters=if ($service) { Get-ItemProperty -LiteralPath 'HKLM:\SYSTEM\CurrentControlSet\Services\Filo\Parameters' }
    $serviceWasRunning=Test-FiloServiceShouldRun $service
    $snapshots=@{}
    foreach ($file in @($configPath,(Join-Path $Worker 'config.json'),(Join-Path $Worker 'launch.json'))) {
        $snapshots[$file]=if (Test-Path -LiteralPath $file) { [IO.File]::ReadAllBytes($file) } else { $null }
    }
    $taskSnapshots=@{}
    foreach ($name in @('Filo-Standalone-Host','Filo-User-Agent')) {
        $task=Get-ScheduledTask -TaskName $name -ErrorAction SilentlyContinue
        $taskSnapshots[$name]=if ($task) { Export-ScheduledTask -TaskName $name } else { $null }
    }
    $workerPlan=$null; $hostPlan=$null; $tasksChanged=$false; $serviceChanged=$false; $runtime=$null
    try {
        $runtime=Install-FiloRuntime $Bundle $Manifest $Root $Client.Sid
        $filo=Join-Path $runtime 'tools\filo.exe'
        if ($service -and $service.State -ne 'Stopped') {
            Stop-Service -Name Filo
            (Get-Service -Name Filo).WaitForStatus('Stopped',[TimeSpan]::FromSeconds(20))
        }
        if (Test-Path -LiteralPath (Join-Path $Worker 'launch.json')) {
            $workerPlan=Get-FiloWorkerUpdatePlan $Worker ('0' * 40)
            if ($workerPlan -and $workerPlan.WasRunning) { Request-FiloWorkerStop $workerPlan }
            $hostPlan=Get-FiloHostPlan $Worker $filo $Client.Sid
            Stop-FiloHost $hostPlan
        } elseif (@($taskSnapshots.Values | Where-Object { $_ }).Count) { throw 'Unrecognized Filo tasks; existing installation preserved' }
        Write-Host '2/4  Prepare Filo private state and ordinary-user helpers' -ForegroundColor Cyan
        Protect-FiloDirectory $State
        Protect-FiloDirectory $Worker $Client.Sid
        New-FiloToken (Join-Path $State 'token')
        New-FiloToken (Join-Path $Worker 'token')
        if (-not (Test-Path -LiteralPath $wrapper)) { Copy-Item -LiteralPath (Join-Path $runtime 'tools\nssm.exe') -Destination $wrapper }
        $workspace=if ($workerPlan) { $workerPlan.Launch.workspaceDirectory } else { $Client.Profile }
        $launch=@{runtimeDirectory=$runtime;revision=$Manifest.revision;
            desktopSid=$Client.Sid;workspaceDirectory=$workspace;appServerUrl=$endpoints.NativeUrl}
        Write-FiloJson (Join-Path $Worker 'launch.json') $launch
        Write-FiloJson (Join-Path $Worker 'config.json') @{appServerUrl=$launch.appServerUrl;bind='127.0.0.1';port=$endpoints.WorkerPort;desktopSid=$Client.Sid}
        Write-FiloJson $configPath @{bind=$Bind;port=$Port;desktopSid=$Client.Sid;
            workerUrl=$endpoints.WorkerUrl;workerTokenPath=(Join-Path $Worker 'token')}
        $tasksChanged=$true
        Register-FiloUserTask 'Filo-Standalone-Host' (New-FiloTaskAction $runtime $Worker Host $workspace) $Client.Sid
        Register-FiloUserTask 'Filo-User-Agent' (New-FiloTaskAction $runtime $Worker Gateway $workspace) $Client.Sid
        $profile=Get-CimInstance Win32_UserProfile | Where-Object SID -eq $Client.Sid
        if ($profile.Loaded) {
            foreach ($name in @('Filo-Standalone-Host','Filo-User-Agent')) { Start-ScheduledTask -TaskName $name }
            Wait-FiloEndpoint $endpoints.WorkerUrl (Join-Path $Worker 'token') 'standalone' -Seconds 45
        }
        Write-Host '3/4  Install the independent SYSTEM gateway' -ForegroundColor Cyan
        $serviceChanged=$true
        & (Join-Path $runtime 'scripts\system-service.ps1') -Action Install -RuntimeDirectory $runtime -StateDirectory $State -FiloPath $filo -WrapperPath $wrapper
        Write-Host '4/4  Verify authenticated service readiness' -ForegroundColor Cyan
        Wait-FiloEndpoint ("http://" + $Bind + ':' + $Port) (Join-Path $State 'token') 'existing' -FiloPath $filo
        Write-Output "Filo installed for $($Client.Name): http://${Bind}:$Port"
        Write-Output "Access token: $State\token. No separate Codex CLI is needed."
    } catch {
        $failure=$_
        $rollbackErrors=[Collections.Generic.List[string]]::new()
        # Stop only newly admitted Filo resources. The original desktop has never been a child.
        try { if ($tasksChanged) { Stop-FiloNewTasks $runtime $Worker } }
        catch { $rollbackErrors.Add("Stop candidate tasks: $($_.Exception.Message)") }
        try { if ($serviceChanged -and (Get-Service -Name Filo -ErrorAction SilentlyContinue)) { Stop-Service -Name Filo } }
        catch { $rollbackErrors.Add("Stop candidate service: $($_.Exception.Message)") }
        foreach ($file in $snapshots.Keys) {
            try {
                if ($null -ne $snapshots[$file]) { [IO.File]::WriteAllBytes($file,$snapshots[$file]) }
                elseif (Test-Path -LiteralPath $file) { Remove-Item -LiteralPath $file -Force }
            } catch { $rollbackErrors.Add("Restore configuration: $($_.Exception.Message)") }
        }
        if ($tasksChanged) {
            foreach ($name in $taskSnapshots.Keys) {
                try {
                    if ($taskSnapshots[$name]) { Register-ScheduledTask -TaskName $name -Xml $taskSnapshots[$name] -Force | Out-Null }
                    elseif (Get-ScheduledTask -TaskName $name -ErrorAction SilentlyContinue) { Unregister-ScheduledTask -TaskName $name -Confirm:$false }
                } catch { $rollbackErrors.Add("Restore ${name}: $($_.Exception.Message)") }
            }
        }
        foreach ($entry in @(@{Name='Filo-Standalone-Host';Plan=$hostPlan},@{Name='Filo-User-Agent';Plan=$workerPlan})) {
            if ($entry.Plan -and $entry.Plan.WasRunning) {
                try { Start-ScheduledTask -TaskName $entry.Name }
                catch { $rollbackErrors.Add("Restart $($entry.Name): $($_.Exception.Message)") }
            }
        }
        try {
            if ($serviceChanged) {
                if ($service) {
                    # This release installs over a Node registration, so it cannot rebuild
                    # the previous command line from its own rules. Restore what was recorded.
                    $restore=@{Action='Restore';StateDirectory=$State;WrapperPath=$wrapper;
                        RestoreApplication=$previousParameters.Application;
                        RestoreAppDirectory=$previousParameters.AppDirectory;
                        RestoreAppParameters=$previousParameters.AppParameters}
                    if ($serviceWasRunning) { $restore.RestoreStart=$true }
                    & (Join-Path $runtime 'scripts\system-service.ps1') @restore
                } else {
                    & (Join-Path $runtime 'scripts\system-service.ps1') -Action Uninstall -StateDirectory $State -WrapperPath $wrapper
                }
            } elseif ($serviceWasRunning) { Start-Service -Name Filo }
        } catch { $rollbackErrors.Add("Restore service: $($_.Exception.Message)") }
        if ($rollbackErrors.Count) { throw "$($failure.Exception.Message). Rollback needs attention: $($rollbackErrors -join '; ')" }
        throw $failure
    }
}
