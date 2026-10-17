# Copyright 2026 cn-mon contributors
# SPDX-License-Identifier: Apache-2.0

$ErrorActionPreference = 'Stop'
$path = Join-Path $PSScriptRoot 'msix-ci.ps1'
$tokens = $null
$parseErrors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile($path, [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count) { throw 'msix-ci.ps1 has a syntax error' }
$functionNames = @('Read-NativeResults', 'Assert-NativeResults')
$functions = @($ast.FindAll({ param($node) $node -is [Management.Automation.Language.FunctionDefinitionAst] -and $functionNames -contains $node.Name }, $true))
if ($functions.Count -ne $functionNames.Count) { throw 'native result validation functions are missing' }
$allowedGateStatuses = @('pass', 'fail', 'incomplete')
$gateNames = @('identity', 'package_hash', 'signature', 'account', 'package_registration', 'exact_identity', 'installed_binary_hash', 'alias_identity', 'native_cli', 'icmp', 'history_config', 'shell_aliases', 'upgrade', 'repair', 'uninstall_reinstall', 'conflicts', 'fail_injection', 'cleanup', 'wack')
foreach ($function in $functions) { Invoke-Expression $function.Extent.Text }

function New-Result([string]$Mode, [switch]$ManualIncomplete, [switch]$AllPass) {
    $gates = [ordered]@{}
    foreach ($name in $script:gateNames) { $gates[$name] = 'pass' }
    if ($ManualIncomplete) { $gates.conflicts = 'incomplete'; $gates.repair = 'incomplete' }
    if ($Mode -eq 'Hosted' -and -not $AllPass) { $gates.wack = 'incomplete' }
    $incomplete = @($gates.Values | Where-Object { $_ -eq 'incomplete' }).Count -gt 0
    return [ordered]@{ schema_version = 2; mode = $Mode; overall = if ($incomplete) { 'incomplete' } else { 'pass' }; evidence = [ordered]@{ packaging_revision = ('c' * 40); signed_package_sha256 = ('a' * 64); signed_receipt_sha256 = ('b' * 64) }; gates = $gates }
}

function Write-Result($Value, [string]$Path) {
    [IO.File]::WriteAllText($Path, (ConvertTo-Json -InputObject $Value -Depth 6))
}

function Assert-ProfileRejected([string]$Name, [string]$Path, [string]$Mode = 'Certify') {
    try {
        Assert-NativeResults -ResultsPath $Path -ExpectedMode $Mode -AggregationProfile StoreUpdate | Out-Null
        throw "StoreUpdate profile accepted $Name"
    } catch {
        if ($_.Exception.Message -eq "StoreUpdate profile accepted $Name") { throw }
    }
}

$temp = Join-Path ([IO.Path]::GetTempPath()) ('store-update-qualification-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $temp | Out-Null
try {
    $report = Join-Path $temp 'results.json'
    $certify = New-Result Certify -ManualIncomplete
    Write-Result $certify $report
    $rawCertify = [IO.File]::ReadAllText($report)
    Assert-NativeResults -ResultsPath $report -ExpectedMode Certify -AggregationProfile StoreUpdate | Out-Null
    try { Assert-NativeResults -ResultsPath $report -ExpectedMode Certify; throw 'Full profile accepted incomplete desktop gates' } catch { if ($_.Exception.Message -eq 'Full profile accepted incomplete desktop gates') { throw } }
    if ([IO.File]::ReadAllText($report) -cne $rawCertify) { throw 'StoreUpdate aggregation changed raw qualification evidence' }

    $hosted = New-Result Hosted -ManualIncomplete
    Write-Result $hosted $report
    Assert-NativeResults -ResultsPath $report -ExpectedMode Hosted -AggregationProfile StoreUpdate | Out-Null

    $hostedFull = New-Result Hosted -AllPass
    $hostedFull.gates.wack = 'incomplete'
    Write-Result $hostedFull $report
    Assert-NativeResults -ResultsPath $report -ExpectedMode Hosted | Out-Null
    Assert-NativeResults -ResultsPath $report -ExpectedMode Hosted -AggregationProfile StoreUpdate | Out-Null

    $required = @('identity', 'package_hash', 'signature', 'account', 'package_registration', 'exact_identity', 'installed_binary_hash', 'alias_identity', 'native_cli', 'icmp', 'history_config', 'shell_aliases', 'upgrade', 'uninstall_reinstall', 'fail_injection', 'cleanup', 'wack')
    foreach ($name in $required) {
        $bad = New-Result Certify -ManualIncomplete
        $bad.gates[$name] = 'incomplete'
        $bad.overall = 'incomplete'
        Write-Result $bad $report
        Assert-ProfileRejected "required incomplete gate $name" $report
        $bad.gates[$name] = 'fail'
        $bad.overall = 'fail'
        Write-Result $bad $report
        Assert-ProfileRejected "required failed gate $name" $report
    }
    foreach ($name in @('conflicts', 'repair')) {
        $bad = New-Result Certify -ManualIncomplete
        $bad.gates[$name] = 'fail'
        $bad.overall = 'fail'
        Write-Result $bad $report
        Assert-ProfileRejected "failed manual gate $name" $report
    }

    $bad = New-Result Certify -AllPass
    $bad.overall = 'incomplete'
    Write-Result $bad $report
    Assert-ProfileRejected 'contradictory overall status' $report
    $bad = New-Result Certify -ManualIncomplete
    $bad.gates.Remove('cleanup')
    Write-Result $bad $report
    Assert-ProfileRejected 'missing gate' $report
    $bad.gates.cleanup = 'pass'; $bad.gates.unexpected = 'pass'
    Write-Result $bad $report
    Assert-ProfileRejected 'unknown gate' $report
    $bad = New-Result Certify -AllPass
    $bad.evidence = $null
    Write-Result $bad $report
    Assert-ProfileRejected 'missing evidence' $report

    $pass = New-Result Certify -AllPass
    [IO.File]::WriteAllText($report, (ConvertTo-Json -InputObject $pass -Depth 6))
    Assert-NativeResults -ResultsPath $report -ExpectedMode Certify | Out-Null
    Write-Host 'PASS StoreUpdate aggregation preserves raw gate status and full strict default'
}
finally { Remove-Item -LiteralPath $temp -Recurse -Force }
