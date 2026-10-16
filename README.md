# cn-mon

`cn-mon` is a native Go network monitor for Linux x86-64 and Windows x86-64. It supports target runs, read-only history listing and inspection, follow mode, JSON/CSV/Markdown/table reports, saved-batch reruns, and best-effort native terminal controls. The current source retains all 31 acceptance mappings. See the [development plan](docs/DEVELOPMENT_PLAN.md) for implementation evidence and release qualification details.

## Download a release

Download [cn-mon 0.3.0](https://github.com/evdanil/cn-mon/releases/tag/v0.3.0), published 2026-10-11 from qualified source `bd1ac548c22c0c49a54e2ce964a2f0a4da48a1ed`: [Linux archive](https://github.com/evdanil/cn-mon/releases/download/v0.3.0/cn-mon_0.3.0_linux_amd64.tar.gz), [Windows archive](https://github.com/evdanil/cn-mon/releases/download/v0.3.0/cn-mon_0.3.0_windows_amd64.zip), and [SHA256SUMS](https://github.com/evdanil/cn-mon/releases/download/v0.3.0/SHA256SUMS). All 13 jobs in [final run 38124176778](https://github.com/evdanil/gotest/actions/runs/38124176778) passed, including native qualification on both platforms and verification of the public tag and asset bytes. Version 0.3.0 includes saved-batch rerun, history management and live display refinements, with all 31 acceptance mappings retained.

Earlier [0.1.0](https://github.com/evdanil/gotest/releases/tag/v0.1.0) and [0.2.0](https://github.com/evdanil/gotest/releases/tag/v0.2.0) sources, tags and assets remain unchanged. Their qualification evidence and the 0.3.0 public hashes are recorded in the [development plan](docs/DEVELOPMENT_PLAN.md).

### Microsoft Store status

The x64 MSIX candidate (`1.3.0.0` for CLI `0.3.0`) is in qualification. Package construction and payload verification are implemented, and native Linux/Windows portable-archive qualification passed at `8d56363`. The latest synthetic run passed its three initial checks; the remaining 16 qualification gates, including account setup, are incomplete, and WACK was skipped. The private real-identity candidate remains unsigned, and no MSIX has been submitted. See the [MSIX specification](https://github.com/evdanil/cn-mon/blob/main/docs/MSIX_PACKAGING_SPEC.md), [implementation plan](https://github.com/evdanil/cn-mon/blob/main/docs/MSIX_PACKAGING_PLAN.md) and [Windows Store preparation guide](https://github.com/evdanil/cn-mon/blob/main/docs/WINDOWS_STORE_PREPARATION.md).

### Install cn-mon 0.3.0

Download the archive for your platform and verify its checksum before extracting.
On Linux:

```sh
line=$(grep -F '  cn-mon_0.3.0_linux_amd64.tar.gz' SHA256SUMS) || exit 1
test "$(printf '%s\n' "$line" | wc -l)" -eq 1 || exit 1
printf '%s\n' "$line" | sha256sum --check - || exit 1
tar -xzf cn-mon_0.3.0_linux_amd64.tar.gz
./cn-mon_0.3.0_linux_amd64/cn-mon --version
```

On Windows PowerShell:

```powershell
$lines = @(Get-Content .\SHA256SUMS | Where-Object { $_.EndsWith('  cn-mon_0.3.0_windows_amd64.zip') })
if ($lines.Count -ne 1) { throw 'Windows archive checksum is missing or duplicated' }
$expected = ($lines[0] -split '\s+', 2)[0]
$actual = (Get-FileHash .\cn-mon_0.3.0_windows_amd64.zip -Algorithm SHA256).Hash.ToLowerInvariant()
if ($actual -cne $expected) { throw 'Windows archive checksum mismatch' }
Expand-Archive .\cn-mon_0.3.0_windows_amd64.zip .\cn-mon
.\cn-mon\cn-mon_0.3.0_windows_amd64\cn-mon.exe --version
```

Release binaries identify their version and full source revision with `cn-mon <version> (<revision>)`; a source build reports `cn-mon dev`. The archives include the matching README, Apache-2.0 license, and third-party notices.

## Current CLI

Build and run help or version:

```sh
go run ./cmd/cn-mon --help
go run ./cmd/cn-mon --version
```

Help and version exit successfully without reading configuration or opening log, history, or network resources. Invalid CLI input exits 2. Target runs default to a table report and support JSON, CSV, and Markdown:

```sh
go run ./cmd/cn-mon --tcp 443 example.com
go run ./cmd/cn-mon --format json --tcp 443 example.com
go run ./cmd/cn-mon --list
go run ./cmd/cn-mon --rerun REQUEST_ID
go run ./cmd/cn-mon --rerun REQUEST_ID --every 30s --for 5m
go run ./cmd/cn-mon --show REQUEST_ID --format md
go run ./cmd/cn-mon --follow REQUEST_ID
go run ./cmd/cn-mon --list-runs REQUEST_ID --format table
go run ./cmd/cn-mon --delete-run RUN_ID
go run ./cmd/cn-mon --delete-request REQUEST_ID
go run ./cmd/cn-mon --clear-history
```

These history-management commands have been available since 0.2.0. `--list-runs` accepts a request ID or unique hexadecimal prefix and lists its runs. `--delete-run` requires the exact case-sensitive run ID; `--delete-request` accepts a unique request ID prefix. `--clear-history` removes all stored requests and runs. These history-management commands do not probe targets. A missing history file is left unchanged by `--clear-history`; listing a missing request returns exit 1. Deletion reports identify whether the change committed and include the number of deleted requests, runs, rounds, and results.

`--rerun` is available in 0.3.0. It accepts the request ID or unique hexadecimal prefix shown by `--list`, then repeats the newest saved run using its stored targets, hosts, ports, interval, and configured duration. `--every` and `--for` replace only the corresponding saved setting; these values start from a fresh run time. The current invocation supplies output, logging, history, retention, and `--since` settings. Rerunning does not read the original input file or stdin, so the saved batch remains usable after that file is removed.

Deletion checks every affected running run before committing. It refuses the entire operation if any selected run is active or its owner cannot be verified; only a run on this host with a PID conclusively verified dead may be removed while its status is still marked running. `--delete-request` acts on the canonical request group, including all of its runs, so an active sibling prevents deleting the other runs in that group.

Target runs collect input, resolve configuration and logging, probe targets, write completed rounds to history, and render the selected report. Exit 0 means the selected success condition was met, exit 1 means the completed run had no selected success, and exit 3 reports probe/history or other execution failure. List and show are read-only history operations. Follow requires stderr to be an interactive terminal. A real stdin terminal accepts `q` to stop and `v` to switch the live run display to a table view even when live drawing is unavailable or stderr is redirected.

In the current source, the live display skips unchanged frames and updates changed rows while its clock/countdown continues independently of the probe interval. The live `v` table shows the existing per-host `State` alongside `ICMP`; it does not add per-port TCP columns. `!` means an ICMP echo reply, while `T` means a TCP response when neither an echo reply nor target rejection determines the state. A `T` can therefore mean an open or refused port; inspect the final report for each requested port's result. Human table reports use available width for full values, including fitting 25-character timestamps on one row.

`--tcp PORTS` adds TCP checks alongside ICMP echo probes; it does not create a TCP-only mode. `!` records an ICMP echo reply. `T` means a TCP response arrived without an ICMP echo reply or a target rejection; the port may have accepted or refused the connection. Each cell has one glyph, so `!` and `T` do not combine. Use the table report to inspect each requested port's open or closed state.

Live terminal setup is best-effort: a setup or drawing failure does not prevent useful reports or history following. The program restores terminal modes and cursor state on normal completion, graceful stop, and Linux job-control suspension; it rearms input after returning to the foreground. Windows restoration preserves the original console modes, cursor visibility and size. If that state cannot be captured, live drawing stays disabled while eligible keys and reports remain available. On Linux, an ordinary diagnostic is omitted when its destination is the controlling terminal, the process is in the background, and that terminal has `TOSTOP` enabled; uncertain terminal job-control state also makes that physical write best-effort silent. Redirected diagnostics, file logging, report generation, and exit status remain available.

Scheduled runs remain anchored to their start time. After system suspension, ordinary timer jitter retains the selected slot; once a later slot has arrived, the next run uses the first aligned slot at or after the corrected time and skips expired slots instead of replaying them.

## Build and test

Use Go 1.27.2 for the current workflow; the root module requires Go 1.26.0 or newer. The repository contains the reviewed pro-bing core and pins the Go SQLite driver with the checksums required for its build and tests. SQLite is supplied as generated Go code, so the history store does not require a system SQLite library or CGO. With Go installed and available on `PATH`, run these commands from the repository root.

On Linux:

```sh
GO111MODULE=on GOTOOLCHAIN=local CGO_ENABLED=0 go build -o cn-mon ./cmd/cn-mon
GO111MODULE=on GOTOOLCHAIN=local go vet ./...
GO111MODULE=on GOTOOLCHAIN=local go test ./...
GO111MODULE=on GOTOOLCHAIN=local CGO_ENABLED=1 go test -race ./...
(cd third_party/pro-bing && GO111MODULE=on GOTOOLCHAIN=local go vet ./...)
(cd third_party/pro-bing && GO111MODULE=on GOTOOLCHAIN=local go test ./...)
(cd third_party/pro-bing && GO111MODULE=on GOTOOLCHAIN=local CGO_ENABLED=1 go test -race ./...)
```

On Windows PowerShell:

```powershell
$env:GO111MODULE = "on"
$env:GOTOOLCHAIN = "local"
$env:CGO_ENABLED = "0"
go build -o cn-mon.exe ./cmd/cn-mon
go vet ./...
go test ./...
$env:CGO_ENABLED = "1"
go test -race ./...
Push-Location third_party/pro-bing
$env:CGO_ENABLED = "0"
go vet ./...
go test ./...
Pop-Location
```

Race tests require a CGO-capable C compiler; the CI race checks run on Linux.
The adapted core has its own module files, so vet and test it separately as
shown above.

Native integration must run on the target operating system; cross-compilation
alone does not establish runtime support. The Linux harness exercises network
namespaces and raw-socket permission roles and requires `sudo`, `ip`,
`iptables`, `ip6tables`, and `setpriv`:

```sh
scripts/test-native-linux.sh
```

The Windows harness requires elevated PowerShell on a disposable test machine;
it creates scoped loopback aliases, firewall rules, and a temporary ordinary
user account:

```powershell
./scripts/test-native-windows.ps1
```

Both harnesses retain phase logs and clean up their temporary network state.
The [development plan](docs/DEVELOPMENT_PLAN.md) links the milestone evidence,
review history, and current release-qualification status. See
[third-party notices](THIRD_PARTY_NOTICES.md) for source provenance and license
details.

## History foundation

The schema-v1 history store reads and writes databases compatible with the
pinned cn-tool `PingStore`. A target run in any supported report format opens the configured
database, prunes it using the resolved retention setting, and appends completed
rounds. The test-only Python helper uses
the pinned cn-tool source to verify both directions of database interoperability
and concurrent WAL access, including held reader snapshots, pruning, and killed
writers in both languages. The fixture remains generated with Python 3.12.14.
The interop helper requires Python 3.12.14 on Linux and 3.12.10 on Windows;
the latter is the newest exact 3.12.x x64 release present in the official
`actions/python-versions` manifest for Windows. To run the Linux gate locally,
provide Python 3.12.14 and a checkout of cn-tool at commit
`2e2f3c40ef6718486f1ca4477f9145029ec495fa`, then run:

```sh
CNMON_HISTORY_INTEROP=1 \
CNMON_CN_TOOL_SOURCE=/path/to/cn-tool \
CNMON_PYTHON=python3 \
CGO_ENABLED=0 go test -v -timeout=8m \
  -run '^Test(PythonSourceFixtureSemantics|NativeHistoryPythonGoInterop|NativeHistoryConcurrentPythonGoWALAndPrune|NativeHistoryKilledPythonWriterRecovers|NativeHistoryKilledGoWriterRecovers|NativeHistoryHeldGoSnapshotAcrossPythonPrune|NativeHistoryHeldPythonSnapshotAcrossGoPrune)$' \
  ./internal/history
```

The native interop gate records the Python and Go SQLite versions, source
revision and checksums, WAL page details and bounded WAL growth during held
snapshots, and sampled Go heap usage. The seven required cases passed on both
hosted platforms in final M3 validation at commit `f0825828ad9e3b8176c80f55d7d449ccd2707cc2`.
The runs verified these runtime versions and the same two pinned raw source
hashes on each platform:

| Hosted runner | Go / mode | Go SQLite driver / SQLite | Python / stdlib SQLite |
| --- | --- | --- | --- |
| Linux `amd64` | Go 1.27.2, `CGO_ENABLED=0` | ncruces driver 0.35.6 / SQLite 3.53.4 | Python 3.12.14 / SQLite 3.45.1 |
| Windows `amd64` | Go 1.27.2, `CGO_ENABLED=0` | ncruces driver 0.35.6 / SQLite 3.53.4 | Python 3.12.10 / SQLite 3.49.1 |

Both source files were checked against their pinned raw SHA-256 values:
`ping_store.py` `326ab0570dc7bf3fc43209d21d00f46f8c2b8778ea0daf4cf905c52a8c337fd4`;
`ping_history.py` `d491678b240f23d26a2673951ba293eabff3a992d2840bbc1219a63ec31a89bf`.
See the [Linux native history job](https://github.com/evdanil/gotest/actions/runs/38045035366/job/114192741491)
and [Windows native history job](https://github.com/evdanil/gotest/actions/runs/38045035366/job/114192741369)
for the runtime annotations and passing case markers. The checked-in fallback history path is
`~/.cn-ping-history.db` with 30-day retention; the five INI settings and CLI
override precedence are listed in [Configuration foundation](#configuration-foundation).
History databases are local files for use on one machine; WAL on NFS/SMB or
shared concurrently across machines is unsupported. For a backup, use SQLite's
online-backup facility, or stop every writer, complete a successful checkpoint
and close the database, then copy it. Do not copy only a live `.db` file while
its WAL may contain committed pages. Hosted `govulncheck` v1.8.0 scans against
the official Go vulnerability database passed for both `linux/amd64` and
`windows/amd64`, covering `./cmd/...` and `./internal/...` at the same candidate
commit; see the [final M3 CI run](https://github.com/evdanil/gotest/actions/runs/38045035366)
for the Linux and Windows scan jobs. The [final review confirmation](https://github.com/evdanil/gotest/pull/10#issuecomment-6096607314)
records the merge-gate result.
The Python dependency is test-only and is not part of the application runtime.

## Configuration foundation

The native configuration resolver recognizes five existing cn-tool INI settings:

| INI setting | CLI override |
| --- | --- |
| `[ping] history_file` | `--history-file` |
| `[ping] history_days` | `--history-days` |
| `[theme] theme` | `--theme` / `-t` |
| `[logging] logfile` | `--log-file` / `-l` |
| `[logging] level` | `--log-level` |

Configuration layers, from lower to higher precedence, are built-in defaults, `.cn` beside the resolved executable, the user's `~/.cn` (`%USERPROFILE%\\.cn` on Windows), an optional INI file added with `--config` / `-c`, and explicit setting overrides. Target runs and history readers use the resolved history path, retention, and logger. Theme selection applies to human reports and the live screen. See the [configuration contract](docs/APPLICATION_SPEC.md#21-existing-cn-tool-configuration) for parsing and fallback details.

## Compatibility and license

The utility retains cn-tool's built-in monitor behavior and compatible `.cn` settings for history, theme, and logging. It excludes the interactive menu, XLSX reports, Python runtime, `process_data` hooks, and custom Python plugins. It uses native Go ICMP support and does not call a system `ping` command.

New utility code is licensed under Apache-2.0. Its behavior is characterized against cn-tool commit [`2e2f3c40ef6718486f1ca4477f9145029ec495fa`](https://github.com/evdanil/cn-tool/tree/2e2f3c40ef6718486f1ca4477f9145029ec495fa); provenance notes describe the adapted behavior, and no Python implementation code is copied. See [LICENSE](LICENSE), the [application specification](docs/APPLICATION_SPEC.md), and the [development plan](docs/DEVELOPMENT_PLAN.md).
