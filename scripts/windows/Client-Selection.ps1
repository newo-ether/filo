# Read-only discovery and selection of native sources under one Windows account.
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
        if ($applications.Count) {
            [pscustomobject]@{
                Id='codex-desktop:' + $profile.SID; Source='codex-desktop'; Name="Codex Desktop ($account)"; Supported=$true
                RuntimeState='desktop-installed'; Detail='Original Desktop; existing task controls and history'
                Profile=$profile.LocalPath; Sid=$profile.SID; Account=$account; Desktop=$applications[0].Executable; CliCandidates=$cli
            }
        }
        if ($cli.Count -or (Test-Path -LiteralPath (Join-Path $profile.LocalPath '.codex') -PathType Container)) {
            [pscustomobject]@{
                Id='codex-cli:' + $profile.SID; Source='codex-cli'; Name="Codex CLI ($account)"; Supported=$true
                RuntimeState='discovered'; Detail='Read-only native history; optional existing daemon metadata, no daemon startup'
                Profile=$profile.LocalPath; Sid=$profile.SID; Account=$account; Desktop=$null; CliCandidates=$cli
            }
        }
        $claude = @(@('.local\bin\claude.exe','AppData\Roaming\npm\claude.cmd') |
            ForEach-Object { Join-Path $profile.LocalPath $_ } | Where-Object { Test-Path -LiteralPath $_ -PathType Leaf })
        if ($claude.Count -or (Test-Path -LiteralPath (Join-Path $profile.LocalPath '.claude') -PathType Container)) {
            [pscustomobject]@{
                Id='claude-code:' + $profile.SID; Source='claude-code'; Name="Claude Code ($account)"; Supported=$true
                RuntimeState='discovered'; Detail='Read-only live observation; requires explicitly loaded Filo Mod, no send or stop'
                Profile=$profile.LocalPath; Sid=$profile.SID; Account=$account; Desktop=$null; CliCandidates=@()
            }
        }
    }
    if (-not $profiles.Count) { Write-Warning 'No eligible Windows user profile was found.' }
}
function Get-FiloClientSource {
    param($Client)
    if ($Client.PSObject.Properties['Source']) { return [string]$Client.Source }
    $prefix = $Client.Id.Split(':')[0]
    if ($prefix -eq 'codex') { return 'codex-desktop' }
    return $prefix
}
function Resolve-FiloSelection {
    param([object[]]$Clients, [string[]]$Ids)
    $selected = @()
    foreach ($requested in $Ids) {
        $id = if ($requested -eq 'codex') { 'codex-desktop' }
            elseif ($requested.StartsWith('codex:')) { 'codex-desktop:' + $requested.Substring(6) } else { $requested }
        $matches = @($Clients | Where-Object {
            $_.Id -eq $id -or (Get-FiloClientSource $_) -eq $id -or
            ($_.Id -eq $requested -and (Get-FiloClientSource $_) -eq 'codex-desktop')
        })
        if (-not $matches.Count) { throw "Client '$requested' was not discovered. Run -ListClients to see available IDs." }
        $selected += $matches
    }
    $selected = @($selected | Sort-Object Id -Unique)
    if (-not $selected.Count) { return @() }
    if (@($selected | Where-Object { -not $_.Supported }).Count) { throw 'An unsupported client cannot be installed.' }
    $sids = @($selected | ForEach-Object {
        if ($_.PSObject.Properties['Sid']) { $_.Sid } else { $_.Id.Substring($_.Id.IndexOf(':') + 1) }
    } | Select-Object -Unique)
    if ($sids.Count -ne 1) { throw 'Choose native sources belonging to the same Windows account. Use full client IDs to disambiguate.' }
    return $selected
}
function Select-FiloClients {
    param([object[]]$Clients)
    if (-not $Clients.Count) { throw 'No supported native sources found. Install or open the intended native client, then discover again.' }
    if ([Console]::IsInputRedirected -or [Console]::IsOutputRedirected) {
        throw 'Interactive selection needs a terminal. Use -ListClients, then -Clients <id> for unattended installation.'
    }
    Write-Host 'Select sources from one Windows account' -ForegroundColor Cyan
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
