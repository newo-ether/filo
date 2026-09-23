$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot '..\windows\Client-Selection.ps1')
function Assert($Condition, $Message) { if (-not $Condition) { throw $Message } }
function Rejects([scriptblock]$Run) {
    $rejected = $false
    try { & $Run | Out-Null } catch { $rejected = $true }
    Assert $rejected 'Expected rejection'
}
$clients = @(
    [pscustomobject]@{Id='codex:user1';Supported=$true},
    [pscustomobject]@{Id='claude-code:user1';Supported=$false}
)
Assert (@(Resolve-FiloSelection $clients @()).Count -eq 0) 'Empty selection must cancel'
Assert (@(Resolve-FiloSelection $clients @('codex')).Count -eq 1) 'Supported alias'
Assert (@(Resolve-FiloSelection $clients @('codex:user1'))[0].Id -eq 'codex:user1') 'Exact selection'
Rejects { Resolve-FiloSelection $clients @('missing') }
Rejects { Resolve-FiloSelection $clients @('claude-code') }
$ambiguous = $clients + [pscustomobject]@{Id='codex:user2';Supported=$true}
Rejects { Resolve-FiloSelection $ambiguous @('codex') }
Assert (@(Resolve-FiloSelection $ambiguous @('codex:user2'))[0].Id -eq 'codex:user2') 'Account disambiguation'
# Discovery uses a disposable profile and read-only operating-system boundaries.
$fixture = Join-Path ([IO.Path]::GetTempPath()) ('filo-discovery-' + [guid]::NewGuid().ToString('N'))
try {
    New-Item -ItemType Directory -Path "$fixture\cli\AppData\Local\OpenAI\Codex\bin\one","$fixture\app\resources","$fixture\cli\.claude" -Force | Out-Null
    [IO.File]::WriteAllText("$fixture\cli\AppData\Local\OpenAI\Codex\bin\one\codex.exe", 'not executed')
    [IO.File]::WriteAllText("$fixture\app\resources\app.asar", 'not executed')
    [IO.File]::WriteAllText("$fixture\app\ChatGPT.exe", 'not executed')
    [IO.File]::WriteAllText("$fixture\AppxManifest.xml", '<Package><Applications><Application Executable="app/ChatGPT.exe" /></Applications></Package>')
    $currentSid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    # Tests run both elevated and as an ordinary desktop user.
    $fixtureSid = if ($currentSid -match '^S-1-(5-21|12-1)-') { $currentSid } else { 'S-1-5-21-1-2-3-1001' }
    function Get-CimInstance {
        [pscustomobject]@{Loaded=$true;Special=$false;SID=$fixtureSid;LocalPath="$fixture\cli"}
        [pscustomobject]@{Loaded=$true;Special=$false;SID='S-1-5-80-1';LocalPath="$fixture\service"}
        [pscustomobject]@{Loaded=$false;Special=$false;SID='S-1-5-21-1-2-3-1002';LocalPath="$fixture\absent"}
    }
    function Get-AppxPackage {
        param([string]$User, [string]$Name, [string]$ErrorAction)
        Assert ($Name -eq 'OpenAI.Codex') 'Discover the original application'
        Assert (-not $User -or $User -eq $fixtureSid) 'Query only the selected account'
        [pscustomobject]@{Version=$fixtureVersion;InstallLocation=$fixture;PublisherId=$fixturePublisher}
    }
    $fixtureVersion='100.2.3.4'; $fixturePublisher='2p2nqsd0c76g0'
    function Get-FileHash { throw 'Discovery must not pin native content hashes' }
    $found = @(Get-FiloClients)
    Assert ($found.Count -eq 2) 'Ignore unloaded and service accounts'
    Assert $found[0].Supported 'Future Codex versions are selectable'
    Assert (-not $found[1].Supported) 'Detection alone must not claim Claude support'
    $fixtureVersion='1.0.0.0'
    Assert (@(Get-FiloClients)[0].Supported) 'Versions are not admission criteria'
    [IO.File]::WriteAllText("$fixture\app\resources\app.asar", 'updated application content')
    Assert (@(Get-FiloClients)[0].Supported) 'Updating native content does not block discovery'
    $fixturePublisher='different-publisher'
    Assert (-not (@(Get-FiloClients)[0].Supported)) 'Wrong application identity is rejected'
    Assert ([IO.File]::ReadAllText("$fixture\cli\AppData\Local\OpenAI\Codex\bin\one\codex.exe") -eq 'not executed') 'Discovery preserves native files'
    $fixturePublisher='2p2nqsd0c76g0'
    Remove-Item -LiteralPath "$fixture\cli\AppData\Local\OpenAI\Codex\bin\one\codex.exe"
    Assert (@(Get-FiloClients)[0].Supported) 'Desktop-only installation does not require a prepared CLI cache'
} finally {
    $resolved = [IO.Path]::GetFullPath($fixture)
    if (-not $resolved.StartsWith([IO.Path]::GetFullPath([IO.Path]::GetTempPath()), [StringComparison]::OrdinalIgnoreCase)) { throw 'Unsafe fixture cleanup' }
    Remove-Item -LiteralPath $resolved -Recurse -Force
}
Write-Output 'PASS: 11 client discovery and selection checks'
