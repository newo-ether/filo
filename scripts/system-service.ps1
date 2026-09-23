param(
    [ValidateSet('Install','Update','ValidateOnly','Uninstall','Restore')][string]$Action = 'ValidateOnly',
    [string]$RuntimeDirectory,
    [string]$StateDirectory = (Join-Path $env:ProgramData 'Filo\service'),
    [string]$FiloPath,
    [string]$WrapperPath = (Join-Path $env:ProgramData 'Filo\nssm.exe'),
    [ValidatePattern('^Filo(?:-Qualification-[a-f0-9]{8})?$')][string]$ServiceName = 'Filo',
    # Restore re-applies one service registration verbatim. A rollback must not
    # reproduce a previous release's command line from this release's rules: the
    # first Filo release installed by the Go delivery replaces a Node one.
    [string]$RestoreApplication,
    [string]$RestoreAppDirectory,
    [string]$RestoreAppParameters,
    [switch]$RestoreStart
)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'windows\Service-State.ps1')
$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
if (-not ([Security.Principal.WindowsPrincipal]::new($identity)).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw 'Filo service installation requires an administrator'
}
$root = [IO.Path]::GetFullPath((Join-Path $env:ProgramData 'Filo')) + '\'
$statePath = [IO.Path]::GetFullPath($StateDirectory)
if (-not $statePath.StartsWith($root, [StringComparison]::OrdinalIgnoreCase)) { throw 'State must stay in the private Filo directory' }
$WrapperPath = (Get-Item -LiteralPath $WrapperPath).FullName
$existing = Get-CimInstance Win32_Service -Filter "Name='$serviceName'"
$restoreRunning = Test-FiloServiceShouldRun $existing
if ($existing -and ($existing.StartName -ne 'LocalSystem' -or $existing.PathName.Trim('"') -ine $WrapperPath)) {
    throw 'Refusing to change an unidentified Filo service'
}
$serviceRegistry = 'HKLM:\SYSTEM\CurrentControlSet\Services\' + $serviceName
function Invoke-Wrapper {
    param([string[]]$Values)
    if ($Values.Count -eq 4 -and $Values[0] -eq 'set' -and $Values[2] -eq 'AppParameters') {
        # Windows PowerShell strips embedded native quotes. Preserve the complete
        # NSSM command line as one registry value, including directories with spaces.
        Set-ItemProperty -LiteralPath ($serviceRegistry + '\Parameters') -Name AppParameters -Value $Values[3]
        return
    }
    & $WrapperPath @Values | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "Filo service manager failed: $($Values[0])" }
}
function Stop-Gateway {
    $service = Get-Service -Name $serviceName -ErrorAction SilentlyContinue
    if ($service -and $service.Status -ne 'Stopped') {
        Stop-Service -InputObject $service -NoWait
        $service.WaitForStatus('Stopped', [TimeSpan]::FromSeconds(20))
    }
}
function Wait-GatewayReady {
    param([double]$Seconds = 25)
    $deadline = [DateTime]::UtcNow.AddSeconds($Seconds)
    do {
        try {
            if ($Action -eq 'Restore' -and [IO.Path]::GetFileName($RestoreApplication) -ne 'filo.exe') {
                # Only a recorded pre-Go rollback uses its original private protocol.
                $reply = Invoke-RestMethod -Uri ("http://$($config.bind):$($config.port)/v1/info") -Headers @{Authorization="Bearer $token"} -TimeoutSec 2 -DisableKeepAlive
            } else {
                $probeBinary = if ($Action -eq 'Restore') { $RestoreApplication } else { $FiloPath }
                $response = & $probeBinary request -url ("http://$($config.bind):$($config.port)") -token-file (Join-Path $statePath 'token') -timeout 2s | Out-String
                if ($LASTEXITCODE -ne 0) { throw 'Encrypted gateway readiness failed' }
                $reply = ($response -join "`n") | ConvertFrom-Json
            }
            $current = Get-CimInstance Win32_Service -Filter "Name='$serviceName'"
            if ($reply.protocolVersion -eq 2 -and $reply.sessionMode -eq 'existing' -and
                $current.StartName -eq 'LocalSystem' -and $current.State -eq 'Running') { return }
        } catch { }
        if ([DateTime]::UtcNow -ge $deadline) { break }
        Start-Sleep -Milliseconds 250
    } while ([DateTime]::UtcNow -lt $deadline)
    throw 'Filo SYSTEM service did not become ready'
}
function Set-FiloGatewayRegistration {
    param([string]$Application,[string]$AppDirectory,[string]$AppParameters)
    Invoke-Wrapper -Values @('set', $serviceName, 'Application', $Application)
    Invoke-Wrapper -Values @('set', $serviceName, 'AppDirectory', $AppDirectory)
    Invoke-Wrapper -Values @('set', $serviceName, 'AppParameters', $AppParameters)
    Invoke-Wrapper -Values @('set', $serviceName, 'ObjectName', 'LocalSystem')
    Invoke-Wrapper -Values @('set', $serviceName, 'Start', 'SERVICE_AUTO_START')
    Invoke-Wrapper -Values @('set', $serviceName, 'AppKillProcessTree', '0')
    Invoke-Wrapper -Values @('set', $serviceName, 'AppNoConsole', '1')
    Invoke-Wrapper -Values @('set', $serviceName, 'AppExit', 'Default', 'Restart')
    Invoke-Wrapper -Values @('set', $serviceName, 'AppRestartDelay', '5000')
    Invoke-Wrapper -Values @('set', $serviceName, 'AppStopMethodConsole', '3000')
    Invoke-Wrapper -Values @('set', $serviceName, 'AppStdout', (Join-Path $statePath 'service.stdout.log'))
    Invoke-Wrapper -Values @('set', $serviceName, 'AppStderr', (Join-Path $statePath 'service.stderr.log'))
    Invoke-Wrapper -Values @('set', $serviceName, 'AppRotateFiles', '1')
    Invoke-Wrapper -Values @('set', $serviceName, 'AppRotateBytes', '10485760')
}
if ($Action -eq 'Uninstall') {
    if ($existing) {
        if ($existing.PathName.Trim('"') -ine $WrapperPath -or $existing.StartName -ne 'LocalSystem') {
            throw 'Refusing to remove an unidentified Filo service'
        }
        Stop-Gateway
        & sc.exe delete $serviceName | Out-Null
        if ($LASTEXITCODE -ne 0) { throw 'Failed to remove Filo service' }
    }
    Write-Output 'Filo SYSTEM gateway removed; agent processes and private data retained'
    exit
}
if ($Action -eq 'Restore') {
    # A rollback reinstates one recorded registration, possibly one this release
    # would never build itself: the first Go-delivered Filo replaces a Node one,
    # and rebuilding that command line from this release's rules would be wrong.
    if (-not $RestoreApplication) { throw 'Provide the previous service application' }
    if (-not $RestoreAppParameters) { throw 'Provide the previous service parameters' }
    if ($RestoreStart) {
        $config = Get-Content -LiteralPath (Join-Path $statePath 'config.json') -Raw | ConvertFrom-Json
        $token = [IO.File]::ReadAllText((Join-Path $statePath 'token')).Trim()
    }
    if ($existing) { Stop-Gateway }
    else { Invoke-Wrapper -Values @('install', $serviceName, $RestoreApplication) }
    Set-FiloGatewayRegistration -Application $RestoreApplication -AppDirectory ([string]$RestoreAppDirectory) `
        -AppParameters ([string]$RestoreAppParameters)
    if ($RestoreStart) {
        Start-Service -Name $serviceName
        Wait-GatewayReady
    }
    Write-Output 'Filo SYSTEM gateway restored to its previous registration'
    exit
}
if (-not $RuntimeDirectory) { throw 'Provide the verified staged runtime directory' }
$runtimePath = [IO.Path]::GetFullPath($RuntimeDirectory)
if (-not $FiloPath) { $FiloPath = Join-Path $runtimePath 'tools\filo.exe' }
if (-not $runtimePath.StartsWith($root, [StringComparison]::OrdinalIgnoreCase)) { throw 'Runtime must stay in private Filo directory' }
$entry = $FiloPath
foreach ($path in @($entry, $WrapperPath, (Join-Path $statePath 'config.json'), (Join-Path $statePath 'token'))) {
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "Missing Filo prerequisite: $path" }
}
$config = Get-Content -LiteralPath (Join-Path $statePath 'config.json') -Raw | ConvertFrom-Json
$token = [IO.File]::ReadAllText((Join-Path $statePath 'token')).Trim()
if ($token -notmatch '^[0-9a-fA-F]{64}$' -or $config.workerUrl -notmatch '^http://127\.0\.0\.1:[0-9]+$' -or
    $config.bind -notmatch '^(127\.0\.0\.1|100\.(6[4-9]|[7-9][0-9]|1[01][0-9]|12[0-7])\.[0-9]{1,3}\.[0-9]{1,3})$' -or
    $config.port -lt 1024 -or $config.port -gt 65535) { throw 'Invalid private Filo configuration' }
# The staged release must run as the binary it claims to be. The manifest hashes
# already verified it; this refuses a truncated or foreign file that happens to
# carry a matching name. A bare call answers its usage on stderr and exits 2, and
# Windows PowerShell turns a redirected native stderr into an error record, so the
# probe runs under the relaxed preference its own output requires.
if (-not (Test-FiloEntrypoint $FiloPath)) { throw 'Staged Filo entrypoint is invalid' }
if ($Action -eq 'ValidateOnly') { Write-Output 'Filo SYSTEM prerequisites validated; no service mutation'; exit }
if ($Action -eq 'Update' -and -not $existing) { throw 'Filo SYSTEM service does not exist' }
$stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
$backup = Join-Path $root ('backups\' + $stamp)
New-Item -ItemType Directory -Path $backup -Force | Out-Null
$previous = $null
$createdService = $false
if ($existing) {
    & reg.exe export ('HKLM\SYSTEM\CurrentControlSet\Services\' + $serviceName) (Join-Path $backup 'service.reg') /y | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Cannot preserve old Filo service registration' }
    $previous = Get-ItemProperty -LiteralPath "$serviceRegistry\Parameters"
    Stop-Gateway
}
try {
    if (-not $existing) {
        Invoke-Wrapper -Values @('install', $serviceName, $FiloPath)
        $createdService = $true
    }
    Set-FiloGatewayRegistration -Application $FiloPath -AppDirectory $runtimePath `
        -AppParameters ('system-service "' + $statePath + '"')
    Start-Service -Name $serviceName
    Wait-GatewayReady
    $current = Get-CimInstance Win32_Service -Filter "Name='$serviceName'"
    [pscustomobject]@{name=$serviceName;account=$current.StartName;runtime=$runtimePath;installedAt=[DateTime]::UtcNow.ToString('o')} |
        ConvertTo-Json | Set-Content -LiteralPath (Join-Path $statePath 'installation.json') -Encoding UTF8
    Write-Output 'Filo LocalSystem service installed and authenticated'
} catch {
    $failure = $_
    try {
        Stop-Gateway
        if ($previous) {
            Invoke-Wrapper -Values @('set',$serviceName,'Application',$previous.Application)
            Invoke-Wrapper -Values @('set',$serviceName,'AppDirectory',$previous.AppDirectory)
            Invoke-Wrapper -Values @('set',$serviceName,'AppParameters',$previous.AppParameters)
            if ($restoreRunning) {
                # Probe the registration actually restored, including the first Go migration.
                $Action = 'Restore'
                $RestoreApplication = [string]$previous.Application
                Start-Service -Name $serviceName
                Wait-GatewayReady
            }
        } elseif ($createdService) {
            & sc.exe delete $serviceName | Out-Null
            if ($LASTEXITCODE -ne 0) { throw 'Cannot remove failed new Filo registration' }
        }
    } catch { Write-Warning "Gateway rollback needs attention: $($_.Exception.Message). Native agents were not controlled" }
    throw $failure
}
