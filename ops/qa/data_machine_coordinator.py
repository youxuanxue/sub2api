#!/usr/bin/env python3
"""Coordinate source-isolated QA cleanup from the data machine.

The coordinator is deliberately a policy engine, not a database client.  It
reads immutable source receipts produced on node1 and emits an append-only
hour plan.  App hosts may consume the plan to remove their own hot files;
shared database cleanup is allowed only when every expected source is present,
or when an explicitly configured and approved gap deadline has passed.
"""
from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import re
import tempfile
from typing import Any


SCHEMA = "qa-data-machine-cleanup-v1"
SOURCE_SCHEMA = "qa-node1-source-hour-v2"
SOURCE_ID = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,63}\Z")
HOUR = "%Y/%m/%d/%H"


class CoordinatorError(ValueError):
    pass


def load_private_config(path: Path) -> dict:
    info = path.lstat()
    if path.is_symlink() or not path.is_file() or info.st_uid != 0 or info.st_mode & 0o077:
        raise CoordinatorError("runtime config must be a root-owned private regular file")
    value = json.loads(path.read_text())
    if not isinstance(value, dict):
        raise CoordinatorError("runtime config must be an object")
    return value


def safe_path(value: str) -> bool:
    return isinstance(value, str) and bool(re.fullmatch(r"/[A-Za-z0-9_./-]+", value)) and ".." not in Path(value).parts


def _hour(value: str | dt.datetime) -> dt.datetime:
    if isinstance(value, str):
        try:
            value = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
        except ValueError as exc:
            raise CoordinatorError("hour must be an ISO-8601 timestamp") from exc
    if not isinstance(value, dt.datetime) or value.tzinfo is None or value.utcoffset() != dt.timedelta(0):
        raise CoordinatorError("hour must use UTC")
    value = value.astimezone(dt.timezone.utc)
    if value.minute or value.second or value.microsecond:
        raise CoordinatorError("hour must start exactly on a UTC hour")
    return value


def _sources(values: list[str]) -> list[str]:
    if not values:
        raise CoordinatorError("at least one expected source is required")
    if len(set(values)) != len(values):
        raise CoordinatorError("expected sources must be unique")
    for value in values:
        if not SOURCE_ID.fullmatch(value):
            raise CoordinatorError(f"unsafe source identity: {value!r}")
    return sorted(values)


def receipt_path(root: Path, environment: str, source_id: str, hour: dt.datetime) -> Path:
    if environment not in ("prod", "cn-prod"):
        raise CoordinatorError("unknown QA environment")
    if not SOURCE_ID.fullmatch(source_id):
        raise CoordinatorError("unsafe source identity")
    return root / environment / "sources" / source_id / (hour.strftime(HOUR) + ".json")


def _read_receipt(root: Path, environment: str, source_id: str, hour: dt.datetime) -> tuple[str, dict | str | None]:
    path = receipt_path(root, environment, source_id, hour)
    try:
        if path.is_symlink():
            return "invalid", "receipt is a symlink"
        if not path.exists():
            return "missing", None
        if not path.is_file():
            return "invalid", "receipt is not a regular file"
        value = json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError:
        return "missing", None
    except (OSError, json.JSONDecodeError):
        return "invalid", "receipt unreadable or invalid JSON"
    expected = {
        "schema_version": SOURCE_SCHEMA,
        "environment": environment,
        "source_id": source_id,
        "window_start": hour.isoformat(),
        "window_end": (hour + dt.timedelta(hours=1)).isoformat(),
    }
    if not isinstance(value, dict) or any(value.get(key) != wanted for key, wanted in expected.items()):
        return "invalid", "receipt identity mismatch"
    if (value.get("ok") is not True
            or not all(isinstance(value.get(key), str) and re.fullmatch(r"[0-9a-f]{64}", value[key])
                       for key in ("manifest_sha256", "seal_digest"))
            or type(value.get("file_count")) is not int or value["file_count"] < 0
            or type(value.get("total_bytes")) is not int or value["total_bytes"] < 0):
        return "invalid", "receipt integrity fields are incomplete"
    return "complete", value


def build_plan(
    *,
    receipt_root: Path,
    environment: str,
    hour: dt.datetime,
    expected_sources: list[str],
    now: dt.datetime | None = None,
    gap_after: dt.timedelta | None = None,
    allow_gap: bool = False,
) -> dict[str, Any]:
    hour = _hour(hour)
    expected_sources = _sources(expected_sources)
    now = now or dt.datetime.now(dt.timezone.utc)
    if now.tzinfo is None or now.utcoffset() != dt.timedelta(0):
        raise CoordinatorError("coordinator clock must use UTC")
    if gap_after is not None and gap_after <= dt.timedelta(0):
        raise CoordinatorError("gap deadline must be positive")
    if allow_gap and gap_after is None:
        raise CoordinatorError("gap deletion requires an explicit deadline")
    source_states = {}
    for source_id in expected_sources:
        state, detail = _read_receipt(receipt_root, environment, source_id, hour)
        source_states[source_id] = {"state": state, **({key: detail[key] for key in ("manifest_sha256", "seal_digest")} if state == "complete" else {"reason": detail})}
    expired = hour + dt.timedelta(hours=25) <= now.replace(minute=0, second=0, microsecond=0)
    complete = [source for source, item in source_states.items() if item["state"] == "complete" and expired]
    missing = [source for source, item in source_states.items() if item["state"] != "complete"]
    gap_deadline = hour + dt.timedelta(hours=1) + gap_after if gap_after else None
    gap_ready = bool(allow_gap and gap_deadline and now >= gap_deadline)
    shared_action = ("delete" if not missing else ("gap-delete" if gap_ready else "hold")) if expired else "hold"
    plan = {
        "schema_version": SCHEMA,
        "environment": environment,
        "window_start": hour.isoformat(),
        "window_end": (hour + dt.timedelta(hours=1)).isoformat(),
        "expected_sources": expected_sources,
        "sources": source_states,
        "source_cleanup_allowed": complete,
        "retention_expired": expired,
        "shared_partition_action": shared_action,
        "gap_deadline": gap_deadline.isoformat() if gap_deadline else None,
        "gap_policy": "approved-after-deadline" if allow_gap else "hold-until-all-sources",
        "created_at": now.isoformat(),
    }
    canonical = json.dumps(plan, sort_keys=True, separators=(",", ":")).encode()
    plan["plan_sha256"] = hashlib.sha256(canonical).hexdigest()
    return plan


def write_plan(path: Path, plan: dict[str, Any]) -> None:
    path = path.expanduser().resolve()
    if path.exists():
        try:
            existing = json.loads(path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as exc:
            raise CoordinatorError("existing cleanup plan is unreadable") from exc
        if existing != plan:
            raise CoordinatorError("refusing to overwrite a different cleanup plan")
        return
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o750)
    fd, temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            json.dump(plan, handle, sort_keys=True, separators=(",", ":"))
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(temporary, 0o640)
        # link is a no-clobber publication even when two invocations race.
        os.link(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--receipt-root", type=Path, required=True)
    parser.add_argument("--environment", choices=("prod", "cn-prod"), required=True)
    parser.add_argument("--hour", required=True)
    parser.add_argument("--source", action="append", dest="sources", required=True)
    parser.add_argument("--gap-after-hours", type=float, default=0)
    parser.add_argument("--allow-gap", action="store_true")
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        gap = dt.timedelta(hours=args.gap_after_hours) if args.gap_after_hours else None
        plan = build_plan(
            receipt_root=args.receipt_root,
            environment=args.environment,
            hour=args.hour,
            expected_sources=args.sources,
            gap_after=gap,
            allow_gap=args.allow_gap,
        )
        write_plan(args.output, plan)
    except (OSError, CoordinatorError) as exc:
        print(f"qa coordinator: {exc}", file=os.sys.stderr)
        return 1
    print(json.dumps(plan, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
