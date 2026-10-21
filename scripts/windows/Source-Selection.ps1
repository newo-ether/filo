# Native-source configuration is independent of process discovery and never edits native settings.
Set-StrictMode -Version 2
function Select-FiloClaudeModCopy {
    param([string]$Profile, [string]$Sid)
    if ([Console]::IsInputRedirected -or [Console]::IsOutputRedirected) { return '' }
    $checked=$false
    Write-Host 'Optional Claude observer copy' -ForegroundColor Cyan
    Write-Host '  Space: check/uncheck   Enter: continue   Esc: skip'
    $origin=$Host.UI.RawUI.CursorPosition
    Write-Host ''
    while ($true) {
        $Host.UI.RawUI.CursorPosition=$origin
        $box=if ($checked) { '[x]' } else { '[ ]' }
        Write-Host ("  $box Copy the Filo Mod to a directory I choose")
        $key=$Host.UI.RawUI.ReadKey('NoEcho,IncludeKeyDown')
        if ($key.VirtualKeyCode -eq 32) { $checked=-not $checked }
        elseif ($key.VirtualKeyCode -eq 27) { Write-Host ''; return '' }
        elseif ($key.VirtualKeyCode -eq 13) { break }
    }
    Write-Host ''
    if (-not $checked) { return '' }
    $destination=Read-Host 'New absolute Mod directory inside the selected user profile (blank skips)'
    if (-not $destination) { return '' }
    return Assert-FiloClaudeModDestination $destination $Profile $Sid
}
function Get-FiloConfiguredSources {
    param($Config)
    if (-not $Config -or -not $Config.PSObject.Properties['sources']) { return @('codex-desktop') }
    $values = @($Config.sources)
    if ($values.Count -lt 1 -or $values.Count -gt 3 -or $Config.sources -is [string]) { throw 'Expected one to three configured Filo sources' }
    $seen = @{}
    foreach ($value in $values) {
        if ($value -isnot [string] -or $value -cnotin @('codex-desktop','codex-cli','claude-code') -or $seen.ContainsKey($value)) {
            throw 'Invalid or duplicate configured Filo source'
        }
        $seen[$value] = $true
    }
    return $values
}
function Get-FiloSavedClients {
    param($Config)
    $sid = [string]$Config.desktopSid
    if ($sid -notmatch '^S-1-(5-21|12-1)-[0-9-]+$') { throw 'Invalid configured Windows account SID' }
    $profile = Get-ItemPropertyValue -LiteralPath "HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\ProfileList\$sid" -Name ProfileImagePath
    foreach ($source in @(Get-FiloConfiguredSources $Config)) {
        [pscustomobject]@{Id=($source + ':' + $sid);Source=$source;Sid=$sid;Profile=$profile;
            Name=($source + ' (' + $sid + ')');Supported=$true;Detail='Previously configured source; native availability is checked at runtime'}
    }
}
function Get-FiloSourceOptions {
    param([string]$Worker, [string[]]$Sources, [string]$RequestedSocket, [bool]$SocketSpecified,
        [int]$RequestedBridgePort, [bool]$BridgePortSpecified, [int]$PublicPort)
    $sourcesConfig = [pscustomobject]@{sources=$Sources}
    $sources = @(Get-FiloConfiguredSources $sourcesConfig)
    $configPath = Join-Path $Worker 'config.json'
    $previous = if (Test-Path -LiteralPath $configPath) { Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json } else { $null }
    if ($SocketSpecified -and -not $RequestedSocket) { throw 'CodexDaemonSocket must name an existing absolute local socket' }
    $socket = if ($SocketSpecified) { $RequestedSocket }
        elseif ($sources -contains 'codex-cli' -and $previous -and $previous.PSObject.Properties['codexDaemonSocket']) { [string]$previous.codexDaemonSocket } else { '' }
    if ($socket -and ($sources -notcontains 'codex-cli' -or -not [IO.Path]::IsPathRooted($socket) -or
        $socket.IndexOf([char]0) -ge 0 -or [Text.Encoding]::UTF8.GetByteCount($socket) -ge 108 -or
        ($SocketSpecified -and -not (Test-Path -LiteralPath $socket)))) { throw 'Codex daemon socket must be an existing absolute local socket path for the selected CLI source' }
    $bridgePort = if ($BridgePortSpecified) { $RequestedBridgePort }
        elseif ($previous -and $previous.PSObject.Properties['claudeBridgePort']) { $previous.claudeBridgePort } else { 7438 }
    if (($bridgePort -isnot [int] -and $bridgePort -isnot [long]) -or $bridgePort -lt 1024 -or $bridgePort -gt 65535) {
        throw 'Invalid Claude bridge port'
    }
    $endpoints = Get-FiloHelperEndpoints $Worker
    $nativePort = if ($sources -contains 'codex-desktop') {
        if ($endpoints.NativeUrl) { ([uri]$endpoints.NativeUrl).Port } else { 7436 }
    } else { 0 }
    if ($PublicPort -eq $endpoints.WorkerPort -or ($nativePort -and
        ($PublicPort -eq $nativePort -or $nativePort -eq $endpoints.WorkerPort))) { throw 'Public, worker and selected native ports must differ' }
    if ($sources -contains 'claude-code' -and ($bridgePort -eq $endpoints.WorkerPort -or
        $bridgePort -eq $PublicPort -or ($nativePort -and $bridgePort -eq $nativePort))) {
        throw 'Claude bridge port must differ from public, worker and native ports'
    }
    return [pscustomobject]@{Sources=$sources;CodexDaemonSocket=$socket;ClaudeBridgePort=[int]$bridgePort}
}
function Assert-FiloClaudeModDestination {
    param([string]$Destination, [string]$Profile, [string]$Sid)
    if (-not $Destination -or -not [IO.Path]::IsPathRooted($Destination)) { throw 'Choose an absolute, user-owned directory for the optional Claude Mod' }
    $profileRoot = [IO.Path]::GetFullPath($Profile).TrimEnd('\') + '\'
    $full = [IO.Path]::GetFullPath($Destination).TrimEnd('\')
    if (-not $full.StartsWith($profileRoot,[StringComparison]::OrdinalIgnoreCase) -or (Test-Path -LiteralPath $full)) {
        throw 'Claude Mod destination must be a new directory inside the selected user profile'
    }
    if ($full.Substring($profileRoot.Length).Split('\') -contains '.claude') { throw 'Choose a separate Mod directory; native .claude settings are never modified' }
    $parent = Split-Path -Parent $full
    if (-not (Test-Path -LiteralPath $parent -PathType Container)) { throw 'Claude Mod destination parent must already exist' }
    for ($part=$parent; $part -and $part.Length -ge $profileRoot.TrimEnd('\').Length; $part=Split-Path -Parent $part) {
        if ((Get-Item -LiteralPath $part -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Claude Mod destination must not contain links' }
    }
    $owner = (Get-Acl -LiteralPath $parent).GetOwner([Security.Principal.SecurityIdentifier]).Value
    if ($owner -ne $Sid) { throw 'Claude Mod destination parent must belong to the selected Windows account' }
    return $full
}
function Copy-FiloClaudeMod {
    param([string]$Bundle, [string]$Destination, [string]$Profile, [string]$Sid)
    $target = Assert-FiloClaudeModDestination $Destination $Profile $Sid
    $source = Join-Path $Bundle 'internal\claude\mod'
    foreach ($relative in @('.claude-plugin\plugin.json','hooks\hooks.json','hooks\register.js')) {
        if (-not (Test-Path -LiteralPath (Join-Path $source $relative) -PathType Leaf)) { throw 'Bundle is missing the Claude Mod payload' }
    }
    Protect-FiloDirectory $target $Sid
    foreach ($relative in @('.claude-plugin\plugin.json','hooks\hooks.json','hooks\register.js')) {
        $file = Join-Path $target $relative
        New-Item -ItemType Directory -Path (Split-Path -Parent $file) -Force | Out-Null
        Copy-Item -LiteralPath (Join-Path $source $relative) -Destination $file
    }
    Write-Output "Claude observer copied to $target. Load it explicitly in Claude Code; native settings were not changed."
}
