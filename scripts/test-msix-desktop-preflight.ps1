# Copyright 2026 cn-mon contributors
# SPDX-License-Identifier: Apache-2.0
[CmdletBinding()]
param(
    [string]$PackagePath,
    [string]$ReceiptPath,

    [Parameter(Mandatory = $true)]
    [string]$SdkBinDirectory,

    [Parameter(Mandatory = $true)]
    [string]$ArtifactDirectory
)

$ErrorActionPreference = 'Stop'

function ConvertTo-CnMonPreflightVersion([object]$Value) {
    $text = [string]$Value
    if ($text -notmatch '^\d+(?:\.\d+){1,3}$') { return '0.0.0.0' }
    try {
        $version = [version]$text
        return '{0}.{1}.{2}.{3}' -f $version.Major, $version.Minor, [math]::Max(0, $version.Build), [math]::Max(0, $version.Revision)
    }
    catch { return '0.0.0.0' }
}

function Get-CnMonPreflightOsDetails(
    [string]$FallbackVersion,
    [string]$CimVersion,
    [string]$CimBuild
) {
    $selectedVersion = ConvertTo-CnMonPreflightVersion $FallbackVersion
    $cimVersionNumber = ConvertTo-CnMonPreflightVersion $CimVersion
    if ($cimVersionNumber -cne '0.0.0.0') { $selectedVersion = $cimVersionNumber }

    $selectedBuild = 0L
    try { $selectedBuild = [long]([version]$selectedVersion).Build } catch { }
    if ($CimBuild -match '^\d{1,10}$') { $selectedBuild = [long]$CimBuild }
    return [pscustomobject]@{
        version = $selectedVersion
        build = $selectedBuild
    }
}

function Get-CnMonPreflightSession(
    [int]$SessionId,
    [int]$ConnectionStateCode,
    [int]$ProtocolType
) {
    $connectionState = switch ($ConnectionStateCode) {
        0 { 'active' }
        1 { 'connected' }
        2 { 'connect_query' }
        3 { 'shadow' }
        4 { 'disconnected' }
        5 { 'idle' }
        6 { 'listen' }
        7 { 'reset' }
        8 { 'down' }
        9 { 'initializing' }
        default { 'unknown' }
    }
    $protocol = switch ($ProtocolType) {
        0 { 'console' }
        2 { 'rdp' }
        -1 { 'unknown' }
        default { 'other' }
    }
    return [pscustomobject]@{
        connection_state = $connectionState
        protocol = $protocol
        eligible = ($SessionId -gt 0 -and $connectionState -eq 'active' -and $protocol -in @('console', 'rdp'))
    }
}

function Test-CnMonPreflightAdministratorsSid([object[]]$Groups) {
    return @($Groups | Where-Object { [string]$_.Value -ceq 'S-1-5-32-544' }).Count -gt 0
}

function Test-CnMonPreflightAppxCmdlets([string[]]$CommandNames) {
    foreach ($requiredName in @('Add-AppxPackage', 'Get-AppxPackage', 'Remove-AppxPackage')) {
        if ($CommandNames -notcontains $requiredName) { return $false }
    }
    return $true
}

function New-CnMonPreflightReport([object]$Observed) {
    $session = Get-CnMonPreflightSession -SessionId $Observed.process_session_id -ConnectionStateCode $Observed.connection_state_code -ProtocolType $Observed.protocol_type
    $osBuild = 0L
    if ([string]$Observed.os_build -match '^\d{1,10}$') { $osBuild = [long]$Observed.os_build }
    $processSessionId = 0
    if ([string]$Observed.process_session_id -match '^\d{1,10}$') { $processSessionId = [int]$Observed.process_session_id }
    $packageHash = $null
    if ([string]$Observed.package_sha256 -match '^[A-Fa-f0-9]{64}$') { $packageHash = ([string]$Observed.package_sha256).ToLowerInvariant() }
    $receiptHash = $null
    if ([string]$Observed.receipt_sha256 -match '^[A-Fa-f0-9]{64}$') { $receiptHash = ([string]$Observed.receipt_sha256).ToLowerInvariant() }
    $packagingRevision = $null
    if ([string]$Observed.packaging_revision -match '^(?:[A-Fa-f0-9]{40}|[A-Fa-f0-9]{64})$') { $packagingRevision = ([string]$Observed.packaging_revision).ToLowerInvariant() }

    $packageAndReceiptAvailable = $Observed.package_available -eq $true -and
        $Observed.receipt_available -eq $true -and $Observed.receipt_valid -eq $true
    return [pscustomobject][ordered]@{
        schema_version = 1
        os_version = ConvertTo-CnMonPreflightVersion $Observed.os_version
        os_build = $osBuild
        powershell_version = ConvertTo-CnMonPreflightVersion $Observed.powershell_version
        process_session_id = $processSessionId
        session_connection_state = $session.connection_state
        session_protocol = $session.protocol
        session_eligible = [bool]$session.eligible
        token_is_administrator = ($Observed.token_is_administrator -eq $true)
        token_has_administrators_sid = ($Observed.token_has_administrators_sid -eq $true)
        token_is_elevated = ($Observed.token_is_elevated -eq $true)
        token_elevation_query_available = ($Observed.token_elevation_query_available -eq $true)
        makeappx_present = ($Observed.makeappx_present -eq $true)
        makeappx_version = ConvertTo-CnMonPreflightVersion $Observed.makeappx_version
        signtool_present = ($Observed.signtool_present -eq $true)
        signtool_version = ConvertTo-CnMonPreflightVersion $Observed.signtool_version
        appcert_present = ($Observed.appcert_present -eq $true)
        appcert_version = ConvertTo-CnMonPreflightVersion $Observed.appcert_version
        appx_cmdlets_available = ($Observed.appx_cmdlets_available -eq $true)
        package_available = ($Observed.package_available -eq $true)
        receipt_available = ($Observed.receipt_available -eq $true)
        receipt_valid = ($Observed.receipt_valid -eq $true)
        package_sha256 = $packageHash
        receipt_sha256 = $receiptHash
        packaging_revision = $packagingRevision
        package_hash_matches_receipt = ($packageAndReceiptAvailable -and $Observed.package_hash_matches_receipt -eq $true)
        package_signer_matches_receipt = ($packageAndReceiptAvailable -and $Observed.package_signer_matches_receipt -eq $true)
        package_signature_trusted = ($packageAndReceiptAvailable -and $Observed.package_signature_trusted -eq $true)
        expected_registration_present = ($packageAndReceiptAvailable -and $Observed.expected_registration_present -eq $true)
        repair_ui = 'not_observed'
        alias_ui = 'not_observed'
    }
}

try {
    if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) { exit 1 }

    $observed = [ordered]@{
        os_version = '0.0.0.0'
        os_build = 0
        powershell_version = $PSVersionTable.PSVersion.ToString()
        process_session_id = 0
        connection_state_code = -1
        protocol_type = -1
        token_is_administrator = $false
        token_has_administrators_sid = $false
        token_is_elevated = $false
        token_elevation_query_available = $false
        makeappx_present = $false
        makeappx_version = '0.0.0.0'
        signtool_present = $false
        signtool_version = '0.0.0.0'
        appcert_present = $false
        appcert_version = '0.0.0.0'
        appx_cmdlets_available = $false
        package_available = $false
        receipt_available = $false
        receipt_valid = $false
        package_sha256 = $null
        receipt_sha256 = $null
        packaging_revision = $null
        package_hash_matches_receipt = $false
        package_signer_matches_receipt = $false
        package_signature_trusted = $false
        expected_registration_present = $false
    }

    $fallbackOsVersion = [Environment]::OSVersion.Version.ToString()
    $osDetails = Get-CnMonPreflightOsDetails -FallbackVersion $fallbackOsVersion
    $observed.os_version = $osDetails.version
    $observed.os_build = $osDetails.build
    try {
        $os = Get-CimInstance -ClassName Win32_OperatingSystem -ErrorAction Stop
        $osDetails = Get-CnMonPreflightOsDetails -FallbackVersion $fallbackOsVersion -CimVersion ([string]$os.Version) -CimBuild ([string]$os.BuildNumber)
        $observed.os_version = $osDetails.version
        $observed.os_build = $osDetails.build
    } catch { }

    try {
        $process = [Diagnostics.Process]::GetCurrentProcess()
        $observed.process_session_id = [int]$process.SessionId
        $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
        $principal = [Security.Principal.WindowsPrincipal]::new($identity)
        $observed.token_is_administrator = $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
        $observed.token_has_administrators_sid = Test-CnMonPreflightAdministratorsSid -Groups @($identity.Groups)

        if ($null -eq ('CnMonDesktopPreflightNative' -as [type])) {
            Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;

public static class CnMonDesktopPreflightNative
{
    [StructLayout(LayoutKind.Sequential)]
    private struct TOKEN_ELEVATION { public int TokenIsElevated; }

    [DllImport("Wtsapi32.dll", EntryPoint = "WTSQuerySessionInformationW", SetLastError = true, CharSet = CharSet.Unicode)]
    private static extern bool WTSQuerySessionInformation(IntPtr server, int sessionId, int infoClass, out IntPtr buffer, out int bytesReturned);
    [DllImport("Wtsapi32.dll")]
    private static extern void WTSFreeMemory(IntPtr buffer);
    [DllImport("kernel32.dll")]
    private static extern IntPtr GetCurrentProcess();
    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool CloseHandle(IntPtr handle);
    [DllImport("advapi32.dll", SetLastError = true)]
    private static extern bool OpenProcessToken(IntPtr process, uint access, out IntPtr token);
    [DllImport("advapi32.dll", SetLastError = true)]
    private static extern bool GetTokenInformation(IntPtr token, int informationClass, out TOKEN_ELEVATION information, int informationLength, out int returnLength);

    public static bool TryGetSessionValue(int sessionId, int infoClass, out int value)
    {
        value = -1;
        IntPtr buffer = IntPtr.Zero;
        int bytesReturned = 0;
        if (!WTSQuerySessionInformation(IntPtr.Zero, sessionId, infoClass, out buffer, out bytesReturned) || buffer == IntPtr.Zero)
            return false;
        try
        {
            if (infoClass == 16)
            {
                if (bytesReturned < 2) return false;
                value = Marshal.ReadInt16(buffer);
            }
            else
            {
                if (bytesReturned < 4) return false;
                value = Marshal.ReadInt32(buffer);
            }
            return true;
        }
        finally { WTSFreeMemory(buffer); }
    }

    public static bool TryGetTokenElevation(out bool elevated)
    {
        elevated = false;
        IntPtr token = IntPtr.Zero;
        if (!OpenProcessToken(GetCurrentProcess(), 0x0008, out token)) return false;
        try
        {
            TOKEN_ELEVATION information;
            int returnLength;
            if (!GetTokenInformation(token, 20, out information, Marshal.SizeOf(typeof(TOKEN_ELEVATION)), out returnLength)) return false;
            elevated = information.TokenIsElevated != 0;
            return true;
        }
        finally { CloseHandle(token); }
    }
}
'@ -ErrorAction Stop
        }

        $state = -1
        $protocol = -1
        $stateAvailable = [CnMonDesktopPreflightNative]::TryGetSessionValue($process.SessionId, 8, [ref]$state)
        $protocolAvailable = [CnMonDesktopPreflightNative]::TryGetSessionValue($process.SessionId, 16, [ref]$protocol)
        if ($stateAvailable) { $observed.connection_state_code = $state }
        if ($protocolAvailable) { $observed.protocol_type = $protocol }
        $elevated = $false
        $elevationAvailable = [CnMonDesktopPreflightNative]::TryGetTokenElevation([ref]$elevated)
        $observed.token_is_elevated = $elevated
        $observed.token_elevation_query_available = $elevationAvailable
    } catch { }

    $toolPaths = @{
        makeappx = Join-Path $SdkBinDirectory 'makeappx.exe'
        signtool = Join-Path $SdkBinDirectory 'signtool.exe'
        appcert = 'C:\Program Files (x86)\Windows Kits\10\App Certification Kit\appcert.exe'
    }
    foreach ($name in @('makeappx', 'signtool', 'appcert')) {
        try {
            $path = $toolPaths[$name]
            if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { continue }
            $observed["${name}_present"] = $true
            $fileVersion = [Diagnostics.FileVersionInfo]::GetVersionInfo($path)
            $observed["${name}_version"] = '{0}.{1}.{2}.{3}' -f $fileVersion.FileMajorPart, $fileVersion.FileMinorPart, $fileVersion.FileBuildPart, $fileVersion.FilePrivatePart
        } catch { }
    }

    try {
        $availableAppxCmdlets = @(
            Get-Command -Name Get-AppxPackage, Add-AppxPackage, Remove-AppxPackage -CommandType Cmdlet -ErrorAction SilentlyContinue |
                ForEach-Object Name
        )
        $observed.appx_cmdlets_available = Test-CnMonPreflightAppxCmdlets -CommandNames $availableAppxCmdlets
    } catch { }

    $packageExists = $false
    if (-not [string]::IsNullOrWhiteSpace($PackagePath)) {
        try {
            $packageExists = Test-Path -LiteralPath $PackagePath -PathType Leaf
            if ($packageExists) { $observed.package_sha256 = (Get-FileHash -LiteralPath $PackagePath -Algorithm SHA256 -ErrorAction Stop).Hash }
        } catch { $packageExists = $false }
    }
    $observed.package_available = $packageExists

    $receipt = $null
    $receiptExists = $false
    if (-not [string]::IsNullOrWhiteSpace($ReceiptPath)) {
        try {
            $receiptExists = Test-Path -LiteralPath $ReceiptPath -PathType Leaf
            if ($receiptExists) {
                $observed.receipt_sha256 = (Get-FileHash -LiteralPath $ReceiptPath -Algorithm SHA256 -ErrorAction Stop).Hash
                $receipt = Get-Content -LiteralPath $ReceiptPath -Raw -ErrorAction Stop | ConvertFrom-Json -ErrorAction Stop
                $observed.receipt_valid = $null -ne $receipt -and $receipt -is [pscustomobject]
            }
        } catch { $receipt = $null; $observed.receipt_valid = $false }
    }
    $observed.receipt_available = $receiptExists
    if ($observed.receipt_valid -and $receipt.packaging_revision -is [string]) {
        $observed.packaging_revision = $receipt.packaging_revision
    }

    if ($packageExists -and $observed.receipt_valid) {
        try {
            $receiptPackageHash = [string]$receipt.package_sha256
            $actualPackageHash = ([string]$observed.package_sha256).ToLowerInvariant()
            $observed.package_hash_matches_receipt = $receiptPackageHash -match '^[A-Fa-f0-9]{64}$' -and $actualPackageHash -ceq $receiptPackageHash.ToLowerInvariant()
        } catch { }

        try {
            $signature = Get-AuthenticodeSignature -LiteralPath $PackagePath -ErrorAction Stop
            $signer = $signature.SignerCertificate
            $thumbprint = [string]$receipt.signing.certificate_thumbprint
            $observed.package_signature_trusted = $signature.Status -eq [Management.Automation.SignatureStatus]::Valid
            $observed.package_signer_matches_receipt = $null -ne $signer -and
                -not [string]::IsNullOrWhiteSpace($thumbprint) -and
                $signer.Thumbprint -ceq $thumbprint -and
                $signer.Subject -ceq [string]$receipt.publisher
        } catch { }
    }

    if ($packageExists -and $observed.receipt_valid -and $observed.appx_cmdlets_available) {
        try {
            $matchingPackages = @(
                Get-AppxPackage -Name ([string]$receipt.name) -ErrorAction Stop |
                    Where-Object {
                        $_.Name -ceq [string]$receipt.name -and
                        $_.Publisher -ceq [string]$receipt.publisher -and
                        $_.PackageFamilyName -ceq [string]$receipt.expected_pfn -and
                        [string]$_.Version -ceq [string]$receipt.package_version
                    }
            )
            $observed.expected_registration_present = $matchingPackages.Count -gt 0
        } catch { }
    }

    $report = New-CnMonPreflightReport -Observed $observed
    $json = ConvertTo-Json -InputObject $report -Depth 3
    $null = New-Item -ItemType Directory -Force -Path $ArtifactDirectory
    [IO.File]::WriteAllText((Join-Path $ArtifactDirectory 'msix-desktop-preflight.json'), $json, [Text.UTF8Encoding]::new($false))
}
catch {
    exit 1
}
