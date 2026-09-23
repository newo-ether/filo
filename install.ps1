<#
.SYNOPSIS
One-command Filo Windows installation, matching the Conch installer workflow.
.EXAMPLE
irm https://raw.githubusercontent.com/newo-ether/filo/main/install.ps1 | iex
.EXAMPLE
.\install.ps1 -Version v0.1.0 -Clients codex:<Windows-SID>
#>
[CmdletBinding()]
param(
    [ValidatePattern('^(latest|v[0-9]+\.[0-9]+\.[0-9]+)$')]
    [string]$Version = 'latest',
    [string[]]$Clients,
    [switch]$ListClients,
    [switch]$ValidateOnly,
    [switch]$Uninstall,
    [string]$Bind,
    [ValidateRange(1024,65535)][int]$Port = 7435
)
$installerOptions = @{}
foreach ($name in @('Clients','ListClients','ValidateOnly','Uninstall','Bind','Port')) {
    if ($PSBoundParameters.ContainsKey($name)) { $installerOptions[$name] = $PSBoundParameters[$name] }
}

function Assert-FiloDownloadUrl {
    param([uri]$Url)
    if ($Url.Scheme -ne 'https' -or $Url.Port -ne 443 -or $Url.UserInfo -or
        $Url.Host -notin @('api.github.com','github.com','objects.githubusercontent.com','release-assets.githubusercontent.com')) {
        throw 'The release download must stay on the official HTTPS GitHub endpoints.'
    }
}

function Receive-FiloReleaseFile {
    param([uri]$Url, [string]$Destination, [long]$MaximumBytes)
    $originalUrl = $Url
    for ($attempt = 1; $attempt -le 3; $attempt++) {
        try {
            $Url = $originalUrl
            $deadline = [DateTime]::UtcNow.AddMinutes(5)
            for ($redirects = 0; $redirects -le 5; $redirects++) {
                Assert-FiloDownloadUrl $Url
                $request = [Net.HttpWebRequest]::Create($Url)
                $request.AllowAutoRedirect = $false
                $request.Timeout = 30000
                $request.ReadWriteTimeout = 30000
                $request.UserAgent = 'Filo-Installer'
                $response = $null
                try {
                    $response = $request.GetResponse()
                    $status = [int]$response.StatusCode
                    if ($status -in @(301,302,303,307,308)) {
                        if ($redirects -eq 5 -or -not $response.Headers['Location']) { throw 'Too many release redirects.' }
                        $Url = [uri]::new($Url, $response.Headers['Location'])
                        continue
                    }
                    if ($status -ne 200 -or $response.ContentLength -gt $MaximumBytes) {
                        throw 'The release response is invalid or exceeds its download limit.'
                    }
                    $inputStream = $response.GetResponseStream()
                    $outputStream = [IO.File]::Open($Destination, 'Create', 'Write', 'None')
                    try {
                        $buffer = New-Object byte[] 65536
                        [long]$total = 0
                        while (($count = $inputStream.Read($buffer,0,$buffer.Length)) -gt 0) {
                            $total += $count
                            if ($total -gt $MaximumBytes -or [DateTime]::UtcNow -gt $deadline) {
                                throw 'Release download exceeded its size or time limit.'
                            }
                            $outputStream.Write($buffer,0,$count)
                        }
                        if ($total -eq 0 -or ($response.ContentLength -ge 0 -and $total -ne $response.ContentLength)) {
                            throw 'Release download is empty or incomplete.'
                        }
                    } finally { $outputStream.Dispose(); $inputStream.Dispose() }
                    return
                } finally { if ($response) { $response.Dispose() } }
            }
        } catch {
            if (Test-Path -LiteralPath $Destination) { Remove-Item -LiteralPath $Destination -Force }
            if ($attempt -eq 3) { throw }
            Write-Warning "Download interrupted; retry $attempt of 2. Installation has not started."
            Start-Sleep -Seconds 2
        }
    }
}

function Get-FiloReleaseHash {
    param([string]$Manifest, [string]$AssetName)
    $matches = @(Get-Content -LiteralPath $Manifest | Where-Object {
        $_ -match ('^([a-fA-F0-9]{64})\s+\*?' + [regex]::Escape($AssetName) + '$')
    })
    if ($matches.Count -ne 1) { throw 'The release checksum manifest must contain exactly one Windows bundle.' }
    return $matches[0].Substring(0,64)
}

function Expand-FiloRelease {
    param([string]$Archive, [string]$Destination)
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $base = [IO.Path]::GetFullPath($Destination).TrimEnd('\') + '\'
    $zip = [IO.Compression.ZipFile]::OpenRead($Archive)
    try {
        if ($zip.Entries.Count -eq 0 -or $zip.Entries.Count -gt 512) { throw 'Invalid release entry count.' }
        $seen = @{}
        $entries = @()
        $root = $null
        [long]$total = 0
        foreach ($entry in $zip.Entries) {
            $name = $entry.FullName.Replace('\','/')
            $parts = $name.TrimEnd('/').Split('/')
            if ($name.StartsWith('/') -or $name -match '[:*?<>|"]' -or
                $parts[0] -notmatch '^filo-windows-[0-9a-f]{40}$') { throw 'Unsafe release archive path.' }
            foreach ($part in $parts) {
                if (-not $part -or $part -in @('.','..') -or $part -match '[. ]$|[\x00-\x1f]' -or
                    $part -match '^(?i:con|prn|aux|nul|com[0-9]|lpt[0-9])(\.|$)') { throw 'Unsafe release archive component.' }
            }
            if ($root -and $root -cne $parts[0]) { throw 'Release archive has multiple roots.' }
            $root = $parts[0]
            $path = [IO.Path]::GetFullPath((Join-Path $base $name))
            if (-not $path.StartsWith($base,[StringComparison]::OrdinalIgnoreCase) -or $seen.ContainsKey($path)) {
                throw 'Duplicate or escaping release archive path.'
            }
            $seen[$path] = $true
            $mode = ($entry.ExternalAttributes -shr 16) -band 0xF000
            if ($mode -eq 0xA000 -or ($entry.ExternalAttributes -band 0x400)) { throw 'Release archive links are not allowed.' }
            $total += $entry.Length
            if ($entry.Length -gt 64MB -or $total -gt 256MB) { throw 'Expanded release exceeds its size limit.' }
            $entries += [pscustomobject]@{Entry=$entry;Path=$path;Directory=$name.EndsWith('/')}
        }
        foreach ($item in $entries) {
            if ($item.Directory) { [IO.Directory]::CreateDirectory($item.Path) | Out-Null; continue }
            [IO.Directory]::CreateDirectory((Split-Path -Parent $item.Path)) | Out-Null
            $inputStream = $item.Entry.Open()
            $outputStream = [IO.File]::Open($item.Path,'CreateNew','Write','None')
            try {
                $buffer = New-Object byte[] 65536
                [long]$written = 0
                while (($count = $inputStream.Read($buffer,0,$buffer.Length)) -gt 0) {
                    $written += $count
                    if ($written -gt $item.Entry.Length) { throw 'Expanded entry size does not match the archive.' }
                    $outputStream.Write($buffer,0,$count)
                }
                if ($written -ne $item.Entry.Length) { throw 'Truncated release entry.' }
            } finally { $outputStream.Dispose(); $inputStream.Dispose() }
        }
        return Join-Path $Destination $root
    } finally { $zip.Dispose() }
}

function Assert-FiloReleaseContents {
    param([string]$Directory)
    $base = [IO.Path]::GetFullPath($Directory).TrimEnd('\') + '\'
    $manifestPath = Join-Path $base 'manifest.json'
    if ((Get-Item -LiteralPath $manifestPath).Length -gt 1MB) { throw 'Release manifest is too large.' }
    $manifest = Get-Content -Encoding UTF8 -LiteralPath $manifestPath -Raw | ConvertFrom-Json
    if ($manifest.revision -notmatch '^[0-9a-f]{40}$' -or
        (Split-Path -Leaf $Directory) -cne ('filo-windows-' + $manifest.revision)) { throw 'Release revision mismatch.' }
    $seen = @{}
    foreach ($file in $manifest.files) {
        if ($file.path -match '[:*?<>|"]' -or [IO.Path]::IsPathRooted($file.path) -or
            $file.sha256 -notmatch '^[a-fA-F0-9]{64}$') { throw 'Invalid release manifest entry.' }
        $path = [IO.Path]::GetFullPath((Join-Path $base $file.path))
        if (-not $path.StartsWith($base,[StringComparison]::OrdinalIgnoreCase) -or $seen.ContainsKey($path)) {
            throw 'Duplicate or escaping manifest path.'
        }
        $seen[$path] = $true
        if ((Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash -ine $file.sha256) {
            throw "Release file checksum mismatch: $($file.path)"
        }
    }
    $actual = @(Get-ChildItem -LiteralPath $base -Recurse -File)
    if ($actual.Count -ne $seen.Count + 1) { throw 'Release contains unverified files.' }
    foreach ($required in @('Install-Filo.ps1','scripts\install.ps1','tools\filo.exe','tools\nssm.exe','tools\FiloBackground.exe')) {
        if (-not $seen.ContainsKey((Join-Path $base $required))) { throw "Incomplete release: $required" }
    }
    return $manifest.revision
}

& {
    $ErrorActionPreference = 'Stop'
    Set-StrictMode -Version 2
    $architecture = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
    if ($env:OS -ne 'Windows_NT' -or $architecture -ne 'AMD64') {
        throw 'Filo currently supports Windows x64 with the original Codex Desktop.'
    }
    if ($ValidateOnly -and $Uninstall) { throw 'ValidateOnly cannot be combined with Uninstall.' }
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $admin = ([Security.Principal.WindowsPrincipal]::new($identity)).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
    if (-not $admin -and -not ($ValidateOnly -or $ListClients)) {
        throw 'Open PowerShell as administrator, then run this command again. No changes were made.'
    }
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
    $stage = Join-Path ([IO.Path]::GetTempPath()) ('Filo-Install-' + [guid]::NewGuid().ToString('N'))
    $stage = [IO.Path]::GetFullPath($stage)
    for ($part = Split-Path -Parent $stage; $part; $part = Split-Path -Parent $part) {
        if ((Get-Item -LiteralPath $part -Force).Attributes -band [IO.FileAttributes]::ReparsePoint) {
            throw 'The temporary installation path must not contain links.'
        }
    }
    New-Item -ItemType Directory -Path $stage | Out-Null
    try {
        $acl = [Security.AccessControl.DirectorySecurity]::new()
        $acl.SetAccessRuleProtection($true,$false)
        foreach ($sid in @('S-1-5-18','S-1-5-32-544',$identity.User.Value) | Select-Object -Unique) {
            $acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new(
                [Security.Principal.SecurityIdentifier]::new($sid),'FullControl','ContainerInherit,ObjectInherit','None','Allow'))
        }
        Set-Acl -LiteralPath $stage -AclObject $acl
        Write-Host '  Filo Installer' -ForegroundColor Cyan
        Write-Host '  Original Codex Desktop only; no Go, Node.js or separate CLI required.'
        Write-Host '  [1/3] Finding the Windows release...' -ForegroundColor Cyan
        $tag = $Version
        if ($tag -eq 'latest') {
            $metadata = Join-Path $stage 'release.json'
            Receive-FiloReleaseFile 'https://api.github.com/repos/newo-ether/filo/releases/latest' $metadata 1MB
            $tag = (Get-Content -Encoding UTF8 -LiteralPath $metadata -Raw | ConvertFrom-Json).tag_name
            if ($tag -notmatch '^v[0-9]+\.[0-9]+\.[0-9]+$') { throw 'The latest release has an invalid version.' }
        }
        $url = "https://github.com/newo-ether/filo/releases/download/$tag"
        $asset = 'filo-windows-amd64.zip'
        $checksums = Join-Path $stage 'checksums.txt'
        $archive = Join-Path $stage $asset
        Write-Host "  [2/3] Downloading and verifying $tag..." -ForegroundColor Cyan
        Receive-FiloReleaseFile "$url/checksums.txt" $checksums 64KB
        $expected = Get-FiloReleaseHash $checksums $asset
        Receive-FiloReleaseFile "$url/$asset" $archive 64MB
        if ((Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash -ine $expected) {
            throw 'Windows bundle SHA-256 mismatch. Installation has not started; run the installer again.'
        }
        $bundle = Expand-FiloRelease $archive (Join-Path $stage 'bundle')
        $revision = Assert-FiloReleaseContents $bundle
        Write-Host "  Verified source $revision" -ForegroundColor Green
        Write-Host '  [3/3] Discovering clients and starting the installer...' -ForegroundColor Cyan
        & (Join-Path $bundle 'Install-Filo.ps1') @installerOptions
    } finally {
        $temporaryRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd('\') + '\'
        if (-not $stage.StartsWith($temporaryRoot,[StringComparison]::OrdinalIgnoreCase) -or
            (Split-Path -Leaf $stage) -notmatch '^Filo-Install-[a-f0-9]{32}$') { throw 'Refusing cleanup outside the installer temporary directory.' }
        if (Test-Path -LiteralPath $stage) {
            $links = @(Get-ChildItem -LiteralPath $stage -Force -Recurse | Where-Object { $_.Attributes -band [IO.FileAttributes]::ReparsePoint })
            if ($links.Count -or ((Get-Item -LiteralPath $stage).Attributes -band [IO.FileAttributes]::ReparsePoint)) {
                Write-Warning 'Temporary directory contains links; automatic cleanup was skipped.'
            } else { Remove-Item -LiteralPath $stage -Recurse -Force }
        }
    }
}
