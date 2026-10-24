$ErrorActionPreference='Stop'
. (Join-Path $PSScriptRoot '..\windows\Uninstall-Transaction.ps1')
function Assert($Value,$Message) { if (-not $Value) { throw $Message } }
$fixture=Join-Path ([IO.Path]::GetTempPath()) ('filo-uninstall-'+[guid]::NewGuid().ToString('N'))
$program=Join-Path $fixture 'Program Files\Filo'; $root=Join-Path $fixture 'data'; $profile=Join-Path $fixture 'user'
$events=[Collections.Generic.List[string]]::new()
function Get-FiloProgramRoot { $program }
function Get-CimInstance { param($ClassName) [pscustomobject]@{Special=$false;LocalPath=$profile} }
function Stop-FiloInstalledResources { $events.Add('stop') }
function Remove-FiloFirewallRules { $events.Add('firewall') }
function Update-FiloMachinePath { param($ProgramRoot,[switch]$Remove) Assert ($Remove -and $ProgramRoot -eq $program) 'Uninstall removes the fixed machine PATH entry'; $events.Add('path') }
try {
    foreach ($directory in @($program,$root) + @(Get-FiloLegacyDirectories $root @($profile))) {
        New-Item -ItemType Directory -Path $directory -Force | Out-Null
        [IO.File]::WriteAllText((Join-Path $directory 'owned.bin'),'Filo data')
    }
    $native=Join-Path $profile '.codex'; New-Item -ItemType Directory -Path $native -Force | Out-Null
    [IO.File]::WriteAllText((Join-Path $native 'history.jsonl'),'native history')
    foreach ($pass in @(1,2)) {
        Invoke-FiloUninstall -Bundle $fixture -Root $root -State (Join-Path $root 'service') -Worker '' -Sid '' | Out-Null
        Assert (-not (Test-Path $program) -and -not (Test-Path $root)) 'Both installed roots are removed even without configuration'
        foreach ($directory in @(Get-FiloLegacyDirectories $root @($profile))) { Assert (-not (Test-Path $directory)) 'AppData and obsolete layouts are removed' }
        Assert ([IO.File]::ReadAllText((Join-Path $native 'history.jsonl')) -eq 'native history') 'Native data remains intact'
    }
    Assert (($events -join ',') -eq 'stop,firewall,path,stop,firewall,path') 'Uninstall always stops before removing registrations and files'
    'PASS: symmetric full removal, absent-config cleanup, repeat uninstall and native history preservation'
} finally { if (Test-Path $fixture) { Remove-Item $fixture -Recurse -Force } }
