<#
.SYNOPSIS
Build an offline Windows candidate bundle from Filo sources.
.DESCRIPTION
NSSM comes from a verified local installation; no remote script is executed.
The service binary is compiled from the Go sources in this checkout, so the bundle
carries no Node runtime, TypeScript compiler or transpiled service.
The bundle includes the installer scripts, licenses and per-file hashes.
.PARAMETER AllowModifiedSource
Opt into a local candidate identified by its source inventory. The manifest records
actual HEAD and modified state; release preparation continues to require clean source.
#>
param(
    [Parameter(Mandatory=$true)][string]$WrapperPath,
    [Parameter(Mandatory=$true)][string]$OutputDirectory,
    [switch]$AllowModifiedSource
)
$ErrorActionPreference='Stop'
$ProgressPreference='SilentlyContinue'
function Get-FiloSourceIdentity {
    param([string]$Repository)
    $head = & git -C $Repository rev-parse HEAD
    if ($LASTEXITCODE -ne 0 -or $head -notmatch '^[0-9a-f]{40}$') { throw 'Cannot identify source revision' }
    $state = & git -C $Repository status --porcelain --untracked-files=normal
    if ($LASTEXITCODE -ne 0) { throw 'Cannot verify source worktree state' }
    $start = [Diagnostics.ProcessStartInfo]::new()
    $start.FileName = (Get-Command git -ErrorAction Stop).Source
    $start.Arguments = 'ls-files --cached --others --exclude-standard -z'
    $start.WorkingDirectory = $Repository
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    $start.StandardOutputEncoding = [Text.UTF8Encoding]::new($false)
    $process = [Diagnostics.Process]::Start($start)
    try {
        $inventory = $process.StandardOutput.ReadToEndAsync()
        $diagnostic = $process.StandardError.ReadToEndAsync()
        if (-not $process.WaitForExit(30000)) { $process.Kill(); throw 'Source inventory timed out' }
        if ($process.ExitCode -ne 0) { throw 'Cannot enumerate source files' }
        $paths = [string[]]@($inventory.GetAwaiter().GetResult().Split([char]0) | Where-Object { $_ })
        $diagnostic.GetAwaiter().GetResult() | Write-Verbose
    } finally { $process.Dispose() }
    [Array]::Sort($paths, [StringComparer]::Ordinal)
    $entries = foreach ($relative in $paths) {
        $sourcePath = Join-Path $Repository $relative
        if (Test-Path -LiteralPath $sourcePath -PathType Leaf) {
            $hash = (Get-FileHash -LiteralPath $sourcePath -Algorithm SHA256).Hash.ToLowerInvariant()
            $relative + [char]0 + $hash
        } elseif (-not (Test-Path -LiteralPath $sourcePath)) {
            $relative + [char]0 + 'deleted'
        } else { throw 'Source inventory contains a non-file entry' }
    }
    $algorithm = [Security.Cryptography.SHA256]::Create()
    try { $digest = ([BitConverter]::ToString($algorithm.ComputeHash([Text.Encoding]::UTF8.GetBytes(($entries -join "`n"))))).Replace('-','').ToLowerInvariant() }
    finally { $algorithm.Dispose() }
    [pscustomobject]@{Head=$head;Modified=[bool]$state;Sha256=$digest;Files=$paths}
}
$repo = Split-Path -Parent $PSScriptRoot
$source = Get-FiloSourceIdentity $repo
if ($source.Modified -and -not $AllowModifiedSource) { throw 'Build from a clean source checkpoint or explicitly select -AllowModifiedSource for a local candidate' }
$revision = if ($source.Modified) { $source.Sha256.Substring(0,40) } else { $source.Head }
$modified = ([string]$source.Modified).ToLowerInvariant()
$OutputDirectory = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($OutputDirectory)
$repoPrefix = [IO.Path]::GetFullPath($repo).TrimEnd('\') + '\'
foreach ($relative in $source.Files) {
    $sourcePath = [IO.Path]::GetFullPath((Join-Path $repo $relative))
    if ($sourcePath.StartsWith($OutputDirectory.TrimEnd('\') + '\',[StringComparison]::OrdinalIgnoreCase)) {
        throw 'Build output must not contain source inventory files'
    }
}
if ($OutputDirectory.StartsWith($repoPrefix,[StringComparison]::OrdinalIgnoreCase)) {
    & git -C $repo check-ignore --quiet -- ($OutputDirectory.TrimEnd('\') + '\filo-build-output')
    if ($LASTEXITCODE -ne 0) { throw 'An in-repository build output must be ignored by Git' }
}
if (-not (Test-Path -LiteralPath $WrapperPath -PathType Leaf)) { throw "Missing build prerequisite: $WrapperPath" }
# Resolve against the caller's directory before the build changes location.
$WrapperPath = (Get-Item -LiteralPath $WrapperPath).FullName
$wrapperVersion = & $WrapperPath version
if ($LASTEXITCODE -ne 0 -or $wrapperVersion -notmatch '^NSSM\s') { throw 'The selected service manager must provide the NSSM interface' }
$go = (Get-Command go -ErrorAction SilentlyContinue).Source
if (-not $go) { throw 'Missing build prerequisite: the Go toolchain must be on PATH' }
$goVersion = & $go version
if ($LASTEXITCODE -ne 0 -or $goVersion -notmatch 'go version go([0-9.]+)') { throw 'Cannot identify the Go toolchain' }
$goVersion = $Matches[1]
Push-Location $repo
try {
    # go vet also type-checks the test files, which is the check tsc used to provide.
    & $go vet ./...
    if ($LASTEXITCODE -ne 0) { throw 'Go vet failed' }
    & $go test -count=1 -timeout 300s ./internal/sourcebudget/
    if ($LASTEXITCODE -ne 0) { throw 'The maintained source size budget failed' }
} finally { Pop-Location }
New-Item -ItemType Directory -Path $OutputDirectory -Force | Out-Null
$target = Join-Path (Get-Item -LiteralPath $OutputDirectory).FullName ('filo-windows-' + $revision)
if (Test-Path -LiteralPath $target) { throw 'Immutable output already exists; use a new output directory' }
New-Item -ItemType Directory -Path $target | Out-Null
function Copy-Payload([string]$Source,[string]$Relative) {
    $destination = Join-Path $target $Relative
    New-Item -ItemType Directory -Path (Split-Path -Parent $destination) -Force | Out-Null
    Copy-Item -LiteralPath $Source -Destination $destination
}
foreach ($relative in @('README.md','LICENSE','THIRD_PARTY_NOTICES.txt','thirdparty\conch\LICENSE','docs\windows-installer.md','docs\windows-auxiliary.md',
    'docs\codex-connection-contract.md','docs\original-desktop-attachment.md','docs\errors.md',
    'scripts\install.ps1','scripts\system-service.ps1',
    'scripts\resolve-desktop-runtime.ps1','scripts\windows\Desktop-Runtime.ps1',
    'scripts\windows\Host-Lifecycle.ps1',
    'scripts\windows\Installer-Tasks.ps1','scripts\windows\Install-Transaction.ps1',
    'scripts\windows\Uninstall-Transaction.ps1','scripts\windows\Service-State.ps1',
    'scripts\windows\Desktop-Identity.ps1','scripts\windows\Client-Selection.ps1','scripts\windows\Install-Support.ps1','scripts\windows\Worker-Update.ps1','scripts\windows\Run-Standalone.ps1')) {
    Copy-Payload (Join-Path $repo $relative) $relative
}
$binary = Join-Path $target 'tools\filo.exe'
New-Item -ItemType Directory -Path (Split-Path -Parent $binary) -Force | Out-Null
$buildEnvironment = @{}
foreach ($name in @('CGO_ENABLED','GOOS','GOARCH')) { $buildEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process') }
$env:CGO_ENABLED='0'; $env:GOOS='windows'; $env:GOARCH='amd64'
Push-Location $repo
try {
    $buildFlags = "-H=windowsgui -X github.com/newo-ether/filo/internal/service.buildRevision=$revision -X github.com/newo-ether/filo/internal/service.buildModified=$modified"
    & $go build -trimpath -buildvcs=false -ldflags $buildFlags -o $binary ./cmd/filo
    if ($LASTEXITCODE -ne 0) { throw 'Go build failed' }
} finally {
    Pop-Location
    foreach ($name in $buildEnvironment.Keys) { [Environment]::SetEnvironmentVariable($name, $buildEnvironment[$name], 'Process') }
}
# The payload must run without Node, so refuse to ship something that only pretends to be Filo.
# A bare call answers its usage on stderr and exits 2. Windows PowerShell turns a redirected
# native stderr into an error record, which the Stop preference above would make terminating,
# so the probe runs under the relaxed preference its own stderr requires.
. (Join-Path $repo 'scripts\windows\Service-State.ps1')
if (-not (Test-FiloEntrypoint $binary)) { throw 'The built service binary is not a Filo release' }
Copy-Payload $WrapperPath 'tools\nssm.exe'
$compiler=[Diagnostics.ProcessStartInfo]::new()
$compiler.FileName=Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
$compiler.Arguments='-NoProfile -NonInteractive -File "'+(Join-Path $repo 'scripts\windows\Build-BackgroundLauncher.ps1')+'" -OutputPath "'+(Join-Path $target 'tools\FiloBackground.exe')+'"'
$compiler.UseShellExecute=$false; $compiler.CreateNoWindow=$true
$compiler.RedirectStandardOutput=$true; $compiler.RedirectStandardError=$true
$compilerProcess=[Diagnostics.Process]::Start($compiler)
try {
    $output=$compilerProcess.StandardOutput.ReadToEndAsync()
    $errors=$compilerProcess.StandardError.ReadToEndAsync()
    $compilerProcess.WaitForExit()
    if ($compilerProcess.ExitCode -ne 0) { throw ('Background launcher build failed: '+$errors.GetAwaiter().GetResult()) }
    $output.GetAwaiter().GetResult() | Write-Verbose
} finally { $compilerProcess.Dispose() }
[IO.File]::WriteAllText((Join-Path $target 'tools\NSSM-NOTICE.txt'),
    "NSSM 2.24 is public domain. Source and usage: https://nssm.cc/usage")
Copy-Payload (Join-Path $repo 'scripts\install-entry.ps1') 'Install-Filo.ps1'
$files = @(Get-ChildItem -LiteralPath $target -File -Recurse | Sort-Object FullName | ForEach-Object {
    @{path=$_.FullName.Substring($target.Length+1);sha256=(Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash}
})
$afterSource = Get-FiloSourceIdentity $repo
if ($source.Head -cne $afterSource.Head -or $source.Sha256 -cne $afterSource.Sha256 -or $source.Modified -ne $afterSource.Modified) {
    throw 'Source changed during the build; the candidate cannot be installed'
}
$manifest = @{revision=$revision;kind='windows-candidate';goVersion=$goVersion;
    sourceRevision=$source.Head;sourceModified=$source.Modified;sourceSha256=$source.Sha256;files=$files}
[IO.File]::WriteAllText((Join-Path $target 'manifest.json'),($manifest|ConvertTo-Json -Depth 5),[Text.UTF8Encoding]::new($false))
. (Join-Path $repo 'scripts\windows\Install-Support.ps1')
Get-FiloBundle $target | Out-Null
$archive = $target + '.zip'
Add-Type -AssemblyName System.IO.Compression.FileSystem
[IO.Compression.ZipFile]::CreateFromDirectory($target, $archive, [IO.Compression.CompressionLevel]::Optimal, $true)
[pscustomobject]@{revision=$revision;directory=$target;archive=$archive;sha256=(Get-FileHash -LiteralPath $archive).Hash;bytes=(Get-Item -LiteralPath $archive).Length}
