[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][ValidateSet('Hosted', 'User', 'Certify')][string]$Mode,
    [Parameter(Mandatory = $true)][string]$PackagePath,
    [Parameter(Mandatory = $true)][string]$ReceiptPath,
    [Parameter(Mandatory = $true)][string]$SdkBinDirectory,
    [Parameter(Mandatory = $true)][string]$ArtifactDirectory,
    [string]$HostedResultsPath,
    [string]$ReleaseMetadataPath,
    [string]$ExpectedSid,
    [switch]$FailAfterInstall
)

$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
$script:Result = [ordered]@{
    schema_version = 2
    mode = $Mode
    overall = 'incomplete'
    evidence = $null
    gates = [ordered]@{}
}
$script:AllowResultWrite = $true
$qualificationGates = @('identity', 'package_hash', 'signature', 'account', 'package_registration', 'exact_identity', 'installed_binary_hash', 'alias_identity', 'native_cli', 'icmp', 'history_config', 'shell_aliases', 'upgrade', 'repair', 'uninstall_reinstall', 'conflicts', 'fail_injection', 'cleanup', 'wack')
foreach ($gate in $qualificationGates) { $script:Result.gates[$gate] = 'incomplete' }
$script:CurrentGate = 'identity'
$script:Failure = $false
$script:InjectedFailure = $false
$script:GateUpdates = [ordered]@{}
$script:OwnedPackageSpecs = [Collections.Generic.List[object]]::new()
$script:CreatedCandidate = $false
$script:CandidateOutputPath = $null
$script:ReleaseMetadataPath = if ([string]::IsNullOrWhiteSpace($ReleaseMetadataPath)) {
    Join-Path (Split-Path -Parent $PSScriptRoot) 'packaging/windows/release.json'
} else { [IO.Path]::GetFullPath($ReleaseMetadataPath) }
$script:CreatedReceipt = $false
$script:SignedOutputsReady = $false
$script:CreatedFixture = $false
$script:CreatedFixtureFiles = @()
$script:CreatedCertificate = $null
$script:CreatedTrustedCertificate = $false
$script:CreatedUser = $null
$script:CreatedUserSid = $null
$script:CreatedProfile = $null
$script:OriginalArtifactAcl = $null
$script:PrivateRoot = $null
$script:IdentityName = $null
$script:DiagnosticIdentity = $null
$script:OwnedProfileFiles = @()
$script:LastSdkToolExitCode = $null
$script:LastSdkToolName = $null
$script:LastSdkToolLogPath = $null
$script:LastSignatureVerifyExitCode = $null
$script:CurrentDiagnosticPhase = 'preflight'
$script:CurrentDiagnosticOperation = 'none'
$script:SignedCatalogFootprintPresent = $false
$script:TrustPresenceChecked = $false
$script:TrustedCertificatePresent = $false
$script:LastSignatureLogPath = $null
$script:HostedResultSnapshot = $null
$script:AllowResultWrite = $true
$script:CurrentSourceOperation = 'script_entry'
$script:FailureSourceOperation = $null
$script:PublicDiagnosticWritten = $false
$script:NativeTestFailure = $null

$repositoryRoot = Split-Path -Parent $PSScriptRoot
$modulePath = Join-Path $PSScriptRoot 'msix-package.psm1'
Import-Module $modulePath -Force -ErrorAction Stop
$script:ReleaseMetadata = Get-Content -LiteralPath $script:ReleaseMetadataPath -Raw -ErrorAction Stop | ConvertFrom-Json -ErrorAction Stop
$null = Assert-CnMonMsixReleaseMetadata -Release $script:ReleaseMetadata

function Set-CnMonMsixGate {
    param([string]$Name, [ValidateSet('pass', 'fail', 'incomplete')][string]$Status)
    $script:Result.gates[$Name] = $Status
    $script:GateUpdates[$Name] = $Status
}

function Set-CnMonMsixCaughtGateFailure {
    param([string]$Name)
    Set-CnMonMsixGate $Name 'fail'
}

function Get-CnMonMsixFailureDiagnostic {
    param(
        [string]$Mode,
        [string]$Gate,
        [string]$Operation,
        [System.Management.Automation.ErrorRecord]$ErrorRecord
    )
    $allowedGates = @('identity', 'package_hash', 'signature', 'account', 'package_registration', 'exact_identity', 'installed_binary_hash', 'alias_identity', 'native_cli', 'icmp', 'history_config', 'shell_aliases', 'upgrade', 'repair', 'uninstall_reinstall', 'conflicts', 'fail_injection', 'cleanup', 'wack')
    $allowedOperations = @(
        'script_entry', 'hosted_preflight', 'user_mode_account_validation', 'identity_read', 'receipt_validation',
        'signature_setup', 'temporary_user_account_create', 'temporary_user_profile_initialize',
        'temporary_user_profile_wait', 'temporary_user_profile_validate', 'user_run_environment_setup',
        'temporary_user_acl_setup', 'user_run_process_start', 'user_run_process_wait', 'user_run_result_read',
        'user_mode_package_registration', 'user_mode_native_qualification', 'cleanup', 'unknown'
    )
    $allowedExceptionTypes = @('RuntimeException', 'MethodInvocationException', 'InvalidOperationException', 'IdentityNotMappedException', 'UnauthorizedAccessException', 'SecurityException', 'Win32Exception', 'IOException', 'FileNotFoundException', 'DirectoryNotFoundException', 'ArgumentException', 'ParameterBindingException', 'TimeoutException', 'NotSupportedException', 'ProcessStartException', 'other')
    $modeValue = if ($Mode -in @('Hosted', 'User')) { $Mode } else { 'unknown' }
    $gateValue = if ($Gate -in $allowedGates) { $Gate } else { 'unknown' }
    $operationValue = if ($Operation -in $allowedOperations) { $Operation } else { 'unknown' }

    $exceptions = [Collections.Generic.List[Exception]]::new()
    $current = if ($null -ne $ErrorRecord) { $ErrorRecord.Exception } else { $null }
    while ($null -ne $current -and $exceptions.Count -lt 8) {
        $exceptions.Add($current)
        $current = $current.InnerException
    }
    $typeName = if ($exceptions.Count -gt 0) { $exceptions[0].GetType().Name } else { 'other' }
    $innerTypeName = if ($exceptions.Count -gt 1) { $exceptions[1].GetType().Name } else { 'none' }
    $exceptionType = if ($typeName -in $allowedExceptionTypes) { $typeName } else { 'other' }
    $innerExceptionType = if ($innerTypeName -in $allowedExceptionTypes) { $innerTypeName } else { 'none' }

    $errorCategory = 'other'
    if (@($exceptions | Where-Object { $_.GetType().Name -eq 'IdentityNotMappedException' }).Count -gt 0) { $errorCategory = 'identity_mapping' }
    elseif (@($exceptions | Where-Object { $_.GetType().Name -eq 'Win32Exception' }).Count -gt 0) { $errorCategory = 'win32_error' }
    elseif (@($exceptions | Where-Object { $_.GetType().Name -eq 'UnauthorizedAccessException' }).Count -gt 0) { $errorCategory = 'access_denied' }
    elseif (@($exceptions | Where-Object { $_.GetType().Name -match 'Security' }).Count -gt 0) { $errorCategory = 'security' }
    elseif (@($exceptions | Where-Object { $_.GetType().Name -eq 'TimeoutException' }).Count -gt 0) { $errorCategory = 'timeout' }
    elseif (@($exceptions | Where-Object { $_.GetType().Name -in @('FileNotFoundException', 'DirectoryNotFoundException') }).Count -gt 0) { $errorCategory = 'not_found' }
    elseif (@($exceptions | Where-Object { $_.GetType().Name -in @('IOException', 'FileLoadException') }).Count -gt 0) { $errorCategory = 'io' }
    elseif (@($exceptions | Where-Object { $_.GetType().Name -in @('ArgumentException', 'ParameterBindingException') }).Count -gt 0) { $errorCategory = 'invalid_argument' }
    elseif (@($exceptions | Where-Object { $_.GetType().Name -eq 'InvalidOperationException' }).Count -gt 0) { $errorCategory = 'invalid_state' }
    elseif ($operationValue -eq 'user_run_process_start') { $errorCategory = 'process_start' }

    $baseException = if ($exceptions.Count -gt 0) { $exceptions[$exceptions.Count - 1] } else { $null }
    $hresult = if ($null -ne $baseException) { [int]$baseException.HResult } else { $null }
    $nativeErrorCode = $null
    foreach ($exception in $exceptions) {
        $nativeProperty = $exception.PSObject.Properties['NativeErrorCode']
        if ($null -ne $nativeProperty -and $nativeProperty.Value -is [ValueType]) {
            $nativeErrorCode = [int]$nativeProperty.Value
            break
        }
    }

    $scriptLine = 0
    if ($null -ne $ErrorRecord -and $null -ne $ErrorRecord.InvocationInfo -and $ErrorRecord.InvocationInfo.ScriptLineNumber -gt 0) {
        $scriptLine = [int]$ErrorRecord.InvocationInfo.ScriptLineNumber
    }
    $stackLines = [Collections.Generic.List[int]]::new()
    if ($null -ne $ErrorRecord -and $ErrorRecord.ScriptStackTrace) {
        foreach ($match in [regex]::Matches([string]$ErrorRecord.ScriptStackTrace, '(?i):\s*line\s+(\d+)\b')) {
            $lineNumber = 0
            if ([int]::TryParse($match.Groups[1].Value, [ref]$lineNumber)) { $stackLines.Add($lineNumber) }
            if ($stackLines.Count -ge 8) { break }
        }
    }

    return [ordered]@{
        mode = $modeValue
        gate = $gateValue
        operation = $operationValue
        error_category = $errorCategory
        exception_type = $exceptionType
        inner_exception_type = $innerExceptionType
        hresult = $hresult
        native_error_code = $nativeErrorCode
        script_line = $scriptLine
        stack_lines = @($stackLines.ToArray())
    }
}

function Save-CnMonMsixQualificationFailureEvidence {
    param(
        [string]$Mode,
        [string]$Gate,
        [string]$Operation,
        [string]$ArtifactDirectory,
        [object]$Identity,
        [System.Management.Automation.ErrorRecord]$ErrorRecord,
        [switch]$PrivateOnly
    )
    try {
        $privateDirectory = Join-Path $ArtifactDirectory 'private-failure-records'
        New-Item -ItemType Directory -Force -Path $privateDirectory | Out-Null
        $privatePath = Join-Path $privateDirectory ($Mode + '-' + [Guid]::NewGuid().ToString('N') + '.clixml')
        Export-Clixml -InputObject $ErrorRecord -LiteralPath $privatePath -Depth 8 -Force -ErrorAction Stop
    }
    catch { }

    if ($PrivateOnly) { return }
    if (-not (Test-CnMonMsixSyntheticPrDiagnostic -Mode $Mode -Identity $Identity)) { return }
    try {
        $publicPath = Join-Path $ArtifactDirectory 'msix-signature-diagnostic.json'
        $diagnostic = Get-CnMonMsixFailureDiagnostic -Mode $Mode -Gate $Gate -Operation $Operation -ErrorRecord $ErrorRecord
        $public = [ordered]@{}
        if (Test-Path -LiteralPath $publicPath -PathType Leaf) {
            if (-not $script:PublicDiagnosticWritten) { return }
            $existing = Get-Content -LiteralPath $publicPath -Raw | ConvertFrom-Json -AsHashtable -ErrorAction Stop
            foreach ($key in $existing.Keys) { $public[$key] = $existing[$key] }
        }
        else {
            $public.schema_version = 1
            $script:PublicDiagnosticWritten = $true
        }
        if ($Mode -eq 'User' -and $Gate -in @('history_config', 'repair', 'uninstall_reinstall') -and
            $Operation -eq 'user_mode_native_qualification' -and $null -ne $script:NativeTestFailure) {
            $diagnostic.native_test = $script:NativeTestFailure
        }
        $public.qualification_failure = $diagnostic
        [IO.File]::WriteAllText($publicPath, (ConvertTo-Json -InputObject $public -Depth 5) + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
    }
    catch { }
}

function Write-CnMonMsixResult {
    param([string]$Directory)
    if (-not (Test-Path -LiteralPath $Directory -PathType Container)) {
        return
    }
    $path = Join-Path $Directory 'msix-native-results.json'
    $json = ConvertTo-Json -InputObject $script:Result -Depth 8
    [IO.File]::WriteAllText($path, $json + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
}

function Set-CnMonMsixOverall {
    param([string]$ForMode)
    $required = switch ($ForMode) {
        'Hosted' { @('identity', 'package_hash', 'signature', 'account', 'package_registration', 'exact_identity', 'installed_binary_hash', 'alias_identity', 'native_cli', 'icmp', 'history_config', 'shell_aliases', 'upgrade', 'repair', 'uninstall_reinstall', 'conflicts', 'fail_injection', 'cleanup') }
        'User' { @('identity', 'package_hash', 'signature', 'account', 'package_registration', 'exact_identity', 'installed_binary_hash', 'alias_identity', 'native_cli', 'icmp', 'history_config', 'shell_aliases', 'upgrade', 'repair', 'uninstall_reinstall', 'conflicts', 'cleanup') }
        'Certify' { @('identity', 'package_hash', 'signature', 'account', 'package_registration', 'exact_identity', 'installed_binary_hash', 'alias_identity', 'native_cli', 'icmp', 'history_config', 'shell_aliases', 'upgrade', 'repair', 'uninstall_reinstall', 'conflicts', 'fail_injection', 'cleanup', 'wack') }
    }
    $statuses = @($required | ForEach-Object { if ($script:Result.gates.Contains($_)) { $script:Result.gates[$_] } else { 'incomplete' } })
    if ($statuses -contains 'fail') {
        $script:Result.overall = 'fail'
    }
    elseif ($statuses -contains 'incomplete') {
        $script:Result.overall = 'incomplete'
    }
    else {
        $script:Result.overall = 'pass'
    }
}

function Write-CnMonMsixStatus {
    param([string]$Message)
    Write-Host "MSIX qualification: $Message"
}

function Assert-CnMonMsixWindows {
    if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
        $script:CurrentGate = 'account'
        Set-CnMonMsixGate 'account' 'incomplete'
        throw 'Windows host required'
    }
}

function Get-CnMonMsixReceipt {
    param([string]$Path)
    try {
        return Get-Content -LiteralPath $Path -Raw | ConvertFrom-Json -AsHashtable -ErrorAction Stop
    }
    catch {
        throw 'receipt could not be parsed'
    }
}

function Get-CnMonMsixSignedCandidatePath {
    param([string]$Directory, [string]$PackageVersion)
    if ($PackageVersion -cnotmatch '^\d+\.\d+\.\d+\.\d+$') { throw 'candidate package version is invalid' }
    return Join-Path $Directory ("cn-mon_{0}_x64-signed.msix" -f $PackageVersion)
}

function Get-CnMonMsixReleasePackageVersion {
    param([string]$ReleasePath = $script:ReleaseMetadataPath)
    try { $version = [string]((Get-Content -LiteralPath $ReleasePath -Raw -ErrorAction Stop | ConvertFrom-Json -ErrorAction Stop).package_version) }
    catch { throw 'release package version could not be read' }
    if ($version -cnotmatch '^\d+\.\d+\.\d+\.\d+$') { throw 'release package version is invalid' }
    return $version
}

function Assert-CnMonMsixReceipt {
    param(
        [System.Collections.IDictionary]$Receipt,
        [string]$Path,
        [ValidateSet('unsigned', 'test')][string]$SigningStatus,
        [switch]$RequireCandidate
    )
    $script:CurrentGate = 'identity'
    $identity = Read-CnMonMsixIdentity
    $expectedPackageVersion = Get-CnMonMsixReleasePackageVersion -ReleasePath $script:ReleaseMetadataPath
    if ([int]$Receipt.schema_version -ne 1 -or
        [string]$Receipt.name -cne $identity.Name -or
        [string]$Receipt.publisher -cne $identity.Publisher -or
        [string]$Receipt.publisher_display_name -cne $identity.PublisherDisplayName -or
        [string]$Receipt.expected_pfn -cne $identity.ExpectedPFN -or
        [string]$Receipt.architecture -cne 'x64' -or
        [string]$Receipt.package_version -cne $expectedPackageVersion -or
        [string]$Receipt.cli_version -cne [string]$script:ReleaseMetadata.cli_version -or
        [string]$Receipt.cli_revision -cne [string]$script:ReleaseMetadata.cli_revision -or
        [string]$Receipt.archive_sha256 -cne [string]$script:ReleaseMetadata.archive_sha256 -or
        [string]$Receipt.binary_sha256 -cne [string]$script:ReleaseMetadata.binary_sha256 -or
        [string]$Receipt.purpose -cne 'candidate' -or
        [string]$Receipt.packaging_revision -notmatch '^[0-9a-f]{40}$' -or
        $null -eq $Receipt.files -or
        [string]$Receipt.manifest_sha256 -notmatch '^[0-9a-f]{64}$') {
        throw 'receipt does not match the approved candidate contract'
    }
    if ($RequireCandidate -and [string]$Receipt.purpose -cne 'candidate') {
        throw 'receipt purpose is not candidate'
    }
    if ($SigningStatus -eq 'unsigned' -and [string]$Receipt.signing -cne 'unsigned') {
        throw 'candidate receipt is not unsigned'
    }
    if ($SigningStatus -eq 'test' -and
        ($null -eq $Receipt.signing -or [string]$Receipt.signing.status -cne 'test' -or
         [string]$Receipt.signing.certificate_thumbprint -notmatch '^[0-9A-Fa-f]{40}$' -or
         [string]$Receipt.signing.unsigned_package_sha256 -notmatch '^[0-9a-f]{64}$' -or
         [string]$Receipt.signing.signed_package_sha256 -notmatch '^[0-9a-f]{64}$')) {
        throw 'signed receipt is invalid'
    }
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw 'package file is missing'
    }
    $script:CurrentGate = 'package_hash'
    $hash = (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($hash -cne ([string]$Receipt.package_sha256).ToLowerInvariant()) {
        throw 'package hash does not match its receipt'
    }
    if ($SigningStatus -eq 'test' -and $hash -cne ([string]$Receipt.signing.signed_package_sha256).ToLowerInvariant()) {
        throw 'signed package hash does not match its receipt'
    }
    if (-not $Receipt.files.Contains('cn-mon.exe') -or
        [string]$Receipt.files['cn-mon.exe'] -cne ([string]$Receipt.binary_sha256).ToLowerInvariant() -or
        [string]$Receipt.files['AppxManifest.xml'] -cne ([string]$Receipt.manifest_sha256).ToLowerInvariant()) {
        throw 'receipt payload hash map is inconsistent'
    }
    Set-CnMonMsixGate 'identity' 'pass'
    Set-CnMonMsixGate 'package_hash' 'pass'
    return $identity
}

function Invoke-CnMonMsixTool {
    param([string]$Executable, [string[]]$Arguments, [string]$LogPath)
    $script:LastSdkToolExitCode = $null
    $script:LastSdkToolName = $null
    $script:LastSdkToolLogPath = $null
    $module = Get-Module -Name 'msix-package'
    $exit = & $module {
        param([string]$Path, [string[]]$ToolArguments, [string]$PrivateLog)
        Invoke-CnMonMsixTool -ExecutablePath $Path -Arguments $ToolArguments -LogPath $PrivateLog
    } $Executable $Arguments $LogPath
    if ($exit -ne 0) {
        $script:LastSdkToolExitCode = [int]$exit
        $script:LastSdkToolName = switch ([IO.Path]::GetFileName($Executable).ToLowerInvariant()) {
            'signtool.exe' { 'signtool'; break }
            'makeappx.exe' { 'makeappx'; break }
            default { 'other' }
        }
        $script:LastSdkToolLogPath = $LogPath
        throw 'a Windows SDK packaging tool failed; private output was retained'
    }
}

function Get-CnMonMsixSignedPayloadExpectedFiles {
    param([string]$Root, [System.Collections.IDictionary]$ExpectedFiles)
    $catalogRelative = 'AppxMetadata/CodeIntegrity.cat'
    if ($null -eq $ExpectedFiles -or $ExpectedFiles.Contains($catalogRelative)) {
        throw 'signed payload map already contains the Code Integrity catalog'
    }
    $catalogPath = Join-Path (Join-Path $Root 'AppxMetadata') 'CodeIntegrity.cat'
    $script:SignedCatalogFootprintPresent = Test-Path -LiteralPath $catalogPath -PathType Leaf
    $files = [ordered]@{}
    foreach ($path in $ExpectedFiles.Keys) { $files[[string]$path] = $ExpectedFiles[$path] }
    if ($script:SignedCatalogFootprintPresent) {
        $catalogHash = (Get-FileHash -LiteralPath $catalogPath -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($catalogHash -notmatch '^[0-9a-f]{64}$') {
            throw 'signed package Code Integrity catalog hash is invalid'
        }
        $files[$catalogRelative] = $catalogHash
    }
    return $files
}

function Assert-CnMonMsixPackagePayload {
    param([string]$Path, [System.Collections.IDictionary]$Files, [switch]$Signed)
    $signatureBlock = Join-Path $Path 'AppxSignature.p7x'
    if (Test-Path -LiteralPath $signatureBlock -PathType Leaf) {
        Remove-Item -LiteralPath $signatureBlock -Force
    }
    $expectedFiles = if ($Signed) { Get-CnMonMsixSignedPayloadExpectedFiles -Root $Path -ExpectedFiles $Files } else { $Files }
    msix-package\Assert-CnMonMsixPayload -Root $Path -ExpectedFiles $expectedFiles
}

function Get-CnMonMsixDiagnosticErrorCategory {
    param([string]$Message, [string]$ExceptionType, [string]$FailureKind)
    if ($FailureKind -in @('sdk_nonzero', 'sdk_invocation_error', 'trusted_certificate_missing')) { return $FailureKind }
    if ($ExceptionType -in @('ParameterBindingException', 'ParameterBindingValidationException', 'ParameterBindingArgumentTransformationException')) {
        return 'parameter_binding'
    }
    $prefixes = [ordered]@{
        'payload root does not exist' = 'payload_root_missing'
        'expected payload map contains an invalid path or SHA-256' = 'payload_expected_map_invalid'
        'payload contains a reparse point' = 'payload_reparse_point'
        'payload file missing:' = 'payload_missing'
        'payload hash mismatch:' = 'payload_hash_mismatch'
        'payload unexpected file:' = 'payload_unexpected'
        'signed package Code Integrity catalog is missing' = 'payload_catalog_missing'
        'signed package Code Integrity catalog hash is invalid' = 'payload_catalog_invalid'
    }
    foreach ($prefix in $prefixes.Keys) {
        if ($Message.StartsWith([string]$prefix, [StringComparison]::OrdinalIgnoreCase)) { return [string]$prefixes[$prefix] }
    }
    switch ($ExceptionType) {
        'InvalidOperationException' { return 'invalid_operation' }
        { $_ -in @('IOException', 'FileLoadException') } { return 'io' }
        'UnauthorizedAccessException' { return 'access_denied' }
        { $_ -in @('PathTooLongException', 'DirectoryNotFoundException') } { return 'path' }
        'ArgumentException' { return 'argument' }
        { $_ -match 'Security' } { return 'security' }
        default { return 'stage_exception' }
    }
}

function Get-CnMonMsixFixtureFiles {
    param(
        [System.Collections.IDictionary]$SourceFiles,
        [string]$BinaryHash,
        [string]$ManifestHash,
        [string]$ConfigHash
    )
    if ($null -eq $SourceFiles -or
        -not $SourceFiles.Contains('cn-mon.exe') -or
        -not $SourceFiles.Contains('AppxManifest.xml') -or
        $SourceFiles.Contains('.cn') -or
        $BinaryHash -notmatch '^[0-9a-fA-F]{64}$' -or
        $ManifestHash -notmatch '^[0-9a-fA-F]{64}$' -or
        $ConfigHash -notmatch '^[0-9a-fA-F]{64}$' -or
        ([string]$SourceFiles['cn-mon.exe']).ToLowerInvariant() -cne $BinaryHash.ToLowerInvariant()) {
        throw 'test fixture source or changed file hashes are invalid'
    }

    $files = [ordered]@{}
    foreach ($path in $SourceFiles.Keys) {
        $hash = [string]$SourceFiles[$path]
        if ($hash -notmatch '^[0-9a-fA-F]{64}$') {
            throw 'test fixture source payload contains an invalid hash'
        }
        $files[[string]$path] = $hash.ToLowerInvariant()
    }
    $files['AppxManifest.xml'] = $ManifestHash.ToLowerInvariant()
    $files['.cn'] = $ConfigHash.ToLowerInvariant()
    return $files
}

function Get-CnMonMsixNativeSelector {
    param([string]$Pattern)
    if ($Pattern.StartsWith('Test', [StringComparison]::Ordinal)) {
        return "^$Pattern`$"
    }
    return "^Test($Pattern)`$"
}

function Merge-CnMonMsixResultGates {
    param([System.Collections.IDictionary]$HostedGates, [System.Collections.IDictionary]$CertifyUpdates)
    $merged = [ordered]@{}
    foreach ($key in $HostedGates.Keys) { $merged[$key] = [string]$HostedGates[$key] }
    $statusRank = @{ pass = 0; incomplete = 1; fail = 2 }
    $sharedGates = @('identity', 'package_hash', 'signature', 'cleanup')
    foreach ($key in $CertifyUpdates.Keys) {
        $update = [string]$CertifyUpdates[$key]
        if ($key -ceq 'wack') {
            $merged[$key] = $update
        }
        elseif ($key -in $sharedGates -and $statusRank[$update] -gt $statusRank[[string]$merged[$key]]) {
            $merged[$key] = $update
        }
    }
    return $merged
}

function Get-CnMonMsixSourceRevision {
    $output = @(& git -C $repositoryRoot rev-parse --verify HEAD 2>&1)
    if ($LASTEXITCODE -ne 0 -or $output.Count -ne 1) { throw 'checked-out source revision could not be verified' }
    $revision = ([string]$output[0]).Trim()
    if ($revision -cnotmatch '^[0-9a-f]{40}$') { throw 'checked-out source revision is invalid' }
    return $revision
}

function Get-CnMonMsixResultEvidence {
    param([string]$PackagePath, [string]$ReceiptPath, [System.Collections.IDictionary]$Receipt, [string]$SourceRevision)
    if (-not (Test-Path -LiteralPath $PackagePath -PathType Leaf) -or -not (Test-Path -LiteralPath $ReceiptPath -PathType Leaf) -or
        $SourceRevision -cnotmatch '^[0-9a-f]{40}$' -or [string]$Receipt.purpose -cne 'candidate' -or
        [string]$Receipt.packaging_revision -cne $SourceRevision) {
        throw 'signed result evidence inputs are invalid'
    }
    $packageHash = (Get-FileHash -LiteralPath $PackagePath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($packageHash -cne ([string]$Receipt.package_sha256).ToLowerInvariant() -or
        $null -eq $Receipt.signing -or [string]$Receipt.signing.status -cne 'test' -or
        ([string]$Receipt.signing.signed_package_sha256).ToLowerInvariant() -cne $packageHash) {
        throw 'signed result evidence does not match package bytes'
    }
    return [ordered]@{
        signed_package_sha256 = $packageHash
        signed_receipt_sha256 = (Get-FileHash -LiteralPath $ReceiptPath -Algorithm SHA256).Hash.ToLowerInvariant()
        packaging_revision = $SourceRevision
    }
}

function ConvertTo-CnMonMsixNumericToolVersion {
    param([object]$VersionInfo)
    if ($null -eq $VersionInfo) { return $null }
    try {
        $parts = @($VersionInfo.FileMajorPart, $VersionInfo.FileMinorPart, $VersionInfo.FileBuildPart, $VersionInfo.FilePrivatePart)
        if ($parts.Count -ne 4 -or $parts[0] -le 0) { return $null }
        foreach ($part in $parts) {
            if ($part -isnot [int] -or $part -lt 0 -or $part -gt 99999) { return $null }
        }
        return ($parts -join '.')
    }
    catch { return $null }
}

function Get-CnMonMsixKnownWackEvaluation {
    param([string]$Path, [object]$ExitCode, [string]$ExpectedName, [string]$ExpectedVersion)

    $evaluation = [ordered]@{ status = 'incomplete'; schema_known = $false }
    $exitKnown = ($null -eq $ExitCode) -or ($ExitCode -is [int]) -or ($ExitCode -is [long])
    $exitFailed = $exitKnown -and $null -ne $ExitCode -and [long]$ExitCode -ne 0
    $exitAvailable = $null -ne $ExitCode
    if (-not $exitKnown) { return [pscustomobject]$evaluation }
    if ([string]::IsNullOrWhiteSpace($Path)) {
        if ($exitFailed) { $evaluation.status = 'fail' }
        return [pscustomobject]$evaluation
    }
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        if ($exitFailed) { $evaluation.status = 'fail' }
        return [pscustomobject]$evaluation
    }
    try {
        if ((Get-Item -LiteralPath $Path -ErrorAction Stop).Length -gt 1048576) {
            if ($exitFailed) { $evaluation.status = 'fail' }
            return [pscustomobject]$evaluation
        }
        $settings = [Xml.XmlReaderSettings]::new()
        $settings.DtdProcessing = [Xml.DtdProcessing]::Prohibit
        $settings.XmlResolver = $null
        $settings.MaxCharactersInDocument = 1048576
        $settings.MaxCharactersFromEntities = 0
        $settings.IgnoreComments = $false
        $settings.IgnoreProcessingInstructions = $false
        $stream = [IO.File]::OpenRead($Path)
        try {
            $reader = [Xml.XmlReader]::Create($stream, $settings)
            try {
                $document = [Xml.XmlDocument]::new()
                $document.PreserveWhitespace = $true
                $document.XmlResolver = $null
                $document.Load($reader)
            }
            finally { $reader.Dispose() }
        }
        finally { $stream.Dispose() }
    }
    catch {
        if ($exitFailed) { $evaluation.status = 'fail' }
        return [pscustomobject]$evaluation
    }

    $report = $document.DocumentElement
    if ($null -eq $report -or $report.LocalName -cne 'REPORT' -or $report.NamespaceURI -cne '') {
        if ($exitFailed) { $evaluation.status = 'fail' }
        return [pscustomobject]$evaluation
    }

    $explicitFailure = $exitFailed
    $shapeKnown = $true
    $candidateName = $report.GetAttribute('APP_NAME')
    $candidateVersion = $report.GetAttribute('APP_VERSION')
    if ([string]::IsNullOrWhiteSpace($candidateName) -or [string]::IsNullOrWhiteSpace($candidateVersion) -or
        [string]::IsNullOrWhiteSpace($ExpectedName) -or [string]::IsNullOrWhiteSpace($ExpectedVersion)) {
        $shapeKnown = $false
    }
    elseif ($candidateName -cne $ExpectedName -or $candidateVersion -cne $ExpectedVersion) {
        $explicitFailure = $true
    }

    $overall = $report.GetAttribute('OVERALL_RESULT').Trim().ToUpperInvariant()
    $overallKind = switch ($overall) {
        { $_ -in @('PASS', 'PASSED') } { 'pass'; break }
        { $_ -in @('FAIL', 'FAILED', 'WARNING', 'WARN', 'PASS WITH WARNINGS', 'PASS_WITH_WARNINGS', 'PASSWITHWARNINGS', 'PASSED WITH WARNINGS', 'PASSED_WITH_WARNINGS', 'PASSEDWITHWARNINGS') } { 'fail'; break }
        'NOT_APPLICABLE' { 'not_applicable'; break }
        default { 'unknown' }
    }
    if ($overallKind -eq 'fail') { $explicitFailure = $true }
    if ($overallKind -eq 'unknown') { $shapeKnown = $false }

    $partialRun = $report.GetAttribute('PARTIAL_RUN').Trim().ToUpperInvariant()
    if ($partialRun -notin @('TRUE', 'FALSE')) { $shapeKnown = $false }
    $partial = $partialRun -ceq 'TRUE'
    if ($report.GetAttribute('VERSION') -cne '10.0.26100.8249') { $shapeKnown = $false }

    $expectedTupleRows = @(
        '3:2:TRUE',
        '6:11:TRUE',
        '12:77:TRUE', '12:31:FALSE', '12:87:TRUE', '12:86:TRUE', '12:91:FALSE', '12:90:FALSE',
        '14:70:FALSE', '14:62:FALSE',
        '19:45:TRUE', '19:61:FALSE',
        '20:46:TRUE',
        '23:52:FALSE',
        '24:56:FALSE', '24:57:FALSE', '24:55:FALSE', '24:54:FALSE', '24:58:TRUE', '24:53:FALSE',
        '25:89:TRUE', '25:88:TRUE', '25:63:TRUE',
        '26:92:FALSE'
    )
    $expectedTuples = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    foreach ($row in $expectedTupleRows) { $null = $expectedTuples.Add($row) }
    $actualTuples = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    $requirementNumbers = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    $testIndices = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    $allTestResults = [Collections.Generic.List[Xml.XmlElement]]::new()

    $requirementsContainers = @($report.ChildNodes | Where-Object { $_ -is [Xml.XmlElement] -and $_.LocalName -ceq 'REQUIREMENTS' })
    if ($requirementsContainers.Count -ne 1 -or $requirementsContainers[0].NamespaceURI -cne '') {
        $shapeKnown = $false
    }
    else {
        $requirements = @($requirementsContainers[0].ChildNodes | Where-Object { $_ -is [Xml.XmlElement] })
        if ($requirements.Count -ne 10 -or @($requirements | Where-Object { $_.LocalName -cne 'REQUIREMENT' -or $_.NamespaceURI -cne '' }).Count -gt 0) {
            $shapeKnown = $false
        }
        foreach ($requirement in $requirements) {
            $numberText = $requirement.GetAttribute('NUMBER')
            $numberValue = 0
            if ($numberText -cnotmatch '^[0-9]{1,4}$' -or -not [int]::TryParse($numberText, [Globalization.NumberStyles]::None, [Globalization.CultureInfo]::InvariantCulture, [ref]$numberValue) -or
                $numberValue -le 0 -or -not $requirementNumbers.Add([string]$numberValue)) {
                $shapeKnown = $false
            }
            $tests = @($requirement.ChildNodes | Where-Object { $_ -is [Xml.XmlElement] })
            if (@($tests | Where-Object { $_.LocalName -cne 'TEST' -or $_.NamespaceURI -cne '' }).Count -gt 0) { $shapeKnown = $false }
            foreach ($test in $tests) {
                $allTestResults.Add($test)
                $indexText = $test.GetAttribute('INDEX')
                $indexValue = 0
                $optional = $test.GetAttribute('OPTIONAL').Trim().ToUpperInvariant()
                if ($indexText -cnotmatch '^[0-9]{1,4}$' -or -not [int]::TryParse($indexText, [Globalization.NumberStyles]::None, [Globalization.CultureInfo]::InvariantCulture, [ref]$indexValue) -or
                    $indexValue -le 0 -or -not $testIndices.Add([string]$indexValue) -or $optional -notin @('TRUE', 'FALSE')) {
                    $shapeKnown = $false
                }
                $tuple = "$numberValue`:$indexValue`:$optional"
                if (-not $actualTuples.Add($tuple)) { $shapeKnown = $false }

                $resultElements = @($test.ChildNodes | Where-Object { $_ -is [Xml.XmlElement] -and $_.LocalName -ceq 'RESULT' })
                foreach ($observedResult in $resultElements) {
                    if ($observedResult.InnerText.Trim().ToUpperInvariant() -in @('FAIL', 'FAILED', 'WARNING', 'WARN')) {
                        $explicitFailure = $true
                    }
                }
                if ($resultElements.Count -ne 1 -or ($resultElements.Count -eq 1 -and $resultElements[0].NamespaceURI -cne '')) {
                    $shapeKnown = $false
                    continue
                }
                $resultElement = $resultElements[0]
                $resultText = [Text.StringBuilder]::new()
                $textOnly = $true
                foreach ($child in $resultElement.ChildNodes) {
                    if ($child.NodeType -in @([Xml.XmlNodeType]::Text, [Xml.XmlNodeType]::CDATA, [Xml.XmlNodeType]::Whitespace, [Xml.XmlNodeType]::SignificantWhitespace)) {
                        $null = $resultText.Append($child.Value)
                    }
                    else { $textOnly = $false }
                }
                $statusText = $resultText.ToString().Trim().ToUpperInvariant()
                if (-not $textOnly -or [string]::IsNullOrWhiteSpace($statusText)) {
                    $shapeKnown = $false
                    continue
                }
                switch ($statusText) {
                    { $_ -in @('PASS', 'PASSED') } { break }
                    { $_ -in @('FAIL', 'FAILED', 'WARNING', 'WARN') } { $explicitFailure = $true; break }
                    'NOT_APPLICABLE' { break }
                    default { $shapeKnown = $false }
                }
            }
        }
    }

    if ($allTestResults.Count -ne 24 -or $actualTuples.Count -ne $expectedTuples.Count -or
        @($expectedTuples | Where-Object { -not $actualTuples.Contains($_) }).Count -gt 0 -or
        @($report.SelectNodes(".//*[local-name()='REQUIREMENT']")).Count -ne 10 -or
        @($report.SelectNodes(".//*[local-name()='TEST']")).Count -ne 24 -or
        @($report.SelectNodes(".//*[local-name()='RESULT']")).Count -ne 24) {
        $shapeKnown = $false
    }

    $evaluation.schema_known = [bool]$shapeKnown
    if ($explicitFailure) { $evaluation.status = 'fail' }
    elseif (-not $exitAvailable -or -not $shapeKnown -or $partial -or $overallKind -ne 'pass') { $evaluation.status = 'incomplete' }
    else {
        $hasNonPassResult = $false
        foreach ($test in $allTestResults) {
            $results = @($test.ChildNodes | Where-Object { $_ -is [Xml.XmlElement] -and $_.LocalName -ceq 'RESULT' })
            if ($results.Count -ne 1 -or $results[0].InnerText.Trim().ToUpperInvariant() -notin @('PASS', 'PASSED')) {
                $hasNonPassResult = $true
                break
            }
        }
        if (-not $hasNonPassResult) { $evaluation.status = 'pass' }
    }
    return [pscustomobject]$evaluation
}

function Get-CnMonMsixWackStructureDiagnostic {
    param(
        [string]$ReportPath,
        [object]$ExitCode,
        [string]$ToolVersion,
        [object]$Identity,
        [string]$ExpectedVersion,
        [ValidateSet('reset', 'test', 'report_missing')][string]$Phase = 'test',
        [ValidateSet('pull_request', 'workflow_dispatch')][string]$Mode = 'pull_request'
    )
    if ($Mode -cne 'pull_request' -or -not (Test-CnMonMsixSyntheticPrDiagnostic -Mode 'Hosted' -Identity $Identity)) {
        return $null
    }
    $safeExitCode = $null
    if ($null -ne $ExitCode) {
        if (($ExitCode -isnot [int]) -and ($ExitCode -isnot [long])) {
            throw 'WACK diagnostic exit code must be an integer or null'
        }
        if ([long]$ExitCode -lt [int]::MinValue -or [long]$ExitCode -gt [int]::MaxValue) {
            throw 'WACK diagnostic exit code is outside the supported range'
        }
        $safeExitCode = [int]$ExitCode
    }

    $maxBytes = 1048576
    $maxDepth = 32
    $maxElements = 256
    $maxAttributes = 512
    $maxStatuses = 64
    $suppressedNameTokens = @(
        [string]$Identity.Name, [string]$Identity.Publisher, [string]$Identity.PublisherDisplayName,
        [string]$Identity.ExpectedPFN, [string]$env:USERNAME, [string]$env:COMPUTERNAME,
        [string][Environment]::UserName, [string][Environment]::MachineName
    ) | Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
    $present = Test-Path -LiteralPath $ReportPath -PathType Leaf
    $length = if ($present) { (Get-Item -LiteralPath $ReportPath).Length } else { 0 }
    $truncated = $length -gt $maxBytes
    $reportHash = if ($present) { (Get-FileHash -LiteralPath $ReportPath -Algorithm SHA256).Hash.ToLowerInvariant() } else { $null }
    $xmlParsed = $false
    $identityMatches = $false
    $versionMatches = $false
    $reportOverallResult = 'missing'
    $reportPartialRun = 'missing'
    $reportVersion = $null
    $testResults = [Collections.Generic.List[object]]::new()
    $testOrdinal = 0
    $currentRequirementNumber = $null
    $currentRequirementTitle = $null
    $currentTest = $null
    $currentDirectResultText = $null
    $wackCategories = @{
        'DPIAwarenessValidation' = 'dpi_awareness'
        'High DPI test' = 'dpi_awareness'
        'Digitally signed file test' = 'digital_signature'
        'Debug configuration test' = 'debug_configuration'
        'Blocked executables' = 'blocked_executables'
        'Special use capabilities' = 'special_use_capabilities'
        'App Capabilities test (special use capabilities)' = 'special_use_capabilities'
        'Compatibility and resiliency test' = 'compatibility'
        'Compatibility & resiliency' = 'compatibility'
    }
    $hierarchy = [Collections.Generic.List[object]]::new()
    $attributeNames = [Collections.Generic.List[string]]::new()
    $statusObservations = [Collections.Generic.List[object]]::new()
    $elementCount = 0
    $attributeCount = 0
    $maximumDepth = 0
    $stack = [Collections.Generic.List[string]]::new()
    if ($present -and -not $truncated) {
        $reader = $null
        $stream = $null
        try {
            $settings = [Xml.XmlReaderSettings]::new()
            $settings.DtdProcessing = [Xml.DtdProcessing]::Prohibit
            $settings.XmlResolver = $null
            $settings.MaxCharactersInDocument = $maxBytes
            $settings.MaxCharactersFromEntities = 0
            $settings.IgnoreComments = $true
            $settings.IgnoreProcessingInstructions = $true
            $stream = [IO.File]::OpenRead($ReportPath)
            $reader = [Xml.XmlReader]::Create($stream, $settings)
            while ($reader.Read()) {
                if ($reader.NodeType -in @([Xml.XmlNodeType]::Text, [Xml.XmlNodeType]::CDATA) -and $stack.Count -gt 0 -and $stack[$stack.Count - 1] -match '(?i)^(RESULT|STATUS)$') {
                    $statusText = $reader.Value.Trim().ToUpperInvariant()
                    if ($statusText) {
                        $token = switch ($statusText) {
                            { $_ -in @('PASS', 'PASSED') } { 'pass'; break }
                            { $_ -in @('FAIL', 'FAILED') } { 'fail'; break }
                            { $_ -in @('WARNING', 'WARN') } { 'warning'; break }
                            'NOT_APPLICABLE' { 'not_applicable'; break }
                            default { 'other' }
                        }
                        if ($statusObservations.Count -lt $maxStatuses) {
                            $statusObservations.Add([ordered]@{ source = 'element'; status = $token; depth = $stack.Count })
                        } else { $truncated = $true }
                        if ($null -ne $currentTest -and $null -ne $currentDirectResultText -and $stack.Count -eq 5 -and
                            ($stack -join '/') -ceq 'REPORT/REQUIREMENTS/REQUIREMENT/TEST/RESULT') {
                            if ($currentDirectResultText.Length + $reader.Value.Length -gt 64) { $truncated = $true; break }
                            $null = $currentDirectResultText.Append($reader.Value)
                        }
                    }
                    continue
                }
                if ($reader.NodeType -eq [Xml.XmlNodeType]::EndElement) {
                    if ($reader.LocalName -ceq 'RESULT' -and $reader.Depth -eq 4 -and $null -ne $currentTest -and
                        $null -ne $currentDirectResultText -and $stack.Count -eq 5 -and
                        ($stack -join '/') -ceq 'REPORT/REQUIREMENTS/REQUIREMENT/TEST/RESULT') {
                        if ($currentTest.direct_results.Count -ge 8) { $truncated = $true; break }
                        $statusText = $currentDirectResultText.ToString().Trim().ToUpperInvariant()
                        $directToken = switch ($statusText) {
                            { $_ -in @('PASS', 'PASSED') } { 'pass'; break }
                            { $_ -in @('FAIL', 'FAILED') } { 'fail'; break }
                            { $_ -in @('WARNING', 'WARN') } { 'warning'; break }
                            'NOT_APPLICABLE' { 'not_applicable'; break }
                            default { 'other' }
                        }
                        $currentTest.direct_results.Add($directToken)
                        $currentDirectResultText = $null
                    }
                    if ($reader.LocalName -ceq 'TEST' -and $reader.Depth -eq 3 -and $null -ne $currentTest -and
                        $stack.Count -eq 4 -and ($stack -join '/') -ceq 'REPORT/REQUIREMENTS/REQUIREMENT/TEST') {
                        $testResults.Add([ordered]@{
                            ordinal = $currentTest.ordinal
                            requirement_number = $currentTest.requirement_number
                            test_index = $currentTest.test_index
                            optional = $currentTest.optional
                            category = $currentTest.category
                            direct_result_count = $currentTest.direct_results.Count
                            direct_results = @($currentTest.direct_results.ToArray())
                            direct_message_count = $currentTest.direct_message_count
                        })
                        $currentTest = $null
                    }
                    if ($reader.LocalName -ceq 'REQUIREMENT' -and $reader.Depth -eq 2 -and
                        $stack.Count -eq 3 -and ($stack -join '/') -ceq 'REPORT/REQUIREMENTS/REQUIREMENT') {
                        $currentRequirementNumber = $null
                        $currentRequirementTitle = $null
                    }
                    while ($stack.Count -gt $reader.Depth) { $stack.RemoveAt($stack.Count - 1) }
                    continue
                }
                if ($reader.NodeType -ne [Xml.XmlNodeType]::Element) { continue }
                $depth = [int]$reader.Depth
                if ($depth -ge $maxDepth -or $elementCount -ge $maxElements) { $truncated = $true; break }
                while ($stack.Count -gt $depth) { $stack.RemoveAt($stack.Count - 1) }
                $localName = [string]$reader.LocalName
                $stack.Add($localName)
                $elementCount++
                $maximumDepth = [Math]::Max($maximumDepth, $depth + 1)
                if ($hierarchy.Count -lt $maxElements) {
                    $safePath = [Collections.Generic.List[string]]::new()
                    foreach ($segment in $stack) {
                        $safeName = $segment -cmatch '^[A-Za-z_][A-Za-z0-9_.-]{0,63}$'
                        foreach ($tokenValue in $suppressedNameTokens) {
                            if ($segment.IndexOf([string]$tokenValue, [StringComparison]::OrdinalIgnoreCase) -ge 0) { $safeName = $false; break }
                        }
                        if ($safeName) {
                            $safePath.Add($segment)
                        } else { $safePath.Add('suppressed') }
                    }
                    $hierarchy.Add([ordered]@{ depth = $depth + 1; path = @($safePath.ToArray()) })
                }
                if ($localName -ceq 'REPORT' -and $depth -eq 0 -and ($stack -join '/') -ceq 'REPORT') {
                    $identityMatches = $reader.GetAttribute('APP_NAME') -ceq [string]$Identity.Name
                    $versionMatches = $reader.GetAttribute('APP_VERSION') -ceq $ExpectedVersion
                    $overallValue = $reader.GetAttribute('OVERALL_RESULT')
                    if ([string]::IsNullOrWhiteSpace($overallValue)) { $reportOverallResult = 'missing' }
                    else {
                        $reportOverallResult = switch ($overallValue.Trim().ToUpperInvariant()) {
                            { $_ -in @('PASS', 'PASSED') } { 'pass'; break }
                            { $_ -in @('FAIL', 'FAILED') } { 'fail'; break }
                            { $_ -in @('WARNING', 'WARN') } { 'warning'; break }
                            { $_ -in @('PASS WITH WARNINGS', 'PASS_WITH_WARNINGS', 'PASSWITHWARNINGS', 'PASSED WITH WARNINGS', 'PASSED_WITH_WARNINGS', 'PASSEDWITHWARNINGS') } { 'pass_with_warnings'; break }
                            'NOT_APPLICABLE' { 'not_applicable'; break }
                            default { 'other' }
                        }
                    }
                    $partialValue = $reader.GetAttribute('PARTIAL_RUN')
                    if ($null -eq $partialValue -or [string]::IsNullOrWhiteSpace($partialValue)) { $reportPartialRun = 'missing' }
                    else {
                        $reportPartialRun = switch ($partialValue.Trim().ToUpperInvariant()) {
                            'TRUE' { 'true'; break }
                            'FALSE' { 'false'; break }
                            default { 'other' }
                        }
                    }
                    $versionValue = $reader.GetAttribute('VERSION')
                    if ($versionValue -cmatch '^[0-9]{1,5}(?:\.[0-9]{1,5}){1,3}$' -and $versionValue.Length -le 23) {
                        $reportVersion = $versionValue
                    }
                }
                if ($localName -ceq 'REQUIREMENT' -and $stack.Count -eq 3 -and ($stack -join '/') -ceq 'REPORT/REQUIREMENTS/REQUIREMENT') {
                    $numberValue = $reader.GetAttribute('NUMBER')
                    if ($numberValue -cmatch '^[0-9]{1,4}(?:\.[0-9]{1,4}){0,3}$') { $currentRequirementNumber = $numberValue }
                    else { $currentRequirementNumber = $null }
                    $currentRequirementTitle = $reader.GetAttribute('TITLE')
                }
                if ($localName -ceq 'TEST' -and $stack.Count -eq 4 -and ($stack -join '/') -ceq 'REPORT/REQUIREMENTS/REQUIREMENT/TEST') {
                    if ($testResults.Count -ge 64) { $truncated = $true; break }
                    $testOrdinal++
                    $indexValue = $reader.GetAttribute('INDEX')
                    $testIndex = $null
                    if ($indexValue -cmatch '^[0-9]{1,4}$') {
                        $parsedIndex = 0
                        if ([int]::TryParse($indexValue, [ref]$parsedIndex) -and $parsedIndex -le 9999) { $testIndex = $parsedIndex }
                    }
                    $optionalValue = $reader.GetAttribute('OPTIONAL')
                    $optional = if ($null -eq $optionalValue -or [string]::IsNullOrWhiteSpace($optionalValue)) { 'missing' }
                        elseif ($optionalValue.Trim().ToUpperInvariant() -ceq 'TRUE') { 'true' }
                        elseif ($optionalValue.Trim().ToUpperInvariant() -ceq 'FALSE') { 'false' }
                        else { 'other' }
                    $category = 'unknown'
                    $testName = $reader.GetAttribute('NAME')
                    if ($null -ne $testName) {
                        foreach ($knownName in $wackCategories.Keys) {
                            if ($testName.Trim() -ceq [string]$knownName) {
                                $category = $wackCategories[$knownName]
                                break
                            }
                        }
                    }
                    if ($category -ceq 'unknown' -and $null -ne $currentRequirementTitle) {
                        foreach ($knownTitle in $wackCategories.Keys) {
                            if ($currentRequirementTitle.Trim() -ceq [string]$knownTitle) {
                                $category = $wackCategories[$knownTitle]
                                break
                            }
                        }
                    }
                    $currentTest = [ordered]@{
                        ordinal = $testOrdinal
                        requirement_number = $currentRequirementNumber
                        test_index = $testIndex
                        optional = $optional
                        category = $category
                        direct_results = [Collections.Generic.List[string]]::new()
                        direct_message_count = 0
                    }
                    if ($reader.IsEmptyElement) {
                        if ($testResults.Count -ge 64) { $truncated = $true; break }
                        $testResults.Add([ordered]@{
                            ordinal = $currentTest.ordinal
                            requirement_number = $currentTest.requirement_number
                            test_index = $currentTest.test_index
                            optional = $currentTest.optional
                            category = $currentTest.category
                            direct_result_count = 0
                            direct_results = @()
                            direct_message_count = 0
                        })
                        $currentTest = $null
                    }
                }
                if ($null -ne $currentTest -and $localName -ceq 'RESULT' -and $reader.Depth -eq 4 -and $stack.Count -eq 5 -and
                    ($stack -join '/') -ceq 'REPORT/REQUIREMENTS/REQUIREMENT/TEST/RESULT') {
                    if ($reader.IsEmptyElement) {
                        if ($currentTest.direct_results.Count -ge 8) { $truncated = $true; break }
                        $currentTest.direct_results.Add('other')
                    } else {
                        $currentDirectResultText = [Text.StringBuilder]::new()
                    }
                }
                if ($null -ne $currentTest -and $localName -ceq 'MESSAGE' -and $stack.Count -eq 6 -and
                    ($stack -join '/') -ceq 'REPORT/REQUIREMENTS/REQUIREMENT/TEST/MESSAGES/MESSAGE') {
                    $currentTest.direct_message_count++
                }
                if ($reader.HasAttributes) {
                    while ($reader.MoveToNextAttribute()) {
                        $attributeCount++
                        if ($attributeNames.Count -lt $maxAttributes) {
                            $name = [string]$reader.LocalName
                            $safeName = $name -cmatch '^[A-Za-z_][A-Za-z0-9_.-]{0,63}$'
                            foreach ($tokenValue in $suppressedNameTokens) {
                                if ($name.IndexOf([string]$tokenValue, [StringComparison]::OrdinalIgnoreCase) -ge 0) { $safeName = $false; break }
                            }
                            if ($safeName) {
                                $attributeNames.Add($name)
                            } else { $attributeNames.Add('suppressed') }
                        } else { $truncated = $true; break }
                        if ($reader.LocalName -match '(?i)^(result|status)$') {
                            $token = switch -Regex ($reader.Value.Trim().ToUpperInvariant()) {
                                '^(PASS|PASSED)$' { 'pass'; break }
                                '^(FAIL|FAILED)$' { 'fail'; break }
                                '^(WARNING|WARN)$' { 'warning'; break }
                                '^NOT_APPLICABLE$' { 'not_applicable'; break }
                                default { 'other' }
                            }
                            if ($statusObservations.Count -lt $maxStatuses) {
                                $statusObservations.Add([ordered]@{ source = 'attribute'; status = $token; depth = $depth + 1 })
                            } else { $truncated = $true }
                        }
                    }
                    $reader.MoveToElement() | Out-Null
                    if ($truncated) { break }
                }
                if ($reader.IsEmptyElement -and $stack.Count -gt 0) {
                    $stack.RemoveAt($stack.Count - 1)
                }
            }
            if (-not $truncated) { $xmlParsed = $true }
        }
        catch { $xmlParsed = $false }
        finally {
            if ($null -ne $reader) { $reader.Dispose() }
            if ($null -ne $stream) { $stream.Dispose() }
        }
    }
    $safeVersion = if ($ToolVersion -cmatch '^[0-9]{1,5}(?:\.[0-9]{1,5}){1,3}$' -and $ToolVersion.Length -le 23) { $ToolVersion } else { $null }
    $knownEvaluation = Get-CnMonMsixKnownWackEvaluation -Path $ReportPath -ExitCode $safeExitCode -ExpectedName ([string]$Identity.Name) -ExpectedVersion $ExpectedVersion
    return [ordered]@{
        schema_version = 1
        phase = $Phase
        tool_version = $safeVersion
        exit_code = $safeExitCode
        report_present = [bool]$present
        xml_parse_success = [bool]$xmlParsed
        unknown_schema = [bool](-not $knownEvaluation.schema_known)
        truncated = [bool]$truncated
        report_sha256 = $reportHash
        identity_value_matches = [bool]$identityMatches
        version_value_matches = [bool]$versionMatches
        report_overall_result = $reportOverallResult
        report_partial_run = $reportPartialRun
        report_version = $reportVersion
        test_results = @($testResults.ToArray())
        element_count = $elementCount
        attribute_count = $attributeCount
        maximum_depth = $maximumDepth
        element_hierarchy = @($hierarchy.ToArray())
        attribute_local_names = @($attributeNames.ToArray())
        status_observations = @($statusObservations.ToArray())
    }
}

function Save-CnMonMsixWackStructureDiagnostic {
    param(
        [string]$ReportPath,
        [object]$ExitCode,
        [string]$ToolVersion,
        [object]$Identity,
        [string]$ExpectedVersion,
        [ValidateSet('reset', 'test', 'report_missing')][string]$Phase
    )
    $diagnostic = Get-CnMonMsixWackStructureDiagnostic -ReportPath $ReportPath -ExitCode $ExitCode -ToolVersion $ToolVersion -Identity $Identity -ExpectedVersion $ExpectedVersion -Phase $Phase
    if ($null -eq $diagnostic) { return }
    $diagnosticPath = Join-Path $ArtifactDirectory 'msix-wack-structure.json'
    [IO.File]::WriteAllText($diagnosticPath, (ConvertTo-Json -InputObject $diagnostic -Depth 12) + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
}

function Assert-CnMonMsixResultEvidence {
    param([System.Collections.IDictionary]$Evidence, [System.Collections.IDictionary]$ExpectedEvidence)
    $expectedKeys = @('packaging_revision', 'signed_package_sha256', 'signed_receipt_sha256')
    if ($null -eq $Evidence -or $null -eq $ExpectedEvidence -or
        (@($Evidence.Keys | Sort-Object) -join "`n") -cne ($expectedKeys -join "`n") -or
        (@($ExpectedEvidence.Keys | Sort-Object) -join "`n") -cne ($expectedKeys -join "`n") -or
        $Evidence.signed_package_sha256 -isnot [string] -or
        $Evidence.signed_receipt_sha256 -isnot [string] -or
        $Evidence.packaging_revision -isnot [string] -or
        $ExpectedEvidence.signed_package_sha256 -isnot [string] -or
        $ExpectedEvidence.signed_receipt_sha256 -isnot [string] -or
        $ExpectedEvidence.packaging_revision -isnot [string] -or
        $Evidence.signed_package_sha256 -cnotmatch '^[0-9a-f]{64}$' -or
        $Evidence.signed_receipt_sha256 -cnotmatch '^[0-9a-f]{64}$' -or
        $Evidence.packaging_revision -cnotmatch '^[0-9a-f]{40}$' -or
        $Evidence.signed_package_sha256 -cne $ExpectedEvidence.signed_package_sha256 -or
        $Evidence.signed_receipt_sha256 -cne $ExpectedEvidence.signed_receipt_sha256 -or
        $Evidence.packaging_revision -cne $ExpectedEvidence.packaging_revision) {
        throw 'native result evidence does not match signed package, receipt, and source revision'
    }
}

function Assert-CnMonMsixUserResultBinding {
    param([System.Collections.IDictionary]$Result, [System.Collections.IDictionary]$ExpectedEvidence)
    $expectedProperties = @('evidence', 'gates', 'mode', 'overall', 'schema_version')
    if ($null -eq $Result -or
        (@($Result.Keys | Sort-Object -CaseSensitive) -join "`n") -cne ($expectedProperties -join "`n") -or
        $Result.schema_version -isnot [long] -or $Result.schema_version -ne 2 -or
        $Result.mode -isnot [string] -or $Result.mode -cne 'User' -or
        $Result.overall -isnot [string] -or $Result.overall -cnotin @('pass', 'fail', 'incomplete') -or
        $null -eq $Result.gates -or -not ($Result.gates -is [System.Collections.IDictionary]) -or
        (@($Result.gates.Keys | Sort-Object -CaseSensitive) -join "`n") -cne (@($qualificationGates | Sort-Object -CaseSensitive) -join "`n")) {
        throw 'ordinary-user result has an invalid schema'
    }
    foreach ($gate in $qualificationGates) {
        $status = $Result.gates[$gate]
        if ($status -isnot [string] -or $status -cnotin @('pass', 'fail', 'incomplete')) {
            throw 'ordinary-user result has an invalid gate status'
        }
    }
    if (-not ($Result.evidence -is [System.Collections.IDictionary])) {
        throw 'ordinary-user result is missing signed evidence'
    }
    Assert-CnMonMsixResultEvidence -Evidence $Result.evidence -ExpectedEvidence $ExpectedEvidence
}

function Read-CnMonMsixHostedResultSnapshot {
    param([string]$ResultsPath, [string]$PackagePath, [string]$ReceiptPath, [string]$SourceRevision)
    $bytes = [IO.File]::ReadAllBytes($ResultsPath)
    $json = [Text.UTF8Encoding]::new($false, $true).GetString($bytes)
    $hosted = ConvertFrom-Json -InputObject $json -AsHashtable -ErrorAction Stop
    $expectedProperties = @('evidence', 'gates', 'mode', 'overall', 'schema_version')
    if ($null -eq $hosted -or -not ($hosted -is [System.Collections.IDictionary]) -or
        (@($hosted.Keys | Sort-Object -CaseSensitive) -join "`n") -cne ($expectedProperties -join "`n") -or
        $hosted.schema_version -isnot [long] -or $hosted.schema_version -ne 2 -or
        $hosted.mode -isnot [string] -or $hosted.mode -cne 'Hosted' -or
        $hosted.overall -isnot [string] -or $hosted.overall -cnotin @('pass', 'fail', 'incomplete') -or
        $null -eq $hosted.gates -or -not ($hosted.gates -is [System.Collections.IDictionary]) -or
        (@($hosted.gates.Keys | Sort-Object -CaseSensitive) -join "`n") -cne (@($qualificationGates | Sort-Object -CaseSensitive) -join "`n")) {
        throw 'Hosted result snapshot has an invalid schema'
    }
    foreach ($gate in $qualificationGates) {
        $status = $hosted.gates[$gate]
        if ($status -isnot [string] -or $status -cnotin @('pass', 'fail', 'incomplete')) { throw 'Hosted result snapshot has an invalid gate status' }
    }
    $receipt = Get-Content -LiteralPath $ReceiptPath -Raw -ErrorAction Stop | ConvertFrom-Json -AsHashtable -ErrorAction Stop
    $expectedEvidence = Get-CnMonMsixResultEvidence -PackagePath $PackagePath -ReceiptPath $ReceiptPath -Receipt $receipt -SourceRevision $SourceRevision
    Assert-CnMonMsixResultEvidence -Evidence $hosted.evidence -ExpectedEvidence $expectedEvidence
    return [pscustomobject]@{ Results = $hosted; JsonBytes = $bytes }
}

function Get-CnMonMsixWackStatus {
    param([string]$Path, [object]$ExitCode, [string]$ExpectedName, [string]$ExpectedVersion)
    $evaluation = Get-CnMonMsixKnownWackEvaluation -Path $Path -ExitCode $ExitCode -ExpectedName $ExpectedName -ExpectedVersion $ExpectedVersion
    return [string]$evaluation.status
}

function Get-CnMonMsixTrustAction {
    param([bool]$HasTrustedCertificate, [bool]$FreshHostedRunner)
    if ($HasTrustedCertificate) { return 'reuse' }
    if ($FreshHostedRunner) { return 'import' }
    return 'incomplete'
}

function Get-CnMonMsixProfileTargets {
    param([string]$ProfilePath, [string]$LocalAppData, [string]$FixtureHistory, [string]$FixtureLog)
    if ([string]::IsNullOrWhiteSpace($ProfilePath) -or [string]::IsNullOrWhiteSpace($LocalAppData)) {
        throw 'actual user profile paths are required'
    }
    $targets = [ordered]@{
        ProfileConfig = Join-Path $ProfilePath '.cn'
        ProfileHistoryDefault = Join-Path $ProfilePath '.cn-ping-history.db'
        ProfileLogDefault = Join-Path $ProfilePath 'cn.log'
        ProfileHistory = Join-Path $ProfilePath 'msix-profile-history.db'
        ProfileLog = Join-Path $ProfilePath 'msix-profile.log'
        CliHistory = Join-Path $ProfilePath 'msix-cli-history.db'
        CliLog = Join-Path $ProfilePath 'msix-cli.log'
        FixtureHistory = $FixtureHistory
        FixtureLog = $FixtureLog
    }
    $all = @($targets.Values | Where-Object { -not [string]::IsNullOrWhiteSpace([string]$_) })
    $targets.All = $all
    return [pscustomobject]$targets
}

function Assert-CnMonMsixTargetsAbsent {
    param([string[]]$Paths)
    foreach ($path in $Paths) {
        if (Test-Path -LiteralPath $path) {
            throw 'qualification profile or fixture contains pre-existing application data'
        }
    }
}

function Add-CnMonMsixOwnedPackage {
    param([object]$Identity, [string]$Version, [string]$PackagePath, [Collections.Generic.List[object]]$Ownership)
    $name = [string]$Identity.Name
    $publisher = [string]$Identity.Publisher
    $family = [string]$Identity.ExpectedPFN
    $matchingSpecs = @($Ownership | Where-Object {
        $_.Name -ceq $name -and $_.Publisher -ceq $publisher -and
        $_.PackageFamilyName -ceq $family -and $_.Version -ceq $Version
    } | Select-Object -First 1)
    if ($matchingSpecs.Count -eq 0) {
        $spec = [pscustomobject]@{
            Name = $name
            Publisher = $publisher
            PackageFamilyName = $family
            Version = $Version
            PackageFullNames = [Collections.Generic.List[string]]::new()
        }
        $Ownership.Add($spec)
    }
    else { $spec = $matchingSpecs[0] }
    $null = Add-AppxPackage -Path $PackagePath -ErrorAction Stop
    $packages = @(Get-AppxPackage -Name $name -ErrorAction Stop | Where-Object {
        [string]$_.Publisher -ceq $publisher -and
        [string]$_.PackageFamilyName -ceq $family -and
        [string]$_.Version -ceq $Version
    })
    foreach ($package in $packages) {
        if ([string]$package.PackageFullName -and $package.PackageFullName -notin $spec.PackageFullNames) {
            $spec.PackageFullNames.Add([string]$package.PackageFullName)
        }
    }
    if ($packages.Count -eq 0) { throw 'new package registration could not be identified for cleanup ownership' }
}

function Remove-CnMonMsixOwnedPackages {
    param([Collections.Generic.List[object]]$Ownership)
    $cleanupFailed = $false
    foreach ($spec in $Ownership) {
        try {
            $packages = @(Get-AppxPackage -Name $spec.Name -ErrorAction Stop | Where-Object {
                [string]$_.Publisher -ceq $spec.Publisher -and
                [string]$_.PackageFamilyName -ceq $spec.PackageFamilyName -and
                [string]$_.Version -ceq $spec.Version -and
                ($spec.PackageFullNames.Count -eq 0 -or [string]$_.PackageFullName -in $spec.PackageFullNames)
            })
            foreach ($package in $packages) {
                $null = Remove-AppxPackage -Package $package.PackageFullName -ErrorAction Stop
            }
            $remaining = @(Get-AppxPackage -Name $spec.Name -ErrorAction Stop | Where-Object {
                [string]$_.Publisher -ceq $spec.Publisher -and
                [string]$_.PackageFamilyName -ceq $spec.PackageFamilyName -and
                [string]$_.Version -ceq $spec.Version -and
                ($spec.PackageFullNames.Count -eq 0 -or [string]$_.PackageFullName -in $spec.PackageFullNames)
            })
            if ($remaining.Count -gt 0) { throw 'owned package remains registered' }
        }
        catch { $cleanupFailed = $true }
    }
    if ($cleanupFailed) { throw 'one or more owned package registrations could not be removed' }
}

function Assert-CnMonMsixRetainedFiles {
    param([System.Collections.IDictionary]$ExpectedHashes)
    foreach ($path in $ExpectedHashes.Keys) {
        if (-not (Test-Path -LiteralPath $path -PathType Leaf) -or
            (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant() -cne [string]$ExpectedHashes[$path]) {
            throw 'profile or fixture data changed during package lifecycle operation'
        }
    }
}

function Test-CnMonMsixAdministratorsMembership {
    param([string[]]$GroupSids)
    return [bool](@($GroupSids | Where-Object { $_ -ceq 'S-1-5-32-544' }).Count -gt 0)
}

function Remove-CnMonMsixOwnedCertificate {
    param([string]$Thumbprint, [bool]$RemoveTrustedCopy, [bool]$DeletePrivateKey)
    if (-not $Thumbprint) { return }
    $cleanupErrors = [Collections.Generic.List[Exception]]::new()
    if ($RemoveTrustedCopy) {
        try { Remove-Item -LiteralPath "Cert:\LocalMachine\TrustedPeople\$Thumbprint" -Force -ErrorAction Stop }
        catch { $cleanupErrors.Add($_.Exception) }
    }
    if ($DeletePrivateKey) {
        try { Remove-Item -LiteralPath "Cert:\LocalMachine\My\$Thumbprint" -Force -DeleteKey -ErrorAction Stop }
        catch { $cleanupErrors.Add($_.Exception) }
    }
    if ($cleanupErrors.Count -gt 0) { throw 'one or more owned certificate objects could not be removed' }
}

function Get-CnMonMsixSyntheticPrDiagnosticGuard {
    param([string]$Mode, [object]$Identity)
    $guard = [ordered]@{
        mode_hosted = $Mode -ceq 'Hosted'
        mode_user = $Mode -ceq 'User'
        github_actions = $env:GITHUB_ACTIONS -ceq 'true'
        event_pull_request = $env:GITHUB_EVENT_NAME -ceq 'pull_request'
        repository_expected = $env:GITHUB_REPOSITORY -ceq 'evdanil/cn-mon'
        image_os_windows = $env:ImageOS -cmatch '^win'
        identity_name_env_matches = $env:CNMON_MSIX_IDENTITY_NAME -ceq 'Example.CnMon'
        identity_publisher_env_matches = $env:CNMON_MSIX_IDENTITY_PUBLISHER -ceq 'CN=Example, O=Example'
        publisher_display_name_env_matches = $env:CNMON_MSIX_PUBLISHER_DISPLAY_NAME -ceq 'Example Publisher'
        expected_pfn_env_matches = $env:CNMON_MSIX_EXPECTED_PFN -ceq 'Example.CnMon_518g7wxbxvqf6'
        identity_name_matches = [string]$Identity.Name -ceq 'Example.CnMon'
        identity_publisher_matches = [string]$Identity.Publisher -ceq 'CN=Example, O=Example'
        publisher_display_name_matches = [string]$Identity.PublisherDisplayName -ceq 'Example Publisher'
        expected_pfn_matches = [string]$Identity.ExpectedPFN -ceq 'Example.CnMon_518g7wxbxvqf6'
    }
    $guard.all = ($guard.mode_hosted -or $guard.mode_user) -and
        @($guard.GetEnumerator() | Where-Object { $_.Key -notin @('mode_hosted', 'mode_user') -and -not [bool]$_.Value }).Count -eq 0
    return $guard
}

function Test-CnMonMsixSyntheticPrDiagnostic {
    param([string]$Mode, [object]$Identity)
    $guard = Get-CnMonMsixSyntheticPrDiagnosticGuard -Mode $Mode -Identity $Identity
    return [bool]$guard.all
}

function Get-CnMonMsixSignatureDiagnostic {
    param(
        [string]$LogPath,
        [int]$ExitCode,
        [ValidateSet('candidate-verify', 'user-verify', 'certify-verify')][string]$Phase
    )
    $raw = if (Test-Path -LiteralPath $LogPath -PathType Leaf) { [IO.File]::ReadAllText($LogPath) } else { '' }
    $hresults = @([regex]::Matches($raw, '(?i)\b0x[0-9a-f]{8}\b') | ForEach-Object { $_.Value.ToUpperInvariant() } | Sort-Object -Unique | Select-Object -First 4)
    $reason = 'sdk_error_unclassified'
    foreach ($hresult in $hresults) {
        switch ($hresult) {
            '0X800B0109' { $reason = 'untrusted_root'; break }
            '0X800B0100' { $reason = 'no_signature'; break }
            '0X800B010A' { $reason = 'certificate_chain_failure'; break }
            '0X800B0101' { $reason = 'certificate_expired'; break }
            '0X800B0110' { $reason = 'wrong_certificate_usage'; break }
            '0X80096010' { $reason = 'signature_digest_mismatch'; break }
        }
        if ($reason -cne 'sdk_error_unclassified') { break }
    }
    return [ordered]@{
        schema_version = 1
        phase = $Phase
        tool = 'signtool'
        exit_code = $ExitCode
        hresults = $hresults
        reason = $reason
    }
}

function Save-CnMonMsixSignatureDiagnostic {
    param(
        [string]$LogPath,
        [string]$ArtifactDirectory,
        [int]$ExitCode,
        [ValidateSet('candidate-verify', 'user-verify', 'certify-verify')][string]$Phase,
        [string]$Mode,
        [object]$Identity
    )
    $privateDirectory = Join-Path $ArtifactDirectory 'private-signature-diagnostics'
    New-Item -ItemType Directory -Force -Path $privateDirectory | Out-Null
    $privateLog = Join-Path $privateDirectory "$Phase.log"
    if (Test-Path -LiteralPath $privateLog) { throw 'private signature diagnostic already exists' }
    if (Test-Path -LiteralPath $LogPath -PathType Leaf) {
        Copy-Item -LiteralPath $LogPath -Destination $privateLog -ErrorAction Stop
    }

    if (Test-CnMonMsixSyntheticPrDiagnostic -Mode $Mode -Identity $Identity) {
        $publicPath = Join-Path $ArtifactDirectory 'msix-signature-diagnostic.json'
        if (Test-Path -LiteralPath $publicPath) { throw 'public signature diagnostic already exists' }
        $diagnostic = Get-CnMonMsixSignatureDiagnostic -LogPath $LogPath -ExitCode $ExitCode -Phase $Phase
        $json = ConvertTo-Json -InputObject $diagnostic -Depth 4
        [IO.File]::WriteAllText($publicPath, $json + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
        $script:PublicDiagnosticWritten = $true
    }
}

function Save-CnMonMsixSignatureDiagnosticStatus {
    param(
        [string]$ArtifactDirectory,
        [System.Collections.IDictionary]$Guard,
        [ValidateSet('candidate-verify', 'user-trust-presence', 'user-verify', 'candidate-setup', 'candidate-post-verify', 'user-post-verify', 'other')][string]$Phase,
        [ValidateSet('none', 'signed_unpack_directory', 'signed_makeappx_unpack', 'signed_payload_validation', 'signed_package_hash', 'signed_receipt_construction', 'signed_receipt_serialization', 'signed_receipt_write')][string]$Operation = 'none',
        [ValidateSet('sdk_nonzero', 'sdk_invocation_error', 'trusted_certificate_missing', 'stage_exception')][string]$FailureKind,
        [ValidateSet('none', 'invalid_operation', 'io', 'access_denied', 'path', 'argument', 'security', 'other')][string]$ExceptionKind,
        [ValidateSet('sdk_nonzero', 'sdk_invocation_error', 'trusted_certificate_missing', 'parameter_binding', 'payload_root_missing', 'payload_expected_map_invalid', 'payload_reparse_point', 'payload_missing', 'payload_hash_mismatch', 'payload_unexpected', 'payload_catalog_missing', 'payload_catalog_invalid', 'invalid_operation', 'io', 'access_denied', 'path', 'argument', 'security', 'stage_exception')][string]$ErrorCategory = 'stage_exception',
        [ValidateSet('none', 'signtool', 'makeappx', 'certificate_store', 'other')][string]$ToolName = 'none',
        [bool]$SdkExitCaptured,
        [bool]$PrivateLogRetained,
        [bool]$TrustPresenceChecked,
        [bool]$TrustedCertificatePresent,
        [bool]$CatalogFootprintPresent,
        [ValidateSet('not_attempted', 'success', 'failure')][string]$SaveStatus,
        [ValidateSet('none', 'io', 'access_denied', 'path', 'argument', 'other')][string]$SaveErrorKind,
        [bool]$PublicDiagnosticWritten
    )
    if ($env:GITHUB_ACTIONS -cne 'true' -or $env:GITHUB_EVENT_NAME -cne 'pull_request' -or
        $env:GITHUB_REPOSITORY -cne 'evdanil/cn-mon' -or -not $Guard.all) {
        return
    }
    $statusPath = Join-Path $ArtifactDirectory 'msix-signature-diagnostic-status.json'
    if (Test-Path -LiteralPath $statusPath -PathType Leaf) { return }
    $status = [ordered]@{ schema_version = 3 }
    foreach ($key in $Guard.Keys) { $status["guard_$key"] = [bool]$Guard[$key] }
    $status.phase = $Phase
    $status.operation = $Operation
    $status.failure_kind = $FailureKind
    $status.exception_kind = $ExceptionKind
    $status.error_category = $ErrorCategory
    $status.tool_name = $ToolName
    $status.sdk_exit_captured = $SdkExitCaptured
    $status.private_log_retained = $PrivateLogRetained
    $status.trust_presence_checked = $TrustPresenceChecked
    $status.trusted_certificate_present = $TrustedCertificatePresent
    $status.catalog_footprint_present = $CatalogFootprintPresent
    $status.save_status = $SaveStatus
    $status.save_error_kind = $SaveErrorKind
    $status.public_diagnostic_written = $PublicDiagnosticWritten
    $json = ConvertTo-Json -InputObject $status -Depth 4
    [IO.File]::WriteAllText($statusPath, $json + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
}

function Save-CnMonMsixSignatureFailureEvidence {
    param(
        [string]$Mode,
        [string]$ArtifactDirectory,
        [ValidateSet('candidate-verify', 'user-trust-presence', 'user-verify', 'candidate-setup', 'candidate-post-verify', 'user-post-verify', 'other')][string]$Phase,
        [object]$Identity,
        [System.Exception]$Exception,
        [bool]$TrustPresenceChecked,
        [bool]$TrustedCertificatePresent
    )
    $guard = Get-CnMonMsixSyntheticPrDiagnosticGuard -Mode $Mode -Identity $Identity

    $exceptionKind = switch ($Exception.GetType().Name) {
        'InvalidOperationException' { 'invalid_operation'; break }
        { $_ -in @('IOException', 'FileLoadException') } { 'io'; break }
        'UnauthorizedAccessException' { 'access_denied'; break }
        { $_ -in @('PathTooLongException', 'DirectoryNotFoundException') } { 'path'; break }
        'ArgumentException' { 'argument'; break }
        { $_ -match 'Security' } { 'security'; break }
        default { 'other' }
    }
    $isSignatureVerify = $Phase -in @('candidate-verify', 'user-verify')
    $knownOperations = @('signed_unpack_directory', 'signed_makeappx_unpack', 'signed_payload_validation', 'signed_package_hash', 'signed_receipt_construction', 'signed_receipt_serialization', 'signed_receipt_write')
    $operation = if ($Phase -eq 'candidate-post-verify' -and $script:CurrentDiagnosticOperation -in $knownOperations) { $script:CurrentDiagnosticOperation } else { 'none' }
    $catalogFootprintPresent = [bool]$script:SignedCatalogFootprintPresent
    $makeAppxFailure = $Phase -eq 'candidate-post-verify' -and $operation -eq 'signed_makeappx_unpack' -and
        $script:LastSdkToolName -ceq 'makeappx' -and $null -ne $script:LastSdkToolExitCode -and $script:LastSdkToolLogPath
    $sdkExitCode = if ($isSignatureVerify) { $script:LastSignatureVerifyExitCode }
        elseif ($Phase -eq 'candidate-setup' -or $makeAppxFailure) { $script:LastSdkToolExitCode }
        else { $null }
    $sdkExitCaptured = $null -ne $sdkExitCode
    $toolName = if ($isSignatureVerify) { 'signtool' }
        elseif ($Phase -eq 'user-trust-presence') { 'certificate_store' }
        elseif ($sdkExitCaptured -and $script:LastSdkToolName -in @('signtool', 'makeappx', 'other')) { $script:LastSdkToolName }
        else { 'none' }
    $sdkLogPath = if ($isSignatureVerify) { $script:LastSignatureLogPath } else { $script:LastSdkToolLogPath }
    $failureKind = if ($sdkExitCaptured) { 'sdk_nonzero' }
        elseif ($isSignatureVerify) { 'sdk_invocation_error' }
        else { 'stage_exception' }
    if ($Phase -eq 'user-trust-presence' -and $TrustPresenceChecked -and -not $TrustedCertificatePresent) {
        $failureKind = 'trusted_certificate_missing'
    }
    $errorCategory = Get-CnMonMsixDiagnosticErrorCategory -Message $Exception.Message -ExceptionType $Exception.GetType().Name -FailureKind $failureKind
    $saveStatus = 'not_attempted'
    $saveErrorKind = 'none'
    $privateLog = $null
    $publicPath = Join-Path $ArtifactDirectory 'msix-signature-diagnostic.json'
    try {
        if ($sdkExitCaptured -and $isSignatureVerify) {
            Save-CnMonMsixSignatureDiagnostic -LogPath $script:LastSignatureLogPath -ArtifactDirectory $ArtifactDirectory -ExitCode ([int]$sdkExitCode) -Phase $Phase -Mode $Mode -Identity $Identity
            $saveStatus = 'success'
            $privateLog = Join-Path (Join-Path $ArtifactDirectory 'private-signature-diagnostics') "$Phase.log"
            if ($guard.all -and (Test-Path -LiteralPath $publicPath -PathType Leaf)) {
                $publicDiagnostic = Get-Content -LiteralPath $publicPath -Raw | ConvertFrom-Json -AsHashtable -ErrorAction Stop
                $publicDiagnostic.failure_kind = $failureKind
                $publicDiagnostic.exception_kind = $exceptionKind
                $publicDiagnostic.trust_presence_checked = $TrustPresenceChecked
                $publicDiagnostic.trusted_certificate_present = $TrustedCertificatePresent
                $publicDiagnostic.operation = $operation
                $publicDiagnostic.error_category = $errorCategory
                $publicDiagnostic.catalog_footprint_present = $catalogFootprintPresent
                [IO.File]::WriteAllText($publicPath, (ConvertTo-Json -InputObject $publicDiagnostic -Depth 4) + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
            }
        }
        elseif ($sdkExitCaptured -and $sdkLogPath -and (Test-Path -LiteralPath $sdkLogPath -PathType Leaf)) {
            $privateDirectory = Join-Path $ArtifactDirectory 'private-signature-diagnostics'
            New-Item -ItemType Directory -Force -Path $privateDirectory | Out-Null
            $privateName = if ($Phase -eq 'candidate-post-verify') { "$Phase-$toolName.log" } else { "$Phase.log" }
            $privateLog = Join-Path $privateDirectory $privateName
            Copy-Item -LiteralPath $sdkLogPath -Destination $privateLog -ErrorAction Stop
            $saveStatus = 'success'
        }
        if (-not $guard.all) { return }

        if (-not ($sdkExitCaptured -and $isSignatureVerify) -and -not (Test-Path -LiteralPath $publicPath -PathType Leaf)) {
            $reason = if ($failureKind -in @('sdk_nonzero', 'sdk_invocation_error')) { 'sdk_error_unclassified' } else { $errorCategory }
            $diagnostic = [ordered]@{
                schema_version = 1
                phase = $Phase
                operation = $operation
                tool = $toolName
                exit_code = if ($sdkExitCaptured) { [int]$sdkExitCode } else { $null }
                hresults = @()
                reason = $reason
                failure_kind = $failureKind
                exception_kind = $exceptionKind
                error_category = $errorCategory
                trust_presence_checked = $TrustPresenceChecked
                trusted_certificate_present = $TrustedCertificatePresent
                catalog_footprint_present = $catalogFootprintPresent
            }
            [IO.File]::WriteAllText($publicPath, (ConvertTo-Json -InputObject $diagnostic -Depth 4) + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
            $script:PublicDiagnosticWritten = $true
            $saveStatus = 'success'
        }
    }
    catch {
        $saveStatus = 'failure'
        $saveErrorKind = switch ($_.Exception.GetType().Name) {
            { $_ -in @('IOException', 'FileLoadException') } { 'io'; break }
            'UnauthorizedAccessException' { 'access_denied'; break }
            { $_ -in @('PathTooLongException', 'DirectoryNotFoundException') } { 'path'; break }
            'ArgumentException' { 'argument'; break }
            default { 'other' }
        }
    }
    $privateLogRetained = $null -ne $privateLog -and (Test-Path -LiteralPath $privateLog -PathType Leaf)
    try {
        Save-CnMonMsixSignatureDiagnosticStatus -ArtifactDirectory $ArtifactDirectory -Guard $guard -Phase $Phase -Operation $operation -FailureKind $failureKind -ExceptionKind $exceptionKind -ErrorCategory $errorCategory -ToolName $toolName -SdkExitCaptured:$sdkExitCaptured -PrivateLogRetained:$privateLogRetained -TrustPresenceChecked:$TrustPresenceChecked -TrustedCertificatePresent:$TrustedCertificatePresent -CatalogFootprintPresent:$catalogFootprintPresent -SaveStatus $saveStatus -SaveErrorKind $saveErrorKind -PublicDiagnosticWritten:(Test-Path -LiteralPath $publicPath -PathType Leaf)
    } catch { }
}

function Assert-CnMonMsixSignature {
    param(
        [string]$Path,
        [string]$LogPath,
        [ValidateSet('candidate-verify', 'user-verify', 'certify-verify')][string]$Phase = 'candidate-verify',
        [object]$Identity
    )
    $script:CurrentGate = 'signature'
    $script:LastSignatureVerifyExitCode = $null
    $script:LastSignatureLogPath = $LogPath
    $signtool = Join-Path $SdkBinDirectory 'signtool.exe'
    if (-not (Test-Path -LiteralPath $signtool -PathType Leaf)) {
        Set-CnMonMsixGate 'signature' 'incomplete'
        throw 'SignTool is unavailable'
    }
    try {
        Invoke-CnMonMsixTool $signtool @('verify', '/pa', '/v', $Path) $LogPath
    }
    catch {
        if ($null -ne $script:LastSdkToolExitCode) {
            $script:LastSignatureVerifyExitCode = [int]$script:LastSdkToolExitCode
        }
        throw
    }
    Set-CnMonMsixGate 'signature' 'pass'
}

function Assert-CnMonMsixOrdinaryUser {
    param([string]$Sid)
    $script:CurrentGate = 'account'
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = [Security.Principal.WindowsPrincipal]::new($identity)
    $groupSids = @($identity.Groups | ForEach-Object { $_.Value })
    if ($identity.User.Value -cne $Sid -or
        $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator) -or
        (Test-CnMonMsixAdministratorsMembership $groupSids) -or
        [string]::IsNullOrWhiteSpace($env:USERPROFILE) -or
        [string]::IsNullOrWhiteSpace($env:LOCALAPPDATA)) {
        throw 'current process is not the expected ordinary user with a loaded profile'
    }
    $profile = Get-CimInstance -ClassName Win32_UserProfile -Filter "SID='$Sid'" -ErrorAction Stop
    if ($null -eq $profile -or
        [IO.Path]::GetFullPath($profile.LocalPath) -ine [IO.Path]::GetFullPath($env:USERPROFILE)) {
        throw 'current environment does not identify the actual loaded user profile'
    }
    Set-CnMonMsixGate 'account' 'pass'
}

function Get-CnMonMsixPackage {
    param([object]$Identity)
    $packages = @(Get-AppxPackage -Name ([string]$Identity.Name) -ErrorAction Stop)
    return $packages | Where-Object {
        [string]$_.Publisher -ceq [string]$Identity.Publisher -and
        [string]$_.PackageFamilyName -ceq [string]$Identity.ExpectedPFN
    } | Sort-Object { [version]$_.Version } -Descending | Select-Object -First 1
}

function Assert-CnMonMsixInstalledPackage {
    param([System.Collections.IDictionary]$Receipt, [object]$Identity)
    $script:CurrentGate = 'package_registration'
    $package = Get-CnMonMsixPackage $Identity
    if ($null -eq $package) {
        throw 'candidate package is not registered for the current user'
    }
    Set-CnMonMsixGate 'package_registration' 'pass'
    $script:CurrentGate = 'exact_identity'
    if ([string]$package.Name -cne [string]$Identity.Name -or
        [string]$package.Publisher -cne [string]$Identity.Publisher -or
        [string]$package.Architecture -cne 'X64' -or
        [string]$package.Version -cne [string]$Receipt.package_version -or
        [string]$package.PackageFamilyName -cne [string]$Identity.ExpectedPFN) {
        throw 'registered package identity or version does not match the signed receipt'
    }
    Set-CnMonMsixGate 'exact_identity' 'pass'
    $executable = Join-Path $package.InstallLocation 'cn-mon.exe'
    $script:CurrentGate = 'installed_binary_hash'
    if (-not (Test-Path -LiteralPath $executable -PathType Leaf) -or
        (Get-FileHash -LiteralPath $executable -Algorithm SHA256).Hash.ToLowerInvariant() -cne ([string]$Receipt.binary_sha256).ToLowerInvariant()) {
        throw 'installed executable hash does not match the signed receipt'
    }
    $sidecarKey = 'cn-mon.exe.manifest'
    $sidecarExpectedHash = $null
    if ($Receipt.files -is [System.Collections.IDictionary] -and $Receipt.files.Contains($sidecarKey)) {
        $sidecarExpectedHash = $Receipt.files[$sidecarKey]
    }
    $sidecarManifest = Join-Path $package.InstallLocation $sidecarKey
    if ($sidecarExpectedHash -isnot [string] -or $sidecarExpectedHash -cnotmatch '^[0-9a-f]{64}$' -or
        -not (Test-Path -LiteralPath $sidecarManifest -PathType Leaf) -or
        (Get-FileHash -LiteralPath $sidecarManifest -Algorithm SHA256).Hash.ToLowerInvariant() -cne $sidecarExpectedHash) {
        throw 'installed package sidecar manifest hash does not match the signed receipt'
    }
    Set-CnMonMsixGate 'installed_binary_hash' 'pass'
    return $package
}

function Invoke-CnMonMsixNativeTests {
    param([string]$Name, [string]$TestExe, [string]$Pattern, [string[]]$RequiredMarkers)
    $diagnosticPhases = @('real-profile-config', 'appdata-seed', 'appdata-upgrade-readback', 'appdata-repair-readback', 'appdata-reinstall-fresh')
    if ($Name -in $diagnosticPhases) { $script:NativeTestFailure = $null }
    $stdout = Join-Path $ArtifactDirectory "msix-$Name.stdout.log"
    $stderr = Join-Path $ArtifactDirectory "msix-$Name.stderr.log"
    $listStdout = Join-Path $ArtifactDirectory "msix-$Name.tests.log"
    $listStderr = Join-Path $ArtifactDirectory "msix-$Name.tests-error.log"
    $selector = Get-CnMonMsixNativeSelector $Pattern
    & $TestExe "-test.list=$selector" 1> $listStdout 2> $listStderr
    $listExit = $LASTEXITCODE
    $selectedTests = @()
    if (Test-Path -LiteralPath $listStdout) { $selectedTests = @(Get-Content -LiteralPath $listStdout | Where-Object { -not [string]::IsNullOrWhiteSpace($_) }) }
    if ($listExit -ne 0 -or $selectedTests.Count -eq 0) {
        throw "native Windows qualification phase '$Name' selected no Go tests"
    }
    Remove-Item -LiteralPath $stdout, $stderr -Force -ErrorAction SilentlyContinue
    & $TestExe '-test.v' "-test.run=$selector" '-test.count=1' '-test.timeout=10m' 1> $stdout 2> $stderr
    $exit = $LASTEXITCODE
    $output = ''
    if (Test-Path -LiteralPath $stdout) { $output += Get-Content -LiteralPath $stdout -Raw }
    if (Test-Path -LiteralPath $stderr) { $output += Get-Content -LiteralPath $stderr -Raw }
    if ($exit -ne 0) {
        if ($Name -in $diagnosticPhases) {
            $script:NativeTestFailure = Get-CnMonMsixNativeTestFailureDiagnostic -Phase $Name -FailureKind child_exit -ExitCode $exit -Output $output -ProfileHistoryPath $env:CNMON_MSIX_PROFILE_HISTORY -ProfileLogPath $env:CNMON_MSIX_PROFILE_LOG -AppDataHistoryPath $env:CNMON_MSIX_APPDATA_HISTORY -AppDataLogPath $env:CNMON_MSIX_APPDATA_LOG -LocalAppData $env:LOCALAPPDATA -PackageFamilyName $env:CNMON_MSIX_EXPECTED_PFN
        }
        throw "native Windows qualification phase '$Name' failed; private output was retained"
    }
    foreach ($marker in $RequiredMarkers) {
        if ($output -notmatch [regex]::Escape($marker)) {
            if ($Name -in $diagnosticPhases) {
                $script:NativeTestFailure = Get-CnMonMsixNativeTestFailureDiagnostic -Phase $Name -FailureKind missing_marker -ExitCode $exit -Output $output -ProfileHistoryPath $env:CNMON_MSIX_PROFILE_HISTORY -ProfileLogPath $env:CNMON_MSIX_PROFILE_LOG -AppDataHistoryPath $env:CNMON_MSIX_APPDATA_HISTORY -AppDataLogPath $env:CNMON_MSIX_APPDATA_LOG -LocalAppData $env:LOCALAPPDATA -PackageFamilyName $env:CNMON_MSIX_EXPECTED_PFN
            }
            throw "native Windows qualification phase '$Name' omitted a required witness"
        }
    }
    return $output
}

function Get-CnMonMsixNativeTestFailureDiagnostic {
    param(
        [ValidateSet('real-profile-config', 'appdata-seed', 'appdata-upgrade-readback', 'appdata-repair-readback', 'appdata-reinstall-fresh')][string]$Phase,
        [ValidateSet('child_exit', 'missing_marker')][string]$FailureKind,
        [int]$ExitCode,
        [string]$Output,
        [string]$ProfileHistoryPath,
        [string]$ProfileLogPath,
        [string]$AppDataHistoryPath,
        [string]$AppDataLogPath,
        [string]$LocalAppData,
        [string]$PackageFamilyName
    )
    if (($FailureKind -eq 'child_exit' -and $ExitCode -eq 0) -or
        ($FailureKind -eq 'missing_marker' -and $ExitCode -ne 0)) {
        throw 'native test failure evidence has an inconsistent child result'
    }

    $sourceLines = [Collections.Generic.List[int]]::new()
    foreach ($match in [regex]::Matches($Output, '(?im)^\s*(?:.*[\\/])?native_msix_windows_test\.go:(\d+):')) {
        $lineNumber = 0
        if ([int]::TryParse($match.Groups[1].Value, [ref]$lineNumber) -and
            $lineNumber -ge 1 -and $lineNumber -le 10000 -and
            -not $sourceLines.Contains($lineNumber)) {
            $sourceLines.Add($lineNumber)
        }
        if ($sourceLines.Count -ge 8) { break }
    }

    $appDataExternalHistoryExists = [bool]($AppDataHistoryPath -and (Test-Path -LiteralPath $AppDataHistoryPath -PathType Leaf -ErrorAction SilentlyContinue))
    $appDataExternalLogExists = [bool]($AppDataLogPath -and (Test-Path -LiteralPath $AppDataLogPath -PathType Leaf -ErrorAction SilentlyContinue))
    $appDataCacheHistoryExists = $false
    $appDataCacheLogExists = $false
    if ($LocalAppData -and $PackageFamilyName) {
        $cacheRoot = Join-Path $LocalAppData (Join-Path 'Packages' (Join-Path $PackageFamilyName 'LocalCache/Local'))
        if ($AppDataHistoryPath) {
            $appDataCacheHistoryExists = Test-Path -LiteralPath (Join-Path $cacheRoot (Split-Path -Leaf $AppDataHistoryPath)) -PathType Leaf -ErrorAction SilentlyContinue
        }
        if ($AppDataLogPath) {
            $appDataCacheLogExists = Test-Path -LiteralPath (Join-Path $cacheRoot (Split-Path -Leaf $AppDataLogPath)) -PathType Leaf -ErrorAction SilentlyContinue
        }
    }
    $testName = if ($Phase -eq 'real-profile-config') { 'NativeMSIXRealProfileLayers' } else { 'NativeMSIXAppDataLifecycle' }
    return [ordered]@{
        phase = $Phase
        test_name = $testName
        failure_kind = $FailureKind
        exit_code = $ExitCode
        timeout = [bool]($Output -match '(?im)(?:panic:\s*)?test timed out after\b|context deadline exceeded')
        source_lines = @($sourceLines.ToArray())
        profile_history_exists = [bool]($ProfileHistoryPath -and (Test-Path -LiteralPath $ProfileHistoryPath -PathType Leaf -ErrorAction SilentlyContinue))
        profile_log_exists = [bool]($ProfileLogPath -and (Test-Path -LiteralPath $ProfileLogPath -PathType Leaf -ErrorAction SilentlyContinue))
        appdata_external_history_exists = $appDataExternalHistoryExists
        appdata_external_log_exists = $appDataExternalLogExists
        appdata_package_localcache_history_exists = [bool]$appDataCacheHistoryExists
        appdata_package_localcache_log_exists = [bool]$appDataCacheLogExists
    }
}

function Invoke-CnMonMsixShell {
    param([string]$FilePath, [string[]]$Arguments, [hashtable]$Environment, [string]$LogPrefix, [string]$StandardInput, [string]$WorkingDirectory)
    $info = [Diagnostics.ProcessStartInfo]::new()
    $info.FileName = $FilePath
    $info.UseShellExecute = $false
    $info.RedirectStandardInput = $null -ne $StandardInput
    $info.RedirectStandardOutput = $true
    $info.RedirectStandardError = $true
    if ($WorkingDirectory) { $info.WorkingDirectory = $WorkingDirectory }
    foreach ($argument in $Arguments) { $info.ArgumentList.Add($argument) }
    if ($Environment) {
        foreach ($key in $Environment.Keys) { $info.Environment[$key] = [string]$Environment[$key] }
    }
    $process = [Diagnostics.Process]::Start($info)
    try {
        $outTask = $process.StandardOutput.ReadToEndAsync()
        $errTask = $process.StandardError.ReadToEndAsync()
        if ($null -ne $StandardInput) {
            $process.StandardInput.Write($StandardInput)
            $process.StandardInput.Close()
        }
        if (-not $process.WaitForExit(60000)) {
            try { $process.Kill($true) } catch { }
            throw 'shell alias check timed out'
        }
        [Threading.Tasks.Task]::WaitAll([Threading.Tasks.Task[]]@($outTask, $errTask))
        [IO.File]::WriteAllText("$LogPrefix.stdout.log", $outTask.Result, [Text.UTF8Encoding]::new($false))
        [IO.File]::WriteAllText("$LogPrefix.stderr.log", $errTask.Result, [Text.UTF8Encoding]::new($false))
        return [pscustomobject]@{ ExitCode = $process.ExitCode; Stdout = $outTask.Result; Stderr = $errTask.Result }
    }
    finally { $process.Dispose() }
}

function Assert-CnMonMsixExpectedNativeRejection {
    param([string]$Name, [string]$TestExe, [string]$Pattern, [string]$ExpectedReason = 'alias-selection|package-identity')
    $stdout = Join-Path $ArtifactDirectory "msix-$Name.stdout.log"
    $stderr = Join-Path $ArtifactDirectory "msix-$Name.stderr.log"
    $selector = Get-CnMonMsixNativeSelector $Pattern
    $listStdout = Join-Path $ArtifactDirectory "msix-$Name.tests.log"
    $listStderr = Join-Path $ArtifactDirectory "msix-$Name.tests-error.log"
    & $TestExe "-test.list=$selector" 1> $listStdout 2> $listStderr
    $selectedTests = @()
    if (Test-Path -LiteralPath $listStdout) { $selectedTests = @(Get-Content -LiteralPath $listStdout | Where-Object { -not [string]::IsNullOrWhiteSpace($_) }) }
    if ($LASTEXITCODE -ne 0 -or $selectedTests.Count -eq 0) {
        throw "negative control '$Name' selected no Go tests"
    }
    & $TestExe '-test.v' "-test.run=$selector" '-test.count=1' 1> $stdout 2> $stderr
    $output = ''
    if (Test-Path -LiteralPath $stdout) { $output += Get-Content -LiteralPath $stdout -Raw }
    if (Test-Path -LiteralPath $stderr) { $output += Get-Content -LiteralPath $stderr -Raw }
    if ($LASTEXITCODE -eq 0 -or $output -notmatch "CNMON_NATIVE_MSIX_REJECTED reason=($ExpectedReason)") {
        throw "negative control '$Name' did not prove that the package alias was unavailable"
    }
}

function New-CnMonMsixFixture {
    param([string]$UnsignedPackage, [System.Collections.IDictionary]$Receipt, [object]$Identity, [string]$Destination, [string]$FixtureReceiptPath, [string]$HistoryPath, [string]$LogPath)
    $script:CurrentGate = 'upgrade'
    $makeappx = Join-Path $SdkBinDirectory 'makeappx.exe'
    $stage = Join-Path $script:PrivateRoot 'fixture-stage'
    $unpacked = Join-Path $script:PrivateRoot 'candidate-unpacked'
    New-Item -ItemType Directory -Path $unpacked | Out-Null
    Invoke-CnMonMsixTool $makeappx @('unpack', '/p', $UnsignedPackage, '/d', $unpacked, '/no') (Join-Path $script:PrivateRoot 'fixture-source-unpack.log')
    Assert-CnMonMsixPackagePayload $unpacked $Receipt.files
    New-Item -ItemType Directory -Path $stage | Out-Null
    Get-ChildItem -LiteralPath $unpacked -Force | ForEach-Object {
        Copy-Item -LiteralPath $_.FullName -Destination $stage -Recurse
    }
    $manifest = Join-Path $stage 'AppxManifest.xml'
    $xml = [xml](Get-Content -LiteralPath $manifest -Raw)
    $identityNode = $xml.SelectSingleNode('//*[local-name()="Identity"]')
    if ($null -eq $identityNode -or $identityNode.GetAttribute('Name') -cne [string]$Identity.Name -or
        $identityNode.GetAttribute('Publisher') -cne [string]$Identity.Publisher) {
        throw 'candidate manifest identity changed before fixture generation'
    }
    $identityNode.SetAttribute('Version', '1.2.999.0')
    $xml.Save($manifest)
    $config = "[ping]`nhistory_file = $HistoryPath`n[logging]`nlogfile = $LogPath`nlevel = INFO`n"
    [IO.File]::WriteAllText((Join-Path $stage '.cn'), $config, [Text.UTF8Encoding]::new($false))
    Invoke-CnMonMsixTool $makeappx @('pack', '/d', $stage, '/p', $Destination, '/no') (Join-Path $script:PrivateRoot 'fixture-pack.log')
    $signtool = Join-Path $SdkBinDirectory 'signtool.exe'
    Invoke-CnMonMsixTool $signtool @('sign', '/fd', 'SHA256', '/s', 'My', '/sm', '/sha1', [string]$script:CreatedCertificate, $Destination) (Join-Path $script:PrivateRoot 'fixture-sign.log')
    Invoke-CnMonMsixTool $signtool @('verify', '/pa', '/v', $Destination) (Join-Path $script:PrivateRoot 'fixture-verify.log')

    $binaryHash = (Get-FileHash -LiteralPath (Join-Path $stage 'cn-mon.exe') -Algorithm SHA256).Hash.ToLowerInvariant()
    $manifestHash = (Get-FileHash -LiteralPath $manifest -Algorithm SHA256).Hash.ToLowerInvariant()
    $configPath = Join-Path $stage '.cn'
    $configHash = (Get-FileHash -LiteralPath $configPath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($binaryHash -cne ([string]$Receipt.binary_sha256).ToLowerInvariant()) {
        throw 'fixture binary provenance does not match the original candidate receipt'
    }
    $fixtureFiles = Get-CnMonMsixFixtureFiles -SourceFiles $Receipt.files -BinaryHash $binaryHash -ManifestHash $manifestHash -ConfigHash $configHash
    $fixtureUnpacked = Join-Path $script:PrivateRoot 'fixture-signed-unpacked'
    New-Item -ItemType Directory -Path $fixtureUnpacked | Out-Null
    Invoke-CnMonMsixTool $makeappx @('unpack', '/p', $Destination, '/d', $fixtureUnpacked, '/no') (Join-Path $script:PrivateRoot 'fixture-signed-unpack.log')
    Assert-CnMonMsixPackagePayload $fixtureUnpacked $fixtureFiles -Signed
    $packageHash = (Get-FileHash -LiteralPath $Destination -Algorithm SHA256).Hash.ToLowerInvariant()
    $fixtureReceipt = [ordered]@{
        schema_version = 1
        purpose = 'test-fixture'
        source_package_sha256 = ([string]$Receipt.package_sha256).ToLowerInvariant()
        source_manifest_sha256 = ([string]$Receipt.manifest_sha256).ToLowerInvariant()
        binary_sha256 = $binaryHash
        name = [string]$Identity.Name
        publisher = [string]$Identity.Publisher
        expected_pfn = [string]$Identity.ExpectedPFN
        architecture = 'x64'
        package_version = '1.2.999.0'
        manifest_sha256 = $manifestHash
        config_sha256 = $configHash
        package_sha256 = $packageHash
        files = $fixtureFiles
        signing = [ordered]@{
            status = 'test'
            certificate_thumbprint = [string]$script:CreatedCertificate
            signed_package_sha256 = $packageHash
        }
    }
    [IO.File]::WriteAllText($FixtureReceiptPath, (ConvertTo-Json -InputObject $fixtureReceipt -Depth 8) + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
}

function Assert-CnMonMsixFixtureReceipt {
    param([string]$PackagePath, [System.Collections.IDictionary]$FixtureReceipt, [System.Collections.IDictionary]$CandidateReceipt, [object]$Identity, [object]$InstalledPackage)
    $sourcePackageHash = if ([string]$CandidateReceipt.signing.status -ceq 'test') {
        [string]$CandidateReceipt.signing.unsigned_package_sha256
    }
    else { [string]$CandidateReceipt.package_sha256 }
    if ([int]$FixtureReceipt.schema_version -ne 1 -or
        [string]$FixtureReceipt.purpose -cne 'test-fixture' -or
        [string]$FixtureReceipt.name -cne [string]$Identity.Name -or
        [string]$FixtureReceipt.publisher -cne [string]$Identity.Publisher -or
        [string]$FixtureReceipt.expected_pfn -cne [string]$Identity.ExpectedPFN -or
        [string]$FixtureReceipt.architecture -cne 'x64' -or
        [string]$FixtureReceipt.package_version -cne '1.2.999.0' -or
        [string]$FixtureReceipt.source_package_sha256 -cne $sourcePackageHash.ToLowerInvariant() -or
        [string]$FixtureReceipt.source_manifest_sha256 -cne ([string]$CandidateReceipt.manifest_sha256).ToLowerInvariant() -or
        [string]$FixtureReceipt.binary_sha256 -cne ([string]$CandidateReceipt.binary_sha256).ToLowerInvariant() -or
        [string]$FixtureReceipt.signing.status -cne 'test' -or
        [string]$FixtureReceipt.signing.certificate_thumbprint -cne [string]$CandidateReceipt.signing.certificate_thumbprint -or
        [string]$FixtureReceipt.package_sha256 -notmatch '^[0-9a-f]{64}$' -or
        [string]$FixtureReceipt.manifest_sha256 -notmatch '^[0-9a-f]{64}$' -or
        [string]$FixtureReceipt.config_sha256 -notmatch '^[0-9a-f]{64}$') {
        throw 'test fixture receipt does not match the signed candidate provenance'
    }
    if ([string]$FixtureReceipt.package_sha256 -cne ([string]$FixtureReceipt.signing.signed_package_sha256).ToLowerInvariant() -or
        (Get-FileHash -LiteralPath $PackagePath -Algorithm SHA256).Hash.ToLowerInvariant() -cne [string]$FixtureReceipt.package_sha256) {
        throw 'test fixture package hash does not match its private receipt'
    }
    foreach ($relativePath in $FixtureReceipt.files.Keys) {
        $expectedHash = ([string]$FixtureReceipt.files[$relativePath]).ToLowerInvariant()
        if ($expectedHash -notmatch '^[0-9a-f]{64}$') { throw 'qualification fixture file hash is malformed' }
    }
    if ([string]$FixtureReceipt.files['cn-mon.exe'] -cne ([string]$CandidateReceipt.binary_sha256).ToLowerInvariant() -or
        [string]$FixtureReceipt.files['AppxManifest.xml'] -cne ([string]$FixtureReceipt.manifest_sha256).ToLowerInvariant() -or
        [string]$FixtureReceipt.files['.cn'] -cne ([string]$FixtureReceipt.config_sha256).ToLowerInvariant()) {
        throw 'qualification fixture file hash map is inconsistent'
    }
    if ($null -ne $InstalledPackage) {
        if ([string]$InstalledPackage.Name -cne [string]$FixtureReceipt.name -or
            [string]$InstalledPackage.Publisher -cne [string]$FixtureReceipt.publisher -or
            [string]$InstalledPackage.PackageFamilyName -cne [string]$FixtureReceipt.expected_pfn -or
            [string]$InstalledPackage.Architecture -cne 'X64' -or
            [string]$InstalledPackage.Version -cne [string]$FixtureReceipt.package_version) {
            throw 'registered lower-version fixture identity does not match its private receipt'
        }
        foreach ($relativePath in $FixtureReceipt.files.Keys) {
            $installedPath = Join-Path $InstalledPackage.InstallLocation $relativePath
            if (-not (Test-Path -LiteralPath $installedPath -PathType Leaf) -or
                (Get-FileHash -LiteralPath $installedPath -Algorithm SHA256).Hash.ToLowerInvariant() -cne ([string]$FixtureReceipt.files[$relativePath]).ToLowerInvariant()) {
                throw 'installed qualification fixture payload does not match its private receipt'
            }
        }
    }
}

function New-CnMonMsixTemporaryUser {
    $script:CurrentGate = 'account'
    $script:CurrentSourceOperation = 'temporary_user_account_create'
    $name = 'cmsx' + [Guid]::NewGuid().ToString('N').Substring(0, 12)
    $random = [Convert]::ToBase64String([Security.Cryptography.RandomNumberGenerator]::GetBytes(36)) + 'aA1!'
    $password = ConvertTo-SecureString $random -AsPlainText -Force
    $user = New-LocalUser -Name $name -Password $password -AccountNeverExpires -PasswordNeverExpires -ErrorAction Stop
    $script:CreatedUser = $name
    $script:CreatedUserSid = [string]$user.SID.Value
    $credential = [Management.Automation.PSCredential]::new("$env:COMPUTERNAME\$name", $password)
    $pwsh = Join-Path $PSHOME 'pwsh.exe'
    $initializeOut = Join-Path $script:PrivateRoot 'profile-init.stdout.log'
    $initializeErr = Join-Path $script:PrivateRoot 'profile-init.stderr.log'
    $script:CurrentSourceOperation = 'temporary_user_profile_initialize'
    $process = Start-Process -FilePath $pwsh -Credential $credential -LoadUserProfile -WorkingDirectory $repositoryRoot -ArgumentList '-NoProfile -Command "exit 0"' -PassThru -RedirectStandardOutput $initializeOut -RedirectStandardError $initializeErr
    $script:CurrentSourceOperation = 'temporary_user_profile_wait'
    if (-not $process.WaitForExit(60000) -or $process.ExitCode -ne 0) {
        if (-not $process.HasExited) { Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue }
        throw 'temporary ordinary-user profile could not be initialized'
    }
    $script:CurrentSourceOperation = 'temporary_user_profile_validate'
    $sid = $script:CreatedUserSid
    $profile = Get-CimInstance -ClassName Win32_UserProfile -Filter "SID='$sid'" -ErrorAction Stop
    if ($null -eq $profile -or -not (Test-Path -LiteralPath $profile.LocalPath -PathType Container)) {
        throw 'temporary ordinary-user profile was not created'
    }
    $script:CreatedProfile = [pscustomobject]@{ SID = $sid; Path = [IO.Path]::GetFullPath($profile.LocalPath) }
    return [pscustomobject]@{ Name = $name; SID = $sid; Credential = $credential; Profile = $script:CreatedProfile.Path }
}

function Invoke-CnMonMsixUserRun {
    param([pscustomobject]$User, [string]$Candidate, [string]$SignedReceipt, [string]$Fixture, [switch]$InjectFailure)
    $script:CurrentGate = 'account'
    $script:CurrentSourceOperation = 'user_run_environment_setup'
    $profile = $User.Profile
    $local = Join-Path $profile 'AppData/Local'
    $roaming = Join-Path $profile 'AppData/Roaming'
    $temp = Join-Path $local 'Temp'
    New-Item -ItemType Directory -Force -Path $temp, $roaming | Out-Null
    $script:CurrentSourceOperation = 'temporary_user_acl_setup'
    $sidIdentity = [Security.Principal.SecurityIdentifier]::new([string]$User.SID)
    $sidRule = [Security.AccessControl.FileSystemAccessRule]::new($sidIdentity, 'Modify', 'ContainerInherit,ObjectInherit', 'None', 'Allow')
    $acl = Get-Acl -LiteralPath $ArtifactDirectory
    if ($null -eq $script:OriginalArtifactAcl) { $script:OriginalArtifactAcl = $acl.GetSecurityDescriptorBinaryForm() }
    $acl.AddAccessRule($sidRule)
    Set-Acl -LiteralPath $ArtifactDirectory -AclObject $acl

    $envOverrides = @{
        USERPROFILE = $profile
        HOME = $profile
        APPDATA = $roaming
        LOCALAPPDATA = $local
        TEMP = $temp
        TMP = $temp
        PATH = (Join-Path $local 'Microsoft/WindowsApps') + ';' + $env:PATH
        CNMON_MSIX_FIXTURE_PACKAGE = $Fixture
        CNMON_MSIX_TEST_EXE = (Join-Path $ArtifactDirectory 'msix-tests.exe')
        CNMON_MSIX_TEST = '1'
        CNMON_MSIX_EXPECTED_SID = $User.SID
        CNMON_MSIX_FIXTURE_HISTORY = (Join-Path $ArtifactDirectory '.msix-fixture-history.db')
        CNMON_MSIX_FIXTURE_LOG = (Join-Path $ArtifactDirectory '.msix-fixture.log')
        CNMON_MSIX_FIXTURE_RECEIPT = (Join-Path $ArtifactDirectory '.cn-mon-1.2.999.0-fixture-receipt.json')
        CNMON_MSIX_PROFILE_HISTORY = (Join-Path $profile 'msix-profile-history.db')
        CNMON_MSIX_PROFILE_LOG = (Join-Path $profile 'msix-profile.log')
        CNMON_MSIX_CLI_HISTORY = (Join-Path $profile 'msix-cli-history.db')
        CNMON_MSIX_CLI_LOG = (Join-Path $profile 'msix-cli.log')
        CNMON_NATIVE_COMMAND = '1'
        CNMON_NATIVE_COMMAND_EXPECT_PROBE = 'ordinary'
        CNMON_NATIVE_COMMAND_EXPECT_SID = $User.SID
        CNMON_NATIVE_COMMAND_BINARY = (Join-Path $local 'Microsoft/WindowsApps/cn-mon.exe')
        GITHUB_ACTIONS = $env:GITHUB_ACTIONS
        GITHUB_EVENT_NAME = $env:GITHUB_EVENT_NAME
        GITHUB_REPOSITORY = $env:GITHUB_REPOSITORY
        ImageOS = $env:ImageOS
    }
    foreach ($key in @('CNMON_MSIX_IDENTITY_NAME', 'CNMON_MSIX_IDENTITY_PUBLISHER', 'CNMON_MSIX_PUBLISHER_DISPLAY_NAME', 'CNMON_MSIX_EXPECTED_PFN')) {
        $envOverrides[$key] = [Environment]::GetEnvironmentVariable($key)
    }
    $args = @('-NoProfile', '-File', $PSCommandPath, '-Mode', 'User', '-PackagePath', $Candidate, '-ReceiptPath', $SignedReceipt, '-SdkBinDirectory', $SdkBinDirectory, '-ArtifactDirectory', $ArtifactDirectory, '-ExpectedSid', $User.SID)
    if ($InjectFailure) { $args += '-FailAfterInstall' }
    $argLine = ($args | ForEach-Object { '"' + ([string]$_).Replace('"', '\"') + '"' }) -join ' '
    $stdout = Join-Path $script:PrivateRoot $(if ($InjectFailure) { 'injected-user.stdout.log' } else { 'ordinary-user.stdout.log' })
    $stderr = Join-Path $script:PrivateRoot $(if ($InjectFailure) { 'injected-user.stderr.log' } else { 'ordinary-user.stderr.log' })
    $pwsh = Join-Path $PSHOME 'pwsh.exe'
    $script:CurrentSourceOperation = 'user_run_process_start'
    $process = Start-Process -FilePath $pwsh -Credential $User.Credential -LoadUserProfile -Environment $envOverrides -WorkingDirectory $repositoryRoot -ArgumentList $argLine -PassThru -RedirectStandardOutput $stdout -RedirectStandardError $stderr
    $script:CurrentSourceOperation = 'user_run_process_wait'
    if (-not $process.WaitForExit(15 * 60 * 1000)) {
        Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
        throw 'ordinary-user package qualification exceeded its time limit'
    }
    $script:CurrentSourceOperation = 'user_run_result_read'
    return [pscustomobject]@{ ExitCode = $process.ExitCode; ResultPath = (Join-Path $ArtifactDirectory 'msix-native-results.json') }
}

function ConvertFrom-CnMonMsixDesktopSurfaceReport {
    param([string]$Json, [object]$ExitCode, [bool]$TimedOut)
    $status = if ($TimedOut) { 'timeout' } else { 'ineligible' }
    $fallback = [ordered]@{
        schema_version = 1
        candidate_installed = $false
        automation_process_session_id = 0
        settings_opened_by_probe = $false
        settings_same_sid = $false
        settings_same_session = $false
        probe_status = $status
        repair_button_status = 'not_observed'
    }
    if ($TimedOut -or $null -eq $ExitCode -or (($ExitCode -isnot [int]) -and ($ExitCode -isnot [long])) -or [long]$ExitCode -ne 0 -or [string]::IsNullOrWhiteSpace($Json)) {
        return [pscustomobject]$fallback
    }
    try {
        $report = ConvertFrom-Json -InputObject $Json -AsHashtable -ErrorAction Stop
        if ($report -isnot [System.Collections.IDictionary]) { return [pscustomobject]$fallback }
        $expectedKeys = @('schema_version', 'candidate_installed', 'automation_process_session_id', 'settings_opened_by_probe', 'settings_same_sid', 'settings_same_session', 'probe_status', 'repair_button_status')
        $actualKeys = @($report.Keys | ForEach-Object { [string]$_ } | Sort-Object -CaseSensitive)
        $sortedExpectedKeys = @($expectedKeys | Sort-Object -CaseSensitive)
        if (($actualKeys -join "`n") -cne ($sortedExpectedKeys -join "`n")) { return [pscustomobject]$fallback }
        if ((($report.schema_version -isnot [int]) -and ($report.schema_version -isnot [long])) -or $report.schema_version -ne 1 -or
            (($report.automation_process_session_id -isnot [int]) -and ($report.automation_process_session_id -isnot [long])) -or
            [long]$report.automation_process_session_id -lt 0 -or [long]$report.automation_process_session_id -gt 2147483647) {
            return [pscustomobject]$fallback
        }
        foreach ($name in @('candidate_installed', 'settings_opened_by_probe', 'settings_same_sid', 'settings_same_session')) {
            if ($report[$name] -isnot [bool]) { return [pscustomobject]$fallback }
        }
        $probeStatuses = @('not_attempted', 'candidate_not_installed', 'settings_preexisting', 'settings_unavailable', 'wrong_security_context', 'ui_automation_unavailable', 'observed')
        if ($report.probe_status -isnot [string] -or $report.probe_status -cnotin $probeStatuses -or
            $report.repair_button_status -isnot [string] -or $report.repair_button_status -cnotin @('not_observed', 'present', 'absent')) {
            return [pscustomobject]$fallback
        }
        if (($report.settings_opened_by_probe -and (-not $report.settings_same_sid -or -not $report.settings_same_session)) -or
            ($report.repair_button_status -in @('present', 'absent') -and -not $report.settings_opened_by_probe) -or
            ($report.repair_button_status -in @('present', 'absent') -and $report.probe_status -cne 'observed') -or
            ($report.probe_status -ceq 'observed' -and (
                -not $report.candidate_installed -or
                -not $report.settings_opened_by_probe -or
                -not $report.settings_same_sid -or
                -not $report.settings_same_session -or
                $report.repair_button_status -cnotin @('present', 'absent')
            ))) {
            return [pscustomobject]$fallback
        }
        return [pscustomobject][ordered]@{
            schema_version = 1
            candidate_installed = [bool]$report.candidate_installed
            automation_process_session_id = [int]$report.automation_process_session_id
            settings_opened_by_probe = [bool]$report.settings_opened_by_probe
            settings_same_sid = [bool]$report.settings_same_sid
            settings_same_session = [bool]$report.settings_same_session
            probe_status = [string]$report.probe_status
            repair_button_status = [string]$report.repair_button_status
        }
    }
    catch { return [pscustomobject]$fallback }
}

function Invoke-CnMonMsixDesktopSurfaceProbe {
    param([object]$Identity)
    if (-not (Test-CnMonMsixSyntheticPrDiagnostic -Mode 'User' -Identity $Identity)) { return }

    $publicPath = Join-Path $ArtifactDirectory 'msix-desktop-surface.json'
    if (Test-Path -LiteralPath $publicPath) { return }
    $privateJson = Join-Path $script:PrivateRoot 'desktop-surface-report.json'
    $stdout = Join-Path $script:PrivateRoot 'desktop-surface.stdout.log'
    $stderr = Join-Path $script:PrivateRoot 'desktop-surface.stderr.log'
    $probePath = Join-Path $PSScriptRoot 'test-msix-desktop-surface.ps1'
    $process = $null
    $exitCode = $null
    $timedOut = $false
    $json = $null
    try {
        if (-not (Test-Path -LiteralPath $probePath -PathType Leaf)) { throw 'probe_unavailable' }
        $pwsh = Join-Path $PSHOME 'pwsh.exe'
        $arguments = @('-NoProfile', '-File', $probePath, '-IdentityName', [string]$Identity.Name, '-ExpectedPackageFamilyName', [string]$Identity.ExpectedPFN)
        $argumentLine = ($arguments | ForEach-Object { '"' + ([string]$_).Replace('"', '\"') + '"' }) -join ' '
        $process = Start-Process -FilePath $pwsh -ArgumentList $argumentLine -WorkingDirectory $repositoryRoot -PassThru -RedirectStandardOutput $stdout -RedirectStandardError $stderr
        if (-not $process.WaitForExit(30000)) {
            $timedOut = $true
            try {
                $process.Refresh()
                if (-not $process.HasExited) { $process.Kill(); $null = $process.WaitForExit(3000) }
            }
            catch { }
        }
        else {
            $exitCode = [int]$process.ExitCode
            if (Test-Path -LiteralPath $stdout -PathType Leaf) { $json = [IO.File]::ReadAllText($stdout) }
        }
    }
    catch { }
    finally { if ($null -ne $process) { $process.Dispose() } }

    $safeReport = ConvertFrom-CnMonMsixDesktopSurfaceReport -Json $json -ExitCode $exitCode -TimedOut $timedOut
    try {
        [IO.File]::WriteAllText($publicPath, (ConvertTo-Json -InputObject $safeReport -Depth 3 -Compress) + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
    }
    catch { }
}

function Remove-CnMonMsixTemporaryUser {
    param([string]$Name, [string]$Sid, [string]$IdentityName)
    if ($Name -and $Sid) {
        $packages = @(Get-AppxPackage -User $Sid -Name $IdentityName -ErrorAction SilentlyContinue)
        foreach ($package in $packages) {
            $null = Remove-AppxPackage -Package $package.PackageFullName -User $Sid -ErrorAction Stop
        }
        if (@(Get-AppxPackage -User $Sid -Name $IdentityName -ErrorAction SilentlyContinue).Count -gt 0) {
            throw 'owned user package registration could not be removed'
        }
    }
    if ($Name) {
        $userExists = $false
        $localUser = $null
        try {
            $localUser = Get-LocalUser -Name $Name -ErrorAction Stop
            $userExists = $null -ne $localUser
        }
        catch {
            if ($_.CategoryInfo.Category -ne [System.Management.Automation.ErrorCategory]::ObjectNotFound) { throw }
        }
        if ($userExists -and [string]$localUser.SID -ceq $Sid) {
            try { Remove-LocalUser -Name $Name -ErrorAction Stop }
            catch {
                if ($_.CategoryInfo.Category -ne [System.Management.Automation.ErrorCategory]::ObjectNotFound) { throw }
            }
        }
    }
    if ($Sid) {
        $profile = Get-CimInstance -ClassName Win32_UserProfile -Filter "SID='$Sid'" -ErrorAction SilentlyContinue
        if ($profile -and -not $profile.Special) {
            Remove-CimInstance -InputObject $profile -ErrorAction Stop
        }
    }
}

function Invoke-CnMonMsixUserMode {
    $script:CurrentGate = 'identity'
    $script:CurrentSourceOperation = 'user_mode_account_validation'
    Assert-CnMonMsixWindows
    if (-not $script:PrivateRoot) {
        $script:PrivateRoot = Join-Path $env:TEMP ('cn-mon-msix-user-' + [Guid]::NewGuid().ToString('N'))
        New-Item -ItemType Directory -Path $script:PrivateRoot | Out-Null
    }
    if (-not $ExpectedSid) {
        Set-CnMonMsixGate 'account' 'incomplete'
        $script:CurrentGate = 'account'
        throw 'ordinary-user SID is required'
    }
    Assert-CnMonMsixOrdinaryUser $ExpectedSid
    $script:CurrentSourceOperation = 'receipt_validation'
    $receipt = Get-CnMonMsixReceipt $ReceiptPath
    $identity = Assert-CnMonMsixReceipt $receipt $PackagePath 'test' -RequireCandidate
    $script:Result.evidence = Get-CnMonMsixResultEvidence -PackagePath $PackagePath -ReceiptPath $ReceiptPath -Receipt $receipt -SourceRevision (Get-CnMonMsixSourceRevision)
    $script:DiagnosticIdentity = $identity
    $script:IdentityName = [string]$identity.Name
    $script:CurrentGate = 'signature'
    $script:CurrentSourceOperation = 'user_mode_signature_validation'
    $script:CurrentDiagnosticPhase = 'user-trust-presence'
    $script:TrustPresenceChecked = $false
    $script:TrustedCertificatePresent = $false
    $script:LastSdkToolExitCode = $null
    $thumbprint = [string]$receipt.signing.certificate_thumbprint
    $trusted = @(Get-ChildItem -LiteralPath Cert:\LocalMachine\TrustedPeople, Cert:\CurrentUser\TrustedPeople -ErrorAction Stop | Where-Object { $_.Thumbprint -eq $thumbprint })
    $script:TrustPresenceChecked = $true
    $script:TrustedCertificatePresent = $trusted.Count -gt 0
    if ($trusted.Count -eq 0) {
        Set-CnMonMsixGate 'signature' 'incomplete'
        throw 'test certificate is not already trusted for the current user'
    }
    $script:CurrentDiagnosticPhase = 'user-verify'
    $script:LastSignatureLogPath = Join-Path $ArtifactDirectory 'msix-user-signature.log'
    Assert-CnMonMsixSignature $PackagePath $script:LastSignatureLogPath -Phase 'user-verify' -Identity $identity
    $script:CurrentDiagnosticPhase = 'user-post-verify'
    if (-not (Get-Command Add-AppxPackage -ErrorAction SilentlyContinue) -or -not (Get-Command Remove-AppxPackage -ErrorAction SilentlyContinue)) {
        $script:CurrentGate = 'package_registration'
        Set-CnMonMsixGate 'package_registration' 'incomplete'
        throw 'Appx package cmdlets are unavailable in this session'
    }
    $existing = @(Get-AppxPackage -Name $identity.Name -ErrorAction Stop)
    if ($existing.Count -ne 0) {
        $script:CurrentGate = 'package_registration'
        Set-CnMonMsixGate 'package_registration' 'incomplete'
        throw 'qualification user already has a package with this name'
    }
    $fixtureHistory = [Environment]::GetEnvironmentVariable('CNMON_MSIX_FIXTURE_HISTORY')
    $fixtureLog = [Environment]::GetEnvironmentVariable('CNMON_MSIX_FIXTURE_LOG')
    $targets = Get-CnMonMsixProfileTargets $env:USERPROFILE $env:LOCALAPPDATA $fixtureHistory $fixtureLog
    $profileConfig = $targets.ProfileConfig
    $profileHistoryDefault = $targets.ProfileHistoryDefault
    $profileLogDefault = $targets.ProfileLogDefault
    $profileHistory = $targets.ProfileHistory
    $profileLog = $targets.ProfileLog
    $historyOverride = $targets.CliHistory
    $logOverride = $targets.CliLog
    $appDataToken = [Guid]::NewGuid().ToString('N')
    $appDataHistory = Join-Path $env:LOCALAPPDATA "cn-mon-msix-appdata-$appDataToken.db"
    $appDataLog = Join-Path $env:LOCALAPPDATA "cn-mon-msix-appdata-$appDataToken.log"
    $appDataBackingRoot = Join-Path $env:LOCALAPPDATA (Join-Path 'Packages' (Join-Path $identity.ExpectedPFN 'LocalCache/Local'))
    $appDataLogicalTargets = @($appDataHistory, $appDataLog)
    $appDataBackingTargets = @(
        (Join-Path $appDataBackingRoot (Split-Path -Leaf $appDataHistory)),
        (Join-Path $appDataBackingRoot (Split-Path -Leaf $appDataLog))
    )
    $env:CNMON_MSIX_APPDATA_HISTORY = $appDataHistory
    $env:CNMON_MSIX_APPDATA_LOG = $appDataLog
    $env:CNMON_MSIX_APPDATA_PHASE = ''
    $envProfileHistory = [Environment]::GetEnvironmentVariable('CNMON_MSIX_PROFILE_HISTORY')
    $envProfileLog = [Environment]::GetEnvironmentVariable('CNMON_MSIX_PROFILE_LOG')
    $envCliHistory = [Environment]::GetEnvironmentVariable('CNMON_MSIX_CLI_HISTORY')
    $envCliLog = [Environment]::GetEnvironmentVariable('CNMON_MSIX_CLI_LOG')
    if (-not $envProfileHistory -or -not $envProfileLog -or -not $envCliHistory -or -not $envCliLog -or
        [IO.Path]::GetFullPath($envProfileHistory) -ine [IO.Path]::GetFullPath($profileHistory) -or
        [IO.Path]::GetFullPath($envProfileLog) -ine [IO.Path]::GetFullPath($profileLog) -or
        [IO.Path]::GetFullPath($envCliHistory) -ine [IO.Path]::GetFullPath($historyOverride) -or
        [IO.Path]::GetFullPath($envCliLog) -ine [IO.Path]::GetFullPath($logOverride)) {
        $script:CurrentGate = 'history_config'
        Set-CnMonMsixGate 'history_config' 'incomplete'
        throw 'profile output paths do not match the fresh qualification profile'
    }
    try {
        Assert-CnMonMsixTargetsAbsent (@($targets.All) + @($appDataLogicalTargets) + @($appDataBackingTargets))
    }
    catch {
        $script:CurrentGate = 'history_config'
        Set-CnMonMsixGate 'history_config' 'incomplete'
        throw
    }
    $script:OwnedProfileFiles = @($targets.All)
    Set-CnMonMsixGate 'signature' 'pass'

    $script:CurrentGate = 'upgrade'
    $script:CurrentSourceOperation = 'user_mode_package_registration'
    $fixture = [Environment]::GetEnvironmentVariable('CNMON_MSIX_FIXTURE_PACKAGE')
    $fixtureReceiptPath = [Environment]::GetEnvironmentVariable('CNMON_MSIX_FIXTURE_RECEIPT')
    $testExe = [Environment]::GetEnvironmentVariable('CNMON_MSIX_TEST_EXE')
    if (-not $fixture -or -not (Test-Path -LiteralPath $fixture -PathType Leaf) -or
        -not $fixtureReceiptPath -or -not (Test-Path -LiteralPath $fixtureReceiptPath -PathType Leaf) -or
        -not $testExe -or -not (Test-Path -LiteralPath $testExe -PathType Leaf)) {
        Set-CnMonMsixGate 'upgrade' 'incomplete'
        throw 'private test fixture or native test helper is unavailable'
    }
    $script:OwnedPackageSpecs.Clear()

    $alias = Join-Path $env:LOCALAPPDATA 'Microsoft/WindowsApps/cn-mon.exe'
    $env:CNMON_NATIVE_COMMAND = '1'
    $env:CNMON_NATIVE_COMMAND_BINARY = $alias
    $env:CNMON_NATIVE_COMMAND_EXPECT_PROBE = 'ordinary'
    $env:CNMON_NATIVE_COMMAND_EXPECT_SID = $ExpectedSid
    $env:CNMON_MSIX_TEST = '1'
    $env:CNMON_MSIX_EXPECTED_SID = $ExpectedSid
    $env:CNMON_MSIX_EXPECTED_PFN = $identity.ExpectedPFN
    $env:CNMON_MSIX_FIXTURE_CONFIG = ''
    $env:CNMON_MSIX_PROFILE_DEFAULTS = ''
    $env:CNMON_MSIX_PROFILE_LAYERS = ''
    $env:CNMON_RELEASE_QUALIFY = '1'
    $env:CNMON_RELEASE_VERSION = [string]$receipt.cli_version
    $env:CNMON_RELEASE_REVISION = [string]$receipt.cli_revision
    $env:CNMON_RELEASE_EXPECTED_SHA256 = ([string]$receipt.binary_sha256).ToLowerInvariant()
    $testExe = [Environment]::GetEnvironmentVariable('CNMON_MSIX_TEST_EXE')
    Assert-CnMonMsixExpectedNativeRejection 'missing-package-preflight' $testExe 'NativeMSIXInstalledAlias'

    $script:CurrentSourceOperation = 'user_mode_native_qualification'
    try {
        $fixturePackage = $fixture
        $fixtureReceipt = Get-CnMonMsixReceipt $fixtureReceiptPath
        Assert-CnMonMsixFixtureReceipt $fixturePackage $fixtureReceipt $receipt $identity $null
        Add-CnMonMsixOwnedPackage $identity '1.2.999.0' $fixturePackage $script:OwnedPackageSpecs
        $fixtureInstalled = Get-CnMonMsixPackage $identity
        if ($null -eq $fixtureInstalled) { throw 'lower-version test fixture did not register' }
        Assert-CnMonMsixFixtureReceipt $fixturePackage $fixtureReceipt $receipt $identity $fixtureInstalled
        Set-CnMonMsixGate 'upgrade' 'pass'

        $env:CNMON_MSIX_FIXTURE_CONFIG = '1'
        Invoke-CnMonMsixNativeTests 'fixture-config' $testExe 'TestNativeMSIXFixtureAdjacentConfig' @('CNMON_NATIVE_MSIX_CONFIG adjacent-fixture=selected history=external log=external') | Out-Null
        $script:CurrentGate = 'history_config'
        $env:CNMON_MSIX_APPDATA_PHASE = 'seed'
        Invoke-CnMonMsixNativeTests 'appdata-seed' $testExe 'NativeMSIXAppDataLifecycle' @('CNMON_NATIVE_MSIX_APPDATA phase=seed create=pass readback=pass external=absent localcache=present') | Out-Null
        $appDataRetained = @{}
        foreach ($path in $appDataBackingTargets) {
            if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
                throw 'packaged AppData seed did not create its actual LocalCache outputs'
            }
            $appDataRetained[$path] = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
        }
        $env:CNMON_MSIX_APPDATA_PHASE = ''
        Set-CnMonMsixGate 'history_config' 'pass'

        $script:CurrentGate = 'package_registration'
        Add-CnMonMsixOwnedPackage $identity ([string]$receipt.package_version) $PackagePath $script:OwnedPackageSpecs
        $installed = Assert-CnMonMsixInstalledPackage $receipt $identity
        if ($FailAfterInstall) {
            $script:InjectedFailure = $true
            Set-CnMonMsixGate 'fail_injection' 'pass'
            throw 'controlled post-install failure'
        }

        $script:CurrentGate = 'history_config'
        $env:CNMON_MSIX_APPDATA_PHASE = 'upgrade-readback'
        Invoke-CnMonMsixNativeTests 'appdata-upgrade-readback' $testExe 'NativeMSIXAppDataLifecycle' @('CNMON_NATIVE_MSIX_APPDATA phase=upgrade-readback create=preserved readback=pass external=absent localcache=present') | Out-Null
        Assert-CnMonMsixRetainedFiles $appDataRetained
        $env:CNMON_MSIX_APPDATA_PHASE = ''

        $script:CurrentGate = 'alias_identity'
        $portableDirectory = Join-Path $script:PrivateRoot 'portable'
        New-Item -ItemType Directory -Path $portableDirectory | Out-Null
        $portable = Join-Path $portableDirectory 'cn-mon.exe'
        Copy-Item -LiteralPath (Join-Path $installed.InstallLocation 'cn-mon.exe') -Destination $portable
        $env:CNMON_NATIVE_COMMAND_BINARY = $alias
        $env:CNMON_MSIX_INSTALL_LOCATION = $installed.InstallLocation
        foreach ($key in @('CNMON_MSIX_IDENTITY_NAME', 'CNMON_MSIX_IDENTITY_PUBLISHER', 'CNMON_MSIX_PUBLISHER_DISPLAY_NAME', 'CNMON_MSIX_EXPECTED_PFN')) {
            $envOverridesValue = [Environment]::GetEnvironmentVariable($key)
            [Environment]::SetEnvironmentVariable($key, $envOverridesValue, 'Process')
        }

        $env:CNMON_NATIVE_COMMAND_BINARY = $portable
        $portableStdout = Join-Path $ArtifactDirectory 'msix-portable-rejection.stdout.log'
        $portableStderr = Join-Path $ArtifactDirectory 'msix-portable-rejection.stderr.log'
        & $testExe '-test.v' '-test.run=^TestNativeMSIXInstalledAlias$' '-test.count=1' 1> $portableStdout 2> $portableStderr
        if ($LASTEXITCODE -eq 0 -or ((Get-Content -LiteralPath $portableStdout -Raw) -notmatch 'CNMON_NATIVE_MSIX_REJECTED reason=alias-selection')) {
            throw 'portable binary falsely passed the installed alias gate'
        }
        $env:CNMON_NATIVE_COMMAND_BINARY = $alias
        $env:CNMON_MSIX_PROFILE_DEFAULTS = '1'
        $suite = 'NativeCommandINIHistoryRetention|NativeCommandRerunSavedBatch|NativeCommandHistoryManagement(|ReportsCommittedOutputFailure|SelectionSemantics|LockedCancellation|LiveWriterProtection)|NativeManagementLiveWriterProtection|NativeCommandWindows(FirstCtrlCStopsAndReports|SecondCtrlCInterruptsFinalization|PreInputCtrlCDoesNoStartupIO|ClosedStdoutPipeIsQuiet|ProbePermissionRole)|NativeCommandWindowsPrivateConsole(Q|UppercaseQ|NoColorQ)RestoresConsole|NativeCommandWindowsPrivateConsole(QLeavesWriterActive|HistoryErrorRestoresConsole|FirstCtrlCRestoresConsole|OutputHandleLossPreservesRun|SecondCtrlCWhileDisplayBlocked|SecondCtrlCRestoresDuringBlockedFinalization)|NativeRelease(EndedFollowAllFormats|InputFileAndStdin|UnreadableInputHasNoStartupEffects)|NativeMSIX(InstalledAlias|OrdinaryICMPEcho|LoopbackTCP|RealProfileDefaults)'
        $required = @(
            'CNMON_NATIVE_MSIX_ALIAS package-identity=verified ordinary-token=medium expected-sid=verified version=exact',
            'CNMON_NATIVE_MSIX_CHILD pid=',
            'CNMON_NATIVE_MSIX_CONSOLE_CHILD package-family=verified',
            'CNMON_NATIVE_MSIX_ORDINARY icmp_ipv4=OK icmp_ipv6=OK history=external log=external profile=preserved',
            'CNMON_NATIVE_MSIX_ORDINARY tcp_open=open tcp_refused=closed',
            'CNMON_NATIVE_MSIX_PROFILE default-history=real-profile default-log=real-profile',
            'CNMON_NATIVE_MSIX_VIEW key=v-and-V toggles=verified',
            'CNMON_NATIVE_COMMAND_CONFIG ini90=retained cli30=pruned-old-seed recent-round-retained history-path=ini log-path=ini',
            'CNMON_NATIVE_COMMAND_RERUN actual-command-seed=list-discovery=pass removed-input=replayed tcp=open overrides=10s,10s json-history=agree source-results=preserved invalid-selection=exit2-no-write',
            'CNMON_NATIVE_RELEASE_INPUT file=space-bom stdin=requested tcp=open json=pass history=finished',
            'CNMON_NATIVE_RELEASE_FOLLOW ended=read-only formats=json,table,csv,md exit=0 history=unchanged private-console=restored'
        )
        $nativeOutput = Invoke-CnMonMsixNativeTests 'installed-native-suite' $testExe $suite $required
        Set-CnMonMsixGate 'alias_identity' 'pass'
        Set-CnMonMsixGate 'native_cli' 'pass'
        Set-CnMonMsixGate 'icmp' 'pass'

        $script:CurrentGate = 'history_config'
        $profileHistory = Join-Path $env:USERPROFILE 'msix-profile-history.db'
        $profileLog = Join-Path $env:USERPROFILE 'msix-profile.log'
        $profileText = "[ping]`nhistory_file = $profileHistory`nhistory_days = 90`n[logging]`nlogfile = $profileLog`nlevel = INFO`n"
        [IO.File]::WriteAllText($profileConfig, $profileText, [Text.UTF8Encoding]::new($false))
        $env:CNMON_MSIX_PROFILE_DEFAULTS = ''
        $env:CNMON_MSIX_PROFILE_LAYERS = '1'
        $env:CNMON_MSIX_PROFILE_HISTORY = $profileHistory
        $env:CNMON_MSIX_PROFILE_LOG = $profileLog
        $profileOutput = Invoke-CnMonMsixNativeTests 'real-profile-config' $testExe 'NativeMSIXRealProfileLayers' @('CNMON_NATIVE_MSIX_CONFIG real-profile=selected explicit-config=selected cli-overrides=selected relative-paths=resolved')
        foreach ($path in @($profileHistory, $profileLog, $historyOverride, $logOverride)) {
            if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw 'real-profile config/override output was not created' }
        }
        Set-CnMonMsixGate 'history_config' 'pass'
        $powerShell = Join-Path $PSHOME 'pwsh.exe'
        $commandPrompt = Join-Path $env:SystemRoot 'System32/cmd.exe'
        $expectedVersion = "cn-mon $($receipt.cli_version) ($($receipt.cli_revision))"
        foreach ($spelling in @('cn-mon.exe', 'cn-mon')) {
            $powerShellVersion = Invoke-CnMonMsixShell $powerShell @('-NoProfile', '-NonInteractive', '-Command', "$spelling --version") $null (Join-Path $ArtifactDirectory "msix-shell-powershell-$spelling")
            $commandPromptVersion = Invoke-CnMonMsixShell $commandPrompt @('/d', '/c', "$spelling --version") $null (Join-Path $ArtifactDirectory "msix-shell-cmd-$spelling")
            foreach ($shellResult in @($powerShellVersion, $commandPromptVersion)) {
                if ($shellResult.ExitCode -ne 0 -or $shellResult.Stderr -ne '' -or $shellResult.Stdout.Trim() -cne $expectedVersion) {
                    throw 'PowerShell or Command Prompt did not resolve the installed alias to the exact CLI version'
                }
            }
        }

        $inputListener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
        $inputListener.Start()
        try {
            $inputPort = ([Net.IPEndPoint]$inputListener.LocalEndpoint).Port
            foreach ($spelling in @('cn-mon.exe', 'cn-mon')) {
                $commandLine = "$spelling --format json --tcp $inputPort --file -"
                $input = Invoke-CnMonMsixShell $powerShell @('-NoProfile', '-NonInteractive', '-Command', $commandLine) $null (Join-Path $ArtifactDirectory "msix-shell-powershell-stdin-$spelling") "127.0.0.1`n"
                $cmdInput = Invoke-CnMonMsixShell $commandPrompt @('/d', '/c', $commandLine) $null (Join-Path $ArtifactDirectory "msix-shell-cmd-stdin-$spelling") "127.0.0.1`n"
                foreach ($shellResult in @($input, $cmdInput)) {
                    if ($shellResult.ExitCode -ne 0 -or $shellResult.Stderr -ne '') {
                        throw 'shell alias failed the standard-input request or returned stderr'
                    }
                    try { $report = $shellResult.Stdout | ConvertFrom-Json -ErrorAction Stop }
                    catch { throw 'shell alias did not return a JSON report for its standard-input request' }
                    $tcpState = $report.latest[0].PSObject.Properties["tcp_$inputPort"]
                    if ($null -eq $report.latest -or $report.latest.Count -ne 1 -or $report.latest[0].host -cne '127.0.0.1' -or
                        $null -eq $tcpState -or [string]$tcpState.Value -cne 'open') {
                        throw 'shell alias standard-input report omitted the requested open loopback target'
                    }
                }
            }
        }
        finally { $inputListener.Stop() }
        Set-CnMonMsixGate 'shell_aliases' 'pass'

        $script:CurrentGate = 'conflicts'
        $env:CNMON_MSIX_RESOLUTION_MODE = 'packaged'
        $pathMarker = Invoke-CnMonMsixNativeTests 'path-alias' $testExe 'NativeMSIXResolvedCommandIdentity' @('CNMON_NATIVE_MSIX_PATH packaged=identity-verified spellings=cn-mon.exe,cn-mon')
        $originalPath = $env:PATH
        try {
            $env:PATH = (Split-Path -Parent $portable) + ';' + $originalPath
            $env:CNMON_MSIX_RESOLUTION_MODE = 'portable'
            $null = Invoke-CnMonMsixNativeTests 'portable-path-conflict' $testExe 'NativeMSIXResolvedCommandIdentity' @('CNMON_NATIVE_MSIX_PATH portable-conflict=selected packaged-identity=absent spellings=cn-mon.exe,cn-mon')
            $env:PATH = @($originalPath.Split(';') | Where-Object { $_ -notmatch '\\Microsoft\\WindowsApps\\?$' }) -join ';'
            $env:CNMON_MSIX_RESOLUTION_MODE = 'disabled'
            $null = Invoke-CnMonMsixNativeTests 'disabled-path-alias' $testExe 'NativeMSIXResolvedCommandIdentity' @('CNMON_NATIVE_MSIX_PATH disabled=command-unresolved spellings=cn-mon.exe,cn-mon')
        }
        finally { $env:PATH = $originalPath }
        $env:CNMON_MSIX_RESOLUTION_MODE = ''
        Set-CnMonMsixGate 'conflicts' 'incomplete'
        Write-CnMonMsixStatus 'AppExecutionAlias Settings toggle was not exercised; PATH conflict checks are supplemental only'

        $script:CurrentGate = 'upgrade'
        $upgraded = Assert-CnMonMsixInstalledPackage $receipt $identity
        if ([string]$upgraded.PackageFullName -cne [string]$installed.PackageFullName) {
            throw 'candidate upgrade registration changed unexpectedly'
        }
        Set-CnMonMsixGate 'upgrade' 'pass'

        $retained = @{}
        foreach ($path in $targets.All) {
            if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
                throw 'qualification did not create every expected profile and fixture witness before repair'
            }
            $retained[$path] = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
        }

        Invoke-CnMonMsixDesktopSurfaceProbe -Identity $identity
        $script:CurrentGate = 'repair'
        $repair = Get-Command Repair-AppxPackage -ErrorAction SilentlyContinue
        if ($null -eq $repair) {
            Set-CnMonMsixGate 'repair' 'incomplete'
        }
        else {
            $null = & $repair -Package $installed.PackageFullName -ErrorAction Stop
            $null = Assert-CnMonMsixInstalledPackage $receipt $identity
            $script:CurrentGate = 'repair'
            Assert-CnMonMsixRetainedFiles $retained
            $env:CNMON_MSIX_APPDATA_PHASE = 'repair-readback'
            Invoke-CnMonMsixNativeTests 'appdata-repair-readback' $testExe 'NativeMSIXAppDataLifecycle' @('CNMON_NATIVE_MSIX_APPDATA phase=repair-readback create=preserved readback=pass external=absent localcache=present') | Out-Null
            Assert-CnMonMsixRetainedFiles $appDataRetained
            $env:CNMON_MSIX_APPDATA_PHASE = ''
            Set-CnMonMsixGate 'repair' 'pass'
        }

        $script:CurrentGate = 'uninstall_reinstall'
        $null = Remove-AppxPackage -Package $installed.PackageFullName -ErrorAction Stop
        Remove-CnMonMsixOwnedPackages $script:OwnedPackageSpecs
        if ($null -ne (Get-CnMonMsixPackage $identity)) { throw 'candidate uninstall left a registered package' }
        $remainingAppData = @($appDataBackingTargets | Where-Object { Test-Path -LiteralPath $_ -PathType Leaf })
        $appDataDeadline = [DateTime]::UtcNow.AddSeconds(15)
        while ($remainingAppData.Count -gt 0 -and [DateTime]::UtcNow -lt $appDataDeadline) {
            Start-Sleep -Milliseconds 200
            $remainingAppData = @($appDataBackingTargets | Where-Object { Test-Path -LiteralPath $_ -PathType Leaf })
        }
        if ($remainingAppData.Count -gt 0 -or @($appDataLogicalTargets | Where-Object { Test-Path -LiteralPath $_ -PathType Leaf }).Count -gt 0) {
            throw 'AppData outputs remained after package uninstall'
        }
        Write-CnMonMsixStatus 'AppData outputs were removed on uninstall'
        foreach ($path in @($appDataLogicalTargets) + @($appDataBackingTargets)) {
            if (Test-Path -LiteralPath $path -PathType Leaf) { throw 'AppData reinstall preflight found pre-existing output' }
        }
        Assert-CnMonMsixExpectedNativeRejection 'uninstalled-alias' $testExe 'NativeMSIXInstalledAlias'
        Add-CnMonMsixOwnedPackage $identity ([string]$receipt.package_version) $PackagePath $script:OwnedPackageSpecs
        $reinstalled = Assert-CnMonMsixInstalledPackage $receipt $identity
        if ($null -eq $reinstalled) { throw 'candidate reinstall failed' }
        $script:CurrentGate = 'uninstall_reinstall'
        $env:CNMON_MSIX_APPDATA_PHASE = 'reinstall-fresh'
        Invoke-CnMonMsixNativeTests 'appdata-reinstall-fresh' $testExe 'NativeMSIXAppDataLifecycle' @('CNMON_NATIVE_MSIX_APPDATA phase=reinstall-fresh create=pass readback=pass external=absent localcache=present') | Out-Null
        $env:CNMON_MSIX_APPDATA_PHASE = ''
        $null = Invoke-CnMonMsixNativeTests 'reinstalled-alias' $testExe 'TestNativeMSIXInstalledAlias' @(
            'CNMON_NATIVE_MSIX_ALIAS package-identity=verified ordinary-token=medium expected-sid=verified version=exact',
            'CNMON_NATIVE_MSIX_CHILD pid='
        )
        Assert-CnMonMsixRetainedFiles $retained
        Set-CnMonMsixGate 'uninstall_reinstall' 'pass'
        Set-CnMonMsixGate 'fail_injection' 'incomplete'
        Set-CnMonMsixGate 'wack' 'incomplete'
    }
    catch {
        if ($null -eq $script:FailureSourceOperation) { $script:FailureSourceOperation = $script:CurrentSourceOperation }
        if ($script:InjectedFailure -and $FailAfterInstall) {
            Set-CnMonMsixGate 'fail_injection' 'pass'
            $script:Failure = $true
        }
        else {
            Set-CnMonMsixCaughtGateFailure -Name $script:CurrentGate
            $script:Failure = $true
        }
        Save-CnMonMsixQualificationFailureEvidence -Mode $Mode -Gate $script:CurrentGate -Operation $script:FailureSourceOperation -ArtifactDirectory $ArtifactDirectory -Identity $script:DiagnosticIdentity -ErrorRecord $_ -PrivateOnly:($script:InjectedFailure -and $FailAfterInstall)
    }
    finally {
        $script:CurrentGate = 'cleanup'
        $script:CurrentSourceOperation = 'cleanup'
        $cleanupFailed = $false
        try { Remove-CnMonMsixOwnedPackages $script:OwnedPackageSpecs } catch { $cleanupFailed = $true }
        if ($script:OwnedProfileFiles) {
            foreach ($path in $script:OwnedProfileFiles) {
                if (Test-Path -LiteralPath $path -PathType Leaf) {
                    try { Remove-Item -LiteralPath $path -Force -ErrorAction Stop } catch { $cleanupFailed = $true }
                }
            }
        }
        if ($cleanupFailed) { Set-CnMonMsixGate 'cleanup' 'fail' } else { Set-CnMonMsixGate 'cleanup' 'pass' }
        if ($script:PrivateRoot -and (Test-Path -LiteralPath $script:PrivateRoot)) {
            Remove-Item -LiteralPath $script:PrivateRoot -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
}

function Invoke-CnMonMsixHostedMode {
    $script:CurrentGate = 'account'
    $script:CurrentSourceOperation = 'hosted_preflight'
    Assert-CnMonMsixWindows
    if ($env:GITHUB_ACTIONS -cne 'true' -or [string]::IsNullOrWhiteSpace($env:ImageOS) -or
        $env:ImageOS -notmatch '^win' -or -not (Test-Path -LiteralPath $env:RUNNER_TEMP -PathType Container)) {
        Set-CnMonMsixGate 'account' 'incomplete'
        throw 'Hosted mode is restricted to a fresh GitHub-hosted Windows runner'
    }
    $runnerIdentity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $runnerPrincipal = [Security.Principal.WindowsPrincipal]::new($runnerIdentity)
    if (-not $runnerPrincipal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        Set-CnMonMsixGate 'account' 'incomplete'
        throw 'Hosted mode requires the runner administrator context for temporary setup'
    }
    if ($FailAfterInstall) { throw 'failure injection is only used by the hosted orchestrator' }
    $script:PrivateRoot = Join-Path $env:RUNNER_TEMP ('cn-mon-msix-private-' + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $script:PrivateRoot | Out-Null
    $script:CurrentSourceOperation = 'identity_read'
    $identity = Read-CnMonMsixIdentity
    $script:DiagnosticIdentity = $identity
    $script:IdentityName = [string]$identity.Name
    $script:CurrentSourceOperation = 'receipt_validation'
    $receipt = Get-CnMonMsixReceipt $ReceiptPath
    $null = Assert-CnMonMsixReceipt $receipt $PackagePath 'unsigned' -RequireCandidate

    $makeappx = Join-Path $SdkBinDirectory 'makeappx.exe'
    if (-not (Test-Path -LiteralPath $makeappx -PathType Leaf)) {
        $script:CurrentGate = 'package_hash'
        Set-CnMonMsixGate 'package_hash' 'incomplete'
        throw 'MakeAppx is unavailable'
    }
    $unpacked = Join-Path $script:PrivateRoot 'unsigned-unpacked'
    New-Item -ItemType Directory -Path $unpacked | Out-Null
    Invoke-CnMonMsixTool $makeappx @('unpack', '/p', $PackagePath, '/d', $unpacked, '/no') (Join-Path $script:PrivateRoot 'unsigned-unpack.log')
    Assert-CnMonMsixPackagePayload $unpacked $receipt.files

    $candidate = Get-CnMonMsixSignedCandidatePath -Directory $ArtifactDirectory -PackageVersion ([string]$receipt.package_version)
    $script:CandidateOutputPath = $candidate
    $signedReceiptPath = Join-Path $ArtifactDirectory 'msix-signed-receipt.json'
    if ((Test-Path -LiteralPath $candidate) -or (Test-Path -LiteralPath $signedReceiptPath)) {
        throw 'signed candidate output already exists'
    }
    $script:CreatedCandidate = $true
    Copy-Item -LiteralPath $PackagePath -Destination $candidate
    $script:CurrentGate = 'signature'
    $script:CurrentDiagnosticPhase = 'candidate-setup'
    $script:CurrentSourceOperation = 'signature_setup'
    $script:TrustPresenceChecked = $false
    $script:TrustedCertificatePresent = $false
    $cert = New-SelfSignedCertificate -Type CodeSigningCert -Subject $identity.Publisher -CertStoreLocation Cert:\LocalMachine\My -KeyExportPolicy Exportable -KeyAlgorithm RSA -KeyLength 2048 -HashAlgorithm SHA256 -NotAfter (Get-Date).AddHours(6) -ErrorAction Stop
    $script:CreatedCertificate = $cert.Thumbprint
    $publicCert = Join-Path $script:PrivateRoot 'msix-test-signing.cer'
    $null = Export-Certificate -Cert $cert -FilePath $publicCert -Type CERT -Force -ErrorAction Stop
    $trustedBefore = @(Get-ChildItem Cert:\LocalMachine\TrustedPeople -ErrorAction SilentlyContinue | Where-Object { $_.Thumbprint -eq $cert.Thumbprint })
    if ($trustedBefore.Count -ne 0) { throw 'new test certificate thumbprint already exists in TrustedPeople' }
    $null = Import-Certificate -FilePath $publicCert -CertStoreLocation Cert:\LocalMachine\TrustedPeople -ErrorAction Stop
    $script:CreatedTrustedCertificate = $true

    $signtool = Join-Path $SdkBinDirectory 'signtool.exe'
    $script:LastSignatureLogPath = Join-Path $script:PrivateRoot 'candidate-sign.log'
    Invoke-CnMonMsixTool $signtool @('sign', '/fd', 'SHA256', '/s', 'My', '/sm', '/sha1', $cert.Thumbprint, $candidate) $script:LastSignatureLogPath
    $script:CurrentDiagnosticPhase = 'candidate-verify'
    $script:LastSignatureLogPath = Join-Path $script:PrivateRoot 'candidate-verify.log'
    Assert-CnMonMsixSignature $candidate $script:LastSignatureLogPath -Phase 'candidate-verify' -Identity $identity
    $script:CurrentDiagnosticPhase = 'candidate-post-verify'
    $script:CurrentDiagnosticOperation = 'signed_unpack_directory'
    $signedUnpacked = Join-Path $script:PrivateRoot 'signed-unpacked'
    New-Item -ItemType Directory -Path $signedUnpacked | Out-Null
    $script:CurrentDiagnosticOperation = 'signed_makeappx_unpack'
    Invoke-CnMonMsixTool $makeappx @('unpack', '/p', $candidate, '/d', $signedUnpacked, '/no') (Join-Path $script:PrivateRoot 'signed-unpack.log')
    $script:CurrentDiagnosticOperation = 'signed_payload_validation'
    $script:SignedCatalogFootprintPresent = $false
    Assert-CnMonMsixPackagePayload $signedUnpacked $receipt.files -Signed
    $script:CurrentDiagnosticOperation = 'signed_package_hash'
    $signedHash = (Get-FileHash -LiteralPath $candidate -Algorithm SHA256).Hash.ToLowerInvariant()
    $script:CurrentDiagnosticOperation = 'signed_receipt_construction'
    $signedReceipt = [ordered]@{}
    foreach ($key in $receipt.Keys) { $signedReceipt[$key] = $receipt[$key] }
    $signedReceipt.package_sha256 = $signedHash
    $signedReceipt.signing = [ordered]@{
        status = 'test'
        certificate_thumbprint = $cert.Thumbprint
        unsigned_package_sha256 = [string]$receipt.package_sha256
        signed_package_sha256 = $signedHash
    }
    $script:CurrentDiagnosticOperation = 'signed_receipt_serialization'
    $signedReceiptJson = ConvertTo-Json -InputObject $signedReceipt -Depth 12
    $script:CurrentDiagnosticOperation = 'signed_receipt_write'
    [IO.File]::WriteAllText($signedReceiptPath, $signedReceiptJson + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
    $script:Result.evidence = Get-CnMonMsixResultEvidence -PackagePath $candidate -ReceiptPath $signedReceiptPath -Receipt $signedReceipt -SourceRevision ([string]$signedReceipt.packaging_revision)
    $script:CreatedReceipt = $true
    $script:SignedOutputsReady = $true
    Set-CnMonMsixGate 'signature' 'pass'
    $script:CurrentDiagnosticPhase = 'other'
    $script:CurrentDiagnosticOperation = 'none'

    $fixturePath = Join-Path $ArtifactDirectory '.cn-mon-1.2.999.0-fixture.msix'
    $fixtureReceiptPath = Join-Path $ArtifactDirectory '.cn-mon-1.2.999.0-fixture-receipt.json'
    $fixtureHistory = Join-Path $ArtifactDirectory '.msix-fixture-history.db'
    $fixtureLog = Join-Path $ArtifactDirectory '.msix-fixture.log'
    if ((Test-Path -LiteralPath $fixturePath) -or (Test-Path -LiteralPath $fixtureReceiptPath) -or (Test-Path -LiteralPath $fixtureHistory) -or (Test-Path -LiteralPath $fixtureLog)) {
        throw 'private fixture output already exists'
    }
    $script:CreatedFixture = $true
    $script:CreatedFixtureFiles = @($fixturePath, $fixtureReceiptPath, $fixtureHistory, $fixtureLog)
    New-CnMonMsixFixture $PackagePath $receipt $identity $fixturePath $fixtureReceiptPath $fixtureHistory $fixtureLog

    $testExe = Join-Path $ArtifactDirectory 'msix-tests.exe'
    $oldGo = $env:CGO_ENABLED
    $env:CGO_ENABLED = '0'
    Push-Location $repositoryRoot
    try {
        & go test -tags native -c -o $testExe ./cmd/cn-mon
        if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $testExe -PathType Leaf)) {
            Set-CnMonMsixGate 'native_cli' 'fail'
            throw 'native Windows qualification tests could not be compiled'
        }
    }
    finally {
        Pop-Location
        $env:CGO_ENABLED = $oldGo
    }

    $script:CurrentGate = 'fail_injection'
    $script:CurrentSourceOperation = 'temporary_user_account_create'
    $failureUser = New-CnMonMsixTemporaryUser
    $script:CurrentSourceOperation = 'user_run_process_start'
    $failureResult = Invoke-CnMonMsixUserRun $failureUser $candidate $signedReceiptPath $fixturePath -InjectFailure
    $failureJson = Get-Content -LiteralPath $failureResult.ResultPath -Raw | ConvertFrom-Json -AsHashtable
    Assert-CnMonMsixUserResultBinding -Result $failureJson -ExpectedEvidence $script:Result.evidence
    if ($failureResult.ExitCode -eq 0 -or $failureJson.gates.fail_injection -cne 'pass' -or $failureJson.gates.cleanup -cne 'pass') {
        throw 'post-install failure injection did not prove nonzero exit and cleanup'
    }
    Set-CnMonMsixGate 'fail_injection' 'pass'
    Remove-CnMonMsixTemporaryUser $failureUser.Name $failureUser.SID $script:IdentityName
    $script:CreatedUser = $null
    $script:CreatedUserSid = $null
    $script:CreatedProfile = $null

    $script:CurrentSourceOperation = 'temporary_user_account_create'
    $user = New-CnMonMsixTemporaryUser
    $script:CurrentSourceOperation = 'user_run_process_start'
    $userResult = Invoke-CnMonMsixUserRun $user $candidate $signedReceiptPath $fixturePath
    $userJson = Get-Content -LiteralPath $userResult.ResultPath -Raw | ConvertFrom-Json -AsHashtable
    Assert-CnMonMsixUserResultBinding -Result $userJson -ExpectedEvidence $script:Result.evidence
    foreach ($key in $userJson.gates.Keys) {
        if ($key -cne 'fail_injection') { $script:Result.gates[$key] = $userJson.gates[$key] }
    }
    if ($userResult.ExitCode -ne 0) {
        $script:Failure = $true
        Remove-CnMonMsixTemporaryUser $user.Name $user.SID $script:IdentityName
        $script:CreatedUser = $null
        $script:CreatedUserSid = $null
        $script:CreatedProfile = $null
        return
    }
    Remove-CnMonMsixTemporaryUser $user.Name $user.SID $script:IdentityName
    $script:CreatedUser = $null
    $script:CreatedUserSid = $null
    $script:CreatedProfile = $null
    Set-CnMonMsixGate 'account' 'pass'
    Set-CnMonMsixGate 'fail_injection' 'pass'
    Set-CnMonMsixGate 'wack' 'incomplete'
}

function Invoke-CnMonMsixCertifyMode {
    $script:CurrentGate = 'package_hash'
    $script:CurrentSourceOperation = 'receipt_validation'
    try {
        $sourceRevision = Get-CnMonMsixSourceRevision
        $hostedResultsPath = if ($HostedResultsPath) { $HostedResultsPath } else { Join-Path $ArtifactDirectory 'msix-native-results.json' }
        $script:HostedResultSnapshot = Read-CnMonMsixHostedResultSnapshot -ResultsPath $hostedResultsPath -PackagePath $PackagePath -ReceiptPath $ReceiptPath -SourceRevision $sourceRevision
        $script:Result.evidence = $script:HostedResultSnapshot.Results.evidence
    }
    catch {
        $script:AllowResultWrite = $false
        throw
    }
    Assert-CnMonMsixWindows
    $identity = Read-CnMonMsixIdentity
    $script:IdentityName = [string]$identity.Name
    $receipt = Get-CnMonMsixReceipt $ReceiptPath
    $null = Assert-CnMonMsixReceipt $receipt $PackagePath 'test' -RequireCandidate
    $tempRoot = if ($env:RUNNER_TEMP -and (Test-Path -LiteralPath $env:RUNNER_TEMP -PathType Container)) { $env:RUNNER_TEMP } else { $env:TEMP }
    if (-not $tempRoot -or -not (Test-Path -LiteralPath $tempRoot -PathType Container)) {
        $script:CurrentGate = 'package_hash'
        Set-CnMonMsixGate 'package_hash' 'incomplete'
        throw 'private temporary storage is unavailable'
    }
    $script:PrivateRoot = Join-Path $tempRoot ('cn-mon-msix-certify-' + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $script:PrivateRoot | Out-Null
    $makeappx = Join-Path $SdkBinDirectory 'makeappx.exe'
    if (-not (Test-Path -LiteralPath $makeappx -PathType Leaf)) {
        $script:CurrentGate = 'package_hash'
        Set-CnMonMsixGate 'package_hash' 'incomplete'
        throw 'MakeAppx is unavailable'
    }
    $unpacked = Join-Path $script:PrivateRoot 'signed-unpacked'
    New-Item -ItemType Directory -Path $unpacked | Out-Null
    Invoke-CnMonMsixTool $makeappx @('unpack', '/p', $PackagePath, '/d', $unpacked, '/no') (Join-Path $script:PrivateRoot 'signed-unpack.log')
    Assert-CnMonMsixPackagePayload $unpacked $receipt.files -Signed
    $script:CurrentGate = 'package_hash'
    $certifyIdentity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $certifyPrincipal = [Security.Principal.WindowsPrincipal]::new($certifyIdentity)
    if (-not $certifyPrincipal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        $script:CurrentGate = 'wack'
        Set-CnMonMsixGate 'wack' 'incomplete'
        throw 'Certify mode requires an elevated administrator session'
    }
    $script:CurrentGate = 'wack'
    $appcert = 'C:\Program Files (x86)\Windows Kits\10\App Certification Kit\appcert.exe'
    if (-not (Test-Path -LiteralPath $appcert -PathType Leaf)) {
        Set-CnMonMsixGate 'wack' 'incomplete'
        throw 'App Certification Kit is unavailable'
    }
    $sessionType = 'CnMonMsixActiveSession'
    if ($null -eq ($sessionType -as [type])) {
        Add-Type -TypeDefinition @'
using System.Runtime.InteropServices;
public static class CnMonMsixActiveSession
{
    [DllImport("kernel32.dll")]
    public static extern uint WTSGetActiveConsoleSessionId();
}
'@ -ErrorAction Stop
    }
    $session = [Diagnostics.Process]::GetCurrentProcess().SessionId
    [uint32]$active = [CnMonMsixActiveSession]::WTSGetActiveConsoleSessionId()
    if ($session -eq 0 -or $active -eq [uint32]::MaxValue -or $session -ne $active) {
        Set-CnMonMsixGate 'wack' 'incomplete'
        throw 'WACK requires an active interactive user session'
    }
    $thumb = [string]$receipt.signing.certificate_thumbprint
    $signature = Get-AuthenticodeSignature -LiteralPath $PackagePath
    if ($null -eq $signature.SignerCertificate -or $signature.SignerCertificate.Thumbprint -cne $thumb) {
        Set-CnMonMsixGate 'signature' 'fail'
        throw 'signed package signer does not match the signed receipt'
    }
    $publicCertPath = Join-Path $script:PrivateRoot 'msix-test-signing.cer'
    $trusted = @(Get-ChildItem Cert:\LocalMachine\TrustedPeople, Cert:\CurrentUser\TrustedPeople -ErrorAction SilentlyContinue | Where-Object { $_.Thumbprint -eq $thumb })
    $freshHosted = $env:GITHUB_ACTIONS -ceq 'true' -and $env:ImageOS -match '^win' -and
        [string]::IsNullOrWhiteSpace($env:RUNNER_TEMP) -eq $false -and
        (Test-Path -LiteralPath $env:RUNNER_TEMP -PathType Container)
    $trustAction = Get-CnMonMsixTrustAction ($trusted.Count -gt 0) $freshHosted
    if ($trustAction -eq 'incomplete') {
        Set-CnMonMsixGate 'wack' 'incomplete'
        throw 'signed package trust is not provisioned for this certification host'
    }
    if ($trustAction -eq 'import') {
        $null = Export-Certificate -Cert $signature.SignerCertificate -FilePath $publicCertPath -Type CERT -Force -ErrorAction Stop
        $exportedCert = [Security.Cryptography.X509Certificates.X509Certificate2]::new($publicCertPath)
        try {
            if ($exportedCert.Thumbprint -cne $thumb) {
                Set-CnMonMsixGate 'signature' 'fail'
                throw 'exported signer certificate does not match the signed receipt'
            }
        }
        finally { $exportedCert.Dispose() }
        $existingMachine = @(Get-ChildItem Cert:\LocalMachine\TrustedPeople -ErrorAction SilentlyContinue | Where-Object { $_.Thumbprint -eq $thumb })
        if ($existingMachine.Count -ne 0) {
            Set-CnMonMsixGate 'wack' 'fail'
            throw 'signer certificate appeared in TrustedPeople during certification setup'
        }
        $null = Import-Certificate -FilePath $publicCertPath -CertStoreLocation Cert:\LocalMachine\TrustedPeople -ErrorAction Stop
        $script:CreatedTrustedCertificate = $true
        $script:CreatedCertificate = $thumb
    }
    $script:CurrentGate = 'signature'
    Assert-CnMonMsixSignature $PackagePath (Join-Path $script:PrivateRoot 'certify-signature.log') -Phase 'certify-verify' -Identity $identity
    $script:CurrentGate = 'wack'
    $helpLog = Join-Path $ArtifactDirectory 'msix-wack-help.log'
    $report = Join-Path $ArtifactDirectory 'wack.xml'
    $appcertToolVersion = $null
    try { $appcertToolVersion = ConvertTo-CnMonMsixNumericToolVersion ([Diagnostics.FileVersionInfo]::GetVersionInfo($appcert)) } catch { }
    if (Test-Path -LiteralPath $report -PathType Leaf) {
        Set-CnMonMsixGate 'wack' 'incomplete'
        throw 'WACK report destination already exists'
    }
    & $appcert '/?' *> $helpLog
    $helpExitCode = [int]$LASTEXITCODE
    $resetExitCode = $null
    try {
        & $appcert reset *> (Join-Path $ArtifactDirectory 'msix-wack-reset.log')
        $resetExitCode = [int]$LASTEXITCODE
    }
    catch {
        try { Save-CnMonMsixWackStructureDiagnostic -ReportPath $report -ExitCode $null -ToolVersion $appcertToolVersion -Identity $identity -ExpectedVersion ([string]$receipt.package_version) -Phase reset } catch { }
        throw
    }
    if ($resetExitCode -ne 0) {
        try { Save-CnMonMsixWackStructureDiagnostic -ReportPath $report -ExitCode $resetExitCode -ToolVersion $appcertToolVersion -Identity $identity -ExpectedVersion ([string]$receipt.package_version) -Phase reset } catch { }
        Set-CnMonMsixGate 'wack' 'fail'
        throw 'WACK reset failed'
    }
    $wackExit = $null
    try {
        & $appcert test -appxpackagepath $PackagePath -reportoutputpath $report *> (Join-Path $ArtifactDirectory 'msix-wack.log')
        $wackExit = [int]$LASTEXITCODE
    } catch { }
    $reportPresent = Test-Path -LiteralPath $report -PathType Leaf
    $diagnosticPhase = if ($reportPresent) { 'test' } else { 'report_missing' }
    try { Save-CnMonMsixWackStructureDiagnostic -ReportPath $report -ExitCode $wackExit -ToolVersion $appcertToolVersion -Identity $identity -ExpectedVersion ([string]$receipt.package_version) -Phase $diagnosticPhase } catch { }
    if ($null -eq $wackExit) { throw 'WACK test invocation failed' }
    $wackStatus = Get-CnMonMsixWackStatus $report $wackExit $identity.Name $receipt.package_version
    Set-CnMonMsixGate 'wack' $wackStatus
    if ($wackStatus -ne 'pass') {
        throw 'WACK report did not establish a complete successful certification run'
    }
}

try {
    if (-not (Test-Path -LiteralPath $ArtifactDirectory -PathType Container)) {
        New-Item -ItemType Directory -Path $ArtifactDirectory | Out-Null
    }
    $ArtifactDirectory = [IO.Path]::GetFullPath($ArtifactDirectory)
    $script:CurrentGate = 'identity'
    switch ($Mode) {
        'Hosted' { Invoke-CnMonMsixHostedMode }
        'User' { Invoke-CnMonMsixUserMode }
        'Certify' { Invoke-CnMonMsixCertifyMode }
    }
}
catch {
    $caughtError = $_
    if ($null -eq $script:FailureSourceOperation) { $script:FailureSourceOperation = $script:CurrentSourceOperation }
    if ($Mode -in @('Hosted', 'User') -and $script:CurrentGate -ceq 'signature' -and $null -ne $script:DiagnosticIdentity) {
        try {
            Save-CnMonMsixSignatureFailureEvidence -Mode $Mode -ArtifactDirectory $ArtifactDirectory -Phase $script:CurrentDiagnosticPhase -Identity $script:DiagnosticIdentity -Exception $caughtError.Exception -TrustPresenceChecked:$script:TrustPresenceChecked -TrustedCertificatePresent:$script:TrustedCertificatePresent
        } catch { }
    }
    if ($Mode -in @('Hosted', 'User')) {
        $failureIdentity = $script:DiagnosticIdentity
        if ($null -eq $failureIdentity) {
            try { $failureIdentity = Read-CnMonMsixIdentity } catch { }
        }
        Save-CnMonMsixQualificationFailureEvidence -Mode $Mode -Gate $script:CurrentGate -Operation $script:FailureSourceOperation -ArtifactDirectory $ArtifactDirectory -Identity $failureIdentity -ErrorRecord $caughtError
    }
    if (-not $script:InjectedFailure) {
        Set-CnMonMsixCaughtGateFailure -Name $script:CurrentGate
        $script:Failure = $true
    }
}
finally {
    if ($Mode -eq 'Hosted') {
        $script:CurrentSourceOperation = 'cleanup'
        if ($script:CreatedUser) {
            try {
                Remove-CnMonMsixTemporaryUser $script:CreatedUser $script:CreatedUserSid $script:IdentityName
                $script:CreatedUser = $null
                $script:CreatedUserSid = $null
                $script:CreatedProfile = $null
            } catch { $script:Failure = $true; Set-CnMonMsixGate 'cleanup' 'fail' }
        }
        if ($script:CreatedCertificate) {
            try { Remove-CnMonMsixOwnedCertificate $script:CreatedCertificate $script:CreatedTrustedCertificate $true } catch { $script:Failure = $true; Set-CnMonMsixGate 'cleanup' 'fail' }
        }
        if ($script:CreatedFixture) {
            foreach ($path in $script:CreatedFixtureFiles) {
                if (Test-Path -LiteralPath $path -PathType Leaf) { Remove-Item -LiteralPath $path -Force -ErrorAction SilentlyContinue }
            }
        }
        if ($script:CreatedCandidate -and -not $script:SignedOutputsReady -and $script:CandidateOutputPath -and (Test-Path -LiteralPath $script:CandidateOutputPath)) {
            Remove-Item -LiteralPath $script:CandidateOutputPath -Force -ErrorAction SilentlyContinue
        }
        if ($script:CreatedReceipt -and -not $script:SignedOutputsReady -and (Test-Path -LiteralPath (Join-Path $ArtifactDirectory 'msix-signed-receipt.json'))) {
            Remove-Item -LiteralPath (Join-Path $ArtifactDirectory 'msix-signed-receipt.json') -Force -ErrorAction SilentlyContinue
        }
        if ($script:PrivateRoot -and (Test-Path -LiteralPath $script:PrivateRoot)) {
            Remove-Item -LiteralPath $script:PrivateRoot -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
    if ($Mode -eq 'Certify' -and $script:CreatedTrustedCertificate -and $script:CreatedCertificate) {
        try { Remove-CnMonMsixOwnedCertificate $script:CreatedCertificate $true $false } catch { $script:Failure = $true; Set-CnMonMsixGate 'cleanup' 'fail' }
    }
    if ($Mode -eq 'Certify' -and $script:Result.gates['cleanup'] -eq 'incomplete') {
        Set-CnMonMsixGate 'cleanup' 'pass'
    }
    if ($Mode -in @('User', 'Certify') -and $script:PrivateRoot -and (Test-Path -LiteralPath $script:PrivateRoot)) {
        Remove-Item -LiteralPath $script:PrivateRoot -Recurse -Force -ErrorAction SilentlyContinue
    }
    if ($script:OriginalArtifactAcl) {
        try {
            $restoreAcl = [Security.AccessControl.DirectorySecurity]::new()
            $restoreAcl.SetSecurityDescriptorBinaryForm([byte[]]$script:OriginalArtifactAcl)
            Set-Acl -LiteralPath $ArtifactDirectory -AclObject $restoreAcl
        }
        catch { $script:Failure = $true; Set-CnMonMsixGate 'cleanup' 'fail' }
    }
    if ($Mode -eq 'Certify' -and $null -ne $script:HostedResultSnapshot) {
        $script:Result.gates = Merge-CnMonMsixResultGates $script:HostedResultSnapshot.Results.gates $script:GateUpdates
        $script:Result.evidence = $script:HostedResultSnapshot.Results.evidence
        $script:Result.mode = 'Certify'
    }
    Set-CnMonMsixOverall $Mode
    if ($script:AllowResultWrite) {
        try { Write-CnMonMsixResult $ArtifactDirectory } catch { $script:Failure = $true }
    }
    if ($Mode -eq 'Hosted' -and $script:Result.gates['wack'] -eq 'incomplete' -and $script:Result.overall -eq 'pass') {
        Write-CnMonMsixStatus 'hosted qualification passed; WACK remains a separate Certify gate'
    }
    else {
        Write-CnMonMsixStatus $script:Result.overall
    }
}

if ($script:Failure -or $script:Result.overall -ne 'pass' -or ($Mode -eq 'User' -and $FailAfterInstall)) {
    exit 1
}
exit 0
