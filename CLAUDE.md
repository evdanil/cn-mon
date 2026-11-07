# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

@AGENTS.md

`AGENTS.md` (imported above) is the binding source for workflow, scope, licensing and the `trail` code graph. This file adds commands, architecture and known traps.

cn-mon is a CGO-free Go network monitor for Linux and Windows x86-64: ICMP and TCP checks, a live terminal display, SQLite history and table/JSON/CSV/Markdown reports. It carries forward the monitor behavior of the Python `cn-tool`.

## Agent roles in Claude Code

`AGENTS.md` names its roles by GPT model. In Claude Code, use these equivalents through the Agent tool's `model` and `effort` settings:

| Role | `AGENTS.md` model | `model` | `effort` |
| --- | --- | --- | --- |
| Implementation | `gpt-6-luna` | `sonnet` | `xhigh` |
| Independent code review | `gpt-6.1-sol` | `opus` | `medium` |
| Specifications and milestone planning | `gpt-6-astra` | `fable` | `xhigh` |

The external `@codex review` step is unchanged. The development plan's rule still applies: verify the model and effort that ran, and never claim one that was unavailable.

## Commands

CI pins Go 1.27.2; `go.mod` sets the floor at 1.26.0. There is no Makefile or linter config: the gates are `gofmt`, `go vet` and `govulncheck`.

If `rtk` is installed, a hook rewrites common commands through it and the filtered output can drop detail. Use `rtk proxy <cmd>` when exact output matters, such as file listings, `gofmt -l` or verbose `go test` markers.

```sh
# Main module
gofmt -l .                                  # must print nothing
go vet ./...
go test ./...
CGO_ENABLED=1 go test -race ./...

# Single test
go test -run '^TestName$' ./internal/monitor

# third_party/pro-bing is a nested module, so the root ./... does not cover it
go -C third_party/pro-bing vet ./...
go -C third_party/pro-bing test ./...
CGO_ENABLED=1 go -C third_party/pro-bing test -race ./...

# Build (without injected -ldflags, --version prints "cn-mon dev")
CGO_ENABLED=0 go build -o dist/cn-mon ./cmd/cn-mon

# Vulnerability scan, as CI runs it (govulncheck v1.8.0)
govulncheck -scan=symbol ./cmd/... ./internal/...
```

The workflow contract tests check the workflow files, so run them after editing `.github/workflows/` or `packaging/`:

```sh
python3 scripts/test-release-workflow.py
python3 scripts/test-msix-workflow.py
python3 scripts/test-store-update-workflow.py
python3 scripts/test-store-update.py
pwsh -NoProfile -File scripts/test-store-update-qualification.ps1
pwsh -NoProfile -File scripts/test-msix-desktop-behavior-baseline.ps1
```

### Test tiers

| Tier | Gate | Notes |
| --- | --- | --- |
| Ordinary | none | Offline and unprivileged. Must not use network silence or public DNS as a test oracle. |
| Native command | `-tags native`, `CNMON_NATIVE_COMMAND=1` | `CGO_ENABLED=0 CNMON_NATIVE_COMMAND=1 go test -tags native -v -run '<regex>' ./cmd/cn-mon`. Some cases need extra `CNMON_*` variables set by `scripts/test-native-command-*`. |
| Native network | `scripts/test-native-linux.sh`, `scripts/test-native-windows.ps1` | The Linux script is marked disposable-runner-only (sudo, network namespaces, iptables). Leave these to CI unless asked. |
| History interop | `CNMON_HISTORY_INTEROP=1`, `CNMON_CN_TOOL_SOURCE`, `CNMON_PYTHON` | Runs `./internal/history` against a pinned `cn-tool` checkout. See the interop jobs in `.github/workflows/native.yml` for the test selector. |

Native tests skip without their gates, and a skip is not a pass. `go test` on Linux does not compile `*_windows.go` files, so Windows behavior is only exercised by the Windows jobs in `native.yml`.

## Architecture

`internal/target`, `internal/probe` and `internal/history` import no other internal packages. `internal/monitor` imports all three, `internal/output` imports `monitor`, `history` and `target`, and `cmd/cn-mon` wires everything together.

| Package | Responsibility |
| --- | --- |
| `target` | Turns input objects into a canonical `Request` with a stable identity |
| `probe` | DNS, ICMP and TCP probing, with OS-specific error mapping |
| `history` | SQLite store: reads, writes, pruning and management |
| `monitor` | Pure `Tracker` model plus the `Run` orchestrator, scheduling and follow |
| `output` | Reports and the live terminal `Screen` |

`runCLIWithRuntime` (`cmd/cn-mon/main.go`) calls `parseCLI` (`cli.go`) to pick a mode and `loadSettings` (`config.go`) to merge cn-tool INI files with CLI overrides. It then dispatches:

- **Target run:** `target.Build`, then `monitor.Run`.
- **Rerun:** `selectRerun` (`rerun.go`) restores the request from saved history, then joins the target-run path.
- **Readers (`--list`, `--show`, `--follow`):** `runReaderCLI` (`readers.go`).
- **History management (`--delete-run` and similar):** `runHistoryManagementCLI` (`history_management.go`).

Design rules from `docs/APPLICATION_SPEC.md` §10 that span packages:

- A single orchestrator owns tracker state and store writes. Probe workers only return indexed values.
- Renderers never mutate the model or write history.
- Add interfaces only at real nondeterministic boundaries: clock, resolver/prober, store, terminal/signal input. Tests use fake clocks and real temporary SQLite.
- Split OS-specific code with build tags or filename suffixes, not one interface per type.

### Compatibility constraints

- **History format:** the database is cn-tool's schema version 1, default path `~/.cn-ping-history.db`, and must interoperate with Python writers concurrently under WAL. There are no migrations, and stored keys and positional host lists are never recomputed or rewritten.
- **SQLite driver:** `ncruces/go-sqlite3` is generated Go and needs no cgo. `modernc` was rejected because its declared module graph includes MPL-2.0 code; lazy module pruning does not waive the no-copyleft rule.
- **ICMP:** `go.mod` replaces `pro-bing` with the patched copy in `third_party/pro-bing`. Upstream drops the ICMP error messages cn-mon must correlate, and its Linux BPF filter excludes them, so it cannot be swapped back unmodified (spec §4).
- **Python:** it is used only by tests and automation under `scripts/`. The product has no Python runtime support.

## Documentation

`docs/APPLICATION_SPEC.md` is the behavior contract and `docs/DEVELOPMENT_PLAN.md` holds the milestone rules and evidence. Both are over 100 KB, so find the heading with `grep -n '^##'` and read that section. Key spec sections: §2 commands and configuration, §4 probes, §5 scheduling and signals, §7 history, §8 terminal and reports, §9 exit status, §10 architecture, §11 acceptance scenarios. PRs map their tests to acceptance IDs such as `RUN-04` and `TTY-02`.

## Known traps

- **Stale repository name:** the docs often name `evdanil/gotest`, but `origin` is `evdanil/cn-mon`. Open PRs against `evdanil/cn-mon`.
- **Stale placeholder rule:** the `main.go` placeholder rule in `AGENTS.md` predates implementation. `cmd/cn-mon/main.go` is the real entry point.
- **Cloud-only path:** the `/workspace/.cloud-setup/...` Go path in the development plan applies only to that cloud setup.
- **Publishing has production side effects:** publishing a stable release starts `store-update.yml`, which submits to the Microsoft Store. The `release.yml` publish job dispatches it, and `store-release-dispatch.yml` does the same for a release published by hand. Retry only a run that stopped before submitting: once a submission exists for a tag and `main` has moved, a new dispatch creates another submission (`docs/STORE_UPDATES.md`). Keep the protected `CNMON_MSIX_*` and `CNMON_STORE_*` values out of repository files and logs.
- **Release version pins:** a release bumps literal version pins across the release scripts, native tests and `release.yml`. `packaging/windows/release.json` is the PR-time MSIX CI pin and keeps its own version. The development plan's release milestone lists which literals change and which stay.
- **WACK versions:** `scripts/test-msix-windows.ps1` accepts only the versions in `$measuredWackVersions`. When a runner image ships a new WACK version, the MSIX checks fail until a report from it is measured and the version is added.
- **Qualification records:** the JSON files in `packaging/windows/qualification/` are historical evidence bound to their original candidates and marked `-text` in `.gitattributes`. Do not regenerate or normalize them.
