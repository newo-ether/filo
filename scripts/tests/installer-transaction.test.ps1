$ErrorActionPreference='Stop'
. (Join-Path $PSScriptRoot '..\windows\Install-Transaction.ps1')
function Assert($Value,$Message) { if (-not $Value) { throw $Message } }
$fixture=Join-Path ([IO.Path]::GetTempPath()) ('filo-transaction-'+[guid]::NewGuid().ToString('N'))
$bundle=Join-Path $fixture 'bundle'; $root=Join-Path $fixture 'data'; $program=Join-Path $fixture 'Program Files\Filo'
$worker=Join-Path $root 'agents\S-1-5-21-1-2-3-1001'; $state=Join-Path $root 'service'
$events=[Collections.Generic.List[string]]::new()
$client=[pscustomobject]@{Name='Selected native clients';Profile=(Join-Path $fixture 'user');Sid='S-1-5-21-1-2-3-1001'}
$script:failReadiness=$false; $script:componentFailure=$false; $script:failRuntime=$false
$script:installRuntime=${function:Install-FiloRuntime}
function Install-FiloRuntime {
    param($Bundle,$Manifest,$Root,$Sid)
    if ($script:failRuntime) { throw 'Injected runtime replacement failure' }
    & $script:installRuntime @PSBoundParameters
}
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
function New-NetFirewallRule {
    param($Name,$DisplayName,$Group,$Direction,$Action,$Protocol,$LocalAddress,$LocalPort,$RemoteAddress,$Program,$Profile)
    Assert ($LocalAddress -eq 'Any' -and $RemoteAddress -eq '100.64.0.0/10') 'Wildcard listening retains the tailnet firewall scope'
    $events.Add('firewall-add')
}
function Wait-FiloEndpoint {
    param($Url,$TokenPath,$Mode,$Seconds,$FiloPath)
    $events.Add('health-'+$Mode)
    Assert (Test-Path $TokenPath) 'Health check requires installed credentials'
    if ($FiloPath) {
        Assert ($FiloPath -eq (Join-Path $program 'bin\filo.exe')) 'Public health uses fixed executable'
        Assert ($Url -eq 'http://127.0.0.1:7435') 'Wildcard public readiness probes loopback using encryption'
        if ($script:failReadiness) { throw 'Injected health failure' }
    }
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
    $arguments=@{Bundle=$bundle;Manifest=$manifest;Root=$root;State=$state;Worker=$worker;Client=$client;Bind='0.0.0.0';Port=7435;SourceOptions=$options}
    New-Item -ItemType Directory -Path $state,$worker -Force | Out-Null
    Write-FiloJson (Join-Path $state 'config.json') @{bind='127.0.0.1';port=9999}
    Write-FiloJson (Join-Path $worker 'config.json') @{port=9998;claudeBridgePort=9997}
    $publicTokenPath=Join-Path $state 'token'
    [IO.File]::WriteAllText($publicTokenPath,'invalid existing token')
    $rejected=$false
    try { Invoke-FiloInstall @arguments | Out-Null } catch { $rejected=$_.Exception.Message -match 'token is invalid' }
    Assert ($rejected -and $events.Count -eq 0) 'Invalid legacy public token fails before interruption'
    Assert ([IO.File]::ReadAllText($publicTokenPath) -ceq 'invalid existing token') 'Invalid public token is preserved verbatim'
    $legacyToken=('Ab12'*16)+"`r`n"
    [IO.File]::WriteAllText($publicTokenPath,$legacyToken)
    [IO.File]::WriteAllText((Join-Path $state 'old-state.txt'),'do not migrate')
    [IO.File]::WriteAllText((Join-Path $worker 'old-state.txt'),'do not migrate')
    $freshOptions=Get-FiloSourceOptions $worker @('claude-code') '' $false 7438 $false 7435 -IgnoreExisting
    Assert ($freshOptions.ClaudeBridgePort -eq 7438) 'Legacy worker settings are not adopted'
    $script:failRuntime=$true; $events.Clear(); $rejected=$false
    try { Invoke-FiloInstall @arguments | Out-Null } catch { $rejected=$_.Exception.Message -match 'runtime replacement failure' }
    Assert ($rejected -and $events[$events.Count-1] -eq 'stop') 'Replacement failure is explicit and stops owned resources'
    Assert ([IO.File]::ReadAllText($publicTokenPath) -ceq $legacyToken) 'Legacy token remains on disk when replacement fails after cleanup'
    Assert (-not (Test-Path (Join-Path $state 'config.json'))) 'Failure exercises cleanup before new configuration exists'
    $script:failRuntime=$false
    foreach ($pass in @(1,2)) {
        $events.Clear()
        foreach ($directory in @(Get-FiloLegacyDirectories $root @($client.Profile)) + @((Join-Path $root 'qualification-deadbeef'))) {
            New-Item -ItemType Directory -Path $directory -Force | Out-Null
            [IO.File]::WriteAllText((Join-Path $directory 'obsolete.txt'),'old version')
        }
        $installOutput=@(Invoke-FiloInstall @arguments | ForEach-Object {
            if ($_ -like 'Access token:*') { $events.Add('token-output') }
            $_
        })
        Assert (($events[0..1] -join ',') -eq 'stop,firewall-remove') 'Old restart sources stop before file replacement'
        Assert (Test-Path (Join-Path $program 'bin\filo.exe')) 'Fixed command exists'
        Assert (-not (Test-Path (Join-Path $program 'tools'))) 'Installed payload has no second tools binary directory'
        foreach ($directory in @(Get-FiloLegacyDirectories $root @($client.Profile))) { Assert (-not (Test-Path $directory)) 'Legacy directories are removed every run' }
        Assert (-not (Test-Path (Join-Path $worker 'old-state.txt'))) 'Old private state is removed, never migrated'
        Assert (-not (Test-Path (Join-Path $state 'old-state.txt'))) 'Legacy public state is removed apart from its access token'
        Assert ([IO.File]::ReadAllText($publicTokenPath) -ceq $legacyToken) 'Legacy migration, failed-install retry and fixed-layout update preserve exact public token bytes'
        $saved=Get-Content (Join-Path $state 'config.json') -Raw | ConvertFrom-Json
        Assert ($saved.layoutVersion -eq 1 -and $saved.workerTokenPath -eq (Join-Path $worker 'token')) 'Private paths are fixed and layout is marked'
        $launch=Get-Content (Join-Path $worker 'launch.json') -Raw | ConvertFrom-Json
        Assert ($launch.runtimeDirectory -eq $program -and $launch.sources.Count -eq 2) 'Runtime and source selection survive publication'
        Assert ($events.Contains('start-Filo-Standalone-Host') -and $events.Contains('start-Filo-User-Agent')) 'Both selected user tasks start'
        Assert ($events.Contains('health-standalone') -and $events.Contains('health-existing')) 'Worker and public authentication are verified'
        $token=[IO.File]::ReadAllText((Join-Path $state 'token'))
        Assert ($saved.bind -eq '0.0.0.0' -and $events.Contains('firewall-add')) 'Wildcard bind is published and firewall installed'
        $private=Get-Content (Join-Path $worker 'config.json') -Raw | ConvertFrom-Json
        Assert ($private.bind -eq '127.0.0.1') 'Wildcard public bind never changes private worker binding'
        $tokenLines=@($installOutput | Where-Object { $_ -like 'Access token:*' })
        Assert ($tokenLines.Count -eq 1 -and $tokenLines[0] -ceq ('Access token: '+$token.Trim())) 'Successful non-interactive install prints the installed public token once'
        Assert ($events[$events.Count-1] -eq 'token-output') 'Token output follows public readiness and entrypoint validation'
        if ($pass -eq 1) { $firstToken=$token } else { Assert ($token -ceq $firstToken) 'New-layout reinstall keeps the access token' }
    }
    $events.Clear(); $script:componentFailure=$true; $rejected=$false
    try { Invoke-FiloInstall @arguments | Out-Null } catch { $rejected=$_.Exception.Message -match 'ownership' }
    Assert ($rejected -and $events.Count -eq 0) 'Invalid component ownership fails before interruption'
    $script:componentFailure=$false; $script:failReadiness=$true; $events.Clear(); $rejected=$false
    $failedOutput=[Collections.Generic.List[string]]::new()
    try { Invoke-FiloInstall @arguments | ForEach-Object { $failedOutput.Add([string]$_) } } catch { $rejected=$_.Exception.Message -match 'Injected health failure' }
    Assert ($rejected -and $events[$events.Count-1] -eq 'stop') 'Readiness failure is explicit and leaves resources stopped'
    Assert (@($failedOutput | Where-Object { $_ -like 'Access token:*' }).Count -eq 0) 'Failed public readiness never prints the token'
    Assert ([IO.File]::ReadAllText($publicTokenPath) -ceq $legacyToken) 'Readiness failure keeps the access token'
    $script:failReadiness=$false; $events.Clear()
    $arguments.SourceOptions=[pscustomobject]@{Sources=@('codex-cli');CodexDaemonSocket='';ClaudeBridgePort=7438}
    Invoke-FiloInstall @arguments | Out-Null
    Assert (-not $events.Contains('register-Filo-Standalone-Host')) 'CLI-only install creates no Desktop host'
    Assert ($events.Contains('health-multi-source')) 'Non-Desktop health checks use multi-source mode'
    Assert (-not (Test-Path (Join-Path $worker 'claude-mod.json'))) 'Deselection removes obsolete observer settings'
    $newRoot=Join-Path $fixture 'new-data'
    $arguments.Root=$newRoot; $arguments.State=Join-Path $newRoot 'service'
    $arguments.Worker=Join-Path $newRoot ('agents\'+$client.Sid)
    Invoke-FiloInstall @arguments | Out-Null
    $newToken=[IO.File]::ReadAllText((Join-Path $arguments.State 'token'))
    Assert ($newToken -match '^[0-9a-f]{64}$') 'An installation without a public token generates a valid one'
    Invoke-FiloInstall @arguments | Out-Null
    Assert ([IO.File]::ReadAllText((Join-Path $arguments.State 'token')) -ceq $newToken) 'The generated public token survives a later update'
    'PASS: fixed payload, legacy and fixed-layout token continuity, failed migration retry, absent-token generation, residual cleanup, source tasks, preflight and failed-health shutdown'
} finally { if (Test-Path $fixture) { Remove-Item $fixture -Recurse -Force } }
