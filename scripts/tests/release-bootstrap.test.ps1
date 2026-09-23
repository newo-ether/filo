$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 2
Add-Type -AssemblyName System.IO.Compression.FileSystem
$installer = Join-Path (Split-Path -Parent (Split-Path -Parent $PSScriptRoot)) 'install.ps1'
$tokens = $null; $parseErrors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile($installer,[ref]$tokens,[ref]$parseErrors)
if ($parseErrors.Count) { throw $parseErrors[0] }
foreach ($function in $ast.FindAll({param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst]},$false)) {
    . ([scriptblock]::Create($function.Extent.Text))
}
function Assert-Throws {
    param([scriptblock]$Action,[string]$Pattern)
    try { & $Action } catch {
        if ($_.Exception.Message -notmatch $Pattern) { throw }
        return
    }
    throw "Expected rejection: $Pattern"
}
$root = Join-Path $env:TEMP ('Filo-Bootstrap-Test-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $root | Out-Null
$revision = ('a' * 40) -join ''
$prefix = 'filo-windows-' + $revision
function New-TestArchive {
    param([string]$Name,[object[]]$Entries)
    $path = Join-Path $root ($Name + '.zip')
    $zip = [IO.Compression.ZipFile]::Open($path,'Create')
    try {
        foreach ($item in $Entries) {
            $entry = $zip.CreateEntry($item.Name)
            if ($item.PSObject.Properties['Attributes']) { $entry.ExternalAttributes = $item.Attributes }
            $writer = [IO.StreamWriter]::new($entry.Open())
            try { $writer.Write($item.Text) } finally { $writer.Dispose() }
        }
    } finally { $zip.Dispose() }
    return $path
}
try {
    foreach ($url in @('http://github.com/a','https://example.com/a','https://github.com:444/a',
        'https://user@github.com/a','https://github.com.evil.invalid/a')) {
        Assert-Throws { Assert-FiloDownloadUrl ([uri]$url) } 'official HTTPS'
    }
    foreach ($url in @('https://github.com/newo-ether/filo/releases/download/v0.1.0/a.zip',
        'https://release-assets.githubusercontent.com/a?signature=public-fixture')) { Assert-FiloDownloadUrl ([uri]$url) }
    $checksums = Join-Path $root 'checksums.txt'
    $hash = 'B' * 64
    Set-Content -LiteralPath $checksums -Value "$hash  filo-windows-amd64.zip"
    if ((Get-FiloReleaseHash $checksums 'filo-windows-amd64.zip') -ne $hash) { throw 'Hash parse failed' }
    Add-Content -LiteralPath $checksums -Value "$hash  filo-windows-amd64.zip"
    Assert-Throws { Get-FiloReleaseHash $checksums 'filo-windows-amd64.zip' } 'exactly one'
    Assert-Throws { Get-FiloReleaseHash $checksums 'missing.zip' } 'exactly one'

    $badNames = @("$prefix/../outside.ps1","$prefix/C:/outside.ps1","/$prefix/a",
        "$prefix/CON.txt","$prefix/trailing./a","$prefix/x/../../escape")
    $index = 0
    foreach ($name in $badNames) {
        $archive = New-TestArchive ('bad' + $index) @([pscustomobject]@{Name=$name;Text='untrusted'})
        $destination = Join-Path $root ('bad-output' + $index)
        Assert-Throws { Expand-FiloRelease $archive $destination } 'Unsafe'
        if (Test-Path -LiteralPath $destination) { throw 'Unsafe paths wrote files before rejection' }
        $index++
    }
    $duplicate = New-TestArchive 'duplicate' @(
        [pscustomobject]@{Name="$prefix/a.txt";Text='one'},
        [pscustomobject]@{Name="$prefix/A.txt";Text='two'})
    Assert-Throws { Expand-FiloRelease $duplicate (Join-Path $root 'duplicate-output') } 'Duplicate'
    $symlink = New-TestArchive 'symlink' @(
        [pscustomobject]@{Name="$prefix/link";Text='target';Attributes=([int]0xA000 -shl 16)})
    Assert-Throws { Expand-FiloRelease $symlink (Join-Path $root 'link-output') } 'links'
    $many = @(1..513 | ForEach-Object { [pscustomobject]@{Name="$prefix/$_.txt";Text='x'} })
    $overflow = New-TestArchive 'many' $many
    Assert-Throws { Expand-FiloRelease $overflow (Join-Path $root 'many-output') } 'entry count'

    # The actual outer script is executed with only the network function replaced.
    # The fixture bundle's installer records arguments; it cannot touch services.
    $payload = Join-Path $root $prefix
    New-Item -ItemType Directory -Path (Join-Path $payload 'scripts'),(Join-Path $payload 'tools') | Out-Null
    $capture = Join-Path $root 'forwarded.json'
    $entryText = 'param([string]$BundleDirectory,[string[]]$Clients,[switch]$ListClients,[switch]$ValidateOnly,[switch]$Uninstall,[int]$Port)' +
        'if($BundleDirectory){throw ''Switch was converted to a positional directory''};' +
        '[pscustomobject]@{Clients=$Clients;ValidateOnly=[bool]$ValidateOnly;Port=$Port}|ConvertTo-Json|Set-Content -LiteralPath ''' + $capture + ''''
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot '..\install-entry.ps1') -Destination (Join-Path $payload 'Install-Filo.ps1')
    [IO.File]::WriteAllText((Join-Path $payload 'scripts/install.ps1'),$entryText)
    foreach ($relative in @('tools/filo.exe','tools/nssm.exe','tools/FiloBackground.exe')) {
        [IO.File]::WriteAllText((Join-Path $payload $relative),'not executable')
    }
    $files = @(Get-ChildItem -LiteralPath $payload -Recurse -File | ForEach-Object {
        $marker = '\' + [string]$prefix + '\'
        $offset = $_.FullName.IndexOf($marker,[StringComparison]::OrdinalIgnoreCase)
        if ($offset -lt 0) { throw 'Fixture file escaped the release directory' }
        $relative = $_.FullName.Substring($offset + $marker.Length)
        @{path=$relative;sha256=[string]((Get-FileHash -LiteralPath $_.FullName).Hash)}
    })
    $manifest = Join-Path $payload 'manifest.json'
    @{revision=$revision;files=$files} | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $manifest
    if ((Assert-FiloReleaseContents $payload) -ne $revision) { throw 'Valid manifest rejected' }
    [IO.File]::WriteAllText((Join-Path $payload 'extra.ps1'),'unverified')
    Assert-Throws { Assert-FiloReleaseContents $payload } 'unverified'
    Remove-Item -LiteralPath (Join-Path $payload 'extra.ps1')
    [IO.File]::WriteAllText((Join-Path $payload 'tools/filo.exe'),'corrupt')
    Assert-Throws { Assert-FiloReleaseContents $payload } 'checksum mismatch'
    [IO.File]::WriteAllText((Join-Path $payload 'tools/filo.exe'),'not executable')
    $goodZip = Join-Path $root 'good.zip'
    Compress-Archive -LiteralPath $payload -DestinationPath $goodZip
    $expanded = Expand-FiloRelease $goodZip (Join-Path $root 'expanded')
    Assert-FiloReleaseContents $expanded | Out-Null
    $goodHash = (Get-FileHash -LiteralPath $goodZip).Hash
    $mock = @'
function Receive-FiloReleaseFile {
    param([uri]$Url,[string]$Destination,[long]$MaximumBytes)
    if ($Url.AbsolutePath.EndsWith('/latest')) {
        '{"tag_name":"v0.1.0"}' | Set-Content -LiteralPath $Destination
    } elseif ($Url.AbsolutePath.EndsWith('/checksums.txt')) {
        '__HASH__  filo-windows-amd64.zip' | Set-Content -LiteralPath $Destination
    } else { Copy-Item -LiteralPath '__ZIP__' -Destination $Destination }
}
'@
    $mock = $mock.Replace('__HASH__',$goodHash).Replace('__ZIP__',$goodZip)
    $source = [IO.File]::ReadAllText($installer)
    $main = $source.LastIndexOf('& {')
    $testScript = $source.Insert($main,$mock + [Environment]::NewLine)
    & ([scriptblock]::Create($testScript)) -ValidateOnly -Clients 'codex:fixture' -Port 8888
    $received = Get-Content -LiteralPath $capture -Raw | ConvertFrom-Json
    if (-not $received.ValidateOnly -or $received.Clients[0] -ne 'codex:fixture' -or $received.Port -ne 8888) {
        throw 'Bootstrap did not forward explicit installer options'
    }
    Remove-Item -LiteralPath $capture
    $badHashScript = $testScript.Replace($goodHash,('0' * 64))
    Assert-Throws { & ([scriptblock]::Create($badHashScript)) -ValidateOnly -Clients 'codex:fixture' } 'SHA-256 mismatch'
    if (Test-Path -LiteralPath $capture) { throw 'Rejected download executed its installer' }
    Write-Host 'Release bootstrap verification passed: URLs, checksums, traversal, duplicates, links, manifest, forwarding and fail-before-execute.'
} finally {
    $resolved = [IO.Path]::GetFullPath($root)
    $temp = [IO.Path]::GetFullPath($env:TEMP).TrimEnd('\') + '\'
    if (-not $resolved.StartsWith($temp,[StringComparison]::OrdinalIgnoreCase) -or
        (Split-Path -Leaf $resolved) -notmatch '^Filo-Bootstrap-Test-[a-f0-9]{32}$') { throw 'Unsafe test cleanup' }
    Remove-Item -LiteralPath $resolved -Recurse -Force
}
