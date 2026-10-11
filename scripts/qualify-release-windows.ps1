$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false

$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$repoRoot = Split-Path -Parent $scriptDir
Set-Location $repoRoot

function Fail([string] $Message) { throw "release qualification: $Message" }
function Invoke-Checked([string] $Name, [string[]] $Arguments) {
    & $Name @Arguments
    if ($LASTEXITCODE -ne 0) { Fail "$Name failed with exit $LASTEXITCODE" }
}
function Get-SHA256([string] $Path) {
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}
function Write-UTF8([string] $Path, [string] $Value) {
    [IO.File]::WriteAllText($Path, $Value, [Text.UTF8Encoding]::new($false))
}

$version = $env:CNMON_RELEASE_VERSION
$revision = $env:CNMON_RELEASE_REVISION
if ($version -cne '0.2.0') { Fail 'CNMON_RELEASE_VERSION must be 0.2.0' }
if ($revision -cnotmatch '^[0-9a-f]{40}$') { Fail 'CNMON_RELEASE_REVISION must be a full lowercase 40-hex commit' }
$head = (& git rev-parse HEAD).Trim()
if ($LASTEXITCODE -ne 0 -or $head -cne $revision) { Fail 'checked out HEAD does not match CNMON_RELEASE_REVISION' }
$dirty = & git status --porcelain --untracked-files=all
if ($LASTEXITCODE -ne 0 -or $dirty) { Fail 'source checkout is not clean' }
# Keep the three distributed text documents byte-identical to their Git blobs
# even on runners configured with core.autocrlf=true.
Invoke-Checked 'git' @('-c', 'core.autocrlf=false', '-c', 'core.eol=lf', 'checkout-index', '--force', '--', 'README.md', 'LICENSE', 'THIRD_PARTY_NOTICES.md')
foreach ($document in @('README.md', 'LICENSE', 'THIRD_PARTY_NOTICES.md')) {
    $expectedBlob = (& git rev-parse "HEAD:$document").Trim()
    if ($LASTEXITCODE -ne 0) { Fail "cannot resolve tracked blob for $document" }
    $actualBlob = (& git hash-object --no-filters -- $document).Trim()
    if ($LASTEXITCODE -ne 0 -or $actualBlob -cne $expectedBlob) { Fail "working-tree bytes differ from the tracked release document: $document" }
}
$goVersion = (& go version).Trim()
if ($LASTEXITCODE -ne 0 -or $goVersion -notmatch '^go version go1\.27\.2 ') { Fail 'qualification requires Go 1.27.2' }

$env:GOTOOLCHAIN = 'local'
$env:GOFLAGS = '-mod=readonly'
$env:CGO_ENABLED = '0'
$env:GOAMD64 = 'v1'
$env:CNMON_RELEASE_QUALIFY = '1'
$env:CNMON_NATIVE_COMMAND = '1'
$env:CNMON_RELEASE_VERSION = $version
$env:CNMON_RELEASE_REVISION = $revision

$artifactDir = $env:CNMON_RELEASE_ARTIFACT_DIR
if (-not $artifactDir) {
    $tempRoot = $env:RUNNER_TEMP
    if (-not $tempRoot) { $tempRoot = [IO.Path]::GetTempPath() }
    $artifactDir = Join-Path $tempRoot 'cn-mon-release-windows'
}
New-Item -ItemType Directory -Force -Path $artifactDir | Out-Null
$artifactDir = (Resolve-Path -LiteralPath $artifactDir).Path
$tempRoot = $env:RUNNER_TEMP
if (-not $tempRoot) { $tempRoot = [IO.Path]::GetTempPath() }
$workDir = Join-Path $tempRoot ("cn mon release windows " + [guid]::NewGuid().ToString('N'))
$sourceDir = Join-Path $workDir 'source'
$extractDir = Join-Path $workDir 'fresh extraction with spaces'
New-Item -ItemType Directory -Path $sourceDir, $extractDir | Out-Null
$binary = Join-Path $sourceDir 'cn-mon.exe'
$archive = Join-Path $artifactDir 'cn-mon_0.2.0_windows_amd64.zip'
$commitUnix = (& git show -s --format=%ct $revision).Trim()
if ($LASTEXITCODE -ne 0 -or $commitUnix -notmatch '^[0-9]+$') { Fail 'selected commit has invalid timestamp' }
if ($env:CNMON_RELEASE_COMMIT_UNIX -cne $commitUnix) { Fail 'workflow commit timestamp does not match the selected checkout' }
Invoke-Checked 'go' @('mod', 'verify')
$moduleOutput = @(& go list -deps -f '{{if .Module}}{{.Module.Path}}{{end}}' ./cmd/cn-mon)
if ($LASTEXITCODE -ne 0) { Fail 'go list production dependency audit failed' }
$modulePaths = @($moduleOutput | ForEach-Object { ([string]$_).Trim() } | Where-Object { $_ } | Sort-Object -Unique)
$mainModule = (& go list -m).Trim()
if ($LASTEXITCODE -ne 0 -or -not $mainModule) { Fail 'go list main module lookup failed' }
$noticeLines = [Collections.Generic.List[string]]::new()
$inInventory = $false
foreach ($line in Get-Content -LiteralPath 'THIRD_PARTY_NOTICES.md') {
    if ($line -ceq '## Go module dependency inventory') { $inInventory = $true; continue }
    if ($line.StartsWith('The full module graph')) { break }
    if ($inInventory) { $noticeLines.Add($line) }
}
$noticeText = $noticeLines -join "`n"
foreach ($module in $modulePaths) {
    if ($module -eq $mainModule) { continue }
    $pattern = '\|\s+`' + [regex]::Escape($module) + '`\s+\|'
    if ($noticeText -notmatch $pattern) { Fail "production import module is missing from notices: $module" }
}
Write-UTF8 (Join-Path $artifactDir 'production-import-modules.txt') (($modulePaths -join "`n") + "`n")

$ldflags = "-X main.version=$version -X main.revision=$revision"
Invoke-Checked 'go' @('build', '-trimpath', '-buildvcs=true', '-mod=readonly', '-ldflags', $ldflags, '-o', $binary, './cmd/cn-mon')
if (-not (Test-Path -LiteralPath $binary -PathType Leaf)) { Fail 'native product build did not produce an executable' }

$packReceipt = Join-Path $artifactDir 'pack-receipt.json'
$packOutput = @(& go run ./scripts/release-artifact pack -binary $binary -source-root $repoRoot -out-dir $artifactDir -version $version -revision $revision -commit-unix $commitUnix -target windows_amd64)
if ($LASTEXITCODE -ne 0) { Fail "release-artifact pack failed with exit $LASTEXITCODE" }
Write-UTF8 $packReceipt (($packOutput -join "`n") + "`n")
if (-not (Test-Path -LiteralPath $archive -PathType Leaf)) { Fail 'pack did not create the expected archive' }
$archiveSHA = Get-SHA256 $archive

$verifyReceipt = Join-Path $artifactDir 'verify-receipt.json'
$verifyOutput = @(& go run ./scripts/release-artifact verify -archive $archive -sha256 $archiveSHA -source-root $repoRoot -extract-dir $extractDir -version $version -revision $revision -target windows_amd64)
if ($LASTEXITCODE -ne 0) { Fail "release-artifact verify failed with exit $LASTEXITCODE" }
Write-UTF8 $verifyReceipt (($verifyOutput -join "`n") + "`n")
$extracted = Join-Path $extractDir 'cn-mon_0.2.0_windows_amd64\cn-mon.exe'
if (-not (Test-Path -LiteralPath $extracted -PathType Leaf)) { Fail 'verified extraction did not produce an executable' }

$pack = Get-Content -Raw -LiteralPath $packReceipt | ConvertFrom-Json
$verify = Get-Content -Raw -LiteralPath $verifyReceipt | ConvertFrom-Json
if ($pack.version -cne $version -or $pack.revision -cne $revision -or $pack.target -cne 'windows_amd64') { Fail 'pack receipt identity mismatch' }
if ($verify.version -cne $version -or $verify.revision -cne $revision -or $verify.target -cne 'windows_amd64') { Fail 'verify receipt identity mismatch' }
if ($pack.archive_sha256 -cne $archiveSHA -or $verify.archive_sha256 -cne $archiveSHA) { Fail 'archive receipt hash mismatch' }
if ($pack.binary_sha256 -cne (Get-SHA256 $binary) -or $pack.binary_sha256 -cne $verify.binary_sha256 -or $verify.binary_sha256 -cne (Get-SHA256 $extracted)) { Fail 'pack/verify binary receipt hash mismatch' }
if ([IO.Path]::GetFullPath($pack.binary_path) -cne [IO.Path]::GetFullPath($binary)) { Fail 'pack receipt identifies a different input binary path' }
if ([IO.Path]::GetFullPath($verify.binary_path) -cne [IO.Path]::GetFullPath($extracted)) { Fail 'verify receipt identifies a different binary path' }

$buildInfo = @(& go version -m $extracted)
if ($LASTEXITCODE -ne 0) { Fail 'go version -m failed for extracted executable' }
$buildInfoPath = Join-Path $artifactDir 'go-build-info.txt'
Write-UTF8 $buildInfoPath (($buildInfo -join "`n") + "`n")
$buildInfoValues = @($buildInfo | ForEach-Object { ([string]$_).Trim() })
function Has-BuildSetting([string] $Name, [string] $Value) {
    $expected = '^build\s+' + [regex]::Escape($Name + '=' + $Value) + '$'
    return [bool]($buildInfoValues | Where-Object { $_ -match $expected } | Select-Object -First 1)
}
if (-not (Has-BuildSetting 'vcs.revision' $revision)) { Fail 'extracted build info has the wrong source revision' }
if (-not (Has-BuildSetting 'vcs.modified' 'false')) { Fail 'extracted build info is dirty' }
if (-not (Has-BuildSetting 'GOOS' 'windows')) { Fail 'extracted build info has the wrong GOOS' }
if (-not (Has-BuildSetting 'GOARCH' 'amd64')) { Fail 'extracted build info has the wrong GOARCH' }
if (-not (Has-BuildSetting 'CGO_ENABLED' '0')) { Fail 'extracted build info has CGO enabled' }

$versionOutputPath = Join-Path $artifactDir 'version-output.txt'
$versionErrorPath = Join-Path $artifactDir 'version-error.txt'
$versionProcess = Start-Process -FilePath $extracted -ArgumentList '--version' -NoNewWindow -PassThru -Wait -RedirectStandardOutput $versionOutputPath -RedirectStandardError $versionErrorPath
if ($versionProcess.ExitCode -ne 0) { Fail 'extracted --version failed' }
$expectedVersionBytes = [Text.Encoding]::UTF8.GetBytes("cn-mon $version ($revision)`n")
$actualVersionBytes = [IO.File]::ReadAllBytes($versionOutputPath)
$versionBytesMatch = $actualVersionBytes.Length -eq $expectedVersionBytes.Length
if ($versionBytesMatch) {
    for ($i = 0; $i -lt $expectedVersionBytes.Length; $i++) {
        if ($actualVersionBytes[$i] -ne $expectedVersionBytes[$i]) { $versionBytesMatch = $false; break }
    }
}
if (-not $versionBytesMatch) { Fail 'extracted --version output does not match exact UTF-8 release identity bytes' }
if ((Test-Path -LiteralPath $versionErrorPath) -and (Get-Item -LiteralPath $versionErrorPath).Length -ne 0) { Fail 'extracted --version wrote unexpected stderr bytes' }

$env:CNMON_NATIVE_COMMAND_BINARY = [IO.Path]::GetFullPath($extracted)
$env:CNMON_RELEASE_EXPECTED_SHA256 = Get-SHA256 $extracted
$env:CNMON_NATIVE_ARTIFACT_DIR = Join-Path $artifactDir 'native-probe'
$env:CNMON_NATIVE_COMMAND_ARTIFACT_DIR = Join-Path $artifactDir 'native-command'
& .\scripts\test-native-windows.ps1
if ($LASTEXITCODE -ne 0) { Fail "native Windows probe gate failed with exit $LASTEXITCODE" }
& .\scripts\test-native-command-windows.ps1
if ($LASTEXITCODE -ne 0) { Fail "native Windows command gate failed with exit $LASTEXITCODE" }

$archiveAfter = Get-SHA256 $archive
$binaryAfter = Get-SHA256 $extracted
if ($archiveAfter -cne $archiveSHA) { Fail 'archive changed during native qualification' }
if ($binaryAfter -cne $env:CNMON_RELEASE_EXPECTED_SHA256) { Fail 'extracted binary changed during native qualification' }

$commandHarnessLog = Join-Path $env:CNMON_NATIVE_COMMAND_ARTIFACT_DIR 'windows-command-harness.log'
if (-not (Test-Path -LiteralPath $commandHarnessLog -PathType Leaf)) { Fail 'Windows command gate did not retain its result log' }
$elevatedMarkers = @(Select-String -LiteralPath $commandHarnessLog -Pattern 'CNMON_NATIVE_COMMAND_WINDOWS_RESULT role=elevated .*')
$ordinaryMarkers = @(Select-String -LiteralPath $commandHarnessLog -Pattern 'CNMON_NATIVE_COMMAND_WINDOWS_RESULT role=ordinary .*')
if ($elevatedMarkers.Count -ne 1 -or $ordinaryMarkers.Count -ne 1) { Fail 'Windows permission outcome markers are missing or duplicated' }
$elevatedOutcome = $elevatedMarkers[0].Line -replace '^.*?(CNMON_NATIVE_COMMAND_WINDOWS_RESULT .*)$', '$1'
$ordinaryOutcome = $ordinaryMarkers[0].Line -replace '^.*?(CNMON_NATIVE_COMMAND_WINDOWS_RESULT .*)$', '$1'
if ($elevatedOutcome -notmatch '(icmp=(?:OK|ERROR) exit=\d+ tcp=open json=pass history=one-round)') { Fail 'elevated Windows role result is not a recognized witnessed outcome' }
$elevatedOutcome = $Matches[1]
if ($ordinaryOutcome -notmatch '(icmp=(?:OK|ERROR) exit=\d+ tcp=open json=pass history=one-round)') { Fail 'ordinary Windows role result is not a recognized witnessed outcome' }
$ordinaryOutcome = $Matches[1]

$summary = [ordered]@{
    version = $version; revision = $revision; target = 'windows_amd64'; commit_unix = [long]$commitUnix
    archive = [IO.Path]::GetFileName($archive); archive_sha256 = $archiveSHA; binary_sha256 = $binaryAfter
    binary_path = [IO.Path]::GetFullPath($extracted); go = $goVersion; os = $env:ImageOS; os_image = $env:ImageVersion
    windows_elevated_role = $elevatedOutcome; windows_ordinary_role = $ordinaryOutcome
    native_probe_log = 'native-probe/windows-harness.log'; native_command_log = 'native-command/windows-command-harness.log'
}
Write-UTF8 (Join-Path $artifactDir 'qualification.json') (($summary | ConvertTo-Json -Depth 5) + "`n")
Write-Host "CNMON_RELEASE_QUALIFICATION target=windows_amd64 revision=$revision archive_sha256=$archiveSHA binary_sha256=$binaryAfter native_probe=pass native_command=pass immutable=pass"
