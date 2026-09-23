param(
    [Parameter(Mandatory=$true)][ValidateSet('Host','Gateway')][string]$Role,
    [Parameter(Mandatory=$true)][string]$StateDirectory
)
$ErrorActionPreference = 'Stop'
$config = Get-Content -LiteralPath (Join-Path $StateDirectory 'launch.json') -Raw | ConvertFrom-Json
$logDirectory = Join-Path $StateDirectory 'logs'
New-Item -ItemType Directory -Path $logDirectory -Force | Out-Null
$stdout = Join-Path $logDirectory ($Role + '.stdout.log')
$stderr = Join-Path $logDirectory ($Role + '.stderr.log')
foreach ($log in @($stdout,$stderr)) {
    if (Test-Path -LiteralPath $log) { Copy-Item -LiteralPath $log -Destination ($log + '.previous') -Force }
}
# Supervise only this role's process; gateway recovery never touches the host.
for ($attempt = 0; $attempt -lt 20; $attempt++) {
    if ($Role -eq 'Gateway' -and (Test-Path -LiteralPath (Join-Path $StateDirectory 'gateway.stop'))) { exit 0 }
    $config = Get-Content -LiteralPath (Join-Path $StateDirectory 'launch.json') -Raw | ConvertFrom-Json
    try {
        if ($Role -eq 'Host') {
            . (Join-Path $PSScriptRoot 'Desktop-Runtime.ps1')
            $sid = if ($config.desktopSid) { $config.desktopSid } else { [Security.Principal.WindowsIdentity]::GetCurrent().User.Value }
            $runtime = Resolve-FiloDesktopRuntime $sid
            $executable = $runtime.Executable
            $endpoint = $config.appServerUrl
            if ($endpoint -notmatch '^ws://127\.0\.0\.1:([0-9]+)$' -or
                [int]$Matches[1] -lt 1024 -or [int]$Matches[1] -gt 65535) { throw 'Native helper endpoint must be loopback with a valid port' }
            $arguments = @('app-server','--listen',$endpoint)
        } else {
            $executable = Join-Path $config.runtimeDirectory 'tools\filo.exe'
            $arguments = @('standalone', ('"' + $StateDirectory + '"'))
        }
    } catch {
        [IO.File]::WriteAllText($stderr, $_.Exception.Message)
        Start-Sleep -Seconds ([Math]::Min(5 * ($attempt + 1), 30))
        continue
    }
    # Create no console at all; hiding a newly allocated console is not sufficient.
    $start = [Diagnostics.ProcessStartInfo]::new()
    $start.FileName = $executable
    $start.Arguments = $arguments -join ' '
    $start.WorkingDirectory = $config.workspaceDirectory
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    foreach ($key in @($start.EnvironmentVariables.Keys)) {
        if ($key -match '^CODEX_APP_SERVER_') { $start.EnvironmentVariables.Remove($key) }
    }
    if ($Role -eq 'Host') { $start.EnvironmentVariables['PATH'] = (Split-Path -Parent $executable) + ';' + $env:PATH }
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $start
    $outputFile = $null; $errorFile = $null
    try {
        $outputFile = [IO.File]::Open($stdout, 'Create', 'Write', 'Read')
        $errorFile = [IO.File]::Open($stderr, 'Create', 'Write', 'Read')
        if (-not $process.Start()) { throw 'Could not start Filo background process' }
        $outputCopy = $process.StandardOutput.BaseStream.CopyToAsync($outputFile)
        $errorCopy = $process.StandardError.BaseStream.CopyToAsync($errorFile)
        $process.WaitForExit()
        $outputCopy.GetAwaiter().GetResult()
        $errorCopy.GetAwaiter().GetResult()
        $exitCode = $process.ExitCode
    } catch {
        $exitCode = 1
        [IO.File]::WriteAllText($stderr + '.launch-error', $_.Exception.Message)
    } finally {
        if ($outputFile) { $outputFile.Dispose() }
        if ($errorFile) { $errorFile.Dispose() }
        $process.Dispose()
    }
    if ($exitCode -eq 0) { exit 0 }
    Copy-Item -LiteralPath $stderr -Destination ($stderr + '.previous') -Force
    Start-Sleep -Seconds ([Math]::Min(5 * ($attempt + 1), 60))
}
exit 1
