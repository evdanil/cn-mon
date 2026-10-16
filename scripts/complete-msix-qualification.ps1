# Copyright 2026 cn-mon contributors
# SPDX-License-Identifier: Apache-2.0

[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)][ValidateSet('LocalAudit', 'ReviewedRecord')][string]$Mode,
    [Parameter(Mandatory = $true)][string]$BaselinePackagePath,
    [Parameter(Mandatory = $true)][string]$BaselineReceiptPath,
    [Parameter(Mandatory = $true)][string]$BaselineCertifyResultPath,
    [Parameter(Mandatory = $true)][string]$SupplementPath,
    [Parameter(Mandatory = $true)][string]$ReviewAttestationPath,
    [string]$ScreenshotDirectory = '',
    [Parameter(Mandatory = $true)][string]$ExpectedNativeHelperSha256,
    [string]$NativeHelperPath = '',
    [string]$LocalAuditRecordPath = '',
    [string]$ExpectedLocalAuditRecordSha256 = '',
    [Parameter(Mandatory = $true)][string]$DesktopDriverPath,
    [Parameter(Mandatory = $true)][string]$ExpectedValidatorRevision,
    [Parameter(Mandatory = $true)][string]$OutputPath
)

$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $false

function Get-CnMonMsixCompletionScreenshotNames {
    return @(
        'alias-off-settings.png',
        'alias-on-settings.png',
        'repair-start.png',
        'repair-progress.png',
        'repair-complete.png',
        '01-cli-help.png',
        '02-icmp-table.png',
        '03-json-tcp.png',
        '04-history-list.png',
        '05-history-detail.png'
    )
}

function Assert-CnMonMsixScreenshotDirectory {
    param([Parameter(Mandatory = $true)][string]$Directory)
    try {
        if (-not [IO.Directory]::Exists($Directory) -or
            ([IO.File]::GetAttributes($Directory) -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw 'invalid screenshot directory'
        }
        $expected = @(Get-CnMonMsixCompletionScreenshotNames)
        $seen = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
        $entries = @(Get-ChildItem -LiteralPath $Directory -Force -ErrorAction Stop)
        if ($entries.Count -ne $expected.Count) { throw 'unexpected screenshot evidence entries' }
        foreach ($entry in $entries) {
            if ($entry.PSIsContainer -or $entry.Name -cnotin $expected -or
                ($entry.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
                throw 'unexpected screenshot evidence entry'
            }
            if (-not $seen.Add([string]$entry.Name)) { throw 'duplicate screenshot evidence entry' }
        }
        foreach ($name in $expected) {
            if (-not $seen.Contains($name)) { throw 'missing screenshot evidence entry' }
        }
    }
    catch { throw 'private screenshot directory must contain exactly the allowlisted PNG files' }
}

function Assert-CnMonMsixJsonHasNoDuplicateProperties {
    param([Parameter(Mandatory = $true)][System.Text.Json.JsonElement]$Element)
    if ($Element.ValueKind -eq [System.Text.Json.JsonValueKind]::Object) {
        $names = [Collections.Generic.HashSet[string]]::new([StringComparer]::Ordinal)
        foreach ($property in $Element.EnumerateObject()) {
            if (-not $names.Add($property.Name)) { throw 'JSON contains duplicate fields' }
            Assert-CnMonMsixJsonHasNoDuplicateProperties -Element $property.Value
        }
    }
    elseif ($Element.ValueKind -eq [System.Text.Json.JsonValueKind]::Array) {
        foreach ($item in $Element.EnumerateArray()) {
            Assert-CnMonMsixJsonHasNoDuplicateProperties -Element $item
        }
    }
}

function ConvertFrom-CnMonMsixStrictJson {
    param([Parameter(Mandatory = $true)][string]$Json)
    $document = $null
    try {
        $document = [System.Text.Json.JsonDocument]::Parse($Json)
        Assert-CnMonMsixJsonHasNoDuplicateProperties -Element $document.RootElement
        $value = ConvertFrom-Json -InputObject $Json -AsHashtable -Depth 64 -ErrorAction Stop
        if ($null -eq $value -or -not ($value -is [System.Collections.IDictionary])) {
            throw 'JSON root is invalid'
        }
        return $value
    }
    catch { throw 'JSON input is invalid' }
    finally { if ($null -ne $document) { $document.Dispose() } }
}

function Get-CnMonMsixPngCrc32 {
    param(
        [Parameter(Mandatory = $true)][byte[]]$Bytes,
        [Parameter(Mandatory = $true)][int]$Offset,
        [Parameter(Mandatory = $true)][int]$Count
    )
    [uint32]$crc = [uint32]::MaxValue
    [uint32]$polynomial = 3988292384
    for ($index = 0; $index -lt $Count; $index++) {
        $crc = [uint32]($crc -bxor [uint32]$Bytes[$Offset + $index])
        for ($bit = 0; $bit -lt 8; $bit++) {
            if (($crc -band [uint32]1) -ne 0) {
                $crc = [uint32](($crc -shr 1) -bxor $polynomial)
            }
            else { $crc = [uint32]($crc -shr 1) }
        }
    }
    return [uint32]([uint64]$crc -bxor [uint64]4294967295)
}

function Read-CnMonMsixBoundedBytes {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][long]$MinimumLength,
        [Parameter(Mandatory = $true)][long]$MaximumLength
    )
    $stream = $null
    try {
        $stream = [IO.File]::OpenRead($Path)
        if ($stream.Length -lt $MinimumLength -or $stream.Length -gt $MaximumLength) {
            throw 'input size is outside its allowed bounds'
        }
        $length = [int]$stream.Length
        $bytes = [byte[]]::new($length)
        $offset = 0
        while ($offset -lt $length) {
            $count = $stream.Read($bytes, $offset, $length - $offset)
            if ($count -le 0) { throw 'input changed while being read' }
            $offset += $count
        }
        if ($stream.ReadByte() -ne -1) { throw 'input grew while being read' }
        return ,$bytes
    }
    catch { throw 'a required qualification input is unavailable or outside its allowed size' }
    finally { if ($null -ne $stream) { $stream.Dispose() } }
}

function Assert-CnMonMsixPngPixelStream {
    param(
        [Parameter(Mandatory = $true)][byte[]]$CompressedData,
        [Parameter(Mandatory = $true)][long]$Width,
        [Parameter(Mandatory = $true)][long]$Height,
        [Parameter(Mandatory = $true)][int]$Channels
    )
    $inputStream = $null
    $decoder = $null
    try {
        if ($CompressedData.Length -lt 6 -or $Width -lt 1 -or $Height -lt 1 -or $Channels -notin @(3, 4)) {
            throw 'pixel stream parameters are invalid'
        }
        $rowLength = $Width * $Channels
        $scanlineLength = $rowLength + 1
        $decodedLength = $scanlineLength * $Height
        if ($rowLength -gt 67108864 -or $decodedLength -gt 104857600) {
            throw 'decoded screenshot exceeds the size bound'
        }
        $inputStream = [IO.MemoryStream]::new($CompressedData, $false)
        $decoder = [IO.Compression.ZLibStream]::new($inputStream, [IO.Compression.CompressionMode]::Decompress, $false)
        $scanline = [byte[]]::new([int]$scanlineLength)
        for ($row = 0; $row -lt $Height; $row++) {
            $offset = 0
            while ($offset -lt $scanline.Length) {
                $count = $decoder.Read($scanline, $offset, $scanline.Length - $offset)
                if ($count -le 0) { throw 'pixel stream ended before all rows were decoded' }
                $offset += $count
            }
            if ($scanline[0] -gt 4) { throw 'pixel stream has an invalid PNG row filter' }
        }
        if ($decoder.ReadByte() -ne -1) { throw 'pixel stream contains extra decoded data' }
    }
    catch { throw 'screenshot pixel stream is invalid' }
    finally {
        if ($null -ne $decoder) { $decoder.Dispose() }
        if ($null -ne $inputStream) { $inputStream.Dispose() }
    }
}

function Assert-CnMonMsixWindowsPngDecodesFully {
    param(
        [Parameter(Mandatory = $true)][byte[]]$Bytes,
        [Parameter(Mandatory = $true)][long]$Width,
        [Parameter(Mandatory = $true)][long]$Height
    )
    if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) { return }
    $imageStream = $null
    $image = $null
    $bitmap = $null
    $locked = $null
    try {
        $imageStream = [IO.MemoryStream]::new($Bytes, $false)
        $image = [Drawing.Image]::FromStream($imageStream, $false, $true)
        if ($image.Width -ne $Width -or $image.Height -ne $Height) { throw 'decoded image dimensions changed' }
        $bitmap = [Drawing.Bitmap]::new($image)
        $rectangle = [Drawing.Rectangle]::new(0, 0, [int]$Width, [int]$Height)
        $locked = $bitmap.LockBits($rectangle, [Drawing.Imaging.ImageLockMode]::ReadOnly, [Drawing.Imaging.PixelFormat]::Format32bppArgb)
        $decodedBytes = [Math]::Abs([long]$locked.Stride) * $Height
        if ($decodedBytes -gt 104857600) { throw 'decoded screenshot exceeds the size bound' }
        $pixels = [byte[]]::new([int]$decodedBytes)
        [Runtime.InteropServices.Marshal]::Copy($locked.Scan0, $pixels, 0, $pixels.Length)
    }
    catch { throw 'screenshot could not be fully decoded' }
    finally {
        if ($null -ne $locked -and $null -ne $bitmap) { $bitmap.UnlockBits($locked) }
        if ($null -ne $bitmap) { $bitmap.Dispose() }
        if ($null -ne $image) { $image.Dispose() }
        if ($null -ne $imageStream) { $imageStream.Dispose() }
    }
}

function Get-CnMonMsixPngObservation {
    param(
        [Parameter(Mandatory = $true)][string]$Directory,
        [Parameter(Mandatory = $true)][string]$Name
    )
    if ($Name -cnotin (Get-CnMonMsixCompletionScreenshotNames)) { throw 'screenshot name is not allowed' }
    try {
        $path = Join-Path -Path $Directory -ChildPath $Name
        $bytes = Read-CnMonMsixBoundedBytes -Path $path -MinimumLength 45 -MaximumLength 52428800
        $signature = [byte[]](137, 80, 78, 71, 13, 10, 26, 10)
        for ($i = 0; $i -lt $signature.Length; $i++) {
            if ($bytes[$i] -ne $signature[$i]) { throw 'screenshot signature is invalid' }
        }

        $offset = 8
        $width = 0L
        $height = 0L
        $channels = 0
        $sawHeader = $false
        $sawData = $false
        $idatEnded = $false
        $idatStream = [IO.MemoryStream]::new()
        $sawEnd = $false
        while ($offset -lt $bytes.Length) {
            if ($bytes.Length - $offset -lt 12) { throw 'screenshot chunks are truncated' }
            $chunkLength = ([long]$bytes[$offset] * 16777216) + ([long]$bytes[$offset + 1] * 65536) +
                ([long]$bytes[$offset + 2] * 256) + [long]$bytes[$offset + 3]
            if ($chunkLength -gt 52428800 -or $chunkLength -gt ($bytes.Length - $offset - 12)) {
                throw 'screenshot chunk length is invalid'
            }
            $chunkType = [Text.Encoding]::ASCII.GetString($bytes, $offset + 4, 4)
            $crcCount = [int](4 + $chunkLength)
            $actualCrc = Get-CnMonMsixPngCrc32 -Bytes $bytes -Offset ($offset + 4) -Count $crcCount
            $crcOffset = $offset + 8 + [int]$chunkLength
            $storedCrc = ([long]$bytes[$crcOffset] * 16777216) + ([long]$bytes[$crcOffset + 1] * 65536) +
                ([long]$bytes[$crcOffset + 2] * 256) + [long]$bytes[$crcOffset + 3]
            if ([long]$actualCrc -ne $storedCrc) { throw 'screenshot chunk checksum is invalid' }

            if (-not $sawHeader) {
                if ($chunkType -cne 'IHDR' -or $chunkLength -ne 13) { throw 'screenshot header is invalid' }
                $width = ([long]$bytes[$offset + 8] * 16777216) + ([long]$bytes[$offset + 9] * 65536) +
                    ([long]$bytes[$offset + 10] * 256) + [long]$bytes[$offset + 11]
                $height = ([long]$bytes[$offset + 12] * 16777216) + ([long]$bytes[$offset + 13] * 65536) +
                    ([long]$bytes[$offset + 14] * 256) + [long]$bytes[$offset + 15]
                if ($width -lt 1 -or $height -lt 1 -or $width -gt 100000 -or $height -gt 100000) {
                    throw 'screenshot dimensions are invalid'
                }
                $bitDepth = $bytes[$offset + 16]
                $colorType = $bytes[$offset + 17]
                $compressionMethod = $bytes[$offset + 18]
                $filterMethod = $bytes[$offset + 19]
                $interlaceMethod = $bytes[$offset + 20]
                if ($bitDepth -ne 8 -or $colorType -notin @(2, 6) -or $compressionMethod -ne 0 -or
                    $filterMethod -ne 0 -or $interlaceMethod -ne 0) {
                    throw 'screenshot encoding is unsupported'
                }
                $channels = if ($colorType -eq 2) { 3 } else { 4 }
                if (($width * $height) -gt 25000000) { throw 'screenshot pixel count exceeds the size bound' }
                $sawHeader = $true
            }
            elseif ($chunkType -ceq 'IHDR') { throw 'screenshot header is duplicated' }

            if ($sawData -and $chunkType -cne 'IDAT' -and $chunkType -cne 'IEND') { $idatEnded = $true }
            if ($chunkType -ceq 'IDAT') {
                if ($idatEnded) { throw 'screenshot image data chunks are not contiguous' }
                $sawData = $true
                if ($chunkLength -gt 0) { $idatStream.Write($bytes, $offset + 8, [int]$chunkLength) }
            }
            if ($chunkType -ceq 'IEND') {
                if ($chunkLength -ne 0 -or -not $sawData) { throw 'screenshot end chunk is invalid' }
                $sawEnd = $true
                $offset += 12
                if ($offset -ne $bytes.Length) { throw 'screenshot has trailing data' }
                break
            }
            $offset += 12 + [int]$chunkLength
        }
        if (-not $sawHeader -or -not $sawData -or -not $sawEnd) { throw 'screenshot is incomplete' }
        Assert-CnMonMsixPngPixelStream -CompressedData $idatStream.ToArray() -Width $width -Height $height -Channels $channels
        Assert-CnMonMsixWindowsPngDecodesFully -Bytes $bytes -Width $width -Height $height
        $sha = [Security.Cryptography.SHA256]::Create()
        try { $hash = [Convert]::ToHexString($sha.ComputeHash($bytes)).ToLowerInvariant() }
        finally { $sha.Dispose() }
        return [ordered]@{ name = $Name; sha256 = $hash; width = $width; height = $height }
    }
    catch { throw 'a required screenshot is unavailable or invalid' }
    finally { if ($null -ne $idatStream) { $idatStream.Dispose() } }
}

function Test-CnMonMsixExactKeys {
    param(
        [object]$Value,
        [Parameter(Mandatory = $true)][string[]]$Expected
    )
    if ($null -eq $Value -or -not ($Value -is [System.Collections.IDictionary])) { return $false }
    $actual = @($Value.Keys | ForEach-Object { [string]$_ })
    if ($actual.Count -ne $Expected.Count) { return $false }
    foreach ($key in $Expected) {
        if (@($actual | Where-Object { $_ -ceq $key }).Count -ne 1) { return $false }
    }
    return $true
}

function Test-CnMonMsixJsonInteger {
    param([object]$Value)
    return ($Value -is [int] -or $Value -is [long])
}

function Get-CnMonMsixDesktopCompletionReport {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)][string]$PackageSha256,
        [Parameter(Mandatory = $true)][string]$ReceiptSha256,
        [Parameter(Mandatory = $true)][string]$CertifyResultSha256,
        [Parameter(Mandatory = $true)][System.Collections.IDictionary]$Receipt,
        [Parameter(Mandatory = $true)][System.Collections.IDictionary]$CertifyResult,
        [Parameter(Mandatory = $true)][System.Collections.IDictionary]$Supplement,
        [Parameter(Mandatory = $true)][System.Collections.IDictionary]$ReviewAttestation,
        [Parameter(Mandatory = $true)][string]$SupplementSha256,
        [Parameter(Mandatory = $true)][string]$ReviewAttestationSha256,
        [Parameter(Mandatory = $true)][string]$ExpectedNativeHelperSha256,
        [string]$ActualNativeHelperSha256 = '',
        [Parameter(Mandatory = $true)][string]$DesktopDriverSha256,
        [Parameter(Mandatory = $true)][string]$ExpectedValidatorRevision,
        [Parameter(Mandatory = $true)][object[]]$ActualScreenshots
    )

    $expectedPackageSha256 = 'd782d72374fe6bef2ca666e6cf64039283d0b1b24f4c9aa22cd940956873642f'
    $expectedReceiptSha256 = '7f7ddda42b87b7611fe5107aeb519e3cf0e04e5e2870a7dc8374d6c7b74db55b'
    $expectedCertifyResultSha256 = 'ebdaa5f3b456afa5e183910b25e80385e3e1d0d86c8db7fee7d7847c9a99971f'
    $expectedArchiveSha256 = 'ed18d14a6a4e17526bd73d47f68336d9ee45ead71c15e61f106fba257d461818'
    $expectedPackagingRevision = 'b6c874d7b5de8257dde37685f0c3bfeb79b50b14'
    $expectedValidatorAuthority = 'independent_root_reviewer'
    $expectedValidatorMode = 'desktop_completion_review'
    $expectedGateNames = @(
        'identity', 'package_hash', 'signature', 'account', 'package_registration', 'exact_identity',
        'installed_binary_hash', 'alias_identity', 'native_cli', 'icmp', 'history_config', 'shell_aliases',
        'upgrade', 'repair', 'uninstall_reinstall', 'conflicts', 'fail_injection', 'cleanup', 'wack'
    )
    $listingScreenshotNames = @('01-cli-help.png', '02-icmp-table.png', '03-json-tcp.png', '04-history-list.png', '05-history-detail.png')
    $requiredScreenshots = @(Get-CnMonMsixCompletionScreenshotNames)

    foreach ($hash in @($PackageSha256, $ReceiptSha256, $CertifyResultSha256, $SupplementSha256,
            $ReviewAttestationSha256, $ExpectedNativeHelperSha256, $DesktopDriverSha256)) {
        if ($hash -cnotmatch '^[0-9a-f]{64}$') { throw 'completion evidence hash is invalid' }
    }
    if ($ActualNativeHelperSha256 -cnotmatch '^[0-9a-f]{64}$' -or
        $ActualNativeHelperSha256 -cne $ExpectedNativeHelperSha256) {
        throw 'local native helper bytes do not match the pinned digest'
    }
    if ($PackageSha256 -cne $expectedPackageSha256 -or $ReceiptSha256 -cne $expectedReceiptSha256 -or
        $CertifyResultSha256 -cne $expectedCertifyResultSha256) {
        throw 'baseline bytes do not match the immutable candidate'
    }
    if ($ExpectedValidatorRevision -cnotmatch '^[0-9a-f]{40}$') { throw 'validator revision is invalid' }

    if (-not (Test-CnMonMsixJsonInteger $Receipt.schema_version) -or $Receipt.schema_version -ne 1 -or
        $Receipt.purpose -isnot [string] -or $Receipt.purpose -cne 'candidate' -or
        $Receipt.package_sha256 -isnot [string] -or $Receipt.package_sha256 -cne $expectedPackageSha256 -or
        $Receipt.packaging_revision -isnot [string] -or $Receipt.packaging_revision -cne $expectedPackagingRevision) {
        throw 'baseline receipt does not match the immutable candidate'
    }

    if (-not (Test-CnMonMsixExactKeys $CertifyResult @('schema_version', 'mode', 'overall', 'evidence', 'gates')) -or
        -not (Test-CnMonMsixJsonInteger $CertifyResult.schema_version) -or $CertifyResult.schema_version -ne 2 -or
        $CertifyResult.mode -isnot [string] -or $CertifyResult.mode -cne 'Certify' -or
        $CertifyResult.overall -isnot [string] -or $CertifyResult.overall -cne 'incomplete' -or
        -not (Test-CnMonMsixExactKeys $CertifyResult.evidence @('signed_package_sha256', 'signed_receipt_sha256', 'packaging_revision')) -or
        $CertifyResult.evidence.signed_package_sha256 -isnot [string] -or
        $CertifyResult.evidence.signed_package_sha256 -cne $expectedPackageSha256 -or
        $CertifyResult.evidence.signed_receipt_sha256 -isnot [string] -or
        $CertifyResult.evidence.signed_receipt_sha256 -cne $expectedReceiptSha256 -or
        $CertifyResult.evidence.packaging_revision -isnot [string] -or
        $CertifyResult.evidence.packaging_revision -cne $expectedPackagingRevision -or
        -not (Test-CnMonMsixExactKeys $CertifyResult.gates $expectedGateNames)) {
        throw 'baseline Certify result does not match the closed schema'
    }
    foreach ($gate in $expectedGateNames) {
        if ($CertifyResult.gates[$gate] -isnot [string] -or
            $CertifyResult.gates[$gate] -cnotin @('pass', 'incomplete', 'fail')) {
            throw 'baseline Certify gate status is invalid'
        }
        if ($gate -in @('repair', 'conflicts')) {
            if ($CertifyResult.gates[$gate] -cne 'incomplete') { throw 'baseline desktop gate state is invalid' }
        }
        elseif ($CertifyResult.gates[$gate] -cne 'pass') {
            throw 'a non-desktop baseline gate is not passing'
        }
    }

    $candidateKeys = @('run_id', 'artifact_id', 'archive_sha256', 'signed_package_sha256', 'signed_receipt_sha256', 'certify_result_sha256', 'packaging_revision')
    if (-not (Test-CnMonMsixExactKeys $Supplement @('schema_version', 'mode', 'overall', 'candidate', 'qualification_scripts', 'observations')) -or
        -not (Test-CnMonMsixJsonInteger $Supplement.schema_version) -or $Supplement.schema_version -ne 1 -or
        $Supplement.mode -isnot [string] -or $Supplement.mode -cne 'desktop_completion_supplement' -or
        $Supplement.overall -isnot [string] -or $Supplement.overall -cne 'incomplete' -or
        -not (Test-CnMonMsixExactKeys $Supplement.candidate $candidateKeys)) {
        throw 'desktop supplement schema is invalid'
    }
    $candidate = $Supplement.candidate
    if ($candidate.run_id -isnot [string] -or $candidate.run_id -cne '38866515555' -or
        $candidate.artifact_id -isnot [string] -or $candidate.artifact_id -cne '12031747143' -or
        $candidate.archive_sha256 -isnot [string] -or $candidate.archive_sha256 -cne $expectedArchiveSha256 -or
        $candidate.signed_package_sha256 -isnot [string] -or $candidate.signed_package_sha256 -cne $expectedPackageSha256 -or
        $candidate.signed_receipt_sha256 -isnot [string] -or $candidate.signed_receipt_sha256 -cne $expectedReceiptSha256 -or
        $candidate.certify_result_sha256 -isnot [string] -or $candidate.certify_result_sha256 -cne $expectedCertifyResultSha256 -or
        $candidate.packaging_revision -isnot [string] -or $candidate.packaging_revision -cne $expectedPackagingRevision) {
        throw 'desktop supplement belongs to a different candidate'
    }

    if (-not (Test-CnMonMsixExactKeys $Supplement.qualification_scripts @('helper_sha256', 'desktop_driver_sha256', 'validator_revision')) -or
        $Supplement.qualification_scripts.helper_sha256 -isnot [string] -or
        $Supplement.qualification_scripts.helper_sha256 -cnotmatch '^[0-9a-f]{64}$' -or
        $Supplement.qualification_scripts.helper_sha256 -cne $ExpectedNativeHelperSha256 -or
        $Supplement.qualification_scripts.desktop_driver_sha256 -isnot [string] -or
        $Supplement.qualification_scripts.desktop_driver_sha256 -cne $DesktopDriverSha256 -or
        $Supplement.qualification_scripts.validator_revision -isnot [string] -or
        $Supplement.qualification_scripts.validator_revision -cne $ExpectedValidatorRevision) {
        throw 'desktop qualification scripts do not match the recorded revision'
    }

    if (-not (Test-CnMonMsixExactKeys $Supplement.observations @(
            'ordinary_user', 'medium_integrity', 'interactive_session', 'alias', 'repair', 'screenshots', 'desktop_cleanup_passed')) -or
        $Supplement.observations.ordinary_user -isnot [bool] -or -not $Supplement.observations.ordinary_user -or
        $Supplement.observations.medium_integrity -isnot [bool] -or -not $Supplement.observations.medium_integrity -or
        $Supplement.observations.interactive_session -isnot [bool] -or -not $Supplement.observations.interactive_session -or
        $Supplement.observations.desktop_cleanup_passed -isnot [bool] -or -not $Supplement.observations.desktop_cleanup_passed) {
        throw 'desktop session or cleanup observations are incomplete'
    }
    if (-not (Test-CnMonMsixExactKeys $Supplement.observations.alias @('off', 'on', 'path_unchanged')) -or
        $Supplement.observations.alias.path_unchanged -isnot [bool] -or -not $Supplement.observations.alias.path_unchanged) {
        throw 'alias observations are incomplete'
    }
    $offAliasKeys = @('settings_opened', 'settings_verified', 'pwsh_failure', 'pwsh_success', 'cmd_failure', 'cmd_success')
    $onAliasKeys = @('settings_opened', 'settings_verified', 'pwsh_failure', 'pwsh_success', 'cmd_failure', 'cmd_success', 'packaged_process_identity_verified')
    if (-not (Test-CnMonMsixExactKeys $Supplement.observations.alias.off $offAliasKeys) -or
        $Supplement.observations.alias.off.settings_opened -isnot [bool] -or -not $Supplement.observations.alias.off.settings_opened -or
        $Supplement.observations.alias.off.settings_verified -isnot [bool] -or -not $Supplement.observations.alias.off.settings_verified -or
        $Supplement.observations.alias.off.pwsh_failure -isnot [bool] -or -not $Supplement.observations.alias.off.pwsh_failure -or
        $Supplement.observations.alias.off.pwsh_success -isnot [bool] -or $Supplement.observations.alias.off.pwsh_success -or
        $Supplement.observations.alias.off.cmd_failure -isnot [bool] -or -not $Supplement.observations.alias.off.cmd_failure -or
        $Supplement.observations.alias.off.cmd_success -isnot [bool] -or $Supplement.observations.alias.off.cmd_success) {
        throw 'alias Off evidence is invalid'
    }
    if (-not (Test-CnMonMsixExactKeys $Supplement.observations.alias.on $onAliasKeys) -or
        $Supplement.observations.alias.on.settings_opened -isnot [bool] -or -not $Supplement.observations.alias.on.settings_opened -or
        $Supplement.observations.alias.on.settings_verified -isnot [bool] -or -not $Supplement.observations.alias.on.settings_verified -or
        $Supplement.observations.alias.on.pwsh_failure -isnot [bool] -or $Supplement.observations.alias.on.pwsh_failure -or
        $Supplement.observations.alias.on.pwsh_success -isnot [bool] -or -not $Supplement.observations.alias.on.pwsh_success -or
        $Supplement.observations.alias.on.cmd_failure -isnot [bool] -or $Supplement.observations.alias.on.cmd_failure -or
        $Supplement.observations.alias.on.cmd_success -isnot [bool] -or -not $Supplement.observations.alias.on.cmd_success -or
        $Supplement.observations.alias.on.packaged_process_identity_verified -isnot [bool] -or
        -not $Supplement.observations.alias.on.packaged_process_identity_verified) {
        throw 'alias On evidence is invalid'
    }

    if (-not (Test-CnMonMsixExactKeys $Supplement.observations.repair @(
            'activation_observed', 'completion_observed', 'data_before_sha256', 'data_after_sha256', 'readback_verified')) -or
        $Supplement.observations.repair.activation_observed -isnot [bool] -or -not $Supplement.observations.repair.activation_observed -or
        $Supplement.observations.repair.completion_observed -isnot [bool] -or -not $Supplement.observations.repair.completion_observed -or
        $Supplement.observations.repair.data_before_sha256 -isnot [string] -or
        $Supplement.observations.repair.data_before_sha256 -cnotmatch '^[0-9a-f]{64}$' -or
        $Supplement.observations.repair.data_after_sha256 -isnot [string] -or
        $Supplement.observations.repair.data_after_sha256 -cnotmatch '^[0-9a-f]{64}$' -or
        $Supplement.observations.repair.data_before_sha256 -cne $Supplement.observations.repair.data_after_sha256 -or
        $Supplement.observations.repair.readback_verified -isnot [bool] -or -not $Supplement.observations.repair.readback_verified) {
        throw 'Repair data, activation, completion, or readback evidence is invalid'
    }

    if ($Supplement.observations.screenshots -isnot [array] -or $Supplement.observations.screenshots.Count -ne $requiredScreenshots.Count -or
        $ActualScreenshots.Count -ne $requiredScreenshots.Count) {
        throw 'required screenshot evidence is incomplete'
    }
    $manifestByName = @{}
    foreach ($item in $Supplement.observations.screenshots) {
        if (-not (Test-CnMonMsixExactKeys $item @('name', 'sha256', 'width', 'height')) -or
            $item.name -isnot [string] -or $item.name -cnotin $requiredScreenshots -or
            $manifestByName.ContainsKey($item.name) -or $item.sha256 -isnot [string] -or
            $item.sha256 -cnotmatch '^[0-9a-f]{64}$' -or
            -not (Test-CnMonMsixJsonInteger $item.width) -or $item.width -lt 1 -or
            -not (Test-CnMonMsixJsonInteger $item.height) -or $item.height -lt 1) {
            throw 'screenshot manifest is invalid'
        }
        $manifestByName[$item.name] = $item
    }
    if ($manifestByName.Count -ne $requiredScreenshots.Count) { throw 'screenshot manifest is incomplete' }
    $actualByName = @{}
    foreach ($item in $ActualScreenshots) {
        if ($null -eq $item -or $item.name -isnot [string] -or $item.name -cnotin $requiredScreenshots -or
            $actualByName.ContainsKey($item.name) -or $item.sha256 -isnot [string] -or
            $item.sha256 -cnotmatch '^[0-9a-f]{64}$' -or
            -not (Test-CnMonMsixJsonInteger $item.width) -or $item.width -lt 1 -or
            -not (Test-CnMonMsixJsonInteger $item.height) -or $item.height -lt 1) {
            throw 'screenshot files do not match the allowlist'
        }
        $actualByName[$item.name] = $item
    }
    if ($actualByName.Count -ne $requiredScreenshots.Count) { throw 'screenshot files are incomplete' }
    $manifestHashes = @()
    foreach ($name in $requiredScreenshots) {
        $manifest = $manifestByName[$name]
        $actual = $actualByName[$name]
        if ($manifest.sha256 -cne $actual.sha256 -or $manifest.width -ne $actual.width -or $manifest.height -ne $actual.height) {
            throw 'screenshot bytes do not match the manifest'
        }
        if ($name -in $listingScreenshotNames -and ($actual.width -lt 1366 -or $actual.height -lt 768)) {
            throw 'Store listing screenshot dimensions are too small'
        }
        $manifestHashes += $manifest.sha256
    }
    if (@($manifestHashes | Select-Object -Unique).Count -ne $requiredScreenshots.Count) {
        throw 'screenshot captures are not distinct'
    }

    if (-not (Test-CnMonMsixExactKeys $ReviewAttestation @(
            'schema_version', 'mode', 'authority', 'supplement_sha256', 'screenshot_review_completed',
            'reviewed_screenshot_sha256s', 'alias_off_on_reviewed', 'repair_activation_completion_reviewed',
            'private_evidence_bundle_sha256', 'independent_sol_review')) -or
        -not (Test-CnMonMsixJsonInteger $ReviewAttestation.schema_version) -or $ReviewAttestation.schema_version -ne 1 -or
        $ReviewAttestation.mode -isnot [string] -or $ReviewAttestation.mode -cne $expectedValidatorMode -or
        $ReviewAttestation.authority -isnot [string] -or $ReviewAttestation.authority -cne $expectedValidatorAuthority -or
        $ReviewAttestation.supplement_sha256 -isnot [string] -or $ReviewAttestation.supplement_sha256 -cne $SupplementSha256 -or
        $ReviewAttestation.screenshot_review_completed -isnot [bool] -or -not $ReviewAttestation.screenshot_review_completed -or
        $ReviewAttestation.alias_off_on_reviewed -isnot [bool] -or -not $ReviewAttestation.alias_off_on_reviewed -or
        $ReviewAttestation.repair_activation_completion_reviewed -isnot [bool] -or -not $ReviewAttestation.repair_activation_completion_reviewed -or
        $ReviewAttestation.private_evidence_bundle_sha256 -isnot [string] -or
        $ReviewAttestation.private_evidence_bundle_sha256 -cnotmatch '^[0-9a-f]{64}$' -or
        $ReviewAttestation.reviewed_screenshot_sha256s -isnot [array] -or
        $ReviewAttestation.reviewed_screenshot_sha256s.Count -ne $manifestHashes.Count) {
        throw 'independent review attestation is incomplete'
    }
    foreach ($hash in $ReviewAttestation.reviewed_screenshot_sha256s) {
        if ($hash -isnot [string] -or $hash -cnotmatch '^[0-9a-f]{64}$') { throw 'reviewed screenshot evidence is invalid' }
    }
    if ((@($ReviewAttestation.reviewed_screenshot_sha256s | Sort-Object -CaseSensitive) -join ',') -cne
        (@($manifestHashes | Sort-Object -CaseSensitive) -join ',')) {
        throw 'review attestation does not cover the screenshot manifest'
    }
    $independentSolReview = $ReviewAttestation.independent_sol_review
    if (-not (Test-CnMonMsixExactKeys $independentSolReview @('role', 'reviewed_at_utc', 'reference', 'status')) -or
        $independentSolReview.role -isnot [string] -or $independentSolReview.role -cne 'gpt-6.1-sol' -or
        $independentSolReview.reviewed_at_utc -isnot [string] -or
        $independentSolReview.reviewed_at_utc -cnotmatch '^\d{8}T\d{6}Z$' -or
        $independentSolReview.reference -isnot [string] -or
        $independentSolReview.reference -cnotmatch '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$' -or
        $independentSolReview.status -isnot [string] -or $independentSolReview.status -cne 'resolved') {
        throw 'independent Sol review metadata is invalid'
    }
    try {
        $null = [DateTime]::ParseExact($independentSolReview.reviewed_at_utc, "yyyyMMdd'T'HHmmss'Z'",
            [Globalization.CultureInfo]::InvariantCulture,
            [Globalization.DateTimeStyles]::AssumeUniversal -bor [Globalization.DateTimeStyles]::AdjustToUniversal)
    }
    catch { throw 'independent Sol review timestamp is invalid' }

    $completedGates = [ordered]@{}
    foreach ($gate in $expectedGateNames) {
        $completedGates[$gate] = if ($gate -in @('repair', 'conflicts')) { 'pass' } else { [string]$CertifyResult.gates[$gate] }
    }
    return [ordered]@{
        schema_version = 1
        mode = 'desktop_local_audit_record'
        overall = 'pass'
        candidate = [ordered]@{
            run_id = '38866515555'
            artifact_id = '12031747143'
            archive_sha256 = $expectedArchiveSha256
            signed_package_sha256 = $expectedPackageSha256
            signed_receipt_sha256 = $expectedReceiptSha256
            certify_result_sha256 = $expectedCertifyResultSha256
            packaging_revision = $expectedPackagingRevision
        }
        qualification_scripts = [ordered]@{
            helper_sha256 = $ExpectedNativeHelperSha256
            helper_verification = 'local_bytes_verified'
            screenshot_verification = 'local_bytes_verified'
            desktop_driver_sha256 = $DesktopDriverSha256
            validator_revision = $ExpectedValidatorRevision
        }
        evidence = [ordered]@{
            completion_supplement_sha256 = $SupplementSha256
            review_attestation_sha256 = $ReviewAttestationSha256
            private_evidence_bundle_sha256 = $ReviewAttestation.private_evidence_bundle_sha256
        }
        gates = $completedGates
    }
}

function Get-CnMonMsixReviewedCompletionReport {
    [CmdletBinding()]
    param(
        [Parameter(Mandatory = $true)][string]$PackageSha256,
        [Parameter(Mandatory = $true)][string]$ReceiptSha256,
        [Parameter(Mandatory = $true)][string]$CertifyResultSha256,
        [Parameter(Mandatory = $true)][System.Collections.IDictionary]$Receipt,
        [Parameter(Mandatory = $true)][System.Collections.IDictionary]$CertifyResult,
        [Parameter(Mandatory = $true)][System.Collections.IDictionary]$Supplement,
        [Parameter(Mandatory = $true)][System.Collections.IDictionary]$ReviewAttestation,
        [Parameter(Mandatory = $true)][string]$SupplementSha256,
        [Parameter(Mandatory = $true)][string]$ReviewAttestationSha256,
        [Parameter(Mandatory = $true)][System.Collections.IDictionary]$LocalAuditRecord,
        [Parameter(Mandatory = $true)][string]$LocalAuditRecordSha256,
        [Parameter(Mandatory = $true)][string]$ExpectedLocalAuditRecordSha256,
        [Parameter(Mandatory = $true)][string]$ExpectedNativeHelperSha256,
        [Parameter(Mandatory = $true)][string]$DesktopDriverSha256,
        [Parameter(Mandatory = $true)][string]$ExpectedValidatorRevision
    )

    foreach ($hash in @($PackageSha256, $ReceiptSha256, $CertifyResultSha256, $SupplementSha256,
            $ReviewAttestationSha256, $LocalAuditRecordSha256, $ExpectedLocalAuditRecordSha256,
            $ExpectedNativeHelperSha256, $DesktopDriverSha256)) {
        if ($hash -cnotmatch '^[0-9a-f]{64}$') { throw 'completion evidence hash is invalid' }
    }
    if ($LocalAuditRecordSha256 -cne $ExpectedLocalAuditRecordSha256) {
        throw 'local audit record does not match its pinned digest'
    }
    if ($ExpectedValidatorRevision -cnotmatch '^[0-9a-f]{40}$') { throw 'validator revision is invalid' }

    $expectedGateNames = @(
        'identity', 'package_hash', 'signature', 'account', 'package_registration', 'exact_identity',
        'installed_binary_hash', 'alias_identity', 'native_cli', 'icmp', 'history_config', 'shell_aliases',
        'upgrade', 'repair', 'uninstall_reinstall', 'conflicts', 'fail_injection', 'cleanup', 'wack'
    )
    $expectedLocalKeys = @('schema_version', 'mode', 'overall', 'candidate', 'qualification_scripts', 'evidence', 'gates')
    if (-not (Test-CnMonMsixExactKeys $LocalAuditRecord $expectedLocalKeys) -or
        -not (Test-CnMonMsixJsonInteger $LocalAuditRecord.schema_version) -or $LocalAuditRecord.schema_version -ne 1 -or
        $LocalAuditRecord.mode -isnot [string] -or $LocalAuditRecord.mode -cne 'desktop_local_audit_record' -or
        $LocalAuditRecord.overall -isnot [string] -or $LocalAuditRecord.overall -cne 'pass') {
        throw 'pinned LocalAudit record schema is invalid'
    }
    if (-not (Test-CnMonMsixExactKeys $LocalAuditRecord.qualification_scripts @(
            'helper_sha256', 'helper_verification', 'screenshot_verification', 'desktop_driver_sha256', 'validator_revision')) -or
        $LocalAuditRecord.qualification_scripts.helper_sha256 -isnot [string] -or
        $LocalAuditRecord.qualification_scripts.helper_sha256 -cne $ExpectedNativeHelperSha256 -or
        $LocalAuditRecord.qualification_scripts.helper_verification -isnot [string] -or
        $LocalAuditRecord.qualification_scripts.helper_verification -cne 'local_bytes_verified' -or
        $LocalAuditRecord.qualification_scripts.screenshot_verification -isnot [string] -or
        $LocalAuditRecord.qualification_scripts.screenshot_verification -cne 'local_bytes_verified' -or
        $LocalAuditRecord.qualification_scripts.desktop_driver_sha256 -isnot [string] -or
        $LocalAuditRecord.qualification_scripts.desktop_driver_sha256 -cne $DesktopDriverSha256 -or
        $LocalAuditRecord.qualification_scripts.validator_revision -isnot [string] -or
        $LocalAuditRecord.qualification_scripts.validator_revision -cne $ExpectedValidatorRevision) {
        throw 'LocalAudit record source or local-byte verification does not match'
    }
    if (-not (Test-CnMonMsixExactKeys $LocalAuditRecord.evidence @(
            'completion_supplement_sha256', 'review_attestation_sha256', 'private_evidence_bundle_sha256')) -or
        $LocalAuditRecord.evidence.completion_supplement_sha256 -isnot [string] -or
        $LocalAuditRecord.evidence.completion_supplement_sha256 -cne $SupplementSha256 -or
        $LocalAuditRecord.evidence.review_attestation_sha256 -isnot [string] -or
        $LocalAuditRecord.evidence.review_attestation_sha256 -cne $ReviewAttestationSha256 -or
        $LocalAuditRecord.evidence.private_evidence_bundle_sha256 -isnot [string] -or
        $LocalAuditRecord.evidence.private_evidence_bundle_sha256 -cnotmatch '^[0-9a-f]{64}$' -or
        -not (Test-CnMonMsixExactKeys $LocalAuditRecord.gates $expectedGateNames)) {
        throw 'LocalAudit record evidence or gate set is invalid'
    }
    foreach ($gate in $expectedGateNames) {
        if ($LocalAuditRecord.gates[$gate] -isnot [string] -or $LocalAuditRecord.gates[$gate] -cne 'pass') {
            throw 'LocalAudit record does not contain 19 passing gates'
        }
    }

    if ($ReviewAttestation.private_evidence_bundle_sha256 -isnot [string] -or
        $ReviewAttestation.private_evidence_bundle_sha256 -cne $LocalAuditRecord.evidence.private_evidence_bundle_sha256) {
        throw 'review record and LocalAudit record do not bind the same private evidence bundle'
    }
    # This call checks the pinned report's schema and source bindings only; it does not open private helper or PNG bytes.
    $manifestScreenshots = @($Supplement.observations.screenshots | ForEach-Object {
        [pscustomobject]@{ name = $_.name; sha256 = $_.sha256; width = $_.width; height = $_.height }
    })
    # These are manifest entries only; the pinned LocalAudit record proves the original PNG bytes were decoded locally.
    $validatedLocalRecord = Get-CnMonMsixDesktopCompletionReport -PackageSha256 $PackageSha256 `
        -ReceiptSha256 $ReceiptSha256 -CertifyResultSha256 $CertifyResultSha256 -Receipt $Receipt `
        -CertifyResult $CertifyResult -Supplement $Supplement -ReviewAttestation $ReviewAttestation `
        -SupplementSha256 $SupplementSha256 -ReviewAttestationSha256 $ReviewAttestationSha256 `
        -ExpectedNativeHelperSha256 $ExpectedNativeHelperSha256 -ActualNativeHelperSha256 $ExpectedNativeHelperSha256 `
        -DesktopDriverSha256 $DesktopDriverSha256 -ExpectedValidatorRevision $ExpectedValidatorRevision `
        -ActualScreenshots $manifestScreenshots
    $validatedLocalJson = ConvertTo-Json -InputObject $validatedLocalRecord -Depth 12 -Compress
    $localAuditJson = ConvertTo-Json -InputObject $LocalAuditRecord -Depth 12 -Compress
    if ($validatedLocalJson -cne $localAuditJson) { throw 'pinned LocalAudit record does not bind the supplied baseline and source evidence' }

    return [ordered]@{
        schema_version = 1
        mode = 'desktop_completion_report'
        overall = 'pass'
        candidate = $validatedLocalRecord.candidate
        qualification_scripts = [ordered]@{
            helper_sha256 = $ExpectedNativeHelperSha256
            helper_verification = 'reviewed_hash_attestation'
            screenshot_verification = 'reviewed_hash_attestation'
            desktop_driver_sha256 = $DesktopDriverSha256
            validator_revision = $ExpectedValidatorRevision
        }
        evidence = [ordered]@{
            local_audit_record_sha256 = $LocalAuditRecordSha256
            completion_supplement_sha256 = $SupplementSha256
            review_attestation_sha256 = $ReviewAttestationSha256
            private_evidence_bundle_sha256 = $LocalAuditRecord.evidence.private_evidence_bundle_sha256
        }
        gates = $validatedLocalRecord.gates
    }
}

function Get-CnMonMsixInputFile {
    param([Parameter(Mandatory = $true)][string]$Path)
    try {
        $bytes = Read-CnMonMsixBoundedBytes -Path $Path -MinimumLength 1 -MaximumLength 10485760
        $encoding = [Text.UTF8Encoding]::new($false, $true)
        $text = $encoding.GetString($bytes)
        if ($text.Length -gt 0 -and $text[0] -eq [char]0xFEFF) { $text = $text.Substring(1) }
        $sha = [Security.Cryptography.SHA256]::Create()
        try { $hash = [Convert]::ToHexString($sha.ComputeHash($bytes)).ToLowerInvariant() }
        finally { $sha.Dispose() }
        return [pscustomobject]@{ Text = $text; Sha256 = $hash }
    }
    catch { throw 'a required qualification input is unavailable or invalid' }
}

function Get-CnMonMsixFileSha256 {
    param([Parameter(Mandatory = $true)][string]$Path)
    $sha = $null
    $stream = $null
    try {
        $sha = [Security.Cryptography.SHA256]::Create()
        $stream = [IO.File]::OpenRead($Path)
        return [Convert]::ToHexString($sha.ComputeHash($stream)).ToLowerInvariant()
    }
    catch { throw 'a required qualification input is unavailable or invalid' }
    finally {
        if ($null -ne $stream) { $stream.Dispose() }
        if ($null -ne $sha) { $sha.Dispose() }
    }
}

function Get-CnMonMsixNativeHelperSha256 {
    param([Parameter(Mandatory = $true)][string]$Path)
    return Get-CnMonMsixFileSha256 -Path $Path
}

function Invoke-CnMonMsixCompletionValidator {
    param(
        [string]$Mode,
        [string]$BaselinePackagePath,
        [string]$BaselineReceiptPath,
        [string]$BaselineCertifyResultPath,
        [string]$SupplementPath,
        [string]$ReviewAttestationPath,
        [string]$ScreenshotDirectory,
        [string]$ExpectedNativeHelperSha256,
        [string]$NativeHelperPath,
        [string]$LocalAuditRecordPath,
        [string]$ExpectedLocalAuditRecordSha256,
        [string]$DesktopDriverPath,
        [string]$ExpectedValidatorRevision,
        [string]$OutputPath
    )
    try {
        $inputPaths = @($BaselinePackagePath, $BaselineReceiptPath, $BaselineCertifyResultPath, $SupplementPath,
            $ReviewAttestationPath, $DesktopDriverPath)
        if ($Mode -ceq 'LocalAudit') {
            if ([string]::IsNullOrWhiteSpace($NativeHelperPath) -or [string]::IsNullOrWhiteSpace($ScreenshotDirectory) -or
                -not [string]::IsNullOrWhiteSpace($LocalAuditRecordPath) -or
                -not [string]::IsNullOrWhiteSpace($ExpectedLocalAuditRecordSha256)) {
                throw 'LocalAudit requires private helper and screenshot bytes only'
            }
            $inputPaths += $NativeHelperPath
        }
        elseif ($Mode -ceq 'ReviewedRecord') {
            if (-not [string]::IsNullOrWhiteSpace($NativeHelperPath) -or
                -not [string]::IsNullOrWhiteSpace($ScreenshotDirectory) -or
                [string]::IsNullOrWhiteSpace($LocalAuditRecordPath) -or
                [string]::IsNullOrWhiteSpace($ExpectedLocalAuditRecordSha256)) {
                throw 'ReviewedRecord requires a pinned LocalAudit record and omits private byte paths'
            }
            $inputPaths += $LocalAuditRecordPath
        }
        else { throw 'completion mode is invalid' }
        $fullOutputPath = [IO.Path]::GetFullPath($OutputPath)
        foreach ($inputPath in $inputPaths) {
            if ([StringComparer]::OrdinalIgnoreCase.Equals([IO.Path]::GetFullPath($inputPath), $fullOutputPath)) {
                throw 'output path must be separate from qualification inputs'
            }
        }
        if ([IO.File]::Exists($fullOutputPath) -or -not [IO.Directory]::Exists([IO.Path]::GetDirectoryName($fullOutputPath))) {
            throw 'completion report output is not available'
        }
        if ($Mode -ceq 'LocalAudit') {
            Assert-CnMonMsixScreenshotDirectory -Directory $ScreenshotDirectory
            $screenshotRoot = [IO.Path]::GetFullPath($ScreenshotDirectory).TrimEnd([char[]]@(
                [IO.Path]::DirectorySeparatorChar, [IO.Path]::AltDirectorySeparatorChar))
            $screenshotPrefix = $screenshotRoot + [IO.Path]::DirectorySeparatorChar
            if ($fullOutputPath.StartsWith($screenshotPrefix, [StringComparison]::OrdinalIgnoreCase)) {
                throw 'LocalAudit output must remain outside private screenshot bytes'
            }
        }

        $packageSha = Get-CnMonMsixFileSha256 -Path $BaselinePackagePath
        $receiptInput = Get-CnMonMsixInputFile -Path $BaselineReceiptPath
        $resultInput = Get-CnMonMsixInputFile -Path $BaselineCertifyResultPath
        $supplementInput = Get-CnMonMsixInputFile -Path $SupplementPath
        $reviewInput = Get-CnMonMsixInputFile -Path $ReviewAttestationPath
        $receipt = ConvertFrom-CnMonMsixStrictJson -Json $receiptInput.Text
        $result = ConvertFrom-CnMonMsixStrictJson -Json $resultInput.Text
        $supplement = ConvertFrom-CnMonMsixStrictJson -Json $supplementInput.Text
        $review = ConvertFrom-CnMonMsixStrictJson -Json $reviewInput.Text
        $driverSha = Get-CnMonMsixFileSha256 -Path $DesktopDriverPath
        $certifySha = $resultInput.Sha256
        if ($Mode -ceq 'LocalAudit') {
            $screenshots = @(Get-CnMonMsixCompletionScreenshotNames | ForEach-Object {
                Get-CnMonMsixPngObservation -Directory $ScreenshotDirectory -Name $_
            })
            $actualHelperSha = Get-CnMonMsixNativeHelperSha256 -Path $NativeHelperPath
            $report = Get-CnMonMsixDesktopCompletionReport -PackageSha256 $packageSha -ReceiptSha256 $receiptInput.Sha256 `
                -CertifyResultSha256 $certifySha -Receipt $receipt -CertifyResult $result -Supplement $supplement `
                -ReviewAttestation $review -SupplementSha256 $supplementInput.Sha256 -ReviewAttestationSha256 $reviewInput.Sha256 `
                -ExpectedNativeHelperSha256 $ExpectedNativeHelperSha256 -ActualNativeHelperSha256 $actualHelperSha `
                -DesktopDriverSha256 $driverSha -ExpectedValidatorRevision $ExpectedValidatorRevision `
                -ActualScreenshots $screenshots
        }
        else {
            $localAuditInput = Get-CnMonMsixInputFile -Path $LocalAuditRecordPath
            $localAudit = ConvertFrom-CnMonMsixStrictJson -Json $localAuditInput.Text
            $report = Get-CnMonMsixReviewedCompletionReport -PackageSha256 $packageSha -ReceiptSha256 $receiptInput.Sha256 `
                -CertifyResultSha256 $certifySha -Receipt $receipt -CertifyResult $result -Supplement $supplement `
                -ReviewAttestation $review -SupplementSha256 $supplementInput.Sha256 -ReviewAttestationSha256 $reviewInput.Sha256 `
                -LocalAuditRecord $localAudit -LocalAuditRecordSha256 $localAuditInput.Sha256 `
                -ExpectedLocalAuditRecordSha256 $ExpectedLocalAuditRecordSha256 `
                -ExpectedNativeHelperSha256 $ExpectedNativeHelperSha256 -DesktopDriverSha256 $driverSha `
                -ExpectedValidatorRevision $ExpectedValidatorRevision
        }
        $json = ConvertTo-Json -InputObject $report -Depth 12
        $outputBytes = [Text.UTF8Encoding]::new($false).GetBytes($json + [Environment]::NewLine)
        $temporaryPath = $fullOutputPath + '.tmp.' + [Guid]::NewGuid().ToString('N')
        try {
            $outputStream = [IO.File]::Open($temporaryPath, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
            try { $outputStream.Write($outputBytes, 0, $outputBytes.Length) }
            finally { $outputStream.Dispose() }
            [IO.File]::Move($temporaryPath, $fullOutputPath)
        }
        finally { if ([IO.File]::Exists($temporaryPath)) { [IO.File]::Delete($temporaryPath) } }
        if ($Mode -ceq 'LocalAudit') { Write-Host 'PASS desktop local audit record written' }
        else { Write-Host 'PASS desktop completion report written' }
    }
    catch {
        [Console]::Error.WriteLine('desktop completion validation failed; no package or trust changes were attempted')
        exit 1
    }
}

if ($MyInvocation.InvocationName -ne '.') {
    Invoke-CnMonMsixCompletionValidator @PSBoundParameters
}
