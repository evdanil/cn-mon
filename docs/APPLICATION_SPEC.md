# cn-mon application specification

Status: M0–M11 are complete through [PR #20](https://github.com/evdanil/gotest/pull/20), merged as `bd1ac548c22c0c49a54e2ce964a2f0a4da48a1ed` at `2026-10-11T08:15:12Z`. [Version 0.3.0](https://github.com/evdanil/gotest/releases/tag/v0.3.0) was qualified and published from that exact source at `2026-10-11T08:24:40Z`. All 13 final source/native/qualification/publication jobs passed; the public tag and all three asset hashes were verified against the qualified source and bytes. The development plan records the exact reviews, native role outcomes and release evidence.

Version 0.3.0 adds M10 saved-batch rerun to the existing monitor, history management and live display refinements, retaining all 31 acceptance mappings. Sections 10.3, 10.6 and 10.8 retain the release contracts. Published 0.1.0 remains pinned to M6 source `f156255812308db7039d9721945957348ddbf8b4`, and 0.2.0 to M9 source `968ea23aed5541e86860bde45743ee054ad4a4d8`; their tags and assets remain unchanged. M10's unpublished 0.2.0 candidates and M11's PR-head candidates remain separate from the freshly qualified merged-source 0.3.0 release. This later evidence-only documentation update does not rebuild or replace any published source snapshot or asset.

Destination: `evdanil/gotest`. Product and executable: **cn-mon** (`cn-mon.exe` on Windows).

## 1. Purpose and compatibility boundary

Build a standalone Go CLI retaining the Ping Monitor behavior of `evdanil/cn-tool` 0.8.0, commit `2e2f3c40ef6718486f1ca4477f9145029ec495fa`. The reference is its `README.md`, `cn_tool/modules/ping_monitor.py`, `bulk_ping.py`, and `utils/ping_history.py`, `ping_store.py`, `ping_grid.py`, `ping_live.py`, `cli_input.py`, and `render.py`. When reference prose and code differ, the explicit contract below governs; characterization fixtures must record the source revision and Python version used.

Supported release targets are **Linux x86-64** and **Windows x86-64**. ICMP is implemented in Go using maintained, permissively licensed Go code and OS socket APIs. Neither platform may execute `ping`, PowerShell, a shell, or another helper to perform network probes. The delivered binary needs no Python, C compiler, separately installed SQLite library, or network service.

Retain IPv4/IPv6 targets, optional TCP probes, repeated and single-round runs, persistent history, request identity, sampled online periods, live terminal grid/table, `q`/`v`, signals, `--list`, `--show`, `--follow`, table/JSON/Markdown/CSV output, and the monitor-effective cn-tool INI settings for history, theme and logging. Existing configured history paths/retention remain effective; their fallbacks remain `~/.cn-ping-history.db` and 30 days. Deliberate changes are the standalone command name, additional CLI overrides for history settings, native ICMP error handling, and the two supported platforms. Native error attribution may improve upon Windows' old text-parser limitations, but must never invent a sender.

The approved compatibility exceptions are removal of the interactive menu/prompts, XLSX and `--report`/`-r`/`--report-file`, and Python `process_data` hooks/custom-plugin framework, together with the platform restriction to Linux and Windows x86-64 and the narrow standard-Go inherited-SIGTERM exception in section 5. Retain built-in monitor behavior and the monitor-effective INI settings defined here. Also exclude other cn-tool modules and their credentials, a daemon/service installer, web UI, notifications, alerts, authentication, packet capture, traceroute, arbitrary packet construction, RTT analytics, and a general-purpose configuration framework. There is no background service; cron or Windows Task Scheduler invokes the CLI.

**Custom extension boundary.** Python hooks and a Python adapter are explicitly excluded from the approved native utility scope. No shipped plugin targets the monitor in the pinned source: the [AD plugin targets `subnet_request`](https://github.com/evdanil/cn-tool/blob/2e2f3c40ef6718486f1ca4477f9145029ec495fa/cn_tool/plugins/activedirectory_support.py#L34-L36), the [SD-WAN plugin targets `config_search`](https://github.com/evdanil/cn-tool/blob/2e2f3c40ef6718486f1ca4477f9145029ec495fa/cn_tool/plugins/sdwan_yaml_search.py#L33-L35), and the [email](https://github.com/evdanil/cn-tool/blob/2e2f3c40ef6718486f1ca4477f9145029ec495fa/cn_tool/plugins/email_support.py#L19-L21) and [config-analyzer settings](https://github.com/evdanil/cn-tool/blob/2e2f3c40ef6718486f1ca4477f9145029ec495fa/cn_tool/plugins/config_analyzer_settings.py#L20-L22) plugins have no target. Custom plugins can nevertheless register monitor `process_data` callbacks: [monitor lines 1016–1037](https://github.com/evdanil/cn-tool/blob/2e2f3c40ef6718486f1ca4477f9145029ec495fa/cn_tool/modules/ping_monitor.py#L1016-L1037) applies them before observations, storage, summaries and exit status, and removed host rows become silent observations. [Callbacks receive the Python context and chain in registration order](https://github.com/evdanil/cn-tool/blob/2e2f3c40ef6718486f1ca4477f9145029ec495fa/cn_tool/core/base.py#L116-L135). That custom transformation/lifecycle behavior is outside compatibility: cn-mon derives new observations from its native probe results, does not discover/load custom plugins or start a Python runtime, and continues to read compatible stored history, including rows previously produced by hooks.

## 2. Command contract

```text
cn-mon [TARGET ...|-] [-f|--file PATH|-] [--tcp PORTS]
       [--every INTERVAL] [--for DURATION] [--since WHEN]
       [--history-file PATH] [--history-days DAYS] [--format table|json|md|csv] [COMMON]
cn-mon --list [--history-file PATH] [--format FORMAT] [COMMON]
cn-mon --show ID [--since WHEN] [--history-file PATH] [--format FORMAT] [COMMON]
cn-mon --follow ID [--since WHEN] [--history-file PATH] [--format FORMAT] [COMMON]
cn-mon -h|--help
cn-mon -v|--version

COMMON: [-c|--config PATH] [-t|--theme THEME] [-l|--log-file PATH] [--log-level LEVEL]
```

The merged command dispatches target/list/show/follow in `table`, `json`, `md` and `csv`, with `table` as the operational default. M5 removed M4's temporary explicit-JSON guard and passed its terminal/native/review gate. M6 must qualify the versioned archives separately before release.

Options work before or after targets; `--` ends option parsing. No arguments prints a usage diagnostic and exits 2; it never opens a menu. `-h/--help` and `-v/--version` exit 0 without reading configuration/targets or opening logs, history or network sockets. Reject unknown options, malformed option values, and combinations outside the four modes; repeated scalar options use their last value, including when short and long spellings are mixed, as in the reference CLI. `--list`, `--show`, and `--follow` are mutually exclusive and accept no targets, `-f/--file`, `--tcp`, `--every`, or `--for`; `--list` also rejects `--since`. `--history-days` applies to target runs only; common options apply to all four modes. A request ID is a case-insensitive hexadecimal prefix of 4–64 characters; it must identify exactly one stored full key.

Preserve the reference parser's early-exit precedence: reaching help/version bypasses unknown options deferred by parsing and later semantic checks (mode conflicts, ID constraints, reader restrictions, minimum durations and `--for >= --every`), but a missing required value or invalid typed/choice value encountered earlier exits 2 first. Thus `--unknown --help`, `--show nope --help` and `--every 5s --help` exit 0, while `--every nonsense --help` and `--file --help` exit 2. Semantic checks use the final scalar value: `--show bad --show a1b2` has a valid final ID. Arguments after an invoked help/version action are not validated; spellings after `--` are target text. These parser exits perform no data I/O. The source [parses before checking monitor modes](https://github.com/evdanil/cn-tool/blob/2e2f3c40ef6718486f1ca4477f9145029ec495fa/cn_tool/main.py#L1035-L1049).

The complete retained short-option map is `-c/--config`, `-f/--file`, `-h/--help`, `-l/--log-file`, `-t/--theme`, and `-v/--version`. The reference's version option is root-only; cn-mon has one root command, so both spellings are available there. The live `v` key remains a separate terminal control. `--format`, `--log-level`, `--tcp`, `--every`, `--for`, `--since`, `--list`, `--show`, and `--follow` have no short aliases; neither do the new history overrides. Source authority is [main.py, lines 193–229](https://github.com/evdanil/cn-tool/blob/2e2f3c40ef6718486f1ca4477f9145029ec495fa/cn_tool/main.py#L193-L229), [monitor options, lines 434–461](https://github.com/evdanil/cn-tool/blob/2e2f3c40ef6718486f1ca4477f9145029ec495fa/cn_tool/main.py#L434-L461), and [help registration, lines 622–624](https://github.com/evdanil/cn-tool/blob/2e2f3c40ef6718486f1ca4477f9145029ec495fa/cn_tool/main.py#L622-L624).

Source framework flags outside the approved utility scope are rejected as unknown options: `-nc/--no-cache` controls cn-tool's cache, which the monitor's OS resolver/native probes do not use; `-g/--gpg-file` supplies credentials for excluded modules/custom plugins; `-r/--report-file` and `--report` select the excluded XLSX output. Do not add cache or credential frameworks to support them. `--view`/`--all-views` belong to other commands and are not source monitor options.

| Setting | Default and rules |
| --- | --- |
| `--format` | `table`; accepted values: `table`, `json`, `md`, `csv` |
| Run length | Neither scheduling flag: exactly one round; only `--every`: until stopped; only `--for`: interval 60 seconds |
| Durations | Case-insensitive 1–4 ASCII digits followed by `s`, `m`, `h`, `d`, or `w`, with outer whitespace ignored; no fractional, signed, bare, or composite durations |
| `--every` | At least 10 seconds |
| `--for` | At least 10 seconds; when `--every` is explicitly supplied, duration must be at least that interval. `--for 30s` alone is valid and has one possible round on the default 60-second interval |
| `--since` | All retained earlier rounds if omitted; age in minutes/hours/days/weeks or a Python 3.12.14 `datetime.fromisoformat`-compatible date/date-time. Retain extended/basic/week dates, single-character date/time separators, dot/comma fractions truncated to microseconds, and offsets `±HH`, `±HHMM`, `±HH:MM` with optional seconds/fractions. Naive date-times mean UTC; explicit offsets and normalized `Z`/`z` are honored. Resolve relative ages once at command start; no sliding follow window |
| `--since 0m` | A target run includes only its own completed rounds; show/follow filter by the resulting command-start cutoff |
| `--history-file` | Overrides INI `[ping] history_file`; fallback `~/.cn-ping-history.db`; relative paths resolve against the working directory; empty CLI value is invalid |
| `--history-days` | Overrides INI `[ping] history_days`; fallback 30; CLI value must be positive integer days; prune only when a target run starts |
| `-c`, `--config` | Optional additional INI layer after executable-adjacent `.cn` and `~/.cn`; does not replace those layers |
| `-t`, `--theme` | Overrides INI `[theme] theme`; CLI choices `default`, `monochrome`, `pastel`, `dark`; fallback `default` |
| `-l`, `--log-file` | Overrides INI `[logging] logfile`; fallback `~/cn.log` |
| `--log-level` | Overrides INI `[logging] level`; CLI choices `DEBUG`, `INFO`, `WARNING`, `ERROR`, `CRITICAL`; fallback `INFO` |
| TCP | None; 1–5 distinct comma-separated ASCII ports, each 1–65535; strip whitespace around each port, deduplicate and sort ascending |

Preserve only the narrow INI compatibility defined below; no cn-mon-specific configuration format, environment-variable override scheme, configuration writer, or general-purpose framework is introduced. `NO_COLOR` is honored. History storage is a local file on one machine; SQLite WAL over NFS/SMB or simultaneous use of a database from different machines is unsupported and documented.

Examples supported by the merged M5 implementation:

```sh
cn-mon 10.1.2.0/24 --for 2h --every 1m --format json
cn-mon 10.1.2.0/28 --every 30s --format json
cn-mon --file sites.txt --tcp 22,443 --since 0m --format json
cn-mon --list
cn-mon --show 1f48 --since 24h --format md
cn-mon --follow 1f48
cn-mon --show 1f48 --history-file ./lab-history.db
cn-mon --list -c ./site.cn --theme monochrome
```

### 2.1 Existing cn-tool configuration

Read the same INI grammar and small set of effective settings, implemented natively. Source authority is [main.py, lines 95–171](https://github.com/evdanil/cn-tool/blob/2e2f3c40ef6718486f1ca4477f9145029ec495fa/cn_tool/main.py#L95-L171) for discovery/logging, [config.py, lines 176–214](https://github.com/evdanil/cn-tool/blob/2e2f3c40ef6718486f1ca4477f9145029ec495fa/cn_tool/utils/config.py#L176-L214) for coercion, and [config.py, lines 317–420](https://github.com/evdanil/cn-tool/blob/2e2f3c40ef6718486f1ca4477f9145029ec495fa/cn_tool/utils/config.py#L317-L420) for parsing. The recognized keys are exactly `[ping] history_file`, `[ping] history_days`, `[theme] theme`, `[logging] logfile`, and `[logging] level`. Other valid sections/options have no effect and must not activate cn-tool services or credentials.

Configuration precedence, from lowest to highest, is built-in fallbacks; `.cn` beside the resolved native executable (follow executable symlinks); the current user's `~/.cn` (`%USERPROFILE%\\.cn` on Windows); `-c/--config PATH` when its final value is nonempty; then explicit per-setting CLI overrides. An empty final `-c` value adds no layer, even if an earlier repeated value was nonempty. This executable-adjacent layer is the native equivalent of the source launcher-adjacent `.cn`; do not search the current working directory, repository parents, `site-packages`, or packaged `sample.cn`. The source's special `python -m cn_tool` checkout detection has no native launcher counterpart. Expand `~` in an explicit configuration path; resolve a relative path against the working directory. Missing/non-file paths, including a missing explicit `-c`, contribute no layer. Files that cannot be opened are skipped as by `ConfigParser.read`; preserve values from readable layers. Never create or edit configuration files.

Use UTF-8 with optional BOM, case-sensitive section names and case-insensitive option names, `=` or `:` delimiters, whole-line `#`/`;` comments, indented value continuations, and normal `ConfigParser` `[DEFAULT]` inheritance through an existing named section. Preserve source indentation rules: section headers and option lines may have leading whitespace; deeper indentation continues the current option, and internal blank lines remain embedded newlines after joining continued values and stripping final trailing whitespace. A section header may have a trailing comment, such as `[ping] # settings`; an option before any section is a parse error. Disable interpolation: `%` and environment-variable-looking text remain literal. Do not strip inline comments from option values. Later files override matching options; duplicate named sections or duplicate options within the same section in one file are a parse error. Repeated `[DEFAULT]` headers are valid when their options are distinct; repeating a default option in that file is an error. Any INI syntax or UTF-8 decoding error in a read file discards the entire merged INI result and restores all five built-in fallbacks, after which CLI overrides still apply. Report that fallback without printing unrelated configuration values; do not silently retain a partly parsed layer. Missing files and source-compatible value fallbacks are not themselves command failures.

For `[ping]` paths and `[theme] theme`, apply the source coercion: trim outer whitespace and then outer single/double quote characters. Expand home notation in history paths as the platform does; relative history paths are relative to the process working directory, not the INI file's directory. A blank or dot-equivalent INI history path falls back to `~/.cn-ping-history.db` ([monitor lines 355–358](https://github.com/evdanil/cn-tool/blob/2e2f3c40ef6718486f1ca4477f9145029ec495fa/cn_tool/modules/ping_monitor.py#L355-L358)); an explicit CLI history path is not subject to that blank fallback. INI history days use the same quote/whitespace cleaning and Python base-10 integer conversion semantics, including signed values and valid digit separators; an invalid/blank value falls back to 30. Clamp a configured integer below 1 to 1 before pruning ([monitor lines 795–797](https://github.com/evdanil/cn-tool/blob/2e2f3c40ef6718486f1ca4477f9145029ec495fa/cn_tool/modules/ping_monitor.py#L795-L797)). The new CLI days override is stricter and rejects nonpositive values with exit 2. Preserve the configured retention value when the CLI option is omitted.

Home expansion includes named-user paths under the source platform's rules. A name whose home cannot be resolved is a settings/logging setup error, not a literal filename or a reason to substitute the current user's home. On POSIX, only `/` separates the username from its suffix; a backslash after `~` remains part of the username. Configuration loading and logger setup return these resolution errors without dumping unrelated values.

Theme names are case-sensitive after INI cleaning; an unknown INI theme emits a warning and uses `default`, while an unknown CLI theme is usage error 2. Retain the reference monitor/table styles for all four named themes from [color_schemes.py](https://github.com/evdanil/cn-tool/blob/2e2f3c40ef6718486f1ca4477f9145029ec495fa/cn_tool/utils/color_schemes.py), including distinct success/warning/error/info styles and monochrome gray shades. `NO_COLOR` and redirected-output rules still take precedence over colored output; theme choices never change structured results.

Preserve effective logging bootstrap semantics rather than applying history-path coercion to it: INI logging values come directly from the non-interpolating parser, then CLI overrides apply; an empty final `-l/--log-file` leaves the INI/default selection in effect. For example, `--log-file first.log --log-file ""` uses the INI/default path, neither `first.log` nor an empty path, matching [main.py, lines 165–171](https://github.com/evdanil/cn-tool/blob/2e2f3c40ef6718486f1ca4477f9145029ec495fa/cn_tool/main.py#L165-L171). Expand `~` in the log path but do not remove literal INI quote characters. Relative log paths use the working directory. Uppercase the level; the standard named levels and Python aliases `WARN`, `FATAL`, `NOTSET` are recognized in INI, and an unknown name falls back to `INFO`. CLI levels remain the five exact uppercase choices above. Following [logging.py, lines 18–64](https://github.com/evdanil/cn-tool/blob/2e2f3c40ef6718486f1ca4477f9145029ec495fa/cn_tool/utils/logging.py#L18-L64), create log parent directories and append UTF-8 records with local timestamp, severity and message; do not rotate or truncate an existing log. If configured directory creation/file preparation fails, use `~/cn.log`; a successful fallback does not fail the command. If the selected/fallback log cannot be opened, report the startup failure on stderr and exit 3 before opening/pruning history or probing. Log run start/end, round/probe failures and relevant diagnostics at the selected level; never dump the full INI or unrelated credential values. Read-only history commands may append to the log; they still never mutate history.

Parse and validate CLI syntax/mode combinations first, before reading stdin, configuration or logs. For target runs, read explicitly requested target input and reject unreadable/empty input before configuration or other startup work, matching [main.py, lines 1035–1049](https://github.com/evdanil/cn-tool/blob/2e2f3c40ef6718486f1ca4477f9145029ec495fa/cn_tool/main.py#L1035-L1049). Then load/resolve settings and CLI overrides, initialize logging, validate usable targets, and only then open history or probe. Reader modes never consume target stdin. Invalid CLI history/theme/log-level values exit 2 before configuration reads or file creation; no help/version path performs any of these reads/writes. Resolve effective history path and retention before any prune so a configured database is never accidentally replaced by the fallback.

### 2.2 M7 history management

The following new modes manage persistent SQLite history. A scan batch means the existing canonical request group, containing all runs with the same canonical targets and TCP ports; no additional batch identity or cache subsystem is introduced.

```text
cn-mon --list-runs REQUEST_ID [--history-file PATH] [--format FORMAT] [COMMON]
cn-mon --delete-run RUN_ID [--history-file PATH] [--format FORMAT] [COMMON]
cn-mon --delete-request REQUEST_ID [--history-file PATH] [--format FORMAT] [COMMON]
cn-mon --clear-history [--history-file PATH] [--format FORMAT] [COMMON]
```

`REQUEST_ID` uses the existing case-insensitive, uniquely matching 4–64 hexadecimal prefix of the canonical request key, not SQLite's internal numeric request ID. `RUN_ID` is the exact, case-sensitive, nonempty UTF-8 identifier stored in `run.id`; do not trim, normalize, restrict it to UUID syntax or accept a prefix. Use `--list` to discover a request, then `--list-runs REQUEST_ID --format json` to obtain its full run IDs. Existing show/list reports and their fields remain unchanged. An ID beginning with `-` can be supplied with `--delete-run=VALUE`.

An imported run ID containing NUL remains representable in JSON, but neither supported OS can carry embedded NUL in a process argument. Such a row can be removed through its request group or clear-history, subject to the same writer guard. The CLI does not decode escape strings into IDs, which would alias legitimate literal identifiers.

These modes are mutually exclusive with one another and with target/list/show/follow modes. They reject targets, `-f/--file`, `--tcp`, `--every`, `--for`, `--since` and `--history-days` before data I/O. Existing early help/version precedence, final repeated scalar values, common configuration/logging/theme options and `--history-file` selection apply. All four existing formats are accepted, defaulting to `table`. No mode reads target stdin, resolves or probes targets, starts the scheduler, acquires terminal modes, handles live keys or prunes history. Configured logging retains its existing startup/fallback/runtime-error behavior. There is no prompt or force option.

`--list-runs` reads one snapshot and lists metadata for the selected request in existing run order: start time descending, then stored row ID descending. JSON is `{"runs":[...]}`; each object has fields `id`, `request_id`, `status`, `state`, `started`, `ended`, `host`, `pid`, `rounds`. Here `request_id` is the full canonical key, `status` is the persisted status, and `state` is the existing display liveness classification. Timestamps use the existing UTC report representation; `ended` is nullable. Table, Markdown and CSV use those columns in that order. JSON and CSV preserve the exact run ID. Table and Markdown display the complete ID as a JSON string literal, with additional Markdown escaping where required; never truncate the only deletion identifier or emit its control characters directly. Other human fields retain normal terminal escaping. A uniquely matched request with no runs succeeds with `{"runs":[]}` in JSON and no section in the other formats. Pagination is internal to the single snapshot; no round/result payload is loaded for this list.

`--delete-run` removes one exact run and its rounds/results, then its request only if that request has no remaining runs. `--delete-request` removes every run in the uniquely matched group, its rounds/results, and the request itself. `--clear-history` removes all request/run/round/result rows transactionally, retaining the compatible database, schema and monotonically advancing SQLite sequences. These operations do not unlink the database or WAL/SHM files, vacuum it, reset sequences or alter retention. Section 10.4 defines the mandatory live-writer guard and transaction boundary.

Successful mutation JSON is `{"committed":true,"deleted":{"requests":N,"runs":N,"rounds":N,"results":N}}`, with actual affected-row counts. Table, Markdown and CSV report one row with columns `committed`, `requests`, `runs`, `rounds`, `results`. Clearing a missing database succeeds without creating it and reports zero counts with `committed:false`; clearing an existing empty compatible database reports zero counts with `committed:true`. No success report is emitted before commit. If commit succeeded but later connection/store cleanup fails, attempt the committed-count report, explicitly diagnose that deletion committed, and exit 3. An output failure after commit also exits 3 and must not imply rollback or suggest that blindly retrying will undo the deletion. Section 8's ordinary closed-downstream-pipe exception retains the existing operation status, including exit 3 for an earlier cleanup failure. Other failures emit a diagnostic and no result report.

| Code | New history-management modes |
| --- | --- |
| 0 | Successful list or deletion; includes a selected request with zero runs and clearing missing/empty history |
| 1 | Missing database or no matching run/request for list-runs/delete-run/delete-request |
| 2 | Invalid options/ID/configuration or ambiguous request prefix |
| 3 | Protected selected run, incompatible/malformed history, lock/storage/output failure, or logging initialization failure |
| 130 | Ctrl+C, using the existing one-shot list/show hard-cancellation behavior |

These new-mode statuses do not change existing target/list/show/follow statuses. A reported cancellation before commit rolls back the operation; first Ctrl+C cancels the hard context and exits promptly as for one-shot readers. SQLite's atomic transaction remains authoritative if interruption races commit: an already committed deletion is not undone, and an interrupted command without a success report is not proof of rollback. No terminal restoration or new signal phase is needed.

### 2.3 M10 rerun a saved batch (merged)

```text
cn-mon --rerun REQUEST_ID [--every INTERVAL] [--for DURATION] [--since WHEN]
       [--history-file PATH] [--history-days DAYS] [--format FORMAT] [COMMON]
```

`REQUEST_ID` is the case-insensitive, uniquely matching 4–64 hexadecimal request-key prefix discovered by `--list`. Select that batch's newest stored run by `started DESC, rowid DESC`, including active, lost, failed, stopped and zero-round runs. Its interval and configured duration supply the schedule. An exact run ID from `--list-runs` is outside this command's scope. Repeated `--rerun` uses the final value. It is mutually exclusive with all existing history modes and rejects positional targets, `-`, `--file`/`-f` and `--tcp` before data I/O. Existing help/version precedence and option syntax apply; there is no short alias or prompt.

Reproduce the saved canonical targets, TCP ports, expanded hosts in their stored order and request key. Resolve DNS afresh through the normal probes. Do not recollect input, reopen the original file, read stdin for targets, re-expand networks or copy past observations. The newest run's optional source label is retained only as provenance. A scoped IPv6 address keeps its saved zone; interface names/indices must be meaningful on the machine running the command, and ordinary probe failures apply if they are not.

History schema 1 preserves the effective interval and optional absolute configured end, but not the original CLI, INI, output preferences or input-file contents. Recover configured duration as `until - started`, never as `ended - started` or from completed rounds. A stopped run thus reruns its configured duration from a fresh start. Omitted interval and end retain their single-round meanings; a non-null interval with no end repeats until stopped. Positive representable historical values, including fractional seconds and intervals below today's CLI minimum, are replayed numerically. SQLite REAL timestamps impose their existing precision limit; replay preserves the decoded duration rather than claiming recovery of precision absent from storage.

| Flags in this invocation | Resulting schedule |
| --- | --- |
| Neither | Saved interval and configured duration |
| `--every` only | Explicit interval, saved configured duration |
| `--for` only | Explicit duration, saved interval; use the existing implicit 60-second interval if the saved interval is absent |
| Both | Both explicit values |

Explicit values use the existing duration grammar and 10-second minima. Apply `--for >= --every` only when **both flags occur explicitly in this invocation**. Restored values are not synthetic CLI flags: saved interval 60s/duration 30s is valid, and changing only the interval to exceed an inherited duration leaves one eligible immediate round. There is no new flag for clearing an inherited interval or duration. Invalid stored schedule values fail before overrides are applied.

The current invocation supplies INI/CLI settings, selected history file, retention, logging, theme, format and `--since`; none is inherited from the old process. Omitted `--since` includes retained earlier rounds, and `--since 0m` includes only the new run, as for an ordinary target command. Start a new UUID, start/end schedule, hostname/PID and heartbeat under the same canonical batch. An active source run continues independently. The existing runner owns native probes, reporting, terminal controls, signals, persistence, pruning and reachability exits.

Resolve and validate the saved request and newest run in one read-only snapshot of the configured history, then close it before opening a logger or terminal, generating a new run or opening writer/probe resources. Configuration may be read to locate history. Failure performs no target-input reads, probing, history creation, pruning or run insertion. Missing history, no match, ambiguity, no saved run or an empty saved batch exits 2; malformed/unreplayable stored data and read/transaction/close failures exit 3. A cleanup error retains exit 3 even if selection also failed. Pre-run Ctrl+C follows the existing exit-130 behavior. Selection failures leave stdout empty and diagnose on stderr. Once started, ordinary target-run exit precedence applies, including exit 1 only for actual reachability results.

Selection does not modify the source run or its results. After selection, ordinary startup retention may prune expired history; concurrent deletion or a later run cannot change the detached replay parameters. If the batch is deleted before insertion, normal writer behavior can recreate the same key with a new internal request row ID. Do not hold a reader transaction across writer startup or introduce a read/write lock spanning the two phases.

## 3. Targets and stable request identity

Read positional objects in order, then `-f/--file`; a positional `-` is replaced at that position by stdin objects, and `-f -` or `--file -` appends them. Both option spellings have identical semantics and share one value: `--file first.txt -f second.txt` reads only `second.txt`. Read stdin at most once and only when explicitly requested. Files/stdin are UTF-8 with optional leading BOM, split on whitespace after removing `#` comments and blank lines. Positional arguments are individual tokens; a quoted comma/space list retains usable IP literals and ignores non-address components if at least one usable IP remains, matching the reference parser's address-list behavior. Deduplicate input tokens while preserving first occurrence for diagnostics. Unreadable/non-UTF-8 input is usage error 2.

Accept IPv4/IPv6 literals, CIDRs with host bits normalized, IPv4 dotted netmasks/hostmasks, and ASCII DNS names (single-label names included). For example, `10.1.2.5/255.255.255.0` and `10.1.2.5/0.0.0.255` both normalize to `10.1.2.0/24`, with the same expansion and request identity as that CIDR; malformed/noncontiguous masks are rejected. Names follow the reference length/label validation: total typed length 2–252, optional trailing dot, labels 1–63 ASCII letters/digits/hyphens with alphanumeric ends. An invalid all-digits-and-dots token is not a hostname. Reject IPv4-mapped IPv6 with a diagnostic giving the IPv4 form. Link-local IPv6 requires a nonempty interface zone on a single address, e.g. `fe80::1%eth0` or a Windows interface index; link-local subnets and zoned CIDRs are rejected. Preserve zone spelling in canonical identity and pass it to both probes.

Each IPv4 network may contain at most 65,536 addresses (`/16` or narrower); each IPv6 network at most 65,536 (`/112` or narrower). These are **per-network** limits; there is no new aggregate ICMP host limit. With TCP, admission is greedy in collected input order, **before canonicalization**: count distinct host strings returned by the source parser for already accepted tokens plus the candidate. A token that would exceed 1,024 is rejected whole, while later smaller tokens may fit. Thus differently cased names can occupy separate admission slots even though later canonicalization merges them. Preserve this source behavior; do not sort or lowercase candidates before admission or reject the whole request because its raw input total is too large. Reordering near the cap can change the accepted target set. Rejected tokens are recorded in `not_found`; any remaining accepted targets are still run, with exit precedence in section 9. The resulting canonical host list also never exceeds 1,024.

Source authority for the two phases is [bulk_ping.py, lines 445–455](https://github.com/evdanil/cn-tool/blob/2e2f3c40ef6718486f1ca4477f9145029ec495fa/cn_tool/modules/bulk_ping.py#L445-L455), which applies the limit in input order using parser-returned strings, followed by [ping_monitor.py, lines 313–350](https://github.com/evdanil/cn-tool/blob/2e2f3c40ef6718486f1ca4477f9145029ec495fa/cn_tool/modules/ping_monitor.py#L313-L350), which canonicalizes only non-refused tokens and re-expands those sorted tokens. The source's broad order-invariance docstring does not override its admission behavior.

Expansion matches the reference Python `ipaddress` host semantics: ordinary IPv4 excludes network/broadcast, `/31` includes both, `/32` the one address; ordinary IPv6 excludes subnet-router anycast, `/127` includes both, `/128` the one address. Subnet expansion additionally excludes loopback, multicast and reserved addresses; a single literal or host-sized prefix is used as supplied, including loopback. Private/documentation addresses are not excluded merely for being nonpublic. The compatibility fixtures freeze the reference exclusions rather than substituting Go's `IsGlobalUnicast`. Characterization baseline is Python 3.12.14: IPv4 reserved `240.0.0.0/4`; IPv6 reserved `::/8`, `100::/8`, `200::/7`, `400::/6`, `800::/5`, `1000::/4`, `4000::/3`, `6000::/3`, `8000::/3`, `a000::/3`, `c000::/3`, `e000::/4`, `f000::/5`, `f800::/6`, `fe00::/9`.

After admission, build identity from the accepted targets in this order:

1. Normalize accepted CIDRs to network/prefix, addresses to compressed canonical spelling with zone unchanged, and names to lowercase while preserving a trailing dot.
2. Deduplicate and lexicographically sort canonical tokens, including accepted tokens whose hosts overlap another token. Sort unique TCP ports numerically.
3. Re-expand these sorted tokens; keep the first occurrence of each host. A subnet's hosts are in numeric address order. DNS answers are **not** part of this host list or key.
4. SHA-256 the UTF-8 encoding of Python-compatible compact JSON `[tokens, ports, hosts]`, with no spaces or newline. Match Python `json.dumps(..., separators=(",", ":"))` escaping, including ASCII escaping of any non-ASCII zone text; do not use Go's default HTML escaping. The full lowercase 64-hex key is stored; display its first 8 characters.

Golden example: targets `10.1.2.5/30 Web01.EXAMPLE.NET`, ports `443,22`, produce bytes

```json
[["10.1.2.4/30","web01.example.net"],[22,443],["10.1.2.5","10.1.2.6","web01.example.net"]]
```

and key `1f48106300cacd9a109b91a8ddc79c64920f0feb58e7840459ff80b639b7cb46` (display `1f481063`). The identity invariant applies to **equal accepted canonical target sets, TCP ports and deterministic expanded host lists**: reorderings, duplicates and equivalent spellings with those same accepted inputs produce the same key. Reordering raw input near the TCP cap can change admission and therefore the key. Different accepted canonical targets, ports, or expansion distinguish requests; file path is not part of identity, while file content order matters only if it changes admission. A stored request always retains its stored host list and key when read, even if a future expansion policy changes.

Required TCP admission/identity fixtures (all with `--tcp 443`; `10.0.0.0/22` expands to 1,022 hosts):

| Raw collected targets, in order | Accepted canonical targets | Rejected raw target | Final hosts / displayed ID |
| --- | --- | --- | --- |
| `10.0.0.0/22 10.0.4.1 10.0.4.2` | `10.0.0.0/22`, `10.0.4.1`, `10.0.4.2` | None | 1,024 / `23fa8108` |
| `10.0.4.2 10.0.4.1 10.0.0.0/22` | Same accepted set as preceding row | None | 1,024 / `23fa8108` |
| `10.0.0.0/22 10.0.4.1 10.0.4.2 10.0.4.3` | `10.0.0.0/22`, `10.0.4.1`, `10.0.4.2` | `10.0.4.3` | 1,024 / `23fa8108` |
| `10.0.4.3 10.0.4.2 10.0.4.1 10.0.0.0/22` | `10.0.4.1`, `10.0.4.2`, `10.0.4.3` | `10.0.0.0/22` | 3 / `3242fd9d` |
| `10.0.0.0/22 HOST01 host01 HOST02` | `10.0.0.0/22`, `host01` | `HOST02` | 1,023; case variants consumed two admission slots |

The two numeric fixture keys are `23fa8108eae6b8e943340ecdf476aefc3572eee18971523ee8822e74bfae63dd` and `3242fd9d5a9b6a2ef1e306ef4dcab9cb491a8ce1bc3e1a70f552d52b9c0d8615`. Rows with rejected targets continue accepted probes and normally exit 2, subject to execution/history failure precedence. These fixtures preserve source-order admission rather than imposing a new order-independent selection policy.

Resolve each name again in every round, using the first OS-resolver IPv4 answer, or first IPv6 answer only when no IPv4 answer is available. Resolve a name once per round and use that address for both ICMP and TCP. An unresolved name produces `no such host`, observation `?`, and blank TCP fields. A run's final `not_found` includes names never resolved during that run; one successful resolution removes a name from that list even if it later fails. DNS completion order must not reorder hosts. Cancellation must release the caller promptly and join application-owned lookup workers without keeping the process alive. Go's native Windows resolver may finish `GetAddrInfoW` after returning a context error; stopping that underlying OS operation is not promised.

## 4. Native probes and result semantics

### Selected implementation

Use the maintained `github.com/prometheus-community/pro-bing` ICMP core, pinned initially to reviewed commit `bcbba8f702ab2b7a4a62697f10b27491d50b520b`, in raw ICMP mode on both supported platforms. Keep a narrowly scoped, checked-in adaptation under `third_party/pro-bing` with a local module replacement, upstream revision, retained licenses/notices, and a documented patch list. Retain upstream core test assertions, adapt public-DNS assertions to injected/literal fixtures, and move real socket/privilege cases to the controlled opt-in native suite. Run that module's tests explicitly; ordinary tests remain offline and nonprivileged. Do not import its unrelated HTTP/example programs or add a generic backend/plugin framework.

Unmodified pro-bing is insufficient: it drops non-echo messages, its Linux BPF filter accepts only echo replies, and Linux's receive buffer is sized for echo data. `OnRecvError` describes socket failures, not ICMP error packets. The bounded extension must:

- Accept a structured correlated ICMP-error callback carrying family, type, code, sender, quoted destination, echo ID/sequence, and receive time.
- Parse only received bytes, provide sufficient bounded receive space for error quotations (65,535-byte buffer per active pinger is acceptable), and reject malformed/truncated quotations safely. A buffer handed to another goroutine cannot be reused until ownership returns; use bounded ownership/copying rather than racing reads against parsing.
- Preserve echo-ID filtering while admitting the relevant error types through Linux BPF; perform full error correlation in Go. No filter failure may silently suppress required errors.
- Recognize IPv4 destination-unreachable, time-exceeded and parameter-problem, and IPv6 destination-unreachable, packet-too-big, time-exceeded and parameter-problem. Redirects and unrelated control messages are ignored.
- Validate the quoted IP family, destination, protocol, ICMP echo request type, active ID and sequence. Walk supported IPv6 extension headers with strict bounds; incomplete or unplaceable packets stay unclassified. Use the payload tracking token whenever enough quoted bytes are available, but support the standard minimum quotation containing only the original IP and echo headers.
- Allocate unique active echo identifiers within the process, correlate by destination/zone and sequence, and ignore stale/duplicate/unrelated packets. Preserve pro-bing's stronger token matching for echo replies. ICMP observations are evidence of network responses, not authenticated identity.
- Correlate errors against a bounded two-entry successful-send ledger retained until the pinger finishes, independently of pending echo replies. An error may still correlate after its echo arrived while that run remains active. Emit at most one target-attributed and one intermediate-attributed error per sent request; an earlier intermediate error cannot hide a later target error. Serialize receive-side correlation with successful-send publication before `OnSend`, preventing a fast response from observing a missing sent entry. Release any ledger lock before callbacks; socket interruption must not need that lock.
- Retain error observations until the probe ends; an error must not finish the probe before a possible echo reply, which takes precedence.
- Interrupt blocked socket reads/writes on both parent cancellation and the host deadline before joining workers. Upstream `Stop` followed by socket closure only after the join is insufficient for a blocked `WriteTo`. Remove/bound send retries that bypass cancellation; do not enable broadcast as error recovery. Intentional stop-induced close/deadline errors and routine receive polling timeouts are not execution failures; independent permission/resource failures remain errors. No callbacks occur after the pinger returns or its identifier is released.

`github.com/digineo/go-ping` commit `d7f7779720587941d094ecae870cab3943bae156` is a useful MIT-licensed reference for quoted echo parsing, not the selected runtime dependency: its current error path handles only destination-unreachable and discards the sender. Do not substitute either library unmodified or write a new ICMP stack. If the bounded pro-bing adaptation cannot pass the native platform gate, revise this architecture in a reviewed PR before proceeding; an unproved fallback is not part of this specification.

### Execution and classification

Resolve all round names first, with at most 100 concurrent application lookup calls, then probe resolved hosts in deterministic batches of at most 100. The run engine records each batch's sample timestamp before probing. Each resolvable host sends two echo requests, nominally at 0 and 1 seconds, within a 3-second host deadline. Count distinct replies, not duplicates. Timeout values are fixed in v1. Run TCP attempts concurrently with ICMP, with at most 100 concurrent TCP connects and a 3-second deadline per attempt starting when its worker begins. Queued TCP attempts may take multiple waves; ICMP completion must not cancel them, and the batch has no shared 3-second deadline. Close connections immediately after connect; transmit no application data. All probes and queued work accept cancellation and join application-owned workers/sockets; no unbounded worker, socket or result queue is permitted. The Windows native-DNS limitation above does not weaken prompt caller cancellation.

| ICMP result | Meaning |
| --- | --- |
| `OK` | Two distinct echo requests received replies |
| `OK (1/2 replies)` | One replied before the deadline |
| `REJECTED` | No echo reply; at least one validated ICMP error came from the probed address |
| `UNREACHABLE` | No reply or target-sourced error; a validated ICMP error came from another address |
| `NO ROUTE` | Local IPv6 route/family unavailable; a reachability outcome, not a failed probe |
| `NO RESPONSE` | No attributable network response by deadline, including an error lacking enough information to attribute |
| `ERROR` | The probe could not execute correctly, including permission/resource/socket failures; diagnostic on stderr |
| `not run` | Retained for reading compatible legacy history; native execution does not silently skip ICMP |

Normalize sender/target addresses before comparing; scope identifiers disambiguate link-local addresses. Target-sourced error wins over intermediate errors; any echo reply wins over either. A local IPv4 host/network-unreachable send outcome is `UNREACHABLE`; IPv6 uses `NO ROUTE`. An ICMP packet is classified from its data and sender, never error-message language. Native Windows can report attributed IPv6 errors when received; if its API/socket provides no attributable packet, do not fabricate `R` or `U`.

TCP states are lowercase: `open` (connected), `closed` (connection refused/reset), `timeout`, `unreachable` (host/network/family unavailable), `error` (other execution failure). `--tcp` success requires **open** in some included round; closed still proves a responding host.

Observation precedence per host is: echo reply `!`; target rejection `R`; any TCP open/closed `T`; ICMP execution error `E`; unresolved name `?`; ICMP unreachable/no-route `U`; otherwise `.`. The separate execution-failure flag still records ICMP `ERROR` or any TCP `error`, even when another response makes the host seen. TCP-only errors do not turn a silent ICMP observation into `E` if the reference would record `.`.

### Runtime permissions

Do not elevate, install capabilities, change sysctls, firewall rules, or network configuration automatically. Linux raw ICMP normally needs `CAP_NET_RAW` on the installed binary or an authorized elevated process; enabling `ping_group_range` alone does not grant raw sockets. A datagram-only fallback is excluded because it has not established equivalent error attribution. Windows attempts the supported raw ICMP path; upstream reports non-elevated success on some Windows versions, which is not evidence for every target system. Publish tested ordinary/elevated-user outcomes and a concrete remedy for access denial.

Permission denial is reported clearly and yields exit 3 as a probe execution failure; it must not be labeled `NO RESPONSE`. TCP may still run and its results are printed, but cannot hide that failure. IPv6 unavailable is `NO ROUTE`, not a raw-socket permission diagnostic. Readers need no ICMP privilege. A real native Linux and Windows runtime gate, including denial behavior, is required before claiming support; cross-compilation alone is insufficient.

The completed-round stderr diagnostic in section 10.1 preserves the underlying execution cause, including errors masked by a successful reply. When any ICMP execution error in that round identifies access denial, append the platform remedy: on Linux, grant `CAP_NET_RAW` to the installed binary or use an authorized elevated process; on Windows, use an authorized elevated Administrator terminal. Identify permission errors structurally through their wrapped causes (`os.ErrPermission`, and Windows Winsock `WSAEACCES`/10013), never localized message matching. This advice performs no privilege or system change.

M2 component evidence: [Linux](https://github.com/evdanil/gotest/actions/runs/38037349729/job/114170451322) and [Windows](https://github.com/evdanil/gotest/actions/runs/38037349729/job/114170451856) native jobs passed for commit `80541a7` in PR #9. The controlled harnesses exercised IPv4/IPv6 echo, target/intermediate error delivery and correlation, and platform privilege contexts through actual sockets. Windows error injection verifies correlated callbacks after the first echo and before the second, preserving echo precedence; it does not claim an echo-suppressed Windows rejection-only aggregate. [Run logs and artifacts](https://github.com/evdanil/gotest/actions/runs/38037349729) retain the observed privilege outcomes. Command output/exit-code integration passed the M4 gate in [PR #11](https://github.com/evdanil/gotest/pull/11); M2 evidence remains evidence of probe APIs. Release-binary qualification remains M6 work.

## 5. Rounds, scheduling and shutdown

One completed round contains one observation for every canonical host, the full latest rows in memory, details for seen hosts, the round start, and per-seen-host batch timestamps. Missing DNS is a completed observation. A cancelled partial round contributes no observations, summary counts, online periods, execution-failure flag, or persisted round. Live pending cells are provisional only. Once all hosts have completed, a subsequent stop during committing keeps that completed round.

Take the run origin before reading earlier history. Repeating slots are `origin + n*interval`. Begin the first eligible round immediately after setup if the end has not passed; subsequent round starts remain on those slots. After overrun, skip covered slots, never run overlapping/catch-up rounds, and report skipped slots in live/status output. A round ending exactly on a slot may use that slot; otherwise use the next slot. No new round begins at or beyond `origin + duration`; a round already in progress may finish past the end. A warning estimates a slow sweep before the first round using batches and the probe deadlines, and actual skipped slots remain observable.

Inject wall and monotonic clocks. Run timestamps never move backwards: absorb a wall-clock rollback using monotonic elapsed time. Preserve forward wall-clock jumps/suspend gaps and end a duration-limited run that resumes past its end. Heartbeats use real UTC wall time, because other processes read them. Scheduled waiting and key polling remain responsive (at most 500 ms between control checks); no wall-clock change causes an indefinite wait. DNS/probe work writes heartbeats at least every 10 seconds while busy; idle scheduled runs remain live under the interval-based stale threshold.

After a scheduled wait, retain its selected slot while corrected run time is before that slot plus one interval; ordinary timer lateness must not discard it. Once the following slot has arrived, reselect the first aligned slot at or after the current time and count only newly skipped slots before the exclusive end. Re-evaluate at the single actual-start reading, wait again when the selected slot is still future, and do not probe after the duration cutoff; a wake at the cutoff still counts eligible missed slots before it. The first immediate round remains exempt from this reselection. Thus a wait for +10 seconds resumed at +25 with a 10-second interval selects +30 and skips +10/+20; a wake at +10 plus ordinary jitter retains +10, and a wake at +20 selects +20 while skipping +10. This native correction closes the source's overdue-wait edge case: pinned Python `_loop`/`_wait` retain the old selected slot after a multi-slot suspension. It preserves anchored scheduling and avoids catch-up work without an arbitrary lateness tolerance.

First Ctrl+C or `q` cancels ongoing work, discards an incomplete round, restores the terminal subject to section 8's foreground ownership rules, finalizes the run as stopped, and prints the summary of completed rounds. The first Ctrl+C is a graceful stop, not automatically exit 130. A second Ctrl+C exits 130 promptly without waiting for network operations or SQLite lock timeouts; best-effort cleanup must not delay it. `q` does not increment the Ctrl+C count. During a Linux run, SIGTERM and non-ignored SIGHUP perform graceful stop. Preserve inherited ignored SIGHUP/SIGINT, including `nohup` behavior. A killed process/closed Windows console may leave a running row, later displayed as lost.

Approved standard-Go compatibility exception: an inherited `SIG_IGN` disposition for Linux SIGTERM is not preserved. The pinned Go runtime replaces it before application initialization; the supported `os/signal` inherited-ignore guarantee covers SIGHUP/SIGINT, not SIGTERM. Consequently, before the running phase SIGTERM terminates the process even when the launcher ignored it; during the running phase it requests the usual graceful stop. Pre-run SIGHUP retains default termination unless inherited ignored, and inherited ignored SIGINT remains ignored. Use the standard Go toolchain without a launcher, runtime-internal access or a custom runtime patch. This exception does not weaken the retained HUP/INT or normal TERM behavior.

At target-run start, read one consistent snapshot of retained same-request rounds with starts before the run origin and on/after the since cutoff. Those are `earlier_rounds`; do not continuously import other processes' new rounds into an active writer's summary. Then fold this run's completed rounds. If a one-round run finishes, include its start when inferring cron spacing; if it stops before completing, exclude that hypothetical sample. Reading failure does not use a half-folded earlier snapshot: continue with this run in memory and exit 3.

## 6. Seen state and sampled online periods

Only `!`, `R`, and `T` mean seen. Display the current seen character; otherwise a host seen earlier in the selected window is `x`. A never-seen host becomes sticky `U` after unreachable/no-route and stays `U` through later silence/error/name failure until it is seen. Other never-seen hosts display `E`, `?`, `.` or blank before probing. Partially replying `!` is highlighted in the warning color; a changed seen/not-seen state is highlighted separately.

`seen` contains every host seen at least once, in canonical host order, with first/last seen sample time, seen-round count, total included round count, latest result, last known responding address, and every sampled online period. If no longer seen, latest is the current observation's result and its TCP summary fields are blank, rather than stale port states. `answering` counts hosts seen in the latest completed round, and includes `R`/`T`.

A period starts with a seen sample and ends at the last seen sample before a not-seen round or a coverage gap. These are **sampled periods**, not a claim of uninterrupted availability or an uptime percentage. A gap exists when round starts are more than 2.5 times the larger expected spacing of adjacent rounds apart; equality does not split. For interval-less single-round runs, infer spacing from the lower median of consecutive sorted start gaps only after at least three samples. With no expected spacing, separate samples are separate periods. A gap splits periods even when both bounding samples see the host.

Sort stored rounds by `(started, round_id)`, because concurrent runs can finish out of order. Clamp successive per-host sample times so last-seen and period endpoints cannot move backwards when an earlier-started slow round sampled later. Model memory holds aggregate host state and required online periods, not all prior rounds; storage reads are paginated. Unavoidable memory is proportional to hosts plus reported periods, not a claim of fixed memory regardless of history.

Compute inferred single-round spacing from the selected round starts before folding and supply that fixed value to the tracker. If new samples or pruning change the inferred spacing, rebuild from the selected rounds; the aggregate tracker does not retain all starts or retroactively repair period boundaries. A tracker folds already ordered complete rounds. Provisional observations affect its display methods only, without committing counts, times or periods.

An online period has `to: null` only when the host was seen in the latest included round **and the run owning that round is currently live**. Another live run alone is insufficient. A completed writer's final output closes its own periods at their last sample; show/follow can report open periods of a live request.

## 7. SQLite history and read modes

Use `github.com/ncruces/go-sqlite3 v0.35.6` through `database/sql`, with `github.com/ncruces/go-sqlite3-wasm/v6 v6.3.35304` supplying SQLite 3.53.4 as generated Go. Import the driver's `driver` package, which registers `sqlite3`; this version needs no separate embed import, external Wasm runtime, C compiler or installed SQLite library. The integrated module graph is pinned in `go.mod`/`go.sum`. The driver requires Go 1.26.0; development/CI remains pinned to Go 1.27.2.

The driver selection replaces the initial modernc proposal after source/license review: the current modernc/libc declared generator graph contains MPL-2.0 `hashicorp/golang-lru/v2`, even though that module is not in its ordinary runtime. Lazy module pruning does not waive the no-copyleft-dependency rule. The selected ncruces driver is MIT, its generated module's own code is MIT-0 with SQLite public-domain sources and permissive shims, and the integrated runtime and declared module graphs were inventoried for permissive licenses in [third-party notices](../THIRD_PARTY_NOTICES.md). Exact modules retain normal checksum verification. Official hosted `govulncheck` 1.8.0 symbol scans against `https://vuln.go.dev` passed at `6b1dc93` for `./cmd/...` and `./internal/...` on [linux/amd64](https://github.com/evdanil/gotest/actions/runs/38043224090/job/114187473308) and [windows/amd64](https://github.com/evdanil/gotest/actions/runs/38043224090/job/114187473356). These are revision/target/scope-specific checks, not a claim that later changes or all code are vulnerability-free. Retain original-source notices and rerun the current-commit checks before merge.

Use the driver's default interoperable VFS, with no alternate flock/dot-lock mode, custom VFS, immutable access, encryption or optional extension. Concurrent Python/Go WAL interoperability on both native platforms is required before compatibility is claimed. Do not add code-generation tools or runtime dependencies to consume the already generated module. Keep one physical connection per Store and measure memory/cancellation behavior rather than assuming an upstream platform claim proves support.

Preserve the reference **schema version 1 read/write contract** and effective INI/CLI history settings from section 2.1, with fallback `~/.cn-ping-history.db`. Existing history at the configured path is used without a new flag; `--history-file` overrides it when desired. No import command, automatic migration, search for other database paths, or overwrite is required. Continuing an old database is supported only after interoperability tests pass; stored keys and positional host lists are never recomputed or rewritten.

The implemented M3 boundary is `internal/history`; it imports neither the monitor model nor the command package. Callers supply paths, times, run identifiers and lifecycle metadata explicitly. Its component APIs are:

| Boundary | API and responsibility |
| --- | --- |
| Opening | `OpenWriter`, `OpenReader`, idempotent `Close`; an absent reader file returns `(nil, nil)` |
| Writes | `BeginRun` atomically records request/run creation; `RecordRound` atomically records one validated complete round and its seen details; `Heartbeat` and `EndRun` update explicit lifecycle metadata |
| Retention | `Prune` accepts arbitrary-precision days, rejects nil, clamps values below one without mutating the caller's value, and returns removed-round count |
| Reads | `View` provides a transaction-bound `Snapshot` with `Find`, `Request`, `Requests`, `Runs`, `Window`, `Rounds` and `Spacing`; returned values outlive the callback, the Snapshot does not |
| Liveness | `LocalPIDState` distinguishes alive/dead/unknown; `RunState` computes lost state without database writes, including overflow-safe long-interval comparisons |

Snapshots capture a global round-ID high-water mark and use opaque cursors retaining original SQLite REAL sort keys. Page limits default/cap at 500; round pages additionally bound the host-detail budget. `Spacing` calculates the selected lower median in SQLite rather than retaining every start in Go. Nullable times/intervals and SQL NULL versus `{}` TCP details stay distinct. Transactions use context-aware manual BEGIN/COMMIT, with an independent bounded rollback on failure; failed cleanup discards the connection and fails the Store. M3 provides store APIs. Merged M4 composes startup pruning, in-memory fallback and exit 3 with those APIs; M5 adds operational readers under section 10.2. The earlier M3 component evidence and later command evidence remain separate.

M3 native evidence at final PR head `f0825828ad9e3b8176c80f55d7d449ccd2707cc2` in [PR #10 run 38045035366](https://github.com/evdanil/gotest/actions/runs/38045035366):

| Native platform | Go / CGO | Go driver / SQLite | Python / its SQLite | Required history gate |
| --- | --- | --- | --- | --- |
| Linux amd64 | 1.27.2 / disabled | 0.35.6 / 3.53.4 | 3.12.14 / 3.45.1 | [All seven passed](https://github.com/evdanil/gotest/actions/runs/38045035366/job/114192741491) |
| Windows amd64 | 1.27.2 / disabled | 0.35.6 / 3.53.4 | 3.12.10 / 3.49.1 | [All seven passed](https://github.com/evdanil/gotest/actions/runs/38045035366/job/114192741369) |

Both jobs verified cn-tool commit `2e2f3c40ef6718486f1ca4477f9145029ec495fa` and the exact `ping_store.py`/`ping_history.py` SHA-256 values recorded in [fixture provenance](../internal/history/testdata/python-source-v1/provenance.json); required PASS/result markers exclude silent skips. The tests establish both Python→Go→Python and Go→Python→Go reads/appends; concurrent default-VFS WAL access and pruning; stable held Go/Python snapshots across the opposite writer's commit/prune, followed by updated fresh reads; and killed-writer recovery in both languages. The killed Go helper left no partial round and preserved committed rows; the killed Python helper's uncommitted status update left the prior completed value intact. Fixture generation and the separate local Linux run used Python 3.12.14/SQLite 3.53.1; hosted Linux's linked SQLite differs as shown above. Fixture/semantic provenance stays unchanged, and platform-specific test interpreters are not product runtime dependencies.

The source-generated fixture and manifests regenerated byte-identically. Configuration/Store composition passed for fallback, `.cn`, explicit `-c` and CLI-selected paths, append/readback and settings-driven 90/30-day retention. Schema-v2/corrupt-file preservation, ambiguous/mixed-case prefixes, start-order/tied-start pagination and full-prune/same-key ID non-reuse also passed component review. Complete summary metadata validation, pre-delete status/scalar guards and the requested-request running-only status guard passed local regression/race/vet checks and independent review. Pruning rejects invalid interpreted values with zero deletions and preserves all raw history tables, including unrelated eligible history; running-only reads reject malformed statuses even outside the cursor without inspecting unrelated requests. Positive fixtures retain SQLite affinity behavior, NULL/long intervals, the exact cutoff boundary and huge-retention scope. All 12 final-head checks and both reviews completed before M3 merged; the final [Linux history](https://github.com/evdanil/gotest/actions/runs/38045035366/job/114192741491) and [Windows history](https://github.com/evdanil/gotest/actions/runs/38045035366/job/114192741369) gates passed at `f0825828ad9e3b8176c80f55d7d449ccd2707cc2`. None of these component results activates operational commands.

Logical schema (column names and meanings are compatibility requirements):

| Table | Columns |
| --- | --- |
| `meta` | `key` primary key, `value`; `schema_version = '1'` |
| `request` | autoincrement integer `id`; unique `key`; JSON `targets`, `tcp_ports`, `hosts` |
| `run` | text UUID `id`; `request_id`; nullable `source`; `started`, nullable `interval`, nullable `until`; `host`, `pid`, `heartbeat`; nullable `round_seconds`, nullable `ended`; `status` |
| `round` | autoincrement integer `id`; `run_id`; `started`; positional `observations`; `answered` |
| `result` | `(round_id, host)` primary key, without rowid; `address`, sample timestamp `t`, `result`, nullable JSON `tcp` |

Retain foreign keys with delete cascades, indexes `run_by_request(request_id, started)` and `round_by_run(run_id, started)`, epoch-second REAL timestamps, decimal-string TCP map keys, reference observation alphabet `!RTU.E?`, and statuses `running`, `finished`, `stopped`, `failed`. `answered` is the number of seen detail rows. A run stores its input file basename in `source` where applicable. Request/run creation is one transaction; each completed round plus all seen result rows is one transaction. Preserve autoincrement non-reuse of round IDs. No observation string may be shorter/longer than its stored host list; no incomplete round is committed.

Enable foreign keys and a bounded 10-second busy wait on every physical connection, including replacements. Set/verify WAL for writers only after schema validation/creation; never put the WAL change in an opening DSN that runs before validation. Writers create missing parent directories and a new schema only in an empty database, after confirming no foreign tables/schema exist. Unknown, malformed, corrupt or newer schemas fail safely without journal-mode changes, migration, pruning, or other mutation. Use correctly escaped filename URIs, parameterized SQL and short transactions. Validate decoded data before indexing/allocating; malformed rows produce a history error rather than a panic or partial summary. Request arrays, including no-TCP ports, store JSON arrays (`[]` when empty), distinct from nullable result TCP. Preserve raw REAL cursor keys rather than reconstructing them from display times. Ensure cancellation interrupts lock waits/reads and preserves causal context errors; a successful COMMIT remains committed even if cancellation follows immediately, while a commit I/O failure must not trigger automatic duplicate retries.

Request-summary queries validate each selected request's complete metadata, including its hosts array, before returning a host-free summary. Malformed hosts such as `[1]` are errors even when no round is loaded. Decode/validate one row's hosts at a time and discard them; do not retain every listed request's hosts in the page.

Before applying the running-only filter in `Snapshot.Runs`, validate every stored status for the requested request, including older/off-cursor runs, against `running`, `finished`, `stopped`, `failed`; unknown/blank/persisted `lost` values fail without mutation, while an unknown positive request ID remains empty and unrelated requests are not inspected.

The stored-status enum `running`, `finished`, `stopped`, `failed` applies to reads and pruning. Before any deletion, `Prune` validates stored statuses in the same write transaction, using a bounded read. Unknown or blank values, including a persisted `lost`, cause rollback with zero removed rounds and a history error; all related and unrelated history remains unchanged. `lost` is computed reader state, never a valid stored status.

When round pruning is enabled, that pre-delete transaction also validates every round's `started` as a non-NULL numeric SQLite value within the existing finite epoch bounds. It then validates heartbeat and nullable interval for running runs with no round surviving `started >= cutoff`: heartbeat uses the same epoch bounds, and non-NULL intervals use the existing finite, nonnegative duration bounds. Stored TEXT/BLOB values are invalid even if Scan could coerce them; a numeric string converted to a numeric value by SQLite column affinity is valid if it passes those bounds. Stream one row at a time and reuse the scalar codecs; malformed operands return zero removals plus an error without deleting any history. Huge retention that disables round pruning skips these unused scalar checks. This gate covers pruning operands, not unrelated run/result/request fields.

Prune rounds older than the retention cutoff at run startup, cascading results. Delete runs with no remaining rounds when finalized or truly old/stale; preserve live runs awaiting their first/next round, including intervals longer than retention. Delete requests only when no runs remain. A writer encountering its first history read/write/finalization failure closes that store, reports the failure once, continues with in-memory rounds, prints the available summary, and exits 3; never claim unsaved data was persisted.

Readers (`--list`, `--show`, `--follow`) open read-only and never create the database or parent directories, prune, update run status, set WAL, or checkpoint. Do not use SQLite `immutable=1` for a changing WAL database. SQLite may use WAL/SHM coordination files during read-only access; this contract guarantees no history/schema mutation, not byte-identical shared-memory files while writers are active. If the SQLite driver cannot access required sidecars with available permissions, report a history error; do not silently reopen as a writer. Tests verify no database writes or missing-history creation by reader modes. Missing history means an empty list/exit 1, or missing ID/exit 2 for show/follow.

`--list` includes every recorded request, newest activity first (latest round or newest run start), with deterministic tie-breaking. Its targets label prefixes the newest run's file basename, then comma-space-joins canonical tokens within a 44-Unicode-code-point token budget, always retaining the first token and suffixing ` +N` for omitted tokens; the source basename and omission suffix are outside that budget. This label also appears in JSON. State is the newest live run if any, otherwise newest run, independently of the run supplying the source label. A stored `running` run is **lost** when heartbeat age is greater than `max(2.5*interval, 60s)`, or its local-machine PID is definitely gone. An inaccessible PID is not proof of death. Lost state is computed by readers and not persisted. Include zero-round active runs. Display running progress/next slot, finished, stopped early, failed, or lost-at-heartbeat detail. JSON retains raw `state: "stopped"` with empty detail; human reports render it as `stopped early`.

`--show` reads a consistent snapshot, applies the fixed since cutoff, folds rounds chronologically, and prints a summary without probing. `--follow` requires stderr to be a terminal, polls at most every 2 seconds, displays that request while any of its runs is live, and prints a final summary when all end or the follower is stopped. `q`/Ctrl+C stop only the follower. If nothing is live initially, print a note to stderr and the summary immediately.

A follower uses a transaction-consistent request state, round high-water mark and ordered pages. It must notice new runs, out-of-start-order commits, new one-round samples changing inferred spacing, pruning of already folded rounds, and a request pruned then recreated under the same key/new integer ID. Rebuild the selected window when incremental folding is invalid; emit each newly committed change only once. A final follow read after `q` includes rounds committed since the preceding poll. Keep database snapshots short and release them between polling cycles.

## 8. Terminal and report contract

All final reports go to stdout; diagnostics, progress, changes and live frames go to stderr. JSON/CSV stdout must remain parseable while the live screen is active. Output errors other than an ordinary closed downstream pipe are reported as failures (exit 3); a closed pipe terminates quietly. The utility requires no secrets and emits no telemetry.

Live drawing requires stderr TTY and, on Linux, foreground process-group ownership. Key eligibility is independent: keys require stdin TTY and foreground ownership even when drawing is disabled or stderr is redirected. Do not consume redirected target data as keys. A background job, cron invocation, or job moved to the background draws nothing and reads no keys; eligible drawing and keys may activate after `fg`, including when the command started in the background. A disappearing terminal disables drawing without aborting useful work. Restore eligible terminal input/console modes and cursor on graceful stop, normal exit, and errors. On Linux, once stdin belongs to another foreground process group, polling and cleanup leave its termios settings to the shell/new owner: do not apply or restore modes from the background. Restore before an intended suspend while still foreground, and check foreground ownership again after resume. Background screen cleanup emits no frame or newline; it may emit only the six-byte show-cursor sequence if this screen hid the cursor and `TOSTOP` is clear, otherwise it leaves cursor handling to the foreground owner. This retains the pinned source's [key ownership rule](https://github.com/evdanil/cn-tool/blob/2e2f3c40ef6718486f1ca4477f9145029ec495fa/cn_tool/utils/ping_live.py#L140-L159) and [background cursor rule](https://github.com/evdanil/cn-tool/blob/2e2f3c40ef6718486f1ca4477f9145029ec495fa/cn_tool/utils/ping_live.py#L68-L80). Handle Windows console modes and Linux suspend/resume explicitly; arrow/function-key/Alt escape sequences must not become `q`/`v` commands.

Ordinary status and error diagnostics are independent of live drawing. They remain eligible when drawing is disabled or has failed, when stderr is redirected, and on Linux when its terminal permits background writes (`TOSTOP` clear). A foreground terminal also remains eligible. When stderr is the process's controlling terminal, the process is in the background and `TOSTOP` is set, omit only that diagnostic's terminal write; continue normal configured logging and preserve history, final reports and exit behavior. An unavailable terminal or an indeterminate job-control check is also best effort: do not force a write. Never ignore, block or reset `SIGTTOU`, change terminal modes, redirect diagnostics into stdout, or queue/replay omitted diagnostics to bypass this restriction. Logging failures retain their actual stderr messages on eligible destinations; immediate textual delivery is unavailable if logging fails while stderr cannot safely accept a diagnostic. This condition does not suppress final report records or alter stdout's ordinary I/O behavior. The distinction follows the source's foreground-only [live change notes](https://github.com/evdanil/cn-tool/blob/2e2f3c40ef6718486f1ca4477f9145029ec495fa/cn_tool/utils/ping_live.py#L393-L401) and separate [ordinary console diagnostics](https://github.com/evdanil/cn-tool/blob/2e2f3c40ef6718486f1ca4477f9145029ec495fa/cn_tool/modules/ping_monitor.py#L130-L132), while preserving cn-mon's approved native probe diagnostics whenever the destination permits them.

The grid has a block for each canonical subnet and a `Hosts` block for single literals/names. Preserve subnet address positions, leaving excluded addresses blank; overlapping target blocks may reference the same host. Choose the largest fitting row length of 64, 32 or 16 cells, grouped by eight. If the grid cannot fit, use one counter line per target block. Fit header, counters, legend and footer to terminal width/height, counting display cells rather than bytes. Show only present legend characters, bounded change lines, and `(+N more)` when cropped. Recompute on resize. `v` toggles to a live table of hosts seen so far with Host, Latest, Last seen and Seen counts, including provisional current-round observations; another `v` returns to the grid. `q` stops; `Q`/`V` are equivalent to lowercase. No alternate menu or target prompt exists.

Table output defaults to readable section tables. The command uses stdout's terminal width when available, with an 80-column fallback; direct report callers that omit a positive width receive a 100-column default. Width affects only table output: it wraps cells and falls back to stacked field/value rows on narrow outputs while retaining every record. Its online-period cell shows the last three periods and `(+N earlier)` when needed; JSON/CSV/Markdown keep all periods. Apply the configured/overridden theme from section 2.1. Color is disabled for redirected output and when `NO_COLOR` is present, including an empty value; terminal bold/dim/reverse remain meaningful without hue, while redirected output has no ANSI. Sanitize control/escape characters in human terminal text without corrupting structured values.

JSON keeps empty sections and lower_snake_case keys. Existing keys below are stable; additions require an explicit contract change. All times are UTC to seconds as `2026-10-09T09:14:00+00:00`; absent text values are `""`, while the defined null cases remain JSON null. Counts are numbers, never formatted strings.

| Mode | Exact top-level sections |
| --- | --- |
| Target run | `seen`, `latest`, `summary`, `not_found` |
| List | `requests` |
| Show/follow | `seen`, `latest`, `summary` |

| Section | Row fields |
| --- | --- |
| `seen` | `host`, `address`, `latest`, ascending `tcp_<port>` fields, `first_seen`, `last_seen`, integer `seen_rounds`, integer `rounds`, `online: [{"from": timestamp, "to": timestamp|null}]` |
| `latest` | Every host of the last completed round in canonical order: `host`, `address`, `result`, ascending `tcp_<port>` fields |
| `summary` | One row when any selected round exists, otherwise `[]`: `id`, `started`, `ended`, `rounds`, `earlier_rounds`, `interval`, `hosts`, `seen`, `answering` |
| `not_found` | `object`, `reason`; invalid input and names never resolved in the current target run |
| `requests` | `id`, `targets`, `tcp` (comma list or `-`), integer `runs`, integer `rounds`, `last_round`, `state`, `detail` |

For a target run, `summary.rounds` counts this run and `earlier_rounds` its initial historical snapshot; for show/follow, `rounds` counts all selected rounds and `earlier_rounds` is 0. `started`/`ended` are first/last selected **round starts**, not process lifetime. `interval` is seconds (number), null for an interval-less last/current run. A summary may show earlier data after a run finishes no round, but that run's success remains false. `seen.rounds` counts all included rounds.

A current run retains full latest rows, including silent hosts' resolved addresses/TCP states. History stores details only for seen hosts: show/follow reconstruct unseen rows from their observation character with empty address/TCP fields; `NO ROUTE` is reconstructed as `UNREACHABLE` from `U`. This deliberate reference behavior must be documented and tested, not silently “repaired” with fabricated data.

CSV is one UTF-8 table with `section` first, followed by the union of human-readable column headers in first-seen order across sections; standard quoting handles commas, quotes and newlines, and records use LF. Non-JSON seen rows use `Seen` (`N of M rounds`) and textual `Online`, not nested JSON. Markdown has one titled section and pipe table per nonempty output section; escape pipes and replace cell newlines with `<br/>`. Table/CSV/Markdown omit empty sections and emit nothing when every section is empty; JSON still keeps every section key. Textual periods use UTC `HH:MM`, include `MM-DD` across days, and show `now` only for genuinely open periods. All nonempty output ends with one newline. Cosmetic table borders may differ from Rich; fields, ordering, meaning, truncation disclosure and escaping may not.

## 9. Exit status

| Code | Target run | List | Show/follow |
| --- | --- | --- | --- |
| 0 | At least one round completed in this run and a host was seen in its selected window; with TCP, some selected round had an open requested port | At least one request | Host seen in selected window; with TCP, an open requested port |
| 1 | Nothing met success, or no current round completed | Empty/missing history | No host/required open port in the selected window |
| 2 | Usage/input/configuration error, including no usable targets | Invalid mode/options | Bad/missing/ambiguous ID, invalid options, or follow without stderr TTY |
| 3 | Probe execution failed in a completed current round, history/report-output failed, or logging could not initialize; print available results | History/output failure or logging could not initialize | History/output failure or logging could not initialize |
| 130 | Second Ctrl+C; Ctrl+C during pre-run input parsing | Ctrl+C | Ctrl+C for show; second Ctrl+C for follow |

Ordinary precedence is **3 > 2 > 0 > 1**; immediate interruption is 130. Invalid targets can coexist with useful output and exit 2; a probe/history failure then wins with 3. First graceful stop computes the normal code. Earlier recorded probe errors alone do not make a new run or show/follow exit 3. `REJECTED` without TCP is success; `NO ROUTE` alone is 1; a TCP closed-only round is seen but exits 1 with `--tcp`. Merely observing silence, unreachable, DNS absence or a closed port is not an execution error.

## 10. Architecture, licensing and delivery

Keep the application small and testable. One command at `cmd/cn-mon`; internal packages for `target` (input/canonical request), `probe` (DNS/ICMP/TCP and OS mapping), `monitor` (pure tracker plus orchestration), `history` (SQLite), and `output` (reports/live terminal). Keep the five-key INI compatibility and logger setup in small command support files; reuse a permissively licensed INI parser if it meets the characterized source grammar, without introducing a configuration framework or loading other cn-tool settings. Split OS-specific files with build tags; do not create one interface per type. Introduce interfaces only at real nondeterministic boundaries: clock, resolver/prober, store, terminal/signal input. A single orchestrator owns tracker state and store writes; workers return indexed values. Renderers never mutate the model or write history. Keep output DTOs separate where JSON typing differs from human tables. Stdlib handles flags/JSON/CSV/networking where adequate; a small option parser is acceptable for interspersed flags, but no large CLI/TUI framework is required. Terminal primitives use `x/term`/`x/sys`, plus a small permissive display-width package if needed.

Use Go modules with the M3 dependency floor Go 1.26.0 and pinned development/CI toolchain Go 1.27.2. Build release artifacts with `CGO_ENABLED=0`; the refreshed cloud setup helper activates `GO111MODULE=on` and `GOTOOLCHAIN=local`, so no additional module-mode override is required. Do not make a runtime toolchain download a hidden prerequisite.

All new first-party code and documentation are **Apache-2.0**, allowing reuse without a requirement to disclose derivative source. M0 replaced the destination's original GPL-3.0 project declaration for this authorized utility; inventory retained material and do not copy third-party GPL/copyleft code. Changing a root LICENSE cannot relicense someone else's material. Preserve cn-tool Apache attribution for adapted logic; preserve pro-bing's root MIT license and Apache source headers rather than asserting one overrides the other; retain Digineo MIT attribution if code is copied. Include `NOTICE`/third-party notices as applicable and an auditable dependency-license inventory, including SQLite bindings/transitives and any UI library. No GPL, LGPL, AGPL, SSPL, or other source-disclosure/copyleft runtime or build dependency is allowed. Pin/checksum modules and run dependency vulnerability checks proportionately; do not disable verification or claim a license audit before performing it.

Release versioned `cn-mon_<version>_linux_amd64.tar.gz` and `cn-mon_<version>_windows_amd64.zip`, each containing the binary, concise usage/permission documentation, LICENSE and third-party notices, plus published SHA-256 checksums. `--version` reports version and revision. Run the built binaries on both native OSes; publish the tested OS/toolchain versions and the actual ICMP permission requirements. No installer, automatic update, signing service, or packaging ecosystem is required for v1.

### 10.1. M4 run-engine contract

M4 implemented this contract against merged M3 `e5b49af` and passed its native/ordinary/review gate in [PR #11](https://github.com/evdanil/gotest/pull/11), merged as `e54f4266177eb1319d9216c79809ced7e05f078d`. `internal/monitor` owns orchestration beside the pure tracker, `internal/output` emits target JSON, and `cmd/cn-mon` supplies startup/signals/logger/exit. One state owner mutates tracker/history state. The injected boundaries are a clock, the existing probe methods and the existing history methods; snapshots remain concrete M3 values, with real temporary SQLite in integration tests. Section 10.2 extends these APIs for M5; it does not replace M4's completion, cancellation, scheduling or permission contracts. No generic task/event/store framework is needed.

The shared declarations are fixed as follows; existing `Row`, `Stats`, `HostSummary`, target/probe types and history DTOs retain their merged meanings:

```go
// internal/monitor/run_types.go
type ClockReading struct {
    Wall time.Time
    Mono time.Duration // elapsed from this clock's own monotonic origin
}
type Clock interface {
    Now() ClockReading
    After(time.Duration) <-chan time.Time
}
type Prober interface {
    Resolve(context.Context, []string) ([]probe.Resolved, error)
    ProbeBatch(context.Context, []probe.Resolved, []int) ([]probe.Result, error)
}
type History interface {
    Close() error
    BeginRun(context.Context, history.Request, history.RunStart) (int64, error)
    RecordRound(context.Context, string, history.RoundInput) (int64, error)
    Heartbeat(context.Context, string, time.Time, *time.Duration) error
    EndRun(context.Context, string, time.Time, string) error
    Prune(context.Context, time.Time, *big.Int) (int64, error)
    View(context.Context, func(*history.Snapshot) error) error
}
type RunPlan struct {
    Request     target.Request
    Interval    *time.Duration
    Duration    *time.Duration
    Since       *time.Time
    OwnRunOnly  bool
    Source      *string
    HistoryFile string
    HistoryDays *big.Int
    RunID       string
    Hostname    string
    PID         int64
}
type RunDeps struct {
    Clock       Clock
    Prober      Prober
    OpenHistory func(context.Context, string) (History, error)
    Stop        <-chan struct{} // nil disables graceful external stop
    Note        func(string)   // optional; caller sends diagnostics/status to stderr
}
type LatestRow struct {
    Host string
    Row  Row // existing monitor.Row: Address, Result, TCP
}
type RunOutcome struct {
    Stats                       Stats
    Seen                        []HostSummary
    Latest                      []LatestRow
    CurrentRounds, EarlierRounds int
    SkippedSlots                int64
    Interval                    *time.Duration
    Unresolved                  []string // sorted, never-resolved names
    ProbeFailed, TCPOpen, TCPRequired, Stopped bool
    HistoryError                error // first store error only
}
func Run(ctx context.Context, plan RunPlan, deps RunDeps) (RunOutcome, error)
func RunExitCode(outcome RunOutcome, invalid bool) int
func NewClock() Clock

// internal/output/run_json.go
func WriteRunJSON(w io.Writer, request target.Request,
    outcome monitor.RunOutcome, rejected []target.Rejection) error
```

`RunPlan` receives resolved settings and a valid canonical request, including caller-supplied UUID, hostname/PID, optional file basename, fixed since cutoff and effective schedule. The command supplies the implicit 60-second interval for `--for` alone. It creates IDs with the existing UUID dependency; no dependency is added just for orchestration. `RunOutcome` contains final aggregates/current latest rows, not every stored round. `Run` returns ordinary probe/history degradation in that outcome; its error return represents hard cancellation, invalid internal input or an unexpected engine/prober failure. `RunExitCode` uses section 9's current-versus-earlier precedence; available output survives ordinary failures.

The engine's context is the hard-abort lifetime. A separate graceful `Stop` closes once and cancels work, including startup history reads/lock waits, resolving and incomplete probes. Completed-round writes and finalization use the hard context, so a first stop cannot erase completed work. The command's second Ctrl+C cancels that context and exits 130 independently of blocked workers, SQLite, logging or output. Direct callers cancel the context without a process exit. Workers return values through bounded channels and are joined; they never mutate the tracker or Store.

The private run clock exposes `newRunClock(Clock) *runClock` and `Now() runReading`, where `runReading` has `Run time.Time` (corrected), `Wall time.Time` (uncorrected UTC) and `Mono time.Duration`. Construction consumes no reading; each Now uses exactly one underlying reading. Only the state owner and its synchronous history helpers call it. Use corrected run time for origin/slots/sample timestamps and raw wall time for heartbeat. Preserve the rollback/forward-jump rules in section 5.

The history bridge owns `openHistory()` as the single OpenWriter → Prune → initial `readEarlier()` View → BeginRun sequence. The engine captures origin first and never repeats those stages. Fold earlier pages into unpublished aggregates, with both prospective-own-start and without-own-start cron-spacing trackers when necessary, and publish only after the full View/transaction succeeds. Skip earlier folding for OwnRunOnly. Record only successful run creation via `runRecorded`; the returned integer request ID has no M4 consumer. A failed initial view supplies no partial history. A genuine store failure detaches/closes once, reports once, continues completed rounds in memory and forces exit 3; do not reopen or retry an uncertain commit.

The completed-round boundary is successful return of the final ProbeBatch with every host accounted for, or successful complete resolution when no hosts can be probed. If a stop races with result publication, join and inspect that result: successful final completion is retained; canceled or nonfinal work is discarded. Do not add a later stop check that erases it. Commit structured probe execution errors only with complete rounds, even when an echo/seen state masks them. `recordCompleted(round history.RoundInput)` performs storage only. After it returns, measure elapsed from the actual round beginning, then call `heartbeat(elapsed *time.Duration, completed bool)` using the hard context for completed rounds; persistence time is included, startup and the heartbeat call's own duration are excluded. Fold/count that complete round exactly once even if persistence fails. `endHistory(status)` finalizes/cleans up once; normal reachability/probe failures use source status finished, a graceful stop uses stopped, and an unexpected engine failure uses failed.

After that completed round's persistence attempt, successful fold, count/flags and latest-row publication, emit at most one probe-failure `RunDeps.Note`, if supplied. Include the first failing host, `ICMP` or `TCP` with its port, the actual execution-error cause, and the number of additional failing host/protocol attempts when nonzero. Choose deterministically in canonical host order, ICMP before ascending requested TCP ports. Count each host/protocol attempt once, even if its error has multiple joined causes; both a non-nil structured Err and an `ERROR`/`error` state qualify, including failures masked by a seen result. If only the failure state is available, explicitly say error detail is unavailable rather than inventing an OS cause. Keep one escaped single-line message of at most 4,096 UTF-8 bytes, truncating host/cause with a visible marker while preserving protocol, count and any section 4 permission remedy. Retain only the first diagnostic, count and permission indicator, not a list of failures. Normal no-response/unreachable/closed/timeout outcomes and incomplete rounds produce no probe-failure Note. This uses the existing Note only, adds no outcome/report fields, and leaves history/lifecycle diagnostics independent. No signal handler emits it; the independent second interrupt must still exit if Note blocks.

A narrow first-use addition, `history.CancellationOnly(err, cause error) bool`, distinguishes expected work cancellation from history failure without driver types leaking into monitor. Private `canceledDBError` retains observed context and single driver cause; private `cleanupFailure` plus `markCleanup(error) error` identifies independent cleanup. Preserve original error identities through Unwrap. Tag causal BUSY/INTERRUPT only when the operation context is already done and the driver error is a single cause, never an arbitrary joined tree. The classifier accepts only the exact non-nil Canceled/DeadlineExceeded cause, matching causal tags and wrappers/joins containing only accepted causes. Any cleanup tag or other independent error rejects cancellation-only classification. The bridge also requires a canceled work context and an active hard context before treating it as graceful stop.

Mark every independently observed rollback/discard/connection-close/store-close failure as cleanup. This includes reader `closeRows`/`failRows` and pruning `closePruneRows`, both standalone and joined failures, even when the close error equals the cancellation sentinel or is BUSY/INTERRUPT. The history-bridge owner alone edits those narrow sites in values/tx/store/read/prune and the new classifier/tests; retention predicates, status/scalar validation and successful-COMMIT semantics stay unchanged. Explicit Store.Close failure always remains a history failure. Tests must prove that intentional cancellation cannot hide cleanup failure.

Target command startup remains parse/validate → requested input → settings/logging → canonical request → run. M4's temporary guard rejected reader modes and non-JSON formats before data I/O; M5 removes that guard through section 10.2's dispatch/report integration. Input read failure/empty collection exits 2 before config/logging; all-rejected input prints the selected format's rejection report and exits 2 without history/network (JSON keeps its empty sections). Mixed input continues. Before the run phase, subscribe only to non-ignored Ctrl+C, which exits 130 promptly even with held stdin. On Linux add TERM and non-ignored HUP only at the running transition, before opening history. Before that transition, TERM terminates and HUP retains default termination unless inherited ignored; preserve inherited ignored HUP/INT and apply only section 5's approved inherited-TERM exception. In the running phase TERM/non-ignored HUP request graceful stop. Keep control active through finalization/reporting; M4 itself installs no terminal modes/keyboard reader; section 10.2 governs their M5 addition.

When the engine reaches graceful finalization, it emits the one-time status `stopping; preparing the final summary` through `RunDeps.Note`, if supplied, after joining work and retaining any completed round, before `endHistory`/EndRun. The command sends this status only to stderr. This is a native status diagnostic, not a source-literal or new report field. Normal completion and hard-only abort do not emit it. The signal handler and stop-forwarder perform no logging/writer I/O; a blocked Note must not block the independent second-interrupt exit path. The neutral wording also applies to q/TERM/HUP without implying those count as a first Ctrl+C.

The JSON writer performs no configuration/history/network I/O and uses section 8's exact typed fields, arrays and UTC-second `+00:00` times. Emit canonical-order seen/latest rows, numerically ordered TCP fields, every sampled period, four arrays even when empty and exactly one final newline. Final target periods are closed; no new persistence/status/error fields are introduced. Input rejections precede sorted never-resolved names. M5 supplies the default table renderer and the other formats under section 10.2 while retaining `WriteRunJSON`. A structurally identified closed downstream pipe adds no pipe/report-write diagnostic and retains the already-computed ordinary exit; legitimate probe/history/lifecycle diagnostics remain allowed. Other output errors return 3. Native tests must cover Go's stdout SIGPIPE behavior and Windows pipe error mapping rather than suppressing arbitrary EINVAL.

Native command permission gates follow section 4 and PRB-03. Linux requires genuinely granted and capability-denied roles. Windows uses elevated and ordinary roles, independently verified against the expected token user SID, elevation and enabled Administrators membership in both the test process and its child command. Elevated controlled loopback must return `OK`/exit 0. Ordinary Windows may return full echo success/exit 0 or structurally established ICMP access denial with `ERROR`/exit 3; any other failure fails the gate. Both branches require open TCP, exact completed-round JSON/history agreement and the verified account identity. Establish the denial branch with a typed permission witness from the existing Prober under the same verified ordinary token; JSON text alone cannot prove structural denial. Only actual access denial requires the bounded observed OS cause/Administrator-remedy diagnostic; never manufacture denial or label successful ordinary probing denied. Query the child token while held stdin prevents startup completion, then release EOF to run normally; this is a test-only synchronization mechanism, not a product hook.

The history-bridge owner moves only the generated ExpectedSpacing cross-package equivalence test from same-package history tests into `monitor/run_history_test.go`, seeded through public Store APIs. This removes the history-test → monitor → history cycle; keep history-local spacing/malformed/raw-REAL fixtures and do not duplicate the median algorithm. The development plan assigns exclusive files and required native/TDD gates.

### 10.2. M5 readers, reports and live terminal contract

M5 implements the existing four command modes and four output formats in the current packages: `monitor` owns read/follow aggregation and mutable tracker state, `output` owns reports/pure layout/thin OS terminal handling, and the command owns settings/logging/control and actual terminal construction. `history` gains no new API or production change. Readers reuse its read-only snapshots, opaque REAL-preserving cursors and spacing queries, M4's stored-round conversions, and the existing typed JSON construction. No menu, Python runtime, UI framework or event bus is added. Core components, CLI integration and terminal repairs have scoped independent review approval. The declarations below define the implemented interfaces and required terminal behavior. M5 passed its reviewed native/CI merge gate in PR #12 at the revisions recorded above. The shared collector has independent real-OS RED-to-GREEN evidence; the precise cause of older intermittent capture failures remains unestablished. M6 reuses these implementations and harnesses to qualify the extracted release binary.

The shared additions are:

```go
// internal/monitor/read_types.go — reader owner
// A genuine nondeterministic boundary; Snapshot stays concrete.
type Reader interface {
    Close() error
    View(context.Context, func(*history.Snapshot) error) error
}
type ReadDeps struct {
    Clock       Clock
    OpenHistory func(context.Context, string) (Reader, error)
    Hostname    string
    PIDState    func(int64) history.PIDState
    Stop        <-chan struct{}
    EnterFollowing func() // command control transition after the initial live snapshot
    Live        *LiveHooks
    Note        func(string)
}
type ReadPlan struct {
    HistoryFile string
    Prefix      string
    Since       *time.Time
    Follow      bool
}
type RequestRow struct {
    ID, Targets, TCP string
    Runs, Rounds     int64
    LastRound        *time.Time
    State, Detail    string
}
type ReadOutcome struct {
    Request target.Request
    Run     RunOutcome
    Found   bool
    Running bool
    Problem string // missing/ambiguous prefix, not a history failure
}
func List(ctx context.Context, path string, deps ReadDeps) ([]RequestRow, error)
func Read(ctx context.Context, plan ReadPlan, deps ReadDeps) (ReadOutcome, error)
func ReadExitCode(out ReadOutcome) int

// internal/monitor/live_types.go — live bridge/terminal owner
// Small display-only values, without per-host period/TCP histories.
type LiveRow struct {
    Index             int
    Host, Latest      string
    Cell              byte
    LastSeen          time.Time
    SeenRounds, Rounds int
}
type LiveData struct {
    Stats            Stats
    Cells            []byte
    Changed, Partial []int
    Rows             []LiveRow
    Seen             int // committed-ever seen plus newly seen pending hosts
}
type LivePhase uint8
const (
    LiveResolving LivePhase = iota
    LiveProbing
    LiveWaiting
    LiveFollowing
    LiveEnded
)
type LiveUpdate struct {
    Request                  target.Request
    Data                     LiveData
    Phase                    LivePhase
    Now, Origin              time.Time
    Until                    *time.Time
    Interval                 *time.Duration
    Source                   *string
    CurrentRounds            int
    SkippedSlots              int64
    Done, Total               int
    NextIn                    *time.Duration
}
type LiveHooks struct {
    Enabled func() bool
    Draw    func(LiveUpdate)
    Change  func(Change)
}
// M5's only RunDeps extension:
//     Live *LiveHooks
// Existing M4 callers leave it nil with identical behavior.

// internal/output/report.go — report/layout owner
// Empty Format means table; nonpositive Width means 100 columns.
// JSON/CSV/Markdown ignore width, theme, terminal and color settings.
type ReportOptions struct {
    Format, Theme     string
    Width             int
    Terminal, NoColor bool
}
func WriteRun(w io.Writer, request target.Request, out monitor.RunOutcome,
    rejected []target.Rejection, options ReportOptions) error
func WriteHistory(w io.Writer, out monitor.ReadOutcome, options ReportOptions) error
func WriteRequests(w io.Writer, rows []monitor.RequestRow, options ReportOptions) error
// Keep WriteRunJSON as the compatible JSON-only wrapper.

// internal/output/terminal.go — live bridge/terminal owner
type KeyHandlers struct { Quit, Interrupt func() }
func OpenScreen(in, errOut *os.File, theme string, noColor bool,
    keys KeyHandlers) *Screen
func (*Screen) Enabled() bool
func (*Screen) Draw(monitor.LiveUpdate)
func (*Screen) Change(monitor.Change)
func (*Screen) Note(string) // ordinary stderr status/diagnostic, serialized with a live frame
func (*Screen) Close()
func (*Screen) EmergencyRestore()

// internal/output/terminal_{linux,windows}.go — live bridge/terminal owner
func DiagnosticWriteAllowed(w io.Writer) bool
```

A `Reader` adapts the existing `*history.Store` at the genuine I/O boundary; Snapshot remains concrete. `ReadDeps.EnterFollowing` is optional and synchronous: call it exactly once after the initial successful found/live snapshot and hard-context check, before the first follow draw/poll, whether drawing is enabled, disabled or absent. The command binds its existing running-control transition. Do not call it for list/show, missing/ambiguous history, no live writer, failed initial reads or preexisting hard cancellation. It performs no terminal/history/report I/O.

Reader startup is parse/semantic validation → settings/logger → mode-specific terminal prerequisite → OpenReader. Readers never collect target input, resolve DNS, construct a prober or prune history. Follow requires real stderr TTY before opening history; this prerequisite is independent of best-effort `OpenScreen`, `Screen.Enabled`, foreground drawing and stdin key eligibility. An absent database creates no parent/database; readers never fall back to write mode or change schema/status/retention. WAL/SHM coordination is distinct from database mutation. Missing list history yields empty requests/exit 1; missing/ambiguous show/follow yields empty sections/exit 2. Open/query/validation/transaction/Close failure yields empty reader sections/exit 3, never a partial report. Historical probe errors alone do not imply reader exit 3.

List folds paged request summaries and running-state pages within one View. Keep only final small request rows and the selected live run, not every request's host array or all run IDs. Source labels use the newest run's source basename independently of the selected live run; target token labels use the characterized 44-code-point budget with the first token always retained. Running detail uses that run's persisted completed-round count. Its upcoming slot uses signed ceiling of `(now - started) / interval`, including clock rollback, and is printed only when strictly before the exclusive cutoff; an interval-less run says `one round, in progress`. Show/follow use stored request tokens/ports/hosts/key as authoritative, without rebuilding identity. Apply the fixed Since, compute spacing in SQLite and fold ordered pages by their original opaque `(REAL started,id)` cursors. Only the last selected round's own live writer permits its final online period to remain open; another live run does not. Compute state/detail and long-interval arithmetic without duration overflow.

Follow keeps both the current snapshot Through value, which may decrease after pruning, and a monotonically increasing observedThrough, which never resets across rebuild or same-key recreation. In one frozen View, detect changed request identity/metadata/window endpoints/count or selected newly committed IDs beyond observedThrough, including late earlier-started commits. Rebuild an unpublished aggregate from bounded ordered pages with current fixed spacing only when selected data changes; otherwise update liveness. Publish aggregate/request/watermark only after the View succeeds. Keep at most old/new aggregates, required report periods, a bounded page and current display state; never cache all starts, rounds, IDs or change notes. New same-key requests use their new integer ID and authoritative hosts. A successful missing/no-live snapshot ends the follower; it does not wait for a later recreation.

Initial history produces no change notes. Later folds emit only changes from IDs greater than the previous observedThrough, one at a time in source start order. Notes remain permanently above the live frame/in scrollback; at most three are retained as redraw state. A later read failure produces empty final sections/exit 3 without retrying already emitted committed observations. Graceful stop during a poll lets that fixed View finish under the hard context, publishes its watermark, then performs one fresh final View; stop while idle directly performs that final View. Hard cancellation aborts promptly. Idle reads occur within two seconds and redraw waits within 500ms, without promising a large SQL fold finishes within two seconds. A follower's q never stops its writer.

Live hooks are synchronous on the monitor state owner. New LiveData slices are read-only display values; borrowed request slices cannot be retained or mutated. Nil hooks preserve M4 behavior. Display pending resolving/batch results only at the source's completed-batch granularity; no Prober extension is required. Pending display never changes Tracker, history, JSON/CSV or exit flags. Build bounded live rows directly from host aggregates without copying retained online periods or TCP histories on redraw. Resolving/probing headers display the in-flight round (`CurrentRounds + 1`); after commit use `LiveWaiting` and the committed count, avoiding a fictitious next round. Preserve the final successful-batch completion boundary, direct Stop observation at startup/finalization and the shared heartbeat deadline across work/waits. Slow synchronous draws must not defer an already-due heartbeat; both work and waiting paths reassess its remaining deadline after drawing. Draws do not sample the mutable run clock independently. Preserve section 5's post-wait slot reselection, ordinary-jitter tolerance defined by the next full interval, exclusive cutoff and first-round exemption. After reselection and synchronous skipped-slot Note publication, the state owner re-enters its actual-start check even when the replacement was exactly current; a blocked Note may have crossed another slot or cutoff. The eventual final cutoff and round stamp share one reading, while retaining the selected index prevents duplicate skipped counts. Capture Tracker.Add changes only after completed persistence/fold/count/latest publication and the existing diagnostic.

While a screen owns the diagnostic destination, route ordinary status/diagnostic output through Screen.Note, preserving the command's prefix, logging and error behavior. Screen.Note and the command's direct diagnostic fallback share `output.DiagnosticWriteAllowed`; drawing eligibility alone never determines ordinary diagnostic delivery. The terminal owner implements this one bounded, synchronous OS predicate, and the command owner applies it before either route, including after Screen.Close/removal. Non-file writers and redirected files/pipes remain eligible; Windows has no Linux background-write restriction. Linux applies section 8's controlling-terminal/foreground/`TOSTOP` rule immediately before a diagnostic write, without treating a terminal belonging to another session as the caller's background controlling terminal. The predicate performs no writes, mode/signal changes, logging or queueing, and controls neither stdout reports nor whether the caller logs a diagnostic. Keep M4's exact one-time graceful note and bounded completed-round probe note, including masked execution errors, actual causes/counts and structural permission remedies. No diagnostic moves into a partial batch or is regenerated from historical rows. Draws and notes share terminal-write serialization; key input only invokes control or changes the screen's view preference, never Tracker/Store. Hard-exit control must not wait for the draw mutex, logging, workers or database cleanup.

Reuse typed JSON for all formats: target has four sections, show/follow three, list only requests. Arrays stay arrays, timestamps/counts/interval/TCP retain their types and canonical ordering, and only eligible historical periods have `to:null`. Human formats omit empty sections and produce empty output when all sections are empty. CSV uses the source human headers with `section` plus their ordered union, standard quoting and LF; Markdown preserves source section order, pipe/newline escaping and all periods. Table alone abbreviates to the last three periods plus the earlier count; it retains all final records. Structured reports retain data values and format escaping without ANSI or terminal sanitization. Human terminal paths escape controls, crop by grapheme/display cells and preserve the characterized four-theme source palette, including its twice-advanced column cycle. NO_COLOR presence disables hue; real-terminal bold/dim/reverse remain, while redirected output contains no ANSI.

Pure layout uses `target.LayoutInputs(tokens []string) []LayoutInput`, where `LayoutInput` has `Network netip.Prefix` and `Hosts []string`. The caller passes canonical `Request.Tokens`, matching the source's display order; the helper preserves that supplied order and uses existing parsing without a TCP cap or identity rebuild. Admission tracks first occurrences of usable parsed hosts, not prefix containment: a later explicit subnet broadcast literal can contribute even though it is inside an earlier prefix. Omit a later token/subnet only when all its usable hosts were already admitted. A retained subnet has a valid masked `Network` and keeps its full grid positions; a retained single/list input supplies only newly contributed `Hosts`. Stored request hosts remain authoritative for actual cells. Build grid indexes once per metadata change; use source 64/32/16 widths, counter fallback, legend and table overlay. Count wrapped Hosts rows when selecting grid versus counters. Recompute dimensions on resize, handle 1×1/tiny terminals safely and never scroll solely because a frame exceeds available height. Layout returns semantic styled segments; the shared output helper alone generates trusted ANSI.

Terminal startup is best effort and does not determine report success. Only real eligible handles change modes. Linux uses foreground-aware cbreak with ISIG retained, bounded input polling, source key-sequence handling, and restoration before suspend while foreground; resume does not imply foreground ownership. Capture original input modes successfully before any change, including lazy activation after a background start; pause key reads without changing modes when stdin loses foreground ownership. Check ownership immediately before terminal side effects; stdin mode restoration and stderr cursor eligibility are independent. Follow section 8's background cleanup rule on normal, error, graceful and emergency paths; do not force background `TCSETS` by ignoring/blocking SIGTTOU or overwrite the new foreground owner's settings. Preserve inherited HUP/INT/nohup and the approved standard-Go inherited-TERM exception. Windows uses actual console modes/key events/dimensions, ensures processed Ctrl+C while keys are active, and restores original VT/input modes; redirected handles remain untouched. Capture the complete original Windows cursor visibility and size before changing console modes or publishing terminal state, then retain that snapshot immutably. Normal, graceful, error and emergency cleanup restore that exact snapshot through the console API, including an originally hidden cursor. If cursor capture fails, disable live drawing while preserving eligible keys and reports; unavailable console restoration remains best effort. Foreground cleanup restores exactly changed modes and cursor state. A command-owned optional emergency callback extends the existing hard-exit path, including startup, without held control/draw locks or waits. Restore through bounded OS operations with race-safe saved-mode metadata; eligible Linux emergency cursor output uses an independently opened nonblocking terminal descriptor with a nonwaiting syscall write, never changes shared inherited descriptor flags, and must not block through Go's file poller. Test vanished terminals and blocked output; do not promise restoration after uncatchable process death. The merged M5 native gate covers these requirements; M6 must exercise them against its extracted release binary.

M5 pins `golang.org/x/term v0.44.0` (BSD-3-Clause) for terminal detection/dimensions and `github.com/rivo/uniseg v0.4.7` (MIT plus the Unicode-DFS-2016 notice for bundled Unicode 15 data) for grapheme/display width, alongside existing x/sys. Do not use MakeRaw, regenerate/download Unicode tables or add a terminal framework/CGO. The integrated modules/checksums and notices retain the Go 1.26 floor and the Unicode data's exact 1991–2022 copyright/permission text. Source-derived CSV/Markdown/palette/header fixtures and their regeneration provenance live in `internal/output/testdata`; M4's JSON fixture remains byte-identical. Native integration, license/dependency checks and official per-target vulnerability scans passed the M5 gate; M6 repeats the required checks at its selected release source revision. Preserve cn-tool attribution for adapted rendering/control behavior.

### 10.3. M6 release artifacts and qualification contract

M6 implements release identity, deterministic packaging, extracted-command fixtures and qualification/publication orchestration around the merged M5 command/native seams. Version 0.1.0 was built, qualified and published from exact M6 merge `f156255812308db7039d9721945957348ddbf8b4` after PR #13 completed its source/native/candidate-archive and review gates. No monitor/probe/history/terminal redesign, installer, updater, additional platform, signing service or runtime dependency is added. All 26 acceptance IDs remain required through the union of same-source component/native evidence and exact extracted-command evidence; component tests are not duplicated as black-box tests. Local dirty-source checks remain development evidence, and PR candidates remain distinct from the published merged-source archives.

The first version is **0.1.0**, with exactly `cn-mon_0.1.0_linux_amd64.tar.gz` and `cn-mon_0.1.0_windows_amd64.zip`. Each contains one matching top-level directory, its `cn-mon`/`cn-mon.exe`, README.md, LICENSE and THIRD_PARTY_NOTICES.md; add NOTICE only if the reviewed inventory requires it. Publish one companion `SHA256SUMS` containing exactly two lowercase digest/filename lines, in lexical order with LF. CI evidence is retained separately. Ship actual license texts/attributions, including the Go runtime/standard library, SQLite/generated code and Unicode data. The full Go 1.27.2 license text is already present in notices; identify its runtime applicability without duplicating identical text and complete the two-target import/notice audit. Preserve reviewed document bytes. Packaging may rewrite README links to the two known specification/plan paths, preserving fragments, to the exact source revision; fail on other unresolved local-file links.

The command uses private `version = "dev"` and `revision = ""`, emitting release version output exactly `cn-mon 0.1.0 (<full 40-hex source commit>)\n`; the development defaults retain `cn-mon dev\n`. There is no startup Git/network call or exported version API. Build natively on Linux/amd64 and Windows/amd64 with pinned Go 1.27.2, `GOTOOLCHAIN=local`, `CGO_ENABLED=0`, `GOAMD64=v1`, module verification, `-trimpath -buildvcs=true -mod=readonly`, and private `-X main.version=0.1.0 -X main.revision=<full-revision>`. Retain the Go 1.26 floor. Validate flag inputs and build from the selected clean source revision.

Use one standard-library CI tool at `scripts/release-artifact`; it does no process, network, product-build or publication work and is not shipped. Normalize member order/paths, Linux binary mode 0755, document modes 0644, owner metadata and selected-commit timestamps, including gzip headers. Reject symlinks, traversal, duplicates, unexpected members, wrong contents/modes and occupied extraction destinations. After semantic validation, the verifier uses the same private `io.Writer` encoding helper as the packer to re-encode the allowed members and archive time, then compares the complete raw bytes. This in-memory check preserves the input archive and the existing ZIP Deflate and gzip/TAR formats. Repacking identical inputs must produce identical archive bytes; no stronger cross-build reproducibility claim is made.

- `pack -binary <absolute-built-file> -source-root <exact-checkout> -out-dir <directory> -version 0.1.0 -revision <40-lowercase-hex> -commit-unix <selected-commit-seconds> -target linux_amd64|windows_amd64`. It writes the one deterministic archive named above. The caller verifies checkout revision/cleanliness and obtains commit time; the tool performs no Git/network/process call. It validates the supplied binary's offline source/build identity and `vcs.time` agreement with `-commit-unix` before packaging the fixed member set; the supplied release version is an expected value, not a value recovered from private linker variables.
- `verify -archive <absolute-file> -sha256 <expected-lowercase-digest> -source-root <exact-checkout> -extract-dir <fresh-directory-with-spaces> -version 0.1.0 -revision <40-lowercase-hex> -target linux_amd64|windows_amd64`. It verifies archive digest, complete member set/content/metadata and offline source/build identity, deriving timestamp expectations from validated `vcs.time`, then extracts only the allowed regular members into a new empty directory. The supplied release version remains an expected value until native `--version` verification. No execution occurs in this tool. Reject symlinks, traversal, duplicate/unexpected entries and an occupied extraction destination before writing product files.

Both operations report a compact JSON object on stdout containing `version`, `revision`, `target`, `archive_path`, `archive_sha256`, `binary_path` and `binary_sha256`; diagnostic text goes to stderr. In pack output `binary_path` identifies the input file, and in verify output it identifies the extracted file. The `version` field records the validated expected input; it is not offline proof of the binary's private version value. The native wrapper must match exact extracted `--version` output to this version/revision and the same hashes before treating the receipt as qualified. The workflow retains these as CI receipts, not shipped application files or a product manifest API. The orchestrator independently rehashes the archive/binary after native checks and before publication; a receipt alone does not replace version or hash checks.

Offline build-info checks validate the pinned Go version, source/module identity, GOOS/GOARCH, CGO=0, clean `vcs.revision`, valid `vcs.time` and expected modules/local pro-bing replacement; Linux ELF must have no dynamic interpreter, while normal Windows OS DLL imports remain expected. `pack` requires `-commit-unix` to agree with the selected commit time and validated `vcs.time`; `verify` derives expected normalized timestamps from validated `vcs.time`, using the format's representation. Missing/invalid/conflicting metadata fails; never use build wall-clock time.

Go 1.27.2 omits `-ldflags` from build info under `-trimpath`. The offline tool cannot recover private release version/revision variables from it and adds no production manifest or symbol parser. After verified extraction to a fresh path with spaces, the native wrapper requires exit 0 and exact `--version` output matching the expected version/revision, offline source revision and same archive/binary hashes. The expected version in an artifact receipt alone is not proof of the binary's release version. Record hashes before and after all native checks and before publication; any changed bytes fail qualification.

`CNMON_NATIVE_COMMAND_BINARY` remains the single product-path override. A nonempty explicit path must resolve to a regular absolute file or fail, with no fallback/rebuild; normal source CI without an override retains its current build behavior. Qualification requires an extracted path and may compile same-source tests/controllers as instrumentation. Hash every privilege-role copy before and after use; never add file capabilities or broaden checkout permissions to make a role pass. Preserve actual Linux capability facts and Windows parent/child SID, elevation and enabled-admin checks, including the reviewed ordinary-success or typed-denial branches.

For the small new extracted-command tests, set `CNMON_RELEASE_QUALIFY=1`, `CNMON_NATIVE_COMMAND=1`, `CNMON_NATIVE_COMMAND_BINARY=<verified-extracted-file>`, `CNMON_RELEASE_VERSION=0.1.0`, `CNMON_RELEASE_REVISION=<full-SHA>` and `CNMON_RELEASE_EXPECTED_SHA256=<binary-digest>`. Missing/invalid inputs in qualification mode fail; ordinary source runs may leave release-only tests inactive. The qualification scripts select each required new test explicitly, reject skips/missing markers, and retain all existing native script selectors/markers. Do not add production fault switches. Existing Linux namespace phases pass their established target/router/phase inputs to the small command assertions rather than creating another topology. Existing Windows console bootstrap/injector code and its real OS witnesses are reused unchanged.

Extracted follow coverage includes bounded ended-request checks for JSON, table, CSV and Markdown on a Linux PTY and a Windows private console, asserting read-only history, successful output and terminal restoration. Active JSON follow retains the real writer/concurrent-commit and follower-only `q` scenarios; the ended-request fixtures do not replace them. These implemented fixtures require native execution against verified extracted bytes before their release gate can pass.

Preserve every required native selector/PASS/result marker, including Linux interrupted-poll and readable-hangup collector regressions, all eight background cases and all nine Windows private-console cases. The collector's shell/self-executed test helpers and package-native probe/history tests are source/OS harness evidence, not extracted-product proof. Reuse the existing Linux namespace phases for missing built-command error assertions; retain Windows' reviewed actual-socket callback versus extracted-command evidence split. Do not introduce fake product faults, a weaker console suite or skipped required cases.

For no-system-ping evidence, run the extracted product with a child-only controlled PATH directory containing no helper executables, isolated working directory/home and preserved required Windows OS environment; exercise actual IPv4/IPv6 echo and TCP. Keep the harness tool PATH. Review the exact production source/import closure for process fallback. Restricted PATH plus successful native probes and source review establishes no runtime ping dependency, not physical OS ping absence or adversarial containment: Windows also searches application/current/system directories, and either OS can use absolute paths. Do not remove host binaries or introduce a product sandbox.

PR-head archives are unpublished validation candidates. Complete source/native/extracted qualification and internal/external reviews before M6 merges. Then rebuild and requalify at the exact M6 merged main SHA, even if its tree equals the PR head; never publish PR-head binaries as merge artifacts. Use read-only preparation and the existing native workflow's same-revision call seam. Only the authorized manual publication job gets write permission after qualification; it downloads the exact successful-run archives, verifies receipts/hashes/version/source and assembles SHA256SUMS. Never rebuild/repack between successful qualification and publication, move an existing `v0.1.0` tag or overwrite different assets.

After proving `v0.1.0` absent, create its lightweight Git ref through the create-only refs API at the exact qualified merged SHA; a collision or API error fails without updating/deleting a ref. Verify the exact ref, commit object type and SHA before creating the draft with `--verify-tag`. Do not rely on draft release creation to create the tag. Verify draft assets and the tag target before publication, then recheck the tag and public asset hashes afterward. Tag immutability here is the never-retarget policy, not a claim that GitHub protects draft tags. A failure can leave the created tag or an unpublished draft; automatic reruns still reject an existing tag. Root-owned recovery must retain that target and verified qualified bytes, with no deletion/retargeting or overwrite of different assets. Failed preparation/upload/checks leave no claimed release; rebuilt retries require fresh qualification.

### 10.4 M7 history-management implementation contract

Management uses `internal/history` for storage, `cmd/cn-mon` for dispatch and the additive `internal/output/management.go` reports, with no new dependency, schema, monitor mode, generic repository abstraction or public plugin API. The storage boundary is:

```go
type DeleteGuard struct {
    Hostname string
    PIDState func(int64) PIDState
}
type DeleteResult struct {
    Requests, Runs, Rounds, Results int64
    Committed bool
}
func OpenExistingWriter(ctx context.Context, path string) (*Store, error)
func (s *Store) DeleteRun(ctx context.Context, id string, guard DeleteGuard) (DeleteResult, error)
func (s *Store) DeleteRequest(ctx context.Context, prefix string, guard DeleteGuard) (DeleteResult, error)
func (s *Store) ClearHistory(ctx context.Context, guard DeleteGuard) (DeleteResult, error)
// errors.Is categories: ErrNotFound, ErrAmbiguousRequest, ErrProtectedRun.
```

The dedicated opener uses SQLite `mode=rw` for an existing file, returns `(nil, nil)` when it is missing and validates the existing compatible nonempty schema. It never creates parent directories/schema, converts the journal mode or runs retention. The existing read-only opener remains the list-runs path. SQLite's normal transaction coordination sidecars are not an application request to create or replace history. CLI supplies the local hostname and `LocalPIDState`; a missing PID callback is unknown, never proof of death.

Each mutation uses one `BEGIN IMMEDIATE` transaction for current schema/selection validation, unique request-prefix resolution, all selected-run guard checks, counts, deletes/cascades and removal of affected empty requests. A blocked or malformed selected row rejects the whole action without partial deletion. Validate the actual SQLite storage classes of identity/status/lifetime/owner operands before scan coercion; a REAL PID or BLOB status must not become plausible typed guard data. Unknown statuses, malformed IDs, missing parent identities and inconsistent status/end values fail closed. Validate fields needed for safe selection and writer eligibility, not discarded observation/result payloads or an unrelated full-database audit.

Finished, stopped and failed rows require a valid, non-NULL end timestamp. A raw `running` row requires NULL `ended`; it is eligible only when its stored nonempty UTF-8 hostname equals the current nonempty hostname exactly and its positive INTEGER PID is representable on this platform, then conclusively classified `PIDDead`. The valid range is 1–2147483647 on Linux and 1–4294967295 on Windows. Range-check before calling `LocalPIDState`, whose invalid-PID fallback cannot authorize deletion. Foreign-host, alive, inaccessible/unknown and unverifiable running owners block the entire selected action with `ErrProtectedRun`; malformed safety scalars are storage errors. Neither heartbeat age nor display `RunState == "lost"` authorizes deletion. Do not infer death from missing process-query privileges or rename/fix stored statuses. Finalized rows do not require their former owner process to have exited.

The immediate write reservation serializes against cooperative history writers: selection and eligibility cannot be checked in an earlier read transaction and reused for deletion. A later writer may create new history after the deletion commits; clear is an atomic removal of the selected transaction's rows, not a persistent ban on future runs. Preserve `sqlite_sequence` so follow's committed-round high-water marks remain valid after request recreation. Cancellation, lock failure or another error before commit follows the existing bounded rollback/discard path. The smallest shared transaction helper may expose whether COMMIT succeeded; retain existing callers' behavior and set `DeleteResult.Committed` at that actual boundary, even if later cleanup returns an error. Counts are meaningful as deletion results only after successful commit. No automatic retry follows a known committed result.

List-runs reuses `OpenReader`, `Store.View`, `Snapshot.Find`, `Snapshot.Request` and paged `Snapshot.Runs`; it needs no new storage listing API. Resolve one request and collect its run metadata within one view, close history before writing the report, and preserve existing read-only/error behavior. The CLI owner supplies dispatch and the small additive output reports while retaining current configuration, diagnostics, logging and signal handling. Storage and CLI implementation have separate owners under the development plan.

### 10.5 M8 live display and table layout (merged)

M8 merged in [PR #16](https://github.com/evdanil/gotest/pull/16) as `6bc5684726c888995d5fcd84d3c7c16951ec1307`, with independent Sol approval, completed external review and passing native Linux/Windows and candidate-archive checks. The development plan records the exact reviewed-head evidence; those unpublished candidates do not qualify version 0.2.0. Keep the existing 500 ms display callback and immediate phase/progress/result updates; the probe interval does not govern the visible clock/countdown. Fix repeated whole-frame erase/repaint in the terminal adapter, not by slowing the scheduler or hiding changing status. Preserve existing key handling, permanent notes, safe diagnostic routing, background/suspend behavior, failure handling and exact native mode/cursor restoration.

`Screen` retains only the last successfully presented frame's sanitized, styled row bytes and its geometry/view/layout identity. Obtain each row through the existing styled writer into a private buffer and compare those exact bytes, including ANSI style, rather than raw `LiveUpdate` values. With unchanged dimensions, view/request layout and row count, identical rows produce no output; changed rows are overwritten in place, then any old suffix is erased with the default style. Do not blank a row before rewriting it; assemble the cursor-relative patch into one write where practical and leave the cursor on the final frame row. A clock-only header change must not delete/repaint the unchanged body or scroll the terminal. Publish the new cache only after all required writes succeed; an output failure follows the existing drawing-disable/cleanup behavior.

At the right edge, preserve delayed autowrap until an explicit carriage return cancels it. Reset SGR before erasing a shortened row's suffix; omit erase-to-end for a row that fills the terminal width, because its cursor can still address the last character. Changed-row patches finish at column zero on the last frame row without a newline. Full frames start at column zero, use explicit CRLF between rows and finish with CR, so a full-width bottom row neither scrolls nor advances twice. Windows acquires processed output, VT processing and `DISABLE_NEWLINE_AUTO_RETURN` alongside the saved output mode, then restores that exact original mode and cursor metadata. Account for the acquired mode's LF behavior in console `Note`/`Change` lines without changing redirected report/log bytes. A mode bit alone is not proof of actual edge behavior.

Dimensions, row count, grid/table view or request-layout changes require full replacement. Frame removal and permanent `Note`/`Change` output invalidate the presented cache. Drawing ineligibility/foreground loss, output outside the owned frame and suspend/resume also discard ownership of its cursor anchor and row count: do not later delete rows relative to a cursor another owner or permanent note may have moved. Reacquisition starts a fresh frame at the current cursor, preserving permanent output; hide the cursor again only when drawing resumes. Invalidation from signal/key handling must not wait for the draw mutex, and a notification arriving during output or cache publication must survive for the next draw. Resume and emergency cleanup retain their existing bounded paths. Reuse the current styled writer; no terminal framework, background renderer, new monitor DTO or production test hook is introduced.

For human report tables, compute each column's natural sanitized display width from its header/cells, at least one cell. Subtract the existing inter-column separators from the terminal budget. Use natural widths unchanged when their sum fits. Otherwise use the largest shared cap whose sum of capped natural widths fits, then distribute the remaining cells to still-unsatisfied columns in stable column order. This transfers spare space from short columns instead of imposing a uniform cap on every column. Retain the existing tiny-width vertical fallback, grapheme-safe wrapping and styles. A 25-character UTC timestamp remains on one line when the complete natural table fits; do not shorten timestamps or change their values to mask the layout defect.

The live `v` table adds a `State` column using the existing `LiveRow.Cell`, and renames its `Latest` header to `ICMP`. Match the existing glyph/style semantics, including applicable partial-reply highlighting; do not derive a new classification from the displayed ICMP text. The legend describes `!` as an ICMP reply and `T` as a TCP response: open or closed TCP can produce `T` when neither echo nor target rejection wins. `T` does not guarantee an open port. `--tcp` adds probes alongside ICMP. The live DTO deliberately has no per-port map; exact port outcomes remain in the final reports. Do not add live TCP columns, alter final report headers, or change JSON/CSV/Markdown schemas or probe/exit semantics in M8.

Source byte-output tests prove duplicate suppression, changed-row/suffix updates and invalidation; focused layout tests prove width allocation and existing state reuse. Native Linux PTY and Windows private-console tests passed actual display-update, control, resize and restoration checks, including right-edge/bottom-row output and a subsequent update. On Windows the edge fixture makes physical buffer and viewport dimensions agree, checks the last-column character, prior rows, final cursor and unchanged viewport, then restores exact modes, cursor and fixture geometry. Linux raw PTY bytes witness emitted control sequences rather than a terminal emulator's rendered pixels; Windows screen-buffer readback proves actual content, not the absence of transient intermediate flicker. Retain the existing required native suite and its blocked-output/loss cases.

### 10.6 M9 version 0.2.0 qualification and delivery (complete)

M9 tooling merged in [PR #17](https://github.com/evdanil/gotest/pull/17) as `968ea23aed5541e86860bde45743ee054ad4a4d8` after the required checks and reviews. It advances the existing strict release-tooling version to **0.2.0**, delivering M7 history management and M8 display fixes through the section 10.3 archive/receipt/native-qualification/publication model. Keep the published `v0.1.0` tag, source and assets unchanged. This is a bounded version update, not a generic version framework, dual-version verifier, new archive format or binary-stripping change. The existing private version/revision interface and development output remain unchanged.

The published 0.2.0 asset set is exactly `cn-mon_0.2.0_linux_amd64.tar.gz`, `cn-mon_0.2.0_windows_amd64.zip` and `SHA256SUMS`, with the same fixed payload and matching top-level directories. The strict version checks, archive names, workflow dispatch/defaults, native markers, receipts and corresponding fixtures now consistently use 0.2.0. Retain Go 1.27.2, CGO-free builds, existing build flags and canonical archive verification. The process-free artifact tool validates offline source/build identity and timestamps; native qualification must still prove exact `cn-mon 0.2.0 (<full 40-hex source commit>)\n` output and bind that version/revision to the archive and extracted-binary hashes. Do not infer private linker values from trimmed build metadata.

Retain all **30 acceptance IDs** through the existing source/component mapping plus actual extracted-command evidence. Run the M7 management and M8 display cases against `CNMON_NATIVE_COMMAND_BINARY` on both native platforms, with the same strict selectors/markers and no fallback or product rebuild. Preserve source-only fault/format/protocol evidence as such instead of duplicating it into a second black-box suite. Role copies and extracted originals must retain their verified hashes; record actual permission outcomes, toolchain and OS facts. Controlled PATH plus source review and native probes remains evidence of no runtime ping dependency, not physical absence of the OS executable.

PR candidates remain unpublished. After the tooling milestone passes current source/native/candidate-archive checks and both reviews, rebuild and requalify from the exact merged main SHA containing M7, M8 and M9. Publication consumes those successful-run bytes without a rebuild or repack. Apply section 10.3's absent-tag/create-only-ref/draft `--verify-tag` sequence to `v0.2.0`, verify its exact commit target and downloaded asset hashes before and after publication, and retain the same honest partial-failure/recovery policy. No existing tag is retargeted and no different asset is overwritten. A later documentation-only evidence update may record the release without changing its source, tag or assets. M9 completed these gates: [final run 38119825919](https://github.com/evdanil/gotest/actions/runs/38119825919) built, qualified and published fresh archives from exact merge `968ea23aed5541e86860bde45743ee054ad4a4d8`, with all 13 jobs successful. The lightweight `v0.2.0` tag points directly to that commit, and downloaded public assets match the qualified bytes. The development plan records the digests and observed native roles; the archives retain their revision-pinned documentation snapshot.

### 10.7 M10 rerun implementation (merged)

The implementation is a small command extension with schema 1 and the current dependencies. The parser recognizes `modeRerun` distinctly for conflict/value validation; after saved selection, the command routes through the shared target-run startup and `monitor.RunPlan`/`monitor.Run` pipeline. Explicit scheduling flags stay distinct from restored numeric durations passed directly into the plan. The runner, signal and terminal lifecycle remain shared.

The command-local selection helper uses the existing read-only opener and `Snapshot.Find` plus `Snapshot.Request` within one `View`; `RequestInfo.NewestRun` supplies the required start/rowid ordering. It returns detached request/schedule/source values after successful transaction completion and close. Selection outcomes are retained separately so they cannot mask a database or cleanup error. No history query framework, migration, settings snapshot table, persisted rerun relationship or exact-run selector is introduced.

The narrow `target.Restore` operation uses the existing identity JSON/SHA-256 encoding over the **saved arrays**. It checks valid canonical target tokens in sorted unique order, sorted unique in-range ports under the existing five-port limit, distinct valid single-address/DNS host strings and the existing 1,024-host limit when TCP is selected. It preserves accepted host spelling/order and stored key spelling, comparing the digest case-insensitively. Valid noncanonical saved address spelling is accepted without normalization; lists, network syntax, malformed numeric addresses, mapped IPv4 and unscoped link-local IPv6 are rejected as probe hosts. Never substitute freshly expanded hosts or require that current expansion equals the saved expansion. The pinned Python schema-1 fixture deliberately has two canonical targets and seven saved hosts; a compatible replay must keep all seven and its original identity. Readers retain their existing broader historical display contract; stricter replay validation belongs at this execution boundary. Empty target/host arrays are a no-work selection error; malformed arrays, invalid hosts/ports/tokens and a mismatched key are operational errors.

Stored interval must be absent or positive and representable as `time.Duration`. A stored end must yield a positive representable `until - started`; verify the inverse addition equals the decoded end so `time.Sub` saturation cannot silently become a different duration. Preserve independent nullability supported by the existing engine: an absent interval remains single-round unless this invocation explicitly supplies a schedule flag that establishes an interval. Read/validate the entire selected schedule before applying either override. Fresh origin, UUID, default history inclusion and runtime settings use the ordinary run path.

The existing live layout derives target blocks from tokens. It now appends saved hosts not represented in those blocks to the existing `Hosts` block in stored order, preserving ordinary target-block ordering and overlap behavior. A noncanonical saved IP spelling goes into this fallback when the subnet grid's canonical-text lookup cannot represent it. Restored hosts participate in the normal grid/counters, seen-host live table and reports without changing the saved arrays.

M10 merged in [PR #19](https://github.com/evdanil/gotest/pull/19) as `8b145ceb557f392b96579f18c0705e407cb77ac1` after independent design/code approval, [completed external review without findings](https://github.com/evdanil/gotest/pull/19#issuecomment-6106866438), and 20 successful checks with two intended skips at reviewed head `a6713f55a67082fa525756860d7e7d1e38f312cf`. Source and actual native Linux/Windows suites and unpublished candidate-archive qualification passed. The existing 30 acceptance rows remain unchanged, with **RUN-04** covering this extension. The development plan records the exact jobs and evidence limits. Required permission roles remain intact: healthy roles require full ICMP success, while an allowed denial requires the same-role typed OS cause, remedy and report/history agreement. The rerun command test and required marker already participate in both native qualification selectors and consume the verified extracted binary through `CNMON_NATIVE_COMMAND_BINARY`; they must remain active in M11.

### 10.8 M11 version 0.3.0 qualification and delivery (complete)

M11 tooling merged in [PR #20](https://github.com/evdanil/gotest/pull/20) as `bd1ac548c22c0c49a54e2ce964a2f0a4da48a1ed`, following M10. It advances the strict release version to **0.3.0**, delivering saved-batch rerun with the monitor, history management and display behavior already merged. It reuses section 10.3's archive/receipt/native-qualification/publication model. This milestone changed release tooling, its tests and documentation only: no production behavior, dependency, schema, platform, build/strip flag or general version framework change. The private version formatter and `dev` default stay generic; historical formatter examples are not current-release guards.

The published asset set is exactly `cn-mon_0.3.0_linux_amd64.tar.gz`, `cn-mon_0.3.0_windows_amd64.zip` and `SHA256SUMS`. Each archive retains the existing payload under the matching versioned top-level directory; SHA256SUMS retains exactly two ordered lowercase digest/filename lines. Strict version inputs/guards, archive paths/allowlists, receipts, workflow choice and PR default, native assertions/markers and offline fixtures now use 0.3.0. They reject obsolete 0.2.0 and mixed versions/names/receipts while retaining source/revision/hash and cleanup protections. Go 1.27.2, CGO-free native builds, current checksums/notices and pinned Actions remain unchanged. Exact extracted output must be `cn-mon 0.3.0 (<full 40-hex source commit>)\n`, independently checked against archive/binary hashes and the same-source receipt. No offline inference of private linker values is added.

Retain all **31 acceptance IDs** through the existing M6 26-ID evidence map, M7's CLI-03/HIS-03/DB-04, M8's TTY-02 and M10's RUN-04. Existing rerun source tests cover selection, legacy arrays, numeric schedules and failures; the already-selected native rerun test exercises the exact extracted product on both OSes. It creates the source batch through that product, discovers its key, removes the input file, applies the bounded override, and checks a distinct run, source preservation, JSON/history agreement and invalid-selection no-write behavior. Preserve required markers, Linux granted/denied and Windows ordinary/elevated role checks, terminal/signal gates, no-rebuild extracted-path selection and before/after hashes for original and role-copy binaries. Source-built, cross-compiled and unpublished 0.2.0-candidate results do not qualify the new 0.3.0 bytes.

README has one 0.3.0 archive installation guide with checksum verification before extraction on both OSes. The archives retain the truthful prepublication documentation snapshot from their exact source revision. Current repository documentation records the subsequently verified 0.3.0 publication without replacing those archive bytes. Rerun is included in 0.3.0; published 0.2.0 binaries remain unchanged.

Design and independent implementation reviews approved the bounded update without outstanding findings. Genuine strict-version artifact and offline workflow failures were recorded before changing production pins, followed by passing local source/tooling checks. At reviewed head `b1431fcf53b8482648187e8be9f5a3aacb105f57`, all 20 applicable source/native/candidate checks passed, with two intended skips, and [external review completed without findings](https://github.com/evdanil/gotest/pull/20#issuecomment-6107014787). PR #20 merged with no review threads. The development plan records the exact source/native/candidate jobs and separates them from final release qualification.

The authorized final dispatch from `main`, [run 38124176778](https://github.com/evdanil/gotest/actions/runs/38124176778), freshly built and qualified both platform archives from exact M11 merge `bd1ac548c22c0c49a54e2ce964a2f0a4da48a1ed`. All 13 source/native/qualification/publication jobs succeeded. Publication consumed those exact successful-run bytes without rebuild/repack, proved `v0.3.0` absent, created its lightweight ref once, verified its commit target, and used draft `--verify-tag` plus before/after downloaded-asset comparison. The release was published at `2026-10-11T08:24:40Z`; its tag points directly to that commit, and all three public hashes match the qualified bytes. Preserve section 10.3's partial-failure recovery rules; no existing tag is retargeted or different asset overwritten. This later evidence update records the completed release while preserving its tagged source and archive snapshot.

### 10.9 Windows Store distribution (packaging implemented; qualification incomplete)

The current distribution remains the qualified Linux and Windows archives described above. The approved MSIX mapping is x64 package `1.3.0.0` containing the unchanged CLI `0.3.0`, with a full-trust `cn-mon.exe` execution alias. Four runtime identity inputs are protected GitHub environment secrets; the public manifest is rendered only in staging, PR jobs use synthetic identity, and the trusted manual workflow uploads only a sanitized status summary. No MSIX has been submitted or published.

Native installed-package qualification must verify ordinary-user CLI/ICMP behavior, both `.cn` configuration layers, actual filesystem paths, console/stream/exit-code behavior, install/upgrade/repair/uninstall, the `runFullTrust` declaration and WACK. The latest synthetic run passed all 16 core gates and all 24 WACK tests, including required `dpi_awareness`, but the formal WACK gate remains incomplete pending exact-schema parser validation; repair and command-conflict/Settings-alias recovery also remain incomplete. Store acceptance is unverified. Detailed run evidence is centralized in the [MSIX packaging plan](MSIX_PACKAGING_PLAN.md). Microsoft documents that Windows 10 1903 and later virtualizes newly created AppData files/folders while writes to existing files use the unvirtualized file, and that Windows 10 1809 virtualizes all AppData writes; virtualized entries are removed at uninstall ([AppData virtualization guidance](https://learn.microsoft.com/windows/msix/desktop/flexible-virtualization)). The CLI's default history and log paths resolve under `USERPROFILE`, outside AppData, so this lifecycle exception applies only to selected AppData paths.

## 11. Acceptance scenarios

These IDs are implementation/review traceability, not claims that the tests already passed.

| ID | Required observable result |
| --- | --- |
| CLI-01 | Every mode/option combination, retained short/long alias pair, interspersed options, no-argument invocation, help/version, invalid durations/IDs/ports, and exit precedence behave as specified; excluded framework flags are usage errors; no prompt occurs |
| CLI-02 | BOM/comments/file/stdin/duplicate `-` cases collect exact targets once; `-f/--file` are equivalent, mixed repetitions use the final value, and reader modes reject either spelling before reading input; unreadable and invalid UTF-8 input exit 2 |
| CFG-01 | Source-derived fixtures cover executable/user/explicit INI layers, per-key CLI overrides, final empty repeated `-c` and `-l` values ignoring earlier CLI values, symlinks, relative/home paths, missing/unreadable layers, BOM/grammar/DEFAULT/case/duplicates, literal percent/inline comments, all-settings fallback on parse error, blank/dot history paths, invalid/nonpositive INI retention, and retention preserved when CLI is omitted |
| CFG-02 | Both spellings of `-h/--help` and `-v/--version`, and invalid CLI/modes, perform no configuration/target/log/history I/O; requested input is read before startup; log location/level/append/fallback/failure behavior matches fixtures; unrelated settings activate nothing and their values are never dumped |
| TGT-01 | Canonicalization reproduces the golden SHA-256; reordering/duplicates/case/host-bit CIDRs agree when accepted canonical target sets, ports and expansion agree; changed accepted sets, trailing-dot names, ports and expansion distinguish requests |
| TGT-02 | `/16`, `/112`, `/31`, `/32`, `/127`, `/128`, equivalent IPv4 dotted netmasks/hostmasks, invalid masks, reserved/loopback/multicast exclusions, mapped IPv6, scoped link-local and malformed numeric tokens match fixtures; TCP boundary cases preserve pre-canonical input-order admission, equal accepted sets after reordering give equal keys, changed admitted sets may give different keys, and case-variant names count separately before canonicalization |
| DNS-01 | Names resolve IPv4-first afresh each round; DNS address changes affect both probes together without changing request ID; unresolved/resolved transitions and cancellation are correct |
| PRB-01 | Native IPv4 and IPv6 loopback each send/count two distinct requests; one reply is partial, duplicate/out-of-order replies never inflate count, and 3-second silence terminates |
| PRB-02 | Packet fixtures cover each supported ICMP error type/family, target/intermediate sender, minimum quote, IPv6 extensions, malformed/truncated/unrelated/late packets, reply-over-error precedence, filter acceptance, and senderless Windows behavior |
| PRB-03 | Native Linux and Windows tests actually exercise echo and controlled ICMP errors through OS sockets; Linux capability denied/granted and Windows ordinary/elevated outcomes are recorded; no shell probe is invoked |
| PRB-04 | Controlled local TCP servers/refusals plus timeout/route/resource-error fixtures produce all five states; closed counts seen but open alone supplies TCP success; cancellation closes sockets |
| MOD-01 | `!RT` seen, `x`, sticky `U`, first-round absence of change events, partial highlight, and pending-versus-committed state follow the precedence table |
| MOD-02 | Seen times/counts/last address and every period match deterministic fixtures; exactly 2.5 intervals stays joined, a larger gap splits; fewer than three cron samples do not imply continuous coverage |
| MOD-03 | Concurrent start-order/commit-order inversion never moves last-seen backwards; open period requires the owner of the latest round to be live |
| RUN-01 | Fake clocks prove aligned slots, bounded duration, default interval, overrun skips, exact-slot completion, backward/forward clock jumps and suspend behavior without sleeping through long tests |
| RUN-02 | Cancellation in DNS, queued work, active probing, waiting, history lock/read, and finalization restores terminal promptly; incomplete rounds vanish; a completed round stopped during commit remains |
| RUN-03 | Earlier snapshot/since filtering, cancelled single-round cron-spacing correction, current-round counts, retained success with new silence, and current-versus-earlier failure precedence are exact |
| DB-01 | Transaction fault injection never leaves half a round or orphan request; killed writers retain completed rounds; read/write failures continue in memory and exit 3 |
| DB-02 | Existing schema-1 history at default or INI-configured paths is read/continued without `--history-file`; configured retention is honored before pruning; CLI overrides select another file/duration; Python fixtures are read/appended correctly and cn-tool reads Go-written fixtures; IDs/host positions/rows are stable; foreign/corrupt/newer DBs remain unchanged |
| DB-03 | Readers do not create missing history or mutate history/schema/statuses; SQLite coordination sidecars are distinguished from history changes; concurrent writers/readers, lock timeout/cancel, retention, live zero-round runs and long-interval runs behave correctly |
| HIS-01 | List ordering/counts/states, prefix ambiguity, missing history/ID, local PID disappearance, heartbeat threshold, and inaccessible PID outcomes are correct |
| HIS-02 | Follow sees new runs, late commits, median changes, pruning and same-key recreation; stop affects only follower; completed-round changes are emitted once |
| OUT-01 | Golden JSON validates exact sections/types/timestamps/nulls, historical sparse latest rows and exit-independent available output; live/status never contaminates stdout |
| OUT-02 | CSV union/quoting/LF, Markdown escaping/all periods, omitted empty human-format sections, all-empty output, table period limit and all four source theme styles match fixtures; INI/CLI theme precedence, unknown-INI fallback and no-color behavior are correct |
| TTY-01 | Both OSes demonstrate grid/table toggle, q/Ctrl+C, resize/crop, restored console modes and stdin redirection; Linux additionally demonstrates foreground/background, suspend/resume, SIGTERM/SIGHUP and nohup |
| REL-01 | Native full CI, race checks where supported, bounded-concurrency/cancellation tests, dependency/license review, CGO-free binary smoke tests, artifact contents/checksums and refreshed docs pass |
| CLI-03 | New history modes preserve early-exit/config/log ordering, reject incompatible input before I/O, never probe or acquire terminal modes, implement all four reports/exits, and truthfully distinguish committed deletion from later cleanup/output failure |
| HIS-03 | List-runs discovers full exact run IDs and canonical parent keys in one snapshot, preserves order/status/state/timestamps/counts and control-character round trips, and distinguishes an existing empty group from missing/ambiguous selection |
| DB-04 | Exact run deletion, unique-prefix group deletion and logical clear atomically guard all selected writers and remove only intended rows; malformed/foreign/alive/unknown owners fail closed, same-host confirmed-dead running rows are eligible, rollback/lock/cancel and post-commit failures are truthful, and schema/files/sequences/read snapshots remain compatible |
| TTY-02 | Identical presented frames emit no bytes and stable-layout updates rewrite only changed rows; invalidation restores full redraw, wide tables retain fitting timestamps, and live State/ICMP labels match existing observations without changing structured reports, controls or native restoration |
| RUN-04 | Rerun selects the unique saved batch and newest run in one closed read-only snapshot, preserves saved identity/host order and source provenance, restores numeric configured scheduling with explicit-only overrides, and starts a distinct native run through the normal lifecycle; missing/ambiguous/empty/corrupt/cleanup cases fail before startup, concurrent deletion/pruning is safe, and both supported OSes demonstrate the command |

The detailed TDD sequence, review gates, and current evidence limits are in [DEVELOPMENT_PLAN.md](DEVELOPMENT_PLAN.md).
