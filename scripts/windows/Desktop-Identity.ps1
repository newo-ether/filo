# Windows registration and executable identity are authoritative; product versions are metadata only.
function Get-FiloDesktopApplications {
    param([Parameter(Mandatory=$true)][string]$UserSid)
    $query = @{Name='OpenAI.Codex';ErrorAction='Stop'}
    if ($UserSid -ne [Security.Principal.WindowsIdentity]::GetCurrent().User.Value) { $query.User = $UserSid }
    foreach ($package in @(Get-AppxPackage @query)) {
        if ($package.PublisherId -ne '2p2nqsd0c76g0' -or -not $package.InstallLocation) { continue }
        $root = [IO.Path]::GetFullPath($package.InstallLocation).TrimEnd('\') + '\'
        $manifestPath = Join-Path $root 'AppxManifest.xml'
        if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) { continue }
        [xml]$manifest = [IO.File]::ReadAllText($manifestPath)
        foreach ($application in $manifest.Package.Applications.Application) {
            if (-not $application.Executable) { continue }
            $executable = [IO.Path]::GetFullPath((Join-Path $root $application.Executable))
            if ($executable.StartsWith($root, [StringComparison]::OrdinalIgnoreCase) -and
                (Test-Path -LiteralPath $executable -PathType Leaf)) {
                $runtimeRoots = @((Join-Path (Split-Path -Parent $executable) 'resources'))
                if ($package.PSObject.Properties['Dependencies']) {
                    foreach ($dependency in @($package.Dependencies)) {
                        if ($dependency.PublisherId -eq $package.PublisherId -and $dependency.InstallLocation) {
                            $runtimeRoots += $dependency.InstallLocation
                        }
                    }
                }
                [pscustomobject]@{Executable=$executable;Version=[string]$package.Version;
                    RuntimeRoots=$runtimeRoots;PackageRoot=$root}
            }
        }
    }
}

function Assert-FiloDesktopPeer {
    param($Peer, $Owner, [string]$ExpectedUserSid, [object[]]$Applications)
    if (-not $Peer -or $Peer.SessionId -eq 0 -or -not $Peer.ExecutablePath -or
        -not @($Applications | Where-Object { $_.Executable -ieq $Peer.ExecutablePath }).Count) {
        throw 'Desktop IPC server is not a registered original interactive Codex application'
    }
    if ($Owner.ReturnValue -ne 0 -or $Owner.Sid -ne $ExpectedUserSid) {
        throw 'Desktop IPC server belongs to another Windows account'
    }
}
