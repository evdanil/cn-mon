# cn-mon

`cn-mon` is a planned native network connectivity monitor for Linux x86-64 and Windows x86-64. The application is specified but not implemented; the current `main.go` is still a placeholder and does not provide the described behavior.

Read the [application specification](docs/APPLICATION_SPEC.md) and [development plan](docs/DEVELOPMENT_PLAN.md) for the product requirements and implementation milestones. The intended behavior takes inspiration from [cn-tool](https://github.com/evdanil/cn-tool/tree/2e2f3c40ef6718486f1ca4477f9145029ec495fa).

The planned utility uses native Go ICMP support without invoking a system `ping` command. It supports IPv4 and IPv6, with optional TCP checks, stores persistent probe history, and presents live results in a terminal grid or table. Its command-line interface includes list, show, and follow operations, with CSV, JSON, and Markdown reports. It has no interactive menu or XLSX export.

The planned application is licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE).
