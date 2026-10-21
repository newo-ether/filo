$ErrorActionPreference='Stop'
. (Join-Path $PSScriptRoot '..\windows\Install-Support.ps1')
. (Join-Path $PSScriptRoot '..\windows\Source-Selection.ps1')
function Assert($Value,$Message) { if (-not $Value) { throw $Message } }
function Rejects([scriptblock]$Run) {
    $failed=$false
    try { & $Run | Out-Null } catch { $failed=$true }
    Assert $failed 'Expected invalid source configuration to be rejected'
}
$fixture=Join-Path ([IO.Path]::GetTempPath()) ('filo-sources-'+[guid]::NewGuid().ToString('N'))
try {
    New-Item -ItemType Directory -Path $fixture -Force | Out-Null
    Assert ((@(Get-FiloConfiguredSources ([pscustomobject]@{})) -join ',') -eq 'codex-desktop') 'Absent sources retain Desktop compatibility'
    foreach ($invalid in @(@(),@('unknown'),@('codex-cli','codex-cli'),@('CODEX-CLI'),@('codex-cli',7))) {
        Rejects { Get-FiloConfiguredSources ([pscustomobject]@{sources=$invalid}) }
    }
    Rejects { Get-FiloConfiguredSources ([pscustomobject]@{sources='codex-cli'}) }
    foreach ($sources in @(@('codex-cli'),@('claude-code'),@('codex-cli','claude-code'),@('codex-desktop','codex-cli','claude-code'))) {
        $options=Get-FiloSourceOptions $fixture $sources '' $false 7438 $false 7435
        Assert (($options.Sources -join ',') -eq ($sources -join ',')) 'Selected sources have one bounded configuration'
        Assert ($options.ClaudeBridgePort -eq 7438) 'Bridge defaults to its separate loopback port'
    }
    foreach ($port in @(7435,7436,7437)) {
        Rejects { Get-FiloSourceOptions $fixture @('codex-desktop','claude-code') '' $false $port $true 7435 }
    }
    Rejects { Get-FiloSourceOptions $fixture @('codex-cli') '' $false 7438 $false 7437 }
    Rejects { Get-FiloSourceOptions $fixture @('codex-desktop') '' $false 7438 $false 7436 }
    $socket=Join-Path $fixture 'daemon.sock'
    [IO.File]::WriteAllText($socket,'fixture; never connected')
    $selected=Get-FiloSourceOptions $fixture @('codex-cli') $socket $true 7438 $false 7435
    Assert ($selected.CodexDaemonSocket -eq $socket) 'Explicit existing socket path persists; protocol/peer verification remains native-owned'
    Rejects { Get-FiloSourceOptions $fixture @('codex-cli') 'relative.sock' $true 7438 $false 7435 }
    Rejects { Get-FiloSourceOptions $fixture @('codex-cli') '' $true 7438 $false 7435 }
    Rejects { Get-FiloSourceOptions $fixture @('claude-code') $socket $true 7438 $false 7435 }
    Write-FiloJson (Join-Path $fixture 'launch.json') @{sources=@('codex-cli','claude-code')}
    Write-FiloJson (Join-Path $fixture 'config.json') @{
        sources=@('codex-cli','claude-code');bind='127.0.0.1';port=28437;codexDaemonSocket=$socket;claudeBridgePort=28438
    }
    $existing=Get-FiloHelperEndpoints $fixture
    Assert (-not $existing.NativeUrl -and $existing.WorkerPort -eq 28437) 'No Desktop means no native endpoint claim'
    Remove-Item -LiteralPath $socket
    $preserved=Get-FiloSourceOptions $fixture @('codex-cli','claude-code') '' $false 7438 $false 7435
    Assert ($preserved.CodexDaemonSocket -eq $socket -and $preserved.ClaudeBridgePort -eq 28438) 'Offline daemon endpoint and configured bridge survive upgrade'
    Rejects { Get-FiloSourceOptions $fixture @('claude-code') '' $false 28437 $true 7435 }
    $withoutCLI=Get-FiloSourceOptions $fixture @('claude-code') '' $false 7438 $false 7435
    Assert (-not $withoutCLI.CodexDaemonSocket) 'Explicit source removal drops irrelevant CLI endpoint'
    Write-FiloJson (Join-Path $fixture 'launch.json') @{sources=@('codex-cli');appServerUrl='ws://127.0.0.1:7436'}
    Rejects { Get-FiloHelperEndpoints $fixture }
    $profile=Join-Path $fixture 'profile'
    New-Item -ItemType Directory -Path $profile -Force | Out-Null
    $owner=[Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    $bundle=Join-Path $fixture 'bundle'
    $mod=Join-Path $bundle 'internal\claude\mod'
    foreach ($relative in @('.claude-plugin\plugin.json','hooks\hooks.json','hooks\register.js')) {
        $file=Join-Path $mod $relative
        New-Item -ItemType Directory -Path (Split-Path -Parent $file) -Force | Out-Null
        [IO.File]::WriteAllText($file,'fixture; never loaded')
    }
    $target=Join-Path $profile 'filo-observer'
    Copy-FiloClaudeMod $bundle $target $profile $owner | Out-Null
    Assert (Test-Path -LiteralPath (Join-Path $target 'hooks\register.js')) 'Opt-in copy includes the Mod module'
    Assert (-not (Test-Path -LiteralPath (Join-Path $profile '.claude'))) 'Copy never creates native settings'
    Rejects { Copy-FiloClaudeMod $bundle $target $profile $owner }
    Rejects { Assert-FiloClaudeModDestination (Join-Path $profile '.claude\filo') $profile $owner }
    Rejects { Assert-FiloClaudeModDestination (Join-Path $fixture 'outside-profile') $profile $owner }
    $build=Get-Content -LiteralPath (Join-Path $PSScriptRoot '..\build-windows.ps1') -Raw
    foreach ($relative in @('scripts\windows\Source-Selection.ps1','internal\claude\mod\.claude-plugin\plugin.json','internal\claude\mod\hooks\hooks.json','internal\claude\mod\hooks\register.js')) {
        Assert ($build.Contains($relative)) ('Bundle explicitly includes '+$relative)
    }
    'PASS: bounded source configuration, legacy/default preservation, endpoint collisions, explicit Mod copy and bundle payload'
} finally {
    $safe=Assert-FiloPrivatePath $fixture ([IO.Path]::GetTempPath())
    if (Test-Path -LiteralPath $safe) { Remove-Item -LiteralPath $safe -Recurse -Force }
}
