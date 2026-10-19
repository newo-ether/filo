. (Join-Path $PSScriptRoot 'Service-State.ps1')
. (Join-Path $PSScriptRoot 'Worker-Update.ps1')

function Stop-FiloNewTasks {
    param([string]$Runtime,[string]$State,[string]$UserSid)
    $launch=Get-Content -LiteralPath (Join-Path $State 'launch.json') -Raw | ConvertFrom-Json
    if ([IO.Path]::GetFullPath([string]$launch.runtimeDirectory) -ine [IO.Path]::GetFullPath($Runtime) -or
        [string]$launch.desktopSid -ne $UserSid) { throw 'Candidate helper identity changed before rollback' }
    $task=Get-ScheduledTask -TaskName 'Filo-User-Agent' -ErrorAction SilentlyContinue
    if ($task) {
        # This also identifies a detached gateway whose scheduler entry is Ready.
        # Shutdown fences new mutations and preserves accepted private executors.
        $workerPlan=Get-FiloWorkerUpdatePlan $State ''
        $gateway=$null
        $lease=Join-Path $State 'gateway.pid'
        if (Test-Path -LiteralPath $lease) {
            $gatewayId=0
            if (-not [int]::TryParse([IO.File]::ReadAllText($lease).Trim(),[ref]$gatewayId) -or $gatewayId -le 0) {
                throw 'Invalid candidate gateway identity'
            }
            $gateway=Get-CimInstance Win32_Process -Filter "ProcessId=$gatewayId"
            if ($gateway) {
                $expectation=Get-FiloGatewayExpectation $launch
                if ([IO.Path]::GetFullPath([string]$gateway.ExecutablePath) -ine $expectation.Executable -or
                    ([string]$gateway.CommandLine).IndexOf($expectation.Marker,[StringComparison]::OrdinalIgnoreCase) -lt 0 -or
                    ([string]$gateway.CommandLine).IndexOf([IO.Path]::GetFullPath($State),[StringComparison]::OrdinalIgnoreCase) -lt 0) {
                    throw 'Candidate gateway process identity changed before rollback'
                }
            }
        }
        if ($workerPlan.WasRunning) { Request-FiloWorkerStop $workerPlan }
        else { Wait-FiloWorkerStopped $workerPlan }
        if ($gateway) { Wait-FiloCandidateExit $gateway }
    } else {
        $config=Get-Content -LiteralPath (Join-Path $State 'config.json') -Raw | ConvertFrom-Json
        if ($config.bind -ne '127.0.0.1' -or $config.port -lt 1024 -or $config.port -gt 65535) {
            throw 'Unrecognized candidate gateway endpoint'
        }
        $listeners=@(Get-NetTCPConnection -LocalAddress '127.0.0.1' -LocalPort $config.port -State Listen -ErrorAction SilentlyContinue)
        if ($listeners.Count -or (Test-Path -LiteralPath (Join-Path $State 'gateway.pid'))) {
            throw 'Candidate gateway has no verifiable task registration; configuration was preserved'
        }
    }
    # Only the separately identified, verified-idle metadata host may be retired.
    # Never infer permission to kill native children from a command-line match.
    $hostPlan=Get-FiloHostPlan $State (Join-Path $Runtime 'tools\filo.exe') $UserSid
    Stop-FiloHost $hostPlan
}

function Wait-FiloCandidateExit {
    param($Process)
    $deadline=[DateTime]::UtcNow.AddSeconds(50)
    do {
        $current=Get-CimInstance Win32_Process -Filter "ProcessId=$($Process.ProcessId)"
        # A reused PID proves the candidate exited. It grants no authority over
        # the replacement process, which must never receive a signal.
        if (-not $current -or $current.CreationDate -ne $Process.CreationDate -or
            $current.ExecutablePath -ine $Process.ExecutablePath) { return }
        Start-Sleep -Milliseconds 200
    } while ([DateTime]::UtcNow -lt $deadline)
    throw 'Candidate gateway process did not exit; configuration was preserved'
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
    $workerStopAttempted=$false; $hostStopAttempted=$false; $serviceStopAttempted=$false; $configurationChanged=$false
    try {
        $runtime=Install-FiloRuntime $Bundle $Manifest $Root $Client.Sid
        $filo=Join-Path $runtime 'tools\filo.exe'
        # Reject an unrecognized or active installation before interrupting it.
        if (Test-Path -LiteralPath (Join-Path $Worker 'launch.json')) {
            $workerPlan=Get-FiloWorkerUpdatePlan $Worker ('0' * 40)
            $hostPlan=Get-FiloHostPlan $Worker $filo $Client.Sid
        } elseif (@($taskSnapshots.Values | Where-Object { $_ }).Count) { throw 'Unrecognized Filo tasks; existing installation preserved' }
        if ($workerPlan -and $workerPlan.WasRunning) {
            $workerStopAttempted=$true
            Request-FiloWorkerStop $workerPlan
        }
        if ($hostPlan) {
            # The gateway fence can race with a final admission; verify idle again.
            $hostPlan=Get-FiloHostPlan $Worker $filo $Client.Sid
            $hostStopAttempted=$true
            Stop-FiloHost $hostPlan
        }
        if ($service -and $service.State -ne 'Stopped') {
            $serviceStopAttempted=$true
            Stop-Service -Name Filo
            (Get-Service -Name Filo).WaitForStatus('Stopped',[TimeSpan]::FromSeconds(20))
        }
        Write-Host '2/4  Prepare Filo private state and ordinary-user helpers' -ForegroundColor Cyan
        Protect-FiloDirectory $State
        Protect-FiloDirectory $Worker $Client.Sid
        New-FiloToken (Join-Path $State 'token')
        New-FiloToken (Join-Path $Worker 'token')
        if (-not (Test-Path -LiteralPath $wrapper)) { Copy-Item -LiteralPath (Join-Path $runtime 'tools\nssm.exe') -Destination $wrapper }
        $workspace=if ($workerPlan) { $workerPlan.Launch.workspaceDirectory } else { $Client.Profile }
        $launch=@{runtimeDirectory=$runtime;revision=$Manifest.revision;
            desktopSid=$Client.Sid;workspaceDirectory=$workspace;appServerUrl=$endpoints.NativeUrl}
        $configurationChanged=$true
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
        try { if ($tasksChanged) { Stop-FiloNewTasks $runtime $Worker $Client.Sid } }
        catch { $rollbackErrors.Add("Stop candidate tasks: $($_.Exception.Message)") }
        try {
            if ($serviceChanged -and (Get-Service -Name Filo -ErrorAction SilentlyContinue)) {
                Stop-Service -Name Filo
                (Get-Service -Name Filo).WaitForStatus('Stopped',[TimeSpan]::FromSeconds(20))
            }
        }
        catch { $rollbackErrors.Add("Stop candidate service: $($_.Exception.Message)") }
        if ($rollbackErrors.Count) {
            # A live candidate must keep its matching launch/configuration and task
            # identity. Restoring the old files here would cross the idle fence.
            throw "$($failure.Exception.Message). Rollback needs attention; candidate configuration was preserved: $($rollbackErrors -join '; ')"
        }
        foreach ($file in @($snapshots.Keys | Where-Object { $configurationChanged })) {
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
        foreach ($entry in @(@{Name='Filo-Standalone-Host';Plan=$hostPlan;Attempted=$hostStopAttempted},@{Name='Filo-User-Agent';Plan=$workerPlan;Attempted=$workerStopAttempted})) {
            if ($entry.Attempted -and $entry.Plan -and $entry.Plan.WasRunning) {
                try {
                    Start-ScheduledTask -TaskName $entry.Name
                    if ($entry.Name -eq 'Filo-User-Agent') {
                        Wait-FiloEndpoint $endpoints.WorkerUrl (Join-Path $Worker 'token') 'standalone' -Seconds 45
                    }
                }
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
            } elseif ($serviceStopAttempted -and $serviceWasRunning) { Start-Service -Name Filo }
        } catch { $rollbackErrors.Add("Restore service: $($_.Exception.Message)") }
        if ($rollbackErrors.Count) { throw "$($failure.Exception.Message). Rollback needs attention: $($rollbackErrors -join '; ')" }
        throw $failure
    }
}
