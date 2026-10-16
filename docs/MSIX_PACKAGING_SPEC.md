# cn-mon MSIX packaging specification

Status: approved written specification, 2026-10-16. The user approved this document after independent specification review. The [implementation plan](MSIX_PACKAGING_PLAN.md) incorporates the user's later requirement to keep reserved identity values out of the public repository. No MSIX implementation, installed-package qualification or Store certification is complete; Store upload and publication require a separate authorized stage.

## 1. Outcome and scope

Deliver one x64 MSIX candidate for the existing native CLI, with manifest/assets, a PowerShell packaging script, artifact-only CI, a `cn-mon.exe` execution alias, installed-package Windows tests and current documentation. Preserve the qualified 0.3.0 executable byte-for-byte, its CLI behavior and all 31 existing acceptance mappings. Linux and Windows x86-64 remain the supported application platforms.

The package adds no launcher, GUI, service, Python runtime, Windows App SDK dependency or Package Support Framework. Existing ZIP/tar.gz releases remain available. MSI/EXE packaging is a fallback requiring a revised design if MSIX cannot satisfy launch or certification requirements; it is not a parallel deliverable.

## 2. Identity configuration and version

Keep the reserved Partner Center values out of repository files and unpublished Git history. Configure only the four required GitHub Actions variables below; local builds receive the same names as process environment variables from an untracked private source. Omit unused Store ID/URL, MSA app ID and package SID metadata entirely. Microsoft's [identity guidance](https://learn.microsoft.com/windows/apps/publish/view-app-identity-details) still governs the actual values: manifest Name and Publisher must match the reserved identity exactly and case-sensitively.

| Runtime variable | Manifest / validation use |
| --- | --- |
| `CNMON_MSIX_IDENTITY_NAME` | `Identity/Name` |
| `CNMON_MSIX_IDENTITY_PUBLISHER` | `Identity/Publisher`; test signing certificate Subject |
| `CNMON_MSIX_PUBLISHER_DISPLAY_NAME` | `Properties/PublisherDisplayName` |
| `CNMON_MSIX_EXPECTED_PFN` | Expected package family name; derive from configured identity using Windows package identity APIs and compare, then verify the installed package |

All four variables are required and nonblank. Validate their schema constraints and mutual consistency without printing their values in diagnostics. The tracked manifest contains `__CNMON_MSIX_IDENTITY_NAME__`, `__CNMON_MSIX_IDENTITY_PUBLISHER__` and `__CNMON_MSIX_PUBLISHER_DISPLAY_NAME__` placeholders. Render them with XML APIs only into the temporary package staging directory; never write the resolved manifest back into the checkout. `release.json` holds public release/version/hash metadata only. Tests use clearly synthetic identities, such as `Example.CnMon` and `CN=Example`, and a matching synthetic PFN.

The initial package is x64 MSIX `1.3.0.0` containing CLI `0.3.0`. Package versions increase independently: a packaging-only update may use `1.3.1.0` with CLI 0.3.0. Keep the fourth component zero, the first nonzero and all components within Microsoft's [allowed range](https://learn.microsoft.com/windows/apps/publish/publish-your-app/msix/app-package-requirements#package-version-numbering). Record package version, CLI version, source revision and hashes for each candidate.

The public friendly display name remains `cn-mon Network Monitor`; confirm the listing name privately before submission. Repository privacy does not make MSIX identity secret: an installable package necessarily contains its identity, and anyone receiving that package can inspect it. GitHub Actions variables are not masked secrets. Public-repository workflow logs/artifacts must not be treated as private storage; real-identity builds mask input values before tool invocation and keep detailed outputs local/private unless publication is explicitly authorized.

## 3. Exact payload and provenance

The first package consumes the [published Windows ZIP](https://github.com/evdanil/cn-mon/releases/download/v0.3.0/cn-mon_0.3.0_windows_amd64.zip), verified against these fixed values before extraction:

| Input | Required value |
| --- | --- |
| CLI source revision | `bd1ac548c22c0c49a54e2ce964a2f0a4da48a1ed` |
| Archive | `cn-mon_0.3.0_windows_amd64.zip` |
| Archive SHA-256 | `3e5ef313b4034202430256f14ac31bfdfc9ae4e7d2d23a9f4c74602cd6c74388` |
| Executable SHA-256 | `8312916ae18998c6cddecbe31d2a1dd9833f047a5e51da3feccc5ca45b6b234d` |
| `--version` output | `cn-mon 0.3.0 (bd1ac548c22c0c49a54e2ce964a2f0a4da48a1ed)` plus LF |

Stage `cn-mon.exe`, `README.md`, `LICENSE` and `THIRD_PARTY_NOTICES.md` from that archive without changing their bytes. Add the packaging revision's manifest, required image assets and a short `README-MSIX.md` explaining terminal launch, alias conflicts and data locations. Ship no `.cn` configuration, history, logs, test fixtures, signing keys or SDK tools.

The packaging source revision and CLI source revision are separate: the former identifies the reviewed manifest/script/assets, while the latter identifies the existing executable. The local/private receipt records both, configured identity, each staged payload hash, rendered-manifest hash, package hash, package version, architecture, tool versions and signing state. Public summaries omit configured identity and identity-bearing paths/tool output. Unpack the generated MSIX and compare payload hashes again; verify the installed executable's hash during native tests. Do not rebuild, strip, patch or individually sign the executable to create this candidate.

This provides reproducible payload identity. It does not claim signed MSIX files are byte-identical across tool versions or signing runs; each concrete artifact has its own SHA-256 and test evidence. Existing release qualification and public hashes are recorded in the [development plan](DEVELOPMENT_PLAN.md#m11--strict-030-qualification-and-delivery-complete).

## 4. Manifest and launch contract

Use `Windows.Desktop`, one application with `Id="CnMon"`, `Executable="cn-mon.exe"` and `EntryPoint="Windows.FullTrustApplication"`, and only the `runFullTrust` restricted capability. Its justification is running the existing native command-line monitor with the invoking user's privileges, network sockets and user-owned SQLite/configuration/log files. It does not request elevation. Confirm acceptance of this justification in the later Partner Center stage.

Use the documented full-trust alias structure below, with the `uap3` and `desktop` namespace URIs and their entries in `IgnorableNamespaces`. This is a specification fragment, not an implemented manifest:

```xml
<Extensions>
  <uap3:Extension Category="windows.appExecutionAlias"
                  Executable="cn-mon.exe"
                  EntryPoint="Windows.FullTrustApplication">
    <uap3:AppExecutionAlias>
      <desktop:ExecutionAlias Alias="cn-mon.exe" />
    </uap3:AppExecutionAlias>
  </uap3:Extension>
</Extensions>
```

This follows Microsoft's [packaged desktop alias example](https://learn.microsoft.com/windows/msix/psf/create-shortcut-with-script-package-support-framework#create-the-application-alias) without adopting that example's PSF shortcut machinery. The [uap3 extension schema](https://learn.microsoft.com/uwp/schemas/appxpackage/uapmanifestschema/element-uap3-extension) defines namespace `http://schemas.microsoft.com/appx/manifest/uap/windows10/3`; the [desktop alias schema](https://learn.microsoft.com/uwp/schemas/appxpackage/uapmanifestschema/element-desktop-executionalias) defines `http://schemas.microsoft.com/appx/manifest/desktop/windows10` and requires an `.exe` alias.

Keep the existing PE console subsystem. Do not add `desktop4:Subsystem` as a desktop-console prerequisite: Microsoft's [console comparison](https://learn.microsoft.com/windows/apps/develop/launch/console-apps#differences-from-uwp-console-apps) distinguishes that UWP requirement from standard desktop console binaries. The optional newer `uap11:Subsystem` override is also unnecessary for this design. Installed alias tests must prove stream, exit-code and console behavior.

Use English manifest strings and first-party PNG assets for the required package logo and 44/150-pixel visual elements. Proposed `uap:VisualElements AppListEntry="none"` reflects the terminal-only interface and avoids a Start entry that immediately exits on no arguments; [the schema permits this setting](https://learn.microsoft.com/uwp/schemas/appxpackage/uapmanifestschema/element-uap-visualelements). Documentation directs users to open PowerShell or Command Prompt and run `cn-mon --help`. Store launch/discoverability acceptance remains a certification question, not a reason to add an unapproved GUI.

The plan selects `MinVersion="10.0.17763.0"` (Windows 10 1809), the [documented Store MSIX installation floor](https://learn.microsoft.com/windows/msix/supported-platforms#microsoft-store-submissions); the chosen alias schema is older. [Go's documented minimum](https://go.dev/wiki/MinimumRequirements#windows) is Windows 10 or later for Go 1.21 and later, supporting this declaration for the released Go 1.27.2 binary without claiming it was tested on Windows 10 1809. Record `MaxVersionTested` from an actually qualified Windows build. Neither field is evidence of successful tests on every intervening Windows release; report the actual tested versions separately. Do not narrow the product to Windows 11 merely because the current VM runs build 26200.

## 5. Build, CI and signing boundaries

The implementation will add a focused `packaging/windows/` directory, `scripts/package-msix.ps1`, `scripts/test-msix-windows.ps1` and `.github/workflows/msix.yml`. Keep the existing archive publication workflow intact. The build script accepts a local release ZIP and output directory; CI obtains the fixed published input and verifies it before invoking the script. Use an explicit Windows SDK tool directory/version, record executable versions, and fail clearly when MakeAppx or required signing tools are absent.

MakeAppx creates a single `.msix`, not a bundle or upload wrapper. Keep its semantic validation enabled; unpack the result for independent identity, allowlisted-content and hash checks. MakeAppx validation is limited and [does not establish installability](https://learn.microsoft.com/windows/msix/package/create-app-package-with-makeappx-tool#semantic-validation-performed-by-makeappxexe). PR CI uses synthetic identity inputs only. Real inputs are supplied from `vars.*` only to a trusted manual build, with read-only repository permissions and value masking before tool calls. Build and qualification run in one job so real packages/receipts need no inter-job artifact upload. Keep identity-bearing packages, receipts, certificates, detailed logs and WACK reports local/private by default; uploading them to public-repository Actions artifacts requires explicit authorization. Only sanitized summaries may be uploaded automatically. There are no Store credentials, Store upload, GitHub release publication or automatic installation on the existing VM.

Test signing produces a separately named copy of the unsigned candidate. A disposable code-signing certificate must have the exact manifest Publisher as its Subject; preserve payload identity through signing and record the certificate thumbprint and signed-file hash. Keep private keys out of Git, logs and uploaded artifacts. A test signature is not a public release signature. Microsoft [signs Store-distributed packages](https://learn.microsoft.com/windows/msix/package/signing-package-overview); the eventual upload format and submission are a later stage.

The current VM probe found neither MakeAppx in the inspected Windows SDK location nor SignTool on PATH, and no registered package with this Name. Resolve SDK/tool availability on a compatible Windows build environment before packaging. File-only portable tool probes may unpack/run tools under `C:\work` without a system installer or PATH change; record their source and versions. Do not silently install tools system-wide, register application packages, alter machine trust, enable Developer Mode or elevate on the current VM. Microsoft's [self-signed testing procedure](https://learn.microsoft.com/windows/msix/package/create-certificate-package-signing) requires trusted installation and describes machine-store changes; creating a PFX under `C:\work` alone does not satisfy that requirement.

The approved scope includes routine reversible per-user install/upgrade/remove tests on a disposable Windows environment. On the existing VM, generated files remain under `C:\work`; registration and Windows-managed package files necessarily involve OS state outside that directory. Select a test environment whose rules permit those operations and whose signing trust is already provisioned, or obtain authorization for the specific additional environment setup. Do not repeatedly seek approval for ordinary package tests already included in the scope. Global trust changes, privilege changes and production changes remain separate side effects. This specification stage performs no VM mutation.

## 6. Installed-package acceptance

The first package carries the existing CLI contract, including caller-relative paths, configuration precedence and persistent user-owned data. Microsoft's [desktop-package filesystem documentation](https://learn.microsoft.com/windows/msix/desktop/desktop-to-uwp-behind-the-scenes) describes read-only package files and some redirected locations; the actual history/log/configuration paths must be measured under package identity.

| Gate | Required evidence |
| --- | --- |
| Package identity | Install the signed candidate, read registered Name/Publisher/version/x64/PFN, and verify the installed executable hash and exact `--version`. Record OS build, account, token integrity and group membership. |
| Alias and streams | Invoke `cn-mon` and `cn-mon.exe` from PowerShell and Command Prompt in a fresh shell. Confirm resolution targets the installed alias, not a portable binary. Exercise help, arguments with spaces, caller-relative files, stdin pipelines, separated stdout/stderr, existing exit statuses and simultaneous invocations. |
| Ordinary-user probes | Under a non-administrator account with a medium token, require IPv4 and IPv6 loopback ICMP success, open and refused local TCP results, valid reports, SQLite write/list/show and saved-batch rerun. No elevation prompt or shell ping is allowed. |
| Console lifecycle | In an actual console, test bounded repeated runs, Ctrl+C, `q`, `v`, follow, redirected output and console restoration. A remote noninteractive invocation alone cannot pass this gate. |
| Files and configuration | Verify real `%USERPROFILE%\.cn`, default history/log paths, explicit and caller-relative paths, and read-only executable-adjacent `.cn` precedence. Use a separately labelled test-only package containing an adjacent `.cn` fixture; the final candidate contains none. Verify user files on disk from outside the packaged process. |
| Installation lifecycle | On a clean disposable profile, test install, upgrade from a lower-version fixture with the same identity, repair using the available Windows package repair flow, uninstall and reinstall. Preserve user-owned history/log/configuration outside the package; confirm the removed package's alias registration is gone. Record any unsupported repair operation as an unresolved gate. |
| Conflicts and discovery | Test a pre-existing portable command on PATH and a disabled alias; document command-resolution and App Execution Aliases recovery without overwriting another command or changing PATH globally. Record the CLI-only first-launch experience and proposed hidden Start entry. |
| Certification preflight | Run Windows App Certification Kit, retain the full report and resolve actionable failures. WACK is a pre-submission check; it cannot prove Store approval. See [Microsoft's WACK guidance](https://learn.microsoft.com/windows/uwp/debug-test-perf/windows-app-certification-kit). |

The lower-version upgrade/configuration fixture is test-only, built from the same executable, with differences recorded in its receipt; it is never substituted for the final candidate's installed evidence. Tests use bounded local targets and disposable data. Preserve existing native Linux and Windows checks in the milestone; cross-compilation cannot replace native runs. Use red-green-refactor for packaging behavior such as corrupted input rejection, payload mismatch rejection and invalid identity/version handling, rather than tests that only duplicate constants.

## 7. Existing evidence and completion gates

The published archives already passed native Linux and Windows release qualification. A further unpackaged Windows smoke run on 2026-10-15 UTC used `windows-ci\agent`, OS build `26200.9711`, no Administrators membership and a medium token. It verified the archive hash above; the extracted binary matched the stated hash. Help/version, IPv4 and IPv6 loopback ICMP, local open TCP and SQLite history write/list all returned exit 0. Its local transcript is `/tmp/cn-mon-unprivileged-20261015/transcript.txt` (VM copy `C:\work\cn-mon-unprivileged-20261015\transcript.txt`); its PowerShell header uses the VM's +11-hour local date, 2026-10-16. This is portable-binary evidence, not an installed-package, default-path, console or certification result.

The implementation plan selects the declared Windows minimum and a disposable hosted SDK/signing/test route; actual tool, deployment, repair and active-session WACK availability remain execution gates. Before the packaging milestone is complete, retain package/receipt/hash evidence, required native results and WACK findings; update the application specification, development plan, README and Store guide to the observed behavior. Deliver the milestone in its own PR to `main`, complete independent `gpt-6.1-sol` medium review, then request `@codex review`, resolve actionable findings and pass required checks before merge. A pending external review is incomplete.

Store upload, listing assets/content, restricted-capability approval and certification/publication remain a separate stage. Package identity reservation and successful local qualification do not establish CLI-only Store eligibility or public availability.
