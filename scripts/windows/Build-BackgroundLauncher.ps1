param([Parameter(Mandatory=$true)][string]$OutputPath)
$ErrorActionPreference='Stop'
if (Test-Path -LiteralPath $OutputPath) { throw 'Build the background launcher into a new output path' }
$repo=Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$go=(Get-Command go -ErrorAction Stop).Source
$previous=@{}
foreach($name in @('CGO_ENABLED','GOOS','GOARCH')) { $previous[$name]=[Environment]::GetEnvironmentVariable($name,'Process') }
Push-Location $repo
try {
    $env:CGO_ENABLED='0'; $env:GOOS='windows'; $env:GOARCH='amd64'
    & $go build -trimpath -ldflags '-H=windowsgui' -o $OutputPath ./cmd/filo-background
    if ($LASTEXITCODE -ne 0) { throw 'Background launcher compilation failed' }
} finally {
    Pop-Location
    foreach($name in $previous.Keys) { [Environment]::SetEnvironmentVariable($name,$previous[$name],'Process') }
}
if (-not (Test-Path -LiteralPath $OutputPath -PathType Leaf)) { throw 'Background launcher compilation failed' }
