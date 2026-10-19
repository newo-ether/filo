$ErrorActionPreference='Stop'
$fixture=Join-Path ([IO.Path]::GetTempPath()) ('filo-locking-'+[guid]::NewGuid().ToString('N'))
$entry=Join-Path $fixture 'scripts\install.ps1'
$ready=[Threading.ManualResetEventSlim]::new($false)
$release=[Threading.ManualResetEventSlim]::new($false)
$holder=$null; $pending=$null
$priorProgramData=$env:ProgramData
function Assert($Value,$Message) { if(-not $Value){throw $Message} }
try {
    $modules=Join-Path $fixture 'scripts\windows'
    New-Item -ItemType Directory -Path $modules,(Join-Path $fixture 'state') -Force | Out-Null
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot '..\install.ps1') -Destination $entry
    [IO.File]::WriteAllText((Join-Path $fixture 'manifest.json'),'fixture')
    [IO.File]::WriteAllText((Join-Path $modules 'Client-Selection.ps1'),@'
Set-StrictMode -Version 2
function Get-FiloClients { [pscustomobject]@{Name='Desktop fixture';Sid=$global:FiloLockSid;Profile=$global:FiloLockRoot} }
function Resolve-FiloSelection { param($Clients,$Ids) $Clients }
function Select-FiloClients { param($Clients) $Clients }
'@)
    [IO.File]::WriteAllText((Join-Path $modules 'Install-Support.ps1'),@'
function Test-FiloInstallAdministrator { $global:FiloLockAdmin }
function Get-FiloBundle { @{revision=('a'*40)} }
function Assert-FiloPrivatePath {
    param($Path,$Root)
    $global:FiloLockReads++
    if(-not $global:FiloLockAdmin){throw 'Protected state opened without elevation'}
    Join-Path $global:FiloLockRoot 'state'
}
function Get-FiloWorkerState {
    param($Root,$Profile,$Sid,$ExistingTokenPath)
    $global:FiloLockTokenPath=$ExistingTokenPath
    Join-Path $global:FiloLockRoot 'worker'
}
'@)
    [IO.File]::WriteAllText((Join-Path $modules 'Installer-Tasks.ps1'),@'
function Get-FiloInstallBind { param($Requested,$Existing) if($Requested){$Requested}else{$Existing} }
'@)
    [IO.File]::WriteAllText((Join-Path $modules 'Install-Transaction.ps1'),@'
function Invoke-FiloInstall {
    param($Bundle,$Manifest,$Root,$State,$Worker,$Client,$Bind,$Port)
    $global:FiloLockInstalls++
    [pscustomobject]@{Bind=$Bind;Port=$Port;Sid=$Client.Sid}
}
'@)
    foreach($name in @('Worker-Update','Host-Lifecycle','Uninstall-Transaction')){
        [IO.File]::WriteAllText((Join-Path $modules ($name+'.ps1')),'# Fixture: no OS interaction')
    }
    $env:ProgramData=$fixture
    $global:FiloLockRoot=$fixture; $global:FiloLockSid='S-1-5-21-1-2-3-1001'
    $global:FiloLockAdmin=$true; $global:FiloLockReads=0; $global:FiloLockInstalls=0; $global:FiloLockTokenPath=''
    $holder=[PowerShell]::Create()
    [void]$holder.AddScript(@'
param($Ready,$Release)
$mutex=[Threading.Mutex]::new($false,'Global\Filo.Install')
$owned=$false
try {
    try {$owned=$mutex.WaitOne(0)} catch [Threading.AbandonedMutexException] {$owned=$true}
    if(-not $owned){throw 'An actual Filo installer is running; locking fixture cannot proceed'}
    $Ready.Set()
    if(-not $Release.Wait(15000)){throw 'Locking fixture release deadline exceeded'}
} finally {if($owned){$mutex.ReleaseMutex()}; $mutex.Dispose()}
'@).AddArgument($ready).AddArgument($release)
    $pending=$holder.BeginInvoke()
    Assert ($ready.Wait(5000)) 'Fixture must acquire the mutex on a different thread'
    foreach($arguments in @(@{},@{ValidateOnly=$true},@{Uninstall=$true})){
        $blocked=$false
        try { & $entry -BundleDirectory $fixture -Clients codex @arguments | Out-Null }
        catch { $blocked=$_.Exception.Message -match 'Another Filo installation' }
        Assert $blocked 'Concurrent install, privileged validation and uninstall must reject before state reads'
        Assert ($global:FiloLockReads -eq 0 -and $global:FiloLockInstalls -eq 0) 'Contending entrypoint never opens private state or mutates resources'
    }
    $global:FiloLockAdmin=$false
    $validation=& $entry -BundleDirectory $fixture -Clients codex -ValidateOnly
    Assert (-not $validation.InstalledStateChecked -and $global:FiloLockReads -eq 0) 'Unprivileged validation remains independent of protected installation state'
    $global:FiloLockAdmin=$true
    # Model the winning installer writing fresh identity/endpoints before releasing its lock.
    $configPath=Join-Path $fixture 'state\config.json'
    $latest=@{desktopSid=$global:FiloLockSid;bind='100.100.100.100';port=28435;workerTokenPath=(Join-Path $fixture 'legacy\token')}
    [IO.File]::WriteAllText($configPath,($latest | ConvertTo-Json))
    $release.Set(); [void]$holder.EndInvoke($pending); $pending=$null
    Assert (-not $holder.HadErrors) 'Competing fixture installer must release the lock cleanly'
    $installed=& $entry -BundleDirectory $fixture -Clients codex
    Assert ($installed.Bind -eq $latest.bind -and $installed.Port -eq $latest.port) 'Retry reads the latest installed endpoint after acquiring the mutex'
    Assert ($installed.Sid -eq $latest.desktopSid -and $global:FiloLockTokenPath -eq $latest.workerTokenPath) 'Retry preserves the latest selected account and worker credential location'
    $validated=& $entry -BundleDirectory $fixture -Clients codex -ValidateOnly
    Assert ($validated.Bind -eq $latest.bind -and $validated.Port -eq $latest.port -and $validated.InstalledStateChecked) 'Privileged validation releases its mutex and reads the same current state'
    $global:FiloLockSid='S-1-5-21-1-2-3-1002'
    $blocked=$false
    try { & $entry -BundleDirectory $fixture -Clients codex | Out-Null }
    catch { $blocked=$_.Exception.Message -match 'belongs to another account' }
    Assert ($blocked -and $global:FiloLockInstalls -eq 1) 'An account mismatch never reaches installation'
    $global:FiloLockSid=$latest.desktopSid
    $retry=& $entry -BundleDirectory $fixture -Clients codex
    Assert ($retry.Port -eq $latest.port -and $global:FiloLockInstalls -eq 2) 'Failure releases the mutex for a subsequent valid installer'
    'PASS: production installer serializes protected reads for install/validate/uninstall and preserves fresh endpoint/account/credential state'
} finally {
    $release.Set()
    if($holder){
        if($pending){[void]$holder.EndInvoke($pending)}
        $holder.Dispose()
    }
    $ready.Dispose(); $release.Dispose()
    $env:ProgramData=$priorProgramData
    if(Test-Path -LiteralPath $fixture){Remove-Item -LiteralPath $fixture -Recurse -Force}
    Remove-Variable FiloLockRoot,FiloLockSid,FiloLockAdmin,FiloLockReads,FiloLockInstalls,FiloLockTokenPath -Scope Global -ErrorAction SilentlyContinue
}
