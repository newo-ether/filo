# Official skills-directory plugins load for this user in both terminal and Desktop Code.
# Only the exact Filo payload and private ownership receipt are managed here.
Set-StrictMode -Version 2

function Assert-FiloClaudeComponentPath {
    param([string]$Path)
    if (-not $Path -or $Path -notmatch '^[A-Za-z]:[\\/]' -or $Path.IndexOf([char]0) -ge 0 -or
        $Path.Substring(2).Contains(':')) { throw 'Claude component path must be an absolute local path' }
    $full = [IO.Path]::GetFullPath($Path).TrimEnd('\')
    for ($part = $full; $part; $part = Split-Path -Parent $part) {
        if (Test-Path -LiteralPath $part) {
            $item = Get-Item -LiteralPath $part -Force
            if ($item.Attributes -band [IO.FileAttributes]::ReparsePoint) { throw 'Claude component paths must not contain links' }
        }
    }
    return $full
}
function Read-FiloClaudeComponentBytes {
    param([string]$Path, [int]$Limit = 524288)
    $null = Assert-FiloClaudeComponentPath $Path
    if (-not (Test-Path -LiteralPath $Path)) { return $null }
    $item = Get-Item -LiteralPath $Path -Force
    if ($item.PSIsContainer -or $item.Length -gt $Limit) { throw 'Invalid Claude component file' }
    return ,([IO.File]::ReadAllBytes($Path))
}
function Get-FiloClaudeComponentHash {
    param([byte[]]$Bytes)
    $hash = [Security.Cryptography.SHA256]::Create()
    try { return ([BitConverter]::ToString($hash.ComputeHash($Bytes))).Replace('-', '').ToLowerInvariant() }
    finally { $hash.Dispose() }
}
function Test-FiloClaudeComponentBytes {
    param($Left, $Right)
    if ($null -eq $Left -or $null -eq $Right) { return $null -eq $Left -and $null -eq $Right }
    return [Convert]::ToBase64String($Left) -ceq [Convert]::ToBase64String($Right)
}
function ConvertTo-FiloClaudeComponentBytes {
    param($Value)
    return ,([Text.UTF8Encoding]::new($false).GetBytes(($Value | ConvertTo-Json -Depth 6)))
}
function New-FiloClaudeComponentPlan {
    param([string]$Bundle, [string]$Worker, [string]$Profile, [string]$Sid, [string[]]$Sources)
    if ($Sid -notmatch '^S-1-(5-21|12-1)-[0-9-]+$') { throw 'Invalid Claude component account SID' }
    $workerPath = Assert-FiloClaudeComponentPath $Worker
    $profilePath = Assert-FiloClaudeComponentPath $Profile
    if (-not (Test-Path -LiteralPath $profilePath -PathType Container)) { throw 'Claude component user profile is unavailable' }
    $selected = @($Sources | Where-Object { $_ -cin @('claude-code', 'claude-desktop') } | Sort-Object -Unique)
    $root = Assert-FiloClaudeComponentPath (Join-Path $profilePath '.claude\skills\filo-observer')
    $receipt = Join-Path $workerPath 'claude-components.json'
    $marker = Join-Path $root '.filo-owner.json'
    $relativeFiles = @('.claude-plugin\plugin.json', 'hooks\hooks.json', 'hooks\register.js', 'filo-config.json')
    $snapshots = @{}
    $snapshots[$receipt] = Read-FiloClaudeComponentBytes $receipt 8192
    $markerBytes = Read-FiloClaudeComponentBytes $marker 8192
    $owned = $null -ne $snapshots[$receipt] -or $null -ne $markerBytes
    $plan = [pscustomobject]@{ Worker=$workerPath; Profile=$profilePath; Sid=$Sid; Sources=$selected;
        Root=$root; Receipt=$receipt; Marker=$marker; Files=$relativeFiles; Owned=$owned;
        Snapshots=$snapshots; Payload=@{}; Written=@{}; CreatedDirectories=[Collections.Generic.List[string]]::new();
        RemovedDirectories=[Collections.Generic.List[object]]::new() }
    if (-not $selected.Count -and -not $owned) { return $plan }
    foreach ($relative in $relativeFiles) {
        $path = Join-Path $root $relative
        $snapshots[$path] = Read-FiloClaudeComponentBytes $path
    }
    $snapshots[$marker] = $markerBytes
    if ($owned) {
        $recordBytes=if ($null -ne $snapshots[$receipt]) { $snapshots[$receipt] } else { $markerBytes }
        $record = [Text.Encoding]::UTF8.GetString($recordBytes) | ConvertFrom-Json
        $workerMatches=$record.worker -ieq $workerPath
        if ($null -eq $snapshots[$receipt]) {
            # A prior uninstall may remove private state before its native helper.
            # Accept only a self-consistent marker for this account and a known
            # Filo worker path; every managed byte is still verified below.
            $workerMatches=$workerMatches -or $record.worker -ieq (Join-Path $profilePath 'AppData\Local\FiloStandalone') -or
                $record.worker -ieq (Join-Path $profilePath 'AppData\Local\Filo')
        }
        if ($record.schema -ne 1 -or $record.owner -cne 'filo-claude-observer' -or $record.sid -cne $Sid -or
            $record.profile -ine $profilePath -or -not $workerMatches -or $record.pluginRoot -ine $root -or
            -not (Test-FiloClaudeComponentBytes $recordBytes $markerBytes) -or
            @($record.files).Count -ne $relativeFiles.Count) { throw 'Claude component ownership receipt does not match; existing files were preserved' }
        for ($i = 0; $i -lt $relativeFiles.Count; $i++) {
            $bytes = $snapshots[(Join-Path $root $relativeFiles[$i])]
            if ($record.files[$i].path -cne $relativeFiles[$i] -or $null -eq $bytes -or
                $record.files[$i].sha256 -cne (Get-FiloClaudeComponentHash $bytes)) {
                throw 'A managed Claude component was changed; existing files were preserved'
            }
        }
    } elseif (Test-Path -LiteralPath $root) {
        throw 'The Claude observer directory is not owned by Filo; existing files were preserved'
    }
    if ($selected.Count) {
        $source = Join-Path (Assert-FiloClaudeComponentPath $Bundle) 'internal\claude\mod'
        foreach ($relative in $relativeFiles[0..2]) {
            $bytes = Read-FiloClaudeComponentBytes (Join-Path $source $relative)
            if ($null -eq $bytes) { throw 'Bundle is missing the Claude Mod payload' }
            $plan.Payload[$relative] = $bytes
        }
        $manifest = [Text.Encoding]::UTF8.GetString($plan.Payload[$relativeFiles[0]]) | ConvertFrom-Json
        if ($manifest.name -cne 'filo-observer' -or ($manifest.PSObject.Properties['defaultEnabled'] -and
            $manifest.defaultEnabled -eq $false)) { throw 'Invalid Filo Claude plugin manifest' }
    }
    return $plan
}
function Set-FiloClaudeComponentBytes {
    param([string]$Path, $Bytes)
    $null = Assert-FiloClaudeComponentPath $Path
    if ($null -eq $Bytes) {
        if (Test-Path -LiteralPath $Path) { Remove-Item -LiteralPath $Path -Force }
        return
    }
    $temporary = Join-Path (Split-Path -Parent $Path) ('.filo-write-' + [guid]::NewGuid().ToString('N'))
    try {
        [IO.File]::WriteAllBytes($temporary, $Bytes)
        if (Test-Path -LiteralPath $Path) { [IO.File]::Replace($temporary, $Path, [NullString]::Value) }
        else { [IO.File]::Move($temporary, $Path) }
    } finally { if (Test-Path -LiteralPath $temporary) { Remove-Item -LiteralPath $temporary -Force } }
}
function Assert-FiloClaudeComponentSnapshot {
    param($Plan)
    foreach ($path in $Plan.Snapshots.Keys) {
        if (-not (Test-FiloClaudeComponentBytes (Read-FiloClaudeComponentBytes $path) $Plan.Snapshots[$path])) {
            throw 'Claude component changed after preflight; existing files were preserved'
        }
    }
    if (-not $Plan.Owned -and $Plan.Sources.Count -and (Test-Path -LiteralPath $Plan.Root)) {
        throw 'Claude observer directory appeared after preflight; existing files were preserved'
    }
}
function New-FiloClaudeComponentDirectory {
    param($Plan, [string]$Path)
    $null = Assert-FiloClaudeComponentPath $Path
    if (Test-Path -LiteralPath $Path -PathType Container) { return }
    $parent = Split-Path -Parent $Path
    if (-not $parent) { throw 'Invalid Claude component directory' }
    New-FiloClaudeComponentDirectory $Plan $parent
    New-Item -ItemType Directory -Path $Path -ErrorAction Stop | Out-Null
    $Plan.CreatedDirectories.Add($Path)
}
function Write-FiloClaudeComponentBytes {
    param($Plan, [string]$Path, $Bytes)
    $expected = $Plan.Snapshots[$Path]
    if ($Plan.Written.ContainsKey($Path)) { $expected = $Plan.Written[$Path] }
    if (-not (Test-FiloClaudeComponentBytes (Read-FiloClaudeComponentBytes $Path) $expected)) {
        throw 'Claude component changed before write; current files were preserved'
    }
    Set-FiloClaudeComponentBytes $Path $Bytes
    $Plan.Written[$Path] = $Bytes
}
function Remove-FiloClaudeComponentDirectories {
    param($Plan)
    foreach ($path in @((Join-Path $Plan.Root '.claude-plugin'), (Join-Path $Plan.Root 'hooks'), $Plan.Root)) {
        $null = Assert-FiloClaudeComponentPath $path
        if ((Test-Path -LiteralPath $path -PathType Container) -and
            -not @(Get-ChildItem -LiteralPath $path -Force).Count) {
            $acl = Get-Acl -LiteralPath $path
            Remove-Item -LiteralPath $path -Force
            $Plan.RemovedDirectories.Add([pscustomobject]@{Path=$path; Acl=$acl})
        }
    }
}
function Install-FiloClaudeComponents {
    param($Plan)
    if (-not $Plan.Sources.Count -and -not $Plan.Owned) { return }
    Assert-FiloClaudeComponentSnapshot $Plan
    if ($Plan.Sources.Count) {
        $configBytes = Read-FiloClaudeComponentBytes (Join-Path $Plan.Worker 'claude-mod.json') 8192
        if ($null -eq $configBytes) { throw 'Claude bridge configuration must be prepared before component installation' }
        $config = [Text.Encoding]::UTF8.GetString($configBytes) | ConvertFrom-Json
        if ($config.url -cnotmatch '^http://127\.0\.0\.1:([0-9]+)/claude-mod/v1/$' -or
            [int]$Matches[1] -lt 1024 -or [int]$Matches[1] -gt 65535 -or
            $config.tokenFile -ine (Join-Path $Plan.Worker 'claude-mod-token')) { throw 'Invalid private Claude bridge configuration' }
        # Copy only the token path. The secret remains under the selected user's worker ACL.
        $Plan.Payload['filo-config.json'] = ConvertTo-FiloClaudeComponentBytes ([ordered]@{
            url=$config.url; tokenFile=$config.tokenFile; sources=@($Plan.Sources)
        })
        New-FiloClaudeComponentDirectory $Plan $Plan.Root
        if (-not $Plan.Owned) { Protect-FiloDirectory $Plan.Root $Plan.Sid }
        foreach ($relative in $Plan.Files) {
            New-FiloClaudeComponentDirectory $Plan (Split-Path -Parent (Join-Path $Plan.Root $relative))
        }
        $fileRecords = @($Plan.Files | ForEach-Object {
            [ordered]@{path=$_; sha256=(Get-FiloClaudeComponentHash $Plan.Payload[$_])}
        })
        $recordBytes = ConvertTo-FiloClaudeComponentBytes ([ordered]@{
            schema=1; owner='filo-claude-observer'; sid=$Plan.Sid; profile=$Plan.Profile;
            worker=$Plan.Worker; pluginRoot=$Plan.Root; files=$fileRecords
        })
        # Publish the manifest last so a fresh session never discovers an incomplete new plugin.
        foreach ($relative in @('filo-config.json', 'hooks\hooks.json', 'hooks\register.js', '.claude-plugin\plugin.json')) {
            $path = Join-Path $Plan.Root $relative
            Write-FiloClaudeComponentBytes $Plan $path $Plan.Payload[$relative]
        }
        foreach ($path in @($Plan.Marker, $Plan.Receipt)) {
            Write-FiloClaudeComponentBytes $Plan $path $recordBytes
        }
    } else {
        # Disable future discovery first. Unknown files and unrelated native configuration stay intact.
        foreach ($path in @(@($Plan.Files | ForEach-Object { Join-Path $Plan.Root $_ }) + @($Plan.Marker, $Plan.Receipt))) {
            Write-FiloClaudeComponentBytes $Plan $path $null
        }
        Remove-FiloClaudeComponentDirectories $Plan
    }
}
function Restore-FiloClaudeComponents {
    param($Plan)
    # Reject outside edits before restoring any bytes, including an interrupted installation.
    foreach ($path in $Plan.Written.Keys) {
        if (-not (Test-FiloClaudeComponentBytes (Read-FiloClaudeComponentBytes $path) $Plan.Written[$path])) {
            throw 'Claude component changed during rollback; current files were preserved'
        }
    }
    for ($i = $Plan.RemovedDirectories.Count - 1; $i -ge 0; $i--) {
        $entry = $Plan.RemovedDirectories[$i]
        $null = Assert-FiloClaudeComponentPath $entry.Path
        if (Test-Path -LiteralPath $entry.Path) { throw 'Claude component directory appeared during rollback; current files were preserved' }
        New-Item -ItemType Directory -Path $entry.Path -ErrorAction Stop | Out-Null
        Set-Acl -LiteralPath $entry.Path -AclObject $entry.Acl
        $Plan.RemovedDirectories.RemoveAt($i)
    }
    $restorePaths = @($Plan.Written.Keys | Sort-Object @{Expression={ $_ -ieq (Join-Path $Plan.Root '.claude-plugin\plugin.json') }}, @{Expression={$_}})
    foreach ($path in $restorePaths) {
        Set-FiloClaudeComponentBytes $path $Plan.Snapshots[$path]
        $Plan.Written.Remove($path)
    }
    for ($i = $Plan.CreatedDirectories.Count - 1; $i -ge 0; $i--) {
        $path = $Plan.CreatedDirectories[$i]
        $null = Assert-FiloClaudeComponentPath $path
        if ((Test-Path -LiteralPath $path -PathType Container) -and
            -not @(Get-ChildItem -LiteralPath $path -Force).Count) { Remove-Item -LiteralPath $path -Force }
    }
    $Plan.CreatedDirectories.Clear()
}
function Remove-FiloClaudeComponents {
    param([string]$Worker, [string]$Sid)
    $receipt = Join-Path (Assert-FiloClaudeComponentPath $Worker) 'claude-components.json'
    $bytes = Read-FiloClaudeComponentBytes $receipt 8192
    if ($null -eq $bytes) { return }
    $record = [Text.Encoding]::UTF8.GetString($bytes) | ConvertFrom-Json
    $plan = New-FiloClaudeComponentPlan '' $Worker ([string]$record.profile) $Sid @()
    Install-FiloClaudeComponents $plan
}
