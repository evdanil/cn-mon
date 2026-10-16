# Copyright 2026 cn-mon contributors
# SPDX-License-Identifier: Apache-2.0

$ErrorActionPreference = 'Stop'

function Assert-True {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw $Message }
}

function Assert-Equal {
    param([object]$Actual, [object]$Expected, [string]$Message)
    if ($Actual -cne $Expected) { throw "$Message (expected '$Expected', got '$Actual')" }
}

function Assert-Throws {
    param([scriptblock]$Action, [string]$Message)
    try { & $Action }
    catch { return }
    throw $Message
}

$repoRoot = Split-Path -Parent $PSScriptRoot
$clientScriptPath = Join-Path $PSScriptRoot 'test-msix-desktop-checks.ps1'
$qualificationScriptPath = Join-Path $PSScriptRoot 'test-msix-windows.ps1'
if (-not (Test-Path -LiteralPath $clientScriptPath -PathType Leaf)) {
    throw 'desktop client supplement is missing'
}
$parseErrors = $null
$clientAst = [System.Management.Automation.Language.Parser]::ParseFile($clientScriptPath, [ref]$null, [ref]$parseErrors)
if ($parseErrors.Count -gt 0) { throw 'desktop client supplement did not parse' }

$clientFunctionNames = @(
    'Read-CnMonMsixDesktopResult',
    'Test-CnMonMsixDesktopEligibility',
    'Get-CnMonMsixDesktopSupplement',
    'Get-CnMonMsixDesktopPeMachine',
    'Export-CnMonMsixPublicSignerCertificate',
    'Get-CnMonMsixClientInputPath',
    'Get-CnMonMsixDesktopInputPath',
    'Remove-CnMonMsixDesktopOwnedOutput',
    'Test-CnMonMsixSignerCertificate',
    'Get-CnMonMsixDesktopSignatureState'
)
$clientFunctions = @($clientAst.FindAll({
    param($candidate)
    $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
        $clientFunctionNames -contains $candidate.Name
}, $true))
$missingClientFunctions = @($clientFunctionNames | Where-Object { $_ -notin @($clientFunctions | ForEach-Object Name) })
if ($missingClientFunctions.Count -gt 0) { throw 'desktop client pure helper contract is incomplete' }

$validationHelperNames = @(
    'Set-CnMonMsixGate',
    'Get-CnMonMsixReceipt',
    'Assert-CnMonMsixReceipt',
    'Get-CnMonMsixResultEvidence',
    'Assert-CnMonMsixResultEvidence'
)
$sourceAst = [System.Management.Automation.Language.Parser]::ParseFile($qualificationScriptPath, [ref]$null, [ref]$parseErrors)
if ($parseErrors.Count -gt 0) { throw 'qualification source did not parse' }
$validationFunctionAsts = @($sourceAst.FindAll({
    param($candidate)
    $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
        $validationHelperNames -contains $candidate.Name
}, $true))
$missingValidationHelpers = @($validationHelperNames | Where-Object { $_ -notin @($validationFunctionAsts | ForEach-Object Name) })
if ($missingValidationHelpers.Count -gt 0) { throw 'fixed qualification validation helpers are missing' }

$functionsText = (@($clientFunctions) + @($validationFunctionAsts) | ForEach-Object { $_.Extent.Text }) -join [Environment]::NewLine
. ([scriptblock]::Create($functionsText))
$clientInputFunction = @($clientFunctions | Where-Object Name -eq 'Get-CnMonMsixClientInputPath')[0]
$safeInputFunction = @($clientFunctions | Where-Object Name -eq 'Get-CnMonMsixDesktopInputPath')[0]

$tempRoot = Join-Path ([IO.Path]::GetTempPath()) ('cn-mon-msix-desktop-checks-' + [Guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $tempRoot
try {
    $preexistingSummaryPath = Join-Path $tempRoot 'msix-desktop-supplement.json'
    $preexistingCertificatePath = Join-Path $tempRoot 'msix-test-signer.cer'
    $summarySentinel = [Text.Encoding]::UTF8.GetBytes('PRIVATE-SUMMARY-SENTINEL')
    $certificateSentinel = [byte[]](0, 1, 2, 3, 4, 5, 6, 7)
    [IO.File]::WriteAllBytes($preexistingSummaryPath, $summarySentinel)
    [IO.File]::WriteAllBytes($preexistingCertificatePath, $certificateSentinel)
    Remove-CnMonMsixDesktopOwnedOutput -Path $preexistingSummaryPath -CreatedByThisRun:$false
    Remove-CnMonMsixDesktopOwnedOutput -Path $preexistingCertificatePath -CreatedByThisRun:$false
    Assert-True ([Linq.Enumerable]::SequenceEqual([byte[]][IO.File]::ReadAllBytes($preexistingSummaryPath), $summarySentinel)) 'preexisting summary sentinel was removed or changed'
    Assert-True ([Linq.Enumerable]::SequenceEqual([byte[]][IO.File]::ReadAllBytes($preexistingCertificatePath), $certificateSentinel)) 'preexisting CER sentinel was removed or changed'
    $ownedOutputPath = Join-Path $tempRoot 'owned-output.tmp'
    [IO.File]::WriteAllText($ownedOutputPath, 'owned by this run')
    Remove-CnMonMsixDesktopOwnedOutput -Path $ownedOutputPath -CreatedByThisRun:$true
    Assert-True (-not (Test-Path -LiteralPath $ownedOutputPath)) 'cleanup did not remove an output created by this run'
    Write-Host 'PASS rejected preexisting outputs remain byte-for-byte unchanged'

    $privateInputCanary = Join-Path $tempRoot 'PRIVATE-IDENTITY-PATH-SID-CANARY-missing.json'
    $probeScriptPath = Join-Path $tempRoot 'private-input-error-probe.ps1'
    $probeScript = @(
        $clientInputFunction.Extent.Text
        $safeInputFunction.Extent.Text
        'Get-CnMonMsixDesktopInputPath -Path $env:CNMON_MSIX_PRIVATE_INPUT_CANARY | Out-Null'
    ) -join [Environment]::NewLine
    [IO.File]::WriteAllText($probeScriptPath, $probeScript, [Text.UTF8Encoding]::new($false))
    $pwshName = if ([Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT) { 'pwsh.exe' } else { 'pwsh' }
    $pwshPath = Join-Path $PSHOME $pwshName
    $startInfo = [Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $pwshPath
    $startInfo.UseShellExecute = $false
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    foreach ($argument in @('-NoLogo', '-NoProfile', '-File', $probeScriptPath)) { $startInfo.ArgumentList.Add($argument) }
    $startInfo.Environment['CNMON_MSIX_PRIVATE_INPUT_CANARY'] = $privateInputCanary
    $probeProcess = [Diagnostics.Process]::Start($startInfo)
    try {
        $probeStdout = $probeProcess.StandardOutput.ReadToEnd()
        $probeStderr = $probeProcess.StandardError.ReadToEnd()
        $probeProcess.WaitForExit()
        Assert-True ($probeProcess.ExitCode -ne 0) 'missing input unexpectedly passed the desktop path guard'
        Assert-True (-not $probeStderr.Contains($privateInputCanary)) 'missing-input stderr exposed the private path canary'
        Assert-True (-not $probeStdout.Contains($privateInputCanary)) 'missing-input stdout exposed the private path canary'
        Assert-True ($probeStderr.Contains('a required desktop qualification input is unavailable')) 'missing input did not report the fixed safe error'
    }
    finally { $probeProcess.Dispose() }
    Write-Host 'PASS missing-input failure keeps private path details off stdout and stderr'

    $qualificationGates = @('identity', 'package_hash', 'signature', 'account', 'package_registration', 'exact_identity', 'installed_binary_hash', 'alias_identity', 'native_cli', 'icmp', 'history_config', 'shell_aliases', 'upgrade', 'repair', 'uninstall_reinstall', 'conflicts', 'fail_injection', 'cleanup', 'wack')
    $evidence = [ordered]@{
        signed_package_sha256 = 'a' * 64
        signed_receipt_sha256 = 'b' * 64
        packaging_revision = 'c' * 40
    }
    $desktopEvidence = [ordered]@{
        signed_package_sha256 = $evidence.signed_package_sha256
        signed_receipt_sha256 = $evidence.signed_receipt_sha256
        packaging_revision = $evidence.packaging_revision
        native_helper_sha256 = 'd' * 64
        private_canary = 'PRIVATE-IDENTITY-PATH-SID-CANARY'
    }
    $certifyGates = [ordered]@{}
    foreach ($gate in $qualificationGates) { $certifyGates[$gate] = 'pass' }
    $certifyGates.repair = 'incomplete'
    $certifyGates.conflicts = 'incomplete'
    $certifyGates.wack = 'pass'
    $certify = [ordered]@{ schema_version = 2; mode = 'Certify'; overall = 'incomplete'; evidence = $evidence; gates = $certifyGates }
    $certifyJson = ConvertTo-Json -InputObject $certify -Depth 6 -Compress

    $finalResult = Read-CnMonMsixDesktopResult -Json $certifyJson -ExpectedEvidence $evidence -QualificationGates $qualificationGates
    Write-Host 'PASS final Certify result binds package, receipt, revision, and WACK state'

    $badCore = [ordered]@{}
    foreach ($gate in $qualificationGates) { $badCore[$gate] = $certifyGates[$gate] }
    $badCore.native_cli = 'incomplete'
    $badCoreResult = [ordered]@{ schema_version = 2; mode = 'Certify'; overall = 'incomplete'; evidence = $evidence; gates = $badCore }
    Assert-Throws { Read-CnMonMsixDesktopResult -Json (ConvertTo-Json $badCoreResult -Depth 6 -Compress) -ExpectedEvidence $evidence -QualificationGates $qualificationGates } 'Certify result with an incomplete core gate was accepted'
    Write-Host 'PASS all 16 core gates must already pass'

    $badWack = [ordered]@{}
    foreach ($gate in $qualificationGates) { $badWack[$gate] = $certifyGates[$gate] }
    $badWack.wack = 'incomplete'
    $badWackCertify = [ordered]@{ schema_version = 2; mode = 'Certify'; overall = 'incomplete'; evidence = $evidence; gates = $badWack }
    Assert-Throws { Read-CnMonMsixDesktopResult -Json (ConvertTo-Json $badWackCertify -Depth 6 -Compress) -ExpectedEvidence $evidence -QualificationGates $qualificationGates } 'incomplete WACK result was accepted as prerequisite'
    Write-Host 'PASS incomplete WACK result blocks desktop supplement'

    $badOverall = [ordered]@{ schema_version = 2; mode = 'Certify'; overall = 'pass'; evidence = $evidence; gates = $certifyGates }
    Assert-Throws { Read-CnMonMsixDesktopResult -Json (ConvertTo-Json $badOverall -Depth 6 -Compress) -ExpectedEvidence $evidence -QualificationGates $qualificationGates } 'inconsistent overall status was accepted'
    $badEvidence = [ordered]@{ signed_package_sha256 = ('f' * 64); signed_receipt_sha256 = $evidence.signed_receipt_sha256; packaging_revision = $evidence.packaging_revision }
    $unboundResult = [ordered]@{ schema_version = 2; mode = 'Certify'; overall = 'incomplete'; evidence = $badEvidence; gates = $certifyGates }
    Assert-Throws { Read-CnMonMsixDesktopResult -Json (ConvertTo-Json $unboundResult -Depth 6 -Compress) -ExpectedEvidence $evidence -QualificationGates $qualificationGates } 'result with mismatched package evidence was accepted'
    $hostedMode = [ordered]@{ schema_version = 2; mode = 'Hosted'; overall = 'incomplete'; evidence = $evidence; gates = $certifyGates }
    Assert-Throws { Read-CnMonMsixDesktopResult -Json (ConvertTo-Json $hostedMode -Depth 6 -Compress) -ExpectedEvidence $evidence -QualificationGates $qualificationGates } 'Hosted-only result was accepted without final Certify output'

    $eligibility = Test-CnMonMsixDesktopEligibility -UserSidPresent:$true -AdministratorRole:$false -AdministratorsSidPresent:$false -UserInteractive:$true -SessionId 7 -SessionActive:$true -InputDesktopAvailable:$true -MediumIntegrity:$true
    Assert-True $eligibility 'eligible ordinary active session was rejected'
    $rdpEligibility = Test-CnMonMsixDesktopEligibility -UserSidPresent:$true -AdministratorRole:$false -AdministratorsSidPresent:$false -UserInteractive:$true -SessionId 12 -SessionActive:$true -InputDesktopAvailable:$true -MediumIntegrity:$true
    Assert-True $rdpEligibility 'active RDP session was rejected'
    $disconnectedSession = Test-CnMonMsixDesktopEligibility -UserSidPresent:$true -AdministratorRole:$false -AdministratorsSidPresent:$false -UserInteractive:$true -SessionId 12 -SessionActive:$false -InputDesktopAvailable:$true -MediumIntegrity:$true
    Assert-True (-not $disconnectedSession) 'disconnected RDP session was treated as eligible'
    $denyOnlyAdmin = Test-CnMonMsixDesktopEligibility -UserSidPresent:$true -AdministratorRole:$false -AdministratorsSidPresent:$true -UserInteractive:$true -SessionId 7 -SessionActive:$true -InputDesktopAvailable:$true -MediumIntegrity:$true
    Assert-True (-not $denyOnlyAdmin) 'deny-only Administrators SID was treated as ordinary'
    $administratorRole = Test-CnMonMsixDesktopEligibility -UserSidPresent:$true -AdministratorRole:$true -AdministratorsSidPresent:$false -UserInteractive:$true -SessionId 7 -SessionActive:$true -InputDesktopAvailable:$true -MediumIntegrity:$true
    Assert-True (-not $administratorRole) 'administrator role was treated as ordinary'
    $sessionZero = Test-CnMonMsixDesktopEligibility -UserSidPresent:$true -AdministratorRole:$false -AdministratorsSidPresent:$false -UserInteractive:$true -SessionId 0 -SessionActive:$true -InputDesktopAvailable:$true -MediumIntegrity:$true
    Assert-True (-not $sessionZero) 'Session 0 was treated as an interactive desktop'
    $missingDesktop = Test-CnMonMsixDesktopEligibility -UserSidPresent:$true -AdministratorRole:$false -AdministratorsSidPresent:$false -UserInteractive:$true -SessionId 7 -SessionActive:$true -InputDesktopAvailable:$false -MediumIntegrity:$true
    Assert-True (-not $missingDesktop) 'locked or unavailable input desktop was treated as visible'
    $lowIntegrity = Test-CnMonMsixDesktopEligibility -UserSidPresent:$true -AdministratorRole:$false -AdministratorsSidPresent:$false -UserInteractive:$true -SessionId 7 -SessionActive:$true -InputDesktopAvailable:$true -MediumIntegrity:$false
    Assert-True (-not $lowIntegrity) 'non-medium token was treated as eligible'
    Write-Host 'PASS ordinary-user, medium-token, active console or RDP session, and visible-desktop guards'

    $canary = 'PRIVATE-IDENTITY-PATH-SID-CANARY'
    $supplement = Get-CnMonMsixDesktopSupplement -Evidence $desktopEvidence -PackageReceiptBound:$true -CertifyResultBound:$true -WackPass:$true -SignerMatchesReceipt:$true -SignerValidNow:$true -MachineTrustedPeople:$true -SignatureValid:$true -SignatureStatus valid -DesktopEligible:$true -UserTokenEligible:$true -InteractiveSessionEligible:$true -InputDesktopAvailable:$true -MediumIntegrity:$true -CandidateNotRegistered:$true -NativeHelperX64:$true
    $safeJson = ConvertTo-Json -InputObject $supplement -Depth 6 -Compress
    Assert-Equal $supplement.gates.repair 'incomplete' 'preflight marked Repair passed'
    Assert-Equal $supplement.gates.conflicts 'incomplete' 'preflight marked alias conflict checks passed'
    Assert-Equal $supplement.gates.screenshots 'incomplete' 'preflight marked screenshots passed'
    Assert-True $supplement.preflight.user_token_eligible 'eligible token fact was lost'
    Assert-True $supplement.preflight.interactive_session_eligible 'eligible session fact was lost'
    Assert-True $supplement.preflight.input_desktop_available 'visible desktop fact was lost'
    Assert-True $supplement.preflight.medium_integrity 'medium integrity fact was lost'
    Assert-True $supplement.preflight.signature_valid 'valid signature fact was lost'
    Assert-Equal $supplement.preflight.signature_status 'valid' 'fixed signature status was lost'
    Assert-True (-not $safeJson.Contains($canary)) 'supplement projection leaked a private canary'
    if ((@($supplement.preflight.Keys | Sort-Object) -join ',') -cne 'candidate_not_registered,certify_result_bound,desktop_eligible,input_desktop_available,interactive_session_eligible,medium_integrity,native_helper_x64,package_receipt_bound,signature_status,signature_valid,signer_matches_receipt,signer_trusted,signer_valid_now,user_token_eligible,wack_pass') {
        throw 'fixed preflight facts changed'
    }
    if ((@($supplement.Keys | Sort-Object) -join ',') -cne 'evidence,gates,mode,overall,preflight,preflight_status,schema_version') {
        throw 'supplement root schema changed'
    }
    Assert-Equal $supplement.preflight_status 'ready_for_review' 'all passing preflight evidence did not produce review readiness'
    $needsTrust = Get-CnMonMsixDesktopSupplement -Evidence $desktopEvidence -PackageReceiptBound:$true -CertifyResultBound:$true -WackPass:$true -SignerMatchesReceipt:$true -SignerValidNow:$true -MachineTrustedPeople:$false -SignatureValid:$true -SignatureStatus valid -DesktopEligible:$true -UserTokenEligible:$true -InteractiveSessionEligible:$true -InputDesktopAvailable:$true -MediumIntegrity:$true -CandidateNotRegistered:$true -NativeHelperX64:$true
    Assert-Equal $needsTrust.preflight_status 'needs_trust_review' 'missing signer trust was not separated for admin review'
    $badPrerequisite = Get-CnMonMsixDesktopSupplement -Evidence $desktopEvidence -PackageReceiptBound:$true -CertifyResultBound:$true -WackPass:$false -SignerMatchesReceipt:$true -SignerValidNow:$true -MachineTrustedPeople:$true -SignatureValid:$true -SignatureStatus valid -DesktopEligible:$true -UserTokenEligible:$true -InteractiveSessionEligible:$true -InputDesktopAvailable:$true -MediumIntegrity:$true -CandidateNotRegistered:$true -NativeHelperX64:$true
    Assert-Equal $badPrerequisite.preflight_status 'blocked' 'failed WACK prerequisite did not block the client check'
    Write-Host 'PASS fixed private supplement keeps unobserved gates incomplete'

    $pePath = Join-Path $tempRoot 'x64-helper.exe'
    $peBytes = [byte[]]::new(0x100)
    $peBytes[0] = 0x4D; $peBytes[1] = 0x5A
    [BitConverter]::GetBytes([int]0x80).CopyTo($peBytes, 0x3C)
    $peBytes[0x80] = 0x50; $peBytes[0x81] = 0x45
    [BitConverter]::GetBytes([UInt16]0x8664).CopyTo($peBytes, 0x84)
    [IO.File]::WriteAllBytes($pePath, $peBytes)
    Assert-Equal (Get-CnMonMsixDesktopPeMachine -Path $pePath) 'x64' 'x64 helper machine was not recognized'
    $peBytes[0x84] = 0x64; $peBytes[0x85] = 0xAA
    [IO.File]::WriteAllBytes($pePath, $peBytes)
    Assert-Throws { Get-CnMonMsixDesktopPeMachine -Path $pePath } 'non-x64 helper machine was accepted'
    Write-Host 'PASS prebuilt native helper architecture guard'

    $rsa = [Security.Cryptography.RSA]::Create(2048)
    $request = [Security.Cryptography.X509Certificates.CertificateRequest]::new('CN=Desktop Fixture', $rsa, [Security.Cryptography.HashAlgorithmName]::SHA256, [Security.Cryptography.RSASignaturePadding]::Pkcs1)
    $cert = $request.CreateSelfSigned([DateTimeOffset]::UtcNow.AddMinutes(-1), [DateTimeOffset]::UtcNow.AddHours(1))
    $publicSigner = [Security.Cryptography.X509Certificates.X509Certificate2]::new($cert.RawData)
    $cerPath = Join-Path $tempRoot 'public-signer.cer'
    $expectedPublisherSubject = 'CN=Desktop Fixture'
    $unknownErrorState = Get-CnMonMsixDesktopSignatureState -Certificate $publicSigner -SignatureStatus UnknownError -ExpectedThumbprint $publicSigner.Thumbprint -ExpectedPublisherSubject $expectedPublisherSubject
    Assert-True (-not $unknownErrorState.SignatureValid) 'UnknownError was treated as a verified signature'
    Assert-Equal $unknownErrorState.SignatureStatus 'signature_unverified' 'UnknownError did not map to the fixed unverified status'
    $exported = Export-CnMonMsixPublicSignerCertificate -Certificate $publicSigner -Path $cerPath
    Assert-Equal $exported $publicSigner.Thumbprint 'public signer certificate thumbprint changed during export'
    $exportedBytes = [IO.File]::ReadAllBytes($cerPath)
    Assert-Throws { Export-CnMonMsixPublicSignerCertificate -Certificate $publicSigner -Path $cerPath } 'certificate export overwrote an existing output'
    Assert-True ([Linq.Enumerable]::SequenceEqual([byte[]][IO.File]::ReadAllBytes($cerPath), $exportedBytes)) 'create-new certificate export changed a preexisting file'
    $unknownSupplement = Get-CnMonMsixDesktopSupplement -Evidence $desktopEvidence -PackageReceiptBound:$true -CertifyResultBound:$true -WackPass:$true -SignerMatchesReceipt:$unknownErrorState.SignerMatchesReceipt -SignerValidNow:$unknownErrorState.SignerValidNow -MachineTrustedPeople:$true -SignatureValid:$unknownErrorState.SignatureValid -SignatureStatus $unknownErrorState.SignatureStatus -DesktopEligible:$true -UserTokenEligible:$true -InteractiveSessionEligible:$true -InputDesktopAvailable:$true -MediumIntegrity:$true -CandidateNotRegistered:$true -NativeHelperX64:$true
    Assert-Equal $unknownSupplement.preflight_status 'blocked_for_registration' 'UnknownError did not block registration after public CER export'
    Assert-Equal $unknownSupplement.preflight.signature_status 'signature_unverified' 'UnknownError projection is not a fixed safe status'
    Write-Host 'PASS bound UnknownError permits public CER export but blocks registration'

    $validState = Get-CnMonMsixDesktopSignatureState -Certificate $publicSigner -SignatureStatus Valid -ExpectedThumbprint $publicSigner.Thumbprint -ExpectedPublisherSubject $expectedPublisherSubject
    Assert-True $validState.SignatureValid 'Valid signature status was not accepted'
    $validSupplement = Get-CnMonMsixDesktopSupplement -Evidence $desktopEvidence -PackageReceiptBound:$true -CertifyResultBound:$true -WackPass:$true -SignerMatchesReceipt:$validState.SignerMatchesReceipt -SignerValidNow:$validState.SignerValidNow -MachineTrustedPeople:$true -SignatureValid:$validState.SignatureValid -SignatureStatus $validState.SignatureStatus -DesktopEligible:$true -UserTokenEligible:$true -InteractiveSessionEligible:$true -InputDesktopAvailable:$true -MediumIntegrity:$true -CandidateNotRegistered:$true -NativeHelperX64:$true
    Assert-Equal $validSupplement.preflight_status 'ready_for_review' 'Valid signature and all other prerequisites did not enable review readiness'
    Assert-Throws { Get-CnMonMsixDesktopSignatureState -Certificate $publicSigner -SignatureStatus Valid -ExpectedThumbprint ('0' * 40) -ExpectedPublisherSubject $expectedPublisherSubject } 'mismatched signer thumbprint was accepted'
    Assert-Throws { Get-CnMonMsixDesktopSignatureState -Certificate $publicSigner -SignatureStatus Valid -ExpectedThumbprint $publicSigner.Thumbprint -ExpectedPublisherSubject 'CN=Wrong Publisher' } 'mismatched Publisher subject was accepted'
    $notTrustedState = Get-CnMonMsixDesktopSignatureState -Certificate $publicSigner -SignatureStatus NotTrusted -ExpectedThumbprint $publicSigner.Thumbprint -ExpectedPublisherSubject $expectedPublisherSubject
    Assert-True (-not $notTrustedState.SignatureValid) 'NotTrusted was treated as a verified signature'
    Assert-Equal $notTrustedState.SignatureStatus 'signature_unverified' 'NotTrusted did not map to the fixed unverified state'
    $notTrustedSupplement = Get-CnMonMsixDesktopSupplement -Evidence $desktopEvidence -PackageReceiptBound:$true -CertifyResultBound:$true -WackPass:$true -SignerMatchesReceipt:$notTrustedState.SignerMatchesReceipt -SignerValidNow:$notTrustedState.SignerValidNow -MachineTrustedPeople:$true -SignatureValid:$notTrustedState.SignatureValid -SignatureStatus $notTrustedState.SignatureStatus -DesktopEligible:$true -UserTokenEligible:$true -InteractiveSessionEligible:$true -InputDesktopAvailable:$true -MediumIntegrity:$true -CandidateNotRegistered:$true -NativeHelperX64:$true
    Assert-Equal $notTrustedSupplement.preflight_status 'blocked_for_registration' 'NotTrusted enabled registration or readiness'
    Assert-Throws { Get-CnMonMsixDesktopSupplement -Evidence $desktopEvidence -PackageReceiptBound:$true -CertifyResultBound:$true -WackPass:$true -SignerMatchesReceipt:$true -SignerValidNow:$true -MachineTrustedPeople:$true -SignatureValid:$false -SignatureStatus valid -DesktopEligible:$true -UserTokenEligible:$true -InteractiveSessionEligible:$true -InputDesktopAvailable:$true -MediumIntegrity:$true -CandidateNotRegistered:$true -NativeHelperX64:$true } 'inconsistent signature status and boolean were accepted'

    $expiredCertificate = $request.CreateSelfSigned([DateTimeOffset]::UtcNow.AddHours(-8), [DateTimeOffset]::UtcNow.AddHours(-1))
    $expiredPublicCertificate = [Security.Cryptography.X509Certificates.X509Certificate2]::new($expiredCertificate.RawData)
    try { Assert-True (-not (Test-CnMonMsixSignerCertificate -Certificate $expiredPublicCertificate -ExpectedThumbprint $expiredPublicCertificate.Thumbprint -ExpectedPublisherSubject $expectedPublisherSubject)) 'expired signer certificate was accepted' }
    finally { $expiredPublicCertificate.Dispose(); $expiredCertificate.Dispose() }
    $longLivedCertificate = $request.CreateSelfSigned([DateTimeOffset]::UtcNow.AddMinutes(-1), [DateTimeOffset]::UtcNow.AddHours(24))
    $longLivedPublicCertificate = [Security.Cryptography.X509Certificates.X509Certificate2]::new($longLivedCertificate.RawData)
    try { Assert-True (-not (Test-CnMonMsixSignerCertificate -Certificate $longLivedPublicCertificate -ExpectedThumbprint $longLivedPublicCertificate.Thumbprint -ExpectedPublisherSubject $expectedPublisherSubject)) 'long-lived signer certificate was accepted' }
    finally { $longLivedPublicCertificate.Dispose(); $longLivedCertificate.Dispose() }
    Assert-Throws { Export-CnMonMsixPublicSignerCertificate -Certificate $cert -Path (Join-Path $tempRoot 'private-signer.cer') } 'private signer key was exported'
    $publicCertificate = [Security.Cryptography.X509Certificates.X509Certificate2]::new($cerPath)
    try { Assert-True (-not $publicCertificate.HasPrivateKey) 'exported signer certificate contains a private key' }
    finally { $publicCertificate.Dispose(); $publicSigner.Dispose(); $cert.Dispose(); $rsa.Dispose() }
    Write-Host 'PASS signer export contains only the public certificate'

    $parseErrors = $null
    $qualificationAst = [System.Management.Automation.Language.Parser]::ParseFile($clientScriptPath, [ref]$null, [ref]$parseErrors)
    $forbiddenCommands = @('Add-AppxPackage', 'Remove-AppxPackage', 'Import-Certificate', 'Register-AppxPackage', 'Reset-AppxPackage', 'New-LocalUser', 'Remove-LocalUser', 'Start-Process', 'Stop-Process')
    foreach ($commandName in $forbiddenCommands) {
        $calls = @($qualificationAst.FindAll({
            param($candidate)
            $candidate -is [System.Management.Automation.Language.CommandAst] -and $candidate.GetCommandName() -eq $commandName
        }, $true))
        Assert-Equal $calls.Count 0 "read-only client preflight invokes forbidden command $commandName"
    }
    $fixedHelperList = @($clientAst.FindAll({
        param($candidate)
        $candidate -is [System.Management.Automation.Language.AssignmentStatementAst] -and
            $candidate.Left.Extent.Text -eq '$validationHelperNames'
    }, $true))
    Assert-Equal $fixedHelperList.Count 1 'qualification helper AST allowlist was not explicit and singular'
    $allowedHelpers = @($fixedHelperList[0].Right.FindAll({
        param($candidate)
        $candidate -is [System.Management.Automation.Language.StringConstantExpressionAst]
    }, $true) | ForEach-Object Value)
    $expectedHelpers = @('Set-CnMonMsixGate', 'Get-CnMonMsixReceipt', 'Assert-CnMonMsixReceipt', 'Get-CnMonMsixResultEvidence', 'Assert-CnMonMsixResultEvidence')
    Assert-Equal ($allowedHelpers -join ',') ($expectedHelpers -join ',') 'qualification helpers no longer use the fixed allowlist'
    Assert-True (-not ($clientAst.Extent.Text -match '(?m)^\s*&\s*\$qualificationScriptPath\b')) 'desktop supplement invokes the main qualification entrypoint'
    $runtimeBoundaries = @($qualificationAst.FindAll({
        param($candidate)
        $candidate -is [System.Management.Automation.Language.TryStatementAst] -and
            $candidate.Body.Extent.Text.Contains('Get-CnMonMsixDesktopInputPath') -and
            $candidate.Body.Extent.Text.Contains('Get-FileHash') -and
            $candidate.Body.Extent.Text.Contains('Get-CnMonMsixDesktopPeMachine') -and
            $candidate.Body.Extent.Text.Contains('summaryStream.Write')
    }, $true))
    Assert-Equal $runtimeBoundaries.Count 1 'path, hash, PE, and output operations are not inside one sanitized boundary'
    $runtimeCatch = $runtimeBoundaries[0].CatchClauses[0].Body.Extent.Text
    Assert-True ($runtimeCatch.Contains('desktop client preflight failed; no package or trust changes were attempted')) 'runtime boundary does not replace raw errors with the fixed safe error'
    Assert-True ($runtimeCatch.Contains('$summaryCreated') -and $runtimeCatch.Contains('$certificateCreated')) 'runtime cleanup does not track created outputs'
    Assert-True (-not $runtimeCatch.Contains('Remove-Item')) 'runtime catch deletes outputs without the ownership helper'
    Assert-True ($runtimeBoundaries[0].Body.Extent.Text.Contains('[IO.FileMode]::CreateNew')) 'output writes do not use create-new semantics'
    Assert-True ($runtimeBoundaries[0].Body.Extent.Text.Contains("Cert:\LocalMachine\TrustedPeople")) 'registration readiness does not require machine TrustedPeople trust'
    Assert-True (-not $runtimeBoundaries[0].Body.Extent.Text.Contains('Cert:\CurrentUser\TrustedPeople')) 'per-user trust incorrectly satisfies registration readiness'
    Assert-True ($runtimeBoundaries[0].Body.Extent.Text.Contains('MachineTrustedPeople:$certificateTrusted')) 'supplement readiness is not bound to machine TrustedPeople trust'
    Assert-True ($clientAst.Extent.Text.Contains('WTSQuerySessionInformationW') -and $clientAst.Extent.Text.Contains('WTSFreeMemory') -and $runtimeBoundaries[0].Body.Extent.Text.Contains('SessionActive')) 'interactive session eligibility does not query and release current-session WTS state'
    $eligibilityCalls = @($clientAst.FindAll({
        param($candidate)
        $candidate -is [System.Management.Automation.Language.CommandAst] -and
            $candidate.GetCommandName() -eq 'Test-CnMonMsixDesktopEligibility'
    }, $true))
    Assert-Equal $eligibilityCalls.Count 1 'desktop eligibility call site changed unexpectedly'
    $userInteractiveArgument = @($eligibilityCalls[0].CommandElements | Where-Object {
        $_ -is [System.Management.Automation.Language.CommandParameterAst] -and $_.ParameterName -eq 'UserInteractive'
    })[0].Argument
    Assert-True ($userInteractiveArgument -is [System.Management.Automation.Language.ParenExpressionAst]) 'static UserInteractive boolean is not parenthesized at binding site'
    function Test-CnMonDesktopBooleanBinding { param([bool]$UserInteractive); return $UserInteractive }
    $bindActualArgument = 'Test-CnMonDesktopBooleanBinding -UserInteractive:' + $userInteractiveArgument.Extent.Text
    $boundUserInteractive = & ([scriptblock]::Create($bindActualArgument))
    Assert-True ($boundUserInteractive -is [bool] -and $boundUserInteractive -eq [Environment]::UserInteractive) 'actual eligibility argument did not bind as a boolean'
    Write-Host 'PASS static UserInteractive argument binds as a boolean'
    $mainScriptCalls = @($qualificationAst.FindAll({
        param($candidate)
        $candidate -is [System.Management.Automation.Language.CommandAst] -and
            $candidate.GetCommandName() -eq 'Invoke-Expression'
    }, $true))
    Assert-Equal $mainScriptCalls.Count 0 'desktop supplement uses unrestricted Invoke-Expression'
    Write-Host 'PASS no OS mutation or main qualification entrypoint execution'

    Write-Host 'PASS MSIX desktop checks focused assertions'
}
finally { Remove-Item -LiteralPath $tempRoot -Recurse -Force -ErrorAction SilentlyContinue }
