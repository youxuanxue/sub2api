"""Shared node1 archive capacity policy.

The policy intentionally uses directory receipts for dataset accounting and the
filesystem free space for the hard global guard.  It does not implement a
filesystem quota; the node1 writers are root-only and publish immutable
receipts, so a second quota system would add operational complexity without
protecting a different failure mode.
"""

from __future__ import annotations

import json
import shutil
from pathlib import Path


TIB = 1024**4
QA_CAP_BYTES = 6 * TIB
DB_CAP_BYTES = 2 * TIB
WARN_USED_PERCENT = 85.0
BLOCK_USED_PERCENT = 90.0
EMERGENCY_USED_PERCENT = 95.0


class StoragePolicyError(RuntimeError):
    pass


def check_write(*, filesystem_root: Path, dataset_used_bytes: int,
                required_bytes: int, dataset_cap_bytes: int) -> dict[str, float | int | bool]:
    """Validate one atomic write before it starts.

    ``dataset_used_bytes`` comes from verified immutable receipts.  The global
    filesystem guard remains authoritative for files outside the archive tree
    and for interrupted staging copies.
    """
    if min(dataset_used_bytes, required_bytes, dataset_cap_bytes) < 0:
        raise StoragePolicyError("storage sizes cannot be negative")
    if dataset_cap_bytes == 0:
        raise StoragePolicyError("dataset cap must be positive")
    probe = filesystem_root
    while not probe.exists() and probe != probe.parent:
        probe = probe.parent
    usage = shutil.disk_usage(probe)
    projected_dataset = dataset_used_bytes + required_bytes
    if projected_dataset > dataset_cap_bytes:
        raise StoragePolicyError(
            f"dataset cap exceeded: projected={projected_dataset} cap={dataset_cap_bytes}"
        )
    if usage.total <= 0:
        raise StoragePolicyError("filesystem reports zero capacity")
    projected_free = usage.free - required_bytes
    projected_used_percent = (usage.total - projected_free) * 100.0 / usage.total
    if projected_used_percent >= EMERGENCY_USED_PERCENT:
        raise StoragePolicyError(
            f"filesystem emergency threshold reached at {projected_used_percent:.1f}% used"
        )
    if projected_used_percent >= BLOCK_USED_PERCENT:
        raise StoragePolicyError(
            f"filesystem write blocked at {projected_used_percent:.1f}% used "
            f"(block threshold {BLOCK_USED_PERCENT:.1f}%)"
        )
    return {
        "filesystem_total_bytes": usage.total,
        "filesystem_free_bytes": usage.free,
        "projected_free_bytes": projected_free,
        "projected_used_percent": round(projected_used_percent, 2),
        "warning": projected_used_percent >= WARN_USED_PERCENT,
        "dataset_used_bytes": dataset_used_bytes,
        "dataset_required_bytes": required_bytes,
        "dataset_projected_bytes": projected_dataset,
        "dataset_cap_bytes": dataset_cap_bytes,
    }


def receipt_bytes(receipt_root: Path, *, kind: str) -> int:
    """Sum bytes from verified receipts without walking archived payloads."""
    total = 0
    for receipt in sorted(receipt_root.rglob("*.json")) if receipt_root.exists() else ():
        try:
            payload = json.loads(receipt.read_text(encoding="utf-8"))
        except (OSError, ValueError):
            continue
        if kind == "qa" and payload.get("schema_version") == "qa-node1-hour-v1" and payload.get("ok") is True:
            total += int(payload.get("total_bytes", 0))
        elif kind in ("db", "qa") and payload.get("status") == "verified":
            total += int((payload.get("manifest") or {}).get("total_bytes", 0))
    return total
