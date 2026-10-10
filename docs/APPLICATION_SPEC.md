# cn-mon application specification

Status: [M0 specification](https://github.com/evdanil/gotest/pull/7) and [M1 foundation](https://github.com/evdanil/gotest/pull/8) are merged. M2 probe components are implemented and validated natively on Linux and Windows in [PR #9](https://github.com/evdanil/gotest/pull/9); external review and merge remain pending. M3 history is next after that gate. Operational target commands arrive in M4 and history-reader commands in M5.

Destination: `evdanil/gotest`. Product and executable: **cn-mon** (`cn-mon.exe` on Windows).

## 1. Purpose and compatibility boundary

Build a standalone Go CLI retaining the Ping Monitor behavior of `evdanil/cn-tool` 0.8.0, commit `2e2f3c40ef6718486f1ca4477f9145029ec495fa`. The reference is its `README.md`, `cn_tool/modules/ping_monitor.py`, `bulk_ping.py`, and `utils/ping_history.py`, `ping_store.py`, `ping_grid.py`, `ping_live.py`, `cli_input.py`, and `render.py`. When reference prose and code differ, the explicit contract below governs; characterization fixtures must record the source revision and Python version used.

Supported release targets are **Linux x86-64** and **Windows x86-64**. ICMP is implemented in Go using maintained, permissively licensed Go code and OS socket APIs. Neither platform may execute `ping`, PowerShell, a shell, or another helper to perform network probes. The delivered binary needs no Python, C compiler, separately installed SQLite library, or network service.

Retain IPv4/IPv6 targets, optional TCP probes, repeated and single-round runs, persistent history, request identity, sampled online periods, live terminal grid/table, `q`/`v`, signals, `--list`, `--show`, `--follow`, table/JSON/Markdown/CSV output, and the monitor-effective cn-tool INI settings for history, theme and logging. Existing configured history paths/retention remain effective; their fallbacks remain `~/.cn-ping-history.db` and 30 days. Deliberate changes are the standalone command name, additional CLI overrides for history settings, native ICMP error handling, and the two supported platforms. Native error attribution may improve upon Windows' old text-parser limitations, but must never invent a sender.

The approved compatibility exceptions are removal of the interactive menu/prompts, XLSX and `--report`/`-r`/`--report-file`, and Python `process_data` hooks/custom-plugin framework, together with the platform restriction to Linux and Windows x86-64. Retain built-in monitor behavior and the monitor-effective INI settings defined here. Also exclude other cn-tool modules and their credentials, a daemon/service installer, web UI, notifications, alerts, authentication, packet capture, traceroute, arbitrary packet construction, RTT analytics, and a general-purpose configuration framework. There is no background service; cron or Windows Task Scheduler invokes the CLI.

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

Examples:

```sh
cn-mon 10.1.2.0/24 --for 2h --every 1m
cn-mon 10.1.2.0/28 --every 30s
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

M2 component evidence: [Linux](https://github.com/evdanil/gotest/actions/runs/38037349729/job/114170451322) and [Windows](https://github.com/evdanil/gotest/actions/runs/38037349729/job/114170451856) native jobs passed for commit `80541a7` in PR #9. The controlled harnesses exercised IPv4/IPv6 echo, target/intermediate error delivery and correlation, and platform privilege contexts through actual sockets. Windows error injection verifies correlated callbacks after the first echo and before the second, preserving echo precedence; it does not claim an echo-suppressed Windows rejection-only aggregate. [Run logs and artifacts](https://github.com/evdanil/gotest/actions/runs/38037349729) retain the observed privilege outcomes. Command output/exit-code integration remains M4 work, and release-binary qualification remains M6 work.

## 5. Rounds, scheduling and shutdown

One completed round contains one observation for every canonical host, the full latest rows in memory, details for seen hosts, the round start, and per-seen-host batch timestamps. Missing DNS is a completed observation. A cancelled partial round contributes no observations, summary counts, online periods, execution-failure flag, or persisted round. Live pending cells are provisional only. Once all hosts have completed, a subsequent stop during committing keeps that completed round.

Take the run origin before reading earlier history. Repeating slots are `origin + n*interval`. Begin the first eligible round immediately after setup if the end has not passed; subsequent round starts remain on those slots. After overrun, skip covered slots, never run overlapping/catch-up rounds, and report skipped slots in live/status output. A round ending exactly on a slot may use that slot; otherwise use the next slot. No new round begins at or beyond `origin + duration`; a round already in progress may finish past the end. A warning estimates a slow sweep before the first round using batches and the probe deadlines, and actual skipped slots remain observable.

Inject wall and monotonic clocks. Run timestamps never move backwards: absorb a wall-clock rollback using monotonic elapsed time. Preserve forward wall-clock jumps/suspend gaps and end a duration-limited run that resumes past its end. Heartbeats use real UTC wall time, because other processes read them. Scheduled waiting and key polling remain responsive (at most 500 ms between control checks); no wall-clock change causes an indefinite wait. DNS/probe work writes heartbeats at least every 10 seconds while busy; idle scheduled runs remain live under the interval-based stale threshold.

First Ctrl+C or `q` cancels ongoing work, discards an incomplete round, restores the terminal, finalizes the run as stopped, and prints the summary of completed rounds. The first Ctrl+C is a graceful stop, not automatically exit 130. A second Ctrl+C exits 130 promptly without waiting for network operations or SQLite lock timeouts; best-effort cleanup must not delay it. `q` does not increment the Ctrl+C count. Linux SIGTERM/SIGHUP perform graceful stop; preserve signals ignored on entry, including `nohup` behavior. A killed process/closed Windows console may leave a running row, later displayed as lost.

At target-run start, read one consistent snapshot of retained same-request rounds with starts before the run origin and on/after the since cutoff. Those are `earlier_rounds`; do not continuously import other processes' new rounds into an active writer's summary. Then fold this run's completed rounds. If a one-round run finishes, include its start when inferring cron spacing; if it stops before completing, exclude that hypothetical sample. Reading failure does not use a half-folded earlier snapshot: continue with this run in memory and exit 3.

## 6. Seen state and sampled online periods

Only `!`, `R`, and `T` mean seen. Display the current seen character; otherwise a host seen earlier in the selected window is `x`. A never-seen host becomes sticky `U` after unreachable/no-route and stays `U` through later silence/error/name failure until it is seen. Other never-seen hosts display `E`, `?`, `.` or blank before probing. Partially replying `!` is highlighted in the warning color; a changed seen/not-seen state is highlighted separately.

`seen` contains every host seen at least once, in canonical host order, with first/last seen sample time, seen-round count, total included round count, latest result, last known responding address, and every sampled online period. If no longer seen, latest is the current observation's result and its TCP summary fields are blank, rather than stale port states. `answering` counts hosts seen in the latest completed round, and includes `R`/`T`.

A period starts with a seen sample and ends at the last seen sample before a not-seen round or a coverage gap. These are **sampled periods**, not a claim of uninterrupted availability or an uptime percentage. A gap exists when round starts are more than 2.5 times the larger expected spacing of adjacent rounds apart; equality does not split. For interval-less single-round runs, infer spacing from the lower median of consecutive sorted start gaps only after at least three samples. With no expected spacing, separate samples are separate periods. A gap splits periods even when both bounding samples see the host.

Sort stored rounds by `(started, round_id)`, because concurrent runs can finish out of order. Clamp successive per-host sample times so last-seen and period endpoints cannot move backwards when an earlier-started slow round sampled later. Model memory holds aggregate host state and required online periods, not all prior rounds; storage reads are paginated. Unavoidable memory is proportional to hosts plus reported periods, not a claim of fixed memory regardless of history.

Compute inferred single-round spacing from the selected round starts before folding and supply that fixed value to the tracker. If new samples or pruning change the inferred spacing, rebuild from the selected rounds; the aggregate tracker does not retain all starts or retroactively repair period boundaries. A tracker folds already ordered complete rounds. Provisional observations affect its display methods only, without committing counts, times or periods.

An online period has `to: null` only when the host was seen in the latest included round **and the run owning that round is currently live**. Another live run alone is insufficient. A completed writer's final output closes its own periods at their last sample; show/follow can report open periods of a live request.

## 7. SQLite history and read modes

Use `modernc.org/sqlite` through `database/sql` for a native, CGO-free binary. Pin an audited version and its complete transitive module graph in `go.mod`/`go.sum` when storage is first implemented in M3; M1 does not import an unused driver. Preserve the reference **schema version 1 read/write contract** and effective INI/CLI history settings from section 2.1, with fallback `~/.cn-ping-history.db`. Existing history at the configured path is used without a new flag; `--history-file` overrides it when desired. No import command, automatic migration, search for other database paths, or overwrite is required. Continuing an old database is supported only after interoperability tests pass; stored keys and positional host lists are never recomputed or rewritten.

Logical schema (column names and meanings are compatibility requirements):

| Table | Columns |
| --- | --- |
| `meta` | `key` primary key, `value`; `schema_version = '1'` |
| `request` | autoincrement integer `id`; unique `key`; JSON `targets`, `tcp_ports`, `hosts` |
| `run` | text UUID `id`; `request_id`; nullable `source`; `started`, nullable `interval`, nullable `until`; `host`, `pid`, `heartbeat`; nullable `round_seconds`, nullable `ended`; `status` |
| `round` | autoincrement integer `id`; `run_id`; `started`; positional `observations`; `answered` |
| `result` | `(round_id, host)` primary key, without rowid; `address`, sample timestamp `t`, `result`, nullable JSON `tcp` |

Retain foreign keys with delete cascades, indexes `run_by_request(request_id, started)` and `round_by_run(run_id, started)`, epoch-second REAL timestamps, decimal-string TCP map keys, reference observation alphabet `!RTU.E?`, and statuses `running`, `finished`, `stopped`, `failed`. `answered` is the number of seen detail rows. A run stores its input file basename in `source` where applicable. Request/run creation is one transaction; each completed round plus all seen result rows is one transaction. Preserve autoincrement non-reuse of round IDs. No observation string may be shorter/longer than its stored host list; no incomplete round is committed.

Enable foreign keys, WAL, a bounded 10-second busy wait, and short transactions. Writers create missing parent directories and a new schema only in an empty database, after confirming no foreign tables/schema exist. Unknown, malformed, corrupt or newer schemas fail safely without journal-mode changes, migration, pruning, or other mutation. SQL values are parameterized. Validate decoded data before indexing/allocating; malformed rows produce a history error rather than a panic or partial summary. Ensure cancellation can interrupt lock waits and reads.

Prune rounds older than the retention cutoff at run startup, cascading results. Delete runs with no remaining rounds when finalized or truly old/stale; preserve live runs awaiting their first/next round, including intervals longer than retention. Delete requests only when no runs remain. A writer encountering its first history read/write/finalization failure closes that store, reports the failure once, continues with in-memory rounds, prints the available summary, and exits 3; never claim unsaved data was persisted.

Readers (`--list`, `--show`, `--follow`) open read-only and never create the database or parent directories, prune, update run status, set WAL, or checkpoint. Do not use SQLite `immutable=1` for a changing WAL database. SQLite may use WAL/SHM coordination files during read-only access; this contract guarantees no history/schema mutation, not byte-identical shared-memory files while writers are active. If the SQLite driver cannot access required sidecars with available permissions, report a history error; do not silently reopen as a writer. Tests verify no database writes or missing-history creation by reader modes. Missing history means an empty list/exit 1, or missing ID/exit 2 for show/follow.

`--list` includes every recorded request, newest activity first (latest round or newest run start), with deterministic tie-breaking. Its targets label prefixes the newest run's file basename, then comma-joins canonical tokens until adding another would exceed 44 characters, always retaining the first token and suffixing ` +N` for omitted tokens; this label also appears in JSON. State is the newest live run if any, otherwise newest run. A stored `running` run is **lost** when heartbeat age is greater than `max(2.5*interval, 60s)`, or its local-machine PID is definitely gone. An inaccessible PID is not proof of death. Lost state is computed by readers and not persisted. Include zero-round active runs. Display running progress/next slot, finished, stopped early, failed, or lost-at-heartbeat detail.

`--show` reads a consistent snapshot, applies the fixed since cutoff, folds rounds chronologically, and prints a summary without probing. `--follow` requires stderr to be a terminal, polls at most every 2 seconds, displays that request while any of its runs is live, and prints a final summary when all end or the follower is stopped. `q`/Ctrl+C stop only the follower. If nothing is live initially, print a note to stderr and the summary immediately.

A follower uses a transaction-consistent request state, round high-water mark and ordered pages. It must notice new runs, out-of-start-order commits, new one-round samples changing inferred spacing, pruning of already folded rounds, and a request pruned then recreated under the same key/new integer ID. Rebuild the selected window when incremental folding is invalid; emit each newly committed change only once. A final follow read after `q` includes rounds committed since the preceding poll. Keep database snapshots short and release them between polling cycles.

## 8. Terminal and report contract

All final reports go to stdout; diagnostics, progress, changes and live frames go to stderr. JSON/CSV stdout must remain parseable while the live screen is active. Output errors other than an ordinary closed downstream pipe are reported as failures (exit 3); a closed pipe terminates quietly. The utility requires no secrets and emits no telemetry.

Live drawing requires stderr TTY and, on Linux, foreground process-group ownership. Keys require stdin TTY and foreground ownership. Do not consume redirected target data as keys. A background job, cron invocation, or job moved to the background draws nothing and reads no keys; drawing may resume after `fg`. A disappearing terminal disables drawing without aborting useful work. Restore terminal input/console modes and cursor on graceful stop, normal exit, and errors. Handle Windows console modes and Linux suspend/resume explicitly; arrow/function-key escape sequences must not become `q`/`v` commands.

The grid has a block for each canonical subnet and a `Hosts` block for single literals/names. Preserve subnet address positions, leaving excluded addresses blank; overlapping target blocks may reference the same host. Choose the largest fitting row length of 64, 32 or 16 cells, grouped by eight. If the grid cannot fit, use one counter line per target block. Fit header, counters, legend and footer to terminal width/height, counting display cells rather than bytes. Show only present legend characters, bounded change lines, and `(+N more)` when cropped. Recompute on resize. `v` toggles to a live table of hosts seen so far with Host, Latest, Last seen and Seen counts, including provisional current-round observations; another `v` returns to the grid. `q` stops; `Q`/`V` are equivalent to lowercase. No alternate menu or target prompt exists.

Table output defaults to readable section tables. Its online-period cell shows the last three periods and `(+N earlier)` when needed; JSON/CSV/Markdown keep all periods. Apply the configured/overridden theme from section 2.1. Color is disabled for redirected output and when `NO_COLOR` is set; meaning remains legible without color. Sanitize control/escape characters in human terminal text without corrupting structured values.

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

Use Go modules and pin the development/CI toolchain (initial validated toolchain: Go 1.27.2; dependency floor currently Go 1.25). Build release artifacts with `CGO_ENABLED=0`; the refreshed cloud setup helper activates `GO111MODULE=on` and `GOTOOLCHAIN=local`, so no additional module-mode override is required. Do not make a runtime toolchain download a hidden prerequisite.

All new first-party code and documentation are **Apache-2.0**, allowing reuse without a requirement to disclose derivative source. M0 replaced the destination's original GPL-3.0 project declaration for this authorized utility; inventory retained material and do not copy third-party GPL/copyleft code. Changing a root LICENSE cannot relicense someone else's material. Preserve cn-tool Apache attribution for adapted logic; preserve pro-bing's root MIT license and Apache source headers rather than asserting one overrides the other; retain Digineo MIT attribution if code is copied. Include `NOTICE`/third-party notices as applicable and an auditable dependency-license inventory, including SQLite bindings/transitives and any UI library. No GPL, LGPL, AGPL, SSPL, or other source-disclosure/copyleft runtime or build dependency is allowed. Pin/checksum modules and run dependency vulnerability checks proportionately; do not disable verification or claim a license audit before performing it.

Release versioned `cn-mon_<version>_linux_amd64.tar.gz` and `cn-mon_<version>_windows_amd64.zip`, each containing the binary, concise usage/permission documentation, LICENSE and third-party notices, plus published SHA-256 checksums. `--version` reports version and revision. Run the built binaries on both native OSes; publish the tested OS/toolchain versions and the actual ICMP permission requirements. No installer, automatic update, signing service, or packaging ecosystem is required for v1.

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

The detailed TDD sequence, review gates, and current evidence limits are in [DEVELOPMENT_PLAN.md](DEVELOPMENT_PLAN.md).
