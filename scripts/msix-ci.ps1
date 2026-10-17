# SPDX-License-Identifier: Apache-2.0
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('Initialize', 'DownloadRelease', 'Build', 'Hosted', 'Certify', 'Summary')]
    [string]$Mode,

    [string]$PackageDirectory = 'package',

    [string]$ReleaseMetadataPath = $env:CNMON_RELEASE_METADATA_PATH
)

$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
$repositoryRoot = Split-Path -Parent $PSScriptRoot
$jobDirectory = Join-Path $env:RUNNER_TEMP 'cn-mon-msix-job'
$sdkBinDirectory = 'C:\Program Files (x86)\Windows Kits\10\bin\10.0.26100.0\x64'
$allowedStepStatuses = @('success', 'failure', 'skipped', 'cancelled', 'incomplete', 'not_applicable')
$allowedGateStatuses = @('pass', 'fail', 'incomplete')
$modulePath = Join-Path $PSScriptRoot 'msix-package.psm1'
Import-Module $modulePath -Force -ErrorAction Stop
$gateNames = @(
    'identity', 'package_hash', 'signature', 'account', 'package_registration',
    'exact_identity', 'installed_binary_hash', 'alias_identity', 'native_cli',
    'icmp', 'history_config', 'shell_aliases', 'upgrade', 'repair',
    'uninstall_reinstall', 'conflicts', 'fail_injection', 'cleanup', 'wack'
)

function Get-ReleaseMetadata {
    $releasePath = if ([string]::IsNullOrWhiteSpace($ReleaseMetadataPath)) {
        Join-Path $repositoryRoot 'packaging/windows/release.json'
    } else { [IO.Path]::GetFullPath($ReleaseMetadataPath) }
    try {
        $release = Get-Content -LiteralPath $releasePath -Raw -ErrorAction Stop | ConvertFrom-Json -ErrorAction Stop
        $null = Assert-CnMonMsixReleaseMetadata -Release $release
        if ($release.packaging_revision -and [string]$release.packaging_revision -cne (Get-SourceRevision)) {
            throw 'source revision mismatch'
        }
        return $release
    } catch { throw 'release metadata could not be read or validated' }
}

function Get-MsixPackageFileName([string]$PackageVersion, [switch]$Signed) {
    if ($PackageVersion -cnotmatch '^\d+\.\d+\.\d+\.\d+$') { throw 'release package version is invalid' }
    $suffix = if ($Signed) { '-signed' } else { '' }
    return "cn-mon_${PackageVersion}_x64${suffix}.msix"
}

function Get-SourceRevision {
    $output = @(& git -C $repositoryRoot rev-parse --verify HEAD 2>&1)
    if ($LASTEXITCODE -ne 0 -or $output.Count -ne 1) {
        throw 'checked-out source revision could not be verified'
    }
    return ([string]$output[0]).Trim()
}

function Get-JobDirectory {
    New-Item -ItemType Directory -Force -Path $jobDirectory | Out-Null
    return $jobDirectory
}

function Write-CnMonMsixSignatureDiagnosticStatus([string]$Path) {
    if ($env:GITHUB_ACTIONS -cne 'true' -or $env:GITHUB_EVENT_NAME -cne 'pull_request' -or
        $env:GITHUB_REPOSITORY -cne 'evdanil/cn-mon') {
        return
    }
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        Write-Host 'MSIX signature diagnostic status: unavailable'
        return
    }
    try {
        $status = Get-Content -LiteralPath $Path -Raw | ConvertFrom-Json -ErrorAction Stop
        $guardNames = @(
            'mode_hosted', 'mode_user', 'github_actions', 'event_pull_request', 'repository_expected',
            'image_os_windows', 'identity_name_env_matches', 'identity_publisher_env_matches',
            'publisher_display_name_env_matches', 'expected_pfn_env_matches',
            'identity_name_matches', 'identity_publisher_matches',
            'publisher_display_name_matches', 'expected_pfn_matches', 'all'
        )
        $booleanNames = @($guardNames | ForEach-Object { "guard_$_" }) + @(
            'sdk_exit_captured', 'private_log_retained', 'trust_presence_checked',
            'trusted_certificate_present', 'catalog_footprint_present', 'public_diagnostic_written'
        )
        $expectedNames = @('schema_version', 'phase', 'operation', 'failure_kind', 'exception_kind', 'error_category', 'tool_name', 'save_status', 'save_error_kind') + $booleanNames
        $actualNames = @($status.PSObject.Properties.Name | Sort-Object)
        $expectedNames = @($expectedNames | Sort-Object)
        $operations = @('none', 'signed_unpack_directory', 'signed_makeappx_unpack', 'signed_payload_validation', 'signed_package_hash', 'signed_receipt_construction', 'signed_receipt_serialization', 'signed_receipt_write')
        $errorCategories = @('sdk_nonzero', 'sdk_invocation_error', 'trusted_certificate_missing', 'parameter_binding', 'payload_root_missing', 'payload_expected_map_invalid', 'payload_reparse_point', 'payload_missing', 'payload_hash_mismatch', 'payload_unexpected', 'payload_catalog_missing', 'payload_catalog_invalid', 'invalid_operation', 'io', 'access_denied', 'path', 'argument', 'security', 'stage_exception')
        if (($actualNames -join "`n") -cne ($expectedNames -join "`n") -or
            $status.schema_version -ne 3 -or
            $status.phase -notin @('candidate-verify', 'user-trust-presence', 'user-verify', 'candidate-setup', 'candidate-post-verify', 'user-post-verify', 'other') -or
            $status.operation -notin $operations -or
            $status.failure_kind -notin @('sdk_nonzero', 'sdk_invocation_error', 'trusted_certificate_missing', 'stage_exception') -or
            $status.exception_kind -notin @('none', 'invalid_operation', 'io', 'access_denied', 'path', 'argument', 'security', 'other') -or
            $status.error_category -notin $errorCategories -or
            $status.tool_name -notin @('none', 'signtool', 'makeappx', 'certificate_store', 'other') -or
            $status.save_status -notin @('not_attempted', 'success', 'failure') -or
            $status.save_error_kind -notin @('none', 'io', 'access_denied', 'path', 'argument', 'other')) {
            Write-Host 'MSIX signature diagnostic status: invalid'
            return
        }
        foreach ($name in $booleanNames) {
            if ($status.$name -isnot [bool]) {
                Write-Host 'MSIX signature diagnostic status: invalid'
                return
            }
        }
        $phaseMatchesMode = switch ([string]$status.phase) {
            { $_ -in @('candidate-verify', 'candidate-setup', 'candidate-post-verify') } { [bool]$status.guard_mode_hosted; break }
            { $_ -in @('user-trust-presence', 'user-verify', 'user-post-verify') } { [bool]$status.guard_mode_user; break }
            default { [bool]($status.guard_mode_hosted -or $status.guard_mode_user) }
        }
        if ($status.guard_mode_hosted -eq $status.guard_mode_user -or -not $phaseMatchesMode -or
            ($status.operation -ne 'none' -and $status.phase -ne 'candidate-post-verify') -or
            (($status.failure_kind -eq 'sdk_nonzero') -ne $status.sdk_exit_captured) -or
            ($status.failure_kind -in @('sdk_nonzero', 'sdk_invocation_error') -and $status.error_category -ne $status.failure_kind) -or
            ($status.phase -in @('candidate-verify', 'user-verify') -and $status.tool_name -ne 'signtool') -or
            ($status.operation -eq 'signed_makeappx_unpack' -and $status.tool_name -notin @('none', 'makeappx')) -or
            ($status.operation -eq 'signed_makeappx_unpack' -and $status.failure_kind -eq 'sdk_nonzero' -and $status.tool_name -ne 'makeappx') -or
            ($status.failure_kind -eq 'sdk_invocation_error' -and ($status.sdk_exit_captured -or $status.phase -notin @('candidate-verify', 'user-verify'))) -or
            ($status.failure_kind -eq 'trusted_certificate_missing') -ne ($status.phase -eq 'user-trust-presence' -and $status.trust_presence_checked -and -not $status.trusted_certificate_present) -or
            ($status.failure_kind -eq 'trusted_certificate_missing' -and $status.error_category -ne 'trusted_certificate_missing') -or
            ($status.trusted_certificate_present -and -not $status.trust_presence_checked) -or
            ($status.save_status -eq 'failure') -ne ($status.save_error_kind -ne 'none') -or
            ($status.save_status -ne 'failure' -and $status.save_error_kind -ne 'none')) {
            Write-Host 'MSIX signature diagnostic status: invalid'
            return
        }
        $flags = foreach ($name in $booleanNames) {
            $value = if ($status.$name) { 'true' } else { 'false' }
            "$name=$value"
        }
        Write-Host ("MSIX signature diagnostic status: phase={0} operation={1} failure_kind={2} exception_kind={3} error_category={4} tool_name={5} {6} save_status={7} save_error_kind={8}" -f ($status.phase, $status.operation, $status.failure_kind, $status.exception_kind, $status.error_category, $status.tool_name, ($flags -join ' '), $status.save_status, $status.save_error_kind))
    }
    catch {
        Write-Host 'MSIX signature diagnostic status: invalid'
    }
}

function Assert-CandidateReceipt([string]$PackagePath, [string]$ReceiptPath) {
    $receipt = Get-Content -LiteralPath $ReceiptPath -Raw | ConvertFrom-Json
    $release = Get-ReleaseMetadata
    $sourceRevision = Get-SourceRevision
    $actualHash = (Get-FileHash -LiteralPath $PackagePath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($receipt.purpose -cne 'candidate' -or
        $receipt.package_version -cne $release.package_version -or
        $receipt.cli_version -cne $release.cli_version -or
        $receipt.cli_revision -cne $release.cli_revision -or
        $receipt.packaging_revision -cne $sourceRevision -or
        $receipt.archive_sha256 -cne $release.archive_sha256 -or
        $receipt.binary_sha256 -cne $release.binary_sha256 -or
        $receipt.name -cne $env:CNMON_MSIX_IDENTITY_NAME -or
        $receipt.publisher -cne $env:CNMON_MSIX_IDENTITY_PUBLISHER -or
        $receipt.publisher_display_name -cne $env:CNMON_MSIX_PUBLISHER_DISPLAY_NAME -or
        $receipt.expected_pfn -cne $env:CNMON_MSIX_EXPECTED_PFN -or
        $receipt.package_sha256 -cne $actualHash) {
        throw 'MSIX receipt failed identity or package hash validation'
    }
}

function Get-SignedEvidenceBinding([string]$PackagePath, [string]$ReceiptPath) {
    Assert-CandidateReceipt -PackagePath $PackagePath -ReceiptPath $ReceiptPath
    $receipt = Get-Content -LiteralPath $ReceiptPath -Raw -ErrorAction Stop | ConvertFrom-Json -ErrorAction Stop
    $packageHash = (Get-FileHash -LiteralPath $PackagePath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($receipt.signing.status -cne 'test' -or
        [string]$receipt.signing.signed_package_sha256 -cne $packageHash -or
        [string]$receipt.package_sha256 -cne $packageHash) {
        throw 'signed result evidence does not match the signed package'
    }
    return [ordered]@{
        signed_package_sha256 = $packageHash
        signed_receipt_sha256 = (Get-FileHash -LiteralPath $ReceiptPath -Algorithm SHA256).Hash.ToLowerInvariant()
        packaging_revision = [string]$receipt.packaging_revision
    }
}

function Assert-NativeResultEvidence($Actual, $Expected) {
    $expectedNames = @('packaging_revision', 'signed_package_sha256', 'signed_receipt_sha256')
    if ($null -eq $Actual -or $null -eq $Expected -or
        (@($Actual.PSObject.Properties.Name | Sort-Object) -join "`n") -cne ($expectedNames -join "`n") -or
        (@($Expected.Keys | Sort-Object) -join "`n") -cne ($expectedNames -join "`n") -or
        [string]$Actual.signed_package_sha256 -cnotmatch '^[0-9a-f]{64}$' -or
        [string]$Actual.signed_receipt_sha256 -cnotmatch '^[0-9a-f]{64}$' -or
        [string]$Actual.packaging_revision -cnotmatch '^[0-9a-f]{40}$' -or
        [string]$Actual.signed_package_sha256 -cne [string]$Expected.signed_package_sha256 -or
        [string]$Actual.signed_receipt_sha256 -cne [string]$Expected.signed_receipt_sha256 -or
        [string]$Actual.packaging_revision -cne [string]$Expected.packaging_revision) {
        throw 'native result evidence does not match signed package, receipt, and source revision'
    }
}

function Read-NativeResults(
    [string]$ResultsPath,
    [ValidateSet('Hosted', 'Certify')][string]$ExpectedMode,
    [string]$RawJson
) {
    $json = if ($PSBoundParameters.ContainsKey('RawJson')) { $RawJson } else { Get-Content -LiteralPath $ResultsPath -Raw -ErrorAction Stop }
    $results = ConvertFrom-Json -InputObject $json -ErrorAction Stop
    $propertyNames = @($results.PSObject.Properties.Name | Sort-Object)
    $expectedProperties = @('evidence', 'gates', 'mode', 'overall', 'schema_version')
    $expectedProperties = @($expectedProperties | Sort-Object)
    if (($propertyNames -join "`n") -cne ($expectedProperties -join "`n") -or
        $results.schema_version -isnot [long] -or $results.schema_version -ne 2 -or
        $results.mode -cne $ExpectedMode -or
        $results.overall -cnotin $allowedGateStatuses -or
        $null -eq $results.gates) {
        throw 'native qualification results have an invalid schema'
    }
    if ($null -ne $results.evidence) {
        $evidenceNames = @($results.evidence.PSObject.Properties.Name | Sort-Object)
        if (($evidenceNames -join "`n") -cne (@('packaging_revision', 'signed_package_sha256', 'signed_receipt_sha256') -join "`n") -or
            $results.evidence.signed_package_sha256 -isnot [string] -or
            $results.evidence.signed_receipt_sha256 -isnot [string] -or
            $results.evidence.packaging_revision -isnot [string] -or
            [string]$results.evidence.signed_package_sha256 -cnotmatch '^[0-9a-f]{64}$' -or
            [string]$results.evidence.signed_receipt_sha256 -cnotmatch '^[0-9a-f]{64}$' -or
            [string]$results.evidence.packaging_revision -cnotmatch '^[0-9a-f]{40}$') {
            throw 'native qualification results contain invalid evidence'
        }
    }

    $actualNames = @($results.gates.PSObject.Properties.Name | Sort-Object)
    $expectedNames = @($gateNames | Sort-Object)
    if (($actualNames -join "`n") -cne ($expectedNames -join "`n")) {
        throw 'native qualification results have an invalid gate set'
    }
    foreach ($name in $actualNames) {
        if ($results.gates.$name -cnotin $allowedGateStatuses) {
            throw 'native qualification results contain an invalid gate status'
        }
    }
    return $results
}

function Assert-NativeResults(
    [string]$ResultsPath,
    [ValidateSet('Hosted', 'Certify')][string]$ExpectedMode,
    [ValidateSet('Full', 'StoreUpdate')][string]$AggregationProfile = 'Full'
) {
    $results = Read-NativeResults -ResultsPath $ResultsPath -ExpectedMode $ExpectedMode
    if ($results.overall -ceq 'pass' -and $null -eq $results.evidence) {
        throw 'passing native qualification results require signed evidence'
    }
    if ($AggregationProfile -eq 'Full' -and $results.overall -cne 'pass') {
        throw 'native qualification results did not pass'
    }
    if ($AggregationProfile -eq 'StoreUpdate') {
        if ($null -eq $results.evidence -or $results.overall -notin @('pass', 'incomplete') -or
            @($gateNames | Where-Object { $results.gates.$_ -eq 'fail' }).Count -ne 0) {
            throw 'StoreUpdate qualification results have a failure or missing evidence'
        }
        $required = @('identity', 'package_hash', 'signature', 'account', 'package_registration', 'exact_identity', 'installed_binary_hash', 'alias_identity', 'native_cli', 'icmp', 'history_config', 'shell_aliases', 'upgrade', 'uninstall_reinstall', 'fail_injection', 'cleanup')
        if ($ExpectedMode -eq 'Certify') { $required += 'wack' }
        $manualGates = @('conflicts', 'repair')
        if (@($required | Where-Object { $results.gates.$_ -cne 'pass' }).Count -ne 0 -or
            @($manualGates | Where-Object { $results.gates.$_ -notin @('pass', 'incomplete') }).Count -ne 0 -or
            ($ExpectedMode -eq 'Hosted' -and $results.gates.wack -cne 'incomplete')) {
            throw 'StoreUpdate qualification requirements were not met'
        }
        $incompleteGates = @($gateNames | Where-Object { $results.gates.$_ -ceq 'incomplete' })
        $manualIncomplete = @($manualGates | Where-Object { $results.gates.$_ -ceq 'incomplete' })
        $expectedIncompleteGates = if ($ExpectedMode -eq 'Hosted') { @('wack') + $manualIncomplete } else { $manualIncomplete }
        $expectedOverall = if ($manualIncomplete.Count -gt 0) { 'incomplete' } else { 'pass' }
        if (@($incompleteGates | Where-Object { $_ -notin $expectedIncompleteGates }).Count -ne 0 -or
            $results.overall -cne $expectedOverall) {
            throw 'native overall status contradicts the gate map'
        }
        return $results
    }
    if ($ExpectedMode -eq 'Hosted') {
        $failedCoreGates = @($gateNames | Where-Object {
            $_ -ne 'wack' -and $results.gates.$_ -cne 'pass'
        })
        if ($failedCoreGates.Count -ne 0 -or $results.gates.wack -cne 'incomplete') {
            throw 'hosted native qualification is incomplete'
        }
    } elseif (@($gateNames | Where-Object { $results.gates.$_ -cne 'pass' }).Count -ne 0) {
        throw 'certification native qualification is incomplete'
    }
    return $results
}

function Read-NativeSummaryResults([string]$ResultsPath) {
    $results = Get-Content -LiteralPath $ResultsPath -Raw | ConvertFrom-Json
    $propertyNames = @($results.PSObject.Properties.Name | Sort-Object)
    $expectedProperties = @('evidence', 'gates', 'mode', 'overall', 'schema_version')
    $expectedProperties = @($expectedProperties | Sort-Object)
    if (($propertyNames -join "`n") -cne ($expectedProperties -join "`n") -or
        $results.schema_version -isnot [long] -or $results.schema_version -ne 2 -or
        $results.mode -cnotin @('Hosted', 'Certify') -or
        $results.overall -cnotin $allowedGateStatuses -or
        $null -eq $results.gates) {
        throw 'native summary results have an invalid schema'
    }
    if ($null -ne $results.evidence) {
        $evidenceNames = @($results.evidence.PSObject.Properties.Name | Sort-Object)
        if (($evidenceNames -join "`n") -cne (@('packaging_revision', 'signed_package_sha256', 'signed_receipt_sha256') -join "`n") -or
            $results.evidence.signed_package_sha256 -isnot [string] -or
            $results.evidence.signed_receipt_sha256 -isnot [string] -or
            $results.evidence.packaging_revision -isnot [string] -or
            [string]$results.evidence.signed_package_sha256 -cnotmatch '^[0-9a-f]{64}$' -or
            [string]$results.evidence.signed_receipt_sha256 -cnotmatch '^[0-9a-f]{64}$' -or
            [string]$results.evidence.packaging_revision -cnotmatch '^[0-9a-f]{40}$') {
            throw 'native summary results contain invalid evidence'
        }
    }
    $actualNames = @($results.gates.PSObject.Properties.Name)
    if (@($actualNames | Where-Object { $_ -cnotin $gateNames }).Count -ne 0) {
        throw 'native summary results have an invalid gate set'
    }
    foreach ($name in $actualNames) {
        if ($results.gates.$name -cnotin $allowedGateStatuses) {
            throw 'native summary results contain an invalid gate status'
        }
    }
    return $results
}

function Write-GateOutput([string]$Status) {
    if ($Status -cnotin $allowedGateStatuses) {
        throw 'WACK gate status is invalid'
    }
    [IO.File]::AppendAllText($env:GITHUB_OUTPUT, "gate_status=$Status`n", [Text.UTF8Encoding]::new($false))
}

switch ($Mode) {
    'Initialize' {
        $null = Assert-CnMonMsixDesktopBehaviorBaseline -ManifestPath (Join-Path $repositoryRoot 'packaging/windows/AppxManifest.xml') -SidecarPath (Join-Path $repositoryRoot 'packaging/windows/cn-mon.exe.manifest')
        $directory = Get-JobDirectory
        $requiredTools = @('makeappx.exe', 'signtool.exe')
        $toolRecords = foreach ($toolName in $requiredTools) {
            $toolPath = Join-Path $sdkBinDirectory $toolName
            if (-not (Test-Path -LiteralPath $toolPath -PathType Leaf)) {
                throw 'required Windows SDK tools are unavailable'
            }
            [pscustomobject]@{
                name = $toolName
                file_version = [Diagnostics.FileVersionInfo]::GetVersionInfo($toolPath).FileVersion
            }
        }
        $os = Get-CimInstance -ClassName Win32_OperatingSystem -ErrorAction Stop
        $runner = [pscustomobject]@{
            image = $env:ImageOS
            image_version = $env:ImageVersion
            os_caption = $os.Caption
            os_version = $os.Version
            os_build = $os.BuildNumber
            sdk_version = '10.0.26100.0'
            sdk_tools = @($toolRecords)
        }
        $runner | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath (Join-Path $directory 'runner-info.json') -Encoding utf8
    }
    'DownloadRelease' {
        $directory = Get-JobDirectory
        $release = Get-ReleaseMetadata
        $archivePath = Join-Path $directory $release.archive_name
        try {
            Invoke-WebRequest -Uri $release.archive_url -OutFile $archivePath
        } catch {
            throw 'approved Windows release download failed'
        }
        $actualHash = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($actualHash -cne ([string]$release.archive_sha256).ToLowerInvariant()) {
            throw 'approved Windows release hash validation failed'
        }
    }
    'Build' {
        $directory = Get-JobDirectory
        $release = Get-ReleaseMetadata
        $metadataPath = if ([string]::IsNullOrWhiteSpace($ReleaseMetadataPath)) { Join-Path $repositoryRoot 'packaging/windows/release.json' } else { [IO.Path]::GetFullPath($ReleaseMetadataPath) }
        $archivePath = Join-Path $directory $release.archive_name
        $packageDirectoryPath = Join-Path $directory $PackageDirectory
        if (Test-Path -LiteralPath $packageDirectoryPath) {
            if (-not (Test-Path -LiteralPath $packageDirectoryPath -PathType Container) -or
                @(Get-ChildItem -LiteralPath $packageDirectoryPath -Force).Count -ne 0) {
                throw 'MSIX package output directory must be empty'
            }
        } else {
            New-Item -ItemType Directory -Path $packageDirectoryPath | Out-Null
        }
        $os = Get-CimInstance -ClassName Win32_OperatingSystem -ErrorAction Stop
        $version = [version]$os.Version
        $maxVersionTested = '{0}.{1}.{2}.0' -f $version.Major, $version.Minor, $version.Build

        $packageTests = Join-Path $repositoryRoot 'scripts/test-msix-package.ps1'
        $packageTestLog = Join-Path $directory 'package-tests.log'
        & pwsh -NoProfile -File $packageTests -ReleaseArchive $archivePath -ReleaseMetadataPath $metadataPath -SdkBinDirectory $sdkBinDirectory *> $packageTestLog
        if ($LASTEXITCODE -ne 0) {
            throw 'MSIX package behavior tests failed; details remain job-local'
        }

        $packager = Join-Path $repositoryRoot 'scripts/package-msix.ps1'
        $packageLog = Join-Path $directory 'package-build.log'
        & pwsh -NoProfile -File $packager -ReleaseArchive $archivePath -OutputDirectory $packageDirectoryPath -SdkBinDirectory $sdkBinDirectory -PackageVersion $release.package_version -ReleaseMetadataPath $metadataPath -MaxVersionTested $maxVersionTested *> $packageLog
        if ($LASTEXITCODE -ne 0) {
            throw 'MSIX package build failed; details remain job-local'
        }

        $packagePath = Join-Path $packageDirectoryPath (Get-MsixPackageFileName -PackageVersion $release.package_version)
        $receiptPath = Join-Path $packageDirectoryPath 'msix-receipt.json'
        if (-not (Test-Path -LiteralPath $packagePath -PathType Leaf) -or
            -not (Test-Path -LiteralPath $receiptPath -PathType Leaf)) {
            throw 'MSIX package outputs are incomplete'
        }
        Assert-CandidateReceipt -PackagePath $packagePath -ReceiptPath $receiptPath
    }
    'Hosted' {
        $directory = Get-JobDirectory
        $release = Get-ReleaseMetadata
        $packageDirectoryPath = Join-Path $directory $PackageDirectory
        $evidenceDirectory = Join-Path $directory 'evidence'
        New-Item -ItemType Directory -Force -Path $evidenceDirectory | Out-Null
        $packagePath = Join-Path $packageDirectoryPath (Get-MsixPackageFileName -PackageVersion $release.package_version)
        $receiptPath = Join-Path $packageDirectoryPath 'msix-receipt.json'
        if (-not (Test-Path -LiteralPath $packagePath -PathType Leaf) -or
            -not (Test-Path -LiteralPath $receiptPath -PathType Leaf)) {
            throw 'synthetic MSIX package inputs are incomplete'
        }
        Assert-CandidateReceipt -PackagePath $packagePath -ReceiptPath $receiptPath

        $tester = Join-Path $repositoryRoot 'scripts/test-msix-windows.ps1'
        $log = Join-Path $directory 'hosted-qualification.log'
        $signedPackage = Join-Path $evidenceDirectory (Get-MsixPackageFileName -PackageVersion $release.package_version -Signed)
        $signedReceipt = Join-Path $evidenceDirectory 'msix-signed-receipt.json'
        $resultsPath = Join-Path $evidenceDirectory 'msix-native-results.json'
        foreach ($path in @($signedPackage, $signedReceipt, $resultsPath)) {
            if (Test-Path -LiteralPath $path) { throw 'hosted qualification evidence already exists' }
        }
        $metadataPath = if ([string]::IsNullOrWhiteSpace($ReleaseMetadataPath)) { Join-Path $repositoryRoot 'packaging/windows/release.json' } else { [IO.Path]::GetFullPath($ReleaseMetadataPath) }
        & pwsh -NoProfile -File $tester -Mode Hosted -PackagePath $packagePath -ReceiptPath $receiptPath -SdkBinDirectory $sdkBinDirectory -ArtifactDirectory $evidenceDirectory -ReleaseMetadataPath $metadataPath *> $log
        $testerExitCode = $LASTEXITCODE

        foreach ($path in @($signedPackage, $signedReceipt, $resultsPath)) {
            if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
                throw 'hosted qualification outputs are incomplete'
            }
        }
        $signed = Get-Content -LiteralPath $signedReceipt -Raw | ConvertFrom-Json
        $signedHash = (Get-FileHash -LiteralPath $signedPackage -Algorithm SHA256).Hash.ToLowerInvariant()
        $unsigned = Get-Content -LiteralPath $receiptPath -Raw | ConvertFrom-Json
        $release = Get-ReleaseMetadata
        $sourceRevision = Get-SourceRevision
        if ($signed.purpose -cne 'candidate' -or
            $signed.package_version -cne $release.package_version -or
            $signed.cli_version -cne $release.cli_version -or
            $signed.cli_revision -cne $release.cli_revision -or
            $signed.packaging_revision -cne $sourceRevision -or
            $signed.archive_sha256 -cne $release.archive_sha256 -or
            $signed.binary_sha256 -cne $release.binary_sha256 -or
            $signed.name -cne $env:CNMON_MSIX_IDENTITY_NAME -or
            $signed.publisher -cne $env:CNMON_MSIX_IDENTITY_PUBLISHER -or
            $signed.publisher_display_name -cne $env:CNMON_MSIX_PUBLISHER_DISPLAY_NAME -or
            $signed.expected_pfn -cne $env:CNMON_MSIX_EXPECTED_PFN -or
            $signed.signing.status -cne 'test' -or
            [string]::IsNullOrWhiteSpace([string]$signed.signing.certificate_thumbprint) -or
            $signed.signing.unsigned_package_sha256 -cne $unsigned.package_sha256 -or
            $signed.signing.signed_package_sha256 -cne $signedHash -or
            $signed.package_sha256 -cne $signedHash) {
            throw 'signed MSIX receipt failed identity or package hash validation'
        }
        $expectedEvidence = Get-SignedEvidenceBinding -PackagePath $signedPackage -ReceiptPath $signedReceipt
        $nativeResults = Read-NativeResults -ResultsPath $resultsPath -ExpectedMode Hosted
        Assert-NativeResultEvidence -Actual $nativeResults.evidence -Expected $expectedEvidence
        $null = Assert-NativeResults -ResultsPath $resultsPath -ExpectedMode Hosted -AggregationProfile StoreUpdate
        if (($testerExitCode -eq 0 -and $nativeResults.overall -cne 'pass') -or
            ($testerExitCode -eq 1 -and $nativeResults.overall -cne 'incomplete') -or
            $testerExitCode -notin @(0, 1)) {
            Write-CnMonMsixSignatureDiagnosticStatus -Path (Join-Path $evidenceDirectory 'msix-signature-diagnostic-status.json')
            throw 'hosted installed-package qualification did not pass: native report contradicts process status'
        }
    }
    'Certify' {
        $directory = Get-JobDirectory
        $release = Get-ReleaseMetadata
        $evidenceDirectory = Join-Path $directory 'evidence'
        $packagePath = Join-Path $evidenceDirectory (Get-MsixPackageFileName -PackageVersion $release.package_version -Signed)
        $receiptPath = Join-Path $evidenceDirectory 'msix-signed-receipt.json'
        $resultsPath = Join-Path $evidenceDirectory 'msix-native-results.json'
        if (-not (Test-Path -LiteralPath $packagePath -PathType Leaf) -or -not (Test-Path -LiteralPath $receiptPath -PathType Leaf) -or
            -not (Test-Path -LiteralPath $resultsPath -PathType Leaf)) {
            throw 'signed package, receipt, and Hosted results are required before WACK certification'
        }
        $expectedEvidence = Get-SignedEvidenceBinding -PackagePath $packagePath -ReceiptPath $receiptPath
        $hostedBytes = [IO.File]::ReadAllBytes($resultsPath)
        $hostedJson = [Text.UTF8Encoding]::new($false, $true).GetString($hostedBytes)
        $hostedResults = Read-NativeResults -ResultsPath $resultsPath -ExpectedMode Hosted -RawJson $hostedJson
        Assert-NativeResultEvidence -Actual $hostedResults.evidence -Expected $expectedEvidence
        $snapshotPath = Join-Path $directory 'hosted-results.snapshot.json'
        if (Test-Path -LiteralPath $snapshotPath) { throw 'Hosted result snapshot already exists' }
        [IO.File]::WriteAllBytes($snapshotPath, $hostedBytes)
        $tester = Join-Path $repositoryRoot 'scripts/test-msix-windows.ps1'
        $log = Join-Path $directory 'wack-certification.log'
        $metadataPath = if ([string]::IsNullOrWhiteSpace($ReleaseMetadataPath)) { Join-Path $repositoryRoot 'packaging/windows/release.json' } else { [IO.Path]::GetFullPath($ReleaseMetadataPath) }
        & pwsh -NoProfile -File $tester -Mode Certify -PackagePath $packagePath -ReceiptPath $receiptPath -SdkBinDirectory $sdkBinDirectory -ArtifactDirectory $evidenceDirectory -HostedResultsPath $snapshotPath -ReleaseMetadataPath $metadataPath *> $log
        $exitCode = $LASTEXITCODE
        if (Test-Path -LiteralPath $resultsPath -PathType Leaf) {
            $results = Read-NativeResults -ResultsPath $resultsPath -ExpectedMode Certify
            if ($null -ne $results.evidence) {
                Assert-NativeResultEvidence -Actual $results.evidence -Expected $expectedEvidence
            }
            $gateStatus = [string]$results.gates.wack
        } else {
            $gateStatus = 'incomplete'
        }
        Write-GateOutput $gateStatus
        $certifyResults = Assert-NativeResults -ResultsPath $resultsPath -ExpectedMode Certify -AggregationProfile StoreUpdate
        if (($exitCode -eq 0 -and $certifyResults.overall -cne 'pass') -or
            ($exitCode -eq 1 -and $certifyResults.overall -cne 'incomplete') -or
            $exitCode -notin @(0, 1) -or $gateStatus -cne 'pass') {
            throw 'WACK or native certification qualification failed; report remains job-local'
        }
    }
    'Summary' {
        $event = $env:EVENT_NAME
        if ($event -notin @('pull_request', 'workflow_dispatch')) {
            throw 'public summary event is invalid'
        }
        $statuses = @{
            job_status = $env:JOB_STATUS
            package_status = $env:PACKAGE_STATUS
            qualification_status = $env:QUALIFICATION_STATUS
            wack_status = $env:WACK_STATUS
        }
        foreach ($status in $statuses.Values) {
            if ($status -notin $allowedStepStatuses -and $status -notin $allowedGateStatuses) {
                throw 'public summary status is invalid'
            }
        }

        $directory = Join-Path (Get-JobDirectory) 'summary'
        New-Item -ItemType Directory -Force -Path $directory | Out-Null
        if (Get-ChildItem -LiteralPath $directory -Force) {
            throw 'public summary directory is not empty'
        }
        $gates = [ordered]@{}
        foreach ($name in $gateNames) { $gates[$name] = 'incomplete' }
        $overall = 'not_available'
        $nativeEvidence = 'missing'
        $resultsPath = Join-Path (Join-Path $jobDirectory 'evidence') 'msix-native-results.json'
        if (Test-Path -LiteralPath $resultsPath -PathType Leaf) {
            try {
                $results = Read-NativeSummaryResults -ResultsPath $resultsPath
                $nativeEvidence = 'validated'
                $overall = [string]$results.overall
                foreach ($name in $gateNames) {
                    if ($results.gates.PSObject.Properties.Name -contains $name) {
                        $gates[$name] = [string]$results.gates.$name
                    }
                }
                $hasFailure = $results.overall -eq 'fail' -or
                    @($gates.Values | Where-Object { $_ -eq 'fail' }).Count -gt 0
                $completePassMap = if ($results.mode -eq 'Hosted') {
                    $null -ne $results.evidence -and $gates.wack -eq 'incomplete' -and
                        @($gateNames | Where-Object { $_ -ne 'wack' -and $gates[$_] -ne 'pass' }).Count -eq 0
                } else {
                    $null -ne $results.evidence -and
                        @($gateNames | Where-Object { $gates[$_] -ne 'pass' }).Count -eq 0
                }
                if ($hasFailure) {
                    $overall = 'fail'
                } elseif ($results.overall -eq 'pass' -and $completePassMap) {
                    $overall = 'pass'
                } else {
                    $overall = 'incomplete'
                }
            } catch {
                $nativeEvidence = 'invalid'
            }
        }
        $counts = [ordered]@{
            pass = @($gates.Values | Where-Object { $_ -eq 'pass' }).Count
            fail = @($gates.Values | Where-Object { $_ -eq 'fail' }).Count
            incomplete = @($gates.Values | Where-Object { $_ -eq 'incomplete' }).Count
        }
        $summary = [ordered]@{
            schema_version = 1
            event = $event
            job_status = $statuses.job_status
            package_status = $statuses.package_status
            qualification_status = $statuses.qualification_status
            wack_status = $statuses.wack_status
            qualification_overall = $overall
            native_evidence = $nativeEvidence
            gate_counts = $counts
            gates = $gates
            details_retention = 'job-local-only'
        }
        $json = ConvertTo-Json -InputObject $summary -Depth 5
        [IO.File]::WriteAllText((Join-Path $directory 'msix-public-summary.json'), $json, [Text.UTF8Encoding]::new($false))
    }
}
