# cn-mon Privacy Policy

Last updated: 2026-10-16

This policy describes cn-mon CLI version 0.3.0. cn-mon is a local command-line network monitor for Windows and Linux.

## Information cn-mon handles

When you run a monitoring command, cn-mon processes the target names or addresses you provide, any selected TCP port numbers, and the responses to the network checks. Hostnames are resolved through the operating system's network resolver. Depending on your system configuration, name lookups may be sent to configured DNS resolvers or other name-resolution services. cn-mon sends native ICMP echo probes to the selected targets and makes TCP connection attempts only when you request TCP checks.

If history is enabled, cn-mon stores local monitoring history in a SQLite database. A history request can contain the original target tokens, resolved host names or addresses, and selected TCP ports. Stored run and result records can include timestamps, probe outcomes, response addresses, TCP states, run status, the local computer name, the process ID, and the basename of a target input file when a file was used. History lets you list, inspect, follow, and replay saved requests.

cn-mon can also write a local log file. It records timestamps, log levels, and operational messages. A probe-failure diagnostic may include the target hostname or address, the probe type, and error text. Logging is configured through existing settings or command-line options.

cn-mon reads the existing .cn configuration files as settings. It does not modify those files. Configuration can select the local history and log paths and history retention period.

## Network use and sharing

cn-mon has no account service, advertising, analytics, telemetry, or automatic update check. The application does not upload its history database, reports, logs, configuration files, or target lists to the project.

Network requests are made for the target checks you start and for host-name resolution through your operating system's resolver. The target systems and the DNS or other name-resolution services used by your device will receive the corresponding network requests and handle them according to their own policies.

cn-mon does not send history or reports to another party. If you choose to share command output or open a support issue, any information you include is shared by your choice. The project's GitHub issue tracker is public, so do not post target lists, logs, or other information you do not want made public.

## Local storage and retention

The CLI defaults to a SQLite history file named .cn-ping-history.db and a log file named cn.log. You can choose other locations with --history-file and --log-file, or configure them in .cn settings. cn-mon does not encrypt history or log files itself; protection depends on the access controls and storage settings for the chosen location.

History retention defaults to 30 days and can be changed in configuration or with --history-days. Retention pruning runs when a new target-monitoring run starts; it does not run as a background task. Old history can therefore remain until another target run starts. Retention applies to history records, not logs, configuration files, or reports you exported elsewhere.

You can remove history with --delete-run RUN_ID, --delete-request REQUEST_ID, or --clear-history. The delete-run command requires the exact run ID. The delete-request command accepts a unique request ID prefix. The clear-history command removes stored request, run, round, and result records from the selected history database, but leaves the database file and any log, configuration, or exported report files in place. A deletion command may refuse an operation if a selected run is active or its owner cannot be verified. These commands perform logical deletion; cn-mon does not securely erase storage media, backups, or copies made outside the application.

Logs are appended to the configured log file and have no automatic retention limit or in-app deletion command. After cn-mon has closed, you can remove the local log file using your operating system's file-management tools. If you store these files in a folder managed by backup or synchronization software, that software may retain its own copies.

## Questions

For questions about this policy or cn-mon, use the [cn-mon GitHub Issues page](https://github.com/evdanil/cn-mon/issues). Please do not post sensitive network targets or logs in public issues.
