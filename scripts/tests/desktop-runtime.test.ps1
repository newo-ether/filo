$ErrorActionPreference='Stop'
. (Join-Path $PSScriptRoot '..\windows\Desktop-Runtime.ps1')
function Assert($Value,$Message) { if (-not $Value) { throw $Message } }
$sid=[Security.Principal.WindowsIdentity]::GetCurrent().User.Value
if ($sid -notmatch '^S-1-(5-21|12-1)-') { throw 'Run runtime qualification as an ordinary desktop user' }
$fixture=Join-Path ([IO.Path]::GetTempPath()) ('filo-runtime-' + [guid]::NewGuid().ToString('N'))
$source=Join-Path $fixture 'Desktop package with spaces'
$cache=Join-Path $fixture 'Filo cache'
$originalPath=$env:PATH
try {
    New-Item -ItemType Directory -Path $source -Force | Out-Null
    [IO.File]::WriteAllText((Join-Path $source 'codex.exe'),'unchanged native fixture')
    [IO.File]::WriteAllText((Join-Path $source 'codex-code-mode-host.exe'),'companion fixture')
    function Get-FiloDesktopRuntimeSource {
        param([string]$UserSid)
        Assert ($UserSid -eq $sid) 'Selected account is preserved'
        [pscustomobject]@{Directory=$source;Application=(Join-Path $source 'ChatGPT.exe')}
    }
    $env:PATH=$env:SystemRoot + '\System32'
    $first=Resolve-FiloDesktopRuntime $sid $cache
    Assert ($first.Executable.StartsWith($cache)) 'Runtime is prepared only inside Filo'
    Assert ([IO.File]::ReadAllText($first.Executable) -eq 'unchanged native fixture') 'Native bytes unchanged'
    Assert (Test-Path (Join-Path $first.Directory 'codex-code-mode-host.exe')) 'Companions are prepared together'
    Assert ((Resolve-FiloDesktopRuntime $sid $cache).Executable -eq $first.Executable) 'Prepared runtime is reusable'
    [IO.File]::WriteAllText($first.Executable,'broken')
    $repaired=Resolve-FiloDesktopRuntime $sid $cache
    Assert ($repaired.Executable -ne $first.Executable) 'Corrupt private copy is repaired without replacing a running executable'
    Assert ((Resolve-FiloDesktopRuntime $sid $cache).Executable -eq $repaired.Executable) 'Repair is reusable, not an endless cache leak'
    $sameLengthBytes=[IO.File]::ReadAllBytes($repaired.Executable)
    $sameLengthBytes[0]=$sameLengthBytes[0] -bxor 1
    [IO.File]::WriteAllBytes($repaired.Executable,$sameLengthBytes)
    $sameLengthRepaired=Resolve-FiloDesktopRuntime $sid $cache
    Assert ($sameLengthRepaired.Executable -ne $repaired.Executable) 'Equal-length native corruption must not remain selected'
    Assert ([IO.File]::ReadAllText($sameLengthRepaired.Executable) -eq 'unchanged native fixture') 'Equal-length repair restores original bytes'
    $repaired=$sameLengthRepaired
    $companion=Join-Path $repaired.Directory 'codex-code-mode-host.exe'
    $companionBytes=[IO.File]::ReadAllBytes($companion)
    $companionBytes[0]=$companionBytes[0] -bxor 1
    [IO.File]::WriteAllBytes($companion,$companionBytes)
    $companionRepaired=Resolve-FiloDesktopRuntime $sid $cache
    Assert ($companionRepaired.Executable -ne $repaired.Executable) 'Equal-length companion corruption is repaired too'
    $repaired=$companionRepaired
    [IO.File]::WriteAllText((Join-Path $repaired.Directory 'unexpected.dll'),'extra component')
    $componentSetRepaired=Resolve-FiloDesktopRuntime $sid $cache
    Assert ($componentSetRepaired.Executable -ne $repaired.Executable) 'Unexpected native components cannot alter the prepared runtime'
    $repaired=$componentSetRepaired
    $sourceExe=Join-Path $source 'codex.exe'
    $sourceStamp=[IO.File]::GetLastWriteTimeUtc($sourceExe)
    $sourceBytes=[IO.File]::ReadAllBytes($sourceExe)
    $sourceBytes[0]=$sourceBytes[0] -bxor 1
    [IO.File]::WriteAllBytes($sourceExe,$sourceBytes)
    [IO.File]::SetLastWriteTimeUtc($sourceExe,$sourceStamp)
    $sourceUpdated=Resolve-FiloDesktopRuntime $sid $cache
    Assert ($sourceUpdated.Executable -ne $repaired.Executable) 'Source changes with preserved size and timestamp are discovered'
    Assert ((Get-FileHash -LiteralPath $sourceUpdated.Executable).Hash -eq (Get-FileHash -LiteralPath $sourceExe).Hash) 'No native hash allowlist rejects an updated source'
    [IO.File]::WriteAllText((Join-Path $source 'codex.exe'),'updated desktop native fixture')
    $updated=Resolve-FiloDesktopRuntime $sid $cache
    Assert ($updated.Executable -ne $repaired.Executable) 'Desktop update invalidates old runtime selection'
    Assert ([IO.File]::ReadAllText($repaired.Executable) -eq 'unchanged native fixture') 'Existing auxiliaries keep immutable runtime'
    $rejected=$false
    try { Resolve-FiloDesktopRuntime 'S-1-5-18' $cache | Out-Null } catch { $rejected=$true }
    Assert $rejected 'SYSTEM cannot launch the selected native runtime'
    'PASS: Desktop-only preparation, exact component bytes, equal-length cache repair/reuse, source update and account isolation'
} finally {
    $env:PATH=$originalPath
    $safe=[IO.Path]::GetFullPath($fixture)
    if (-not $safe.StartsWith([IO.Path]::GetFullPath([IO.Path]::GetTempPath()),[StringComparison]::OrdinalIgnoreCase)) { throw 'Unsafe fixture cleanup' }
    if (Test-Path -LiteralPath $safe) { Remove-Item -LiteralPath $safe -Recurse -Force }
}
