<#
.SYNOPSIS
Run the PowerShell installer and lifecycle tests without Node.js.
.DESCRIPTION
Each fixture is self-contained: it stubs the Windows cmdlets it needs and dot-sources
the real delivery script under test. Fixtures run in a fresh non-interactive Windows
PowerShell host with the inherited module path removed, so they see the same system
cmdlets an installed Filo sees.
.PARAMETER Only
Run only these fixtures, given by their base name without the .test.ps1 suffix.
.PARAMETER TimeoutSeconds
Per-fixture wall-clock limit.
#>
param(
    [string[]]$Only,
    [int]$TimeoutSeconds = 60
)
$ErrorActionPreference='Stop'
# Two fixtures are opt-in and never part of the default sweep: desktop-runtime needs an
# interactive desktop account, and installer-lifecycle registers a temporary real service,
# so it needs -FiloPath plus a service manager binary.
$names = @('client-selection','desktop-identity','hidden-runner','host-lifecycle',
    'installer-entry','installer-files','installer-task-stop','release-bootstrap',
    'installer-transaction','service-state','uninstall-transaction','worker-update')
if ($Only) { $names = @($names | Where-Object { $Only -contains $_ }) }
if (-not $names.Count) { throw 'No matching Filo installer fixture' }
$powershell = Join-Path $env:SystemRoot 'System32\WindowsPowerShell\v1.0\powershell.exe'
$failures = [Collections.Generic.List[string]]::new()
foreach ($name in $names) {
    $path = Join-Path $PSScriptRoot ('tests\' + $name + '.test.ps1')
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        $failures.Add($name + ': missing fixture')
        Write-Host ('FAIL  ' + $name) -ForegroundColor Red
        continue
    }
    $start = [Diagnostics.ProcessStartInfo]::new()
    $start.FileName = $powershell
    $start.Arguments = '-NoProfile -NonInteractive -File "' + $path + '"'
    $start.WorkingDirectory = $PSScriptRoot
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    foreach ($key in @($start.EnvironmentVariables.Keys)) {
        if ($key -ieq 'PSModulePath') { $start.EnvironmentVariables.Remove($key) }
    }
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $start
    $code = $null; $text = ''; $errors = ''
    try {
        if (-not $process.Start()) { throw 'Cannot start the test host for ' + $name }
        $stdout = $process.StandardOutput.ReadToEndAsync()
        $stderr = $process.StandardError.ReadToEndAsync()
        if (-not $process.WaitForExit($TimeoutSeconds * 1000)) {
            $process.Kill()
            $failures.Add($name + ': timed out after ' + $TimeoutSeconds + ' seconds')
            Write-Host ('FAIL  ' + $name) -ForegroundColor Red
            continue
        }
        $code = $process.ExitCode
        $text = $stdout.GetAwaiter().GetResult()
        $errors = $stderr.GetAwaiter().GetResult()
    } finally { $process.Dispose() }
    if ($code -ne 0) {
        $failures.Add($name + ': exit ' + $code + [Environment]::NewLine + $text + $errors)
        Write-Host ('FAIL  ' + $name) -ForegroundColor Red
    } else {
        Write-Host ('ok    ' + $name)
    }
}
if ($failures.Count) {
    Write-Host ''
    foreach ($failure in $failures) { Write-Host $failure -ForegroundColor Red }
    throw ('Filo installer tests failed: ' + $failures.Count + ' of ' + $names.Count)
}
Write-Host ('Filo installer tests passed: ' + $names.Count)
