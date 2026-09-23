Set-StrictMode -Version 2
. (Join-Path $PSScriptRoot 'Installer-Tasks.ps1')

# A launch record names one gateway shape. The Go delivery ships tools\filo.exe
# beside the scripts; a record that still carries nodePath predates that migration.
function Get-FiloGatewayExpectation {
    param($Launch)
    $runtime = [IO.Path]::GetFullPath([string]$Launch.runtimeDirectory)
    if ($Launch.PSObject.Properties['nodePath'] -and $Launch.nodePath) {
        return [pscustomobject]@{
            Executable = [IO.Path]::GetFullPath([string]$Launch.nodePath)
            Marker = [IO.Path]::GetFullPath((Join-Path $runtime 'dist\packages\service\src\standalone.js'))
        }
    }
    return [pscustomobject]@{
        Executable = [IO.Path]::GetFullPath((Join-Path $runtime 'tools\filo.exe'))
        Marker = 'standalone'
    }
}

# Only Filo's user gateway is replaceable here. The independent native host task is never stopped.
function Get-FiloWorkerUpdatePlan {
    param([string]$StateDirectory, [string]$Revision)
    $launchPath = Join-Path $StateDirectory 'launch.json'
    $launch = Get-Content -LiteralPath $launchPath -Raw | ConvertFrom-Json
    if ($launch.revision -eq $Revision) { return $null }
    $task = Get-ScheduledTask -TaskName 'Filo-User-Agent'
    $running = $task.State -eq 'Running'
    $tokenPath = Join-Path $StateDirectory 'token'
    $config = Get-Content -LiteralPath (Join-Path $StateDirectory 'config.json') -Raw | ConvertFrom-Json
    if ($config.bind -ne '127.0.0.1' -or ($config.port -isnot [int] -and $config.port -isnot [long]) -or
        $config.port -lt 1024 -or $config.port -gt 65535) { throw 'Unrecognized user gateway endpoint' }
    $url = 'http://127.0.0.1:' + $config.port
    if (-not $running) {
        $listeners = @(Get-NetTCPConnection -LocalAddress $config.bind -LocalPort $config.port `
            -State Listen -ErrorAction SilentlyContinue)
        $lease = Join-Path $StateDirectory 'gateway.pid'
        if ($listeners.Count) {
            $gatewayId = 0
            if (-not (Test-Path -LiteralPath $lease) -or
                -not [int]::TryParse([IO.File]::ReadAllText($lease).Trim(), [ref]$gatewayId) -or
                @($listeners | Where-Object OwningProcess -ne $gatewayId).Count) {
                throw 'The stopped user gateway task does not own its occupied port'
            }
            $gateway = Get-CimInstance Win32_Process -Filter "ProcessId=$gatewayId"
            $expectation = Get-FiloGatewayExpectation $launch
            $commandLine = [string]$gateway.CommandLine
            if (-not $gateway -or [IO.Path]::GetFullPath([string]$gateway.ExecutablePath) -ine $expectation.Executable -or
                $commandLine.IndexOf($expectation.Marker, [StringComparison]::OrdinalIgnoreCase) -lt 0 -or
                $commandLine.IndexOf([IO.Path]::GetFullPath($StateDirectory), [StringComparison]::OrdinalIgnoreCase) -lt 0) {
                throw 'The stopped user gateway task has an unrecognized listener'
            }
            $running = $true
        } elseif (Test-Path -LiteralPath $lease) {
            $gatewayId = 0
            if (-not [int]::TryParse([IO.File]::ReadAllText($lease).Trim(), [ref]$gatewayId) -or
                (Get-Process -Id $gatewayId -ErrorAction SilentlyContinue)) { throw 'A user gateway lease is still active or invalid' }
        }
    }
    if ($running) {
        $headers = @{Authorization='Bearer ' + [IO.File]::ReadAllText($tokenPath).Trim()}
        $info = Invoke-RestMethod -Uri ($url + '/v1/info') -Headers $headers -TimeoutSec 5 -DisableKeepAlive
        if (-not $info.PSObject.Properties['supportsSafeShutdown'] -or -not $info.supportsSafeShutdown) {
            throw 'This older Filo worker cannot verify safe shutdown. Complete its one-time idle migration before upgrading; no native process was stopped.'
        }
    }
    return [pscustomobject]@{StateDirectory=$StateDirectory;Launch=$launch;Action=$task.Actions[0];WasRunning=$running;Url=$url;Changed=$false}
}
function Wait-FiloWorkerStopped {
    param($Plan)
    $port = ([uri]$Plan.Url).Port
    $deadline = [DateTime]::UtcNow.AddSeconds(50)
    do {
        $task = Get-ScheduledTask -TaskName 'Filo-User-Agent'
        $listeners = [Net.NetworkInformation.IPGlobalProperties]::GetIPGlobalProperties().GetActiveTcpListeners()
        if ($task.State -ne 'Running' -and -not @($listeners | Where-Object {
            $_.Port -eq $port -and $_.Address.ToString() -eq '127.0.0.1'
        }).Count) { return }
        Start-Sleep -Milliseconds 200
    } while ([DateTime]::UtcNow -lt $deadline)
    throw 'Filo user gateway did not stop; the native host was preserved'
}
function Request-FiloWorkerStop {
    param($Plan)
    $headers = @{Authorization='Bearer ' + [IO.File]::ReadAllText((Join-Path $Plan.StateDirectory 'token')).Trim()}
    try {
        $result = Invoke-RestMethod -Uri ($Plan.Url + '/v1/shutdown') -Method Post -Headers $headers -TimeoutSec 40 -DisableKeepAlive
        if (-not $result.stopped) { throw 'Filo user gateway shutdown was not confirmed' }
    } catch {
        if ($_.Exception.PSObject.Properties['Response'] -and $_.Exception.Response -and
            [int]$_.Exception.Response.StatusCode -eq 409) { throw }
        # A new runtime may still be starting. Its private cancellation path uses the same idle fence.
        $request = Join-Path $Plan.StateDirectory 'gateway.stop'
        [IO.File]::WriteAllText($request, 'Requested by Filo installer')
        try { Wait-FiloWorkerStopped $Plan } finally { Remove-Item -LiteralPath $request -Force -ErrorAction SilentlyContinue }
        return
    }
    Wait-FiloWorkerStopped $Plan
}
function Restore-FiloWorkerUpdate {
    param($Plan)
    if (-not $Plan -or -not $Plan.Changed) { return }
    if ((Get-ScheduledTask -TaskName 'Filo-User-Agent').State -eq 'Running') {
        # Never force-stop a gateway after it could have admitted a historical turn.
        Request-FiloWorkerStop $Plan
    }
    Write-FiloJson (Join-Path $Plan.StateDirectory 'launch.json') $Plan.Launch
    Set-ScheduledTask -TaskName 'Filo-User-Agent' -Action $Plan.Action | Out-Null
    if ($Plan.WasRunning) {
        Start-ScheduledTask -TaskName 'Filo-User-Agent'
        Wait-FiloEndpoint $Plan.Url (Join-Path $Plan.StateDirectory 'token') 'standalone'
    }
    $Plan.Changed = $false
}
function Start-FiloWorkerUpdate {
    param($Plan, [string]$Runtime, [string]$Revision)
    if (-not $Plan) { return }
    if ($Plan.WasRunning) { Request-FiloWorkerStop $Plan }
    # A plain copy prevents rollback state from being modified along with the new launch settings.
    $next = $Plan.Launch | ConvertTo-Json -Depth 8 | ConvertFrom-Json
    $next.runtimeDirectory = $Runtime; $next.revision = $Revision
    # The migrated record must stop claiming a Node entry point.
    if ($next.PSObject.Properties['nodePath']) { $next.PSObject.Properties.Remove('nodePath') }
    $Plan.Changed = $true
    Write-FiloJson (Join-Path $Plan.StateDirectory 'launch.json') $next
    $action = New-FiloTaskAction $Runtime $Plan.StateDirectory Gateway $next.workspaceDirectory
    Set-ScheduledTask -TaskName 'Filo-User-Agent' -Action $action | Out-Null
    Start-ScheduledTask -TaskName 'Filo-User-Agent'
    Wait-FiloEndpoint $Plan.Url (Join-Path $Plan.StateDirectory 'token') 'standalone'
}
