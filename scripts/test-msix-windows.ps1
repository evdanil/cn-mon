[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][ValidateSet('Hosted', 'User', 'Certify')][string]$Mode,
    [Parameter(Mandatory = $true)][string]$PackagePath,
    [Parameter(Mandatory = $true)][string]$ReceiptPath,
    [Parameter(Mandatory = $true)][string]$SdkBinDirectory,
    [Parameter(Mandatory = $true)][string]$ArtifactDirectory,
    [string]$ExpectedSid,
    [switch]$FailAfterInstall
)

$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false
$script:Result = [ordered]@{
    schema_version = 1
    mode = $Mode
    overall = 'incomplete'
    gates = [ordered]@{}
}
$script:AllowResultWrite = $true
$qualificationGates = @('identity', 'package_hash', 'signature', 'account', 'package_registration', 'exact_identity', 'installed_binary_hash', 'alias_identity', 'native_cli', 'icmp', 'history_config', 'shell_aliases', 'upgrade', 'repair', 'uninstall_reinstall', 'conflicts', 'fail_injection', 'cleanup', 'wack')
foreach ($gate in $qualificationGates) { $script:Result.gates[$gate] = 'incomplete' }
$script:CurrentGate = 'identity'
$script:Failure = $false
$script:InjectedFailure = $false
$script:GateUpdates = [ordered]@{}
$script:OwnedPackageSpecs = [Collections.Generic.List[object]]::new()
$script:CreatedCandidate = $false
$script:CreatedReceipt = $false
$script:SignedOutputsReady = $false
$script:CreatedFixture = $false
$script:CreatedFixtureFiles = @()
$script:CreatedCertificate = $null
$script:CreatedTrustedCertificate = $false
$script:CreatedUser = $null
$script:CreatedProfile = $null
$script:OriginalArtifactAcl = $null
$script:PrivateRoot = $null
$script:IdentityName = $null
$script:OwnedProfileFiles = @()

$repositoryRoot = Split-Path -Parent $PSScriptRoot
$modulePath = Join-Path $PSScriptRoot 'msix-package.psm1'
Import-Module $modulePath -Force -ErrorAction Stop

function Set-CnMonMsixGate {
    param([string]$Name, [ValidateSet('pass', 'fail', 'incomplete')][string]$Status)
    $script:Result.gates[$Name] = $Status
    $script:GateUpdates[$Name] = $Status
}

function Write-CnMonMsixResult {
    param([string]$Directory)
    if (-not (Test-Path -LiteralPath $Directory -PathType Container)) {
        return
    }
    $path = Join-Path $Directory 'msix-native-results.json'
    $json = ConvertTo-Json -InputObject $script:Result -Depth 8
    [IO.File]::WriteAllText($path, $json + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
}

function Set-CnMonMsixOverall {
    param([string]$ForMode)
    $required = switch ($ForMode) {
        'Hosted' { @('identity', 'package_hash', 'signature', 'account', 'package_registration', 'exact_identity', 'installed_binary_hash', 'alias_identity', 'native_cli', 'icmp', 'history_config', 'shell_aliases', 'upgrade', 'repair', 'uninstall_reinstall', 'conflicts', 'fail_injection', 'cleanup') }
        'User' { @('identity', 'package_hash', 'signature', 'account', 'package_registration', 'exact_identity', 'installed_binary_hash', 'alias_identity', 'native_cli', 'icmp', 'history_config', 'shell_aliases', 'upgrade', 'repair', 'uninstall_reinstall', 'conflicts', 'cleanup') }
        'Certify' { @('identity', 'package_hash', 'signature', 'account', 'package_registration', 'exact_identity', 'installed_binary_hash', 'alias_identity', 'native_cli', 'icmp', 'history_config', 'shell_aliases', 'upgrade', 'repair', 'uninstall_reinstall', 'conflicts', 'fail_injection', 'cleanup', 'wack') }
    }
    $statuses = @($required | ForEach-Object { if ($script:Result.gates.Contains($_)) { $script:Result.gates[$_] } else { 'incomplete' } })
    if ($statuses -contains 'fail') {
        $script:Result.overall = 'fail'
    }
    elseif ($statuses -contains 'incomplete') {
        $script:Result.overall = 'incomplete'
    }
    else {
        $script:Result.overall = 'pass'
    }
}

function Write-CnMonMsixStatus {
    param([string]$Message)
    Write-Host "MSIX qualification: $Message"
}

function Assert-CnMonMsixWindows {
    if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
        $script:CurrentGate = 'account'
        Set-CnMonMsixGate 'account' 'incomplete'
        throw 'Windows host required'
    }
}

function Get-CnMonMsixReceipt {
    param([string]$Path)
    try {
        return Get-Content -LiteralPath $Path -Raw | ConvertFrom-Json -AsHashtable -ErrorAction Stop
    }
    catch {
        throw 'receipt could not be parsed'
    }
}

function Assert-CnMonMsixReceipt {
    param(
        [System.Collections.IDictionary]$Receipt,
        [string]$Path,
        [ValidateSet('unsigned', 'test')][string]$SigningStatus,
        [switch]$RequireCandidate
    )
    $script:CurrentGate = 'identity'
    $identity = Read-CnMonMsixIdentity
    if ([int]$Receipt.schema_version -ne 1 -or
        [string]$Receipt.name -cne $identity.Name -or
        [string]$Receipt.publisher -cne $identity.Publisher -or
        [string]$Receipt.publisher_display_name -cne $identity.PublisherDisplayName -or
        [string]$Receipt.expected_pfn -cne $identity.ExpectedPFN -or
        [string]$Receipt.architecture -cne 'x64' -or
        [string]$Receipt.package_version -cne '1.3.0.0' -or
        [string]$Receipt.cli_version -cne '0.3.0' -or
        [string]$Receipt.cli_revision -cne 'bd1ac548c22c0c49a54e2ce964a2f0a4da48a1ed' -or
        [string]$Receipt.binary_sha256 -cne '8312916ae18998c6cddecbe31d2a1dd9833f047a5e51da3feccc5ca45b6b234d' -or
        [string]$Receipt.purpose -cne 'candidate' -or
        [string]$Receipt.packaging_revision -notmatch '^[0-9a-f]{40}$' -or
        $null -eq $Receipt.files -or
        [string]$Receipt.manifest_sha256 -notmatch '^[0-9a-f]{64}$') {
        throw 'receipt does not match the approved candidate contract'
    }
    if ($RequireCandidate -and [string]$Receipt.purpose -cne 'candidate') {
        throw 'receipt purpose is not candidate'
    }
    if ($SigningStatus -eq 'unsigned' -and [string]$Receipt.signing -cne 'unsigned') {
        throw 'candidate receipt is not unsigned'
    }
    if ($SigningStatus -eq 'test' -and
        ($null -eq $Receipt.signing -or [string]$Receipt.signing.status -cne 'test' -or
         [string]$Receipt.signing.certificate_thumbprint -notmatch '^[0-9A-Fa-f]{40}$' -or
         [string]$Receipt.signing.unsigned_package_sha256 -notmatch '^[0-9a-f]{64}$' -or
         [string]$Receipt.signing.signed_package_sha256 -notmatch '^[0-9a-f]{64}$')) {
        throw 'signed receipt is invalid'
    }
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw 'package file is missing'
    }
    $script:CurrentGate = 'package_hash'
    $hash = (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($hash -cne ([string]$Receipt.package_sha256).ToLowerInvariant()) {
        throw 'package hash does not match its receipt'
    }
    if ($SigningStatus -eq 'test' -and $hash -cne ([string]$Receipt.signing.signed_package_sha256).ToLowerInvariant()) {
        throw 'signed package hash does not match its receipt'
    }
    if (-not $Receipt.files.Contains('cn-mon.exe') -or
        [string]$Receipt.files['cn-mon.exe'] -cne ([string]$Receipt.binary_sha256).ToLowerInvariant() -or
        [string]$Receipt.files['AppxManifest.xml'] -cne ([string]$Receipt.manifest_sha256).ToLowerInvariant()) {
        throw 'receipt payload hash map is inconsistent'
    }
    Set-CnMonMsixGate 'identity' 'pass'
    Set-CnMonMsixGate 'package_hash' 'pass'
    return $identity
}

function Invoke-CnMonMsixTool {
    param([string]$Executable, [string[]]$Arguments, [string]$LogPath)
    $module = Get-Module -Name 'msix-package'
    $exit = & $module {
        param([string]$Path, [string[]]$ToolArguments, [string]$PrivateLog)
        Invoke-CnMonMsixTool -ExecutablePath $Path -Arguments $ToolArguments -LogPath $PrivateLog
    } $Executable $Arguments $LogPath
    if ($exit -ne 0) {
        throw 'a Windows SDK packaging tool failed; private output was retained'
    }
}

function Assert-CnMonMsixPackagePayload {
    param([string]$Path, [System.Collections.IDictionary]$Files)
    $signatureBlock = Join-Path $Path 'AppxSignature.p7x'
    if (Test-Path -LiteralPath $signatureBlock -PathType Leaf) {
        Remove-Item -LiteralPath $signatureBlock -Force
    }
    msix-package\Assert-CnMonMsixPayload -Root $Path -ExpectedFiles $Files
}

function Get-CnMonMsixFixtureFiles {
    param(
        [System.Collections.IDictionary]$SourceFiles,
        [string]$BinaryHash,
        [string]$ManifestHash,
        [string]$ConfigHash
    )
    if ($null -eq $SourceFiles -or
        -not $SourceFiles.Contains('cn-mon.exe') -or
        -not $SourceFiles.Contains('AppxManifest.xml') -or
        $SourceFiles.Contains('.cn') -or
        $BinaryHash -notmatch '^[0-9a-fA-F]{64}$' -or
        $ManifestHash -notmatch '^[0-9a-fA-F]{64}$' -or
        $ConfigHash -notmatch '^[0-9a-fA-F]{64}$' -or
        ([string]$SourceFiles['cn-mon.exe']).ToLowerInvariant() -cne $BinaryHash.ToLowerInvariant()) {
        throw 'test fixture source or changed file hashes are invalid'
    }

    $files = [ordered]@{}
    foreach ($path in $SourceFiles.Keys) {
        $hash = [string]$SourceFiles[$path]
        if ($hash -notmatch '^[0-9a-fA-F]{64}$') {
            throw 'test fixture source payload contains an invalid hash'
        }
        $files[[string]$path] = $hash.ToLowerInvariant()
    }
    $files['AppxManifest.xml'] = $ManifestHash.ToLowerInvariant()
    $files['.cn'] = $ConfigHash.ToLowerInvariant()
    return $files
}

function Get-CnMonMsixNativeSelector {
    param([string]$Pattern)
    if ($Pattern.StartsWith('Test', [StringComparison]::Ordinal)) {
        return "^$Pattern`$"
    }
    return "^Test($Pattern)`$"
}

function Merge-CnMonMsixResultGates {
    param([System.Collections.IDictionary]$HostedGates, [System.Collections.IDictionary]$CertifyUpdates)
    $merged = [ordered]@{}
    foreach ($key in $HostedGates.Keys) { $merged[$key] = [string]$HostedGates[$key] }
    foreach ($key in $CertifyUpdates.Keys) { $merged[$key] = [string]$CertifyUpdates[$key] }
    return $merged
}

function Get-CnMonMsixWackStatus {
    param([string]$Path, [int]$ExitCode, [string]$ExpectedName, [string]$ExpectedVersion)
    if ($ExitCode -ne 0) { return 'fail' }
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return 'incomplete' }
    try {
        $settings = [Xml.XmlReaderSettings]::new()
        $settings.DtdProcessing = [Xml.DtdProcessing]::Prohibit
        $settings.XmlResolver = $null
        $reader = [Xml.XmlReader]::Create($Path, $settings)
        try {
            $document = [Xml.XmlDocument]::new()
            $document.XmlResolver = $null
            $document.Load($reader)
        }
        finally { $reader.Dispose() }
    }
    catch { return 'incomplete' }

    $report = $document.DocumentElement
    if ($null -eq $report -or $report.LocalName -cne 'REPORT') { return 'incomplete' }
    $overall = $report.GetAttribute('OVERALL_RESULT').Trim().ToUpperInvariant()
    if ($overall -in @('FAIL', 'FAILED', 'WARNING', 'WARN')) { return 'fail' }
    if ($overall -cne 'PASS') { return 'incomplete' }
    if ([string]::IsNullOrWhiteSpace($ExpectedName) -or [string]::IsNullOrWhiteSpace($ExpectedVersion) -or
        [string]::IsNullOrWhiteSpace($report.GetAttribute('APP_NAME')) -or
        [string]::IsNullOrWhiteSpace($report.GetAttribute('APP_VERSION'))) { return 'incomplete' }
    if ($report.GetAttribute('APP_NAME') -cne $ExpectedName -or $report.GetAttribute('APP_VERSION') -cne $ExpectedVersion) { return 'fail' }
    if ($report.GetAttribute('PARTIAL_RUN').Trim().ToUpperInvariant() -cne 'FALSE') { return 'incomplete' }

    $tests = @($report.SelectNodes(".//*[local-name()='TEST']"))
    if ($tests.Count -eq 0) { return 'incomplete' }
    foreach ($test in $tests) {
        $results = @($test.SelectNodes(".//*[local-name()='RESULT']"))
        if ($results.Count -eq 0) { return 'incomplete' }
        foreach ($result in $results) {
            $value = $result.InnerText.Trim().ToUpperInvariant()
            if ($value -in @('FAIL', 'FAILED', 'WARNING', 'WARN')) { return 'fail' }
            if ($value -notin @('PASS', 'PASSED')) { return 'incomplete' }
        }
    }

    # The installed WACK report schema has not yet been measured on a supported
    # Windows host. Inspect explicit failures, but do not treat guessed result
    # node names as sufficient evidence for a complete qualification.
    return 'incomplete'
}

function Get-CnMonMsixTrustAction {
    param([bool]$HasTrustedCertificate, [bool]$FreshHostedRunner)
    if ($HasTrustedCertificate) { return 'reuse' }
    if ($FreshHostedRunner) { return 'import' }
    return 'incomplete'
}

function Get-CnMonMsixProfileTargets {
    param([string]$ProfilePath, [string]$LocalAppData, [string]$FixtureHistory, [string]$FixtureLog)
    if ([string]::IsNullOrWhiteSpace($ProfilePath) -or [string]::IsNullOrWhiteSpace($LocalAppData)) {
        throw 'actual user profile paths are required'
    }
    $targets = [ordered]@{
        ProfileConfig = Join-Path $ProfilePath '.cn'
        ProfileHistoryDefault = Join-Path $ProfilePath '.cn-ping-history.db'
        ProfileLogDefault = Join-Path $ProfilePath 'cn.log'
        ProfileHistory = Join-Path $LocalAppData 'msix-profile-history.db'
        ProfileLog = Join-Path $LocalAppData 'msix-profile.log'
        CliHistory = Join-Path $LocalAppData 'msix-cli-history.db'
        CliLog = Join-Path $LocalAppData 'msix-cli.log'
        FixtureHistory = $FixtureHistory
        FixtureLog = $FixtureLog
    }
    $all = @($targets.Values | Where-Object { -not [string]::IsNullOrWhiteSpace([string]$_) })
    $targets.All = $all
    return [pscustomobject]$targets
}

function Assert-CnMonMsixTargetsAbsent {
    param([string[]]$Paths)
    foreach ($path in $Paths) {
        if (Test-Path -LiteralPath $path) {
            throw 'qualification profile or fixture contains pre-existing application data'
        }
    }
}

function Add-CnMonMsixOwnedPackage {
    param([object]$Identity, [string]$Version, [string]$PackagePath, [Collections.Generic.List[object]]$Ownership)
    $name = [string]$Identity.Name
    $publisher = [string]$Identity.Publisher
    $family = [string]$Identity.ExpectedPFN
    $matchingSpecs = @($Ownership | Where-Object {
        $_.Name -ceq $name -and $_.Publisher -ceq $publisher -and
        $_.PackageFamilyName -ceq $family -and $_.Version -ceq $Version
    } | Select-Object -First 1)
    if ($matchingSpecs.Count -eq 0) {
        $spec = [pscustomobject]@{
            Name = $name
            Publisher = $publisher
            PackageFamilyName = $family
            Version = $Version
            PackageFullNames = [Collections.Generic.List[string]]::new()
        }
        $Ownership.Add($spec)
    }
    else { $spec = $matchingSpecs[0] }
    $null = Add-AppxPackage -Path $PackagePath -ErrorAction Stop
    $packages = @(Get-AppxPackage -Name $name -ErrorAction Stop | Where-Object {
        [string]$_.Publisher -ceq $publisher -and
        [string]$_.PackageFamilyName -ceq $family -and
        [string]$_.Version -ceq $Version
    })
    foreach ($package in $packages) {
        if ([string]$package.PackageFullName -and $package.PackageFullName -notin $spec.PackageFullNames) {
            $spec.PackageFullNames.Add([string]$package.PackageFullName)
        }
    }
    if ($packages.Count -eq 0) { throw 'new package registration could not be identified for cleanup ownership' }
}

function Remove-CnMonMsixOwnedPackages {
    param([Collections.Generic.List[object]]$Ownership)
    $cleanupFailed = $false
    foreach ($spec in $Ownership) {
        try {
            $packages = @(Get-AppxPackage -Name $spec.Name -ErrorAction Stop | Where-Object {
                [string]$_.Publisher -ceq $spec.Publisher -and
                [string]$_.PackageFamilyName -ceq $spec.PackageFamilyName -and
                [string]$_.Version -ceq $spec.Version -and
                ($spec.PackageFullNames.Count -eq 0 -or [string]$_.PackageFullName -in $spec.PackageFullNames)
            })
            foreach ($package in $packages) {
                $null = Remove-AppxPackage -Package $package.PackageFullName -ErrorAction Stop
            }
            $remaining = @(Get-AppxPackage -Name $spec.Name -ErrorAction Stop | Where-Object {
                [string]$_.Publisher -ceq $spec.Publisher -and
                [string]$_.PackageFamilyName -ceq $spec.PackageFamilyName -and
                [string]$_.Version -ceq $spec.Version -and
                ($spec.PackageFullNames.Count -eq 0 -or [string]$_.PackageFullName -in $spec.PackageFullNames)
            })
            if ($remaining.Count -gt 0) { throw 'owned package remains registered' }
        }
        catch { $cleanupFailed = $true }
    }
    if ($cleanupFailed) { throw 'one or more owned package registrations could not be removed' }
}

function Assert-CnMonMsixRetainedFiles {
    param([System.Collections.IDictionary]$ExpectedHashes)
    foreach ($path in $ExpectedHashes.Keys) {
        if (-not (Test-Path -LiteralPath $path -PathType Leaf) -or
            (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant() -cne [string]$ExpectedHashes[$path]) {
            throw 'profile or fixture data changed during package lifecycle operation'
        }
    }
}

function Test-CnMonMsixAdministratorsMembership {
    param([string[]]$GroupSids)
    return [bool](@($GroupSids | Where-Object { $_ -ceq 'S-1-5-32-544' }).Count -gt 0)
}

function Remove-CnMonMsixOwnedCertificate {
    param([string]$Thumbprint, [bool]$RemoveTrustedCopy, [bool]$DeletePrivateKey)
    if (-not $Thumbprint) { return }
    $cleanupErrors = [Collections.Generic.List[Exception]]::new()
    if ($RemoveTrustedCopy) {
        try { Remove-Item -LiteralPath "Cert:\LocalMachine\TrustedPeople\$Thumbprint" -Force -ErrorAction Stop }
        catch { $cleanupErrors.Add($_.Exception) }
    }
    if ($DeletePrivateKey) {
        try { Remove-Item -LiteralPath "Cert:\LocalMachine\My\$Thumbprint" -Force -DeleteKey -ErrorAction Stop }
        catch { $cleanupErrors.Add($_.Exception) }
    }
    if ($cleanupErrors.Count -gt 0) { throw 'one or more owned certificate objects could not be removed' }
}

function Assert-CnMonMsixSignature {
    param([string]$Path, [string]$LogPath)
    $script:CurrentGate = 'signature'
    $signtool = Join-Path $SdkBinDirectory 'signtool.exe'
    if (-not (Test-Path -LiteralPath $signtool -PathType Leaf)) {
        Set-CnMonMsixGate 'signature' 'incomplete'
        throw 'SignTool is unavailable'
    }
    Invoke-CnMonMsixTool $signtool @('verify', '/pa', '/v', $Path) $LogPath
    Set-CnMonMsixGate 'signature' 'pass'
}

function Assert-CnMonMsixOrdinaryUser {
    param([string]$Sid)
    $script:CurrentGate = 'account'
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = [Security.Principal.WindowsPrincipal]::new($identity)
    $groupSids = @($identity.Groups | ForEach-Object { $_.Value })
    if ($identity.User.Value -cne $Sid -or
        $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator) -or
        (Test-CnMonMsixAdministratorsMembership $groupSids) -or
        [string]::IsNullOrWhiteSpace($env:USERPROFILE) -or
        [string]::IsNullOrWhiteSpace($env:LOCALAPPDATA)) {
        throw 'current process is not the expected ordinary user with a loaded profile'
    }
    $profile = Get-CimInstance -ClassName Win32_UserProfile -Filter "SID='$Sid'" -ErrorAction Stop
    if ($null -eq $profile -or
        [IO.Path]::GetFullPath($profile.LocalPath) -ine [IO.Path]::GetFullPath($env:USERPROFILE)) {
        throw 'current environment does not identify the actual loaded user profile'
    }
    Set-CnMonMsixGate 'account' 'pass'
}

function Get-CnMonMsixPackage {
    param([object]$Identity)
    $packages = @(Get-AppxPackage -Name ([string]$Identity.Name) -ErrorAction Stop)
    return $packages | Where-Object {
        [string]$_.Publisher -ceq [string]$Identity.Publisher -and
        [string]$_.PackageFamilyName -ceq [string]$Identity.ExpectedPFN
    } | Sort-Object { [version]$_.Version } -Descending | Select-Object -First 1
}

function Assert-CnMonMsixInstalledPackage {
    param([System.Collections.IDictionary]$Receipt, [object]$Identity)
    $script:CurrentGate = 'package_registration'
    $package = Get-CnMonMsixPackage $Identity
    if ($null -eq $package) {
        throw 'candidate package is not registered for the current user'
    }
    Set-CnMonMsixGate 'package_registration' 'pass'
    $script:CurrentGate = 'exact_identity'
    if ([string]$package.Name -cne [string]$Identity.Name -or
        [string]$package.Publisher -cne [string]$Identity.Publisher -or
        [string]$package.Architecture -cne 'X64' -or
        [string]$package.Version -cne [string]$Receipt.package_version -or
        [string]$package.PackageFamilyName -cne [string]$Identity.ExpectedPFN) {
        throw 'registered package identity or version does not match the signed receipt'
    }
    Set-CnMonMsixGate 'exact_identity' 'pass'
    $executable = Join-Path $package.InstallLocation 'cn-mon.exe'
    $script:CurrentGate = 'installed_binary_hash'
    if (-not (Test-Path -LiteralPath $executable -PathType Leaf) -or
        (Get-FileHash -LiteralPath $executable -Algorithm SHA256).Hash.ToLowerInvariant() -cne ([string]$Receipt.binary_sha256).ToLowerInvariant()) {
        throw 'installed executable hash does not match the signed receipt'
    }
    Set-CnMonMsixGate 'installed_binary_hash' 'pass'
    return $package
}

function Invoke-CnMonMsixNativeTests {
    param([string]$Name, [string]$TestExe, [string]$Pattern, [string[]]$RequiredMarkers)
    $stdout = Join-Path $ArtifactDirectory "msix-$Name.stdout.log"
    $stderr = Join-Path $ArtifactDirectory "msix-$Name.stderr.log"
    $listStdout = Join-Path $ArtifactDirectory "msix-$Name.tests.log"
    $listStderr = Join-Path $ArtifactDirectory "msix-$Name.tests-error.log"
    $selector = Get-CnMonMsixNativeSelector $Pattern
    & $TestExe "-test.list=$selector" 1> $listStdout 2> $listStderr
    $listExit = $LASTEXITCODE
    $selectedTests = @()
    if (Test-Path -LiteralPath $listStdout) { $selectedTests = @(Get-Content -LiteralPath $listStdout | Where-Object { -not [string]::IsNullOrWhiteSpace($_) }) }
    if ($listExit -ne 0 -or $selectedTests.Count -eq 0) {
        throw "native Windows qualification phase '$Name' selected no Go tests"
    }
    Remove-Item -LiteralPath $stdout, $stderr -Force -ErrorAction SilentlyContinue
    & $TestExe '-test.v' "-test.run=$selector" '-test.count=1' '-test.timeout=10m' 1> $stdout 2> $stderr
    $exit = $LASTEXITCODE
    $output = ''
    if (Test-Path -LiteralPath $stdout) { $output += Get-Content -LiteralPath $stdout -Raw }
    if (Test-Path -LiteralPath $stderr) { $output += Get-Content -LiteralPath $stderr -Raw }
    if ($exit -ne 0) {
        throw "native Windows qualification phase '$Name' failed; private output was retained"
    }
    foreach ($marker in $RequiredMarkers) {
        if ($output -notmatch [regex]::Escape($marker)) {
            throw "native Windows qualification phase '$Name' omitted a required witness"
        }
    }
    return $output
}

function Invoke-CnMonMsixShell {
    param([string]$FilePath, [string[]]$Arguments, [hashtable]$Environment, [string]$LogPrefix, [string]$StandardInput, [string]$WorkingDirectory)
    $info = [Diagnostics.ProcessStartInfo]::new()
    $info.FileName = $FilePath
    $info.UseShellExecute = $false
    $info.RedirectStandardInput = $null -ne $StandardInput
    $info.RedirectStandardOutput = $true
    $info.RedirectStandardError = $true
    if ($WorkingDirectory) { $info.WorkingDirectory = $WorkingDirectory }
    foreach ($argument in $Arguments) { $info.ArgumentList.Add($argument) }
    if ($Environment) {
        foreach ($key in $Environment.Keys) { $info.Environment[$key] = [string]$Environment[$key] }
    }
    $process = [Diagnostics.Process]::Start($info)
    try {
        $outTask = $process.StandardOutput.ReadToEndAsync()
        $errTask = $process.StandardError.ReadToEndAsync()
        if ($null -ne $StandardInput) {
            $process.StandardInput.Write($StandardInput)
            $process.StandardInput.Close()
        }
        if (-not $process.WaitForExit(60000)) {
            try { $process.Kill($true) } catch { }
            throw 'shell alias check timed out'
        }
        [Threading.Tasks.Task]::WaitAll([Threading.Tasks.Task[]]@($outTask, $errTask))
        [IO.File]::WriteAllText("$LogPrefix.stdout.log", $outTask.Result, [Text.UTF8Encoding]::new($false))
        [IO.File]::WriteAllText("$LogPrefix.stderr.log", $errTask.Result, [Text.UTF8Encoding]::new($false))
        return [pscustomobject]@{ ExitCode = $process.ExitCode; Stdout = $outTask.Result; Stderr = $errTask.Result }
    }
    finally { $process.Dispose() }
}

function Assert-CnMonMsixExpectedNativeRejection {
    param([string]$Name, [string]$TestExe, [string]$Pattern, [string]$ExpectedReason = 'alias-selection|package-identity')
    $stdout = Join-Path $ArtifactDirectory "msix-$Name.stdout.log"
    $stderr = Join-Path $ArtifactDirectory "msix-$Name.stderr.log"
    $selector = Get-CnMonMsixNativeSelector $Pattern
    $listStdout = Join-Path $ArtifactDirectory "msix-$Name.tests.log"
    $listStderr = Join-Path $ArtifactDirectory "msix-$Name.tests-error.log"
    & $TestExe "-test.list=$selector" 1> $listStdout 2> $listStderr
    $selectedTests = @()
    if (Test-Path -LiteralPath $listStdout) { $selectedTests = @(Get-Content -LiteralPath $listStdout | Where-Object { -not [string]::IsNullOrWhiteSpace($_) }) }
    if ($LASTEXITCODE -ne 0 -or $selectedTests.Count -eq 0) {
        throw "negative control '$Name' selected no Go tests"
    }
    & $TestExe '-test.v' "-test.run=$selector" '-test.count=1' 1> $stdout 2> $stderr
    $output = ''
    if (Test-Path -LiteralPath $stdout) { $output += Get-Content -LiteralPath $stdout -Raw }
    if (Test-Path -LiteralPath $stderr) { $output += Get-Content -LiteralPath $stderr -Raw }
    if ($LASTEXITCODE -eq 0 -or $output -notmatch "CNMON_NATIVE_MSIX_REJECTED reason=($ExpectedReason)") {
        throw "negative control '$Name' did not prove that the package alias was unavailable"
    }
}

function New-CnMonMsixFixture {
    param([string]$UnsignedPackage, [System.Collections.IDictionary]$Receipt, [object]$Identity, [string]$Destination, [string]$FixtureReceiptPath, [string]$HistoryPath, [string]$LogPath)
    $script:CurrentGate = 'upgrade'
    $makeappx = Join-Path $SdkBinDirectory 'makeappx.exe'
    $stage = Join-Path $script:PrivateRoot 'fixture-stage'
    $unpacked = Join-Path $script:PrivateRoot 'candidate-unpacked'
    New-Item -ItemType Directory -Path $unpacked | Out-Null
    Invoke-CnMonMsixTool $makeappx @('unpack', '/p', $UnsignedPackage, '/d', $unpacked, '/no') (Join-Path $script:PrivateRoot 'fixture-source-unpack.log')
    Assert-CnMonMsixPackagePayload $unpacked $Receipt.files
    New-Item -ItemType Directory -Path $stage | Out-Null
    Get-ChildItem -LiteralPath $unpacked -Force | ForEach-Object {
        Copy-Item -LiteralPath $_.FullName -Destination $stage -Recurse
    }
    $manifest = Join-Path $stage 'AppxManifest.xml'
    $xml = [xml](Get-Content -LiteralPath $manifest -Raw)
    $identityNode = $xml.SelectSingleNode('//*[local-name()="Identity"]')
    if ($null -eq $identityNode -or $identityNode.GetAttribute('Name') -cne [string]$Identity.Name -or
        $identityNode.GetAttribute('Publisher') -cne [string]$Identity.Publisher) {
        throw 'candidate manifest identity changed before fixture generation'
    }
    $identityNode.SetAttribute('Version', '1.2.999.0')
    $xml.Save($manifest)
    $config = "[ping]`nhistory_file = $HistoryPath`n[logging]`nlogfile = $LogPath`nlevel = INFO`n"
    [IO.File]::WriteAllText((Join-Path $stage '.cn'), $config, [Text.UTF8Encoding]::new($false))
    Invoke-CnMonMsixTool $makeappx @('pack', '/d', $stage, '/p', $Destination, '/no') (Join-Path $script:PrivateRoot 'fixture-pack.log')
    $signtool = Join-Path $SdkBinDirectory 'signtool.exe'
    Invoke-CnMonMsixTool $signtool @('sign', '/fd', 'SHA256', '/s', 'My', '/sm', '/sha1', [string]$script:CreatedCertificate, $Destination) (Join-Path $script:PrivateRoot 'fixture-sign.log')
    Invoke-CnMonMsixTool $signtool @('verify', '/pa', '/v', $Destination) (Join-Path $script:PrivateRoot 'fixture-verify.log')

    $binaryHash = (Get-FileHash -LiteralPath (Join-Path $stage 'cn-mon.exe') -Algorithm SHA256).Hash.ToLowerInvariant()
    $manifestHash = (Get-FileHash -LiteralPath $manifest -Algorithm SHA256).Hash.ToLowerInvariant()
    $configPath = Join-Path $stage '.cn'
    $configHash = (Get-FileHash -LiteralPath $configPath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($binaryHash -cne ([string]$Receipt.binary_sha256).ToLowerInvariant()) {
        throw 'fixture binary provenance does not match the original candidate receipt'
    }
    $fixtureFiles = Get-CnMonMsixFixtureFiles -SourceFiles $Receipt.files -BinaryHash $binaryHash -ManifestHash $manifestHash -ConfigHash $configHash
    $fixtureUnpacked = Join-Path $script:PrivateRoot 'fixture-signed-unpacked'
    New-Item -ItemType Directory -Path $fixtureUnpacked | Out-Null
    Invoke-CnMonMsixTool $makeappx @('unpack', '/p', $Destination, '/d', $fixtureUnpacked, '/no') (Join-Path $script:PrivateRoot 'fixture-signed-unpack.log')
    Assert-CnMonMsixPackagePayload $fixtureUnpacked $fixtureFiles
    $packageHash = (Get-FileHash -LiteralPath $Destination -Algorithm SHA256).Hash.ToLowerInvariant()
    $fixtureReceipt = [ordered]@{
        schema_version = 1
        purpose = 'test-fixture'
        source_package_sha256 = ([string]$Receipt.package_sha256).ToLowerInvariant()
        source_manifest_sha256 = ([string]$Receipt.manifest_sha256).ToLowerInvariant()
        binary_sha256 = $binaryHash
        name = [string]$Identity.Name
        publisher = [string]$Identity.Publisher
        expected_pfn = [string]$Identity.ExpectedPFN
        architecture = 'x64'
        package_version = '1.2.999.0'
        manifest_sha256 = $manifestHash
        config_sha256 = $configHash
        package_sha256 = $packageHash
        files = $fixtureFiles
        signing = [ordered]@{
            status = 'test'
            certificate_thumbprint = [string]$script:CreatedCertificate
            signed_package_sha256 = $packageHash
        }
    }
    [IO.File]::WriteAllText($FixtureReceiptPath, (ConvertTo-Json -InputObject $fixtureReceipt -Depth 8) + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
}

function Assert-CnMonMsixFixtureReceipt {
    param([string]$PackagePath, [System.Collections.IDictionary]$FixtureReceipt, [System.Collections.IDictionary]$CandidateReceipt, [object]$Identity, [object]$InstalledPackage)
    $sourcePackageHash = if ([string]$CandidateReceipt.signing.status -ceq 'test') {
        [string]$CandidateReceipt.signing.unsigned_package_sha256
    }
    else { [string]$CandidateReceipt.package_sha256 }
    if ([int]$FixtureReceipt.schema_version -ne 1 -or
        [string]$FixtureReceipt.purpose -cne 'test-fixture' -or
        [string]$FixtureReceipt.name -cne [string]$Identity.Name -or
        [string]$FixtureReceipt.publisher -cne [string]$Identity.Publisher -or
        [string]$FixtureReceipt.expected_pfn -cne [string]$Identity.ExpectedPFN -or
        [string]$FixtureReceipt.architecture -cne 'x64' -or
        [string]$FixtureReceipt.package_version -cne '1.2.999.0' -or
        [string]$FixtureReceipt.source_package_sha256 -cne $sourcePackageHash.ToLowerInvariant() -or
        [string]$FixtureReceipt.source_manifest_sha256 -cne ([string]$CandidateReceipt.manifest_sha256).ToLowerInvariant() -or
        [string]$FixtureReceipt.binary_sha256 -cne ([string]$CandidateReceipt.binary_sha256).ToLowerInvariant() -or
        [string]$FixtureReceipt.signing.status -cne 'test' -or
        [string]$FixtureReceipt.signing.certificate_thumbprint -cne [string]$CandidateReceipt.signing.certificate_thumbprint -or
        [string]$FixtureReceipt.package_sha256 -notmatch '^[0-9a-f]{64}$' -or
        [string]$FixtureReceipt.manifest_sha256 -notmatch '^[0-9a-f]{64}$' -or
        [string]$FixtureReceipt.config_sha256 -notmatch '^[0-9a-f]{64}$') {
        throw 'test fixture receipt does not match the signed candidate provenance'
    }
    if ([string]$FixtureReceipt.package_sha256 -cne ([string]$FixtureReceipt.signing.signed_package_sha256).ToLowerInvariant() -or
        (Get-FileHash -LiteralPath $PackagePath -Algorithm SHA256).Hash.ToLowerInvariant() -cne [string]$FixtureReceipt.package_sha256) {
        throw 'test fixture package hash does not match its private receipt'
    }
    foreach ($relativePath in $FixtureReceipt.files.Keys) {
        $expectedHash = ([string]$FixtureReceipt.files[$relativePath]).ToLowerInvariant()
        if ($expectedHash -notmatch '^[0-9a-f]{64}$') { throw 'qualification fixture file hash is malformed' }
    }
    if ([string]$FixtureReceipt.files['cn-mon.exe'] -cne ([string]$CandidateReceipt.binary_sha256).ToLowerInvariant() -or
        [string]$FixtureReceipt.files['AppxManifest.xml'] -cne ([string]$FixtureReceipt.manifest_sha256).ToLowerInvariant() -or
        [string]$FixtureReceipt.files['.cn'] -cne ([string]$FixtureReceipt.config_sha256).ToLowerInvariant()) {
        throw 'qualification fixture file hash map is inconsistent'
    }
    if ($null -ne $InstalledPackage) {
        if ([string]$InstalledPackage.Name -cne [string]$FixtureReceipt.name -or
            [string]$InstalledPackage.Publisher -cne [string]$FixtureReceipt.publisher -or
            [string]$InstalledPackage.PackageFamilyName -cne [string]$FixtureReceipt.expected_pfn -or
            [string]$InstalledPackage.Architecture -cne 'X64' -or
            [string]$InstalledPackage.Version -cne [string]$FixtureReceipt.package_version) {
            throw 'registered lower-version fixture identity does not match its private receipt'
        }
        foreach ($relativePath in $FixtureReceipt.files.Keys) {
            $installedPath = Join-Path $InstalledPackage.InstallLocation $relativePath
            if (-not (Test-Path -LiteralPath $installedPath -PathType Leaf) -or
                (Get-FileHash -LiteralPath $installedPath -Algorithm SHA256).Hash.ToLowerInvariant() -cne ([string]$FixtureReceipt.files[$relativePath]).ToLowerInvariant()) {
                throw 'installed qualification fixture payload does not match its private receipt'
            }
        }
    }
}

function New-CnMonMsixTemporaryUser {
    $script:CurrentGate = 'account'
    $name = 'cmsx' + [Guid]::NewGuid().ToString('N').Substring(0, 12)
    $random = [Convert]::ToBase64String([Security.Cryptography.RandomNumberGenerator]::GetBytes(36)) + 'aA1!'
    $password = ConvertTo-SecureString $random -AsPlainText -Force
    $user = New-LocalUser -Name $name -Password $password -AccountNeverExpires -PasswordNeverExpires -ErrorAction Stop
    $script:CreatedUser = $name
    $credential = [Management.Automation.PSCredential]::new("$env:COMPUTERNAME\$name", $password)
    $pwsh = Join-Path $PSHOME 'pwsh.exe'
    $initializeOut = Join-Path $script:PrivateRoot 'profile-init.stdout.log'
    $initializeErr = Join-Path $script:PrivateRoot 'profile-init.stderr.log'
    $process = Start-Process -FilePath $pwsh -Credential $credential -LoadUserProfile -WorkingDirectory $repositoryRoot -ArgumentList '-NoProfile -Command "exit 0"' -PassThru -RedirectStandardOutput $initializeOut -RedirectStandardError $initializeErr
    if (-not $process.WaitForExit(60000) -or $process.ExitCode -ne 0) {
        if (-not $process.HasExited) { Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue }
        throw 'temporary ordinary-user profile could not be initialized'
    }
    $sid = $user.SID.Value
    $profile = Get-CimInstance -ClassName Win32_UserProfile -Filter "SID='$sid'" -ErrorAction Stop
    if ($null -eq $profile -or -not (Test-Path -LiteralPath $profile.LocalPath -PathType Container)) {
        throw 'temporary ordinary-user profile was not created'
    }
    $script:CreatedProfile = [pscustomobject]@{ SID = $sid; Path = [IO.Path]::GetFullPath($profile.LocalPath) }
    return [pscustomobject]@{ Name = $name; SID = $sid; Credential = $credential; Profile = $script:CreatedProfile.Path }
}

function Invoke-CnMonMsixUserRun {
    param([pscustomobject]$User, [string]$Candidate, [string]$SignedReceipt, [string]$Fixture, [switch]$InjectFailure)
    $script:CurrentGate = 'account'
    $profile = $User.Profile
    $local = Join-Path $profile 'AppData/Local'
    $roaming = Join-Path $profile 'AppData/Roaming'
    $temp = Join-Path $local 'Temp'
    New-Item -ItemType Directory -Force -Path $temp, $roaming | Out-Null
    $sidRule = [Security.AccessControl.FileSystemAccessRule]::new($User.SID, 'Modify', 'ContainerInherit,ObjectInherit', 'None', 'Allow')
    $acl = Get-Acl -LiteralPath $ArtifactDirectory
    if ($null -eq $script:OriginalArtifactAcl) { $script:OriginalArtifactAcl = $acl.GetSecurityDescriptorBinaryForm() }
    $acl.AddAccessRule($sidRule)
    Set-Acl -LiteralPath $ArtifactDirectory -AclObject $acl

    $envOverrides = @{
        USERPROFILE = $profile
        HOME = $profile
        APPDATA = $roaming
        LOCALAPPDATA = $local
        TEMP = $temp
        TMP = $temp
        PATH = (Join-Path $local 'Microsoft/WindowsApps') + ';' + $env:PATH
        CNMON_MSIX_FIXTURE_PACKAGE = $Fixture
        CNMON_MSIX_TEST_EXE = (Join-Path $ArtifactDirectory 'msix-tests.exe')
        CNMON_MSIX_TEST = '1'
        CNMON_MSIX_EXPECTED_SID = $User.SID
        CNMON_MSIX_FIXTURE_HISTORY = (Join-Path $ArtifactDirectory '.msix-fixture-history.db')
        CNMON_MSIX_FIXTURE_LOG = (Join-Path $ArtifactDirectory '.msix-fixture.log')
        CNMON_MSIX_FIXTURE_RECEIPT = (Join-Path $ArtifactDirectory '.cn-mon-1.2.999.0-fixture-receipt.json')
        CNMON_MSIX_PROFILE_HISTORY = (Join-Path $local 'msix-profile-history.db')
        CNMON_MSIX_PROFILE_LOG = (Join-Path $local 'msix-profile.log')
        CNMON_MSIX_CLI_HISTORY = (Join-Path $local 'msix-cli-history.db')
        CNMON_MSIX_CLI_LOG = (Join-Path $local 'msix-cli.log')
        CNMON_NATIVE_COMMAND = '1'
        CNMON_NATIVE_COMMAND_EXPECT_PROBE = 'ordinary'
        CNMON_NATIVE_COMMAND_EXPECT_SID = $User.SID
        CNMON_NATIVE_COMMAND_BINARY = (Join-Path $local 'Microsoft/WindowsApps/cn-mon.exe')
    }
    foreach ($key in @('CNMON_MSIX_IDENTITY_NAME', 'CNMON_MSIX_IDENTITY_PUBLISHER', 'CNMON_MSIX_PUBLISHER_DISPLAY_NAME', 'CNMON_MSIX_EXPECTED_PFN')) {
        $envOverrides[$key] = [Environment]::GetEnvironmentVariable($key)
    }
    $args = @('-NoProfile', '-File', $PSCommandPath, '-Mode', 'User', '-PackagePath', $Candidate, '-ReceiptPath', $SignedReceipt, '-SdkBinDirectory', $SdkBinDirectory, '-ArtifactDirectory', $ArtifactDirectory, '-ExpectedSid', $User.SID)
    if ($InjectFailure) { $args += '-FailAfterInstall' }
    $argLine = ($args | ForEach-Object { '"' + ([string]$_).Replace('"', '\"') + '"' }) -join ' '
    $stdout = Join-Path $script:PrivateRoot $(if ($InjectFailure) { 'injected-user.stdout.log' } else { 'ordinary-user.stdout.log' })
    $stderr = Join-Path $script:PrivateRoot $(if ($InjectFailure) { 'injected-user.stderr.log' } else { 'ordinary-user.stderr.log' })
    $pwsh = Join-Path $PSHOME 'pwsh.exe'
    $process = Start-Process -FilePath $pwsh -Credential $User.Credential -LoadUserProfile -Environment $envOverrides -WorkingDirectory $repositoryRoot -ArgumentList $argLine -PassThru -RedirectStandardOutput $stdout -RedirectStandardError $stderr
    if (-not $process.WaitForExit(15 * 60 * 1000)) {
        Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
        throw 'ordinary-user package qualification exceeded its time limit'
    }
    return [pscustomobject]@{ ExitCode = $process.ExitCode; ResultPath = (Join-Path $ArtifactDirectory 'msix-native-results.json') }
}

function Remove-CnMonMsixTemporaryUser {
    param([string]$Name, [string]$Sid, [string]$IdentityName)
    if ($Name -and $Sid) {
        $packages = @(Get-AppxPackage -User $Sid -Name $IdentityName -ErrorAction SilentlyContinue)
        foreach ($package in $packages) {
            $null = Remove-AppxPackage -Package $package.PackageFullName -User $Sid -ErrorAction Stop
        }
        if (@(Get-AppxPackage -User $Sid -Name $IdentityName -ErrorAction SilentlyContinue).Count -gt 0) {
            throw 'owned user package registration could not be removed'
        }
    }
    if ($Name) {
        Remove-LocalUser -Name $Name -ErrorAction Stop
    }
    if ($Sid) {
        $profile = Get-CimInstance -ClassName Win32_UserProfile -Filter "SID='$Sid'" -ErrorAction SilentlyContinue
        if ($profile -and -not $profile.Special) {
            Remove-CimInstance -InputObject $profile -ErrorAction Stop
        }
    }
}

function Invoke-CnMonMsixUserMode {
    $script:CurrentGate = 'identity'
    Assert-CnMonMsixWindows
    if (-not $script:PrivateRoot) {
        $script:PrivateRoot = Join-Path $env:TEMP ('cn-mon-msix-user-' + [Guid]::NewGuid().ToString('N'))
        New-Item -ItemType Directory -Path $script:PrivateRoot | Out-Null
    }
    if (-not $ExpectedSid) {
        Set-CnMonMsixGate 'account' 'incomplete'
        $script:CurrentGate = 'account'
        throw 'ordinary-user SID is required'
    }
    Assert-CnMonMsixOrdinaryUser $ExpectedSid
    $receipt = Get-CnMonMsixReceipt $ReceiptPath
    $identity = Assert-CnMonMsixReceipt $receipt $PackagePath 'test' -RequireCandidate
    $script:IdentityName = [string]$identity.Name
    $thumbprint = [string]$receipt.signing.certificate_thumbprint
    $trusted = @(Get-ChildItem -LiteralPath Cert:\LocalMachine\TrustedPeople, Cert:\CurrentUser\TrustedPeople -ErrorAction SilentlyContinue | Where-Object { $_.Thumbprint -eq $thumbprint })
    if ($trusted.Count -eq 0) {
        $script:CurrentGate = 'signature'
        Set-CnMonMsixGate 'signature' 'incomplete'
        throw 'test certificate is not already trusted for the current user'
    }
    Assert-CnMonMsixSignature $PackagePath (Join-Path $ArtifactDirectory 'msix-user-signature.log')
    if (-not (Get-Command Add-AppxPackage -ErrorAction SilentlyContinue) -or -not (Get-Command Remove-AppxPackage -ErrorAction SilentlyContinue)) {
        $script:CurrentGate = 'package_registration'
        Set-CnMonMsixGate 'package_registration' 'incomplete'
        throw 'Appx package cmdlets are unavailable in this session'
    }
    $existing = @(Get-AppxPackage -Name $identity.Name -ErrorAction Stop)
    if ($existing.Count -ne 0) {
        $script:CurrentGate = 'package_registration'
        Set-CnMonMsixGate 'package_registration' 'incomplete'
        throw 'qualification user already has a package with this name'
    }
    $fixtureHistory = [Environment]::GetEnvironmentVariable('CNMON_MSIX_FIXTURE_HISTORY')
    $fixtureLog = [Environment]::GetEnvironmentVariable('CNMON_MSIX_FIXTURE_LOG')
    $targets = Get-CnMonMsixProfileTargets $env:USERPROFILE $env:LOCALAPPDATA $fixtureHistory $fixtureLog
    $profileConfig = $targets.ProfileConfig
    $profileHistoryDefault = $targets.ProfileHistoryDefault
    $profileLogDefault = $targets.ProfileLogDefault
    $profileHistory = $targets.ProfileHistory
    $profileLog = $targets.ProfileLog
    $historyOverride = $targets.CliHistory
    $logOverride = $targets.CliLog
    $envProfileHistory = [Environment]::GetEnvironmentVariable('CNMON_MSIX_PROFILE_HISTORY')
    $envProfileLog = [Environment]::GetEnvironmentVariable('CNMON_MSIX_PROFILE_LOG')
    $envCliHistory = [Environment]::GetEnvironmentVariable('CNMON_MSIX_CLI_HISTORY')
    $envCliLog = [Environment]::GetEnvironmentVariable('CNMON_MSIX_CLI_LOG')
    if (-not $envProfileHistory -or -not $envProfileLog -or -not $envCliHistory -or -not $envCliLog -or
        [IO.Path]::GetFullPath($envProfileHistory) -ine [IO.Path]::GetFullPath($profileHistory) -or
        [IO.Path]::GetFullPath($envProfileLog) -ine [IO.Path]::GetFullPath($profileLog) -or
        [IO.Path]::GetFullPath($envCliHistory) -ine [IO.Path]::GetFullPath($historyOverride) -or
        [IO.Path]::GetFullPath($envCliLog) -ine [IO.Path]::GetFullPath($logOverride)) {
        $script:CurrentGate = 'history_config'
        Set-CnMonMsixGate 'history_config' 'incomplete'
        throw 'profile output paths do not match the fresh qualification profile'
    }
    try {
        Assert-CnMonMsixTargetsAbsent $targets.All
    }
    catch {
        $script:CurrentGate = 'history_config'
        Set-CnMonMsixGate 'history_config' 'incomplete'
        throw
    }
    $script:OwnedProfileFiles = @($targets.All)
    Set-CnMonMsixGate 'signature' 'pass'

    $script:CurrentGate = 'upgrade'
    $fixture = [Environment]::GetEnvironmentVariable('CNMON_MSIX_FIXTURE_PACKAGE')
    $fixtureReceiptPath = [Environment]::GetEnvironmentVariable('CNMON_MSIX_FIXTURE_RECEIPT')
    $testExe = [Environment]::GetEnvironmentVariable('CNMON_MSIX_TEST_EXE')
    if (-not $fixture -or -not (Test-Path -LiteralPath $fixture -PathType Leaf) -or
        -not $fixtureReceiptPath -or -not (Test-Path -LiteralPath $fixtureReceiptPath -PathType Leaf) -or
        -not $testExe -or -not (Test-Path -LiteralPath $testExe -PathType Leaf)) {
        Set-CnMonMsixGate 'upgrade' 'incomplete'
        throw 'private test fixture or native test helper is unavailable'
    }
    $script:OwnedPackageSpecs.Clear()

    $alias = Join-Path $env:LOCALAPPDATA 'Microsoft/WindowsApps/cn-mon.exe'
    $env:CNMON_NATIVE_COMMAND = '1'
    $env:CNMON_NATIVE_COMMAND_BINARY = $alias
    $env:CNMON_NATIVE_COMMAND_EXPECT_PROBE = 'ordinary'
    $env:CNMON_NATIVE_COMMAND_EXPECT_SID = $ExpectedSid
    $env:CNMON_MSIX_TEST = '1'
    $env:CNMON_MSIX_EXPECTED_SID = $ExpectedSid
    $env:CNMON_MSIX_EXPECTED_PFN = $identity.ExpectedPFN
    $env:CNMON_MSIX_FIXTURE_CONFIG = ''
    $env:CNMON_MSIX_PROFILE_DEFAULTS = ''
    $env:CNMON_MSIX_PROFILE_LAYERS = ''
    $env:CNMON_RELEASE_QUALIFY = '1'
    $env:CNMON_RELEASE_VERSION = [string]$receipt.cli_version
    $env:CNMON_RELEASE_REVISION = [string]$receipt.cli_revision
    $env:CNMON_RELEASE_EXPECTED_SHA256 = ([string]$receipt.binary_sha256).ToLowerInvariant()
    $testExe = [Environment]::GetEnvironmentVariable('CNMON_MSIX_TEST_EXE')
    Assert-CnMonMsixExpectedNativeRejection 'missing-package-preflight' $testExe 'NativeMSIXInstalledAlias'

    try {
        $fixturePackage = $fixture
        $fixtureReceipt = Get-CnMonMsixReceipt $fixtureReceiptPath
        Assert-CnMonMsixFixtureReceipt $fixturePackage $fixtureReceipt $receipt $identity $null
        Add-CnMonMsixOwnedPackage $identity '1.2.999.0' $fixturePackage $script:OwnedPackageSpecs
        $fixtureInstalled = Get-CnMonMsixPackage $identity
        if ($null -eq $fixtureInstalled) { throw 'lower-version test fixture did not register' }
        Assert-CnMonMsixFixtureReceipt $fixturePackage $fixtureReceipt $receipt $identity $fixtureInstalled
        Set-CnMonMsixGate 'upgrade' 'pass'

        $env:CNMON_MSIX_FIXTURE_CONFIG = '1'
        Invoke-CnMonMsixNativeTests 'fixture-config' $testExe 'TestNativeMSIXFixtureAdjacentConfig' @('CNMON_NATIVE_MSIX_CONFIG adjacent-fixture=selected history=external log=external') | Out-Null
        Set-CnMonMsixGate 'history_config' 'pass'

        $script:CurrentGate = 'package_registration'
        Add-CnMonMsixOwnedPackage $identity ([string]$receipt.package_version) $PackagePath $script:OwnedPackageSpecs
        $installed = Assert-CnMonMsixInstalledPackage $receipt $identity
        if ($FailAfterInstall) {
            $script:InjectedFailure = $true
            Set-CnMonMsixGate 'fail_injection' 'pass'
            throw 'controlled post-install failure'
        }

        $script:CurrentGate = 'alias_identity'
        $portableDirectory = Join-Path $script:PrivateRoot 'portable'
        New-Item -ItemType Directory -Path $portableDirectory | Out-Null
        $portable = Join-Path $portableDirectory 'cn-mon.exe'
        Copy-Item -LiteralPath (Join-Path $installed.InstallLocation 'cn-mon.exe') -Destination $portable
        $env:CNMON_NATIVE_COMMAND_BINARY = $alias
        $env:CNMON_MSIX_INSTALL_LOCATION = $installed.InstallLocation
        foreach ($key in @('CNMON_MSIX_IDENTITY_NAME', 'CNMON_MSIX_IDENTITY_PUBLISHER', 'CNMON_MSIX_PUBLISHER_DISPLAY_NAME', 'CNMON_MSIX_EXPECTED_PFN')) {
            $envOverridesValue = [Environment]::GetEnvironmentVariable($key)
            [Environment]::SetEnvironmentVariable($key, $envOverridesValue, 'Process')
        }

        $env:CNMON_NATIVE_COMMAND_BINARY = $portable
        $portableStdout = Join-Path $ArtifactDirectory 'msix-portable-rejection.stdout.log'
        $portableStderr = Join-Path $ArtifactDirectory 'msix-portable-rejection.stderr.log'
        & $testExe '-test.v' '-test.run=^TestNativeMSIXInstalledAlias$' '-test.count=1' 1> $portableStdout 2> $portableStderr
        if ($LASTEXITCODE -eq 0 -or ((Get-Content -LiteralPath $portableStdout -Raw) -notmatch 'CNMON_NATIVE_MSIX_REJECTED reason=alias-selection')) {
            throw 'portable binary falsely passed the installed alias gate'
        }
        $env:CNMON_NATIVE_COMMAND_BINARY = $alias
        $env:CNMON_MSIX_PROFILE_DEFAULTS = '1'
        $suite = 'NativeCommandINIHistoryRetention|NativeCommandRerunSavedBatch|NativeCommandHistoryManagement(|ReportsCommittedOutputFailure|SelectionSemantics|LockedCancellation|LiveWriterProtection)|NativeManagementLiveWriterProtection|NativeCommandWindows(FirstCtrlCStopsAndReports|SecondCtrlCInterruptsFinalization|PreInputCtrlCDoesNoStartupIO|ClosedStdoutPipeIsQuiet|ProbePermissionRole)|NativeCommandWindowsPrivateConsole(Q|UppercaseQ|NoColorQ)RestoresConsole|NativeCommandWindowsPrivateConsole(QLeavesWriterActive|HistoryErrorRestoresConsole|FirstCtrlCRestoresConsole|OutputHandleLossPreservesRun|SecondCtrlCWhileDisplayBlocked|SecondCtrlCRestoresDuringBlockedFinalization)|NativeRelease(EndedFollowAllFormats|InputFileAndStdin|UnreadableInputHasNoStartupEffects)|NativeMSIX(InstalledAlias|OrdinaryICMPEcho|LoopbackTCP|RealProfileDefaults)'
        $required = @(
            'CNMON_NATIVE_MSIX_ALIAS package-identity=verified ordinary-token=medium expected-sid=verified version=exact',
            'CNMON_NATIVE_MSIX_CHILD pid=',
            'CNMON_NATIVE_MSIX_CONSOLE_CHILD package-family=verified',
            'CNMON_NATIVE_MSIX_ORDINARY icmp_ipv4=OK icmp_ipv6=OK history=external log=external profile=preserved',
            'CNMON_NATIVE_MSIX_ORDINARY tcp_open=open tcp_refused=closed',
            'CNMON_NATIVE_MSIX_PROFILE default-history=real-profile default-log=real-profile',
            'CNMON_NATIVE_MSIX_VIEW key=v-and-V toggles=verified',
            'CNMON_NATIVE_COMMAND_CONFIG ini90=retained cli30=pruned-old-seed recent-round-retained history-path=ini log-path=ini',
            'CNMON_NATIVE_COMMAND_RERUN actual-command-seed=list-discovery=pass removed-input=replayed tcp=open overrides=10s,10s json-history=agree source-results=preserved invalid-selection=exit2-no-write',
            'CNMON_NATIVE_RELEASE_INPUT file=space-bom stdin=requested tcp=open json=pass history=finished',
            'CNMON_NATIVE_RELEASE_FOLLOW ended=read-only formats=json,table,csv,md exit=0 history=unchanged private-console=restored'
        )
        $nativeOutput = Invoke-CnMonMsixNativeTests 'installed-native-suite' $testExe $suite $required
        Set-CnMonMsixGate 'alias_identity' 'pass'
        Set-CnMonMsixGate 'native_cli' 'pass'
        Set-CnMonMsixGate 'icmp' 'pass'

        $profileHistory = Join-Path $env:LOCALAPPDATA 'msix-profile-history.db'
        $profileLog = Join-Path $env:LOCALAPPDATA 'msix-profile.log'
        $profileText = "[ping]`nhistory_file = $profileHistory`nhistory_days = 90`n[logging]`nlogfile = $profileLog`nlevel = INFO`n"
        [IO.File]::WriteAllText($profileConfig, $profileText, [Text.UTF8Encoding]::new($false))
        $env:CNMON_MSIX_PROFILE_DEFAULTS = ''
        $env:CNMON_MSIX_PROFILE_LAYERS = '1'
        $env:CNMON_MSIX_PROFILE_HISTORY = $profileHistory
        $env:CNMON_MSIX_PROFILE_LOG = $profileLog
        $profileOutput = Invoke-CnMonMsixNativeTests 'real-profile-config' $testExe 'NativeMSIXRealProfileLayers' @('CNMON_NATIVE_MSIX_CONFIG real-profile=selected explicit-config=selected cli-overrides=selected relative-paths=resolved')
        foreach ($path in @($profileHistory, $profileLog, $historyOverride, $logOverride)) {
            if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw 'real-profile config/override output was not created' }
        }
        Set-CnMonMsixGate 'history_config' 'pass'
        $powerShell = Join-Path $PSHOME 'pwsh.exe'
        $commandPrompt = Join-Path $env:SystemRoot 'System32/cmd.exe'
        $expectedVersion = "cn-mon $($receipt.cli_version) ($($receipt.cli_revision))"
        foreach ($spelling in @('cn-mon.exe', 'cn-mon')) {
            $powerShellVersion = Invoke-CnMonMsixShell $powerShell @('-NoProfile', '-NonInteractive', '-Command', "$spelling --version") $null (Join-Path $ArtifactDirectory "msix-shell-powershell-$spelling")
            $commandPromptVersion = Invoke-CnMonMsixShell $commandPrompt @('/d', '/c', "$spelling --version") $null (Join-Path $ArtifactDirectory "msix-shell-cmd-$spelling")
            foreach ($shellResult in @($powerShellVersion, $commandPromptVersion)) {
                if ($shellResult.ExitCode -ne 0 -or $shellResult.Stderr -ne '' -or $shellResult.Stdout.Trim() -cne $expectedVersion) {
                    throw 'PowerShell or Command Prompt did not resolve the installed alias to the exact CLI version'
                }
            }
        }

        $inputListener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
        $inputListener.Start()
        try {
            $inputPort = ([Net.IPEndPoint]$inputListener.LocalEndpoint).Port
            foreach ($spelling in @('cn-mon.exe', 'cn-mon')) {
                $commandLine = "$spelling --format json --tcp $inputPort --file -"
                $input = Invoke-CnMonMsixShell $powerShell @('-NoProfile', '-NonInteractive', '-Command', $commandLine) $null (Join-Path $ArtifactDirectory "msix-shell-powershell-stdin-$spelling") "127.0.0.1`n"
                $cmdInput = Invoke-CnMonMsixShell $commandPrompt @('/d', '/c', $commandLine) $null (Join-Path $ArtifactDirectory "msix-shell-cmd-stdin-$spelling") "127.0.0.1`n"
                foreach ($shellResult in @($input, $cmdInput)) {
                    if ($shellResult.ExitCode -ne 0 -or $shellResult.Stderr -ne '') {
                        throw 'shell alias failed the standard-input request or returned stderr'
                    }
                    try { $report = $shellResult.Stdout | ConvertFrom-Json -ErrorAction Stop }
                    catch { throw 'shell alias did not return a JSON report for its standard-input request' }
                    $tcpState = $report.latest[0].PSObject.Properties["tcp_$inputPort"]
                    if ($null -eq $report.latest -or $report.latest.Count -ne 1 -or $report.latest[0].host -cne '127.0.0.1' -or
                        $null -eq $tcpState -or [string]$tcpState.Value -cne 'open') {
                        throw 'shell alias standard-input report omitted the requested open loopback target'
                    }
                }
            }
        }
        finally { $inputListener.Stop() }
        Set-CnMonMsixGate 'shell_aliases' 'pass'

        $script:CurrentGate = 'conflicts'
        $env:CNMON_MSIX_RESOLUTION_MODE = 'packaged'
        $pathMarker = Invoke-CnMonMsixNativeTests 'path-alias' $testExe 'NativeMSIXResolvedCommandIdentity' @('CNMON_NATIVE_MSIX_PATH packaged=identity-verified spellings=cn-mon.exe,cn-mon')
        $originalPath = $env:PATH
        try {
            $env:PATH = (Split-Path -Parent $portable) + ';' + $originalPath
            $env:CNMON_MSIX_RESOLUTION_MODE = 'portable'
            $null = Invoke-CnMonMsixNativeTests 'portable-path-conflict' $testExe 'NativeMSIXResolvedCommandIdentity' @('CNMON_NATIVE_MSIX_PATH portable-conflict=selected packaged-identity=absent spellings=cn-mon.exe,cn-mon')
            $env:PATH = @($originalPath.Split(';') | Where-Object { $_ -notmatch '\\Microsoft\\WindowsApps\\?$' }) -join ';'
            $env:CNMON_MSIX_RESOLUTION_MODE = 'disabled'
            $null = Invoke-CnMonMsixNativeTests 'disabled-path-alias' $testExe 'NativeMSIXResolvedCommandIdentity' @('CNMON_NATIVE_MSIX_PATH disabled=command-unresolved spellings=cn-mon.exe,cn-mon')
        }
        finally { $env:PATH = $originalPath }
        $env:CNMON_MSIX_RESOLUTION_MODE = ''
        Set-CnMonMsixGate 'conflicts' 'incomplete'
        Write-CnMonMsixStatus 'AppExecutionAlias Settings toggle was not exercised; PATH conflict checks are supplemental only'

        $script:CurrentGate = 'upgrade'
        $upgraded = Assert-CnMonMsixInstalledPackage $receipt $identity
        if ([string]$upgraded.PackageFullName -cne [string]$installed.PackageFullName) {
            throw 'candidate upgrade registration changed unexpectedly'
        }
        Set-CnMonMsixGate 'upgrade' 'pass'

        $retained = @{}
        foreach ($path in $targets.All) {
            if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
                throw 'qualification did not create every expected profile and fixture witness before repair'
            }
            $retained[$path] = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
        }

        $script:CurrentGate = 'repair'
        $repair = Get-Command Repair-AppxPackage -ErrorAction SilentlyContinue
        if ($null -eq $repair) {
            Set-CnMonMsixGate 'repair' 'incomplete'
        }
        else {
            $null = & $repair -Package $installed.PackageFullName -ErrorAction Stop
            $null = Assert-CnMonMsixInstalledPackage $receipt $identity
            Assert-CnMonMsixRetainedFiles $retained
            Set-CnMonMsixGate 'repair' 'pass'
        }

        $script:CurrentGate = 'uninstall_reinstall'
        $null = Remove-AppxPackage -Package $installed.PackageFullName -ErrorAction Stop
        Remove-CnMonMsixOwnedPackages $script:OwnedPackageSpecs
        if ($null -ne (Get-CnMonMsixPackage $identity)) { throw 'candidate uninstall left a registered package' }
        Assert-CnMonMsixExpectedNativeRejection 'uninstalled-alias' $testExe 'NativeMSIXInstalledAlias'
        Add-CnMonMsixOwnedPackage $identity ([string]$receipt.package_version) $PackagePath $script:OwnedPackageSpecs
        $reinstalled = Assert-CnMonMsixInstalledPackage $receipt $identity
        if ($null -eq $reinstalled) { throw 'candidate reinstall failed' }
        $null = Invoke-CnMonMsixNativeTests 'reinstalled-alias' $testExe 'TestNativeMSIXInstalledAlias' @(
            'CNMON_NATIVE_MSIX_ALIAS package-identity=verified ordinary-token=medium expected-sid=verified version=exact',
            'CNMON_NATIVE_MSIX_CHILD pid='
        )
        Assert-CnMonMsixRetainedFiles $retained
        Set-CnMonMsixGate 'uninstall_reinstall' 'pass'
        Set-CnMonMsixGate 'fail_injection' 'incomplete'
        Set-CnMonMsixGate 'wack' 'incomplete'
    }
    catch {
        if ($script:InjectedFailure -and $FailAfterInstall) {
            Set-CnMonMsixGate 'fail_injection' 'pass'
            $script:Failure = $true
        }
        else {
            if (-not $script:Result.gates.Contains($script:CurrentGate) -or $script:Result.gates[$script:CurrentGate] -eq 'pass') {
                Set-CnMonMsixGate $script:CurrentGate 'fail'
            }
            $script:Failure = $true
        }
    }
    finally {
        $script:CurrentGate = 'cleanup'
        $cleanupFailed = $false
        try { Remove-CnMonMsixOwnedPackages $script:OwnedPackageSpecs } catch { $cleanupFailed = $true }
        if ($script:OwnedProfileFiles) {
            foreach ($path in $script:OwnedProfileFiles) {
                if (Test-Path -LiteralPath $path -PathType Leaf) {
                    try { Remove-Item -LiteralPath $path -Force -ErrorAction Stop } catch { $cleanupFailed = $true }
                }
            }
        }
        if ($cleanupFailed) { Set-CnMonMsixGate 'cleanup' 'fail' } else { Set-CnMonMsixGate 'cleanup' 'pass' }
        if ($script:PrivateRoot -and (Test-Path -LiteralPath $script:PrivateRoot)) {
            Remove-Item -LiteralPath $script:PrivateRoot -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
}

function Invoke-CnMonMsixHostedMode {
    $script:CurrentGate = 'account'
    Assert-CnMonMsixWindows
    if ($env:GITHUB_ACTIONS -cne 'true' -or [string]::IsNullOrWhiteSpace($env:ImageOS) -or
        $env:ImageOS -notmatch '^win' -or -not (Test-Path -LiteralPath $env:RUNNER_TEMP -PathType Container)) {
        Set-CnMonMsixGate 'account' 'incomplete'
        throw 'Hosted mode is restricted to a fresh GitHub-hosted Windows runner'
    }
    $runnerIdentity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $runnerPrincipal = [Security.Principal.WindowsPrincipal]::new($runnerIdentity)
    if (-not $runnerPrincipal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        Set-CnMonMsixGate 'account' 'incomplete'
        throw 'Hosted mode requires the runner administrator context for temporary setup'
    }
    if ($FailAfterInstall) { throw 'failure injection is only used by the hosted orchestrator' }
    $script:PrivateRoot = Join-Path $env:RUNNER_TEMP ('cn-mon-msix-private-' + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $script:PrivateRoot | Out-Null
    $identity = Read-CnMonMsixIdentity
    $script:IdentityName = [string]$identity.Name
    $receipt = Get-CnMonMsixReceipt $ReceiptPath
    $null = Assert-CnMonMsixReceipt $receipt $PackagePath 'unsigned' -RequireCandidate

    $makeappx = Join-Path $SdkBinDirectory 'makeappx.exe'
    if (-not (Test-Path -LiteralPath $makeappx -PathType Leaf)) {
        $script:CurrentGate = 'package_hash'
        Set-CnMonMsixGate 'package_hash' 'incomplete'
        throw 'MakeAppx is unavailable'
    }
    $unpacked = Join-Path $script:PrivateRoot 'unsigned-unpacked'
    New-Item -ItemType Directory -Path $unpacked | Out-Null
    Invoke-CnMonMsixTool $makeappx @('unpack', '/p', $PackagePath, '/d', $unpacked, '/no') (Join-Path $script:PrivateRoot 'unsigned-unpack.log')
    Assert-CnMonMsixPackagePayload $unpacked $receipt.files

    $candidate = Join-Path $ArtifactDirectory 'cn-mon_1.3.0.0_x64-signed.msix'
    $signedReceiptPath = Join-Path $ArtifactDirectory 'msix-signed-receipt.json'
    if ((Test-Path -LiteralPath $candidate) -or (Test-Path -LiteralPath $signedReceiptPath)) {
        throw 'signed candidate output already exists'
    }
    $script:CreatedCandidate = $true
    Copy-Item -LiteralPath $PackagePath -Destination $candidate
    $cert = New-SelfSignedCertificate -Type CodeSigningCert -Subject $identity.Publisher -CertStoreLocation Cert:\LocalMachine\My -KeyExportPolicy Exportable -KeyAlgorithm RSA -KeyLength 2048 -HashAlgorithm SHA256 -NotAfter (Get-Date).AddHours(6) -ErrorAction Stop
    $script:CreatedCertificate = $cert.Thumbprint
    $publicCert = Join-Path $script:PrivateRoot 'msix-test-signing.cer'
    $null = Export-Certificate -Cert $cert -FilePath $publicCert -Type CERT -Force -ErrorAction Stop
    $trustedBefore = @(Get-ChildItem Cert:\LocalMachine\TrustedPeople -ErrorAction SilentlyContinue | Where-Object { $_.Thumbprint -eq $cert.Thumbprint })
    if ($trustedBefore.Count -ne 0) { throw 'new test certificate thumbprint already exists in TrustedPeople' }
    $null = Import-Certificate -FilePath $publicCert -CertStoreLocation Cert:\LocalMachine\TrustedPeople -ErrorAction Stop
    $script:CreatedTrustedCertificate = $true

    $signtool = Join-Path $SdkBinDirectory 'signtool.exe'
    Invoke-CnMonMsixTool $signtool @('sign', '/fd', 'SHA256', '/s', 'My', '/sm', '/sha1', $cert.Thumbprint, $candidate) (Join-Path $script:PrivateRoot 'candidate-sign.log')
    Assert-CnMonMsixSignature $candidate (Join-Path $script:PrivateRoot 'candidate-verify.log')
    $signedUnpacked = Join-Path $script:PrivateRoot 'signed-unpacked'
    New-Item -ItemType Directory -Path $signedUnpacked | Out-Null
    Invoke-CnMonMsixTool $makeappx @('unpack', '/p', $candidate, '/d', $signedUnpacked, '/no') (Join-Path $script:PrivateRoot 'signed-unpack.log')
    Assert-CnMonMsixPackagePayload $signedUnpacked $receipt.files
    $signedHash = (Get-FileHash -LiteralPath $candidate -Algorithm SHA256).Hash.ToLowerInvariant()
    $signedReceipt = [ordered]@{}
    foreach ($key in $receipt.Keys) { $signedReceipt[$key] = $receipt[$key] }
    $signedReceipt.package_sha256 = $signedHash
    $signedReceipt.signing = [ordered]@{
        status = 'test'
        certificate_thumbprint = $cert.Thumbprint
        unsigned_package_sha256 = [string]$receipt.package_sha256
        signed_package_sha256 = $signedHash
    }
    [IO.File]::WriteAllText($signedReceiptPath, (ConvertTo-Json -InputObject $signedReceipt -Depth 12) + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
    $script:CreatedReceipt = $true
    $script:SignedOutputsReady = $true
    Set-CnMonMsixGate 'signature' 'pass'

    $fixturePath = Join-Path $ArtifactDirectory '.cn-mon-1.2.999.0-fixture.msix'
    $fixtureReceiptPath = Join-Path $ArtifactDirectory '.cn-mon-1.2.999.0-fixture-receipt.json'
    $fixtureHistory = Join-Path $ArtifactDirectory '.msix-fixture-history.db'
    $fixtureLog = Join-Path $ArtifactDirectory '.msix-fixture.log'
    if ((Test-Path -LiteralPath $fixturePath) -or (Test-Path -LiteralPath $fixtureReceiptPath) -or (Test-Path -LiteralPath $fixtureHistory) -or (Test-Path -LiteralPath $fixtureLog)) {
        throw 'private fixture output already exists'
    }
    $script:CreatedFixture = $true
    $script:CreatedFixtureFiles = @($fixturePath, $fixtureReceiptPath, $fixtureHistory, $fixtureLog)
    New-CnMonMsixFixture $PackagePath $receipt $identity $fixturePath $fixtureReceiptPath $fixtureHistory $fixtureLog

    $testExe = Join-Path $ArtifactDirectory 'msix-tests.exe'
    $oldGo = $env:CGO_ENABLED
    $env:CGO_ENABLED = '0'
    Push-Location $repositoryRoot
    try {
        & go test -tags native -c -o $testExe ./cmd/cn-mon
        if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $testExe -PathType Leaf)) {
            Set-CnMonMsixGate 'native_cli' 'fail'
            throw 'native Windows qualification tests could not be compiled'
        }
    }
    finally {
        Pop-Location
        $env:CGO_ENABLED = $oldGo
    }

    $script:CurrentGate = 'fail_injection'
    $failureUser = New-CnMonMsixTemporaryUser
    $failureResult = Invoke-CnMonMsixUserRun $failureUser $candidate $signedReceiptPath $fixturePath -InjectFailure
    $failureJson = Get-Content -LiteralPath $failureResult.ResultPath -Raw | ConvertFrom-Json -AsHashtable
    if ($failureResult.ExitCode -eq 0 -or $failureJson.gates.fail_injection -cne 'pass' -or $failureJson.gates.cleanup -cne 'pass') {
        throw 'post-install failure injection did not prove nonzero exit and cleanup'
    }
    Set-CnMonMsixGate 'fail_injection' 'pass'
    Remove-CnMonMsixTemporaryUser $failureUser.Name $failureUser.SID $script:IdentityName
    $script:CreatedUser = $null
    $script:CreatedProfile = $null

    $user = New-CnMonMsixTemporaryUser
    $userResult = Invoke-CnMonMsixUserRun $user $candidate $signedReceiptPath $fixturePath
    $userJson = Get-Content -LiteralPath $userResult.ResultPath -Raw | ConvertFrom-Json -AsHashtable
    foreach ($key in $userJson.gates.Keys) {
        if ($key -cne 'fail_injection') { $script:Result.gates[$key] = $userJson.gates[$key] }
    }
    if ($userResult.ExitCode -ne 0) {
        $script:Failure = $true
        Remove-CnMonMsixTemporaryUser $user.Name $user.SID $script:IdentityName
        $script:CreatedUser = $null
        $script:CreatedProfile = $null
        return
    }
    Remove-CnMonMsixTemporaryUser $user.Name $user.SID $script:IdentityName
    $script:CreatedUser = $null
    $script:CreatedProfile = $null
    Set-CnMonMsixGate 'account' 'pass'
    Set-CnMonMsixGate 'fail_injection' 'pass'
    Set-CnMonMsixGate 'wack' 'incomplete'
}

function Invoke-CnMonMsixCertifyMode {
    Assert-CnMonMsixWindows
    $identity = Read-CnMonMsixIdentity
    $script:IdentityName = [string]$identity.Name
    $receipt = Get-CnMonMsixReceipt $ReceiptPath
    $null = Assert-CnMonMsixReceipt $receipt $PackagePath 'test' -RequireCandidate
    $tempRoot = if ($env:RUNNER_TEMP -and (Test-Path -LiteralPath $env:RUNNER_TEMP -PathType Container)) { $env:RUNNER_TEMP } else { $env:TEMP }
    if (-not $tempRoot -or -not (Test-Path -LiteralPath $tempRoot -PathType Container)) {
        $script:CurrentGate = 'package_hash'
        Set-CnMonMsixGate 'package_hash' 'incomplete'
        throw 'private temporary storage is unavailable'
    }
    $script:PrivateRoot = Join-Path $tempRoot ('cn-mon-msix-certify-' + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $script:PrivateRoot | Out-Null
    $makeappx = Join-Path $SdkBinDirectory 'makeappx.exe'
    if (-not (Test-Path -LiteralPath $makeappx -PathType Leaf)) {
        $script:CurrentGate = 'package_hash'
        Set-CnMonMsixGate 'package_hash' 'incomplete'
        throw 'MakeAppx is unavailable'
    }
    $unpacked = Join-Path $script:PrivateRoot 'signed-unpacked'
    New-Item -ItemType Directory -Path $unpacked | Out-Null
    Invoke-CnMonMsixTool $makeappx @('unpack', '/p', $PackagePath, '/d', $unpacked, '/no') (Join-Path $script:PrivateRoot 'signed-unpack.log')
    Assert-CnMonMsixPackagePayload $unpacked $receipt.files
    $script:CurrentGate = 'account'
    $certifyIdentity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $certifyPrincipal = [Security.Principal.WindowsPrincipal]::new($certifyIdentity)
    if (-not $certifyPrincipal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        Set-CnMonMsixGate 'account' 'incomplete'
        $script:CurrentGate = 'wack'
        Set-CnMonMsixGate 'wack' 'incomplete'
        throw 'Certify mode requires an elevated administrator session'
    }
    Set-CnMonMsixGate 'account' 'pass'
    $script:CurrentGate = 'wack'
    $appcert = 'C:\Program Files (x86)\Windows Kits\10\App Certification Kit\appcert.exe'
    if (-not (Test-Path -LiteralPath $appcert -PathType Leaf)) {
        Set-CnMonMsixGate 'wack' 'incomplete'
        throw 'App Certification Kit is unavailable'
    }
    $sessionType = 'CnMonMsixActiveSession'
    if ($null -eq ($sessionType -as [type])) {
        Add-Type -TypeDefinition @'
using System.Runtime.InteropServices;
public static class CnMonMsixActiveSession
{
    [DllImport("kernel32.dll")]
    public static extern uint WTSGetActiveConsoleSessionId();
}
'@ -ErrorAction Stop
    }
    $session = [Diagnostics.Process]::GetCurrentProcess().SessionId
    [uint32]$active = [CnMonMsixActiveSession]::WTSGetActiveConsoleSessionId()
    if ($session -eq 0 -or $active -eq [uint32]::MaxValue -or $session -ne $active) {
        Set-CnMonMsixGate 'wack' 'incomplete'
        throw 'WACK requires an active interactive user session'
    }
    $thumb = [string]$receipt.signing.certificate_thumbprint
    $signature = Get-AuthenticodeSignature -LiteralPath $PackagePath
    if ($null -eq $signature.SignerCertificate -or $signature.SignerCertificate.Thumbprint -cne $thumb) {
        Set-CnMonMsixGate 'signature' 'fail'
        throw 'signed package signer does not match the signed receipt'
    }
    $publicCertPath = Join-Path $script:PrivateRoot 'msix-test-signing.cer'
    $trusted = @(Get-ChildItem Cert:\LocalMachine\TrustedPeople, Cert:\CurrentUser\TrustedPeople -ErrorAction SilentlyContinue | Where-Object { $_.Thumbprint -eq $thumb })
    $freshHosted = $env:GITHUB_ACTIONS -ceq 'true' -and $env:ImageOS -match '^win' -and
        [string]::IsNullOrWhiteSpace($env:RUNNER_TEMP) -eq $false -and
        (Test-Path -LiteralPath $env:RUNNER_TEMP -PathType Container)
    $trustAction = Get-CnMonMsixTrustAction ($trusted.Count -gt 0) $freshHosted
    if ($trustAction -eq 'incomplete') {
        Set-CnMonMsixGate 'wack' 'incomplete'
        throw 'signed package trust is not provisioned for this certification host'
    }
    if ($trustAction -eq 'import') {
        $null = Export-Certificate -Cert $signature.SignerCertificate -FilePath $publicCertPath -Type CERT -Force -ErrorAction Stop
        $exportedCert = [Security.Cryptography.X509Certificates.X509Certificate2]::new($publicCertPath)
        try {
            if ($exportedCert.Thumbprint -cne $thumb) {
                Set-CnMonMsixGate 'signature' 'fail'
                throw 'exported signer certificate does not match the signed receipt'
            }
        }
        finally { $exportedCert.Dispose() }
        $existingMachine = @(Get-ChildItem Cert:\LocalMachine\TrustedPeople -ErrorAction SilentlyContinue | Where-Object { $_.Thumbprint -eq $thumb })
        if ($existingMachine.Count -ne 0) {
            Set-CnMonMsixGate 'wack' 'fail'
            throw 'signer certificate appeared in TrustedPeople during certification setup'
        }
        $null = Import-Certificate -FilePath $publicCertPath -CertStoreLocation Cert:\LocalMachine\TrustedPeople -ErrorAction Stop
        $script:CreatedTrustedCertificate = $true
        $script:CreatedCertificate = $thumb
    }
    $script:CurrentGate = 'signature'
    Assert-CnMonMsixSignature $PackagePath (Join-Path $script:PrivateRoot 'certify-signature.log')
    $helpLog = Join-Path $ArtifactDirectory 'msix-wack-help.log'
    & $appcert '/?' *> $helpLog
    if ($LASTEXITCODE -ne 0) {
        Set-CnMonMsixGate 'wack' 'incomplete'
        throw 'WACK help preflight failed'
    }
    & $appcert reset *> (Join-Path $ArtifactDirectory 'msix-wack-reset.log')
    if ($LASTEXITCODE -ne 0) {
        Set-CnMonMsixGate 'wack' 'fail'
        throw 'WACK reset failed'
    }
    $report = Join-Path $ArtifactDirectory 'wack.xml'
    & $appcert test -appxpackagepath $PackagePath -reportoutputpath $report *> (Join-Path $ArtifactDirectory 'msix-wack.log')
    $wackExit = $LASTEXITCODE
    $wackStatus = Get-CnMonMsixWackStatus $report $wackExit $identity.Name $receipt.package_version
    Set-CnMonMsixGate 'wack' $wackStatus
    if ($wackStatus -ne 'pass') {
        throw 'WACK report did not establish a complete successful certification run'
    }
}

try {
    if (-not (Test-Path -LiteralPath $ArtifactDirectory -PathType Container)) {
        New-Item -ItemType Directory -Path $ArtifactDirectory | Out-Null
    }
    $ArtifactDirectory = [IO.Path]::GetFullPath($ArtifactDirectory)
    $script:CurrentGate = 'identity'
    switch ($Mode) {
        'Hosted' { Invoke-CnMonMsixHostedMode }
        'User' { Invoke-CnMonMsixUserMode }
        'Certify' { Invoke-CnMonMsixCertifyMode }
    }
}
catch {
    if (-not $script:InjectedFailure) {
        if (-not $script:Result.gates.Contains($script:CurrentGate) -or $script:Result.gates[$script:CurrentGate] -eq 'pass') {
            Set-CnMonMsixGate $script:CurrentGate 'fail'
        }
        $script:Failure = $true
    }
}
finally {
    if ($Mode -eq 'Hosted') {
        if ($script:CreatedUser) {
            try { Remove-CnMonMsixTemporaryUser $script:CreatedUser $script:CreatedProfile.SID $script:IdentityName } catch { $script:Failure = $true; Set-CnMonMsixGate 'cleanup' 'fail' }
        }
        if ($script:CreatedCertificate) {
            try { Remove-CnMonMsixOwnedCertificate $script:CreatedCertificate $script:CreatedTrustedCertificate $true } catch { $script:Failure = $true; Set-CnMonMsixGate 'cleanup' 'fail' }
        }
        if ($script:CreatedFixture) {
            foreach ($path in $script:CreatedFixtureFiles) {
                if (Test-Path -LiteralPath $path -PathType Leaf) { Remove-Item -LiteralPath $path -Force -ErrorAction SilentlyContinue }
            }
        }
        if ($script:CreatedCandidate -and -not $script:SignedOutputsReady -and (Test-Path -LiteralPath (Join-Path $ArtifactDirectory 'cn-mon_1.3.0.0_x64-signed.msix'))) {
            Remove-Item -LiteralPath (Join-Path $ArtifactDirectory 'cn-mon_1.3.0.0_x64-signed.msix') -Force -ErrorAction SilentlyContinue
        }
        if ($script:CreatedReceipt -and -not $script:SignedOutputsReady -and (Test-Path -LiteralPath (Join-Path $ArtifactDirectory 'msix-signed-receipt.json'))) {
            Remove-Item -LiteralPath (Join-Path $ArtifactDirectory 'msix-signed-receipt.json') -Force -ErrorAction SilentlyContinue
        }
        if ($script:PrivateRoot -and (Test-Path -LiteralPath $script:PrivateRoot)) {
            Remove-Item -LiteralPath $script:PrivateRoot -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
    if ($Mode -eq 'Certify' -and $script:CreatedTrustedCertificate -and $script:CreatedCertificate) {
        try { Remove-CnMonMsixOwnedCertificate $script:CreatedCertificate $true $false } catch { $script:Failure = $true; Set-CnMonMsixGate 'cleanup' 'fail' }
    }
    if ($Mode -eq 'Certify' -and $script:Result.gates['cleanup'] -eq 'incomplete') {
        Set-CnMonMsixGate 'cleanup' 'pass'
    }
    if ($Mode -in @('User', 'Certify') -and $script:PrivateRoot -and (Test-Path -LiteralPath $script:PrivateRoot)) {
        Remove-Item -LiteralPath $script:PrivateRoot -Recurse -Force -ErrorAction SilentlyContinue
    }
    if ($script:OriginalArtifactAcl) {
        try {
            $restoreAcl = [Security.AccessControl.DirectorySecurity]::new()
            $restoreAcl.SetSecurityDescriptorBinaryForm([byte[]]$script:OriginalArtifactAcl)
            Set-Acl -LiteralPath $ArtifactDirectory -AclObject $restoreAcl
        }
        catch { $script:Failure = $true; Set-CnMonMsixGate 'cleanup' 'fail' }
    }
    if ($Mode -eq 'Certify' -and $script:Result.gates.Contains('wack')) {
        $existingPath = Join-Path $ArtifactDirectory 'msix-native-results.json'
        if (Test-Path -LiteralPath $existingPath -PathType Leaf) {
            try {
                $existing = Get-Content -LiteralPath $existingPath -Raw | ConvertFrom-Json -AsHashtable
                if ($existing.schema_version -ne 1 -or $existing.mode -cne 'Hosted' -or $null -eq $existing.gates) {
                    throw 'hosted result has an unsupported schema'
                }
                $script:Result.gates = Merge-CnMonMsixResultGates $existing.gates $script:GateUpdates
                $script:Result.mode = 'Certify'
            }
            catch { $script:Failure = $true; $script:AllowResultWrite = $false }
        }
        else {
            $script:Failure = $true
            $script:AllowResultWrite = $false
        }
    }
    Set-CnMonMsixOverall $Mode
    if ($script:AllowResultWrite) {
        try { Write-CnMonMsixResult $ArtifactDirectory } catch { $script:Failure = $true }
    }
    if ($Mode -eq 'Hosted' -and $script:Result.gates['wack'] -eq 'incomplete' -and $script:Result.overall -eq 'pass') {
        Write-CnMonMsixStatus 'hosted qualification passed; WACK remains a separate Certify gate'
    }
    else {
        Write-CnMonMsixStatus $script:Result.overall
    }
}

if ($script:Failure -or $script:Result.overall -ne 'pass' -or ($Mode -eq 'User' -and $FailAfterInstall)) {
    exit 1
}
exit 0
