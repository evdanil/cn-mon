#!/usr/bin/env python3
"""Regenerate the M4 JSON golden from the pinned cn-tool source functions.

Copyright 2026 cn-mon contributors
SPDX-License-Identifier: Apache-2.0
"""

from __future__ import annotations

import argparse
import ast
import datetime
import hashlib
import json
import re
import subprocess
import sys
import types
from pathlib import Path

SOURCE_COMMIT = "2e2f3c40ef6718486f1ca4477f9145029ec495fa"
SOURCE_SHA256 = {
    "cn_tool/modules/ping_monitor.py": "268543bcc9e38026cc5502821f5cfeceaa9015d9fef745829ddc01d517e82396",
    "cn_tool/utils/config_history.py": "24565c496b3c77ee11b823c336990a44875b66471fb93ba08ea67f877d4d4568",
    "cn_tool/utils/render.py": "8fbac4b6cd906108537d7abfe812a6a41e0d32234caa938b3457f7f80923e152",
}


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source_file:
        for chunk in iter(lambda: source_file.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def nodes(path: Path, wanted: set[str], class_name: str | None = None) -> list[ast.FunctionDef | ast.AsyncFunctionDef]:
    tree = ast.parse(path.read_text(encoding="utf-8"))
    if class_name:
        owner = next(n for n in tree.body if isinstance(n, ast.ClassDef) and n.name == class_name)
        return [n for n in owner.body if isinstance(n, (ast.FunctionDef, ast.AsyncFunctionDef)) and n.name in wanted]
    return [n for n in tree.body if isinstance(n, (ast.FunctionDef, ast.AsyncFunctionDef)) and n.name in wanted]


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--source-root", required=True, type=Path, help="pinned cn-tool repository checkout")
    args = parser.parse_args()
    if sys.version_info[:3] != (3, 12, 14):
        parser.error(f"fixture generation requires Python 3.12.14, got {sys.version.split()[0]}")
    try:
        source_root = args.source_root.resolve(strict=True)
        commit = subprocess.run(
            ["git", "-C", str(source_root), "rev-parse", "HEAD"],
            check=True, capture_output=True, text=True,
        ).stdout.strip()
    except (OSError, subprocess.CalledProcessError) as exc:
        parser.error(f"cannot verify source checkout: {exc}")
    if commit != SOURCE_COMMIT:
        parser.error(f"source checkout is {commit}, want pinned commit {SOURCE_COMMIT}")

    paths = {name: source_root / name for name in SOURCE_SHA256}
    for name, path in paths.items():
        if not path.is_file() or sha256(path) != SOURCE_SHA256[name]:
            parser.error(f"pinned source checksum mismatch: {name}")

    monitor_source = paths["cn_tool/modules/ping_monitor.py"]
    history_source = paths["cn_tool/utils/config_history.py"]
    render_source = paths["cn_tool/utils/render.py"]
    namespace = {
        "datetime": datetime.datetime,
        "timezone": datetime.timezone,
        "json": json,
        "re": re,
        "List": list,
        "Dict": dict,
        "Any": object,
        "ph": types.SimpleNamespace(SEEN=frozenset("!RT")),
    }
    format_time = nodes(history_source, {"format_time"})
    monitor_funcs = nodes(monitor_source, {"_when", "_day"})
    render_funcs = nodes(render_source, {"snake_key", "_render_json"})
    monitor_methods = nodes(monitor_source, {"_sections", "_seen_rows", "_summary_row"}, "PingMonitorModule")
    expected = {
        "format_time": {n.name for n in format_time},
        "monitor functions": {n.name for n in monitor_funcs},
        "render functions": {n.name for n in render_funcs},
        "monitor methods": {n.name for n in monitor_methods},
    }
    wanted = {
        "format_time": {"format_time"},
        "monitor functions": {"_when", "_day"},
        "render functions": {"snake_key", "_render_json"},
        "monitor methods": {"_sections", "_seen_rows", "_summary_row"},
    }
    if expected != wanted:
        parser.error("pinned source AST does not contain the expected JSON functions")
    source_class = ast.ClassDef(name="SourceMonitor", bases=[], keywords=[], body=monitor_methods, decorator_list=[])
    module = ast.fix_missing_locations(ast.Module(body=[
        ast.ImportFrom(module="__future__", names=[ast.alias(name="annotations")], level=0),
        *format_time, *monitor_funcs, *render_funcs, source_class,
    ], type_ignores=[]))
    try:
        exec(compile(module, str(monitor_source), "exec"), namespace)
    except (SyntaxError, TypeError, ValueError) as exc:
        parser.error(f"cannot compile pinned source functions: {exc}")

    utc = datetime.timezone.utc

    def stamp(year: int, month: int, day: int, hour: int, minute: int, second: int) -> float:
        return datetime.datetime(year, month, day, hour, minute, second, tzinfo=utc).timestamp()

    alpha_first = stamp(2026, 1, 1, 4, 5, 6)
    alpha_last = stamp(2026, 1, 2, 3, 4, 5)
    zeta_first = stamp(2026, 1, 2, 3, 5, 0)
    zeta_last = stamp(2026, 1, 2, 3, 6, 0)
    started = stamp(2026, 1, 2, 3, 0, 0)
    summaries = [
        types.SimpleNamespace(host="alpha.example", address='A <&> "雪"', latest="OK (1/2 replies)", tcp={80: "", 443: "open"}, first_seen=alpha_first, last_seen=alpha_last, seen_rounds=2, rounds=3, periods=[(alpha_first, alpha_first), (alpha_last, alpha_last)], ongoing=True),
        types.SimpleNamespace(host="zeta.example", address="2001:db8::2", latest="NO RESPONSE", tcp={80: "closed", 443: "error"}, first_seen=zeta_first, last_seen=zeta_last, seen_rounds=1, rounds=3, periods=[(zeta_first, zeta_last)], ongoing=False),
    ]
    tracker = types.SimpleNamespace(summaries=lambda: summaries, first_round=started, last_round=zeta_last, rounds=3, cells=lambda: list("!."))
    request = types.SimpleNamespace(short_id="deadbeef", ports=(80, 443), hosts=["alpha.example", "zeta.example"])
    outcome = types.SimpleNamespace(tracker=tracker, request=request, last_rows=[
        {"Host": "alpha.example", "Address": 'A <&> "雪"', "Result": "OK (1/2 replies)", "TCP 80": "", "TCP 443": "open"},
        {"Host": "zeta.example", "Address": "2001:db8::2", "Result": "NO RESPONSE", "TCP 80": "closed", "TCP 443": "error"},
    ], interval=1.5, rounds=1, earlier=2, online_now=lambda: False)
    data = namespace["SourceMonitor"]()._sections(outcome, "json")
    data["not_found"] = [
        {"object": 'late <&> "x"', "reason": "invalid <target>"},
        {"object": "first", "reason": "bad input"},
        {"object": "alpha.invalid", "reason": "no such host"},
        {"object": "zulu.invalid", "reason": "no such host"},
    ]
    text = namespace["_render_json"](data)
    output = Path(__file__).with_name("run-json-source-v1.json")
    try:
        output.write_text(text, encoding="utf-8", newline="\n")
    except (OSError, UnicodeEncodeError) as exc:
        parser.error(f"cannot write bounded fixture {output}: {exc}")
    print(text, end="")
    print(f"fixture_sha256={sha256(output)}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
