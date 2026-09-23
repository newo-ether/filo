# Read-only discovery and terminal selection. No client configuration is opened or changed.
Set-StrictMode -Version 2
. (Join-Path $PSScriptRoot 'Desktop-Identity.ps1')

function Get-FiloClients {
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $elevated = ([Security.Principal.WindowsPrincipal]::new($identity)).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
    $profiles = @(Get-CimInstance Win32_UserProfile | Where-Object {
        -not $_.Special -and $_.SID -match '^S-1-(5-21|12-1)-' -and
        ($elevated -or $_.SID -eq $identity.User.Value)
    })
    foreach ($profile in $profiles) {
        $applications = @()
        try { $applications = @(Get-FiloDesktopApplications -UserSid $profile.SID) } catch { }
        $account = try { ([Security.Principal.SecurityIdentifier]::new($profile.SID)).Translate([Security.Principal.NTAccount]).Value } catch { $profile.SID }
        $cliRoot = Join-Path $profile.LocalPath 'AppData\Local\OpenAI\Codex\bin'
        $cli = @(Get-ChildItem -LiteralPath $cliRoot -Directory -ErrorAction SilentlyContinue |
            ForEach-Object { Join-Path $_.FullName 'codex.exe' } | Where-Object { Test-Path -LiteralPath $_ -PathType Leaf })
        if ($applications.Count -or $cli.Count) {
            $reason = if (-not $applications.Count) { 'Install and open the original Codex desktop for this account' }
                elseif (-not $profile.Loaded) { 'Codex Desktop installed; sign in to this Windows account to connect' }
                else { 'Codex Desktop installed; no separate CLI is required' }
            [pscustomobject]@{
                Id = 'codex:' + $profile.SID; Name = "Codex ($account)"; Supported = [bool]$applications.Count
                RuntimeState = if ($applications.Count) { 'desktop-installed' } else { 'desktop-missing' }
                Detail = $reason; Profile = $profile.LocalPath; Sid = $profile.SID; Account = $account
                Desktop = if ($applications.Count) { $applications[0].Executable } else { $null }
                CliCandidates = $cli
            }
        }
        foreach ($adapter in @(
            @{ Id='claude-code'; Name='Claude Code'; Commands=@('.local\bin\claude.exe','AppData\Roaming\npm\claude.cmd'); Marker='.claude' },
            @{ Id='opencode'; Name='OpenCode'; Commands=@('.opencode\bin\opencode.exe','AppData\Roaming\npm\opencode.cmd'); Marker='.config\opencode' }
        )) {
            $found = @( $adapter.Commands | ForEach-Object { Join-Path $profile.LocalPath $_ } |
                Where-Object { Test-Path -LiteralPath $_ -PathType Leaf } )
            if ($found.Count -or (Test-Path -LiteralPath (Join-Path $profile.LocalPath $adapter.Marker))) {
                [pscustomobject]@{
                    Id=$adapter.Id + ':' + $profile.SID; Name="$($adapter.Name) ($account)"; Supported=$false
                    Detail='Detected; Filo adapter is not implemented'; Profile=$profile.LocalPath; Sid=$profile.SID
                    Account=$account; Desktop=$null; CliCandidates=@()
                }
            }
        }
    }
    if (-not $profiles.Count) {
        Write-Warning 'No signed-in desktop user was found. Sign in to the target Windows account and run discovery again.'
    }
}

function Resolve-FiloSelection {
    param([object[]]$Clients, [string[]]$Ids)
    $selected = @($Clients | Where-Object { $_.Id -in $Ids -or ($_.Id.Split(':')[0] -in $Ids) })
    foreach ($id in $Ids) {
        if (-not @($selected | Where-Object { $_.Id -eq $id -or $_.Id.Split(':')[0] -eq $id }).Count) {
            throw "Client '$id' was not discovered. Run -ListClients to see available IDs."
        }
    }
    if (-not $selected.Count) { return @() }
    if (@($selected | Where-Object { -not $_.Supported }).Count) { throw 'An unsupported client cannot be installed.' }
    if ($selected.Count -gt 1) { throw 'This package supports one Codex desktop account. Select its full client ID.' }
    return $selected
}

function Select-FiloClients {
    param([object[]]$Clients)
    if (-not $Clients.Count) { throw 'No clients found. Install and open a supported Codex desktop, then try again.' }
    if ([Console]::IsInputRedirected -or [Console]::IsOutputRedirected) {
        throw 'Interactive selection needs a terminal. Use -ListClients, then -Clients <id> for unattended installation.'
    }
    Write-Host 'Select clients' -ForegroundColor Cyan
    Write-Host '  Up/Down: move   Space: check/uncheck   Enter: install   Esc: cancel'
    Write-Host ''
    $origin = $Host.UI.RawUI.CursorPosition
    $cursor = 0
    $checked = @{}
    for ($i = 0; $i -lt $Clients.Count; $i++) { Write-Host ''; Write-Host '' }
    try {
        while ($true) {
            $Host.UI.RawUI.CursorPosition = $origin
            $width = [Math]::Max(20, $Host.UI.RawUI.BufferSize.Width - 2)
            for ($i = 0; $i -lt $Clients.Count; $i++) {
                $item = $Clients[$i]
                $box = if (-not $item.Supported) { '[-]' } elseif ($checked.ContainsKey($item.Id)) { '[x]' } else { '[ ]' }
                $lead = if ($i -eq $cursor) { '>' } else { ' ' }
                $color = if (-not $item.Supported) { 'DarkGray' } elseif ($i -eq $cursor) { 'Cyan' } else { 'White' }
                foreach ($line in @(" $lead $box $($item.Name)", "       $($item.Detail)")) {
                    if ($line.Length -gt $width) { $line = $line.Substring(0, $width - 3) + '...' }
                    Write-Host $line.PadRight($width) -ForegroundColor $color
                }
            }
            $key = $Host.UI.RawUI.ReadKey('NoEcho,IncludeKeyDown')
            switch ($key.VirtualKeyCode) {
                38 { $cursor = ($cursor + $Clients.Count - 1) % $Clients.Count }
                40 { $cursor = ($cursor + 1) % $Clients.Count }
                32 {
                    $item = $Clients[$cursor]
                    if ($item.Supported) {
                        if ($checked.ContainsKey($item.Id)) { $checked.Remove($item.Id) } else { $checked[$item.Id] = $true }
                    }
                }
                13 { return @(Resolve-FiloSelection -Clients $Clients -Ids @($checked.Keys)) }
                27 { return @() }
            }
        }
    } finally { Write-Host '' }
}
