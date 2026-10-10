# cn-mon

`cn-mon` is being built as a native Go network monitor for Linux x86-64 and Windows x86-64. The M1 foundation includes help/version, CLI validation, configuration and logging components, target input and request identity, and the pure monitor model. M2 native probe components passed their Linux and Windows gates. M3 adds persistent history APIs and cross-language interoperability; all seven required history cases passed on hosted Linux and Windows CI. The current [PR 10](https://github.com/evdanil/gotest/pull/10) is awaiting final review and merge. The CLI does not yet connect these components; target runs and JSON reports are M4 work, while table/CSV/Markdown reports and live display/readers remain M5 work. This is not yet an operational network monitor.

## Current CLI

Build and run help or version:

```sh
go run ./cmd/cn-mon --help
go run ./cmd/cn-mon --version
```

Help and version exit successfully without reading configuration or opening log, history, or network resources. Invalid CLI input exits 2. Valid target, `--list`, `--show`, and `--follow` requests report that the operation is unavailable and exit 3; these command paths currently perform no target, configuration, log, history, or network I/O.

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
shown above. Run its race test on Linux with CGO enabled. The native ICMP gate
must also be run on the target operating system; cross-compilation alone does
not establish runtime support:

```bash
# Linux: run from the repository root on a disposable host with sudo access.
scripts/test-native-linux.sh
```

The Linux harness requires `sudo`, `ip`, `iptables`, `ip6tables`, and
`setpriv`; it creates disposable network namespaces and exercises both
privileged and denied raw-socket phases. On Windows, use an elevated PowerShell
session on a disposable test machine:

```powershell
./scripts/test-native-windows.ps1
```

The Windows harness requires administrator rights to add and remove scoped
loopback aliases and firewall rules, and to create and remove a temporary
unprivileged test account. Both scripts log phase evidence and clean up their
temporary network state. The native Linux gate passed its echo, target-error,
intermediate-error, and raw-socket permission phases. The Windows native gate
also passed its required probe and permission phases. Both results, along with
the ordinary Linux and Windows validation jobs, are recorded in [CI run
38037349729](https://github.com/evdanil/gotest/actions/runs/38037349729):
[Linux native](https://github.com/evdanil/gotest/actions/runs/38037349729/job/114170451322),
[Windows native](https://github.com/evdanil/gotest/actions/runs/38037349729/job/114170451856),
[Linux checks](https://github.com/evdanil/gotest/actions/runs/38037349729/job/114170451464),
and [Windows checks](https://github.com/evdanil/gotest/actions/runs/38037349729/job/114170451412).
The job artifacts contain the native phase logs and platform details. Package
tests and cross-compilation alone do not establish native network support or
raw ICMP permission behavior. See [third-party notices](THIRD_PARTY_NOTICES.md)
for source provenance and license details.
CI also runs `govulncheck` v1.8.0 against the Linux and Windows production
package imports using the official Go vulnerability database.

## History foundation

M3 adds a source-compatible schema-v1 history store API. It reads and writes
history databases compatible with the pinned cn-tool `PingStore`; application
startup does not yet open history or prune it. The test-only Python helper uses
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
hosted platforms at candidate commit `6b1dc93236fff50828957b92698993d4de07de7c`.
The runs verified these runtime versions and the same two pinned raw source
hashes on each platform:

| Hosted runner | Go / mode | Go SQLite driver / SQLite | Python / stdlib SQLite |
| --- | --- | --- | --- |
| Linux `amd64` | Go 1.27.2, `CGO_ENABLED=0` | ncruces driver 0.35.6 / SQLite 3.53.4 | Python 3.12.14 / SQLite 3.45.1 |
| Windows `amd64` | Go 1.27.2, `CGO_ENABLED=0` | ncruces driver 0.35.6 / SQLite 3.53.4 | Python 3.12.10 / SQLite 3.49.1 |

Both source files were checked against their pinned raw SHA-256 values:
`ping_store.py` `326ab0570dc7bf3fc43209d21d00f46f8c2b8778ea0daf4cf905c52a8c337fd4`;
`ping_history.py` `d491678b240f23d26a2673951ba293eabff3a992d2840bbc1219a63ec31a89bf`.
See the [Linux native history job](https://github.com/evdanil/gotest/actions/runs/38043224090/job/114187473559)
and [Windows native history job](https://github.com/evdanil/gotest/actions/runs/38043224090/job/114187473348)
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
commit; see the [Linux scan](https://github.com/evdanil/gotest/actions/runs/38043224090/job/114187473308)
and [Windows scan](https://github.com/evdanil/gotest/actions/runs/38043224090/job/114187473356).
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

Configuration layers, from lower to higher precedence, are built-in defaults, `.cn` beside the resolved executable, the user's `~/.cn` (`%USERPROFILE%\\.cn` on Windows), an optional INI file added with `--config` / `-c`, and explicit setting overrides. The resolver and logger are implemented as components; command startup does not yet connect them. See the [configuration contract](docs/APPLICATION_SPEC.md#21-existing-cn-tool-configuration) for parsing and fallback details.

## Compatibility and license

The planned utility retains cn-tool's built-in monitor behavior and compatible `.cn` settings for history, theme, and logging. It excludes the interactive menu, XLSX reports, Python runtime, `process_data` hooks, and custom Python plugins. The planned monitor uses native Go ICMP support and will not call a system `ping` command.

New utility code is licensed under Apache-2.0. Its behavior is characterized against cn-tool commit [`2e2f3c40ef6718486f1ca4477f9145029ec495fa`](https://github.com/evdanil/cn-tool/tree/2e2f3c40ef6718486f1ca4477f9145029ec495fa); provenance notes describe the adapted behavior, and no Python implementation code is copied. See [LICENSE](LICENSE), the [application specification](docs/APPLICATION_SPEC.md), and the [development plan](docs/DEVELOPMENT_PLAN.md).
