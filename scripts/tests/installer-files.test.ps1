$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot '..\windows\Install-Support.ps1')
function Assert($Value, $Message) { if (-not $Value) { throw $Message } }
function Rejects([scriptblock]$Run) {
    $rejected = $false
    try { & $Run | Out-Null } catch { $rejected = $true }
    Assert $rejected 'Expected rejection'
}
$fixture = Join-Path ([IO.Path]::GetTempPath()) ('filo-install-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $fixture | Out-Null
try {
    $endpointState=Join-Path $fixture 'endpoint-state'
    New-Item -ItemType Directory -Path $endpointState | Out-Null
    $defaults=Get-FiloHelperEndpoints $endpointState
    Assert ($defaults.NativeUrl -eq 'ws://127.0.0.1:7436' -and $defaults.WorkerPort -eq 7437) 'Fresh install retains documented defaults'
    Write-FiloJson (Join-Path $endpointState 'launch.json') @{appServerUrl='ws://127.0.0.1:28436'}
    Rejects { Get-FiloHelperEndpoints $endpointState }
    foreach ($case in @(
        @{appServerUrl='ws://127.0.0.1:28435';bind='127.0.0.1';port=28437},
        @{appServerUrl='ws://127.0.0.1:28436';bind='0.0.0.0';port=28437},
        @{appServerUrl='ws://127.0.0.1:28436';bind='127.0.0.1';port=28436},
        @{appServerUrl='ws://127.0.0.1:28436';bind='127.0.0.1';port=65536},
        @{appServerUrl='ws://127.0.0.1:28436';bind='127.0.0.1';port='28437'}
    )) {
        Write-FiloJson (Join-Path $endpointState 'config.json') $case
        $before=[IO.File]::ReadAllBytes((Join-Path $endpointState 'config.json'))
        Rejects { Get-FiloHelperEndpoints $endpointState }
        Assert ([Convert]::ToBase64String($before) -eq [Convert]::ToBase64String([IO.File]::ReadAllBytes((Join-Path $endpointState 'config.json')))) 'Invalid endpoints remain untouched'
    }
    Write-FiloJson (Join-Path $endpointState 'config.json') @{appServerUrl='ws://127.0.0.1:28436';bind='127.0.0.1';port=28437}
    $custom=Get-FiloHelperEndpoints $endpointState
    Assert ($custom.NativeUrl -eq 'ws://127.0.0.1:28436' -and $custom.WorkerUrl -eq 'http://127.0.0.1:28437') 'Configured endpoints survive updates'
    $legacyLaunch=@{nodePath='C:\old-filo\tools\node.exe';codexPath='C:\original-runtime\codex.exe'}
    Write-FiloJson (Join-Path $endpointState 'launch.json') $legacyLaunch
    $beforeLaunch=[IO.File]::ReadAllText((Join-Path $endpointState 'launch.json'))
    $legacyEndpoints=Get-FiloHelperEndpoints $endpointState
    Assert ($legacyEndpoints.NativeUrl -eq 'ws://127.0.0.1:28436' -and $legacyEndpoints.WorkerPort -eq 28437) 'Legacy update uses recorded ports, not defaults'
    Assert ([IO.File]::ReadAllText((Join-Path $endpointState 'launch.json')) -ceq $beforeLaunch) 'Endpoint discovery never rewrites legacy state'
    foreach($invalid in @(@{}, @{nodePath='relative.exe';codexPath='C:\native\codex.exe'},
        @{nodePath='C:\old\node.exe'}, @{nodePath='C:\old\node.exe';codexPath='C:\native\codex.exe';appServerUrl=''})) {
        Write-FiloJson (Join-Path $endpointState 'launch.json') $invalid
        Rejects { Get-FiloHelperEndpoints $endpointState }
    }
    $sid = 'S-1-5-21-1-2-3-1001'
    $profile = Join-Path $fixture 'user'
    $shared = Join-Path $fixture ('agents\' + $sid)
    $legacy = Join-Path $profile 'AppData\Local\FiloStandalone'
    Assert ((Get-FiloWorkerState $fixture $profile $sid '') -eq $shared) 'New worker state must avoid virtualized AppData'
    Assert ((Get-FiloWorkerState $fixture $profile $sid (Join-Path $shared 'token')) -eq $shared) 'Preserve shared state on update'
    Assert ((Get-FiloWorkerState $fixture $profile $sid (Join-Path $legacy 'token')) -eq $legacy) 'Preserve existing worker state and token'
    Rejects { Get-FiloWorkerState $fixture $profile $sid (Join-Path $fixture 'other-user\token') }
    Rejects { Get-FiloWorkerState $fixture $profile '..\escape' '' }
    $tokenPath = Join-Path $fixture 'token'
    New-FiloToken $tokenPath
    $token = [IO.File]::ReadAllText($tokenPath)
    Assert ($token -match '^[0-9a-f]{64}$') 'Strong token format'
    New-FiloToken $tokenPath
    Assert ([IO.File]::ReadAllText($tokenPath) -eq $token) 'Update must preserve token'
    [IO.File]::WriteAllText($tokenPath,'broken')
    Rejects { New-FiloToken $tokenPath }
    Assert ([IO.File]::ReadAllText($tokenPath) -eq 'broken') 'Invalid prior token must remain unchanged'
    $tokenAcl = (Get-Acl -LiteralPath $tokenPath).Sddl
    Grant-FiloDirectoryRead $fixture $sid
    Grant-FiloDirectoryRead $fixture $sid
    $grants = @((Get-Acl -LiteralPath $fixture).GetAccessRules($true,$false,[Security.Principal.SecurityIdentifier]) |
        Where-Object { $_.IdentityReference.Value -eq $sid })
    Assert ($grants.Count -eq 1) 'Directory-read migration must be idempotent'
    Assert ($grants[0].InheritanceFlags -eq 'None') 'Traversal grant must not propagate to private descendants'
    Assert ($grants[0].FileSystemRights -eq 'ReadAndExecute, Synchronize') 'Selected user receives read/execute only'
    Assert ((Get-Acl -LiteralPath $tokenPath).Sddl -eq $tokenAcl) 'Existing token permissions must stay unchanged'
    $config = Join-Path $fixture 'config.json'
    Write-FiloJson $config @{first=1}
    Write-FiloJson $config @{second=2}
    Assert ((Get-Content -LiteralPath $config -Raw | ConvertFrom-Json).second -eq 2) 'Atomic replacement'
    Assert (@(Get-ChildItem -LiteralPath $fixture -Filter '*.tmp').Count -eq 0) 'No partial files'
    Rejects { Assert-FiloPrivatePath (Join-Path $fixture '..\escape') $fixture }
    Rejects { Assert-FiloPrivatePath ($fixture + '-sibling') $fixture }
    Assert ((Assert-FiloPrivatePath (Join-Path $fixture 'child') $fixture) -eq (Join-Path $fixture 'child')) 'Contained path'
    $bundle = Join-Path $fixture 'bundle'
    New-Item -ItemType Directory -Path $bundle | Out-Null
    $files = @()
    foreach ($relative in @('tools\filo.exe','scripts\system-service.ps1',
        'scripts\windows\Desktop-Identity.ps1',
        'scripts\resolve-desktop-runtime.ps1','scripts\windows\Desktop-Runtime.ps1',
        'scripts\windows\Host-Lifecycle.ps1',
        'scripts\windows\Installer-Tasks.ps1','scripts\windows\Install-Transaction.ps1',
        'scripts\windows\Uninstall-Transaction.ps1','scripts\windows\Service-State.ps1',
        'scripts\windows\Run-Standalone.ps1','scripts\windows\Worker-Update.ps1','tools\nssm.exe','tools\FiloBackground.exe')) {
        $file = Join-Path $bundle $relative
        New-Item -ItemType Directory -Path (Split-Path -Parent $file) -Force | Out-Null
        [IO.File]::WriteAllText($file,'fixture - never executed')
        $files += @{path=$relative;sha256=(Get-FileHash -LiteralPath $file).Hash}
    }
    $manifestPath = Join-Path $bundle 'manifest.json'
    $manifest = @{revision=('a' * 40);files=$files}
    Write-FiloJson $manifestPath $manifest
    Assert ((Get-FiloBundle $bundle).revision -eq ('a' * 40)) 'Complete verified package'
    [IO.File]::WriteAllText((Join-Path $bundle 'tools\filo.exe'),'tampered')
    Rejects { Get-FiloBundle $bundle }
    [IO.File]::WriteAllText((Join-Path $bundle 'tools\filo.exe'),'fixture - never executed')
    $manifest.files = $files + $files[0]
    Write-FiloJson $manifestPath $manifest
    Rejects { Get-FiloBundle $bundle }
    $manifest.files = @(@{path='..\token';sha256=('a' * 64)})
    Write-FiloJson $manifestPath $manifest
    Rejects { Get-FiloBundle $bundle }
    $manifest.files = @(@{path='tools\filo.exe:stream';sha256=('a' * 64)})
    Write-FiloJson $manifestPath $manifest
    Rejects { Get-FiloBundle $bundle }
    $manifest.files = @($files[0])
    Write-FiloJson $manifestPath $manifest
    Rejects { Get-FiloBundle $bundle }
} finally {
    $safe = Assert-FiloPrivatePath $fixture ([IO.Path]::GetTempPath())
    Remove-Item -LiteralPath $safe -Recurse -Force
}
Write-Output 'PASS: installer token, atomic file, containment and bundle-integrity checks'
