$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 2
$repo = Split-Path -Parent (Split-Path -Parent $PSScriptRoot)
$builder = Join-Path $repo 'scripts\build-windows.ps1'
$tokens = $null; $parseErrors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile($builder,[ref]$tokens,[ref]$parseErrors)
if ($parseErrors.Count) { throw $parseErrors[0] }
$progressAssignment = $ast.Find({param($node)
    $node -is [Management.Automation.Language.AssignmentStatementAst] -and
    $node.Left.Extent.Text -eq '$ProgressPreference'
},$true)
if (-not $progressAssignment) { throw 'Build must suppress serialized progress before work starts' }
. ([scriptblock]::Create($progressAssignment.Extent.Text))
if ($ProgressPreference -ne 'SilentlyContinue') { throw 'Build progress must not block native remoting' }
$definition = $ast.Find({param($node)
    $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq 'Get-FiloSourceIdentity'
},$false)
if (-not $definition) { throw 'Missing production source identity owner' }
. ([scriptblock]::Create($definition.Extent.Text))
function Assert($Value,[string]$Message) { if (-not $Value) { throw $Message } }
$fixture = Join-Path ([IO.Path]::GetTempPath()) ('filo-source-identity-' + [guid]::NewGuid().ToString('N'))
try {
    $head = & git -C $repo rev-parse HEAD
    if ($LASTEXITCODE -ne 0) { throw 'Cannot read fixture source HEAD' }
    $common = & git -C $repo rev-parse --git-common-dir
    if ($LASTEXITCODE -ne 0) { throw 'Cannot read fixture object directory' }
    if (-not [IO.Path]::IsPathRooted($common)) { $common = Join-Path $repo $common }
    # A disposable repository view borrows read-only objects. No Git mutation command
    # runs, and the real repository's worktree, index, refs and history stay untouched.
    $gitDirectory = Join-Path $fixture '.git'
    foreach ($directory in @('objects\info','refs')) {
        [IO.Directory]::CreateDirectory((Join-Path $gitDirectory $directory)) | Out-Null
    }
    [IO.File]::WriteAllText((Join-Path $gitDirectory 'HEAD'),$head + "`n")
    [IO.File]::WriteAllText((Join-Path $gitDirectory 'config'),"[core]`nrepositoryformatversion = 0`nbare = false`n")
    $objects = [IO.Path]::GetFullPath((Join-Path $common 'objects')).Replace('\','/')
    [IO.File]::WriteAllText((Join-Path $gitDirectory 'objects\info\alternates'),$objects + "`n")
    [IO.File]::WriteAllText((Join-Path $fixture '.gitignore'),"ignored/`n")
    [IO.File]::WriteAllText((Join-Path $fixture 'z.txt'),'last')
    [IO.File]::WriteAllText((Join-Path $fixture 'a space.txt'),'first')
    $first = Get-FiloSourceIdentity $fixture
    $repeat = Get-FiloSourceIdentity $fixture
    Assert ($first.Head -ceq $head -and $first.Modified) 'Fixture records the real source HEAD and modified state'
    Assert ($first.Sha256 -cmatch '^[0-9a-f]{64}$' -and $first.Sha256 -ceq $repeat.Sha256) 'Repeated source inventory is deterministic'
    Assert ($first.Files.Count -eq 3 -and $first.Files[1] -ceq 'a space.txt') 'Untracked names including spaces are inventoried in ordinal order'
    [IO.File]::WriteAllText((Join-Path $fixture 'a space.txt'),'changed')
    $changed = Get-FiloSourceIdentity $fixture
    Assert ($changed.Sha256 -cne $first.Sha256) 'Content edits change source identity'
    [IO.File]::WriteAllText((Join-Path $fixture 'a space.txt'),'first')
    Assert ((Get-FiloSourceIdentity $fixture).Sha256 -ceq $first.Sha256) 'Identical bytes restore identical identity'
    [IO.File]::WriteAllText((Join-Path $fixture 'new.txt'),'new')
    Assert ((Get-FiloSourceIdentity $fixture).Sha256 -cne $first.Sha256) 'An added source file changes identity'
    [IO.File]::Delete((Join-Path $fixture 'new.txt'))
    [IO.File]::Delete((Join-Path $fixture 'z.txt'))
    Assert ((Get-FiloSourceIdentity $fixture).Sha256 -cne $first.Sha256) 'A removed source file changes identity'
    [IO.File]::WriteAllText((Join-Path $fixture 'z.txt'),'last')
    [IO.Directory]::CreateDirectory((Join-Path $fixture 'ignored')) | Out-Null
    [IO.File]::WriteAllText((Join-Path $fixture 'ignored\output.bin'),'generated output')
    Assert ((Get-FiloSourceIdentity $fixture).Sha256 -ceq $first.Sha256) 'Ignored generated output does not pollute source identity'
    $archiveCall = $ast.Find({param($node)
        $node -is [Management.Automation.Language.InvokeMemberExpressionAst] -and
        $node.Extent.Text -like '[[]IO.Compression.ZipFile]::CreateFromDirectory*'
    },$true)
    Assert ($null -ne $archiveCall) 'Production bundle uses the bounded native ZIP implementation'
    $target = Join-Path $fixture 'payload'
    [IO.Directory]::CreateDirectory($target) | Out-Null
    [IO.File]::WriteAllText((Join-Path $target 'a space.txt'),'payload')
    [IO.File]::WriteAllText((Join-Path $target '.hidden'),'hidden payload')
    $archive = Join-Path $fixture 'candidate.zip'
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    & ([scriptblock]::Create($archiveCall.Extent.Text))
    $zip = [IO.Compression.ZipFile]::OpenRead($archive)
    try {
        $names = @($zip.Entries | ForEach-Object { $_.FullName.Replace('\','/') })
        Assert ($names.Count -eq 2 -and $names -contains 'payload/a space.txt' -and $names -contains 'payload/.hidden') 'Archive preserves root, spaces and hidden payloads'
    } finally { $zip.Dispose() }
    $expanded = Join-Path $fixture 'expanded'
    [IO.Compression.ZipFile]::ExtractToDirectory($archive,$expanded)
    Assert ([IO.File]::ReadAllText((Join-Path $expanded 'payload\a space.txt')) -ceq 'payload') 'Archive extracts exact payload bytes'
    'PASS: production source identity and ZIP packaging preserve deterministic provenance, complete payloads and exact round-trip bytes'
} finally {
    if (Test-Path -LiteralPath $fixture) { Remove-Item -LiteralPath $fixture -Recurse -Force }
}
