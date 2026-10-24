function New-FiloTaskAction {
    param([string]$Runtime,[string]$State,[string]$Role,[string]$Workspace)
    $launcher=Join-Path $Runtime 'bin\FiloBackground.exe'
    if (-not (Test-Path -LiteralPath $launcher -PathType Leaf)) { throw 'Missing Filo background launcher' }
    $arguments='-Role ' + $Role + ' -StateDirectory "' + $State + '"'
    # This class is a value object. Avoid a provider round-trip while constructing it:
    # New-ScheduledTaskAction can wait indefinitely in the CIM adapter's completion.
    $action=New-CimInstance -Namespace 'Root/Microsoft/Windows/TaskScheduler' `
        -ClassName 'MSFT_TaskExecAction' -ClientOnly -Property @{
            Execute=$launcher; Arguments=$arguments; WorkingDirectory=$Workspace
        }
    $action.PSObject.TypeNames.Insert(0,'Microsoft.Management.Infrastructure.CimInstance#MSFT_TaskAction')
    $action
}

function New-FiloTaskOptions {
    param([string]$Sid)
    $namespace='Root/Microsoft/Windows/TaskScheduler'
    # Construct all value objects locally, before interrupting an installed service.
    $principal=New-CimInstance -Namespace $namespace -ClassName 'MSFT_TaskPrincipal' -ClientOnly -Property @{
        UserId=$Sid; LogonType=[int32]3; RunLevel=[int32]0
    }
    $trigger=New-CimInstance -Namespace $namespace -ClassName 'MSFT_TaskLogonTrigger' -ClientOnly -Property @{
        Enabled=$true; UserId=$Sid
    }
    $trigger.PSObject.TypeNames.Insert(0,'Microsoft.Management.Infrastructure.CimInstance#MSFT_TaskTrigger')
    $settings=New-CimInstance -Namespace $namespace -ClassName 'MSFT_TaskSettings3' -ClientOnly -Property @{
        Enabled=$true; AllowDemandStart=$true; AllowHardTerminate=$true; StartWhenAvailable=$true;
        DisallowStartIfOnBatteries=$false; StopIfGoingOnBatteries=$false;
        ExecutionTimeLimit='PT0S'; MultipleInstances=[int32]2; Priority=[uint32]7
    }
    $settings.PSObject.TypeNames.Insert(0,'Microsoft.Management.Infrastructure.CimInstance#MSFT_TaskSettings')
    [pscustomobject]@{Principal=$principal;Trigger=$trigger;Settings=$settings}
}

function ConvertTo-FiloTaskXml {
    param($Action,$Options)
    $e={ param($Value) [Security.SecurityElement]::Escape([string]$Value) }
    $b={ param($Value) if ($Value) { 'true' } else { 'false' } }
    $s=$Options.Settings
    # LogonType 3 = InteractiveToken, RunLevel 0 = LeastPrivilege, MultipleInstances 2 = IgnoreNew.
    if ($Options.Principal.LogonType -ne 3 -or $Options.Principal.RunLevel -ne 0 -or $s.MultipleInstances -ne 2) {
        throw 'Unsupported Filo task principal or instance policy'
    }
    @"
<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <Triggers><LogonTrigger><Enabled>$(& $b $Options.Trigger.Enabled)</Enabled><UserId>$(& $e $Options.Trigger.UserId)</UserId></LogonTrigger></Triggers>
  <Principals><Principal id="Author"><UserId>$(& $e $Options.Principal.UserId)</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>$(& $b $s.DisallowStartIfOnBatteries)</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>$(& $b $s.StopIfGoingOnBatteries)</StopIfGoingOnBatteries>
    <AllowHardTerminate>$(& $b $s.AllowHardTerminate)</AllowHardTerminate>
    <StartWhenAvailable>$(& $b $s.StartWhenAvailable)</StartWhenAvailable>
    <AllowStartOnDemand>$(& $b $s.AllowDemandStart)</AllowStartOnDemand>
    <Enabled>$(& $b $s.Enabled)</Enabled>
    <ExecutionTimeLimit>$(& $e $s.ExecutionTimeLimit)</ExecutionTimeLimit>
    <Priority>$([int]$s.Priority)</Priority>
  </Settings>
  <Actions Context="Author"><Exec><Command>$(& $e $Action.Execute)</Command><Arguments>$(& $e $Action.Arguments)</Arguments><WorkingDirectory>$(& $e $Action.WorkingDirectory)</WorkingDirectory></Exec></Actions>
</Task>
"@
}

function Invoke-FiloTaskSchedulerCommand {
    param([string]$Name,[string]$FilePath,[string]$Arguments,[ValidateRange(1,60)][int]$TimeoutSeconds=20)
    # One short-lived native process per scheduler mutation. It never loads the CIM
    # adapter that stalled in installer sessions, and a deadline can always kill it.
    $start=[Diagnostics.ProcessStartInfo]::new($FilePath,$Arguments)
    $start.WorkingDirectory=$env:SystemRoot
    $start.UseShellExecute=$false; $start.CreateNoWindow=$true
    $start.RedirectStandardOutput=$true; $start.RedirectStandardError=$true
    $process=[Diagnostics.Process]::new()
    $process.StartInfo=$start
    try {
        if (-not $process.Start()) { throw "Cannot start the task scheduler command for $Name" }
        $output=$process.StandardOutput.ReadToEndAsync()
        $errors=$process.StandardError.ReadToEndAsync()
        if (-not $process.WaitForExit($TimeoutSeconds*1000)) {
            $stopped=$false
            try {
                if (-not $process.HasExited) { $process.Kill() }
                $stopped=$process.WaitForExit(5000)
            } catch { $stopped=$process.HasExited }
            if (-not $stopped) {
                $failure=[InvalidOperationException]::new("Task scheduler command for $Name did not stop; registration state is uncertain")
                $failure.Data['FiloTaskRegistrationUncertain']=$true
                throw $failure
            }
            throw "Task registration for $Name timed out after $TimeoutSeconds seconds; registration may have changed and requires rollback"
        }
        $process.WaitForExit()
        $detail=($output.GetAwaiter().GetResult()+' '+$errors.GetAwaiter().GetResult()).Trim()
        if ($process.ExitCode -ne 0) { throw "Task registration for $Name failed (exit $($process.ExitCode)): $detail" }
    } finally { $process.Dispose() }
}

function Invoke-FiloSchtasks {
    param([string]$Name,[string[]]$Arguments,[string]$Xml,[int]$TimeoutSeconds=20)
    if ([string]::IsNullOrWhiteSpace($Name) -or $Name -match '["\\]') { throw 'Invalid Filo task name' }
    $file=$null
    try {
        $line=$Arguments[0]+' /TN "\'+$Name+'"'
        foreach ($argument in @($Arguments | Select-Object -Skip 1)) { $line+=' '+$argument }
        if ($Xml) {
            $file=Join-Path ([IO.Path]::GetTempPath()) ('filo-task-'+[guid]::NewGuid().ToString('N')+'.xml')
            # schtasks reads UTF-16 task XML; the declaration matches this encoding.
            [IO.File]::WriteAllText($file,$Xml,[Text.Encoding]::Unicode)
            $line+=' /XML "'+$file+'"'
        }
        Invoke-FiloTaskSchedulerCommand -Name $Name -FilePath (Join-Path $env:SystemRoot 'System32\schtasks.exe') `
            -Arguments $line -TimeoutSeconds $TimeoutSeconds
    } finally {
        if ($file -and (Test-Path -LiteralPath $file)) { Remove-Item -LiteralPath $file -Force }
    }
}

function Register-FiloUserTask {
    param([string]$Name,$Action,[string]$Sid,$Options)
    if (-not $Options) { $Options=New-FiloTaskOptions $Sid }
    Invoke-FiloSchtasks -Name $Name -Arguments @('/Create','/F') -Xml (ConvertTo-FiloTaskXml $Action $Options)
}

function Restore-FiloUserTask {
    param([string]$Name,[string]$Xml)
    if ([string]::IsNullOrWhiteSpace($Xml)) { throw "Task registration for $Name failed: no saved definition" }
    Invoke-FiloSchtasks -Name $Name -Arguments @('/Create','/F') -Xml $Xml
}

function Remove-FiloUserTask {
    param([string]$Name)
    Invoke-FiloSchtasks -Name $Name -Arguments @('/Delete','/F')
}

function Get-FiloInstallBind {
    param([string]$Requested,[string]$Existing)
    $address=if ($Requested) { $Requested } elseif ($Existing) { $Existing } else {
        $tailnet=@(Get-NetIPAddress -AddressFamily IPv4 -ErrorAction SilentlyContinue | Where-Object {
            $_.IPAddress -match '^100\.(6[4-9]|[7-9][0-9]|1[01][0-9]|12[0-7])\.' })
        if ($tailnet.Count -eq 1) { $tailnet[0].IPAddress } else { '127.0.0.1' }
    }
    $parsed=$null
    if (-not [Net.IPAddress]::TryParse($address,[ref]$parsed) -or
        $address -notmatch '^(127\.0\.0\.1|100\.(6[4-9]|[7-9][0-9]|1[01][0-9]|12[0-7])\.[0-9]{1,3}\.[0-9]{1,3})$') {
        throw 'Filo bind must be loopback or the device tailnet IPv4 address'
    }
    $address
}
