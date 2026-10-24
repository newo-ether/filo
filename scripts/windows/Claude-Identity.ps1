# Read-only discovery of the original Claude Desktop under the selected account.
Set-StrictMode -Version 2
function Get-FiloClaudeDesktopApplications {
    param([string]$UserSid, [string]$Profile)
    $paths = [Collections.Generic.List[string]]::new()
    $current = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    try {
        $packages = if ($current -eq $UserSid) { @(Get-AppxPackage -Name '*Claude*' -ErrorAction Stop) }
            else { @(Get-AppxPackage -User $UserSid -Name '*Claude*' -ErrorAction Stop) }
        foreach ($package in $packages) {
            $manifest = Join-Path $package.InstallLocation 'AppxManifest.xml'
            if (-not (Test-Path -LiteralPath $manifest -PathType Leaf)) { continue }
            $document = [xml](Get-Content -LiteralPath $manifest -Raw)
            foreach ($application in @($document.Package.Applications.Application)) {
                if (-not $application.Executable) { continue }
                $root = [IO.Path]::GetFullPath($package.InstallLocation).TrimEnd('\') + '\'
                $path = [IO.Path]::GetFullPath((Join-Path $root ([string]$application.Executable)))
                if ($path.StartsWith($root, [StringComparison]::OrdinalIgnoreCase)) { $paths.Add($path) }
            }
        }
    } catch { }
    foreach ($relative in @('AppData\Local\AnthropicClaude\claude.exe', 'AppData\Local\Programs\Claude\Claude.exe')) {
        $paths.Add((Join-Path $Profile $relative))
    }
    foreach ($registry in @("Registry::HKEY_USERS\$UserSid\Software\Microsoft\Windows\CurrentVersion\Uninstall",
        'HKLM:\Software\Microsoft\Windows\CurrentVersion\Uninstall',
        'HKLM:\Software\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall')) {
        foreach ($key in @(Get-ChildItem -LiteralPath $registry -ErrorAction SilentlyContinue)) {
            $record = Get-ItemProperty -LiteralPath $key.PSPath -ErrorAction SilentlyContinue
            if (-not $record -or -not $record.PSObject.Properties['DisplayName'] -or
                [string]$record.DisplayName -notmatch '^Claude(?: Desktop)?$' -or
                -not $record.PSObject.Properties['Publisher'] -or [string]$record.Publisher -notmatch 'Anthropic') { continue }
            if ($record.PSObject.Properties['InstallLocation'] -and $record.InstallLocation) {
                $paths.Add((Join-Path ([string]$record.InstallLocation) 'claude.exe'))
            }
            if ($record.PSObject.Properties['DisplayIcon'] -and $record.DisplayIcon) {
                $icon = ([string]$record.DisplayIcon -replace ',\s*\d+$','').Trim('"')
                if ([IO.Path]::IsPathRooted($icon)) { $paths.Add($icon) }
            }
        }
    }
    foreach ($path in @($paths | Select-Object -Unique)) {
        if ([IO.Path]::GetFileName($path) -ine 'Claude.exe' -or
            -not (Test-Path -LiteralPath $path -PathType Leaf)) { continue }
        try {
            $signature = Get-AuthenticodeSignature -LiteralPath $path -ErrorAction Stop
            if ($signature.Status -ne 'Valid' -or -not $signature.SignerCertificate -or
                $signature.SignerCertificate.Subject -notmatch '(?:^|,\s*)(?:CN|O)=(?:"Anthropic(?:, PBC)?"|Anthropic(?:, PBC)?)(?=,|$)') { continue }
            [pscustomobject]@{ Executable = $path; Source = 'claude-desktop' }
        } catch { }
    }
}
