$ErrorActionPreference='Stop'
. (Join-Path $PSScriptRoot '..\windows\Desktop-Runtime.ps1')
function Assert($Value,$Message) { if (-not $Value) { throw $Message } }
$sid=[Security.Principal.WindowsIdentity]::GetCurrent().User.Value
$fixture=Join-Path ([IO.Path]::GetTempPath()) ('filo-runtime-'+[guid]::NewGuid().ToString('N'))
$source=Join-Path $fixture 'Desktop package with spaces'; $cache=Join-Path $fixture 'data\desktop-runtime'
function Get-FiloDesktopRuntimeSource { param($UserSid) Assert ($UserSid -eq $sid) 'Selected account is preserved'; [pscustomobject]@{Directory=$source;Application=(Join-Path $source 'ChatGPT.exe')} }
try {
    New-Item -ItemType Directory -Path $source -Force | Out-Null
    [IO.File]::WriteAllText((Join-Path $source 'codex.exe'),'native bytes')
    [IO.File]::WriteAllText((Join-Path $source 'companion.dll'),'native companion')
    $first=Resolve-FiloDesktopRuntime $sid $cache
    Assert ($first.Directory -eq $cache -and $first.Executable -eq (Join-Path $cache 'codex.exe')) 'Runtime uses one fixed directory'
    Assert ((Resolve-FiloDesktopRuntime $sid $cache).Executable -eq $first.Executable) 'Repeated discovery does not create another directory'
    [IO.File]::WriteAllText($first.Executable,'broken bytes')
    $repaired=Resolve-FiloDesktopRuntime $sid $cache
    Assert ($repaired.Executable -eq $first.Executable -and [IO.File]::ReadAllText($repaired.Executable) -eq 'native bytes') 'Corrupt copy is repaired in the fixed location'
    [IO.File]::WriteAllText((Join-Path $source 'codex.exe'),'new native version')
    $updated=Resolve-FiloDesktopRuntime $sid $cache
    Assert ($updated.Executable -eq $first.Executable -and [IO.File]::ReadAllText($updated.Executable) -eq 'new native version') 'Native update replaces the one copy'
    Assert (@(Get-ChildItem $cache -Directory).Count -eq 0) 'No hash, staging or fallback directories remain'
    $rejected=$false
    try { Resolve-FiloDesktopRuntime 'S-1-5-18' $cache | Out-Null } catch { $rejected=$true }
    Assert $rejected 'SYSTEM cannot run a selected-account native runtime'
    'PASS: fixed native runtime, unchanged components, repeat resolution, corruption repair, update and account boundary'
} finally { if (Test-Path $fixture) { Remove-Item $fixture -Recurse -Force } }
