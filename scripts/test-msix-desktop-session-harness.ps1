# Copyright 2026 cn-mon contributors
# SPDX-License-Identifier: Apache-2.0

$ErrorActionPreference = 'Stop'

function Assert-True([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw $Message }
}

function Assert-Equal([object]$Actual, [object]$Expected, [string]$Message) {
    if ($Actual -cne $Expected) { throw "$Message (expected '$Expected', got '$Actual')" }
}

$scriptPath = Join-Path $PSScriptRoot 'test-msix-desktop-session.ps1'
if (-not (Test-Path -LiteralPath $scriptPath -PathType Leaf)) { throw 'desktop session driver is missing' }
$parseErrors = $null
$sourceAst = [System.Management.Automation.Language.Parser]::ParseFile($scriptPath, [ref]$null, [ref]$parseErrors)
if ($parseErrors.Count -gt 0) { throw "desktop session driver did not parse: $($parseErrors[0].Message)" }

$helperNames = @(
    'Test-CnMonMsixDesktopPhaseAllowed',
    'Test-CnMonMsixSessionStateBinding',
    'Test-CnMonMsixSessionRepairTarget',
    'Test-CnMonMsixSessionRepairActionAllowed',
    'Test-CnMonMsixSessionRepairObservationAllowsInvoke',
    'Test-CnMonMsixOwnedShellProcess',
    'Stop-CnMonMsixSessionShellIfOwned',
    'Test-CnMonMsixSessionRecoveryPaths',
    'Invoke-CnMonMsixSessionReadOnlyPreflight',
    'Test-CnMonMsixOwnedFileCleanup',
    'Get-CnMonMsixCompletionScreenshotNames',
    'New-CnMonMsixDesktopCompletionSupplement'
)
$functionAsts = @($sourceAst.FindAll({
    param($candidate)
    $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
        $helperNames -contains $candidate.Name
}, $true))
if ($functionAsts.Count -ne $helperNames.Count) { throw 'desktop session pure helpers are missing' }
$helperSource = @($functionAsts | ForEach-Object { $_.Extent.Text }) -join [Environment]::NewLine
. ([scriptblock]::Create($helperSource))

$requiredCommands = @('Add-AppxPackage', 'Remove-AppxPackage', 'CopyFromScreen', 'ms-settings:appsfeatures', 'CNMON_MSIX_APPDATA_PHASE')
$sourceText = [IO.File]::ReadAllText($scriptPath)
foreach ($required in $requiredCommands) {
    Assert-True ($sourceText.Contains($required, [StringComparison]::Ordinal)) "driver is missing actual desktop operation: $required"
}
foreach ($forbidden in @('Reset-AppxPackage', 'Register-AppxPackage', 'Unregister-AppxPackage', 'Invoke-Expression', 'SendKeys')) {
    Assert-True (-not $sourceText.Contains($forbidden, [StringComparison]::Ordinal)) "driver contains forbidden action: $forbidden"
}
Assert-True (-not $sourceText.Contains('$env:Path =', [StringComparison]::OrdinalIgnoreCase)) 'driver changes process PATH'
Assert-True (-not $sourceText.Contains("SetEnvironmentVariable('PATH'", [StringComparison]::OrdinalIgnoreCase)) 'driver changes PATH outside the process'
Assert-True ($sourceText.Contains('CreateNew', [StringComparison]::Ordinal)) 'driver does not protect private output ownership with create-new writes'
Assert-True ($sourceText.Contains('PackageFullName', [StringComparison]::Ordinal)) 'driver does not bind cleanup to the exact registered package full name'
Assert-True (-not $sourceText.Contains('$LASTEXITCODE -ne 0', [StringComparison]::Ordinal)) 'driver uses a native exit-code variable to judge a PowerShell preflight script'
Assert-True ($sourceText.Contains("'-w', 'new'", [StringComparison]::Ordinal)) 'listing screenshots do not use a dedicated Windows Terminal window'
Assert-True ($sourceText.Contains('Test-CnMonMsixSessionRepairObservationAllowsInvoke -Observation $before', [StringComparison]::Ordinal)) 'repair activation is not gated by the observed button shape'
Assert-True ($sourceText.Contains('Export-Clixml', [StringComparison]::Ordinal)) 'private desktop failure diagnostics are not preserved before cleanup'
$repairObservationAst = $sourceAst.Find({
    param($candidate)
    $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
        $candidate.Name -ceq 'Get-CnMonMsixSessionRepairObservation'
}, $true)
Assert-True ($null -ne $repairObservationAst) 'desktop Repair observation constructor is missing'
Assert-True ([regex]::IsMatch($repairObservationAst.Extent.Text, '\$button\s*=\s*\[pscustomobject\]@\{\s*name\s*=\s''Repair''\s+element\s*=\s*\$element\s+enabled\s*=')) 'actual Repair observation does not bind its exact name, UIA element, and enabled state'
$seedAst = $sourceAst.Find({
    param($candidate)
    $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
        $candidate.Name -ceq 'Invoke-CnMonMsixSessionSeed'
}, $true)
Assert-True ($null -ne $seedAst) 'profile seed function is missing'
$configAssignment = $seedAst.Find({
    param($candidate)
    $candidate -is [System.Management.Automation.Language.AssignmentStatementAst] -and
        $candidate.Left.Extent.Text -ceq '$config'
}, $true)
Assert-True ($null -ne $configAssignment) 'profile seed config rendering is missing'
$profileFixture = Join-Path ([IO.Path]::GetTempPath()) ('cnmon-profile fixture ' + [Guid]::NewGuid().ToString('N'))
$profileHistory = [IO.Path]::GetFullPath((Join-Path $profileFixture 'msix-profile-history.db'))
$profileLog = [IO.Path]::GetFullPath((Join-Path $profileFixture 'msix-profile.log'))
$renderProfileConfig = [scriptblock]::Create($configAssignment.Right.Extent.Text)
$renderedProfileConfig = & $renderProfileConfig
Assert-True ($renderedProfileConfig.Contains("history_file = $profileHistory`n", [StringComparison]::Ordinal)) 'profile history path was not rendered as an absolute path with spaces preserved'
Assert-True ($renderedProfileConfig.Contains("logfile = $profileLog`n", [StringComparison]::Ordinal)) 'profile log path was not rendered as an absolute path with spaces preserved'

$empty = @{}
Assert-True (Test-CnMonMsixDesktopPhaseAllowed -Phase 'Prepare' -Completed $empty) 'Prepare should be allowed first'
Assert-True (-not (Test-CnMonMsixDesktopPhaseAllowed -Phase 'AliasOff' -Completed $empty)) 'Alias Off ran without preparation'
$completed = @{ prepare = $true }
Assert-True (Test-CnMonMsixDesktopPhaseAllowed -Phase 'AliasOff' -Completed $completed) 'Alias Off should follow preparation'
Assert-True (-not (Test-CnMonMsixDesktopPhaseAllowed -Phase 'RepairAfter' -Completed $completed)) 'Repair readback ran before activation'
$completed.alias_off = $true
$completed.alias_on = $true
$completed.repair_before = $true
Assert-True (Test-CnMonMsixDesktopPhaseAllowed -Phase 'RepairAfter' -Completed $completed) 'Repair readback should follow an observed activation'
Assert-True (-not (Test-CnMonMsixDesktopPhaseAllowed -Phase 'Screenshots' -Completed $completed)) 'screenshots ran before repair readback'
Assert-True (Test-CnMonMsixDesktopPhaseAllowed -Phase 'Cleanup' -Completed @{}) 'Cleanup must remain available for recovery'

Assert-True (Test-CnMonMsixSessionRepairTarget -VisibleNames @('cn-mon Network Monitor', 'Example Publisher', '1.3.0.0') -RepairButtonName 'Repair' -ExpectedPublisher 'Example Publisher' -ExpectedVersion '1.3.0.0') 'exact candidate Settings page was rejected'
Assert-True (Test-CnMonMsixSessionRepairTarget -VisibleNames @('cn-mon Network Monitor', 'Example Publisher', '1.3.0.0') -RepairButtonName 'Repair' -ExpectedPublisher 'Example Publisher' -ExpectedVersion '1.3.0.0') 'disabled candidate Repair control was lost during progress observation'
Assert-True (-not (Test-CnMonMsixSessionRepairActionAllowed -CandidatePageVerified $true -RepairButtonName 'Repair' -RepairButtonEnabled $false)) 'disabled Repair control authorized activation or completion'
Assert-True (Test-CnMonMsixSessionRepairActionAllowed -CandidatePageVerified $true -RepairButtonName 'Repair' -RepairButtonEnabled $true) 'enabled candidate Repair control was rejected'
$repairObservation = [pscustomobject]@{
    candidate_page_verified = $true
    repair_button = [pscustomobject]@{ name = 'Repair'; enabled = $false; element = 'UIA element placeholder' }
}
Assert-True (-not (Test-CnMonMsixSessionRepairObservationAllowsInvoke -Observation $repairObservation)) 'disabled button from the real observation shape authorized Invoke'
$repairObservation.repair_button.enabled = $true
Assert-True (Test-CnMonMsixSessionRepairObservationAllowsInvoke -Observation $repairObservation) 'enabled button from the real observation shape did not authorize Invoke'
$repairObservation.repair_button.PSObject.Properties.Remove('name')
Assert-True (-not (Test-CnMonMsixSessionRepairObservationAllowsInvoke -Observation $repairObservation)) 'observation missing its bound Repair name authorized Invoke'
Assert-True (-not (Test-CnMonMsixSessionRepairTarget -VisibleNames @('Other application', 'Example Publisher', '1.3.0.0') -OffscreenNames @('cn-mon Network Monitor') -RepairButtonName 'Repair' -ExpectedPublisher 'Example Publisher' -ExpectedVersion '1.3.0.0')) 'an offscreen previous cn-mon heading authorized Repair on another app page'
Assert-True (-not (Test-CnMonMsixSessionRepairTarget -VisibleNames @('cn-mon Network Monitor', 'Example Publisher', '1.3.0.0') -RepairButtonName 'Reset' -ExpectedPublisher 'Example Publisher' -ExpectedVersion '1.3.0.0')) 'Reset was accepted as Repair'

$ownedRunner = 'C:\private\desktop-session-terminal-command.ps1'
$shellIdentity = @{
    executable_path = 'C:\Program Files\PowerShell\7\pwsh.exe'
    session_id = 7
    owner_sid = 'S-1-5-21-1000'
    command_line = 'pwsh.exe -File C:\private\desktop-session-terminal-command.ps1 -Token 0123456789abcdef0123456789abcdef'
}
$shellState = @{ pid = 444; token = '0123456789abcdef0123456789abcdef'; runner_path = $ownedRunner }
$expectedShell = @{
    executable_path = $shellIdentity.executable_path
    session_id = 7
    owner_sid = 'S-1-5-21-1000'
    runner_path = $ownedRunner
}
$global:stoppedShellIds = [Collections.Generic.List[int]]::new()
function Stop-Process {
    param([int]$Id, [switch]$Force, [string]$ErrorAction)
    $global:stoppedShellIds.Add($Id)
}
$wrongTokenIdentity = $shellIdentity.Clone()
$wrongTokenIdentity.command_line = 'pwsh.exe -File C:\private\desktop-session-terminal-command.ps1 -Token ffffffffffffffffffffffffffffffff'
Assert-True (-not (Stop-CnMonMsixSessionShellIfOwned -Shell $shellState -ProcessIdentity $wrongTokenIdentity -Expected $expectedShell)) 'same-user process with a different token was treated as run-owned'
Assert-Equal $global:stoppedShellIds.Count 0 'wrong-token process reached Stop-Process'
$wrongRunnerIdentity = $shellIdentity.Clone()
$wrongRunnerIdentity.command_line = 'pwsh.exe -File C:\private\other-runner.ps1 -Token 0123456789abcdef0123456789abcdef'
$wrongRunnerIdentity.runner_path = 'C:\private\other-runner.ps1'
Assert-True (-not (Stop-CnMonMsixSessionShellIfOwned -Shell $shellState -ProcessIdentity $wrongRunnerIdentity -Expected $expectedShell)) 'same-user process with a different runner was treated as run-owned'
Assert-Equal $global:stoppedShellIds.Count 0 'wrong-runner process reached Stop-Process'
Assert-True (Stop-CnMonMsixSessionShellIfOwned -Shell $shellState -ProcessIdentity $shellIdentity -Expected $expectedShell) 'exact run-owned process was not eligible for Stop-Process'
Assert-Equal $global:stoppedShellIds.Count 1 'verified process did not reach Stop-Process exactly once'

$hashA = 'a' * 64
$hashB = 'b' * 64
$boundState = @{ package_sha256 = $hashA; receipt_sha256 = $hashB; helper_sha256 = $hashA }
Assert-True (Test-CnMonMsixSessionStateBinding -State $boundState -PackageSha256 $hashA -ReceiptSha256 $hashB -HelperSha256 $hashA) 'exact candidate state binding was rejected'
Assert-True (-not (Test-CnMonMsixSessionStateBinding -State $boundState -PackageSha256 $hashB -ReceiptSha256 $hashB -HelperSha256 $hashA)) 'state from another package was accepted'
Assert-True (-not (Test-CnMonMsixSessionStateBinding -State $boundState -PackageSha256 $hashA -ReceiptSha256 $hashB -HelperSha256 $hashB)) 'state from another native helper was accepted'

Assert-True (Test-CnMonMsixOwnedFileCleanup -OwnedByThisRun $true -ExpectedSha256 $hashA -ActualSha256 $hashA) 'unchanged run-owned file was not eligible for cleanup'
Assert-True (-not (Test-CnMonMsixOwnedFileCleanup -OwnedByThisRun $false -ExpectedSha256 $hashA -ActualSha256 $hashA)) 'preexisting file was eligible for cleanup'
Assert-True (-not (Test-CnMonMsixOwnedFileCleanup -OwnedByThisRun $true -ExpectedSha256 $hashA -ActualSha256 $hashB)) 'changed user file was eligible for cleanup'

$testRoot = Join-Path ([IO.Path]::GetTempPath()) ('cnmon-desktop-session-harness-' + [Guid]::NewGuid().ToString('N'))
$profileRoot = Join-Path $testRoot 'profile'
$localRoot = Join-Path $testRoot 'local'
$artifactRoot = Join-Path $testRoot 'artifacts'
$null = New-Item -ItemType Directory -Path $profileRoot, $localRoot, $artifactRoot -Force
$runId = '0123456789abcdef0123456789abcdef'
$profileNames = @('.cn', '.cn-ping-history.db', 'cn.log', 'msix-profile-history.db', 'msix-profile.log', 'msix-cli-history.db', 'msix-cli.log')
$appNames = @('cn-mon-msix-desktop-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.db', 'cn-mon-msix-desktop-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb.log')
$appRoot = Join-Path $localRoot 'Packages\Example.CnMon_test\LocalCache\Local'
$recoveryState = [ordered]@{
    run_id = $runId; artifact_directory = $artifactRoot; expected_pfn = 'Example.CnMon_test'
    planned_user_paths = @($profileNames | ForEach-Object { [IO.Path]::GetFullPath((Join-Path $profileRoot $_)) })
    owned_files = @([ordered]@{ path = [IO.Path]::GetFullPath((Join-Path $profileRoot '.cn')); sha256 = $hashA; owned = $true })
    appdata_history_name = $appNames[0]; appdata_log_name = $appNames[1]
    planned_appdata_paths = @($appNames | ForEach-Object { [IO.Path]::GetFullPath((Join-Path $appRoot $_)) })
    owned_appdata_files = @([ordered]@{ path = [IO.Path]::GetFullPath((Join-Path $appRoot $appNames[0])); sha256 = $hashB; owned = $true })
    work_directory = [IO.Path]::GetFullPath((Join-Path $profileRoot ('cn-mon-msix-desktop-session-' + $runId))); work_directory_owned = $true
    work_files = @([ordered]@{ path = [IO.Path]::GetFullPath((Join-Path (Join-Path $profileRoot ('cn-mon-msix-desktop-session-' + $runId)) 'history.db')); sha256 = $hashA; owned = $true })
    owned_shells = @([ordered]@{ pid = 100; parent_pid = 200; token = $runId; runner_path = (Join-Path $artifactRoot 'desktop-session-terminal-command.ps1'); mode = 'help'; exited = $true })
}
Assert-True (Test-CnMonMsixSessionRecoveryPaths -State $recoveryState -UserProfile $profileRoot -LocalAppData $localRoot -ArtifactDirectory $artifactRoot) 'valid recovery state paths were rejected'
$sentinelPath = Join-Path $profileRoot 'unrelated-sentinel.txt'
[IO.File]::WriteAllText($sentinelPath, 'keep')
$recoveryState.owned_files[0].path = $sentinelPath
Assert-True (-not (Test-CnMonMsixSessionRecoveryPaths -State $recoveryState -UserProfile $profileRoot -LocalAppData $localRoot -ArtifactDirectory $artifactRoot)) 'tampered recovery state accepted an unrelated sentinel path'
Assert-Equal ([IO.File]::ReadAllText($sentinelPath)) 'keep' 'state validation altered an unrelated sentinel'

$preflightStub = Join-Path $testRoot 'preflight-stub.ps1'
$stubSource = @'
param($PackagePath,$ReceiptPath,$ResultsPath,$NativeHelperPath,$ExpectedNativeHelperSha256,$ArtifactDirectory)
$global:LASTEXITCODE = 41
$summary = [ordered]@{
  preflight_status='ready_for_review'
  preflight=[ordered]@{desktop_eligible=$true;user_token_eligible=$true;interactive_session_eligible=$true;input_desktop_available=$true;medium_integrity=$true;signature_valid=$true;signer_trusted=$true;candidate_not_registered=$true}
  evidence=[ordered]@{signed_package_sha256='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa';signed_receipt_sha256='bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb';native_helper_sha256='cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc'}
}
[IO.File]::WriteAllText((Join-Path $ArtifactDirectory 'msix-desktop-supplement.json'),(ConvertTo-Json $summary -Depth 8))
'@
[IO.File]::WriteAllText($preflightStub, $stubSource)
$preflightArtifact = Join-Path $testRoot 'preflight-stale'
$null = New-Item -ItemType Directory -Path $preflightArtifact
$global:LASTEXITCODE = 17
$validPreflight = Invoke-CnMonMsixSessionReadOnlyPreflight -ScriptPath $preflightStub -PackagePath 'package' -ReceiptPath 'receipt' -ResultsPath 'results' -NativeHelperPath 'helper' -HelperSha256 ('c' * 64) -ArtifactDirectory $preflightArtifact -ExpectedPackageSha256 ('a' * 64) -ExpectedReceiptSha256 ('b' * 64)
Assert-Equal $validPreflight.preflight_status 'ready_for_review' 'stale native exit code overrode a successful preflight PowerShell script'
$preflightArtifact2 = Join-Path $testRoot 'preflight-unset'
$null = New-Item -ItemType Directory -Path $preflightArtifact2
$global:LASTEXITCODE = $null
$validPreflight2 = Invoke-CnMonMsixSessionReadOnlyPreflight -ScriptPath $preflightStub -PackagePath 'package' -ReceiptPath 'receipt' -ResultsPath 'results' -NativeHelperPath 'helper' -HelperSha256 ('c' * 64) -ArtifactDirectory $preflightArtifact2 -ExpectedPackageSha256 ('a' * 64) -ExpectedReceiptSha256 ('b' * 64)
Assert-Equal $validPreflight2.preflight_status 'ready_for_review' 'unset native exit code prevented a successful preflight PowerShell script'
Remove-Item -LiteralPath $testRoot -Recurse -Force

$names = @(Get-CnMonMsixCompletionScreenshotNames)
Assert-Equal $names.Count 10 'screenshot allowlist size changed'
Assert-Equal ($names -join ',') 'alias-off-settings.png,alias-on-settings.png,repair-start.png,repair-progress.png,repair-complete.png,01-cli-help.png,02-icmp-table.png,03-json-tcp.png,04-history-list.png,05-history-detail.png' 'screenshot allowlist changed'

$privateMarker = 'PRIVATE_PROFILE_PATH_MARKER'
$candidate = [ordered]@{
    run_id = '38866515555'
    artifact_id = '12031747143'
    archive_sha256 = 'c' * 64
    signed_package_sha256 = $hashA
    signed_receipt_sha256 = $hashB
    certify_result_sha256 = 'd' * 64
    packaging_revision = 'e' * 40
}
$scripts = [ordered]@{ helper_sha256 = $hashA; desktop_driver_sha256 = $hashB; validator_revision = 'f' * 40 }
$observations = @{
    ordinary_user = $true
    medium_integrity = $true
    interactive_session = $true
    alias = @{
        off = @{ settings_opened = $true; settings_verified = $true; pwsh_failure = $true; pwsh_success = $false; cmd_failure = $true; cmd_success = $false }
        on = @{ settings_opened = $true; settings_verified = $true; pwsh_failure = $false; pwsh_success = $true; cmd_failure = $false; cmd_success = $true; packaged_process_identity_verified = $true }
        path_unchanged = $true
    }
    repair = @{ activation_observed = $false; completion_observed = $false; data_before_sha256 = ''; data_after_sha256 = ''; readback_verified = $false }
    screenshots = @()
    desktop_cleanup_passed = $false
    private_profile = $privateMarker
}
$report = New-CnMonMsixDesktopCompletionSupplement -Candidate $candidate -QualificationScripts $scripts -Observations $observations
$expectedRoot = @('schema_version', 'mode', 'overall', 'candidate', 'qualification_scripts', 'observations')
Assert-Equal ((@($report.Keys | Sort-Object -CaseSensitive)) -join ',') ((@($expectedRoot | Sort-Object -CaseSensitive)) -join ',') 'supplement root schema changed'
Assert-Equal $report.schema_version 1 'supplement schema version changed'
Assert-Equal $report.mode 'desktop_completion_supplement' 'supplement mode changed'
Assert-Equal $report.overall 'incomplete' 'driver auto-qualified evidence before independent review'
Assert-Equal $report.observations.repair.activation_observed $false 'unobserved repair was marked complete'
Assert-Equal $report.observations.desktop_cleanup_passed $false 'cleanup was auto-qualified'
$json = ConvertTo-Json -InputObject $report -Depth 8 -Compress
Assert-True (-not $json.Contains($privateMarker, [StringComparison]::Ordinal)) 'private desktop path leaked into supplement'
Assert-Equal ((@($report.observations.Keys | Sort-Object -CaseSensitive)) -join ',') ((@(@('ordinary_user', 'medium_integrity', 'interactive_session', 'alias', 'repair', 'screenshots', 'desktop_cleanup_passed') | Sort-Object -CaseSensitive)) -join ',') 'supplement observation schema changed'

Write-Host 'PASS desktop session phase, binding, cleanup and privacy harness'
