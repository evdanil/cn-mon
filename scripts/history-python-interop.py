#!/usr/bin/env python3
"""Test-only Python side of cn-mon/cn-tool SQLite interoperability checks.

Copyright 2026 cn-mon contributors
SPDX-License-Identifier: Apache-2.0

This imports only the pinned source PingStore and Python's stdlib sqlite3; it is
not application runtime code. See THIRD_PARTY_NOTICES.md for source provenance.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import sys
import threading
import time

SOURCE_COMMIT = "2e2f3c40ef6718486f1ca4477f9145029ec495fa"
PING_STORE_SHA256 = "326ab0570dc7bf3fc43209d21d00f46f8c2b8778ea0daf4cf905c52a8c337fd4"
PING_HISTORY_SHA256 = "d491678b240f23d26a2673951ba293eabff3a992d2840bbc1219a63ec31a89bf"
KEY = "a" * 64
TARGETS = ["interop.example"]
HOSTS = ["interop.example", "silent.example"]


def sha256(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def load_reference(source: Path):
    source = source.resolve(strict=True)
    commit = subprocess.run(
        ["git", "-C", str(source), "rev-parse", "HEAD"],
        check=True,
        capture_output=True,
        text=True,
    ).stdout.strip()
    if commit != SOURCE_COMMIT:
        raise RuntimeError(f"reference source commit is {commit}, want {SOURCE_COMMIT}")
    store_file = source / "cn_tool/utils/ping_store.py"
    history_file = source / "cn_tool/utils/ping_history.py"
    if sha256(store_file) != PING_STORE_SHA256 or sha256(history_file) != PING_HISTORY_SHA256:
        raise RuntimeError("reference source file checksum mismatch")
    sys.path.insert(0, str(source))
    sys.dont_write_bytecode = True
    from cn_tool.utils import ping_store  # noqa: E402

    return ping_store, {
        "python": sys.version.split()[0],
        "sqlite": sqlite3.sqlite_version,
        "source_commit": commit,
        "ping_store_sha256": PING_STORE_SHA256,
        "ping_history_sha256": PING_HISTORY_SHA256,
    }


def report(source: Path) -> int:
    _, facts = load_reference(source)
    payload = json.dumps(facts, ensure_ascii=True, sort_keys=True)
    print("CNMON_PYTHON_INTEROP_FACTS " + payload)
    if sys.version_info[:3] != (3, 12, 14):
        raise RuntimeError(f"interop requires Python 3.12.14, got {sys.version.split()[0]}")
    if "GITHUB_ACTIONS" in os.environ:
        annotation = " ".join(f"{key}={value}" for key, value in facts.items())
        annotation = annotation.replace("%", "%25").replace("\r", "%0D").replace("\n", "%0A")
        print(f"::notice title=cn-mon Python history interop::{annotation}")
    return 0


def append_many(ping_store, database: Path, count: int, ready_file: Path | None, start_file: Path | None) -> dict:
    if not 1 <= count <= 100:
        raise RuntimeError("append count must be between 1 and 100")
    errors: list[BaseException] = []
    stop_readers = threading.Event()

    with ping_store.PingStore.open(database, write=True) as writer:
        if writer is None:
            raise RuntimeError("source PingStore did not open a writer")

        def read_during_writes() -> None:
            try:
                with ping_store.PingStore.open(database, write=False) as reader:
                    if reader is None:
                        raise RuntimeError("source reader did not find the writer database")
                    while not stop_readers.is_set():
                        with reader.snapshot():
                            request = reader.request(KEY)
                            if request is not None:
                                reader.runs(request.id)
                                rounds = reader.rounds(request.id)
                                if any(len(round_.rows) != 1 for round_ in rounds):
                                    raise RuntimeError("source reader observed a partial result round")
                        stop_readers.wait(0.005)
            except BaseException as error:
                errors.append(error)
                stop_readers.set()

        reader_thread = threading.Thread(target=read_during_writes, name="cn-mon-source-reader")
        reader_thread.start()
        try:
            if ready_file is not None:
                ready_file.parent.mkdir(parents=True, exist_ok=True)
                ready_file.touch(exist_ok=False)
            if start_file is not None:
                deadline = time.monotonic() + 30
                while not start_file.exists():
                    if time.monotonic() >= deadline:
                        raise RuntimeError("source writer did not receive the bounded start signal")
                    time.sleep(0.005)
            for index in range(count):
                run_id = f"python-concurrent-{index:03d}"
                write_run(ping_store, writer, run_id, 1800000200.125 + index * 2, "stopped")
                time.sleep(0.01)
        finally:
            stop_readers.set()
            reader_thread.join(timeout=10)
        if reader_thread.is_alive():
            raise RuntimeError("source reader did not stop after concurrent writes")
        if errors:
            raise RuntimeError(f"source concurrent reader failed: {errors[0]}")
    return {"python_runs": count}


def hold_uncommitted_update(ping_store, database: Path, run_id: str, ready_file: Path) -> None:
    store = ping_store.PingStore.open(database, write=True)
    if store is None:
        raise RuntimeError("source PingStore did not open a writer")
    try:
        store._connection.execute("BEGIN IMMEDIATE")
        cursor = store._connection.execute("UPDATE run SET status = 'failed' WHERE id = ?", (run_id,))
        if cursor.rowcount != 1:
            raise RuntimeError(f"source run {run_id!r} was not present for crash test")
        ready_file.parent.mkdir(parents=True, exist_ok=True)
        ready_file.touch(exist_ok=False)
        time.sleep(60)
    finally:
        store.close()


def hold_snapshot(ping_store, database: Path, ready_file: Path, release_file: Path) -> dict:
    with ping_store.PingStore.open(database, write=False) as reader:
        if reader is None:
            raise RuntimeError("source PingStore did not open a snapshot reader")
        with reader.snapshot():
            request = reader.request(KEY)
            if request is None:
                raise RuntimeError("source snapshot cannot see the history request")
            before_runs = tuple(run.id for run in reader.runs(request.id))
            before_rounds = tuple((round_.id, round_.observations, tuple(row.host for row in round_.rows)) for round_ in reader.rounds(request.id))
            if set(before_runs) != {"python-created", "old-prunable"} or len(before_rounds) != 2:
                raise RuntimeError(f"source snapshot fixture differs before the Go mutation: runs={before_runs} rounds={len(before_rounds)}")
            ready_file.parent.mkdir(parents=True, exist_ok=True)
            ready_file.touch(exist_ok=False)
            deadline = time.monotonic() + 30
            while not release_file.exists():
                if time.monotonic() >= deadline:
                    raise RuntimeError("source snapshot did not receive its bounded release signal")
                time.sleep(0.005)
            after_runs = tuple(run.id for run in reader.runs(request.id))
            after_rounds = tuple((round_.id, round_.observations, tuple(row.host for row in round_.rows)) for round_ in reader.rounds(request.id))
            if before_runs != after_runs or before_rounds != after_rounds:
                raise RuntimeError("source read snapshot changed while another writer committed and pruned")
            return {"snapshot_runs": len(before_runs), "snapshot_rounds": len(before_rounds)}


def write_run(ping_store, store, run_id: str, started: float, status: str) -> None:
    request_id = store.begin_run(
        KEY,
        TARGETS,
        [],
        HOSTS,
        run_id,
        source="198.51.100.44",
        started=started,
        interval=None,
        until=None,
        host="interop-test-host",
        pid=5678,
        heartbeat=started + 0.125,
    )
    observation = "!." if status == "finished" else "R."
    rows = [
        ping_store.ResultRow(
            "interop.example",
            "192.0.2.44",
            started + 0.25,
            "OK (2/2 replies)" if status == "finished" else "REJECTED",
            None,
        )
    ]
    store.record_round(run_id, started + 0.25, observation, rows)
    store.end_run(run_id, ended=started + 0.5, status=status)
    return request_id


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--source", required=True, type=Path)
    parser.add_argument("--operation", choices=("report", "create", "append", "append-many", "append-prune", "hold-transaction", "hold-snapshot", "verify"), required=True)
    parser.add_argument("--database", type=Path)
    parser.add_argument("--run-id", default="python-appended")
    parser.add_argument("--started", type=float, default=1800000002.125)
    parser.add_argument("--count", type=int, default=10)
    parser.add_argument("--ready-file", type=Path)
    parser.add_argument("--start-file", type=Path)
    parser.add_argument("--release-file", type=Path)
    parser.add_argument("--expect-run", action="append", default=[])
    parser.add_argument("--expect-absent-run", action="append", default=[])
    parser.add_argument("--expect-status", action="append", default=[])
    args = parser.parse_args()
    ping_store, facts = load_reference(args.source)
    if args.operation == "report":
        return report(args.source)
    if args.database is None:
        parser.error("--database is required for database operations")
    database = args.database.resolve()

    if args.operation == "create":
        if database.exists():
            raise RuntimeError(f"refusing to overwrite an existing database: {database.name}")
        with ping_store.PingStore.open(database, write=True) as store:
            if store is None:
                raise RuntimeError("source PingStore did not create a writer")
            write_run(ping_store, store, "python-created", 1800000000.125, "finished")
    elif args.operation == "append":
        if not database.is_file():
            raise RuntimeError(f"cannot append because the database does not exist: {database.name}")
        with ping_store.PingStore.open(database, write=True) as store:
            if store is None:
                raise RuntimeError("source PingStore did not open a writer")
            write_run(ping_store, store, args.run_id, args.started, "stopped")
    elif args.operation == "append-many":
        if not database.is_file():
            raise RuntimeError(f"cannot append because the database does not exist: {database.name}")
        facts.update(append_many(ping_store, database, args.count, args.ready_file, args.start_file))
    elif args.operation == "append-prune":
        if not database.is_file():
            raise RuntimeError(f"cannot append/prune because the database does not exist: {database.name}")
        with ping_store.PingStore.open(database, write=True) as store:
            if store is None:
                raise RuntimeError("source PingStore did not open a writer")
            write_run(ping_store, store, "python-after-prune", 1800000400.125, "finished")
            deleted = store.prune(now=1800005000.125, days=30)
            if deleted != 1:
                raise RuntimeError(f"source Python prune removed {deleted} rounds, want the one old round")
            facts["pruned_rounds"] = deleted
    elif args.operation == "hold-transaction":
        if not database.is_file() or args.ready_file is None:
            raise RuntimeError("hold-transaction requires an existing database and --ready-file")
        hold_uncommitted_update(ping_store, database, args.run_id, args.ready_file)
    elif args.operation == "hold-snapshot":
        if not database.is_file() or args.ready_file is None or args.release_file is None:
            raise RuntimeError("hold-snapshot requires an existing database and ready/release files")
        facts.update(hold_snapshot(ping_store, database, args.ready_file, args.release_file))
    else:
        if not database.is_file():
            raise RuntimeError(f"cannot verify because the database does not exist: {database.name}")
        with ping_store.PingStore.open(database, write=False) as store:
            if store is None:
                raise RuntimeError("source PingStore did not find the Go-created history")
            request = store.request(KEY)
            if request is None or request.targets != tuple(TARGETS) or request.tcp_ports != () or request.hosts != tuple(HOSTS):
                raise RuntimeError("source PingStore request metadata differs from the Go/Python contract")
            runs = store.runs(request.id)
            run_ids = {run.id for run in runs}
            missing = sorted(set(args.expect_run) - run_ids)
            if missing:
                raise RuntimeError(f"source PingStore cannot see expected committed runs: {missing}")
            unexpected = sorted(set(args.expect_absent_run) & run_ids)
            if unexpected:
                raise RuntimeError(f"source PingStore still sees runs expected to be pruned: {unexpected}")
            statuses = {run.id: run.status for run in runs}
            for requirement in args.expect_status:
                run_id, separator, expected_status = requirement.partition("=")
                if not separator or statuses.get(run_id) != expected_status:
                    raise RuntimeError(f"source status for {run_id!r} is {statuses.get(run_id)!r}, want {expected_status!r}")
            rounds = store.rounds(request.id)
            if len(rounds) != len(runs) or any(len(round_.rows) != 1 for round_ in rounds):
                raise RuntimeError("source PingStore did not read complete committed round details")

    facts.update({"operation": args.operation, "database": database.name})
    print("CNMON_PYTHON_INTEROP_RESULT " + json.dumps(facts, ensure_ascii=True, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
