# cn-mon

`cn-mon` is being built as a native Go network monitor for Linux x86-64 and Windows x86-64. The M1 foundation includes help/version, CLI validation, configuration and logging components, target input and request identity, and the pure monitor model. Target execution, probes, history storage, live display, and reports are pending. This is not yet an operational network monitor.

## Current CLI

Build and run help or version:

```sh
go run ./cmd/cn-mon --help
go run ./cmd/cn-mon --version
```

Help and version exit successfully without reading configuration or opening log, history, or network resources. Invalid CLI input exits 2. Valid target, `--list`, `--show`, and `--follow` requests report that the operation is unavailable and exit 3; they perform no target, configuration, log, history, or network I/O in M1.

## Build and test

Use Go 1.27.2 for the current workflow; the module requires Go 1.25 or newer. It currently has no external dependencies. With Go installed and available on `PATH`, run these commands from the repository root.

On Linux:

```sh
GO111MODULE=on GOTOOLCHAIN=local CGO_ENABLED=0 go build -o cn-mon ./cmd/cn-mon
GO111MODULE=on GOTOOLCHAIN=local go test ./...
GO111MODULE=on GOTOOLCHAIN=local CGO_ENABLED=1 go test -race ./...
```

On Windows PowerShell:

```powershell
$env:GO111MODULE = "on"
$env:GOTOOLCHAIN = "local"
$env:CGO_ENABLED = "0"
go build -o cn-mon.exe ./cmd/cn-mon
go test ./...
$env:CGO_ENABLED = "1"
go test -race ./...
```

Build and run on each target OS for native validation. A cross-compiled executable does not establish runtime support. The current build and tests exercise the M1 foundation only; they do not validate network monitoring.

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
