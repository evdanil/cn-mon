# Copyright 2026 cn-mon contributors
# SPDX-License-Identifier: Apache-2.0
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$IdentityName,

    [Parameter(Mandatory = $true)]
    [string]$ExpectedPackageFamilyName
)

$ErrorActionPreference = 'Stop'

function Get-CnMonDesktopSurfaceDecision([bool]$SameSid, [bool]$SameSession) {
    if (-not $SameSid -or -not $SameSession) {
        return [pscustomobject]@{ eligible = $false; status = 'wrong_security_context' }
    }
    return [pscustomobject]@{ eligible = $true; status = 'eligible' }
}

function Test-CnMonDesktopSurfaceCandidate([object[]]$Packages, [string]$IdentityName, [string]$ExpectedPackageFamilyName) {
    foreach ($package in $Packages) {
        if ([string]$package.Name -ceq $IdentityName -and [string]$package.PackageFamilyName -ceq $ExpectedPackageFamilyName) { return $true }
    }
    return $false
}

function Set-CnMonDesktopSurfaceOwnership([object]$Process, [bool]$SameSid, [bool]$SameSession, [hashtable]$Observed) {
    $Observed.settings_same_sid = $SameSid
    $Observed.settings_same_session = $SameSession
    $decision = Get-CnMonDesktopSurfaceDecision -SameSid $SameSid -SameSession $SameSession
    if (-not $decision.eligible) {
        $Observed.probe_status = $decision.status
        return $null
    }
    $Observed.settings_opened_by_probe = $true
    return $Process
}

function New-CnMonDesktopSurfaceReport([hashtable]$Observed) {
    $sessionId = 0
    if ([string]$Observed.automation_process_session_id -match '^\d{1,10}$') {
        try { $sessionId = [int]$Observed.automation_process_session_id } catch { }
    }
    $probeStatuses = @('not_attempted', 'candidate_not_installed', 'settings_preexisting', 'settings_unavailable', 'wrong_security_context', 'ui_automation_unavailable', 'observed')
    $probeStatus = 'not_attempted'
    if ([string]$Observed.probe_status -cin $probeStatuses) { $probeStatus = [string]$Observed.probe_status }
    $repairStatus = 'not_observed'
    if ([string]$Observed.repair_button_status -cin @('present', 'absent')) { $repairStatus = [string]$Observed.repair_button_status }

    return [pscustomobject][ordered]@{
        schema_version = 1
        candidate_installed = ($Observed.candidate_installed -is [bool] -and $Observed.candidate_installed)
        automation_process_session_id = $sessionId
        settings_opened_by_probe = ($Observed.settings_opened_by_probe -is [bool] -and $Observed.settings_opened_by_probe)
        settings_same_sid = ($Observed.settings_same_sid -is [bool] -and $Observed.settings_same_sid)
        settings_same_session = ($Observed.settings_same_session -is [bool] -and $Observed.settings_same_session)
        probe_status = $probeStatus
        repair_button_status = $repairStatus
    }
}

function Add-CnMonDesktopSurfaceNativeType {
    if ($null -ne ('CnMonDesktopSurfaceNative' -as [type])) { return }
    Add-Type -TypeDefinition @'
using System;
using System.Diagnostics;
using System.Runtime.InteropServices;
using System.Security.Principal;

public static class CnMonDesktopSurfaceNative
{
    [StructLayout(LayoutKind.Sequential)]
    private struct SID_AND_ATTRIBUTES { public IntPtr Sid; public int Attributes; }

    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern IntPtr OpenProcess(uint access, bool inheritHandle, int processId);
    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool CloseHandle(IntPtr handle);
    [DllImport("advapi32.dll", SetLastError = true)]
    private static extern bool OpenProcessToken(IntPtr process, uint access, out IntPtr token);
    [DllImport("advapi32.dll", SetLastError = true)]
    private static extern bool GetTokenInformation(IntPtr token, int infoClass, IntPtr info, int length, out int needed);

    public static bool IsSameUser(int processId)
    {
        IntPtr process = IntPtr.Zero, token = IntPtr.Zero, buffer = IntPtr.Zero;
        try
        {
            process = OpenProcess(0x1000, false, processId);
            if (process == IntPtr.Zero || !OpenProcessToken(process, 0x0008, out token)) return false;
            int needed;
            GetTokenInformation(token, 1, IntPtr.Zero, 0, out needed);
            if (needed <= 0) return false;
            buffer = Marshal.AllocHGlobal(needed);
            if (!GetTokenInformation(token, 1, buffer, needed, out needed)) return false;
            SID_AND_ATTRIBUTES user = (SID_AND_ATTRIBUTES)Marshal.PtrToStructure(buffer, typeof(SID_AND_ATTRIBUTES));
            return WindowsIdentity.GetCurrent().User.Equals(new SecurityIdentifier(user.Sid));
        }
        catch { return false; }
        finally
        {
            if (buffer != IntPtr.Zero) Marshal.FreeHGlobal(buffer);
            if (token != IntPtr.Zero) CloseHandle(token);
            if (process != IntPtr.Zero) CloseHandle(process);
        }
    }
}
'@ -ErrorAction Stop
}

function Test-CnMonDesktopRepairControl([int]$ProcessId) {
    Add-Type -AssemblyName UIAutomationClient -ErrorAction Stop
    Add-Type -AssemblyName UIAutomationTypes -ErrorAction Stop
    $root = [Windows.Automation.AutomationElement]::RootElement
    $processCondition = [Windows.Automation.PropertyCondition]::new(
        [Windows.Automation.AutomationElement]::ProcessIdProperty, $ProcessId)
    $windows = $root.FindAll([Windows.Automation.TreeScope]::Children, $processCondition)
    $sawButton = $false
    foreach ($window in $windows) {
        $buttonCondition = [Windows.Automation.PropertyCondition]::new(
            [Windows.Automation.AutomationElement]::ControlTypeProperty,
            [Windows.Automation.ControlType]::Button)
        $buttons = $window.FindAll([Windows.Automation.TreeScope]::Descendants, $buttonCondition)
        foreach ($button in $buttons) {
            $sawButton = $true
            if ([string]$button.Current.Name -ceq 'Repair') { return [pscustomobject]@{ buttons_seen = $true; repair = 'present' } }
        }
    }
    return [pscustomobject]@{ buttons_seen = $sawButton; repair = 'not_observed' }
}

function Close-CnMonDesktopSurfaceOwnedProcess([object]$Process, [Nullable[DateTime]]$StartTime) {
    if ($null -eq $Process -or $null -eq $StartTime) { return }
    try {
        $Process.Refresh()
        if (-not $Process.HasExited -and $Process.StartTime -eq $StartTime) {
            $null = $Process.CloseMainWindow()
            if (-not $Process.WaitForExit(3000)) {
                $Process.Refresh()
                if (-not $Process.HasExited -and $Process.StartTime -eq $StartTime) {
                    Stop-Process -Id ([int]$Process.Id) -Force -ErrorAction SilentlyContinue
                }
            }
        }
    }
    catch { }
}

function Merge-CnMonDesktopRepairObservation([object]$Prior, [object]$Current) {
    return [pscustomobject]@{
        buttons_seen = ($Prior.buttons_seen -eq $true -or $Current.buttons_seen -eq $true)
        repair_present = ($Prior.repair_present -eq $true -or [string]$Current.repair -ceq 'present')
    }
}

$observed = @{
    candidate_installed = $false
    automation_process_session_id = 0
    settings_opened_by_probe = $false
    settings_same_sid = $false
    settings_same_session = $false
    probe_status = 'not_attempted'
    repair_button_status = 'not_observed'
}
$ownedSettings = $null
$ownedSettingsStartTime = $null

try {
    if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) { throw 'unsupported_platform' }
    if ($ExpectedPackageFamilyName -notmatch '^[A-Za-z0-9.-]+_[A-Za-z0-9]{13}$') { throw 'invalid_input' }

    $currentProcess = [Diagnostics.Process]::GetCurrentProcess()
    $observed.automation_process_session_id = [int]$currentProcess.SessionId
    $packages = @(Get-AppxPackage -Name $IdentityName -ErrorAction Stop | Where-Object { [string]$_.PackageFamilyName -ceq $ExpectedPackageFamilyName })
            $observed.candidate_installed = Test-CnMonDesktopSurfaceCandidate -Packages $packages -IdentityName $IdentityName -ExpectedPackageFamilyName $ExpectedPackageFamilyName
    if (-not $observed.candidate_installed) {
        $observed.probe_status = 'candidate_not_installed'
    }
    else {
        Add-CnMonDesktopSurfaceNativeType
        $existingSettings = @(Get-Process -Name SystemSettings -ErrorAction SilentlyContinue)
        $sameContextSettings = @($existingSettings | Where-Object {
            try { $_.SessionId -eq $currentProcess.SessionId -and [CnMonDesktopSurfaceNative]::IsSameUser([int]$_.Id) }
            catch { $false }
        })
        if ($sameContextSettings.Count -gt 0) {
            $observed.settings_same_sid = $true
            $observed.settings_same_session = $true
            $observed.probe_status = 'settings_preexisting'
        }
        else {
            $beforeIds = @($existingSettings | ForEach-Object { [int]$_.Id })
            $uri = 'ms-settings:appsfeatures-app?' + $ExpectedPackageFamilyName
            $null = Start-Process -FilePath $uri -PassThru -ErrorAction Stop
            $candidateSettings = $null
            $deadline = [DateTime]::UtcNow.AddSeconds(12)
            while ([DateTime]::UtcNow -lt $deadline -and $null -eq $candidateSettings) {
                foreach ($process in @(Get-Process -Name SystemSettings -ErrorAction SilentlyContinue)) {
                    if ($beforeIds -notcontains [int]$process.Id) {
                        try {
                            $candidate = [Diagnostics.Process]::GetProcessById([int]$process.Id)
                            $candidate.Refresh()
                            if ($candidate.HasExited) { continue }
                            $candidateSettings = $candidate
                            break
                        }
                        catch { }
                    }
                }
                if ($null -eq $candidateSettings) { Start-Sleep -Milliseconds 300 }
            }

            if ($null -eq $candidateSettings) {
                $observed.probe_status = 'settings_unavailable'
            }
            else {
                try {
                    $sameSid = [CnMonDesktopSurfaceNative]::IsSameUser([int]$candidateSettings.Id)
                    $sameSession = ($candidateSettings.SessionId -eq $currentProcess.SessionId)
                }
                catch { $sameSid = $false; $sameSession = $false }
                $ownedSettings = Set-CnMonDesktopSurfaceOwnership -Process $candidateSettings -SameSid $sameSid -SameSession $sameSession -Observed $observed
                if ($null -eq $ownedSettings) {
                    # The process identity did not prove ownership, so leave it untouched.
                }
                else {
                    $ownedSettingsStartTime = $ownedSettings.StartTime
                    $observed.probe_status = 'ui_automation_unavailable'
                    # UI Automation FindAll can hang; the caller must bound this probe process.
                    $repairState = [pscustomobject]@{ buttons_seen = $false; repair_present = $false }
                    $deadline = [DateTime]::UtcNow.AddSeconds(12)
                    while ([DateTime]::UtcNow -lt $deadline) {
                        $surface = Test-CnMonDesktopRepairControl -ProcessId ([int]$ownedSettings.Id)
                        $repairState = Merge-CnMonDesktopRepairObservation -Prior $repairState -Current $surface
                        if ($repairState.repair_present) {
                            $observed.repair_button_status = 'present'
                            $observed.probe_status = 'observed'
                            break
                        }
                        Start-Sleep -Milliseconds 400
                    }
                    if ($observed.probe_status -eq 'ui_automation_unavailable' -and $repairState.buttons_seen) {
                        $observed.repair_button_status = 'absent'
                        $observed.probe_status = 'observed'
                    }
                }
            }
        }
    }
}
catch {
    if ($_.Exception.Message -eq 'unsupported_platform' -or $_.Exception.Message -eq 'invalid_input') {
        $observed.probe_status = 'not_attempted'
    }
    elseif ($observed.settings_opened_by_probe -eq $true) {
        $observed.probe_status = 'ui_automation_unavailable'
    }
    else {
        $observed.probe_status = 'settings_unavailable'
    }
}
finally {
    Close-CnMonDesktopSurfaceOwnedProcess -Process $ownedSettings -StartTime $ownedSettingsStartTime
}

$report = New-CnMonDesktopSurfaceReport -Observed $observed
ConvertTo-Json -InputObject $report -Depth 2 -Compress
