# Target fixture provenance

The behavioral fixtures in `target_test.go` characterize the pinned `cn-tool`
source at commit `2e2f3c40ef6718486f1ca4477f9145029ec495fa` under Python 3.12.14.

| Behavior | Source authority | Fixture coverage |
| --- | --- | --- |
| File/stdin token splitting, comments, target classification and greedy TCP host admission | `cn_tool/modules/bulk_ping.py`, `_expand_targets` and `_parse_target` (lines 428–518) | input order, one stdin read, BOM and comments, malformed UTF-8, raw IPv6 spelling during pre-canonical admission, IPv4 lists, CIDR expansion, scoped IPv6 rules and all five TCP admission rows |
| DNS-name validation and TCP port syntax | `cn_tool/utils/validation.py`, `is_fqdn` and `parse_tcp_ports` (lines 188–240) | ASCII labels, single-label names, trailing dot, sorted/deduplicated ports and invalid lists |
| Canonical tokens, request host expansion and SHA-256 request key | `cn_tool/utils/ping_history.py`, `canonical_tokens` and `request_key` (lines 98–123); `cn_tool/modules/ping_monitor.py`, `_tokens` and `_request_of` (lines 313–359) | exact golden JSON/key, mask equivalence, canonical sorting, overlapping inputs, scoped zones and Unicode JSON escaping |
| Python `ipaddress` network host iteration and address classification | `cn_tool/modules/bulk_ping.py`, `_parse_target` (lines 462–502), characterized with Python 3.12.14 | `/31`, `/127`, loopback/multicast/reserved exclusions, per-network boundaries, dotted netmask/hostmask parsing and no aggregate ICMP cap |

Configuration-layer provenance is recorded by the configuration package; target
fixtures do not adapt or copy Python runtime code. The retained command behavior
uses Go's standard library only.
