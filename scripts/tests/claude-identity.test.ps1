$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot '..\windows\Claude-Identity.ps1')
function Assert($Value, $Message) { if (-not $Value) { throw $Message } }
$fixture = Join-Path ([IO.Path]::GetTempPath()) ('filo-claude-identity-' + [guid]::NewGuid().ToString('N'))
try {
    New-Item -ItemType Directory -Path "$fixture\AppData\Local\AnthropicClaude","$fixture\package\app" -Force | Out-Null
    [IO.File]::WriteAllText("$fixture\AppData\Local\AnthropicClaude\claude.exe", 'fixture; never executed')
    [IO.File]::WriteAllText("$fixture\package\app\Claude.exe", 'fixture; never executed')
    [IO.File]::WriteAllText("$fixture\package\AppxManifest.xml", '<Package><Applications><Application Executable="app/Claude.exe" /></Applications></Package>')
    $script:signatureStatus = 'Valid'
    $script:signatureSubject = 'CN=Anthropic, PBC, O=Anthropic, PBC, C=US'
    function Get-AuthenticodeSignature {
        param($LiteralPath, $ErrorAction)
        [pscustomobject]@{Status=$script:signatureStatus;SignerCertificate=[pscustomobject]@{Subject=$script:signatureSubject}}
    }
    function Get-AppxPackage {
        param($Name, $User, $ErrorAction)
        Assert ($Name -eq '*Claude*') 'Only inspect the Claude package namespace'
        [pscustomobject]@{InstallLocation="$fixture\package"}
    }
    function Get-ChildItem { param($LiteralPath, $ErrorAction) @() }
    $sid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    $found = @(Get-FiloClaudeDesktopApplications $sid $fixture)
    Assert ($found.Count -eq 2) 'Signed package and conventional per-user installation are discovered'
    Assert (@($found | Where-Object Source -ne 'claude-desktop').Count -eq 0) 'Discovery labels only the desktop source'
    $script:signatureSubject = 'CN="Anthropic, PBC", O="Anthropic, PBC", L=San Francisco, C=US'
    [IO.File]::WriteAllText("$fixture\package\app\claude-ssh-proxy.exe", 'fixture; never executed')
    [IO.File]::WriteAllText("$fixture\package\AppxManifest.xml", '<Package><Applications><Application Id="Claude" Executable="app/Claude.exe" /><Application Id="SshProxy" Executable="app/claude-ssh-proxy.exe" /></Applications></Package>')
    $found = @(Get-FiloClaudeDesktopApplications $sid $fixture)
    Assert ($found.Count -eq 2) 'Quoted production signer is accepted without discovering signed helper applications'
    Assert (@($found | Where-Object { [IO.Path]::GetFileName($_.Executable) -ine 'Claude.exe' }).Count -eq 0) 'Only the main Claude application is returned'
    $script:signatureSubject = 'CN="Anthropic, PBC Impersonator", O="Anthropic Tools", C=US'
    Assert (@(Get-FiloClaudeDesktopApplications $sid $fixture).Count -eq 0) 'Publisher names that merely start with Anthropic are rejected'
    $script:signatureSubject = 'CN="Anthropic, PBC", O="Anthropic, PBC", C=US'
    $script:signatureStatus = 'NotSigned'
    Assert (@(Get-FiloClaudeDesktopApplications $sid $fixture).Count -eq 0) 'Unsigned same-named executables are rejected'
    $script:signatureStatus = 'Valid'; $script:signatureSubject = 'CN=Another Publisher, O=Another Publisher'
    Assert (@(Get-FiloClaudeDesktopApplications $sid $fixture).Count -eq 0) 'Another publisher cannot claim native Claude'
    $script:signatureSubject = 'CN=Anthropic, PBC, O=Anthropic, PBC, C=US'
    [IO.File]::WriteAllText("$fixture\package\AppxManifest.xml", '<Package><Applications><Application Executable="../../outside.exe" /></Applications></Package>')
    Assert (@(Get-FiloClaudeDesktopApplications $sid $fixture).Count -eq 1) 'Manifest traversal cannot escape the package root'
    Assert ([IO.File]::ReadAllText("$fixture\AppData\Local\AnthropicClaude\claude.exe") -eq 'fixture; never executed') 'Discovery never launches or changes native files'
    'PASS: Claude Desktop identity, package boundaries, signed application discovery and no native mutations'
} finally {
    if ($fixture.StartsWith([IO.Path]::GetTempPath(), [StringComparison]::OrdinalIgnoreCase)) {
        Remove-Item -LiteralPath $fixture -Recurse -Force -ErrorAction SilentlyContinue
    }
}
