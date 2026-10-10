# cn-mon

`cn-mon` is being built as a native Go network monitor for Linux x86-64 and Windows x86-64. The M1 foundation includes help/version, CLI validation, configuration and logging components, target input and request identity, and the pure monitor model. M2 native probe components and their Linux/Windows runtime gates are complete; target execution through the CLI, history storage, live display, and reports remain pending. This is not yet an operational network monitor.

## Current CLI

Build and run help or version:

```sh
go run ./cmd/cn-mon --help
go run ./cmd/cn-mon --version
```

Help and version exit successfully without reading configuration or opening log, history, or network resources. Invalid CLI input exits 2. Valid target, `--list`, `--show`, and `--follow` requests report that the operation is unavailable and exit 3; these command paths currently perform no target, configuration, log, history, or network I/O.

## Build and test

Use Go 1.27.2 for the current workflow; the module requires Go 1.25 or newer. M2 has a local copy of the reviewed pro-bing core and pins its full Go dependency graph and checksums. With Go installed and available on `PATH`, run these commands from the repository root.

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
