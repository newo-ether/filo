$ErrorActionPreference = 'Stop'
$repo = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$fixture = Join-Path ([IO.Path]::GetTempPath()) ('filo-hidden-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $fixture | Out-Null
try {
    $tools=Join-Path $fixture 'tools'
    $scripts=Join-Path $fixture 'scripts\windows'
    New-Item -ItemType Directory -Path $tools,$scripts | Out-Null
    Copy-Item -LiteralPath (Join-Path $repo 'scripts\windows\Run-Standalone.ps1') -Destination $scripts
    $launcher=Join-Path $tools 'FiloBackground.exe'
    & (Join-Path $repo 'scripts\windows\Build-BackgroundLauncher.ps1') -OutputPath $launcher
    . (Join-Path $repo 'scripts\windows\Installer-Tasks.ps1')
    $action=New-FiloTaskAction $fixture $fixture Gateway $fixture
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
