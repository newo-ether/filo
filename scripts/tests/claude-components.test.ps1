$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot '..\windows\Claude-Components.ps1')
function Assert($Value, $Message) { if (-not $Value) { throw $Message } }
function Rejects([scriptblock]$Run) {
    $failed = $false
    try { & $Run | Out-Null } catch { $failed = $true }
    Assert $failed 'Expected conflicting Claude component state to be rejected'
}
# Fixtures never alter ACLs or native settings. Production uses Install-Support's ACL owner.
function Protect-FiloDirectory([string]$Path, [string]$UserSid) {
    Assert ($UserSid -eq $script:owner) 'Component protection uses the selected user SID'
    New-Item -ItemType Directory -Path $Path -Force | Out-Null
}
$fixture = Join-Path ([IO.Path]::GetTempPath()) ('filo-claude-components-' + [guid]::NewGuid().ToString('N'))
try {
    $profile = Join-Path $fixture 'profile'
    $worker = Join-Path $fixture 'worker'
    $bundle = Join-Path $fixture 'bundle'
    $owner = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    New-Item -ItemType Directory -Path $profile, $worker -Force | Out-Null
    $source = Join-Path $bundle 'internal\claude\mod'
    $payload = @{
        '.claude-plugin\plugin.json' = '{"name":"filo-observer","version":"fixture"}'
        'hooks\hooks.json' = '{"modules":["./register.js"]}'
        'hooks\register.js' = '// fixture module; never loaded'
    }
    foreach ($relative in $payload.Keys) {
        $path = Join-Path $source $relative
        New-Item -ItemType Directory -Path (Split-Path -Parent $path) -Force | Out-Null
        [IO.File]::WriteAllText($path, $payload[$relative])
    }
    $configPath = Join-Path $worker 'claude-mod.json'
    [IO.File]::WriteAllText($configPath, (@{url='http://127.0.0.1:7438/claude-mod/v1/';tokenFile=(Join-Path $worker 'claude-mod-token')} | ConvertTo-Json))
    $none = New-FiloClaudeComponentPlan $bundle $worker $profile $owner @('codex-cli')
    Install-FiloClaudeComponents $none
    Assert (-not (Test-Path -LiteralPath (Join-Path $profile '.claude'))) 'Unselected Claude creates no native directory'
    $plan = New-FiloClaudeComponentPlan $bundle $worker $profile $owner @('claude-desktop')
    Assert (-not (Test-Path -LiteralPath $plan.Root)) 'Preflight is read-only'
    Install-FiloClaudeComponents $plan
    $root = $plan.Root
    $config = [IO.File]::ReadAllText((Join-Path $root 'filo-config.json')) | ConvertFrom-Json
    Assert (($config.sources -join ',') -ceq 'claude-desktop') 'Desktop-only selection is self-contained'
    Assert ($config.tokenFile -eq (Join-Path $worker 'claude-mod-token')) 'Config has a private token pointer only'
    Assert (-not (Test-Path -LiteralPath (Join-Path $profile '.claude\settings.json'))) 'No native settings file was written'
    Assert (Test-Path -LiteralPath $plan.Receipt) 'Selected-user ownership receipt is persisted'
    Restore-FiloClaudeComponents $plan
    Assert (-not (Test-Path -LiteralPath (Join-Path $profile '.claude'))) 'Fresh-install rollback removes only its new directories'
    Assert (-not (Test-Path -LiteralPath $plan.Receipt)) 'Fresh-install rollback removes its receipt'

    $plan = New-FiloClaudeComponentPlan $bundle $worker $profile $owner @('claude-code', 'claude-desktop')
    Install-FiloClaudeComponents $plan
    $settings = Join-Path $profile '.claude\settings.json'
    [IO.File]::WriteAllText($settings, '{"untouched":"fixture"}')
    $settingsBytes = [IO.File]::ReadAllBytes($settings)
    $unrelated = Join-Path $root 'user-note.txt'
    [IO.File]::WriteAllText($unrelated, 'keep me')
    $before = @{}
    foreach ($path in @($plan.Snapshots.Keys)) { $before[$path] = [IO.File]::ReadAllBytes($path) }
    Remove-Item -LiteralPath $unrelated -Force
    $cleanRemoval = New-FiloClaudeComponentPlan $bundle $worker $profile $owner @('codex-cli')
    Install-FiloClaudeComponents $cleanRemoval
    Assert (-not (Test-Path -LiteralPath $root)) 'Clean deselection removes the empty plugin directory'
    Restore-FiloClaudeComponents $cleanRemoval
    foreach ($path in $before.Keys) {
        Assert (Test-FiloClaudeComponentBytes ([IO.File]::ReadAllBytes($path)) $before[$path]) 'Deselection rollback recreates exact managed payload'
    }
    [IO.File]::WriteAllText($unrelated, 'keep me')
    $repeat = New-FiloClaudeComponentPlan $bundle $worker $profile $owner @('claude-code', 'claude-desktop')
    Install-FiloClaudeComponents $repeat
    foreach ($path in $before.Keys) {
        Assert (Test-FiloClaudeComponentBytes ([IO.File]::ReadAllBytes($path)) $before[$path]) 'Repeated install is byte-idempotent'
    }
    $upgrade = New-FiloClaudeComponentPlan $bundle $worker $profile $owner @('claude-desktop')
    Install-FiloClaudeComponents $upgrade
    Restore-FiloClaudeComponents $upgrade
    foreach ($path in $before.Keys) {
        Assert (Test-FiloClaudeComponentBytes ([IO.File]::ReadAllBytes($path)) $before[$path]) 'Upgrade rollback restores exact owned bytes'
    }
    $module = Join-Path $root 'hooks\register.js'
    [IO.File]::WriteAllText($module, 'user modified the module')
    Rejects { New-FiloClaudeComponentPlan $bundle $worker $profile $owner @('claude-code') }
    Rejects { Remove-FiloClaudeComponents $worker $owner }
    [IO.File]::WriteAllBytes($module, $before[$module])
    $changed = New-FiloClaudeComponentPlan $bundle $worker $profile $owner @('claude-code')
    [IO.File]::WriteAllText($module, 'changed after preflight')
    Rejects { Install-FiloClaudeComponents $changed }
    [IO.File]::WriteAllBytes($module, $before[$module])

    $failed = New-FiloClaudeComponentPlan $bundle $worker $profile $owner @('claude-desktop')
    $script:realWrite = ${function:Set-FiloClaudeComponentBytes}
    $script:failAt = Join-Path $root 'hooks\register.js'
    function Set-FiloClaudeComponentBytes([string]$Path, $Bytes) {
        if ($Path -eq $script:failAt) { throw 'fixture interrupted write' }
        & $script:realWrite $Path $Bytes
    }
    Rejects { Install-FiloClaudeComponents $failed }
    ${function:Set-FiloClaudeComponentBytes} = $script:realWrite
    Restore-FiloClaudeComponents $failed
    foreach ($path in $before.Keys) {
        Assert (Test-FiloClaudeComponentBytes ([IO.File]::ReadAllBytes($path)) $before[$path]) 'Interrupted install can restore exact previous bytes'
    }
    $ownerConflict = New-FiloClaudeComponentPlan $bundle $worker $profile $owner @('claude-desktop')
    Install-FiloClaudeComponents $ownerConflict
    [IO.File]::WriteAllText($module, 'changed during rollback')
    Rejects { Restore-FiloClaudeComponents $ownerConflict }
    Assert ([IO.File]::ReadAllText($module) -ceq 'changed during rollback') 'Rollback preserves concurrent edits'
    foreach ($path in $before.Keys) { [IO.File]::WriteAllBytes($path, $before[$path]) }
    $deselected = New-FiloClaudeComponentPlan $bundle $worker $profile $owner @('codex-cli')
    Install-FiloClaudeComponents $deselected
    Assert (-not (Test-Path -LiteralPath (Join-Path $root '.claude-plugin\plugin.json'))) 'Deselecting Claude removes automatic loading manifest'
    Assert (Test-Path -LiteralPath $unrelated) 'Deselect preserves unrelated plugin-directory data'
    Restore-FiloClaudeComponents $deselected
    Remove-FiloClaudeComponents $worker $owner
    Assert (-not (Test-Path -LiteralPath $plan.Receipt)) 'Uninstall removes only the owned receipt and payload'
    Assert (Test-Path -LiteralPath $unrelated) 'Uninstall preserves unknown files'
    Assert (Test-FiloClaudeComponentBytes ([IO.File]::ReadAllBytes($settings)) $settingsBytes) 'All operations preserve exact native settings bytes'
    Rejects { New-FiloClaudeComponentPlan $bundle $worker $profile $owner @('claude-code') }
    Remove-Item -LiteralPath $unrelated -Force
    Remove-Item -LiteralPath $root -Force
    $plan = New-FiloClaudeComponentPlan $bundle $worker $profile $owner @('claude-code')
    New-Item -ItemType Directory -Path $root -Force | Out-Null
    Rejects { Install-FiloClaudeComponents $plan }
    Remove-Item -LiteralPath $root -Force
    Rejects { Assert-FiloClaudeComponentPath '\\server\share\observer' }
    Rejects { Assert-FiloClaudeComponentPath ($profile + ':stream') }
    # Each robustness case uses real component files under its own disposable profile.
    function New-ComponentFixture([string]$Name, [switch]$Installed) {
        $base = Join-Path $fixture $Name
        $value = [pscustomobject]@{ Profile=(Join-Path $base 'profile'); Worker=(Join-Path $base 'worker');
            Root=(Join-Path $base 'profile\.claude\skills\filo-observer'); Plan=$null }
        New-Item -ItemType Directory -Path (Join-Path $value.Profile '.claude'), $value.Worker -Force | Out-Null
        [IO.File]::WriteAllText((Join-Path $value.Profile '.claude\settings.json'), '{"native":"preserve settings"}')
        [IO.File]::WriteAllText((Join-Path $value.Profile '.claude\.credentials.json'), 'native credential fixture')
        [IO.File]::WriteAllText((Join-Path $value.Worker 'token'), ('a' * 64) + "`r`n")
        [IO.File]::WriteAllText((Join-Path $value.Worker 'claude-mod-token'), ('b' * 64) + "`r`n")
        [IO.File]::WriteAllText((Join-Path $value.Worker 'claude-mod.json'), (@{
            url='http://127.0.0.1:7438/claude-mod/v1/'; tokenFile=(Join-Path $value.Worker 'claude-mod-token')
        } | ConvertTo-Json))
        if ($Installed) {
            $value.Plan = New-FiloClaudeComponentPlan $bundle $value.Worker $value.Profile $owner @('claude-code')
            Install-FiloClaudeComponents $value.Plan
        }
        return $value
    }
    function Get-ComponentFixtureSnapshot($Case) {
        $bytes = @{}
        foreach ($directory in @($Case.Profile, $Case.Worker)) {
            foreach ($file in @(Get-ChildItem -LiteralPath $directory -File -Recurse -Force)) {
                $bytes[$file.FullName] = [Convert]::ToBase64String([IO.File]::ReadAllBytes($file.FullName))
            }
        }
        return $bytes
    }
    function Assert-ComponentFixtureSnapshot($Case, $Expected, [string]$Message) {
        $actual = Get-ComponentFixtureSnapshot $Case
        Assert ($actual.Count -eq $Expected.Count) ($Message + ': file inventory')
        foreach ($path in $Expected.Keys) {
            Assert ($actual.ContainsKey($path) -and $actual[$path] -ceq $Expected[$path]) ($Message + ': ' + $path)
        }
    }
    $robustnessCases = 0
    foreach ($relative in @('.claude-plugin\plugin.json', 'hooks\hooks.json', 'hooks\register.js')) {
        $robustnessCases++
        $case = New-ComponentFixture ('missing-source-' + $robustnessCases) -Installed
        $snapshot = Get-ComponentFixtureSnapshot $case
        $path = Join-Path $source $relative
        $sourceBytes = [IO.File]::ReadAllBytes($path)
        try {
            Remove-Item -LiteralPath $path -Force
            Rejects { New-FiloClaudeComponentPlan $bundle $case.Worker $case.Profile $owner @('claude-code') }
            Assert-ComponentFixtureSnapshot $case $snapshot 'Missing bundle payload is refused before mutation'
        } finally { [IO.File]::WriteAllBytes($path, $sourceBytes) }
    }
    foreach ($relative in @('.claude-plugin\plugin.json', 'hooks\hooks.json', 'hooks\register.js', 'filo-config.json')) {
        foreach ($damage in @('missing', 'corrupt')) {
            $robustnessCases++
            $case = New-ComponentFixture ('managed-damage-' + $robustnessCases) -Installed
            $path = Join-Path $case.Root $relative
            if ($damage -eq 'missing') { Remove-Item -LiteralPath $path -Force }
            else {
                $bytes = [IO.File]::ReadAllBytes($path)
                $bytes[0] = $bytes[0] -bxor 1
                [IO.File]::WriteAllBytes($path, $bytes)
            }
            $snapshot = Get-ComponentFixtureSnapshot $case
            Rejects { New-FiloClaudeComponentPlan $bundle $case.Worker $case.Profile $owner @('claude-desktop') }
            Rejects { Remove-FiloClaudeComponents $case.Worker $owner }
            Assert-ComponentFixtureSnapshot $case $snapshot 'Damaged owned payload is preserved on update and removal refusal'
        }
    }
    foreach ($damage in @('missing-marker', 'different-marker', 'invalid-json', 'sid', 'profile', 'worker', 'pluginRoot', 'hash')) {
        $robustnessCases++
        $case = New-ComponentFixture ('receipt-damage-' + $robustnessCases) -Installed
        $receipt = Join-Path $case.Worker 'claude-components.json'
        $marker = Join-Path $case.Root '.filo-owner.json'
        switch ($damage) {
            'missing-receipt' { Remove-Item -LiteralPath $receipt -Force }
            'missing-marker' { Remove-Item -LiteralPath $marker -Force }
            'different-marker' { [IO.File]::WriteAllText($marker, 'not the private receipt') }
            'invalid-json' {
                [IO.File]::WriteAllText($receipt, '{invalid')
                [IO.File]::WriteAllText($marker, '{invalid')
            }
            default {
                $record = [IO.File]::ReadAllText($receipt) | ConvertFrom-Json
                if ($damage -eq 'sid') { $record.sid = 'S-1-5-21-9-9-9-1002' }
                elseif ($damage -eq 'hash') { $record.files[0].sha256 = '0' * 64 }
                else { $record.$damage = Join-Path $fixture 'other-owner' }
                $text = $record | ConvertTo-Json -Depth 6
                [IO.File]::WriteAllText($receipt, $text)
                [IO.File]::WriteAllText($marker, $text)
            }
        }
        $snapshot = Get-ComponentFixtureSnapshot $case
        Rejects { New-FiloClaudeComponentPlan $bundle $case.Worker $case.Profile $owner @('claude-code') }
        Assert-ComponentFixtureSnapshot $case $snapshot 'Ownership refusal leaves every current byte intact'
    }
    foreach ($legacy in @($false,$true)) {
        $case=New-ComponentFixture ('orphan-marker-'+$legacy) -Installed
        $receipt=Join-Path $case.Worker 'claude-components.json'
        $marker=Join-Path $case.Root '.filo-owner.json'
        Remove-Item -LiteralPath $receipt -Force
        if ($legacy) {
            $record=Get-Content -LiteralPath $marker -Raw | ConvertFrom-Json
            $record.worker=Join-Path $case.Profile 'AppData\Local\FiloStandalone'
            [IO.File]::WriteAllText($marker,($record | ConvertTo-Json -Depth 6))
        }
        $repair=New-FiloClaudeComponentPlan $bundle $case.Worker $case.Profile $owner @('claude-code')
        Install-FiloClaudeComponents $repair
        Assert (Test-Path -LiteralPath $receipt) 'Verified orphan helper is replaced and gets a fixed-worker receipt'
        Remove-Item -LiteralPath $receipt -Force
        $removal=New-FiloClaudeComponentPlan $bundle $case.Worker $case.Profile $owner @()
        Install-FiloClaudeComponents $removal
        Assert (-not (Test-Path -LiteralPath $case.Root)) 'Verified orphan helper can be fully uninstalled without private configuration'
    }
    $robustnessCases++
    $case = New-ComponentFixture 'equal-size-source-change' -Installed
    $snapshot = Get-ComponentFixtureSnapshot $case
    $sourceModule = Join-Path $source 'hooks\register.js'
    $sourceBytes = [IO.File]::ReadAllBytes($sourceModule)
    $sourceTime = [IO.File]::GetLastWriteTimeUtc($sourceModule)
    try {
        $changedBytes = [byte[]]$sourceBytes.Clone()
        $changedBytes[$changedBytes.Length - 1] = $changedBytes[$changedBytes.Length - 1] -bxor 1
        [IO.File]::WriteAllBytes($sourceModule, $changedBytes)
        [IO.File]::SetLastWriteTimeUtc($sourceModule, $sourceTime)
        $change = New-FiloClaudeComponentPlan $bundle $case.Worker $case.Profile $owner @('claude-desktop')
        Install-FiloClaudeComponents $change
        Assert ([Convert]::ToBase64String([IO.File]::ReadAllBytes((Join-Path $case.Root 'hooks\register.js'))) -ceq
            [Convert]::ToBase64String($changedBytes)) 'Changed source bytes replace the old payload despite identical size and timestamp'
        $repeatSnapshot = Get-ComponentFixtureSnapshot $case
        $repeat = New-FiloClaudeComponentPlan $bundle $case.Worker $case.Profile $owner @('claude-desktop')
        Install-FiloClaudeComponents $repeat
        Assert-ComponentFixtureSnapshot $case $repeatSnapshot 'Repeated updated payload installation preserves exact bytes'
        Restore-FiloClaudeComponents $change
        Assert-ComponentFixtureSnapshot $case $snapshot 'Source and source-selection rollback restores the previous installation'
    } finally {
        [IO.File]::WriteAllBytes($sourceModule, $sourceBytes)
        [IO.File]::SetLastWriteTimeUtc($sourceModule, $sourceTime)
    }
    foreach ($installed in @($false, $true)) {
        foreach ($destination in @('filo-config.json', 'hooks\hooks.json', 'hooks\register.js', '.claude-plugin\plugin.json', '.filo-owner.json', 'receipt')) {
            $robustnessCases++
            $case = New-ComponentFixture ('interrupted-' + $robustnessCases) -Installed:$installed
            $snapshot = Get-ComponentFixtureSnapshot $case
            $interrupted = New-FiloClaudeComponentPlan $bundle $case.Worker $case.Profile $owner @('claude-desktop')
            $script:failAt = if ($destination -eq 'receipt') { $interrupted.Receipt } else { Join-Path $case.Root $destination }
            function Set-FiloClaudeComponentBytes([string]$Path, $Bytes) {
                if ($Path -eq $script:failAt) { throw 'fixture interrupted write' }
                & $script:realWrite $Path $Bytes
            }
            try { Rejects { Install-FiloClaudeComponents $interrupted } }
            finally { ${function:Set-FiloClaudeComponentBytes} = $script:realWrite }
            if (-not $installed -and $destination -notin @('.filo-owner.json', 'receipt')) {
                Assert (-not (Test-Path -LiteralPath (Join-Path $case.Root '.claude-plugin\plugin.json'))) 'Fresh partial payload is not published for native discovery'
            }
            Restore-FiloClaudeComponents $interrupted
            Assert-ComponentFixtureSnapshot $case $snapshot 'Interrupted writes restore exact native, token, configuration and payload bytes'
            if (-not $installed) { Assert (-not (Test-Path -LiteralPath $case.Root)) 'Fresh partial-write rollback removes its own directories' }
        }
    }
    # Real NTFS junctions exercise link refusal without requiring symbolic-link privilege.
    foreach ($location in @('profile', 'worker', 'plugin', 'hooks', 'bundle')) {
        $robustnessCases++
        $case = New-ComponentFixture ('junction-' + $location) -Installed
        $link = switch ($location) {
            'profile' { Join-Path $case.Profile '.claude' }
            'worker' { $case.Worker }
            'plugin' { $case.Root }
            'hooks' { Join-Path $case.Root 'hooks' }
            'bundle' { $source }
        }
        $target = $link + '-junction-target'
        Move-Item -LiteralPath $link -Destination $target
        try {
            New-Item -ItemType Junction -Path $link -Target $target | Out-Null
            $snapshot = Get-ComponentFixtureSnapshot $case
            $targetCase = [pscustomobject]@{Profile=$target; Worker=$case.Worker}
            $targetSnapshot = Get-ComponentFixtureSnapshot $targetCase
            Rejects { New-FiloClaudeComponentPlan $bundle $case.Worker $case.Profile $owner @('claude-code') }
            Assert-ComponentFixtureSnapshot $case $snapshot 'Linked component path is refused without touching its target'
            Assert-ComponentFixtureSnapshot $targetCase $targetSnapshot 'Link refusal preserves files read directly from the real target'
        } finally {
            if (Test-Path -LiteralPath $link) { [IO.Directory]::Delete($link) }
            Move-Item -LiteralPath $target -Destination $link
        }
    }
    "PASS: existing Claude component lifecycle checks and $robustnessCases additional production robustness cases (missing/corrupt payload, ownership, source updates, partial writes and real NTFS links)"
} finally {
    if (Test-Path -LiteralPath $fixture) { Remove-Item -LiteralPath $fixture -Recurse -Force }
}
