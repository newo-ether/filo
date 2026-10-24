# Read-only discovery and selection of native sources under one Windows account.
Set-StrictMode -Version 2
. (Join-Path $PSScriptRoot 'Desktop-Identity.ps1')
. (Join-Path $PSScriptRoot 'Claude-Identity.ps1')
function Get-FiloClaudeCodeVersion {
    # Read-only: never execute the native client to learn its version.
    param([string]$Path)
    try {
        if ($Path -like '*.cmd') {
            $package = Join-Path (Split-Path -Parent $Path) 'node_modules\@anthropic-ai\claude-code\package.json'
            $text = [string](Get-Content -LiteralPath $package -Raw -ErrorAction Stop | ConvertFrom-Json).version
        } else { $text = [string](Get-Item -LiteralPath $Path -ErrorAction Stop).VersionInfo.ProductVersion }
        if ($text -match '^(\d+\.\d+\.\d+)') { return [version]$Matches[1] }
    } catch { }
    return $null
}
function Get-FiloClaudeCodeCompatibility {
    param([string[]]$Paths)
    $minimum = [version]'2.1.287'
    $versions = @($Paths | ForEach-Object { Get-FiloClaudeCodeVersion $_ } | Where-Object { $_ } | Sort-Object -Descending)
    if (-not $versions.Count) {
        return [pscustomobject]@{ Version=$null; State='version-unknown'
            Label="version unknown; observer needs $minimum+" }
    }
    $version = $versions[0]
    if ($version -lt $minimum) {
        return [pscustomobject]@{ Version=[string]$version; State='needs-upgrade'
            Label="version $version is below $minimum; installs but stays offline until Claude Code is upgraded" }
    }
    [pscustomobject]@{ Version=[string]$version; State='compatible'; Label="version $version, compatible" }
}
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
        $cli = @(@(Get-ChildItem -LiteralPath $cliRoot -Directory -ErrorAction SilentlyContinue |
            ForEach-Object { Join-Path $_.FullName 'codex.exe' }) + @(
                (Join-Path $profile.LocalPath '.codex\packages\standalone\current\bin\codex.exe'),
                (Join-Path $profile.LocalPath '.local\bin\codex.exe'),
                (Join-Path $profile.LocalPath 'AppData\Roaming\npm\codex.cmd')) |
            Where-Object { Test-Path -LiteralPath $_ -PathType Leaf })
        if ($applications.Count) {
            [pscustomobject]@{
                Id='codex-desktop:' + $profile.SID; Source='codex-desktop'; Name="Codex Desktop ($account)"; Supported=$true
                RuntimeState='desktop-installed'; Detail='Original Desktop; existing task controls and history'
                Profile=$profile.LocalPath; Sid=$profile.SID; Account=$account; Desktop=$applications[0].Executable; CliCandidates=$cli
            }
        }
        if ($cli.Count) {
            [pscustomobject]@{
                Id='codex-cli:' + $profile.SID; Source='codex-cli'; Name="Codex CLI ($account)"; Supported=$true
                RuntimeState='discovered'; Detail='Read-only native history; optional existing daemon metadata, no daemon startup'
                Profile=$profile.LocalPath; Sid=$profile.SID; Account=$account; Desktop=$null; CliCandidates=$cli
            }
        }
        $claude = @(@('.local\bin\claude.exe','AppData\Roaming\npm\claude.cmd') |
            ForEach-Object { Join-Path $profile.LocalPath $_ } | Where-Object { Test-Path -LiteralPath $_ -PathType Leaf })
        if ($claude.Count) {
            $compatibility = Get-FiloClaudeCodeCompatibility $claude
            $suffix = if ($compatibility.State -eq 'compatible') { '' } elseif ($compatibility.State -eq 'needs-upgrade') { ' [needs upgrade]' } else { ' [version unknown]' }
            [pscustomobject]@{
                Id='claude-code:' + $profile.SID; Source='claude-code'; Name="Claude Code ($account)$suffix"; Supported=$true
                Version=$compatibility.Version; Compatibility=$compatibility.State
                RuntimeState='discovered'; Detail="Automatic observer, read-only; $($compatibility.Label); Mods must be enabled"
                Profile=$profile.LocalPath; Sid=$profile.SID; Account=$account; Desktop=$null; CliCandidates=@()
            }
        }
        $claudeDesktop = @(Get-FiloClaudeDesktopApplications -UserSid $profile.SID -Profile $profile.LocalPath)
        if ($claudeDesktop.Count) {
            [pscustomobject]@{
                Id='claude-desktop:' + $profile.SID; Source='claude-desktop'; Name="Claude Desktop ($account) [version unverified]"; Supported=$true
                Version=$null; Compatibility='version-unknown'
                RuntimeState='desktop-installed'; Detail='Local Code only; embedded engine version cannot be read before launch, observer needs 2.1.286+ with Mods enabled'
                Profile=$profile.LocalPath; Sid=$profile.SID; Account=$account; Desktop=$claudeDesktop[0].Executable; CliCandidates=@()
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
    # powershell.exe -File passes "-Clients a,b" as one string; accept both forms.
    $Ids = @($Ids | ForEach-Object { "$_".Split(',') } | ForEach-Object { $_.Trim() } | Where-Object { $_ })
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
    param([object[]]$Clients, [string[]]$InitialIds = @())
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
    foreach ($item in $Clients) {
        if ($item.Supported -and $InitialIds -contains $item.Id) { $checked[$item.Id] = $true }
    }
    for ($i = 0; $i -lt $Clients.Count; $i++) { Write-Host ''; Write-Host '' }
    try {
        while ($true) {
            $Host.UI.RawUI.CursorPosition = $origin
            $width = [Math]::Max(20, $Host.UI.RawUI.BufferSize.Width - 2)
            for ($i = 0; $i -lt $Clients.Count; $i++) {
                $item = $Clients[$i]
                $box = if (-not $item.Supported) { '[-]' } elseif ($checked.ContainsKey($item.Id)) { '[x]' } else { '[ ]' }
                $lead = if ($i -eq $cursor) { '>' } else { ' ' }
                $warn = $item.PSObject.Properties['Compatibility'] -and $item.Compatibility -ne 'compatible'
                $color = if (-not $item.Supported) { 'DarkGray' } elseif ($i -eq $cursor) { 'Cyan' } elseif ($warn) { 'Yellow' } else { 'White' }
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
