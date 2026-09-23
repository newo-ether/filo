<#
Opt-in real SCM regression for the Go delivery. Requires an administrator PowerShell,
NSSM, and a service binary produced by scripts/build-windows.ps1 or by
`go build ./cmd/filo`. It registers only a newly allocated qualification service,
never the live Filo service. It supports an administrator or SYSTEM controller.

The staged-binary self check refuses a runtime whose tools\filo.exe cannot answer
`Usage: filo <...>`, so a refused runtime is rejected before any registration is
written. A readiness failure with a valid binary (a bound port, a missing privilege)
is covered by the transaction fixtures with a stub installer instead, because
provoking it here would need a second compiled binary to serve as the bad payload.
#>
param(
    [Parameter(Mandatory=$true)][string]$FiloPath,
    [Parameter(Mandatory=$true)][string]$WrapperPath
)
$ErrorActionPreference='Stop'
$WrapperPath=(Get-Item -LiteralPath $WrapperPath).FullName
. (Join-Path $PSScriptRoot '..\windows\Install-Support.ps1')
. (Join-Path $PSScriptRoot '..\windows\Service-State.ps1')
$installer = Join-Path $PSScriptRoot '..\system-service.ps1'
if (-not (Test-Path -LiteralPath $FiloPath -PathType Leaf)) { throw "Missing staged service binary: $FiloPath" }
$suffix = [guid]::NewGuid().ToString('N').Substring(0,8)
$name = 'Filo-Qualification-' + $suffix
$root = Join-Path $env:ProgramData 'Filo'
$fixture = Assert-FiloPrivatePath (Join-Path $root ('qualification-' + $suffix)) $root
$state = Join-Path $fixture 'state'
$good = Join-Path $fixture 'good'
$bad = Join-Path $fixture 'bad'
$entry = Join-Path $good 'tools\filo.exe'
$before = Get-CimInstance Win32_Service -Filter "Name='Filo'" | Select-Object Name,ProcessId,StartName,State,PathName
$listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback,0)
$listener.Start(); $port = $listener.LocalEndpoint.Port; $listener.Stop()
try {
    Protect-FiloDirectory $state
    New-FiloToken (Join-Path $state 'token')
    Write-FiloJson (Join-Path $state 'config.json') @{
        bind='127.0.0.1'; port=$port; desktopSid='S-1-5-21-0-0-0-1001';
        workerUrl='http://127.0.0.1:7437'; workerTokenPath=(Join-Path $state 'helper-token')
    }
    foreach ($runtime in @($good,$bad)) {
        New-Item -ItemType Directory -Path (Join-Path $runtime 'tools') -Force | Out-Null
        New-Item -ItemType Directory -Path (Join-Path $runtime 'scripts\windows') -Force | Out-Null
        [IO.File]::WriteAllText((Join-Path $runtime 'scripts\windows\Desktop-Identity.ps1'), '# qualification placeholder - never used')
    }
    Copy-Item -LiteralPath $FiloPath -Destination $entry
    [IO.File]::WriteAllText((Join-Path $bad 'tools\filo.exe'), 'not a Filo entrypoint')
    $common = @{StateDirectory=$state;WrapperPath=$WrapperPath;ServiceName=$name}
    $failed = $false
    try { & $installer @common -Action Install -RuntimeDirectory $bad } catch { $failed = $true }
    if (-not $failed -or (Get-Service -Name $name -ErrorAction SilentlyContinue)) { throw 'Failed first install left a service registration' }
    Write-Output 'PASS: a runtime without a valid Filo entrypoint was refused before registration'
    & $installer @common -Action Install -RuntimeDirectory $good
    Wait-FiloEndpoint ('http://127.0.0.1:' + $port) (Join-Path $state 'token') 'existing' 30 -FiloPath $entry
    $registered = Get-CimInstance Win32_Service -Filter "Name='$name'"
    if ($registered.StartName -ne 'LocalSystem' -or $registered.State -ne 'Running') { throw 'Installed qualification service is not a running LocalSystem service' }
    $settings = Get-ItemProperty -LiteralPath ('HKLM:\SYSTEM\CurrentControlSet\Services\' + $name + '\Parameters')
    if ($settings.Application -ine $entry -or $settings.AppParameters -cne ('system-service "' + $state + '"')) {
        throw "The Go gateway was not registered verbatim: $($settings.Application) $($settings.AppParameters)"
    }
    Write-Output 'PASS: the compiled binary served /v1/info as the registered LocalSystem gateway'
    # The same verified executable remains valid with alternate separator spelling.
    $common.WrapperPath = $WrapperPath.Replace('\','/')
    $failed = $false
    try { & $installer @common -Action Update -RuntimeDirectory $bad } catch { $failed = $true }
    if (-not $failed) { throw 'Invalid update was accepted' }
    Wait-FiloEndpoint ('http://127.0.0.1:' + $port) (Join-Path $state 'token') 'existing' 30 -FiloPath $entry
    $settings = Get-ItemProperty -LiteralPath ('HKLM:\SYSTEM\CurrentControlSet\Services\' + $name + '\Parameters')
    if ($settings.Application -ine $entry) { throw 'A refused update changed the registered application' }
    Write-Output 'PASS: a refused update left the ready gateway on its previous registration'
    # Exercise rollback after registration, not only pre-validation refusal.
    $failsAfterRegistration=Join-Path $fixture 'fails-after-registration'
    New-Item -ItemType Directory -Path (Join-Path $failsAfterRegistration 'tools') -Force | Out-Null
    $failingBinary=Join-Path $failsAfterRegistration 'tools\filo.exe'
    $repo=Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
    Push-Location $repo
    try {
        & (Get-Command go -ErrorAction Stop).Source build -ldflags '-H=windowsgui' -o $failingBinary ./scripts/tests/fixtures/failing-service
        if ($LASTEXITCODE -ne 0) { throw 'Failing-service fixture did not compile' }
    } finally { Pop-Location }
    if (-not (Test-FiloEntrypoint $failingBinary)) { throw 'Fixture must pass entry validation' }
    $failed=$false
    try { & $installer @common -Action Update -RuntimeDirectory $failsAfterRegistration } catch { $failed=$true }
    if (-not $failed) { throw 'Non-ready registered service was accepted' }
    $settings=Get-ItemProperty -LiteralPath ('HKLM:\SYSTEM\CurrentControlSet\Services\'+$name+'\Parameters')
    if ($settings.Application -ine $entry) { throw 'Failed readiness did not restore the previous application' }
    Wait-FiloEndpoint ('http://127.0.0.1:'+$port) (Join-Path $state 'token') 'existing' 30 -FiloPath $entry
    Write-Output 'PASS: post-registration readiness failure restored the previous encrypted gateway'
    & $installer @common -Action Uninstall
    if (Get-Service -Name $name -ErrorAction SilentlyContinue) { throw 'Qualification service was not removed' }
    $after = Get-CimInstance Win32_Service -Filter "Name='Filo'" | Select-Object Name,ProcessId,StartName,State,PathName
    if (($before|ConvertTo-Json -Compress) -ne ($after|ConvertTo-Json -Compress)) { throw 'Live Filo service changed during isolated qualification' }
    Write-Output 'PASS: uninstall removed only the qualification service; live Filo stayed unchanged'
} catch {
    $evidence=Join-Path (Split-Path -Parent (Split-Path -Parent $PSScriptRoot)) ('.harness\scm-failure-' + $suffix)
    New-Item -ItemType Directory -Path $evidence -Force | Out-Null
    Get-ChildItem -LiteralPath $state -Filter '*.log' -ErrorAction SilentlyContinue | Copy-Item -Destination $evidence
    throw
} finally {
    $remaining = Get-CimInstance Win32_Service -Filter "Name='$name'"
    if ($remaining) {
        if ($remaining.PathName.Trim('"') -ine $WrapperPath -or $remaining.StartName -ne 'LocalSystem') { throw 'Unexpected qualification service; refusing cleanup' }
        & $installer -Action Uninstall -WrapperPath $WrapperPath -ServiceName $name
    }
    $safe = Assert-FiloPrivatePath $fixture $root
    Remove-Item -LiteralPath $safe -Recurse -Force
}
