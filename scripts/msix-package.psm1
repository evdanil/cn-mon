$ErrorActionPreference = 'Stop'

$script:ReleaseEntries = @(
    'cn-mon.exe',
    'README.md',
    'LICENSE',
    'THIRD_PARTY_NOTICES.md'
)
$script:MakeAppxFootprint = @(
    '[Content_Types].xml',
    'AppxBlockMap.xml'
)

function Get-CnMonMsixDerivedPFN {
    param([string]$Name, [string]$Publisher)

    if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
        return $null
    }
    if ($null -eq ('CnMonMsixNativeIdentity' -as [type])) {
        Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
using System.Text;

[StructLayout(LayoutKind.Sequential, CharSet = CharSet.Unicode)]
public struct CnMonMsixPackageId
{
    public UInt32 Reserved;
    public UInt32 ProcessorArchitecture;
    public UInt64 Version;
    public IntPtr Name;
    public IntPtr Publisher;
    public IntPtr ResourceId;
    public IntPtr PublisherId;
}

public static class CnMonMsixNativeIdentity
{
    [DllImport("kernel32.dll", CharSet = CharSet.Unicode)]
    public static extern Int32 PackageFamilyNameFromId(
        ref CnMonMsixPackageId packageId,
        ref UInt32 packageFamilyNameLength,
        StringBuilder packageFamilyName);
}
'@ -ErrorAction Stop
    }

    $packageId = [CnMonMsixPackageId]::new()
    $namePointer = [IntPtr]::Zero
    $publisherPointer = [IntPtr]::Zero
    try {
        $namePointer = [Runtime.InteropServices.Marshal]::StringToHGlobalUni($Name)
        $publisherPointer = [Runtime.InteropServices.Marshal]::StringToHGlobalUni($Publisher)
        $packageId.ProcessorArchitecture = 9
        $packageId.Name = $namePointer
        $packageId.Publisher = $publisherPointer
        [uint32]$length = 0
        $result = [CnMonMsixNativeIdentity]::PackageFamilyNameFromId([ref]$packageId, [ref]$length, $null)
        if ($result -ne 122 -or $length -lt 2) {
            throw "Windows PFN API first call failed with code $result and length $length"
        }
        $builder = [Text.StringBuilder]::new([int]$length)
        $result = [CnMonMsixNativeIdentity]::PackageFamilyNameFromId([ref]$packageId, [ref]$length, $builder)
        if ($result -ne 0) {
            throw "Windows PFN API second call failed with code $result and length $length"
        }
        return $builder.ToString()
    }
    finally {
        if ($namePointer -ne [IntPtr]::Zero) {
            [Runtime.InteropServices.Marshal]::FreeHGlobal($namePointer)
        }
        if ($publisherPointer -ne [IntPtr]::Zero) {
            [Runtime.InteropServices.Marshal]::FreeHGlobal($publisherPointer)
        }
    }
}

function Read-CnMonMsixIdentity {
    $name = [Environment]::GetEnvironmentVariable('CNMON_MSIX_IDENTITY_NAME')
    $publisher = [Environment]::GetEnvironmentVariable('CNMON_MSIX_IDENTITY_PUBLISHER')
    $displayName = [Environment]::GetEnvironmentVariable('CNMON_MSIX_PUBLISHER_DISPLAY_NAME')
    $pfn = [Environment]::GetEnvironmentVariable('CNMON_MSIX_EXPECTED_PFN')

    foreach ($required in @(
        @{ Name = 'CNMON_MSIX_IDENTITY_NAME'; Value = $name },
        @{ Name = 'CNMON_MSIX_IDENTITY_PUBLISHER'; Value = $publisher },
        @{ Name = 'CNMON_MSIX_PUBLISHER_DISPLAY_NAME'; Value = $displayName },
        @{ Name = 'CNMON_MSIX_EXPECTED_PFN'; Value = $pfn }
    )) {
        if ([string]::IsNullOrWhiteSpace($required.Value)) {
            throw "required environment variable $($required.Name) is missing or blank"
        }
    }

    if ($name.Length -gt 50 -or $name -notmatch '^[A-Za-z0-9][A-Za-z0-9.-]*$') {
        throw 'identity Name is invalid'
    }
    if ($displayName.Length -gt 256 -or $displayName -match '[\x00-\x1f]') {
        throw 'PublisherDisplayName is invalid'
    }
    try {
        $null = [Security.Cryptography.X509Certificates.X500DistinguishedName]::new($publisher)
    }
    catch {
        throw 'identity Publisher is not a valid distinguished name'
    }
    $pfnPrefix = $name + '_'
    if (-not $pfn.StartsWith($pfnPrefix, [StringComparison]::Ordinal) -or
        $pfn.Substring($pfnPrefix.Length) -notmatch '^[a-z0-9]{13}$') {
        throw 'expected PFN is inconsistent with identity Name'
    }
    $derivedPfn = Get-CnMonMsixDerivedPFN -Name $name -Publisher $publisher
    if ($null -ne $derivedPfn -and $pfn -cne $derivedPfn) {
        throw 'expected PFN does not match identity Name and Publisher'
    }

    return [pscustomobject]@{
        Name = $name
        Publisher = $publisher
        PublisherDisplayName = $displayName
        ExpectedPFN = $pfn
    }
}

function Invoke-CnMonMsixTool {
    param(
        [Parameter(Mandatory = $true)][string]$ExecutablePath,
        [Parameter(Mandatory = $true)][string[]]$Arguments,
        [Parameter(Mandatory = $true)][string]$LogPath
    )

    $startInfo = [Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $ExecutablePath
    $startInfo.UseShellExecute = $false
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    foreach ($argument in $Arguments) {
        $startInfo.ArgumentList.Add($argument)
    }

    $process = [Diagnostics.Process]::Start($startInfo)
    try {
        $stdoutTask = $process.StandardOutput.ReadToEndAsync()
        $stderrTask = $process.StandardError.ReadToEndAsync()
        $process.WaitForExit()
        [Threading.Tasks.Task]::WaitAll([Threading.Tasks.Task[]]@($stdoutTask, $stderrTask))
        $logText = "stdout:`n$($stdoutTask.Result)`nstderr:`n$($stderrTask.Result)"
        [IO.File]::WriteAllText($LogPath, $logText, [Text.UTF8Encoding]::new($false))
        return $process.ExitCode
    }
    finally {
        $process.Dispose()
    }
}

function Get-CnMonMsixSha256 {
    param([string]$Path)

    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}

function Test-CnMonMsixHash {
    param([string]$Hash)

    return $Hash -match '^[0-9a-fA-F]{64}$'
}

function Expand-CnMonMsixRelease {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)][string]$ArchivePath,
        [Parameter(Mandatory = $true)][string]$Destination,
        [Parameter(Mandatory = $true)][object]$Release
    )

    if (-not (Test-Path -LiteralPath $ArchivePath -PathType Leaf)) {
        throw 'release archive does not exist'
    }
    if (-not $Release.archive_sha256 -or -not (Test-CnMonMsixHash ([string]$Release.archive_sha256))) {
        throw 'release archive SHA-256 is invalid'
    }
    if ([string]$Release.cli_version -cne '0.3.0' -or
        [string]$Release.cli_revision -cne 'bd1ac548c22c0c49a54e2ce964a2f0a4da48a1ed' -or
        [string]$Release.archive_root -cne 'cn-mon_0.3.0_windows_amd64' -or
        [string]$Release.archive_name -cne 'cn-mon_0.3.0_windows_amd64.zip' -or
        [string]$Release.binary_sha256 -cne '8312916ae18998c6cddecbe31d2a1dd9833f047a5e51da3feccc5ca45b6b234d') {
        throw 'release metadata does not identify the approved public Windows release'
    }

    $archiveHash = Get-CnMonMsixSha256 -Path $ArchivePath
    if ($archiveHash -cne ([string]$Release.archive_sha256).ToLowerInvariant()) {
        throw 'archive SHA-256 mismatch'
    }

    Add-Type -AssemblyName System.IO.Compression.FileSystem
    $archive = $null
    $createdDestination = $false
    try {
        $archive = [IO.Compression.ZipFile]::OpenRead([IO.Path]::GetFullPath($ArchivePath))
        $seen = [Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
        $validatedEntries = [Collections.Generic.List[IO.Compression.ZipArchiveEntry]]::new()
        foreach ($entry in $archive.Entries) {
            $entryName = $entry.FullName
            if ([string]::IsNullOrEmpty($entryName) -or $entryName.Contains('\') -or
                $entryName.StartsWith('/') -or $entryName.Contains(':')) {
                throw 'archive contains an unsafe path'
            }
            $components = $entryName.Split('/')
            if ($components.Count -ne 2 -or $components[0] -cne [string]$Release.archive_root -or
                [string]::IsNullOrEmpty($components[1]) -or $components[1] -in @('.', '..')) {
                throw 'archive contains an unexpected path'
            }
            if (-not $seen.Add($entryName)) {
                throw 'archive contains a duplicate path'
            }
            if ($components[1] -cnotin $script:ReleaseEntries) {
                throw 'archive contains an unexpected file'
            }
            if ($entryName.EndsWith('/')) {
                throw 'archive contains an unexpected directory entry'
            }
            $validatedEntries.Add($entry)
        }
        if ($validatedEntries.Count -ne $script:ReleaseEntries.Count) {
            throw 'archive is missing one or more release files'
        }
        foreach ($expectedName in $script:ReleaseEntries) {
            if (-not $seen.Contains(([string]$Release.archive_root) + '/' + $expectedName)) {
                throw "archive is missing release file $expectedName"
            }
        }

        if (Test-Path -LiteralPath $Destination) {
            throw 'release destination already exists'
        }
        [IO.Directory]::CreateDirectory([IO.Path]::GetFullPath($Destination)) | Out-Null
        $createdDestination = $true
        foreach ($entry in $validatedEntries) {
            $relativePath = $entry.FullName.Substring(([string]$Release.archive_root).Length + 1)
            $targetPath = Join-Path ([IO.Path]::GetFullPath($Destination)) $relativePath
            $sourceStream = $entry.Open()
            try {
                $targetStream = [IO.File]::Open($targetPath, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
                try {
                    $sourceStream.CopyTo($targetStream)
                }
                finally {
                    $targetStream.Dispose()
                }
            }
            finally {
                $sourceStream.Dispose()
            }
        }

        $files = @{}
        foreach ($name in $script:ReleaseEntries) {
            $files[$name] = Get-CnMonMsixSha256 -Path (Join-Path ([IO.Path]::GetFullPath($Destination)) $name)
        }
        if ($files['cn-mon.exe'] -cne ([string]$Release.binary_sha256).ToLowerInvariant()) {
            throw 'release executable SHA-256 mismatch'
        }
        return $files
    }
    catch {
        if ($createdDestination -and (Test-Path -LiteralPath $Destination)) {
            Remove-Item -LiteralPath $Destination -Recurse -Force -ErrorAction SilentlyContinue
        }
        throw
    }
    finally {
        if ($null -ne $archive) {
            $archive.Dispose()
        }
    }
}

function Assert-CnMonMsixPayload {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)][string]$Root,
        [Parameter(Mandatory = $true)][Collections.IDictionary]$ExpectedFiles
    )

    if (-not (Test-Path -LiteralPath $Root -PathType Container)) {
        throw 'payload root does not exist'
    }
    $rootPath = [IO.Path]::GetFullPath($Root)
    $expected = @{}
    foreach ($key in $ExpectedFiles.Keys) {
        $relative = ([string]$key).Replace('\', '/')
        if ([string]::IsNullOrWhiteSpace($relative) -or $relative.StartsWith('/') -or
            $relative.Split('/') -contains '..' -or -not (Test-CnMonMsixHash ([string]$ExpectedFiles[$key]))) {
            throw 'expected payload map contains an invalid path or SHA-256'
        }
        $expected[$relative] = ([string]$ExpectedFiles[$key]).ToLowerInvariant()
    }

    $actual = @{}
    foreach ($file in Get-ChildItem -LiteralPath $rootPath -File -Recurse -Force) {
        if ($file.Attributes -band [IO.FileAttributes]::ReparsePoint) {
            throw 'payload contains a reparse point'
        }
        $relative = [IO.Path]::GetRelativePath($rootPath, $file.FullName).Replace('\', '/')
        $actual[$relative] = Get-CnMonMsixSha256 -Path $file.FullName
    }

    foreach ($relative in $expected.Keys) {
        if (-not $actual.ContainsKey($relative)) {
            throw "payload file missing: $relative"
        }
        if ($actual[$relative] -cne $expected[$relative]) {
            throw "payload hash mismatch: $relative"
        }
    }
    foreach ($relative in $actual.Keys) {
        if (-not $expected.ContainsKey($relative) -and $relative -notin $script:MakeAppxFootprint) {
            throw "payload unexpected file: $relative"
        }
    }
    return $null
}

function ConvertTo-CnMonMsixVersion {
    param([string]$Value, [string]$FieldName)

    if ($Value -notmatch '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$') {
        throw "$FieldName version is malformed"
    }
    $parts = @($Matches[1], $Matches[2], $Matches[3], $Matches[4])
    foreach ($part in $parts) {
        $number = 0
        if (-not [int]::TryParse($part, [ref]$number) -or $number -gt 65535) {
            throw "$FieldName version component is out of range"
        }
    }
    if ([int]$parts[0] -eq 0) {
        throw "$FieldName version major component must be nonzero"
    }
    if ([int]$parts[3] -ne 0) {
        throw "$FieldName version fourth component must be zero"
    }
    return [Version]::new([int]$parts[0], [int]$parts[1], [int]$parts[2], [int]$parts[3])
}

function Get-CnMonMsixManifestAssets {
    param([xml]$Document, [string]$ManifestDirectory)

    $paths = [Collections.Generic.List[string]]::new()
    $selectors = @(
        '/*[local-name()="Package"]/*[local-name()="Properties"]/*[local-name()="Logo"]',
        '/*[local-name()="Package"]/*[local-name()="Applications"]/*[local-name()="Application"]/*[local-name()="VisualElements"]/@Square44x44Logo',
        '/*[local-name()="Package"]/*[local-name()="Applications"]/*[local-name()="Application"]/*[local-name()="VisualElements"]/@Square150x150Logo'
    )
    foreach ($selector in $selectors) {
        $node = $Document.SelectSingleNode($selector)
        if ($null -eq $node) {
            throw 'manifest is missing a referenced asset path'
        }
        $rawPath = if ($node -is [Xml.XmlAttribute]) { $node.Value } else { $node.InnerText }
        if ([string]::IsNullOrWhiteSpace($rawPath)) {
            throw 'manifest is missing a referenced asset path'
        }
        $assetPath = ([string]$rawPath).Replace('\', '/')
        if ($assetPath.StartsWith('/') -or $assetPath.Contains(':') -or $assetPath.Split('/') -contains '..') {
            throw 'manifest contains an unsafe asset path'
        }
        $paths.Add($assetPath)
    }
    if ($paths.Count -ne 3 -or @($paths | Select-Object -Unique).Count -ne 3) {
        throw 'manifest must reference the three distinct package logo assets'
    }
    foreach ($assetPath in $paths) {
        $localPath = Join-Path $ManifestDirectory ($assetPath.Replace('/', [IO.Path]::DirectorySeparatorChar))
        if (-not (Test-Path -LiteralPath $localPath -PathType Leaf)) {
            throw "manifest referenced asset is missing: $assetPath"
        }
    }
    return $paths
}

function Read-CnMonMsixManifest {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)][string]$ManifestPath,
        [Parameter(Mandatory = $true)][object]$Release,
        [Parameter(Mandatory = $true)][object]$Identity,
        [Parameter(Mandatory = $true)][string]$PackageVersion,
        [Parameter(Mandatory = $true)][string]$MaxVersionTested
    )

    if (-not (Test-Path -LiteralPath $ManifestPath -PathType Leaf)) {
        throw 'manifest does not exist'
    }
    foreach ($field in @('Name', 'Publisher', 'PublisherDisplayName', 'ExpectedPFN')) {
        if ($null -eq $Identity.PSObject.Properties[$field] -or
            [string]::IsNullOrWhiteSpace([string]$Identity.$field)) {
            throw 'validated identity object is incomplete'
        }
    }
    if ([string]$Identity.Name -notmatch '^[A-Za-z0-9][A-Za-z0-9.-]*$' -or
        ([string]$Identity.Name).Length -gt 50 -or
        -not ([string]$Identity.ExpectedPFN).StartsWith(([string]$Identity.Name) + '_', [StringComparison]::Ordinal) -or
        ([string]$Identity.ExpectedPFN).Substring(([string]$Identity.Name).Length + 1) -notmatch '^[a-z0-9]{13}$') {
        throw 'validated identity object is invalid'
    }
    $identity = $Identity
    $packageVersionValue = ConvertTo-CnMonMsixVersion -Value $PackageVersion -FieldName 'package'
    $maxVersionValue = ConvertTo-CnMonMsixVersion -Value $MaxVersionTested -FieldName 'MaxVersionTested'
    if ($maxVersionValue -lt [Version]'10.0.17763.0') {
        throw 'MaxVersionTested version is below the supported minimum'
    }
    if ([string]$Release.package_version -cne $PackageVersion) {
        throw 'package version does not match the public release input'
    }

    $settings = [Xml.XmlReaderSettings]::new()
    $settings.DtdProcessing = [Xml.DtdProcessing]::Prohibit
    $settings.XmlResolver = $null
    $document = [Xml.XmlDocument]::new()
    $document.PreserveWhitespace = $true
    $document.XmlResolver = $null
    $reader = [Xml.XmlReader]::Create([IO.Path]::GetFullPath($ManifestPath), $settings)
    try {
        $document.Load($reader)
    }
    finally {
        $reader.Dispose()
    }

    $identityNode = $document.SelectSingleNode('/*[local-name()="Package"]/*[local-name()="Identity"]')
    if ($null -eq $identityNode) {
        throw 'manifest is missing package Identity'
    }
    if ($identityNode.GetAttribute('Name') -cne '__CNMON_MSIX_IDENTITY_NAME__') {
        throw 'manifest identity Name placeholder is invalid'
    }
    if ($identityNode.GetAttribute('Publisher') -cne '__CNMON_MSIX_IDENTITY_PUBLISHER__') {
        throw 'manifest identity Publisher placeholder is invalid'
    }
    if ($identityNode.GetAttribute('ProcessorArchitecture') -cne 'x64') {
        throw 'manifest identity architecture must be x64'
    }
    $publisherNode = $document.SelectSingleNode('/*[local-name()="Package"]/*[local-name()="Properties"]/*[local-name()="PublisherDisplayName"]')
    if ($null -eq $publisherNode -or $publisherNode.InnerText -cne '__CNMON_MSIX_PUBLISHER_DISPLAY_NAME__') {
        throw 'manifest PublisherDisplayName placeholder is invalid'
    }

    $applications = $document.SelectNodes('/*[local-name()="Package"]/*[local-name()="Applications"]/*[local-name()="Application"]')
    if ($applications.Count -ne 1 -or $applications[0].GetAttribute('Id') -cne 'CnMon' -or
        $applications[0].GetAttribute('Executable') -cne 'cn-mon.exe' -or
        $applications[0].GetAttribute('EntryPoint') -cne 'Windows.FullTrustApplication') {
        throw 'manifest application contract is invalid'
    }
    $visuals = $applications[0].SelectSingleNode('*[local-name()="VisualElements"]')
    if ($null -eq $visuals -or $visuals.GetAttribute('AppListEntry') -cne 'default') {
        throw 'manifest must declare the default AppListEntry'
    }
    $aliasNodes = $applications[0].SelectNodes('.//*[local-name()="Extension"][@Category="windows.appExecutionAlias"]/*[local-name()="AppExecutionAlias"]/*[local-name()="ExecutionAlias"]')
    if ($aliasNodes.Count -ne 1 -or $aliasNodes[0].GetAttribute('Alias') -cne 'cn-mon.exe') {
        throw 'manifest execution alias is invalid'
    }
    $capabilities = $document.SelectNodes('/*[local-name()="Package"]/*[local-name()="Capabilities"]/*')
    if ($capabilities.Count -ne 1 -or $capabilities[0].LocalName -cne 'Capability' -or
        $capabilities[0].GetAttribute('Name') -cne 'runFullTrust') {
        throw 'manifest must request only runFullTrust'
    }
    $target = $document.SelectSingleNode('/*[local-name()="Package"]/*[local-name()="Dependencies"]/*[local-name()="TargetDeviceFamily"]')
    if ($null -eq $target -or $target.GetAttribute('Name') -cne 'Windows.Desktop' -or
        $target.GetAttribute('MinVersion') -cne '10.0.17763.0') {
        throw 'manifest target device family is invalid'
    }

    $null = Get-CnMonMsixManifestAssets -Document $document -ManifestDirectory (Split-Path -Parent ([IO.Path]::GetFullPath($ManifestPath)))
    $identityNode.SetAttribute('Name', $identity.Name)
    $identityNode.SetAttribute('Publisher', $identity.Publisher)
    $identityNode.SetAttribute('Version', $packageVersionValue.ToString())
    $publisherNode.InnerText = $identity.PublisherDisplayName
    $target.SetAttribute('MaxVersionTested', $maxVersionValue.ToString())

    return [pscustomobject]@{
        Xml = $document
        Identity = $identity
        PackageVersion = $packageVersionValue.ToString()
        MinVersion = '10.0.17763.0'
        MaxVersionTested = $maxVersionValue.ToString()
    }
}

Export-ModuleMember -Function Read-CnMonMsixIdentity, Expand-CnMonMsixRelease, Read-CnMonMsixManifest, Assert-CnMonMsixPayload
