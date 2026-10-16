[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$ReleaseArchive,

    [Parameter(Mandatory = $true)]
    [string]$OutputDirectory,

    [Parameter(Mandatory = $true)]
    [string]$SdkBinDirectory,

    [string]$PackageVersion,

    [Parameter(Mandatory = $true)]
    [string]$MaxVersionTested,

    [ValidateSet('candidate', 'test-fixture')]
    [string]$Purpose = 'candidate'
)

$ErrorActionPreference = 'Stop'
$repositoryRoot = Split-Path -Parent $PSScriptRoot
$modulePath = Join-Path $PSScriptRoot 'msix-package.psm1'
Import-Module $modulePath -Force -ErrorAction Stop

function Get-PackagingRevision {
    param([string]$Root)

    $paths = @(
        'packaging/windows',
        'scripts/msix-package.psm1',
        'scripts/package-msix.ps1',
        'scripts/test-msix-package.ps1'
    )
    $status = @(& git -C $Root status --porcelain --untracked-files=all -- @paths 2>&1)
    if ($LASTEXITCODE -ne 0) {
        throw 'could not inspect packaging source status'
    }
    if ($status.Count -ne 0) {
        throw 'packaging inputs must be committed and clean before creating an MSIX'
    }
    $revision = (& git -C $Root rev-parse --verify HEAD 2>&1)
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace([string]$revision)) {
        throw 'could not determine packaging source revision'
    }
    return ([string]$revision).Trim()
}

function Assert-WindowsVersionOutput {
    param([string]$ExecutablePath, [object]$Release)

    $startInfo = [Diagnostics.ProcessStartInfo]::new()
    $startInfo.FileName = $ExecutablePath
    $startInfo.UseShellExecute = $false
    $startInfo.RedirectStandardOutput = $true
    $startInfo.RedirectStandardError = $true
    $startInfo.ArgumentList.Add('--version')
    $process = [Diagnostics.Process]::Start($startInfo)
    try {
        $stdout = $process.StandardOutput.ReadToEnd()
        $stderr = $process.StandardError.ReadToEnd()
        $process.WaitForExit()
        $expected = "cn-mon $($Release.cli_version) ($($Release.cli_revision))`n"
        if ($process.ExitCode -ne 0 -or $stdout -cne $expected -or $stderr.Length -ne 0) {
            throw 'release executable did not return the exact approved version output'
        }
    }
    finally {
        $process.Dispose()
    }
}

function Get-ToolInfo {
    param([string]$Path)

    $info = [Diagnostics.FileVersionInfo]::GetVersionInfo($Path)
    return [pscustomobject]@{
        path = [IO.Path]::GetFullPath($Path)
        file_version = $info.FileVersion
        product_version = $info.ProductVersion
    }
}

$sourceRevision = Get-PackagingRevision -Root $repositoryRoot
if (-not (Test-Path -LiteralPath $OutputDirectory -PathType Container)) {
    throw 'output directory must already exist and be empty'
}
$outputPath = [IO.Path]::GetFullPath($OutputDirectory)
if (@(Get-ChildItem -LiteralPath $outputPath -Force).Count -ne 0) {
    throw 'output directory must already be empty'
}

$releasePath = Join-Path $repositoryRoot 'packaging/windows/release.json'
try {
    $release = Get-Content -LiteralPath $releasePath -Raw | ConvertFrom-Json
}
catch {
    throw 'release metadata could not be read as JSON'
}
if ([string]::IsNullOrWhiteSpace($PackageVersion)) {
    $PackageVersion = [string]$release.package_version
}
$sourceManifest = Join-Path $repositoryRoot 'packaging/windows/AppxManifest.xml'
$identity = Read-CnMonMsixIdentity
$manifest = Read-CnMonMsixManifest -ManifestPath $sourceManifest -Release $release -Identity $identity -PackageVersion $PackageVersion -MaxVersionTested $MaxVersionTested
$PackageVersion = $manifest.PackageVersion

$temporaryRoot = Join-Path $outputPath ('.cn-mon-msix-' + [guid]::NewGuid().ToString('N'))
$finalPackageName = "cn-mon_${PackageVersion}_x64.msix"
$finalPackagePath = Join-Path $outputPath $finalPackageName
$finalReceiptPath = Join-Path $outputPath 'msix-receipt.json'
$pendingPackagePath = Join-Path $outputPath ('.' + $finalPackageName + '.pending')
$pendingReceiptPath = Join-Path $outputPath '.msix-receipt.json.pending'
$completed = $false
$pendingPackageCreated = $false
$pendingReceiptCreated = $false
$finalPackageCreated = $false
$finalReceiptCreated = $false
try {
    New-Item -ItemType Directory -Path $temporaryRoot | Out-Null
    $releasePayload = Join-Path $temporaryRoot 'release-payload'
    $releaseFiles = Expand-CnMonMsixRelease -ArchivePath $ReleaseArchive -Destination $releasePayload -Release $release

    if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
        throw 'native Windows is required to run the release executable and MakeAppx'
    }
    Assert-WindowsVersionOutput -ExecutablePath (Join-Path $releasePayload 'cn-mon.exe') -Release $release

    $makeAppxPath = Join-Path $SdkBinDirectory 'makeappx.exe'
    $signToolPath = Join-Path $SdkBinDirectory 'signtool.exe'
    if (-not (Test-Path -LiteralPath $makeAppxPath -PathType Leaf)) {
        throw 'makeappx.exe was not found in the explicit SDK bin directory'
    }
    if (-not (Test-Path -LiteralPath $signToolPath -PathType Leaf)) {
        throw 'signtool.exe was not found in the explicit SDK bin directory'
    }

    $stage = Join-Path $temporaryRoot 'stage'
    $unpacked = Join-Path $temporaryRoot 'unpacked'
    $temporaryPackage = Join-Path $temporaryRoot 'package.msix'
    New-Item -ItemType Directory -Path $stage | Out-Null
    New-Item -ItemType Directory -Path (Join-Path $stage 'Assets') | Out-Null
    foreach ($name in @('cn-mon.exe', 'README.md', 'LICENSE', 'THIRD_PARTY_NOTICES.md')) {
        Copy-Item -LiteralPath (Join-Path $releasePayload $name) -Destination (Join-Path $stage $name)
    }
    Copy-Item -LiteralPath (Join-Path $repositoryRoot 'packaging/windows/cn-mon.exe.manifest') -Destination (Join-Path $stage 'cn-mon.exe.manifest')
    Copy-Item -LiteralPath (Join-Path $repositoryRoot 'packaging/windows/README-MSIX.md') -Destination (Join-Path $stage 'README-MSIX.md')
    foreach ($assetName in @('StoreLogo.png', 'Square44x44Logo.png', 'Square150x150Logo.png')) {
        Copy-Item -LiteralPath (Join-Path $repositoryRoot "packaging/windows/Assets/$assetName") -Destination (Join-Path $stage "Assets/$assetName")
    }
    $manifestPath = Join-Path $stage 'AppxManifest.xml'
    $manifest.Xml.Save($manifestPath)

    $expectedFiles = @{}
    foreach ($name in $releaseFiles.Keys) {
        $expectedFiles[$name] = $releaseFiles[$name]
    }
    $expectedFiles['cn-mon.exe.manifest'] = (Get-FileHash -LiteralPath (Join-Path $stage 'cn-mon.exe.manifest') -Algorithm SHA256).Hash.ToLowerInvariant()
    $expectedFiles['README-MSIX.md'] = (Get-FileHash -LiteralPath (Join-Path $stage 'README-MSIX.md') -Algorithm SHA256).Hash.ToLowerInvariant()
    foreach ($assetName in @('StoreLogo.png', 'Square44x44Logo.png', 'Square150x150Logo.png')) {
        $relativePath = "Assets/$assetName"
        $expectedFiles[$relativePath] = (Get-FileHash -LiteralPath (Join-Path $stage $relativePath) -Algorithm SHA256).Hash.ToLowerInvariant()
    }
    $expectedFiles['AppxManifest.xml'] = (Get-FileHash -LiteralPath $manifestPath -Algorithm SHA256).Hash.ToLowerInvariant()
    Assert-CnMonMsixPayload -Root $stage -ExpectedFiles $expectedFiles

    New-Item -ItemType Directory -Path $unpacked | Out-Null
    $packArguments = @('pack', '/d', $stage, '/p', $temporaryPackage, '/no')
    $msixModule = Get-Module -Name 'msix-package'
    $packExitCode = & $msixModule {
        param([string]$Path, [string[]]$ToolArguments, [string]$LogPath)
        Invoke-CnMonMsixTool -ExecutablePath $Path -Arguments $ToolArguments -LogPath $LogPath
    } $makeAppxPath $packArguments (Join-Path $temporaryRoot 'makeappx-pack.log')
    if ($packExitCode -ne 0) {
        throw "MakeAppx pack failed (exit code $packExitCode)"
    }
    if (-not (Test-Path -LiteralPath $temporaryPackage -PathType Leaf)) {
        throw 'MakeAppx pack did not create a package'
    }

    $unpackArguments = @('unpack', '/p', $temporaryPackage, '/d', $unpacked, '/no')
    $unpackExitCode = & $msixModule {
        param([string]$Path, [string[]]$ToolArguments, [string]$LogPath)
        Invoke-CnMonMsixTool -ExecutablePath $Path -Arguments $ToolArguments -LogPath $LogPath
    } $makeAppxPath $unpackArguments (Join-Path $temporaryRoot 'makeappx-unpack.log')
    if ($unpackExitCode -ne 0) {
        throw "MakeAppx unpack failed (exit code $unpackExitCode)"
    }
    Assert-CnMonMsixPayload -Root $unpacked -ExpectedFiles $expectedFiles

    $packageHash = (Get-FileHash -LiteralPath $temporaryPackage -Algorithm SHA256).Hash.ToLowerInvariant()
    $manifestHash = $expectedFiles['AppxManifest.xml']
    $orderedFiles = [ordered]@{}
    foreach ($name in @($expectedFiles.Keys | Sort-Object -CaseSensitive)) {
        $orderedFiles[$name] = $expectedFiles[$name]
    }
    $receipt = [ordered]@{
        schema_version = 1
        purpose = $Purpose
        package_version = $manifest.PackageVersion
        cli_version = [string]$release.cli_version
        cli_revision = [string]$release.cli_revision
        packaging_revision = $sourceRevision
        archive_sha256 = ([string]$release.archive_sha256).ToLowerInvariant()
        binary_sha256 = ([string]$release.binary_sha256).ToLowerInvariant()
        name = $manifest.Identity.Name
        publisher = $manifest.Identity.Publisher
        publisher_display_name = $manifest.Identity.PublisherDisplayName
        expected_pfn = $manifest.Identity.ExpectedPFN
        architecture = 'x64'
        min_version = $manifest.MinVersion
        max_version_tested = $manifest.MaxVersionTested
        files = $orderedFiles
        manifest_sha256 = $manifestHash
        package_sha256 = $packageHash
        tools = [ordered]@{
            sdk_bin_directory = [IO.Path]::GetFullPath($SdkBinDirectory)
            makeappx = Get-ToolInfo -Path $makeAppxPath
            signtool = Get-ToolInfo -Path $signToolPath
        }
        signing = 'unsigned'
    }
    $receiptJson = ConvertTo-Json -InputObject $receipt -Depth 8
    [IO.File]::Copy($temporaryPackage, $pendingPackagePath)
    $pendingPackageCreated = $true
    $receiptBytes = [Text.UTF8Encoding]::new($false).GetBytes($receiptJson + [Environment]::NewLine)
    $receiptStream = [IO.File]::Open($pendingReceiptPath, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
    $pendingReceiptCreated = $true
    try {
        $receiptStream.Write($receiptBytes, 0, $receiptBytes.Length)
    }
    finally {
        $receiptStream.Dispose()
    }
    [IO.File]::Move($pendingPackagePath, $finalPackagePath)
    $pendingPackageCreated = $false
    $finalPackageCreated = $true
    try {
        [IO.File]::Move($pendingReceiptPath, $finalReceiptPath)
        $pendingReceiptCreated = $false
        $finalReceiptCreated = $true
    }
    catch {
        Remove-Item -LiteralPath $finalPackagePath -Force -ErrorAction SilentlyContinue
        $finalPackageCreated = $false
        throw
    }
    $completed = $true
    Write-Host "Created unsigned MSIX and receipt in $outputPath"
}
finally {
    Remove-Item -LiteralPath $temporaryRoot -Recurse -Force -ErrorAction SilentlyContinue
    if ($pendingPackageCreated) {
        Remove-Item -LiteralPath $pendingPackagePath -Force -ErrorAction SilentlyContinue
    }
    if ($pendingReceiptCreated) {
        Remove-Item -LiteralPath $pendingReceiptPath -Force -ErrorAction SilentlyContinue
    }
    if (-not $completed) {
        if ($finalPackageCreated) {
            Remove-Item -LiteralPath $finalPackagePath -Force -ErrorAction SilentlyContinue
        }
        if ($finalReceiptCreated) {
            Remove-Item -LiteralPath $finalReceiptPath -Force -ErrorAction SilentlyContinue
        }
    }
}
