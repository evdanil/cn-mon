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

function Assert-ThrowsMatching {
    param([scriptblock]$Action, [string]$Pattern, [string]$Message)
    try { & $Action }
    catch {
        if ($_.Exception.Message -match $Pattern) { return }
        throw "$Message (unexpected error: $($_.Exception.Message))"
    }
    throw $Message
}

function Get-CommandNodes {
    param([System.Management.Automation.Language.Ast]$Node, [string]$CommandName)
    return @($Node.FindAll({
        param($candidate)
        $candidate -is [System.Management.Automation.Language.CommandAst] -and
            $candidate.GetCommandName() -eq $CommandName
    }, $true))
}

function Write-WackFixture {
    param([string]$Path, [string]$Overall = 'PASS', [string]$Partial = 'FALSE', [string]$Name = 'Example.CnMon', [string]$Version = '1.3.0.0', [string]$TestResult = 'PASS')
    $xml = @"
<REPORT OVERALL_RESULT="$Overall" PARTIAL_RUN="$Partial" APP_NAME="$Name" APP_VERSION="$Version"><REQUIREMENTS><REQUIREMENT><TEST NAME="Package launch"><RESULT>$TestResult</RESULT></TEST></REQUIREMENT></REQUIREMENTS></REPORT>
"@
    [IO.File]::WriteAllText($Path, $xml, [Text.UTF8Encoding]::new($false))
}

function Invoke-MockedCertificateCleanup {
    param([bool]$RemoveTrustedCopy, [bool]$DeletePrivateKey, [bool]$FailTrustedCopy)
    $script:certificateRemovalCalls = [Collections.Generic.List[object]]::new()
    $script:certificateRemovalFailed = $false
    $script:mockTrustedCopyFailure = $FailTrustedCopy
    function Remove-Item {
        [CmdletBinding()]
        param([string]$LiteralPath, [switch]$Force, [switch]$DeleteKey)
        $script:certificateRemovalCalls.Add([pscustomobject]@{
            LiteralPath = $LiteralPath
            Force = $Force.IsPresent
            DeleteKey = $DeleteKey.IsPresent
        })
        if ($script:mockTrustedCopyFailure -and $LiteralPath -like 'Cert:\LocalMachine\TrustedPeople\*') {
            throw 'mock TrustedPeople removal failure'
        }
    }

    try {
        Remove-CnMonMsixOwnedCertificate -Thumbprint '0123456789ABCDEF0123456789ABCDEF01234567' -RemoveTrustedCopy:$RemoveTrustedCopy -DeletePrivateKey:$DeletePrivateKey
    }
    catch { $script:certificateRemovalFailed = $true }
    return [pscustomobject]@{
        Calls = @($script:certificateRemovalCalls)
        Failed = $script:certificateRemovalFailed
    }
}

function Invoke-MockedPackageLifecycle {
    $script:mockPackageState = [Collections.Generic.List[object]]::new()
    $script:mockRemovedPackages = [Collections.Generic.List[string]]::new()
    $script:mockPackageOwnership = [Collections.Generic.List[object]]::new()
    $script:mockPackageValidationFailed = $false
    $script:mockPackageCandidate = [pscustomobject]@{
        Name = 'Example.CnMon'
        Publisher = 'CN=Example, O=Example'
        PackageFamilyName = 'Example.CnMon_518g7wxbxvqf6'
        Version = '1.3.0.0'
        PackageFullName = 'Example.CnMon_1.3.0.0_x64__518g7wxbxvqf6'
    }
    $script:mockPackagePreexisting = [pscustomobject]@{
        Name = 'Example.CnMon'
        Publisher = 'CN=Other Publisher'
        PackageFamilyName = 'Example.CnMon_otherfamily'
        Version = '1.3.0.0'
        PackageFullName = 'Example.CnMon_1.3.0.0_x64__otherfamily'
    }
    $script:mockPackageState.Add($script:mockPackagePreexisting)

    function Add-AppxPackage {
        [CmdletBinding()]
        param([string]$Path)
        $script:mockPackageState.Add($script:mockPackageCandidate)
    }
    function Get-AppxPackage {
        [CmdletBinding()]
        param([string]$Name)
        return @($script:mockPackageState | Where-Object { [string]$_.Name -ceq $Name })
    }
    function Remove-AppxPackage {
        [CmdletBinding()]
        param([string]$Package)
        $script:mockRemovedPackages.Add($Package)
        $matches = @($script:mockPackageState | Where-Object { [string]$_.PackageFullName -ceq $Package })
        foreach ($match in $matches) { $null = $script:mockPackageState.Remove($match) }
    }

    try {
        $identity = [pscustomobject]@{
            Name = 'Example.CnMon'
            Publisher = 'CN=Example, O=Example'
            ExpectedPFN = 'Example.CnMon_518g7wxbxvqf6'
        }
        Add-CnMonMsixOwnedPackage -Identity $identity -Version '1.3.0.0' -PackagePath 'candidate.msix' -Ownership $script:mockPackageOwnership
        throw 'simulated validation failure after successful package registration'
    }
    catch { $script:mockPackageValidationFailed = $true }

    try { Remove-CnMonMsixOwnedPackages -Ownership $script:mockPackageOwnership }
    catch { throw 'mock package cleanup unexpectedly failed' }
    return [pscustomobject]@{
        ValidationFailed = $script:mockPackageValidationFailed
        Ownership = $script:mockPackageOwnership
        Removed = @($script:mockRemovedPackages)
        Remaining = @($script:mockPackageState)
        Candidate = $script:mockPackageCandidate
        Preexisting = $script:mockPackagePreexisting
    }
}

function Invoke-MockedPartialUserCleanup {
    $script:partialUserCleanupCalls = [Collections.Generic.List[string]]::new()

    function Get-AppxPackage {
        [CmdletBinding()]
        param([string]$User, [string]$Name)
        return @()
    }
    function Remove-AppxPackage {
        [CmdletBinding()]
        param([string]$Package, [string]$User)
        throw 'unexpected package removal in partial-user cleanup fixture'
    }
    function Remove-LocalUser {
        [CmdletBinding()]
        param([string]$Name)
        $script:partialUserCleanupCalls.Add("user:$Name")
    }
    function Get-CimInstance {
        [CmdletBinding()]
        param([string]$ClassName, [string]$Filter)
        $script:partialUserCleanupCalls.Add("profile-query:$Filter")
        return [pscustomobject]@{ Special = $false; LocalPath = 'mock-profile' }
    }
    function Remove-CimInstance {
        [CmdletBinding()]
        param([object]$InputObject)
        $script:partialUserCleanupCalls.Add("profile:$($InputObject.LocalPath)")
    }

    Remove-CnMonMsixTemporaryUser -Name 'owned-user' -Sid 'S-1-5-21-123-456-789-1001' -IdentityName 'Example.CnMon'
    return @($script:partialUserCleanupCalls)
}

$repoRoot = Split-Path -Parent $PSScriptRoot
$targetPath = Join-Path $PSScriptRoot 'test-msix-windows.ps1'
$modulePath = Join-Path $PSScriptRoot 'msix-package.psm1'
$ciHelperPath = Join-Path $PSScriptRoot 'msix-ci.ps1'
$goTestPath = Join-Path $repoRoot 'cmd/cn-mon'
$parseErrors = $null
$sourceAst = [System.Management.Automation.Language.Parser]::ParseFile($targetPath, [ref]$null, [ref]$parseErrors)
if ($parseErrors.Count -gt 0) {
    throw "Task2 harness did not parse: $($parseErrors[0].Message)"
}
$moduleParseErrors = $null
$moduleAst = [System.Management.Automation.Language.Parser]::ParseFile($modulePath, [ref]$null, [ref]$moduleParseErrors)
if ($moduleParseErrors.Count -gt 0) {
    throw "packaging module did not parse: $($moduleParseErrors[0].Message)"
}
$ciParseErrors = $null
$ciAst = [System.Management.Automation.Language.Parser]::ParseFile($ciHelperPath, [ref]$null, [ref]$ciParseErrors)
if ($ciParseErrors.Count -gt 0) {
    throw "CI helper did not parse: $($ciParseErrors[0].Message)"
}

# Import only pure helper definitions; the qualification script's top-level entry point is never executed.
$requiredFunctions = @(
    'Set-CnMonMsixGate',
    'Get-CnMonMsixNativeSelector',
    'Merge-CnMonMsixResultGates',
    'Get-CnMonMsixResultEvidence',
    'Assert-CnMonMsixResultEvidence',
    'Assert-CnMonMsixUserResultBinding',
    'Read-CnMonMsixHostedResultSnapshot',
    'Get-CnMonMsixWackStatus',
    'Get-CnMonMsixWackStructureDiagnostic',
    'Save-CnMonMsixWackStructureDiagnostic',
    'Get-CnMonMsixTrustAction',
    'Test-CnMonMsixAdministratorsMembership',
    'Remove-CnMonMsixOwnedCertificate',
    'Add-CnMonMsixOwnedPackage',
    'Remove-CnMonMsixOwnedPackages',
    'Assert-CnMonMsixTargetsAbsent',
    'Get-CnMonMsixProfileTargets',
    'Invoke-CnMonMsixNativeTests',
    'Get-CnMonMsixSignedPayloadExpectedFiles',
    'Get-CnMonMsixDiagnosticErrorCategory',
    'Get-CnMonMsixFailureDiagnostic',
    'Get-CnMonMsixNativeTestFailureDiagnostic',
    'Save-CnMonMsixQualificationFailureEvidence',
    'Set-CnMonMsixCaughtGateFailure',
    'Remove-CnMonMsixTemporaryUser',
    'Assert-CnMonMsixPackagePayload',
    'Assert-CnMonMsixSignature',
    'Get-CnMonMsixSyntheticPrDiagnosticGuard',
    'Test-CnMonMsixSyntheticPrDiagnostic',
    'Get-CnMonMsixSignatureDiagnostic',
    'Save-CnMonMsixSignatureDiagnostic',
    'Save-CnMonMsixSignatureDiagnosticStatus',
    'Save-CnMonMsixSignatureFailureEvidence'
)
$functionAsts = @($sourceAst.FindAll({
    param($candidate)
    $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
        $requiredFunctions -contains $candidate.Name
}, $true))
$fixtureFilesFunctionAsts = @($sourceAst.FindAll({
    param($candidate)
    $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
        $candidate.Name -eq 'Get-CnMonMsixFixtureFiles'
}, $true))
$ciStatusFunctionAsts = @($ciAst.FindAll({
    param($candidate)
    $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
        $candidate.Name -eq 'Write-CnMonMsixSignatureDiagnosticStatus'
}, $true))
$ciResultsFunctionAsts = @($ciAst.FindAll({
    param($candidate)
    $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
        $candidate.Name -eq 'Read-NativeResults'
}, $true))
$ciGateOutputFunctionAsts = @($ciAst.FindAll({
    param($candidate)
    $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
        $candidate.Name -eq 'Write-GateOutput'
}, $true))
if ($ciStatusFunctionAsts.Count -ne 1) {
    throw 'safe CI diagnostic status writer is missing'
}
if ($ciResultsFunctionAsts.Count -ne 1) {
    throw 'schema-only native result reader is missing'
}
if ($ciGateOutputFunctionAsts.Count -ne 1) {
    throw 'validated WACK gate output writer is missing'
}
$moduleHelperNames = @('Get-CnMonMsixSha256', 'Test-CnMonMsixHash', 'Assert-CnMonMsixPayload')
$moduleFunctionAsts = @($moduleAst.FindAll({
    param($candidate)
    $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
        $moduleHelperNames -contains $candidate.Name
}, $true))
$missingModuleHelpers = @($moduleHelperNames | Where-Object { $_ -notin @($moduleFunctionAsts | ForEach-Object Name) })
if ($missingModuleHelpers.Count -gt 0) {
    throw "payload assertion helper contract is not available: $($missingModuleHelpers -join ', ')"
}
$foundFunctions = @($functionAsts | ForEach-Object Name)
$missingFunctions = @($requiredFunctions | Where-Object { $_ -notin $foundFunctions })
if ($missingFunctions.Count -gt 0) {
    throw "Task2 pure helper contract is not available yet: $($missingFunctions -join ', ')"
}

$tempRoot = Join-Path ([IO.Path]::GetTempPath()) ('cn-mon-msix-harness-test-' + [Guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $tempRoot
$helperPath = Join-Path $tempRoot 'extracted-helpers.ps1'
$helperText = (@($functionAsts) + @($fixtureFilesFunctionAsts) + @($moduleFunctionAsts) + @($ciStatusFunctionAsts) + @($ciResultsFunctionAsts) + @($ciGateOutputFunctionAsts) | ForEach-Object { $_.Extent.Text }) -join [Environment]::NewLine
[IO.File]::WriteAllText($helperPath, $helperText, [Text.UTF8Encoding]::new($false))
Import-Module $modulePath -Force -ErrorAction Stop
$qualificationGates = @('identity', 'package_hash', 'signature', 'account', 'package_registration', 'exact_identity', 'installed_binary_hash', 'alias_identity', 'native_cli', 'icmp', 'history_config', 'shell_aliases', 'upgrade', 'repair', 'uninstall_reinstall', 'conflicts', 'fail_injection', 'cleanup', 'wack')
. $helperPath
$script:MakeAppxFootprint = @('[Content_Types].xml', 'AppxBlockMap.xml')
$ArtifactDirectory = $tempRoot

$passed = 0
$failures = [Collections.Generic.List[string]]::new()
function Invoke-Check {
    param([string]$Name, [scriptblock]$Action)
    try {
        & $Action
        $script:passed++
        Write-Host "PASS $Name"
    }
    catch {
        $script:failures.Add("$Name`: $($_.Exception.Message)")
        Write-Host "FAIL $Name`: $($_.Exception.Message)"
    }
}

try {
    Invoke-Check 'native selectors are anchored and select real Go test names' {
        $goTests = @(
            Get-ChildItem -LiteralPath $goTestPath -Filter '*_test.go' -File |
                ForEach-Object { Get-Content -LiteralPath $_.FullName } |
                ForEach-Object { if ($_ -match '^\s*func\s+(Test\w+)\s*\(') { $Matches[1] } }
        )
        Assert-True ($goTests.Count -gt 0) 'no actual Go test names were found'

        $direct = Get-CnMonMsixNativeSelector 'TestNativeMSIXInstalledAlias'
        Assert-Equal $direct '^TestNativeMSIXInstalledAlias$' 'already-prefixed selector changed'
        $grouped = Get-CnMonMsixNativeSelector 'NativeMSIX(InstalledAlias|ResolvedCommandIdentity)'
        Assert-Equal $grouped '^Test(NativeMSIX(InstalledAlias|ResolvedCommandIdentity))$' 'selector prefix was not anchored'

        $calls = Get-CommandNodes $sourceAst 'Invoke-CnMonMsixNativeTests'
        $patterns = [Collections.Generic.List[string]]::new()
        foreach ($call in $calls) {
            if ($call.CommandElements.Count -lt 4) { continue }
            $patternArg = $call.CommandElements[3]
            if ($patternArg -is [System.Management.Automation.Language.StringConstantExpressionAst]) {
                $patterns.Add($patternArg.Value)
            }
            elseif ($patternArg -is [System.Management.Automation.Language.VariableExpressionAst] -and $patternArg.VariablePath.UserPath -eq 'suite') {
                $suiteAssignment = $sourceAst.FindAll({
                    param($candidate)
                    $candidate -is [System.Management.Automation.Language.AssignmentStatementAst] -and
                        $candidate.Left -is [System.Management.Automation.Language.VariableExpressionAst] -and
                        $candidate.Left.VariablePath.UserPath -eq 'suite' -and
                        $candidate.Right -is [System.Management.Automation.Language.StringConstantExpressionAst]
                }, $true) | Select-Object -First 1
                if ($suiteAssignment) { $patterns.Add($suiteAssignment.Right.Value) }
            }
        }
        Assert-True ($patterns.Count -gt 0) 'no native test selectors were found at call sites'
        foreach ($pattern in $patterns) {
            $selector = Get-CnMonMsixNativeSelector $pattern
            Assert-True ($selector.StartsWith('^Test', [StringComparison]::Ordinal) -and $selector.EndsWith('$', [StringComparison]::Ordinal)) "selector is not anchored to Test names: $selector"
            $selected = @($goTests | Where-Object { $_ -match $selector })
            Assert-True ($selected.Count -gt 0) "selector matches no actual Go tests: $selector"
            Assert-True (@($selected | Where-Object { -not $_.StartsWith('Test', [StringComparison]::Ordinal) }).Count -eq 0) "selector matched a non-Test Go function: $selector"
        }
    }

    Invoke-Check 'zero selected native tests fail even when the child exits zero' {
        $fakeExe = Join-Path $tempRoot $(if ($IsWindows) { 'fake-test.exe.cmd' } else { 'fake-test.sh' })
        if ($IsWindows) {
            $fakeContent = @'
@echo off
if /i "%~1"=="-test.list" goto list
echo CNMON_HARNESS_MARKER=passed
exit /b 0
:list
if "%CNMON_HARNESS_EMPTY_LIST%"=="1" exit /b 0
echo TestNativeMSIXFixtureAdjacentConfig
exit /b 0
'@
        }
        else {
            $fakeContent = @'
#!/usr/bin/env bash
case "$1" in
  -test.list=*)
    if [[ "${CNMON_HARNESS_EMPTY_LIST:-}" != "1" ]]; then echo TestNativeMSIXFixtureAdjacentConfig; fi
    ;;
  *) echo CNMON_HARNESS_MARKER=passed ;;
esac
exit 0
'@
        }
        [IO.File]::WriteAllText($fakeExe, $fakeContent, [Text.UTF8Encoding]::new($false))
        if (-not $IsWindows) {
            [IO.File]::SetUnixFileMode($fakeExe, [IO.UnixFileMode]::UserRead -bor [IO.UnixFileMode]::UserWrite -bor [IO.UnixFileMode]::UserExecute)
        }

        $oldEmpty = $env:CNMON_HARNESS_EMPTY_LIST
        try {
            $env:CNMON_HARNESS_EMPTY_LIST = '1'
            Assert-Throws { Invoke-CnMonMsixNativeTests -Name 'empty-list' -TestExe $fakeExe -Pattern 'TestNativeMSIXFixtureAdjacentConfig' -RequiredMarkers @('CNMON_HARNESS_MARKER=passed') } 'zero-match child incorrectly passed native qualification'
            Remove-Item Env:CNMON_HARNESS_EMPTY_LIST -ErrorAction SilentlyContinue
            $output = Invoke-CnMonMsixNativeTests -Name 'known-test' -TestExe $fakeExe -Pattern 'TestNativeMSIXFixtureAdjacentConfig' -RequiredMarkers @('CNMON_HARNESS_MARKER=passed')
            Assert-True ($output -match 'CNMON_HARNESS_MARKER=passed') 'known selected test did not preserve its required witness'
        }
        finally {
            if ($null -eq $oldEmpty) { Remove-Item Env:CNMON_HARNESS_EMPTY_LIST -ErrorAction SilentlyContinue }
            else { $env:CNMON_HARNESS_EMPTY_LIST = $oldEmpty }
        }
    }

    Invoke-Check 'profile cleanup targets include all real and CLI paths and preflight preserves existing data' {
        $profile = Join-Path $tempRoot 'User Profile With Spaces'
        $local = Join-Path $tempRoot 'Local AppData With Spaces'
        $fixtureHistory = Join-Path $tempRoot 'fixture-history.db'
        $fixtureLog = Join-Path $tempRoot 'fixture.log'
        $targets = Get-CnMonMsixProfileTargets -ProfilePath $profile -LocalAppData $local -FixtureHistory $fixtureHistory -FixtureLog $fixtureLog
        foreach ($field in @('ProfileConfig', 'ProfileHistoryDefault', 'ProfileLogDefault', 'ProfileHistory', 'ProfileLog', 'CliHistory', 'CliLog', 'FixtureHistory', 'FixtureLog')) {
            Assert-True (-not [string]::IsNullOrWhiteSpace([string]$targets.$field)) "cleanup target '$field' is empty"
        }
        Assert-Equal ([IO.Path]::GetFullPath($targets.ProfileConfig)) ([IO.Path]::GetFullPath((Join-Path $profile '.cn'))) 'profile config path was not derived from the actual profile root'
        Assert-Equal ([IO.Path]::GetFullPath($targets.ProfileHistory)) ([IO.Path]::GetFullPath((Join-Path $profile 'msix-profile-history.db'))) 'profile history path was not derived from the actual user profile'
        Assert-Equal ([IO.Path]::GetFullPath($targets.ProfileLog)) ([IO.Path]::GetFullPath((Join-Path $profile 'msix-profile.log'))) 'profile log path was not derived from the actual user profile'
        Assert-Equal ([IO.Path]::GetFullPath($targets.CliHistory)) ([IO.Path]::GetFullPath((Join-Path $profile 'msix-cli-history.db'))) 'CLI history override path was not derived from the actual user profile'
        Assert-Equal ([IO.Path]::GetFullPath($targets.CliLog)) ([IO.Path]::GetFullPath((Join-Path $profile 'msix-cli.log'))) 'CLI log override path was not derived from the actual user profile'
        Assert-Equal $targets.All.Count 9 'cleanup preflight omitted an owned path'
        Assert-True (@($targets.All | Select-Object -Unique).Count -eq 9) 'cleanup target list contains duplicate destinations'

        $null = New-Item -ItemType Directory -Path $profile -Force
        $sentinel = [IO.Path]::GetFullPath($targets.ProfileConfig)
        $sentinelContents = 'preserve this user data'
        [IO.File]::WriteAllText($sentinel, $sentinelContents)
        Assert-Throws { Assert-CnMonMsixTargetsAbsent -Paths $targets.All } 'preflight accepted a pre-existing profile target'
        Assert-Equal ([IO.File]::ReadAllText($sentinel)) $sentinelContents 'preflight modified or deleted pre-existing user data'
        foreach ($path in $targets.All | Where-Object { [IO.Path]::GetFullPath($_) -cne $sentinel }) {
            Assert-True (-not (Test-Path -LiteralPath $path)) "preflight created a cleanup target: $path"
        }

        $users = @($sourceAst.FindAll({
            param($candidate)
            $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
                $candidate.Name -match '^Invoke-CnMonMsixUser'
        }, $true))
        $targetSetupFunctions = @($users | Where-Object { (Get-CommandNodes $_ 'Get-CnMonMsixProfileTargets').Count -gt 0 })
        Assert-Equal $targetSetupFunctions.Count 1 'User mode must derive its target set in one setup function'
        $targetCalls = Get-CommandNodes $targetSetupFunctions[0] 'Get-CnMonMsixProfileTargets'
        Assert-Equal $targetCalls.Count 1 'User mode must derive its target set once per run'
        $preflightCalls = Get-CommandNodes $targetSetupFunctions[0] 'Assert-CnMonMsixTargetsAbsent'
        Assert-Equal $preflightCalls.Count 1 'User mode must preflight the derived target set once'
        Assert-True ($targetSetupFunctions[0].Extent.Text -match '\$env:USERPROFILE' -and $targetSetupFunctions[0].Extent.Text -match '\$env:LOCALAPPDATA') 'User mode does not derive targets from the loaded profile environment'
        Assert-True ($preflightCalls[0].Extent.Text -match '\.All\b') 'User mode does not preflight the complete owned target list'

        $runEnvFunctions = @($sourceAst.FindAll({
            param($candidate)
            $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
                $candidate.Name -eq 'Invoke-CnMonMsixUserRun'
        }, $true))
        Assert-Equal $runEnvFunctions.Count 1 'User runner environment helper is missing or ambiguous'
        $runEnvText = $runEnvFunctions[0].Extent.Text
        foreach ($assignment in @(
            "CNMON_MSIX_PROFILE_HISTORY = (Join-Path `$profile 'msix-profile-history.db')",
            "CNMON_MSIX_PROFILE_LOG = (Join-Path `$profile 'msix-profile.log')",
            "CNMON_MSIX_CLI_HISTORY = (Join-Path `$profile 'msix-cli-history.db')",
            "CNMON_MSIX_CLI_LOG = (Join-Path `$profile 'msix-cli.log')"
        )) {
            Assert-True ($runEnvText.Contains($assignment)) "User runner does not direct durable output outside AppData: $assignment"
        }
        Assert-True ($targetSetupFunctions[0].Extent.Text.Contains('$profileHistory = Join-Path $env:USERPROFILE')) 'real-profile configuration does not store its history outside AppData'
        Assert-True ($targetSetupFunctions[0].Extent.Text.Contains('$profileLog = Join-Path $env:USERPROFILE')) 'real-profile configuration does not store its log outside AppData'
    }

    Invoke-Check 'AppData observations use the staged package lifecycle test without entering durable cleanup targets' {
        $calls = Get-CommandNodes $sourceAst 'Invoke-CnMonMsixNativeTests'
        $staged = @($calls | Where-Object {
            $_.CommandElements.Count -gt 1 -and
                $_.CommandElements[1] -is [System.Management.Automation.Language.StringConstantExpressionAst] -and
                $_.CommandElements[1].Value -like 'appdata-*'
        })
        $expectedStages = @('appdata-seed', 'appdata-upgrade-readback', 'appdata-repair-readback', 'appdata-reinstall-fresh')
        Assert-Equal $staged.Count $expectedStages.Count 'AppData lifecycle test stage count changed'
        foreach ($stage in $expectedStages) {
            $call = @($staged | Where-Object { $_.CommandElements[1].Value -ceq $stage })
            Assert-Equal $call.Count 1 "AppData lifecycle stage '$stage' is missing or duplicated"
            Assert-Equal $call[0].CommandElements[3].Value 'NativeMSIXAppDataLifecycle' "AppData stage '$stage' selected a different native test"
        }
        $userMode = @($sourceAst.FindAll({
            param($candidate)
            $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $candidate.Name -eq 'Invoke-CnMonMsixUserMode'
        }, $true))[0]
        $stageGates = @{
            'appdata-seed' = 'history_config'
            'appdata-upgrade-readback' = 'history_config'
            'appdata-repair-readback' = 'repair'
            'appdata-reinstall-fresh' = 'uninstall_reinstall'
        }
        $userModeCalls = Get-CommandNodes $userMode 'Invoke-CnMonMsixNativeTests'
        foreach ($stage in $expectedStages) {
            $stageCall = @($userModeCalls | Where-Object {
                $_.CommandElements.Count -gt 1 -and
                    $_.CommandElements[1] -is [System.Management.Automation.Language.StringConstantExpressionAst] -and
                    $_.CommandElements[1].Value -ceq $stage
            })[0]
            $priorGate = @($userMode.FindAll({
                param($candidate)
                $candidate -is [System.Management.Automation.Language.AssignmentStatementAst] -and
                    $candidate.Extent.Text -match '\$script:CurrentGate\s*=\s*'
            }, $true) | Where-Object { $_.Extent.StartOffset -lt $stageCall.Extent.StartOffset } | Sort-Object { $_.Extent.StartOffset } | Select-Object -Last 1)
            Assert-Equal $priorGate.Count 1 "AppData stage '$stage' has no gate assignment before its child test"
            $gateMatch = [regex]::Match($priorGate[0].Extent.Text, "CurrentGate\s*=\s*'([^']+)'")
            Assert-True $gateMatch.Success "AppData stage '$stage' gate assignment is not a fixed literal"
            Assert-Equal $gateMatch.Groups[1].Value $stageGates[$stage] "AppData stage '$stage' failure would be attributed to the wrong gate"
        }
        $repairReadback = @($userModeCalls | Where-Object {
            $_.CommandElements.Count -gt 1 -and $_.CommandElements[1].Value -ceq 'appdata-repair-readback'
        })[0]
        $repairRetained = @((Get-CommandNodes $userMode 'Assert-CnMonMsixRetainedFiles') | Where-Object {
            $_.Extent.StartOffset -lt $repairReadback.Extent.StartOffset -and $_.Extent.Text -match '\$retained\b'
        } | Sort-Object { $_.Extent.StartOffset } | Select-Object -Last 1)
        Assert-Equal $repairRetained.Count 1 'repair no longer checks durable file retention before AppData readback'
        $repairInstalledCheck = @((Get-CommandNodes $userMode 'Assert-CnMonMsixInstalledPackage') | Where-Object {
            $_.Extent.StartOffset -lt $repairRetained[0].Extent.StartOffset
        } | Sort-Object { $_.Extent.StartOffset } | Select-Object -Last 1)
        Assert-Equal $repairInstalledCheck.Count 1 'repair package validation is missing before durable retention'
        $repairGateReset = @($userMode.FindAll({
            param($candidate)
            $candidate -is [System.Management.Automation.Language.AssignmentStatementAst] -and
                $candidate.Extent.Text -match '\$script:CurrentGate\s*=\s*''repair'''
        }, $true) | Where-Object {
            $_.Extent.StartOffset -gt $repairInstalledCheck[0].Extent.StartOffset -and
                $_.Extent.StartOffset -lt $repairRetained[0].Extent.StartOffset
        } | Sort-Object { $_.Extent.StartOffset } | Select-Object -Last 1)
        Assert-Equal $repairGateReset.Count 1 'repair gate is not reset after package validation and before durable retention assertion'
        $targetHelper = $functionAsts | Where-Object Name -eq 'Get-CnMonMsixProfileTargets' | Select-Object -First 1
        Assert-True ($targetHelper.Extent.Text -notmatch 'msix-appdata') 'test-only AppData outputs were added to durable profile cleanup targets'
    }

    Invoke-Check 'Certify overlays touched gates without erasing Hosted failures' {
        $hosted = [ordered]@{ identity = 'pass'; package_hash = 'incomplete'; signature = 'fail'; account = 'fail'; cleanup = 'pass'; native_cli = 'pass'; wack = 'incomplete' }
        $script:Result = [pscustomobject]@{ gates = [ordered]@{} }
        $script:GateUpdates = [ordered]@{}
        Set-CnMonMsixGate 'signature' 'fail'
        Set-CnMonMsixGate 'signature' 'pass'
        Set-CnMonMsixGate 'package_hash' 'pass'
        Set-CnMonMsixGate 'account' 'pass'
        Set-CnMonMsixGate 'cleanup' 'incomplete'
        Set-CnMonMsixGate 'native_cli' 'fail'
        Set-CnMonMsixGate 'wack' 'incomplete'
        $mergeCalls = Get-CommandNodes $sourceAst 'Merge-CnMonMsixResultGates'
        Assert-Equal $mergeCalls.Count 1 'Certify finalization must merge the Hosted result exactly once'
        Assert-True ($mergeCalls[0].Extent.Text -match '\$script:HostedResultSnapshot\.Results\.gates\s+\$script:GateUpdates') 'Certify does not merge only its touched gates into the snapshotted Hosted map'
        $merged = Merge-CnMonMsixResultGates -HostedGates $hosted -CertifyUpdates $script:GateUpdates
        Assert-Equal $merged.signature 'fail' 'Certify erased a signature failure'
        Assert-Equal $merged.package_hash 'incomplete' 'Certify improved an incomplete package validation gate'
        Assert-Equal $merged.account 'fail' 'Certify changed the Hosted ordinary-user account result'
        Assert-Equal $merged.cleanup 'incomplete' 'Certify did not preserve the worse cleanup status'
        Assert-Equal $merged.identity 'pass' 'Certify erased an untouched Hosted gate'
        Assert-Equal $merged.native_cli 'pass' 'Certify changed an unrelated Hosted native gate'
        Assert-Equal $merged.wack 'incomplete' 'Certify did not merge its actual WACK gate'
        Assert-Equal $hosted.signature 'fail' 'merge modified the Hosted input map'
        Assert-Equal $hosted.cleanup 'pass' 'merge modified the Hosted input map'
    }

    Invoke-Check 'result evidence binds both signed files and source revision before certification' {
        $directory = Join-Path $tempRoot 'result-evidence-binding'
        $null = New-Item -ItemType Directory -Path $directory
        $package = Join-Path $directory 'signed.msix'
        $receiptPath = Join-Path $directory 'signed-receipt.json'
        $resultsPath = Join-Path $directory 'hosted-results.json'
        [IO.File]::WriteAllBytes($package, [byte[]](1, 2, 3, 4, 5))
        $packageHash = (Get-FileHash -LiteralPath $package -Algorithm SHA256).Hash.ToLowerInvariant()
        $revision = 'a' * 40
        $receipt = [ordered]@{
            purpose = 'candidate'; package_sha256 = $packageHash; packaging_revision = $revision
            signing = [ordered]@{ status = 'test'; signed_package_sha256 = $packageHash }
        }
        $receiptJson = ConvertTo-Json -InputObject $receipt -Compress
        [IO.File]::WriteAllText($receiptPath, $receiptJson, [Text.UTF8Encoding]::new($false))
        $evidence = Get-CnMonMsixResultEvidence -PackagePath $package -ReceiptPath $receiptPath -Receipt $receipt -SourceRevision $revision
        Assert-Equal $evidence.signed_package_sha256 $packageHash 'evidence did not bind signed package bytes'
        Assert-Equal $evidence.signed_receipt_sha256 ((Get-FileHash -LiteralPath $receiptPath -Algorithm SHA256).Hash.ToLowerInvariant()) 'evidence did not bind signed receipt bytes'
        Assert-Equal $evidence.packaging_revision $revision 'evidence did not bind the checkout revision'

        $gates = [ordered]@{}
        foreach ($gate in $qualificationGates) { $gates[$gate] = 'pass' }
        $gates.repair = 'incomplete'
        $gates.conflicts = 'incomplete'
        $hosted = [ordered]@{ schema_version = 2; mode = 'Hosted'; overall = 'incomplete'; evidence = $evidence; gates = $gates }
        [IO.File]::WriteAllText($resultsPath, (ConvertTo-Json -InputObject $hosted -Depth 5), [Text.UTF8Encoding]::new($false))
        $snapshot = Read-CnMonMsixHostedResultSnapshot -ResultsPath $resultsPath -PackagePath $package -ReceiptPath $receiptPath -SourceRevision $revision
        Assert-Equal $snapshot.Results.mode 'Hosted' 'valid bound Hosted result was not snapshotted'
        Assert-Equal $snapshot.Results.evidence.signed_receipt_sha256 $evidence.signed_receipt_sha256 'snapshot changed the evidence binding'
        [IO.File]::WriteAllText($resultsPath, '{"mutated":true}', [Text.UTF8Encoding]::new($false))
        Assert-Equal $snapshot.Results.gates.repair 'incomplete' 'certification snapshot reread the mutated Hosted file'

        $badCases = @(
            'changed package bytes', 'changed receipt bytes', 'wrong packaging revision', 'null evidence',
            'schema 1', 'string schema version', 'extra result field', 'extra evidence field',
            'uppercase mode', 'uppercase overall', 'wrong overall type', 'uppercase gate status', 'wrong gate status type',
            'array evidence hash'
        )
        foreach ($case in $badCases) {
            [IO.File]::WriteAllBytes($package, [byte[]](1, 2, 3, 4, 5))
            [IO.File]::WriteAllText($receiptPath, $receiptJson, [Text.UTF8Encoding]::new($false))
            $caseEvidence = [ordered]@{
                signed_package_sha256 = $evidence.signed_package_sha256
                signed_receipt_sha256 = $evidence.signed_receipt_sha256
                packaging_revision = $evidence.packaging_revision
            }
            $caseSchema = 2
            $caseExtra = $false
            $caseMode = 'Hosted'
            $caseOverall = 'incomplete'
            $caseGateStatus = 'incomplete'
            switch ($case) {
                'changed package bytes' { [IO.File]::AppendAllText($package, 'changed') }
                'changed receipt bytes' { [IO.File]::AppendAllText($receiptPath, ' ') }
                'wrong packaging revision' { $caseEvidence.packaging_revision = 'b' * 40 }
                'null evidence' { $caseEvidence = $null }
                'schema 1' { $caseSchema = 1 }
                'string schema version' { $caseSchema = '2' }
                'extra result field' { $caseExtra = $true }
                'extra evidence field' { $caseEvidence.unexpected = 'value' }
                'uppercase mode' { $caseMode = 'HOSTED' }
                'uppercase overall' { $caseOverall = 'PASS' }
                'wrong overall type' { $caseOverall = 1 }
                'uppercase gate status' { $caseGateStatus = 'PASS' }
                'wrong gate status type' { $caseGateStatus = 1 }
                'array evidence hash' { $caseEvidence.signed_package_sha256 = @($evidence.signed_package_sha256) }
            }
            $caseGates = [ordered]@{} + $gates
            if ($case -in @('uppercase gate status', 'wrong gate status type')) { $caseGates.identity = $caseGateStatus }
            $hosted = [ordered]@{ schema_version = $caseSchema; mode = $caseMode; overall = $caseOverall; evidence = $caseEvidence; gates = $caseGates }
            if ($caseExtra) { $hosted.unexpected = 'value' }
            [IO.File]::WriteAllText($resultsPath, (ConvertTo-Json -InputObject $hosted -Depth 5), [Text.UTF8Encoding]::new($false))
            $before = [IO.File]::ReadAllBytes($resultsPath)
            Assert-Throws { Read-CnMonMsixHostedResultSnapshot -ResultsPath $resultsPath -PackagePath $package -ReceiptPath $receiptPath -SourceRevision $revision } "Certify accepted $case"
            $after = [IO.File]::ReadAllBytes($resultsPath)
            Assert-Equal ([Convert]::ToBase64String($after)) ([Convert]::ToBase64String($before)) "rejected $case modified the Hosted result bytes"
        }
        $wrongChild = [ordered]@{ signed_package_sha256 = $evidence.signed_package_sha256; signed_receipt_sha256 = $evidence.signed_receipt_sha256; packaging_revision = 'c' * 40 }
        Assert-Throws { Assert-CnMonMsixResultEvidence -Evidence $wrongChild -ExpectedEvidence $evidence } 'Hosted accepted a User child bound to another revision'
    }

    Invoke-Check 'both User child results require exact schema 2 and signed evidence' {
        $expectedEvidence = [ordered]@{
            signed_package_sha256 = 'a' * 64
            signed_receipt_sha256 = 'b' * 64
            packaging_revision = 'c' * 40
        }
        $userGates = [ordered]@{}
        foreach ($gate in $qualificationGates) { $userGates[$gate] = 'pass' }
        $validUser = [ordered]@{ schema_version = [long]2; mode = 'User'; overall = 'pass'; evidence = $expectedEvidence; gates = $userGates }
        Assert-CnMonMsixUserResultBinding -Result $validUser -ExpectedEvidence $expectedEvidence

        $badKinds = @('unbound evidence', 'schema 1', 'string schema version', 'extra root key', 'uppercase gate status', 'extra gate', 'array evidence hash')
        foreach ($kind in $badKinds) {
            $case = ConvertFrom-Json -InputObject (ConvertTo-Json -InputObject $validUser -Depth 6 -Compress) -AsHashtable
            switch ($kind) {
                'unbound evidence' { $case.evidence.signed_receipt_sha256 = 'd' * 64 }
                'schema 1' { $case.schema_version = [long]1 }
                'string schema version' { $case.schema_version = '2' }
                'extra root key' { $case.extra = 'value' }
                'uppercase gate status' { $case.gates.account = 'PASS' }
                'extra gate' { $case.gates.extra = 'pass' }
                'array evidence hash' { $case.evidence.signed_package_sha256 = @($expectedEvidence.signed_package_sha256) }
            }
            Assert-Throws { Assert-CnMonMsixUserResultBinding -Result $case -ExpectedEvidence $expectedEvidence } "User child accepted $kind"
        }

        $hosted = @($sourceAst.FindAll({
            param($candidate)
            $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
                $candidate.Name -eq 'Invoke-CnMonMsixHostedMode'
        }, $true))[0]
        $validators = @(Get-CommandNodes $hosted 'Assert-CnMonMsixUserResultBinding')
        Assert-Equal $validators.Count 2 'Hosted does not validate both failure-injection and normal User results'
        Assert-True (@($validators | Where-Object { $_.Extent.Text -match '\$failureJson' }).Count -eq 1) 'failure-injection User result lacks evidence validation'
        Assert-True (@($validators | Where-Object { $_.Extent.Text -match '\$userJson' }).Count -eq 1) 'ordinary User result lacks evidence validation'
    }

    Invoke-Check 'Certify source revision failures stay inside the no-overwrite guard' {
        $certifyFunction = @($sourceAst.FindAll({
            param($candidate)
            $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
                $candidate.Name -eq 'Invoke-CnMonMsixCertifyMode'
        }, $true))[0]
        $guardedTry = @($certifyFunction.FindAll({
            param($candidate)
            $candidate -is [System.Management.Automation.Language.TryStatementAst] -and
                $candidate.Extent.Text -match 'Get-CnMonMsixSourceRevision' -and
                $candidate.Extent.Text -match 'Read-CnMonMsixHostedResultSnapshot' -and
                $candidate.Extent.Text -match '\$script:AllowResultWrite\s*=\s*\$false'
        }, $true))
        Assert-Equal $guardedTry.Count 1 'Certify can fail revision lookup outside the Hosted-file preservation guard'
        Assert-True ($sourceAst.Extent.Text -match '(?s)if\s*\(\$script:AllowResultWrite\).*?Write-CnMonMsixResult') 'Certify finalization does not honor the no-overwrite guard'
    }

    Invoke-Check 'synthetic WACK structure evidence is bounded and contains no raw XML values' {
        $keys = @('GITHUB_ACTIONS', 'GITHUB_EVENT_NAME', 'GITHUB_REPOSITORY', 'ImageOS', 'CNMON_MSIX_IDENTITY_NAME', 'CNMON_MSIX_IDENTITY_PUBLISHER', 'CNMON_MSIX_PUBLISHER_DISPLAY_NAME', 'CNMON_MSIX_EXPECTED_PFN', 'USERNAME', 'COMPUTERNAME')
        $old = @{}
        foreach ($key in $keys) { $old[$key] = [Environment]::GetEnvironmentVariable($key) }
        try {
            $env:GITHUB_ACTIONS = 'true'; $env:GITHUB_EVENT_NAME = 'pull_request'; $env:GITHUB_REPOSITORY = 'evdanil/cn-mon'; $env:ImageOS = 'win25'
            $env:CNMON_MSIX_IDENTITY_NAME = 'Example.CnMon'; $env:CNMON_MSIX_IDENTITY_PUBLISHER = 'CN=Example, O=Example'
            $env:CNMON_MSIX_PUBLISHER_DISPLAY_NAME = 'Example Publisher'; $env:CNMON_MSIX_EXPECTED_PFN = 'Example.CnMon_518g7wxbxvqf6'
            $env:USERNAME = 'AccountCanary'; $env:COMPUTERNAME = 'HostCanary'
            $identity = [pscustomobject]@{ Name = 'Example.CnMon'; Publisher = 'CN=Example, O=Example'; PublisherDisplayName = 'Example Publisher'; ExpectedPFN = 'Example.CnMon_518g7wxbxvqf6' }
            $report = Join-Path $tempRoot 'wack-structure.xml'
            [IO.File]::WriteAllText($report, '<REPORT OVERALL_RESULT="PASS" PARTIAL_RUN="FALSE" APP_NAME="Example.CnMon" APP_VERSION="1.3.0.0"><GROUP><TEST NAME="Private Test Description"><RESULT>PASS</RESULT><AccountCanaryTag>secret value</AccountCanaryTag><HostCanaryNode>secret host</HostCanaryNode></TEST></GROUP></REPORT>', [Text.UTF8Encoding]::new($false))
            $diagnostic = Get-CnMonMsixWackStructureDiagnostic -ReportPath $report -ExitCode 0 -ToolVersion '10.0.26100.8249' -Identity $identity -ExpectedVersion '1.3.0.0'
            Assert-True $diagnostic.report_present 'WACK structure diagnostic omitted report presence'
            Assert-Equal $diagnostic.phase 'test' 'WACK test report phase was not identified'
            Assert-True $diagnostic.xml_parse_success 'well-formed WACK report did not parse'
            Assert-True $diagnostic.unknown_schema 'unmeasured WACK structure was treated as known'
            Assert-True ($diagnostic.identity_value_matches -and $diagnostic.version_value_matches) 'diagnostic omitted boolean identity/version witnesses'
            Assert-Equal $diagnostic.report_sha256 ((Get-FileHash -LiteralPath $report -Algorithm SHA256).Hash.ToLowerInvariant()) 'diagnostic report hash did not match the report bytes'
            Assert-True (@($diagnostic.status_observations | Where-Object { $_.status -eq 'pass' }).Count -gt 0) 'recognized fixed status tokens were omitted'
            $json = ConvertTo-Json -InputObject $diagnostic -Depth 12
            foreach ($canary in @('Example.CnMon', 'CN=Example', 'Example Publisher', '518g7wxbxvqf6', '1.3.0.0', 'Private Test Description', 'AccountCanary', 'HostCanary', 'secret value', 'secret host')) {
                Assert-True (-not $json.Contains($canary)) "WACK diagnostic leaked raw XML or identity/account canary '$canary'"
            }

            $manual = Get-CnMonMsixWackStructureDiagnostic -ReportPath $report -ExitCode 0 -ToolVersion '10.0.26100.8249' -Identity $identity -ExpectedVersion '1.3.0.0' -Mode workflow_dispatch
            Assert-Equal $manual $null 'manual WACK run published synthetic-only structure evidence'

            $dtdReport = Join-Path $tempRoot 'wack-dtd.xml'
            [IO.File]::WriteAllText($dtdReport, '<!DOCTYPE REPORT [<!ENTITY leak SYSTEM "file:///private/CN=Canary">]><REPORT>&leak;</REPORT>', [Text.UTF8Encoding]::new($false))
            $dtd = Get-CnMonMsixWackStructureDiagnostic $dtdReport 0 '10.0.26100.8249' $identity '1.3.0.0'
            Assert-True (-not $dtd.xml_parse_success) 'DTD-bearing WACK report was parsed'
            Assert-True (-not (ConvertTo-Json $dtd -Depth 12).Contains('Canary')) 'DTD parser failure leaked external-entity data'

            $deepReport = Join-Path $tempRoot 'wack-deep.xml'
            $deep = ('<N>' * 40) + 'PASS' + ('</N>' * 40)
            [IO.File]::WriteAllText($deepReport, '<REPORT>' + $deep + '</REPORT>', [Text.UTF8Encoding]::new($false))
            $deepDiagnostic = Get-CnMonMsixWackStructureDiagnostic $deepReport 0 '10.0.26100.8249' $identity '1.3.0.0'
            Assert-True $deepDiagnostic.truncated 'deep WACK report was not bounded'

            $largeReport = Join-Path $tempRoot 'wack-large.xml'
            [IO.File]::WriteAllText($largeReport, '<REPORT>' + ('x' * 1048577) + '</REPORT>', [Text.UTF8Encoding]::new($false))
            $largeDiagnostic = Get-CnMonMsixWackStructureDiagnostic $largeReport 0 '10.0.26100.8249' $identity '1.3.0.0'
            Assert-True $largeDiagnostic.truncated 'oversized WACK report was not bounded'

            $missingReport = Join-Path $tempRoot 'wack-missing-report.xml'
            $unknownExit = Get-CnMonMsixWackStructureDiagnostic -ReportPath $missingReport -ExitCode $null -ToolVersion '10.0.26100.8249' -Identity $identity -ExpectedVersion '1.3.0.0' -Phase report_missing
            Assert-Equal $unknownExit.exit_code $null 'WACK invocation failure with no exit code was coerced to zero'
            Assert-Throws { Get-CnMonMsixWackStructureDiagnostic -ReportPath $missingReport -ExitCode 'unknown' -ToolVersion '10.0.26100.8249' -Identity $identity -ExpectedVersion '1.3.0.0' -Phase report_missing } 'non-numeric WACK exit code was accepted'

            $structurePath = Join-Path $ArtifactDirectory 'msix-wack-structure.json'
            Save-CnMonMsixWackStructureDiagnostic -ReportPath $missingReport -ExitCode 7 -ToolVersion '10.0.26100.8249' -Identity $identity -ExpectedVersion '1.3.0.0' -Phase reset
            $resetDiagnostic = Get-Content -LiteralPath $structurePath -Raw | ConvertFrom-Json
            Assert-Equal $resetDiagnostic.phase 'reset' 'reset failure diagnostic phase was not distinguished'
            Assert-Equal $resetDiagnostic.exit_code 7 'reset native exit code was not retained numerically'
            Assert-True (-not $resetDiagnostic.report_present) 'reset failure diagnostic claimed that a report existed'
            Remove-Item -LiteralPath $structurePath -Force
            Save-CnMonMsixWackStructureDiagnostic -ReportPath $missingReport -ExitCode 0 -ToolVersion '10.0.26100.8249' -Identity $identity -ExpectedVersion '1.3.0.0' -Phase report_missing
            $missingDiagnostic = Get-Content -LiteralPath $structurePath -Raw | ConvertFrom-Json
            Assert-Equal $missingDiagnostic.phase 'report_missing' 'missing report was not distinguished from reset/test'
            Remove-Item -LiteralPath $structurePath -Force
            $env:GITHUB_EVENT_NAME = 'workflow_dispatch'
            Save-CnMonMsixWackStructureDiagnostic -ReportPath $report -ExitCode 0 -ToolVersion '10.0.26100.8249' -Identity $identity -ExpectedVersion '1.3.0.0' -Phase test
            Assert-True (-not (Test-Path -LiteralPath $structurePath)) 'trusted manual WACK report produced a public structure diagnostic'
        }
        finally { foreach ($key in $keys) { [Environment]::SetEnvironmentVariable($key, $old[$key], 'Process') } }
    }

    Invoke-Check 'trust setup reuses existing trust and rejects non-hosted certificate imports' {
        Assert-Equal (Get-CnMonMsixTrustAction -HasTrustedCertificate $true -FreshHostedRunner $false) 'reuse' 'existing trusted certificate was not reused'
        Assert-Equal (Get-CnMonMsixTrustAction -HasTrustedCertificate $false -FreshHostedRunner $true) 'import' 'fresh hosted runner did not permit its owned public certificate import'
        Assert-Equal (Get-CnMonMsixTrustAction -HasTrustedCertificate $false -FreshHostedRunner $false) 'incomplete' 'untrusted non-hosted certificate was accepted'
    }

    Invoke-Check 'certificate cleanup attempts owned private-key removal after public-copy failure' {
        $ordinary = Invoke-MockedCertificateCleanup -RemoveTrustedCopy $true -DeletePrivateKey $false -FailTrustedCopy $false
        Assert-Equal $ordinary.Calls.Count 1 'ordinary cleanup should remove exactly the owned TrustedPeople copy'
        Assert-Equal $ordinary.Calls[0].LiteralPath 'Cert:\LocalMachine\TrustedPeople\0123456789ABCDEF0123456789ABCDEF01234567' 'ordinary cleanup targeted an unexpected store'
        Assert-True $ordinary.Calls[0].Force 'ordinary public-copy cleanup omitted force removal'
        Assert-True (-not $ordinary.Calls[0].DeleteKey) 'ordinary TrustedPeople cleanup tried to delete a private key'
        Assert-True (-not $ordinary.Failed) 'ordinary cleanup reported failure without an injected removal error'

        $failedTrustedCopy = Invoke-MockedCertificateCleanup -RemoveTrustedCopy $true -DeletePrivateKey $true -FailTrustedCopy $true
        Assert-True $failedTrustedCopy.Failed 'cleanup did not report the injected TrustedPeople removal failure'
        Assert-Equal $failedTrustedCopy.Calls.Count 2 'private-key cleanup was skipped after TrustedPeople removal failed'
        Assert-Equal $failedTrustedCopy.Calls[1].LiteralPath 'Cert:\LocalMachine\My\0123456789ABCDEF0123456789ABCDEF01234567' 'private-key cleanup targeted an unexpected store'
        Assert-True $failedTrustedCopy.Calls[1].Force 'private-key cleanup omitted force removal'
        Assert-True $failedTrustedCopy.Calls[1].DeleteKey 'private-key cleanup did not request deletion of the owned key'
    }

    Invoke-Check 'failed post-registration validation removes only the exact newly owned package' {
        $lifecycle = Invoke-MockedPackageLifecycle
        Assert-True $lifecycle.ValidationFailed 'the post-registration validation failure was not simulated'
        Assert-Equal $lifecycle.Ownership.Count 1 'registration did not record its ownership intent'
        Assert-True ($lifecycle.Ownership[0].PackageFullNames -contains $lifecycle.Candidate.PackageFullName) 'actual registered PackageFullName was not recorded before later validation'
        Assert-Equal $lifecycle.Removed.Count 1 'cleanup removed an unexpected number of registrations'
        Assert-Equal $lifecycle.Removed[0] $lifecycle.Candidate.PackageFullName 'cleanup did not remove the actual newly registered PackageFullName'
        Assert-Equal $lifecycle.Remaining.Count 1 'cleanup removed a preexisting unrelated registration'
        Assert-Equal $lifecycle.Remaining[0].PackageFullName $lifecycle.Preexisting.PackageFullName 'cleanup did not preserve the other-family registration'

        $userMode = @($sourceAst.FindAll({
            param($candidate)
            $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
                $candidate.Name -eq 'Invoke-CnMonMsixUserMode'
        }, $true))[0]
        $preflight = Get-CommandNodes $userMode 'Get-AppxPackage' | Where-Object { $_.Extent.Text -match '-Name \$identity\.Name' } | Select-Object -First 1
        $registration = Get-CommandNodes $userMode 'Add-CnMonMsixOwnedPackage' | Select-Object -First 1
        Assert-True ($null -ne $preflight -and $null -ne $registration) 'User mode is missing same-name preflight or owned registration'
        Assert-True ($preflight.Extent.StartOffset -lt $registration.Extent.StartOffset) 'owned package registration starts before preexisting same-name registrations are rejected'
        Assert-True ($userMode.Extent.Text -match 'qualification user already has a package with this name') 'User mode no longer rejects any preexisting same-name package before registration'
    }

    Invoke-Check 'fixture allowlist retains all candidate files and validates the actual payload exactly' {
        Assert-Equal $fixtureFilesFunctionAsts.Count 1 'full fixture file-map helper is not implemented'
        $sourceNames = @(
            'cn-mon.exe', 'README.md', 'LICENSE', 'THIRD_PARTY_NOTICES.md',
            'AppxManifest.xml', 'Assets/StoreLogo.png', 'Assets/Square44x44Logo.png',
            'Assets/Square150x150Logo.png', 'README-MSIX.md'
        )
        $candidateRoot = Join-Path $tempRoot 'candidate-payload'
        $fixtureRoot = Join-Path $tempRoot 'fixture-payload'
        $null = New-Item -ItemType Directory -Path $candidateRoot, $fixtureRoot
        $sourceFiles = [ordered]@{}
        $contents = @{}
        foreach ($name in $sourceNames) {
            $content = "candidate source bytes for $name"
            $contents[$name] = $content
            $candidatePath = Join-Path $candidateRoot $name
            $fixturePath = Join-Path $fixtureRoot $name
            $null = New-Item -ItemType Directory -Path (Split-Path -Parent $candidatePath) -Force
            $null = New-Item -ItemType Directory -Path (Split-Path -Parent $fixturePath) -Force
            [IO.File]::WriteAllText($candidatePath, $content, [Text.UTF8Encoding]::new($false))
            $fixtureContent = if ($name -ceq 'AppxManifest.xml') { '<manifest version="1.2.999.0" />' } else { $content }
            [IO.File]::WriteAllText($fixturePath, $fixtureContent, [Text.UTF8Encoding]::new($false))
            $sourceFiles[$name] = Get-CnMonMsixSha256 -Path $candidatePath
        }
        $binaryHash = [string]$sourceFiles['cn-mon.exe']
        $manifestHash = Get-CnMonMsixSha256 -Path (Join-Path $fixtureRoot 'AppxManifest.xml')
        $configContent = "[ping]`nhistory_file = fixture-history.db`n[logging]`nlogfile = fixture.log`n"
        [IO.File]::WriteAllText((Join-Path $fixtureRoot '.cn'), $configContent, [Text.UTF8Encoding]::new($false))
        $configHash = Get-CnMonMsixSha256 -Path (Join-Path $fixtureRoot '.cn')
        $expected = Get-CnMonMsixFixtureFiles -SourceFiles $sourceFiles -BinaryHash $binaryHash -ManifestHash $manifestHash -ConfigHash $configHash

        Assert-Equal $sourceFiles.Count 9 'source candidate fixture does not represent all nine package files'
        Assert-Equal $expected.Count 10 'fixture allowlist does not preserve nine source files plus .cn'
        foreach ($name in $sourceNames) {
            Assert-True ($expected.Contains($name)) "fixture allowlist omitted candidate file '$name'"
            if ($name -ceq 'AppxManifest.xml') {
                Assert-Equal $expected[$name] $manifestHash 'fixture did not override the source manifest hash'
                Assert-True ($expected[$name] -cne $sourceFiles[$name]) 'fixture retained the original manifest hash after changing its version'
            }
            else {
                Assert-Equal $expected[$name] $sourceFiles[$name] "fixture changed or omitted source payload hash '$name'"
            }
        }
        Assert-Equal $expected['cn-mon.exe'] $binaryHash 'fixture binary provenance changed'
        Assert-Equal $expected['.cn'] $configHash 'fixture did not add its generated configuration hash'

        Assert-CnMonMsixPayload -Root $fixtureRoot -ExpectedFiles $expected
        $unexpected = Join-Path $fixtureRoot 'unexpected.bin'
        [IO.File]::WriteAllText($unexpected, 'unexpected')
        Assert-ThrowsMatching { Assert-CnMonMsixPayload -Root $fixtureRoot -ExpectedFiles $expected } 'payload unexpected file' 'shared payload validation did not reject an unexpected fixture file'
        Remove-Item -LiteralPath $unexpected -Force
        $assetPath = Join-Path $fixtureRoot 'Assets/StoreLogo.png'
        [IO.File]::AppendAllText($assetPath, 'corruption')
        Assert-ThrowsMatching { Assert-CnMonMsixPayload -Root $fixtureRoot -ExpectedFiles $expected } 'payload hash mismatch' 'shared payload validation did not reject a corrupted retained asset'

        $fixtureWriter = @($sourceAst.FindAll({
            param($candidate)
            $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
                $candidate.Name -eq 'New-CnMonMsixFixture'
        }, $true))[0]
        $fixtureReader = @($sourceAst.FindAll({
            param($candidate)
            $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
                $candidate.Name -eq 'Assert-CnMonMsixFixtureReceipt'
        }, $true))[0]
        Assert-True ($fixtureWriter.Extent.Text -match "purpose\s*=\s*'test-fixture'") 'fixture receipt writer does not mark its artifact as test-fixture'
        Assert-True ($fixtureReader.Extent.Text -match "purpose\s*-cne\s*'test-fixture'") 'fixture receipt validator accepts a non-test purpose'
    }

    Invoke-Check 'signed payload accepts only the exact CodeIntegrity catalog footprint' {
        $sourceRoot = Join-Path $tempRoot 'signed-catalog-source'
        $null = New-Item -ItemType Directory -Path $sourceRoot
        $binaryPath = Join-Path $sourceRoot 'cn-mon.exe'
        $manifestPath = Join-Path $sourceRoot 'AppxManifest.xml'
        [IO.File]::WriteAllText($binaryPath, 'approved binary bytes', [Text.UTF8Encoding]::new($false))
        [IO.File]::WriteAllText($manifestPath, '<manifest />', [Text.UTF8Encoding]::new($false))
        $sourceFiles = [ordered]@{
            'cn-mon.exe' = Get-CnMonMsixSha256 $binaryPath
            'AppxManifest.xml' = Get-CnMonMsixSha256 $manifestPath
        }
        $signedRoot = Join-Path $tempRoot 'signed-catalog-package'
        $catalogDirectory = Join-Path $signedRoot 'AppxMetadata'
        $null = New-Item -ItemType Directory -Path $catalogDirectory -Force
        Copy-Item -LiteralPath $binaryPath, $manifestPath -Destination $signedRoot
        [IO.File]::WriteAllText((Join-Path $catalogDirectory 'CodeIntegrity.cat'), 'signature-bound catalog bytes', [Text.UTF8Encoding]::new($false))
        $signedExpected = Get-CnMonMsixSignedPayloadExpectedFiles -Root $signedRoot -ExpectedFiles $sourceFiles
        Assert-True $signedExpected.Contains('AppxMetadata/CodeIntegrity.cat') 'signed payload map omitted the exact catalog path'
        Assert-Equal $sourceFiles.Count 2 'signed expected-map helper changed the original receipt map'
        Assert-True (-not $sourceFiles.Contains('AppxMetadata/CodeIntegrity.cat')) 'signed expected-map helper mutated the source receipt map'
        Assert-CnMonMsixPackagePayload -Path $signedRoot -Files $sourceFiles -Signed
        Assert-ThrowsMatching { Assert-CnMonMsixPackagePayload -Path $signedRoot -Files $sourceFiles } 'payload unexpected file: AppxMetadata/CodeIntegrity.cat' 'unsigned wrapper unexpectedly allowed the signing catalog'

        $unsignedFootprintRoot = Join-Path $tempRoot 'signed-catalog-optional-package'
        $null = New-Item -ItemType Directory -Path $unsignedFootprintRoot
        Copy-Item -LiteralPath $binaryPath, $manifestPath -Destination $unsignedFootprintRoot
        $optionalCatalogExpected = Get-CnMonMsixSignedPayloadExpectedFiles -Root $unsignedFootprintRoot -ExpectedFiles $sourceFiles
        Assert-Equal $optionalCatalogExpected.Count $sourceFiles.Count 'signed expected map changed when the optional catalog was absent'
        Assert-True (-not $script:SignedCatalogFootprintPresent) 'missing optional catalog was reported as present'
        Assert-CnMonMsixPackagePayload -Path $unsignedFootprintRoot -Files $sourceFiles -Signed
        Assert-Equal $sourceFiles.Count 2 'signed validation without a catalog mutated the source receipt map'

        [IO.File]::WriteAllText((Join-Path $catalogDirectory 'unexpected.dat'), 'unexpected sibling', [Text.UTF8Encoding]::new($false))
        Assert-ThrowsMatching { Assert-CnMonMsixPackagePayload -Path $signedRoot -Files $sourceFiles -Signed } 'payload unexpected file: AppxMetadata/unexpected.dat' 'signed payload acceptance allowed an arbitrary AppxMetadata sibling'
        Remove-Item -LiteralPath (Join-Path $catalogDirectory 'unexpected.dat') -Force

        [IO.File]::AppendAllText($binaryPath, ' changed')
        Copy-Item -LiteralPath $binaryPath -Destination $signedRoot -Force
        Assert-ThrowsMatching { Assert-CnMonMsixPackagePayload -Path $signedRoot -Files $sourceFiles -Signed } 'payload hash mismatch: cn-mon.exe' 'signed payload acceptance weakened the source binary hash check'
    }

    Invoke-Check 'ordinary-user guard rejects Administrators SID even when token membership is deny-only' {
        Assert-True (Test-CnMonMsixAdministratorsMembership @('S-1-5-32-544')) 'Administrators group SID was not detected'
        Assert-True (-not (Test-CnMonMsixAdministratorsMembership @('S-1-5-32-545', 'S-1-5-32-546'))) 'ordinary built-in groups were treated as Administrators'
        $userGuard = @($sourceAst.FindAll({
            param($candidate)
            $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
                $candidate.Name -eq 'Assert-CnMonMsixOrdinaryUser'
        }, $true))[0]
        Assert-True ((Get-CommandNodes $userGuard 'Test-CnMonMsixAdministratorsMembership').Count -eq 1) 'ordinary-user guard does not reject deny-only Administrators SID membership'
    }

    Invoke-Check 'WACK status requires explicit complete success and preserves failures' {
        $report = Join-Path $tempRoot 'wack-report.xml'
        Write-WackFixture $report
        $pass = Get-CnMonMsixWackStatus -Path $report -ExitCode 0 -ExpectedName 'Example.CnMon' -ExpectedVersion '1.3.0.0'
        Assert-Equal $pass 'incomplete' 'unmeasured WACK per-test schema was incorrectly treated as success'

        Write-WackFixture $report -Overall 'FAIL'
        Assert-Equal (Get-CnMonMsixWackStatus $report 0 'Example.CnMon' '1.3.0.0') 'fail' 'explicit WACK failure was not preserved'
        Write-WackFixture $report -Overall 'WARNING'
        Assert-Equal (Get-CnMonMsixWackStatus $report 0 'Example.CnMon' '1.3.0.0') 'fail' 'WACK warning was incorrectly accepted'
        Write-WackFixture $report -Overall 'PASS' -TestResult 'FAIL'
        Assert-Equal (Get-CnMonMsixWackStatus $report 0 'Example.CnMon' '1.3.0.0') 'fail' 'per-test WACK failure was hidden by the overall PASS'
        Write-WackFixture $report -Overall 'PASS' -Partial 'TRUE'
        Assert-Equal (Get-CnMonMsixWackStatus $report 0 'Example.CnMon' '1.3.0.0') 'incomplete' 'partial WACK run was accepted'
        Write-WackFixture $report -Overall 'PASS' -TestResult 'UNKNOWN'
        Assert-Equal (Get-CnMonMsixWackStatus $report 0 'Example.CnMon' '1.3.0.0') 'incomplete' 'unknown WACK result was accepted'
        Write-WackFixture $report -Overall 'PASS' -Name 'Other.Package'
        Assert-Equal (Get-CnMonMsixWackStatus $report 0 'Example.CnMon' '1.3.0.0') 'fail' 'wrong package identity was accepted'
        Write-WackFixture $report -Overall 'PASS' -Version '9.9.9.9'
        Assert-Equal (Get-CnMonMsixWackStatus $report 0 'Example.CnMon' '1.3.0.0') 'fail' 'wrong package version was accepted'
        Write-WackFixture $report
        Assert-Equal (Get-CnMonMsixWackStatus $report 1 'Example.CnMon' '1.3.0.0') 'fail' 'nonzero WACK exit was accepted'
        [IO.File]::WriteAllText($report, '<REPORT OVERALL_RESULT="PASS"')
        Assert-Equal (Get-CnMonMsixWackStatus $report 0 'Example.CnMon' '1.3.0.0') 'incomplete' 'malformed WACK XML was accepted'
    }

    Invoke-Check 'Certify reads a valid WACK gate before applying pass-only acceptance' {
        $gateNames = @('identity', 'package_hash', 'signature', 'account', 'package_registration', 'exact_identity', 'installed_binary_hash', 'alias_identity', 'native_cli', 'icmp', 'history_config', 'shell_aliases', 'upgrade', 'repair', 'uninstall_reinstall', 'conflicts', 'fail_injection', 'cleanup', 'wack')
        $allowedGateStatuses = @('pass', 'fail', 'incomplete')
        $resultsPath = Join-Path $tempRoot 'certify-results.json'
        $gateMap = [ordered]@{}
        foreach ($name in $gateNames) { $gateMap[$name] = 'pass' }
        $gateMap.wack = 'fail'
        $result = [ordered]@{ schema_version = 2; mode = 'Certify'; overall = 'fail'; evidence = [ordered]@{ signed_package_sha256 = ('a' * 64); signed_receipt_sha256 = ('b' * 64); packaging_revision = ('c' * 40) }; gates = $gateMap }
        [IO.File]::WriteAllText($resultsPath, (ConvertTo-Json -InputObject $result -Depth 5), [Text.UTF8Encoding]::new($false))
        $failure = Read-NativeResults -ResultsPath $resultsPath -ExpectedMode Certify
        Assert-Equal $failure.gates.wack 'fail' 'valid WACK failure status was not extracted'
        $gateOutputPath = Join-Path $tempRoot 'certify-gate-output.txt'
        $env:GITHUB_OUTPUT = $gateOutputPath
        Write-GateOutput $failure.gates.wack
        Assert-Equal ([IO.File]::ReadAllText($gateOutputPath)) "gate_status=fail`n" 'valid WACK failure was not emitted to the step output'

        $gateMap.wack = 'incomplete'
        $result.overall = 'incomplete'
        [IO.File]::WriteAllText($resultsPath, (ConvertTo-Json -InputObject $result -Depth 5), [Text.UTF8Encoding]::new($false))
        $incomplete = Read-NativeResults -ResultsPath $resultsPath -ExpectedMode Certify
        Assert-Equal $incomplete.gates.wack 'incomplete' 'valid incomplete WACK status was not extracted'
        [IO.File]::WriteAllText($gateOutputPath, '', [Text.UTF8Encoding]::new($false))
        Write-GateOutput $incomplete.gates.wack
        Assert-Equal ([IO.File]::ReadAllText($gateOutputPath)) "gate_status=incomplete`n" 'valid incomplete WACK status was not emitted to the step output'

        $gateMap.Remove('repair')
        [IO.File]::WriteAllText($resultsPath, (ConvertTo-Json -InputObject $result -Depth 5), [Text.UTF8Encoding]::new($false))
        Assert-Throws { Read-NativeResults -ResultsPath $resultsPath -ExpectedMode Certify } 'partial native gate maps were accepted for WACK status extraction'
        [IO.File]::WriteAllText($resultsPath, '{', [Text.UTF8Encoding]::new($false))
        [IO.File]::WriteAllText($gateOutputPath, '', [Text.UTF8Encoding]::new($false))
        Assert-Throws {
            $invalid = Read-NativeResults -ResultsPath $resultsPath -ExpectedMode Certify
            Write-GateOutput $invalid.gates.wack
        } 'malformed native result JSON was accepted for WACK status extraction'
        Assert-Equal ([IO.File]::ReadAllText($gateOutputPath)) '' 'malformed native results emitted an unvalidated WACK gate'
    }

    Invoke-Check 'Test-Path boolean conditions execute with command-safe argument binding' {
        $conditions = @($sourceAst.FindAll({
            param($candidate)
            $candidate -is [System.Management.Automation.Language.BinaryExpressionAst] -and
                $candidate.Operator -eq [System.Management.Automation.Language.TokenKind]::Or -and
                $candidate.Extent.Text -match '(?i)Test-Path'
        }, $true))
        $evaluated = 0
        foreach ($condition in $conditions) {
            $commands = Get-CommandNodes $condition 'Test-Path'
            $allCommands = @($condition.FindAll({ param($candidate) $candidate -is [System.Management.Automation.Language.CommandAst] }, $true))
            if ($commands.Count -lt 2 -or $commands.Count -ne $allCommands.Count) { continue }
            $mockBody = [scriptblock]::Create("function Test-Path { param([string]`$LiteralPath, [string]`$PathType) return `$false }; [bool](`$($condition.Extent.Text))")
            $null = & $mockBody
            $evaluated++
        }
        Assert-True ($evaluated -gt 0) 'no multi-Test-Path Boolean condition was safely evaluated'
    }

    Invoke-Check 'new signer certificate is exported before validating its destination' {
        $certFunction = @($sourceAst.FindAll({
            param($candidate)
            $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
                $candidate.Name -eq 'Invoke-CnMonMsixCertifyMode'
        }, $true))[0]
        $export = (Get-CommandNodes $certFunction 'Export-Certificate' | Sort-Object { $_.Extent.StartOffset } | Select-Object -First 1)
        $destinationChecks = @(Get-CommandNodes $certFunction 'Test-Path' | Where-Object { $_.Extent.Text -match '\$publicCertPath' })
        Assert-True ($null -ne $export) 'Certify no longer exports its owned public signer certificate'
        foreach ($destinationCheck in $destinationChecks) {
            Assert-True ($export.Extent.StartOffset -lt $destinationCheck.Extent.StartOffset) 'certificate destination is checked before Export-Certificate creates it'
        }
        $certificateLoad = @($certFunction.FindAll({
            param($candidate)
            $candidate -is [System.Management.Automation.Language.InvokeMemberExpressionAst] -and
                $candidate.Extent.Text -match 'X509Certificate2' -and
                $candidate.Extent.Text -match '\$publicCertPath'
        }, $true)) | Sort-Object { $_.Extent.StartOffset } | Select-Object -First 1
        Assert-True ($null -ne $certificateLoad) 'Certify does not validate the newly exported public certificate'
        Assert-True ($export.Extent.StartOffset -lt $certificateLoad.Extent.StartOffset) 'certificate file is loaded before Export-Certificate creates it'
    }

    Invoke-Check 'Certify binds evidence first and attributes AppCert failures only to WACK' {
        $certifyFunction = @($sourceAst.FindAll({
            param($candidate)
            $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
                $candidate.Name -eq 'Invoke-CnMonMsixCertifyMode'
        }, $true))[0]
        $certifyText = $certifyFunction.Extent.Text
        $snapshot = Get-CommandNodes $certifyFunction 'Read-CnMonMsixHostedResultSnapshot' | Select-Object -First 1
        $trustImport = Get-CommandNodes $certifyFunction 'Import-Certificate' | Select-Object -First 1
        $appcertCall = Get-CommandNodes $certifyFunction 'Save-CnMonMsixWackStructureDiagnostic' | Select-Object -First 1
        Assert-True ($null -ne $snapshot -and $null -ne $trustImport -and $snapshot.Extent.StartOffset -lt $trustImport.Extent.StartOffset) 'Hosted evidence is not validated before Certify trust-store mutation'
        $diagnosticWriter = @($sourceAst.FindAll({
            param($candidate)
            $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
                $candidate.Name -eq 'Save-CnMonMsixWackStructureDiagnostic'
        }, $true))[0]
        Assert-True ($null -ne $appcertCall -and $null -ne $diagnosticWriter -and $diagnosticWriter.Extent.Text -match 'msix-wack-structure\.json') 'AppCert report does not use the bounded synthetic structure diagnostic'
        Assert-True ((Get-CommandNodes $certifyFunction 'Set-CnMonMsixGate' | Where-Object { $_.Extent.Text -match "'account'" }).Count -eq 0) 'Certify administrator/session prerequisites changed the ordinary account gate'
        Assert-True ($certifyText -match '(?s)Assert-CnMonMsixSignature.*?\$script:CurrentGate\s*=\s*\x27wack\x27') 'Certify did not switch attribution to WACK immediately after signature verification'
        $helpStart = $certifyText.IndexOf("& `$appcert '/?'")
        $resetStart = $certifyText.IndexOf('& $appcert reset', $helpStart)
        Assert-True ($helpStart -ge 0 -and $resetStart -gt $helpStart) 'AppCert help preflight or reset call is missing'
        $helpBlock = $certifyText.Substring($helpStart, $resetStart - $helpStart)
        Assert-True ($helpBlock -match '\$helpExitCode\s*=\s*\[int\]\$LASTEXITCODE') 'AppCert help exit was not retained for private diagnostics'
        Assert-True ($helpBlock -notmatch '(?i)\bif\b|throw|Set-CnMonMsixGate') 'AppCert help exit can block reset/test or determine the WACK gate'
        Assert-True ($certifyText.IndexOf('Read-CnMonMsixHostedResultSnapshot') -lt $certifyText.IndexOf('Get-AuthenticodeSignature')) 'Hosted snapshot is not captured before signature/trust checks'
    }

    Invoke-Check 'signature diagnostics are synthetic-only, sanitized, and retained across cleanup' {
        $keys = @(
            'GITHUB_ACTIONS', 'GITHUB_EVENT_NAME', 'GITHUB_REPOSITORY', 'ImageOS',
            'CNMON_MSIX_IDENTITY_NAME', 'CNMON_MSIX_IDENTITY_PUBLISHER',
            'CNMON_MSIX_PUBLISHER_DISPLAY_NAME', 'CNMON_MSIX_EXPECTED_PFN'
        )
        $old = @{}
        foreach ($key in $keys) { $old[$key] = [Environment]::GetEnvironmentVariable($key) }
        try {
            $env:GITHUB_ACTIONS = 'true'
            $env:GITHUB_EVENT_NAME = 'pull_request'
            $env:GITHUB_REPOSITORY = 'evdanil/cn-mon'
            $env:ImageOS = 'win25'
            $env:CNMON_MSIX_IDENTITY_NAME = 'Example.CnMon'
            $env:CNMON_MSIX_IDENTITY_PUBLISHER = 'CN=Example, O=Example'
            $env:CNMON_MSIX_PUBLISHER_DISPLAY_NAME = 'Example Publisher'
            $env:CNMON_MSIX_EXPECTED_PFN = 'Example.CnMon_518g7wxbxvqf6'
            $syntheticIdentity = [pscustomobject]@{
                Name = 'Example.CnMon'
                Publisher = 'CN=Example, O=Example'
                PublisherDisplayName = 'Example Publisher'
                ExpectedPFN = 'Example.CnMon_518g7wxbxvqf6'
            }
            Assert-True (Test-CnMonMsixSyntheticPrDiagnostic -Mode 'Hosted' -Identity $syntheticIdentity) 'exact golden PR identity was not authorized for a safe diagnostic'
            $guardStatus = Get-CnMonMsixSyntheticPrDiagnosticGuard -Mode 'Hosted' -Identity $syntheticIdentity
            Assert-True $guardStatus.all 'exact synthetic fixture guard did not report a complete match'
            foreach ($guard in $guardStatus.Keys) {
                if ($guard -notin @('mode_hosted', 'mode_user')) { Assert-True ([bool]$guardStatus[$guard]) "expected synthetic guard '$guard' to be true" }
            }
            Assert-True (Test-CnMonMsixSyntheticPrDiagnostic -Mode 'User' -Identity $syntheticIdentity) 'nested synthetic User mode was not authorized for a safe diagnostic'
            $userGuardStatus = Get-CnMonMsixSyntheticPrDiagnosticGuard -Mode 'User' -Identity $syntheticIdentity
            Assert-True $userGuardStatus.all 'nested synthetic User guard did not report a complete match'
            Assert-True $userGuardStatus.mode_user 'User-mode guard did not identify the nested qualification stage'

            $privateRoot = Join-Path $tempRoot 'signature-diagnostic-source'
            $privateArtifactDirectory = Join-Path $tempRoot 'signature-diagnostic-private-artifacts'
            $null = New-Item -ItemType Directory -Path $privateRoot, $privateArtifactDirectory
            $privateLog = Join-Path $privateRoot 'candidate-verify.log'
            $rawLog = @'
stdout:
SignTool Error: Certificate chain failed with HRESULT 0x800B0109.
Private path: C:\runner\temp\cn-mon-msix-private\candidate.msix
Identity: Example.CnMon; CN=Example, O=Example; Example Publisher; Example.CnMon_518g7wxbxvqf6
Thumbprint: 0123456789ABCDEF0123456789ABCDEF01234567
-----BEGIN PRIVATE KEY-----
mock PFX payload must stay private
stderr:
'@
            $script:mockSignatureLog = $rawLog
            $script:mockSignatureExitMode = 'captured'
            function Invoke-CnMonMsixTool {
                [CmdletBinding()]
                param([string]$Executable, [string[]]$Arguments, [string]$LogPath)
                if ($script:mockSignatureExitMode -eq 'success') {
                    $script:LastSdkToolExitCode = $null
                    return 0
                }
                [IO.File]::WriteAllText($LogPath, $script:mockSignatureLog, [Text.UTF8Encoding]::new($false))
                if ($script:mockSignatureExitMode -eq 'captured') { $script:LastSdkToolExitCode = 1 }
                throw 'mock SDK child failed'
            }
            $sdkDirectory = Join-Path $tempRoot 'signature-sdk'
            $null = New-Item -ItemType Directory -Path $sdkDirectory
            [IO.File]::WriteAllText((Join-Path $sdkDirectory 'signtool.exe'), 'mock executable')
            $SdkBinDirectory = $sdkDirectory
            $ArtifactDirectory = $privateArtifactDirectory
            $Mode = 'Hosted'
            $script:CurrentDiagnosticOperation = 'none'
            $script:SignedCatalogFootprintPresent = $false
            $script:LastSdkToolName = $null
            $script:LastSdkToolLogPath = $null
            $script:LastSdkToolExitCode = $null
            $script:CurrentDiagnosticPhase = 'candidate-verify'
            $script:TrustPresenceChecked = $false
            $script:TrustedCertificatePresent = $false
            $failedChild = $false
            try { Assert-CnMonMsixSignature -Path 'candidate.msix' -LogPath $privateLog -Identity $syntheticIdentity }
            catch {
                $failedChild = $true
                Save-CnMonMsixSignatureFailureEvidence -Mode 'Hosted' -ArtifactDirectory $privateArtifactDirectory -Phase 'candidate-verify' -Identity $syntheticIdentity -Exception $_.Exception -TrustPresenceChecked:$false -TrustedCertificatePresent:$false
            }
            Assert-True $failedChild 'nonzero mocked SignTool verify child was not reported as a failure'
            Assert-Equal $script:LastSdkToolExitCode 1 'failed SignTool child exit code was not retained'

            $diagnostic = Get-CnMonMsixSignatureDiagnostic -LogPath $privateLog -ExitCode 1 -Phase 'candidate-verify'
            Assert-Equal $diagnostic.exit_code 1 'SDK failure exit code was not retained'
            Assert-Equal $diagnostic.reason 'untrusted_root' 'known HRESULT did not map to a fixed reason category'
            Assert-True ($diagnostic.hresults -contains '0x800B0109') 'SDK HRESULT was not retained'
            Assert-Equal $diagnostic.phase 'candidate-verify' 'signature phase was not retained'

            $publicPath = Join-Path $privateArtifactDirectory 'msix-signature-diagnostic.json'
            $statusPath = Join-Path $privateArtifactDirectory 'msix-signature-diagnostic-status.json'
            $retainedPrivateLog = Join-Path (Join-Path $privateArtifactDirectory 'private-signature-diagnostics') 'candidate-verify.log'
            Assert-True (Test-Path -LiteralPath $publicPath -PathType Leaf) 'golden synthetic PR did not receive its allowlisted diagnostic'
            Assert-True (Test-Path -LiteralPath $statusPath -PathType Leaf) 'PR capture status was not retained for safe hosted diagnosis'
            Assert-True (Test-Path -LiteralPath $retainedPrivateLog -PathType Leaf) 'failed-child SDK log was not retained privately'
            Assert-Equal ([IO.File]::ReadAllText($retainedPrivateLog)) $rawLog 'private SDK log was modified while being retained'
            $publicText = [IO.File]::ReadAllText($publicPath)
            $statusText = [IO.File]::ReadAllText($statusPath)
            $status = Get-Content -LiteralPath $statusPath -Raw | ConvertFrom-Json -AsHashtable
            Assert-Equal $status.schema_version 3 'diagnostic status schema version was invalid'
            Assert-Equal $status.phase 'candidate-verify' 'diagnostic status omitted the failing signature phase'
            Assert-Equal $status.operation 'none' 'signature verification status had an unexpected post-verify operation'
            Assert-Equal $status.error_category 'sdk_nonzero' 'signature verification status omitted its fixed error category'
            Assert-Equal $status.tool_name 'signtool' 'signature verification status omitted its tool identity'
            Assert-True (-not $status.catalog_footprint_present) 'signature verification status unexpectedly reported a catalog footprint'
            Assert-Equal $status.exception_kind 'other' 'diagnostic exception type was not mapped to a closed category'
            Assert-True $status.guard_all 'diagnostic status did not record the complete synthetic guard'
            Assert-True $status.sdk_exit_captured 'diagnostic status did not record the child exit capture'
            Assert-True $status.private_log_retained 'diagnostic status did not record private log retention'
            Assert-Equal $status.failure_kind 'sdk_nonzero' 'diagnostic failure kind was not constrained'
            Assert-Equal $status.save_status 'success' 'diagnostic save status was not recorded'
            Assert-Equal $status.save_error_kind 'none' 'unexpected safe diagnostic save error kind'
            Assert-True $status.public_diagnostic_written 'diagnostic status did not record public synthetic JSON'
            foreach ($sensitive in @('CN=Example, O=Example', 'Example Publisher', 'Example.CnMon_518g7wxbxvqf6', '0123456789ABCDEF0123456789ABCDEF01234567', 'C:\runner\temp', 'BEGIN PRIVATE KEY', 'mock PFX payload')) {
                Assert-True ($publicText -notmatch [regex]::Escape($sensitive)) "synthetic diagnostic leaked '$sensitive'"
                Assert-True ($statusText -notmatch [regex]::Escape($sensitive)) "diagnostic status leaked '$sensitive'"
            }

            # A later MakeAppx failure must not reuse an earlier successful SignTool exit/log.
            $postVerifyDirectory = Join-Path $tempRoot 'signature-post-verify-artifacts'
            $postVerifySource = Join-Path $tempRoot 'signature-post-verify-source'
            $null = New-Item -ItemType Directory -Path $postVerifyDirectory, $postVerifySource
            $successfulVerifyLog = Join-Path $postVerifySource 'candidate-verify.log'
            [IO.File]::WriteAllText($successfulVerifyLog, 'successful SignTool verification log', [Text.UTF8Encoding]::new($false))
            $script:mockSignatureExitMode = 'success'
            $script:LastSdkToolExitCode = $null
            $null = Assert-CnMonMsixSignature -Path 'candidate.msix' -LogPath $successfulVerifyLog -Identity $syntheticIdentity
            $script:LastSdkToolExitCode = 1 # Simulate a distinct, later MakeAppx failure.
            $script:LastSignatureLogPath = $successfulVerifyLog
            Save-CnMonMsixSignatureFailureEvidence -Mode 'Hosted' -ArtifactDirectory $postVerifyDirectory -Phase 'candidate-post-verify' -Identity $syntheticIdentity -Exception ([InvalidOperationException]::new('private MakeAppx failure')) -TrustPresenceChecked:$false -TrustedCertificatePresent:$false
            $postVerifyDiagnostic = Get-Content -LiteralPath (Join-Path $postVerifyDirectory 'msix-signature-diagnostic.json') -Raw | ConvertFrom-Json -AsHashtable
            $postVerifyStatus = Get-Content -LiteralPath (Join-Path $postVerifyDirectory 'msix-signature-diagnostic-status.json') -Raw | ConvertFrom-Json -AsHashtable
            Assert-Equal $postVerifyDiagnostic.failure_kind 'stage_exception' 'post-verification MakeAppx failure was attributed to SignTool'
            Assert-Equal $postVerifyDiagnostic.tool 'none' 'post-verification failure was attributed to a tool without SDK failure provenance'
            Assert-True ($null -eq $postVerifyDiagnostic.exit_code) 'post-verification failure reused a stale SignTool exit code'
            Assert-Equal $postVerifyStatus.failure_kind 'stage_exception' 'status attributed a post-verification failure to SignTool'
            Assert-True (-not $postVerifyStatus.sdk_exit_captured) 'status reused a stale SignTool exit code after successful verification'
            Assert-True (-not (Test-Path -LiteralPath (Join-Path (Join-Path $postVerifyDirectory 'private-signature-diagnostics') 'candidate-post-verify.log'))) 'post-verification failure retained the stale SignTool log as MakeAppx evidence'

            $makeAppxDirectory = Join-Path $tempRoot 'signature-makeappx-diagnostic-artifacts'
            $makeAppxSource = Join-Path $tempRoot 'signature-makeappx-diagnostic-source'
            $null = New-Item -ItemType Directory -Path $makeAppxDirectory, $makeAppxSource
            $makeAppxLog = Join-Path $makeAppxSource 'signed-unpack.log'
            [IO.File]::WriteAllText($makeAppxLog, 'private MakeAppx output with a private path C:\runner\temp\signed-unpacked', [Text.UTF8Encoding]::new($false))
            $script:CurrentDiagnosticOperation = 'signed_makeappx_unpack'
            $script:LastSdkToolName = 'makeappx'
            $script:LastSdkToolExitCode = 23
            $script:LastSdkToolLogPath = $makeAppxLog
            Save-CnMonMsixSignatureFailureEvidence -Mode 'Hosted' -ArtifactDirectory $makeAppxDirectory -Phase 'candidate-post-verify' -Identity $syntheticIdentity -Exception ([InvalidOperationException]::new('private MakeAppx failure at C:\runner\temp')) -TrustPresenceChecked:$false -TrustedCertificatePresent:$false
            $makeAppxDiagnosticPath = Join-Path $makeAppxDirectory 'msix-signature-diagnostic.json'
            $makeAppxStatusPath = Join-Path $makeAppxDirectory 'msix-signature-diagnostic-status.json'
            $makeAppxDiagnostic = Get-Content -LiteralPath $makeAppxDiagnosticPath -Raw | ConvertFrom-Json -AsHashtable
            $makeAppxStatus = Get-Content -LiteralPath $makeAppxStatusPath -Raw | ConvertFrom-Json -AsHashtable
            Assert-Equal $makeAppxDiagnostic.operation 'signed_makeappx_unpack' 'MakeAppx failure lost its fixed operation provenance'
            Assert-Equal $makeAppxDiagnostic.tool 'makeappx' 'MakeAppx failure was attributed to the wrong SDK tool'
            Assert-Equal $makeAppxDiagnostic.exit_code 23 'MakeAppx failure lost its SDK exit code'
            Assert-Equal $makeAppxDiagnostic.failure_kind 'sdk_nonzero' 'MakeAppx nonzero exit was classified as a generic stage failure'
            Assert-Equal $makeAppxStatus.operation 'signed_makeappx_unpack' 'closed status lost MakeAppx operation provenance'
            Assert-Equal $makeAppxStatus.tool_name 'makeappx' 'closed status omitted MakeAppx tool provenance'
            Assert-Equal $makeAppxStatus.error_category 'sdk_nonzero' 'closed status omitted the fixed SDK failure category'
            Assert-True $makeAppxStatus.sdk_exit_captured 'closed status did not retain MakeAppx exit capture'
            Assert-True $makeAppxStatus.private_log_retained 'closed status did not retain MakeAppx output privately'
            Assert-True (Test-Path -LiteralPath (Join-Path (Join-Path $makeAppxDirectory 'private-signature-diagnostics') 'candidate-post-verify-makeappx.log')) 'MakeAppx private log was not retained under its fixed name'
            $makeAppxSerialized = [IO.File]::ReadAllText($makeAppxDiagnosticPath) + [IO.File]::ReadAllText($makeAppxStatusPath)
            Assert-True ($makeAppxSerialized -notmatch [regex]::Escape('C:\runner\temp')) 'MakeAppx diagnostic exposed private output paths'

            $categoryCases = @(
                @('payload root does not exist: C:\private\x', 'InvalidOperationException', 'stage_exception', 'payload_root_missing'),
                @('payload file missing: C:\private\x', 'InvalidOperationException', 'stage_exception', 'payload_missing'),
                @('payload hash mismatch: C:\private\x', 'InvalidOperationException', 'stage_exception', 'payload_hash_mismatch'),
                @('payload unexpected file: C:\private\x', 'InvalidOperationException', 'stage_exception', 'payload_unexpected'),
                @('payload contains a reparse point: C:\private\x', 'InvalidOperationException', 'stage_exception', 'payload_reparse_point'),
                @('private argument value at C:\private\x', 'ParameterBindingException', 'stage_exception', 'parameter_binding')
            )
            foreach ($categoryCase in $categoryCases) {
                $category = Get-CnMonMsixDiagnosticErrorCategory -Message $categoryCase[0] -ExceptionType $categoryCase[1] -FailureKind $categoryCase[2]
                Assert-Equal $category $categoryCase[3] 'known post-verify failure did not map to its fixed safe category'
                Assert-True ($category -notmatch [regex]::Escape('C:\private')) 'error category retained private path content'
            }

            $payloadDiagnosticDirectory = Join-Path $tempRoot 'signature-payload-diagnostic-artifacts'
            $null = New-Item -ItemType Directory -Path $payloadDiagnosticDirectory
            $script:CurrentDiagnosticOperation = 'signed_payload_validation'
            $payloadErrorMessage = 'payload hash mismatch: C:\runner\temp\private\signed-unpacked\AppxManifest.xml'
            Save-CnMonMsixSignatureFailureEvidence -Mode 'Hosted' -ArtifactDirectory $payloadDiagnosticDirectory -Phase 'candidate-post-verify' -Identity $syntheticIdentity -Exception ([InvalidOperationException]::new($payloadErrorMessage)) -TrustPresenceChecked:$false -TrustedCertificatePresent:$false
            $payloadDiagnosticPath = Join-Path $payloadDiagnosticDirectory 'msix-signature-diagnostic.json'
            $payloadStatusPath = Join-Path $payloadDiagnosticDirectory 'msix-signature-diagnostic-status.json'
            $payloadDiagnostic = Get-Content -LiteralPath $payloadDiagnosticPath -Raw | ConvertFrom-Json -AsHashtable
            $payloadStatus = Get-Content -LiteralPath $payloadStatusPath -Raw | ConvertFrom-Json -AsHashtable
            Assert-Equal $payloadDiagnostic.operation 'signed_payload_validation' 'post-verification diagnostic omitted the failing fixed operation'
            Assert-Equal $payloadDiagnostic.error_category 'payload_hash_mismatch' 'known payload error prefix was not mapped to a fixed category'
            Assert-Equal $payloadStatus.operation 'signed_payload_validation' 'closed status omitted the failing fixed operation'
            Assert-Equal $payloadStatus.error_category 'payload_hash_mismatch' 'closed status omitted the fixed payload category'
            $payloadSerialized = [IO.File]::ReadAllText($payloadDiagnosticPath) + [IO.File]::ReadAllText($payloadStatusPath)
            Assert-True ($payloadSerialized -notmatch [regex]::Escape('C:\runner\temp\private')) 'post-verification diagnostic exposed a private payload path'

            $ciStatusOutput = (Write-CnMonMsixSignatureDiagnosticStatus -Path $statusPath 6>&1 | Out-String).Trim()
            Assert-True ($ciStatusOutput.Contains('guard_image_os_windows=true')) 'CI status did not expose the verified Windows boolean'
            Assert-True ($ciStatusOutput.Contains('phase=candidate-verify')) 'CI status did not expose the fixed hosted phase'
            Assert-True ($ciStatusOutput.Contains('operation=none')) 'CI status did not expose the fixed operation'
            Assert-True ($ciStatusOutput.Contains('error_category=sdk_nonzero')) 'CI status did not expose the fixed error category'
            Assert-True ($ciStatusOutput.Contains('tool_name=signtool')) 'CI status did not expose the fixed tool name'
            Assert-True ($ciStatusOutput.Contains('catalog_footprint_present=false')) 'CI status did not expose the safe catalog-presence boolean'
            Assert-True ($ciStatusOutput.Contains('save_status=success')) 'CI status did not expose the fixed save result'
            $invalidStatusPath = Join-Path $privateArtifactDirectory 'invalid-diagnostic-status.json'
            $invalidStatus = [ordered]@{}
            foreach ($key in $status.Keys) { $invalidStatus[$key] = $status[$key] }
            $invalidStatus.unexpected = 'CN=Example, O=Example'
            [IO.File]::WriteAllText($invalidStatusPath, (ConvertTo-Json -InputObject $invalidStatus -Depth 4), [Text.UTF8Encoding]::new($false))
            $invalidOutput = (Write-CnMonMsixSignatureDiagnosticStatus -Path $invalidStatusPath 6>&1 | Out-String).Trim()
            Assert-Equal $invalidOutput 'MSIX signature diagnostic status: invalid' 'CI status logger accepted unrecognized fields or exposed their values'
            Remove-Item -LiteralPath $privateRoot -Recurse -Force
            Assert-True (Test-Path -LiteralPath $retainedPrivateLog -PathType Leaf) 'private SDK evidence did not survive PrivateRoot cleanup'

            $env:CNMON_MSIX_IDENTITY_PUBLISHER = 'CN=Private Example, O=Example'
            $nonGoldenIdentity = [pscustomobject]@{
                Name = 'Example.CnMon'
                Publisher = 'CN=Private Example, O=Example'
                PublisherDisplayName = 'Private Example Publisher'
                ExpectedPFN = 'Example.CnMon_518g7wxbxvqf6'
            }
            $env:CNMON_MSIX_PUBLISHER_DISPLAY_NAME = 'Private Example Publisher'
            Assert-True (-not (Test-CnMonMsixSyntheticPrDiagnostic -Mode 'Hosted' -Identity $nonGoldenIdentity)) 'non-golden identity was authorized for public diagnostics'
            $nonGoldenGuard = Get-CnMonMsixSyntheticPrDiagnosticGuard -Mode 'Hosted' -Identity $nonGoldenIdentity
            Assert-True (-not $nonGoldenGuard.all) 'non-golden identity guard did not report a mismatch'
            Assert-True (-not $nonGoldenGuard.identity_publisher_env_matches) 'publisher environment mismatch was not isolated'
            Assert-True (-not $nonGoldenGuard.identity_publisher_matches) 'publisher object mismatch was not isolated'
            $realArtifactDirectory = Join-Path $tempRoot 'real-signature-diagnostic-artifacts'
            $realPrivateRoot = Join-Path $tempRoot 'real-signature-diagnostic-source'
            $null = New-Item -ItemType Directory -Path $realArtifactDirectory, $realPrivateRoot
            $realLog = Join-Path $realPrivateRoot 'candidate-verify.log'
            [IO.File]::WriteAllText($realLog, $rawLog, [Text.UTF8Encoding]::new($false))
            Save-CnMonMsixSignatureDiagnostic -LogPath $realLog -ArtifactDirectory $realArtifactDirectory -ExitCode 1 -Phase 'candidate-verify' -Mode 'Hosted' -Identity $nonGoldenIdentity
            Assert-True (-not (Test-Path -LiteralPath (Join-Path $realArtifactDirectory 'msix-signature-diagnostic.json'))) 'non-golden identity produced a public diagnostic'
            Assert-True (Test-Path -LiteralPath (Join-Path (Join-Path $realArtifactDirectory 'private-signature-diagnostics') 'candidate-verify.log')) 'real-run SDK failure was not retained privately'
            $realEvidenceDirectory = Join-Path $tempRoot 'real-signature-failure-evidence'
            $null = New-Item -ItemType Directory -Path $realEvidenceDirectory
            $env:GITHUB_EVENT_NAME = 'workflow_dispatch'
            $script:LastSdkToolExitCode = 1
            $script:LastSignatureVerifyExitCode = 1
            $script:LastSignatureLogPath = $realLog
            Save-CnMonMsixSignatureFailureEvidence -Mode 'Hosted' -ArtifactDirectory $realEvidenceDirectory -Phase 'candidate-verify' -Identity $nonGoldenIdentity -Exception ([InvalidOperationException]::new('private verification detail')) -TrustPresenceChecked:$false -TrustedCertificatePresent:$false
            $privateManualLog = Join-Path (Join-Path $realEvidenceDirectory 'private-signature-diagnostics') 'candidate-verify.log'
            Assert-True (Test-Path -LiteralPath $privateManualLog -PathType Leaf) 'non-golden manual signature failure did not retain its SDK log privately'
            Assert-Equal ([IO.File]::ReadAllText($privateManualLog)) $rawLog 'non-golden manual SDK log changed during private retention'
            Assert-True (-not (Test-Path -LiteralPath (Join-Path $realEvidenceDirectory 'msix-signature-diagnostic.json'))) 'non-golden manual signature failure emitted a public diagnostic'
            Assert-True (-not (Test-Path -LiteralPath (Join-Path $realEvidenceDirectory 'msix-signature-diagnostic-status.json'))) 'non-golden manual signature failure emitted public status'
            $env:GITHUB_EVENT_NAME = 'pull_request'

            $env:CNMON_MSIX_IDENTITY_PUBLISHER = 'CN=Example, O=Example'
            $env:CNMON_MSIX_PUBLISHER_DISPLAY_NAME = 'Example Publisher'
            $missingExitDirectory = Join-Path $tempRoot 'signature-missing-exit-artifacts'
            $missingExitSource = Join-Path $tempRoot 'signature-missing-exit-source'
            $null = New-Item -ItemType Directory -Path $missingExitDirectory, $missingExitSource
            $ArtifactDirectory = $missingExitDirectory
            $script:mockSignatureExitMode = 'missing'
            $script:LastSdkToolExitCode = $null
            try { Assert-CnMonMsixSignature -Path 'candidate.msix' -LogPath (Join-Path $missingExitSource 'candidate-verify.log') -Identity $syntheticIdentity }
            catch {
                $failedChild = $true
                Save-CnMonMsixSignatureFailureEvidence -Mode 'Hosted' -ArtifactDirectory $missingExitDirectory -Phase 'candidate-verify' -Identity $syntheticIdentity -Exception $_.Exception -TrustPresenceChecked:$false -TrustedCertificatePresent:$false
            }
            $missingExitStatus = Get-Content -LiteralPath (Join-Path $missingExitDirectory 'msix-signature-diagnostic-status.json') -Raw | ConvertFrom-Json -AsHashtable
            Assert-Equal $missingExitStatus.failure_kind 'sdk_invocation_error' 'missing SDK exit was not classified safely'
            Assert-True (-not $missingExitStatus.sdk_exit_captured) 'missing SDK exit was incorrectly recorded as captured'
            Assert-Equal $missingExitStatus.save_status 'success' 'fixed diagnostic status was not written without an SDK exit code'

            $saveFailureDirectory = Join-Path $tempRoot 'signature-save-failure-artifacts'
            $saveFailureSource = Join-Path $tempRoot 'signature-save-failure-source'
            $saveFailurePrivate = Join-Path $saveFailureDirectory 'private-signature-diagnostics'
            $null = New-Item -ItemType Directory -Path $saveFailurePrivate, $saveFailureSource
            [IO.File]::WriteAllText((Join-Path $saveFailurePrivate 'candidate-verify.log'), 'pre-existing private evidence', [Text.UTF8Encoding]::new($false))
            $ArtifactDirectory = $saveFailureDirectory
            $script:mockSignatureExitMode = 'captured'
            $script:LastSdkToolExitCode = $null
            try { Assert-CnMonMsixSignature -Path 'candidate.msix' -LogPath (Join-Path $saveFailureSource 'candidate-verify.log') -Identity $syntheticIdentity }
            catch {
                $failedChild = $true
                Save-CnMonMsixSignatureFailureEvidence -Mode 'Hosted' -ArtifactDirectory $saveFailureDirectory -Phase 'candidate-verify' -Identity $syntheticIdentity -Exception $_.Exception -TrustPresenceChecked:$false -TrustedCertificatePresent:$false
            }
            $saveFailureStatus = Get-Content -LiteralPath (Join-Path $saveFailureDirectory 'msix-signature-diagnostic-status.json') -Raw | ConvertFrom-Json -AsHashtable
            Assert-Equal $saveFailureStatus.save_status 'failure' 'failed diagnostic copy was not distinguished'
            Assert-Equal $saveFailureStatus.save_error_kind 'other' 'failed diagnostic copy did not map to a fixed error kind'
            Assert-True (-not $saveFailureStatus.public_diagnostic_written) 'failed diagnostic copy reported a public diagnostic'

            $env:CNMON_MSIX_IDENTITY_PUBLISHER = 'CN=Example, O=Example'
            $env:CNMON_MSIX_PUBLISHER_DISPLAY_NAME = 'Example Publisher'
            $env:GITHUB_EVENT_NAME = 'workflow_dispatch'
            Assert-True (-not (Test-CnMonMsixSyntheticPrDiagnostic -Mode 'Hosted' -Identity $syntheticIdentity)) 'manual job was authorized for public signature diagnostics'
            Assert-True (-not (Test-CnMonMsixSyntheticPrDiagnostic -Mode 'User' -Identity $syntheticIdentity)) 'manual job was authorized for public User-mode diagnostics'
            Save-CnMonMsixSignatureDiagnosticStatus -ArtifactDirectory $realArtifactDirectory -Guard $guardStatus -Phase 'candidate-verify' -FailureKind 'sdk_nonzero' -ExceptionKind 'other' -SdkExitCaptured:$true -PrivateLogRetained:$true -TrustPresenceChecked:$false -TrustedCertificatePresent:$false -SaveStatus success -SaveErrorKind none -PublicDiagnosticWritten:$false
            Assert-True (-not (Test-Path -LiteralPath (Join-Path $realArtifactDirectory 'msix-signature-diagnostic-status.json'))) 'manual job received synthetic capture status'
            $manualOutput = (Write-CnMonMsixSignatureDiagnosticStatus -Path $statusPath 6>&1 | Out-String).Trim()
            Assert-Equal $manualOutput '' 'manual job exposed synthetic signature diagnostic status'

            $env:GITHUB_EVENT_NAME = 'pull_request'
            $userTrustDirectory = Join-Path $tempRoot 'user-trust-diagnostic-artifacts'
            $null = New-Item -ItemType Directory -Path $userTrustDirectory
            $Mode = 'User'
            $script:CurrentDiagnosticPhase = 'user-trust-presence'
            $script:TrustPresenceChecked = $true
            $script:TrustedCertificatePresent = $false
            $script:LastSdkToolExitCode = $null
            $trustError = [InvalidOperationException]::new('private store lookup detail')
            Save-CnMonMsixSignatureFailureEvidence -Mode $Mode -ArtifactDirectory $userTrustDirectory -Phase 'user-trust-presence' -Identity $syntheticIdentity -Exception $trustError -TrustPresenceChecked:$true -TrustedCertificatePresent:$false
            $userTrustPublicPath = Join-Path $userTrustDirectory 'msix-signature-diagnostic.json'
            $userTrustStatusPath = Join-Path $userTrustDirectory 'msix-signature-diagnostic-status.json'
            Assert-True (Test-Path -LiteralPath $userTrustPublicPath -PathType Leaf) 'synthetic User trust failure did not write its exact allowlisted diagnostic'
            Assert-True (Test-Path -LiteralPath $userTrustStatusPath -PathType Leaf) 'synthetic User trust failure did not write its closed status record'
            $userTrustDiagnostic = Get-Content -LiteralPath $userTrustPublicPath -Raw | ConvertFrom-Json -AsHashtable
            $userTrustStatus = Get-Content -LiteralPath $userTrustStatusPath -Raw | ConvertFrom-Json -AsHashtable
            Assert-Equal $userTrustDiagnostic.phase 'user-trust-presence' 'diagnostic did not identify the User trust lookup stage'
            Assert-Equal $userTrustDiagnostic.reason 'trusted_certificate_missing' 'diagnostic did not use the fixed missing-trust reason'
            Assert-True $userTrustDiagnostic.trust_presence_checked 'diagnostic did not record that trust lookup ran'
            Assert-True (-not $userTrustDiagnostic.trusted_certificate_present) 'missing trust was reported as present'
            Assert-Equal $userTrustStatus.phase 'user-trust-presence' 'status did not retain the User trust lookup stage'
            Assert-Equal $userTrustStatus.failure_kind 'trusted_certificate_missing' 'status did not classify the missing trust gate'
            Assert-Equal $userTrustStatus.exception_kind 'invalid_operation' 'status did not map the exception to a fixed category'
            Assert-True $userTrustStatus.guard_mode_user 'status did not confirm the nested User-mode guard'
            $userTrustOutput = (Write-CnMonMsixSignatureDiagnosticStatus -Path $userTrustStatusPath 6>&1 | Out-String).Trim()
            Assert-True ($userTrustOutput.Contains('phase=user-trust-presence')) 'CI status did not expose the exact nested trust-check phase'
            Assert-True ($userTrustOutput.Contains('guard_mode_user=true')) 'CI status did not expose the verified nested User guard'
            $serializedUserDiagnostic = [IO.File]::ReadAllText($userTrustPublicPath) + [IO.File]::ReadAllText($userTrustStatusPath)
            foreach ($sensitive in @('private store lookup detail', 'CN=Example, O=Example', 'Example Publisher', 'Example.CnMon_518g7wxbxvqf6')) {
                Assert-True ($serializedUserDiagnostic -notmatch [regex]::Escape($sensitive)) "User diagnostic leaked '$sensitive'"
            }

            $userVerifyDirectory = Join-Path $tempRoot 'user-verify-diagnostic-artifacts'
            $userVerifySource = Join-Path $tempRoot 'user-verify-diagnostic-source'
            $null = New-Item -ItemType Directory -Path $userVerifyDirectory, $userVerifySource
            $ArtifactDirectory = $userVerifyDirectory
            $Mode = 'User'
            $script:CurrentDiagnosticPhase = 'user-verify'
            $script:TrustPresenceChecked = $true
            $script:TrustedCertificatePresent = $true
            $script:mockSignatureExitMode = 'captured'
            $script:LastSdkToolExitCode = $null
            try { Assert-CnMonMsixSignature -Path 'candidate.msix' -LogPath (Join-Path $userVerifySource 'user-verify.log') -Phase 'user-verify' -Identity $syntheticIdentity }
            catch {
                $failedChild = $true
                Save-CnMonMsixSignatureFailureEvidence -Mode 'User' -ArtifactDirectory $userVerifyDirectory -Phase 'user-verify' -Identity $syntheticIdentity -Exception $_.Exception -TrustPresenceChecked:$true -TrustedCertificatePresent:$true
            }
            $userVerifyDiagnostic = Get-Content -LiteralPath (Join-Path $userVerifyDirectory 'msix-signature-diagnostic.json') -Raw | ConvertFrom-Json -AsHashtable
            $userVerifyStatus = Get-Content -LiteralPath (Join-Path $userVerifyDirectory 'msix-signature-diagnostic-status.json') -Raw | ConvertFrom-Json -AsHashtable
            Assert-Equal $userVerifyDiagnostic.phase 'user-verify' 'SDK diagnostic did not identify User-context verification'
            Assert-Equal $userVerifyDiagnostic.reason 'untrusted_root' 'User-context SDK HRESULT did not retain its fixed category'
            Assert-True $userVerifyDiagnostic.trust_presence_checked 'User-context verification omitted the preceding trust check'
            Assert-True $userVerifyDiagnostic.trusted_certificate_present 'User-context verification omitted the positive trust result'
            Assert-Equal $userVerifyStatus.failure_kind 'sdk_nonzero' 'User-context SDK failure class was not retained'
            Assert-Equal $userVerifyStatus.phase 'user-verify' 'User-context status stage was not retained'
            $userVerifyPublicText = [IO.File]::ReadAllText((Join-Path $userVerifyDirectory 'msix-signature-diagnostic.json'))
            $userVerifyStatusText = [IO.File]::ReadAllText((Join-Path $userVerifyDirectory 'msix-signature-diagnostic-status.json'))
            foreach ($sensitive in @('private store lookup detail', 'CN=Example, O=Example', 'Example Publisher', 'Example.CnMon_518g7wxbxvqf6', '0123456789ABCDEF0123456789ABCDEF01234567', 'BEGIN PRIVATE KEY', 'mock PFX payload')) {
                Assert-True ($userVerifyPublicText -notmatch [regex]::Escape($sensitive)) "User SDK diagnostic leaked '$sensitive'"
                Assert-True ($userVerifyStatusText -notmatch [regex]::Escape($sensitive)) "User SDK status leaked '$sensitive'"
            }

            $failureEvidenceCalls = @($sourceAst.FindAll({
                param($candidate)
                $candidate -is [System.Management.Automation.Language.CommandAst] -and
                    $candidate.GetCommandName() -eq 'Save-CnMonMsixSignatureFailureEvidence'
            }, $true))
            Assert-Equal $failureEvidenceCalls.Count 1 'outer catch does not provide one fallback capture for failures inside diagnostic helpers'
        }
        finally {
            foreach ($key in $keys) { [Environment]::SetEnvironmentVariable($key, $old[$key], 'Process') }
        }
    }

    Invoke-Check 'native profile failure evidence is fixed, numeric, and distinguishes exit from marker failure' {
        $privateRoot = Join-Path $tempRoot 'native-failure-private'
        $expectedProfile = Join-Path $privateRoot 'profile'
        $localAppData = Join-Path $privateRoot 'local'
        $cache = Join-Path $localAppData 'Packages/Example.CnMon_518g7wxbxvqf6/LocalCache/Local'
        $null = New-Item -ItemType Directory -Force -Path $expectedProfile, $cache
        $history = Join-Path $localAppData 'msix-profile-history.db'
        $log = Join-Path $localAppData 'msix-profile.log'
        $cacheHistory = Join-Path $cache 'msix-profile-history.db'
        [IO.File]::WriteAllText($cacheHistory, 'synthetic cache witness', [Text.UTF8Encoding]::new($false))
        $secretPath = Join-Path $privateRoot 'private-user-profile'
        $rawOutput = @"
--- FAIL: TestNativeMSIXRealProfileLayers (0.02s)
    native_msix_windows_test.go:324: private failure at $secretPath CN=Actual Publisher, O=Private
    C:\Users\private\profile\native_other_test.go:711: unrelated helper frame
panic: test timed out after 10m0s
"@
        $childFailure = Get-CnMonMsixNativeTestFailureDiagnostic -Phase 'real-profile-config' -FailureKind child_exit -ExitCode 1 -Output $rawOutput -ProfileHistoryPath $history -ProfileLogPath $log -LocalAppData $localAppData -PackageFamilyName 'Example.CnMon_518g7wxbxvqf6'
        Assert-Equal $childFailure.phase 'real-profile-config' 'native failure evidence lost its fixed phase'
        Assert-Equal $childFailure.test_name 'NativeMSIXRealProfileLayers' 'native failure evidence accepted an arbitrary test name'
        Assert-Equal $childFailure.failure_kind 'child_exit' 'nonzero child was not classified as a child exit'
        Assert-Equal $childFailure.exit_code 1 'native failure evidence lost the numeric exit code'
        Assert-True $childFailure.timeout 'native test timeout marker was not reduced to a boolean'
        Assert-Equal (@($childFailure.source_lines) -join ',') '324' 'native failure evidence did not keep only bounded assertion source lines'
        Assert-True (-not $childFailure.profile_history_exists -and -not $childFailure.profile_log_exists) 'expected profile output existence was misreported'
        Assert-True (-not $childFailure.appdata_package_localcache_history_exists -and -not $childFailure.appdata_package_localcache_log_exists) 'profile-only diagnostic inferred AppData cache outputs without paths'
        $safeJson = ConvertTo-Json -InputObject $childFailure -Depth 5
        foreach ($sensitive in @($secretPath, 'C:\Users\private', 'Actual Publisher', 'CN=')) {
            Assert-True ($safeJson -notmatch [regex]::Escape($sensitive)) 'native failure evidence retained raw output, identity, or path data'
        }

        $markerFailure = Get-CnMonMsixNativeTestFailureDiagnostic -Phase 'real-profile-config' -FailureKind missing_marker -ExitCode 0 -Output $rawOutput -ProfileHistoryPath $history -ProfileLogPath $log -LocalAppData $localAppData -PackageFamilyName 'Example.CnMon_518g7wxbxvqf6'
        Assert-Equal $markerFailure.failure_kind 'missing_marker' 'zero-exit marker failure was confused with a child exit'
        Assert-Equal $markerFailure.exit_code 0 'marker failure lost its zero child exit code'

        $script:NativeTestFailure = $childFailure
        $directory = Join-Path $tempRoot 'native-failure-public'
        $null = New-Item -ItemType Directory -Path $directory
        $syntheticIdentity = [pscustomobject]@{
            Name = 'Example.CnMon'
            Publisher = 'CN=Example, O=Example'
            PublisherDisplayName = 'Example Publisher'
            ExpectedPFN = 'Example.CnMon_518g7wxbxvqf6'
        }
        $identityKeys = @(
            'GITHUB_ACTIONS', 'GITHUB_EVENT_NAME', 'GITHUB_REPOSITORY', 'ImageOS',
            'CNMON_MSIX_IDENTITY_NAME', 'CNMON_MSIX_IDENTITY_PUBLISHER',
            'CNMON_MSIX_PUBLISHER_DISPLAY_NAME', 'CNMON_MSIX_EXPECTED_PFN'
        )
        $oldIdentity = @{}
        foreach ($key in $identityKeys) { $oldIdentity[$key] = [Environment]::GetEnvironmentVariable($key) }
        try {
            $env:GITHUB_ACTIONS = 'true'
            $env:GITHUB_EVENT_NAME = 'pull_request'
            $env:GITHUB_REPOSITORY = 'evdanil/cn-mon'
            $env:ImageOS = 'win25'
            $env:CNMON_MSIX_IDENTITY_NAME = 'Example.CnMon'
            $env:CNMON_MSIX_IDENTITY_PUBLISHER = 'CN=Example, O=Example'
            $env:CNMON_MSIX_PUBLISHER_DISPLAY_NAME = 'Example Publisher'
            $env:CNMON_MSIX_EXPECTED_PFN = 'Example.CnMon_518g7wxbxvqf6'
            $errorRecord = [System.Management.Automation.ErrorRecord]::new([InvalidOperationException]::new('private child failure'), 'NativeTestFailure', [System.Management.Automation.ErrorCategory]::InvalidOperation, $null)
            Save-CnMonMsixQualificationFailureEvidence -Mode User -Gate history_config -Operation user_mode_native_qualification -ArtifactDirectory $directory -Identity $syntheticIdentity -ErrorRecord $errorRecord
            $publicText = [IO.File]::ReadAllText((Join-Path $directory 'msix-signature-diagnostic.json'))
            $publicFailure = (ConvertFrom-Json -InputObject $publicText -AsHashtable).qualification_failure
            Assert-Equal $publicFailure.native_test.failure_kind 'child_exit' 'guarded qualification diagnostic omitted the native child classification'
            Assert-Equal (@($publicFailure.native_test.source_lines) -join ',') '324' 'public qualification diagnostic did not include only the numeric assertion line'
            foreach ($sensitive in @($secretPath, 'C:\Users\private', 'Actual Publisher', 'CN=')) {
                Assert-True ($publicText -notmatch [regex]::Escape($sensitive)) 'public qualification diagnostic leaked private child output'
            }
        }
        finally {
            $script:NativeTestFailure = $null
            foreach ($key in $identityKeys) { [Environment]::SetEnvironmentVariable($key, $oldIdentity[$key], 'Process') }
        }
        foreach ($key in $identityKeys) {
            Assert-Equal ([Environment]::GetEnvironmentVariable($key)) $oldIdentity[$key] "native diagnostic test did not restore process environment '$key'"
        }

        $profileFunction = @($sourceAst.FindAll({
            param($candidate)
            $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $candidate.Name -eq 'Invoke-CnMonMsixUserMode'
        }, $true))[0]
        $profileTest = Get-CommandNodes $profileFunction 'Invoke-CnMonMsixNativeTests' | Where-Object { $_.Extent.Text -match 'real-profile-config' } | Select-Object -First 1
        Assert-True ($null -ne $profileTest) 'real-profile configuration Go test invocation is missing'
        $historyGate = @($profileFunction.FindAll({
            param($candidate)
            $candidate -is [System.Management.Automation.Language.AssignmentStatementAst] -and $candidate.Extent.Text -match "CurrentGate\s*=\s*'history_config'"
        }, $true) | Where-Object { $_.Extent.StartOffset -lt $profileTest.Extent.StartOffset } | Sort-Object { $_.Extent.StartOffset } | Select-Object -Last 1)
        Assert-True ($historyGate.Count -eq 1) 'real-profile test failure is still attributed to the prior alias gate'
    }

    Invoke-Check 'AppData failure evidence keeps exact stages, bounded Go lines, and gated summaries' {
        $privateRoot = Join-Path $tempRoot 'appdata-native-failure-private'
        $profile = Join-Path $privateRoot 'profile'
        $localAppData = Join-Path $privateRoot 'local'
        $cache = Join-Path $localAppData 'Packages/Example.CnMon_518g7wxbxvqf6/LocalCache/Local'
        $null = New-Item -ItemType Directory -Force -Path $profile, $cache
        $history = Join-Path $localAppData 'cn-mon-msix-appdata-unique.db'
        $log = Join-Path $localAppData 'cn-mon-msix-appdata-unique.log'
        $cacheHistory = Join-Path $cache (Split-Path -Leaf $history)
        [IO.File]::WriteAllText($cacheHistory, 'synthetic appdata witness', [Text.UTF8Encoding]::new($false))
        $secretPath = Join-Path $privateRoot 'private-profile'
        $rawOutput = @"
--- FAIL: TestNativeMSIXAppDataLifecycle (0.02s)
    native_msix_windows_test.go:431: private failure at $secretPath CN=Actual Publisher, O=Private
    C:\Users\private\profile\native_msix_windows_test.go:9999: bounded source witness
    native_msix_windows_test.go:432: second source witness
    native_msix_windows_test.go:433: third source witness
    native_msix_windows_test.go:434: fourth source witness
    native_msix_windows_test.go:435: fifth source witness
    native_msix_windows_test.go:436: sixth source witness
    native_msix_windows_test.go:437: seventh source witness
    native_msix_windows_test.go:438: eighth source witness
    native_msix_windows_test.go:439: beyond source bound
    other_native_test.go:7: unrelated source
"@
        $phases = @('appdata-seed', 'appdata-upgrade-readback', 'appdata-repair-readback', 'appdata-reinstall-fresh')
        $phaseGates = @{
            'appdata-seed' = 'history_config'
            'appdata-upgrade-readback' = 'history_config'
            'appdata-repair-readback' = 'repair'
            'appdata-reinstall-fresh' = 'uninstall_reinstall'
        }
        $identityKeys = @(
            'GITHUB_ACTIONS', 'GITHUB_EVENT_NAME', 'GITHUB_REPOSITORY', 'ImageOS',
            'CNMON_MSIX_IDENTITY_NAME', 'CNMON_MSIX_IDENTITY_PUBLISHER',
            'CNMON_MSIX_PUBLISHER_DISPLAY_NAME', 'CNMON_MSIX_EXPECTED_PFN'
        )
        $oldIdentity = @{}
        foreach ($key in $identityKeys) { $oldIdentity[$key] = [Environment]::GetEnvironmentVariable($key) }
        $oldNativeFailure = $script:NativeTestFailure
        $oldPublicDiagnostic = $script:PublicDiagnosticWritten
        try {
            $env:GITHUB_ACTIONS = 'true'
            $env:GITHUB_EVENT_NAME = 'pull_request'
            $env:GITHUB_REPOSITORY = 'evdanil/cn-mon'
            $env:ImageOS = 'win25'
            $env:CNMON_MSIX_IDENTITY_NAME = 'Example.CnMon'
            $env:CNMON_MSIX_IDENTITY_PUBLISHER = 'CN=Example, O=Example'
            $env:CNMON_MSIX_PUBLISHER_DISPLAY_NAME = 'Example Publisher'
            $env:CNMON_MSIX_EXPECTED_PFN = 'Example.CnMon_518g7wxbxvqf6'
            $syntheticIdentity = [pscustomobject]@{
                Name = 'Example.CnMon'
                Publisher = 'CN=Example, O=Example'
                PublisherDisplayName = 'Example Publisher'
                ExpectedPFN = 'Example.CnMon_518g7wxbxvqf6'
            }
            foreach ($phase in $phases) {
                $native = Get-CnMonMsixNativeTestFailureDiagnostic -Phase $phase -FailureKind child_exit -ExitCode 1 -Output $rawOutput -AppDataHistoryPath $history -AppDataLogPath $log -LocalAppData $localAppData -PackageFamilyName 'Example.CnMon_518g7wxbxvqf6'
                Assert-Equal $native.phase $phase 'AppData diagnostic lost its exact fixed stage'
                Assert-Equal $native.test_name 'NativeMSIXAppDataLifecycle' 'AppData phase mapped to an arbitrary Go test'
                Assert-Equal (@($native.source_lines) -join ',') '431,9999,432,433,434,435,436,437' 'AppData diagnostic did not keep only bounded numeric lines from the known Go test file'
                Assert-True (-not $native.appdata_external_history_exists -and -not $native.appdata_external_log_exists) 'AppData diagnostic misreported external targets'
                Assert-True $native.appdata_package_localcache_history_exists 'AppData diagnostic missed the exact package-local history witness'
                Assert-True (-not $native.appdata_package_localcache_log_exists) 'AppData diagnostic misreported the absent LocalCache log'
                $safeJson = ConvertTo-Json -InputObject $native -Depth 5
                foreach ($sensitive in @($secretPath, 'C:\Users\private', 'Actual Publisher', 'CN=')) {
                    Assert-True ($safeJson -notmatch [regex]::Escape($sensitive)) 'AppData diagnostic retained raw output, identity, or path data'
                }

                $gate = $phaseGates[$phase]
                $directory = Join-Path $tempRoot "appdata-public-$phase"
                $null = New-Item -ItemType Directory -Path $directory
                $script:NativeTestFailure = $native
                $script:PublicDiagnosticWritten = $false
                $errorRecord = [System.Management.Automation.ErrorRecord]::new([InvalidOperationException]::new('private child failure'), 'NativeTestFailure', [System.Management.Automation.ErrorCategory]::InvalidOperation, $null)
                Save-CnMonMsixQualificationFailureEvidence -Mode User -Gate $gate -Operation user_mode_native_qualification -ArtifactDirectory $directory -Identity $syntheticIdentity -ErrorRecord $errorRecord
                $publicText = [IO.File]::ReadAllText((Join-Path $directory 'msix-signature-diagnostic.json'))
                $publicFailure = (ConvertFrom-Json -InputObject $publicText -AsHashtable).qualification_failure
                Assert-Equal $publicFailure.gate $gate 'AppData child failure was attributed to the wrong gate'
                Assert-Equal $publicFailure.native_test.phase $phase 'public diagnostic omitted the exact AppData stage'
                Assert-Equal $publicFailure.native_test.test_name 'NativeMSIXAppDataLifecycle' 'public diagnostic omitted the fixed AppData test name'
                foreach ($sensitive in @($secretPath, 'C:\Users\private', 'Actual Publisher', 'CN=')) {
                    Assert-True ($publicText -notmatch [regex]::Escape($sensitive)) 'public AppData diagnostic leaked raw child output'
                }
            }
        }
        finally {
            $script:NativeTestFailure = $oldNativeFailure
            $script:PublicDiagnosticWritten = $oldPublicDiagnostic
            foreach ($key in $identityKeys) { [Environment]::SetEnvironmentVariable($key, $oldIdentity[$key], 'Process') }
        }
    }

    Invoke-Check 'account failure diagnostics are closed, guarded, and privately retained' {
        $keys = @(
            'GITHUB_ACTIONS', 'GITHUB_EVENT_NAME', 'GITHUB_REPOSITORY', 'ImageOS',
            'CNMON_MSIX_IDENTITY_NAME', 'CNMON_MSIX_IDENTITY_PUBLISHER',
            'CNMON_MSIX_PUBLISHER_DISPLAY_NAME', 'CNMON_MSIX_EXPECTED_PFN'
        )
        $old = @{}
        foreach ($key in $keys) { $old[$key] = [Environment]::GetEnvironmentVariable($key) }
        try {
            $env:GITHUB_ACTIONS = 'true'
            $env:GITHUB_EVENT_NAME = 'pull_request'
            $env:GITHUB_REPOSITORY = 'evdanil/cn-mon'
            $env:ImageOS = 'win25'
            $env:CNMON_MSIX_IDENTITY_NAME = 'Example.CnMon'
            $env:CNMON_MSIX_IDENTITY_PUBLISHER = 'CN=Example, O=Example'
            $env:CNMON_MSIX_PUBLISHER_DISPLAY_NAME = 'Example Publisher'
            $env:CNMON_MSIX_EXPECTED_PFN = 'Example.CnMon_518g7wxbxvqf6'
            $syntheticIdentity = [pscustomobject]@{
                Name = 'Example.CnMon'
                Publisher = 'CN=Example, O=Example'
                PublisherDisplayName = 'Example Publisher'
                ExpectedPFN = 'Example.CnMon_518g7wxbxvqf6'
            }
            $privateText = 'private exception text CN=Actual Publisher, O=Secret at C:\Users\private\profile'
            if ([Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT) {
                $inner = [Security.Principal.IdentityNotMappedException]::new($privateText)
                $expectedCategory = 'identity_mapping'
                $expectedInnerType = 'IdentityNotMappedException'
            }
            else {
                $inner = [UnauthorizedAccessException]::new($privateText)
                $expectedCategory = 'access_denied'
                $expectedInnerType = 'UnauthorizedAccessException'
            }
            $outer = [InvalidOperationException]::new($privateText, $inner)
            try { throw $outer }
            catch { $failureRecord = $_ }

            $failureDiagnostic = Get-CnMonMsixFailureDiagnostic -Mode Hosted -Gate account -Operation temporary_user_acl_setup -ErrorRecord $failureRecord
            Assert-Equal $failureDiagnostic.gate 'account' 'failure diagnostic lost the qualification gate'
            Assert-Equal $failureDiagnostic.operation 'temporary_user_acl_setup' 'failure diagnostic lost the safe account operation'
            Assert-Equal $failureDiagnostic.error_category $expectedCategory 'nested account error was not classified'
            Assert-Equal $failureDiagnostic.exception_type 'InvalidOperationException' 'outer exception type was not mapped to its fixed value'
            Assert-Equal $failureDiagnostic.inner_exception_type $expectedInnerType 'inner exception type was not mapped to its fixed value'
            Assert-True ($failureDiagnostic.hresult -is [ValueType]) 'diagnostic omitted the numeric HRESULT'
            Assert-True ($failureDiagnostic.script_line -is [ValueType]) 'diagnostic omitted the numeric source line'
            Assert-True ($failureDiagnostic.stack_lines.Count -le 8) 'diagnostic retained too many stack line numbers'
            foreach ($line in $failureDiagnostic.stack_lines) { Assert-True ($line -is [ValueType]) 'stack evidence included a nonnumeric value' }
            $safeText = ConvertTo-Json -InputObject $failureDiagnostic -Depth 5
            foreach ($sensitive in @($privateText, 'C:\Users\private', 'CN=Actual Publisher', 'Actual Publisher')) {
                Assert-True ($safeText -notmatch [regex]::Escape($sensitive)) "fixed failure diagnostic leaked '$sensitive'"
            }
            $untrustedValues = Get-CnMonMsixFailureDiagnostic -Mode $privateText -Gate $privateText -Operation $privateText -ErrorRecord $failureRecord
            Assert-Equal $untrustedValues.mode 'unknown' 'failure diagnostic accepted an unrecognized mode value'
            Assert-Equal $untrustedValues.gate 'unknown' 'failure diagnostic accepted an unrecognized gate value'
            Assert-Equal $untrustedValues.operation 'unknown' 'failure diagnostic accepted an unrecognized source operation'
            Assert-True ((ConvertTo-Json -InputObject $untrustedValues -Depth 5) -notmatch [regex]::Escape($privateText)) 'closed-field fallback leaked caller-controlled values'

            $publicDirectory = Join-Path $tempRoot 'general-failure-public'
            $null = New-Item -ItemType Directory -Path $publicDirectory
            Save-CnMonMsixQualificationFailureEvidence -Mode Hosted -Gate account -Operation temporary_user_acl_setup -ArtifactDirectory $publicDirectory -Identity $syntheticIdentity -ErrorRecord $failureRecord
            $publicPath = Join-Path $publicDirectory 'msix-signature-diagnostic.json'
            $publicText = [IO.File]::ReadAllText($publicPath)
            $publicDiagnostic = Get-Content -LiteralPath $publicPath -Raw | ConvertFrom-Json -AsHashtable
            Assert-Equal $publicDiagnostic.qualification_failure.gate 'account' 'outer-catch evidence omitted its gate'
            Assert-Equal $publicDiagnostic.qualification_failure.operation 'temporary_user_acl_setup' 'outer-catch evidence omitted its source operation'
            Assert-Equal $publicDiagnostic.qualification_failure.error_category $expectedCategory 'outer-catch evidence omitted its mapped error category'
            Assert-True ($publicText -notmatch [regex]::Escape($privateText)) 'public JSON included the private exception message'
            Assert-True ($publicText -notmatch [regex]::Escape('C:\Users\private')) 'public JSON included the private path'
            Assert-Equal (@(Get-ChildItem -LiteralPath (Join-Path $publicDirectory 'private-failure-records') -Filter '*.clixml').Count) 1 'synthetic error record was not retained in the private job directory'
            $privateXml = Get-Content -LiteralPath (Get-ChildItem -LiteralPath (Join-Path $publicDirectory 'private-failure-records') -Filter '*.clixml' | Select-Object -First 1).FullName -Raw
            Assert-True ($privateXml.Contains('private exception text')) 'full ErrorRecord was not retained privately'

            $manualDirectory = Join-Path $tempRoot 'general-failure-manual'
            $null = New-Item -ItemType Directory -Path $manualDirectory
            $env:GITHUB_EVENT_NAME = 'workflow_dispatch'
            Save-CnMonMsixQualificationFailureEvidence -Mode Hosted -Gate account -Operation temporary_user_acl_setup -ArtifactDirectory $manualDirectory -Identity $syntheticIdentity -ErrorRecord $failureRecord
            Assert-True (-not (Test-Path -LiteralPath (Join-Path $manualDirectory 'msix-signature-diagnostic.json'))) 'manual job wrote public failure evidence'
            Assert-Equal (@(Get-ChildItem -LiteralPath (Join-Path $manualDirectory 'private-failure-records') -Filter '*.clixml').Count) 1 'manual ErrorRecord was not retained privately'

            $env:GITHUB_EVENT_NAME = 'pull_request'
            $injectedDirectory = Join-Path $tempRoot 'general-failure-injected'
            $null = New-Item -ItemType Directory -Path $injectedDirectory
            Save-CnMonMsixQualificationFailureEvidence -Mode User -Gate fail_injection -Operation user_run_process_wait -ArtifactDirectory $injectedDirectory -Identity $syntheticIdentity -ErrorRecord $failureRecord -PrivateOnly
            Assert-True (-not (Test-Path -LiteralPath (Join-Path $injectedDirectory 'msix-signature-diagnostic.json'))) 'expected failure injection left a public diagnostic that could mask a later failure'
            Assert-Equal (@(Get-ChildItem -LiteralPath (Join-Path $injectedDirectory 'private-failure-records') -Filter '*.clixml').Count) 1 'expected failure injection did not preserve its private record'

            $outerCatchCalls = Get-CommandNodes $sourceAst 'Save-CnMonMsixQualificationFailureEvidence'
            Assert-Equal $outerCatchCalls.Count 2 'User and outer catches do not capture general failure evidence'
            $userMode = @($sourceAst.FindAll({
                param($candidate)
                $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $candidate.Name -eq 'Invoke-CnMonMsixUserMode'
            }, $true))[0]
            $userRunAst = @($sourceAst.FindAll({
                param($candidate)
                $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $candidate.Name -eq 'Invoke-CnMonMsixUserRun'
            }, $true))[0]
            Assert-Equal (Get-CommandNodes $userMode 'Save-CnMonMsixQualificationFailureEvidence').Count 1 'credentialed User-mode child does not capture its own swallowed failure'
            Assert-True ($userMode.Extent.Text -match 'PrivateOnly:\(\$script:InjectedFailure -and \$FailAfterInstall\)') 'expected injection can create a public record before real User-mode failures'
            foreach ($contextName in @('GITHUB_ACTIONS', 'GITHUB_EVENT_NAME', 'GITHUB_REPOSITORY', 'ImageOS')) {
                Assert-True ($userRunAst.Extent.Text.Contains("$contextName = " + '$env:' + $contextName)) "credentialed child does not receive the fixed diagnostic context $contextName"
            }
            $gateFailureCalls = Get-CommandNodes $sourceAst 'Set-CnMonMsixCaughtGateFailure'
            Assert-Equal $gateFailureCalls.Count 2 'both User and outer catches must distinguish default from explicit incomplete gates'

            $previousResult = $script:Result
            $previousGateUpdates = $script:GateUpdates
            try {
                $script:Result = [ordered]@{ gates = [ordered]@{ account = 'incomplete' } }
                $script:GateUpdates = [ordered]@{}
                Set-CnMonMsixCaughtGateFailure -Name account
                Assert-Equal $script:Result.gates.account 'fail' 'caught execution failure left a default-incomplete gate unchanged'
                $script:Result.gates.account = 'incomplete'
                $script:GateUpdates = [ordered]@{ account = 'incomplete' }
                Set-CnMonMsixCaughtGateFailure -Name account
                Assert-Equal $script:Result.gates.account 'incomplete' 'caught failure overwrote an explicitly recorded prerequisite-incomplete gate'
            }
            finally {
                $script:Result = $previousResult
                $script:GateUpdates = $previousGateUpdates
            }
        }
        finally {
            foreach ($key in $keys) { [Environment]::SetEnvironmentVariable($key, $old[$key], 'Process') }
        }
    }

    Invoke-Check 'temporary user account uses a SID ACL and owns partial profile cleanup' {
        $userRun = @($sourceAst.FindAll({
            param($candidate)
            $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $candidate.Name -eq 'Invoke-CnMonMsixUserRun'
        }, $true))[0]
        $aclConstructor = @($userRun.FindAll({
            param($candidate)
            $candidate -is [System.Management.Automation.Language.InvokeMemberExpressionAst] -and
                $candidate.Extent.Text -match 'FileSystemAccessRule\]::new'
        }, $true))
        Assert-True ($aclConstructor.Count -eq 1) 'User mode no longer creates its artifact ACL rule'
        Assert-True ($aclConstructor[0].Extent.Text -match '\$sidIdentity') 'artifact ACL rule does not use its SID identity'
        $sidIdentityAssignment = @($userRun.FindAll({
            param($candidate)
            $candidate -is [System.Management.Automation.Language.AssignmentStatementAst] -and
                $candidate.Left.Extent.Text -eq '$sidIdentity'
        }, $true)) | Select-Object -First 1
        Assert-True ($null -ne $sidIdentityAssignment -and $sidIdentityAssignment.Right.Extent.Text -match '\[Security\.Principal\.SecurityIdentifier\]::new\(\[string\]\$User\.SID\)') 'artifact ACL rule still binds a SID string through the NTAccount overload'

        $newUserFunction = @($sourceAst.FindAll({
            param($candidate)
            $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $candidate.Name -eq 'New-CnMonMsixTemporaryUser'
        }, $true))[0]
        $newLocalUser = Get-CommandNodes $newUserFunction 'New-LocalUser' | Select-Object -First 1
        $sidAssignment = @($newUserFunction.FindAll({
            param($candidate)
            $candidate -is [System.Management.Automation.Language.AssignmentStatementAst] -and
                $candidate.Left.Extent.Text -eq '$script:CreatedUserSid'
        }, $true)) | Select-Object -First 1
        Assert-True ($null -ne $newLocalUser -and $null -ne $sidAssignment) 'temporary user ownership does not record the returned SID'
        Assert-True ($sidAssignment.Extent.StartOffset -gt $newLocalUser.Extent.StartOffset -and $sidAssignment.Extent.StartOffset -lt $newUserFunction.Extent.EndOffset) 'owned SID is not recorded immediately after user creation'
        $createdNameAssignment = @($newUserFunction.FindAll({
            param($candidate)
            $candidate -is [System.Management.Automation.Language.AssignmentStatementAst] -and
                $candidate.Left.Extent.Text -eq '$script:CreatedUser'
        }, $true)) | Select-Object -First 1
        $sourceText = [IO.File]::ReadAllText($targetPath)
        $assignmentGap = $sourceText.Substring($createdNameAssignment.Extent.EndOffset, $sidAssignment.Extent.StartOffset - $createdNameAssignment.Extent.EndOffset)
        Assert-True ($assignmentGap -match '^\s*$') 'owned SID is not captured immediately alongside the newly created user name'
        Assert-True ($sidAssignment.Right.Extent.Text -match '\[string\]\$user\.SID\.Value') 'partial-profile cleanup SID is not captured from the owned New-LocalUser result'
        $outerCleanup = Get-CommandNodes $sourceAst 'Remove-CnMonMsixTemporaryUser' | Where-Object { $_.Extent.Text -match '\$script:CreatedUser\s+\$script:CreatedUserSid' }
        Assert-True ($outerCleanup.Count -ge 1) 'Hosted rollback does not pass the independently owned SID when profile initialization fails'

        $cleanupCalls = @(Invoke-MockedPartialUserCleanup)
        Assert-True ($cleanupCalls -contains 'user:owned-user') 'partial-user cleanup did not remove its owned account'
        Assert-True ($cleanupCalls -contains "profile-query:SID='S-1-5-21-123-456-789-1001'") 'partial-user cleanup did not query the owned SID profile'
        Assert-True ($cleanupCalls -contains 'profile:mock-profile') 'partial-user cleanup did not remove an existing partially initialized profile'

        if ([Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT) {
            $sid = [Security.Principal.WindowsIdentity]::GetCurrent().User
            $rule = [Security.AccessControl.FileSystemAccessRule]::new(
                [Security.Principal.SecurityIdentifier]::new([string]$sid.Value),
                'Modify', 'ContainerInherit,ObjectInherit', 'None', 'Allow'
            )
            $directorySecurity = [Security.AccessControl.DirectorySecurity]::new()
            $directorySecurity.AddAccessRule($rule)
            Assert-Equal $rule.IdentityReference.GetType().Name 'SecurityIdentifier' 'ACL constructor did not preserve SID identity'
        }
    }

    if ($failures.Count -gt 0) {
        throw "$($failures.Count) harness regression check(s) failed after $passed passed: $($failures -join ' | ')"
    }
    Write-Host "PASS all $passed Task2 harness regression checks"
}
finally {
    Remove-Item -LiteralPath $tempRoot -Recurse -Force -ErrorAction SilentlyContinue
}
