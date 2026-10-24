$ErrorActionPreference='Stop'
. (Join-Path $PSScriptRoot '..\windows\Install-Transaction.ps1')
function Assert($Value,$Message) { if (-not $Value) { throw $Message } }
$fixture=Join-Path ([IO.Path]::GetTempPath()) ('filo-transaction-'+[guid]::NewGuid().ToString('N'))
$bundle=Join-Path $fixture 'bundle'; $root=Join-Path $fixture 'data'; $program=Join-Path $fixture 'Program Files\Filo'
$worker=Join-Path $root 'agents\S-1-5-21-1-2-3-1001'; $state=Join-Path $root 'service'
$events=[Collections.Generic.List[string]]::new()
$client=[pscustomobject]@{Name='Selected native clients';Profile=(Join-Path $fixture 'user');Sid='S-1-5-21-1-2-3-1001'}
$script:failReadiness=$false; $script:componentFailure=$false
function Get-FiloProgramRoot { $program }
function Get-CimInstance { param($ClassName) if ($ClassName -eq 'Win32_UserProfile') { [pscustomobject]@{Special=$false;LocalPath=$client.Profile} } }
function Stop-FiloInstalledResources { $events.Add('stop') }
function Remove-FiloFirewallRules { $events.Add('firewall-remove') }
function Protect-FiloDirectory { param($Path,$UserSid) New-Item -ItemType Directory -Path $Path -Force | Out-Null }
function Grant-FiloDirectoryRead { param($Path,$UserSid) New-Item -ItemType Directory -Path $Path -Force | Out-Null }
function New-FiloTaskOptions { param($Sid) @{Sid=$Sid} }
function New-FiloTaskAction { param($Runtime,$State,$Role,$Workspace) Assert (Test-Path (Join-Path $Runtime 'bin\FiloBackground.exe')) 'Task requires the installed fixed entry'; @{Role=$Role} }
function Register-FiloUserTask { param($Name,$Action,$Sid,$Options) $events.Add('register-'+$Name) }
function Start-ScheduledTask { param($TaskName) $events.Add('start-'+$TaskName) }
function Update-FiloMachinePath { param($ProgramRoot) Assert ($ProgramRoot -eq $program) 'PATH uses Program Files'; $events.Add('path') }
function New-NetFirewallRule { $events.Add('firewall-add') }
function Wait-FiloEndpoint {
    param($Url,$TokenPath,$Mode,$Seconds,$FiloPath)
    $events.Add('health-'+$Mode)
    Assert (Test-Path $TokenPath) 'Health check requires installed credentials'
    if ($FiloPath) { Assert ($FiloPath -eq (Join-Path $program 'bin\filo.exe')) 'Public health uses fixed executable' }
    if ($script:failReadiness) { throw 'Injected health failure' }
}
function New-FiloClaudeComponentPlan {
    param($Bundle,$Worker,$Profile,$Sid,$Sources)
    if ($script:componentFailure) { throw 'Injected component ownership failure' }
    [pscustomobject]@{Sources=$Sources}
}
function Install-FiloClaudeComponents { param($Plan) $events.Add('components') }
try {
    New-Item -ItemType Directory -Path (Join-Path $bundle 'tools'),(Join-Path $bundle 'scripts'),$client.Profile -Force | Out-Null
    foreach ($name in @('filo.exe','nssm.exe','FiloBackground.exe')) { [IO.File]::WriteAllText((Join-Path $bundle ('tools\'+$name)),'fixture v1') }
    [IO.File]::WriteAllText((Join-Path $bundle 'scripts\system-service.ps1'), 'param($Action,$RuntimeDirectory,$StateDirectory,$FiloPath,$WrapperPath); if ($Action -ne ''Install'') {throw ''Wrong service action''}')
    $files=@(Get-ChildItem $bundle -Recurse -File | ForEach-Object { @{path=$_.FullName.Substring($bundle.Length+1);sha256=(Get-FileHash $_.FullName).Hash} })
    $manifest=@{revision=('a'*40);files=$files}
    Write-FiloJson (Join-Path $bundle 'manifest.json') $manifest
    $options=[pscustomobject]@{Sources=@('codex-desktop','claude-code');CodexDaemonSocket='';ClaudeBridgePort=7438}
    $arguments=@{Bundle=$bundle;Manifest=$manifest;Root=$root;State=$state;Worker=$worker;Client=$client;Bind='127.0.0.1';Port=7435;SourceOptions=$options}
    New-Item -ItemType Directory -Path $state,$worker -Force | Out-Null
    Write-FiloJson (Join-Path $state 'config.json') @{bind='127.0.0.1';port=9999}
    Write-FiloJson (Join-Path $worker 'config.json') @{port=9998;claudeBridgePort=9997}
    [IO.File]::WriteAllText((Join-Path $state 'token'),'obsolete legacy token')
    [IO.File]::WriteAllText((Join-Path $worker 'old-state.txt'),'do not migrate')
    $freshOptions=Get-FiloSourceOptions $worker @('claude-code') '' $false 7438 $false 7435 -IgnoreExisting
    Assert ($freshOptions.ClaudeBridgePort -eq 7438) 'Legacy worker settings are not adopted'
    foreach ($pass in @(1,2)) {
        $events.Clear()
        foreach ($directory in @(Get-FiloLegacyDirectories $root @($client.Profile)) + @((Join-Path $root 'qualification-deadbeef'))) {
            New-Item -ItemType Directory -Path $directory -Force | Out-Null
            [IO.File]::WriteAllText((Join-Path $directory 'obsolete.txt'),'old version')
        }
        Invoke-FiloInstall @arguments | Out-Null
        Assert (($events[0..1] -join ',') -eq 'stop,firewall-remove') 'Old restart sources stop before file replacement'
        Assert (Test-Path (Join-Path $program 'bin\filo.exe')) 'Fixed command exists'
        Assert (-not (Test-Path (Join-Path $program 'tools'))) 'Installed payload has no second tools binary directory'
        foreach ($directory in @(Get-FiloLegacyDirectories $root @($client.Profile))) { Assert (-not (Test-Path $directory)) 'Legacy directories are removed every run' }
        Assert (-not (Test-Path (Join-Path $worker 'old-state.txt'))) 'Old private state is removed, never migrated'
        Assert ([IO.File]::ReadAllText((Join-Path $state 'token')) -cne 'obsolete legacy token') 'Legacy credentials are not adopted'
        $saved=Get-Content (Join-Path $state 'config.json') -Raw | ConvertFrom-Json
        Assert ($saved.layoutVersion -eq 1 -and $saved.workerTokenPath -eq (Join-Path $worker 'token')) 'Private paths are fixed and layout is marked'
        $launch=Get-Content (Join-Path $worker 'launch.json') -Raw | ConvertFrom-Json
        Assert ($launch.runtimeDirectory -eq $program -and $launch.sources.Count -eq 2) 'Runtime and source selection survive publication'
        Assert ($events.Contains('start-Filo-Standalone-Host') -and $events.Contains('start-Filo-User-Agent')) 'Both selected user tasks start'
        Assert ($events.Contains('health-standalone') -and $events.Contains('health-existing')) 'Worker and public authentication are verified'
        $token=[IO.File]::ReadAllText((Join-Path $state 'token'))
        if ($pass -eq 1) { $firstToken=$token } else { Assert ($token -ceq $firstToken) 'New-layout reinstall keeps the access token' }
    }
    $events.Clear(); $script:componentFailure=$true; $rejected=$false
    try { Invoke-FiloInstall @arguments | Out-Null } catch { $rejected=$_.Exception.Message -match 'ownership' }
    Assert ($rejected -and $events.Count -eq 0) 'Invalid component ownership fails before interruption'
    $script:componentFailure=$false; $script:failReadiness=$true; $events.Clear(); $rejected=$false
    try { Invoke-FiloInstall @arguments | Out-Null } catch { $rejected=$_.Exception.Message -match 'Injected health failure' }
    Assert ($rejected -and $events[$events.Count-1] -eq 'stop') 'Readiness failure is explicit and leaves resources stopped'
    $script:failReadiness=$false; $events.Clear()
    $arguments.SourceOptions=[pscustomobject]@{Sources=@('codex-cli');CodexDaemonSocket='';ClaudeBridgePort=7438}
    Invoke-FiloInstall @arguments | Out-Null
    Assert (-not $events.Contains('register-Filo-Standalone-Host')) 'CLI-only install creates no Desktop host'
    Assert ($events.Contains('health-multi-source')) 'Non-Desktop health checks use multi-source mode'
    Assert (-not (Test-Path (Join-Path $worker 'claude-mod.json'))) 'Deselection removes obsolete observer settings'
    'PASS: fixed payload, two reinstall passes, residual cleanup, token continuity, source tasks, preflight and failed-health shutdown'
} finally { if (Test-Path $fixture) { Remove-Item $fixture -Recurse -Force } }
