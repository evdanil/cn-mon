# Third-party notices

The current runtime and build use only the Go standard library. The Go module graph has no external modules.

The native Go implementation is written against the monitor behavior and compatibility contract of [`evdanil/cn-tool` 0.8.0 at commit `2e2f3c40ef6718486f1ca4477f9145029ec495fa`](https://github.com/evdanil/cn-tool/tree/2e2f3c40ef6718486f1ca4477f9145029ec495fa). The reference source is distributed under the [Apache License, Version 2.0](https://github.com/evdanil/cn-tool/blob/2e2f3c40ef6718486f1ca4477f9145029ec495fa/LICENSE). Compatibility behavior has been translated into native Go. cn-mon does not bundle Python or include a Python runtime.

The application itself is licensed under the Apache License, Version 2.0. Future third-party modules or adapted source will be listed here with their versions, licenses, and required notices when introduced.
