$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$fixture = Join-Path ([IO.Path]::GetTempPath()) ('filo-hidden-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $fixture | Out-Null
try {
    $tools=Join-Path $fixture 'bin'
    $scripts=Join-Path $fixture 'scripts\windows'
    New-Item -ItemType Directory -Path $tools,$scripts | Out-Null
    Copy-Item -LiteralPath (Join-Path $repo 'scripts\windows\Run-Standalone.ps1') -Destination $scripts
    $launcher=Join-Path $tools 'FiloBackground.exe'
    & (Join-Path $repo 'scripts\windows\Build-BackgroundLauncher.ps1') -OutputPath $launcher
    . (Join-Path $repo 'scripts\windows\Installer-Tasks.ps1')
    $action=New-FiloTaskAction $fixture $fixture Gateway $fixture
    if ($action.CimClass.CimClassName -cne 'MSFT_TaskExecAction' -or
        $action.Arguments -cne ('-Role Gateway -StateDirectory "' + $fixture + '"') -or
        $action.WorkingDirectory -cne $fixture) { throw 'Invalid scheduled-task action' }
    $sid=[Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    $options=New-FiloTaskOptions $sid
    $task=New-ScheduledTask -Action $action -Principal $options.Principal -Trigger $options.Trigger -Settings $options.Settings
    if ($task.Principal.UserId -cne $sid -or $task.Principal.LogonType -ne 3 -or $task.Principal.RunLevel -ne 0 -or
        $task.Triggers[0].UserId -cne $sid -or -not $task.Triggers[0].Enabled -or
        $task.Settings.ExecutionTimeLimit -cne 'PT0S' -or $task.Settings.MultipleInstances -ne 2 -or
        $task.Settings.DisallowStartIfOnBatteries -or $task.Settings.StopIfGoingOnBatteries -or
        -not $task.Settings.StartWhenAvailable -or -not $task.Settings.Enabled -or -not $task.Settings.AllowDemandStart) {
        throw 'Client-only task options changed the installed task contract'
    }
    if (@($task.Actions).Count -ne 1 -or $task.Actions[0].Execute -cne $launcher) {
        throw 'Client-only action is not accepted as a concrete task action'
    }
    # Parenthesize: the comma operator binds tighter than string concatenation.
    $taskNames=@(('Filo-Fixture-'+[guid]::NewGuid().ToString('N')),('Filo-Fixture-'+[guid]::NewGuid().ToString('N')))
    $scheduler=New-Object -ComObject 'Schedule.Service'
    $scheduler.Connect()
    $folder=$scheduler.GetFolder('\')
    function Get-FixtureSid([string]$Identity) {
        if ($Identity -like 'S-1-*') { return $Identity }
        ([Security.Principal.NTAccount]::new($Identity)).Translate([Security.Principal.SecurityIdentifier]).Value
    }
    function Assert-FixtureTask($Registered,$Expected) {
        $definition=$Registered.Definition
        if ((Get-FixtureSid $definition.Principal.UserId) -cne $sid -or
            $definition.Principal.LogonType -ne 3 -or $definition.Principal.RunLevel -ne 0 -or
            $definition.Triggers.Count -ne 1 -or $definition.Triggers.Item(1).Type -ne 9 -or
            (Get-FixtureSid $definition.Triggers.Item(1).UserId) -cne $sid -or -not $definition.Triggers.Item(1).Enabled -or
            $definition.Settings.ExecutionTimeLimit -cne 'PT0S' -or $definition.Settings.MultipleInstances -ne 2 -or
            $definition.Settings.DisallowStartIfOnBatteries -or $definition.Settings.StopIfGoingOnBatteries -or
            -not $definition.Settings.StartWhenAvailable -or -not $definition.Settings.Enabled -or
            -not $definition.Settings.AllowDemandStart -or -not $definition.Settings.AllowHardTerminate -or
            $definition.Settings.Priority -ne 7 -or $definition.Actions.Count -ne 1 -or
            $definition.Actions.Item(1).Path -cne $Expected.Execute -or
            $definition.Actions.Item(1).Arguments -cne $Expected.Arguments -or
            $definition.Actions.Item(1).WorkingDirectory -cne $Expected.WorkingDirectory -or
            $Registered.State -ne 3) { throw 'Real registered task changed its identity, settings, action or Ready state' }
    }
    try {
        Register-FiloUserTask $taskNames[0] $action $sid $options
        Register-FiloUserTask $taskNames[1] $action $sid $options
        $originalXml=$folder.GetTask($taskNames[0]).Xml
        $unrelatedXml=$folder.GetTask($taskNames[1]).Xml
        $hostAction=New-FiloTaskAction $fixture $fixture Host $fixture
        foreach ($replacement in @($hostAction,$action)) {
            Register-FiloUserTask $taskNames[0] $replacement $sid $options
            Assert-FixtureTask ($folder.GetTask($taskNames[0])) $replacement
            if ($folder.GetTask($taskNames[1]).Xml -cne $unrelatedXml) { throw 'Overwrite changed an unrelated task' }
        }
        Register-FiloUserTask $taskNames[0] $hostAction $sid $options
        $rejected=$false
        try { Restore-FiloUserTask $taskNames[0] '<invalid-task />' }
        catch { $rejected=$_.Exception.Message -match 'registration.*failed' }
        if (-not $rejected) { throw 'Invalid task XML was not rejected' }
        Assert-FixtureTask ($folder.GetTask($taskNames[0])) $hostAction
        Restore-FiloUserTask $taskNames[0] $originalXml
        Assert-FixtureTask ($folder.GetTask($taskNames[0])) $action
        if ($folder.GetTask($taskNames[0]).Xml -cne $originalXml -or
            $folder.GetTask($taskNames[1]).Xml -cne $unrelatedXml) { throw 'Rollback did not preserve exact task XML' }
        # A helper can report failure after writing. Never retry the mutation:
        # surface failure, inspect the scheduler, and restore the saved registration.
        $effect={
            $scheduler=New-Object -ComObject 'Schedule.Service'
            $scheduler.Connect()
            $folder=$scheduler.GetFolder('\')
            $definition=$folder.GetTask($taskName).Definition
            $definition.Actions.Item(1).Arguments='injected-after-effect'
            [void]$folder.RegisterTaskDefinition($taskName,$definition,6,$definition.Principal.UserId,$null,3,$null)
            [Console]::Error.WriteLine('Injected failure after scheduler mutation')
            exit 1
        }
        $effectCommand="`$taskName='$($taskNames[0])'`r`n"+$effect.ToString()
        $powershell=Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
        $encoded={ param($Command) '-NoProfile -NonInteractive -EncodedCommand '+[Convert]::ToBase64String([Text.Encoding]::Unicode.GetBytes($Command)) }
        $rejected=$false
        try { Invoke-FiloTaskSchedulerCommand $taskNames[0] $powershell (& $encoded $effectCommand) }
        catch { $rejected=$_.Exception.Message -match 'Injected failure after scheduler mutation' }
        if (-not $rejected -or $folder.GetTask($taskNames[0]).Definition.Actions.Item(1).Arguments -cne 'injected-after-effect') {
            throw 'Failure after registration was hidden or replayed'
        }
        Restore-FiloUserTask $taskNames[0] $originalXml
        $timer=[Diagnostics.Stopwatch]::StartNew()
        $rejected=$false
        try { Invoke-FiloTaskSchedulerCommand $taskNames[0] $powershell (& $encoded 'Start-Sleep -Seconds 30') -TimeoutSeconds 1 }
        catch { $rejected=$_.Exception.Message -match 'timed out after 1 seconds' }
        if (-not $rejected -or $timer.Elapsed.TotalSeconds -gt 8) { throw 'Task scheduler helper has no effective deadline' }
        Assert-FixtureTask ($folder.GetTask($taskNames[0])) $action
        Remove-FiloUserTask $taskNames[1]
        $removed=$false
        try { [void]$folder.GetTask($taskNames[1]) } catch { $removed=$true }
        if (-not $removed -or -not $folder.GetTask($taskNames[0]).Xml) { throw 'Bounded removal missed its task or touched another task' }
        'PASS: real task creation, repeated overwrite, exact XML rollback, unrelated task preservation, after-effect failure and bounded helper timeout'
    } finally {
        foreach ($taskName in $taskNames) {
            try { [void]$folder.GetTask($taskName) } catch { continue }
            $folder.DeleteTask($taskName,0)
        }
    }
    $binary=[IO.File]::ReadAllBytes($action.Execute)
    $pe=[BitConverter]::ToInt32($binary,60)
    if ([BitConverter]::ToInt16($binary,$pe+92) -ne 2) { throw 'Scheduled-task entry is not a GUI executable' }
    $child = Join-Path $tools 'filo.exe'
    Push-Location $repo
    try {
        & (Get-Command go -ErrorAction Stop).Source build -o $child ./scripts/tests/fixtures/console-probe
        if ($LASTEXITCODE -ne 0) { throw 'Console fixture build failed' }
    } finally { Pop-Location }
    @{ runtimeDirectory=$fixture; workspaceDirectory=$fixture } |
        ConvertTo-Json | Set-Content -LiteralPath (Join-Path $fixture 'launch.json') -Encoding UTF8
    # Deliberately do not hide the process: the actual scheduler entry must create no console.
    $start=[Diagnostics.ProcessStartInfo]::new($action.Execute,$action.Arguments)
    $start.UseShellExecute=$false
    $process=[Diagnostics.Process]::Start($start)
    if (-not $process.WaitForExit(8000)) { $process.Kill(); throw 'Background runner did not exit' }
    $code=$process.ExitCode
    $process.Dispose()
    if ($code -ne 0) { throw ('Background runner failed: '+(Get-Content (Join-Path $fixture 'logs\Gateway.launcher.log') -Raw)) }
    if ((Get-Content (Join-Path $fixture 'logs\Gateway.launcher.log') -Raw) -notmatch '^console=0;') { throw 'Outer launcher allocated a console' }
    if ([IO.File]::ReadAllText((Join-Path $fixture 'console.txt')) -ne '0') { throw 'Background child allocated a console window' }
    if ((Get-Content (Join-Path $fixture 'logs\Gateway.stdout.log') -Raw).Trim() -ne 'stdout drained') { throw 'Lost stdout' }
    if ((Get-Content (Join-Path $fixture 'logs\Gateway.stderr.log') -Raw).Trim() -ne 'stderr drained') { throw 'Lost stderr' }
    if (@(Get-CimInstance Win32_Process | Where-Object ExecutablePath -eq $child).Count) { throw 'Background child did not exit' }
    [IO.File]::WriteAllText((Join-Path $scripts 'Run-Standalone.ps1'),'param($Role,$StateDirectory); exit 7')
    $process=[Diagnostics.Process]::Start($start)
    if (-not $process.WaitForExit(8000)) { $process.Kill(); throw 'Exit-code fixture did not settle' }
    $code=$process.ExitCode
    $process.Dispose()
    if ($code -ne 7) { throw ("Launcher lost script exit code: $code; "+(Get-Content (Join-Path $fixture 'logs\Gateway.launcher.log') -Raw)) }
    'PASS: actual task entry and child have no console, streams drained, runner and child exited, failure code preserved'
} finally {
    $resolved = [IO.Path]::GetFullPath($fixture)
    if (-not $resolved.StartsWith([IO.Path]::GetFullPath([IO.Path]::GetTempPath()), [StringComparison]::OrdinalIgnoreCase)) { throw 'Unsafe fixture cleanup' }
    Remove-Item -LiteralPath $resolved -Recurse -Force
}
