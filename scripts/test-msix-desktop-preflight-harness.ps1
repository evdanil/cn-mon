# Copyright 2026 cn-mon contributors
# SPDX-License-Identifier: Apache-2.0

$ErrorActionPreference = 'Stop'

function Assert-True([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw $Message }
}

function Assert-Equal([object]$Actual, [object]$Expected, [string]$Message) {
    if ($Actual -cne $Expected) { throw "$Message (expected '$Expected', got '$Actual')" }
}

$scriptPath = Join-Path $PSScriptRoot 'test-msix-desktop-preflight.ps1'
$parseErrors = $null
$sourceAst = [System.Management.Automation.Language.Parser]::ParseFile($scriptPath, [ref]$null, [ref]$parseErrors)
if ($parseErrors.Count -gt 0) {
    throw "desktop preflight did not parse: $($parseErrors[0].Message)"
}

$requiredFunctions = @(
    'ConvertTo-CnMonPreflightVersion',
    'Get-CnMonPreflightSession',
    'Get-CnMonPreflightOsDetails',
    'Test-CnMonPreflightAdministratorsSid',
    'Test-CnMonPreflightAppxCmdlets',
    'New-CnMonPreflightReport'
)
$functionAsts = @($sourceAst.FindAll({
    param($candidate)
    $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
        $requiredFunctions -contains $candidate.Name
}, $true))
$foundFunctions = @($functionAsts | ForEach-Object Name)
$missingFunctions = @($requiredFunctions | Where-Object { $_ -notin $foundFunctions })
if ($missingFunctions.Count -gt 0) {
    throw "desktop preflight pure helper contract is missing: $($missingFunctions -join ', ')"
}

$commands = @($sourceAst.FindAll({
    param($candidate)
    $candidate -is [System.Management.Automation.Language.CommandAst]
}, $true) | ForEach-Object { $_.GetCommandName() })
$forbiddenCommands = @(
    '.', 'Add-AppxPackage', 'Import-Certificate', 'Import-PfxCertificate',
    'New-LocalUser', 'Remove-AppxPackage', 'Remove-LocalUser', 'Reset-AppxPackage',
    'Start-Process', 'Unregister-AppxPackage'
)
$foundForbidden = @($commands | Where-Object { $_ -in $forbiddenCommands })
Assert-Equal $foundForbidden.Count 0 'desktop inventory contains a mutating or dot-source command'
Assert-True (-not (Select-String -LiteralPath $scriptPath -Pattern 'test-msix-windows\.ps1' -Quiet)) 'desktop inventory references the mutating harness'
$adminGroupChecks = @($sourceAst.FindAll({
    param($candidate)
    $candidate -is [System.Management.Automation.Language.CommandAst] -and
        $candidate.GetCommandName() -eq 'Test-CnMonPreflightAdministratorsSid'
}, $true))
Assert-Equal $adminGroupChecks.Count 1 'desktop inventory must inspect the current token group SIDs once'
Assert-True ($adminGroupChecks[0].Extent.Text -match '\$identity\.Groups') 'Administrators SID check does not inspect current token groups'
$appxCmdletQueries = @($sourceAst.FindAll({
    param($candidate)
    $candidate -is [System.Management.Automation.Language.CommandAst] -and
        $candidate.GetCommandName() -eq 'Get-Command' -and
        $candidate.Extent.Text -match 'Add-AppxPackage' -and
        $candidate.Extent.Text -match 'Get-AppxPackage' -and
        $candidate.Extent.Text -match 'Remove-AppxPackage'
}, $true))
Assert-Equal $appxCmdletQueries.Count 1 'Appx availability check must query all three lifecycle cmdlets'
Assert-True ($sourceAst.Extent.Text -match '(?s)\$fallbackOsVersion\s*=\s*\[Environment\]::OSVersion\.Version\.ToString\(\)') 'desktop inventory must seed OS details from the runtime version'
$osDetailsCalls = @($sourceAst.FindAll({
    param($candidate)
    $candidate -is [System.Management.Automation.Language.CommandAst] -and
        $candidate.GetCommandName() -eq 'Get-CnMonPreflightOsDetails'
}, $true))
Assert-True (@($osDetailsCalls | Where-Object { $_.Extent.Text -match '\$os\.Version' -and $_.Extent.Text -match '\$os\.BuildNumber' }).Count -eq 1) 'successful CIM data does not override fallback OS details'

$tempRoot = Join-Path ([IO.Path]::GetTempPath()) ('cn-mon-msix-preflight-test-' + [Guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $tempRoot
$helperPath = Join-Path $tempRoot 'helpers.ps1'
[IO.File]::WriteAllText($helperPath, (@($functionAsts | ForEach-Object { $_.Extent.Text }) -join [Environment]::NewLine), [Text.UTF8Encoding]::new($false))
. $helperPath

try {
    $activeRdp = Get-CnMonPreflightSession -SessionId 3 -ConnectionStateCode 0 -ProtocolType 2
    Assert-Equal $activeRdp.connection_state 'active' 'active WTS state was not mapped to its fixed enum'
    Assert-Equal $activeRdp.protocol 'rdp' 'RDP protocol was not mapped to its fixed enum'
    Assert-True $activeRdp.eligible 'a nonzero active RDP session was not eligible'

    $console = Get-CnMonPreflightSession -SessionId 1 -ConnectionStateCode 0 -ProtocolType 0
    Assert-True $console.eligible 'a nonzero active console session was not eligible'
    Assert-True (-not (Get-CnMonPreflightSession -SessionId 0 -ConnectionStateCode 0 -ProtocolType 0).eligible) 'session zero was eligible'
    Assert-True (-not (Get-CnMonPreflightSession -SessionId 3 -ConnectionStateCode 4 -ProtocolType 2).eligible) 'a disconnected RDP session was eligible'
    Assert-True (-not (Get-CnMonPreflightSession -SessionId 3 -ConnectionStateCode 0 -ProtocolType 1).eligible) 'an unsupported protocol was eligible'

    Assert-True (Test-CnMonPreflightAdministratorsSid -Groups @([pscustomobject]@{ Value = 'S-1-5-32-544' })) 'Administrators SID membership was not observed'
    Assert-True (-not (Test-CnMonPreflightAdministratorsSid -Groups @([pscustomobject]@{ Value = 'S-1-5-32-545' }))) 'a different SID was reported as Administrators membership'
    $requiredAppxCmdlets = @('Add-AppxPackage', 'Get-AppxPackage', 'Remove-AppxPackage')
    Assert-True (Test-CnMonPreflightAppxCmdlets -CommandNames $requiredAppxCmdlets) 'complete Appx lifecycle cmdlet set was unavailable'
    Assert-True (-not (Test-CnMonPreflightAppxCmdlets -CommandNames @('Add-AppxPackage', 'Get-AppxPackage'))) 'Appx availability omitted Remove-AppxPackage'
    $fallbackOs = Get-CnMonPreflightOsDetails -FallbackVersion '10.0.26200.9711'
    Assert-Equal $fallbackOs.version '10.0.26200.9711' 'runtime OS version fallback was not retained'
    Assert-Equal $fallbackOs.build 26200 'runtime OS build fallback was not derived numerically'
    $cimOs = Get-CnMonPreflightOsDetails -FallbackVersion '10.0.26200.9711' -CimVersion '10.0.26100.4652' -CimBuild '26100'
    Assert-Equal $cimOs.version '10.0.26100.4652' 'successful CIM OS version did not override fallback'
    Assert-Equal $cimOs.build 26100 'successful CIM build did not override fallback'
    $invalidCimOs = Get-CnMonPreflightOsDetails -FallbackVersion '10.0.26200.9711' -CimVersion 'unavailable' -CimBuild 'invalid'
    Assert-Equal $invalidCimOs.version '10.0.26200.9711' 'invalid CIM data erased the runtime OS version'
    Assert-Equal $invalidCimOs.build 26200 'invalid CIM data erased the runtime OS build'

    $observed = [pscustomobject]@{
        os_version = '10.0.26200.9711'
        os_build = '26200'
        powershell_version = '7.5.2'
        process_session_id = 3
        connection_state_code = 0
        protocol_type = 2
        token_is_administrator = $true
        token_has_administrators_sid = $true
        token_is_elevated = $false
        token_elevation_query_available = $true
        makeappx_present = $true
        makeappx_version = '10.0.26100.1'
        signtool_present = $true
        signtool_version = '10.0.26100.1'
        appcert_present = $false
        appcert_version = 'untrusted-version-text'
        appx_cmdlets_available = $false
        package_available = $false
        receipt_available = $false
        receipt_valid = $false
        package_sha256 = 'not-a-hash'
        receipt_sha256 = $null
        packaging_revision = 'not-a-revision'
        package_hash_matches_receipt = $true
        package_signer_matches_receipt = $true
        package_signature_trusted = $true
        expected_registration_present = $true
        account_name = 'PRIVATE_ACCOUNT_MARKER'
        sid = 'PRIVATE_SID_MARKER'
        path = 'PRIVATE_PATH_MARKER'
        raw_tool_output = 'PRIVATE_TOOL_OUTPUT_MARKER'
        raw_exception = 'PRIVATE_EXCEPTION_MARKER'
        package_family_name = 'PRIVATE_PFN_MARKER'
        certificate_subject = 'PRIVATE_SUBJECT_MARKER'
    }
    $report = New-CnMonPreflightReport -Observed $observed
    $expectedProperties = @(
        'schema_version', 'os_version', 'os_build', 'powershell_version', 'process_session_id',
        'session_connection_state', 'session_protocol', 'session_eligible', 'token_is_administrator',
        'token_has_administrators_sid',
        'token_is_elevated', 'token_elevation_query_available', 'makeappx_present', 'makeappx_version',
        'signtool_present', 'signtool_version', 'appcert_present', 'appcert_version',
        'appx_cmdlets_available', 'package_available', 'receipt_available', 'receipt_valid',
        'package_sha256', 'receipt_sha256', 'packaging_revision', 'package_hash_matches_receipt',
        'package_signer_matches_receipt', 'package_signature_trusted', 'expected_registration_present',
        'repair_ui', 'alias_ui'
    )
    Assert-Equal ((@($report.PSObject.Properties.Name | Sort-Object)) -join ',') ((@($expectedProperties | Sort-Object)) -join ',') 'inventory projection fields changed'
    Assert-Equal $report.os_version '10.0.26200.9711' 'OS version was not kept numeric'
    Assert-Equal $report.powershell_version '7.5.2.0' 'PowerShell version was not normalized numerically'
    Assert-Equal $report.makeappx_version '10.0.26100.1' 'SDK tool version was not kept numeric'
    Assert-Equal $report.appcert_version '0.0.0.0' 'invalid tool version text was retained'
    Assert-Equal $report.token_has_administrators_sid $true 'Administrators SID presence was not projected as a boolean'
    Assert-Equal $report.package_available $false 'missing package was reported available'
    Assert-Equal $report.receipt_available $false 'missing receipt was reported available'
    Assert-Equal $report.package_hash_matches_receipt $false 'unavailable inputs were marked as a passing hash gate'
    Assert-Equal $report.package_signer_matches_receipt $false 'unavailable inputs were marked as a matching signer'
    Assert-Equal $report.package_signature_trusted $false 'unavailable inputs were marked as trusted'
    Assert-Equal $report.expected_registration_present $false 'unavailable inputs were marked registered'
    Assert-Equal $report.packaging_revision $null 'invalid revision text was retained'
    Assert-Equal $report.repair_ui 'not_observed' 'repair UI was represented as observed'
    Assert-Equal $report.alias_ui 'not_observed' 'alias UI was represented as observed'

    $json = ConvertTo-Json -InputObject $report -Depth 3 -Compress
    foreach ($marker in @('PRIVATE_ACCOUNT_MARKER', 'PRIVATE_SID_MARKER', 'PRIVATE_PATH_MARKER', 'PRIVATE_TOOL_OUTPUT_MARKER', 'PRIVATE_EXCEPTION_MARKER', 'PRIVATE_PFN_MARKER', 'PRIVATE_SUBJECT_MARKER')) {
        Assert-True (-not $json.Contains($marker, [StringComparison]::Ordinal)) "private field leaked into inventory JSON: $marker"
    }

    $observed.package_available = $true
    $observed.receipt_available = $true
    $observed.receipt_valid = $true
    $observed.package_sha256 = 'a' * 64
    $observed.receipt_sha256 = 'b' * 64
    $observed.packaging_revision = 'c' * 40
    $observed.package_hash_matches_receipt = $true
    $observed.package_signer_matches_receipt = $true
    $observed.package_signature_trusted = $true
    $observed.expected_registration_present = $true
    $report = New-CnMonPreflightReport -Observed $observed
    Assert-Equal $report.package_sha256 ('a' * 64) 'valid package hash was omitted'
    Assert-Equal $report.receipt_sha256 ('b' * 64) 'valid receipt hash was omitted'
    Assert-Equal $report.packaging_revision ('c' * 40) 'valid packaging revision was omitted'
}
finally {
    Remove-Item -LiteralPath $tempRoot -Recurse -Force -ErrorAction SilentlyContinue
}

Write-Host 'PASS desktop preflight projection and read-only safety'
