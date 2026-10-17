# Copyright 2026 cn-mon contributors
# SPDX-License-Identifier: Apache-2.0

$ErrorActionPreference = 'Stop'
$modulePath = Join-Path $PSScriptRoot 'msix-package.psm1'
Import-Module $modulePath -Force -ErrorAction Stop
$root = Split-Path -Parent $PSScriptRoot
$manifest = Join-Path $root 'packaging/windows/AppxManifest.xml'
$sidecar = Join-Path $root 'packaging/windows/cn-mon.exe.manifest'
Assert-CnMonMsixDesktopBehaviorBaseline -ManifestPath $manifest -SidecarPath $sidecar

$temporary = Join-Path ([IO.Path]::GetTempPath()) ('cn-mon-desktop-contract-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $temporary | Out-Null
try {
    $changedManifest = Join-Path $temporary 'AppxManifest.xml'
    $changedSidecar = Join-Path $temporary 'cn-mon.exe.manifest'
    Copy-Item -LiteralPath $manifest -Destination $changedManifest
    Copy-Item -LiteralPath $sidecar -Destination $changedSidecar
    $manifestText = [IO.File]::ReadAllText($changedManifest).Replace("`r`n", "`n").Replace("`n", "`r`n")
    $sidecarText = [IO.File]::ReadAllText($changedSidecar).Replace("`r`n", "`n").Replace("`n", "`r`n")
    [IO.File]::WriteAllText($changedManifest, $manifestText, [Text.UTF8Encoding]::new($false))
    [IO.File]::WriteAllText($changedSidecar, $sidecarText, [Text.UTF8Encoding]::new($false))
    Assert-CnMonMsixDesktopBehaviorBaseline -ManifestPath $changedManifest -SidecarPath $changedSidecar
    [IO.File]::AppendAllText($changedManifest, "`n", [Text.UTF8Encoding]::new($false))
    try {
        Assert-CnMonMsixDesktopBehaviorBaseline -ManifestPath $changedManifest -SidecarPath $changedSidecar
        throw 'changed launch and alias contract was accepted'
    } catch {
        if ($_.Exception.Message -eq 'changed launch and alias contract was accepted') { throw }
        if ($_.Exception.Message -notmatch 'fresh visible') { throw }
    }
    Copy-Item -LiteralPath $manifest -Destination $changedManifest -Force
    [IO.File]::AppendAllText($changedSidecar, "`n", [Text.UTF8Encoding]::new($false))
    try {
        Assert-CnMonMsixDesktopBehaviorBaseline -ManifestPath $changedManifest -SidecarPath $changedSidecar
        throw 'changed executable launch sidecar was accepted'
    } catch {
        if ($_.Exception.Message -eq 'changed executable launch sidecar was accepted') { throw }
        if ($_.Exception.Message -notmatch 'fresh visible') { throw }
    }
    Write-Host 'PASS desktop launch, alias, and Repair behavior inputs require reviewed baseline'
}
finally { Remove-Item -LiteralPath $temporary -Recurse -Force }
