$ErrorActionPreference='Stop'
. (Join-Path $PSScriptRoot '..\windows\Install-Layout.ps1')
function Assert($Value,$Message) { if (-not $Value) { throw $Message } }
$script:machine='C:\Windows;C:\Program Files\Filo\bin;C:\WINDOWS;"c:\program files\filo\bin\";C:\Tools'
$script:broadcasts=0
$original=$env:Path
function Get-FiloMachinePath { $script:machine }
function Set-FiloMachinePath { param($Value) $script:machine=$Value }
function Send-FiloEnvironmentChange { $script:broadcasts++ }
try {
    Update-FiloMachinePath 'C:\Program Files\Filo'
    Assert ($script:machine -ceq 'C:\Windows;C:\Tools;C:\Program Files\Filo\bin') 'Machine PATH is normalized, de-duplicated, and includes one fixed entry'
    $first=$script:machine
    Update-FiloMachinePath 'C:\Program Files\Filo'
    Assert ($script:machine -ceq $first -and $script:broadcasts -eq 2) 'Repeated update is idempotent and broadcasts environment changes'
    Update-FiloMachinePath 'C:\Program Files\Filo' -Remove
    Assert ($script:machine -ceq 'C:\Windows;C:\Tools') 'Uninstall removes only the Filo entry'
    Assert ($env:Path -notlike '*C:\Program Files\Filo\bin*') 'Installer child environment follows removal'
    'PASS: machine PATH normalization, quoted/case duplicate removal, repeat installation and uninstall notification'
} finally { $env:Path=$original }
