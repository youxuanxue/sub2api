#!/usr/bin/env python3
"""Validate qa_records JSONL blob_uri references against an extracted qa_blobs tree."""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any

BLOB_FIELDS = ("blob_uri", "request_blob_uri", "response_blob_uri", "stream_blob_uri")
CONTAINER_PREFIXES = ("file:///app/data/qa_blobs/", "file:///var/lib/tokenkey/app/qa_blobs/")


def local_path_for_uri(uri: str, blob_root: Path) -> Path | None:
    uri = uri.strip()
    if not uri:
        return None
    for prefix in CONTAINER_PREFIXES:
        if uri.startswith(prefix):
            rel = uri[len(prefix) :]
            break
    else:
        return None
    path = blob_root / rel
    try:
        path.resolve().relative_to(blob_root.resolve())
    except ValueError:
        raise ValueError(f"unsafe blob uri path escape: {uri}")
    return path


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("records_jsonl")
    parser.add_argument("blob_root")
    parser.add_argument("--json", action="store_true")
    args = parser.parse_args()

    records_path = Path(args.records_jsonl)
    blob_root = Path(args.blob_root)
    failures: list[str] = []
    referenced = 0
    checked = 0

    if not records_path.is_file():
        failures.append(f"records file missing: {records_path}")
    if not blob_root.is_dir():
        failures.append(f"blob root missing: {blob_root}")

    if not failures:
        with records_path.open(encoding="utf-8") as handle:
            for line_no, raw in enumerate(handle, start=1):
                raw = raw.strip()
                if not raw:
                    continue
                try:
                    row: dict[str, Any] = json.loads(raw)
                except json.JSONDecodeError as exc:
                    failures.append(f"line {line_no}: invalid JSON ({exc})")
                    continue
                for field in BLOB_FIELDS:
                    value = row.get(field)
                    if not isinstance(value, str) or not value.strip():
                        continue
                    referenced += 1
                    try:
                        path = local_path_for_uri(value, blob_root)
                    except ValueError as exc:
                        failures.append(f"line {line_no} {field}: {exc}")
                        continue
                    if path is None:
                        continue
                    checked += 1
                    if not path.is_file():
                        failures.append(f"line {line_no} {field}: missing local blob for {value}")

    report = {"referenced_blob_uris": referenced, "checked_local_blob_uris": checked, "failures": failures}
    if args.json:
        json.dump(report, sys.stdout, indent=2)
        sys.stdout.write("\n")
    else:
        print(
            "qa blob reference check: "
            f"referenced={referenced} checked_local={checked} failures={len(failures)}"
        )
        for failure in failures:
            print(f"  - {failure}")
    return 0 if not failures else 1


if __name__ == "__main__":
    raise SystemExit(main())
