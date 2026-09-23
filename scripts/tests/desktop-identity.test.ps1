$ErrorActionPreference='Stop'
. (Join-Path $PSScriptRoot '..\windows\Desktop-Identity.ps1')
function Rejects([scriptblock]$Run) {
    $rejected=$false
    try { & $Run | Out-Null } catch { $rejected=$true }
    if (-not $rejected) { throw 'Expected identity rejection' }
}
$sid='S-1-5-21-1-2-3-1001'
$owner=[pscustomobject]@{Sid=$sid;ReturnValue=0}
foreach ($version in @('1.0.0.0','100.999.12345.7')) {
    $exe="C:\Apps\Codex_$version\new-name.exe"
    $apps=@([pscustomobject]@{Executable=$exe;Version=$version})
    $peer=[pscustomobject]@{ExecutablePath=$exe;SessionId=1}
    Assert-FiloDesktopPeer $peer $owner $sid $apps
    Rejects { Assert-FiloDesktopPeer $peer $owner 'S-1-5-21-1-2-3-1002' $apps }
    Rejects { Assert-FiloDesktopPeer $peer $owner $sid @() }
    $peer.SessionId=0
    Rejects { Assert-FiloDesktopPeer $peer $owner $sid $apps }
    $peer.SessionId=1; $peer.ExecutablePath='C:\Fake\ChatGPT.exe'
    Rejects { Assert-FiloDesktopPeer $peer $owner $sid $apps }
}
Write-Output 'PASS: application/account/session identity across arbitrary versions and paths'
