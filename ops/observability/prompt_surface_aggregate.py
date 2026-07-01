#!/usr/bin/env python3
"""Aggregate prod prompt-surface fingerprint logs vs registry baselines."""
from __future__ import annotations

import argparse
import json
import sys
from collections import Counter
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
REGISTRY = REPO_ROOT / "ops" / "anthropic" / "prompt_surface_registry.json"


def load_registry() -> dict:
    return json.loads(REGISTRY.read_text(encoding="utf-8"))


def canonical_geo_classes(registry: dict) -> set[str]:
    for surf in registry.get("surfaces") or []:
        if surf.get("id") == "geo_stego_date_line":
            base = set(surf.get("prod_canonical_values") or [])
            base.add("NONE")
            return base
    return {"NONE", "ISO_DASH_ASCII"}


def aggregate(payload: dict, registry: dict) -> dict:
    fps = payload.get("fingerprints") or []
    sig_counter: Counter[str] = Counter()
    date_class: Counter[str] = Counter()
    identity: Counter[str] = Counter()
    unknown_surfaces: Counter[str] = Counter()
    noncanonical_geo = 0
    cc_environment = 0
    canonical_geo = canonical_geo_classes(registry)

    for row in fps:
        sig = str(row.get("surface_signature") or "")
        if sig:
            sig_counter[sig] += 1
        cls = str(row.get("reminder_date_line_class") or "NONE")
        date_class[cls] += 1
        if cls not in canonical_geo:
            noncanonical_geo += 1
        identity[str(row.get("identity_anchor_id") or "absent")] += 1
        raw_unknown = str(row.get("unknown_surfaces") or "").strip()
        if raw_unknown:
            for item in raw_unknown.split(","):
                item = item.strip()
                if item:
                    unknown_surfaces[item] += 1
                    if item == "cc_environment_section":
                        cc_environment += 1

    alerts: list[str] = []
    agg_cfg = registry.get("aggregate") or {}
    if agg_cfg.get("noncanonical_geo_alert") and noncanonical_geo:
        alerts.append(f"noncanonical_geo_count={noncanonical_geo}")
    if agg_cfg.get("cc_environment_alert") and cc_environment:
        alerts.append(f"cc_environment_leak_count={cc_environment}")
    if agg_cfg.get("unknown_surface_alert") and unknown_surfaces:
        alerts.append(f"unknown_surfaces={dict(unknown_surfaces)}")

    return {
        "count": len(fps),
        "surface_signatures": dict(sig_counter.most_common(20)),
        "reminder_date_line_class": dict(date_class),
        "identity_anchor_id": dict(identity),
        "unknown_surfaces": dict(unknown_surfaces),
        "noncanonical_geo_count": noncanonical_geo,
        "cc_environment_leak_count": cc_environment,
        "alerts": alerts,
        "has_actionable_drift": bool(alerts),
    }


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--input", type=Path, help="JSON from probe-prompt-surface-fingerprints.sh")
    ap.add_argument("--report-json", type=Path)
    ap.add_argument("--report-md", type=Path)
    args = ap.parse_args()

    if args.input:
        raw = args.input.read_text(encoding="utf-8")
        if "--output truncated--" in raw:
            print(
                "error: probe output truncated (SSM ~24KB limit); lower LIMIT in workflow",
                file=sys.stderr,
            )
            return 1
        payload = json.loads(raw)
    else:
        payload = json.load(sys.stdin)

    registry = load_registry()
    report = {
        "meta": payload.get("meta") or {},
        "summary": aggregate(payload, registry),
    }

    if args.report_json:
        args.report_json.parent.mkdir(parents=True, exist_ok=True)
        args.report_json.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")

    md_lines = [
        "# Prompt surface fingerprint aggregate",
        "",
        f"- rows: {report['summary']['count']}",
        f"- actionable_drift: {report['summary']['has_actionable_drift']}",
    ]
    for alert in report["summary"].get("alerts") or []:
        md_lines.append(f"- alert: {alert}")
    md_lines.append("")
    md_lines.append("## reminder_date_line_class")
    for key, val in sorted((report["summary"].get("reminder_date_line_class") or {}).items()):
        md_lines.append(f"- {key}: {val}")
    md_text = "\n".join(md_lines) + "\n"

    if args.report_md:
        args.report_md.parent.mkdir(parents=True, exist_ok=True)
        args.report_md.write_text(md_text, encoding="utf-8")
    else:
        print(md_text, end="")

    return 1 if report["summary"]["has_actionable_drift"] else 0


if __name__ == "__main__":
    raise SystemExit(main())
