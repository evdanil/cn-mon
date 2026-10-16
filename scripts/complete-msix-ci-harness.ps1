# Copyright 2026 cn-mon contributors
# SPDX-License-Identifier: Apache-2.0

[CmdletBinding()]
param()

$ErrorActionPreference = 'Stop'

function Assert-True {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw $Message }
}

function Assert-Throws {
    param([scriptblock]$Action, [string]$Message)
    try { & $Action }
    catch { return }
    throw $Message
}

$wrapperPath = Join-Path $PSScriptRoot 'complete-msix-ci.ps1'
if (-not (Test-Path -LiteralPath $wrapperPath -PathType Leaf)) { throw 'completion CI wrapper is missing' }
$parseErrors = $null
$wrapperAst = [System.Management.Automation.Language.Parser]::ParseFile($wrapperPath, [ref]$null, [ref]$parseErrors)
if ($parseErrors.Count -gt 0) { throw 'completion CI wrapper did not parse' }
$requiredFunctions = @(
    'Get-CnMonMsixRoute',
    'Get-CnMonMsixPinnedCandidate',
    'Get-CnMonMsixCompletionAllowedChangedPaths',
    'Get-CnMonMsixRequiredCompletionPaths',
    'Assert-CnMonMsixCompletionChangedPaths',
    'Get-CnMonMsixGitOutput',
    'Get-CnMonMsixSha256',
    'Get-CnMonMsixGitBlobBytes',
    'Get-CnMonMsixGitBlobSha256',
    'Export-CnMonMsixGitBlob',
    'Assert-CnMonMsixReviewedController',
    'Get-CnMonMsixChangedPaths',
    'Assert-CnMonMsixCandidateRevisionAncestor',
    'Assert-CnMonMsixCompletionConfig',
    'Assert-CnMonMsixPinnedArtifactMetadata',
    'Assert-CnMonMsixArtifactMemberNames',
    'Assert-CnMonMsixPinnedInnerHashes',
    'Test-CnMonMsixExactDictionaryKeys',
    'Assert-CnMonMsixReviewedCompletionReport',
    'New-CnMonMsixReviewedCompletionSummary'
)
$functionAsts = @($wrapperAst.FindAll({
    param($candidate)
    $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
        $requiredFunctions -contains $candidate.Name
}, $true))
if (@($requiredFunctions | Where-Object { $_ -notin @($functionAsts | ForEach-Object Name) }).Count -gt 0) {
    throw 'completion CI wrapper safety function is missing'
}
. ([scriptblock]::Create(($functionAsts | ForEach-Object { $_.Extent.Text }) -join [Environment]::NewLine))

$routeRoot = Join-Path ([IO.Path]::GetTempPath()) ('cn-mon-msix-route-' + [Guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $routeRoot
try {
    & git -C $routeRoot init --quiet
    if ($LASTEXITCODE -ne 0) { throw 'route fixture repository could not be initialized' }
    & git -C $routeRoot config user.name 'Completion CI harness'
    & git -C $routeRoot config user.email 'completion-ci@example.invalid'
    [IO.File]::WriteAllText((Join-Path $routeRoot 'base.txt'), 'base')
    & git -C $routeRoot add base.txt
    & git -C $routeRoot commit --quiet -m base
    if ($LASTEXITCODE -ne 0) { throw 'route fixture base commit could not be created' }
    $routeBaseRevision = (& git -C $routeRoot rev-parse HEAD).Trim()
    $routeConfigPath = Join-Path $routeRoot 'packaging/windows/qualification/completion.json'
    Assert-True ((Get-CnMonMsixRoute -ConfigurationPath $routeConfigPath -RepositoryRoot $routeRoot `
        -PullRequestBaseRevision $routeBaseRevision -PullRequestHeadRevision $routeBaseRevision `
        -EventName 'pull_request') -ceq 'Qualification') `
        'missing reviewed configuration did not retain ordinary qualification'
    $null = New-Item -ItemType Directory -Path (Split-Path -Parent $routeConfigPath) -Force
    [IO.File]::WriteAllText($routeConfigPath, '{"mode":"ReviewedRecord"}')
    & git -C $routeRoot add packaging/windows/qualification/completion.json
    & git -C $routeRoot commit --quiet -m 'introduce reviewed completion config'
    if ($LASTEXITCODE -ne 0) { throw 'route fixture config-add commit could not be created' }
    $routeAddedRevision = (& git -C $routeRoot rev-parse HEAD).Trim()
    Assert-True ((Get-CnMonMsixRoute -ConfigurationPath $routeConfigPath -RepositoryRoot $routeRoot `
        -PullRequestBaseRevision $routeBaseRevision -PullRequestHeadRevision $routeAddedRevision `
        -EventName 'pull_request') -ceq 'ReviewedRecord') `
        'newly introduced reviewed configuration did not select the reviewed-record route'

    [IO.File]::WriteAllText((Join-Path $routeRoot 'unrelated.txt'), 'inherited config')
    & git -C $routeRoot add unrelated.txt
    & git -C $routeRoot commit --quiet -m 'leave completion config unchanged'
    if ($LASTEXITCODE -ne 0) { throw 'route fixture inherited-config commit could not be created' }
    $routeInheritedRevision = (& git -C $routeRoot rev-parse HEAD).Trim()
    Assert-True ((Get-CnMonMsixRoute -ConfigurationPath $routeConfigPath -RepositoryRoot $routeRoot `
        -PullRequestBaseRevision $routeAddedRevision -PullRequestHeadRevision $routeInheritedRevision `
        -EventName 'pull_request') -ceq 'Qualification') `
        'inherited unchanged reviewed configuration selected reviewed-record mode'

    [IO.File]::WriteAllText($routeConfigPath, '{"mode":"ModifiedReviewedRecord"}')
    & git -C $routeRoot add packaging/windows/qualification/completion.json
    & git -C $routeRoot commit --quiet -m 'modify existing completion config'
    if ($LASTEXITCODE -ne 0) { throw 'route fixture config-modification commit could not be created' }
    $routeModifiedRevision = (& git -C $routeRoot rev-parse HEAD).Trim()
    Assert-True ((Get-CnMonMsixRoute -ConfigurationPath $routeConfigPath -RepositoryRoot $routeRoot `
        -PullRequestBaseRevision $routeAddedRevision -PullRequestHeadRevision $routeModifiedRevision `
        -EventName 'pull_request') -ceq 'Qualification') `
        'modified existing reviewed configuration selected reviewed-record mode'
    Assert-Throws { Get-CnMonMsixRoute -ConfigurationPath $routeConfigPath -RepositoryRoot $routeRoot `
        -PullRequestBaseRevision $routeAddedRevision -PullRequestHeadRevision $routeModifiedRevision `
        -EventName 'workflow_dispatch' } `
        'reviewed-record route was available outside pull_request'
}
finally { Remove-Item -LiteralPath $routeRoot -Recurse -Force -ErrorAction SilentlyContinue }
Write-Host 'PASS reviewed-record routing requires a newly added PR config and preserves qualification otherwise'

$wantAllowedPaths = @(
    '.github/workflows/msix.yml',
        '.gitattributes',
    'README.md',
    'docs/APPLICATION_SPEC.md',
    'docs/DEVELOPMENT_PLAN.md',
    'docs/MSIX_PACKAGING_PLAN.md',
    'docs/MSIX_PACKAGING_SPEC.md',
    'docs/WINDOWS_STORE_LISTING.md',
    'docs/WINDOWS_STORE_PREPARATION.md',
    'packaging/windows/qualification/completion.json',
    'packaging/windows/qualification/desktop-review-record.json',
    'packaging/windows/qualification/desktop-supplement.json',
    'packaging/windows/qualification/local-audit-report.json',
    'scripts/complete-msix-ci-harness.ps1',
    'scripts/complete-msix-ci.ps1',
    'scripts/complete-msix-qualification-harness.ps1',
    'scripts/complete-msix-qualification.ps1',
    'scripts/release-artifact/archive.go',
    'scripts/release-artifact/main_test.go',
    'scripts/test-msix-desktop-preflight-harness.ps1',
    'scripts/test-msix-desktop-preflight.ps1',
    'scripts/test-msix-desktop-checks-harness.ps1',
    'scripts/test-msix-desktop-checks.ps1',
    'scripts/test-msix-desktop-session-harness.ps1',
    'scripts/test-msix-desktop-session.ps1',
    'scripts/test-msix-workflow.py'
)
$allowedPaths = @(Get-CnMonMsixCompletionAllowedChangedPaths)
Assert-True (($allowedPaths -join "`n") -ceq ($wantAllowedPaths -join "`n")) 'completion changed-path allowlist changed'
Assert-True (@($allowedPaths | Where-Object { $_ -match '[*?]' }).Count -eq 0) 'completion changed-path allowlist uses a wildcard'
Assert-CnMonMsixCompletionChangedPaths -ChangedPaths $allowedPaths
Assert-CnMonMsixCompletionChangedPaths -ChangedPaths (Get-CnMonMsixRequiredCompletionPaths)
foreach ($path in @(
    'scripts/msix-ci.ps1',
    'packaging/windows/release.json',
    'cmd/cn-mon/main.go',
    'docs/PRIVACY.md',
    'scripts/release-artifact/other.go',
    '../outside.txt',
    'README.md.bak'
)) {
    Assert-Throws { Assert-CnMonMsixCompletionChangedPaths -ChangedPaths @($path) } "completion path '$path' was accepted"
}
Assert-Throws {
    Assert-CnMonMsixCompletionChangedPaths -ChangedPaths @('README.md', 'README.md')
} 'duplicate changed paths were accepted'
Write-Host 'PASS completion path changes are explicit, exact and require all sanitized evidence files'

$eolRoot = Join-Path ([IO.Path]::GetTempPath()) ('cn-mon-msix-eol-' + [Guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $eolRoot
try {
    & git -C $eolRoot init --quiet
    & git -C $eolRoot config user.name 'Completion EOL harness'
    & git -C $eolRoot config user.email 'completion-eol@example.invalid'
    [IO.File]::WriteAllText((Join-Path $eolRoot '.gitattributes'), @'
packaging/windows/qualification/completion.json -text
packaging/windows/qualification/local-audit-report.json -text
packaging/windows/qualification/desktop-supplement.json -text
packaging/windows/qualification/desktop-review-record.json -text
'@)
    $eolPaths = Get-CnMonMsixRequiredCompletionPaths
    $eolHashes = @{}
    foreach ($path in $eolPaths) {
        $full = Join-Path $eolRoot $path
        $null = New-Item -ItemType Directory -Path (Split-Path -Parent $full) -Force
        $bytes = [byte[]](0x7B, 0x0D, 0x0A, 0x20, 0x22, 0x6B, 0x22, 0x3A, 0x20, 0x31, 0x0A, 0x7D, 0x0D, 0x0A)
        [IO.File]::WriteAllBytes($full, $bytes)
        $eolHashes[$path] = [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($bytes)).ToLowerInvariant()
    }
    & git -C $eolRoot add .
    & git -C $eolRoot commit --quiet -m raw-evidence-json
    $eolRevision = (& git -C $eolRoot rev-parse HEAD).Trim()
    foreach ($autocrlf in @('true', 'false')) {
        & git -C $eolRoot -c "core.autocrlf=$autocrlf" checkout --quiet --force $eolRevision
        foreach ($path in $eolPaths) {
            Assert-True ((Get-CnMonMsixSha256 -Path (Join-Path $eolRoot $path)) -ceq $eolHashes[$path]) `
                "Git checkout with core.autocrlf=$autocrlf changed evidence bytes in '$path'"
        }
    }
}
finally { Remove-Item -LiteralPath $eolRoot -Recurse -Force -ErrorAction SilentlyContinue }
Write-Host 'PASS Git preserves exact sanitized evidence JSON bytes with core.autocrlf enabled or disabled'

$testGitRoot = Join-Path ([IO.Path]::GetTempPath()) ('cn-mon-msix-git-' + [Guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $testGitRoot
try {
    & git -C $testGitRoot init --quiet
    if ($LASTEXITCODE -ne 0) { throw 'test git repository could not be initialized' }
    & git -C $testGitRoot config user.name 'Completion CI harness'
    if ($LASTEXITCODE -ne 0) { throw 'test git repository name could not be configured' }
    & git -C $testGitRoot config user.email 'completion-ci@example.invalid'
    if ($LASTEXITCODE -ne 0) { throw 'test git repository email could not be configured' }
    [IO.File]::WriteAllText((Join-Path $testGitRoot 'base.txt'), 'base')
    & git -C $testGitRoot add base.txt
    if ($LASTEXITCODE -ne 0) { throw 'test base file could not be staged' }
    & git -C $testGitRoot commit --quiet -m base
    if ($LASTEXITCODE -ne 0) { throw 'test base commit could not be created' }
    $baseRevision = (& git -C $testGitRoot rev-parse HEAD).Trim()
    if ($LASTEXITCODE -ne 0) { throw 'test base revision could not be read' }

    [IO.File]::WriteAllText((Join-Path $testGitRoot 'head.txt'), 'head')
    & git -C $testGitRoot add head.txt
    if ($LASTEXITCODE -ne 0) { throw 'test descendant file could not be staged' }
    & git -C $testGitRoot commit --quiet -m descendant
    if ($LASTEXITCODE -ne 0) { throw 'test descendant commit could not be created' }
    $descendantRevision = (& git -C $testGitRoot rev-parse HEAD).Trim()
    if ($LASTEXITCODE -ne 0) { throw 'test descendant revision could not be read' }
    Assert-CnMonMsixCandidateRevisionAncestor -RepositoryRoot $testGitRoot -BaselineRevision $baseRevision -HeadRevision $descendantRevision

    $desktopCheckPaths = @(
        'scripts/test-msix-desktop-checks-harness.ps1',
        'scripts/test-msix-desktop-checks.ps1'
    )
    foreach ($path in $desktopCheckPaths) {
        $fullPath = Join-Path $testGitRoot $path
        $null = New-Item -ItemType Directory -Path (Split-Path -Parent $fullPath) -Force
        [IO.File]::WriteAllText($fullPath, 'reviewed fixture')
    }
    & git -C $testGitRoot add -- $desktopCheckPaths
    if ($LASTEXITCODE -ne 0) { throw 'reviewed desktop-check paths could not be staged' }
    & git -C $testGitRoot commit --quiet -m 'reviewed desktop check paths'
    if ($LASTEXITCODE -ne 0) { throw 'reviewed desktop-check fixture commit could not be created' }
    $desktopRevision = (& git -C $testGitRoot rev-parse HEAD).Trim()
    if ($LASTEXITCODE -ne 0) { throw 'reviewed desktop-check revision could not be read' }
    $desktopChangedPaths = Get-CnMonMsixChangedPaths -RepositoryRoot $testGitRoot `
        -BaselineRevision $descendantRevision -HeadRevision $desktopRevision
    Assert-True ($desktopChangedPaths.Count -eq 2 -and
        $desktopCheckPaths[0] -cin $desktopChangedPaths -and $desktopCheckPaths[1] -cin $desktopChangedPaths) `
        'temporary Git diff did not report the exact reviewed desktop-check paths'
    foreach ($path in $desktopChangedPaths) {
        Assert-True ($allowedPaths -ccontains $path) "temporary Git path '$path' was not allowlisted"
    }

    $forbiddenAppPath = 'cmd/cn-mon/main.go'
    $forbiddenAppFullPath = Join-Path $testGitRoot $forbiddenAppPath
    $null = New-Item -ItemType Directory -Path (Split-Path -Parent $forbiddenAppFullPath) -Force
    [IO.File]::WriteAllText($forbiddenAppFullPath, 'forbidden application change')
    & git -C $testGitRoot add -- $forbiddenAppPath
    if ($LASTEXITCODE -ne 0) { throw 'forbidden Go application path could not be staged' }
    & git -C $testGitRoot commit --quiet -m 'forbidden application change'
    if ($LASTEXITCODE -ne 0) { throw 'forbidden Go application fixture commit could not be created' }
    $forbiddenRevision = (& git -C $testGitRoot rev-parse HEAD).Trim()
    if ($LASTEXITCODE -ne 0) { throw 'forbidden application fixture revision could not be read' }
    $forbiddenChangedPaths = Get-CnMonMsixChangedPaths -RepositoryRoot $testGitRoot `
        -BaselineRevision $desktopRevision -HeadRevision $forbiddenRevision
    Assert-True (($forbiddenChangedPaths -join "`n") -ceq $forbiddenAppPath) `
        'temporary Git diff did not report the forbidden Go application path'
    Assert-Throws {
        Assert-CnMonMsixCompletionChangedPaths -ChangedPaths @((Get-CnMonMsixRequiredCompletionPaths) + $forbiddenChangedPaths)
    } 'temporary Git diff accepted a forbidden Go application path'
    $descendantRevision = $forbiddenRevision

    & git -C $testGitRoot checkout --quiet --detach $baseRevision
    if ($LASTEXITCODE -ne 0) { throw 'test side branch could not be selected' }
    [IO.File]::WriteAllText((Join-Path $testGitRoot 'side.txt'), 'side')
    & git -C $testGitRoot add side.txt
    if ($LASTEXITCODE -ne 0) { throw 'test side file could not be staged' }
    & git -C $testGitRoot commit --quiet -m side
    if ($LASTEXITCODE -ne 0) { throw 'test side commit could not be created' }
    $sideRevision = (& git -C $testGitRoot rev-parse HEAD).Trim()
    if ($LASTEXITCODE -ne 0) { throw 'test side revision could not be read' }
    Assert-Throws {
        Assert-CnMonMsixCandidateRevisionAncestor -RepositoryRoot $testGitRoot -BaselineRevision $descendantRevision -HeadRevision $sideRevision
    } 'non-ancestor candidate revision was accepted'
}
finally { Remove-Item -LiteralPath $testGitRoot -Recurse -Force -ErrorAction SilentlyContinue }
Write-Host 'PASS the immutable packaging revision must be an ancestor of the PR head'

$controllerRoot = Join-Path ([IO.Path]::GetTempPath()) ('cn-mon-msix-controller-' + [Guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $controllerRoot
try {
    & git -C $controllerRoot init --quiet
    & git -C $controllerRoot config user.name 'Completion controller harness'
    & git -C $controllerRoot config user.email 'completion-controller@example.invalid'
    $controllerWrapper = Join-Path $controllerRoot 'scripts/complete-msix-ci.ps1'
    $controllerWorkflow = Join-Path $controllerRoot '.github/workflows/msix.yml'
    $historicalValidator = Join-Path $controllerRoot 'scripts/complete-msix-qualification.ps1'
    $null = New-Item -ItemType Directory -Path (Split-Path -Parent $controllerWrapper) -Force
    $null = New-Item -ItemType Directory -Path (Split-Path -Parent $controllerWorkflow) -Force
    [IO.File]::WriteAllText($controllerWrapper, 'reviewed controller bytes')
    [IO.File]::WriteAllText($historicalValidator, 'historical validator bytes')
    [IO.File]::WriteAllText($controllerWorkflow, "pin=REVIEWED_COMPLETION_CONTROLLER_REVISION`n")
    & git -C $controllerRoot add .
    & git -C $controllerRoot commit --quiet -m reviewed-controller
    if ($LASTEXITCODE -ne 0) { throw 'reviewed controller fixture commit failed' }
    $controllerRevision = (& git -C $controllerRoot rev-parse HEAD).Trim()
    $historicalHash = Get-CnMonMsixGitBlobSha256 -RepositoryRoot $controllerRoot -Revision $controllerRevision -Path 'scripts/complete-msix-qualification.ps1'
    $controllerWorkflowText = [IO.File]::ReadAllText($controllerWorkflow).Replace('REVIEWED_COMPLETION_CONTROLLER_REVISION', $controllerRevision)
    [IO.File]::WriteAllText($controllerWorkflow, $controllerWorkflowText)
    foreach ($path in @(
        'packaging/windows/qualification/completion.json',
        'packaging/windows/qualification/local-audit-report.json',
        'packaging/windows/qualification/desktop-supplement.json',
        'packaging/windows/qualification/desktop-review-record.json'
    )) {
        $full = Join-Path $controllerRoot $path
        $null = New-Item -ItemType Directory -Path (Split-Path -Parent $full) -Force
        [IO.File]::WriteAllText($full, '{}')
    }
    & git -C $controllerRoot add .
    & git -C $controllerRoot commit --quiet -m evidence-and-pin
    if ($LASTEXITCODE -ne 0) { throw 'evidence controller fixture commit failed' }
    $controllerHead = (& git -C $controllerRoot rev-parse HEAD).Trim()
    Assert-CnMonMsixReviewedController -RepositoryRoot $controllerRoot -ControllerRevision $controllerRevision -HeadRevision $controllerHead -EvidenceOnly
    $forbiddenControllerPath = Join-Path $controllerRoot 'cmd/forbidden.go'
    $null = New-Item -ItemType Directory -Path (Split-Path -Parent $forbiddenControllerPath) -Force
    [IO.File]::WriteAllText($forbiddenControllerPath, 'forbidden')
    & git -C $controllerRoot add -- cmd/forbidden.go
    & git -C $controllerRoot commit --quiet -m forbidden-change
    $tamperedHead = (& git -C $controllerRoot rev-parse HEAD).Trim()
    Assert-Throws { Assert-CnMonMsixReviewedController -RepositoryRoot $controllerRoot -ControllerRevision $controllerRevision -HeadRevision $tamperedHead -EvidenceOnly } `
        'evidence-only controller check accepted a forbidden application path'
    [IO.File]::WriteAllText($controllerWorkflow, $controllerWorkflowText + "changed=true`n")
    & git -C $controllerRoot add -- .github/workflows/msix.yml
    & git -C $controllerRoot commit --quiet -m workflow-change
    $tamperedHead = (& git -C $controllerRoot rev-parse HEAD).Trim()
    Assert-Throws { Assert-CnMonMsixReviewedController -RepositoryRoot $controllerRoot -ControllerRevision $controllerRevision -HeadRevision $tamperedHead } `
        'controller check accepted a changed workflow'
    [IO.File]::WriteAllText($controllerWrapper, 'changed controller bytes')
    & git -C $controllerRoot add -- scripts/complete-msix-ci.ps1
    & git -C $controllerRoot commit --quiet -m wrapper-change
    $tamperedHead = (& git -C $controllerRoot rev-parse HEAD).Trim()
    Assert-Throws { Assert-CnMonMsixReviewedController -RepositoryRoot $controllerRoot -ControllerRevision $controllerRevision -HeadRevision $tamperedHead } `
        'controller check accepted changed wrapper bytes'
    [IO.File]::WriteAllText($historicalValidator, 'changed validator bytes')
    & git -C $controllerRoot add -- scripts/complete-msix-qualification.ps1
    & git -C $controllerRoot commit --quiet -m changed-validator
    $tamperedHead = (& git -C $controllerRoot rev-parse HEAD).Trim()
    Assert-True ((Get-CnMonMsixGitBlobSha256 -RepositoryRoot $controllerRoot -Revision $controllerRevision -Path 'scripts/complete-msix-qualification.ps1') -ceq $historicalHash) `
        'historical validator blob changed when the current source changed'
    Assert-True ((Get-CnMonMsixGitBlobSha256 -RepositoryRoot $controllerRoot -Revision $tamperedHead -Path 'scripts/complete-msix-qualification.ps1') -cne $historicalHash) `
        'historical replay fixture did not distinguish changed current validator source'
}
finally { Remove-Item -LiteralPath $controllerRoot -Recurse -Force -ErrorAction SilentlyContinue }
Write-Host 'PASS reviewed controller, normalized pin transition, evidence-only paths and historical blob replay'

$candidate = Get-CnMonMsixPinnedCandidate
$artifact = ConvertFrom-Json -InputObject (@{
    id = $candidate.artifact_id
    name = $candidate.artifact_name
    expired = $false
    workflow_run = @{ id = $candidate.run_id; head_sha = $candidate.head_sha }
} | ConvertTo-Json -Depth 4)
$run = ConvertFrom-Json -InputObject (@{
    id = $candidate.run_id
    run_attempt = $candidate.run_attempt
    head_sha = $candidate.head_sha
    event = 'pull_request'
    status = 'completed'
    conclusion = 'failure'
    repository = @{ full_name = $candidate.repository }
} | ConvertTo-Json -Depth 4)
Assert-CnMonMsixPinnedArtifactMetadata -Artifact $artifact -Run $run -Candidate $candidate
Assert-Throws {
    $successfulRun = ConvertFrom-Json -InputObject ($run | ConvertTo-Json -Depth 4)
    $successfulRun.conclusion = 'success'
    Assert-CnMonMsixPinnedArtifactMetadata -Artifact $artifact -Run $successfulRun -Candidate $candidate
} 'pinned baseline with an unexpected success conclusion was accepted'
Assert-Throws {
    $unexpectedRun = ConvertFrom-Json -InputObject ($run | ConvertTo-Json -Depth 4)
    $unexpectedRun.conclusion = 'cancelled'
    Assert-CnMonMsixPinnedArtifactMetadata -Artifact $artifact -Run $unexpectedRun -Candidate $candidate
} 'pinned baseline with an unexpected conclusion was accepted'
foreach ($mutate in @(
    { param($value) $value.Artifact.id++ },
    { param($value) $value.Artifact.name = 'other-artifact' },
    { param($value) $value.Artifact.expired = $true },
    { param($value) $value.Artifact.workflow_run.id++ },
    { param($value) $value.Artifact.workflow_run.head_sha = ('a' * 40) },
    { param($value) $value.Run.id++ },
    { param($value) $value.Run.run_attempt = 2 },
    { param($value) $value.Run.head_sha = ('a' * 40) },
    { param($value) $value.Run.event = 'push' },
    { param($value) $value.Run.status = 'in_progress' },
    { param($value) $value.Run.repository.full_name = 'other/repository' }
)) {
    $changedArtifact = [ordered]@{
        id = $artifact.id; name = $artifact.name; expired = $artifact.expired
        workflow_run = @{ id = $artifact.workflow_run.id; head_sha = $artifact.workflow_run.head_sha }
    }
    $changedRun = @{
        id = $run.id; run_attempt = $run.run_attempt; head_sha = $run.head_sha; event = $run.event
        status = $run.status; conclusion = $run.conclusion
        repository = @{ full_name = $run.repository.full_name }
    }
    $changedArtifact = ConvertFrom-Json -InputObject ($changedArtifact | ConvertTo-Json -Depth 4)
    $changedRun = ConvertFrom-Json -InputObject ($changedRun | ConvertTo-Json -Depth 4)
    & $mutate ([ordered]@{ Artifact = $changedArtifact; Run = $changedRun })
    Assert-Throws {
        Assert-CnMonMsixPinnedArtifactMetadata -Artifact $changedArtifact -Run $changedRun -Candidate $candidate
    } 'mismatched artifact/run provenance was accepted'
}
Write-Host 'PASS artifact metadata must match the pinned repository, run, head, attempt and artifact ID'

$expectedNames = @(Get-CnMonMsixPinnedCandidate).artifact_member_names
Assert-CnMonMsixArtifactMemberNames -Names $expectedNames -Candidate $candidate
Assert-Throws {
    Assert-CnMonMsixArtifactMemberNames -Names ($expectedNames | Where-Object { $_ -ne 'msix-native-results.json' }) -Candidate $candidate
} 'artifact with a missing member was accepted'
Assert-Throws {
    Assert-CnMonMsixArtifactMemberNames -Names @($expectedNames + 'unexpected.json') -Candidate $candidate
} 'artifact with an unexpected member was accepted'
Assert-Throws {
    Assert-CnMonMsixArtifactMemberNames -Names @($expectedNames + '../escape') -Candidate $candidate
} 'artifact with an unsafe member path was accepted'
Assert-CnMonMsixPinnedInnerHashes -ActualHashes $candidate.inner_hashes -Candidate $candidate
$wrongInnerHashes = [ordered]@{}
foreach ($name in $candidate.inner_hashes.Keys) { $wrongInnerHashes[$name] = $candidate.inner_hashes[$name] }
$wrongInnerHashes['msix-native-results.json'] = '0' * 64
Assert-Throws {
    Assert-CnMonMsixPinnedInnerHashes -ActualHashes $wrongInnerHashes -Candidate $candidate
} 'artifact with an unpinned inner result hash was accepted'
Write-Host 'PASS artifact members and the three immutable candidate byte hashes are exact'

$validConfig = [ordered]@{
    schema_version = 1
    mode = 'ReviewedRecord'
    expected_local_audit_record_sha256 = 'a' * 64
    expected_native_helper_sha256 = $candidate.native_helper_sha256
    validator_revision = 'c19e7984ec8b5bf7058a5dbc2864ac56722fbaf6'
}
Assert-CnMonMsixCompletionConfig -Config $validConfig -Candidate $candidate
$badConfig = [ordered]@{}
foreach ($key in $validConfig.Keys) { $badConfig[$key] = $validConfig[$key] }
$badConfig.validator_revision = $candidate.packaging_revision
Assert-Throws { Assert-CnMonMsixCompletionConfig -Config $badConfig -Candidate $candidate } `
    'completion config accepted the candidate packaging revision as its validator revision'
$badConfig.validator_revision = 'd' * 40
Assert-Throws { Assert-CnMonMsixCompletionConfig -Config $badConfig -Candidate $candidate } `
    'completion config accepted an unreviewed validator revision'
$badConfig = [ordered]@{}
foreach ($key in $validConfig.Keys) { $badConfig[$key] = $validConfig[$key] }
$badConfig.expected_native_helper_sha256 = 'b' * 64
Assert-Throws { Assert-CnMonMsixCompletionConfig -Config $badConfig -Candidate $candidate } `
    'completion config accepted an unpinned native helper hash'
$badConfig = [ordered]@{}
foreach ($key in $validConfig.Keys) { $badConfig[$key] = $validConfig[$key] }
$badConfig.unexpected = 'value'
Assert-Throws { Assert-CnMonMsixCompletionConfig -Config $badConfig -Candidate $candidate } `
    'completion config accepted an extra field'
Write-Host 'PASS reviewed configuration pins the exact local audit report, helper and validator revision'

$gateNames = @(
    'identity', 'package_hash', 'signature', 'account', 'package_registration', 'exact_identity',
    'installed_binary_hash', 'alias_identity', 'native_cli', 'icmp', 'history_config', 'shell_aliases',
    'upgrade', 'repair', 'uninstall_reinstall', 'conflicts', 'fail_injection', 'cleanup', 'wack'
)
$passingGates = [ordered]@{}
foreach ($gate in $gateNames) { $passingGates[$gate] = 'pass' }
$auditSha = 'a' * 64
$validatorRevision = 'c19e7984ec8b5bf7058a5dbc2864ac56722fbaf6'
$driverSha = 'd' * 64
$supplementSha = 'e' * 64
$reviewSha = 'f' * 64
$report = [ordered]@{
    schema_version = 1
    mode = 'desktop_completion_report'
    overall = 'pass'
    candidate = [ordered]@{
        run_id = [string]$candidate.run_id
        artifact_id = [string]$candidate.artifact_id
        archive_sha256 = $candidate.archive_sha256
        signed_package_sha256 = $candidate.inner_hashes['cn-mon_1.3.0.0_x64-signed.msix']
        signed_receipt_sha256 = $candidate.inner_hashes['msix-signed-receipt.json']
        certify_result_sha256 = $candidate.inner_hashes['msix-native-results.json']
        packaging_revision = $candidate.packaging_revision
    }
    qualification_scripts = [ordered]@{
        helper_sha256 = $candidate.native_helper_sha256
        helper_verification = 'reviewed_hash_attestation'
        screenshot_verification = 'reviewed_hash_attestation'
        desktop_driver_sha256 = $driverSha
        validator_revision = $validatorRevision
    }
    evidence = [ordered]@{
        local_audit_record_sha256 = $auditSha
        completion_supplement_sha256 = $supplementSha
        review_attestation_sha256 = $reviewSha
        private_evidence_bundle_sha256 = '1' * 64
    }
    gates = $passingGates
}
Assert-CnMonMsixReviewedCompletionReport -Report $report -Candidate $candidate `
    -ExpectedLocalAuditRecordSha256 $auditSha -ExpectedNativeHelperSha256 $candidate.native_helper_sha256 `
    -ExpectedValidatorRevision $validatorRevision -ExpectedDesktopDriverSha256 $driverSha `
    -ExpectedSupplementSha256 $supplementSha -ExpectedReviewAttestationSha256 $reviewSha
$wrongReport = [ordered]@{}
foreach ($key in $report.Keys) { $wrongReport[$key] = $report[$key] }
$wrongReport.qualification_scripts = [ordered]@{}
foreach ($key in $report.qualification_scripts.Keys) { $wrongReport.qualification_scripts[$key] = $report.qualification_scripts[$key] }
$wrongReport.qualification_scripts.screenshot_verification = 'local_bytes_verified'
Assert-Throws {
    Assert-CnMonMsixReviewedCompletionReport -Report $wrongReport -Candidate $candidate `
        -ExpectedLocalAuditRecordSha256 $auditSha -ExpectedNativeHelperSha256 $candidate.native_helper_sha256 `
        -ExpectedValidatorRevision $validatorRevision -ExpectedDesktopDriverSha256 $driverSha `
        -ExpectedSupplementSha256 $supplementSha -ExpectedReviewAttestationSha256 $reviewSha
} 'completion report accepted CI as having read private screenshot bytes'
Write-Host 'PASS completion report binds candidate hashes and keeps private-byte provenance as reviewed hash attestations'

$summary = New-CnMonMsixReviewedCompletionSummary -Report $report -Candidate $candidate
Assert-True ($summary.mode -ceq 'desktop_reviewed_completion_summary') 'reviewed completion summary has the wrong mode'
Assert-True (Test-CnMonMsixExactDictionaryKeys $summary @(
    'schema_version', 'mode', 'overall', 'candidate_packaging_revision', 'gate_count',
    'passed_gate_count', 'incomplete_gate_count', 'gates', 'qualification_provenance', 'evidence'
)) 'reviewed completion summary schema changed'
Assert-True ($summary.overall -ceq 'pass' -and $summary.gate_count -eq 19 -and
    $summary.passed_gate_count -eq 19 -and $summary.incomplete_gate_count -eq 0) `
    'reviewed completion summary did not project the validated 19/19 gates'
Assert-True ($summary.qualification_provenance.helper_verification -ceq 'reviewed_hash_attestation' -and
    $summary.qualification_provenance.screenshot_verification -ceq 'reviewed_hash_attestation') `
    'reviewed completion summary misreported private-byte provenance'
Assert-True (-not $summary.Contains('identity') -and -not $summary.Contains('publisher') -and
    -not $summary.Contains('signed_package_sha256')) 'reviewed completion summary included package identity or raw candidate hashes'
$incompleteReport = [ordered]@{}
foreach ($key in $report.Keys) { $incompleteReport[$key] = $report[$key] }
$incompleteReport.gates = [ordered]@{}
foreach ($key in $report.gates.Keys) { $incompleteReport.gates[$key] = $report.gates[$key] }
$incompleteReport.gates.repair = 'incomplete'
Assert-Throws { New-CnMonMsixReviewedCompletionSummary -Report $incompleteReport -Candidate $candidate } `
    'reviewed completion summary reported a pass with an incomplete gate'
Write-Host 'PASS reviewed completion summary reports only validated gate totals and identity-free provenance'
