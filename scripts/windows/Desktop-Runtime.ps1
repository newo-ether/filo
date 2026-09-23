# Resolve the selected registered Desktop on every launch. Never use PATH or saved native paths.
. (Join-Path $PSScriptRoot 'Desktop-Identity.ps1')

function Get-FiloDesktopRuntimeSource {
    param([Parameter(Mandatory=$true)][string]$UserSid)
    foreach ($application in @(Get-FiloDesktopApplications -UserSid $UserSid)) {
        foreach ($root in @($application.RuntimeRoots)) {
            $exe = Join-Path $root 'codex.exe'
            if (Test-Path -LiteralPath $exe -PathType Leaf) {
                return [pscustomobject]@{Application=$application.Executable;Directory=$root;Executable=$exe}
            }
        }
    }
    throw 'Codex Desktop runtime is unavailable for the selected account. Open or repair Codex Desktop, then retry.'
}

function Get-FiloRuntimeFiles {
    param([string]$Directory)
    $files = @(Get-ChildItem -LiteralPath $Directory -File | Where-Object { $_.Extension -in '.exe','.dll' } | Sort-Object Name)
    if (-not @($files | Where-Object Name -eq 'codex.exe').Count) { throw 'Desktop runtime has no codex.exe' }
    foreach ($file in $files) {
        if (($file.Attributes -band [IO.FileAttributes]::ReparsePoint) -or $file.Length -lt 2) {
            throw 'Desktop runtime contains an invalid native component'
        }
        [pscustomobject]@{Name=$file.Name;Length=$file.Length;Stamp=$file.LastWriteTimeUtc.Ticks;
            Hash=(Get-FileHash -LiteralPath $file.FullName -Algorithm SHA256 -ErrorAction Stop).Hash}
    }
}

function Assert-FiloRuntimeCopy {
    param([string]$Directory,[object[]]$Files)
    $actual=@(Get-FiloRuntimeFiles $Directory)
    if ($actual.Count -ne $Files.Count) { throw 'Runtime component set changed' }
    for ($index=0; $index -lt $Files.Count; $index++) {
        if ($actual[$index].Name -cne $Files[$index].Name -or
            $actual[$index].Length -ne $Files[$index].Length -or
            $actual[$index].Hash -ne $Files[$index].Hash) {
            throw 'Runtime copy differs from the currently installed source'
        }
    }
}

function Resolve-FiloDesktopRuntime {
    param([Parameter(Mandatory=$true)][string]$UserSid,
        [string]$CacheRoot = (Join-Path $env:LOCALAPPDATA 'Filo\runtimes'))
    if ($UserSid -notmatch '^S-1-(5-21|12-1)-[0-9-]+$' -or
        [Security.Principal.WindowsIdentity]::GetCurrent().User.Value -ne $UserSid) {
        throw 'The desktop runtime must run as its selected Windows user, never as SYSTEM'
    }
    $source = Get-FiloDesktopRuntimeSource $UserSid
    $files = @(Get-FiloRuntimeFiles $source.Directory)
    # This key invalidates Filo's private copy after any installed-package change.
    # It is computed from the current package; there is no native version/hash allowlist.
    $identity = [ordered]@{Source=$source.Directory;Files=$files} | ConvertTo-Json -Depth 5 -Compress
    $hash = [Security.Cryptography.SHA256]::Create()
    try { $key = ([BitConverter]::ToString($hash.ComputeHash([Text.Encoding]::UTF8.GetBytes($identity)))).Replace('-','').ToLowerInvariant() }
    finally { $hash.Dispose() }
    $cache = [IO.Path]::GetFullPath($CacheRoot).TrimEnd('\')
    for ($part=$cache; $part; $part=Split-Path -Parent $part) {
        $item=Get-Item -LiteralPath $part -Force -ErrorAction SilentlyContinue
        if ($item -and ($item.Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw 'Runtime cache must not contain links' }
    }
    New-Item -ItemType Directory -Path $cache -Force | Out-Null
    $target = Join-Path $cache $key
    $ready = $false
    $candidates = @(Get-ChildItem -LiteralPath $cache -Directory | Where-Object {
        $_.Name -eq $key -or $_.Name.StartsWith($key + '-') } | Sort-Object LastWriteTimeUtc -Descending)
    foreach ($candidate in $candidates) {
        try {
            $target=$candidate.FullName
            if ((Get-Item -LiteralPath $target).Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Linked runtime' }
            $receipt=Get-Content -Raw -LiteralPath (Join-Path $target 'ready.json') | ConvertFrom-Json
            if ($receipt.Identity -ne $identity) { throw 'Runtime source changed' }
            Assert-FiloRuntimeCopy -Directory $target -Files $files
            $ready=$true
            break
        } catch { $ready=$false }
    }
    if (-not $ready) {
        $target=Join-Path $cache $key
        # WindowsApps executables may not be externally executable. Copy the unchanged
        # native component set into Filo's own directory; never write Desktop's cache.
        $stage=Join-Path $cache ('staging-' + [guid]::NewGuid().ToString('N'))
        New-Item -ItemType Directory -Path $stage | Out-Null
        try {
            foreach ($file in $files) {
                Copy-Item -LiteralPath (Join-Path $source.Directory $file.Name) -Destination (Join-Path $stage $file.Name)
            }
            Assert-FiloRuntimeCopy -Directory $stage -Files $files
            $after=@(Get-FiloRuntimeFiles $source.Directory) | ConvertTo-Json -Compress
            if ($after -ne ($files | ConvertTo-Json -Compress)) { throw 'Codex Desktop updated while preparing its runtime; retry' }
            [IO.File]::WriteAllText((Join-Path $stage 'ready.json'),(@{Identity=$identity} | ConvertTo-Json),[Text.UTF8Encoding]::new($false))
            if (Test-Path -LiteralPath $target) { $target += '-' + [guid]::NewGuid().ToString('N') }
            Move-Item -LiteralPath $stage -Destination $target
        } finally {
            $safe=[IO.Path]::GetFullPath($stage)
            if (-not $safe.StartsWith($cache + '\',[StringComparison]::OrdinalIgnoreCase)) { throw 'Unsafe runtime staging path' }
            if (Test-Path -LiteralPath $safe) { Remove-Item -LiteralPath $safe -Recurse -Force }
        }
    }
    [pscustomobject]@{Executable=(Join-Path $target 'codex.exe');Directory=$target;Application=$source.Application}
}
