#!/usr/bin/env python3
"""Generate source-composed CSV/Markdown goldens using pinned cn-tool AST nodes.

Copyright 2026 cn-mon contributors
SPDX-License-Identifier: Apache-2.0
"""

from __future__ import annotations

import argparse
import ast
import csv
import datetime
import hashlib
import io
import json
import math
import re
import subprocess
import sys
import types
from collections import Counter
from pathlib import Path

COMMIT = "2e2f3c40ef6718486f1ca4477f9145029ec495fa"
HASHES = {
    "cn_tool/modules/ping_monitor.py": "268543bcc9e38026cc5502821f5cfeceaa9015d9fef745829ddc01d517e82396",
    "cn_tool/utils/config_history.py": "24565c496b3c77ee11b823c336990a44875b66471fb93ba08ea67f877d4d4568",
    "cn_tool/utils/render.py": "8fbac4b6cd906108537d7abfe812a6a41e0d32234caa938b3457f7f80923e152",
    "cn_tool/utils/display.py": "866df6d40446b10b1965bd6ba1e12af4b8cdcd05a383a451a537a28e07049435",
    "cn_tool/utils/ping_history.py": "d491678b240f23d26a2673951ba293eabff3a992d2840bbc1219a63ec31a89bf",
    "cn_tool/utils/color_schemes.py": "792748e8d91dad83e1870a877b6e242152e2f7efdc5e3d5fc8b47ad1ffe6a42d",
    "cn_tool/utils/ping_grid.py": "61c08796c0375071173dd080060c8c819693eb2a9c659e0d1503d908656fbfd5",
}


def sha256(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def functions(path: Path, names: set[str], class_name: str | None = None) -> list[ast.FunctionDef]:
    tree = ast.parse(path.read_text(encoding="utf-8"))
    if class_name:
        owner = next(n for n in tree.body if isinstance(n, ast.ClassDef) and n.name == class_name)
        found = [n for n in owner.body if isinstance(n, ast.FunctionDef) and n.name in names]
    else:
        found = [n for n in tree.body if isinstance(n, ast.FunctionDef) and n.name in names]
    if {node.name for node in found} != names:
        raise ValueError(f"source functions missing from {path}")
    return found


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--source-root", required=True, type=Path)
    args = parser.parse_args()
    if sys.version_info[:3] != (3, 12, 14):
        parser.error(f"requires Python 3.12.14, got {sys.version.split()[0]}")
    root = args.source_root.resolve(strict=True)
    commit = subprocess.run(["git", "-C", str(root), "rev-parse", "HEAD"], check=True, capture_output=True, text=True).stdout.strip()
    if commit != COMMIT:
        parser.error(f"source commit is {commit}, want {COMMIT}")
    paths = {name: root / name for name in HASHES}
    for name, path in paths.items():
        if sha256(path) != HASHES[name]:
            parser.error(f"source checksum mismatch: {name}")

    monitor_path = paths["cn_tool/modules/ping_monitor.py"]
    history_path = paths["cn_tool/utils/config_history.py"]
    history_model_path = paths["cn_tool/utils/ping_history.py"]
    render_path = paths["cn_tool/utils/render.py"]
    display_path = paths["cn_tool/utils/display.py"]
    namespace = {
        "datetime": datetime.datetime, "timezone": datetime.timezone, "json": __import__("json"),
        "re": re, "csv": csv, "io": io, "List": list, "Dict": dict, "Any": object,
        "Data": dict, "Row": dict,
    }
    nodes = functions(history_path, {"format_time"})
    nodes += functions(monitor_path, {"_when", "_day", "_span"})
    nodes += functions(history_model_path, {"_clock", "online_text"})
    monitor_methods = functions(monitor_path, {"_sections", "_seen_rows", "_summary_row"}, "PingMonitorModule")
    render_nodes = functions(render_path, {"markdown_table", "_render_md", "_render_csv"})
    render_nodes += functions(display_path, {"table_columns"})
    source_class = ast.ClassDef(name="SourceMonitor", bases=[], keywords=[], body=monitor_methods, decorator_list=[])
    module = ast.fix_missing_locations(ast.Module(body=[
        ast.ImportFrom(module="__future__", names=[ast.alias(name="annotations")], level=0),
        *nodes, *render_nodes, source_class,
    ], type_ignores=[]))
    namespace["ph"] = types.SimpleNamespace(SEEN=frozenset("!RT"), online_text=namespace.get("online_text"))
    try:
        exec(compile(module, str(monitor_path), "exec"), namespace)
        namespace["ph"].online_text = namespace["online_text"]
    except (SyntaxError, TypeError, ValueError) as exc:
        parser.error(f"cannot execute pinned source AST: {exc}")

    utc = datetime.timezone.utc
    first = datetime.datetime(2026, 1, 1, 23, 59, tzinfo=utc).timestamp()
    last = datetime.datetime(2026, 1, 2, 0, 1, tzinfo=utc).timestamp()
    host = 'alpha,"\n\r\x01\x1braw雪'
    periods = [(first, last)]
    summary = types.SimpleNamespace(
        host=host, address="2001:db8::1", latest="OK\npartial", tcp={80: "open", 443: "closed"},
        first_seen=first, last_seen=last, seen_rounds=2, rounds=2, periods=periods, ongoing=False,
    )
    tracker = types.SimpleNamespace(
        summaries=lambda: [summary], first_round=first, last_round=last, rounds=2,
        cells=lambda: ["!"],
    )
    request = types.SimpleNamespace(short_id="12345678", ports=(80, 443), hosts=[host])
    outcome = types.SimpleNamespace(
        tracker=tracker, request=request, last_rows=[{
            "Host": host, "Address": "2001:db8::1", "Result": "OK\npartial", "TCP 80": "open", "TCP 443": "closed",
        }], interval=60.0, rounds=2, earlier=0, online_now=lambda: False,
    )
    source = namespace["SourceMonitor"]()
    data = source._sections(outcome, "csv")
    if list(data) != ["seen", "latest", "summary"] or data["seen"][0].get("Online") != "01-01 23:59–01-02 00:01" or data["summary"][0].get("Interval") != "1m":
        parser.error("source semantic composition does not match characterized fixture")
    output = Path(__file__).parent
    for ext, renderer in (("csv", namespace["_render_csv"]), ("md", namespace["_render_md"])):
        path = output / f"run-human-source-v1.{ext}"
        path.write_bytes(renderer(data).encode("utf-8"))
        print(f"{path.name} sha256={sha256(path)}")

    # Characterize the source live header's committed-round vs in-flight-round
    # distinction and its grouped progress/count presentation independently.
    live_header_ns = {
        "__name__": "source_live_header", "datetime": datetime.datetime,
        "timezone": datetime.timezone, "Optional": object, "Sequence": list,
        "Tuple": tuple, "Set": set, "math": math,
    }
    monitor_tree = ast.parse(monitor_path.read_text(encoding="utf-8"))
    sheet_node = next(node for node in monitor_tree.body if isinstance(node, ast.Assign) and any(isinstance(target, ast.Name) and target.id == "SHEET" for target in node.targets))
    live_header_ns["SHEET"] = ast.literal_eval(sheet_node.value)
    live_header_nodes = functions(monitor_path, {"_clock", "_span", "_slots", "_fit", "_fit_parts"})
    live_header_nodes += functions(monitor_path, {"_header"}, "_Run")
    live_header_module = ast.fix_missing_locations(ast.Module(body=[
        ast.ImportFrom(module="__future__", names=[ast.alias(name="annotations")], level=0),
        *live_header_nodes,
        ast.ClassDef(name="SourceHeader", bases=[], keywords=[], body=[
            next(node for node in live_header_nodes if isinstance(node, ast.FunctionDef) and node.name == "_header"),
        ], decorator_list=[]),
    ], type_ignores=[]))
    live_header_ns["cell_len"] = len
    live_header_ns["set_cell_size"] = lambda text, size: text[:size]
    exec(compile(live_header_module, str(monitor_path), "exec"), live_header_ns)

    class Clock:
        def now(self) -> float:
            return datetime.datetime(2026, 1, 2, 3, 4, 5, tzinfo=utc).timestamp()

    def source_header(rounds: int, done: int | None = None, total: int | None = None) -> str:
        source = live_header_ns["SourceHeader"]()
        source.plan = types.SimpleNamespace(source=None, every=None, until=None)
        source.outcome = types.SimpleNamespace(rounds=rounds, late=0)
        source.started = 0.0
        source.request = types.SimpleNamespace(short_id="12345678")
        source.clock = Clock()
        return source._header(None, done, total, 200)

    def round_part(header: str) -> str:
        return next(part for part in header.split(" · ") if part.startswith("round "))

    grid_path = paths["cn_tool/utils/ping_grid.py"]
    grid_tree = ast.parse(grid_path.read_text(encoding="utf-8"))
    grid_function = next(node for node in grid_tree.body if isinstance(node, ast.FunctionDef) and node.name == "compact_lines")
    styles_node = next(node for node in grid_tree.body if isinstance(node, ast.AnnAssign) and isinstance(node.target, ast.Name) and node.target.id == "STYLES")
    counted_node = next(node for node in grid_tree.body if isinstance(node, ast.Assign) and any(isinstance(target, ast.Name) and target.id == "_COUNTED" for target in node.targets))
    grid_ns = {"Counter": Counter, "Sequence": list, "List": list, "Line": list}
    grid_ns["STYLES"] = ast.literal_eval(styles_node.value)
    grid_ns["_COUNTED"] = ast.literal_eval(counted_node.value)
    grid_module = ast.fix_missing_locations(ast.Module(body=[
        ast.ImportFrom(module="__future__", names=[ast.alias(name="annotations")], level=0), grid_function,
    ], type_ignores=[]))
    exec(compile(grid_module, str(grid_path), "exec"), grid_ns)
    block = types.SimpleNamespace(title="Hosts", cells=list(range(1024)), network=object(), names=[])
    compact = grid_ns["compact_lines"]([block], ["."] * 1024)[0]
    compact_text = "".join(part[0] for part in compact)
    live_fixture = {
        "waiting_after_one": round_part(source_header(1)),
        "resolving_before_first": round_part(source_header(0, 0, 1024)),
        "probing_after_one": round_part(source_header(1, 300, 1024)),
        "compact_1024": compact_text,
    }
    live_path = output / "live-header-source-v1.json"
    live_path.write_text(json.dumps(live_fixture, indent=2) + "\n", encoding="utf-8", newline="\n")
    print(f"{live_path.name} sha256={sha256(live_path)}")

    color_tree = ast.parse(paths["cn_tool/utils/color_schemes.py"].read_text(encoding="utf-8"))
    assignment = next(node for node in color_tree.body if isinstance(node, ast.AnnAssign) and isinstance(node.target, ast.Name) and node.target.id == "COLOR_SCHEMES")
    schemes = ast.literal_eval(assignment.value)
    role_names = ["header", "sn", "pid", "date", "code", "license_full", "reserved", "auth_type", "start_date", "term_count", "tag", "feature"]
    theme_data = {"roles": role_names, "themes": {}}
    for theme, palette in schemes.items():
        title = palette["title"].split()[-1]
        theme_data["themes"][theme] = {"title": title, "roles": [palette[name] for name in role_names]}
    theme_path = output / "theme-source-v1.json"
    theme_path.write_text(json.dumps(theme_data, indent=2, ensure_ascii=False) + "\n", encoding="utf-8", newline="\n")
    print(f"{theme_path.name} sha256={sha256(theme_path)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
