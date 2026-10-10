$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false

$repoRoot = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
Set-Location $repoRoot
$artifactDir = $env:CNMON_NATIVE_COMMAND_ARTIFACT_DIR
if (-not $artifactDir) {
    $tempRoot = $env:RUNNER_TEMP
    if (-not $tempRoot) { $tempRoot = [IO.Path]::GetTempPath() }
    $artifactDir = Join-Path $tempRoot 'cn-mon-native-command-windows'
}
New-Item -ItemType Directory -Force -Path $artifactDir | Out-Null
$artifactDir = (Resolve-Path $artifactDir).Path
$scriptLog = Join-Path $artifactDir 'windows-command-harness.log'
$testExe = Join-Path $artifactDir 'cn-mon-command.test.exe'
$commandExe = Join-Path $artifactDir 'cn-mon.exe'
$createdUser = $null
$exitCode = 0

function Write-Log([string] $Text) {
    $line = "$(Get-Date -Format o) $Text"
    Add-Content -LiteralPath $scriptLog -Value $line
    Write-Host $line
}

function Write-GitHubFailure([string] $Title, [string] $Message) {
    if ($Message.Length -gt 6000) { $Message = $Message.Substring($Message.Length - 6000) }
    $escaped = $Message.Replace('%', '%25').Replace("`r", '%0D').Replace("`n", '%0A')
    Write-Host "::error title=${Title}::$escaped"
}

function Invoke-CommandTests([string] $Name, [string[]] $Arguments, [System.Management.Automation.PSCredential] $Credential = $null) {
    $stdout = Join-Path $artifactDir "windows-command-$Name.stdout.log"
    $stderr = Join-Path $artifactDir "windows-command-$Name.stderr.log"
    $env:CNMON_NATIVE_COMMAND = '1'
    $env:CNMON_NATIVE_COMMAND_BINARY = $commandExe
    $env:TEMP = $artifactDir
    $env:TMP = $artifactDir
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent().Name
    if ($Credential) { $identity = $Credential.UserName }
    Write-Log "phase=$Name expected_probe=$env:CNMON_NATIVE_COMMAND_EXPECT_PROBE test_identity=$identity"
    if ($Credential) {
        $process = Start-Process -FilePath $testExe -ArgumentList $Arguments -Credential $Credential -WorkingDirectory $artifactDir -PassThru -RedirectStandardOutput $stdout -RedirectStandardError $stderr
        if (-not $process.WaitForExit(6 * 60 * 1000)) {
            Stop-Process -Id $process.Id -Force -ErrorAction SilentlyContinue
            throw "Windows command native $Name tests exceeded 6 minutes"
        }
        $code = $process.ExitCode
    } else {
        & $testExe @Arguments 1> $stdout 2> $stderr
        $code = $LASTEXITCODE
    }
    $outText = if (Test-Path $stdout) { Get-Content -Raw -LiteralPath $stdout } else { '' }
    $errText = if (Test-Path $stderr) { Get-Content -Raw -LiteralPath $stderr } else { '' }
    if ($outText) { Write-Log "native command $Name stdout:`n$outText" }
    if ($errText) { Write-Log "native command $Name stderr:`n$errText" }
    return [pscustomobject]@{ ExitCode = $code; Output = "$outText`n$errText" }
}

function Require-CommandTests([string] $Name, [string[]] $Arguments, [string[]] $Markers, [System.Management.Automation.PSCredential] $Credential = $null) {
    $result = Invoke-CommandTests $Name $Arguments $Credential
    $missing = @($Markers | Where-Object { $result.Output -notmatch $_ })
    if ($result.ExitCode -ne 0 -or $missing.Count -gt 0) {
        $detail = $result.Output
        if (-not $detail) { $detail = '(native command tests emitted no output)' }
        if ($missing.Count -gt 0) { $detail += "`nmissing pass markers: $($missing -join ', ')" }
        Write-GitHubFailure 'cn-mon native Windows command' "phase '$Name' failed with exit $($result.ExitCode):`n$detail"
        throw "required Windows command native phase '$Name' failed"
    }
}

try {
    $admin = ([Security.Principal.WindowsPrincipal]::new([Security.Principal.WindowsIdentity]::GetCurrent())).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
    Write-Log "runner=$env:ImageOS imageVersion=$env:ImageVersion identity=$([Security.Principal.WindowsIdentity]::GetCurrent().Name) is_admin=$admin"
    if (-not $admin) { throw 'native Windows command gate requires the hosted elevated runner' }

    $env:CGO_ENABLED = '0'
    & go build -o $commandExe ./cmd/cn-mon
    if ($LASTEXITCODE -ne 0) { throw "native cn-mon build failed with exit $LASTEXITCODE" }
    & go test -c -tags native -o $testExe ./cmd/cn-mon
    if ($LASTEXITCODE -ne 0) { throw "native command test-binary build failed with exit $LASTEXITCODE" }

    $adminArgs = @(
        '-test.v',
        '-test.run=^Test(NativeCommandINIHistoryRetention|NativeCommandWindows(FirstCtrlCStopsAndReports|SecondCtrlCInterruptsFinalization|PreInputCtrlCDoesNoStartupIO|ClosedStdoutPipeIsQuiet|ProbePermissionRole))$',
        '-test.count=1',
        '-test.timeout=8m'
    )
    $env:CNMON_NATIVE_COMMAND_EXPECT_PROBE = 'granted'
    Require-CommandTests 'elevated' $adminArgs @(
        '--- PASS: TestNativeCommandINIHistoryRetention',
        '--- PASS: TestNativeCommandWindowsFirstCtrlCStopsAndReports',
        '--- PASS: TestNativeCommandWindowsSecondCtrlCInterruptsFinalization',
        '--- PASS: TestNativeCommandWindowsPreInputCtrlCDoesNoStartupIO',
        '--- PASS: TestNativeCommandWindowsClosedStdoutPipeIsQuiet',
        '--- PASS: TestNativeCommandWindowsProbePermissionRole',
        'CNMON_NATIVE_COMMAND_CONFIG ini90=retained cli30=pruned-old-seed recent-round-retained history-path=ini log-path=ini',
        'CNMON_NATIVE_COMMAND_WINDOWS ICMP-permission=granted identity=\S+ result=OK exit=0 tcp=open history=one-round'
    )

    $name = "cnmc-$([guid]::NewGuid().ToString('N').Substring(0,8))"
    $passwordBytes = [byte[]]::new(32)
    [Security.Cryptography.RandomNumberGenerator]::Fill($passwordBytes)
    $securePassword = ConvertTo-SecureString ([Convert]::ToBase64String($passwordBytes)) -AsPlainText -Force
    $createdUser = New-LocalUser -Name $name -Password $securePassword -AccountNeverExpires -Description 'Temporary cn-mon native command test account' -ErrorAction Stop
    $credential = [System.Management.Automation.PSCredential]::new("$env:COMPUTERNAME\$name", $securePassword)
    & icacls $artifactDir /grant "${env:COMPUTERNAME}\${name}:(OI)(CI)M" /T /C | Out-String | ForEach-Object { Write-Log $_ }
    if ($LASTEXITCODE -ne 0) { throw "failed to grant temporary account access to test artifacts (exit $LASTEXITCODE)" }

    $deniedArgs = @('-test.v', '-test.run=^TestNativeCommandWindowsProbePermissionRole$', '-test.count=1', '-test.timeout=3m')
    $env:CNMON_NATIVE_COMMAND_EXPECT_PROBE = 'denied'
    Require-CommandTests 'ordinary-user-denied' $deniedArgs @(
        '--- PASS: TestNativeCommandWindowsProbePermissionRole',
        'CNMON_NATIVE_COMMAND_WINDOWS ICMP-permission=denied identity=\S+ result=ERROR exit=3 tcp=open history=one-round'
    ) $credential

    Write-Log 'CNMON_NATIVE_WINDOWS_COMMAND_RESULT elevated=pass ordinary-user-denied=pass private-console=pass'
} catch {
    $exitCode = 1
    Write-Log "FAIL $($_.Exception.Message)`n$($_.ScriptStackTrace)"
    $failureDetails = $_.Exception.Message
    if ($_.InvocationInfo.PositionMessage) { $failureDetails += "`n$($_.InvocationInfo.PositionMessage)" }
    if ($_.ScriptStackTrace) { $failureDetails += "`n$($_.ScriptStackTrace)" }
    Write-GitHubFailure 'cn-mon native Windows command harness' $failureDetails
} finally {
    if ($createdUser) {
        try {
            Remove-LocalUser -Name $createdUser.Name -ErrorAction Stop
            Write-Log "removed temporary account=$($createdUser.Name)"
        } catch {
            $exitCode = 1
            Write-Log "FAIL account cleanup name=$($createdUser.Name) error=$($_.Exception.Message)"
        }
    }
    Write-Log "cleanup_exit=$exitCode"
}

if ($exitCode -ne 0) { exit $exitCode }
Write-Log 'native Windows command gate completed'
