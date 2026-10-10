# Monitor model source provenance

The retained model behavior was characterized against `cn-tool` commit
`2e2f3c40ef6718486f1ca4477f9145029ec495fa`, files
`cn_tool/utils/ping_history.py` and `cn_tool/modules/ping_monitor.py`.
The Go implementation is new Apache-2.0 code. This note records the behavioral
source only; it does not copy Python implementation code.

| Behavior | Source location |
| --- | --- |
| Observation precedence, partial reply | `ping_history.py`: `observation` (50–70), `partial_reply` (72–74) |
| Single-run spacing estimate | `ping_history.py`: `expected_spacing` (160–173) |
| Seen, sticky unreachable, changes and aggregates | `ping_history.py`: `_derived`, `Tracker.add`, `_seen`, `cells`, `summaries` (201–220, 286–394) |
| Sampled periods and gap threshold | `ping_history.py`: `Tracker._spacing`, `_gap_before`, `_close`, `add` (269–328) |
| Per-host batch sample times | `ping_monitor.py`: completed-round construction (1019–1030) |
| Open period belongs to latest round's run | `ping_monitor.py`: `RunOutcome.online_now` (205–208), seen-row serialization (614–626) |

The specification narrows/clarifies concurrent-run behavior beyond the pinned
tracker: completed rounds are folded in `(started, round_id)` order, sample
times are clamped per host, and only the owner of the latest round can leave
its final period open. Those contracts are from `docs/APPLICATION_SPEC.md` §6.
