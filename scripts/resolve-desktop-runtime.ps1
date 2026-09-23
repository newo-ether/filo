param([string]$ExpectedUserSid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value)
$ErrorActionPreference='Stop'
if ($ExpectedUserSid -notmatch '^S-1-(5-21|12-1)-[0-9-]+$') { throw 'Select an ordinary desktop account' }
. (Join-Path $PSScriptRoot 'windows\Desktop-Runtime.ps1')
$runtime=Resolve-FiloDesktopRuntime -UserSid $ExpectedUserSid
[Console]::OutputEncoding=[Text.UTF8Encoding]::new($false)
[Console]::WriteLine(($runtime | ConvertTo-Json -Compress))
