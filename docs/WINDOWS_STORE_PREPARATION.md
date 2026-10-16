# Windows Store preparation

This note records the current package design and remaining validation before `cn-mon` can be submitted to Microsoft Store. The [MSIX packaging specification](MSIX_PACKAGING_SPEC.md) and [implementation plan](MSIX_PACKAGING_PLAN.md) incorporate the approved scope and repository privacy requirement. Package construction and payload verification are implemented; synthetic and real-identity MakeAppx builds pass, while the private real-identity candidate remains unsigned. Native Linux and Windows portable-archive qualification passed at `8d56363`, but does not establish MSIX behavior. Synthetic hosted run [38852774635](https://github.com/evdanil/cn-mon/actions/runs/38852774635) at `f7e1c02` passed 13 of 19 gates, including `alias_identity`; `history_config` failed and five remain incomplete. It found newly created history/log files requested under `LOCALAPPDATA` in package-specific `LocalCache`, confirming AppData virtualization. The approved contract preserves files outside AppData and accepts Windows-managed cleanup of virtualized AppData entries at uninstall; that cleanup has not yet been measured. No real-identity package has been installed or fully qualified. Store certification, upload and publication remain pending.

The [copy-ready Microsoft Store listing](WINDOWS_STORE_LISTING.md) links its license field to the repository's Apache-2.0 text and its privacy-policy field to the [standalone policy draft](PRIVACY.md). Real Windows Store screenshots remain pending until they can be captured from an installed MSIX; review and publication of the policy URL also remain open.

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

The Windows pure-helper safety probe passed. The completed read-only VM inventory found OS build `26200`; the current SSH context is Session 0 and is not desktop-session eligible, without establishing whether another interactive session exists. The current token is not elevated or administrator and has no Administrators SID. MakeAppx, SignTool and all three Appx cmdlets are available; AppCert is absent. The private unsigned package hash matches its receipt, and the expected package/version is not registered for the current user. Hosted inventory remains pending the next CI run. The synthetic workflow retains the fixed-schema preflight JSON. In the trusted manual workflow, detailed output remains job-private and only the sanitized summary is uploaded. This read-only step leaves the existing VM's file-only constraint unchanged and does not authorize desktop mutations.

## Distribution route

Microsoft's [Store submission overview](https://learn.microsoft.com/windows/apps/publish/) describes two relevant routes:

1. **MSIX (recommended starting point):** Microsoft recommends MSIX for Store apps. For Store-distributed MSIX, Microsoft re-signs and hosts the package and manages app updates. See [MSIX package requirements](https://learn.microsoft.com/windows/apps/publish/publish-your-app/msix/app-package-requirements).
2. **Installer listing (MSI/EXE fallback):** the publisher provides a direct, versioned HTTPS URL to a CA-trusted Authenticode-signed `.msi` or `.exe`; the installer and its PE files must be signed. The submitted installer must be a standalone offline installer, install silently (a UAC prompt is allowed), and remain byte-for-byte unchanged at its submitted URL; publish updates at a new versioned URL. It must not bundle unrelated software. See Microsoft's [MSI/EXE package requirements](https://learn.microsoft.com/en-us/windows/apps/publish/publish-your-app/msi/app-package-requirements).

Start with MSIX, while keeping the MSI/EXE listing route as a fallback if package validation or certification requirements make MSIX unsuitable. A command-line-only app's eligibility and launch acceptance remain unconfirmed and require package testing and Store certification. Do not infer approval from this preparation note.

## MSIX identity and command launch

The Partner Center identity is reserved, but its literal values are not stored in this repository. The [packaging specification](MSIX_PACKAGING_SPEC.md#2-identity-configuration-and-version) defines four required inputs: `CNMON_MSIX_IDENTITY_NAME`, `CNMON_MSIX_IDENTITY_PUBLISHER`, `CNMON_MSIX_PUBLISHER_DISPLAY_NAME` and `CNMON_MSIX_EXPECTED_PFN`. They are protected GitHub environment secrets in `msix-packaging`, passed to the trusted manual job at runtime and masked before tool calls. Render the public manifest template only in staging; PR tests use the synthetic `Example.CnMon` identity. Omit unused Store identifiers and MSA metadata. The trusted workflow uploads only sanitized status summaries; real packages, receipts and reports remain private.

The approved initial mapping is MSIX `1.3.0.0` containing CLI `0.3.0`; package versions increase independently, including for packaging-only fixes. Store MSIX versions require a nonzero major component and a zero fourth component. The proposed pipeline produces one x64 `.msix` from the hash-verified published Windows ZIP and records the exact source/payload/package identities. It adds no Store upload operation. For the later submission stage, supported upload forms include `.msix`, `.msixbundle`, and `.msixupload`; see Microsoft's [package upload guidance](https://learn.microsoft.com/windows/apps/publish/publish-your-app/msix/upload-app-packages).

Microsoft's [packaged desktop alias example](https://learn.microsoft.com/en-us/windows/msix/psf/create-shortcut-with-script-package-support-framework#create-the-application-alias) shows a full-trust desktop entry point with an execution alias. Microsoft's [console-app comparison](https://learn.microsoft.com/en-us/windows/apps/develop/launch/console-apps#differences-from-uwp-console-apps) says `desktop4:Subsystem="console"` is for UWP console apps and is not required for a standard Win32 console binary such as `cn-mon.exe`. Plan for a packaged full-trust desktop alias and the `runFullTrust` restricted capability; confirm its justification and manifest against Partner Center requirements. Validate an alias for `cn-mon` on a clean Windows installation, including PATH/alias behavior, argument and stdin/stdout/exit-code forwarding, invocation from PowerShell and Command Prompt, and conflicts with an existing `cn-mon` command. Validate the Store listing, install and first-launch experience, and certification expectations specifically for a CLI-only product; eligibility and acceptance remain unconfirmed.

The specification defines the `uap3`/`desktop` alias structure, unchanged native console executable, an artifact-only Windows workflow and separate test signing. Synthetic and real-identity MakeAppx pack/unpack checks verify payload hashes, which establishes package construction rather than installation behavior. The existing unpackaged ordinary-user smoke run passed IPv4/IPv6 ICMP, local TCP and history operations; that does not establish behavior under package identity. Native Linux and Windows portable-archive qualification passed at `8d56363`. Synthetic hosted run [38852774635](https://github.com/evdanil/cn-mon/actions/runs/38852774635) at `f7e1c02` passed 13 of 19 gates, including alias identity; `history_config` failed and five remain incomplete. Newly created history/log files requested under `LOCALAPPDATA` were found in package-specific `LocalCache`. The private real-identity candidate remains unsigned, with no real-identity install or full qualification. WACK must run through the actual `appcert.exe` in an active user session; unsupported prerequisites must remain incomplete.

## Required Windows package validation

Before submission, build a candidate package on the Windows packaging toolchain and check:

- Install, upgrade, uninstall, repair, and clean-machine behavior for Windows x64.
- CLI launch through the execution alias, including help/version, arguments, stdin/stdout/stderr, exit codes, cancellation, and interactive terminal use.
- Declare the `runFullTrust` restricted capability for the full-trust desktop entry point and confirm the manifest and justification with Partner Center. Full trust does not establish raw ICMP behavior; validate raw ICMP under package identity. The app must not require elevation, and package testing must confirm it runs without an administrator prompt.
- Verify history/log defaults and other selected paths outside AppData separately from AppData paths. The defaults under `USERPROFILE` remain user-owned across package removal; on Windows 10 1903 and later, newly created AppData files/folders are virtualized, while writes to existing files use the unvirtualized file. On Windows 10 1809, all AppData writes use copy-on-write virtualization. Verify the app-visible view and physical package backing, and measure cleanup of virtualized entries at uninstall. The latest run observed redirection, not cleanup. Verify configuration precedence: executable-adjacent `.cn` loads first, `%USERPROFILE%\.cn` overrides it, and explicit `--config` and CLI overrides follow. Confirm mutable data is not placed in the package install directory. See Microsoft's [AppData virtualization guidance](https://learn.microsoft.com/windows/msix/desktop/flexible-virtualization).
- If using the MSI/EXE route, verify the signed installer at its immutable versioned URL is standalone/offline, silent (apart from allowed UAC), and does not bundle unrelated software.
- Package declaration, reserved publisher identity, architecture, signing, and all declared capabilities against the actual package and Partner Center review.
- Microsoft's [Windows App Certification Kit (WACK)](https://learn.microsoft.com/windows/uwp/debug-test-perf/windows-app-certification-kit) results, followed by Partner Center certification. WACK is a recommended pre-submission check, not a guarantee of Store approval.

Capture the Windows version, package build inputs, identity, test results, WACK report, and any certification feedback in local/private release evidence. Public summaries omit reserved identity values and identity-bearing tool output. Until those checks are complete, package behavior and Store acceptance remain unverified.
