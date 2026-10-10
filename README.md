# cn-mon

`cn-mon` is a native Go network monitor for Linux x86-64 and Windows x86-64. The M1 configuration, logging, target-input and model foundation, M2 native probes, and M3 persistent history/interoperability are merged. M3 merged in [PR 10](https://github.com/evdanil/gotest/pull/10); all 12 CI checks passed, and external review reported no remaining findings. M4 now connects target runs to persistent history and emits final JSON when `--format json` is explicit. Independent Sol medium code review has approved M4; external review and the built-command native Linux/Windows gates are pending. Table/CSV/Markdown reports and live display/readers remain M5 work; the final default format is still table.

## Current CLI

Build and run help or version:

```sh
go run ./cmd/cn-mon --help
go run ./cmd/cn-mon --version
```

Help and version exit successfully without reading configuration or opening log, history, or network resources. Invalid CLI input exits 2. A target run must explicitly request JSON during M4:

```sh
go run ./cmd/cn-mon --format json --tcp 443 example.com
```

That command collects input, resolves configuration and logging, opens history, probes targets, and writes the final JSON report. Exit 0 means the selected success condition was met, exit 1 means the completed run had no selected success, and exit 3 reports probe/history or other execution failure. `--list`, `--show`, `--follow`, and target runs using `table`, `md`, or `csv` remain unavailable until M5 and exit 3 before data I/O.

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

The M4 built-command checks exercise the compiled CLI and are separate from the
probe-package gates above. They verify config-driven history retention, loopback
TCP/history/JSON readback, startup and active-run signals, second-interrupt
finalization, pause/resume, and closed stdout. Linux additionally runs the same
built command with and without `CAP_NET_RAW`; Windows checks ICMP under the
elevated runner and a temporary ordinary account using a private console.
On Linux, the standard Go runtime resets an inherited ignored `SIGTERM` before
`main`; the command therefore keeps default pre-run `SIGTERM` behavior while
handling `SIGTERM` gracefully during a run. Inherited ignored `SIGHUP` and
`SIGINT` remain honored.
Run them only on disposable target-OS machines with the required privileges:

```sh
# Linux: sudo, setpriv, and getcap must be available.
scripts/test-native-command-linux.sh
```

```powershell
# Windows: elevated PowerShell on a disposable machine.
./scripts/test-native-command-windows.ps1
```

The M4 native command checks have not yet passed hosted CI; do not treat
cross-compilation or the component tests as native command validation.

## History foundation

The schema-v1 history store reads and writes databases compatible with the
pinned cn-tool `PingStore`. An explicit-JSON target run opens the configured
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

Configuration layers, from lower to higher precedence, are built-in defaults, `.cn` beside the resolved executable, the user's `~/.cn` (`%USERPROFILE%\\.cn` on Windows), an optional INI file added with `--config` / `-c`, and explicit setting overrides. The explicit JSON target command uses the resolved history path/retention and logger. Theme selection takes effect with the human renderers when they arrive in M5. See the [configuration contract](docs/APPLICATION_SPEC.md#21-existing-cn-tool-configuration) for parsing and fallback details.

## Compatibility and license

The planned utility retains cn-tool's built-in monitor behavior and compatible `.cn` settings for history, theme, and logging. It excludes the interactive menu, XLSX reports, Python runtime, `process_data` hooks, and custom Python plugins. The planned monitor uses native Go ICMP support and will not call a system `ping` command.

New utility code is licensed under Apache-2.0. Its behavior is characterized against cn-tool commit [`2e2f3c40ef6718486f1ca4477f9145029ec495fa`](https://github.com/evdanil/cn-tool/tree/2e2f3c40ef6718486f1ca4477f9145029ec495fa); provenance notes describe the adapted behavior, and no Python implementation code is copied. See [LICENSE](LICENSE), the [application specification](docs/APPLICATION_SPEC.md), and the [development plan](docs/DEVELOPMENT_PLAN.md).
