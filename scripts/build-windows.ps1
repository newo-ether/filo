<#
.SYNOPSIS
Build an offline Windows candidate bundle from a clean Filo checkout.
.DESCRIPTION
NSSM comes from a verified local installation; no remote script is executed.
The service binary is compiled from the Go sources in this checkout, so the bundle
carries no Node runtime, TypeScript compiler or transpiled service.
The bundle includes the installer scripts, licenses and per-file hashes.
#>
param(
    [Parameter(Mandatory=$true)][string]$WrapperPath,
    [Parameter(Mandatory=$true)][string]$OutputDirectory
)
$ErrorActionPreference='Stop'
$repo = Split-Path -Parent $PSScriptRoot
$revision = & git -c "safe.directory=$($repo.Replace('\','/'))" -C $repo rev-parse HEAD
if ($LASTEXITCODE -ne 0 -or $revision -notmatch '^[0-9a-f]{40}$') { throw 'Cannot identify source revision' }
$dirty = & git -c "safe.directory=$($repo.Replace('\','/'))" -C $repo status --porcelain --untracked-files=normal
if ($dirty) { throw 'Build from a clean source checkpoint' }
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
$env:CGO_ENABLED='0'; $env:GOOS='windows'; $env:GOARCH='amd64'
Push-Location $repo
try {
    & $go build -trimpath -ldflags '-H=windowsgui' -o $binary ./cmd/filo
    if ($LASTEXITCODE -ne 0) { throw 'Go build failed' }
} finally {
    Pop-Location
    Remove-Item Env:CGO_ENABLED,Env:GOOS,Env:GOARCH -ErrorAction SilentlyContinue
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
$manifest = @{revision=$revision;kind='windows-candidate';goVersion=$goVersion;files=$files}
[IO.File]::WriteAllText((Join-Path $target 'manifest.json'),($manifest|ConvertTo-Json -Depth 5),[Text.UTF8Encoding]::new($false))
. (Join-Path $repo 'scripts\windows\Install-Support.ps1')
Get-FiloBundle $target | Out-Null
$archive = $target + '.zip'
Compress-Archive -LiteralPath $target -DestinationPath $archive -CompressionLevel Optimal
[pscustomobject]@{revision=$revision;directory=$target;archive=$archive;sha256=(Get-FileHash -LiteralPath $archive).Hash;bytes=(Get-Item -LiteralPath $archive).Length}
