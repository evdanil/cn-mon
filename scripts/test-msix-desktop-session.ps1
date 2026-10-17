# Copyright 2026 cn-mon contributors
# SPDX-License-Identifier: Apache-2.0

[CmdletBinding()]
param(
    [ValidateSet('Run', 'Cleanup')][string]$Phase = 'Run',
    [Parameter(Mandatory = $true)][string]$PackagePath,
    [Parameter(Mandatory = $true)][string]$ReceiptPath,
    [string]$ResultsPath,
    [Parameter(Mandatory = $true)][string]$NativeHelperPath,
    [Parameter(Mandatory = $true)][string]$ExpectedNativeHelperSha256,
    [Parameter(Mandatory = $true)][string]$ExpectedValidatorRevision,
    [Parameter(Mandatory = $true)][string]$ArtifactDirectory
)

$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false

function Test-CnMonMsixDesktopPhaseAllowed {
    param([string]$Phase, [System.Collections.IDictionary]$Completed)
    $required = switch ($Phase) {
        'Prepare' { @() }
        'AliasOff' { @('prepare') }
        'AliasOn' { @('prepare', 'alias_off') }
        'RepairBefore' { @('prepare', 'alias_off', 'alias_on') }
        'RepairAfter' { @('prepare', 'alias_off', 'alias_on', 'repair_before') }
        'Screenshots' { @('prepare', 'alias_off', 'alias_on', 'repair_before', 'repair_after') }
        'Cleanup' { @() }
        default { return $false }
    }
    foreach ($name in $required) { if ($Completed[$name] -ne $true) { return $false } }
    return $true
}

function Test-CnMonMsixSessionStateBinding {
    param(
        [System.Collections.IDictionary]$State,
        [string]$PackageSha256,
        [string]$ReceiptSha256,
        [string]$HelperSha256
    )
    return $null -ne $State -and
        [string]$State.package_sha256 -ceq $PackageSha256 -and
        [string]$State.receipt_sha256 -ceq $ReceiptSha256 -and
        [string]$State.helper_sha256 -ceq $HelperSha256
}

function Test-CnMonMsixSessionSignerWindow {
    param([DateTimeOffset]$StartedUtc, [DateTimeOffset]$CompletedUtc, [DateTimeOffset]$NotAfterUtc)
    return $StartedUtc.Offset -eq [TimeSpan]::Zero -and $CompletedUtc.Offset -eq [TimeSpan]::Zero -and
        $NotAfterUtc.Offset -eq [TimeSpan]::Zero -and $StartedUtc -le $CompletedUtc -and $CompletedUtc -le $NotAfterUtc
}

function Get-CnMonMsixSessionSignerNotAfter {
    param([object]$Certificate, [string]$ExpectedThumbprint)
    if ($null -eq $Certificate -or [string]$Certificate.Thumbprint -ine $ExpectedThumbprint) { throw 'verified signer certificate does not match the receipt' }
    return ([DateTimeOffset]::new(([DateTime]$Certificate.NotAfter).ToUniversalTime())).ToString('o')
}

function Write-CnMonMsixSessionCheckpoint {
    param([string]$Path, [byte[]]$Bytes, [switch]$ReplaceExisting)
    $fullPath = [IO.Path]::GetFullPath($Path)
    $directory = [IO.Path]::GetDirectoryName($fullPath)
    if (-not (Test-Path -LiteralPath $directory -PathType Container)) { throw 'checkpoint directory is unavailable' }
    $destinationExists = Test-Path -LiteralPath $fullPath
    if ($ReplaceExisting -and -not $destinationExists) { throw 'checkpoint replacement target is missing' }
    if (-not $ReplaceExisting -and $destinationExists) { throw 'checkpoint already exists and is not owned by this run' }
    if (-not $ReplaceExisting) {
        $stream = [IO.File]::Open($fullPath, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
        try { $stream.Write($Bytes, 0, $Bytes.Length); $stream.Flush($true) }
        finally { $stream.Dispose() }
        return
    }
    $temporary = Join-Path $directory (([IO.Path]::GetFileName($fullPath)) + '.' + [Guid]::NewGuid().ToString('N') + '.tmp')
    try {
        $stream = [IO.File]::Open($temporary, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
        try { $stream.Write($Bytes, 0, $Bytes.Length); $stream.Flush($true) }
        finally { $stream.Dispose() }
        [IO.File]::Move($temporary, $fullPath, $true)
    }
    finally { if (Test-Path -LiteralPath $temporary) { Remove-Item -LiteralPath $temporary -Force -ErrorAction SilentlyContinue } }
}

function Get-CnMonMsixSessionCaptureBounds {
    param([IntPtr]$WindowHandle, [IntPtr]$ForegroundHandle, [object]$MonitorBounds)
    if ($WindowHandle -eq [IntPtr]::Zero -or $ForegroundHandle -ne $WindowHandle -or $null -eq $MonitorBounds) { return $null }
    if ([int]$MonitorBounds.Width -lt 1 -or [int]$MonitorBounds.Height -lt 1) { return $null }
    return $MonitorBounds
}

function Test-CnMonMsixSessionRepairTarget {
    param(
        [object]$WindowMetadata,
        [string]$ExpectedPublisher,
        [string]$ExpectedVersion
    )
    if ($null -eq $WindowMetadata -or @($WindowMetadata.page_content).Count -ne 1 -or
        [int]$WindowMetadata.breadcrumb_bar_count -ne 1 -or @($WindowMetadata.breadcrumb_bars).Count -ne 1) { return $false }
    $content = @($WindowMetadata.page_content)[0]
    $breadcrumb = @($WindowMetadata.breadcrumb_bars)[0]
    if (@($content.specifications).Count -ne 1 -or @($content.repair_buttons).Count -ne 1) { return $false }
    $specification = @($content.specifications)[0]
    $repair = @($content.repair_buttons)[0]
    return [string]$breadcrumb.automation_id -ceq 'PermanentNavigationViewBreadcrumbBar' -and
        [bool]$breadcrumb.visible -and [string]$breadcrumb.name -ceq 'cn-mon — Network Monitor' -and [int]$breadcrumb.match_count -gt 0 -and
        [string]$specification.name -ceq 'Specifications' -and
        @($specification.names | Where-Object { [string]$_ -ceq 'Publisher' }).Count -eq 1 -and
        @($specification.names | Where-Object { [string]$_ -ceq $ExpectedPublisher }).Count -eq 1 -and
        @($specification.names | Where-Object { [string]$_ -ceq 'Version' }).Count -eq 1 -and
        @($specification.names | Where-Object { [string]$_ -ceq $ExpectedVersion }).Count -eq 1 -and
        [string]$repair.automation_id -ceq 'SystemSettings_StorageSense_AppSizesAdvanced_AppRepair_Button' -and
        [string]$repair.name -ceq 'Repair' -and [bool]$repair.visible
}

function New-CnMonMsixSessionRepairBreadcrumbMetadata {
    param([string]$AutomationId, [string[]]$VisibleNames)
    $matches = @($VisibleNames | Where-Object { [string]$_ -ceq 'cn-mon — Network Monitor' })
    return [pscustomobject]@{ automation_id = $AutomationId; name = if ($matches.Count -gt 0) { 'cn-mon — Network Monitor' } else { '' }; visible = ($matches.Count -gt 0); match_count = $matches.Count }
}

function Test-CnMonMsixSessionRepairActionAllowed {
    param([bool]$CandidatePageVerified, [string]$RepairButtonName, [bool]$RepairButtonEnabled)
    return $CandidatePageVerified -and $RepairButtonName -ceq 'Repair' -and $RepairButtonEnabled
}

function Test-CnMonMsixSessionRepairObservationAllowsInvoke {
    param([object]$Observation)
    if ($null -eq $Observation -or $null -eq $Observation.repair_button) { return $false }
    return Test-CnMonMsixSessionRepairActionAllowed -CandidatePageVerified ([bool]$Observation.candidate_page_verified) `
        -RepairButtonName ([string]$Observation.repair_button.name) -RepairButtonEnabled ([bool]$Observation.repair_button.enabled)
}

function Get-CnMonMsixSessionSeedRequestId {
    param([string]$Json)
    $report = ConvertFrom-Json -InputObject $Json -ErrorAction Stop
    if (@($report.requests).Count -lt 1 -or [string]$report.requests[0].id -notmatch '^[0-9a-fA-F]{4,64}$') {
        throw 'seeded request ID was not available for the history screenshots'
    }
    return [string]$report.requests[0].id
}

function Test-CnMonMsixOwnedShellProcess {
    param(
        [System.Collections.IDictionary]$Shell,
        [System.Collections.IDictionary]$ProcessIdentity,
        [System.Collections.IDictionary]$Expected
    )
    if ([string]$ProcessIdentity.executable_path -ine [string]$Expected.executable_path -or
        [int]$ProcessIdentity.session_id -ne [int]$Expected.session_id -or
        [string]$ProcessIdentity.owner_sid -cne [string]$Expected.owner_sid -or
        [string]$Shell.runner_path -ine [string]$Expected.runner_path -or
        [string]::IsNullOrWhiteSpace([string]$ProcessIdentity.command_line) -or
        [string]$Shell.token -cnotmatch '^[0-9a-f]{32}$') { return $false }
    $hasRunToken = ([string]$ProcessIdentity.command_line).Contains([string]$Shell.token, [StringComparison]::Ordinal)
    $hasRunnerPath = ([string]$ProcessIdentity.command_line).Contains([string]$Expected.runner_path, [StringComparison]::OrdinalIgnoreCase)
    return $hasRunToken -and $hasRunnerPath
}

function Stop-CnMonMsixSessionShellIfOwned {
    param(
        [System.Collections.IDictionary]$Shell,
        [System.Collections.IDictionary]$ProcessIdentity,
        [System.Collections.IDictionary]$Expected
    )
    if (-not (Test-CnMonMsixOwnedShellProcess -Shell $Shell -ProcessIdentity $ProcessIdentity -Expected $Expected)) { return $false }
    Stop-Process -Id ([int]$Shell.pid) -Force -ErrorAction Stop
    return $true
}

function Test-CnMonMsixOwnedFileCleanup {
    param([bool]$OwnedByThisRun, [string]$ExpectedSha256, [string]$ActualSha256)
    return $OwnedByThisRun -and
        $ExpectedSha256 -cmatch '^[0-9a-f]{64}$' -and
        $ExpectedSha256 -ceq $ActualSha256
}

function Remove-CnMonMsixSessionOwnedWorkDirectory {
    param([string]$Path, [bool]$OwnedByThisRun, [object[]]$WorkFiles)
    if (-not (Test-Path -LiteralPath $Path)) { return $true }
    $directory = Get-Item -LiteralPath $Path -Force
    if (-not $OwnedByThisRun -or -not $directory.PSIsContainer -or (($directory.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0)) { return $false }
    $owned = @{}
    foreach ($file in $WorkFiles) { $owned[[IO.Path]::GetFullPath([string]$file.path)] = $file }
    $ok = $true
    foreach ($name in @('history.db', 'history.db-wal', 'history.db-shm', 'history.db-journal', 'cn.log')) {
        $filePath = Join-Path $Path $name
        if (-not (Test-Path -LiteralPath $filePath -PathType Leaf)) { continue }
        $item = Get-Item -LiteralPath $filePath -Force
        $entry = $owned[[IO.Path]::GetFullPath($filePath)]
        if ($null -eq $entry -or (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) -or
            -not (Test-CnMonMsixOwnedFileCleanup -OwnedByThisRun:([bool]$entry.owned) -ExpectedSha256 ([string]$entry.sha256) -ActualSha256 (Get-CnMonMsixSessionHash -Path $filePath))) { $ok = $false }
        else { Remove-Item -LiteralPath $filePath -Force -ErrorAction Stop }
    }
    if (@(Get-ChildItem -LiteralPath $Path -Force).Count -eq 0) { Remove-Item -LiteralPath $Path -Force -ErrorAction Stop }
    else { $ok = $false }
    return $ok
}

function Test-CnMonMsixSessionRecoveryPaths {
    param(
        [System.Collections.IDictionary]$State,
        [string]$UserProfile,
        [string]$LocalAppData,
        [string]$ArtifactDirectory
    )
    if ([string]$State.run_id -cnotmatch '^[0-9a-f]{32}$') { return $false }
    if ([IO.Path]::GetFullPath([string]$State.artifact_directory) -ine [IO.Path]::GetFullPath($ArtifactDirectory)) { return $false }
    $allowedUser = @('.cn', '.cn-ping-history.db', 'cn.log', 'msix-profile-history.db', 'msix-profile.log', 'msix-cli-history.db', 'msix-cli.log') |
        ForEach-Object { [IO.Path]::GetFullPath((Join-Path $UserProfile $_)) }
    if (@($State.planned_user_paths).Count -ne $allowedUser.Count) { return $false }
    foreach ($path in $allowedUser) { if (@($State.planned_user_paths | Where-Object { [IO.Path]::GetFullPath([string]$_) -ieq $path }).Count -ne 1) { return $false } }
    $seen = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
    foreach ($file in @($State.owned_files)) {
        $path = [IO.Path]::GetFullPath([string]$file.path)
        if ($path -notin $allowedUser -or -not $seen.Add($path) -or
            [string]$file.sha256 -cnotmatch '^[0-9a-f]{64}$' -or $file.owned -ne $true) { return $false }
    }
    foreach ($path in @($State.planned_user_paths)) {
        if ([IO.Path]::GetFullPath([string]$path) -notin $allowedUser) { return $false }
    }
    if ($State.work_directory) {
        $expectedWork = [IO.Path]::GetFullPath((Join-Path $UserProfile ('cn-mon-msix-desktop-session-' + [string]$State.run_id)))
        if ([IO.Path]::GetFullPath([string]$State.work_directory) -cne $expectedWork) { return $false }
        if ($State.work_directory_owned -notin @($true, $false)) { return $false }
        $seenWork = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
        foreach ($file in @($State.work_files)) {
            $name = [IO.Path]::GetFileName([string]$file.path)
            $path = [IO.Path]::GetFullPath([string]$file.path)
            if ($name -notin @('history.db', 'history.db-wal', 'history.db-shm', 'history.db-journal', 'cn.log') -or
                [IO.Path]::GetDirectoryName($path) -cne $expectedWork -or -not $seenWork.Add($path) -or
                [string]$file.sha256 -cnotmatch '^[0-9a-f]{64}$' -or $file.owned -ne $true) { return $false }
        }
    }
    $appDataRoot = [IO.Path]::GetFullPath((Join-Path $LocalAppData ('Packages\' + [string]$State.expected_pfn + '\LocalCache\Local')))
    $appDataNames = @([string]$State.appdata_history_name, [string]$State.appdata_log_name)
    if ($appDataNames[0] -cnotmatch '^cn-mon-msix-desktop-[0-9a-f]{32}\.db$' -or
        $appDataNames[1] -cnotmatch '^cn-mon-msix-desktop-[0-9a-f]{32}\.log$') { return $false }
    $expectedAppData = @($appDataNames | ForEach-Object { [IO.Path]::GetFullPath((Join-Path $appDataRoot $_)) })
    if (@($State.planned_appdata_paths).Count -ne 2) { return $false }
    foreach ($path in $expectedAppData) { if (@($State.planned_appdata_paths | Where-Object { [IO.Path]::GetFullPath([string]$_) -ieq $path }).Count -ne 1) { return $false } }
    $seenAppData = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
    foreach ($file in @($State.owned_appdata_files)) {
        $path = [IO.Path]::GetFullPath([string]$file.path)
        if ([IO.Path]::GetDirectoryName($path) -cne $appDataRoot -or
            [IO.Path]::GetFileName($path) -notin $appDataNames -or -not $seenAppData.Add($path) -or
            [string]$file.sha256 -cnotmatch '^[0-9a-f]{64}$' -or $file.owned -ne $true) { return $false }
    }
    $runnerPath = [IO.Path]::GetFullPath((Join-Path $ArtifactDirectory 'desktop-session-terminal-command.ps1'))
    $seenShellIds = [Collections.Generic.HashSet[int]]::new()
    $seenTokens = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
    foreach ($shell in @($State.owned_shells)) {
        if ([int]$shell.pid -le 0 -or [int]$shell.parent_pid -le 0 -or
            [string]$shell.token -cnotmatch '^[0-9a-f]{32}$' -or
            [IO.Path]::GetFullPath([string]$shell.runner_path) -ine $runnerPath -or
            [string]$shell.mode -cnotin @('help', 'table', 'json-tcp', 'history-list', 'history-detail') -or
            $shell.exited -notin @($true, $false) -or
            -not $seenShellIds.Add([int]$shell.pid) -or -not $seenTokens.Add([string]$shell.token)) { return $false }
    }
    return $true
}

function Invoke-CnMonMsixSessionReadOnlyPreflight {
    param(
        [string]$ScriptPath,
        [string]$PackagePath,
        [string]$ReceiptPath,
        [string]$ResultsPath,
        [string]$NativeHelperPath,
        [string]$HelperSha256,
        [string]$ArtifactDirectory,
        [string]$ExpectedPackageSha256,
        [string]$ExpectedReceiptSha256
    )
    & $ScriptPath -PackagePath $PackagePath -ReceiptPath $ReceiptPath -ResultsPath $ResultsPath `
        -NativeHelperPath $NativeHelperPath -ExpectedNativeHelperSha256 $HelperSha256 -ArtifactDirectory $ArtifactDirectory
    $summaryPath = Join-Path $ArtifactDirectory 'msix-desktop-supplement.json'
    $preflight = Get-Content -LiteralPath $summaryPath -Raw | ConvertFrom-Json -AsHashtable -ErrorAction Stop
    if ($preflight.preflight_status -cne 'ready_for_review' -or
        $preflight.preflight.desktop_eligible -ne $true -or $preflight.preflight.user_token_eligible -ne $true -or
        $preflight.preflight.interactive_session_eligible -ne $true -or $preflight.preflight.input_desktop_available -ne $true -or
        $preflight.preflight.medium_integrity -ne $true -or $preflight.preflight.signature_valid -ne $true -or
        $preflight.preflight.signer_trusted -ne $true -or $preflight.preflight.candidate_not_registered -ne $true -or
        $preflight.evidence.signed_package_sha256 -cne $ExpectedPackageSha256 -or
        $preflight.evidence.signed_receipt_sha256 -cne $ExpectedReceiptSha256 -or
        $preflight.evidence.native_helper_sha256 -cne $HelperSha256) {
        throw 'read-only preflight did not prove the exact candidate, signer trust and ordinary active desktop'
    }
    return $preflight
}

function Get-CnMonMsixCompletionScreenshotNames {
    return @(
        'alias-off-settings.png', 'alias-on-settings.png',
        'repair-start.png', 'repair-progress.png', 'repair-complete.png',
        '01-cli-help.png', '02-icmp-table.png', '03-json-tcp.png',
        '04-history-list.png', '05-history-detail.png'
    )
}

function New-CnMonMsixDesktopCompletionSupplement {
    param(
        [System.Collections.IDictionary]$Candidate,
        [System.Collections.IDictionary]$QualificationScripts,
        [System.Collections.IDictionary]$Observations
    )
    $safeCandidate = [ordered]@{}
    foreach ($key in @('run_id', 'artifact_id', 'archive_sha256', 'signed_package_sha256', 'signed_receipt_sha256', 'certify_result_sha256', 'packaging_revision')) {
        $safeCandidate[$key] = [string]$Candidate[$key]
    }
    $safeScripts = [ordered]@{}
    foreach ($key in @('helper_sha256', 'desktop_driver_sha256', 'validator_revision')) {
        $safeScripts[$key] = [string]$QualificationScripts[$key]
    }
    $falseAlias = [ordered]@{
        settings_opened = $false; settings_verified = $false
        pwsh_failure = $false; pwsh_success = $false
        cmd_failure = $false; cmd_success = $false
    }
    $off = [ordered]@{}
    foreach ($key in $falseAlias.Keys) { $off[$key] = ($Observations.alias.off[$key] -is [bool] -and $Observations.alias.off[$key]) }
    $on = [ordered]@{}
    foreach ($key in $falseAlias.Keys) { $on[$key] = ($Observations.alias.on[$key] -is [bool] -and $Observations.alias.on[$key]) }
    $on.packaged_process_identity_verified = ($Observations.alias.on.packaged_process_identity_verified -is [bool] -and $Observations.alias.on.packaged_process_identity_verified)
    $screenshots = [Collections.Generic.List[object]]::new()
    foreach ($item in @($Observations.screenshots)) {
        if ($null -eq $item -or [string]$item.name -cnotin (Get-CnMonMsixCompletionScreenshotNames) -or
            [string]$item.sha256 -cnotmatch '^[0-9a-f]{64}$' -or [int]$item.width -lt 1 -or [int]$item.height -lt 1) { continue }
        $screenshots.Add([ordered]@{ name = [string]$item.name; sha256 = [string]$item.sha256; width = [int]$item.width; height = [int]$item.height })
    }
    $repair = [ordered]@{
        activation_observed = ($Observations.repair.activation_observed -is [bool] -and $Observations.repair.activation_observed)
        completion_observed = ($Observations.repair.completion_observed -is [bool] -and $Observations.repair.completion_observed)
        data_before_sha256 = if ([string]$Observations.repair.data_before_sha256 -cmatch '^[0-9a-f]{64}$') { [string]$Observations.repair.data_before_sha256 } else { '' }
        data_after_sha256 = if ([string]$Observations.repair.data_after_sha256 -cmatch '^[0-9a-f]{64}$') { [string]$Observations.repair.data_after_sha256 } else { '' }
        readback_verified = ($Observations.repair.readback_verified -is [bool] -and $Observations.repair.readback_verified)
    }
    return [ordered]@{
        schema_version = 1
        mode = 'desktop_completion_supplement'
        overall = 'incomplete'
        candidate = $safeCandidate
        qualification_scripts = $safeScripts
        observations = [ordered]@{
            ordinary_user = ($Observations.ordinary_user -is [bool] -and $Observations.ordinary_user)
            medium_integrity = ($Observations.medium_integrity -is [bool] -and $Observations.medium_integrity)
            interactive_session = ($Observations.interactive_session -is [bool] -and $Observations.interactive_session)
            alias = [ordered]@{
                off = $off
                on = $on
                path_unchanged = ($Observations.alias.path_unchanged -is [bool] -and $Observations.alias.path_unchanged)
            }
            repair = $repair
            screenshots = @($screenshots.ToArray())
            desktop_cleanup_passed = ($Observations.desktop_cleanup_passed -is [bool] -and $Observations.desktop_cleanup_passed)
        }
    }
}

function Get-CnMonMsixSessionInputPath {
    param([string]$Path)
    try {
        $item = Get-Item -LiteralPath $Path -ErrorAction Stop
        if ($item.PSIsContainer -or (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0)) { throw 'invalid input' }
        return [IO.Path]::GetFullPath($item.FullName)
    }
    catch { throw 'a required private desktop input is unavailable' }
}

function Get-CnMonMsixSessionHash {
    param([string]$Path)
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function Get-CnMonMsixSessionNativeTest {
    param(
        [Parameter(Mandatory = $true)][string]$TestName,
        [hashtable]$Environment = @{}
    )
    $start = [Diagnostics.ProcessStartInfo]::new()
    $start.FileName = $script:NativeHelperFullPath
    $start.UseShellExecute = $false
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    $start.RedirectStandardInput = $true
    $start.ArgumentList.Add('-test.run')
    $start.ArgumentList.Add("^$TestName`$")
    $start.ArgumentList.Add('-test.v')
    $start.Environment['CNMON_MSIX_TEST'] = '1'
    $start.Environment['CNMON_NATIVE_COMMAND_BINARY'] = $script:AliasPath
    $start.Environment['CNMON_MSIX_EXPECTED_PFN'] = [string]$script:Receipt.expected_pfn
    $start.Environment['CNMON_MSIX_EXPECTED_SID'] = [string]$script:State.user_sid
    foreach ($key in $Environment.Keys) { $start.Environment[[string]$key] = [string]$Environment[$key] }
    $process = [Diagnostics.Process]::Start($start)
    try {
        $stdoutTask = $process.StandardOutput.ReadToEndAsync()
        $stderrTask = $process.StandardError.ReadToEndAsync()
        $process.StandardInput.Close()
        if (-not $process.WaitForExit(120000)) {
            $process.Kill($true)
            throw 'native desktop helper exceeded its bounded run time'
        }
        $stdout = $stdoutTask.GetAwaiter().GetResult()
        $stderr = $stderrTask.GetAwaiter().GetResult()
        $transcript = "TEST=$TestName`r`nEXIT=$($process.ExitCode)`r`nSTDOUT:`r`n$stdout`r`nSTDERR:`r`n$stderr"
        Save-CnMonMsixSessionTranscript -Name ("native-$TestName-" + [Guid]::NewGuid().ToString('N') + '.txt') -Text $transcript
        if ($process.ExitCode -ne 0 -or $stdout -notmatch "(?m)^--- PASS: $([regex]::Escape($TestName))(?:\s|$)" -or
            $stdout -match "(?m)^--- SKIP: $([regex]::Escape($TestName))(?:\s|$)|no tests to run") {
            throw "native desktop helper did not report an exact passing test: $TestName"
        }
        return $stdout
    }
    finally { $process.Dispose() }
}

function Get-CnMonMsixSessionShellResult {
    param([ValidateSet('pwsh', 'cmd')][string]$Shell, [string]$Spelling)
    $start = [Diagnostics.ProcessStartInfo]::new()
    $start.UseShellExecute = $false
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    if ($Shell -eq 'pwsh') {
        $start.FileName = $script:PwshExe
        $start.ArgumentList.Add('-NoLogo')
        $start.ArgumentList.Add('-NoProfile')
        $start.ArgumentList.Add('-NonInteractive')
        $start.ArgumentList.Add('-Command')
        $start.ArgumentList.Add("$Spelling --version")
    }
    else {
        $start.FileName = $env:ComSpec
        $start.ArgumentList.Add('/d')
        $start.ArgumentList.Add('/c')
        $start.ArgumentList.Add("$Spelling --version")
    }
    $process = [Diagnostics.Process]::Start($start)
    try {
        $stdoutTask = $process.StandardOutput.ReadToEndAsync()
        $stderrTask = $process.StandardError.ReadToEndAsync()
        if (-not $process.WaitForExit(30000)) {
            $process.Kill($true)
            throw 'shell alias probe exceeded its bounded run time'
        }
        return [pscustomobject]@{
            ExitCode = [int]$process.ExitCode
            Stdout = $stdoutTask.GetAwaiter().GetResult()
            Stderr = $stderrTask.GetAwaiter().GetResult()
        }
    }
    finally { $process.Dispose() }
}

function Save-CnMonMsixSessionTranscript {
    param([string]$Name, [string]$Text)
    if ($Name -notmatch '^[A-Za-z0-9._-]{1,100}\.txt$') { throw 'private transcript name is invalid' }
    $path = Join-Path $script:ArtifactFullPath $Name
    $bytes = [Text.UTF8Encoding]::new($false).GetBytes($Text)
    $stream = [IO.File]::Open($path, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
    try { $stream.Write($bytes, 0, $bytes.Length) }
    finally { $stream.Dispose() }
}

function Get-CnMonMsixSessionSettingsUri {
    param([ValidateSet('Aliases', 'Package')][string]$Page)
    if ($Page -eq 'Aliases') { return 'ms-settings:appsfeatures' }
    return 'ms-settings:appsfeatures-app?' + [Uri]::EscapeDataString([string]$script:Receipt.expected_pfn)
}

function Open-CnMonMsixSessionSettings {
    param([ValidateSet('Aliases', 'Package')][string]$Page)
    $uri = Get-CnMonMsixSessionSettingsUri -Page $Page
    $null = Start-Process -FilePath $uri -ErrorAction Stop
    Add-Type -AssemblyName UIAutomationClient -ErrorAction Stop
    Add-Type -AssemblyName UIAutomationTypes -ErrorAction Stop
    $deadline = [DateTime]::UtcNow.AddSeconds(15)
    while ([DateTime]::UtcNow -lt $deadline) {
        $settingsProcesses = @(Get-Process -Name SystemSettings -ErrorAction SilentlyContinue | Where-Object {
            $_.SessionId -eq [Diagnostics.Process]::GetCurrentProcess().SessionId
        })
        if ($settingsProcesses.Count -gt 0) {
            $topWindows = [Windows.Automation.AutomationElement]::RootElement.FindAll(
                [Windows.Automation.TreeScope]::Children, [Windows.Automation.Condition]::TrueCondition)
            $contexts = [Collections.Generic.List[object]]::new()
            foreach ($settingsProcess in $settingsProcesses) {
                if (-not (Test-CnMonMsixSessionSameUserProcess -Process $settingsProcess)) { continue }
                foreach ($window in $topWindows) {
                    $windowProcessId = [int]$window.Current.ProcessId
                    $windowHandle = [IntPtr]$window.Current.NativeWindowHandle
                    if ($windowHandle -eq [IntPtr]::Zero) { continue }
                    $ownerProcess = $null
                    $descendantProcessIds = @()
                    if ($windowProcessId -eq [int]$settingsProcess.Id) {
                        $ownerProcess = $settingsProcess
                    }
                    elseif ([string]$window.Current.ClassName -ceq 'ApplicationFrameWindow') {
                        try { $ownerProcess = Get-Process -Id $windowProcessId -ErrorAction Stop }
                        catch { continue }
                        if ([string]$ownerProcess.ProcessName -cne 'ApplicationFrameHost' -or
                            $ownerProcess.SessionId -ne [Diagnostics.Process]::GetCurrentProcess().SessionId -or
                            -not (Test-CnMonMsixSessionSameUserProcess -Process $ownerProcess)) { continue }
                        $settingsCondition = [Windows.Automation.PropertyCondition]::new(
                            [Windows.Automation.AutomationElement]::ProcessIdProperty, [int]$settingsProcess.Id)
                        $descendants = $window.FindAll([Windows.Automation.TreeScope]::Descendants, $settingsCondition)
                        $descendantProcessIds = @($descendants | ForEach-Object { [int]$_.Current.ProcessId })
                    }
                    else { continue }

                    $binding = [pscustomobject]@{
                        process_id = $windowProcessId
                        session_id = [int]$ownerProcess.SessionId
                        window_handle = [int64]$windowHandle.ToInt64()
                        class_name = [string]$window.Current.ClassName
                        descendant_process_ids = $descendantProcessIds
                    }
                    if (Test-CnMonMsixSessionSettingsWindowBinding -Window $binding -SettingsProcessId ([int]$settingsProcess.Id) `
                            -OwnerProcessId ([int]$ownerProcess.Id) -ExpectedSessionId ([Diagnostics.Process]::GetCurrentProcess().SessionId) `
                            -SettingsIdentityVerified $true -OwnerIdentityVerified $true) {
                        $linkage = if ($ownerProcess.Id -eq $settingsProcess.Id) { 'direct' } else { 'descendant' }
                        $contexts.Add([pscustomobject]@{
                            Process = $ownerProcess
                            SettingsProcess = $settingsProcess
                            WindowHandle = $windowHandle
                            Linkage = $linkage
                        })
                    }
                }
            }
            $context = Select-CnMonMsixSessionSettingsWindowContext -Candidates @($contexts.ToArray())
            if ($null -ne $context) { return $context }
        }
        Start-Sleep -Milliseconds 300
    }
    throw 'Windows Settings did not expose one unambiguous same-user window linked to the active Settings process'
}

function Test-CnMonMsixSessionSameUserProcess {
    param([Diagnostics.Process]$Process)
    try {
        if ($Process.SessionId -ne [Diagnostics.Process]::GetCurrentProcess().SessionId) { return $false }
        $processInfo = Get-CimInstance -ClassName Win32_Process -Filter "ProcessId=$([int]$Process.Id)" -ErrorAction Stop
        $owner = Invoke-CimMethod -InputObject $processInfo -MethodName GetOwnerSid -ErrorAction Stop
        return [string]$owner.Sid -ceq [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    }
    catch { return $false }
}

function Test-CnMonMsixSessionSettingsWindowBinding {
    param(
        [object]$Window,
        [int]$SettingsProcessId,
        [int]$OwnerProcessId,
        [int]$ExpectedSessionId,
        [int64]$ExpectedWindowHandle = 0,
        [bool]$SettingsIdentityVerified,
        [bool]$OwnerIdentityVerified
    )
    if ($null -eq $Window -or -not $SettingsIdentityVerified -or -not $OwnerIdentityVerified) { return $false }
    if ([int]$Window.session_id -ne $ExpectedSessionId -or [int64]$Window.window_handle -eq 0) { return $false }
    if ($ExpectedWindowHandle -ne 0 -and [int64]$Window.window_handle -ne $ExpectedWindowHandle) { return $false }
    if ($SettingsProcessId -le 0 -or $OwnerProcessId -le 0) { return $false }
    if ([int]$Window.process_id -eq $SettingsProcessId) { return $OwnerProcessId -eq $SettingsProcessId }
    return [int]$Window.process_id -eq $OwnerProcessId -and
        [string]$Window.class_name -ceq 'ApplicationFrameWindow' -and
        @($Window.descendant_process_ids | Where-Object { [int]$_ -eq $SettingsProcessId }).Count -gt 0
}

function Select-CnMonMsixSessionSettingsWindowContext {
    param([object[]]$Candidates)
    if ($Candidates.Count -ne 1) { return $null }
    return $Candidates[0]
}

function Get-CnMonMsixSessionAutomationWindows {
    param([Diagnostics.Process]$Process, [IntPtr]$WindowHandle = [IntPtr]::Zero, [Diagnostics.Process]$SettingsProcess)
    Add-Type -AssemblyName UIAutomationClient -ErrorAction Stop
    Add-Type -AssemblyName UIAutomationTypes -ErrorAction Stop
    if (-not (Test-CnMonMsixSessionSameUserProcess -Process $Process)) { throw 'Settings process is outside the active user session' }
    if ($null -ne $SettingsProcess) {
        if ($WindowHandle -eq [IntPtr]::Zero -or -not (Test-CnMonMsixSessionSameUserProcess -Process $SettingsProcess)) {
            throw 'verified Settings window context is outside the active user session'
        }
        $Process.Refresh()
        $SettingsProcess.Refresh()
        $condition = [Windows.Automation.PropertyCondition]::new(
            [Windows.Automation.AutomationElement]::NativeWindowHandleProperty, [int]$WindowHandle.ToInt64())
        $windows = [Windows.Automation.AutomationElement]::RootElement.FindAll(
            [Windows.Automation.TreeScope]::Children, $condition)
        if ($windows.Count -ne 1) { throw 'the exact Settings top-level window could not be reacquired' }
        $window = $windows[0]
        $windowProcessId = [int]$window.Current.ProcessId
        $descendantProcessIds = @()
        if ([int]$Process.Id -ne [int]$SettingsProcess.Id) {
            if ([string]$Process.ProcessName -cne 'ApplicationFrameHost' -or
                [string]$window.Current.ClassName -cne 'ApplicationFrameWindow') {
                throw 'Settings frame host identity changed'
            }
            $settingsCondition = [Windows.Automation.PropertyCondition]::new(
                [Windows.Automation.AutomationElement]::ProcessIdProperty, [int]$SettingsProcess.Id)
            $descendants = $window.FindAll([Windows.Automation.TreeScope]::Descendants, $settingsCondition)
            $descendantProcessIds = @($descendants | ForEach-Object { [int]$_.Current.ProcessId })
        }
        $binding = [pscustomobject]@{
            process_id = $windowProcessId
            session_id = [int]$Process.SessionId
            window_handle = [int64]$window.Current.NativeWindowHandle
            class_name = [string]$window.Current.ClassName
            descendant_process_ids = $descendantProcessIds
        }
        if (-not (Test-CnMonMsixSessionSettingsWindowBinding -Window $binding -SettingsProcessId ([int]$SettingsProcess.Id) `
                -OwnerProcessId ([int]$Process.Id) -ExpectedSessionId ([Diagnostics.Process]::GetCurrentProcess().SessionId) `
                -ExpectedWindowHandle $WindowHandle.ToInt64() -SettingsIdentityVerified $true -OwnerIdentityVerified $true)) {
            throw 'Settings window no longer contains the verified Settings process'
        }
        return @($window)
    }
    $condition = [Windows.Automation.PropertyCondition]::new(
        [Windows.Automation.AutomationElement]::ProcessIdProperty, [int]$Process.Id)
    return [Windows.Automation.AutomationElement]::RootElement.FindAll(
        [Windows.Automation.TreeScope]::Children, $condition)
}

function Find-CnMonMsixSessionWindowHandle {
    param([Diagnostics.Process]$Process, [string]$TitleToken)
    $windows = Get-CnMonMsixSessionAutomationWindows -Process $Process
    foreach ($window in $windows) {
        $windowName = [string]$window.Current.Name
        if ($windowName -and $windowName.Contains($TitleToken, [StringComparison]::Ordinal)) {
            $handle = [IntPtr]$window.Current.NativeWindowHandle
            if ($handle -ne [IntPtr]::Zero) { return $handle }
        }
    }
    throw 'the exact run-owned Windows Terminal window title was not observed'
}

function Get-CnMonMsixSessionRepairObservation {
    param([Diagnostics.Process]$Process, [Diagnostics.Process]$SettingsProcess, [IntPtr]$WindowHandle)
    $observation = [ordered]@{ repair_button = $null; progress_visible = $false; candidate_page_verified = $false; visible_names = @() }
    $windows = Get-CnMonMsixSessionAutomationWindows -Process $Process -SettingsProcess $SettingsProcess -WindowHandle $WindowHandle
    foreach ($window in $windows) {
        $elements = $window.FindAll([Windows.Automation.TreeScope]::Descendants,
            [Windows.Automation.Condition]::TrueCondition)
        $visibleNames = [Collections.Generic.List[string]]::new()
        $windowRepairButton = $null
        $pageContentElements = @($elements | Where-Object { [string]$_.Current.AutomationId -ceq 'pageContent' })
        $breadcrumbBars = @($elements | Where-Object { [string]$_.Current.AutomationId -ceq 'PermanentNavigationViewBreadcrumbBar' })
        $pageContentMetadata = @()
        foreach ($pageContent in $pageContentElements) {
            $contentDescendants = $pageContent.FindAll([Windows.Automation.TreeScope]::Descendants, [Windows.Automation.Condition]::TrueCondition)
            $specificationGroups = @($contentDescendants | Where-Object { $_.Current.ControlType -eq [Windows.Automation.ControlType]::Group -and [string]$_.Current.Name -ceq 'Specifications' })
            $specificationMetadata = @()
            foreach ($specificationGroup in $specificationGroups) {
                $specificationElements = $specificationGroup.FindAll([Windows.Automation.TreeScope]::Descendants, [Windows.Automation.Condition]::TrueCondition)
                $specificationMetadata += [pscustomobject]@{ name = [string]$specificationGroup.Current.Name; names = @($specificationElements | ForEach-Object { [string]$_.Current.Name }) }
            }
            $repairControls = @($contentDescendants | Where-Object { [string]$_.Current.AutomationId -ceq 'SystemSettings_StorageSense_AppSizesAdvanced_AppRepair_Button' -and $_.Current.ControlType -eq [Windows.Automation.ControlType]::Button })
            $repairMetadata = @($repairControls | ForEach-Object { [pscustomobject]@{ automation_id = [string]$_.Current.AutomationId; name = [string]$_.Current.Name; visible = -not [bool]$_.Current.IsOffscreen; enabled = [bool]$_.Current.IsEnabled } })
            $pageContentMetadata += [pscustomobject]@{ specifications = $specificationMetadata; repair_buttons = $repairMetadata }
            if ($repairControls.Count -eq 1 -and -not [bool]$repairControls[0].Current.IsOffscreen) {
                $windowRepairButton = [pscustomobject]@{ name = [string]$repairControls[0].Current.Name; element = $repairControls[0]; enabled = [bool]$repairControls[0].Current.IsEnabled }
            }
        }
        $breadcrumbMetadata = @()
        foreach ($breadcrumbBar in $breadcrumbBars) {
            $breadcrumbDescendants = $breadcrumbBar.FindAll([Windows.Automation.TreeScope]::Descendants, [Windows.Automation.Condition]::TrueCondition)
            $visibleBreadcrumbNames = @($breadcrumbDescendants | Where-Object { -not [bool]$_.Current.IsOffscreen } | ForEach-Object { [string]$_.Current.Name })
            $breadcrumbMetadata += New-CnMonMsixSessionRepairBreadcrumbMetadata -AutomationId ([string]$breadcrumbBar.Current.AutomationId) -VisibleNames $visibleBreadcrumbNames
        }
        foreach ($element in $elements) {
            $name = [string]$element.Current.Name
            $isVisible = -not [bool]$element.Current.IsOffscreen
            if ($isVisible -and -not [string]::IsNullOrWhiteSpace($name)) { $visibleNames.Add($name) }
            if ($isVisible -and ($element.Current.ControlType -eq [Windows.Automation.ControlType]::ProgressBar -or
                $name -match '(?i)repairing|repair in progress|applying repair')) {
                $observation.progress_visible = $true
            }
        }
        $metadata = [pscustomobject]@{ page_content = $pageContentMetadata; breadcrumb_bar_count = $breadcrumbBars.Count; breadcrumb_bars = $breadcrumbMetadata }
        if ($null -ne $windowRepairButton -and (Test-CnMonMsixSessionRepairTarget -WindowMetadata $metadata `
                -ExpectedPublisher ([string]$script:Receipt.publisher_display_name) -ExpectedVersion ([string]$script:Receipt.package_version))) {
            $observation.repair_button = $windowRepairButton
            $observation.visible_names = @($visibleNames.ToArray())
            $observation.candidate_page_verified = $true
        }
    }
    return [pscustomobject]$observation
}

function Save-CnMonMsixSessionRepairUiTrace {
    param([Diagnostics.Process]$Process, [Diagnostics.Process]$SettingsProcess, [IntPtr]$WindowHandle)
    $rows = [Collections.Generic.List[string]]::new()
    $rows.Add("SettingsProcessId=$($Process.Id); SessionId=$($Process.SessionId); UserSid=$([Security.Principal.WindowsIdentity]::GetCurrent().User.Value)")
    try {
        $windows = Get-CnMonMsixSessionAutomationWindows -Process $Process -SettingsProcess $SettingsProcess -WindowHandle $WindowHandle
        $count = 0
        foreach ($window in $windows) {
            $elements = $window.FindAll([Windows.Automation.TreeScope]::Descendants,
                [Windows.Automation.Condition]::TrueCondition)
            foreach ($element in $elements) {
                if ($count -ge 100) { break }
                $name = [string]$element.Current.Name
                $automationId = [string]$element.Current.AutomationId
                if ($name.Length -gt 180) { $name = $name.Substring(0, 180) }
                if ($automationId.Length -gt 120) { $automationId = $automationId.Substring(0, 120) }
                $rows.Add("$($element.Current.ControlType.ProgrammaticName)|$automationId|$([bool]$element.Current.IsEnabled)|$name")
                $count++
            }
            if ($count -ge 100) { break }
        }
    }
    catch { $rows.Add('UIA scan failed or the page was still loading') }
    Save-CnMonMsixSessionTranscript -Name 'repair-uia-before.txt' -Text ($rows -join "`r`n")
}

function Set-CnMonMsixSessionWindowForeground {
    param([Diagnostics.Process]$Process, [IntPtr]$WindowHandle = [IntPtr]::Zero, [switch]$Maximize)
    if ($null -eq ('CnMonMsixSessionWindowNative' -as [type])) {
        Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class CnMonMsixSessionWindowNative
{
    [DllImport("user32.dll", SetLastError=true)] public static extern bool SetForegroundWindow(IntPtr handle);
    [DllImport("user32.dll", SetLastError=true)] public static extern bool ShowWindow(IntPtr handle, int command);
    [DllImport("user32.dll", SetLastError=true)] public static extern IntPtr GetForegroundWindow();
}
'@ -ErrorAction Stop
    }
    $Process.Refresh()
    if ($Process.HasExited -or $Process.SessionId -ne [Diagnostics.Process]::GetCurrentProcess().SessionId) {
        throw 'the expected visible application window is unavailable'
    }
    $handle = if ($WindowHandle -ne [IntPtr]::Zero) { $WindowHandle } else { $Process.MainWindowHandle }
    if ($handle -eq [IntPtr]::Zero) { throw 'the expected visible application window is unavailable' }
    for ($attempt = 0; $attempt -lt 8; $attempt++) {
        $Process.Refresh()
        $null = [CnMonMsixSessionWindowNative]::ShowWindow($handle, $(if ($Maximize) { 3 } else { 5 }))
        $null = [CnMonMsixSessionWindowNative]::SetForegroundWindow($handle)
        Start-Sleep -Milliseconds 250
        if ([CnMonMsixSessionWindowNative]::GetForegroundWindow() -eq $handle) { return }
    }
    throw 'the requested Settings or Terminal window could not be foregrounded for a genuine capture'
}

function Save-CnMonMsixSessionScreenshot {
    param([string]$Name, [Parameter(Mandatory = $true)][IntPtr]$WindowHandle, [switch]$Listing)
    if ($Name -cnotin (Get-CnMonMsixCompletionScreenshotNames)) { throw 'screenshot name is outside the allowlist' }
    $path = Join-Path $script:ArtifactFullPath $Name
    if (Test-Path -LiteralPath $path) { throw 'screenshot output already exists' }
    Add-Type -AssemblyName System.Drawing -ErrorAction Stop
    Add-Type -AssemblyName System.Windows.Forms -ErrorAction Stop
    if ([CnMonMsixSessionWindowNative]::GetForegroundWindow() -ne $WindowHandle) { throw 'verified capture window is no longer foreground' }
    $bounds = [Windows.Forms.Screen]::FromHandle($WindowHandle).Bounds
    $bounds = Get-CnMonMsixSessionCaptureBounds -WindowHandle $WindowHandle -ForegroundHandle ([CnMonMsixSessionWindowNative]::GetForegroundWindow()) -MonitorBounds $bounds
    if ($null -eq $bounds) { throw 'verified window monitor bounds are unavailable' }
    if ($Listing -and ($bounds.Width -lt 1366 -or $bounds.Height -lt 768)) { throw 'listing screenshot resolution is below 1366x768' }
    $bitmap = [Drawing.Bitmap]::new($bounds.Width, $bounds.Height)
    $graphics = [Drawing.Graphics]::FromImage($bitmap)
    try {
        $graphics.CopyFromScreen($bounds.Location, [Drawing.Point]::Empty, $bounds.Size)
        $bitmap.Save($path, [Drawing.Imaging.ImageFormat]::Png)
    }
    finally { $graphics.Dispose(); $bitmap.Dispose() }
    $bytes = [IO.File]::ReadAllBytes($path)
    if ($bytes.Length -lt 45 -or $bytes.Length -gt 52428800) { throw 'captured screenshot size is invalid' }
    return [pscustomobject]@{
        name = $Name
        sha256 = (Get-CnMonMsixSessionHash -Path $path)
        width = [int]$bounds.Width
        height = [int]$bounds.Height
    }
}

function Write-CnMonMsixSessionSupplement {
    $script:State.observations.screenshots = @($script:State.screenshots)
    $script:State.observations.desktop_cleanup_passed = [bool]$script:State.cleanup_passed
    $candidate = [ordered]@{
        run_id = '38866515555'
        artifact_id = '12031747143'
        archive_sha256 = 'ed18d14a6a4e17526bd73d47f68336d9ee45ead71c15e61f106fba257d461818'
        signed_package_sha256 = 'd782d72374fe6bef2ca666e6cf64039283d0b1b24f4c9aa22cd940956873642f'
        signed_receipt_sha256 = '7f7ddda42b87b7611fe5107aeb519e3cf0e04e5e2870a7dc8374d6c7b74db55b'
        certify_result_sha256 = 'ebdaa5f3b456afa5e183910b25e80385e3e1d0d86c8db7fee7d7847c9a99971f'
        packaging_revision = 'b6c874d7b5de8257dde37685f0c3bfeb79b50b14'
    }
    $scripts = [ordered]@{
        helper_sha256 = $script:HelperSha256
        desktop_driver_sha256 = Get-CnMonMsixSessionHash -Path $PSCommandPath
        validator_revision = $ExpectedValidatorRevision.ToLowerInvariant()
    }
    $report = New-CnMonMsixDesktopCompletionSupplement -Candidate $candidate -QualificationScripts $scripts -Observations $script:State.observations
    $json = ConvertTo-Json -InputObject $report -Depth 10
    $path = Join-Path $script:ArtifactFullPath 'msix-desktop-completion-supplement.json'
    $bytes = [Text.UTF8Encoding]::new($false).GetBytes($json + [Environment]::NewLine)
    if ($script:State.cleanup_passed) {
        try {
            $started = [DateTimeOffset]::Parse([string]$script:State.run_started_utc, [Globalization.CultureInfo]::InvariantCulture, [Globalization.DateTimeStyles]::RoundtripKind)
            $completed = [DateTimeOffset]::Parse([string]$script:State.run_completed_utc, [Globalization.CultureInfo]::InvariantCulture, [Globalization.DateTimeStyles]::RoundtripKind)
        }
        catch { return }
        $expires = [DateTimeOffset]::Parse([string]$script:State.signer_not_after_utc, [Globalization.CultureInfo]::InvariantCulture, [Globalization.DateTimeStyles]::RoundtripKind)
        if (-not (Test-CnMonMsixSessionSignerWindow -StartedUtc $started -CompletedUtc $completed -NotAfterUtc $expires)) { return }
    }
    Write-CnMonMsixSessionCheckpoint -Path $path -Bytes $bytes -ReplaceExisting:([bool]$script:State.supplement_created)
    $script:SupplementCreated = $true
    $script:State.supplement_created = $true
    Save-CnMonMsixSessionState
}

function Save-CnMonMsixSessionState {
    $path = Join-Path $script:ArtifactFullPath 'desktop-session-state.json'
    $json = ConvertTo-Json -InputObject $script:State -Depth 12
    $bytes = [Text.UTF8Encoding]::new($false).GetBytes($json + [Environment]::NewLine)
    Write-CnMonMsixSessionCheckpoint -Path $path -Bytes $bytes -ReplaceExisting:$script:StateCreated
    $script:StateCreated = $true
}

function Set-CnMonMsixSessionUserFileHash {
    param([string[]]$Paths)
    $items = [Collections.Generic.List[object]]::new()
    foreach ($existing in @($script:State.owned_files)) { $items.Add($existing) }
    foreach ($path in $Paths) {
        if (Test-Path -LiteralPath $path -PathType Leaf) {
            $fullPath = [IO.Path]::GetFullPath($path)
            $match = @($items | Where-Object { [string]$_.path -ieq $fullPath })
            $witness = [ordered]@{ path = $fullPath; sha256 = Get-CnMonMsixSessionHash -Path $path; owned = $true }
            if ($match.Count -gt 0) { $index = $items.IndexOf($match[0]); $items[$index] = $witness }
            else { $items.Add($witness) }
        }
    }
    $script:State.owned_files = @($items.ToArray())
    Save-CnMonMsixSessionState
    foreach ($path in $Paths) {
        $fullPath = [IO.Path]::GetFullPath($path)
        if (@($items | Where-Object { [string]$_.path -ieq $fullPath -and [string]$_.sha256 -cmatch '^[0-9a-f]{64}$' }).Count -ne 1) {
            throw 'native qualification did not create all expected profile witnesses'
        }
    }
}

function Set-CnMonMsixSessionAppDataFileHashes {
    param([string[]]$Paths)
    $items = [Collections.Generic.List[object]]::new()
    foreach ($existing in @($script:State.owned_appdata_files)) { $items.Add($existing) }
    foreach ($path in $Paths) {
        if (Test-Path -LiteralPath $path -PathType Leaf) {
            $fullPath = [IO.Path]::GetFullPath($path)
            $match = @($items | Where-Object { [string]$_.path -ieq $fullPath })
            $witness = [ordered]@{ path = $fullPath; sha256 = Get-CnMonMsixSessionHash -Path $path; owned = $true }
            if ($match.Count -gt 0) { $items[$items.IndexOf($match[0])] = $witness }
            else { $items.Add($witness) }
        }
    }
    $script:State.owned_appdata_files = @($items.ToArray())
    Save-CnMonMsixSessionState
    foreach ($path in $Paths) {
        $fullPath = [IO.Path]::GetFullPath($path)
        if (@($items | Where-Object { [string]$_.path -ieq $fullPath -and [string]$_.sha256 -cmatch '^[0-9a-f]{64}$' }).Count -ne 1) {
            throw 'native AppData lifecycle did not create all expected backing witnesses'
        }
    }
}

function Set-CnMonMsixSessionWorkFileHashes {
    param([string[]]$Paths)
    $items = [Collections.Generic.List[object]]::new()
    foreach ($existing in @($script:State.work_files)) { $items.Add($existing) }
    $pathsToHash = [Collections.Generic.List[string]]::new()
    foreach ($path in $Paths) {
        $pathsToHash.Add([IO.Path]::GetFullPath($path))
        if ([IO.Path]::GetFileName($path) -ceq 'history.db') {
            foreach ($sidecar in @('history.db-wal', 'history.db-shm', 'history.db-journal')) { $pathsToHash.Add((Join-Path ([IO.Path]::GetDirectoryName([IO.Path]::GetFullPath($path))) $sidecar)) }
        }
    }
    foreach ($path in $pathsToHash) {
        if (Test-Path -LiteralPath $path -PathType Leaf) {
            $fullPath = [IO.Path]::GetFullPath($path)
            if ([IO.Path]::GetDirectoryName($fullPath) -cne [IO.Path]::GetFullPath([string]$script:State.work_directory) -or
                [IO.Path]::GetFileName($fullPath) -notin @('history.db', 'history.db-wal', 'history.db-shm', 'history.db-journal', 'cn.log')) { throw 'listing work output escaped its owned directory' }
            $match = @($items | Where-Object { [string]$_.path -ieq $fullPath })
            $witness = [ordered]@{ path = $fullPath; sha256 = Get-CnMonMsixSessionHash -Path $path; owned = $true }
            if ($match.Count -gt 0) { $items[$items.IndexOf($match[0])] = $witness }
            else { $items.Add($witness) }
        }
    }
    $script:State.work_files = @($items.ToArray())
    Save-CnMonMsixSessionState
    foreach ($path in $Paths) {
        $fullPath = [IO.Path]::GetFullPath($path)
        if (@($items | Where-Object { [string]$_.path -ieq $fullPath -and [string]$_.sha256 -cmatch '^[0-9a-f]{64}$' }).Count -ne 1) {
            throw 'listing command did not create all expected private output files'
        }
    }
}

function Get-CnMonMsixSessionDataDigest {
    param([object[]]$Files)
    $rows = [Collections.Generic.List[string]]::new()
    foreach ($file in @($Files | Sort-Object { [string]$_.path })) {
        if (-not (Test-Path -LiteralPath ([string]$file.path) -PathType Leaf)) { throw 'a run-owned profile file is missing' }
        $actual = Get-CnMonMsixSessionHash -Path ([string]$file.path)
        if ($actual -cne [string]$file.sha256) { throw 'a run-owned profile file changed during qualification' }
        $rows.Add(([string]$file.path).ToLowerInvariant() + ':' + $actual)
    }
    $localRoot = Join-Path $env:LOCALAPPDATA ('Packages\' + [string]$script:Receipt.expected_pfn + '\LocalCache\Local')
    foreach ($file in @($script:State.owned_appdata_files)) {
        $path = [string]$file.path
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw 'package-private AppData backing file is missing' }
        $actual = Get-CnMonMsixSessionHash -Path $path
        if ($actual -cne [string]$file.sha256) { throw 'package-private AppData backing bytes changed during qualification' }
        $rows.Add($path.ToLowerInvariant() + ':' + $actual)
    }
    $bytes = [Text.UTF8Encoding]::new($false).GetBytes(($rows.ToArray() | Sort-Object -CaseSensitive) -join "`n")
    $sha = [Security.Cryptography.SHA256]::HashData($bytes)
    return [Convert]::ToHexString($sha).ToLowerInvariant()
}

function Invoke-CnMonMsixSessionShellSet {
    param([string]$ExpectedAliasState)
    $version = "cn-mon $($script:Receipt.cli_version) ($($script:Receipt.cli_revision))`n"
    $results = @{}
    foreach ($shell in @('pwsh', 'cmd')) {
        foreach ($spelling in @('cn-mon', 'cn-mon.exe')) {
            $result = Get-CnMonMsixSessionShellResult -Shell $shell -Spelling $spelling
            $key = "$shell/$spelling"
            $results[$key] = $result
            if ($ExpectedAliasState -eq 'Off') {
                if ($result.ExitCode -eq 0) { throw "disabled alias unexpectedly resolved in $key" }
            }
            elseif ($result.ExitCode -ne 0 -or $result.Stdout -cne $version -or -not [string]::IsNullOrEmpty($result.Stderr)) {
                throw "enabled alias did not return the exact published version in $key"
            }
        }
    }
    $mode = if ($ExpectedAliasState -eq 'Off') { 'disabled' } else { 'packaged' }
    $native = Get-CnMonMsixSessionNativeTest -TestName 'TestNativeMSIXResolvedCommandIdentity' -Environment @{ CNMON_MSIX_RESOLUTION_MODE = $mode }
    if ($ExpectedAliasState -eq 'Off' -and -not $native.Contains('disabled=command-unresolved', [StringComparison]::Ordinal)) { throw 'native disabled-alias identity check did not pass' }
    if ($ExpectedAliasState -eq 'On' -and -not $native.Contains('packaged=identity-verified', [StringComparison]::Ordinal)) { throw 'native packaged-alias identity check did not pass' }
    return $results
}

function Wait-CnMonMsixSessionUserConfirmation {
    param([string]$Instruction, [string]$RequiredText)
    Write-Host $Instruction
    $answer = Read-Host "Type $RequiredText exactly after observing the requested UI state"
    if ($answer -cne $RequiredText) { throw 'desktop observation was not confirmed' }
}

function Invoke-CnMonMsixSessionAliasState {
    param([ValidateSet('Off', 'On')][string]$AliasState)
    $phaseName = if ($AliasState -eq 'Off') { 'AliasOff' } else { 'AliasOn' }
    if (-not (Test-CnMonMsixDesktopPhaseAllowed -Phase $phaseName -Completed $script:State.completed)) {
        throw 'desktop alias phase is out of order'
    }
    $settings = Open-CnMonMsixSessionSettings -Page Aliases
    try {
        $direction = if ($AliasState -eq 'Off') { 'turn the cn-mon.exe execution alias Off' } else { 'turn the cn-mon.exe execution alias On' }
        Wait-CnMonMsixSessionUserConfirmation -Instruction "In Settings, open Advanced app settings > App execution aliases (or the equivalent visible link on this Windows build), locate cn-mon.exe, and $direction. Leave that Settings page visible." -RequiredText $AliasState.ToUpperInvariant()
        $settingsWindows = @(Get-CnMonMsixSessionAutomationWindows -Process $settings.Process -SettingsProcess $settings.SettingsProcess -WindowHandle $settings.WindowHandle)
        if ($settingsWindows.Count -ne 1) { throw 'the exact verified Settings window was unavailable after operator confirmation' }
        Set-CnMonMsixSessionWindowForeground -Process $settings.Process -WindowHandle $settings.WindowHandle
        $screenshotName = if ($AliasState -eq 'Off') { 'alias-off-settings.png' } else { 'alias-on-settings.png' }
        $screenshot = Save-CnMonMsixSessionScreenshot -Name $screenshotName -WindowHandle $settings.WindowHandle
        if ([string]::IsNullOrEmpty([string]$screenshot)) { throw 'verified Settings window screenshot was not captured' }
        $beforePath = [string]$script:State.path_before
        if ([Environment]::GetEnvironmentVariable('PATH', 'Process') -cne $beforePath) { throw 'PATH changed during alias testing' }
        $results = Invoke-CnMonMsixSessionShellSet -ExpectedAliasState $AliasState
        $pathUnchanged = [Environment]::GetEnvironmentVariable('PATH', 'Process') -ceq $beforePath
        if (-not $pathUnchanged) { throw 'PATH changed during alias testing' }
        $script:State.screenshots.Add($screenshot)
        $script:State.observations.alias[$AliasState.ToLowerInvariant()].settings_opened = $true
        $script:State.observations.alias[$AliasState.ToLowerInvariant()].settings_verified = $true
        Save-CnMonMsixSessionState
    }
    finally {
        $settings.Process.Dispose()
        if ($settings.SettingsProcess.Id -ne $settings.Process.Id) { $settings.SettingsProcess.Dispose() }
    }
    $key = $AliasState.ToLowerInvariant()
    if ($AliasState -eq 'Off') {
        $script:State.observations.alias.off.pwsh_failure = $true
        $script:State.observations.alias.off.cmd_failure = $true
    }
    else {
        $script:State.observations.alias.on.pwsh_success = $true
        $script:State.observations.alias.on.cmd_success = $true
        $script:State.observations.alias.on.packaged_process_identity_verified = $true
    }
    $script:State.observations.alias.path_unchanged = $pathUnchanged
    $script:State.completed["alias_$key"] = $true
    Save-CnMonMsixSessionState
    Write-CnMonMsixSessionSupplement
}

function Invoke-CnMonMsixSessionRepair {
    if (-not (Test-CnMonMsixDesktopPhaseAllowed -Phase 'RepairBefore' -Completed $script:State.completed)) {
        throw 'desktop repair phase is out of order'
    }
    $startHash = Get-CnMonMsixSessionDataDigest -Files @($script:State.owned_files)
    $script:State.observations.repair.data_before_sha256 = $startHash
    $settings = Open-CnMonMsixSessionSettings -Page Package
    try {
        Set-CnMonMsixSessionWindowForeground -Process $settings.Process -WindowHandle $settings.WindowHandle -Maximize
        Wait-CnMonMsixSessionUserConfirmation -Instruction "In Settings, open Installed apps, select cn-mon — Network Monitor (version $($script:Receipt.package_version), $($script:Receipt.publisher_display_name)), choose the three-dot menu and Advanced options. Leave the page visible. Locate Repair, but do not click Repair or Reset." -RequiredText 'READY'
        Set-CnMonMsixSessionWindowForeground -Process $settings.Process -WindowHandle $settings.WindowHandle
        $script:State.screenshots.Add((Save-CnMonMsixSessionScreenshot -Name 'repair-start.png' -WindowHandle $settings.WindowHandle))
        Save-CnMonMsixSessionRepairUiTrace -Process $settings.Process -SettingsProcess $settings.SettingsProcess -WindowHandle $settings.WindowHandle
        Save-CnMonMsixSessionState
        $deadline = [DateTime]::UtcNow.AddSeconds(15)
        $before = $null
        while ([DateTime]::UtcNow -lt $deadline) {
            $before = Get-CnMonMsixSessionRepairObservation -Process $settings.Process -SettingsProcess $settings.SettingsProcess -WindowHandle $settings.WindowHandle
            if ($before.candidate_page_verified -and $null -ne $before.repair_button -and $before.repair_button.enabled) { break }
            Start-Sleep -Milliseconds 300
        }
        if (-not (Test-CnMonMsixSessionRepairObservationAllowsInvoke -Observation $before)) {
            throw 'the exact cn-mon — Network Monitor page and its enabled English Repair control were not observed; no repair action was invoked'
        }
        $script:State.completed.repair_before = $true
        Save-CnMonMsixSessionState

        $invokePattern = $null
        if (-not $before.repair_button.element.TryGetCurrentPattern([Windows.Automation.InvokePattern]::Pattern, [ref]$invokePattern)) {
            throw 'observed Repair control has no InvokePattern'
        }
        $invokePattern.Invoke()
        Write-Host ('CNMON_MSIX_REPAIR_EVENT=invoke_returned UTC=' + [DateTime]::UtcNow.ToString('o'))
        $script:State.observations.repair.activation_observed = $true
        Save-CnMonMsixSessionState

        $deadline = [DateTime]::UtcNow.AddSeconds(30)
        $sawProgress = $false
        while ([DateTime]::UtcNow -lt $deadline) {
            $current = Get-CnMonMsixSessionRepairObservation -Process $settings.Process -SettingsProcess $settings.SettingsProcess -WindowHandle $settings.WindowHandle
            if ($current.progress_visible -or ($null -ne $current.repair_button -and -not $current.repair_button.enabled)) {
                $sawProgress = $true
                Write-Host ('CNMON_MSIX_REPAIR_EVENT=progress_observed UTC=' + [DateTime]::UtcNow.ToString('o'))
                Set-CnMonMsixSessionWindowForeground -Process $settings.Process -WindowHandle $settings.WindowHandle
                $script:State.screenshots.Add((Save-CnMonMsixSessionScreenshot -Name 'repair-progress.png' -WindowHandle $settings.WindowHandle))
                Save-CnMonMsixSessionState
                break
            }
            Start-Sleep -Milliseconds 250
        }

        if ($sawProgress) {
            $deadline = [DateTime]::UtcNow.AddSeconds(120)
            while ([DateTime]::UtcNow -lt $deadline) {
                $current = Get-CnMonMsixSessionRepairObservation -Process $settings.Process -SettingsProcess $settings.SettingsProcess -WindowHandle $settings.WindowHandle
                if (-not $current.progress_visible -and $null -ne $current.repair_button -and
                    (Test-CnMonMsixSessionRepairObservationAllowsInvoke -Observation $current)) {
                    Write-Host ('CNMON_MSIX_REPAIR_EVENT=completion_observed UTC=' + [DateTime]::UtcNow.ToString('o'))
                    Set-CnMonMsixSessionWindowForeground -Process $settings.Process -WindowHandle $settings.WindowHandle
                    $script:State.screenshots.Add((Save-CnMonMsixSessionScreenshot -Name 'repair-complete.png' -WindowHandle $settings.WindowHandle))
                    $script:State.observations.repair.completion_observed = $true
                    break
                }
                Start-Sleep -Milliseconds 500
            }
        }
    }
    finally {
        $settings.Process.Dispose()
        if ($settings.SettingsProcess.Id -ne $settings.Process.Id) { $settings.SettingsProcess.Dispose() }
    }

    if (-not (Test-CnMonMsixDesktopPhaseAllowed -Phase 'RepairAfter' -Completed $script:State.completed)) {
        throw 'desktop repair readback phase is out of order'
    }
    $package = Assert-CnMonMsixSessionInstalledPackage
    if ([string]$package.PackageFullName -cne [string]$script:State.package_full_name) { throw 'Repair changed the registered package identity' }
    $repairReadback = Get-CnMonMsixSessionNativeTest -TestName 'TestNativeMSIXAppDataLifecycle' -Environment @{
        CNMON_MSIX_APPDATA_PHASE = 'repair-readback'
        CNMON_MSIX_APPDATA_HISTORY = $script:State.appdata_history_path
        CNMON_MSIX_APPDATA_LOG = $script:State.appdata_log_path
    }
    if (-not $repairReadback.Contains('phase=repair-readback', [StringComparison]::Ordinal)) { throw 'AppData repair readback was not observed' }
    $afterHash = Get-CnMonMsixSessionDataDigest -Files @($script:State.owned_files)
    $script:State.observations.repair.data_after_sha256 = $afterHash
    $script:State.observations.repair.readback_verified = ($afterHash -ceq $startHash)
    if ($afterHash -cne $startHash) { throw 'Repair changed run-owned profile or AppData bytes' }
    $script:State.completed.repair_after = $true
    Save-CnMonMsixSessionState
    Write-CnMonMsixSessionSupplement
}

function Assert-CnMonMsixSessionInstalledPackage {
    $matches = @(Get-AppxPackage -Name ([string]$script:Receipt.name) -ErrorAction Stop | Where-Object {
        [string]$_.Name -ceq [string]$script:Receipt.name -and
        [string]$_.Publisher -ceq [string]$script:Receipt.publisher -and
        [string]$_.PackageFamilyName -ceq [string]$script:Receipt.expected_pfn -and
        [string]$_.Version -ceq [string]$script:Receipt.package_version -and
        [string]$_.Architecture -ceq 'X64'
    })
    if ($matches.Count -ne 1) { throw 'exact signed candidate package is not uniquely registered for this user' }
    $package = $matches[0]
    if ($script:State.package_full_name -and [string]$package.PackageFullName -cne [string]$script:State.package_full_name) { throw 'registered package full name changed' }
    foreach ($relativePath in @('cn-mon.exe', 'cn-mon.exe.manifest')) {
        if (-not $script:Receipt.files.Contains($relativePath)) { throw 'signed receipt omits a required installed payload hash' }
        $path = Join-Path ([string]$package.InstallLocation) $relativePath
        if (-not (Test-Path -LiteralPath $path -PathType Leaf) -or (Get-CnMonMsixSessionHash -Path $path) -cne [string]$script:Receipt.files[$relativePath]) {
            throw 'installed payload hash does not match the signed receipt'
        }
    }
    return $package
}

function Invoke-CnMonMsixSessionPrepare {
    $script:Receipt = Get-Content -LiteralPath $script:ReceiptFullPath -Raw | ConvertFrom-Json -AsHashtable -ErrorAction Stop
    $certificateResult = Get-Content -LiteralPath $script:ResultsFullPath -Raw | ConvertFrom-Json -AsHashtable -ErrorAction Stop
    if ((Get-CnMonMsixSessionHash -Path $script:PackageFullPath) -cne 'd782d72374fe6bef2ca666e6cf64039283d0b1b24f4c9aa22cd940956873642f' -or
        (Get-CnMonMsixSessionHash -Path $script:ReceiptFullPath) -cne '7f7ddda42b87b7611fe5107aeb519e3cf0e04e5e2870a7dc8374d6c7b74db55b' -or
        (Get-CnMonMsixSessionHash -Path $script:ResultsFullPath) -cne 'ebdaa5f3b456afa5e183910b25e80385e3e1d0d86c8db7fee7d7847c9a99971f' -or
        (Get-CnMonMsixSessionHash -Path $script:NativeHelperFullPath) -cne $ExpectedNativeHelperSha256.ToLowerInvariant() -or
        $ExpectedValidatorRevision -cnotmatch '^[0-9a-fA-F]{40}$') {
        throw 'desktop qualification inputs do not match the pinned candidate bundle'
    }
    if ($script:Receipt.schema_version -ne 1 -or $script:Receipt.purpose -cne 'candidate' -or
        $script:Receipt.package_sha256 -cne 'd782d72374fe6bef2ca666e6cf64039283d0b1b24f4c9aa22cd940956873642f' -or
        $script:Receipt.packaging_revision -cne 'b6c874d7b5de8257dde37685f0c3bfeb79b50b14' -or
        $script:Receipt.signing.status -cne 'test' -or
        $script:Receipt.signing.signed_package_sha256 -cne $script:Receipt.package_sha256) {
        throw 'signed receipt does not bind the pinned candidate'
    }
    $script:HelperSha256 = Get-CnMonMsixSessionHash -Path $script:NativeHelperFullPath
    $null = Get-CnMonMsixDesktopPeMachine -Path $script:NativeHelperFullPath

    $profile = [Environment]::GetFolderPath([Environment+SpecialFolder]::UserProfile)
    if ([string]::IsNullOrWhiteSpace($profile) -or $profile -cne $env:USERPROFILE -or [string]::IsNullOrWhiteSpace($env:LOCALAPPDATA)) {
        throw 'actual user profile paths are unavailable'
    }
    $profileOutputPaths = @(
        (Join-Path $profile '.cn'), (Join-Path $profile '.cn-ping-history.db'), (Join-Path $profile 'cn.log'),
        (Join-Path $profile 'msix-profile-history.db'), (Join-Path $profile 'msix-profile.log'),
        (Join-Path $profile 'msix-cli-history.db'), (Join-Path $profile 'msix-cli.log')
    )
    foreach ($path in $profileOutputPaths) { if (Test-Path -LiteralPath $path) { throw 'a real-profile qualification target already exists; existing user data was preserved' } }
    if (-not (Test-Path -LiteralPath $script:ArtifactFullPath -PathType Container)) { throw 'private artifact directory must already exist' }
    if (Test-Path -LiteralPath (Join-Path $script:ArtifactFullPath 'desktop-session-state.json')) { throw 'desktop session state already exists' }

    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = [Security.Principal.WindowsPrincipal]::new($identity)
    if ($null -eq $identity.User -or $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator) -or
        @($identity.Groups | Where-Object { $_.Value -ceq 'S-1-5-32-544' }).Count -gt 0) {
        throw 'desktop driver requires the ordinary user token'
    }
    $script:PwshExe = Join-Path $PSHOME 'pwsh.exe'
    if (-not (Test-Path -LiteralPath $script:PwshExe -PathType Leaf)) { throw 'PowerShell 7 executable is unavailable for fresh-shell probes' }

    $preflightDirectory = Join-Path $script:ArtifactFullPath 'preflight'
    $null = New-Item -ItemType Directory -Path $preflightDirectory -ErrorAction Stop
    $desktopChecks = Join-Path $PSScriptRoot 'test-msix-desktop-checks.ps1'
    if (-not (Test-Path -LiteralPath $desktopChecks -PathType Leaf)) { throw 'read-only desktop preflight is unavailable' }
    $preflight = Invoke-CnMonMsixSessionReadOnlyPreflight -ScriptPath $desktopChecks `
        -PackagePath $script:PackageFullPath -ReceiptPath $script:ReceiptFullPath -ResultsPath $script:ResultsFullPath `
        -NativeHelperPath $script:NativeHelperFullPath -HelperSha256 $script:HelperSha256 -ArtifactDirectory $preflightDirectory `
        -ExpectedPackageSha256 ([string]$script:Receipt.package_sha256) -ExpectedReceiptSha256 (Get-CnMonMsixSessionHash -Path $script:ReceiptFullPath)
    $signerCertificatePath = Join-Path $preflightDirectory 'msix-test-signer.cer'
    $signerCertificate = [Security.Cryptography.X509Certificates.X509Certificate2]::new($signerCertificatePath)
    try { $signerNotAfterUtc = Get-CnMonMsixSessionSignerNotAfter -Certificate $signerCertificate -ExpectedThumbprint ([string]$script:Receipt.signing.certificate_thumbprint) }
    finally { $signerCertificate.Dispose() }

    $existing = @(Get-AppxPackage -Name ([string]$script:Receipt.name) -ErrorAction Stop)
    if ($existing.Count -ne 0) { throw 'a package with this candidate name is already registered; no package state was changed' }
    $script:State = [ordered]@{
        schema_version = 1
        run_id = [Guid]::NewGuid().ToString('N')
        run_started_utc = [DateTimeOffset]::UtcNow.ToString('o')
        run_completed_utc = ''
        signer_not_after_utc = $signerNotAfterUtc
        supplement_created = $false
        package_sha256 = [string]$script:Receipt.package_sha256
        receipt_sha256 = Get-CnMonMsixSessionHash -Path $script:ReceiptFullPath
        helper_sha256 = $script:HelperSha256
        user_sid = [string]$identity.User.Value
        session_id = [Diagnostics.Process]::GetCurrentProcess().SessionId
        artifact_directory = [IO.Path]::GetFullPath($script:ArtifactFullPath)
        package_full_name = ''
        package_owned = $false
        package_install_attempted = $false
        candidate_absent_before_install = $true
        expected_pfn = [string]$script:Receipt.expected_pfn
        path_before = [Environment]::GetEnvironmentVariable('PATH', 'Process')
        completed = [ordered]@{ prepare = $false; alias_off = $false; alias_on = $false; repair_before = $false; repair_after = $false; screenshots = $false }
        owned_files = @()
        planned_user_paths = @($profileOutputPaths | ForEach-Object { [IO.Path]::GetFullPath($_) })
        owned_appdata_files = @()
        planned_appdata_paths = @()
        work_files = @()
        owned_shells = @()
        appdata_history_name = 'cn-mon-msix-desktop-' + [Guid]::NewGuid().ToString('N') + '.db'
        appdata_log_name = 'cn-mon-msix-desktop-' + [Guid]::NewGuid().ToString('N') + '.log'
        appdata_history_path = ''
        appdata_log_path = ''
        settings_window_binding = $null
        screenshots = [Collections.Generic.List[object]]::new()
        cleanup_passed = $false
        observations = @{
            ordinary_user = $true; medium_integrity = [bool]$preflight.preflight.medium_integrity
            interactive_session = [bool]$preflight.preflight.interactive_session_eligible
            alias = @{
                off = @{ settings_opened = $false; settings_verified = $false; pwsh_failure = $false; pwsh_success = $false; cmd_failure = $false; cmd_success = $false }
                on = @{ settings_opened = $false; settings_verified = $false; pwsh_failure = $false; pwsh_success = $false; cmd_failure = $false; cmd_success = $false; packaged_process_identity_verified = $false }
                path_unchanged = $false
            }
            repair = @{ activation_observed = $false; completion_observed = $false; data_before_sha256 = ''; data_after_sha256 = ''; readback_verified = $false }
            screenshots = @(); desktop_cleanup_passed = $false
        }
    }
    $script:State.appdata_history_path = Join-Path $env:LOCALAPPDATA $script:State.appdata_history_name
    $script:State.appdata_log_path = Join-Path $env:LOCALAPPDATA $script:State.appdata_log_name
    $packageLocalRoot = Join-Path $env:LOCALAPPDATA ('Packages\' + [string]$script:Receipt.expected_pfn + '\LocalCache\Local')
    $script:State.planned_appdata_paths = @(
        [IO.Path]::GetFullPath((Join-Path $packageLocalRoot $script:State.appdata_history_name)),
        [IO.Path]::GetFullPath((Join-Path $packageLocalRoot $script:State.appdata_log_name))
    )
    foreach ($path in $script:State.planned_appdata_paths) {
        if (Test-Path -LiteralPath $path) { throw 'package-private AppData target already exists; prior data was preserved' }
    }
    Save-CnMonMsixSessionState

    $initialSettings = Open-CnMonMsixSessionSettings -Page Aliases
    try {
        $script:State.settings_window_binding = [ordered]@{
            linkage = [string]$initialSettings.Linkage
            host_pid = [int]$initialSettings.Process.Id
            hwnd = '0x{0:X}' -f $initialSettings.WindowHandle.ToInt64()
            settings_pid = [int]$initialSettings.SettingsProcess.Id
            session_id = [int]$initialSettings.Process.SessionId
            user_sid = [string]$identity.User.Value
        }
        Save-CnMonMsixSessionState
        Write-Host "Pre-install Settings window binding verified: linkage=$($initialSettings.Linkage); host_pid=$($initialSettings.Process.Id); hwnd=$($script:State.settings_window_binding.hwnd); settings_pid=$($initialSettings.SettingsProcess.Id); session_id=$($initialSettings.Process.SessionId); user_sid=$($identity.User.Value)"
    }
    finally {
        $initialSettings.Process.Dispose()
        if ($initialSettings.SettingsProcess.Id -ne $initialSettings.Process.Id) { $initialSettings.SettingsProcess.Dispose() }
    }

    $script:State.package_install_attempted = $true
    Save-CnMonMsixSessionState
    Add-AppxPackage -Path $script:PackageFullPath -ErrorAction Stop
    $installed = @(Get-AppxPackage -Name ([string]$script:Receipt.name) -ErrorAction Stop | Where-Object {
        [string]$_.PackageFamilyName -ceq [string]$script:Receipt.expected_pfn -and
        [string]$_.Publisher -ceq [string]$script:Receipt.publisher -and
        [string]$_.Version -ceq [string]$script:Receipt.package_version
    })
    if ($installed.Count -ne 1) { throw 'per-user candidate installation did not register the exact package' }
    $script:State.package_full_name = [string]$installed[0].PackageFullName
    $script:State.package_owned = $true
    Save-CnMonMsixSessionState
    $null = Assert-CnMonMsixSessionInstalledPackage

    $script:AliasPath = Join-Path $env:LOCALAPPDATA 'Microsoft\WindowsApps\cn-mon.exe'
    $null = Get-CnMonMsixSessionNativeTest -TestName 'TestNativeMSIXInstalledAlias'
    $script:State.completed.prepare = $true
    Save-CnMonMsixSessionState
}

function Invoke-CnMonMsixSessionSeed {
    $profile = $env:USERPROFILE
    $profileConfig = Join-Path $profile '.cn'
    $defaults = @((Join-Path $profile '.cn-ping-history.db'), (Join-Path $profile 'cn.log'))
    try {
        $null = Get-CnMonMsixSessionNativeTest -TestName 'TestNativeMSIXRealProfileDefaults' -Environment @{ CNMON_MSIX_PROFILE_DEFAULTS = '1' }
    }
    finally { Set-CnMonMsixSessionUserFileHash -Paths $defaults }

    $profileHistory = [IO.Path]::GetFullPath((Join-Path $profile 'msix-profile-history.db'))
    $profileLog = [IO.Path]::GetFullPath((Join-Path $profile 'msix-profile.log'))
    $config = "[ping]`nhistory_file = $profileHistory`n[logging]`nlogfile = $profileLog`n"
    $stream = [IO.File]::Open($profileConfig, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
    try {
        $bytes = [Text.UTF8Encoding]::new($false).GetBytes($config)
        $stream.Write($bytes, 0, $bytes.Length)
    }
    finally {
        $stream.Dispose()
        Set-CnMonMsixSessionUserFileHash -Paths @($profileConfig)
    }

    $layerFiles = @(
        (Join-Path $profile 'msix-profile-history.db'), (Join-Path $profile 'msix-profile.log'),
        (Join-Path $profile 'msix-cli-history.db'), (Join-Path $profile 'msix-cli.log')
    )
    try {
        $null = Get-CnMonMsixSessionNativeTest -TestName 'TestNativeMSIXRealProfileLayers' -Environment @{
            CNMON_MSIX_PROFILE_LAYERS = '1'
            CNMON_MSIX_PROFILE_HISTORY = $layerFiles[0]
            CNMON_MSIX_PROFILE_LOG = $layerFiles[1]
            CNMON_MSIX_CLI_HISTORY = $layerFiles[2]
            CNMON_MSIX_CLI_LOG = $layerFiles[3]
        }
    }
    finally { Set-CnMonMsixSessionUserFileHash -Paths $layerFiles }

    $appDataFiles = @($script:State.planned_appdata_paths)
    try {
        $null = Get-CnMonMsixSessionNativeTest -TestName 'TestNativeMSIXAppDataLifecycle' -Environment @{
            CNMON_MSIX_APPDATA_PHASE = 'seed'
            CNMON_MSIX_APPDATA_HISTORY = $script:State.appdata_history_path
            CNMON_MSIX_APPDATA_LOG = $script:State.appdata_log_path
        }
    }
    finally { Set-CnMonMsixSessionAppDataFileHashes -Paths $appDataFiles }
}

function Invoke-CnMonMsixSessionListingScreenshots {
    if (-not (Test-CnMonMsixDesktopPhaseAllowed -Phase 'Screenshots' -Completed $script:State.completed)) {
        throw 'listing screenshots are out of phase'
    }
    $workDirectory = Join-Path $env:USERPROFILE ('cn-mon-msix-desktop-session-' + [string]$script:State.run_id)
    if (Test-Path -LiteralPath $workDirectory) { throw 'desktop listing work directory already exists' }
    $script:State.work_directory = [IO.Path]::GetFullPath($workDirectory)
    $script:State.work_directory_owned = $false
    Save-CnMonMsixSessionState
    $null = New-Item -ItemType Directory -Path $workDirectory -ErrorAction Stop
    $script:State.work_directory_owned = $true
    Save-CnMonMsixSessionState
    $history = Join-Path $workDirectory 'history.db'
    $logPath = Join-Path $workDirectory 'cn.log'
    $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    $listener.Start()
    $port = ([Net.IPEndPoint]$listener.LocalEndpoint).Port
    $script:State.tcp_port = [int]$port
    Save-CnMonMsixSessionState
    try {
        $seed = [Diagnostics.ProcessStartInfo]::new()
        $seed.FileName = $script:AliasPath
        $seed.UseShellExecute = $false
        $seed.RedirectStandardOutput = $true
        $seed.RedirectStandardError = $true
        foreach ($arg in @('127.0.0.1', '--format', 'json', '--history-file', $history, '--log-file', $logPath, '--tcp', [string]$port)) { $seed.ArgumentList.Add([string]$arg) }
        $seedProcess = [Diagnostics.Process]::Start($seed)
        try {
            $seedOutTask = $seedProcess.StandardOutput.ReadToEndAsync()
            $seedErrTask = $seedProcess.StandardError.ReadToEndAsync()
            if (-not $seedProcess.WaitForExit(30000)) { $seedProcess.Kill($true); throw 'loopback history seed timed out' }
            $seedExitCode = [int]$seedProcess.ExitCode
            $seedOut = $seedOutTask.GetAwaiter().GetResult()
            $seedErr = $seedErrTask.GetAwaiter().GetResult()
        }
        finally { $seedProcess.Dispose() }
        Save-CnMonMsixSessionTranscript -Name ('listing-seed-' + [Guid]::NewGuid().ToString('N') + '.txt') -Text "EXIT=$seedExitCode`r`nSTDOUT:`r`n$seedOut`r`nSTDERR:`r`n$seedErr"
        if ($seedExitCode -ne 0 -or -not [string]::IsNullOrEmpty($seedErr)) { throw 'loopback history seed failed' }
        Set-CnMonMsixSessionWorkFileHashes -Paths @($history, $logPath)

    $listStart = [Diagnostics.ProcessStartInfo]::new()
    $listStart.FileName = $script:AliasPath
    $listStart.UseShellExecute = $false
    $listStart.RedirectStandardOutput = $true
    $listStart.RedirectStandardError = $true
    foreach ($arg in @('--list', '--format', 'json', '--history-file', $history, '--log-file', $logPath)) { $listStart.ArgumentList.Add([string]$arg) }
    $listProcess = [Diagnostics.Process]::Start($listStart)
    $listOutput = $listProcess.StandardOutput.ReadToEnd()
    $listError = $listProcess.StandardError.ReadToEnd()
    $listProcess.WaitForExit()
    Set-CnMonMsixSessionWorkFileHashes -Paths @($history, $logPath)
    if ($listProcess.ExitCode -ne 0 -or $listError) { $listProcess.Dispose(); throw 'history listing could not be prepared' }
    $listProcess.Dispose()
    $requestId = Get-CnMonMsixSessionSeedRequestId -Json $listOutput

    $runnerPath = Join-Path $script:ArtifactFullPath 'desktop-session-terminal-command.ps1'
    $runner = @'
param([string]$Mode, [string]$AliasPath, [string]$HistoryPath, [string]$LogPath, [string]$Port, [string]$RequestId, [string]$ChildToken, [string]$ChildReportPath)
$ErrorActionPreference = 'Stop'
[string[]]$arguments = switch ($Mode) {
    'help' { @('--help') }
    'table' { @('127.0.0.1', '--format', 'table', '--history-file', $HistoryPath, '--log-file', $LogPath) }
    'json-tcp' { @('127.0.0.1', '--format', 'json', '--tcp', $Port, '--history-file', $HistoryPath, '--log-file', $LogPath) }
    'history-list' { @('--list', '--history-file', $HistoryPath, '--log-file', $LogPath) }
    'history-detail' { @('--show', $RequestId, '--history-file', $HistoryPath, '--log-file', $LogPath) }
    default { throw 'invalid desktop screenshot command' }
}
& $AliasPath @arguments
$code = $LASTEXITCODE
$self = Get-CimInstance -ClassName Win32_Process -Filter "ProcessId=$PID" -ErrorAction Stop
$report = [ordered]@{ pid = [int]$PID; parent_pid = [int]$self.ParentProcessId; session_id = [Diagnostics.Process]::GetCurrentProcess().SessionId; token = $ChildToken; mode = $Mode; exit_code = [int]$code }
$bytes = [Text.UTF8Encoding]::new($false).GetBytes((ConvertTo-Json -InputObject $report -Compress))
$stream = [IO.File]::Open($ChildReportPath, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
try { $stream.Write($bytes, 0, $bytes.Length) }
finally { $stream.Dispose() }
Write-Host "cn-mon exit code: $code"
Read-Host 'After the screenshot is saved, press Enter here to close this run-owned shell'
exit $code
'@
    if (Test-Path -LiteralPath $runnerPath) { throw 'private Terminal command script already exists' }
    $runnerStream = [IO.File]::Open($runnerPath, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
    try { $runnerBytes = [Text.UTF8Encoding]::new($false).GetBytes($runner); $runnerStream.Write($runnerBytes, 0, $runnerBytes.Length) }
    finally { $runnerStream.Dispose() }
    $wt = Get-Command wt.exe -ErrorAction SilentlyContinue
    if ($null -eq $wt) { throw 'Windows Terminal is unavailable for listing screenshots' }
    $screens = @(
        @{ Mode = 'help'; Name = '01-cli-help.png' },
        @{ Mode = 'table'; Name = '02-icmp-table.png' },
        @{ Mode = 'json-tcp'; Name = '03-json-tcp.png' },
        @{ Mode = 'history-list'; Name = '04-history-list.png' },
        @{ Mode = 'history-detail'; Name = '05-history-detail.png' }
    )
    foreach ($screen in $screens) {
        $childToken = [Guid]::NewGuid().ToString('N')
        $terminalTitle = 'cnmon-' + [string]$script:State.run_id.Substring(0, 8) + '-' + [string]$screen.Mode
        $childReportPath = Join-Path $script:ArtifactFullPath ('desktop-child-' + $childToken + '.json')
        $beforeIds = [Collections.Generic.HashSet[int]]::new()
        foreach ($existing in @(Get-CimInstance -ClassName Win32_Process -Filter "Name='pwsh.exe'" -ErrorAction Stop)) { $null = $beforeIds.Add([int]$existing.ProcessId) }
        $start = [Diagnostics.ProcessStartInfo]::new()
        $start.FileName = $wt.Source
        $start.UseShellExecute = $false
        foreach ($arg in @('-w', 'new', 'new-tab', '--title', $terminalTitle, '--suppressApplicationTitle', '--startingDirectory', $workDirectory,
                $script:PwshExe, '-NoLogo', '-NoProfile', '-File', $runnerPath,
                '-Mode', $screen.Mode, '-AliasPath', $script:AliasPath, '-HistoryPath', $history,
                '-LogPath', $logPath, '-Port', [string]$port, '-RequestId', $requestId,
                '-ChildToken', $childToken, '-ChildReportPath', $childReportPath)) {
            $start.ArgumentList.Add([string]$arg)
        }
        $terminalLaunch = [Diagnostics.Process]::Start($start)
        $terminalLaunch.Dispose()
        $childDeadline = [DateTime]::UtcNow.AddSeconds(20)
        while (-not (Test-Path -LiteralPath $childReportPath -PathType Leaf) -and [DateTime]::UtcNow -lt $childDeadline) { Start-Sleep -Milliseconds 250 }
        if (-not (Test-Path -LiteralPath $childReportPath -PathType Leaf)) { throw 'the requested Terminal tab did not report its own shell identity' }
        $childReport = Get-Content -LiteralPath $childReportPath -Raw | ConvertFrom-Json -AsHashtable -ErrorAction Stop
        if ([string]$childReport.token -cne $childToken -or [string]$childReport.mode -cne [string]$screen.Mode -or
            [int]$childReport.exit_code -ne 0 -or [int]$childReport.session_id -ne [Diagnostics.Process]::GetCurrentProcess().SessionId -or
            $beforeIds.Contains([int]$childReport.pid)) { throw 'the Terminal shell report did not match this new screenshot command' }
        $childProcess = Get-Process -Id ([int]$childReport.pid) -ErrorAction Stop
        try {
            $childCim = Get-CimInstance -ClassName Win32_Process -Filter "ProcessId=$([int]$childReport.pid)" -ErrorAction Stop
            $childOwner = Invoke-CimMethod -InputObject $childCim -MethodName GetOwnerSid -ErrorAction Stop
            if ([string]$childCim.ExecutablePath -ine $script:PwshExe -or
                [int]$childCim.SessionId -ne [Diagnostics.Process]::GetCurrentProcess().SessionId -or
                [string]$childOwner.Sid -cne $script:State.user_sid -or
                [int]$childCim.ParentProcessId -ne [int]$childReport.parent_pid -or
                -not [string]$childCim.CommandLine -or
                -not $childCim.CommandLine.Contains($runnerPath, [StringComparison]::OrdinalIgnoreCase) -or
                -not $childCim.CommandLine.Contains($childToken, [StringComparison]::Ordinal)) {
                throw 'the Terminal shell process did not match the reported run-owned command and active user session'
            }
            $parentCim = Get-CimInstance -ClassName Win32_Process -Filter "ProcessId=$([int]$childReport.parent_pid)" -ErrorAction Stop
            $parentOwner = Invoke-CimMethod -InputObject $parentCim -MethodName GetOwnerSid -ErrorAction Stop
            if ([int]$parentCim.SessionId -ne [Diagnostics.Process]::GetCurrentProcess().SessionId -or [string]$parentOwner.Sid -cne $script:State.user_sid) {
                throw 'the Terminal shell parent is outside the active ordinary user session'
            }
            $script:State.owned_shells = @($script:State.owned_shells) + @([ordered]@{
                pid = [int]$childReport.pid; parent_pid = [int]$childReport.parent_pid; token = $childToken
                runner_path = [IO.Path]::GetFullPath($runnerPath); mode = [string]$screen.Mode; exited = $false
            })
            Save-CnMonMsixSessionState
        }
        finally { $childProcess.Dispose() }
        Set-CnMonMsixSessionWorkFileHashes -Paths @($history, $logPath)
        Wait-CnMonMsixSessionUserConfirmation -Instruction "Windows Terminal opened the $($screen.Mode) command using the installed package alias. Confirm the expected output is visible, then return here." -RequiredText 'CAPTURE'
        $terminalProcess = $null
        $terminalHandle = [IntPtr]::Zero
        foreach ($candidateTerminal in @(Get-Process -Name WindowsTerminal -ErrorAction SilentlyContinue | Where-Object {
            $_.SessionId -eq [Diagnostics.Process]::GetCurrentProcess().SessionId
        })) {
            try {
                $candidateHandle = Find-CnMonMsixSessionWindowHandle -Process $candidateTerminal -TitleToken $terminalTitle
                $terminalProcess = $candidateTerminal
                $terminalHandle = $candidateHandle
                break
            }
            catch { $candidateTerminal.Dispose() }
        }
        if ($null -eq $terminalProcess) { throw 'the exact run-owned Windows Terminal window is not visible in this user session' }
        Set-CnMonMsixSessionWindowForeground -Process $terminalProcess -WindowHandle $terminalHandle -Maximize
        $script:State.screenshots.Add((Save-CnMonMsixSessionScreenshot -Name $screen.Name -WindowHandle $terminalHandle -Listing))
        Save-CnMonMsixSessionState
        $terminalProcess.Dispose()
        Write-Host 'The screenshot is saved. Press Enter at the Terminal tab prompt so its run-owned PowerShell shell exits.'
        $shellDeadline = [DateTime]::UtcNow.AddSeconds(90)
        while ([DateTime]::UtcNow -lt $shellDeadline) {
            $stillRunning = Get-Process -Id ([int]$childReport.pid) -ErrorAction SilentlyContinue
            if ($null -eq $stillRunning) { break }
            $stillRunning.Dispose()
            Start-Sleep -Milliseconds 500
        }
        $stillRunning = Get-Process -Id ([int]$childReport.pid) -ErrorAction SilentlyContinue
        if ($null -ne $stillRunning) { $stillRunning.Dispose(); throw 'the run-owned Terminal shell did not exit after the user closed its prompt' }
        $ownedShell = @($script:State.owned_shells | Where-Object { [string]$_.token -ceq $childToken })[0]
        $ownedShell.exited = $true
        Save-CnMonMsixSessionState
        Set-CnMonMsixSessionWorkFileHashes -Paths @($history, $logPath)
    }
    $script:State.completed.screenshots = $true
    }
    finally { $listener.Stop() }
}

function Invoke-CnMonMsixSessionCleanup {
    if (-not (Test-Path -LiteralPath $script:StatePath -PathType Leaf)) { return $false }
    if ($null -eq $script:State) { $script:State = Get-Content -LiteralPath $script:StatePath -Raw | ConvertFrom-Json -AsHashtable -ErrorAction Stop }
    if (-not (Test-CnMonMsixSessionStateBinding -State $script:State -PackageSha256 $script:ExpectedPackageSha256 -ReceiptSha256 $script:ExpectedReceiptSha256 -HelperSha256 $script:HelperSha256)) {
        throw 'cleanup state is bound to a different candidate bundle'
    }
    $currentSid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    if ([string]$script:State.user_sid -cne $currentSid -or [int]$script:State.session_id -ne [Diagnostics.Process]::GetCurrentProcess().SessionId) {
        throw "cleanup must run in the original ordinary user active session"
    }
    if ([string]$script:State.expected_pfn -cne [string]$script:Receipt.expected_pfn -or
        $script:State.candidate_absent_before_install -ne $true -or
        -not (Test-CnMonMsixSessionRecoveryPaths -State $script:State -UserProfile $env:USERPROFILE -LocalAppData $env:LOCALAPPDATA -ArtifactDirectory $script:ArtifactFullPath)) {
        throw 'cleanup state contains paths or process identities outside the run allowlist'
    }
    $script:StateCreated = $true
    $ok = $true
    foreach ($shell in @($script:State.owned_shells)) {
        $live = Get-Process -Id ([int]$shell.pid) -ErrorAction SilentlyContinue
        if ($null -eq $live) { $shell.exited = $true; continue }
        try {
            $shellCim = Get-CimInstance -ClassName Win32_Process -Filter "ProcessId=$([int]$shell.pid)" -ErrorAction Stop
            $shellOwner = Invoke-CimMethod -InputObject $shellCim -MethodName GetOwnerSid -ErrorAction Stop
            $expectedRunner = [IO.Path]::GetFullPath((Join-Path $script:ArtifactFullPath 'desktop-session-terminal-command.ps1'))
            $processIdentity = @{
                executable_path = [string]$shellCim.ExecutablePath
                session_id = [int]$shellCim.SessionId
                owner_sid = [string]$shellOwner.Sid
                command_line = [string]$shellCim.CommandLine
            }
            $expectedShell = @{
                executable_path = $script:PwshExe
                session_id = [Diagnostics.Process]::GetCurrentProcess().SessionId
                owner_sid = $currentSid
                runner_path = $expectedRunner
            }
            if (-not (Stop-CnMonMsixSessionShellIfOwned -Shell $shell -ProcessIdentity $processIdentity -Expected $expectedShell)) {
                $ok = $false
                continue
            }
            $deadline = [DateTime]::UtcNow.AddSeconds(5)
            while ([DateTime]::UtcNow -lt $deadline -and (Get-Process -Id ([int]$shell.pid) -ErrorAction SilentlyContinue)) { Start-Sleep -Milliseconds 200 }
            if (Get-Process -Id ([int]$shell.pid) -ErrorAction SilentlyContinue) { $ok = $false }
            else { $shell.exited = $true }
        }
        catch { $ok = $false }
        finally { $live.Dispose() }
    }

    if ($script:State.package_install_attempted -eq $true -or $script:State.package_owned -eq $true) {
        $candidatePackages = @(Get-AppxPackage -Name ([string]$script:Receipt.name) -ErrorAction Stop | Where-Object {
            [string]$_.Name -ceq [string]$script:Receipt.name -and
            [string]$_.PackageFamilyName -ceq [string]$script:Receipt.expected_pfn -and
            [string]$_.Publisher -ceq [string]$script:Receipt.publisher -and
            [string]$_.Version -ceq [string]$script:Receipt.package_version -and
            [string]$_.Architecture -ceq 'X64'
        })
        if ($candidatePackages.Count -gt 1) { $ok = $false }
        elseif ($candidatePackages.Count -eq 1) {
            $candidate = $candidatePackages[0]
            if ($script:State.package_full_name -and [string]$candidate.PackageFullName -cne [string]$script:State.package_full_name) { $ok = $false }
            else {
                $script:State.package_full_name = [string]$candidate.PackageFullName
                $script:State.package_owned = $true
                Save-CnMonMsixSessionState
                Remove-AppxPackage -Package ([string]$candidate.PackageFullName) -ErrorAction Stop
                $remaining = @(Get-AppxPackage -Name ([string]$script:Receipt.name) -ErrorAction Stop | Where-Object {
                    [string]$_.PackageFullName -ceq [string]$candidate.PackageFullName
                })
                if ($remaining.Count -gt 0) { $ok = $false }
                else { $script:State.package_owned = $false; $script:State.package_install_attempted = $false }
            }
        }
        else {
            $script:State.package_owned = $false
            $script:State.package_install_attempted = $false
        }
    }

    $ownedProfile = @{}
    foreach ($file in @($script:State.owned_files)) { $ownedProfile[[IO.Path]::GetFullPath([string]$file.path)] = $file }
    foreach ($path in @($script:State.planned_user_paths)) {
        $fullPath = [IO.Path]::GetFullPath([string]$path)
        if (-not (Test-Path -LiteralPath $fullPath -PathType Leaf)) { continue }
        $entry = $ownedProfile[$fullPath]
        $item = Get-Item -LiteralPath $fullPath -Force
        if ($null -eq $entry -or (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0)) { $ok = $false; continue }
        $actual = Get-CnMonMsixSessionHash -Path $fullPath
        if (Test-CnMonMsixOwnedFileCleanup -OwnedByThisRun:([bool]$entry.owned) -ExpectedSha256 ([string]$entry.sha256) -ActualSha256 $actual) {
            Remove-Item -LiteralPath $fullPath -Force -ErrorAction Stop
        }
        else { $ok = $false }
    }

    foreach ($file in @($script:State.owned_appdata_files)) {
        $path = [IO.Path]::GetFullPath([string]$file.path)
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { continue }
        $item = Get-Item -LiteralPath $path -Force
        if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) { $ok = $false; continue }
        $actual = Get-CnMonMsixSessionHash -Path $path
        if (Test-CnMonMsixOwnedFileCleanup -OwnedByThisRun:([bool]$file.owned) -ExpectedSha256 ([string]$file.sha256) -ActualSha256 $actual) {
            Remove-Item -LiteralPath $path -Force -ErrorAction Stop
        }
        else { $ok = $false }
    }
    foreach ($path in @($script:State.planned_appdata_paths)) {
        if ((Test-Path -LiteralPath ([string]$path) -PathType Leaf) -and
            (@($script:State.owned_appdata_files | Where-Object { [string]$_.path -ieq [string]$path }).Count -eq 0)) { $ok = $false }
    }

    if ($script:State.work_directory) {
        $workPath = [IO.Path]::GetFullPath([string]$script:State.work_directory)
        if (Test-Path -LiteralPath $workPath) {
            if (-not (Remove-CnMonMsixSessionOwnedWorkDirectory -Path $workPath -OwnedByThisRun ([bool]$script:State.work_directory_owned) -WorkFiles @($script:State.work_files))) { $ok = $false }
        }
    }

    foreach ($file in @($script:State.owned_files)) {
        if (Test-Path -LiteralPath ([string]$file.path) -PathType Leaf) { $ok = $false }
    }
    $script:State.cleanup_passed = $ok
    $script:State.observations.desktop_cleanup_passed = $ok
    if ($ok -and [string]::IsNullOrWhiteSpace([string]$script:State.run_completed_utc)) {
        $script:State.run_completed_utc = [DateTimeOffset]::UtcNow.ToString('o')
    }
    Save-CnMonMsixSessionState
    if ($script:SupplementCreated) { Write-CnMonMsixSessionSupplement }
    return $ok
}

function Get-CnMonMsixDesktopPeMachine {
    param([string]$Path)
    $stream = [IO.File]::OpenRead($Path)
    $reader = [IO.BinaryReader]::new($stream)
    try {
        if ($stream.Length -lt 0x86 -or $reader.ReadUInt16() -ne 0x5A4D) { throw 'native helper is not a PE executable' }
        $stream.Position = 0x3C
        $offset = $reader.ReadInt32()
        if ($offset -lt 0x40 -or $offset -gt $stream.Length - 6) { throw 'native helper PE header is invalid' }
        $stream.Position = $offset
        if ($reader.ReadUInt32() -ne 0x00004550 -or $reader.ReadUInt16() -ne 0x8664) { throw 'native helper is not x64' }
    }
    finally { $reader.Dispose(); $stream.Dispose() }
}

$script:State = $null
$script:StateCreated = $false
$script:SupplementCreated = $false
$script:HelperSha256 = ''
$script:ExpectedPackageSha256 = 'd782d72374fe6bef2ca666e6cf64039283d0b1b24f4c9aa22cd940956873642f'
$script:ExpectedReceiptSha256 = '7f7ddda42b87b7611fe5107aeb519e3cf0e04e5e2870a7dc8374d6c7b74db55b'
$script:ArtifactFullPath = $null
$script:PackageFullPath = $null
$script:ReceiptFullPath = $null
$script:ResultsFullPath = $null
$script:NativeHelperFullPath = $null
$script:StatePath = $null
$script:Receipt = $null
$script:AliasPath = $null
$script:PwshExe = $null
$script:ArtifactOwned = $false
$transcriptStarted = $false

try {
    if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) { throw 'desktop session driver requires Windows' }
    if ($PSVersionTable.PSVersion.Major -lt 7) { throw 'PowerShell 7 is required' }
    $script:ArtifactFullPath = [IO.Path]::GetFullPath($ArtifactDirectory)
    if (-not (Test-Path -LiteralPath $script:ArtifactFullPath -PathType Container)) { throw 'private artifact directory must already exist' }
    if ($Phase -eq 'Run') {
        if ((Get-ChildItem -LiteralPath $script:ArtifactFullPath -Force | Measure-Object).Count -ne 0) { throw 'private artifact directory must be an empty run-owned directory' }
        $script:ArtifactOwned = $true
    }
    $transcriptName = if ($Phase -eq 'Run') { 'desktop-session-transcript.txt' } else { 'desktop-session-cleanup-' + [Guid]::NewGuid().ToString('N') + '.txt' }
    $transcriptPath = Join-Path $script:ArtifactFullPath $transcriptName
    Start-Transcript -LiteralPath $transcriptPath -NoClobber | Out-Null
    $transcriptStarted = $true

    $script:PackageFullPath = Get-CnMonMsixSessionInputPath -Path $PackagePath
    $script:ReceiptFullPath = Get-CnMonMsixSessionInputPath -Path $ReceiptPath
    if ($Phase -eq 'Run') {
        if ([string]::IsNullOrWhiteSpace($ResultsPath)) { throw 'ResultsPath is required during Run' }
        $script:ResultsFullPath = Get-CnMonMsixSessionInputPath -Path $ResultsPath
    }
    $script:NativeHelperFullPath = Get-CnMonMsixSessionInputPath -Path $NativeHelperPath
    $script:StatePath = Join-Path $script:ArtifactFullPath 'desktop-session-state.json'

    if ($Phase -eq 'Cleanup') {
        $script:HelperSha256 = Get-CnMonMsixSessionHash -Path $script:NativeHelperFullPath
        if ($script:HelperSha256 -cne $ExpectedNativeHelperSha256.ToLowerInvariant() -or
            (Get-CnMonMsixSessionHash -Path $script:ReceiptFullPath) -cne $script:ExpectedReceiptSha256 -or
            (Get-CnMonMsixSessionHash -Path $script:PackageFullPath) -cne $script:ExpectedPackageSha256) {
            throw 'recovery inputs do not match the pinned candidate bundle'
        }
        $script:Receipt = Get-Content -LiteralPath $script:ReceiptFullPath -Raw | ConvertFrom-Json -AsHashtable -ErrorAction Stop
        $script:AliasPath = Join-Path $env:LOCALAPPDATA 'Microsoft\WindowsApps\cn-mon.exe'
        $script:PwshExe = Join-Path $PSHOME 'pwsh.exe'
        if (-not (Test-Path -LiteralPath $script:StatePath -PathType Leaf)) { throw 'no captured desktop state exists to recover' }
        $script:State = Get-Content -LiteralPath $script:StatePath -Raw | ConvertFrom-Json -AsHashtable -ErrorAction Stop
        $script:StateCreated = $true
        $script:SupplementCreated = [bool]$script:State.supplement_created
        if (-not (Invoke-CnMonMsixSessionCleanup)) { throw 'desktop cleanup is incomplete; see the private state and transcript' }
        Write-Host 'Desktop session cleanup passed.'
    }
    else {
        if (Test-Path -LiteralPath $script:StatePath) { throw 'desktop session state already exists' }
        Invoke-CnMonMsixSessionPrepare
        if (-not (Test-CnMonMsixDesktopPhaseAllowed -Phase 'Prepare' -Completed @{})) { throw 'desktop phase order is invalid' }
        Invoke-CnMonMsixSessionSeed
        Write-CnMonMsixSessionSupplement
        Write-Host 'Candidate is installed for this ordinary user. Follow each prompt and leave the requested Settings or Terminal view visible.'
        Invoke-CnMonMsixSessionAliasState -AliasState Off
        Invoke-CnMonMsixSessionAliasState -AliasState On
        Invoke-CnMonMsixSessionRepair
        Invoke-CnMonMsixSessionListingScreenshots
        Write-CnMonMsixSessionSupplement
        if (-not (Invoke-CnMonMsixSessionCleanup)) { throw 'desktop cleanup is incomplete; the exact package or run-owned data remains' }
        $started = [DateTimeOffset]::Parse([string]$script:State.run_started_utc, [Globalization.CultureInfo]::InvariantCulture, [Globalization.DateTimeStyles]::RoundtripKind)
        $completed = [DateTimeOffset]::Parse([string]$script:State.run_completed_utc, [Globalization.CultureInfo]::InvariantCulture, [Globalization.DateTimeStyles]::RoundtripKind)
        $expires = [DateTimeOffset]::Parse([string]$script:State.signer_not_after_utc, [Globalization.CultureInfo]::InvariantCulture, [Globalization.DateTimeStyles]::RoundtripKind)
        if (-not (Test-CnMonMsixSessionSignerWindow -StartedUtc $started -CompletedUtc $completed -NotAfterUtc $expires)) { throw 'desktop completion fell outside the signed candidate validity window' }
        Write-Host 'Desktop session actions finished. The supplement remains incomplete until independent screenshot and UI review.'
    }
}
catch {
    $originalErrorRecord = $_
    if ($transcriptStarted -and $script:ArtifactOwned) {
        try {
            $failurePath = Join-Path $script:ArtifactFullPath ('desktop-session-failure-' + [Guid]::NewGuid().ToString('N') + '.clixml')
            $originalErrorRecord | Export-Clixml -LiteralPath $failurePath -Depth 8 -NoClobber -ErrorAction Stop
        }
        catch { [Console]::Error.WriteLine('The private failure record could not be saved; the transcript remains available.') }
    }
    [Console]::Error.WriteLine('Desktop session driver stopped safely; inspect the private transcript and run -Phase Cleanup in the same ordinary RDP session if package installation began.')
    if ($Phase -eq 'Run' -and $script:StateCreated -and $null -ne $script:State -and $null -ne $script:Receipt) {
        try { $null = Invoke-CnMonMsixSessionCleanup }
        catch { [Console]::Error.WriteLine('Automatic cleanup could not complete; the exact owned state remains recorded for recovery.') }
    }
    exit 1
}
finally {
    if ($transcriptStarted) { try { Stop-Transcript | Out-Null } catch { } }
}
