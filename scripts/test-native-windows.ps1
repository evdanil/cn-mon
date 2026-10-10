$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false

$repoRoot = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
Set-Location $repoRoot
$artifactDir = $env:CNMON_NATIVE_ARTIFACT_DIR
if (-not $artifactDir) {
    $tempRoot = $env:RUNNER_TEMP
    if (-not $tempRoot) { $tempRoot = [IO.Path]::GetTempPath() }
    $artifactDir = Join-Path $tempRoot 'cn-mon-native-windows'
}
New-Item -ItemType Directory -Force -Path $artifactDir | Out-Null
$artifactDir = (Resolve-Path $artifactDir).Path
$scriptLog = Join-Path $artifactDir 'windows-harness.log'
$exitCode = 0
$createdAliases = [System.Collections.Generic.List[object]]::new()
$createdRules = [System.Collections.Generic.List[string]]::new()
$createdUser = $null
$userCredential = $null
$ip4If = $null
$ip6If = $null
$nativeExe = Join-Path $artifactDir 'probe-native.test.exe'
$nestedNativeExe = Join-Path $artifactDir 'pro-bing-native.test.exe'

function Write-Log([string] $Text) {
    $line = "$(Get-Date -Format o) $Text"
    Add-Content -LiteralPath $scriptLog -Value $line
    Write-Host $line
}

function Write-GitHubFailure([string] $Title, [string] $Message) {
    if ($Message.Length -gt 6000) { $Message = $Message.Substring($Message.Length - 6000) }
    $escaped = $Message.Replace('%', '%25').Replace("`r", '%0D').Replace("`n", '%0A')
    Write-Host "::error title=$Title::$escaped"
}

function Invoke-NativePhase([string] $Phase, [System.Management.Automation.PSCredential] $Credential = $null) {
    $stdout = Join-Path $artifactDir "windows-$Phase.stdout.log"
    $stderr = Join-Path $artifactDir "windows-$Phase.stderr.log"
    $arguments = @('-test.v', '-test.run=^TestNativeICMPGate$', '-test.count=1', '-test.timeout=30s')
    $env:CNMON_NATIVE = '1'
    $env:CNMON_NATIVE_PHASE = $Phase
    $env:CNMON_NATIVE_IPV4_TARGET = '127.0.0.1'
    $env:CNMON_NATIVE_IPV4_ROUTER = '127.0.0.2'
    $env:CNMON_NATIVE_IPV6_TARGET = '::1'
    $env:CNMON_NATIVE_IPV6_ROUTER = 'fd42:636e:1::2'
    $env:CNMON_NATIVE_ARTIFACT_DIR = $artifactDir
    if ($Credential) {
        $process = Start-Process -FilePath $nativeExe -ArgumentList $arguments -Credential $Credential -WorkingDirectory $artifactDir -Wait -PassThru -RedirectStandardOutput $stdout -RedirectStandardError $stderr
        $code = $process.ExitCode
    } else {
        & $nativeExe @arguments 1> $stdout 2> $stderr
        $code = $LASTEXITCODE
    }
    $outText = if (Test-Path $stdout) { Get-Content -Raw -LiteralPath $stdout } else { '' }
    $errText = if (Test-Path $stderr) { Get-Content -Raw -LiteralPath $stderr } else { '' }
    if ($outText) { Write-Log "phase=$Phase stdout:`n$outText" }
    if ($errText) { Write-Log "phase=$Phase stderr:`n$errText" }
    return [pscustomobject]@{ ExitCode = $code; Output = "$outText`n$errText" }
}

function Require-NativePhase([string] $Phase, [System.Management.Automation.PSCredential] $Credential = $null) {
    $result = Invoke-NativePhase $Phase $Credential
    if ($result.ExitCode -ne 0 -or $result.Output -notmatch '--- PASS: TestNativeICMPGate') {
        $details = $result.Output
        if (-not $details) { $details = '(native test emitted no output)' }
        Write-GitHubFailure 'cn-mon native Windows' "phase '$Phase' failed with exit $($result.ExitCode):`n$details"
        throw "required native Windows phase '$Phase' failed"
    }
}

function Require-NestedNativeTests {
    $stdout = Join-Path $artifactDir 'windows-pro-bing-native.stdout.log'
    $stderr = Join-Path $artifactDir 'windows-pro-bing-native.stderr.log'
    $arguments = @('-test.v', '-test.run=^Test(SetInterfaceName|SetSource)Native$', '-test.count=1', '-test.timeout=30s')
    & $nestedNativeExe @arguments 1> $stdout 2> $stderr
    $code = $LASTEXITCODE
    $outText = if (Test-Path $stdout) { Get-Content -Raw -LiteralPath $stdout } else { '' }
    $errText = if (Test-Path $stderr) { Get-Content -Raw -LiteralPath $stderr } else { '' }
    if ($outText) { Write-Log "nested pro-bing native stdout:`n$outText" }
    if ($errText) { Write-Log "nested pro-bing native stderr:`n$errText" }
    $combined = "$outText`n$errText"
    if ($code -ne 0 -or $combined -notmatch '--- PASS: TestSetInterfaceNameNative' -or $combined -notmatch '--- PASS: TestSetSourceNative') {
        if (-not $combined) { $combined = '(native test emitted no output)' }
        Write-GitHubFailure 'cn-mon native Windows pro-bing' "nested native tests failed with exit ${code}:`n$combined"
        throw "required nested pro-bing native tests failed with exit $code"
    }
}

function New-ScopedNativeFirewallRules {
    $group = "cn-mon-native-$([guid]::NewGuid().ToString('N'))"
    # Record before the first firewall mutation so finally can remove a partial group.
    $createdRules.Add($group)
    $null = New-NetFirewallRule -DisplayName "$group-v4" -Group $group -Direction Inbound -Action Allow -Protocol ICMPv4 -IcmpType 3,8 -LocalAddress '127.0.0.1','127.0.0.2' -RemoteAddress '127.0.0.1','127.0.0.2' -Profile Any
    $null = New-NetFirewallRule -DisplayName "$group-v6" -Group $group -Direction Inbound -Action Allow -Protocol ICMPv6 -IcmpType 1,128 -LocalAddress '::1','fd42:636e:1::2' -RemoteAddress '::1','fd42:636e:1::2' -Profile Any
    Write-Log "installed scoped temporary firewall group=$group"
}

try {
    Write-Log "runner=$env:ImageOS imageVersion=$env:ImageVersion"
    Get-ComputerInfo | Select-Object OsName, OsVersion, OsBuildNumber, WindowsVersion | Format-List | Out-String | ForEach-Object { Write-Log $_ }
    $admin = ([Security.Principal.WindowsPrincipal]::new([Security.Principal.WindowsIdentity]::GetCurrent())).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
    Write-Log "setup_identity=$([Security.Principal.WindowsIdentity]::GetCurrent().Name) is_admin=$admin"
    if (-not $admin) { throw 'native Windows setup runner is not elevated; expected hosted administrator context' }

    & go test -c -o $nativeExe ./internal/probe
    if ($LASTEXITCODE -ne 0) { throw "go test -c failed with exit $LASTEXITCODE" }
    & go -C third_party/pro-bing test -mod=readonly -c -tags=native -o $nestedNativeExe .
    if ($LASTEXITCODE -ne 0) { throw "nested pro-bing native test compilation failed with exit $LASTEXITCODE" }

    $ip4If = (Get-NetIPAddress -AddressFamily IPv4 -IPAddress '127.0.0.1' -ErrorAction Stop | Select-Object -First 1).InterfaceIndex
    $ip6If = (Get-NetIPAddress -AddressFamily IPv6 -IPAddress '::1' -ErrorAction Stop | Select-Object -First 1).InterfaceIndex
    foreach ($interface in @(@{ Index = $ip4If; Family = 'IPv4' }, @{ Index = $ip6If; Family = 'IPv6' })) {
        Get-NetIPInterface -InterfaceIndex $interface.Index -AddressFamily $interface.Family |
            Select-Object InterfaceIndex, InterfaceAlias, AddressFamily, Dhcp, ConnectionState, InterfaceMetric |
            Format-List | Out-String | ForEach-Object { Write-Log "native test interface:`n$_" }
    }
    foreach ($address in @('127.0.0.2', 'fd42:636e:1::2')) {
        if (Get-NetIPAddress -IPAddress $address -ErrorAction SilentlyContinue) { throw "refusing to reuse preexisting test alias $address" }
    }
    $alias4 = New-NetIPAddress -InterfaceIndex $ip4If -IPAddress '127.0.0.2' -PrefixLength 8 -SkipAsSource $true -PolicyStore ActiveStore -ErrorAction Stop
    $createdAliases.Add($alias4)
    $alias6 = New-NetIPAddress -InterfaceIndex $ip6If -IPAddress 'fd42:636e:1::2' -PrefixLength 128 -SkipAsSource $true -PolicyStore ActiveStore -ErrorAction Stop
    $createdAliases.Add($alias6)
    foreach ($interface in @(@{ Index = $ip4If; Family = 'IPv4' }, @{ Index = $ip6If; Family = 'IPv6' })) {
        Get-NetIPAddress -InterfaceIndex $interface.Index -AddressFamily $interface.Family |
            Select-Object IPAddress, AddressFamily, PrefixLength, AddressState, SkipAsSource, PolicyStore |
            Format-Table -AutoSize | Out-String | ForEach-Object { Write-Log "native test interface addresses:`n$_" }
    }
    foreach ($expected in @(@{ Address = '127.0.0.2'; Family = 'IPv4' }, @{ Address = 'fd42:636e:1::2'; Family = 'IPv6' })) {
        $ready = $false
        for ($attempt = 0; $attempt -lt 50; $attempt++) {
            $entry = Get-NetIPAddress -IPAddress $expected.Address -AddressFamily $expected.Family -ErrorAction SilentlyContinue | Select-Object -First 1
            if ($entry -and $entry.AddressState -eq 'Preferred') { $ready = $true; break }
            Start-Sleep -Milliseconds 100
        }
        if (-not $ready) { throw "temporary alias did not become preferred: $($expected.Address)" }
    }
    Get-NetIPAddress -InterfaceIndex $ip4If -AddressFamily IPv4 | Format-Table -AutoSize | Out-String | ForEach-Object { Write-Log $_ }
    Get-NetIPAddress -InterfaceIndex $ip6If -AddressFamily IPv6 | Format-Table -AutoSize | Out-String | ForEach-Object { Write-Log $_ }

    Require-NativePhase 'echo'
    Require-NativePhase 'granted'
    Require-NestedNativeTests

    # The default firewall path is recorded first. If required packet delivery is
    # blocked, retry only after adding narrowly scoped, uniquely grouped rules.
    $targetDefault = Invoke-NativePhase 'target-error'
    if ($targetDefault.ExitCode -ne 0 -or $targetDefault.Output -notmatch '--- PASS: TestNativeICMPGate') {
        Write-Log 'default firewall target-error delivery did not pass; retrying with scoped rules'
        New-ScopedNativeFirewallRules
        Require-NativePhase 'target-error'
    }
    $routerDefault = Invoke-NativePhase 'intermediate-error'
    if ($routerDefault.ExitCode -ne 0 -or $routerDefault.Output -notmatch '--- PASS: TestNativeICMPGate') {
        if ($createdRules.Count -eq 0) {
            Write-Log 'default firewall intermediate-error delivery did not pass; retrying with scoped rules'
            New-ScopedNativeFirewallRules
        }
        Require-NativePhase 'intermediate-error'
    }

    $name = "cnmn-$([guid]::NewGuid().ToString('N').Substring(0,12))"
    $passwordBytes = [byte[]]::new(32)
    [Security.Cryptography.RandomNumberGenerator]::Fill($passwordBytes)
    $password = [Convert]::ToBase64String($passwordBytes)
    $securePassword = ConvertTo-SecureString $password -AsPlainText -Force
    $createdUser = New-LocalUser -Name $name -Password $securePassword -AccountNeverExpires -Description 'Temporary cn-mon native test account' -ErrorAction Stop
    $userCredential = [System.Management.Automation.PSCredential]::new("$env:COMPUTERNAME\$name", $securePassword)
    & icacls $artifactDir /grant "${env:COMPUTERNAME}\${name}:(OI)(CI)M" /T /C | Out-String | ForEach-Object { Write-Log $_ }
    if ($LASTEXITCODE -ne 0) { throw "failed to grant temporary test account access to artifacts (exit $LASTEXITCODE)" }
    Require-NativePhase 'denied' $userCredential
} catch {
    $exitCode = 1
    Write-Log "FAIL $($_.Exception.Message)`n$($_.ScriptStackTrace)"
    $failureDetails = $_.Exception.Message
    if ($_.InvocationInfo.PositionMessage) { $failureDetails += "`n$($_.InvocationInfo.PositionMessage)" }
    if ($_.ScriptStackTrace) { $failureDetails += "`n$($_.ScriptStackTrace)" }
    Write-GitHubFailure 'cn-mon native Windows harness' $failureDetails
} finally {
    foreach ($group in $createdRules) {
        try {
            $rules = @(Get-NetFirewallRule -Group $group -ErrorAction SilentlyContinue)
            if ($rules.Count -gt 0) {
                $rules | Remove-NetFirewallRule -ErrorAction Stop
                Write-Log "removed temporary firewall group=$group"
            } else {
                Write-Log "no temporary firewall rules remained for group=$group"
            }
        } catch {
            $exitCode = 1
            Write-Log "FAIL firewall cleanup group=$group error=$($_.Exception.Message)"
        }
    }
    foreach ($address in @($createdAliases | Sort-Object { $_.IPAddress } -Descending)) {
        try {
            Remove-NetIPAddress -InterfaceIndex $address.InterfaceIndex -IPAddress $address.IPAddress -PolicyStore ActiveStore -Confirm:$false -ErrorAction Stop
            Write-Log "removed temporary alias=$($address.IPAddress)"
        } catch {
            $exitCode = 1
            Write-Log "FAIL alias cleanup address=$($address.IPAddress) error=$($_.Exception.Message)"
        }
    }
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
Write-Log "native Windows probe gate completed"
