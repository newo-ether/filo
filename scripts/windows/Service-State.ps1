# NSSM reports Paused while backing off before an automatic child restart.
# Preserve that running intent; an explicit stop or stop in progress stays stopped.
function Test-FiloServiceShouldRun {
    param($Service)
    return $null -ne $Service -and $Service.State -in @(
        'Running', 'Paused', 'Start Pending', 'Continue Pending', 'Pause Pending'
    )
}

# Read the native pipes directly: Windows PowerShell wraps stderr in ErrorRecord,
# and GUI subsystem processes otherwise do not have consistent pipeline wait semantics.
function Test-FiloEntrypoint {
    param([string]$Path)
    $start=[Diagnostics.ProcessStartInfo]::new()
    $start.FileName=$Path
    $start.UseShellExecute=$false
    $start.CreateNoWindow=$true
    $start.RedirectStandardOutput=$true
    $start.RedirectStandardError=$true
    $process=[Diagnostics.Process]::new()
    $process.StartInfo=$start
    try {
        if (-not $process.Start()) { return $false }
        $output=$process.StandardOutput.ReadToEndAsync()
        $errors=$process.StandardError.ReadToEndAsync()
        if (-not $process.WaitForExit(10000)) {
            $process.Kill()
            return $false
        }
        return $process.ExitCode -eq 2 -and $errors.GetAwaiter().GetResult().StartsWith('Usage: filo <') -and $output.GetAwaiter().GetResult() -eq ''
    } catch { return $false } finally { $process.Dispose() }
}