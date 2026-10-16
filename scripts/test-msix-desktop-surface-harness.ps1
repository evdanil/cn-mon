# Copyright 2026 cn-mon contributors
# SPDX-License-Identifier: Apache-2.0

$ErrorActionPreference = 'Stop'

function Assert-True([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw $Message }
}

function Assert-Equal([object]$Actual, [object]$Expected, [string]$Message) {
    if ($Actual -cne $Expected) { throw "$Message (expected '$Expected', got '$Actual')" }
}

function Stop-Process {
    param([int]$Id, [switch]$Force, [string]$ErrorAction)
    $script:stopProcessCalls++
}

$scriptPath = Join-Path $PSScriptRoot 'test-msix-desktop-surface.ps1'
if (-not (Test-Path -LiteralPath $scriptPath -PathType Leaf)) {
    throw 'desktop surface probe is missing'
}
$parseErrors = $null
$sourceAst = [System.Management.Automation.Language.Parser]::ParseFile($scriptPath, [ref]$null, [ref]$parseErrors)
if ($parseErrors.Count -gt 0) {
    throw "desktop surface probe did not parse: $($parseErrors[0].Message)"
}

$requiredFunctions = @(
    'Get-CnMonDesktopSurfaceDecision',
    'Test-CnMonDesktopSurfaceCandidate',
    'Set-CnMonDesktopSurfaceOwnership',
    'Close-CnMonDesktopSurfaceOwnedProcess',
    'Merge-CnMonDesktopRepairObservation',
    'New-CnMonDesktopSurfaceReport'
)
$functionAsts = @($sourceAst.FindAll({
    param($candidate)
    $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
        $requiredFunctions -contains $candidate.Name
}, $true))
if ($functionAsts.Count -ne $requiredFunctions.Count) {
    throw 'desktop surface pure projection helpers are missing'
}

$forbiddenCommands = @('Add-AppxPackage', 'Remove-AppxPackage', 'Repair-AppxPackage', 'Set-AppxPackage', 'Register-AppxPackage', 'Unregister-AppxPackage')
$commands = @($sourceAst.FindAll({ param($node) $node -is [System.Management.Automation.Language.CommandAst] }, $true) | ForEach-Object { $_.GetCommandName() })
foreach ($forbiddenCommand in $forbiddenCommands) {
    Assert-True ($commands -notcontains $forbiddenCommand) "probe contains forbidden package mutation command: $forbiddenCommand"
}
$sourceText = [IO.File]::ReadAllText($scriptPath)
foreach ($forbiddenPattern in @('InvokePattern', 'TogglePattern', 'SelectionItemPattern', 'SendKeys', 'SetValue')) {
    Assert-True (-not $sourceText.Contains($forbiddenPattern, [StringComparison]::Ordinal)) "probe contains a UI action API: $forbiddenPattern"
}
Assert-True ($sourceText.Contains('ms-settings:appsfeatures-app?', [StringComparison]::Ordinal)) 'probe does not use the documented Settings app URI'
Assert-True ($sourceText.Contains('$observed.candidate_installed = Test-CnMonDesktopSurfaceCandidate', [StringComparison]::Ordinal)) 'installed candidate was not recorded in the report state'
$contextGuard = $sourceText.IndexOf('if (-not $decision.eligible)', [StringComparison]::Ordinal)
$repairObservation = $sourceText.IndexOf('Test-CnMonDesktopRepairControl -ProcessId', [StringComparison]::Ordinal)
Assert-True ($contextGuard -ge 0 -and $repairObservation -gt $contextGuard) 'UI Automation can run before the SID/session guard'
$repairWaitGuard = $sourceText.IndexOf('if ($repairState.repair_present)', [StringComparison]::Ordinal)
Assert-True ($repairWaitGuard -gt $repairObservation -and -not $sourceText.Contains('$surface.ready', [StringComparison]::Ordinal)) 'navigation buttons can prematurely end the Repair observation window'
Assert-True ($sourceText.Contains('$observed.repair_button_status = ''absent''', [StringComparison]::Ordinal)) 'final absent status is not recorded after the observation window'
$packageCheck = $sourceText.IndexOf('Get-AppxPackage -Name', [StringComparison]::Ordinal)
$settingsLaunch = $sourceText.IndexOf('Start-Process -FilePath $uri', [StringComparison]::Ordinal)
Assert-True ($packageCheck -ge 0 -and $settingsLaunch -gt $packageCheck) 'Settings can launch before confirming the candidate package'

$tempRoot = Join-Path ([IO.Path]::GetTempPath()) ('cn-mon-msix-desktop-surface-' + [guid]::NewGuid().ToString('N'))
$null = New-Item -ItemType Directory -Path $tempRoot
$helperPath = Join-Path $tempRoot 'helpers.ps1'
[IO.File]::WriteAllText($helperPath, (@($functionAsts | ForEach-Object { $_.Extent.Text }) -join [Environment]::NewLine), [Text.UTF8Encoding]::new($false))
. $helperPath

try {
    Assert-True (Get-CnMonDesktopSurfaceDecision -SameSid $true -SameSession $true).eligible 'same-user/session Settings process was rejected'
    Assert-Equal (Get-CnMonDesktopSurfaceDecision -SameSid $false -SameSession $true).status 'wrong_security_context' 'different SID was not rejected'
    Assert-Equal (Get-CnMonDesktopSurfaceDecision -SameSid $true -SameSession $false).status 'wrong_security_context' 'different session was not rejected'

    $candidate = [pscustomobject]@{ Name = 'Example.CnMon'; PackageFamilyName = 'Example.CnMon_518g7wxbxvqf6' }
    Assert-True (Test-CnMonDesktopSurfaceCandidate -Packages @($candidate) -IdentityName $candidate.Name -ExpectedPackageFamilyName $candidate.PackageFamilyName) 'matching installed candidate was not recognized'
    Assert-True (-not (Test-CnMonDesktopSurfaceCandidate -Packages @($candidate) -IdentityName 'Other.CnMon' -ExpectedPackageFamilyName $candidate.PackageFamilyName)) 'same PFN with a mismatched package name was accepted'
    Assert-True (-not (Test-CnMonDesktopSurfaceCandidate -Packages @($candidate) -IdentityName $candidate.Name -ExpectedPackageFamilyName 'Other.CnMon_518g7wxbxvqf6')) 'mismatched candidate PFN was accepted'

    $ownershipObserved = @{ settings_opened_by_probe = $false; probe_status = 'not_attempted' }
    $owned = Set-CnMonDesktopSurfaceOwnership -Process $candidate -SameSid $false -SameSession $true -Observed $ownershipObserved
    Assert-Equal $null $owned 'wrong-context Settings process was marked as owned'
    Assert-Equal $ownershipObserved.settings_opened_by_probe $false 'wrong-context process was recorded as opened/owned'
    Assert-Equal $ownershipObserved.probe_status 'wrong_security_context' 'wrong-context process was not reported safely'
    $script:stopProcessCalls = 0
    Close-CnMonDesktopSurfaceOwnedProcess -Process $owned -StartTime ([DateTime]::UtcNow)
    Assert-Equal $script:stopProcessCalls 0 'wrong-context process was passed to cleanup'
    $ownershipObserved = @{ settings_opened_by_probe = $false; probe_status = 'not_attempted' }
    $owned = Set-CnMonDesktopSurfaceOwnership -Process $candidate -SameSid $true -SameSession $false -Observed $ownershipObserved
    Assert-Equal $null $owned 'different-session Settings process was marked as owned'
    Close-CnMonDesktopSurfaceOwnedProcess -Process $owned -StartTime ([DateTime]::UtcNow)
    Assert-Equal $script:stopProcessCalls 0 'different-session process was passed to cleanup'

    function New-MockedOwnedProcess {
        param([DateTime]$StartTime, [bool]$WaitSucceeds)
        $process = [pscustomobject]@{ Id = 4321; HasExited = $false; StartTime = $StartTime }
        $process | Add-Member ScriptMethod Refresh { }
        $process | Add-Member ScriptMethod CloseMainWindow { $script:closeWindowCalls++; return $true }
        $process | Add-Member ScriptMethod WaitForExit { param([int]$Milliseconds); $script:waitForExitCalls++; return $this.WaitSucceeds }
        $process | Add-Member NoteProperty WaitSucceeds $WaitSucceeds
        return $process
    }
    $startTime = [DateTime]::UtcNow
    $script:closeWindowCalls = 0
    $script:waitForExitCalls = 0
    $script:stopProcessCalls = 0
    $gracefulProcess = New-MockedOwnedProcess -StartTime $startTime -WaitSucceeds $true
    Close-CnMonDesktopSurfaceOwnedProcess -Process $gracefulProcess -StartTime $startTime
    Assert-Equal $script:closeWindowCalls 1 'owned process was not asked to close gracefully'
    Assert-Equal $script:waitForExitCalls 1 'owned process exit was not awaited'
    Assert-Equal $script:stopProcessCalls 0 'gracefully closed process was force-stopped'

    $script:closeWindowCalls = 0
    $script:waitForExitCalls = 0
    $script:stopProcessCalls = 0
    $stuckProcess = New-MockedOwnedProcess -StartTime $startTime -WaitSucceeds $false
    Close-CnMonDesktopSurfaceOwnedProcess -Process $stuckProcess -StartTime $startTime
    Assert-Equal $script:closeWindowCalls 1 'owned process was not asked to close before fallback'
    Assert-Equal $script:waitForExitCalls 1 'owned process timeout was not observed'
    Assert-Equal $script:stopProcessCalls 1 'owned process was not force-stopped after timeout'

    $script:closeWindowCalls = 0
    $script:waitForExitCalls = 0
    $script:stopProcessCalls = 0
    $reusedPidProcess = New-MockedOwnedProcess -StartTime $startTime.AddSeconds(1) -WaitSucceeds $false
    Close-CnMonDesktopSurfaceOwnedProcess -Process $reusedPidProcess -StartTime $startTime
    Assert-Equal $script:closeWindowCalls 0 'process with a reused PID and different start time was closed'
    Assert-Equal $script:stopProcessCalls 0 'process with a reused PID and different start time was force-stopped'

    $repairState = [pscustomobject]@{ buttons_seen = $false; repair_present = $false }
    $repairState = Merge-CnMonDesktopRepairObservation -Prior $repairState -Current ([pscustomobject]@{ buttons_seen = $true; repair = 'not_observed' })
    Assert-Equal $repairState.repair_present $false 'unrelated navigation button was mistaken for Repair'
    $repairState = Merge-CnMonDesktopRepairObservation -Prior $repairState -Current ([pscustomobject]@{ buttons_seen = $true; repair = 'present' })
    Assert-Equal $repairState.repair_present $true 'later Repair control was not observed after navigation buttons'

    $ownershipObserved = @{ settings_opened_by_probe = $false; probe_status = 'not_attempted' }
    $owned = Set-CnMonDesktopSurfaceOwnership -Process $candidate -SameSid $true -SameSession $true -Observed $ownershipObserved
    Assert-True ([object]::ReferenceEquals($owned, $candidate)) 'verified same-context Settings process was not owned'
    Assert-Equal $ownershipObserved.settings_opened_by_probe $true 'verified Settings process was not recorded as owned'

    $observed = @{
        candidate_installed = $true
        automation_process_session_id = '7'
        settings_opened_by_probe = $true
        settings_same_sid = $true
        settings_same_session = $true
        probe_status = 'observed'
        repair_button_status = 'present'
        identity_name = 'PRIVATE_IDENTITY_MARKER'
        package_family_name = 'PRIVATE_PFN_MARKER'
        sid = 'PRIVATE_SID_MARKER'
        ui_text = 'PRIVATE_UI_TEXT_MARKER'
        window_title = 'PRIVATE_TITLE_MARKER'
        raw_exception = 'PRIVATE_EXCEPTION_MARKER'
        path = 'PRIVATE_PATH_MARKER'
    }
    $report = New-CnMonDesktopSurfaceReport -Observed $observed
    $expectedProperties = @(
        'schema_version', 'candidate_installed', 'automation_process_session_id',
        'settings_opened_by_probe', 'settings_same_sid', 'settings_same_session',
        'probe_status', 'repair_button_status'
    )
    Assert-Equal ((@($report.PSObject.Properties.Name | Sort-Object)) -join ',') ((@($expectedProperties | Sort-Object)) -join ',') 'safe report fields changed'
    Assert-Equal $report.automation_process_session_id 7 'session ID was not normalized numerically'
    Assert-Equal $report.repair_button_status 'present' 'Repair button presence was not retained'
    $json = ConvertTo-Json -InputObject $report -Depth 2 -Compress
    foreach ($marker in @('PRIVATE_IDENTITY_MARKER', 'PRIVATE_PFN_MARKER', 'PRIVATE_SID_MARKER', 'PRIVATE_UI_TEXT_MARKER', 'PRIVATE_TITLE_MARKER', 'PRIVATE_EXCEPTION_MARKER', 'PRIVATE_PATH_MARKER')) {
        Assert-True (-not $json.Contains($marker, [StringComparison]::Ordinal)) "private value leaked into the report: $marker"
    }

    $invalid = New-CnMonDesktopSurfaceReport -Observed @{
        candidate_installed = 'true'
        automation_process_session_id = 'private'
        settings_opened_by_probe = $true
        settings_same_sid = $true
        settings_same_session = $false
        probe_status = 'raw exception text'
        repair_button_status = 'private button name'
    }
    Assert-Equal $invalid.candidate_installed $false 'non-boolean candidate state was accepted'
    Assert-Equal $invalid.automation_process_session_id 0 'non-numeric session ID was retained'
    Assert-Equal $invalid.probe_status 'not_attempted' 'unrecognized probe status was retained'
    Assert-Equal $invalid.repair_button_status 'not_observed' 'unrecognized UI state was retained'
}
finally {
    Remove-Item -LiteralPath $tempRoot -Recurse -Force -ErrorAction SilentlyContinue
}

Write-Host 'PASS desktop surface safety projection and session guard'
