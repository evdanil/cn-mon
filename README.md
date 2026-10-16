# cn-mon

Check whether your hosts are reachable, watch for connection problems, and keep a record of the results.

**cn-mon is a network monitor for your terminal.** Use it to check a router, follow an intermittent outage, or verify a group of servers after maintenance. It can check one address, a subnet, or a list of hosts, then show the results live and save them for later.

- **Check hosts and ports.** Ping IPv4 or IPv6 addresses and hostnames; add TCP checks for services such as SSH or HTTPS.
- **Watch over time.** Repeat checks at a chosen interval, stop after a set duration, or leave monitoring running until you stop it.
- **Revisit your results.** Browse local history and repeat a saved set of checks without rebuilding the target list.
- **Share what you found.** Export a readable table, JSON, CSV, or Markdown report.

Runs on **Windows and Linux, x86-64**. The downloads are ready to run: no Go, Python, or database installation is needed.

## Download and install

Get [cn-mon 0.3.0](https://github.com/evdanil/cn-mon/releases/tag/v0.3.0):

| Platform | Download |
| --- | --- |
| Windows x86-64 | [Windows ZIP](https://github.com/evdanil/cn-mon/releases/download/v0.3.0/cn-mon_0.3.0_windows_amd64.zip) |
| Linux x86-64 | [Linux archive](https://github.com/evdanil/cn-mon/releases/download/v0.3.0/cn-mon_0.3.0_linux_amd64.tar.gz) |

Download [SHA256SUMS](https://github.com/evdanil/cn-mon/releases/download/v0.3.0/SHA256SUMS) into the same folder as your archive. The commands below check that the download is intact before extracting it.

### Windows

Open PowerShell in the folder containing the downloaded files:

```powershell
$lines = @(Get-Content .\SHA256SUMS | Where-Object { $_.EndsWith('  cn-mon_0.3.0_windows_amd64.zip') })
if ($lines.Count -ne 1) { throw 'Windows archive checksum is missing or duplicated' }
$expected = ($lines[0] -split '\s+', 2)[0]
$actual = (Get-FileHash .\cn-mon_0.3.0_windows_amd64.zip -Algorithm SHA256).Hash.ToLowerInvariant()
if ($actual -cne $expected) { throw 'Windows archive checksum mismatch' }
Expand-Archive .\cn-mon_0.3.0_windows_amd64.zip .\cn-mon
.\cn-mon\cn-mon_0.3.0_windows_amd64\cn-mon.exe --version
```

Move into the extracted folder and try a first check:

```powershell
Set-Location .\cn-mon\cn-mon_0.3.0_windows_amd64
.\cn-mon.exe example.com
```

Start in a normal PowerShell window. If cn-mon reports an ICMP permission error, use an Administrator terminal.

### Linux

Open a terminal in the folder containing the downloaded files:

```sh
line=$(grep -F '  cn-mon_0.3.0_linux_amd64.tar.gz' SHA256SUMS) || exit 1
test "$(printf '%s\n' "$line" | wc -l)" -eq 1 || exit 1
printf '%s\n' "$line" | sha256sum --check - || exit 1
tar -xzf cn-mon_0.3.0_linux_amd64.tar.gz
./cn-mon_0.3.0_linux_amd64/cn-mon --version
```

Linux needs permission to send ICMP probes. Grant it to the extracted executable once, then run cn-mon as your normal user:

```sh
cd cn-mon_0.3.0_linux_amd64
sudo setcap cap_net_raw+ep ./cn-mon
./cn-mon example.com
```

If `setcap` is unavailable or your system does not support file capabilities, ask your administrator to enable ICMP access for the executable.

## Everyday commands

The examples below use `cn-mon`. From its extracted folder, use `./cn-mon` on Linux or `.\cn-mon.exe` in PowerShell. If you add that folder to your `PATH`, you can use `cn-mon` as written.

### Check a host or a network

Check a hostname, a few addresses, or every host in a subnet:

```sh
cn-mon example.com
cn-mon 192.168.1.1 192.168.1.10
cn-mon 192.168.1.0/24
```

Replace the example addresses with the hosts you want to check. With no scheduling options, cn-mon runs one round and prints a table.

To check whether SSH and HTTPS ports accept connections, add TCP checks:

```sh
cn-mon example.com --tcp 22,443
```

TCP checks run alongside ping. You can check up to five ports at once. The final report shows each port's result.

### Watch for intermittent problems

Check your router and an external host every 30 seconds for 10 minutes:

```sh
cn-mon 192.168.1.1 example.com --every 30s --for 10m
```

Leave out `--for` to keep monitoring until you stop it. Intervals must be at least 10 seconds.

In the live display, press **`v`** for a table view or **`q`** to stop. In the grid, `!` means a ping reply and `T` means a TCP response, which can include a refused connection; use the final report to see which ports were open.

### Work from a target list

Put hostnames, addresses, or subnets in `targets.txt`, separated by spaces or newlines. Blank lines and `#` comments are allowed.

```text
# Office network
192.168.1.1
192.168.1.10
example.com
```

Run the list once, or add the same monitoring options:

```sh
cn-mon --file targets.txt --tcp 22,443
cn-mon --file targets.txt --every 30s --for 10m
```

### Save a report

Use CSV for a spreadsheet, JSON for a script, or Markdown for notes and issue reports. These examples export only the new run's results:

```sh
cn-mon --file targets.txt --since 0m --format csv > results.csv
cn-mon example.com --tcp 443 --since 0m --format json > results.json
cn-mon example.com --since 0m --format md > report.md
```

## Find and repeat earlier checks

Monitoring results are saved locally. List your saved target groups, then use the request ID from the list to inspect or repeat one:

```sh
cn-mon --list
cn-mon --show REQUEST_ID
cn-mon --rerun REQUEST_ID
```

Replace `REQUEST_ID` with an ID from `--list`; a unique prefix of at least four characters also works. A rerun uses the saved targets, TCP ports and schedule, even if the original target file is gone. You can change the schedule for the new run:

```sh
cn-mon --rerun REQUEST_ID --every 30s --for 5m
```

For a closer look at a saved group:

| Command | What it does |
| --- | --- |
| `cn-mon --list-runs REQUEST_ID` | Lists its individual runs. |
| `cn-mon --show REQUEST_ID --since 24h` | Shows results from the past day. |
| `cn-mon --show REQUEST_ID --format md > history.md` | Exports its saved results as Markdown. |
| `cn-mon --follow REQUEST_ID` | Watches a running request from another terminal on the same computer. Press `q` to stop following. |

By default, history is kept in `.cn-ping-history.db` in your home folder, with 30-day retention. Old history is pruned when a new monitoring run starts. Use `--history-file` to keep a separate history for a project and `--history-days` to change retention:

```sh
cn-mon --file targets.txt --history-file office-history.db --history-days 90
cn-mon --list --history-file office-history.db
```

Keep history databases on a local disk. `--delete-run RUN_ID` removes one run, `--delete-request REQUEST_ID` removes a target group's history, and `--clear-history` removes all saved results in the selected database. Deletion is immediate, without a confirmation prompt; active runs are protected.

## Settings and help

Most settings can be supplied on the command line:

| Option | Use it to |
| --- | --- |
| `--theme dark` | Choose a display theme: `default`, `monochrome`, `pastel`, or `dark`. |
| `--history-file PATH` | Choose where to keep monitoring history. |
| `--history-days 90` | Keep up to 90 days of history. |
| `--log-file PATH` | Choose a log file; the default is `cn.log` in your home folder. |
| `--config PATH` | Read settings from an additional configuration file. |

For persistent settings, create a `.cn` file in your home folder (`%USERPROFILE%` on Windows). For example:

```ini
[ping]
history_days = 90

[theme]
theme = dark
```

Command-line settings override configuration files. Existing cn-tool monitor settings are supported; the [configuration reference](docs/APPLICATION_SPEC.md#21-existing-cn-tool-configuration) describes the available settings and how files are combined.

History and logs stay on your computer. cn-mon has no account requirement or telemetry.

Run `cn-mon --help` for all options, or [open an issue](https://github.com/evdanil/cn-mon/issues) for a bug or question.

## Development and license

For source builds and contributor details, see the [development plan](docs/DEVELOPMENT_PLAN.md) and [application specification](docs/APPLICATION_SPEC.md).

cn-mon carries forward the network-monitoring behavior of [cn-tool](https://github.com/evdanil/cn-tool). It is licensed under the [Apache License, Version 2.0](LICENSE). Dependency licenses and attributions are in [third-party notices](THIRD_PARTY_NOTICES.md).
