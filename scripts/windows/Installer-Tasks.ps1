function New-FiloTaskAction {
    param([string]$Runtime,[string]$State,[string]$Role,[string]$Workspace)
    $launcher=Join-Path $Runtime 'tools\FiloBackground.exe'
    if (-not (Test-Path -LiteralPath $launcher -PathType Leaf)) { throw 'Missing Filo background launcher' }
    $arguments='-Role ' + $Role + ' -StateDirectory "' + $State + '"'
    New-ScheduledTaskAction -Execute $launcher -Argument $arguments -WorkingDirectory $Workspace
}

function Register-FiloUserTask {
    param([string]$Name,$Action,[string]$Sid)
    $principal=New-ScheduledTaskPrincipal -UserId $Sid -LogonType Interactive -RunLevel Limited
    $trigger=New-ScheduledTaskTrigger -AtLogOn -User $Sid
    $settings=New-ScheduledTaskSettingsSet -StartWhenAvailable -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
        -ExecutionTimeLimit ([TimeSpan]::Zero) -MultipleInstances IgnoreNew
    Register-ScheduledTask -TaskName $Name -Action $Action -Principal $principal -Trigger $trigger -Settings $settings -Force | Out-Null
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
