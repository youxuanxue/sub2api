#!/usr/bin/env python3
"""Project capacity-first runway from a data-layer probe snapshot.

This is deliberately offline: it consumes tagged probe output or a JSON object
on stdin and never calls AWS, PostgreSQL, Docker, or the network. Reclaim and
residual-growth assumptions must be explicit so a planning estimate cannot be
mistaken for live evidence.
"""

from __future__ import annotations

import argparse
import json
import math
import sys
from typing import Any

GIB = 1024**3


def _finite_nonnegative(name: str, value: float) -> float:
    value = float(value)
    if not math.isfinite(value) or value < 0:
        raise ValueError(f"{name} must be a finite non-negative number")
    return value


def _positive(name: str, value: float) -> float:
    value = float(value)
    if not math.isfinite(value) or value <= 0:
        raise ValueError(f"{name} must be a finite positive number")
    return value


def _months_until(limit_gib: float, used_gib: float, growth_gib: float) -> float | None:
    if used_gib >= limit_gib:
        return 0.0
    if growth_gib <= 0:
        return None
    return round((limit_gib - used_gib) / growth_gib, 1)


def _parse_snapshot(text: str) -> dict[str, Any]:
    text = text.strip()
    if not text:
        raise ValueError("stdin is empty")
    if text.startswith("{"):
        doc = json.loads(text)
        if not isinstance(doc, dict):
            raise ValueError("snapshot JSON must be an object")
        return doc

    stats: dict[str, Any] = {}
    for line in text.splitlines():
        line = line.strip()
        for tag in ("PGSTATS", "PGGROWTH", "DFSTATS"):
            if line.startswith(tag):
                payload = json.loads(line[len(tag):].strip())
                if not isinstance(payload, dict):
                    raise ValueError(f"{tag} payload must be an object")
                stats.update(payload)
    if not stats:
        raise ValueError("stdin contains no PGSTATS/PGGROWTH/DFSTATS payload")
    return stats


def project_capacity(
    stats: dict[str, Any],
    *,
    target_volume_gib: float,
    usage_hot_days: float,
    ops_reclaim_gib_low: float,
    ops_reclaim_gib_high: float,
    residual_growth_gib_per_month: float,
    operational_limit_pct: float,
) -> dict[str, Any]:
    target_volume_gib = _positive("target_volume_gib", target_volume_gib)
    usage_hot_days = _positive("usage_hot_days", usage_hot_days)
    low = _finite_nonnegative("ops_reclaim_gib_low", ops_reclaim_gib_low)
    high = _finite_nonnegative("ops_reclaim_gib_high", ops_reclaim_gib_high)
    residual = _finite_nonnegative(
        "residual_growth_gib_per_month", residual_growth_gib_per_month
    )
    operational_limit_pct = _positive("operational_limit_pct", operational_limit_pct)
    if high < low:
        raise ValueError("ops_reclaim_gib_high must be >= ops_reclaim_gib_low")
    if operational_limit_pct >= 100:
        raise ValueError("operational_limit_pct must be below 100")

    if stats.get("catalog_probe_ok") is not True:
        raise ValueError("catalog probe is inconclusive; do not project from missing evidence")
    if stats.get("growth_probe_ok") is not True:
        raise ValueError("growth probe is inconclusive; do not project from missing evidence")

    required = (
        "df_total_bytes",
        "df_used_bytes",
        "usage_logs_bytes",
        "usage_logs_rows",
        "usage_logs_rows_30d",
        "ops_system_logs_bytes",
        "ops_error_logs_bytes",
    )
    missing = [name for name in required if name not in stats or stats[name] is None]
    if missing:
        raise ValueError("snapshot missing required fields: " + ", ".join(missing))

    current_total_gib = _positive("df_total_bytes", float(stats["df_total_bytes"]) / GIB)
    current_used_gib = _finite_nonnegative(
        "df_used_bytes", float(stats["df_used_bytes"]) / GIB
    )
    current_usage_gib = _finite_nonnegative(
        "usage_logs_bytes", float(stats["usage_logs_bytes"]) / GIB
    )
    usage_rows = _positive("usage_logs_rows", float(stats["usage_logs_rows"]))
    rows_30d = _finite_nonnegative(
        "usage_logs_rows_30d", float(stats["usage_logs_rows_30d"])
    )
    observed_ops_gib = (
        _finite_nonnegative("ops_system_logs_bytes", float(stats["ops_system_logs_bytes"]))
        + _finite_nonnegative("ops_error_logs_bytes", float(stats["ops_error_logs_bytes"]))
    ) / GIB
    if current_used_gib > current_total_gib:
        raise ValueError("df_used_bytes cannot exceed df_total_bytes")
    if target_volume_gib < current_total_gib:
        raise ValueError("target volume cannot be smaller than the current volume")
    if high > observed_ops_gib:
        raise ValueError(
            "ops_reclaim_gib_high cannot exceed observed ops relation size "
            f"({observed_ops_gib:.3f} GiB)"
        )

    monthly_usage_growth_gib = rows_30d * (current_usage_gib / usage_rows)
    usage_steady_gib = monthly_usage_growth_gib * usage_hot_days / 30.0
    usage_growth_to_steady_gib = max(0.0, usage_steady_gib - current_usage_gib)

    scenarios: list[dict[str, Any]] = []
    for name, reclaim_gib in (("low_reclaim", low), ("high_reclaim", high)):
        projected_used_gib = max(0.0, current_used_gib + usage_growth_to_steady_gib - reclaim_gib)
        scenarios.append(
            {
                "name": name,
                "ops_reclaim_gib": round(reclaim_gib, 3),
                "projected_used_gib": round(projected_used_gib, 3),
                "projected_used_pct": round(projected_used_gib / target_volume_gib * 100.0, 1),
                "projected_free_gib": round(max(0.0, target_volume_gib - projected_used_gib), 3),
                "months_to_operational_limit": _months_until(
                    target_volume_gib * operational_limit_pct / 100.0,
                    projected_used_gib,
                    residual,
                ),
                "months_to_full": _months_until(target_volume_gib, projected_used_gib, residual),
            }
        )

    return {
        "schema_version": 1,
        "mode": "offline_projection",
        "current": {
            "volume_gib": round(current_total_gib, 3),
            "used_gib": round(current_used_gib, 3),
            "used_pct": round(current_used_gib / current_total_gib * 100.0, 1),
            "usage_logs_gib": round(current_usage_gib, 3),
            "monthly_usage_growth_gib": round(monthly_usage_growth_gib, 3),
            "observed_ops_relation_gib": round(observed_ops_gib, 3),
        },
        "assumptions": {
            "target_volume_gib": target_volume_gib,
            "usage_hot_days": usage_hot_days,
            "ops_reclaim_gib_low": low,
            "ops_reclaim_gib_high": high,
            "residual_growth_gib_per_month": residual,
            "operational_limit_pct": operational_limit_pct,
        },
        "derived": {
            "usage_steady_gib": round(usage_steady_gib, 3),
            "usage_growth_to_steady_gib": round(usage_growth_to_steady_gib, 3),
        },
        "scenarios": scenarios,
        "warning": (
            "projection only: reclaim must be proven by non-production restore/drop rehearsal "
            "and later by host df; PostgreSQL DELETE alone does not prove filesystem reclaim"
        ),
    }


def _selftest() -> int:
    stats = {
        "usage_logs_bytes": 5.4 * GIB,
        "usage_logs_rows": 9_000_000,
        "usage_logs_rows_30d": 6_000_000,
        "ops_system_logs_bytes": 7 * GIB,
        "ops_error_logs_bytes": 5 * GIB,
        "catalog_probe_ok": True,
        "growth_probe_ok": True,
        "df_total_bytes": 50 * GIB,
        "df_used_bytes": 36.8 * GIB,
    }
    out = project_capacity(
        stats,
        target_volume_gib=100,
        usage_hot_days=90,
        ops_reclaim_gib_low=5,
        ops_reclaim_gib_high=10,
        residual_growth_gib_per_month=0.5,
        operational_limit_pct=85,
    )
    assert out["derived"]["usage_steady_gib"] == 10.8
    assert out["scenarios"][0]["projected_used_gib"] == 37.2
    assert out["scenarios"][1]["projected_used_gib"] == 32.2
    assert out["scenarios"][0]["months_to_operational_limit"] == 95.6
    try:
        project_capacity(
            {**stats, "growth_probe_ok": False},
            target_volume_gib=100,
            usage_hot_days=90,
            ops_reclaim_gib_low=5,
            ops_reclaim_gib_high=10,
            residual_growth_gib_per_month=0.5,
            operational_limit_pct=85,
        )
    except ValueError as exc:
        assert "inconclusive" in str(exc)
    else:
        raise AssertionError("inconclusive growth must fail closed")
    print("data_layer_capacity_projection selftest: PASS")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--selftest", action="store_true")
    parser.add_argument("--target-volume-gib", type=float, default=100)
    parser.add_argument("--usage-hot-days", type=float, default=90)
    parser.add_argument("--ops-reclaim-gib-low", type=float, required=False)
    parser.add_argument("--ops-reclaim-gib-high", type=float, required=False)
    parser.add_argument("--residual-growth-gib-per-month", type=float, required=False)
    parser.add_argument("--operational-limit-pct", type=float, default=85)
    args = parser.parse_args()

    if args.selftest:
        return _selftest()
    missing = [
        flag
        for flag, value in (
            ("--ops-reclaim-gib-low", args.ops_reclaim_gib_low),
            ("--ops-reclaim-gib-high", args.ops_reclaim_gib_high),
            ("--residual-growth-gib-per-month", args.residual_growth_gib_per_month),
        )
        if value is None
    ]
    if missing:
        parser.error("planning assumptions must be explicit: " + ", ".join(missing))

    try:
        stats = _parse_snapshot(sys.stdin.read())
        out = project_capacity(
            stats,
            target_volume_gib=args.target_volume_gib,
            usage_hot_days=args.usage_hot_days,
            ops_reclaim_gib_low=args.ops_reclaim_gib_low,
            ops_reclaim_gib_high=args.ops_reclaim_gib_high,
            residual_growth_gib_per_month=args.residual_growth_gib_per_month,
            operational_limit_pct=args.operational_limit_pct,
        )
    except (ValueError, json.JSONDecodeError) as exc:
        print(json.dumps({"ok": False, "error": str(exc)}, sort_keys=True))
        return 2
    print(json.dumps(out, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
