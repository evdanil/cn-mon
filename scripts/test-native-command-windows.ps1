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

function Count-MatchingLines([string] $Text, [string] $Pattern) {
    return @($Text -split '\r?\n' | Where-Object { $_ -match $Pattern }).Count
}

function Invoke-CommandTests([string] $Name, [string[]] $Arguments, [string] $ExpectedRole, [string] $ExpectedSid, [System.Management.Automation.PSCredential] $Credential = $null) {
    $stdout = Join-Path $artifactDir "windows-command-$Name.stdout.log"
    $stderr = Join-Path $artifactDir "windows-command-$Name.stderr.log"
    $env:CNMON_NATIVE_COMMAND = '1'
    $env:CNMON_NATIVE_COMMAND_BINARY = $commandExe
    $env:CNMON_NATIVE_COMMAND_EXPECT_PROBE = $ExpectedRole
    $env:CNMON_NATIVE_COMMAND_EXPECT_SID = $ExpectedSid
    $env:TEMP = $artifactDir
    $env:TMP = $artifactDir
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent().Name
    if ($Credential) { $identity = $Credential.UserName }
    Write-Log "phase=$Name expected_role=$ExpectedRole expected_sid=$ExpectedSid test_identity=$identity"
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

function Require-CommandTests([string] $Name, [string[]] $Arguments, [string] $ExpectedRole, [string] $ExpectedSid, [string[]] $Markers, [System.Management.Automation.PSCredential] $Credential = $null) {
    $result = Invoke-CommandTests $Name $Arguments $ExpectedRole $ExpectedSid $Credential
    $missing = @($Markers | Where-Object { $result.Output -notmatch $_ })
    if ($result.ExitCode -ne 0 -or $missing.Count -gt 0) {
        $detail = $result.Output
        if (-not $detail) { $detail = '(native command tests emitted no output)' }
        if ($missing.Count -gt 0) { $detail += "`nmissing pass markers: $($missing -join ', ')" }
        Write-GitHubFailure 'cn-mon native Windows command' "phase '$Name' failed with exit $($result.ExitCode):`n$detail"
        throw "required Windows command native phase '$Name' failed"
    }
    return $result
}

try {
    $runnerIdentity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $runnerSid = $runnerIdentity.User.Value
    $admin = ([Security.Principal.WindowsPrincipal]::new($runnerIdentity)).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
    Write-Log "runner=$env:ImageOS imageVersion=$env:ImageVersion identity=$($runnerIdentity.Name) sid=$runnerSid is_admin=$admin"
    if (-not $admin) { throw 'native Windows command gate requires the hosted elevated runner' }

    $env:CGO_ENABLED = '0'
    & go build -o $commandExe ./cmd/cn-mon
    if ($LASTEXITCODE -ne 0) { throw "native cn-mon build failed with exit $LASTEXITCODE" }
    & go test -c -tags native -o $testExe ./cmd/cn-mon
    if ($LASTEXITCODE -ne 0) { throw "native command test-binary build failed with exit $LASTEXITCODE" }

    $adminArgs = @(
        '-test.v',
        '-test.run=^Test(NativeCommandINIHistoryRetention|NativeCommandWindows(FirstCtrlCStopsAndReports|SecondCtrlCInterruptsFinalization|PreInputCtrlCDoesNoStartupIO|ClosedStdoutPipeIsQuiet|ProbePermissionRole)|NativeCommandWindowsPrivateConsole(Q|UppercaseQ|NoColorQ)RestoresConsole|NativeCommandWindowsPrivateConsoleQLeavesWriterActive|NativeCommandWindowsPrivateConsoleHistoryErrorRestoresConsole|NativeCommandWindowsPrivateConsoleFirstCtrlCRestoresConsole|NativeCommandWindowsPrivateConsoleOutputHandleLossPreservesRun|NativeCommandWindowsPrivateConsoleSecondCtrlCWhileDisplayBlocked|NativeCommandWindowsPrivateConsoleSecondCtrlCRestoresDuringBlockedFinalization)$',
        '-test.count=1',
        '-test.timeout=8m'
    )
    $elevatedSid = [regex]::Escape($runnerSid)
    $elevatedRoleMarker = "CNMON_NATIVE_COMMAND_WINDOWS_ROLE expected_role=elevated expected_sid=$elevatedSid parent_sid=$elevatedSid parent_elevated=true parent_admin_enabled=true child_sid=$elevatedSid child_elevated=true child_admin_enabled=true"
    $elevated = Require-CommandTests 'elevated' $adminArgs 'elevated' $runnerSid @(
        '--- PASS: TestNativeCommandINIHistoryRetention',
        '--- PASS: TestNativeCommandWindowsFirstCtrlCStopsAndReports',
        '--- PASS: TestNativeCommandWindowsSecondCtrlCInterruptsFinalization',
        '--- PASS: TestNativeCommandWindowsPreInputCtrlCDoesNoStartupIO',
        '--- PASS: TestNativeCommandWindowsClosedStdoutPipeIsQuiet',
        '--- PASS: TestNativeCommandWindowsProbePermissionRole',
        '--- PASS: TestNativeCommandWindowsPrivateConsoleQRestoresConsole',
        '--- PASS: TestNativeCommandWindowsPrivateConsoleQLeavesWriterActive',
        '--- PASS: TestNativeCommandWindowsPrivateConsoleUppercaseQRestoresConsole',
        '--- PASS: TestNativeCommandWindowsPrivateConsoleNoColorQRestoresConsole',
        '--- PASS: TestNativeCommandWindowsPrivateConsoleOutputHandleLossPreservesRun',
        '--- PASS: TestNativeCommandWindowsPrivateConsoleSecondCtrlCWhileDisplayBlocked',
        '--- PASS: TestNativeCommandWindowsPrivateConsoleHistoryErrorRestoresConsole',
        '--- PASS: TestNativeCommandWindowsPrivateConsoleFirstCtrlCRestoresConsole',
        '--- PASS: TestNativeCommandWindowsPrivateConsoleSecondCtrlCRestoresDuringBlockedFinalization',
        'CNMON_NATIVE_COMMAND_CONFIG ini90=retained cli30=pruned-old-seed recent-round-retained history-path=ini log-path=ini',
        $elevatedRoleMarker,
        "CNMON_NATIVE_COMMAND_WINDOWS_RESULT role=elevated sid=$elevatedSid icmp=OK exit=0 tcp=open json=pass history=one-round",
        "CNMON_NATIVE_COMMAND_WINDOWS_CONSOLE q=graceful report=complete input=restored output=restored cursor=restored history=stopped sid=$elevatedSid",
        "CNMON_NATIVE_COMMAND_WINDOWS_CONSOLE q=follower-only writer=still-running restored=true sid=$elevatedSid",
        'CNMON_NATIVE_COMMAND_WINDOWS_CONSOLE key=Q exit=graceful report=table-theme console=rendered literal-escape=false input=restored output=restored cursor=restored history=stopped',
        'CNMON_NATIVE_COMMAND_WINDOWS_CONSOLE key=Q report=NO_COLOR-empty console=rendered literal-escape=false input=restored output=restored cursor=restored history=stopped',
        'CNMON_NATIVE_COMMAND_WINDOWS_CONSOLE output-loss=product-stderr-only useful-rounds>=2 q=input-alive report=complete history=stopped input=restored',
        'CNMON_NATIVE_COMMAND_WINDOWS_CONSOLE display-output=blocked selection=verified write=pending db=stopped second-ctrl-c=130 input=restored output=restored cursor=restored',
        'CNMON_NATIVE_COMMAND_WINDOWS_CONSOLE error=history-open exit=3 input=restored output=restored cursor=restored blocked-parent=preserved cause=logged',
        'CNMON_NATIVE_COMMAND_WINDOWS_CONSOLE ctrlc=first-graceful exit=0 history=stopped input=restored output=restored cursor=restored',
        'CNMON_NATIVE_COMMAND_WINDOWS_CONSOLE ctrlc=first-graceful second=hard-blocked-finalization=pass input=restored output=restored cursor=restored'
    )
    if ((Count-MatchingLines $elevated.Output $elevatedRoleMarker) -ne 1 -or
        (Count-MatchingLines $elevated.Output "CNMON_NATIVE_COMMAND_WINDOWS_RESULT role=elevated sid=$elevatedSid icmp=OK exit=0 tcp=open json=pass history=one-round") -ne 1) {
        throw 'elevated Windows role did not emit exactly one matching identity and result marker'
    }
    if ((Count-MatchingLines $elevated.Output "CNMON_NATIVE_COMMAND_WINDOWS_CONSOLE q=graceful report=complete input=restored output=restored cursor=restored history=stopped sid=$elevatedSid") -ne 1) {
        throw 'private-console test did not emit exactly one matching elevated-role restoration marker'
    }
    if ((Count-MatchingLines $elevated.Output "CNMON_NATIVE_COMMAND_WINDOWS_CONSOLE q=follower-only writer=still-running restored=true sid=$elevatedSid") -ne 1) {
        throw 'private-console follower test did not prove q left its writer running'
    }
    if ((Count-MatchingLines $elevated.Output 'CNMON_NATIVE_COMMAND_WINDOWS_CONSOLE key=Q exit=graceful report=table-theme console=rendered literal-escape=false input=restored output=restored cursor=restored history=stopped') -ne 1) {
        throw 'private-console uppercase-Q test did not emit exactly one key and restoration marker'
    }
    if ((Count-MatchingLines $elevated.Output 'CNMON_NATIVE_COMMAND_WINDOWS_CONSOLE key=Q report=NO_COLOR-empty console=rendered literal-escape=false input=restored output=restored cursor=restored history=stopped') -ne 1) {
        throw 'private-console NO_COLOR-empty test did not emit exactly one key and restoration marker'
    }
    if ((Count-MatchingLines $elevated.Output 'CNMON_NATIVE_COMMAND_WINDOWS_CONSOLE output-loss=product-stderr-only useful-rounds>=2 q=input-alive report=complete history=stopped input=restored') -ne 1) {
        throw 'private-console output-handle-loss test did not emit exactly one useful-work and report marker'
    }
    if ((Count-MatchingLines $elevated.Output 'CNMON_NATIVE_COMMAND_WINDOWS_CONSOLE display-output=blocked selection=verified write=pending db=stopped second-ctrl-c=130 input=restored output=restored cursor=restored') -ne 1) {
        throw 'private-console display-block test did not prove real selection, blocked write, persisted stop, and emergency restore'
    }
    if ((Count-MatchingLines $elevated.Output 'CNMON_NATIVE_COMMAND_WINDOWS_CONSOLE error=history-open exit=3 input=restored output=restored cursor=restored blocked-parent=preserved cause=logged') -ne 1) {
        throw 'private-console error-path test did not emit exactly one restoration/preservation marker'
    }
    if ((Count-MatchingLines $elevated.Output 'CNMON_NATIVE_COMMAND_WINDOWS_CONSOLE ctrlc=first-graceful exit=0 history=stopped input=restored output=restored cursor=restored') -ne 1) {
        throw 'private-console first-Ctrl+C test did not emit exactly one graceful restoration marker'
    }
    if ((Count-MatchingLines $elevated.Output 'CNMON_NATIVE_COMMAND_WINDOWS_CONSOLE ctrlc=first-graceful second=hard-blocked-finalization=pass input=restored output=restored cursor=restored') -ne 1) {
        throw 'private-console Ctrl+C test did not prove hard-interrupt restoration during blocked finalization'
    }

    $name = "cnmc-$([guid]::NewGuid().ToString('N').Substring(0,8))"
    $passwordBytes = [byte[]]::new(32)
    [Security.Cryptography.RandomNumberGenerator]::Fill($passwordBytes)
    $securePassword = ConvertTo-SecureString ([Convert]::ToBase64String($passwordBytes)) -AsPlainText -Force
    $createdUser = New-LocalUser -Name $name -Password $securePassword -AccountNeverExpires -Description 'Temporary cn-mon native command test account' -ErrorAction Stop
    $credential = [System.Management.Automation.PSCredential]::new("$env:COMPUTERNAME\$name", $securePassword)
    $ordinarySid = (Get-LocalUser -Name $name -ErrorAction Stop).SID.Value
    & icacls $artifactDir /grant "${env:COMPUTERNAME}\${name}:(OI)(CI)M" /T /C | Out-String | ForEach-Object { Write-Log $_ }
    if ($LASTEXITCODE -ne 0) { throw "failed to grant temporary account access to test artifacts (exit $LASTEXITCODE)" }

    $ordinaryArgs = @('-test.v', '-test.run=^TestNativeCommandWindowsProbePermissionRole$', '-test.count=1', '-test.timeout=3m')
    $ordinaryEscapedSid = [regex]::Escape($ordinarySid)
    $ordinaryRoleMarker = "CNMON_NATIVE_COMMAND_WINDOWS_ROLE expected_role=ordinary expected_sid=$ordinaryEscapedSid parent_sid=$ordinaryEscapedSid parent_elevated=false parent_admin_enabled=false child_sid=$ordinaryEscapedSid child_elevated=false child_admin_enabled=false"
    $ordinaryResultPrefix = "CNMON_NATIVE_COMMAND_WINDOWS_RESULT role=ordinary sid=$ordinaryEscapedSid "
    $ordinarySuccess = "${ordinaryResultPrefix}icmp=OK exit=0 tcp=open json=pass history=one-round"
    $ordinaryDenied = "${ordinaryResultPrefix}icmp=ERROR exit=3 tcp=open json=pass history=one-round"
    $ordinary = Invoke-CommandTests 'ordinary' $ordinaryArgs 'ordinary' $ordinarySid $credential
    $ordinaryPassCount = Count-MatchingLines $ordinary.Output '--- PASS: TestNativeCommandWindowsProbePermissionRole'
    $ordinaryRoleCount = Count-MatchingLines $ordinary.Output $ordinaryRoleMarker
    $ordinarySuccessCount = Count-MatchingLines $ordinary.Output $ordinarySuccess
    $ordinaryDeniedCount = Count-MatchingLines $ordinary.Output $ordinaryDenied
    $ordinaryWitnessCount = Count-MatchingLines $ordinary.Output 'CNMON_NATIVE_COMMAND_WINDOWS_PERMISSION role=ordinary witness=access-denied cause=observed admin-remedy=present'
    $validOrdinaryResult = ($ordinarySuccessCount -eq 1 -and $ordinaryDeniedCount -eq 0 -and $ordinaryWitnessCount -eq 0) -or
        ($ordinaryDeniedCount -eq 1 -and $ordinarySuccessCount -eq 0 -and $ordinaryWitnessCount -eq 1)
    if ($ordinary.ExitCode -ne 0 -or $ordinaryPassCount -ne 1 -or $ordinaryRoleCount -ne 1 -or -not $validOrdinaryResult) {
        $detail = $ordinary.Output
        if (-not $detail) { $detail = '(native command tests emitted no output)' }
        if ($ordinaryPassCount -ne 1 -or $ordinaryRoleCount -ne 1) { $detail += "`nexpected one test pass and one matching parent/child identity marker; got pass=$ordinaryPassCount role=$ordinaryRoleCount" }
        if (-not $validOrdinaryResult) { $detail += "`nordinary role must produce exactly one success result or a witnessed permission-denial result" }
        Write-GitHubFailure 'cn-mon native Windows command' "phase 'ordinary' failed with exit $($ordinary.ExitCode):`n$detail"
        throw 'required Windows ordinary-user command phase failed'
    }

    Write-Log 'CNMON_NATIVE_WINDOWS_COMMAND_RESULT elevated=pass ordinary=pass private-console=pass'
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
