$ErrorActionPreference='Stop'
. (Join-Path $PSScriptRoot '../windows/Service-State.ps1')
function Assert($Value,$Message) { if (-not $Value) { throw $Message } }
foreach($state in @('Running','Paused','Start Pending','Continue Pending','Pause Pending')) {
    Assert (Test-FiloServiceShouldRun ([pscustomobject]@{State=$state})) "Preserve automatic recovery intent: $state"
}
foreach($state in @('Stopped','Stop Pending','Unknown')) {
    Assert (-not (Test-FiloServiceShouldRun ([pscustomobject]@{State=$state}))) "Do not resurrect stopped or unknown service: $state"
}
Assert (-not (Test-FiloServiceShouldRun $null)) 'No service does not imply a restart'

# Exercise the actual readiness function without loading the administrative entrypoint.
$path=Join-Path $PSScriptRoot '../system-service.ps1'
$tokens=$null; $errors=$null
$ast=[Management.Automation.Language.Parser]::ParseFile($path,[ref]$tokens,[ref]$errors)
Assert (-not $errors.Count) 'Service entrypoint parses'
$definition=$ast.Find({param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'Wait-GatewayReady'},$false)
Assert ($null -ne $definition) 'Readiness contract is present'
. ([scriptblock]::Create($definition.Extent.Text))
$config=@{bind='127.0.0.1';port=12345}; $token='fixture'; $serviceName='fixture'
$script:reads=0; $script:state='Paused'; $script:protocol=2; $script:progress=$true
$Action='Install'; $FiloPath='Invoke-FixtureEncryptedProbe'; $statePath='C:\FiloFixture'
function Invoke-FixtureEncryptedProbe {
    param($Command,$url,${token-file},$timeout)
    Assert ($Command -eq 'request' -and $url -eq 'http://127.0.0.1:12345') 'Readiness uses the selected encrypted endpoint'
    Assert (${token-file} -eq (Join-Path $statePath 'token') -and $timeout -eq '2s') 'Readiness passes a private token file, never its contents'
    $script:reads++
    $global:LASTEXITCODE=0
    @{protocolVersion=$script:protocol;sessionMode='existing'} | ConvertTo-Json -Compress
}
function Invoke-RestMethod { throw 'Normal Go readiness must never send a plaintext request' }
function Get-CimInstance {
    param($ClassName,$Filter)
    [pscustomobject]@{State=$script:state;StartName='LocalSystem'}
}
function Start-Sleep { param($Milliseconds) if($script:progress){$script:state='Running'} }
Wait-GatewayReady
Assert ($script:reads -eq 2) 'HTTP readiness alone cannot accept SCM recovery backoff'
foreach($case in @('paused','wrong-protocol')) {
    $script:progress=$false; $script:reads=0
    $script:state=if($case -eq 'paused'){'Paused'}else{'Running'}
    $script:protocol=if($case -eq 'wrong-protocol'){1}else{2}
    $failed=$false
    try { Wait-GatewayReady -Seconds 0 } catch { $failed=$_.Exception.Message -eq 'Filo SYSTEM service did not become ready' }
    Assert $failed "Incomplete recovery fails explicitly: $case"
}
'PASS: service restart intent and authenticated HTTP/SCM recovery readiness'
