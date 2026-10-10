# Target-run JSON fixture provenance

`run-json-source-v1.json` was generated from the pinned cn-tool 0.8.0 source at
commit `2e2f3c40ef6718486f1ca4477f9145029ec495fa`. The test-only generator
checks that Git revision and the exact SHA-256 of each source file before it
extracts and executes any source AST:

- `cn_tool/modules/ping_monitor.py`: `268543bcc9e38026cc5502821f5cfeceaa9015d9fef745829ddc01d517e82396`
- `cn_tool/utils/config_history.py`: `24565c496b3c77ee11b823c336990a44875b66471fb93ba08ea67f877d4d4568`
- `cn_tool/utils/render.py`: `8fbac4b6cd906108537d7abfe812a6a41e0d32234caa938b3457f7f80923e152`

The generator extracts and executes `PingMonitorModule._sections`, `_seen_rows`,
and `_summary_row` (ping_monitor.py lines 601–654), `_when`/`_day` (lines
95–100), `format_time` (config_history.py line 76), and `snake_key`/`_render_json`
(render.py lines 40–60). It supplies a fixed model fixture without importing
the cn-tool package or running probes, storage, configuration, or networking.

Reproduce with Python 3.12.14 using the verified checkout path:

```sh
python3 internal/output/testdata/generate_source_golden.py --source-root /tmp/cn-tool-review.zoO7Ym/repo
```

The bounded generator writes only `run-json-source-v1.json`. The recorded
fixture SHA-256 is
`dc3327122d1c4855bb450a5c1be34250c377fa46c4a8cc0f1260e769dded6cc6`.

The target-run composition follows source `_cli_run` (ping_monitor.py lines
406–454): input rejections remain in admission order, then never-resolved names
are sorted and appended with `no such host`. The fixture's interval is a numeric
float, matching the reviewed native M4 contract and source JSON behavior. The
golden covers HTML/non-ASCII/quoted text, sparse latest rows, sampled periods,
and TCP field order. Low-year timestamp padding is separately specified and
tested as a native extension.
