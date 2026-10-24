$ErrorActionPreference='Stop'
. (Join-Path $PSScriptRoot '..\windows\Install-Layout.ps1')
function Assert($Value,$Message) { if (-not $Value) { throw $Message } }
$root='C:\ProgramData\Filo'; $program='C:\Program Files\Filo'; $profiles=@('C:\Users\fixture')
$directories=@($program,$root) + @(Get-FiloLegacyDirectories $root $profiles)
$events=[Collections.Generic.List[string]]::new(); $script:processes=@(); $script:tasks=@('Filo-Standalone-Host','Filo-User-Agent')
function Process($Id,$Executable,$Command) { [pscustomobject]@{ProcessId=$Id;Name=[IO.Path]::GetFileName($Executable);ExecutablePath=$Executable;CommandLine=$Command;CreationDate='original'} }
$shell=Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
$supervisor=Process 700 $shell '-NoProfile -File "C:\ProgramData\Filo\releases\old\scripts\windows\Run-Standalone.ps1" -Role Host'
$launcher=Process 701 'C:\Program Files\Filo\bin\FiloBackground.exe' '-Role Gateway'
$child=Process 702 'C:\ProgramData\Filo\releases\old\tools\filo.exe' 'standalone'
$native=Process 703 'C:\Users\fixture\AppData\Local\OpenAI\Codex\bin\codex.exe' 'app-server'
$repository=Process 704 $shell '-Command "Get-Content F:\workspace\repo\Filo\scripts\windows\Run-Standalone.ps1"'
$foreignScript=Process 705 $shell '-File "F:\workspace\repo\Filo\scripts\windows\Run-Standalone.ps1" -Role Host'
$prefixSibling=Process 706 'C:\Program Files\Filo-other\bin\filo.exe' 'standalone'
function Get-ScheduledTask { param($TaskName,$ErrorAction) if ($TaskName -in $script:tasks) { @{Name=$TaskName} } }
function Stop-ScheduledTask { param($TaskName) $events.Add('task-stop-'+$TaskName) }
function Remove-FiloUserTask { param($Name) $events.Add('task-remove-'+$Name); $script:tasks=@($script:tasks | Where-Object { $_ -ne $Name }) }
function Get-CimInstance {
    param($ClassName,$Filter)
    if ($ClassName -eq 'Win32_Service') { return }
    if ($Filter -match '^ProcessId=(\d+)$') { $script:processes | Where-Object ProcessId -eq ([int]$Matches[1]) }
    else { $script:processes }
}
function Stop-Process { param($Id,[switch]$Force,$ErrorAction) $events.Add('process-'+$Id); $script:processes=@($script:processes | Where-Object ProcessId -ne $Id) }
Assert (Test-FiloSupervisorProcess $supervisor $directories) 'Actual installed -File argument is recognized'
Assert (-not (Test-FiloSupervisorProcess $repository $directories)) 'A shell command mentioning repository paths is never selected'
Assert (-not (Test-FiloSupervisorProcess $foreignScript $directories)) 'Repository supervisors are outside installed directories'
$quoted=Process 707 $shell 'powershell.exe -Command "Write-Output ''-File C:\ProgramData\Filo\Run-Standalone.ps1''"'
Assert (-not (Test-FiloSupervisorProcess $quoted $directories)) 'Quoted command payloads never establish supervisor ownership'
$invalid=Process 708 $shell 'powershell.exe -File "C:\bad<path>\Run-Standalone.ps1"'
Assert (-not (Test-FiloSupervisorProcess $invalid $directories)) 'Malformed paths are ignored without aborting cleanup'
$spaced=Process 709 $shell 'powershell.exe -NoProfile -File "C:\Program Files\Filo\scripts\windows\Run-Standalone.ps1" -Role Host'
Assert (Test-FiloSupervisorProcess $spaced $directories) 'Windows-quoted fixed script path is recognized'
Assert (-not (Test-FiloOwnedProcess $native $directories)) 'Original native Codex is outside owned directories'
Assert (-not (Test-FiloOwnedProcess $prefixSibling $directories)) 'Directory matching uses a separator boundary'
$script:processes=@($supervisor,$launcher,$child,$native,$repository,$foreignScript,$prefixSibling)
Stop-FiloInstalledResources $root $program $profiles
Assert (($script:processes.ProcessId -join ',') -eq '703,704,705,706') 'Only installed supervisors and children are stopped'
Assert ($events.IndexOf('process-701') -lt $events.IndexOf('process-702') -and $events.IndexOf('process-700') -lt $events.IndexOf('process-702')) 'Restart sources retire before children'
Assert ($events.IndexOf('task-remove-Filo-User-Agent') -lt $events.IndexOf('process-701')) 'Task restart registrations retire first'
$before=$events.Count
Stop-FiloInstalledResources $root $program $profiles
Assert ($events.Count -eq $before) 'Repeated cleanup is idempotent'
$reused=Process 702 $child.ExecutablePath 'standalone'; $reused.CreationDate='reused'; $script:processes=@($reused)
$rejected=$false
try { Stop-FiloProcessIdentity $child } catch { $rejected=$_.Exception.Message -match 'identity changed' }
Assert ($rejected -and $events.Count -eq $before) 'Reused PID never receives a signal'
'PASS: exact installed ownership, supervisor-first shutdown, unrelated shell/native preservation, repeat cleanup and PID reuse'
