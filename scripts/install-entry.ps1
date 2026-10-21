<#
.SYNOPSIS
Forward the packaged installer options without converting switches to positional values.
#>
[CmdletBinding()]
param(
    [string]$BundleDirectory,
    [string[]]$Clients,
    [switch]$ListClients,
    [switch]$ValidateOnly,
    [switch]$Uninstall,
    [string]$Bind,
    [string]$CodexDaemonSocket,
    [ValidateRange(1024,65535)][int]$ClaudeBridgePort = 7438,
    [switch]$CopyClaudeMod,
    [string]$ClaudeModDirectory,
    [ValidateRange(1024,65535)][int]$Port = 7435
)
& (Join-Path $PSScriptRoot 'scripts\install.ps1') @PSBoundParameters
