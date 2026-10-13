# Windows Store preparation

This note records the packaging decisions and validation still needed before `cn-mon` can be submitted to Microsoft Store. It does not define a package identity or claim Store eligibility.

## Current application facts

- `cn-mon` is a Go command-line network monitor. The supported Windows target is x86-64; the current public release is 0.3.0.
- The program performs network probes, including raw ICMP, and uses `USERPROFILE`-based defaults for history and logs (with CLI options for selecting paths). It also checks executable-adjacent `.cn` configuration as a read-only input. It does not launch production helper processes.
- Its primary interface is command-line input/output, including optional interactive terminal controls. There is no graphical application flow documented in this repository.

These facts describe the current source and release. Packaging behavior, Store policy eligibility, and certification outcomes must be established against an actual Windows package and the current Partner Center requirements.

## Distribution route

Microsoft's [Store submission overview](https://learn.microsoft.com/windows/apps/publish/) describes two relevant routes:

1. **MSIX (recommended starting point):** Microsoft recommends MSIX for Store apps. For Store-distributed MSIX, Microsoft re-signs and hosts the package and manages app updates. See [MSIX package requirements](https://learn.microsoft.com/windows/apps/publish/publish-your-app/msix/app-package-requirements).
2. **Installer listing (MSI/EXE fallback):** the installer remains hosted by the publisher at a versioned HTTPS URL, and the installer must have a CA-trusted Authenticode signature. The publisher is responsible for hosting and updating it.

Start with MSIX, while keeping the MSI/EXE listing route as a fallback if package validation or certification requirements make MSIX unsuitable. A command-line-only app's eligibility and launch acceptance remain unconfirmed and require package testing and Store certification. Do not infer approval from this preparation note.

## MSIX identity and command launch

No Partner Center product identity has been reserved. Keep package identity and MSIX scaffolding pending that reservation. Microsoft's [Partner Center identity guidance](https://learn.microsoft.com/windows/apps/publish/view-app-identity-details) describes the identity values to use. Record `Identity/Name`, `Identity/Publisher`, and `PublisherDisplayName`; package Name and Publisher must match Partner Center exactly and are case-sensitive.

For Store upload, supported package upload forms include `.msix`, `.msixbundle`, and `.msixupload`; see Microsoft's [package upload guidance](https://learn.microsoft.com/windows/apps/publish/publish-your-app/msix/upload-app-packages). Select the appropriate form after deciding the architecture and packaging pipeline. Store MSIX versions require a nonzero major component and a zero fourth component. Do not map CLI version `0.3.0` to package version `0.3.0.0`; choose an independent valid Store package version and document how it maps to the CLI release.

Microsoft's [app execution alias schema](https://learn.microsoft.com/en-us/uwp/schemas/appxpackage/uapmanifestschema/element-uap5-appexecutionalias) documents `uap5:AppExecutionAlias` and `desktop4:Subsystem="console"` for console semantics. These provide a documented route to expose a packaged command-line app without adding a GUI. Validate an alias for `cn-mon` on a clean Windows installation, including PATH/alias behavior, argument and stdin/stdout/exit-code forwarding, invocation from PowerShell and Command Prompt, and conflicts with an existing `cn-mon` command. Validate the Store listing, install and first-launch experience, and certification expectations specifically for a CLI-only product; eligibility and acceptance remain unconfirmed.

## Required Windows package validation

Before submission, build a candidate package on the Windows packaging toolchain and check:

- Install, upgrade, uninstall, repair, and clean-machine behavior for Windows x64.
- CLI launch through the execution alias, including help/version, arguments, stdin/stdout/stderr, exit codes, cancellation, and interactive terminal use.
- Declare `runFullTrust` only if required by the desktop console app, and confirm the requested capability and justification with Partner Center. Full trust does not establish raw ICMP behavior; validate raw ICMP under package identity. The app must not require elevation, and package testing must confirm it runs without an administrator prompt.
- Read/write access and upgrade/uninstall handling for `USERPROFILE` history and log paths, and read-only access to executable-adjacent `.cn` configuration; confirm mutable data is not placed in the package install directory.
- Package declaration, reserved publisher identity, architecture, signing, and all declared capabilities against the actual package and Partner Center review.
- Microsoft's [Windows App Certification Kit (WACK)](https://learn.microsoft.com/windows/uwp/debug-test-perf/windows-app-certification-kit) results, followed by Partner Center certification. WACK is a recommended pre-submission check, not a guarantee of Store approval.

Capture the Windows version, package build inputs, identity, test results, WACK report, and any certification feedback with the release evidence. Until those checks are complete, package behavior and Store acceptance remain unverified.
