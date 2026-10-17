# Windows Store preparation

This note records the current package design and validation evidence. Store upload rejected historical package `1.3.0.0` because its hidden Start entry required headless-app permission that is not granted to this app. Package `1.3.1.0` was rejected during Store package validation because its display name did not match the reserved title. The owner reports that package `1.3.2.0`, using the exact title `cn-mon — Network Monitor` in both manifest display-name fields and CLI `0.3.0`, was submitted successfully on 2026-10-17; its certification and publication status are unknown. Earlier frozen records remain unchanged and apply to their original package versions. The [MSIX packaging specification](MSIX_PACKAGING_SPEC.md) and [implementation plan](MSIX_PACKAGING_PLAN.md) describe the approved scope and privacy requirements. Frozen synthetic run `38866515555` and protected real-identity run `38866546789` each report 17 passed, 0 failed and 2 incomplete gates; the later hosted MSIX workflow passes 19/19; strict WACK parsing passes all 24 tests, including DPI awareness. Actual visible-desktop alias OFF/ON checks, Repair with data preserved, all five screenshots and cleanup completed before signer expiry. LocalAudit and the hosted MSIX workflow report 19/19 PASS. Test-signer trust cleanup is complete. See the implementation plan for hosted evidence and [Automated Microsoft Store updates](STORE_UPDATES.md) for owner setup and recovery. The automated controller has not yet been validated against the live Store API.

The [copy-ready Microsoft Store listing](WINDOWS_STORE_LISTING.md) links its license field to the repository's Apache-2.0 text and its privacy-policy field to the [standalone policy draft](PRIVACY.md). Five genuine screenshots were captured from the installed MSIX; review and publication of the policy URL remain open.

## Current application facts

- `cn-mon` is a Go command-line network monitor. The supported Windows target is x86-64; the current public release is 0.3.0.
- The program performs network probes, including raw ICMP, and uses `USERPROFILE`-based defaults for history and logs outside AppData (with CLI options for selecting paths). It also checks executable-adjacent `.cn` configuration as a read-only input. It does not launch production helper processes.
- Its primary interface is command-line input/output, including optional interactive terminal controls. There is no graphical application flow documented in this repository.

These facts describe the current source and release. Packaging behavior, Store policy eligibility, and certification outcomes must be established against an actual Windows package and the current Partner Center requirements.

## Read-only desktop preflight

The standalone preflight inventories the current session/token, SDK tools, AppCert, Appx cmdlets, and optional package/receipt hashes, signature, and registration. Its fixed-schema JSON report is an eligibility inventory, not qualification evidence. The script makes no deployment, trust, account, package-reset, or certification changes; it writes only `msix-desktop-preflight.json` under the specified artifact directory. Run its pure helper/safety harness separately:

```powershell
pwsh -NoProfile -File ./scripts/test-msix-desktop-preflight-harness.ps1
pwsh -NoProfile -File ./scripts/test-msix-desktop-preflight.ps1 `
  -PackagePath <PackagePath> `
  -ReceiptPath <ReceiptPath> `
  -SdkBinDirectory <SdkBinDirectory> `
  -ArtifactDirectory <ArtifactDirectory>
```

The Windows pure-helper safety probe passed. The VM inventory was collected from SSH Session 0 on OS build `26200`; its token was not elevated or an administrator and had no Administrators SID. MakeAppx, SignTool and all three Appx cmdlets were available; AppCert was absent. The private unsigned package hash matched its receipt, and that package was not registered for the inspected user. A separate hosted preflight used an eligible active console and elevated token with AppCert `10.0.26100.8249`. Current hosted WACK and qualification results are centralized in the [implementation plan](MSIX_PACKAGING_PLAN.md#hosted-qualification-evidence). Synthetic workflow output retains fixed-schema preflight JSON; the protected run uploaded only its sanitized summary, and its public log was audited against the four reserved identity inputs with no matches. The user has explicitly authorized registration of this candidate and trust for its exact test signer on the existing VM; generated files remain under `C:\work`.

### Read-only visible-desktop supplement preflight

The separate `test-msix-desktop-checks.ps1` preflight binds the signed candidate, receipt, Certify result and x64 native helper. It checks the signer certificate's validity and existing trust, the current ordinary-user token and active interactive desktop, and whether the candidate is already registered. It makes no package registration, trust-store, repair, alias or screenshot changes. The helper safety harness verifies its fixed schema, private-path handling and no-mutation behavior:

```powershell
pwsh -NoProfile -File ./scripts/test-msix-desktop-checks-harness.ps1
pwsh -NoProfile -File ./scripts/test-msix-desktop-checks.ps1 `
  -PackagePath <SignedCandidate> `
  -ReceiptPath <SignedReceipt> `
  -ResultsPath <CertifyResults> `
  -NativeHelperPath <X64NativeHelper> `
  -ExpectedNativeHelperSha256 <Sha256> `
  -ArtifactDirectory <ExistingPrivateDirectory>
```

It writes `msix-desktop-supplement.json` and the public test-signer certificate into the existing private artifact directory. Its summary reports preflight eligibility only and deliberately leaves repair, conflicts, screenshots and cleanup incomplete. It does not pass the desktop supplement or substitute for actual installed-package checks. The actual visible-console run subsequently completed the checks and screenshot capture; its result is recorded in the implementation plan.

For the frozen synthetic candidate, the signer certificate is valid from `2026-10-16 07:12:33 UTC` through `2026-10-16 13:22:32 UTC`. Complete any new installed-desktop qualification start to finish within that interval and retain the existing validity checks. Later immutable-evidence review does not require current certificate validity; independent private review must verify the actual run's start and end times. If qualification is incomplete at expiry, clean up owned state, obtain fresh Hosted and Certify results, replace the candidate pins, records and private evidence atomically, and recapture desktop proof. Re-signing alone does not preserve prior gates.

## Distribution route

Microsoft's [Store submission overview](https://learn.microsoft.com/windows/apps/publish/) describes two relevant routes:

1. **MSIX (recommended starting point):** Microsoft recommends MSIX for Store apps. For Store-distributed MSIX, Microsoft re-signs and hosts the package and manages app updates. See [MSIX package requirements](https://learn.microsoft.com/windows/apps/publish/publish-your-app/msix/app-package-requirements).
2. **Installer listing (MSI/EXE fallback):** the publisher provides a direct, versioned HTTPS URL to a CA-trusted Authenticode-signed `.msi` or `.exe`; the installer and its PE files must be signed. The submitted installer must be a standalone offline installer, install silently (a UAC prompt is allowed), and remain byte-for-byte unchanged at its submitted URL; publish updates at a new versioned URL. It must not bundle unrelated software. See Microsoft's [MSI/EXE package requirements](https://learn.microsoft.com/en-us/windows/apps/publish/publish-your-app/msi/app-package-requirements).

Start with MSIX, while keeping the MSI/EXE listing route as a fallback if package validation or certification requirements make MSIX unsuitable. A command-line-only app's eligibility and launch acceptance remain unconfirmed and require package testing and Store certification. Do not infer approval from this preparation note.

## MSIX identity and command launch

The Partner Center identity is reserved, but its literal values are not stored in this repository. The [packaging specification](MSIX_PACKAGING_SPEC.md#2-identity-configuration-and-version) defines four required inputs: `CNMON_MSIX_IDENTITY_NAME`, `CNMON_MSIX_IDENTITY_PUBLISHER`, `CNMON_MSIX_PUBLISHER_DISPLAY_NAME` and `CNMON_MSIX_EXPECTED_PFN`. They are protected GitHub environment secrets in `msix-packaging`, passed to the trusted manual job at runtime and masked before tool calls. Render the public manifest template only in staging; PR tests use the synthetic `Example.CnMon` identity. Omit unused Store identifiers and MSA metadata. The trusted workflow uploads only sanitized status summaries; real packages, receipts and reports remain private.

The historical mapping is MSIX `1.3.0.0` containing CLI `0.3.0`; Store upload rejected it because its hidden Start entry required headless-app permission not granted to this app. Package `1.3.1.0` was rejected during Store package validation because its display name did not match the reserved title. The owner reports submission of `1.3.2.0` around the same CLI, with the exact reserved title in both manifest display-name fields and a default Start entry; its certification and publication status are unknown. Package versions increase independently. Store MSIX versions require a nonzero major component and a zero fourth component. The automated pipeline produces one x64 `.msix` from the hash-verified published Windows ZIP and records the exact source/payload/package identities. It submits package updates through the Store services API; see [Automated Microsoft Store updates](STORE_UPDATES.md) for protected environment setup and recovery. For upload forms, Microsoft's [package upload guidance](https://learn.microsoft.com/windows/apps/publish/publish-your-app/msix/upload-app-packages) lists `.msix`, `.msixbundle`, and `.msixupload`.

Microsoft's [packaged desktop alias example](https://learn.microsoft.com/en-us/windows/msix/psf/create-shortcut-with-script-package-support-framework#create-the-application-alias) shows a full-trust desktop entry point with an execution alias. Microsoft's [console-app comparison](https://learn.microsoft.com/en-us/windows/apps/develop/launch/console-apps#differences-from-uwp-console-apps) says `desktop4:Subsystem="console"` is for UWP console apps and is not required for a standard Win32 console binary such as `cn-mon.exe`. Plan for a packaged full-trust desktop alias and the `runFullTrust` restricted capability; confirm its justification and manifest against Partner Center requirements. Validate an alias for `cn-mon` on a clean Windows installation, including PATH/alias behavior, argument and stdin/stdout/exit-code forwarding, invocation from PowerShell and Command Prompt, and conflicts with an existing `cn-mon` command. Validate the Store listing, install and first-launch experience, and certification expectations specifically for a CLI-only product; eligibility and acceptance remain unconfirmed.

The specification defines the `uap3`/`desktop` alias structure, unchanged native console executable, an artifact-only Windows workflow and separate test signing. Synthetic and real-identity MakeAppx pack/unpack checks verify payload hashes, which establishes package construction rather than installation behavior. The existing unpackaged ordinary-user smoke run passed IPv4/IPv6 ICMP, local TCP and history operations. Frozen synthetic and protected real-identity installed-package runs each report 17 passed, 0 failed and 2 incomplete gates; formal WACK acceptance passes all 24 tests with the strict parser. Actual visible-desktop alias and Repair checks and five screenshots are complete; LocalAudit, its replayed ReviewedRecord and the hosted MSIX workflow pass 19/19; test-signer trust cleanup is complete. Detailed hosted results are in the [implementation plan](MSIX_PACKAGING_PLAN.md#hosted-qualification-evidence).

## Windows package validation and ongoing checks

For each new package candidate, build it on the Windows packaging toolchain and check:

- Install, upgrade, uninstall, repair, and clean-machine behavior for Windows x64.
- CLI launch through the execution alias, including help/version, arguments, stdin/stdout/stderr, exit codes, cancellation, and interactive terminal use.
- Declare the `runFullTrust` restricted capability for the full-trust desktop entry point and confirm the manifest and justification with Partner Center. Full trust does not establish raw ICMP behavior; validate raw ICMP under package identity. The app must not require elevation, and package testing must confirm it runs without an administrator prompt.
- Verify history/log defaults and other selected paths outside AppData separately from AppData paths. The defaults under `USERPROFILE` remain user-owned across package removal; on Windows 10 1903 and later, newly created AppData files/folders are virtualized, while writes to existing files use the unvirtualized file. On Windows 10 1809, all AppData writes use copy-on-write virtualization. Verify the app-visible view and physical package backing for the remaining lifecycle cases. Verify configuration precedence: executable-adjacent `.cn` loads first, `%USERPROFILE%\.cn` overrides it, and explicit `--config` and CLI overrides follow. Confirm mutable data is not placed in the package install directory. See Microsoft's [AppData virtualization guidance](https://learn.microsoft.com/windows/msix/desktop/flexible-virtualization). Current lifecycle evidence is summarized in the [implementation plan](MSIX_PACKAGING_PLAN.md#hosted-qualification-evidence).
- If using the MSI/EXE route, verify the signed installer at its immutable versioned URL is standalone/offline, silent (apart from allowed UAC), and does not bundle unrelated software.
- Package declaration, reserved publisher identity, architecture, signing, and all declared capabilities against the actual package and Partner Center review.
- Microsoft's [Windows App Certification Kit (WACK)](https://learn.microsoft.com/windows/uwp/debug-test-perf/windows-app-certification-kit) results, followed by Partner Center certification. WACK is a recommended pre-submission check, not a guarantee of Store approval.

Capture the Windows version, package build inputs, identity, test results, WACK report, and any certification feedback in local/private release evidence. Public summaries omit reserved identity values and identity-bearing tool output. The current WACK gate passes. Actual visible-desktop Repair, command-conflict/alias-disable checks and screenshots are complete; LocalAudit and its replayed ReviewedRecord pass 19/19. The frozen Hosted candidate records still show two incomplete gates; test-signer trust cleanup is complete, and Store acceptance remains unverified. See the [implementation plan](MSIX_PACKAGING_PLAN.md#hosted-qualification-evidence) for current run results.
