# Microsoft Store listing draft

Working editorial copy for the initial x64 MSIX package, version 1.3.0.0, which contains the unchanged cn-mon CLI 0.3.0. Package construction and payload verification are implemented. Frozen synthetic and protected real-identity runs each report 17 passed, 0 failed and 2 incomplete gates; the later hosted MSIX workflow passes 19/19; strict WACK parsing passes all 24 tests, including DPI awareness. The actual visible-desktop run completed alias OFF/ON checks, Repair with data preserved and all five screenshots. LocalAudit, its replayed ReviewedRecord and the hosted MSIX workflow pass 19/19; test-signer trust cleanup is complete, and runtime PerMonitorV2 behavior is unmeasured. No Store submission or publication has occurred; hosted evidence is in the [MSIX packaging plan](MSIX_PACKAGING_PLAN.md#hosted-qualification-evidence).

## Copy-ready English listing

**Product name draft:** cn-mon Network Monitor

The package already displays “cn-mon Network Monitor.” Confirm the exact reserved spelling privately in Partner Center before entering the listing title.

**Short description:**

Monitor IPv4 and IPv6 targets from Windows Terminal with native ICMP probes, optional TCP checks, live follow, local history, and table, JSON, CSV, or Markdown reports.

**Description:**

cn-mon is a command-line network monitor for Windows x64. Check hostnames, IPv4 and IPv6 addresses, and subnets with native ICMP echo probes. Add optional TCP port checks to test service reachability alongside host reachability.

Run a single check or schedule repeated rounds for a chosen interval and duration. Watch a live terminal view, follow saved runs, inspect previous results, or replay a saved target set. Choose table, JSON, CSV, or Markdown reports for interactive and scripted workflows.

Completed runs can be stored in a local SQLite history file. List and inspect saved requests and runs, then remove an individual run, a request, or all stored history with explicit commands. Choose history and log paths through command-line options or the existing .cn configuration file.

cn-mon is a terminal tool with no graphical dashboard or background monitoring service. Network activity consists of resolving hostnames you supply and sending the ICMP or TCP checks you request. The application has no telemetry or account service; it does not upload its history, reports, logs, or configuration to the project.

**Product features:**

- Native ICMP echo monitoring for IPv4 and IPv6 targets
- Optional TCP checks for selected ports
- Single checks and scheduled repeated runs
- Live terminal display and saved-run follow mode
- Table, JSON, CSV, and Markdown reports
- Local history with list, inspect, replay, and delete commands
- Targets from command-line arguments, a file, or standard input

**Search terms:**

- network monitor
- ICMP ping
- TCP port monitor
- IPv4 IPv6
- host monitoring
- network diagnostics
- command line network monitor

**System requirements:**

- Windows 10 version 1809 (build 17763) or later, 64-bit
- Network access to selected targets; DNS resolution for hostnames

The manifest declares Windows Desktop minimum version 10.0.17763.0 and x64 architecture. The declared minimum has not been runtime-qualified with the installed package. Do not turn the manifest minimum into a claim that every listed Windows build has been tested.

**Price:** Free. The current app has no paid features, subscriptions, or in-app purchases.

**Market availability:** No custom market restrictions were requested. Actual Partner Center market settings are not confirmed in this draft.

**License:** Apache License, Version 2.0, matching the repository's root `LICENSE` file.

**Additional license terms field (copy-ready single URL):** https://github.com/evdanil/cn-mon/blob/main/LICENSE

The Windows package payload includes both `LICENSE` and `THIRD_PARTY_NOTICES.md`. Preserve both; the Apache-2.0 terms for first-party code do not replace the notices and licenses for third-party code. The Store field above should contain only the single license URL.

**Support contact:** https://github.com/evdanil/cn-mon/issues

**Proposed privacy policy URL:** https://github.com/evdanil/cn-mon/blob/main/docs/PRIVACY.md

The privacy URL is pending merge and publication of this file at that repository path. Do not enter it as a live link until it resolves to the reviewed policy. No separate product website or support email has been provided.

**Future-update notes draft:**

Initial Microsoft Store release of cn-mon 0.3.0: native ICMP monitoring, optional TCP checks, live terminal follow, local history, saved-run replay, and table, JSON, CSV, and Markdown reports.

Microsoft says to leave “What's new in this version” blank for a first Store submission. Keep the text above for a later update or reuse only if Partner Center treats the submission as an update.

## Reviewer notes and launch instructions

The following capability justification is ready for certification notes:

> The package declares the `runFullTrust` restricted capability to run the existing native command-line executable as a full-trust desktop process. This allows the invoking command-line session to pass arguments and standard streams and lets cn-mon resolve hostnames, send native ICMP probes, make TCP connection attempts for user-selected targets, and use configuration, history, and log files. The package installs no service or driver.

This capability justification describes intended desktop execution; hosted qualification covers installation and core alias/ICMP behavior for the tested candidates, but it does not establish Store eligibility. Actual repair and alias conflict/recovery checks ran against the installed candidate. LocalAudit and the hosted MSIX workflow report 19/19 PASS. The strict WACK parser passes all 24 tests for both frozen candidates, and five genuine screenshots have been captured and independently approved. Runtime DPI state is unmeasured. See the [MSIX packaging plan](MSIX_PACKAGING_PLAN.md#hosted-qualification-evidence) for run details.

~~~powershell
$reviewDir = Join-Path $env:TEMP ("cn-mon-store-review-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $reviewDir | Out-Null
$history = Join-Path $reviewDir 'history.db'
$log = Join-Path $reviewDir 'cn.log'
cn-mon.exe --version
cn-mon.exe --help
cn-mon.exe --history-file $history --log-file $log 127.0.0.1
cn-mon.exe --format json --tcp 443 --history-file $history --log-file $log 127.0.0.1
cn-mon.exe --list --format table --history-file $history --log-file $log
cn-mon.exe --show REQUEST_ID --format md --history-file $history --log-file $log
cn-mon.exe --rerun REQUEST_ID --every 10s --for 30s --history-file $history --log-file $log
cn-mon.exe --list-runs REQUEST_ID --format table --history-file $history --log-file $log
~~~

Replace REQUEST_ID with the full hexadecimal ID or a unique prefix returned by --list. The loopback target keeps the review traffic local; confirm expected ICMP and TCP behavior on the test machine rather than assuming it. For destructive history checks, use only the disposable history file above:

~~~powershell
cn-mon.exe --delete-run RUN_ID --history-file $history --log-file $log
cn-mon.exe --delete-request REQUEST_ID --history-file $history --log-file $log
cn-mon.exe --clear-history --history-file $history --log-file $log
~~~

Replace RUN_ID and REQUEST_ID with values returned by the history reports. The delete-run command requires the exact run ID; delete-request accepts a unique request ID prefix; clear-history deletes all records from the selected history database. These operations do not securely erase data from the storage device. Verify only in the disposable database.

## Screenshot capture plan

A prior Session 0 capture probe produced a black 1024 × 768 image and remains unusable. The five final genuine 1920 × 1080 PNG screenshots were captured from the actual installed MSIX in a visible Windows desktop session. All five passed independent review and cleanup; they are ready for owner review before any Store upload.

Microsoft requires at least one screenshot and recommends at least four for each supported device family. Desktop screenshots must be PNG, at least 1366 × 768 pixels, no larger than 50 MB, and may include captions up to 200 characters. The five captured screenshots are listed below in capture order:

| File to capture | Command and setup | Caption |
| --- | --- | --- |
| 01-cli-help.png | Run cn-mon.exe --help in Windows Terminal. | Start from the command line and see the available monitoring and history commands. |
| 02-icmp-table.png | Run cn-mon.exe --history-file $history --log-file $log 127.0.0.1 after setting the temporary paths as above. | Check a local IPv4 target and read the human-readable result table. |
| 03-json-tcp.png | Run cn-mon.exe --format json --tcp 443 --history-file $history --log-file $log 127.0.0.1. | Combine an ICMP check with an optional TCP port check and export structured JSON. |
| 04-history-list.png | Run cn-mon.exe --list --format table --history-file $history --log-file $log after the two runs above. | Review saved requests and runs in the local history database. |
| 05-history-detail.png | Replace REQUEST_ID with the ID from the history listing, then run cn-mon.exe --show REQUEST_ID --format md --history-file $history --log-file $log. | Inspect a saved request's results in Markdown. |

Keep the actual screenshots as source captures, preserve the console context that makes each command legible, and review every image for personal paths or test-machine identifiers before upload.

## Owner decisions still needed

- Confirm the exact reserved Store title spelling in Partner Center.
- Choose the primary category. “Developer tools > Networking” is a reasonable starting proposal, not a completed selection.
- Set release timing.
- Complete the mandatory IARC questionnaire for the first submission, including for a free app. Microsoft generates the applicable age rating from the answers; this draft does not assign one.
- Review the privacy policy and determine the Partner Center personal-information declaration for the package's declared capabilities. Microsoft may require a privacy policy based on declared capabilities even when the app itself does not upload user data.
- Confirm any publisher display name and package listing name in Partner Center privately. Keep reserved identity values out of public files.
- Omit the optional website field unless the owner supplies a product webpage. The GitHub Issues URL above is the selected support contact link.

## Editorial notes and current Microsoft guidance

Checked 2026-10-16 against Microsoft's current MSIX documentation:

- [MSIX Store listing fields and limits](https://learn.microsoft.com/en-us/windows/apps/publish/publish-your-app/msix/add-and-edit-store-listing-info): description up to 10,000 characters; product features up to 20, each no more than 200 characters; short description up to 1,000 characters and recommended under 270; the “What's new” field is up to 1,500 characters and should be blank for an initial submission. The description must be plain text without HTML, code snippets, or URLs.
- [Reserved app names](https://learn.microsoft.com/en-us/windows/apps/publish/publish-your-app/msix/reserve-your-apps-name): the title must be reserved; Microsoft documents a 256-character app-name maximum.
- [MSIX supplemental listing fields](https://learn.microsoft.com/en-us/windows/apps/publish/publish-your-app/msix/add-additional-information): up to seven search terms, no more than 40 characters each.
- [MSIX additional license terms](https://learn.microsoft.com/en-us/windows/apps/publish/publish-your-app/msix/add-additional-information): the field accepts one URL displayed as a link, or up to 10,000 characters of plain text. The repository `LICENSE` is 11,358 characters, so use the single URL above to link its exact Apache-2.0 text without rewriting it; keep `THIRD_PARTY_NOTICES.md` in the package as well.
- [MSIX screenshots and images](https://learn.microsoft.com/en-us/windows/apps/publish/publish-your-app/msix/screenshots-and-images): one desktop screenshot is required; Microsoft's guidance recommends at least four. Desktop PNGs must be at least 1366 × 768 and no larger than 50 MB; captions may be up to 200 characters.
- [MSIX support and privacy information](https://learn.microsoft.com/en-us/windows/apps/publish/publish-your-app/msix/support-info): a privacy policy is required when the app accesses, collects, or transmits personal information, and Microsoft may require one based on package capabilities. The support contact field is recommended for non-Xbox submissions.
- [MSIX categories](https://learn.microsoft.com/en-us/windows/apps/publish/publish-your-app/msix/categories-and-subcategories): a primary category is required; “Developer tools > Networking” is available.
- [MSIX age ratings](https://learn.microsoft.com/en-us/windows/apps/publish/publish-your-app/msix/age-ratings): the first submission requires the IARC questionnaire; the rating is based on questionnaire answers, including for a free app.

Before publishing the privacy URL, verify the actual history, log, and configuration paths under the installed package identity. The policy describes the CLI's local-file behavior; test-signer trust cleanup is complete.

This file prepares listing content only. It does not write to Partner Center, reserve a name, or submit or publish the product.
