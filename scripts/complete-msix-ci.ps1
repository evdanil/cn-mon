# Copyright 2026 cn-mon contributors
# SPDX-License-Identifier: Apache-2.0

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('SelectRoute', 'Complete')]
    [string]$Mode,
    [string]$ConfigurationPath = 'packaging/windows/qualification/completion.json',
    [string]$RepositoryRoot = '',
    [string]$PullRequestBaseRevision = '',
    [string]$PullRequestHeadRevision = '',
    [string]$ControllerRevision = ''
)

$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false

function Get-CnMonMsixRoute {
    param(
        [Parameter(Mandatory = $true)][string]$ConfigurationPath,
        [Parameter(Mandatory = $true)][string]$EventName,
        [Parameter(Mandatory = $true)][string]$RepositoryRoot,
        [Parameter(Mandatory = $true)][string]$PullRequestBaseRevision,
        [Parameter(Mandatory = $true)][string]$PullRequestHeadRevision
    )
    if ($EventName -cne 'pull_request') { throw 'reviewed completion is available only in pull request context' }
    if ($PullRequestBaseRevision -cnotmatch '^[0-9a-f]{40}$' -or
        $PullRequestHeadRevision -cnotmatch '^[0-9a-f]{40}$') {
        throw 'pull request revisions are invalid'
    }
    if (-not (Test-Path -LiteralPath $ConfigurationPath -PathType Leaf)) { return 'Qualification' }
    $relativePath = [IO.Path]::GetRelativePath($RepositoryRoot, [IO.Path]::GetFullPath($ConfigurationPath)).Replace('\', '/')
    if ($relativePath -cne 'packaging/windows/qualification/completion.json') {
        throw 'reviewed completion route configuration path is invalid'
    }
    $change = Get-CnMonMsixGitOutput -RepositoryRoot $RepositoryRoot -Arguments @(
        'diff', '--no-renames', '--name-status', $PullRequestBaseRevision, $PullRequestHeadRevision, '--', $relativePath
    )
    if ($change -ceq ("A`t" + $relativePath)) { return 'ReviewedRecord' }
    return 'Qualification'
}

function Get-CnMonMsixPinnedCandidate {
    $artifactNames = @(
        'cn-mon_1.3.0.0_x64-signed.msix',
        'msix-signed-receipt.json',
        'msix-native-results.json',
        'msix-desktop-preflight.json',
        'msix-desktop-surface.json',
        'msix-wack-structure.json'
    )
    return [ordered]@{
        repository = 'evdanil/cn-mon'
        run_id = [long]38866515555
        run_attempt = 1
        head_sha = 'b6c874d7b5de8257dde37685f0c3bfeb79b50b14'
        artifact_id = [long]12031747143
        artifact_name = 'cn-mon-msix-synthetic-qualification-38866515555-1'
        archive_sha256 = 'ed18d14a6a4e17526bd73d47f68336d9ee45ead71c15e61f106fba257d461818'
        packaging_revision = 'b6c874d7b5de8257dde37685f0c3bfeb79b50b14'
        native_helper_sha256 = '136ed196db55ca43930473d95ca37c3b1a11963e06138329f2d22c698fbde9fc'
        artifact_member_names = $artifactNames
        inner_hashes = [ordered]@{
            'cn-mon_1.3.0.0_x64-signed.msix' = 'd782d72374fe6bef2ca666e6cf64039283d0b1b24f4c9aa22cd940956873642f'
            'msix-signed-receipt.json' = '7f7ddda42b87b7611fe5107aeb519e3cf0e04e5e2870a7dc8374d6c7b74db55b'
            'msix-native-results.json' = 'ebdaa5f3b456afa5e183910b25e80385e3e1d0d86c8db7fee7d7847c9a99971f'
        }
    }
}

function Get-CnMonMsixCompletionAllowedChangedPaths {
    return @(
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
}

function Get-CnMonMsixRequiredCompletionPaths {
    return @(
        'packaging/windows/qualification/completion.json',
        'packaging/windows/qualification/local-audit-report.json',
        'packaging/windows/qualification/desktop-supplement.json',
        'packaging/windows/qualification/desktop-review-record.json'
    )
}

function Assert-CnMonMsixCompletionChangedPaths {
    param([Parameter(Mandatory = $true)][string[]]$ChangedPaths)

    $allowed = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    foreach ($path in Get-CnMonMsixCompletionAllowedChangedPaths) { $null = $allowed.Add($path) }
    $seen = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    foreach ($path in $ChangedPaths) {
        if ([string]::IsNullOrEmpty($path) -or $path.Contains([char]0) -or $path.Contains('\') -or
            [IO.Path]::IsPathRooted($path) -or -not $seen.Add($path) -or -not $allowed.Contains($path)) {
            throw 'pull request changes are outside the reviewed completion allowlist'
        }
    }
    if ($seen.Count -eq 0) { throw 'pull request has no reviewed completion changes' }
    foreach ($path in Get-CnMonMsixRequiredCompletionPaths) {
        if (-not $seen.Contains($path)) { throw 'pull request is missing a fixed sanitized completion record' }
    }
}

function Assert-CnMonMsixCandidateRevisionAncestor {
    param(
        [Parameter(Mandatory = $true)][string]$RepositoryRoot,
        [Parameter(Mandatory = $true)][string]$BaselineRevision,
        [Parameter(Mandatory = $true)][string]$HeadRevision
    )
    if ($BaselineRevision -cnotmatch '^[0-9a-f]{40}$' -or $HeadRevision -cnotmatch '^[0-9a-f]{40}$') {
        throw 'candidate or pull request revision is invalid'
    }
    & git -C $RepositoryRoot merge-base --is-ancestor $BaselineRevision $HeadRevision 2>$null | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'immutable candidate revision is not an ancestor of the pull request head' }
}

function Assert-CnMonMsixCompletionConfig {
    param(
        [Parameter(Mandatory = $true)][System.Collections.IDictionary]$Config,
        [Parameter(Mandatory = $true)][System.Collections.IDictionary]$Candidate
    )
    $expectedKeys = @(
        'schema_version', 'mode', 'expected_local_audit_record_sha256',
        'expected_native_helper_sha256', 'validator_revision'
    )
    $actualKeys = @($Config.Keys | ForEach-Object { [string]$_ })
    if ($actualKeys.Count -ne $expectedKeys.Count) { throw 'reviewed completion configuration has an invalid schema' }
    $keySet = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    foreach ($key in $actualKeys) {
        if (-not $keySet.Add($key) -or $key -cnotin $expectedKeys) { throw 'reviewed completion configuration has an invalid schema' }
    }
    if (($Config.schema_version -isnot [int] -and $Config.schema_version -isnot [long]) -or
        $Config.schema_version -ne 1 -or $Config.mode -isnot [string] -or
        $Config.mode -cne 'ReviewedRecord' -or
        $Config.expected_local_audit_record_sha256 -isnot [string] -or
        $Config.expected_local_audit_record_sha256 -cnotmatch '^[0-9a-f]{64}$' -or
        $Config.expected_native_helper_sha256 -isnot [string] -or
        $Config.expected_native_helper_sha256 -cne $Candidate.native_helper_sha256 -or
        $Config.validator_revision -isnot [string] -or
        $Config.validator_revision -cne 'c19e7984ec8b5bf7058a5dbc2864ac56722fbaf6' -or
        $Config.validator_revision -ceq $Candidate.packaging_revision) {
        throw 'reviewed completion configuration does not pin the immutable evidence'
    }
}

function Assert-CnMonMsixPinnedArtifactMetadata {
    param(
        [Parameter(Mandatory = $true)][object]$Artifact,
        [Parameter(Mandatory = $true)][object]$Run,
        [Parameter(Mandatory = $true)][System.Collections.IDictionary]$Candidate
    )
    if ($Artifact.id -ne $Candidate.artifact_id -or
        $Artifact.name -isnot [string] -or $Artifact.name -cne $Candidate.artifact_name -or
        $Artifact.expired -isnot [bool] -or $Artifact.expired -or
        $null -eq $Artifact.workflow_run -or $Artifact.workflow_run.id -ne $Candidate.run_id -or
        $Artifact.workflow_run.head_sha -isnot [string] -or $Artifact.workflow_run.head_sha -cne $Candidate.head_sha -or
        $Run.id -ne $Candidate.run_id -or $Run.run_attempt -ne $Candidate.run_attempt -or
        $Run.head_sha -isnot [string] -or $Run.head_sha -cne $Candidate.head_sha -or
        $Run.event -isnot [string] -or $Run.event -cne 'pull_request' -or
        $Run.status -isnot [string] -or $Run.status -cne 'completed' -or
        $Run.conclusion -isnot [string] -or $Run.conclusion -cne 'failure' -or
        $null -eq $Run.repository -or $Run.repository.full_name -isnot [string] -or
        $Run.repository.full_name -cne $Candidate.repository) {
        throw 'pinned synthetic artifact provenance did not match the reviewed candidate'
    }
}

function Assert-CnMonMsixArtifactMemberNames {
    param(
        [Parameter(Mandatory = $true)][string[]]$Names,
        [Parameter(Mandatory = $true)][System.Collections.IDictionary]$Candidate
    )
    if ($Names.Count -ne $Candidate.artifact_member_names.Count) { throw 'pinned artifact member set is incomplete' }
    $expected = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    foreach ($name in $Candidate.artifact_member_names) { $null = $expected.Add($name) }
    $seen = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    foreach ($name in $Names) {
        if ([string]::IsNullOrEmpty($name) -or $name.Contains('/') -or $name.Contains('\') -or
            [IO.Path]::IsPathRooted($name) -or -not $seen.Add($name) -or -not $expected.Contains($name)) {
            throw 'pinned artifact contains an unexpected or unsafe member'
        }
    }
    foreach ($name in $expected) {
        if (-not $seen.Contains($name)) { throw 'pinned artifact member set is incomplete' }
    }
}

function Assert-CnMonMsixPinnedInnerHashes {
    param(
        [Parameter(Mandatory = $true)][System.Collections.IDictionary]$ActualHashes,
        [Parameter(Mandatory = $true)][System.Collections.IDictionary]$Candidate
    )
    foreach ($name in $Candidate.inner_hashes.Keys) {
        if (@($ActualHashes.Keys) -cnotcontains [string]$name -or $ActualHashes[$name] -isnot [string] -or
            $ActualHashes[$name] -cne $Candidate.inner_hashes[$name]) {
            throw 'pinned artifact content does not match the immutable candidate hashes'
        }
    }
}

function Test-CnMonMsixExactDictionaryKeys {
    param(
        [Parameter(Mandatory = $true)][object]$Value,
        [Parameter(Mandatory = $true)][string[]]$ExpectedKeys
    )
    if ($Value -isnot [System.Collections.IDictionary] -or $Value.Count -ne $ExpectedKeys.Count) { return $false }
    $expected = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    $seen = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    foreach ($key in $ExpectedKeys) { $null = $expected.Add($key) }
    foreach ($key in $Value.Keys) {
        if (-not $seen.Add([string]$key) -or -not $expected.Contains([string]$key)) { return $false }
    }
    return $true
}

function Assert-CnMonMsixReviewedCompletionReport {
    param(
        [Parameter(Mandatory = $true)][System.Collections.IDictionary]$Report,
        [Parameter(Mandatory = $true)][System.Collections.IDictionary]$Candidate,
        [Parameter(Mandatory = $true)][string]$ExpectedLocalAuditRecordSha256,
        [Parameter(Mandatory = $true)][string]$ExpectedNativeHelperSha256,
        [Parameter(Mandatory = $true)][string]$ExpectedValidatorRevision,
        [Parameter(Mandatory = $true)][string]$ExpectedDesktopDriverSha256,
        [Parameter(Mandatory = $true)][string]$ExpectedSupplementSha256,
        [Parameter(Mandatory = $true)][string]$ExpectedReviewAttestationSha256
    )
    if (-not (Test-CnMonMsixExactDictionaryKeys $Report @(
            'schema_version', 'mode', 'overall', 'candidate', 'qualification_scripts', 'evidence', 'gates')) -or
        ($Report.schema_version -isnot [int] -and $Report.schema_version -isnot [long]) -or
        $Report.schema_version -ne 1 -or $Report.mode -isnot [string] -or
        $Report.mode -cne 'desktop_completion_report' -or $Report.overall -isnot [string] -or
        $Report.overall -cne 'pass' -or
        -not (Test-CnMonMsixExactDictionaryKeys $Report.candidate @(
            'run_id', 'artifact_id', 'archive_sha256', 'signed_package_sha256', 'signed_receipt_sha256',
            'certify_result_sha256', 'packaging_revision')) -or
        $Report.candidate.run_id -isnot [string] -or $Report.candidate.run_id -cne [string]$Candidate.run_id -or
        $Report.candidate.artifact_id -isnot [string] -or $Report.candidate.artifact_id -cne [string]$Candidate.artifact_id -or
        $Report.candidate.archive_sha256 -isnot [string] -or $Report.candidate.archive_sha256 -cne $Candidate.archive_sha256 -or
        $Report.candidate.signed_package_sha256 -isnot [string] -or
        $Report.candidate.signed_package_sha256 -cne $Candidate.inner_hashes['cn-mon_1.3.0.0_x64-signed.msix'] -or
        $Report.candidate.signed_receipt_sha256 -isnot [string] -or
        $Report.candidate.signed_receipt_sha256 -cne $Candidate.inner_hashes['msix-signed-receipt.json'] -or
        $Report.candidate.certify_result_sha256 -isnot [string] -or
        $Report.candidate.certify_result_sha256 -cne $Candidate.inner_hashes['msix-native-results.json'] -or
        $Report.candidate.packaging_revision -isnot [string] -or
        $Report.candidate.packaging_revision -cne $Candidate.packaging_revision -or
        -not (Test-CnMonMsixExactDictionaryKeys $Report.qualification_scripts @(
            'helper_sha256', 'helper_verification', 'screenshot_verification', 'desktop_driver_sha256', 'validator_revision')) -or
        $Report.qualification_scripts.helper_sha256 -isnot [string] -or
        $Report.qualification_scripts.helper_sha256 -cne $ExpectedNativeHelperSha256 -or
        $Report.qualification_scripts.helper_verification -isnot [string] -or
        $Report.qualification_scripts.helper_verification -cne 'reviewed_hash_attestation' -or
        $Report.qualification_scripts.screenshot_verification -isnot [string] -or
        $Report.qualification_scripts.screenshot_verification -cne 'reviewed_hash_attestation' -or
        $Report.qualification_scripts.desktop_driver_sha256 -isnot [string] -or
        $Report.qualification_scripts.desktop_driver_sha256 -cne $ExpectedDesktopDriverSha256 -or
        $Report.qualification_scripts.validator_revision -isnot [string] -or
        $Report.qualification_scripts.validator_revision -cne $ExpectedValidatorRevision -or
        -not (Test-CnMonMsixExactDictionaryKeys $Report.evidence @(
            'local_audit_record_sha256', 'completion_supplement_sha256', 'review_attestation_sha256',
            'private_evidence_bundle_sha256')) -or
        $Report.evidence.local_audit_record_sha256 -isnot [string] -or
        $Report.evidence.local_audit_record_sha256 -cne $ExpectedLocalAuditRecordSha256 -or
        $Report.evidence.completion_supplement_sha256 -isnot [string] -or
        $Report.evidence.completion_supplement_sha256 -cne $ExpectedSupplementSha256 -or
        $Report.evidence.review_attestation_sha256 -isnot [string] -or
        $Report.evidence.review_attestation_sha256 -cne $ExpectedReviewAttestationSha256 -or
        $Report.evidence.private_evidence_bundle_sha256 -isnot [string] -or
        $Report.evidence.private_evidence_bundle_sha256 -cnotmatch '^[0-9a-f]{64}$') {
        throw 'reviewed completion report did not bind the exact candidate and evidence provenance'
    }
    $expectedGates = @(
        'identity', 'package_hash', 'signature', 'account', 'package_registration', 'exact_identity',
        'installed_binary_hash', 'alias_identity', 'native_cli', 'icmp', 'history_config', 'shell_aliases',
        'upgrade', 'repair', 'uninstall_reinstall', 'conflicts', 'fail_injection', 'cleanup', 'wack'
    )
    if (-not (Test-CnMonMsixExactDictionaryKeys $Report.gates $expectedGates)) {
        throw 'reviewed completion report does not contain the exact gate set'
    }
    foreach ($gate in $expectedGates) {
        if ($Report.gates[$gate] -isnot [string] -or $Report.gates[$gate] -cne 'pass') {
            throw 'reviewed completion report does not contain 19 passing gates'
        }
    }
}

function New-CnMonMsixReviewedCompletionSummary {
    param(
        [Parameter(Mandatory = $true)][System.Collections.IDictionary]$Report,
        [Parameter(Mandatory = $true)][System.Collections.IDictionary]$Candidate
    )
    $gateCount = @($Report.gates.Keys).Count
    $passedGateCount = @($Report.gates.GetEnumerator() | Where-Object { $_.Value -ceq 'pass' }).Count
    if ($gateCount -ne 19 -or $passedGateCount -ne $gateCount) {
        throw 'reviewed completion summary requires all 19 validated gates'
    }
    return [ordered]@{
        schema_version = 1
        mode = 'desktop_reviewed_completion_summary'
        overall = 'pass'
        candidate_packaging_revision = $Candidate.packaging_revision
        gate_count = $gateCount
        passed_gate_count = $passedGateCount
        incomplete_gate_count = $gateCount - $passedGateCount
        gates = $Report.gates
        qualification_provenance = [ordered]@{
            helper_verification = $Report.qualification_scripts.helper_verification
            screenshot_verification = $Report.qualification_scripts.screenshot_verification
            validator_revision = $Report.qualification_scripts.validator_revision
        }
        evidence = [ordered]@{
            local_audit_record_sha256 = $Report.evidence.local_audit_record_sha256
            completion_supplement_sha256 = $Report.evidence.completion_supplement_sha256
            review_attestation_sha256 = $Report.evidence.review_attestation_sha256
        }
    }
}

function Assert-CnMonMsixJsonHasNoDuplicateProperties {
    param([Parameter(Mandatory = $true)][System.Text.Json.JsonElement]$Element)
    if ($Element.ValueKind -eq [System.Text.Json.JsonValueKind]::Object) {
        $names = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
        foreach ($property in $Element.EnumerateObject()) {
            if (-not $names.Add($property.Name)) { throw 'JSON contains duplicate fields' }
            Assert-CnMonMsixJsonHasNoDuplicateProperties -Element $property.Value
        }
    }
    elseif ($Element.ValueKind -eq [System.Text.Json.JsonValueKind]::Array) {
        foreach ($item in $Element.EnumerateArray()) {
            Assert-CnMonMsixJsonHasNoDuplicateProperties -Element $item
        }
    }
}

function Read-CnMonMsixCompletionConfig {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][System.Collections.IDictionary]$Candidate
    )
    $file = Get-Item -LiteralPath $Path -ErrorAction Stop
    if ($file.PSIsContainer -or ($file.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw 'reviewed completion configuration must be a regular file'
    }
    $document = $null
    try {
        $json = [IO.File]::ReadAllText($file.FullName, [Text.Encoding]::UTF8)
        $document = [System.Text.Json.JsonDocument]::Parse($json)
        Assert-CnMonMsixJsonHasNoDuplicateProperties -Element $document.RootElement
        $config = ConvertFrom-Json -InputObject $json -AsHashtable -Depth 8 -ErrorAction Stop
        Assert-CnMonMsixCompletionConfig -Config $config -Candidate $Candidate
        return $config
    }
    catch { throw 'reviewed completion configuration is invalid' }
    finally { if ($null -ne $document) { $document.Dispose() } }
}

function Get-CnMonMsixGitOutput {
    param(
        [Parameter(Mandatory = $true)][string]$RepositoryRoot,
        [Parameter(Mandatory = $true)][string[]]$Arguments
    )
    $output = @(& git -C $RepositoryRoot @Arguments 2>$null)
    if ($LASTEXITCODE -ne 0) { throw 'required repository provenance could not be read' }
    return ($output -join '')
}

function Get-CnMonMsixGitBlobBytes {
    param(
        [Parameter(Mandatory = $true)][string]$RepositoryRoot,
        [Parameter(Mandatory = $true)][string]$Revision,
        [Parameter(Mandatory = $true)][string]$Path
    )
    if ($Revision -cnotmatch '^[0-9a-f]{40}$' -or $Path -match '(^/|\\|\.\.)') {
        throw 'required repository blob provenance is invalid'
    }
    $start = [Diagnostics.ProcessStartInfo]::new('git')
    foreach ($argument in @('-C', $RepositoryRoot, 'cat-file', 'blob', "${Revision}:$Path")) { $start.ArgumentList.Add($argument) }
    $start.UseShellExecute = $false
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    $process = [Diagnostics.Process]::Start($start)
    $memory = [IO.MemoryStream]::new()
    try {
        $stderrTask = $process.StandardError.ReadToEndAsync()
        $process.StandardOutput.BaseStream.CopyTo($memory)
        $process.WaitForExit()
        $null = $stderrTask.GetAwaiter().GetResult()
        if ($process.ExitCode -ne 0) { throw 'required repository blob could not be read' }
        return ,$memory.ToArray()
    }
    finally { $memory.Dispose(); $process.Dispose() }
}

function Get-CnMonMsixGitBlobSha256 {
    param([Parameter(Mandatory = $true)][string]$RepositoryRoot, [Parameter(Mandatory = $true)][string]$Revision, [Parameter(Mandatory = $true)][string]$Path)
    $bytes = Get-CnMonMsixGitBlobBytes -RepositoryRoot $RepositoryRoot -Revision $Revision -Path $Path
    return [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($bytes)).ToLowerInvariant()
}

function Export-CnMonMsixGitBlob {
    param([Parameter(Mandatory = $true)][string]$RepositoryRoot, [Parameter(Mandatory = $true)][string]$Revision, [Parameter(Mandatory = $true)][string]$Path, [Parameter(Mandatory = $true)][string]$Destination)
    $bytes = Get-CnMonMsixGitBlobBytes -RepositoryRoot $RepositoryRoot -Revision $Revision -Path $Path
    [IO.File]::WriteAllBytes($Destination, $bytes)
}

function Assert-CnMonMsixReviewedController {
    param(
        [Parameter(Mandatory = $true)][string]$RepositoryRoot,
        [Parameter(Mandatory = $true)][string]$ControllerRevision,
        [Parameter(Mandatory = $true)][string]$HeadRevision,
        [switch]$EvidenceOnly
    )
    if ($ControllerRevision -cnotmatch '^[0-9a-f]{40}$' -or $HeadRevision -cnotmatch '^[0-9a-f]{40}$') {
        throw 'reviewed completion controller revision is not pinned'
    }
    Assert-CnMonMsixCandidateRevisionAncestor -RepositoryRoot $RepositoryRoot -BaselineRevision $ControllerRevision -HeadRevision $HeadRevision
    foreach ($path in @('scripts/complete-msix-ci.ps1', '.github/workflows/msix.yml')) {
        $headHash = Get-CnMonMsixGitBlobSha256 -RepositoryRoot $RepositoryRoot -Revision $HeadRevision -Path $path
        if ($path -ceq 'scripts/complete-msix-ci.ps1') {
            $pinnedHash = Get-CnMonMsixGitBlobSha256 -RepositoryRoot $RepositoryRoot -Revision $ControllerRevision -Path $path
            if ($headHash -cne $pinnedHash) { throw 'pull request changed the reviewed completion controller' }
        }
        else {
            $templateBytes = Get-CnMonMsixGitBlobBytes -RepositoryRoot $RepositoryRoot -Revision $ControllerRevision -Path $path
            $headBytes = Get-CnMonMsixGitBlobBytes -RepositoryRoot $RepositoryRoot -Revision $HeadRevision -Path $path
            $utf8 = [Text.UTF8Encoding]::new($false, $true)
            $template = $utf8.GetString($templateBytes)
            $head = $utf8.GetString($headBytes)
            $placeholder = 'REVIEWED_COMPLETION_CONTROLLER_REVISION'
            if ([regex]::Matches($template, [regex]::Escape($placeholder)).Count -ne 1) { throw 'reviewed workflow template has an invalid controller pin placeholder' }
            $expected = $template.Replace($placeholder, $ControllerRevision)
            if ($head -cne $expected) { throw 'pull request changed the reviewed MSIX workflow outside its controller pin' }
        }
    }
    if (-not $EvidenceOnly) { return }
    $evidencePaths = @(
        '.github/workflows/msix.yml',
        'packaging/windows/qualification/completion.json',
        'packaging/windows/qualification/local-audit-report.json',
        'packaging/windows/qualification/desktop-supplement.json',
        'packaging/windows/qualification/desktop-review-record.json'
    )
    $changed = Get-CnMonMsixChangedPaths -RepositoryRoot $RepositoryRoot -BaselineRevision $ControllerRevision -HeadRevision $HeadRevision
    foreach ($path in $changed) { if ($path -cnotin $evidencePaths) { throw 'pull request changes are outside the evidence-only completion allowlist' } }
}

function Get-CnMonMsixChangedPaths {
    param(
        [Parameter(Mandatory = $true)][string]$RepositoryRoot,
        [Parameter(Mandatory = $true)][string]$BaselineRevision,
        [Parameter(Mandatory = $true)][string]$HeadRevision
    )
    $output = Get-CnMonMsixGitOutput -RepositoryRoot $RepositoryRoot -Arguments @(
        'diff', '--no-renames', '--name-only', '-z', $BaselineRevision, $HeadRevision
    )
    return @($output.Split([char]0, [StringSplitOptions]::RemoveEmptyEntries))
}

function Get-CnMonMsixSha256 {
    param([Parameter(Mandatory = $true)][string]$Path)
    $stream = $null
    $sha = $null
    try {
        $stream = [IO.File]::OpenRead($Path)
        $sha = [Security.Cryptography.SHA256]::Create()
        return [Convert]::ToHexString($sha.ComputeHash($stream)).ToLowerInvariant()
    }
    finally {
        if ($null -ne $stream) { $stream.Dispose() }
        if ($null -ne $sha) { $sha.Dispose() }
    }
}

function Get-CnMonMsixGitHubHeaders {
    param([Parameter(Mandatory = $true)][string]$Token)
    if ([string]::IsNullOrWhiteSpace($Token)) { throw 'read-only Actions token is missing' }
    return @{
        Accept = 'application/vnd.github+json'
        Authorization = "Bearer $Token"
        'X-GitHub-Api-Version' = '2022-11-28'
    }
}

function Invoke-CnMonMsixGitHubGet {
    param(
        [Parameter(Mandatory = $true)][string]$Uri,
        [Parameter(Mandatory = $true)][string]$Token
    )
    try {
        return Invoke-RestMethod -Method Get -Uri $Uri -Headers (Get-CnMonMsixGitHubHeaders -Token $Token) -ErrorAction Stop
    }
    catch { throw 'read-only GitHub Actions metadata request failed' }
}

function Save-CnMonMsixPinnedArtifactArchive {
    param(
        [Parameter(Mandatory = $true)][string]$Uri,
        [Parameter(Mandatory = $true)][string]$Token,
        [Parameter(Mandatory = $true)][string]$Destination
    )
    $apiHandler = [System.Net.Http.HttpClientHandler]::new()
    $apiHandler.AllowAutoRedirect = $false
    $apiClient = [System.Net.Http.HttpClient]::new($apiHandler)
    $request = $null
    $response = $null
    $downloadClient = $null
    $downloadResponse = $null
    $downloadStream = $null
    $outputStream = $null
    try {
        $request = [System.Net.Http.HttpRequestMessage]::new([System.Net.Http.HttpMethod]::Get, [Uri]$Uri)
        $request.Headers.Authorization = [System.Net.Http.Headers.AuthenticationHeaderValue]::new('Bearer', $Token)
        $null = $request.Headers.Accept.ParseAdd('application/vnd.github+json')
        $null = $request.Headers.TryAddWithoutValidation('X-GitHub-Api-Version', '2022-11-28')
        $response = $apiClient.Send($request)
        if ($response.StatusCode -notin @(
            [Net.HttpStatusCode]::MovedPermanently, [Net.HttpStatusCode]::Found,
            [Net.HttpStatusCode]::SeeOther, [Net.HttpStatusCode]::TemporaryRedirect
        ) -or $null -eq $response.Headers.Location) {
            throw 'artifact download did not return a signed redirect'
        }
        $downloadUri = $response.Headers.Location
        if (-not $downloadUri.IsAbsoluteUri) { $downloadUri = [Uri]::new([Uri]$Uri, $downloadUri) }
        if ($downloadUri.Scheme -cne 'https') { throw 'artifact download redirect must use HTTPS' }

        $downloadClient = [System.Net.Http.HttpClient]::new()
        $downloadResponse = $downloadClient.GetAsync($downloadUri, [System.Net.Http.HttpCompletionOption]::ResponseHeadersRead).GetAwaiter().GetResult()
        if (-not $downloadResponse.IsSuccessStatusCode) { throw 'artifact archive download failed' }
        if ([IO.File]::Exists($Destination)) { throw 'artifact archive output already exists' }
        $downloadStream = $downloadResponse.Content.ReadAsStream()
        $outputStream = [IO.File]::Open($Destination, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
        $downloadStream.CopyTo($outputStream)
        $outputStream.Flush()
    }
    catch { throw 'pinned synthetic artifact could not be downloaded' }
    finally {
        if ($null -ne $outputStream) { $outputStream.Dispose() }
        if ($null -ne $downloadStream) { $downloadStream.Dispose() }
        if ($null -ne $downloadResponse) { $downloadResponse.Dispose() }
        if ($null -ne $downloadClient) { $downloadClient.Dispose() }
        if ($null -ne $response) { $response.Dispose() }
        if ($null -ne $request) { $request.Dispose() }
        if ($null -ne $apiClient) { $apiClient.Dispose() }
        if ($null -ne $apiHandler) { $apiHandler.Dispose() }
    }
}

function Get-CnMonMsixArtifactContents {
    param(
        [Parameter(Mandatory = $true)][string]$ArchivePath,
        [Parameter(Mandatory = $true)][System.Collections.IDictionary]$Candidate
    )
    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $archive = $null
    try {
        $archive = [IO.Compression.ZipFile]::OpenRead($ArchivePath)
        $names = @($archive.Entries | ForEach-Object { [string]$_.FullName })
        Assert-CnMonMsixArtifactMemberNames -Names $names -Candidate $Candidate
        $hashes = [Collections.Generic.Dictionary[string, string]]::new([StringComparer]::Ordinal)
        foreach ($entry in $archive.Entries) {
            $stream = $null
            $sha = $null
            try {
                $stream = $entry.Open()
                $sha = [Security.Cryptography.SHA256]::Create()
                $hashes.Add([string]$entry.FullName, [Convert]::ToHexString($sha.ComputeHash($stream)).ToLowerInvariant())
            }
            finally {
                if ($null -ne $stream) { $stream.Dispose() }
                if ($null -ne $sha) { $sha.Dispose() }
            }
        }
        Assert-CnMonMsixPinnedInnerHashes -ActualHashes $hashes -Candidate $Candidate
        return [ordered]@{ names = $names; hashes = $hashes }
    }
    catch { throw 'pinned synthetic artifact archive or member hashes are invalid' }
    finally { if ($null -ne $archive) { $archive.Dispose() } }
}

function Export-CnMonMsixPinnedBaselineFiles {
    param(
        [Parameter(Mandatory = $true)][string]$ArchivePath,
        [Parameter(Mandatory = $true)][string]$Destination,
        [Parameter(Mandatory = $true)][System.Collections.IDictionary]$Candidate
    )
    $null = New-Item -ItemType Directory -Path $Destination
    $archive = $null
    try {
        $archive = [IO.Compression.ZipFile]::OpenRead($ArchivePath)
        foreach ($name in $Candidate.inner_hashes.Keys) {
            $entry = $archive.GetEntry([string]$name)
            if ($null -eq $entry) { throw 'pinned synthetic candidate file is missing' }
            $source = $null
            $target = $null
            try {
                $source = $entry.Open()
                $target = [IO.File]::Open((Join-Path $Destination $name), [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
                $source.CopyTo($target)
            }
            finally {
                if ($null -ne $target) { $target.Dispose() }
                if ($null -ne $source) { $source.Dispose() }
            }
        }
    }
    finally { if ($null -ne $archive) { $archive.Dispose() } }
}

function Invoke-CnMonMsixReviewedCompletion {
    param(
        [Parameter(Mandatory = $true)][string]$ConfigurationPath,
        [Parameter(Mandatory = $true)][string]$ExpectedPRHeadSha,
        [Parameter(Mandatory = $true)][string]$RepositoryRoot,
        [Parameter(Mandatory = $true)][string]$ControllerRevision
    )
    $candidate = Get-CnMonMsixPinnedCandidate
    if ($env:GITHUB_ACTIONS -cne 'true' -or $env:GITHUB_EVENT_NAME -cne 'pull_request' -or
        $env:GITHUB_REPOSITORY -cne $candidate.repository) {
        throw 'reviewed completion requires the configured GitHub pull request context'
    }
    if ($ExpectedPRHeadSha -cnotmatch '^[0-9a-f]{40}$') { throw 'pull request head SHA is invalid' }
    $headRevision = Get-CnMonMsixGitOutput -RepositoryRoot $repositoryRoot -Arguments @('rev-parse', '--verify', 'HEAD')
    if ($headRevision -cne $ExpectedPRHeadSha) { throw 'checked out source does not match the pull request head SHA' }

    Assert-CnMonMsixCandidateRevisionAncestor -RepositoryRoot $repositoryRoot `
        -BaselineRevision $candidate.packaging_revision -HeadRevision $headRevision
    $config = Read-CnMonMsixCompletionConfig -Path $ConfigurationPath -Candidate $candidate
    Assert-CnMonMsixCandidateRevisionAncestor -RepositoryRoot $repositoryRoot `
        -BaselineRevision $config.validator_revision -HeadRevision $headRevision
    Assert-CnMonMsixReviewedController -RepositoryRoot $repositoryRoot `
        -ControllerRevision $ControllerRevision -HeadRevision $headRevision -EvidenceOnly
    $changedPaths = Get-CnMonMsixChangedPaths -RepositoryRoot $repositoryRoot `
        -BaselineRevision $candidate.packaging_revision -HeadRevision $headRevision
    Assert-CnMonMsixCompletionChangedPaths -ChangedPaths $changedPaths

    foreach ($relativePath in Get-CnMonMsixRequiredCompletionPaths) {
        $fullPath = Join-Path $repositoryRoot $relativePath
        $file = Get-Item -LiteralPath $fullPath -ErrorAction Stop
        if ($file.PSIsContainer -or ($file.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw 'fixed sanitized completion evidence must be regular files'
        }
    }

    $auditPath = Join-Path $repositoryRoot 'packaging/windows/qualification/local-audit-report.json'
    if ((Get-CnMonMsixSha256 -Path $auditPath) -cne $config.expected_local_audit_record_sha256) {
        throw 'sanitized local audit report does not match the reviewed SHA-256 pin'
    }

    $token = [Environment]::GetEnvironmentVariable('GH_TOKEN')
    if ([string]::IsNullOrWhiteSpace($token)) { throw 'read-only Actions token is missing' }
    $artifact = Invoke-CnMonMsixGitHubGet -Uri (
        'https://api.github.com/repos/' + $candidate.repository + '/actions/artifacts/' + $candidate.artifact_id
    ) -Token $token
    $run = Invoke-CnMonMsixGitHubGet -Uri (
        'https://api.github.com/repos/' + $candidate.repository + '/actions/runs/' + $candidate.run_id
    ) -Token $token
    Assert-CnMonMsixPinnedArtifactMetadata -Artifact $artifact -Run $run -Candidate $candidate

    if ([string]::IsNullOrWhiteSpace($env:RUNNER_TEMP) -or
        [string]::IsNullOrWhiteSpace($env:GITHUB_RUN_ID) -or
        [string]::IsNullOrWhiteSpace($env:GITHUB_RUN_ATTEMPT)) {
        throw 'reviewed completion runner paths are unavailable'
    }
    $jobRoot = Join-Path $env:RUNNER_TEMP ('cn-mon-msix-reviewed-completion-' + $env:GITHUB_RUN_ID + '-' + $env:GITHUB_RUN_ATTEMPT)
    if (Test-Path -LiteralPath $jobRoot) { throw 'reviewed completion output directory already exists' }
    $null = New-Item -ItemType Directory -Path $jobRoot
    $archivePath = Join-Path $jobRoot 'pinned-artifact.zip'
    Save-CnMonMsixPinnedArtifactArchive -Uri (
        'https://api.github.com/repos/' + $candidate.repository + '/actions/artifacts/' + $candidate.artifact_id + '/zip'
    ) -Token $token -Destination $archivePath
    if ((Get-CnMonMsixSha256 -Path $archivePath) -cne $candidate.archive_sha256) {
        throw 'downloaded synthetic artifact archive does not match the reviewed SHA-256 pin'
    }
    $null = Get-CnMonMsixArtifactContents -ArchivePath $archivePath -Candidate $candidate

    $inputDirectory = Join-Path $jobRoot 'baseline'
    Export-CnMonMsixPinnedBaselineFiles -ArchivePath $archivePath -Destination $inputDirectory -Candidate $candidate
    $outputPath = Join-Path $jobRoot 'completion-report.json'
    $validatorRevision = 'c19e7984ec8b5bf7058a5dbc2864ac56722fbaf6'
    $validatorPath = Join-Path $jobRoot 'complete-msix-qualification.ps1'
    $driverPath = Join-Path $jobRoot 'test-msix-desktop-session.ps1'
    Export-CnMonMsixGitBlob -RepositoryRoot $repositoryRoot -Revision $validatorRevision -Path 'scripts/complete-msix-qualification.ps1' -Destination $validatorPath
    Export-CnMonMsixGitBlob -RepositoryRoot $repositoryRoot -Revision $validatorRevision -Path 'scripts/test-msix-desktop-session.ps1' -Destination $driverPath
    if ((Get-CnMonMsixSha256 -Path $validatorPath) -cne 'f50a7bce0752b9b3ba36f42f1fb2a6fd84b21cd32b1a7ed9324cada5ae141e7c' -or
        (Get-CnMonMsixSha256 -Path $driverPath) -cne '42fce86a4c1e3f9bce0686a65d28b5ef5bf19e554d38e9ce4e4d2ebd6b7c5613') {
        throw 'historical desktop validator or driver bytes do not match the reviewed pin'
    }
    $supplementPath = Join-Path $repositoryRoot 'packaging/windows/qualification/desktop-supplement.json'
    $reviewPath = Join-Path $repositoryRoot 'packaging/windows/qualification/desktop-review-record.json'
    $desktopDriverSha256 = Get-CnMonMsixSha256 -Path $driverPath
    $supplementSha256 = Get-CnMonMsixSha256 -Path $supplementPath
    $reviewAttestationSha256 = Get-CnMonMsixSha256 -Path $reviewPath
    & $validatorPath -Mode ReviewedRecord `
        -BaselinePackagePath (Join-Path $inputDirectory 'cn-mon_1.3.0.0_x64-signed.msix') `
        -BaselineReceiptPath (Join-Path $inputDirectory 'msix-signed-receipt.json') `
        -BaselineCertifyResultPath (Join-Path $inputDirectory 'msix-native-results.json') `
        -SupplementPath $supplementPath `
        -ReviewAttestationPath $reviewPath `
        -LocalAuditRecordPath $auditPath `
        -ExpectedLocalAuditRecordSha256 $config.expected_local_audit_record_sha256 `
        -ExpectedNativeHelperSha256 $config.expected_native_helper_sha256 `
        -DesktopDriverPath $driverPath `
        -ExpectedValidatorRevision $config.validator_revision `
        -OutputPath $outputPath
    if (-not (Test-Path -LiteralPath $outputPath -PathType Leaf)) {
        throw 'reviewed desktop completion record did not validate'
    }
    $reportDocument = $null
    try {
        $reportJson = [IO.File]::ReadAllText($outputPath, [Text.Encoding]::UTF8)
        $reportDocument = [System.Text.Json.JsonDocument]::Parse($reportJson)
        Assert-CnMonMsixJsonHasNoDuplicateProperties -Element $reportDocument.RootElement
        $report = ConvertFrom-Json -InputObject $reportJson -AsHashtable -Depth 12 -ErrorAction Stop
        Assert-CnMonMsixReviewedCompletionReport -Report $report -Candidate $candidate `
            -ExpectedLocalAuditRecordSha256 $config.expected_local_audit_record_sha256 `
            -ExpectedNativeHelperSha256 $config.expected_native_helper_sha256 `
            -ExpectedValidatorRevision $config.validator_revision `
            -ExpectedDesktopDriverSha256 $desktopDriverSha256 `
            -ExpectedSupplementSha256 $supplementSha256 `
            -ExpectedReviewAttestationSha256 $reviewAttestationSha256
    }
    catch { throw 'reviewed desktop completion report did not match the pinned evidence' }
    finally { if ($null -ne $reportDocument) { $reportDocument.Dispose() } }
    $publicSummary = New-CnMonMsixReviewedCompletionSummary -Report $report -Candidate $candidate
    $publicSummaryPath = Join-Path $jobRoot 'reviewed-completion-summary.json'
    $publicSummaryJson = ConvertTo-Json -InputObject $publicSummary -Depth 8
    [IO.File]::WriteAllText($publicSummaryPath, $publicSummaryJson + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
    if ([string]::IsNullOrWhiteSpace($env:GITHUB_OUTPUT)) { throw 'GitHub step output path is unavailable' }
    $stepOutputs = @(
        'overall=pass',
        "passed_gate_count=$($publicSummary.passed_gate_count)",
        "gate_count=$($publicSummary.gate_count)",
        'provenance=reviewed_hash_attestation'
    ) -join [Environment]::NewLine
    [IO.File]::AppendAllText($env:GITHUB_OUTPUT, $stepOutputs + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
    $summary = @(
        '## MSIX reviewed desktop completion',
        '',
        '- Result: **PASS**',
        "- Gates: **$($publicSummary.passed_gate_count)/$($publicSummary.gate_count) passed**",
        '- Candidate packaging revision: `b6c874d7b5de8257dde37685f0c3bfeb79b50b14`',
        '- Helper provenance: `reviewed_hash_attestation`',
        '- Screenshot provenance: `reviewed_hash_attestation`',
        '- CI validated the pinned sanitized local audit, supplement, and review records; it did not read or decode private helper or screenshot bytes.'
    ) -join [Environment]::NewLine
    if (-not [string]::IsNullOrWhiteSpace($env:GITHUB_STEP_SUMMARY)) {
        [IO.File]::AppendAllText($env:GITHUB_STEP_SUMMARY, $summary + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
    }
    Write-Host "PASS reviewed desktop completion record validated ($($publicSummary.passed_gate_count)/$($publicSummary.gate_count) gates; reviewed_hash_attestation provenance)"
}

if ($MyInvocation.InvocationName -ne '.') {
    $root = if (-not [string]::IsNullOrWhiteSpace($RepositoryRoot)) { $RepositoryRoot } elseif (-not [string]::IsNullOrWhiteSpace($env:GITHUB_WORKSPACE)) { $env:GITHUB_WORKSPACE } else { Split-Path -Parent $PSScriptRoot }
    if ($Mode -eq 'SelectRoute') {
        Assert-CnMonMsixReviewedController -RepositoryRoot $root -ControllerRevision $ControllerRevision -HeadRevision $PullRequestHeadRevision
    }
    if ($Mode -eq 'SelectRoute') {
        $path = if ([IO.Path]::IsPathRooted($ConfigurationPath)) { $ConfigurationPath } else { Join-Path $root $ConfigurationPath }
        $route = Get-CnMonMsixRoute -ConfigurationPath $path -EventName $env:GITHUB_EVENT_NAME `
            -RepositoryRoot $root -PullRequestBaseRevision $PullRequestBaseRevision -PullRequestHeadRevision $PullRequestHeadRevision
        if ([string]::IsNullOrWhiteSpace($env:GITHUB_OUTPUT)) { throw 'GitHub step output path is unavailable' }
        [IO.File]::AppendAllText($env:GITHUB_OUTPUT, "mode=$route$([Environment]::NewLine)", [Text.UTF8Encoding]::new($false))
        Write-Host "Selected pull request qualification route: $route"
    }
    else {
        $path = if ([IO.Path]::IsPathRooted($ConfigurationPath)) { $ConfigurationPath } else { Join-Path $root $ConfigurationPath }
        Invoke-CnMonMsixReviewedCompletion -ConfigurationPath $path -ExpectedPRHeadSha $env:EXPECTED_PR_HEAD_SHA `
            -RepositoryRoot $root -ControllerRevision $ControllerRevision
    }
}
