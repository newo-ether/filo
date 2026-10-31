. (Join-Path $PSScriptRoot 'Install-Layout.ps1')
. (Join-Path $PSScriptRoot 'Source-Selection.ps1')
. (Join-Path $PSScriptRoot 'Claude-Components.ps1')

function Invoke-FiloInstall {
    param([string]$Bundle,$Manifest,[string]$Root,[string]$State,[string]$Worker,$Client,[string]$Bind,[int]$Port,$SourceOptions)
    if (-not $SourceOptions) {
        $sourceIds=if ($Client.PSObject.Properties['Sources']) { @($Client.Sources) } else { @('codex-desktop') }
        $SourceOptions=Get-FiloSourceOptions $Worker $sourceIds '' $false 7438 $false $Port
    }
    $hasDesktop=$SourceOptions.Sources -contains 'codex-desktop'
    $hasClaude=@($SourceOptions.Sources | Where-Object { $_ -in @('claude-code','claude-desktop') }).Count -gt 0
    $publicMode=if ($hasDesktop) { 'existing' } else { 'multi-source' }
    $mode=if ($hasDesktop) { 'standalone' } else { 'multi-source' }
    $program=Get-FiloProgramRoot
    $savedPath=Join-Path $State 'config.json'
    $saved=if (Test-Path -LiteralPath $savedPath) { Get-Content -LiteralPath $savedPath -Raw | ConvertFrom-Json } else { $null }
    $fresh=-not ($saved -and $saved.PSObject.Properties['layoutVersion'] -and $saved.layoutVersion -eq 1)
    if ((Test-FiloPathInside $Bundle $program) -or (Test-FiloPathInside $Bundle $Root) -or
        [IO.Path]::GetFullPath($Bundle).TrimEnd('\') -ieq $program.TrimEnd('\')) {
        throw 'Run installation from an extracted bundle outside the installed program and data directories'
    }
    # Complete integrity, component ownership and endpoint preflight before stopping.
    $safeState=Assert-FiloPrivatePath $State $Root
    if ((Split-Path -Parent $safeState).TrimEnd('\') -ine [IO.Path]::GetFullPath($Root).TrimEnd('\')) {
        throw 'Public Filo state must be a direct child of its data directory'
    }
    $publicTokenPath=Assert-FiloPrivatePath (Join-Path $safeState 'token') $Root
    # Validate existing credentials before interruption, without creating new ones.
    if (Test-Path -LiteralPath $publicTokenPath) { New-FiloToken $publicTokenPath }
    $endpoints=Get-FiloHelperEndpoints $Worker -IgnoreExisting:$fresh
    $nativeUrl=if ($hasDesktop) { $endpoints.NativeUrl } else { '' }
    if ($hasDesktop -and -not $nativeUrl) { $nativeUrl='ws://127.0.0.1:7436' }
    $componentPlan=New-FiloClaudeComponentPlan $Bundle $Worker $Client.Profile $Client.Sid $SourceOptions.Sources
    $profiles=@(Get-CimInstance Win32_UserProfile | Where-Object { -not $_.Special -and $_.LocalPath } | ForEach-Object LocalPath)
    $profiles=@($profiles + @($Client.Profile) | Select-Object -Unique)
    Write-Host '1/4  Stop Filo services, tasks and detached supervisors' -ForegroundColor Cyan
    Stop-FiloInstalledResources $Root $program $profiles
    Remove-FiloFirewallRules
    if ($fresh) {
        # Remove managed native helper bytes before discarding their old receipts.
        foreach ($base in @($Root) + @(Get-FiloLegacyDirectories $Root $profiles)) {
            if (-not (Test-Path -LiteralPath $base)) { continue }
            foreach ($receipt in @(Get-ChildItem -LiteralPath $base -Filter 'claude-components.json' -File -Recurse -Force)) {
                $record=Get-Content -LiteralPath $receipt.FullName -Raw | ConvertFrom-Json
                Remove-FiloClaudeComponents $receipt.DirectoryName ([string]$record.sid)
            }
        }
        if (Test-Path -LiteralPath $Root) {
            $safe=Assert-FiloPrivatePath $Root (Split-Path -Parent $Root)
            # Keep the public credential in place even if cleanup or replacement
            # fails. No copy, regeneration or in-memory-only backup is needed.
            foreach ($entry in @(Get-ChildItem -LiteralPath $safe -Force)) {
                if ($entry.FullName -ieq $safeState) { continue }
                Remove-Item -LiteralPath $entry.FullName -Recurse -Force
            }
            if (Test-Path -LiteralPath $safeState) {
                foreach ($entry in @(Get-ChildItem -LiteralPath $safeState -Force)) {
                    if ($entry.FullName -ieq $publicTokenPath) { continue }
                    Remove-Item -LiteralPath $entry.FullName -Recurse -Force
                }
            }
        }
        $componentPlan=New-FiloClaudeComponentPlan $Bundle $Worker $Client.Profile $Client.Sid $SourceOptions.Sources
    }
    Remove-FiloLegacyDirectories $Root $profiles
    try {
        Write-Host '2/4  Replace the fixed program directory and prepare private data' -ForegroundColor Cyan
        $runtime=Install-FiloRuntime $Bundle $Manifest $Root $Client.Sid
        $filo=Join-Path $runtime 'bin\filo.exe'
        $wrapper=Join-Path $runtime 'bin\nssm.exe'
        Grant-FiloDirectoryRead $Root $Client.Sid
        Grant-FiloDirectoryRead (Join-Path $Root 'agents') $Client.Sid
        Protect-FiloDirectory $State
        Protect-FiloDirectory $Worker $Client.Sid
        New-FiloToken (Join-Path $State 'token')
        New-FiloToken (Join-Path $Worker 'token')
        if ($hasClaude) { New-FiloToken (Join-Path $Worker 'claude-mod-token') }
        $launch=@{layoutVersion=1;runtimeDirectory=$runtime;revision=$Manifest.revision;
            desktopSid=$Client.Sid;workspaceDirectory=$Client.Profile;sources=@($SourceOptions.Sources)}
        $workerConfig=@{bind='127.0.0.1';port=$endpoints.WorkerPort;desktopSid=$Client.Sid;sources=@($SourceOptions.Sources)}
        $gatewayConfig=@{layoutVersion=1;bind=$Bind;port=$Port;desktopSid=$Client.Sid;sources=@($SourceOptions.Sources);
            workerUrl=$endpoints.WorkerUrl;workerTokenPath=(Join-Path $Worker 'token')}
        if ($hasDesktop) { $launch.appServerUrl=$nativeUrl; $workerConfig.appServerUrl=$nativeUrl }
        foreach ($record in @($launch,$workerConfig,$gatewayConfig)) {
            if ($SourceOptions.CodexDaemonSocket) { $record.codexDaemonSocket=$SourceOptions.CodexDaemonSocket }
            if ($hasClaude) { $record.claudeBridgePort=$SourceOptions.ClaudeBridgePort }
        }
        if ($hasClaude) {
            Write-FiloJson (Join-Path $Worker 'claude-mod.json') @{
                url=('http://127.0.0.1:' + $SourceOptions.ClaudeBridgePort + '/claude-mod/v1/')
                tokenFile=(Join-Path $Worker 'claude-mod-token')
            }
        }
        Install-FiloClaudeComponents $componentPlan
        if (-not $hasClaude -and (Test-Path -LiteralPath (Join-Path $Worker 'claude-mod.json'))) {
            Remove-Item -LiteralPath (Join-Path $Worker 'claude-mod.json') -Force
        }
        Write-FiloJson (Join-Path $Worker 'launch.json') $launch
        Write-FiloJson (Join-Path $Worker 'config.json') $workerConfig
        Write-FiloJson (Join-Path $State 'config.json') $gatewayConfig
        foreach ($name in @('gateway.stop','gateway.pid')) {
            $path=Join-Path $Worker $name
            if (Test-Path -LiteralPath $path) { Remove-Item -LiteralPath $path -Force }
        }
        Update-FiloMachinePath $program
        Write-Host '3/4  Register and start ordinary-user tasks and the SYSTEM gateway' -ForegroundColor Cyan
        $taskOptions=New-FiloTaskOptions $Client.Sid
        if ($hasDesktop) {
            $hostAction=New-FiloTaskAction $runtime $Worker Host $Client.Profile
            Register-FiloUserTask 'Filo-Standalone-Host' $hostAction $Client.Sid $taskOptions
        }
        $gatewayAction=New-FiloTaskAction $runtime $Worker Gateway $Client.Profile
        Register-FiloUserTask 'Filo-User-Agent' $gatewayAction $Client.Sid $taskOptions
        if ($hasDesktop) { Start-ScheduledTask -TaskName 'Filo-Standalone-Host' }
        Start-ScheduledTask -TaskName 'Filo-User-Agent'
        Wait-FiloEndpoint $endpoints.WorkerUrl (Join-Path $Worker 'token') $mode -Seconds 45
        & (Join-Path $runtime 'scripts\system-service.ps1') -Action Install -RuntimeDirectory $runtime -StateDirectory $State -FiloPath $filo -WrapperPath $wrapper
        if ($Bind -ne '127.0.0.1') {
            $firewallAddress=if ($Bind -eq '0.0.0.0') { 'Any' } else { $Bind }
            New-NetFirewallRule -Name 'Filo-Remote' -DisplayName 'Filo Remote' -Group 'Filo' -Direction Inbound `
                -Action Allow -Protocol TCP -LocalAddress $firewallAddress -LocalPort $Port -RemoteAddress '100.64.0.0/10' `
                -Program $filo -Profile Any | Out-Null
        }
        Write-Host '4/4  Verify authenticated readiness and fixed entrypoint' -ForegroundColor Cyan
        $probeBind=if ($Bind -eq '0.0.0.0') { '127.0.0.1' } else { $Bind }
        Wait-FiloEndpoint ("http://${probeBind}:$Port") (Join-Path $State 'token') $publicMode -FiloPath $filo
        if (-not (Test-Path -LiteralPath $filo -PathType Leaf)) { throw 'Fixed Filo entrypoint is missing' }
        Write-Output "Filo installed for $($Client.Name): http://${Bind}:$Port"
        Write-Output "Command: $filo. Access token file: $State\token. Sources: $($SourceOptions.Sources -join ', ')."
        Show-FiloAccessToken (Join-Path $State 'token')
    } catch {
        $failure=$_
        # An in-place replacement cannot roll back to a hash directory. Leave one
        # stopped installation and a precise nonzero error, never mixed versions.
        try { Stop-FiloInstalledResources $Root $program $profiles }
        catch { throw "Filo installation failed: $($failure.Exception.Message). Cleanup also failed: $($_.Exception.Message)" }
        throw "Filo installation failed: $($failure.Exception.Message). Filo resources are stopped; rerun the complete installer after correcting the reported cause."
    }
}
