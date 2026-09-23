Set-StrictMode -Version 2
function Test-FiloInstallAdministrator {
    $identity=[Security.Principal.WindowsIdentity]::GetCurrent()
    return ([Security.Principal.WindowsPrincipal]::new($identity)).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}
function Get-FiloHelperEndpoints {
    param([string]$StateDirectory)
    $launchPath = Join-Path $StateDirectory 'launch.json'
    $configPath = Join-Path $StateDirectory 'config.json'
    $hasLaunch = Test-Path -LiteralPath $launchPath
    $hasConfig = Test-Path -LiteralPath $configPath
    if (-not $hasLaunch -and -not $hasConfig) {
        # Defaults are chosen only for a new installation, never during update/recovery.
        return [pscustomobject]@{NativeUrl='ws://127.0.0.1:7436';WorkerUrl='http://127.0.0.1:7437';WorkerPort=7437}
    }
    if (-not $hasLaunch -or -not $hasConfig) { throw 'Incomplete Filo helper configuration; existing state was preserved' }
    $launch = Get-Content -Raw -LiteralPath $launchPath | ConvertFrom-Json
    $config = Get-Content -Raw -LiteralPath $configPath | ConvertFrom-Json
    # Early releases stored the authoritative endpoint only in config.json.
    # Admit that known launch shape without inventing a port or mutating state.
    $nativeUrl = if ($launch.PSObject.Properties['appServerUrl']) { $launch.appServerUrl }
    elseif ($launch.PSObject.Properties['nodePath'] -and $launch.PSObject.Properties['codexPath'] -and
        [IO.Path]::IsPathRooted([string]$launch.nodePath) -and
        [IO.Path]::IsPathRooted([string]$launch.codexPath)) { $config.appServerUrl }
    else { throw 'Missing native helper endpoint in an unrecognized launch record' }
    if ($nativeUrl -notmatch '^ws://127\.0\.0\.1:([0-9]+)$') { throw 'Invalid native helper endpoint' }
    $nativePort = [int]$Matches[1]
    if ($nativePort -lt 1024 -or $nativePort -gt 65535 -or $config.appServerUrl -cne $nativeUrl -or
        $config.bind -ne '127.0.0.1' -or ($config.port -isnot [int] -and $config.port -isnot [long]) -or
        $config.port -lt 1024 -or $config.port -gt 65535 -or $config.port -eq $nativePort) {
        throw 'Inconsistent Filo helper endpoints; existing state was preserved'
    }
    return [pscustomobject]@{NativeUrl=$nativeUrl;WorkerUrl=('http://127.0.0.1:' + $config.port);WorkerPort=$config.port}
}

function Get-FiloWorkerState {
    param([string]$Root, [string]$Profile, [string]$Sid, [string]$ExistingTokenPath)
    if ($Sid -notmatch '^S-1-(5-21|12-1)-[0-9-]+$') { throw 'Invalid desktop account SID' }
    $shared = Join-Path $Root ('agents\' + $Sid)
    $legacy = Join-Path $Profile 'AppData\Local\FiloStandalone'
    if (-not $ExistingTokenPath -or $ExistingTokenPath -ieq (Join-Path $shared 'token')) { return $shared }
    if ($ExistingTokenPath -ieq (Join-Path $legacy 'token')) { return $legacy }
    throw 'Existing Filo configuration belongs to another account.'
}
function Get-FiloBundle {
    param([string]$Directory)
    $base = [IO.Path]::GetFullPath($Directory).TrimEnd('\') + '\'
    $manifest = Get-Content -LiteralPath (Join-Path $base 'manifest.json') -Raw | ConvertFrom-Json
    if ($manifest.revision -notmatch '^[0-9a-f]{40}$' -or -not $manifest.files.Count) { throw 'Invalid Filo bundle manifest' }
    $seen = @{}
    foreach ($file in $manifest.files) {
        if ($file.path -match '[:*?]' -or [IO.Path]::IsPathRooted($file.path) -or $file.sha256 -notmatch '^[0-9a-fA-F]{64}$') { throw 'Unsafe bundle entry' }
        $path = [IO.Path]::GetFullPath((Join-Path $base $file.path))
        if (-not $path.StartsWith($base, [StringComparison]::OrdinalIgnoreCase) -or $seen.ContainsKey($path)) { throw 'Unsafe or duplicate bundle path' }
        $seen[$path] = $true
        $item = Get-Item -LiteralPath $path
        $part = $path
        while ($part.Length -ge $base.TrimEnd('\').Length) {
            if ((Get-Item -LiteralPath $part).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Bundle must not contain links' }
            $part = Split-Path -Parent $part
        }
        if ($item.PSIsContainer -or (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash -ine $file.sha256) { throw "Bundle checksum mismatch: $($file.path)" }
    }
    foreach ($required in @('tools\filo.exe','scripts\system-service.ps1',
        'scripts\windows\Desktop-Identity.ps1',
        'scripts\resolve-desktop-runtime.ps1','scripts\windows\Desktop-Runtime.ps1',
        'scripts\windows\Host-Lifecycle.ps1',
        'scripts\windows\Installer-Tasks.ps1','scripts\windows\Install-Transaction.ps1',
        'scripts\windows\Uninstall-Transaction.ps1','scripts\windows\Service-State.ps1',
        'scripts\windows\Run-Standalone.ps1','scripts\windows\Worker-Update.ps1','tools\nssm.exe','tools\FiloBackground.exe')) {
        if (-not $seen.ContainsKey([IO.Path]::GetFullPath((Join-Path $base $required)))) { throw "Incomplete bundle: $required" }
    }
    return $manifest
}
function Protect-FiloDirectory {
    param([string]$Path, [string]$UserSid, [switch]$ReadOnlyUser)
    New-Item -ItemType Directory -Path $Path -Force | Out-Null
    $acl = [Security.AccessControl.DirectorySecurity]::new()
    $acl.SetAccessRuleProtection($true, $false)
    foreach ($sid in @('S-1-5-18','S-1-5-32-544')) {
        $acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new([Security.Principal.SecurityIdentifier]::new($sid),
            'FullControl','ContainerInherit,ObjectInherit','None','Allow'))
    }
    if ($UserSid) {
        $rights = if ($ReadOnlyUser) { 'ReadAndExecute' } else { 'FullControl' }
        $acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new([Security.Principal.SecurityIdentifier]::new($UserSid),
            $rights,'ContainerInherit,ObjectInherit','None','Allow'))
    }
    Set-Acl -LiteralPath $Path -AclObject $acl
}
function Write-FiloJson {
    param([string]$Path, $Value)
    $temporary = $Path + '.' + [guid]::NewGuid().ToString('N') + '.tmp'
    $backup = $temporary + '.backup'
    try {
        [IO.File]::WriteAllText($temporary, ($Value | ConvertTo-Json -Depth 8), [Text.UTF8Encoding]::new($false))
        if (Test-Path -LiteralPath $Path) { [IO.File]::Replace($temporary, $Path, $backup) }
        else { Move-Item -LiteralPath $temporary -Destination $Path }
    } finally {
        foreach ($leftover in @($temporary,$backup)) {
            if (Test-Path -LiteralPath $leftover) { Remove-Item -LiteralPath $leftover -Force }
        }
    }
}
function New-FiloToken {
    param([string]$Path)
    if (Test-Path -LiteralPath $Path) {
        if ([IO.File]::ReadAllText($Path).Trim() -notmatch '^[0-9a-fA-F]{64}$') { throw 'Existing Filo token is invalid; it was preserved' }
        return
    }
    $bytes = New-Object byte[] 32
    $random = [Security.Cryptography.RandomNumberGenerator]::Create()
    try { $random.GetBytes($bytes) } finally { $random.Dispose() }
    [IO.File]::WriteAllText($Path, ([BitConverter]::ToString($bytes)).Replace('-','').ToLowerInvariant())
}
function Wait-FiloEndpoint {
    param([string]$Url, [string]$TokenPath, [string]$Mode, [int]$Seconds=30, [string]$FiloPath)
    $token = [IO.File]::ReadAllText($TokenPath).Trim()
    $deadline = [DateTime]::UtcNow.AddSeconds($Seconds)
    do {
        try {
            if ($Mode -eq 'existing') {
                if (-not $FiloPath) { throw 'Encrypted public readiness requires the verified Filo binary' }
                $response = & $FiloPath request -url $Url -token-file $TokenPath -timeout 2s | Out-String
                if ($LASTEXITCODE -ne 0) { throw 'Encrypted Filo readiness failed' }
                $info = ($response -join "`n") | ConvertFrom-Json
            } else {
                $info = Invoke-RestMethod -Uri ($Url + '/v1/info') -Headers @{Authorization="Bearer $token"} -TimeoutSec 2 -DisableKeepAlive
            }
            if ($info.protocolVersion -eq 2 -and $info.sessionMode -eq $Mode) { return }
        } catch { }
        Start-Sleep -Milliseconds 300
    } while ([DateTime]::UtcNow -lt $deadline)
    throw "Filo endpoint did not become ready: $Url. Check the Filo logs."
}
function Assert-FiloPrivatePath {
    param([string]$Path, [string]$Root)
    $rootPath = [IO.Path]::GetFullPath($Root).TrimEnd('\') + '\'
    $full = [IO.Path]::GetFullPath($Path)
    if (-not $full.StartsWith($rootPath, [StringComparison]::OrdinalIgnoreCase)) { throw 'Path must stay inside its Filo directory' }
    for ($part = $full; $part -and $part.Length -ge $rootPath.TrimEnd('\').Length; $part = Split-Path -Parent $part) {
        $item = Get-Item -LiteralPath $part -Force -ErrorAction SilentlyContinue
        if ($item -and ($item.Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw 'Filo installation paths must not contain links' }
    }
    return $full
}
function Grant-FiloDirectoryRead {
    param([string]$Path, [string]$UserSid)
    New-Item -ItemType Directory -Path $Path -Force | Out-Null
    $acl = Get-Acl -LiteralPath $Path
    # The user-scoped helper traverses every parent directory to reach its scripts. This folder-only grant must not expose private descendants.
    $acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new(
        [Security.Principal.SecurityIdentifier]::new($UserSid), 'ReadAndExecute', 'None', 'None', 'Allow'))
    Set-Acl -LiteralPath $Path -AclObject $acl
}
function Install-FiloRuntime {
    param([string]$Bundle, $Manifest, [string]$Root, [string]$Sid)
    $target = Assert-FiloPrivatePath (Join-Path $Root ('releases\' + $Manifest.revision)) $Root
    Grant-FiloDirectoryRead $Root $Sid
    Grant-FiloDirectoryRead (Split-Path -Parent $target) $Sid
    if (Test-Path -LiteralPath $target) {
        $existing = Get-FiloBundle $target
        if (($existing | ConvertTo-Json -Depth 8 -Compress) -ne ($Manifest | ConvertTo-Json -Depth 8 -Compress)) { throw 'Immutable release already exists with different contents' }
        return $target
    }
    $stage = Assert-FiloPrivatePath ($target + '.staging-' + [guid]::NewGuid().ToString('N')) $Root
    Protect-FiloDirectory $stage $Sid -ReadOnlyUser
    try {
        foreach ($file in $Manifest.files) {
            $destination = Join-Path $stage $file.path
            New-Item -ItemType Directory -Path (Split-Path -Parent $destination) -Force | Out-Null
            Copy-Item -LiteralPath (Join-Path $Bundle $file.path) -Destination $destination
        }
        Copy-Item -LiteralPath (Join-Path $Bundle 'manifest.json') -Destination $stage
        Get-FiloBundle $stage | Out-Null
        Move-Item -LiteralPath $stage -Destination $target
    } finally {
        if (Test-Path -LiteralPath $stage) {
            $safe = Assert-FiloPrivatePath $stage $Root
            Remove-Item -LiteralPath $safe -Recurse -Force
        }
    }
    return $target
}
