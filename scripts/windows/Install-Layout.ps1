. (Join-Path $PSScriptRoot 'Install-Support.ps1')

function Get-FiloProgramRoot {
    return Join-Path $env:ProgramFiles 'Filo'
}
function Test-FiloPathInside {
    param([string]$Path,[string]$Directory)
    if (-not $Path -or -not [IO.Path]::IsPathRooted($Path)) { return $false }
    $prefix=[IO.Path]::GetFullPath($Directory).TrimEnd('\') + '\'
    return [IO.Path]::GetFullPath($Path).StartsWith($prefix,[StringComparison]::OrdinalIgnoreCase)
}
function Get-FiloLegacyDirectories {
    param([string]$Root,[string[]]$Profiles)
    foreach ($name in @('releases','candidate','backups','packages')) { Join-Path $Root $name }
    if (Test-Path -LiteralPath $Root) {
        Get-ChildItem -LiteralPath $Root -Directory -Force | Where-Object Name -Like 'qualification-*' | ForEach-Object FullName
    }
    foreach ($profile in $Profiles) {
        Join-Path $profile 'AppData\Local\Filo'
        Join-Path $profile 'AppData\Local\FiloStandalone'
    }
}
function Test-FiloOwnedProcess {
    param($Process,[string[]]$Directories)
    # Match executable location only. Repository paths and command-line mentions
    # never establish ownership of a process.
    foreach ($directory in $Directories) {
        if (Test-FiloPathInside ([string]$Process.ExecutablePath) $directory) { return $true }
    }
    return $false
}
function Get-FiloProcessArguments {
    param([string]$CommandLine)
    if (-not ('Filo.ProcessArguments' -as [type])) {
        Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
namespace Filo {
    public static class ProcessArguments {
        [DllImport("shell32.dll", CharSet=CharSet.Unicode, SetLastError=true)]
        private static extern IntPtr CommandLineToArgvW(string commandLine, out int count);
        [DllImport("kernel32.dll")]
        private static extern IntPtr LocalFree(IntPtr memory);
        public static string[] Parse(string commandLine) {
            int count;
            IntPtr memory = CommandLineToArgvW(commandLine, out count);
            if (memory == IntPtr.Zero) throw new InvalidOperationException("Cannot parse process arguments");
            try {
                string[] result = new string[count];
                for (int i = 0; i < count; i++)
                    result[i] = Marshal.PtrToStringUni(Marshal.ReadIntPtr(memory, i * IntPtr.Size));
                return result;
            } finally { LocalFree(memory); }
        }
    }
}
'@
    }
    if (-not $CommandLine) { return @() }
    return [Filo.ProcessArguments]::Parse($CommandLine)
}
function Test-FiloSupervisorProcess {
    param($Process,[string[]]$Directories)
    $shell=Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
    if ([string]$Process.ExecutablePath -ine $shell) { return $false }
    # Windows escaping must be decoded before inspecting actual shell arguments.
    # Stop at command payloads; text inside -Command never identifies ownership.
    $arguments=@(Get-FiloProcessArguments ([string]$Process.CommandLine))
    $script=$null
    for ($index=0; $index -lt $arguments.Count; $index++) {
        if ($arguments[$index] -in @('-Command','-EncodedCommand','-c','-ec')) { return $false }
        if ($arguments[$index] -ieq '-File') {
            if ($index+1 -ge $arguments.Count) { return $false }
            $script=$arguments[$index+1]
            break
        }
    }
    if (-not $script) { return $false }
    try {
        if ([IO.Path]::GetFileName($script) -ine 'Run-Standalone.ps1') { return $false }
        foreach ($directory in $Directories) {
            if (Test-FiloPathInside $script $directory) { return $true }
        }
    } catch [ArgumentException] { return $false }
    return $false
}
function Stop-FiloProcessIdentity {
    param($Process)
    if ([int]$Process.ProcessId -eq $PID) { throw 'Refusing to stop the installer process' }
    $current=Get-CimInstance Win32_Process -Filter "ProcessId=$($Process.ProcessId)"
    if (-not $current) { return }
    if ($current.CreationDate -ne $Process.CreationDate -or
        $current.ExecutablePath -ine $Process.ExecutablePath -or $current.CommandLine -cne $Process.CommandLine) {
        throw "Filo process identity changed before stop: $($Process.ProcessId)"
    }
    Stop-Process -Id $current.ProcessId -Force -ErrorAction Stop
}
function Stop-FiloInstalledResources {
    param([string]$Root,[string]$ProgramRoot,[string[]]$Profiles)
    $directories=@($ProgramRoot,$Root) + @(Get-FiloLegacyDirectories $Root $Profiles)
    # Stop restart sources first. Names are exact Filo-owned registrations.
    foreach ($name in @('Filo-Standalone-Host','Filo-User-Agent')) {
        if (Get-ScheduledTask -TaskName $name -ErrorAction SilentlyContinue) {
            Stop-ScheduledTask -TaskName $name
            Remove-FiloUserTask $name
        }
    }
    $service=Get-CimInstance Win32_Service -Filter "Name='Filo'"
    if ($service) {
        $executable=([string]$service.PathName).Trim('"')
        if (-not (Test-FiloOwnedProcess ([pscustomobject]@{ExecutablePath=$executable}) $directories)) {
            throw 'Filo service executable is outside the installation directories'
        }
        if ($service.State -ne 'Stopped') {
            Stop-Service -Name Filo -NoWait
            (Get-Service -Name Filo).WaitForStatus('Stopped',[TimeSpan]::FromSeconds(30))
        }
        & sc.exe delete Filo | Out-Null
        if ($LASTEXITCODE -ne 0) { throw 'Cannot remove the previous Filo service registration' }
    }
    # Detached supervisors can survive Task Scheduler. Retire them before children
    # so no process can respawn while files are being replaced. Never tree-kill an
    # unrelated native executable outside the owned installation directories.
    $deadline=[DateTime]::UtcNow.AddSeconds(30)
    do {
        $processes=@(Get-CimInstance Win32_Process)
        $supervisors=@($processes | Where-Object { Test-FiloSupervisorProcess $_ $directories })
        $launchers=@($processes | Where-Object {
            $_.Name -ieq 'FiloBackground.exe' -and (Test-FiloOwnedProcess $_ $directories)
        })
        foreach ($process in @($launchers) + @($supervisors)) { Stop-FiloProcessIdentity $process }
        $children=@($processes | Where-Object {
            (Test-FiloOwnedProcess $_ $directories) -and $_.Name -ine 'FiloBackground.exe'
        })
        foreach ($process in $children) { Stop-FiloProcessIdentity $process }
        $remaining=@(Get-CimInstance Win32_Process | Where-Object {
            (Test-FiloOwnedProcess $_ $directories) -or (Test-FiloSupervisorProcess $_ $directories)
        })
        if (-not $remaining.Count) { return }
        Start-Sleep -Milliseconds 150
    } while ([DateTime]::UtcNow -lt $deadline)
    throw 'Filo processes did not stop; installation files were not replaced'
}
function Remove-FiloFirewallRules {
    foreach ($rule in @(Get-NetFirewallRule -ErrorAction Stop | Where-Object {
        $_.DisplayName -in @('Filo Remote','Filo standalone on quantum tailnet only','Filo Remote Gateway') -or
        $_.Group -eq 'Filo' -or $_.Name -eq 'Filo-Remote'
    })) { Remove-NetFirewallRule -Name $rule.Name }
}
function Remove-FiloLegacyDirectories {
    param([string]$Root,[string[]]$Profiles)
    foreach ($directory in @(Get-FiloLegacyDirectories $Root $Profiles)) {
        if (Test-Path -LiteralPath $directory) {
            $parent=Split-Path -Parent $directory
            $safe=Assert-FiloPrivatePath $directory $parent
            Remove-Item -LiteralPath $safe -Recurse -Force
        }
    }
    foreach ($name in @('nssm.exe','service.json')) {
        $path=Join-Path $Root $name
        if (Test-Path -LiteralPath $path) { Remove-Item -LiteralPath $path -Force }
    }
}
function Get-FiloMachinePath {
    return [Environment]::GetEnvironmentVariable('Path','Machine')
}
function Set-FiloMachinePath {
    param([string]$Value)
    [Environment]::SetEnvironmentVariable('Path',$Value,'Machine')
}
function Send-FiloEnvironmentChange {
    if (-not ('Filo.EnvironmentBroadcast' -as [type])) {
        Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
namespace Filo {
    public static class EnvironmentBroadcast {
        [DllImport("user32.dll", CharSet=CharSet.Unicode, SetLastError=true)]
        public static extern IntPtr SendMessageTimeout(IntPtr window, uint message,
            UIntPtr wParam, string lParam, uint flags, uint timeout, out UIntPtr result);
    }
}
'@
    }
    $result=[UIntPtr]::Zero
    $sent=[Filo.EnvironmentBroadcast]::SendMessageTimeout([IntPtr]0xffff,0x1a,
        [UIntPtr]::Zero,'Environment',2,5000,[ref]$result)
    if ($sent -eq [IntPtr]::Zero) { Write-Warning 'PATH saved; environment notification timed out' }
}
function Update-FiloMachinePath {
    param([string]$ProgramRoot,[switch]$Remove)
    $bin=Join-Path $ProgramRoot 'bin'
    $values=[Collections.Generic.List[string]]::new()
    $seen=[Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
    foreach ($entry in ([string](Get-FiloMachinePath)).Split(';')) {
        $value=$entry.Trim()
        if (-not $value) { continue }
        $normalized=[Environment]::ExpandEnvironmentVariables($value.Trim('"')).TrimEnd('\')
        if ($normalized -ieq $bin.TrimEnd('\')) { continue }
        if ($seen.Add($normalized)) { $values.Add($value) }
    }
    if (-not $Remove) { $values.Add($bin) }
    $updated=$values -join ';'
    if ($updated -cne (Get-FiloMachinePath)) { Set-FiloMachinePath $updated }
    Send-FiloEnvironmentChange
    # Child processes from this installer see the same new entry immediately.
    $processEntries=@($env:Path.Split(';') | Where-Object { $_.Trim().Trim('"').TrimEnd('\') -ine $bin.TrimEnd('\') })
    $env:Path=(@($processEntries) + @($bin | Where-Object { -not $Remove })) -join ';'
}
