#!/usr/bin/env python3
"""data_layer_capacity_verdict.py — turn the read-only capacity probe output into a
deterministic verdict (green | approaching | trigger) for the data-layer / RDS-extraction
decision (#587 Trigger B).

This is the *logic* half of the capacity check; the *transport* half is the read-only
bash probe `probe-data-layer-capacity.sh` (delivered to the host via run-probe.sh).
Keeping the verdict here (pure Python, no AWS) makes it unit-testable with fixtures
(`--selftest`) and registerable in preflight, mirroring the determinism contract in
dev-rules-convention.mdc §"skill / command 确定性基线".

The signal is the LEDGER's growth runway against the data volume's free space, NOT
current total disk % (which is polluted by OS / docker images / ops logs):

    monthly_growth_bytes = usage_logs_rows_30d * (usage_logs_bytes / usage_logs_rows)
    months_to_volume_full = df_avail_bytes / monthly_growth_bytes
    usage_logs_pct_of_volume = usage_logs_bytes / df_total_bytes * 100

Input (stdin): the probe's tagged JSON lines:
    PGSTATS {"usage_logs_bytes":..., "usage_logs_rows":..., "usage_logs_rows_30d":..., ...}
    DFSTATS {"df_total_bytes":..., "df_avail_bytes":..., ...}

Output (stdout): one JSON object with the computed metrics + "verdict".
Exit code is always 0 in normal mode (verdict is in the payload); --selftest exits 1 on failure.
"""
from __future__ import annotations

import argparse
import json
import math
import pathlib
import sys

_DEFAULT_THRESHOLDS = pathlib.Path(__file__).with_name("capacity-thresholds.json")


def _load_thresholds(path: pathlib.Path) -> dict:
    data = json.loads(path.read_text(encoding="utf-8"))
    return data["thresholds"]


def compute_verdict(stats: dict, thresholds: dict) -> dict:
    """Pure function: probe stats + thresholds -> verdict payload. No I/O."""
    usage_logs_bytes = float(stats.get("usage_logs_bytes") or 0)
    usage_logs_rows = float(stats.get("usage_logs_rows") or 0)
    rows_30d = float(stats.get("usage_logs_rows_30d") or 0)
    df_total = float(stats.get("df_total_bytes") or 0)
    df_avail = float(stats.get("df_avail_bytes") or 0)

    avg_row = usage_logs_bytes / usage_logs_rows if usage_logs_rows > 0 else 0.0
    monthly_growth = rows_30d * avg_row  # ~bytes added to usage_logs per 30 days

    # No volume data (df missing / probe partial failure) => inconclusive, NOT a
    # trigger. Guard on df_total only: df_total>0 with df_avail==0 is a genuinely
    # full disk and SHOULD still trigger below.
    if df_total <= 0:
        return {
            "verdict": "unknown",
            "usage_logs_gib": round(usage_logs_bytes / 1024**3, 3),
            "monthly_growth_gib": round(monthly_growth / 1024**3, 3),
            "months_to_volume_full": None,
            "usage_logs_pct_of_volume": None,
            "df_avail_gib": None,
            "summary": "no volume (df) data from probe — capacity verdict inconclusive",
        }

    months_to_full = (df_avail / monthly_growth) if monthly_growth > 0 else math.inf
    pct_of_volume = usage_logs_bytes / df_total * 100.0

    m = thresholds["months_to_volume_full"]
    p = thresholds["usage_logs_pct_of_volume"]

    if months_to_full <= m["trigger"] or pct_of_volume >= p["trigger"]:
        verdict = "trigger"
    elif months_to_full <= m["approaching"] or pct_of_volume >= p["approaching"]:
        verdict = "approaching"
    else:
        verdict = "green"

    return {
        "verdict": verdict,
        "usage_logs_gib": round(usage_logs_bytes / 1024**3, 3),
        "monthly_growth_gib": round(monthly_growth / 1024**3, 3),
        "months_to_volume_full": (None if months_to_full == math.inf else round(months_to_full, 1)),
        "usage_logs_pct_of_volume": round(pct_of_volume, 1),
        "df_avail_gib": round(df_avail / 1024**3, 2),
        "summary": (
            f"usage_logs {round(usage_logs_bytes/1024**3,2)}GiB "
            f"({round(pct_of_volume,1)}% of volume), growth ~{round(monthly_growth/1024**3,2)}GiB/30d, "
            f"runway {'∞' if months_to_full==math.inf else str(round(months_to_full,1))+'mo'} -> {verdict}"
        ),
    }


def _parse_probe_stdin(text: str) -> dict:
    """Merge the probe's tagged JSON lines (PGSTATS {...} / DFSTATS {...})."""
    stats: dict = {}
    for line in text.splitlines():
        line = line.strip()
        for tag in ("PGSTATS", "DFSTATS"):
            if line.startswith(tag):
                payload = line[len(tag):].strip()
                try:
                    stats.update(json.loads(payload))
                except json.JSONDecodeError:
                    pass
    return stats


# --- selftest fixtures: (name, stats, expected_verdict) -----------------------
_FIXTURES = [
    (
        "green_low_growth",
        # 1.2GiB ledger on a 30GiB volume, modest 30d growth, lots of runway
        {"usage_logs_bytes": 1.2 * 1024**3, "usage_logs_rows": 2_000_000,
         "usage_logs_rows_30d": 200_000, "df_total_bytes": 30 * 1024**3, "df_avail_bytes": 18 * 1024**3},
        "green",
    ),
    (
        "approaching_runway",
        # growth eats free space in ~5 months (<=6 warn, >3 trigger)
        {"usage_logs_bytes": 4 * 1024**3, "usage_logs_rows": 8_000_000,
         "usage_logs_rows_30d": 1_000_000, "df_total_bytes": 30 * 1024**3, "df_avail_bytes": 2.5 * 1024**3},
        "approaching",
    ),
    (
        "trigger_runway",
        # ~2 months of runway (<=3 trigger)
        {"usage_logs_bytes": 5 * 1024**3, "usage_logs_rows": 8_000_000,
         "usage_logs_rows_30d": 2_000_000, "df_total_bytes": 30 * 1024**3, "df_avail_bytes": 2 * 1024**3},
        "trigger",
    ),
    (
        "trigger_absolute_pct",
        # usage_logs alone >= 40% of volume, even with zero growth
        {"usage_logs_bytes": 13 * 1024**3, "usage_logs_rows": 20_000_000,
         "usage_logs_rows_30d": 0, "df_total_bytes": 30 * 1024**3, "df_avail_bytes": 15 * 1024**3},
        "trigger",
    ),
    (
        "green_zero_growth_small",
        # brand-new / low-traffic edge: no 30d rows, tiny ledger -> infinite runway
        {"usage_logs_bytes": 50 * 1024**2, "usage_logs_rows": 1000,
         "usage_logs_rows_30d": 0, "df_total_bytes": 30 * 1024**3, "df_avail_bytes": 25 * 1024**3},
        "green",
    ),
    (
        "unknown_missing_df",
        # probe failed to read df (no DFSTATS) + nonzero growth: must NOT false-trigger
        {"usage_logs_bytes": 5 * 1024**3, "usage_logs_rows": 8_000_000,
         "usage_logs_rows_30d": 2_000_000, "df_total_bytes": 0, "df_avail_bytes": 0},
        "unknown",
    ),
    (
        "trigger_disk_genuinely_full",
        # df present (df_total>0) but df_avail==0 => genuinely full => trigger is correct
        {"usage_logs_bytes": 5 * 1024**3, "usage_logs_rows": 8_000_000,
         "usage_logs_rows_30d": 2_000_000, "df_total_bytes": 30 * 1024**3, "df_avail_bytes": 0},
        "trigger",
    ),
]


def _selftest(thresholds: dict) -> int:
    failures = 0
    for name, stats, expected in _FIXTURES:
        got = compute_verdict(stats, thresholds)["verdict"]
        ok = got == expected
        print(f"[selftest] {name}: got={got} expected={expected} {'OK' if ok else 'FAIL'}")
        if not ok:
            failures += 1
    print(f"[selftest] {len(_FIXTURES) - failures}/{len(_FIXTURES)} passed")
    return 1 if failures else 0


def main() -> int:
    ap = argparse.ArgumentParser(description="data-layer capacity verdict")
    ap.add_argument("--thresholds", type=pathlib.Path, default=_DEFAULT_THRESHOLDS)
    ap.add_argument("--selftest", action="store_true", help="run fixtures, no stdin/AWS")
    args = ap.parse_args()

    thresholds = _load_thresholds(args.thresholds)

    if args.selftest:
        return _selftest(thresholds)

    stats = _parse_probe_stdin(sys.stdin.read())
    if not stats:
        print(json.dumps({"verdict": "unknown", "summary": "no probe stats on stdin"}))
        return 0
    print(json.dumps(compute_verdict(stats, thresholds)))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
