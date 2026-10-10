#!/usr/bin/env python3
"""Generate a clean source-compatible history fixture for Go interoperability tests.

Copyright 2026 cn-mon contributors
SPDX-License-Identifier: Apache-2.0

The fixture data and schema are produced through the pinned cn-tool PingStore, not a
second handwritten schema implementation. See THIRD_PARTY_NOTICES.md for provenance.
"""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import sqlite3
import subprocess
import sys

SOURCE_COMMIT = "2e2f3c40ef6718486f1ca4477f9145029ec495fa"
PING_STORE_SHA256 = "326ab0570dc7bf3fc43209d21d00f46f8c2b8778ea0daf4cf905c52a8c337fd4"
PING_HISTORY_SHA256 = "d491678b240f23d26a2673951ba293eabff3a992d2840bbc1219a63ec31a89bf"
SCHEMA_VERSION = 1

HOSTS = [
    "api.example.com",
    "db.example.com",
    "legacy.example.com",
    "timeout.example.com",
    "unreachable.example.com",
    "unresolved.example.com",
    "future.example.com",
]
TARGETS = ["api.example.com", "db.example.com"]


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as file:
        for chunk in iter(lambda: file.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--source", required=True, type=Path, help="pinned cn-tool source checkout")
    parser.add_argument("--output", required=True, type=Path, help="new output directory for the fixture files")
    args = parser.parse_args()

    source = args.source.resolve(strict=True)
    output = args.output.resolve()
    if output.exists() and any(output.iterdir()):
        parser.error(f"output directory must be empty: {output}")
    if sys.version_info[:3] != (3, 12, 14):
        parser.error(f"fixture generation requires Python 3.12.14, got {sys.version.split()[0]}")
    commit = subprocess.run(
        ["git", "-C", str(source), "rev-parse", "HEAD"],
        check=True,
        capture_output=True,
        text=True,
    ).stdout.strip()
    if commit != SOURCE_COMMIT:
        parser.error(f"source checkout is {commit}, want pinned commit {SOURCE_COMMIT}")

    ping_store_path = source / "cn_tool/utils/ping_store.py"
    ping_history_path = source / "cn_tool/utils/ping_history.py"
    if sha256(ping_store_path) != PING_STORE_SHA256 or sha256(ping_history_path) != PING_HISTORY_SHA256:
        parser.error("pinned source file checksum mismatch")

    sys.path.insert(0, str(source))
    sys.dont_write_bytecode = True
    from cn_tool.utils import ping_history, ping_store  # noqa: E402

    if ping_store.SCHEMA_VERSION != SCHEMA_VERSION or len(ping_store._SCHEMA) != 7:
        parser.error("pinned PingStore schema contract changed")
    if output.exists() and not output.is_dir():
        parser.error(f"output path is not a directory: {output}")
    output.mkdir(parents=True, exist_ok=True)
    database = output / "cn-tool-schema1.db"
    manifest_path = output / "semantic-manifest.json"
    provenance_path = output / "provenance.json"
    for path in (database, manifest_path, provenance_path):
        if path.exists():
            parser.error(f"refusing to overwrite existing fixture file: {path}")

    key = ping_history.request_key(TARGETS, [], HOSTS)
    with ping_store.PingStore.open(database, write=True) as store:
        assert store is not None
        request_id = store.begin_run(
            key,
            TARGETS,
            [],
            HOSTS,
            "fixture-finished",
            source="198.51.100.8",
            started=1700000000.125,
            interval=None,
            until=None,
            host="fixture-linux-or-windows",
            pid=1234,
            heartbeat=1700000000.25,
        )
        store.record_round(
            "fixture-finished",
            1700000000.375,
            "!RTU.E?",
            [
                ping_store.ResultRow("api.example.com", "192.0.2.10", 1700000000.5, "OK (1/2 replies)", None),
                ping_store.ResultRow(
                    "db.example.com", "2001:db8::2%eth0", 1700000000.625, "REJECTED - custom firewall text", None
                ),
                ping_store.ResultRow(
                    "legacy.example.com", 'legacy "quoted" address – β', 1700000000.75,
                    "historic custom hook text – β", {},
                ),
            ],
        )
        store.record_round("fixture-finished", 1700000001.625, "U??E...", [])
        store.end_run("fixture-finished", ended=1700000002.125, status="finished")

        stopped_id = store.begin_run(
            key,
            TARGETS,
            [],
            HOSTS,
            "fixture-stopped",
            source=None,
            started=1700000100.25,
            interval=5.0,
            until=1700001000.0,
            host="fixture-linux-or-windows",
            pid=1235,
            heartbeat=1700000105.125,
        )
        if stopped_id != request_id:
            raise RuntimeError("PingStore did not reuse the existing request")
        store.record_round(
            "fixture-stopped",
            1700000110.5,
            "!......",
            [ping_store.ResultRow("api.example.com", "192.0.2.10", 1700000110.75, "OK (2/2 replies)", {443: "open"})],
        )
        store.heartbeat("fixture-stopped", 1700000111.0, round_seconds=0.875)
        store.end_run("fixture-stopped", ended=1700000112.25, status="stopped")

        failed_id = store.begin_run(
            key,
            TARGETS,
            [],
            HOSTS,
            "fixture-failed-zero-round",
            source="quote\"target",
            started=1700000200.5,
            interval=10.0,
            until=None,
            host="fixture-linux-or-windows",
            pid=1236,
            heartbeat=1700000200.75,
        )
        if failed_id != request_id:
            raise RuntimeError("PingStore did not reuse the existing request")
        store.end_run("fixture-failed-zero-round", ended=1700000201.0, status="failed")

        running_id = store.begin_run(
            key,
            TARGETS,
            [],
            HOSTS,
            "fixture-running-zero-round",
            source=None,
            started=1700000300.125,
            interval=172800.0,
            until=None,
            host="fixture-linux-or-windows",
            pid=1237,
            heartbeat=1700000300.375,
        )
        if running_id != request_id:
            raise RuntimeError("PingStore did not reuse the existing request")

    with ping_store.PingStore.open(database, write=False) as reader:
        assert reader is not None
        request = reader.request(key)
        if request is None:
            raise RuntimeError("source PingStore did not read back generated request")
        rounds = reader.rounds(request_id)
        runs = reader.runs(request_id)
        if len(runs) != 4 or len(rounds) != 3:
            raise RuntimeError(f"source PingStore read back runs={len(runs)} rounds={len(rounds)}")
        if request.tcp_ports != () or request.hosts != tuple(HOSTS):
            raise RuntimeError("source PingStore changed request arrays")

    connection = sqlite3.connect(database)
    try:
        checkpoint = connection.execute("PRAGMA wal_checkpoint(TRUNCATE)").fetchone()
        if checkpoint is None or checkpoint[0] != 0:
            raise RuntimeError(f"fixture WAL checkpoint failed: {checkpoint!r}")
    finally:
        connection.close()
    for suffix in ("-wal", "-shm"):
        sidecar = Path(str(database) + suffix)
        if sidecar.exists() and sidecar.stat().st_size != 0:
            raise RuntimeError(f"fixture has a nonempty live WAL sidecar: {sidecar}")
        if sidecar.exists():
            sidecar.unlink()

    manifest = {
        "schema_version": SCHEMA_VERSION,
        "request": {
            "id": request_id,
            "key": key,
            "targets": TARGETS,
            "tcp_ports": [],
            "hosts": HOSTS,
        },
        "runs": [
            {"id": run.id, "request_id": run.request_id, "started": run.started, "interval": run.interval,
             "until": run.until, "host": run.host, "pid": run.pid, "heartbeat": run.heartbeat,
             "round_seconds": run.round_seconds, "ended": run.ended, "status": run.status,
             "rounds": run.rounds, "source": run.source}
            for run in runs
        ],
        "rounds": [
            {"id": round_.id, "run_id": round_.run_id, "started": round_.started,
             "interval": round_.interval, "observations": round_.observations,
             "answered": round_.answered,
             "rows": [
                 {"host": row.host, "address": row.address, "time": row.t,
                  "result": row.result, "tcp": row.tcp}
                 for row in round_.rows
             ]}
            for round_ in rounds
        ],
    }
    manifest_path.write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    generator = Path(__file__).resolve()
    provenance = {
        "source_repository": "https://github.com/evdanil/cn-tool",
        "source_commit": SOURCE_COMMIT,
        "source_license": "Apache-2.0",
        "source_files": {
            "cn_tool/utils/ping_store.py": PING_STORE_SHA256,
            "cn_tool/utils/ping_history.py": PING_HISTORY_SHA256,
        },
        "python": sys.version.split()[0],
        "sqlite": sqlite3.sqlite_version,
        "generator": str(generator.relative_to(Path(__file__).resolve().parents[1])),
        "generator_sha256": sha256(generator),
        "fixture": database.name,
        "fixture_sha256": sha256(database),
        "semantic_manifest": manifest_path.name,
        "schema_version": SCHEMA_VERSION,
        "schema_create_statements": len(ping_store._SCHEMA),
        "tcp_ports_empty_array": True,
        "result_tcp_null_and_empty_values": True,
        "wal_checkpoint": list(checkpoint),
        "wal_sidecars_committed": False,
    }
    provenance_path.write_text(json.dumps(provenance, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(provenance, ensure_ascii=False, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
