# Copyright 2026 cn-mon contributors
# SPDX-License-Identifier: Apache-2.0

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][string]$PackagePath,
    [Parameter(Mandatory = $true)][string]$ReceiptPath,
    [Parameter(Mandatory = $true)][string]$ResultsPath,
    [Parameter(Mandatory = $true)][string]$NativeHelperPath,
    [Parameter(Mandatory = $true)][string]$ExpectedNativeHelperSha256,
    [Parameter(Mandatory = $true)][string]$ArtifactDirectory
)

$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false

function Read-CnMonMsixDesktopResult {
    param(
        [Parameter(Mandatory = $true)][string]$Json,
        [Parameter(Mandatory = $true)][System.Collections.IDictionary]$ExpectedEvidence,
        [Parameter(Mandatory = $true)][string[]]$QualificationGates
    )
    try { $result = ConvertFrom-Json -InputObject $Json -AsHashtable -ErrorAction Stop }
    catch { throw 'result JSON is invalid' }
    $expectedRoot = @('evidence', 'gates', 'mode', 'overall', 'schema_version')
    if ($null -eq $result -or -not ($result -is [System.Collections.IDictionary]) -or
        (@($result.Keys | Sort-Object -CaseSensitive) -join "`n") -cne (@($expectedRoot | Sort-Object -CaseSensitive) -join "`n") -or
        (($result.schema_version -isnot [int]) -and ($result.schema_version -isnot [long])) -or $result.schema_version -ne 2 -or
        $result.mode -isnot [string] -or $result.mode -cne 'Certify' -or
        $result.overall -isnot [string] -or $result.overall -cne 'incomplete' -or
        -not ($result.evidence -is [System.Collections.IDictionary]) -or
        -not ($result.gates -is [System.Collections.IDictionary]) -or
        (@($result.gates.Keys | Sort-Object -CaseSensitive) -join "`n") -cne (@($QualificationGates | Sort-Object -CaseSensitive) -join "`n")) {
        throw 'result schema is invalid'
    }
    $coreGates = @($QualificationGates | Where-Object { $_ -notin @('repair', 'conflicts', 'wack') })
    if ($coreGates.Count -ne 16) { throw 'qualification result gate contract is invalid' }
    foreach ($gate in $coreGates) {
        if ([string]$result.gates[$gate] -cne 'pass') { throw 'a core qualification gate is not passing' }
    }
    if ([string]$result.gates.repair -cne 'incomplete' -or
        [string]$result.gates.conflicts -cne 'incomplete' -or
        [string]$result.gates.wack -cne 'pass') {
        throw 'final Certify result does not contain the required WACK-only completion state'
    }
    foreach ($gate in $QualificationGates) {
        if ($result.gates[$gate] -isnot [string] -or $result.gates[$gate] -cnotin @('pass', 'fail', 'incomplete')) {
            throw 'result gate status is invalid'
        }
    }
    Assert-CnMonMsixResultEvidence -Evidence $result.evidence -ExpectedEvidence $ExpectedEvidence
    return $result
}

function Test-CnMonMsixDesktopEligibility {
    param(
        [bool]$UserSidPresent,
        [bool]$AdministratorRole,
        [bool]$AdministratorsSidPresent,
        [bool]$UserInteractive,
        [int]$SessionId,
        [bool]$SessionActive,
        [bool]$InputDesktopAvailable,
        [bool]$MediumIntegrity
    )
    return $UserSidPresent -and -not $AdministratorRole -and -not $AdministratorsSidPresent -and
        $UserInteractive -and $SessionId -gt 0 -and $SessionActive -and
        $InputDesktopAvailable -and $MediumIntegrity
}

function Get-CnMonMsixDesktopSupplement {
    param(
        [Parameter(Mandatory = $true)][System.Collections.IDictionary]$Evidence,
        [bool]$PackageReceiptBound,
        [bool]$CertifyResultBound,
        [bool]$WackPass,
        [bool]$SignerMatchesReceipt,
        [bool]$SignerValidNow,
        [bool]$MachineTrustedPeople,
        [bool]$SignatureValid,
        [ValidateSet('valid', 'signature_unverified')][string]$SignatureStatus,
        [bool]$DesktopEligible,
        [bool]$UserTokenEligible,
        [bool]$InteractiveSessionEligible,
        [bool]$InputDesktopAvailable,
        [bool]$MediumIntegrity,
        [bool]$CandidateNotRegistered,
        [bool]$NativeHelperX64
    )
    $safeEvidence = [ordered]@{
        signed_package_sha256 = [string]$Evidence.signed_package_sha256
        signed_receipt_sha256 = [string]$Evidence.signed_receipt_sha256
        packaging_revision = [string]$Evidence.packaging_revision
        native_helper_sha256 = [string]$Evidence.native_helper_sha256
    }
    foreach ($key in @('signed_package_sha256', 'signed_receipt_sha256', 'native_helper_sha256')) {
        if ($safeEvidence[$key] -cnotmatch '^[0-9a-f]{64}$') { throw 'desktop supplement evidence hash is invalid' }
    }
    if ($safeEvidence.packaging_revision -cnotmatch '^[0-9a-f]{40}$') { throw 'desktop supplement revision is invalid' }
    if (($SignatureStatus -ceq 'valid') -ne $SignatureValid) { throw 'desktop supplement signature state is inconsistent' }
    $preflightStatus = if (-not $PackageReceiptBound -or -not $CertifyResultBound -or -not $WackPass -or
        -not $SignerMatchesReceipt -or -not $SignerValidNow -or -not $DesktopEligible -or
        -not $CandidateNotRegistered -or -not $NativeHelperX64) { 'blocked' }
    elseif (-not $SignatureValid) { 'blocked_for_registration' }
    elseif (-not $MachineTrustedPeople) { 'needs_trust_review' }
    else { 'ready_for_review' }
    return [ordered]@{
        schema_version = 1
        mode = 'desktop_supplement'
        overall = 'incomplete'
        preflight_status = $PreflightStatus
        evidence = $safeEvidence
        preflight = [ordered]@{
            package_receipt_bound = [bool]$PackageReceiptBound
            certify_result_bound = [bool]$CertifyResultBound
            wack_pass = [bool]$WackPass
            signer_matches_receipt = [bool]$SignerMatchesReceipt
            signer_valid_now = [bool]$SignerValidNow
            signer_trusted = [bool]$MachineTrustedPeople
            signature_valid = [bool]$SignatureValid
            signature_status = $SignatureStatus
            desktop_eligible = [bool]$DesktopEligible
            user_token_eligible = [bool]$UserTokenEligible
            interactive_session_eligible = [bool]$InteractiveSessionEligible
            input_desktop_available = [bool]$InputDesktopAvailable
            medium_integrity = [bool]$MediumIntegrity
            candidate_not_registered = [bool]$CandidateNotRegistered
            native_helper_x64 = [bool]$NativeHelperX64
        }
        gates = [ordered]@{
            repair = 'incomplete'
            conflicts = 'incomplete'
            screenshots = 'incomplete'
            cleanup = 'incomplete'
        }
    }
}

function Get-CnMonMsixDesktopPeMachine {
    param([Parameter(Mandatory = $true)][string]$Path)
    $stream = [IO.File]::OpenRead($Path)
    $reader = [IO.BinaryReader]::new($stream)
    try {
        if ($stream.Length -lt 0x86 -or $reader.ReadUInt16() -ne 0x5A4D) { throw 'native helper is not a PE executable' }
        $stream.Position = 0x3C
        $peOffset = $reader.ReadInt32()
        if ($peOffset -lt 0x40 -or $peOffset -gt $stream.Length - 6) { throw 'native helper PE header is invalid' }
        $stream.Position = $peOffset
        if ($reader.ReadUInt32() -ne 0x00004550) { throw 'native helper PE signature is invalid' }
        $machine = $reader.ReadUInt16()
        if ($machine -ne 0x8664) { throw 'native helper is not x64' }
        return 'x64'
    }
    finally { $reader.Dispose(); $stream.Dispose() }
}

function Export-CnMonMsixPublicSignerCertificate {
    param(
        [Parameter(Mandatory = $true)][Security.Cryptography.X509Certificates.X509Certificate2]$Certificate,
        [Parameter(Mandatory = $true)][string]$Path
    )
    if ($Certificate.HasPrivateKey -or [string]::IsNullOrWhiteSpace($Certificate.Thumbprint) -or (Test-Path -LiteralPath $Path)) {
        throw 'public signer certificate export input is invalid'
    }
    $bytes = $Certificate.Export([Security.Cryptography.X509Certificates.X509ContentType]::Cert)
    $created = $false
    try {
        $stream = [IO.File]::Open($Path, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
        $created = $true
        try { $stream.Write($bytes, 0, $bytes.Length) }
        finally { $stream.Dispose() }
        $publicOnly = [Security.Cryptography.X509Certificates.X509Certificate2]::new($Path)
    }
    catch {
        if ($created) { Remove-Item -LiteralPath $Path -Force -ErrorAction SilentlyContinue }
        throw 'public signer certificate export failed'
    }
    try {
        if ($publicOnly.HasPrivateKey -or $publicOnly.Thumbprint -cne $Certificate.Thumbprint) {
            throw 'public signer certificate verification failed'
        }
    }
    catch {
        if ($created) { Remove-Item -LiteralPath $Path -Force -ErrorAction SilentlyContinue }
        throw 'public signer certificate verification failed'
    }
    finally { $publicOnly.Dispose() }
    return $Certificate.Thumbprint
}

function Get-CnMonMsixClientInputPath {
    param([string]$Path)
    $item = Get-Item -LiteralPath $Path -ErrorAction Stop
    if (-not $item.PSIsContainer -and ($item.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
        throw 'input file is a reparse point'
    }
    return [IO.Path]::GetFullPath($item.FullName)
}

function Get-CnMonMsixDesktopInputPath {
    param([Parameter(Mandatory = $true)][string]$Path)
    try { return Get-CnMonMsixClientInputPath -Path $Path }
    catch { throw 'a required desktop qualification input is unavailable' }
}

function Remove-CnMonMsixDesktopOwnedOutput {
    param([string]$Path, [bool]$CreatedByThisRun)
    if ($CreatedByThisRun -and -not [string]::IsNullOrWhiteSpace($Path)) {
        Remove-Item -LiteralPath $Path -Force -ErrorAction SilentlyContinue
    }
}

function Get-CnMonMsixDesktopInteractiveSession {
    if ($null -eq ('CnMonMsixDesktopSessionNative' -as [type])) {
        Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class CnMonMsixDesktopSessionNative
{
    [DllImport("kernel32.dll", SetLastError=true)]
    public static extern bool CloseHandle(IntPtr handle);
    [DllImport("wtsapi32.dll", CharSet=CharSet.Unicode, SetLastError=true)]
    public static extern bool WTSQuerySessionInformationW(IntPtr server, UInt32 sessionId, int infoClass, out IntPtr buffer, out UInt32 bytesReturned);
    [DllImport("wtsapi32.dll")]
    public static extern void WTSFreeMemory(IntPtr memory);
    [DllImport("user32.dll", SetLastError=true)]
    public static extern IntPtr OpenInputDesktop(UInt32 flags, bool inherit, UInt32 desiredAccess);
    [DllImport("user32.dll", SetLastError=true)]
    public static extern bool CloseDesktop(IntPtr desktop);
    [DllImport("advapi32.dll", SetLastError=true)]
    public static extern bool OpenProcessToken(IntPtr processHandle, UInt32 desiredAccess, out IntPtr tokenHandle);
    [DllImport("advapi32.dll", SetLastError=true)]
    public static extern bool GetTokenInformation(IntPtr tokenHandle, int informationClass, IntPtr information, UInt32 informationLength, out UInt32 returnLength);
    [DllImport("advapi32.dll", SetLastError=true)]
    public static extern IntPtr GetSidSubAuthorityCount(IntPtr sid);
    [DllImport("advapi32.dll", SetLastError=true)]
    public static extern IntPtr GetSidSubAuthority(IntPtr sid, UInt32 index);
}
'@ -ErrorAction Stop
    }
    $sessionId = [Diagnostics.Process]::GetCurrentProcess().SessionId
    [IntPtr]$stateBuffer = [IntPtr]::Zero
    [uint32]$stateBytes = 0
    $sessionActive = $false
    try {
        if ($sessionId -gt 0 -and [CnMonMsixDesktopSessionNative]::WTSQuerySessionInformationW([IntPtr]::Zero, [uint32]$sessionId, 8, [ref]$stateBuffer, [ref]$stateBytes) -and $stateBuffer -ne [IntPtr]::Zero -and $stateBytes -ge 4) {
            $sessionActive = [Runtime.InteropServices.Marshal]::ReadInt32($stateBuffer) -eq 0
        }
    }
    finally { if ($stateBuffer -ne [IntPtr]::Zero) { [CnMonMsixDesktopSessionNative]::WTSFreeMemory($stateBuffer) } }
    $desktop = [CnMonMsixDesktopSessionNative]::OpenInputDesktop(0, $false, 0x0001)
    $desktopAvailable = $desktop -ne [IntPtr]::Zero
    if ($desktopAvailable) { $null = [CnMonMsixDesktopSessionNative]::CloseDesktop($desktop) }
    return [pscustomobject]@{ SessionId = [int]$sessionId; SessionActive = [bool]$sessionActive; InputDesktopAvailable = [bool]$desktopAvailable }
}

function Test-CnMonMsixMediumIntegrity {
    $processHandle = [Diagnostics.Process]::GetCurrentProcess().Handle
    [IntPtr]$token = [IntPtr]::Zero
    if (-not [CnMonMsixDesktopSessionNative]::OpenProcessToken($processHandle, 0x0008, [ref]$token)) { return $false }
    [IntPtr]$buffer = [IntPtr]::Zero
    try {
        [uint32]$size = 0
        $null = [CnMonMsixDesktopSessionNative]::GetTokenInformation($token, 25, [IntPtr]::Zero, 0, [ref]$size)
        if ($size -lt [uint32][IntPtr]::Size) { return $false }
        $buffer = [Runtime.InteropServices.Marshal]::AllocHGlobal([int]$size)
        if (-not [CnMonMsixDesktopSessionNative]::GetTokenInformation($token, 25, $buffer, $size, [ref]$size)) { return $false }
        $sid = [Runtime.InteropServices.Marshal]::ReadIntPtr($buffer)
        $countPointer = [CnMonMsixDesktopSessionNative]::GetSidSubAuthorityCount($sid)
        if ($countPointer -eq [IntPtr]::Zero) { return $false }
        $count = [Runtime.InteropServices.Marshal]::ReadByte($countPointer)
        if ($count -eq 0) { return $false }
        $ridPointer = [CnMonMsixDesktopSessionNative]::GetSidSubAuthority($sid, [uint32]($count - 1))
        if ($ridPointer -eq [IntPtr]::Zero) { return $false }
        return [Runtime.InteropServices.Marshal]::ReadInt32($ridPointer) -eq 0x2000
    }
    finally {
        if ($buffer -ne [IntPtr]::Zero) { [Runtime.InteropServices.Marshal]::FreeHGlobal($buffer) }
        $null = [CnMonMsixDesktopSessionNative]::CloseHandle($token)
    }
}

function Test-CnMonMsixSignerCertificate {
    param(
        [Parameter(Mandatory = $true)][Security.Cryptography.X509Certificates.X509Certificate2]$Certificate,
        [Parameter(Mandatory = $true)][string]$ExpectedThumbprint,
        [Parameter(Mandatory = $true)][string]$ExpectedPublisherSubject,
        [DateTime]$Now = [DateTime]::Now
    )
    $lifetime = $Certificate.NotAfter - $Certificate.NotBefore
    return -not [string]::IsNullOrWhiteSpace($Certificate.Thumbprint) -and
        $Certificate.Thumbprint -ceq $ExpectedThumbprint -and
        $Certificate.Subject -ceq $ExpectedPublisherSubject -and
        $Now -ge $Certificate.NotBefore -and $Now -le $Certificate.NotAfter -and
        $lifetime -gt [TimeSpan]::Zero -and $lifetime -le [TimeSpan]::FromHours(6.5)
}

function Get-CnMonMsixDesktopSignatureState {
    param(
        [Parameter(Mandatory = $true)][Security.Cryptography.X509Certificates.X509Certificate2]$Certificate,
        [Parameter(Mandatory = $true)][string]$SignatureStatus,
        [Parameter(Mandatory = $true)][string]$ExpectedThumbprint,
        [Parameter(Mandatory = $true)][string]$ExpectedPublisherSubject,
        [DateTime]$Now = [DateTime]::Now
    )
    if ($SignatureStatus -cnotin @('Valid', 'UnknownError', 'NotTrusted') -or
        -not (Test-CnMonMsixSignerCertificate -Certificate $Certificate -ExpectedThumbprint $ExpectedThumbprint -ExpectedPublisherSubject $ExpectedPublisherSubject -Now $Now)) {
        throw 'candidate signature or signer does not match the bound receipt'
    }
    $signatureValid = $SignatureStatus -ceq 'Valid'
    return [pscustomobject]@{
        SignatureValid = [bool]$signatureValid
        SignatureStatus = if ($signatureValid) { 'valid' } else { 'signature_unverified' }
        SignerMatchesReceipt = $true
        SignerValidNow = $true
    }
}

$qualificationGates = @('identity', 'package_hash', 'signature', 'account', 'package_registration', 'exact_identity', 'installed_binary_hash', 'alias_identity', 'native_cli', 'icmp', 'history_config', 'shell_aliases', 'upgrade', 'repair', 'uninstall_reinstall', 'conflicts', 'fail_injection', 'cleanup', 'wack')
$qualificationScriptPath = Join-Path $PSScriptRoot 'test-msix-windows.ps1'
$validationHelperNames = @(
    'Set-CnMonMsixGate',
    'Get-CnMonMsixReceipt',
    'Assert-CnMonMsixReceipt',
    'Get-CnMonMsixResultEvidence',
    'Assert-CnMonMsixResultEvidence'
)
$sourceErrors = $null
$sourceAst = [System.Management.Automation.Language.Parser]::ParseFile($qualificationScriptPath, [ref]$null, [ref]$sourceErrors)
if ($sourceErrors.Count -gt 0) { throw 'qualification validation source did not parse' }
$validationHelpers = @($sourceAst.FindAll({
    param($candidate)
    $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
        $validationHelperNames -contains $candidate.Name
}, $true))
if (@($validationHelpers | ForEach-Object Name | Select-Object -Unique).Count -ne $validationHelperNames.Count) {
    throw 'fixed qualification validation helper set is incomplete'
}
foreach ($helper in $validationHelpers) { . ([scriptblock]::Create($helper.Extent.Text)) }

if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) { throw 'desktop checks require native Windows' }
if ($PSVersionTable.PSVersion.Major -lt 7) { throw 'PowerShell 7 or later is required for desktop checks' }

$modulePath = Join-Path $PSScriptRoot 'msix-package.psm1'
Import-Module $modulePath -Force -ErrorAction Stop
$script:CurrentGate = 'identity'
$script:Result = [pscustomobject]@{ gates = [ordered]@{} }
$script:GateUpdates = [ordered]@{}
$script:SignedCatalogFootprintPresent = $false

$originalIdentityEnvironment = @{}
foreach ($name in @('CNMON_MSIX_IDENTITY_NAME', 'CNMON_MSIX_IDENTITY_PUBLISHER', 'CNMON_MSIX_PUBLISHER_DISPLAY_NAME', 'CNMON_MSIX_EXPECTED_PFN')) {
    $originalIdentityEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}
$summaryPath = $null
$certificatePath = $null
$summaryCreated = $false
$certificateCreated = $false
try {
    $packageFullPath = Get-CnMonMsixDesktopInputPath $PackagePath
    $receiptFullPath = Get-CnMonMsixDesktopInputPath $ReceiptPath
    $resultsFullPath = Get-CnMonMsixDesktopInputPath $ResultsPath
    $helperFullPath = Get-CnMonMsixDesktopInputPath $NativeHelperPath
    foreach ($path in @($packageFullPath, $receiptFullPath, $resultsFullPath, $helperFullPath)) {
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw 'a required desktop qualification input is missing' }
    }
    if (-not (Test-Path -LiteralPath $ArtifactDirectory -PathType Container)) { throw 'private output directory must already exist' }
    $artifactFullPath = [IO.Path]::GetFullPath($ArtifactDirectory)
    $summaryPath = Join-Path $artifactFullPath 'msix-desktop-supplement.json'
    $certificatePath = Join-Path $artifactFullPath 'msix-test-signer.cer'
    if ((Test-Path -LiteralPath $summaryPath) -or (Test-Path -LiteralPath $certificatePath)) { throw 'desktop supplement output already exists' }
    if ($ExpectedNativeHelperSha256 -cnotmatch '^[0-9a-f]{64}$') { throw 'expected native helper hash is invalid' }
    $helperHash = (Get-FileHash -LiteralPath $helperFullPath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($helperHash -cne $ExpectedNativeHelperSha256) { throw 'native helper hash does not match its supplied bundle hash' }
    $null = Get-CnMonMsixDesktopPeMachine -Path $helperFullPath

    $receipt = Get-CnMonMsixReceipt -Path $receiptFullPath
    foreach ($pair in @(
        @{ Name = 'CNMON_MSIX_IDENTITY_NAME'; Value = [string]$receipt.name },
        @{ Name = 'CNMON_MSIX_IDENTITY_PUBLISHER'; Value = [string]$receipt.publisher },
        @{ Name = 'CNMON_MSIX_PUBLISHER_DISPLAY_NAME'; Value = [string]$receipt.publisher_display_name },
        @{ Name = 'CNMON_MSIX_EXPECTED_PFN'; Value = [string]$receipt.expected_pfn }
    )) { [Environment]::SetEnvironmentVariable($pair.Name, $pair.Value, 'Process') }

    $identity = Assert-CnMonMsixReceipt -Receipt $receipt -Path $packageFullPath -SigningStatus test -RequireCandidate
    $sidecarKey = 'cn-mon.exe.manifest'
    if ($receipt.files -isnot [System.Collections.IDictionary] -or
        -not $receipt.files.Contains($sidecarKey) -or [string]$receipt.files[$sidecarKey] -cnotmatch '^[0-9a-f]{64}$') {
        throw 'signed receipt does not bind the executable DPI sidecar'
    }

    $expectedEvidence = Get-CnMonMsixResultEvidence -PackagePath $packageFullPath -ReceiptPath $receiptFullPath -Receipt $receipt -SourceRevision ([string]$receipt.packaging_revision)
    $resultsJson = [Text.UTF8Encoding]::new($false, $true).GetString([IO.File]::ReadAllBytes($resultsFullPath))
    $result = Read-CnMonMsixDesktopResult -Json $resultsJson -ExpectedEvidence $expectedEvidence -QualificationGates $qualificationGates

    $signature = Get-AuthenticodeSignature -LiteralPath $packageFullPath
    $signer = $signature.SignerCertificate
    if ($null -eq $signer) { throw 'candidate signer or signature status does not match the bound receipt' }
    $signatureState = Get-CnMonMsixDesktopSignatureState -Certificate $signer -SignatureStatus ([string]$signature.Status) `
        -ExpectedThumbprint ([string]$receipt.signing.certificate_thumbprint) -ExpectedPublisherSubject ([string]$identity.Publisher)
    $certificateThumbprint = Export-CnMonMsixPublicSignerCertificate -Certificate $signer -Path $certificatePath
    $certificateCreated = $true
    if ($certificateThumbprint -cne [string]$receipt.signing.certificate_thumbprint) {
        throw 'extracted public signer certificate does not match the signed receipt'
    }
    $certificateValidNow = $signatureState.SignerValidNow
    $certificateTrusted = $false
    try {
        $certificateTrusted = @(Get-ChildItem -LiteralPath 'Cert:\LocalMachine\TrustedPeople' -ErrorAction Stop | Where-Object {
            $_.Thumbprint -ceq $certificateThumbprint
        }).Count -gt 0
    }
    catch { }

    $desktopIdentity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $desktopPrincipal = [Security.Principal.WindowsPrincipal]::new($desktopIdentity)
    $groupSids = @($desktopIdentity.Groups | ForEach-Object { $_.Value })
    $desktopSession = Get-CnMonMsixDesktopInteractiveSession
    $mediumIntegrity = Test-CnMonMsixMediumIntegrity
    $administratorRole = $desktopPrincipal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
    $administratorsSidPresent = $groupSids -contains 'S-1-5-32-544'
    $userTokenEligible = $null -ne $desktopIdentity.User -and
        -not [string]::IsNullOrWhiteSpace($desktopIdentity.User.Value) -and
        -not $administratorRole -and -not $administratorsSidPresent
    $interactiveSessionEligible = [Environment]::UserInteractive -and
        $desktopSession.SessionId -gt 0 -and $desktopSession.SessionActive
    $desktopEligible = Test-CnMonMsixDesktopEligibility `
        -UserSidPresent:($null -ne $desktopIdentity.User -and -not [string]::IsNullOrWhiteSpace($desktopIdentity.User.Value)) `
        -AdministratorRole:($administratorRole) `
        -AdministratorsSidPresent:($administratorsSidPresent) `
        -UserInteractive:([Environment]::UserInteractive) `
        -SessionId $desktopSession.SessionId `
        -SessionActive:($desktopSession.SessionActive) `
        -InputDesktopAvailable ($desktopSession.InputDesktopAvailable) `
        -MediumIntegrity:($mediumIntegrity)

    $existingCandidate = @(Get-AppxPackage -Name ([string]$identity.Name) -ErrorAction Stop | Where-Object {
        [string]$_.Name -ceq [string]$identity.Name -and
        [string]$_.Publisher -ceq [string]$identity.Publisher -and
        [string]$_.PackageFamilyName -ceq [string]$identity.ExpectedPFN
    }).Count -gt 0
    $candidateNotRegistered = -not $existingCandidate

    $packageHash = [string]$expectedEvidence.signed_package_sha256
    $receiptHash = [string]$expectedEvidence.signed_receipt_sha256
    $revision = [string]$expectedEvidence.packaging_revision
    $evidence = [ordered]@{
        signed_package_sha256 = $packageHash
        signed_receipt_sha256 = $receiptHash
        packaging_revision = $revision
        native_helper_sha256 = $helperHash
    }
    $supplement = Get-CnMonMsixDesktopSupplement -Evidence $evidence `
        -PackageReceiptBound:$true -CertifyResultBound:$true -WackPass:([string]$result.gates.wack -ceq 'pass') `
        -SignerMatchesReceipt:$signatureState.SignerMatchesReceipt `
        -SignerValidNow:$certificateValidNow -MachineTrustedPeople:$certificateTrusted `
        -SignatureValid:$signatureState.SignatureValid -SignatureStatus $signatureState.SignatureStatus `
        -DesktopEligible:$desktopEligible -UserTokenEligible:$userTokenEligible `
        -InteractiveSessionEligible:$interactiveSessionEligible -InputDesktopAvailable:$desktopSession.InputDesktopAvailable `
        -MediumIntegrity:$mediumIntegrity -CandidateNotRegistered:$candidateNotRegistered -NativeHelperX64:$true
    $summaryBytes = [Text.UTF8Encoding]::new($false).GetBytes((ConvertTo-Json -InputObject $supplement -Depth 8) + [Environment]::NewLine)
    $summaryStream = [IO.File]::Open($summaryPath, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
    $summaryCreated = $true
    try { $summaryStream.Write($summaryBytes, 0, $summaryBytes.Length) }
    finally { $summaryStream.Dispose() }
    Write-Host "Desktop client preflight: $($supplement.preflight_status); repair, conflicts, screenshots, and cleanup remain incomplete."
}
catch {
    Remove-CnMonMsixDesktopOwnedOutput -Path $summaryPath -CreatedByThisRun:$summaryCreated
    Remove-CnMonMsixDesktopOwnedOutput -Path $certificatePath -CreatedByThisRun:$certificateCreated
    throw 'desktop client preflight failed; no package or trust changes were attempted'
}
finally {
    foreach ($name in $originalIdentityEnvironment.Keys) {
        [Environment]::SetEnvironmentVariable($name, $originalIdentityEnvironment[$name], 'Process')
    }
}
