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
    'New-CnMonMsixSessionRepairBreadcrumbMetadata',
    'Test-CnMonMsixSessionRepairActionAllowed',
    'Test-CnMonMsixSessionRepairObservationAllowsInvoke',
    'Get-CnMonMsixSessionSeedRequestId',
    'Get-CnMonMsixSessionHash',
    'Set-CnMonMsixSessionWorkFileHashes',
    'Test-CnMonMsixSessionSettingsWindowBinding',
    'Select-CnMonMsixSessionSettingsWindowContext',
    'Test-CnMonMsixOwnedShellProcess',
    'Stop-CnMonMsixSessionShellIfOwned',
    'Test-CnMonMsixSessionRecoveryPaths',
    'Invoke-CnMonMsixSessionReadOnlyPreflight',
    'Test-CnMonMsixOwnedFileCleanup',
'Remove-CnMonMsixSessionOwnedWorkDirectory',
    'Get-CnMonMsixCompletionScreenshotNames',
    'New-CnMonMsixDesktopCompletionSupplement',
    'Test-CnMonMsixSessionSignerWindow',
    'Get-CnMonMsixSessionSignerNotAfter',
    'Write-CnMonMsixSessionCheckpoint',
    'Get-CnMonMsixSessionCaptureBounds'
)
$functionAsts = @($sourceAst.FindAll({
    param($candidate)
    $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
        $helperNames -contains $candidate.Name
}, $true))
if ($functionAsts.Count -ne $helperNames.Count) { throw 'desktop session pure helpers are missing' }
$helperSource = @($functionAsts | ForEach-Object { $_.Extent.Text }) -join [Environment]::NewLine
. ([scriptblock]::Create($helperSource))

$signerStart = [DateTimeOffset]::Parse('2026-10-16T13:12:50Z')
$signerExpiry = [DateTimeOffset]::Parse('2026-10-16T13:22:32Z')
Assert-True (Test-CnMonMsixSessionSignerWindow -StartedUtc $signerStart -CompletedUtc ([DateTimeOffset]::Parse('2026-10-16T13:20:00Z')) -NotAfterUtc $signerExpiry) 'completion inside the signer window was rejected'
Assert-True (-not (Test-CnMonMsixSessionSignerWindow -StartedUtc $signerStart -CompletedUtc ([DateTimeOffset]::Parse('2026-10-16T13:22:33Z')) -NotAfterUtc $signerExpiry)) 'completion after signer expiry was accepted'
Assert-True (Test-CnMonMsixDesktopPhaseAllowed -Phase 'Cleanup' -Completed @{}) 'expired signer window blocked recovery cleanup'
$signerFixture = [pscustomobject]@{ Thumbprint = 'AABBCC'; NotAfter = [DateTime]::SpecifyKind([DateTime]::Parse('2026-10-16T13:22:32'), [DateTimeKind]::Utc) }
Assert-Equal (Get-CnMonMsixSessionSignerNotAfter -Certificate $signerFixture -ExpectedThumbprint 'AABBCC') '2026-10-16T13:22:32.0000000+00:00' 'verified certificate expiry was not normalized to UTC'
try { $null = Get-CnMonMsixSessionSignerNotAfter -Certificate $signerFixture -ExpectedThumbprint 'OTHER'; throw 'mismatched signer thumbprint was accepted' } catch { if ($_.Exception.Message -ceq 'mismatched signer thumbprint was accepted') { throw } }

$checkpointFixture = Join-Path ([IO.Path]::GetTempPath()) ('cnmon-checkpoint-' + [Guid]::NewGuid().ToString('N'))
$checkpointPayload = [Text.UTF8Encoding]::new($false).GetBytes('{"checkpoint":"first"}')
try {
    Write-CnMonMsixSessionCheckpoint -Path $checkpointFixture -Bytes $checkpointPayload
    Assert-Equal ([IO.File]::ReadAllText($checkpointFixture)) '{"checkpoint":"first"}' 'initial checkpoint was not created'
    try { Write-CnMonMsixSessionCheckpoint -Path $checkpointFixture -Bytes ([Text.UTF8Encoding]::new($false).GetBytes('{"checkpoint":"collision"}')); throw 'unowned initial checkpoint was replaced' } catch { if ($_.Exception.Message -ceq 'unowned initial checkpoint was replaced') { throw } }
    Assert-Equal ([IO.File]::ReadAllText($checkpointFixture)) '{"checkpoint":"first"}' 'CreateNew collision altered the prior checkpoint'
    if ([Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT) {
        $locked = [IO.File]::Open($checkpointFixture, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::None)
        try {
            try { Write-CnMonMsixSessionCheckpoint -Path $checkpointFixture -Bytes ([Text.UTF8Encoding]::new($false).GetBytes('{"checkpoint":"failed"}')) -ReplaceExisting; throw 'locked checkpoint replacement unexpectedly succeeded' } catch { if ($_.Exception.Message -ceq 'locked checkpoint replacement unexpectedly succeeded') { throw } }
        } finally { $locked.Dispose() }
        Assert-Equal ([IO.File]::ReadAllText($checkpointFixture)) '{"checkpoint":"first"}' 'failed replacement damaged the previously readable checkpoint'
    }
    Write-CnMonMsixSessionCheckpoint -Path $checkpointFixture -Bytes ([Text.UTF8Encoding]::new($false).GetBytes('{"checkpoint":"second"}')) -ReplaceExisting
    Assert-Equal ([IO.File]::ReadAllText($checkpointFixture)) '{"checkpoint":"second"}' 'checkpoint replacement did not preserve the new complete state'
    $temporaryPattern = '^' + [regex]::Escape((Split-Path $checkpointFixture -Leaf)) + '\.[0-9a-f]{32}\.tmp$'
    Assert-Equal (@(Get-ChildItem -LiteralPath (Split-Path $checkpointFixture) | Where-Object { $_.Name -match $temporaryPattern }).Count) 0 'checkpoint left a temporary file behind'
} finally { Remove-Item -LiteralPath $checkpointFixture -Force -ErrorAction SilentlyContinue }

$bounds = [pscustomobject]@{ X = 1920; Y = 0; Width = 1920; Height = 1080 }
Assert-Equal (Get-CnMonMsixSessionCaptureBounds -WindowHandle ([IntPtr]42) -ForegroundHandle ([IntPtr]42) -MonitorBounds $bounds).X 1920 'capture did not select the verified window monitor bounds'
Assert-True ($null -eq (Get-CnMonMsixSessionCaptureBounds -WindowHandle ([IntPtr]42) -ForegroundHandle ([IntPtr]43) -MonitorBounds $bounds)) 'capture accepted a window that was not foreground'

$requiredCommands = @('Add-AppxPackage', 'Remove-AppxPackage', 'CopyFromScreen', 'ms-settings:appsfeatures', 'CNMON_MSIX_APPDATA_PHASE')
$sourceText = [IO.File]::ReadAllText($scriptPath)
foreach ($required in $requiredCommands) {
    Assert-True ($sourceText.Contains($required, [StringComparison]::Ordinal)) "driver is missing actual desktop operation: $required"
}
Assert-True ($sourceText.Contains('[Windows.Forms.Screen]::FromHandle($WindowHandle).Bounds', [StringComparison]::Ordinal) -and
    -not $sourceText.Contains('[Windows.Forms.Screen]::PrimaryScreen.Bounds', [StringComparison]::Ordinal)) 'screenshots must use the verified window monitor bounds'
Assert-True ($sourceText.Contains('Get-CnMonMsixSessionSignerNotAfter -Certificate $signerCertificate', [StringComparison]::Ordinal) -and
    -not $sourceText.Contains("Parse('2026-10-16T13:22:32Z')", [StringComparison]::Ordinal)) 'completion expiry must come from the verified signer certificate'
Assert-True ($sourceText.Contains('[string]$ResultsPath', [StringComparison]::Ordinal) -and
    [regex]::IsMatch($sourceText, 'if \(\$Phase -eq ''Run''\) \{\r?\n\s+if \(\[string\]::IsNullOrWhiteSpace\(\$ResultsPath\)\)', [Text.RegularExpressions.RegexOptions]::CultureInvariant)) 'ResultsPath must only be resolved during Run'
foreach ($forbidden in @('Reset-AppxPackage', 'Register-AppxPackage', 'Unregister-AppxPackage', 'Invoke-Expression', 'SendKeys')) {
    Assert-True (-not $sourceText.Contains($forbidden, [StringComparison]::Ordinal)) "driver contains forbidden action: $forbidden"
}
Assert-True (-not $sourceText.Contains('$env:Path =', [StringComparison]::OrdinalIgnoreCase)) 'driver changes process PATH'
Assert-True (-not $sourceText.Contains("SetEnvironmentVariable('PATH'", [StringComparison]::OrdinalIgnoreCase)) 'driver changes PATH outside the process'
Assert-True ($sourceText.Contains('CreateNew', [StringComparison]::Ordinal)) 'driver does not protect private output ownership with create-new writes'
Assert-True ($sourceText.Contains('PackageFullName', [StringComparison]::Ordinal)) 'driver does not bind cleanup to the exact registered package full name'
Assert-True (-not $sourceText.Contains('$LASTEXITCODE -ne 0', [StringComparison]::Ordinal)) 'driver uses a native exit-code variable to judge a PowerShell preflight script'
Assert-True ($sourceText.Contains("'-w', 'new'", [StringComparison]::Ordinal)) 'listing screenshots do not use a dedicated Windows Terminal window'
Assert-True ($sourceText.Contains("'--title', `$terminalTitle, '--suppressApplicationTitle'", [StringComparison]::Ordinal)) 'owned Windows Terminal title can be overridden by the application title'
Assert-True ($sourceText.Contains('Test-CnMonMsixSessionRepairObservationAllowsInvoke -Observation $before', [StringComparison]::Ordinal)) 'repair activation is not gated by the observed button shape'
Assert-True ($sourceText.Contains('Export-Clixml', [StringComparison]::Ordinal)) 'private desktop failure diagnostics are not preserved before cleanup'

# Execute the generated runner's real argument selection and splatting. A one-item
# switch result otherwise becomes a string, which PowerShell splats as characters.
$runnerAssignmentAst = $sourceAst.Find({
    param($candidate)
    $candidate -is [System.Management.Automation.Language.AssignmentStatementAst] -and
        $candidate.Left.Extent.Text -ceq '$runner'
}, $true)
Assert-True ($null -ne $runnerAssignmentAst) 'generated Terminal runner is missing'
$runnerText = $runnerAssignmentAst.Right.Expression.Value
$runnerErrors = $null
$runnerAst = [System.Management.Automation.Language.Parser]::ParseInput($runnerText, [ref]$null, [ref]$runnerErrors)
Assert-Equal $runnerErrors.Count 0 'generated Terminal runner does not parse'
$runnerArgumentsAst = $runnerAst.Find({
    param($candidate)
    $candidate -is [System.Management.Automation.Language.AssignmentStatementAst] -and
        $candidate.Left.Extent.Text -match '\$arguments$'
}, $true)
$runnerInvokeAst = $runnerAst.Find({
    param($candidate)
    $candidate -is [System.Management.Automation.Language.CommandAst] -and
        $candidate.Extent.Text -ceq '& $AliasPath @arguments'
}, $true)
Assert-True ($null -ne $runnerArgumentsAst -and $null -ne $runnerInvokeAst) 'generated Terminal argument construction or invocation is missing'
function Save-CnMonMsixHarnessTerminalArguments {
    $script:CapturedTerminalArguments = @($args)
}
$runnerArgumentProbe = [scriptblock]::Create($runnerArgumentsAst.Extent.Text + [Environment]::NewLine + $runnerInvokeAst.Extent.Text)
foreach ($case in @(
    @{ Mode = 'help'; Expected = @('--help') },
    @{ Mode = 'table'; Expected = @('127.0.0.1', '--format', 'table', '--history-file', 'C:\private work\history.db', '--log-file', 'C:\private work\cn.log') },
    @{ Mode = 'json-tcp'; Expected = @('127.0.0.1', '--format', 'json', '--tcp', '12345', '--history-file', 'C:\private work\history.db', '--log-file', 'C:\private work\cn.log') },
    @{ Mode = 'history-list'; Expected = @('--list', '--history-file', 'C:\private work\history.db', '--log-file', 'C:\private work\cn.log') },
    @{ Mode = 'history-detail'; Expected = @('--show', 'abcd1234', '--history-file', 'C:\private work\history.db', '--log-file', 'C:\private work\cn.log') }
)) {
    $Mode = $case.Mode
    $AliasPath = 'Save-CnMonMsixHarnessTerminalArguments'
    $HistoryPath = 'C:\private work\history.db'
    $LogPath = 'C:\private work\cn.log'
    $Port = '12345'
    $RequestId = 'abcd1234'
    & $runnerArgumentProbe
    Assert-Equal $script:CapturedTerminalArguments.Count $case.Expected.Count "Terminal $Mode command has the wrong argument count"
    for ($index = 0; $index -lt $case.Expected.Count; $index++) {
        Assert-Equal $script:CapturedTerminalArguments[$index] $case.Expected[$index] "Terminal $Mode argument $index changed during splatting"
    }
}
Remove-Item Function:Save-CnMonMsixHarnessTerminalArguments
$repairObservationAst = $sourceAst.Find({
    param($candidate)
    $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
        $candidate.Name -ceq 'Get-CnMonMsixSessionRepairObservation'
}, $true)
Assert-True ($null -ne $repairObservationAst) 'desktop Repair observation constructor is missing'
Assert-True ($repairObservationAst.Extent.Text.Contains('automation_id = [string]$_.Current.AutomationId', [StringComparison]::Ordinal) -and
    $repairObservationAst.Extent.Text.Contains('element = $repairControls[0]', [StringComparison]::Ordinal) -and
    $repairObservationAst.Extent.Text.Contains('enabled = [bool]$repairControls[0].Current.IsEnabled', [StringComparison]::Ordinal)) 'actual Repair observation does not bind its exact control, UIA element, and enabled state'
$openSettingsAst = $sourceAst.Find({
    param($candidate)
    $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
        $candidate.Name -ceq 'Open-CnMonMsixSessionSettings'
}, $true)
Assert-True ($null -ne $openSettingsAst) 'Settings window discovery function is missing'
Assert-True ($openSettingsAst.Extent.Text.Contains('Start-Process -FilePath $uri -ErrorAction Stop', [StringComparison]::Ordinal)) 'Settings URI launch changed'
Assert-True (-not $openSettingsAst.Extent.Text.Contains('-PassThru', [StringComparison]::Ordinal)) 'Settings URI launch requests a ShellExecute process handle'
Assert-True ($openSettingsAst.Extent.Text.Contains('Select-CnMonMsixSessionSettingsWindowContext', [StringComparison]::Ordinal)) 'Settings discovery does not reject ambiguous root windows'
$automationWindowsAst = $sourceAst.Find({
    param($candidate)
    $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
        $candidate.Name -ceq 'Get-CnMonMsixSessionAutomationWindows'
}, $true)
Assert-True ($automationWindowsAst.Extent.Text.Contains('NativeWindowHandleProperty', [StringComparison]::Ordinal)) 'Settings UIA reacquisition is not scoped to the exact HWND'
Assert-True ($automationWindowsAst.Extent.Text.Contains('Test-CnMonMsixSessionSettingsWindowBinding', [StringComparison]::Ordinal)) 'Settings UIA reacquisition does not revalidate its process binding'
Assert-True ($sourceText.Contains('Set-CnMonMsixSessionWindowForeground -Process $settings.Process -WindowHandle $settings.WindowHandle', [StringComparison]::Ordinal)) 'Settings foregrounding does not use the selected HWND'
$terminalWindowAst = $sourceAst.Find({
    param($candidate)
    $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
        $candidate.Name -ceq 'Find-CnMonMsixSessionWindowHandle'
}, $true)
Assert-True ($terminalWindowAst.Extent.Text.Contains('Get-CnMonMsixSessionAutomationWindows -Process $Process', [StringComparison]::Ordinal)) 'Terminal UIA process-window behavior changed'
$terminalFunction = . ([scriptblock]::Create($terminalWindowAst.Extent.Text))
function Get-CnMonMsixSessionAutomationWindows { return $script:TerminalMockWindows }
$unrelatedTerminalWindow = [pscustomobject]@{ Current = [pscustomobject]@{ Name = 'Windows Terminal'; NativeWindowHandle = [IntPtr]101 } }
$ownedTerminalWindow = [pscustomobject]@{ Current = [pscustomobject]@{ Name = 'cnmon-12345678-help pwsh.exe'; NativeWindowHandle = [IntPtr]202 } }
$script:TerminalMockWindows = @($unrelatedTerminalWindow, $ownedTerminalWindow)
$terminalHandle = Find-CnMonMsixSessionWindowHandle -Process ([Diagnostics.Process]::GetCurrentProcess()) -TitleToken 'cnmon-12345678-help'
Assert-Equal ([int64]$terminalHandle) 202 'Terminal lookup selected an unrelated window ahead of the exact owned title'
$script:TerminalMockWindows = @($unrelatedTerminalWindow)
try { $null = Find-CnMonMsixSessionWindowHandle -Process ([Diagnostics.Process]::GetCurrentProcess()) -TitleToken 'cnmon-12345678-help'; throw 'unmatched Terminal window was accepted' } catch { if ($_.Exception.Message -ceq 'unmatched Terminal window was accepted') { throw } }
$prepareAst = $sourceAst.Find({
    param($candidate)
    $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
        $candidate.Name -ceq 'Invoke-CnMonMsixSessionPrepare'
}, $true)
$prepareSource = $prepareAst.Extent.Text
Assert-True ($prepareSource.IndexOf('Open-CnMonMsixSessionSettings -Page Aliases', [StringComparison]::Ordinal) -lt
    $prepareSource.IndexOf('Add-AppxPackage -Path $script:PackageFullPath', [StringComparison]::Ordinal)) 'Settings binding is not verified before package installation'
Assert-True ($prepareSource.Contains('settings_pid', [StringComparison]::Ordinal) -and $prepareSource.Contains('host_pid', [StringComparison]::Ordinal) -and
    $prepareSource.Contains('hwnd', [StringComparison]::Ordinal) -and $prepareSource.Contains('user_sid', [StringComparison]::Ordinal)) 'private pre-install Settings witness omits identity or window fields'
$aliasPhaseAst = $sourceAst.Find({
    param($candidate)
    $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and
        $candidate.Name -ceq 'Invoke-CnMonMsixSessionAliasState'
}, $true)
$aliasPhaseSource = $aliasPhaseAst.Extent.Text
Assert-True (-not $aliasPhaseSource.Contains('Test-CnMonMsixSessionAliasToggle', [StringComparison]::Ordinal) -and
    -not $aliasPhaseSource.Contains('TogglePattern', [StringComparison]::Ordinal)) 'alias phase still depends on a UIA toggle pattern'
Assert-True ($aliasPhaseSource.IndexOf('Wait-CnMonMsixSessionUserConfirmation', [StringComparison]::Ordinal) -lt
    $aliasPhaseSource.IndexOf('Get-CnMonMsixSessionAutomationWindows', [StringComparison]::Ordinal) -and
    $aliasPhaseSource.IndexOf('Get-CnMonMsixSessionAutomationWindows', [StringComparison]::Ordinal) -lt
    $aliasPhaseSource.IndexOf('Save-CnMonMsixSessionScreenshot', [StringComparison]::Ordinal) -and
    $aliasPhaseSource.IndexOf('Save-CnMonMsixSessionScreenshot', [StringComparison]::Ordinal) -lt
    $aliasPhaseSource.IndexOf('Invoke-CnMonMsixSessionShellSet', [StringComparison]::Ordinal) -and
    $aliasPhaseSource.IndexOf('Invoke-CnMonMsixSessionShellSet', [StringComparison]::Ordinal) -lt
    $aliasPhaseSource.IndexOf('settings_verified = $true', [StringComparison]::Ordinal)) 'alias evidence is marked before exact-window capture and native behavior'
$aliasFunction = $aliasPhaseAst.Extent.Text
. ([scriptblock]::Create($aliasFunction))
function Open-CnMonMsixSessionSettings { $p = [Diagnostics.Process]::GetCurrentProcess(); return @{ Process = $p; SettingsProcess = $p; WindowHandle = [IntPtr]1 } }
function Wait-CnMonMsixSessionUserConfirmation {}
function Get-CnMonMsixSessionAutomationWindows { ,@([pscustomobject]@{ verified = $true }) }
function Set-CnMonMsixSessionWindowForeground {}
function Save-CnMonMsixSessionScreenshot { param($Name, $WindowHandle, [switch]$Listing) return $Name }
function Invoke-CnMonMsixSessionShellSet { if ($script:AliasMockNativeFails) { throw 'mock native failure' }; return @{} }
function Save-CnMonMsixSessionState {}
function Write-CnMonMsixSessionSupplement {}
function Initialize-AliasMockState {
    $script:State = @{ completed = @{ prepare = $true; alias_off = $false; alias_on = $false }; path_before = [Environment]::GetEnvironmentVariable('PATH', 'Process');
        screenshots = [Collections.ArrayList]::new(); observations = @{ alias = @{ off = @{ settings_opened = $false; settings_verified = $false; pwsh_failure = $false; cmd_failure = $false }; on = @{ settings_opened = $false; settings_verified = $false; pwsh_success = $false; cmd_success = $false; packaged_process_identity_verified = $false }; path_unchanged = $false } } }
}
Initialize-AliasMockState
$script:AliasMockNativeFails = $false
Invoke-CnMonMsixSessionAliasState -AliasState Off
Assert-True $script:State.observations.alias.off.settings_verified 'operator-confirmed Settings evidence without a TogglePattern was not accepted'
Initialize-AliasMockState
$script:AliasMockNativeFails = $true
try { Invoke-CnMonMsixSessionAliasState -AliasState Off; throw 'mock native failure was not propagated' } catch { Assert-True (-not $script:State.observations.alias.off.settings_verified -and -not $script:State.completed.alias_off) 'native failure incorrectly marked alias evidence complete' }
$repairFunctionAst = $sourceAst.Find({ param($candidate) $candidate -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $candidate.Name -ceq 'Invoke-CnMonMsixSessionRepair' }, $true)
. ([scriptblock]::Create($repairFunctionAst.Extent.Text))
$repairPhaseSource = $repairFunctionAst.Extent.Text
Assert-True ($repairPhaseSource.IndexOf('Wait-CnMonMsixSessionUserConfirmation', [StringComparison]::Ordinal) -lt $repairPhaseSource.IndexOf('Save-CnMonMsixSessionScreenshot', [StringComparison]::Ordinal) -and
    $repairPhaseSource.IndexOf('Wait-CnMonMsixSessionUserConfirmation', [StringComparison]::Ordinal) -lt $repairPhaseSource.IndexOf('Get-CnMonMsixSessionRepairObservation', [StringComparison]::Ordinal) -and
    $repairPhaseSource.IndexOf('Get-CnMonMsixSessionRepairObservation', [StringComparison]::Ordinal) -lt $repairPhaseSource.IndexOf('$invokePattern.Invoke()', [StringComparison]::Ordinal)) 'Repair operator confirmation is not before observation and activation'
$repairConfirmationIndex = $repairPhaseSource.IndexOf('Wait-CnMonMsixSessionUserConfirmation', [StringComparison]::Ordinal)
$repairRefocusIndex = $repairPhaseSource.IndexOf('Set-CnMonMsixSessionWindowForeground', $repairConfirmationIndex, [StringComparison]::Ordinal)
Assert-True ($repairConfirmationIndex -lt $repairRefocusIndex -and $repairRefocusIndex -lt $repairPhaseSource.IndexOf('Save-CnMonMsixSessionScreenshot', [StringComparison]::Ordinal)) 'Repair window is not refocused after confirmation and before capture'
Assert-True ($repairPhaseSource.Contains("-RequiredText 'READY'", [StringComparison]::Ordinal) -and $repairPhaseSource.Contains('$script:Receipt.package_version', [StringComparison]::Ordinal) -and $repairPhaseSource.Contains('$script:Receipt.publisher_display_name', [StringComparison]::Ordinal)) 'Repair prompt does not identify the exact candidate and require READY confirmation'
function Get-CnMonMsixSessionDataDigest { 'fixture-digest' }
function Save-CnMonMsixSessionRepairUiTrace {}
function Get-CnMonMsixSessionRepairObservation { $script:RepairObservationCalls++; $script:RepairMockEvents.Add('observation'); throw 'stop after observed confirmation ordering' }
function Set-CnMonMsixSessionWindowForeground { $script:RepairMockEvents.Add('foreground') }
function Save-CnMonMsixSessionScreenshot { param($Name, $WindowHandle, [switch]$Listing) $script:RepairScreenshotCalls++; $script:RepairMockEvents.Add('screenshot'); return 'fixture.png' }
function Wait-CnMonMsixSessionUserConfirmation { if ($script:RejectRepairConfirmation) { throw 'desktop observation was not confirmed' }; $script:RepairConfirmed = $true; $script:RepairMockEvents.Add('confirmation') }
function Initialize-RepairMockState {
    $script:State = @{ completed = @{ prepare = $true; alias_off = $true; alias_on = $true }; owned_files = @(); screenshots = [Collections.ArrayList]::new(); package_full_name = 'fixture'; observations = @{ repair = @{ data_before_sha256 = $null } } }
}
$script:RepairObservationCalls = 0; $script:RepairScreenshotCalls = 0; $script:RepairConfirmed = $false; $script:RepairMockEvents = [Collections.ArrayList]::new()
$script:RejectRepairConfirmation = $true; Initialize-RepairMockState
try { Invoke-CnMonMsixSessionRepair; throw 'rejected Repair confirmation was not enforced' } catch {}
Assert-True ($script:RepairObservationCalls -eq 0 -and $script:RepairScreenshotCalls -eq 0 -and -not $script:RepairConfirmed) 'Repair observation or capture ran after rejected confirmation'
$script:RejectRepairConfirmation = $false; Initialize-RepairMockState
try { Invoke-CnMonMsixSessionRepair; throw 'Repair observation stop was not reached' } catch {}
Assert-True ($script:RepairConfirmed -and $script:RepairObservationCalls -eq 1 -and $script:RepairScreenshotCalls -eq 1) 'valid Repair confirmation did not precede capture and observation'
Assert-True ($script:RepairMockEvents.IndexOf('confirmation') -lt $script:RepairMockEvents.LastIndexOf('foreground') -and $script:RepairMockEvents.LastIndexOf('foreground') -lt $script:RepairMockEvents.IndexOf('screenshot') -and $script:RepairMockEvents.IndexOf('screenshot') -lt $script:RepairMockEvents.IndexOf('observation')) 'Repair capture sequence did not refocus after confirmation'
$directWindow = [pscustomobject]@{ process_id = 201; session_id = 1; window_handle = 0x100; class_name = 'SystemSettings'; descendant_process_ids = @() }
Assert-True (Test-CnMonMsixSessionSettingsWindowBinding -Window $directWindow -SettingsProcessId 201 -OwnerProcessId 201 -ExpectedSessionId 1 -ExpectedWindowHandle 0x100 -SettingsIdentityVerified $true -OwnerIdentityVerified $true) 'direct Settings window binding was rejected'
$hostedWindow = [pscustomobject]@{ process_id = 301; session_id = 1; window_handle = 0x200; class_name = 'ApplicationFrameWindow'; descendant_process_ids = @(201) }
Assert-True (Test-CnMonMsixSessionSettingsWindowBinding -Window $hostedWindow -SettingsProcessId 201 -OwnerProcessId 301 -ExpectedSessionId 1 -ExpectedWindowHandle 0x200 -SettingsIdentityVerified $true -OwnerIdentityVerified $true) 'verified hosted Settings descendant binding was rejected'
Assert-True (-not (Test-CnMonMsixSessionSettingsWindowBinding -Window $hostedWindow -SettingsProcessId 201 -OwnerProcessId 301 -ExpectedSessionId 1 -ExpectedWindowHandle 0x200 -SettingsIdentityVerified $true -OwnerIdentityVerified $false)) 'wrong host owner SID was accepted'
Assert-True (-not (Test-CnMonMsixSessionSettingsWindowBinding -Window $hostedWindow -SettingsProcessId 201 -OwnerProcessId 301 -ExpectedSessionId 2 -ExpectedWindowHandle 0x200 -SettingsIdentityVerified $true -OwnerIdentityVerified $true)) 'wrong host session was accepted'
Assert-True (-not (Test-CnMonMsixSessionSettingsWindowBinding -Window $hostedWindow -SettingsProcessId 201 -OwnerProcessId 999 -ExpectedSessionId 1 -ExpectedWindowHandle 0x200 -SettingsIdentityVerified $true -OwnerIdentityVerified $true)) 'unrelated frame host was accepted'
$unrelatedFrame = $hostedWindow.PSObject.Copy(); $unrelatedFrame.descendant_process_ids = @(999)
Assert-True (-not (Test-CnMonMsixSessionSettingsWindowBinding -Window $unrelatedFrame -SettingsProcessId 201 -OwnerProcessId 301 -ExpectedSessionId 1 -ExpectedWindowHandle 0x200 -SettingsIdentityVerified $true -OwnerIdentityVerified $true)) 'frame without the exact Settings descendant was accepted'
Assert-True (-not (Test-CnMonMsixSessionSettingsWindowBinding -Window $hostedWindow -SettingsProcessId 201 -OwnerProcessId 301 -ExpectedSessionId 1 -ExpectedWindowHandle 0x201 -SettingsIdentityVerified $true -OwnerIdentityVerified $true)) 'swapped Settings window HWND was accepted'
Assert-True (-not (Test-CnMonMsixSessionSettingsWindowBinding -Window $hostedWindow -SettingsProcessId 201 -OwnerProcessId 301 -ExpectedSessionId 1 -ExpectedWindowHandle 0x200 -SettingsIdentityVerified $false -OwnerIdentityVerified $true)) 'wrong Settings process owner SID was accepted'
Assert-True ($null -eq (Select-CnMonMsixSessionSettingsWindowContext -Candidates @($hostedWindow, $directWindow))) 'ambiguous Settings top-level windows were accepted'
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

function New-RepairWindowMetadata {
    param([string]$Heading = 'cn-mon — Network Monitor', [string]$Publisher = 'Example Publisher', [string]$Version = '1.3.0.0', [bool]$HeadingVisible = $true, [bool]$RepairVisible = $true, [bool]$RepairEnabled = $true, [int]$PageCount = 1, [int]$SpecificationsCount = 1, [int]$BreadcrumbBarCount = 1)
    $specifications = @(); for ($i = 0; $i -lt $SpecificationsCount; $i++) { $specifications += [pscustomobject]@{ name = 'Specifications'; names = @('Specifications', 'Publisher', $Publisher, 'Version', $Version) } }
    $page = [pscustomobject]@{ specifications = $specifications; repair_buttons = @(@{ automation_id = 'SystemSettings_StorageSense_AppSizesAdvanced_AppRepair_Button'; name = 'Repair'; visible = $RepairVisible; enabled = $RepairEnabled }) }
    $breadcrumb = New-CnMonMsixSessionRepairBreadcrumbMetadata -AutomationId 'PermanentNavigationViewBreadcrumbBar' -VisibleNames $(if ($HeadingVisible) { @($Heading) } else { @() })
    return [pscustomobject]@{ page_content = @($page) * $PageCount; breadcrumb_bar_count = $BreadcrumbBarCount; breadcrumb_bars = @($breadcrumb) }
}
$repairMetadata = New-RepairWindowMetadata
Assert-True (Test-CnMonMsixSessionRepairTarget -WindowMetadata $repairMetadata -ExpectedPublisher 'Example Publisher' -ExpectedVersion '1.3.0.0') 'scoped candidate Settings metadata with offscreen Specifications values was rejected'
Assert-True (Test-CnMonMsixSessionRepairTarget -WindowMetadata (New-RepairWindowMetadata -Heading 'cn-mon — Network Monitor') -ExpectedPublisher 'Example Publisher' -ExpectedVersion '1.3.0.0') 'reserved-title Settings metadata was rejected'
Assert-True (-not (Test-CnMonMsixSessionRepairTarget -WindowMetadata (New-RepairWindowMetadata -Heading 'cn-mon Network Monitor') -ExpectedPublisher 'Example Publisher' -ExpectedVersion '1.3.0.0')) 'old unreserved Settings title was accepted'
$duplicateBreadcrumbLabels = New-CnMonMsixSessionRepairBreadcrumbMetadata -AutomationId 'PermanentNavigationViewBreadcrumbBar' -VisibleNames @('cn-mon — Network Monitor', 'cn-mon — Network Monitor')
Assert-True ($duplicateBreadcrumbLabels.match_count -eq 2 -and (Test-CnMonMsixSessionRepairTarget -WindowMetadata ([pscustomobject]@{ page_content = $repairMetadata.page_content; breadcrumb_bar_count = 1; breadcrumb_bars = @($duplicateBreadcrumbLabels) }) -ExpectedPublisher 'Example Publisher' -ExpectedVersion '1.3.0.0')) 'button and text labels for one visible breadcrumb were treated as ambiguous'
Assert-True (Test-CnMonMsixSessionRepairTarget -WindowMetadata (New-RepairWindowMetadata -RepairEnabled $false) -ExpectedPublisher 'Example Publisher' -ExpectedVersion '1.3.0.0') 'disabled candidate Repair control was lost during progress observation'
Assert-True (-not (Test-CnMonMsixSessionRepairTarget -WindowMetadata (New-RepairWindowMetadata -Heading 'Other app') -ExpectedPublisher 'Example Publisher' -ExpectedVersion '1.3.0.0')) 'wrong visible breadcrumb was accepted'
Assert-True (-not (Test-CnMonMsixSessionRepairTarget -WindowMetadata (New-RepairWindowMetadata -Publisher 'Other publisher') -ExpectedPublisher 'Example Publisher' -ExpectedVersion '1.3.0.0')) 'wrong scoped Specifications publisher was accepted'
Assert-True (-not (Test-CnMonMsixSessionRepairTarget -WindowMetadata (New-RepairWindowMetadata -Version '9.9.9.9') -ExpectedPublisher 'Example Publisher' -ExpectedVersion '1.3.0.0')) 'wrong scoped Specifications version was accepted'
Assert-True (-not (Test-CnMonMsixSessionRepairTarget -WindowMetadata (New-RepairWindowMetadata -HeadingVisible $false) -ExpectedPublisher 'Example Publisher' -ExpectedVersion '1.3.0.0')) 'offscreen exact breadcrumb was accepted'
Assert-True (-not (Test-CnMonMsixSessionRepairTarget -WindowMetadata (New-RepairWindowMetadata -RepairVisible $false) -ExpectedPublisher 'Example Publisher' -ExpectedVersion '1.3.0.0')) 'offscreen Repair control was accepted'
Assert-True (-not (Test-CnMonMsixSessionRepairTarget -WindowMetadata (New-RepairWindowMetadata -PageCount 2) -ExpectedPublisher 'Example Publisher' -ExpectedVersion '1.3.0.0')) 'duplicate pageContent was accepted'
Assert-True (-not (Test-CnMonMsixSessionRepairTarget -WindowMetadata (New-RepairWindowMetadata -SpecificationsCount 0) -ExpectedPublisher 'Example Publisher' -ExpectedVersion '1.3.0.0')) 'missing Specifications group was accepted'
Assert-True (-not (Test-CnMonMsixSessionRepairTarget -WindowMetadata (New-RepairWindowMetadata -SpecificationsCount 2) -ExpectedPublisher 'Example Publisher' -ExpectedVersion '1.3.0.0')) 'duplicate Specifications groups were accepted'
Assert-True (-not (Test-CnMonMsixSessionRepairTarget -WindowMetadata (New-RepairWindowMetadata -BreadcrumbBarCount 2) -ExpectedPublisher 'Example Publisher' -ExpectedVersion '1.3.0.0')) 'duplicate breadcrumb bars were accepted'
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
$seededRequestId = Get-CnMonMsixSessionSeedRequestId -Json '{"requests":[{"id":"77db8c93"}]}'
Assert-Equal $seededRequestId '77db8c93' 'history listing request id did not use the published JSON schema field'
foreach ($badRequestJson in @('{"requests":[{"request_id":"77db8c93"}]}', '{"requests":[{"id":"bad-id"}]}', '{"requests":[]}')) {
    try { $null = Get-CnMonMsixSessionSeedRequestId -Json $badRequestJson; throw 'invalid history request ID was accepted' } catch { if ($_.Exception.Message -ceq 'invalid history request ID was accepted') { throw } }
}
$workFixture = Join-Path ([IO.Path]::GetTempPath()) ('cnmon-workhash fixture ' + [Guid]::NewGuid().ToString('N'))
[IO.Directory]::CreateDirectory($workFixture) | Out-Null
try {
    $historyFixture = Join-Path $workFixture 'history.db'; $logFixture = Join-Path $workFixture 'cn.log'
    [IO.File]::WriteAllText($historyFixture, 'history-before'); [IO.File]::WriteAllText($logFixture, 'log-before')
    foreach ($sidecar in @('history.db-wal', 'history.db-shm', 'history.db-journal')) { [IO.File]::WriteAllText((Join-Path $workFixture $sidecar), $sidecar) }
    $script:State = @{ work_directory = $workFixture; work_files = @(
        @{ path = $historyFixture; sha256 = (Get-CnMonMsixSessionHash $historyFixture); owned = $true },
        @{ path = $logFixture; sha256 = (Get-CnMonMsixSessionHash $logFixture); owned = $true }) }
    [IO.File]::AppendAllText($logFixture, ' appended by listing')
    Set-CnMonMsixSessionWorkFileHashes -Paths @($historyFixture, $logFixture)
    Assert-True (@($script:State.work_files | Where-Object { [IO.Path]::GetFileName([string]$_.path) -in @('history.db-wal', 'history.db-shm', 'history.db-journal') }).Count -eq 3) 'listing output refresh did not record present known SQLite companions'
    try { $null = Get-CnMonMsixSessionSeedRequestId -Json '{malformed'; throw 'malformed listing JSON was accepted' } catch { if ($_.Exception.Message -ceq 'malformed listing JSON was accepted') { throw } }
    $recordedLogHash = [string](@($script:State.work_files | Where-Object { $_.path -ceq $logFixture })[0].sha256)
    Assert-Equal $recordedLogHash (Get-CnMonMsixSessionHash $logFixture) 'listing log output was not refreshed before parser failure'
    Assert-True (Test-CnMonMsixOwnedFileCleanup -OwnedByThisRun $true -ExpectedSha256 $recordedLogHash -ActualSha256 (Get-CnMonMsixSessionHash $logFixture)) 'refreshed listing log did not remain eligible for owned cleanup'
} finally { Remove-Item -LiteralPath $workFixture -Recurse -Force -ErrorAction SilentlyContinue }

$cleanupFixture = Join-Path ([IO.Path]::GetTempPath()) ('cnmon-workcleanup fixture ' + [Guid]::NewGuid().ToString('N'))
[IO.Directory]::CreateDirectory($cleanupFixture) | Out-Null
try {
    $workEntries = @()
    foreach ($name in @('history.db', 'history.db-wal', 'history.db-shm', 'history.db-journal', 'cn.log')) {
        $path = Join-Path $cleanupFixture $name; [IO.File]::WriteAllText($path, $name)
        $workEntries += [ordered]@{ path = $path; sha256 = Get-CnMonMsixSessionHash $path; owned = $true }
    }
    $unknownPath = Join-Path $cleanupFixture 'unexpected.tmp'; [IO.File]::WriteAllText($unknownPath, 'preserve')
    Assert-True (-not (Remove-CnMonMsixSessionOwnedWorkDirectory -Path $cleanupFixture -OwnedByThisRun $true -WorkFiles $workEntries)) 'unknown work file did not keep cleanup incomplete'
    foreach ($name in @('history.db', 'history.db-wal', 'history.db-shm', 'history.db-journal', 'cn.log')) { Assert-True (-not (Test-Path (Join-Path $cleanupFixture $name))) "known hash-matched file '$name' was not removed" }
    Assert-Equal ([IO.File]::ReadAllText($unknownPath)) 'preserve' 'cleanup altered an unknown work file'
    Remove-Item -LiteralPath $unknownPath -Force
} finally { Remove-Item -LiteralPath $cleanupFixture -Recurse -Force -ErrorAction SilentlyContinue }

$mismatchFixture = Join-Path ([IO.Path]::GetTempPath()) ('cnmon-workmismatch fixture ' + [Guid]::NewGuid().ToString('N'))
[IO.Directory]::CreateDirectory($mismatchFixture) | Out-Null
try {
    $changedSidecar = Join-Path $mismatchFixture 'history.db-shm'; $matchingSidecar = Join-Path $mismatchFixture 'history.db-wal'
    [IO.File]::WriteAllText($changedSidecar, 'changed'); [IO.File]::WriteAllText($matchingSidecar, 'wal')
    $entries = @(@{ path = $changedSidecar; sha256 = ('0' * 64); owned = $true }, @{ path = $matchingSidecar; sha256 = (Get-CnMonMsixSessionHash $matchingSidecar); owned = $true })
    Assert-True (-not (Remove-CnMonMsixSessionOwnedWorkDirectory -Path $mismatchFixture -OwnedByThisRun $true -WorkFiles $entries)) 'changed sidecar did not block cleanup'
    Assert-True (Test-Path -LiteralPath $changedSidecar) 'changed sidecar was deleted despite a hash mismatch'
    Assert-True (-not (Test-Path -LiteralPath $matchingSidecar)) 'matching sidecar was not removed'
} finally { Remove-Item -LiteralPath $mismatchFixture -Recurse -Force -ErrorAction SilentlyContinue }

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
    work_files = @(
        [ordered]@{ path = [IO.Path]::GetFullPath((Join-Path (Join-Path $profileRoot ('cn-mon-msix-desktop-session-' + $runId)) 'history.db')); sha256 = $hashA; owned = $true },
        [ordered]@{ path = [IO.Path]::GetFullPath((Join-Path (Join-Path $profileRoot ('cn-mon-msix-desktop-session-' + $runId)) 'history.db-wal')); sha256 = $hashA; owned = $true },
        [ordered]@{ path = [IO.Path]::GetFullPath((Join-Path (Join-Path $profileRoot ('cn-mon-msix-desktop-session-' + $runId)) 'history.db-shm')); sha256 = $hashB; owned = $true })
    owned_shells = @([ordered]@{ pid = 100; parent_pid = 200; token = $runId; runner_path = (Join-Path $artifactRoot 'desktop-session-terminal-command.ps1'); mode = 'help'; exited = $true })
}
Assert-True (Test-CnMonMsixSessionRecoveryPaths -State $recoveryState -UserProfile $profileRoot -LocalAppData $localRoot -ArtifactDirectory $artifactRoot) 'valid recovery state paths were rejected'
$sentinelPath = Join-Path $profileRoot 'unrelated-sentinel.txt'
[IO.File]::WriteAllText($sentinelPath, 'keep')
$recoveryState.owned_files[0].path = $sentinelPath
Assert-True (-not (Test-CnMonMsixSessionRecoveryPaths -State $recoveryState -UserProfile $profileRoot -LocalAppData $localRoot -ArtifactDirectory $artifactRoot)) 'tampered recovery state accepted an unrelated sentinel path'
Assert-Equal ([IO.File]::ReadAllText($sentinelPath)) 'keep' 'state validation altered an unrelated sentinel'
$recoveryState.owned_files[0].path = [IO.Path]::GetFullPath((Join-Path $profileRoot '.cn'))
$recoveryState.work_files += [ordered]@{ path = [IO.Path]::GetFullPath((Join-Path $recoveryState.work_directory 'history.db-unexpected')); sha256 = $hashA; owned = $true }
Assert-True (-not (Test-CnMonMsixSessionRecoveryPaths -State $recoveryState -UserProfile $profileRoot -LocalAppData $localRoot -ArtifactDirectory $artifactRoot)) 'state validation accepted an unknown SQLite sidecar suffix'

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
