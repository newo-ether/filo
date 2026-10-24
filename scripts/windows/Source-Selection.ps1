# Native-source configuration is independent of process discovery and native execution.
Set-StrictMode -Version 2
function Get-FiloConfiguredSources {
    param($Config)
    if (-not $Config -or -not $Config.PSObject.Properties['sources']) { return @('codex-desktop') }
    $values = @($Config.sources)
    if ($values.Count -lt 1 -or $values.Count -gt 4 -or $Config.sources -is [string]) { throw 'Expected one to four installed Filo sources' }
    $seen = @{}
    foreach ($value in $values) {
        if ($value -isnot [string] -or $value -cnotin @('codex-desktop','codex-cli','claude-code','claude-desktop') -or $seen.ContainsKey($value)) {
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
            Name=($source + ' (' + $sid + ')');Supported=$true;Detail='Previously installed integration; native availability is checked at runtime'}
    }
}
function Get-FiloSourceOptions {
    param([string]$Worker, [string[]]$Sources, [string]$RequestedSocket, [bool]$SocketSpecified,
        [int]$RequestedBridgePort, [bool]$BridgePortSpecified, [int]$PublicPort, [switch]$IgnoreExisting)
    $sourcesConfig = [pscustomobject]@{sources=$Sources}
    $sources = @(Get-FiloConfiguredSources $sourcesConfig)
    $configPath = Join-Path $Worker 'config.json'
    $previous = if (-not $IgnoreExisting -and (Test-Path -LiteralPath $configPath)) { Get-Content -LiteralPath $configPath -Raw | ConvertFrom-Json } else { $null }
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
    $endpoints = Get-FiloHelperEndpoints $Worker -IgnoreExisting:$IgnoreExisting
    $nativePort = if ($sources -contains 'codex-desktop') {
        if ($endpoints.NativeUrl) { ([uri]$endpoints.NativeUrl).Port } else { 7436 }
    } else { 0 }
    if ($PublicPort -eq $endpoints.WorkerPort -or ($nativePort -and
        ($PublicPort -eq $nativePort -or $nativePort -eq $endpoints.WorkerPort))) { throw 'Public, worker and selected native ports must differ' }
    if (@($sources | Where-Object { $_ -in @('claude-code','claude-desktop') }).Count -and ($bridgePort -eq $endpoints.WorkerPort -or
        $bridgePort -eq $PublicPort -or ($nativePort -and $bridgePort -eq $nativePort))) {
        throw 'Claude bridge port must differ from public, worker and native ports'
    }
    return [pscustomobject]@{Sources=$sources;CodexDaemonSocket=$socket;ClaudeBridgePort=[int]$bridgePort}
}
