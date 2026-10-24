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
        [string]$CacheRoot = (Join-Path $env:ProgramData ('Filo\agents\' + $UserSid + '\desktop-runtime')))
    if ($UserSid -notmatch '^S-1-(5-21|12-1)-[0-9-]+$' -or
        [Security.Principal.WindowsIdentity]::GetCurrent().User.Value -ne $UserSid) {
        throw 'The desktop runtime must run as its selected Windows user, never as SYSTEM'
    }
    $source=Get-FiloDesktopRuntimeSource $UserSid
    $files=@(Get-FiloRuntimeFiles $source.Directory)
    $target=[IO.Path]::GetFullPath($CacheRoot).TrimEnd('\')
    for ($part=$target; $part; $part=Split-Path -Parent $part) {
        $item=Get-Item -LiteralPath $part -Force -ErrorAction SilentlyContinue
        if ($item -and ($item.Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw 'Runtime directory must not contain links' }
    }
    # WindowsApps native executables may refuse external launch in place. Keep
    # unchanged native components in one private ProgramData directory, not a
    # user AppData cache or an ever-growing set of hash directories.
    $ready=$false
    if (Test-Path -LiteralPath $target) {
        try { Assert-FiloRuntimeCopy $target $files; $ready=$true } catch { $ready=$false }
    }
    if (-not $ready) {
        if (Test-Path -LiteralPath $target) { Remove-Item -LiteralPath $target -Recurse -Force }
        New-Item -ItemType Directory -Path $target -Force | Out-Null
        foreach ($file in $files) {
            Copy-Item -LiteralPath (Join-Path $source.Directory $file.Name) -Destination (Join-Path $target $file.Name)
        }
        Assert-FiloRuntimeCopy $target $files
        $after=@(Get-FiloRuntimeFiles $source.Directory) | ConvertTo-Json -Compress
        if ($after -cne ($files | ConvertTo-Json -Compress)) { throw 'Codex Desktop updated while preparing its runtime' }
    }
    [pscustomobject]@{Executable=(Join-Path $target 'codex.exe');Directory=$target;Application=$source.Application}
}
