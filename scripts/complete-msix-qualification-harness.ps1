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

function Get-Sha256Hex {
    param([string]$Text)
    $sha = [Security.Cryptography.SHA256]::Create()
    try { return [Convert]::ToHexString($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($Text))).ToLowerInvariant() }
    finally { $sha.Dispose() }
}

function Add-TestPngChunk {
    param([Collections.Generic.List[byte]]$Output, [string]$Name, [byte[]]$Data)
    $typeBytes = [Text.Encoding]::ASCII.GetBytes($Name)
    foreach ($value in [BitConverter]::GetBytes([uint32]$Data.Length)[3..0]) { $Output.Add($value) }
    $Output.AddRange($typeBytes)
    $Output.AddRange($Data)
    $crcBytes = [byte[]]::new($typeBytes.Length + $Data.Length)
    [Array]::Copy($typeBytes, 0, $crcBytes, 0, $typeBytes.Length)
    [Array]::Copy($Data, 0, $crcBytes, $typeBytes.Length, $Data.Length)
    $crc = Get-CnMonMsixPngCrc32 -Bytes $crcBytes -Offset 0 -Count $crcBytes.Length
    foreach ($shift in @(24, 16, 8, 0)) { $Output.Add([byte](($crc -shr $shift) -band 255)) }
}

function New-TestRgbPng {
    param([byte[]]$Header, [byte[]]$CompressedData)
    $output = [Collections.Generic.List[byte]]::new()
    $output.AddRange([byte[]](137, 80, 78, 71, 13, 10, 26, 10))
    Add-TestPngChunk -Output $output -Name 'IHDR' -Data $Header
    Add-TestPngChunk -Output $output -Name 'IDAT' -Data $CompressedData
    Add-TestPngChunk -Output $output -Name 'IEND' -Data ([byte[]]::new(0))
    return ,$output.ToArray()
}

$validatorPath = Join-Path $PSScriptRoot 'complete-msix-qualification.ps1'
if (-not (Test-Path -LiteralPath $validatorPath -PathType Leaf)) { throw 'completion validator pure function is missing' }
$parseErrors = $null
$validatorAst = [System.Management.Automation.Language.Parser]::ParseFile($validatorPath, [ref]$null, [ref]$parseErrors)
if ($parseErrors.Count -gt 0) { throw 'completion validator source did not parse' }
$requiredFunctions = @(
    'Get-CnMonMsixCompletionScreenshotNames',
    'Assert-CnMonMsixScreenshotDirectory',
    'Assert-CnMonMsixJsonHasNoDuplicateProperties',
    'ConvertFrom-CnMonMsixStrictJson',
    'Get-CnMonMsixPngCrc32',
    'Read-CnMonMsixBoundedBytes',
    'Assert-CnMonMsixPngPixelStream',
    'Assert-CnMonMsixWindowsPngDecodesFully',
    'Get-CnMonMsixPngObservation',
    'Get-CnMonMsixFileSha256',
    'Get-CnMonMsixNativeHelperSha256',
    'Test-CnMonMsixExactKeys',
    'Test-CnMonMsixJsonInteger',
    'Assert-CnMonMsixScreenshotHashGroups',
    'Get-CnMonMsixDesktopCompletionReport',
    'Get-CnMonMsixReviewedCompletionReport'
)
$functionAsts = @($validatorAst.FindAll({
    param($candidate)
    $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
        $requiredFunctions -contains $candidate.Name
}, $true))
if (@($requiredFunctions | Where-Object { $_ -notin @($functionAsts | ForEach-Object Name) }).Count -gt 0) {
    throw 'completion validator pure function is missing'
}
. ([scriptblock]::Create(($functionAsts | ForEach-Object { $_.Extent.Text }) -join [Environment]::NewLine))

$parsed = ConvertFrom-CnMonMsixStrictJson -Json '{"schema_version":1,"nested":{"ok":true}}'
Assert-True ($parsed.nested.ok -eq $true) 'strict JSON reader changed a valid object'
Assert-Throws { ConvertFrom-CnMonMsixStrictJson -Json '{"schema_version":1,"schema_version":2}' } 'duplicate JSON fields were accepted'
Assert-Throws { ConvertFrom-CnMonMsixStrictJson -Json '{not-json}' } 'malformed JSON was accepted'
Write-Host 'PASS strict JSON parsing rejects duplicate and malformed fields'

Assert-CnMonMsixScreenshotHashGroups -Screenshots @(
    [pscustomobject]@{ name = 'repair-progress.png'; sha256 = 'd' * 64 },
    [pscustomobject]@{ name = 'repair-complete.png'; sha256 = 'd' * 64 }
)
foreach ($case in @(
    @{ Names = @('alias-off-settings.png', 'alias-on-settings.png'); Message = 'alias screenshots' },
    @{ Names = @('01-cli-help.png', '02-icmp-table.png'); Message = 'Store screenshots' },
    @{ Names = @('repair-start.png', 'repair-progress.png', 'repair-complete.png'); Message = 'three repair screenshots' },
    @{ Names = @('alias-off-settings.png', 'repair-start.png'); Message = 'an unrelated screenshot pair' }
)) {
    $duplicateScreenshots = @($case.Names | ForEach-Object {
        [pscustomobject]@{ name = $_; sha256 = 'd' * 64 }
    })
    Assert-Throws { Assert-CnMonMsixScreenshotHashGroups -Screenshots $duplicateScreenshots } `
        "$($case.Message) with duplicate hashes were accepted"
}
Write-Host 'PASS only the repair progress/completion screenshot pair may share a hash'

$directoryTemp = Join-Path ([IO.Path]::GetTempPath()) ('cn-mon-msix-evidence-dir-' + [Guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $directoryTemp
try {
    foreach ($name in Get-CnMonMsixCompletionScreenshotNames) {
        [IO.File]::WriteAllBytes((Join-Path $directoryTemp $name), [byte[]](1))
    }
    Assert-CnMonMsixScreenshotDirectory -Directory $directoryTemp
    [IO.File]::WriteAllBytes((Join-Path $directoryTemp 'desktop-review-record.json'), [byte[]](2))
    Assert-Throws { Assert-CnMonMsixScreenshotDirectory -Directory $directoryTemp } 'review sidecar inside the private screenshot directory was accepted'
    [IO.File]::Delete((Join-Path $directoryTemp 'desktop-review-record.json'))
    $null = New-Item -ItemType Directory -Path (Join-Path $directoryTemp 'nested')
    Assert-Throws { Assert-CnMonMsixScreenshotDirectory -Directory $directoryTemp } 'nested output directory inside the private screenshot directory was accepted'
}
finally { Remove-Item -LiteralPath $directoryTemp -Recurse -Force }
Write-Host 'PASS private screenshot directory excludes review and audit sidecars'

$pngTemp = Join-Path ([IO.Path]::GetTempPath()) ('cn-mon-msix-completion-png-' + [Guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $pngTemp
try {
    $pngPath = Join-Path $pngTemp 'alias-off-settings.png'
    $pngBytes = [Convert]::FromBase64String('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGP4z8AAAAMBAQDJ/pLvAAAAAElFTkSuQmCC')
    [IO.File]::WriteAllBytes($pngPath, $pngBytes)
    $pngObservation = Get-CnMonMsixPngObservation -Directory $pngTemp -Name 'alias-off-settings.png'
    Assert-True ($pngObservation.width -eq 1 -and $pngObservation.height -eq 1) 'PNG dimensions were not read from the file bytes'
    $expectedPngSha = [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($pngBytes)).ToLowerInvariant()
    Assert-True ($pngObservation.sha256 -ceq $expectedPngSha) 'PNG digest was not computed from original file bytes'
    $badCrcBytes = [byte[]]::new($pngBytes.Length)
    [Array]::Copy($pngBytes, $badCrcBytes, $pngBytes.Length)
    $badCrcBytes[30] = [byte]($badCrcBytes[30] -bxor 1)
    [IO.File]::WriteAllBytes($pngPath, $badCrcBytes)
    Assert-Throws { Get-CnMonMsixPngObservation -Directory $pngTemp -Name 'alias-off-settings.png' } 'PNG with a broken chunk checksum was accepted'

    $header = [byte[]]$pngBytes[16..28]
    $compressedData = [byte[]]$pngBytes[41..52]
    $corruptData = [byte[]]::new($compressedData.Length)
    [Array]::Copy($compressedData, $corruptData, $compressedData.Length)
    $corruptData[0] = 0
    $corruptPng = New-TestRgbPng -Header $header -CompressedData $corruptData
    [IO.File]::WriteAllBytes($pngPath, $corruptPng)
    Assert-Throws { Get-CnMonMsixPngObservation -Directory $pngTemp -Name 'alias-off-settings.png' } 'corrupt zlib image data with valid PNG CRCs was accepted'

    $truncatedData = [byte[]]$compressedData[0..5]
    $truncatedPng = New-TestRgbPng -Header $header -CompressedData $truncatedData
    [IO.File]::WriteAllBytes($pngPath, $truncatedPng)
    Assert-Throws { Get-CnMonMsixPngObservation -Directory $pngTemp -Name 'alias-off-settings.png' } 'truncated zlib image data with valid PNG CRCs was accepted'

    $emptyDataPath = Join-Path $pngTemp '01-cli-help.png'
    $emptyDataPng = [Convert]::FromBase64String('iVBORw0KGgoAAAANSUhEUgAABVYAAAMACAYAAADPPjzCAAAAAElEQVQ1rwYeAAAAAElFTkSuQmCC')
    Assert-True ($emptyDataPng.Length -eq 57) 'header-only PNG regression fixture changed'
    [IO.File]::WriteAllBytes($emptyDataPath, $emptyDataPng)
    Assert-Throws { Get-CnMonMsixPngObservation -Directory $pngTemp -Name '01-cli-help.png' } 'empty image data with valid PNG chunk checksums was accepted'
}
finally { Remove-Item -LiteralPath $pngTemp -Recurse -Force -ErrorAction SilentlyContinue }
Write-Host 'PASS screenshot file hash, dimensions, and decoded pixel-stream checks'

$oversizedPath = Join-Path ([IO.Path]::GetTempPath()) ('cn-mon-msix-completion-size-' + [Guid]::NewGuid().ToString('N'))
$oversizedStream = [IO.File]::Create($oversizedPath)
try { $oversizedStream.SetLength(10485761) }
finally { $oversizedStream.Dispose() }
try { Assert-Throws { Read-CnMonMsixBoundedBytes -Path $oversizedPath -MinimumLength 1 -MaximumLength 10485760 } 'oversized input was accepted' }
finally { Remove-Item -LiteralPath $oversizedPath -Force -ErrorAction SilentlyContinue }
Write-Host 'PASS bounded file reads check size before allocating'

$helperPathTemp = Join-Path ([IO.Path]::GetTempPath()) ('cn-mon-msix-helper-' + [Guid]::NewGuid().ToString('N'))
$helperBytes = [Text.Encoding]::UTF8.GetBytes('helper byte fixture')
try {
    [IO.File]::WriteAllBytes($helperPathTemp, $helperBytes)
    $expectedFixtureHelperSha = [Convert]::ToHexString([Security.Cryptography.SHA256]::HashData($helperBytes)).ToLowerInvariant()
    Assert-True ((Get-CnMonMsixNativeHelperSha256 -Path $helperPathTemp) -ceq $expectedFixtureHelperSha) 'local helper byte hash changed'
    Assert-Throws { Get-CnMonMsixNativeHelperSha256 -Path ($helperPathTemp + '.missing') } 'provided missing helper path was accepted'
    Assert-Throws { Get-CnMonMsixNativeHelperSha256 -Path '' } 'LocalAudit accepted an omitted helper path'
}
finally { if ([IO.File]::Exists($helperPathTemp)) { [IO.File]::Delete($helperPathTemp) } }
Write-Host 'PASS LocalAudit requires actual helper bytes and rejects a missing file'

$pngFunctions = @($validatorAst.FindAll({
    param($candidate)
    $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
        $candidate.Name -in @('Read-CnMonMsixBoundedBytes', 'Get-CnMonMsixPngObservation', 'Get-CnMonMsixInputFile')
}, $true))
foreach ($functionAst in $pngFunctions) {
    Assert-True (-not $functionAst.Extent.Text.Contains('ReadAllBytes')) "unbounded file read remains in $($functionAst.Name)"
}
Write-Host 'PASS file-size checks precede bounded input allocation'

$packageSha = 'd782d72374fe6bef2ca666e6cf64039283d0b1b24f4c9aa22cd940956873642f'
$receiptSha = '7f7ddda42b87b7611fe5107aeb519e3cf0e04e5e2870a7dc8374d6c7b74db55b'
$certifySha = 'ebdaa5f3b456afa5e183910b25e80385e3e1d0d86c8db7fee7d7847c9a99971f'
$archiveSha = 'ed18d14a6a4e17526bd73d47f68336d9ee45ead71c15e61f106fba257d461818'
$packagingRevision = 'b6c874d7b5de8257dde37685f0c3bfeb79b50b14'
$helperSha = '1111111111111111111111111111111111111111111111111111111111111111'
$driverSha = '2222222222222222222222222222222222222222222222222222222222222222'
$validatorRevision = '3333333333333333333333333333333333333333'
$qualificationGates = @(
    'identity', 'package_hash', 'signature', 'account', 'package_registration', 'exact_identity',
    'installed_binary_hash', 'alias_identity', 'native_cli', 'icmp', 'history_config', 'shell_aliases',
    'upgrade', 'repair', 'uninstall_reinstall', 'conflicts', 'fail_injection', 'cleanup', 'wack'
)
$receipt = [ordered]@{
    schema_version = 1
    purpose = 'candidate'
    package_sha256 = $packageSha
    packaging_revision = $packagingRevision
}
$gates = [ordered]@{}
foreach ($gate in $qualificationGates) { $gates[$gate] = 'pass' }
$gates.repair = 'incomplete'
$gates.conflicts = 'incomplete'
$certifyResult = [ordered]@{
    schema_version = 2
    mode = 'Certify'
    overall = 'incomplete'
    evidence = [ordered]@{
        signed_package_sha256 = $packageSha
        signed_receipt_sha256 = $receiptSha
        packaging_revision = $packagingRevision
    }
    gates = $gates
}
$candidate = [ordered]@{
    run_id = '38866515555'
    artifact_id = '12031747143'
    archive_sha256 = $archiveSha
    signed_package_sha256 = $packageSha
    signed_receipt_sha256 = $receiptSha
    certify_result_sha256 = $certifySha
    packaging_revision = $packagingRevision
}
$screenshots = @(
    [ordered]@{ name = 'alias-off-settings.png'; sha256 = 'a' * 64; width = 900; height = 700 },
    [ordered]@{ name = 'alias-on-settings.png'; sha256 = 'b' * 64; width = 900; height = 700 },
    [ordered]@{ name = 'repair-start.png'; sha256 = 'c' * 64; width = 900; height = 700 },
    [ordered]@{ name = 'repair-progress.png'; sha256 = 'd' * 64; width = 900; height = 700 },
    [ordered]@{ name = 'repair-complete.png'; sha256 = 'd' * 64; width = 900; height = 700 },
    [ordered]@{ name = '01-cli-help.png'; sha256 = 'f' * 64; width = 1366; height = 768 },
    [ordered]@{ name = '02-icmp-table.png'; sha256 = '1' * 64; width = 1366; height = 768 },
    [ordered]@{ name = '03-json-tcp.png'; sha256 = '2' * 64; width = 1366; height = 768 },
    [ordered]@{ name = '04-history-list.png'; sha256 = '3' * 64; width = 1366; height = 768 },
    [ordered]@{ name = '05-history-detail.png'; sha256 = '4' * 64; width = 1366; height = 768 }
)
$observations = [ordered]@{
    ordinary_user = $true
    medium_integrity = $true
    interactive_session = $true
    alias = [ordered]@{
        off = [ordered]@{
            settings_opened = $true; settings_verified = $true
            pwsh_failure = $true; pwsh_success = $false; cmd_failure = $true; cmd_success = $false
        }
        on = [ordered]@{
            settings_opened = $true; settings_verified = $true
            pwsh_failure = $false; pwsh_success = $true; cmd_failure = $false; cmd_success = $true
            packaged_process_identity_verified = $true
        }
        path_unchanged = $true
    }
    repair = [ordered]@{
        activation_observed = $true
        completion_observed = $true
        data_before_sha256 = '5' * 64
        data_after_sha256 = '5' * 64
        readback_verified = $true
    }
    screenshots = $screenshots
    desktop_cleanup_passed = $true
}
$supplement = [ordered]@{
    schema_version = 1
    mode = 'desktop_completion_supplement'
    overall = 'incomplete'
    candidate = $candidate
    qualification_scripts = [ordered]@{
        helper_sha256 = $helperSha
        desktop_driver_sha256 = $driverSha
        validator_revision = $validatorRevision
    }
    observations = $observations
}
$supplementJson = ConvertTo-Json -InputObject $supplement -Depth 12 -Compress
$supplementSha = Get-Sha256Hex $supplementJson
$attestation = [ordered]@{
    schema_version = 1
    mode = 'desktop_completion_review'
    authority = 'independent_root_reviewer'
    supplement_sha256 = $supplementSha
    screenshot_review_completed = $true
    reviewed_screenshot_sha256s = @($screenshots | ForEach-Object sha256)
    alias_off_on_reviewed = $true
    repair_activation_completion_reviewed = $true
    private_evidence_bundle_sha256 = '6' * 64
    independent_sol_review = [ordered]@{
        role = 'gpt-6.1-sol'
        reviewed_at_utc = '20261016T120000Z'
        reference = 'review:abcdef0123456789'
        status = 'resolved'
    }
}
$attestationJson = ConvertTo-Json -InputObject $attestation -Depth 6 -Compress
$attestationSha = Get-Sha256Hex $attestationJson
$actualScreenshots = @($screenshots | ForEach-Object {
    [pscustomobject]@{ name = $_.name; sha256 = $_.sha256; width = $_.width; height = $_.height }
})

$validArgs = @{
    PackageSha256 = $packageSha
    ReceiptSha256 = $receiptSha
    CertifyResultSha256 = $certifySha
    Receipt = $receipt
    CertifyResult = $certifyResult
    Supplement = $supplement
    ReviewAttestation = $attestation
    SupplementSha256 = $supplementSha
    ReviewAttestationSha256 = $attestationSha
    ExpectedNativeHelperSha256 = $helperSha
    ActualNativeHelperSha256 = $helperSha
    DesktopDriverSha256 = $driverSha
    ExpectedValidatorRevision = $validatorRevision
    ActualScreenshots = $actualScreenshots
}
$report = Get-CnMonMsixDesktopCompletionReport @validArgs
Assert-True ($report.overall -ceq 'pass') 'valid independent desktop evidence did not complete the separate report'
Assert-True ($report.mode -ceq 'desktop_local_audit_record') 'local audit record mode changed'
Assert-True ($report.qualification_scripts.helper_verification -ceq 'local_bytes_verified') 'local audit did not prove helper bytes were present'
Assert-True ($report.qualification_scripts.screenshot_verification -ceq 'local_bytes_verified') 'local audit did not prove screenshot bytes were decoded'
Assert-True ($report.evidence.private_evidence_bundle_sha256 -ceq $attestation.private_evidence_bundle_sha256) 'local audit lost the reviewed private evidence bundle binding'
Assert-True ($report.evidence.completion_supplement_sha256 -ceq $supplementSha) 'completion report lost its supplement byte binding'
Assert-True ((@($report.gates.Keys).Count) -eq 19) 'completion report gate set changed'
Assert-True ($report.candidate.packaging_revision -ceq $packagingRevision) 'completion report changed the candidate packaging revision'
Assert-True ($report.qualification_scripts.helper_verification -ceq 'local_bytes_verified') 'locally hashed helper provenance was not recorded'
Assert-True ($report.gates.repair -ceq 'pass' -and $report.gates.conflicts -ceq 'pass') 'completion report did not promote the two permitted gates'
Assert-True ($attestation.reviewed_screenshot_sha256s.Count -eq 10 -and
    @($attestation.reviewed_screenshot_sha256s | Where-Object { $_ -ceq ('d' * 64) }).Count -eq 2) `
    'review attestation did not preserve both repair screenshot hash entries'
$badReview = [ordered]@{} + $attestation
$reviewedHashes = [Collections.Generic.List[string]]::new()
foreach ($hash in $attestation.reviewed_screenshot_sha256s) { $reviewedHashes.Add($hash) }
$null = $reviewedHashes.Remove('d' * 64)
$badReview.reviewed_screenshot_sha256s = $reviewedHashes.ToArray()
$bad = @{} + $validArgs; $bad.ReviewAttestation = $badReview
Assert-Throws { Get-CnMonMsixDesktopCompletionReport @bad } `
    'review attestation that collapsed the two repair hashes to a nine-entry list was accepted'
foreach ($gate in $qualificationGates | Where-Object { $_ -notin @('repair', 'conflicts') }) {
    Assert-True ($report.gates[$gate] -ceq $certifyResult.gates[$gate]) "completion report changed unrelated gate '$gate'"
}
Write-Host 'PASS candidate-bound completion report preserves all non-desktop gates'

$bad = @{} + $validArgs
$bad.PackageSha256 = '9' * 64
Assert-Throws { Get-CnMonMsixDesktopCompletionReport @bad } 'changed baseline package bytes were accepted'
Write-Host 'PASS changed candidate package bytes are rejected'

$badCertify = [ordered]@{} + $certifyResult
$badCertify.mode = 'Hosted'
$bad = @{} + $validArgs; $bad.CertifyResult = $badCertify
Assert-Throws { Get-CnMonMsixDesktopCompletionReport @bad } 'Hosted result was relabeled as final Certify output'
$badCertify = [ordered]@{} + $certifyResult
$badCertify.overall = 'pass'
$bad = @{} + $validArgs; $bad.CertifyResult = $badCertify
Assert-Throws { Get-CnMonMsixDesktopCompletionReport @bad } 'baseline Certify result with overall pass was accepted'
Write-Host 'PASS only the immutable incomplete Certify candidate is eligible'

$badSupplement = [ordered]@{} + $supplement
$badSupplement.schema_version = 9
$bad = @{} + $validArgs; $bad.Supplement = $badSupplement
Assert-Throws { Get-CnMonMsixDesktopCompletionReport @bad } 'unknown supplement schema was accepted'
Write-Host 'PASS unknown supplement schema is rejected'

$badAlias = [ordered]@{} + $supplement
$badAlias.observations = [ordered]@{} + $observations
$badAlias.observations.alias = [ordered]@{} + $observations.alias
$badAlias.observations.alias.Remove('on')
$bad = @{} + $validArgs; $bad.Supplement = $badAlias
Assert-Throws { Get-CnMonMsixDesktopCompletionReport @bad } 'missing On alias observation was accepted'
Write-Host 'PASS both alias states are required'

$badRepair = [ordered]@{} + $supplement
$badRepair.observations = [ordered]@{} + $observations
$badRepair.observations.repair = [ordered]@{} + $observations.repair
$badRepair.observations.repair.completion_observed = $false
$bad = @{} + $validArgs; $bad.Supplement = $badRepair
Assert-Throws { Get-CnMonMsixDesktopCompletionReport @bad } 'unobserved Repair completion was accepted'
$badRepair.observations.repair.activation_observed = $false
$bad = @{} + $validArgs; $bad.Supplement = $badRepair
Assert-Throws { Get-CnMonMsixDesktopCompletionReport @bad } 'unobserved Repair activation was accepted'
Write-Host 'PASS Repair requires observed activation and completion'

$badData = [ordered]@{} + $supplement
$badData.observations = [ordered]@{} + $observations
$badData.observations.repair = [ordered]@{} + $observations.repair
$badData.observations.repair.data_after_sha256 = '6' * 64
$bad = @{} + $validArgs; $bad.Supplement = $badData
Assert-Throws { Get-CnMonMsixDesktopCompletionReport @bad } 'Repair data hash change was accepted'
Write-Host 'PASS Repair data must survive the operation and readback'

$badCleanup = [ordered]@{} + $supplement
$badCleanup.observations = [ordered]@{} + $observations
$badCleanup.observations.desktop_cleanup_passed = $false
$bad = @{} + $validArgs; $bad.Supplement = $badCleanup
Assert-Throws { Get-CnMonMsixDesktopCompletionReport @bad } 'incomplete desktop cleanup was accepted'
Write-Host 'PASS incomplete desktop cleanup blocks completion'

$badGates = [ordered]@{} + $certifyResult
$badGates.gates = [ordered]@{} + $certifyResult.gates
$badGates.gates.native_cli = 'incomplete'
$bad = @{} + $validArgs; $bad.CertifyResult = $badGates
Assert-Throws { Get-CnMonMsixDesktopCompletionReport @bad } 'completion accepted an unrelated baseline gate change'
$badSupplementPromotion = [ordered]@{} + $supplement
$badSupplementPromotion.promoted_gates = [ordered]@{ native_cli = 'pass' }
$bad = @{} + $validArgs; $bad.Supplement = $badSupplementPromotion
Assert-Throws { Get-CnMonMsixDesktopCompletionReport @bad } 'supplement arbitrary gate promotion was accepted'
Write-Host 'PASS only Repair and conflicts can be supplemented'

$badCandidate = [ordered]@{} + $supplement
$badCandidate.candidate = [ordered]@{} + $candidate
$badCandidate.candidate.signed_receipt_sha256 = '8' * 64
$bad = @{} + $validArgs; $bad.Supplement = $badCandidate
Assert-Throws { Get-CnMonMsixDesktopCompletionReport @bad } 'mismatched baseline receipt candidate was accepted'
Write-Host 'PASS all baseline evidence remains candidate-bound'

$badScripts = [ordered]@{} + $supplement
$badScripts.qualification_scripts = [ordered]@{} + $supplement.qualification_scripts
$badScripts.qualification_scripts.helper_sha256 = '9' * 64
$bad = @{} + $validArgs; $bad.Supplement = $badScripts
Assert-Throws { Get-CnMonMsixDesktopCompletionReport @bad } 'mismatched helper bytes were accepted'
$badScripts = [ordered]@{} + $supplement
$badScripts.qualification_scripts = [ordered]@{} + $supplement.qualification_scripts
$badScripts.qualification_scripts.validator_revision = '4' * 40
$bad = @{} + $validArgs; $bad.Supplement = $badScripts
Assert-Throws { Get-CnMonMsixDesktopCompletionReport @bad } 'mismatched validator revision was accepted'
Write-Host 'PASS helper, driver, and validator revision are separately bound'

$localAuditSha = 'a' * 64
$reviewedArgs = @{
    PackageSha256 = $packageSha
    ReceiptSha256 = $receiptSha
    CertifyResultSha256 = $certifySha
    Receipt = $receipt
    CertifyResult = $certifyResult
    Supplement = $supplement
    ReviewAttestation = $attestation
    SupplementSha256 = $supplementSha
    ReviewAttestationSha256 = $attestationSha
    LocalAuditRecord = $report
    LocalAuditRecordSha256 = $localAuditSha
    ExpectedLocalAuditRecordSha256 = $localAuditSha
    ExpectedNativeHelperSha256 = $helperSha
    DesktopDriverSha256 = $driverSha
    ExpectedValidatorRevision = $validatorRevision
}
$reviewedReport = Get-CnMonMsixReviewedCompletionReport @reviewedArgs
Assert-True ($reviewedReport.mode -ceq 'desktop_completion_report') 'ReviewedRecord output mode changed'
Assert-True ($reviewedReport.qualification_scripts.helper_verification -ceq 'reviewed_hash_attestation') 'CI helper provenance claimed local byte access'
Assert-True ($reviewedReport.qualification_scripts.screenshot_verification -ceq 'reviewed_hash_attestation') 'CI screenshot provenance claimed local byte access'
Assert-True ($reviewedReport.evidence.local_audit_record_sha256 -ceq $localAuditSha) 'ReviewedRecord output lost its pinned local audit report binding'
$badHelperHash = @{} + $validArgs
$badHelperHash.ExpectedNativeHelperSha256 = '9' * 64
Assert-Throws { Get-CnMonMsixDesktopCompletionReport @badHelperHash } 'supplement helper hash not matching the pinned expectation was accepted'
$badHelperHash = @{} + $validArgs
$badHelperHash.ActualNativeHelperSha256 = ''
Assert-Throws { Get-CnMonMsixDesktopCompletionReport @badHelperHash } 'LocalAudit accepted an omitted native helper byte hash'
$badHelperHash = @{} + $validArgs
$badHelperHash.ActualNativeHelperSha256 = '8' * 64
Assert-Throws { Get-CnMonMsixDesktopCompletionReport @badHelperHash } 'local helper bytes not matching the pinned expectation were accepted'
Assert-Throws {
    $badHelperFormat = @{} + $validArgs
    $badHelperFormat.ExpectedNativeHelperSha256 = ('A' * 64)
    Get-CnMonMsixDesktopCompletionReport @badHelperFormat
} 'non-lowercase pinned helper digest was accepted'
Write-Host 'PASS LocalAudit verifies private bytes and ReviewedRecord reports attested provenance'

$badAudit = [ordered]@{} + $report
$badAudit.qualification_scripts = [ordered]@{} + $report.qualification_scripts
$badAudit.qualification_scripts.helper_verification = 'reviewed_hash_attestation'
$bad = @{} + $reviewedArgs; $bad.LocalAuditRecord = $badAudit
Assert-Throws { Get-CnMonMsixReviewedCompletionReport @bad } 'ReviewedRecord accepted an audit without local helper byte verification'
$badAudit = [ordered]@{} + $report
$badAudit.qualification_scripts = [ordered]@{} + $report.qualification_scripts
$badAudit.qualification_scripts.screenshot_verification = 'reviewed_hash_attestation'
$bad = @{} + $reviewedArgs; $bad.LocalAuditRecord = $badAudit
Assert-Throws { Get-CnMonMsixReviewedCompletionReport @bad } 'ReviewedRecord accepted an audit without local screenshot byte verification'
$bad = @{} + $reviewedArgs; $bad.ExpectedLocalAuditRecordSha256 = 'b' * 64
Assert-Throws { Get-CnMonMsixReviewedCompletionReport @bad } 'ReviewedRecord accepted an unpinned local audit report'
$badAudit = [ordered]@{} + $report
$badAudit.gates = [ordered]@{} + $report.gates
$badAudit.gates.wack = 'incomplete'
$bad = @{} + $reviewedArgs; $bad.LocalAuditRecord = $badAudit
Assert-Throws { Get-CnMonMsixReviewedCompletionReport @bad } 'ReviewedRecord accepted a local audit with a nonpassing gate'
$bad = @{} + $reviewedArgs; $bad.ExpectedNativeHelperSha256 = '9' * 64
Assert-Throws { Get-CnMonMsixReviewedCompletionReport @bad } 'ReviewedRecord accepted a helper hash outside the pinned expectation'
Write-Host 'PASS ReviewedRecord requires a pinned LocalAudit record with complete local verification and 19 passing gates'

$badReview = [ordered]@{} + $attestation
$badReview.supplement_sha256 = '7' * 64
$bad = @{} + $validArgs; $bad.ReviewAttestation = $badReview
Assert-Throws { Get-CnMonMsixDesktopCompletionReport @bad } 'review for different supplement bytes was accepted'
$badReview = [ordered]@{} + $attestation
$badReview.screenshot_review_completed = $false
$bad = @{} + $validArgs; $bad.ReviewAttestation = $badReview
Assert-Throws { Get-CnMonMsixDesktopCompletionReport @bad } 'operator-only screenshot completion was accepted'
$badReview = [ordered]@{} + $attestation
$badReview.independent_sol_review = [ordered]@{} + $attestation.independent_sol_review
$badReview.independent_sol_review.status = 'pending'
$bad = @{} + $validArgs; $bad.ReviewAttestation = $badReview
Assert-Throws { Get-CnMonMsixDesktopCompletionReport @bad } 'unresolved independent Sol review was accepted'
$badReview = [ordered]@{} + $attestation
$badReview.independent_sol_review = [ordered]@{} + $attestation.independent_sol_review
$badReview.independent_sol_review.reviewed_at_utc = '20261399T250000Z'
$bad = @{} + $validArgs; $bad.ReviewAttestation = $badReview
Assert-Throws { Get-CnMonMsixDesktopCompletionReport @bad } 'invalid UTC review timestamp was accepted'
$badReview = [ordered]@{} + $attestation
$badReview.independent_sol_review = [ordered]@{} + $attestation.independent_sol_review
$badReview.independent_sol_review.reference = '/private/review/output'
$bad = @{} + $validArgs; $bad.ReviewAttestation = $badReview
Assert-Throws { Get-CnMonMsixDesktopCompletionReport @bad } 'private path review reference was accepted'
Write-Host 'PASS review attestation binds inspected raw evidence'

$badListing = [ordered]@{} + $supplement
$badListing.observations = [ordered]@{} + $observations
$badListing.observations.screenshots = @($screenshots | ForEach-Object {
    $item = [ordered]@{} + $_
    if ($item.name -ceq '03-json-tcp.png') { $item.width = 1280 }
    $item
})
$bad = @{} + $validArgs; $bad.Supplement = $badListing
Assert-Throws { Get-CnMonMsixDesktopCompletionReport @bad } 'undersized listing screenshot was accepted'
Write-Host 'PASS Store listing screenshots enforce minimum desktop dimensions'

$badActual = @($actualScreenshots | ForEach-Object {
    if ($_.name -ceq '01-cli-help.png') { [pscustomobject]@{ name = $_.name; sha256 = '0' * 64; width = $_.width; height = $_.height } }
    else { $_ }
})
$bad = @{} + $validArgs; $bad.ActualScreenshots = $badActual
Assert-Throws { Get-CnMonMsixDesktopCompletionReport @bad } 'screenshot hash not matching original file bytes was accepted'
Write-Host 'PASS screenshot manifest hashes bind actual image observations'

$badAliasResult = [ordered]@{} + $supplement
$badAliasResult.observations = [ordered]@{} + $observations
$badAliasResult.observations.alias = [ordered]@{} + $observations.alias
$badAliasResult.observations.alias.off = [ordered]@{} + $observations.alias.off
$badAliasResult.observations.alias.off.pwsh_success = $true
$bad = @{} + $validArgs; $bad.Supplement = $badAliasResult
Assert-Throws { Get-CnMonMsixDesktopCompletionReport @bad } 'Off alias success was accepted'
$badAliasResult.observations.alias.off.pwsh_success = $false
$badAliasResult.observations.alias.path_unchanged = $false
$bad = @{} + $validArgs; $bad.Supplement = $badAliasResult
Assert-Throws { Get-CnMonMsixDesktopCompletionReport @bad } 'changed PATH was accepted'
Write-Host 'PASS alias Off and On shell outcomes are exact'

Write-Host 'PASS MSIX completion validator focused assertions'
