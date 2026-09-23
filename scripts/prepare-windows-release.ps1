<#
.SYNOPSIS
Build and verify the assets consumed by the one-command installer.
.DESCRIPTION
Creates a Windows ZIP and checksums.txt from the clean current source.
Does not create a tag, publish a release or change an installed service.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory=$true)][string]$WrapperPath,
    [Parameter(Mandatory=$true)][string]$OutputDirectory
)
$ErrorActionPreference = 'Stop'
# Resolve relative to the caller's PowerShell location, not the process working directory.
$OutputDirectory = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($OutputDirectory)
$bundle = & (Join-Path $PSScriptRoot 'build-windows.ps1') -WrapperPath $WrapperPath -OutputDirectory $OutputDirectory |
    Where-Object { $_.PSObject.Properties['archive'] }
if (-not $bundle -or -not (Test-Path -LiteralPath $bundle.archive)) { throw 'Windows bundle build did not complete.' }
$assets = Join-Path ([IO.Path]::GetFullPath($OutputDirectory)) 'release-assets'
if (Test-Path -LiteralPath $assets) { throw 'Release assets already exist; use a fresh output directory.' }
New-Item -ItemType Directory -Path $assets | Out-Null
$archive = Join-Path $assets 'filo-windows-amd64.zip'
Copy-Item -LiteralPath $bundle.archive -Destination $archive
$bootstrap = Join-Path (Split-Path -Parent $PSScriptRoot) 'install.ps1'
Copy-Item -LiteralPath $bootstrap -Destination (Join-Path $assets 'install.ps1')
# Invoke only the validation functions from the online entry, without downloading or installing.
$tokens = $null; $errors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile($bootstrap,[ref]$tokens,[ref]$errors)
if ($errors.Count) { throw 'The installer has a PowerShell syntax error.' }
foreach ($function in $ast.FindAll({param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst]},$false)) {
    . ([scriptblock]::Create($function.Extent.Text))
}
$verified = Expand-FiloRelease $archive (Join-Path $assets 'verification')
$revision = Assert-FiloReleaseContents $verified
if ($revision -cne $bundle.revision) { throw 'Release identity changed during packaging.' }
$checksums = @()
foreach ($name in @('filo-windows-amd64.zip','install.ps1')) {
    $hash = (Get-FileHash -LiteralPath (Join-Path $assets $name) -Algorithm SHA256).Hash.ToLowerInvariant()
    $checksums += "$hash  $name"
}
[IO.File]::WriteAllLines((Join-Path $assets 'checksums.txt'),$checksums,[Text.UTF8Encoding]::new($false))
[pscustomobject]@{
    Revision=$revision;Directory=$assets;Archive=$archive
    Sha256=(Get-FileHash -LiteralPath $archive).Hash;Bytes=(Get-Item -LiteralPath $archive).Length
    Assets=@('filo-windows-amd64.zip','install.ps1','checksums.txt')
}
