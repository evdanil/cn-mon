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

$repoRoot = Split-Path -Parent $PSScriptRoot
$targetPath = Join-Path $PSScriptRoot 'test-msix-windows.ps1'
$modulePath = Join-Path $PSScriptRoot 'msix-package.psm1'
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

# Import only pure helper definitions; the qualification script's top-level entry point is never executed.
$requiredFunctions = @(
    'Set-CnMonMsixGate',
    'Get-CnMonMsixNativeSelector',
    'Merge-CnMonMsixResultGates',
    'Get-CnMonMsixWackStatus',
    'Get-CnMonMsixTrustAction',
    'Test-CnMonMsixAdministratorsMembership',
    'Remove-CnMonMsixOwnedCertificate',
    'Add-CnMonMsixOwnedPackage',
    'Remove-CnMonMsixOwnedPackages',
    'Assert-CnMonMsixTargetsAbsent',
    'Get-CnMonMsixProfileTargets',
    'Invoke-CnMonMsixNativeTests'
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
$helperText = (@($functionAsts) + @($fixtureFilesFunctionAsts) + @($moduleFunctionAsts) | ForEach-Object { $_.Extent.Text }) -join [Environment]::NewLine
[IO.File]::WriteAllText($helperPath, $helperText, [Text.UTF8Encoding]::new($false))
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
        Assert-Equal ([IO.Path]::GetFullPath($targets.ProfileHistory)) ([IO.Path]::GetFullPath((Join-Path $local 'msix-profile-history.db'))) 'profile history path was not derived from LocalAppData'
        Assert-Equal ([IO.Path]::GetFullPath($targets.CliHistory)) ([IO.Path]::GetFullPath((Join-Path $local 'msix-cli-history.db'))) 'CLI history override path is wrong'
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
    }

    Invoke-Check 'Certify overlays touched gates without erasing Hosted failures' {
        $hosted = [ordered]@{ identity = 'pass'; signature = 'pass'; cleanup = 'pass'; native_cli = 'pass'; wack = 'incomplete' }
        $script:Result = [pscustomobject]@{ gates = [ordered]@{} }
        $script:GateUpdates = [ordered]@{}
        Set-CnMonMsixGate 'signature' 'fail'
        Set-CnMonMsixGate 'cleanup' 'fail'
        Set-CnMonMsixGate 'wack' 'incomplete'
        $mergeCalls = Get-CommandNodes $sourceAst 'Merge-CnMonMsixResultGates'
        Assert-Equal $mergeCalls.Count 1 'Certify finalization must merge the Hosted result exactly once'
        Assert-True ($mergeCalls[0].Extent.Text -match '\$existing\.gates\s+\$script:GateUpdates') 'Certify does not merge only its touched gates into the Hosted map'
        $merged = Merge-CnMonMsixResultGates -HostedGates $hosted -CertifyUpdates $script:GateUpdates
        Assert-Equal $merged.signature 'fail' 'Certify erased a signature failure'
        Assert-Equal $merged.cleanup 'fail' 'Certify erased a cleanup failure'
        Assert-Equal $merged.identity 'pass' 'Certify erased an untouched Hosted gate'
        Assert-Equal $merged.native_cli 'pass' 'Certify erased an untouched native gate'
        Assert-Equal $hosted.signature 'pass' 'merge modified the Hosted input map'
        Assert-Equal $hosted.cleanup 'pass' 'merge modified the Hosted input map'
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

    if ($failures.Count -gt 0) {
        throw "$($failures.Count) harness regression check(s) failed after $passed passed: $($failures -join ' | ')"
    }
    Write-Host "PASS all $passed Task2 harness regression checks"
}
finally {
    Remove-Item -LiteralPath $tempRoot -Recurse -Force -ErrorAction SilentlyContinue
}
