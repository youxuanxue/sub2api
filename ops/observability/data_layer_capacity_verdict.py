#!/usr/bin/env python3
"""Evaluate the bounded capacity probe as green, approaching, trigger, or unknown.

This is the *logic* half of the capacity check; the *transport* half is the read-only
bash probe `probe-data-layer-capacity.sh` delivered through `run-probe.sh`.
Keeping the verdict here (pure Python, no AWS) makes it unit-testable with fixtures
(`--selftest`) and registerable in preflight, mirroring the determinism contract in
dev-rules-convention.mdc §"skill / command 确定性基线".

The signal is the LEDGER's growth runway against the data volume's free space, NOT
current total disk % (which is polluted by OS / docker images / ops logs):

    monthly_growth_bytes = sum(bytes of day partitions whose lower bound is in the window)
                           scaled to 30 days when the window is partially retained
    months_to_volume_full = df_avail_bytes / monthly_growth_bytes
    usage_logs_pct_of_volume = usage_logs_bytes / df_total_bytes * 100

Growth is measured in BYTES straight from the partition catalog. It used to be
derived as rows_30d * (usage_logs_bytes / usage_logs_rows), with `usage_logs_rows`
coming from pg_stat_user_tables.n_live_tup -- a cumulative counter that lives in
the stats file rather than in the data files. pgBackRest restores data files, so
that counter starts from zero and the ratio becomes fiction: live prod reported
22.8k rows for a 7.1GiB table whose catalog reltuples said 7.7M, inflating average
row size ~390x and turning a 12-month runway into a fabricated 0.0-month
"trigger". The two `rows` terms always cancelled algebraically; dropping them
removes the stats dependency along with the whole class of bug, on every restore,
host move and standby promotion rather than one at a time.

Cardinality is kept only as an independent cross-check (see _cross_check), never
as an input to the verdict.

Input (stdin): the probe's tagged JSON lines:
    PGSTATS {"catalog_probe_ok":true, "usage_logs_bytes":..., "usage_logs_rows":..., ...}
    PGWINDOW {"window_probe_ok":true, "window_span_days":30, "window_tables":{...}}
    PGGROWTH {"growth_probe_ok":true, "usage_logs_rows_30d":..., ...}
    DFSTATS {"df_total_bytes":..., "df_avail_bytes":..., ...}

Output (stdout): one JSON object with the computed metrics + "verdict".
Exit code is always 0 in normal mode (verdict is in the payload); --selftest exits 1 on failure.
"""
from __future__ import annotations

import argparse
import json
import math
import re
import pathlib
import sys

_DEFAULT_THRESHOLDS = pathlib.Path(__file__).with_name("capacity-thresholds.json")


def _load_thresholds(path: pathlib.Path) -> dict:
    data = json.loads(path.read_text(encoding="utf-8"))
    return data["thresholds"]


# The ledger whose day partitions define growth against the data volume. The
# probe reports the other partitioned ops tables too, but only as evidence: see
# the long note in compute_verdict for why they are not summed into growth.
GROWTH_TABLE = "usage_logs"


def _inconclusive(usage_logs_bytes: float, df_total: float, df_avail: float, summary: str) -> dict:
    """Abstain while still reporting what WAS measured.

    Occupancy and free space come from the catalog and from `df`; only growth was
    unusable. Blanking everything would hide a volume that is already nearly full.
    """
    return {
        "verdict": "unknown",
        "usage_logs_gib": round(usage_logs_bytes / 1024**3, 3),
        "monthly_growth_gib": None,
        "months_to_volume_full": None,
        "usage_logs_pct_of_volume": (
            round(usage_logs_bytes / df_total * 100.0, 1) if df_total > 0 else None
        ),
        "df_avail_gib": round(df_avail / 1024**3, 2) if df_total > 0 else None,
        "summary": summary,
    }


def _cross_check(stats: dict, window: dict, monthly_growth: float) -> dict | None:
    """Independently re-derive growth from cardinality, for corroboration only.

    reltuples is a pg_class field, so unlike n_live_tup it travels with the data
    files and survives a physical restore -- where it is populated it is a real
    second opinion on the byte path. It never changes the verdict: a disagreement
    is reported, not acted on, because the byte path is the one a restore cannot
    invalidate. Returns None when the inputs are unusable, which is normal on a
    host where nothing has been analyzed yet.

    It reads the window's own reltuples rather than PGGROWTH's row count: the
    exact scan PGGROWTH performs is the one that times out at statement_timeout=2s
    on a 5M-row window, so the cross-check would otherwise be absent precisely on
    the hosts where corroboration is most wanted.
    """
    entry = window.get(GROWTH_TABLE)
    if not isinstance(entry, dict):
        return None
    try:
        total_bytes = float(stats["usage_logs_bytes"])
        rows_total = float(stats["usage_logs_reltuples"])
        rows_window = float(entry["window_rows_est"])
    except (KeyError, TypeError, ValueError):
        return None
    if min(total_bytes, rows_total, rows_window) <= 0:
        return None
    if not all(math.isfinite(v) for v in (total_bytes, rows_total, rows_window)):
        return None
    implied = rows_window * (total_bytes / rows_total)
    if monthly_growth <= 0 or implied <= 0:
        return None
    return {
        "method": "window reltuples x avg_row",
        "monthly_growth_gib": round(implied / 1024**3, 3),
        "ratio_to_primary": round(implied / monthly_growth, 3),
    }


def compute_verdict(stats: dict, thresholds: dict) -> dict:
    """Pure function: probe stats + thresholds -> verdict payload. No I/O."""
    if stats.get("catalog_probe_ok") is not True:
        return {
            "verdict": "unknown",
            "usage_logs_gib": None,
            "monthly_growth_gib": None,
            "months_to_volume_full": None,
            "usage_logs_pct_of_volume": None,
            "df_avail_gib": None,
            "df_avail_source": None,
            "summary": "catalog capacity probe missing or failed - capacity verdict inconclusive",
        }

    # The byte path is the growth owner, so a failed row scan no longer blocks the
    # verdict: it only costs the cross-check. The byte path has its own
    # fail-closed branch below.
    if stats.get("growth_probe_ok") is not True and stats.get("window_probe_ok") is not True:
        return {
            "verdict": "unknown",
            "usage_logs_gib": None,
            "monthly_growth_gib": None,
            "months_to_volume_full": None,
            "usage_logs_pct_of_volume": None,
            "df_avail_gib": None,
            "df_avail_source": None,
            "summary": "bounded recent-growth probe missing or failed - capacity verdict inconclusive",
        }

    # usage_logs_rows / usage_logs_rows_30d are deliberately absent: they feed the
    # cross-check, not the verdict.
    required = (
        "usage_logs_bytes",
        "df_total_bytes",
        "df_avail_bytes",
    )
    missing = [name for name in required if name not in stats or stats[name] is None]
    if missing:
        return {
            "verdict": "unknown",
            "usage_logs_gib": None,
            "monthly_growth_gib": None,
            "months_to_volume_full": None,
            "usage_logs_pct_of_volume": None,
            "df_avail_gib": None,
            "df_avail_source": None,
            "summary": f"probe snapshot missing {', '.join(missing)} - capacity verdict inconclusive",
        }

    try:
        usage_logs_bytes = float(stats["usage_logs_bytes"])
        df_total = float(stats["df_total_bytes"])
        df_avail = float(stats["df_avail_bytes"])
        # Optional: present only when the data volume is a file on another
        # filesystem (loop/LUKS). Absent or null on raw-partition hosts, and on
        # probe revisions predating DFBACKING -- both read as "no outer layer".
        _backing_raw = stats.get("backing_avail_bytes")
        backing_avail = None if _backing_raw is None else float(_backing_raw)
        backing_mount = stats.get("backing_mount")
    except (TypeError, ValueError):
        return {
            "verdict": "unknown",
            "usage_logs_gib": None,
            "monthly_growth_gib": None,
            "months_to_volume_full": None,
            "usage_logs_pct_of_volume": None,
            "df_avail_gib": None,
            "df_avail_source": None,
            "summary": "probe snapshot contains non-numeric capacity data - capacity verdict inconclusive",
        }
    values = (usage_logs_bytes, df_total, df_avail)
    if not all(math.isfinite(value) and value >= 0 for value in values) or df_avail > df_total:
        return {
            "verdict": "unknown",
            "usage_logs_gib": None,
            "monthly_growth_gib": None,
            "months_to_volume_full": None,
            "usage_logs_pct_of_volume": None,
            "df_avail_gib": None,
            "df_avail_source": None,
            "summary": "probe snapshot contains invalid capacity data - capacity verdict inconclusive",
        }

    # --- growth, in bytes, straight from the partition catalog -----------------
    # One day partition = one day of ledger bytes, so the window's growth is the
    # sum of the partitions whose lower bound lands in it. No stats, no scan.
    #
    # usage_logs only, which is exactly the term this replaces: the old formula
    # was rows_30d(usage_logs) * avg_row(usage_logs). The other partitioned ops
    # tables are reported by the probe as evidence but deliberately NOT added
    # here. Two reasons, and both matter:
    #   * the thresholds in capacity-thresholds.json are calibrated against
    #     ledger-only growth, so widening the numerator silently re-tunes the gate;
    #   * a retention-bounded table is at steady state once it fills its window --
    #     ops_system_logs keeps 7 days, dropping partitions as fast as it creates
    #     them -- so its window bytes are THROUGHPUT, not net accumulation against
    #     free space. Scaling 7 days of throughput to 30 would invent ~9GiB/month
    #     of growth that never lands on the volume.
    # Retention-aware net growth is a real question, but it is a threshold change
    # (and the approved design puts retention out of scope), not part of making
    # the measurement survive a physical restore.
    window = stats.get("window_tables")
    span_days = stats.get("window_span_days")
    if not isinstance(window, dict) or not isinstance(span_days, (int, float)) or span_days <= 0:
        return _inconclusive(
            usage_logs_bytes, df_total, df_avail,
            "partition-bound growth accounting unavailable - capacity verdict inconclusive",
        )

    entry = window.get(GROWTH_TABLE)
    if not isinstance(entry, dict):
        return _inconclusive(
            usage_logs_bytes, df_total, df_avail,
            f"partition-bound growth missing for {GROWTH_TABLE} - capacity verdict inconclusive",
        )
    try:
        window_bytes = float(entry["window_bytes"])
        window_days = float(entry["window_days"])
    except (KeyError, TypeError, ValueError):
        return _inconclusive(
            usage_logs_bytes, df_total, df_avail,
            f"partition-bound growth invalid for {GROWTH_TABLE} - capacity verdict inconclusive",
        )
    if (
        not math.isfinite(window_bytes)
        or window_bytes < 0
        or not math.isfinite(window_days)
        or window_days < 0
        or window_days > span_days
    ):
        return _inconclusive(
            usage_logs_bytes, df_total, df_avail,
            f"partition-bound growth invalid for {GROWTH_TABLE} - capacity verdict inconclusive",
        )

    partial = ""
    if window_days <= 0:
        # Zero days of evidence is not zero growth. A ledger with no day
        # partitions in the window is a reason to abstain, not to report an
        # infinite runway -- that is the shape a broken partition maintainer has.
        return _inconclusive(
            usage_logs_bytes, df_total, df_avail,
            f"{GROWTH_TABLE} has no day partitions in the {int(span_days)}d window "
            "- capacity verdict inconclusive",
        )
    if window_days < span_days:
        # Fewer retained days than the window: scale to keep the "bytes added per
        # 30 days" meaning the thresholds were calibrated against, rather than
        # counting the unretained days as zero growth.
        partial = f", scaled from {int(window_days)}/{int(span_days)}d retained"
        monthly_growth = window_bytes * (span_days / window_days)
    else:
        monthly_growth = window_bytes

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
            "df_avail_source": None,
            "summary": "no volume (df) data from probe — capacity verdict inconclusive",
        }

    # The data volume may be a FILE on another filesystem rather than a raw
    # partition, and then df is optimistic at the inner layer: on live prod
    # (2026-10-23) /var/lib/tokenkey is a LUKS2 container backed by a sparse
    # 81GiB file on the root filesystem, so the inner df reported 53GiB free
    # while root had 28GiB. Writing past root's 28GiB fails with ENOSPC while the
    # inner df still shows space, which made the runway roughly 2x too long.
    #
    # Take the binding constraint: the smaller free space of the two layers. A
    # host on a raw partition reports no backing file and is unaffected.
    effective_avail = df_avail
    avail_source = "data volume"
    if isinstance(backing_avail, (int, float)) and backing_avail >= 0 and backing_avail < df_avail:
        effective_avail = backing_avail
        avail_source = f"backing fs {backing_mount or '?'}"

    months_to_full = (effective_avail / monthly_growth) if monthly_growth > 0 else math.inf
    pct_of_volume = usage_logs_bytes / df_total * 100.0

    # ONE decision (extract the ledger to RDS) driven by ONE signal (runway).
    #
    # usage_logs_pct_of_volume used to be OR-ed in here as an absolute guard and
    # was removed 2026-10-23 (see capacity-thresholds.json for the full note): it
    # restated runway from the stock side, could not justify an action on its own
    # -- a 40% ledger with 20 months of runway must not trigger an extraction --
    # and its denominator is the whole volume, which also holds the rest of
    # PGDATA, docker, the pgBackRest repo and QA artifacts. On live prod that made
    # it read 9.1% while 21 of the volume's 26 used GiB were the database itself.
    # It is still reported below as evidence; it just no longer decides.
    m = thresholds["months_to_volume_full"]

    if months_to_full <= m["trigger"]:
        verdict = "trigger"
    elif months_to_full <= m["approaching"]:
        verdict = "approaching"
    else:
        verdict = "green"

    payload = {
        "verdict": verdict,
        "usage_logs_gib": round(usage_logs_bytes / 1024**3, 3),
        "monthly_growth_gib": round(monthly_growth / 1024**3, 3),
        "monthly_growth_source": "partition_bytes",
        "months_to_volume_full": (None if months_to_full == math.inf else round(months_to_full, 1)),
        "usage_logs_pct_of_volume": round(pct_of_volume, 1),
        "df_avail_gib": round(effective_avail / 1024**3, 2),
        # Which layer bound the runway, and which mount it was. The raw per-layer
        # numbers stay in the probe's own DFSTATS / DFBACKING lines -- that is
        # where evidence belongs, not duplicated into the verdict payload.
        "df_avail_source": avail_source,
        "summary": (
            f"usage_logs {round(usage_logs_bytes/1024**3,2)}GiB "
            f"({round(pct_of_volume,1)}% of volume, evidence only), ledger growth "
            f"~{round(monthly_growth/1024**3,2)}GiB/30d from partition bytes{partial}, "
            f"runway {'∞' if months_to_full==math.inf else str(round(months_to_full,1))+'mo'}"
            + ("" if avail_source == "data volume" else f" (bound by {avail_source})")
            + f" -> {verdict}"
        ),
    }
    corroboration = _cross_check(stats, window, monthly_growth)
    if corroboration is not None:
        payload["cross_check"] = corroboration
    return payload


def _parse_probe_stdin(text: str) -> dict:
    """Merge the probe's tagged JSON lines into one field-addressable snapshot."""
    # Match the SHAPE of a tagged line, not a list of known tags. The probe used
    # to be paired with an enumerated whitelist here, and adding DFBACKING to the
    # probe without adding it to that list made the field arrive as None -- the
    # verdict silently kept using the optimistic inner df. Recognising the shape
    # removes the second place that has to be updated, so that cannot recur.
    stats: dict = {}
    for line in text.splitlines():
        m = re.match(r"([A-Z][A-Z0-9_]+) (\{.*\})$", line.strip())
        if not m:
            continue
        try:
            stats.update(json.loads(m.group(2)))
        except json.JSONDecodeError:
            pass
    return stats


# --- selftest fixtures: (name, stats, expected_verdict) -----------------------
#
# `W(...)` builds the PGWINDOW shape. Growth is byte-based now, so fixtures state
# growth directly instead of implying it through a row count.
def W(window_bytes, window_days=30, window_rows_est=0, span=30):
    return {
        "window_probe_ok": True,
        "window_span_days": span,
        "window_tables": {
            GROWTH_TABLE: {
                "window_bytes": window_bytes,
                "window_days": window_days,
                "window_rows_est": window_rows_est,
                "leaf_parts": 82,
                "unbounded_parts": 1,
                "total_bytes": window_bytes,
            }
        },
    }


_FIXTURES = [
    (
        "green_low_growth",
        {"usage_logs_bytes": 1.2 * 1024**3, "catalog_probe_ok": True, "growth_probe_ok": True,
         "df_total_bytes": 30 * 1024**3, "df_avail_bytes": 18 * 1024**3,
         **W(0.12 * 1024**3)},
        "green",
    ),
    (
        "approaching_runway",
        # ~5 months of runway (<=6 warn, >3 trigger)
        {"usage_logs_bytes": 4 * 1024**3, "catalog_probe_ok": True, "growth_probe_ok": True,
         "df_total_bytes": 30 * 1024**3, "df_avail_bytes": 2.5 * 1024**3,
         **W(0.5 * 1024**3)},
        "approaching",
    ),
    (
        "trigger_runway",
        # ~2 months of runway (<=3 trigger)
        {"usage_logs_bytes": 5 * 1024**3, "catalog_probe_ok": True, "growth_probe_ok": True,
         "df_total_bytes": 30 * 1024**3, "df_avail_bytes": 2 * 1024**3,
         **W(1 * 1024**3)},
        "trigger",
    ),
    (
        "trigger_bound_by_backing_filesystem",
        # Inner df looks comfortable (200GiB free) but the volume is a file on a
        # filesystem with only 8GiB left. At 4GiB/30d the inner view claims 50
        # months while the real runway is 2 -- writes fail with ENOSPC long
        # before the inner df notices. Pins the min-of-two-layers rule.
        {"usage_logs_bytes": 20 * 1024**3, "catalog_probe_ok": True, "growth_probe_ok": True,
         "df_total_bytes": 400 * 1024**3, "df_avail_bytes": 200 * 1024**3,
         "backing_avail_bytes": 8 * 1024**3, "backing_mount": "/",
         **W(4 * 1024**3)},
        "trigger",
    ),
    (
        "green_backing_roomier_than_data_volume",
        # Reverse: the backing filesystem has more room than the volume itself,
        # so the data volume stays the binding constraint and nothing changes.
        {"usage_logs_bytes": 20 * 1024**3, "catalog_probe_ok": True, "growth_probe_ok": True,
         "df_total_bytes": 400 * 1024**3, "df_avail_bytes": 200 * 1024**3,
         "backing_avail_bytes": 900 * 1024**3, "backing_mount": "/",
         **W(4 * 1024**3)},
        "green",
    ),
    (
        "green_raw_partition_has_no_backing_layer",
        # Raw-partition host (and any pre-DFBACKING probe revision): the field is
        # absent, which must read as "no outer layer", never as zero free space.
        {"usage_logs_bytes": 20 * 1024**3, "catalog_probe_ok": True, "growth_probe_ok": True,
         "df_total_bytes": 400 * 1024**3, "df_avail_bytes": 200 * 1024**3,
         **W(4 * 1024**3)},
        "green",
    ),
    (
        "green_large_share_but_ample_runway",
        # The removed absolute-pct guard would have called this 'trigger' purely
        # because the ledger is 43% of the volume. With no growth the runway is
        # infinite, so extracting to RDS would be the wrong action -- share alone
        # never justifies it. Pins the 2026-10-23 removal.
        {"usage_logs_bytes": 13 * 1024**3, "catalog_probe_ok": True, "growth_probe_ok": True,
         "df_total_bytes": 30 * 1024**3, "df_avail_bytes": 15 * 1024**3,
         **W(0)},
        "green",
    ),
    (
        "trigger_runway_regardless_of_small_share",
        # Mirror image: a ledger that is only 4% of a large volume still triggers
        # when growth eats the free space within the trigger window. Runway is the
        # signal; share is evidence.
        {"usage_logs_bytes": 40 * 1024**3, "catalog_probe_ok": True, "growth_probe_ok": True,
         "df_total_bytes": 1000 * 1024**3, "df_avail_bytes": 20 * 1024**3,
         **W(10 * 1024**3)},
        "trigger",
    ),
    (
        "green_zero_growth_small",
        # brand-new / low-traffic edge: no growth, tiny ledger -> infinite runway
        {"usage_logs_bytes": 50 * 1024**2, "catalog_probe_ok": True, "growth_probe_ok": True,
         "df_total_bytes": 30 * 1024**3, "df_avail_bytes": 25 * 1024**3,
         **W(0)},
        "green",
    ),
    (
        "unknown_missing_df",
        # df unreadable + nonzero growth: must NOT false-trigger
        {"usage_logs_bytes": 5 * 1024**3, "catalog_probe_ok": True, "growth_probe_ok": True,
         "df_total_bytes": 0, "df_avail_bytes": 0,
         **W(1 * 1024**3)},
        "unknown",
    ),
    (
        "trigger_disk_genuinely_full",
        # df_total>0 with df_avail==0 is a genuinely full disk => trigger is right
        {"usage_logs_bytes": 5 * 1024**3, "catalog_probe_ok": True, "growth_probe_ok": True,
         "df_total_bytes": 30 * 1024**3, "df_avail_bytes": 0,
         **W(1 * 1024**3)},
        "trigger",
    ),
    (
        "restored_host_stats_are_zero_but_verdict_still_lands",
        # THE REGRESSION THIS DESIGN EXISTS FOR, with live-prod numbers. Shape of a
        # pgBackRest-restored host: n_live_tup near zero (the stats file starts
        # fresh), 1 of 82 leaf partitions ever analyzed, and the 30-day row scan
        # timing out at statement_timeout=2s. The old formula read 24.7k rows
        # against a 7.1GiB table, inflated avg row size ~390x and reported a
        # 0.0-month "trigger". Byte accounting consults none of that.
        {"usage_logs_bytes": 7657177088, "usage_logs_rows": 24685,
         "usage_logs_reltuples": 7717412,
         "usage_logs_leaf_partitions": 82, "usage_logs_analyzed_partitions": 1,
         "catalog_probe_ok": True,
         "growth_probe_ok": False, "usage_logs_rows_30d": None, "usage_logs_rows_7d": None,
         "df_total_bytes": 83938926592, "df_avail_bytes": 57783738368,
         **W(5130936320, window_rows_est=5030000)},
        "green",
    ),
    (
        "restored_host_without_any_analyze_still_lands",
        # Nothing analyzed at all: reltuples clamps to 0, so the cross-check drops
        # out. The verdict must not depend on having corroboration.
        {"usage_logs_bytes": 7657177088, "usage_logs_rows": 0, "usage_logs_reltuples": 0,
         "usage_logs_leaf_partitions": 82, "usage_logs_analyzed_partitions": 0,
         "catalog_probe_ok": True, "growth_probe_ok": False,
         "df_total_bytes": 83938926592, "df_avail_bytes": 57783738368,
         **W(5130936320)},
        "green",
    ),
    (
        "partial_retention_is_scaled_not_treated_as_zero",
        # 7 of 30 days retained: 1GiB/7d -> ~4.3GiB/30d, so ~2.3mo of runway.
        # Counting the unretained days as zero growth would have said 10mo.
        {"usage_logs_bytes": 5 * 1024**3, "catalog_probe_ok": True, "growth_probe_ok": True,
         "df_total_bytes": 30 * 1024**3, "df_avail_bytes": 10 * 1024**3,
         **W(1 * 1024**3, window_days=7)},
        "trigger",
    ),
    (
        "unknown_no_day_partitions_in_window",
        # No day partitions in the window is a broken partition maintainer, not an
        # infinite runway.
        {"usage_logs_bytes": 5 * 1024**3, "catalog_probe_ok": True, "growth_probe_ok": True,
         "df_total_bytes": 30 * 1024**3, "df_avail_bytes": 10 * 1024**3,
         **W(0, window_days=0)},
        "unknown",
    ),
    (
        "unknown_window_signal_missing_never_falls_back_to_stats",
        # Byte accounting unavailable => abstain. The stats path is present and
        # tempting here; using it is the fiction this replaced.
        {"usage_logs_bytes": 5 * 1024**3, "usage_logs_rows": 24685,
         "usage_logs_reltuples": 7717412, "usage_logs_rows_30d": 5_000_000,
         "catalog_probe_ok": True, "growth_probe_ok": True,
         "df_total_bytes": 30 * 1024**3, "df_avail_bytes": 10 * 1024**3,
         "window_probe_ok": False, "window_span_days": None, "window_tables": None},
        "unknown",
    ),
    (
        "unknown_window_days_exceed_span",
        # More retained days than the window spans is incoherent input.
        {"usage_logs_bytes": 5 * 1024**3, "catalog_probe_ok": True, "growth_probe_ok": True,
         "df_total_bytes": 30 * 1024**3, "df_avail_bytes": 10 * 1024**3,
         **W(1 * 1024**3, window_days=45)},
        "unknown",
    ),
    (
        "unknown_catalog_probe_failed",
        {"catalog_probe_ok": False, "growth_probe_ok": True,
         "df_total_bytes": 30 * 1024**3, "df_avail_bytes": 10 * 1024**3,
         **W(1 * 1024**3)},
        "unknown",
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
