# SPDX-License-Identifier: Apache-2.0
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('Initialize', 'DownloadRelease', 'Build', 'Hosted', 'Certify', 'Summary')]
    [string]$Mode,

    [string]$PackageDirectory = 'package'
)

$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
$repositoryRoot = Split-Path -Parent $PSScriptRoot
$jobDirectory = Join-Path $env:RUNNER_TEMP 'cn-mon-msix-job'
$sdkBinDirectory = 'C:\Program Files (x86)\Windows Kits\10\bin\10.0.26100.0\x64'
$allowedStepStatuses = @('success', 'failure', 'skipped', 'cancelled', 'incomplete', 'not_applicable')
$allowedGateStatuses = @('pass', 'fail', 'incomplete')
$gateNames = @(
    'identity', 'package_hash', 'signature', 'account', 'package_registration',
    'exact_identity', 'installed_binary_hash', 'alias_identity', 'native_cli',
    'icmp', 'history_config', 'shell_aliases', 'upgrade', 'repair',
    'uninstall_reinstall', 'conflicts', 'fail_injection', 'cleanup', 'wack'
)

function Get-ReleaseMetadata {
    $releasePath = Join-Path $repositoryRoot 'packaging/windows/release.json'
    return Get-Content -LiteralPath $releasePath -Raw | ConvertFrom-Json
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

function Assert-NativeResults(
    [string]$ResultsPath,
    [ValidateSet('Hosted', 'Certify')][string]$ExpectedMode
) {
    $results = Get-Content -LiteralPath $ResultsPath -Raw | ConvertFrom-Json
    $propertyNames = @($results.PSObject.Properties.Name | Sort-Object)
    $expectedProperties = @('gates', 'mode', 'overall', 'schema_version')
    $expectedProperties = @($expectedProperties | Sort-Object)
    if (($propertyNames -join "`n") -cne ($expectedProperties -join "`n") -or
        $results.schema_version -ne 1 -or
        $results.mode -cne $ExpectedMode -or
        $results.overall -notin $allowedGateStatuses -or
        $null -eq $results.gates) {
        throw 'native qualification results have an invalid schema'
    }

    $actualNames = @($results.gates.PSObject.Properties.Name | Sort-Object)
    $expectedNames = @($gateNames | Sort-Object)
    if (($actualNames -join "`n") -cne ($expectedNames -join "`n")) {
        throw 'native qualification results have an invalid gate set'
    }
    foreach ($name in $actualNames) {
        if ($results.gates.$name -notin $allowedGateStatuses) {
            throw 'native qualification results contain an invalid gate status'
        }
    }
    if ($results.overall -cne 'pass') {
        throw 'native qualification results did not pass'
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
    $expectedProperties = @('gates', 'mode', 'overall', 'schema_version')
    $expectedProperties = @($expectedProperties | Sort-Object)
    if (($propertyNames -join "`n") -cne ($expectedProperties -join "`n") -or
        $results.schema_version -ne 1 -or
        $results.mode -notin @('Hosted', 'Certify') -or
        $results.overall -notin $allowedGateStatuses -or
        $null -eq $results.gates) {
        throw 'native summary results have an invalid schema'
    }
    $actualNames = @($results.gates.PSObject.Properties.Name)
    if (@($actualNames | Where-Object { $_ -notin $gateNames }).Count -ne 0) {
        throw 'native summary results have an invalid gate set'
    }
    foreach ($name in $actualNames) {
        if ($results.gates.$name -notin $allowedGateStatuses) {
            throw 'native summary results contain an invalid gate status'
        }
    }
    return $results
}

function Write-GateOutput([string]$Status) {
    if ($Status -notin $allowedGateStatuses) {
        throw 'WACK gate status is invalid'
    }
    [IO.File]::AppendAllText($env:GITHUB_OUTPUT, "gate_status=$Status`n", [Text.UTF8Encoding]::new($false))
}

switch ($Mode) {
    'Initialize' {
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
        & pwsh -NoProfile -File $packageTests -ReleaseArchive $archivePath -SdkBinDirectory $sdkBinDirectory *> $packageTestLog
        if ($LASTEXITCODE -ne 0) {
            throw 'MSIX package behavior tests failed; details remain job-local'
        }

        $packager = Join-Path $repositoryRoot 'scripts/package-msix.ps1'
        $packageLog = Join-Path $directory 'package-build.log'
        & pwsh -NoProfile -File $packager -ReleaseArchive $archivePath -OutputDirectory $packageDirectoryPath -SdkBinDirectory $sdkBinDirectory -PackageVersion $release.package_version -MaxVersionTested $maxVersionTested *> $packageLog
        if ($LASTEXITCODE -ne 0) {
            throw 'MSIX package build failed; details remain job-local'
        }

        $packagePath = Join-Path $packageDirectoryPath 'cn-mon_1.3.0.0_x64.msix'
        $receiptPath = Join-Path $packageDirectoryPath 'msix-receipt.json'
        if (-not (Test-Path -LiteralPath $packagePath -PathType Leaf) -or
            -not (Test-Path -LiteralPath $receiptPath -PathType Leaf)) {
            throw 'MSIX package outputs are incomplete'
        }
        Assert-CandidateReceipt -PackagePath $packagePath -ReceiptPath $receiptPath
    }
    'Hosted' {
        $directory = Get-JobDirectory
        $packageDirectoryPath = Join-Path $directory $PackageDirectory
        $evidenceDirectory = Join-Path $directory 'evidence'
        New-Item -ItemType Directory -Force -Path $evidenceDirectory | Out-Null
        $packagePath = Join-Path $packageDirectoryPath 'cn-mon_1.3.0.0_x64.msix'
        $receiptPath = Join-Path $packageDirectoryPath 'msix-receipt.json'
        if (-not (Test-Path -LiteralPath $packagePath -PathType Leaf) -or
            -not (Test-Path -LiteralPath $receiptPath -PathType Leaf)) {
            throw 'synthetic MSIX package inputs are incomplete'
        }
        Assert-CandidateReceipt -PackagePath $packagePath -ReceiptPath $receiptPath

        $tester = Join-Path $repositoryRoot 'scripts/test-msix-windows.ps1'
        $log = Join-Path $directory 'hosted-qualification.log'
        & pwsh -NoProfile -File $tester -Mode Hosted -PackagePath $packagePath -ReceiptPath $receiptPath -SdkBinDirectory $sdkBinDirectory -ArtifactDirectory $evidenceDirectory *> $log
        if ($LASTEXITCODE -ne 0) {
            throw 'hosted installed-package qualification did not pass; details remain job-local'
        }

        $signedPackage = Join-Path $evidenceDirectory 'cn-mon_1.3.0.0_x64-signed.msix'
        $signedReceipt = Join-Path $evidenceDirectory 'msix-signed-receipt.json'
        $resultsPath = Join-Path $evidenceDirectory 'msix-native-results.json'
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
        $null = Assert-NativeResults -ResultsPath $resultsPath -ExpectedMode Hosted
    }
    'Certify' {
        $directory = Get-JobDirectory
        $evidenceDirectory = Join-Path $directory 'evidence'
        $packagePath = Join-Path $evidenceDirectory 'cn-mon_1.3.0.0_x64-signed.msix'
        $receiptPath = Join-Path $evidenceDirectory 'msix-signed-receipt.json'
        $resultsPath = Join-Path $evidenceDirectory 'msix-native-results.json'
        $tester = Join-Path $repositoryRoot 'scripts/test-msix-windows.ps1'
        $log = Join-Path $directory 'wack-certification.log'
        & pwsh -NoProfile -File $tester -Mode Certify -PackagePath $packagePath -ReceiptPath $receiptPath -SdkBinDirectory $sdkBinDirectory -ArtifactDirectory $evidenceDirectory *> $log
        $exitCode = $LASTEXITCODE
        if (Test-Path -LiteralPath $resultsPath -PathType Leaf) {
            $results = Assert-NativeResults -ResultsPath $resultsPath -ExpectedMode Certify
            $gateStatus = [string]$results.gates.wack
            if ($results.mode -cne 'Certify') {
                $gateStatus = 'incomplete'
            }
        } else {
            $gateStatus = 'incomplete'
        }
        Write-GateOutput $gateStatus
        if ($exitCode -ne 0 -or $gateStatus -ne 'pass') {
            throw 'WACK certification gate did not pass; report remains job-local'
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
                    $gates.wack -eq 'incomplete' -and
                        @($gateNames | Where-Object { $_ -ne 'wack' -and $gates[$_] -ne 'pass' }).Count -eq 0
                } else {
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
