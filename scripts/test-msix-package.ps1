[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$ReleaseArchive,

    [string]$SdkBinDirectory,

    [string]$TestDirectory,

    [switch]$ModuleOnly
)

$ErrorActionPreference = 'Stop'
$repositoryRoot = Split-Path -Parent $PSScriptRoot
$modulePath = Join-Path $PSScriptRoot 'msix-package.psm1'
Import-Module $modulePath -Force -ErrorAction Stop

$identityEnvironmentNames = @(
    'CNMON_MSIX_IDENTITY_NAME',
    'CNMON_MSIX_IDENTITY_PUBLISHER',
    'CNMON_MSIX_PUBLISHER_DISPLAY_NAME',
    'CNMON_MSIX_EXPECTED_PFN'
)
$originalIdentityEnvironment = @{}
foreach ($variableName in $identityEnvironmentNames) {
    $originalIdentityEnvironment[$variableName] = [Environment]::GetEnvironmentVariable($variableName)
}
$identityModule = Get-Module -Name 'msix-package'

$releasePath = Join-Path $repositoryRoot 'packaging/windows/release.json'
$release = Get-Content -LiteralPath $releasePath -Raw | ConvertFrom-Json
    $releaseText = Get-Content -LiteralPath $releasePath -Raw
$expectedArchiveHash = '3e5ef313b4034202430256f14ac31bfdfc9ae4e7d2d23a9f4c74602cd6c74388'
$expectedBinaryHash = '8312916ae18998c6cddecbe31d2a1dd9833f047a5e51da3feccc5ca45b6b234d'
if ($release.archive_sha256 -cne $expectedArchiveHash) {
    throw 'release.json archive hash differs from the approved public artifact'
}
if ($release.binary_sha256 -cne $expectedBinaryHash) {
    throw 'release.json executable hash differs from the approved public artifact'
}
    if ($releaseText -match '(?i)identity|publisher|pfn|Example\.CnMon|CN=Example') {
        throw 'public release metadata contains package identity data'
    }

$sidecarPath = Join-Path $repositoryRoot 'packaging/windows/cn-mon.exe.manifest'
if (-not (Test-Path -LiteralPath $sidecarPath -PathType Leaf)) {
    throw 'neutral executable DPI sidecar is missing'
}
$sidecarDocument = [Xml.XmlDocument]::new()
$sidecarDocument.XmlResolver = $null
$sidecarDocument.Load($sidecarPath)
$sidecarRoot = $sidecarDocument.DocumentElement
$sidecarIdentity = $sidecarDocument.SelectSingleNode('/*[local-name()="assembly"]/*[local-name()="assemblyIdentity"]')
$sidecarApplication = $sidecarDocument.SelectSingleNode('/*[local-name()="assembly"]/*[local-name()="application"]')
$dpiAwareness = $sidecarDocument.SelectSingleNode('/*[local-name()="assembly"]/*[local-name()="application"]/*[local-name()="windowsSettings"]/*[local-name()="dpiAwareness"]')
if ($sidecarRoot.LocalName -cne 'assembly' -or $sidecarRoot.NamespaceURI -cne 'urn:schemas-microsoft-com:asm.v1' -or
    $sidecarRoot.GetAttribute('manifestVersion') -cne '1.0' -or $sidecarRoot.ChildNodes.Count -ne 2 -or
    $null -eq $sidecarIdentity -or $sidecarIdentity.GetAttribute('type') -cne 'win32' -or
    $sidecarIdentity.GetAttribute('name') -cne 'CnMon.CLI' -or $sidecarIdentity.GetAttribute('version') -cne '0.3.0.0' -or
    $sidecarIdentity.GetAttribute('processorArchitecture') -cne 'amd64' -or
    $null -eq $sidecarApplication -or $sidecarApplication.NamespaceURI -cne 'urn:schemas-microsoft-com:asm.v3' -or
    $sidecarApplication.ChildNodes.Count -ne 1 -or $null -eq $dpiAwareness -or
    $dpiAwareness.NamespaceURI -cne 'http://schemas.microsoft.com/SMI/2016/WindowsSettings' -or
    $dpiAwareness.InnerText -cne 'PerMonitorV2') {
    throw 'executable DPI sidecar does not match the approved neutral Win32 manifest'
}
$testCount = 1
Write-Host 'PASS neutral Win32 DPI sidecar contract'

function Assert-Rejected {
    param(
        [Parameter(Mandatory = $true)][scriptblock]$Action,
        [Parameter(Mandatory = $true)][string]$MessagePattern,
        [Parameter(Mandatory = $true)][string]$Name
    )
    try {
        & $Action
    }
    catch {
        if ($_.Exception.Message -notmatch $MessagePattern) {
            throw "$Name failed for the wrong reason"
        }
        $script:testCount++
        Write-Host "PASS $Name"
        return
    }
    throw "$Name was accepted"
}

$testParent = if ([string]::IsNullOrWhiteSpace($TestDirectory)) { [IO.Path]::GetTempPath() } else { [IO.Path]::GetFullPath($TestDirectory) }
if (-not (Test-Path -LiteralPath $testParent -PathType Container)) {
    New-Item -ItemType Directory -Path $testParent | Out-Null
}
$testRoot = Join-Path $testParent ("cn-mon msix tests 'literal paths-" + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $testRoot | Out-Null
try {
    $env:CNMON_MSIX_IDENTITY_NAME = 'Example.CnMon'
    $env:CNMON_MSIX_IDENTITY_PUBLISHER = 'CN=Example'
    $env:CNMON_MSIX_PUBLISHER_DISPLAY_NAME = 'Example Publisher'
    $env:CNMON_MSIX_EXPECTED_PFN = 'Example.CnMon_abcdefghijklm'
    if ([Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT) {
        $env:CNMON_MSIX_EXPECTED_PFN = & $identityModule {
            Get-CnMonMsixDerivedPFN -Name 'Example.CnMon' -Publisher 'CN=Example'
        }
    }

    $identity = Read-CnMonMsixIdentity
    if ($identity.Name -cne 'Example.CnMon' -or $identity.Publisher -cne 'CN=Example') {
        throw 'identity reader returned unexpected synthetic values'
    }
    $testCount++
    Write-Host 'PASS shared identity reader validates synthetic inputs'

    foreach ($variableName in $identityEnvironmentNames) {
        $savedValue = [Environment]::GetEnvironmentVariable($variableName)
        Remove-Item -LiteralPath ('Env:' + $variableName) -ErrorAction SilentlyContinue
        Assert-Rejected -Name "missing $variableName is rejected" -MessagePattern 'required environment variable' -Action {
            Read-CnMonMsixIdentity
        }
        [Environment]::SetEnvironmentVariable($variableName, '   ')
        Assert-Rejected -Name "blank $variableName is rejected" -MessagePattern 'required environment variable' -Action {
            Read-CnMonMsixIdentity
        }
        if ($null -eq $savedValue) {
            Remove-Item -LiteralPath ('Env:' + $variableName) -ErrorAction SilentlyContinue
        } else {
            [Environment]::SetEnvironmentVariable($variableName, $savedValue)
        }
    }

    $privateNameMarker = 'SYNTHETIC-PRIVATE-NAME-MARKER'
    $env:CNMON_MSIX_IDENTITY_NAME = $privateNameMarker + ' !'
    try { Read-CnMonMsixIdentity | Out-Null; throw 'malformed synthetic Name was accepted' }
    catch {
        if ($_.Exception.Message -notmatch 'identity Name is invalid') { throw }
        if ($_.Exception.Message.Contains($privateNameMarker)) { throw 'identity Name value leaked in its diagnostic' }
    }
    $testCount++
    Write-Host 'PASS malformed identity Name is rejected without echo'
    $env:CNMON_MSIX_IDENTITY_NAME = 'Example.CnMon'

    $privatePublisherMarker = 'SYNTHETIC-PRIVATE-PUBLISHER-MARKER'
    $env:CNMON_MSIX_IDENTITY_PUBLISHER = $privatePublisherMarker + ' not a DN'
    try { Read-CnMonMsixIdentity | Out-Null; throw 'malformed synthetic Publisher was accepted' }
    catch {
        if ($_.Exception.Message -notmatch 'identity Publisher is not a valid distinguished name') { throw }
        if ($_.Exception.Message.Contains($privatePublisherMarker)) { throw 'identity Publisher value leaked in its diagnostic' }
    }
    $testCount++
    Write-Host 'PASS malformed identity Publisher is rejected without echo'
    $env:CNMON_MSIX_IDENTITY_PUBLISHER = 'CN=Example'

    $toolLogPath = Join-Path $testRoot 'private-tool-output.log'
    $powershellPath = (Get-Command pwsh -ErrorAction Stop).Source
    $toolMarker = 'SYNTHETIC-PRIVATE-TOOL-OUTPUT-MARKER'
    $toolArguments = @(
        '-NoProfile', '-Command',
        "[Console]::Out.WriteLine('$toolMarker stdout'); [Console]::Error.WriteLine('$toolMarker stderr'); exit 47"
    )
    $toolOutput = @(& $identityModule {
        param([string]$Path, [string[]]$ToolArguments, [string]$LogPath)
        Invoke-CnMonMsixTool -ExecutablePath $Path -Arguments $ToolArguments -LogPath $LogPath
    } $powershellPath $toolArguments $toolLogPath)
    if ($toolOutput.Count -ne 1 -or [int]$toolOutput[0] -ne 47 -or
        ($toolOutput -join '') -match [regex]::Escape($toolMarker)) {
        throw 'external tool output escaped its private log'
    }
    $toolLog = [IO.File]::ReadAllText($toolLogPath)
    if ($toolLog -notmatch [regex]::Escape($toolMarker + ' stdout') -or
        $toolLog -notmatch [regex]::Escape($toolMarker + ' stderr')) {
        throw 'private tool log did not capture both output streams'
    }
    $testCount++
    Write-Host 'PASS tool stdout and stderr remain in the private log'

    if (-not (Test-Path -LiteralPath $ReleaseArchive -PathType Leaf)) {
        throw 'test release archive does not exist'
    }
    $archiveUnderTest = Join-Path $testRoot "published archive 'release.zip"
    [IO.File]::Copy([IO.Path]::GetFullPath($ReleaseArchive), $archiveUnderTest)

    $missingArchive = Join-Path $testRoot 'missing.zip'
    $missingDestination = Join-Path $testRoot 'missing destination'
    Assert-Rejected -Name 'missing archive is rejected' -MessagePattern 'archive.*(not found|does not exist)' -Action {
        Expand-CnMonMsixRelease -ArchivePath $missingArchive -Destination $missingDestination -Release $release
    }
    if (Test-Path -LiteralPath $missingDestination) { throw 'missing archive created its destination' }

    $corruptArchive = Join-Path $testRoot 'one-byte-corrupt.zip'
    [IO.File]::Copy($archiveUnderTest, $corruptArchive)
    $bytes = [IO.File]::ReadAllBytes($corruptArchive)
    $bytes[128] = $bytes[128] -bxor 1
    [IO.File]::WriteAllBytes($corruptArchive, $bytes)
    Assert-Rejected -Name 'corrupted archive is rejected' -MessagePattern 'archive SHA-256 mismatch' -Action {
        Expand-CnMonMsixRelease -ArchivePath $corruptArchive -Destination (Join-Path $testRoot 'corrupt destination') -Release $release
    }

    $literalDestination = Join-Path $testRoot "release payload 'quoted path"
    $expected = Expand-CnMonMsixRelease -ArchivePath $archiveUnderTest -Destination $literalDestination -Release $release
    if ($expected['cn-mon.exe'] -cne $expectedBinaryHash) {
        throw 'extracted executable hash differs from its pinned value'
    }
    if ($expected.Count -ne 4) {
        throw "expected four release files, got $($expected.Count)"
    }
    $testCount++
    Write-Host 'PASS literal paths and exact release extraction'

    Add-Type -AssemblyName System.IO.Compression.FileSystem
    function New-ArchiveFixture {
        param([string]$Path, [string]$ExtraEntry, [switch]$Duplicate, [switch]$OmitNotices)
        $stream = [IO.File]::Open($Path, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
        $zip = $null
        try {
            $zip = [IO.Compression.ZipArchive]::new($stream, [IO.Compression.ZipArchiveMode]::Create, $false)
            foreach ($name in @('cn-mon.exe', 'README.md', 'LICENSE', 'THIRD_PARTY_NOTICES.md')) {
                if ($OmitNotices -and $name -eq 'THIRD_PARTY_NOTICES.md') { continue }
                $entry = $zip.CreateEntry([string]$release.archive_root + '/' + $name)
                $entryStream = $entry.Open()
                $sourceStream = [IO.File]::OpenRead((Join-Path $literalDestination $name))
                try { $sourceStream.CopyTo($entryStream) }
                finally { $sourceStream.Dispose(); $entryStream.Dispose() }
            }
            if ($Duplicate) {
                $duplicateEntry = $zip.CreateEntry([string]$release.archive_root + '/cn-mon.exe')
                $duplicateStream = $duplicateEntry.Open()
                try { $duplicateStream.WriteByte(0) }
                finally { $duplicateStream.Dispose() }
            }
            if (-not [string]::IsNullOrEmpty($ExtraEntry)) {
                $extra = $zip.CreateEntry($ExtraEntry)
                $extraStream = $extra.Open()
                try { $extraStream.WriteByte(0) }
                finally { $extraStream.Dispose() }
            }
        }
        finally {
            if ($null -ne $zip) { $zip.Dispose() } else { $stream.Dispose() }
        }
        $fixtureRelease = $release | ConvertTo-Json -Depth 4 | ConvertFrom-Json
        $fixtureRelease.archive_sha256 = (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
        return $fixtureRelease
    }
    foreach ($archiveCase in @(
        @{ Name = 'rooted archive entry is rejected'; Entry = '/escape.txt'; Pattern = 'unsafe path'; Duplicate = $false; Omit = $false },
        @{ Name = 'traversal archive entry is rejected'; Entry = [string]$release.archive_root + '/../escape.txt'; Pattern = 'unexpected path'; Duplicate = $false; Omit = $false },
        @{ Name = 'unexpected archive entry is rejected'; Entry = [string]$release.archive_root + '/extra.txt'; Pattern = 'unexpected file'; Duplicate = $false; Omit = $false },
        @{ Name = 'duplicate archive entry is rejected'; Entry = ''; Pattern = 'duplicate path'; Duplicate = $true; Omit = $false },
        @{ Name = 'missing archive entry is rejected'; Entry = ''; Pattern = 'missing'; Duplicate = $false; Omit = $true }
    )) {
        $fixtureArchivePath = Join-Path $testRoot ([guid]::NewGuid().ToString('N') + '.zip')
        $fixtureRelease = New-ArchiveFixture -Path $fixtureArchivePath -ExtraEntry $archiveCase.Entry -Duplicate:$archiveCase.Duplicate -OmitNotices:$archiveCase.Omit
        Assert-Rejected -Name $archiveCase.Name -MessagePattern $archiveCase.Pattern -Action {
            Expand-CnMonMsixRelease -ArchivePath $fixtureArchivePath -Destination (Join-Path $testRoot ([guid]::NewGuid().ToString('N'))) -Release $fixtureRelease
        }
    }

    $executablePath = Join-Path $literalDestination 'cn-mon.exe'
    $originalExecutableBytes = [IO.File]::ReadAllBytes($executablePath)
    [IO.File]::AppendAllText($executablePath, 'changed')
    Assert-Rejected -Name 'modified staged executable is rejected' -MessagePattern 'payload hash mismatch' -Action {
        Assert-CnMonMsixPayload -Root $literalDestination -ExpectedFiles $expected
    }
    [IO.File]::WriteAllBytes($executablePath, $originalExecutableBytes)

    $unexpectedFile = Join-Path $literalDestination 'unexpected.txt'
    [IO.File]::WriteAllText($unexpectedFile, 'unexpected')
    Assert-Rejected -Name 'unexpected staged file is rejected' -MessagePattern 'unexpected file' -Action {
        Assert-CnMonMsixPayload -Root $literalDestination -ExpectedFiles $expected
    }
    Remove-Item -LiteralPath $unexpectedFile -Force

    $manifestPath = Join-Path $repositoryRoot 'packaging/windows/AppxManifest.xml'
    $validManifest = Read-CnMonMsixManifest -ManifestPath $manifestPath -Release $release -Identity $identity -PackageVersion $release.package_version -MaxVersionTested '10.0.26200.0'
    if ($validManifest.Identity.Name -cne 'Example.CnMon' -or $validManifest.Identity.Publisher -cne 'CN=Example') {
        throw 'manifest did not render the synthetic identity values'
    }
    $renderedIdentity = $validManifest.Xml.SelectSingleNode('/*[local-name()="Package"]/*[local-name()="Identity"]')
    if ($renderedIdentity.GetAttribute('Name') -cne 'Example.CnMon' -or $renderedIdentity.GetAttribute('ProcessorArchitecture') -cne 'x64') {
        throw 'rendered package identity did not retain the expected architecture and name'
    }
    $renderedVisuals = $validManifest.Xml.SelectSingleNode('/*[local-name()="Package"]/*[local-name()="Applications"]/*[local-name()="Application"]/*[local-name()="VisualElements"]')
    if ($renderedVisuals.GetAttribute('AppListEntry') -cne 'default') {
        throw 'rendered package does not have a normal Start entry'
    }
    $testCount++
    Write-Host 'PASS manifest renders runtime identity and normal Start entry'

    $manifestSource = Get-Content -LiteralPath $manifestPath -Raw
    foreach ($case in @(
        @{ Name = 'literal manifest identity is rejected'; Old = '__CNMON_MSIX_IDENTITY_NAME__'; New = 'SYNTHETIC-LITERAL-IDENTITY-MARKER'; Pattern = 'Name' },
        @{ Name = 'wrong manifest Publisher is rejected'; Old = '__CNMON_MSIX_IDENTITY_PUBLISHER__'; New = '__WRONG_PUBLISHER__'; Pattern = 'Publisher' },
        @{ Name = 'non-x64 manifest is rejected'; Old = 'ProcessorArchitecture="x64"'; New = 'ProcessorArchitecture="arm64"'; Pattern = 'x64' },
        @{ Name = 'hidden Start entry is rejected'; Old = 'AppListEntry="default"'; New = 'AppListEntry="none"'; Pattern = 'AppListEntry' },
        @{ Name = 'missing Start entry is rejected'; Old = 'AppListEntry="default"'; New = ''; Pattern = 'AppListEntry' },
        @{ Name = 'unknown Start entry is rejected'; Old = 'AppListEntry="default"'; New = 'AppListEntry="unknown"'; Pattern = 'AppListEntry' },
        @{ Name = 'wrong execution alias is rejected'; Old = 'Alias="cn-mon.exe"'; New = 'Alias="other.exe"'; Pattern = 'alias' }
    )) {
        $fixturePath = Join-Path $testRoot ('manifest-' + [guid]::NewGuid().ToString('N') + '.xml')
        $fixtureContent = $manifestSource.Replace($case.Old, $case.New)
        [IO.File]::WriteAllText($fixturePath, $fixtureContent, [Text.UTF8Encoding]::new($false))
        Assert-Rejected -Name $case.Name -MessagePattern $case.Pattern -Action {
            Read-CnMonMsixManifest -ManifestPath $fixturePath -Release $release -Identity $identity -PackageVersion $release.package_version -MaxVersionTested '10.0.26200.0'
        }
    }

    $missingAssetsDirectory = Join-Path $testRoot 'manifest-with-missing-assets'
    New-Item -ItemType Directory -Path $missingAssetsDirectory | Out-Null
    $missingAssetsManifest = Join-Path $missingAssetsDirectory 'AppxManifest.xml'
    [IO.File]::Copy($manifestPath, $missingAssetsManifest)
    Assert-Rejected -Name 'missing referenced manifest asset is rejected' -MessagePattern 'referenced asset.*missing' -Action {
        Read-CnMonMsixManifest -ManifestPath $missingAssetsManifest -Release $release -Identity $identity -PackageVersion $release.package_version -MaxVersionTested '10.0.26200.0'
    }

    foreach ($version in @('0.3.0.0', '1.3.0.1', '1.3.65536.0', '1.3')) {
        Assert-Rejected -Name "invalid package version $version is rejected" -MessagePattern 'version' -Action {
            Read-CnMonMsixManifest -ManifestPath $manifestPath -Release $release -Identity $identity -PackageVersion $version -MaxVersionTested '10.0.26200.0'
        }
    }
    Assert-Rejected -Name 'malformed MaxVersionTested is rejected' -MessagePattern 'MaxVersionTested' -Action {
        Read-CnMonMsixManifest -ManifestPath $manifestPath -Release $release -Identity $identity -PackageVersion $release.package_version -MaxVersionTested '10.0.latest.0'
    }
    $env:CNMON_MSIX_EXPECTED_PFN = 'Other.Package_abcdefghijklm'
    Assert-Rejected -Name 'PFN for a different package name is rejected' -MessagePattern 'PFN.*Name' -Action {
        Read-CnMonMsixIdentity
    }
    if ([Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT) {
        $env:CNMON_MSIX_EXPECTED_PFN = & $identityModule {
            Get-CnMonMsixDerivedPFN -Name 'Example.CnMon' -Publisher 'CN=Example'
        }
        $env:CNMON_MSIX_IDENTITY_PUBLISHER = 'CN=Different'
        Assert-Rejected -Name 'PFN for a different publisher is rejected' -MessagePattern 'PFN.*Publisher' -Action {
            Read-CnMonMsixIdentity
        }
    }
    $env:CNMON_MSIX_IDENTITY_NAME = 'Example.CnMon'
    $env:CNMON_MSIX_IDENTITY_PUBLISHER = 'CN=Example'
    $env:CNMON_MSIX_PUBLISHER_DISPLAY_NAME = 'Example Publisher'
    if ([Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT) {
        $env:CNMON_MSIX_EXPECTED_PFN = & $identityModule {
            Get-CnMonMsixDerivedPFN -Name 'Example.CnMon' -Publisher 'CN=Example'
        }
    } else {
        $env:CNMON_MSIX_EXPECTED_PFN = 'Example.CnMon_abcdefghijklm'
    }
    if ([Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT) {
        $identity = Read-CnMonMsixIdentity
    }

    if (-not $ModuleOnly) {
        if ([string]::IsNullOrWhiteSpace($SdkBinDirectory)) {
            throw 'SdkBinDirectory is required unless -ModuleOnly is set'
        }
        if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
            throw 'End-to-end MakeAppx tests require native Windows; use -ModuleOnly for portable module assertions'
        }
        foreach ($tool in @('makeappx.exe', 'signtool.exe')) {
            if (-not (Test-Path -LiteralPath (Join-Path $SdkBinDirectory $tool) -PathType Leaf)) {
                throw "$tool is missing from the explicit SDK bin directory"
            }
        }

        function Copy-FixtureSource {
            param([string]$Destination)
            New-Item -ItemType Directory -Path $Destination | Out-Null
            $fixturePackaging = Join-Path $Destination 'packaging'
            New-Item -ItemType Directory -Path $fixturePackaging | Out-Null
            Copy-Item -LiteralPath (Join-Path $repositoryRoot 'packaging/windows') -Destination $fixturePackaging -Recurse
            $fixtureScripts = Join-Path $Destination 'scripts'
            New-Item -ItemType Directory -Path $fixtureScripts | Out-Null
            foreach ($scriptName in @('msix-package.psm1', 'package-msix.ps1', 'test-msix-package.ps1')) {
                Copy-Item -LiteralPath (Join-Path $PSScriptRoot $scriptName) -Destination (Join-Path $fixtureScripts $scriptName)
            }
        }

        function Initialize-FixtureGit {
            param([string]$Root)
            & git -C $Root init --quiet
            if ($LASTEXITCODE -ne 0) { throw 'could not initialize clean packaging fixture repository' }
            & git -C $Root config user.name 'MSIX Test Fixture'
            if ($LASTEXITCODE -ne 0) { throw 'could not configure packaging fixture repository' }
            & git -C $Root config user.email 'msix-fixture@example.invalid'
            if ($LASTEXITCODE -ne 0) { throw 'could not configure packaging fixture repository' }
            & git -C $Root add -- packaging/windows scripts/msix-package.psm1 scripts/package-msix.ps1 scripts/test-msix-package.ps1
            if ($LASTEXITCODE -ne 0) { throw 'could not stage packaging fixture source' }
            & git -C $Root commit --quiet -m 'test fixture packaging source'
            if ($LASTEXITCODE -ne 0) { throw 'could not commit packaging fixture source' }
        }

        function Invoke-FixturePackager {
            param(
                [string]$FixtureRoot,
                [string]$Archive,
                [string]$Output,
                [string]$SdkDirectory,
                [string]$RequestedPackageVersion
            )
            if ([string]::IsNullOrWhiteSpace($RequestedPackageVersion)) {
                $RequestedPackageVersion = [string]$release.package_version
            }
            $pwsh = (Get-Command pwsh -ErrorAction Stop).Source
            $arguments = @(
                '-NoProfile', '-File', (Join-Path $FixtureRoot 'scripts/package-msix.ps1'),
                '-ReleaseArchive', $Archive,
                '-OutputDirectory', $Output,
                '-SdkBinDirectory', $SdkDirectory,
                '-PackageVersion', $RequestedPackageVersion,
                '-MaxVersionTested', $maxVersionTested,
                '-Purpose', 'test-fixture'
            )
            $lines = @(& $pwsh @arguments 2>&1)
            $exitCode = $LASTEXITCODE
            return [pscustomobject]@{
                ExitCode = $exitCode
                Output = ($lines | ForEach-Object { [string]$_ }) -join [Environment]::NewLine
            }
        }

        function Assert-EmptyDirectory {
            param([string]$Path, [string]$CaseName)
            if (@(Get-ChildItem -LiteralPath $Path -Force).Count -ne 0) {
                throw "$CaseName left output files behind"
            }
            $script:testCount++
            Write-Host "PASS $CaseName"
        }

        $osVersion = [Environment]::OSVersion.Version
        $maxVersionTested = '{0}.{1}.{2}.0' -f $osVersion.Major, $osVersion.Minor, $osVersion.Build
        $goodFixture = Join-Path $testRoot "committed fixture 'source"
        Copy-FixtureSource -Destination $goodFixture
        Initialize-FixtureGit -Root $goodFixture

        $dirtyOutput = Join-Path $testRoot 'dirty source output'
        New-Item -ItemType Directory -Path $dirtyOutput | Out-Null
        $readmePath = Join-Path $goodFixture 'packaging/windows/README-MSIX.md'
        $readmeBytes = [IO.File]::ReadAllBytes($readmePath)
        [IO.File]::AppendAllText($readmePath, 'uncommitted change')
        $dirtyResult = Invoke-FixturePackager -FixtureRoot $goodFixture -Archive $archiveUnderTest -Output $dirtyOutput -SdkDirectory (Join-Path $testRoot 'missing SDK')
        if ($dirtyResult.ExitCode -eq 0 -or $dirtyResult.Output -notmatch 'packaging inputs must be committed and clean') {
            throw 'packager did not reject changed packaging inputs'
        }
        [IO.File]::WriteAllBytes($readmePath, $readmeBytes)
        Assert-EmptyDirectory -Path $dirtyOutput -CaseName 'modified packaging source is rejected atomically'

        $corruptOutput = Join-Path $testRoot 'corrupt archive output'
        New-Item -ItemType Directory -Path $corruptOutput | Out-Null
        $corruptResult = Invoke-FixturePackager -FixtureRoot $goodFixture -Archive $corruptArchive -Output $corruptOutput -SdkDirectory (Join-Path $testRoot 'missing SDK')
        if ($corruptResult.ExitCode -eq 0 -or $corruptResult.Output -notmatch 'archive SHA-256 mismatch') {
            throw 'packager did not reject a corrupt archive before invoking MakeAppx'
        }
        Assert-EmptyDirectory -Path $corruptOutput -CaseName 'corrupt archive fails before MakeAppx with no receipt'

        $invalidVersionOutput = Join-Path $testRoot 'invalid version output'
        New-Item -ItemType Directory -Path $invalidVersionOutput | Out-Null
        $versionTraversalSentinel = Join-Path $testRoot 'victim_x64.msix'
        [IO.File]::WriteAllText($versionTraversalSentinel, 'preserve sibling output')
        $versionResult = Invoke-FixturePackager -FixtureRoot $goodFixture -Archive $archiveUnderTest -Output $invalidVersionOutput -SdkDirectory (Join-Path $testRoot 'missing SDK') -RequestedPackageVersion 'x/../../victim'
        if ($versionResult.ExitCode -eq 0 -or $versionResult.Output -notmatch 'package version is malformed') {
            throw 'packager did not reject a path-shaped package version before output setup'
        }
        if ([IO.File]::ReadAllText($versionTraversalSentinel) -cne 'preserve sibling output') {
            throw 'invalid package version removed an unrelated sibling file'
        }
        Assert-EmptyDirectory -Path $invalidVersionOutput -CaseName 'path-shaped package version preserves sibling output'

        $identityFailureOutput = Join-Path $testRoot 'identity failure output'
        New-Item -ItemType Directory -Path $identityFailureOutput | Out-Null
        $privateFailureMarker = 'SYNTHETIC-PACKAGER-IDENTITY-LEAK-MARKER'
        $env:CNMON_MSIX_IDENTITY_NAME = $privateFailureMarker + ' !'
        try {
            $identityFailureResult = Invoke-FixturePackager -FixtureRoot $goodFixture -Archive $archiveUnderTest -Output $identityFailureOutput -SdkDirectory (Join-Path $testRoot 'missing SDK')
        }
        finally {
            $env:CNMON_MSIX_IDENTITY_NAME = 'Example.CnMon'
        }
        if ($identityFailureResult.ExitCode -eq 0 -or $identityFailureResult.Output -notmatch 'identity Name is invalid' -or
            $identityFailureResult.Output -match [regex]::Escape($privateFailureMarker)) {
            throw 'invalid identity was accepted or echoed by the packager'
        }
        Assert-EmptyDirectory -Path $identityFailureOutput -CaseName 'invalid identity fails atomically without echo'

        $wrongManifestFixture = Join-Path $testRoot "invalid manifest fixture 'source"
        Copy-FixtureSource -Destination $wrongManifestFixture
        $wrongManifestPath = Join-Path $wrongManifestFixture 'packaging/windows/AppxManifest.xml'
        $wrongManifestText = (Get-Content -LiteralPath $wrongManifestPath -Raw).Replace('__CNMON_MSIX_IDENTITY_NAME__', '__WRONG_NAME__')
        [IO.File]::WriteAllText($wrongManifestPath, $wrongManifestText, [Text.UTF8Encoding]::new($false))
        Initialize-FixtureGit -Root $wrongManifestFixture
        $manifestOutput = Join-Path $testRoot 'invalid manifest output'
        New-Item -ItemType Directory -Path $manifestOutput | Out-Null
        $manifestResult = Invoke-FixturePackager -FixtureRoot $wrongManifestFixture -Archive $archiveUnderTest -Output $manifestOutput -SdkDirectory (Join-Path $testRoot 'missing SDK')
        if ($manifestResult.ExitCode -eq 0 -or $manifestResult.Output -notmatch 'manifest identity Name placeholder is invalid') {
            throw 'packager did not reject an invalid manifest before invoking MakeAppx'
        }
        Assert-EmptyDirectory -Path $manifestOutput -CaseName 'invalid manifest fails before MakeAppx with no receipt'

        $occupiedOutput = Join-Path $testRoot 'occupied output'
        New-Item -ItemType Directory -Path $occupiedOutput | Out-Null
        $sentinelPath = Join-Path $occupiedOutput 'preserve.txt'
        [IO.File]::WriteAllText($sentinelPath, 'preserve existing output')
        $occupiedResult = Invoke-FixturePackager -FixtureRoot $goodFixture -Archive $archiveUnderTest -Output $occupiedOutput -SdkDirectory (Join-Path $testRoot 'missing SDK')
        if ($occupiedResult.ExitCode -eq 0 -or $occupiedResult.Output -notmatch 'output directory must already be empty') {
            throw 'packager did not reject an occupied output directory'
        }
        if ([IO.File]::ReadAllText($sentinelPath) -cne 'preserve existing output' -or (Get-ChildItem -LiteralPath $occupiedOutput -Force).Count -ne 1) {
            throw 'packager modified a pre-existing output directory'
        }
        $testCount++
        Write-Host 'PASS existing output is preserved'

        $packageOutput = Join-Path $testRoot "successful output 'with spaces"
        New-Item -ItemType Directory -Path $packageOutput | Out-Null
        $packageResult = Invoke-FixturePackager -FixtureRoot $goodFixture -Archive $archiveUnderTest -Output $packageOutput -SdkDirectory $SdkBinDirectory
        if ($packageResult.ExitCode -ne 0) {
            throw "clean fixture packaging failed: $($packageResult.Output)"
        }
        $packagePath = Join-Path $packageOutput "cn-mon_$($release.package_version)_x64.msix"
        $receiptPath = Join-Path $packageOutput 'msix-receipt.json'
        if (-not (Test-Path -LiteralPath $packagePath -PathType Leaf) -or -not (Test-Path -LiteralPath $receiptPath -PathType Leaf)) {
            throw 'successful packaging did not create both package and receipt'
        }
        $receipt = Get-Content -LiteralPath $receiptPath -Raw | ConvertFrom-Json
        $actualPackageHash = (Get-FileHash -LiteralPath $packagePath -Algorithm SHA256).Hash.ToLowerInvariant()
        $expectedSidecarHash = (Get-FileHash -LiteralPath (Join-Path $goodFixture 'packaging/windows/cn-mon.exe.manifest') -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($receipt.purpose -cne 'test-fixture' -or $receipt.package_sha256 -cne $actualPackageHash -or
            $receipt.binary_sha256 -cne $expectedBinaryHash -or @($receipt.files.PSObject.Properties).Count -ne 10 -or
            $receipt.files.'cn-mon.exe.manifest' -cne $expectedSidecarHash) {
            throw 'package receipt does not match the verified test fixture artifact'
        }
        $testCount++
        Write-Host 'PASS committed fixture MakeAppx pack/unpack and receipt'
    }

    Write-Host "PASS $testCount MSIX packaging assertions"
}
finally {
    Remove-Item -LiteralPath $testRoot -Recurse -Force -ErrorAction SilentlyContinue
    foreach ($variableName in $identityEnvironmentNames) {
        $originalValue = $originalIdentityEnvironment[$variableName]
        if ($null -eq $originalValue) {
            Remove-Item -LiteralPath ('Env:' + $variableName) -ErrorAction SilentlyContinue
        } else {
            [Environment]::SetEnvironmentVariable($variableName, $originalValue)
        }
    }
}
foreach ($variableName in $identityEnvironmentNames) {
    if ([Environment]::GetEnvironmentVariable($variableName) -cne $originalIdentityEnvironment[$variableName]) {
        throw "MSIX assertion script did not restore $variableName"
    }
}
Write-Host 'PASS caller identity environment restored'
