#!/usr/bin/env python3
"""Aggregate per-edge OAuth mimicry probe JSONL vs ratio thresholds."""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any

MIN_SDK_INGRESS = 5
BILLING_PREFIX_RATE_MIN = 0.8
CANONICAL_UA_RATE_MIN = 0.8


def load_jsonl(path: Path) -> list[dict[str, Any]]:
    rows: list[dict[str, Any]] = []
    for line in path.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if not line:
            continue
        rows.append(json.loads(line))
    return rows


def edge_alerts(probe: dict[str, Any]) -> list[str]:
    alerts: list[str] = []
    edge = probe.get("edge_id") or "unknown"
    if probe.get("probe_error"):
        if probe.get("probe_error") != "no_schedulable_anthropic_oauth_edges":
            alerts.append(f"probe_error={probe['probe_error']}")
        return alerts

    usage = probe.get("usage_logs_ingress") or {}
    if usage.get("error"):
        alerts.append(f"usage_logs_error={usage['error']}")
        return alerts

    egress = probe.get("egress_oauth_mimic") or {}
    prompt_fp = probe.get("egress_prompt_fingerprint") or {}
    sdk_count = int(usage.get("oauth_openai_python_count") or 0)
    egress_count = int(egress.get("count") or 0)
    prompt_count = int(prompt_fp.get("count") or 0)

    if sdk_count >= MIN_SDK_INGRESS and egress_count == 0 and prompt_count == 0:
        alerts.append("ingress_sdk_no_egress_fingerprint_logs")
    if sdk_count >= MIN_SDK_INGRESS and egress_count > 0:
        billing_rate = float(egress.get("billing_prefix_rate") or 0)
        if billing_rate < BILLING_PREFIX_RATE_MIN:
            alerts.append(f"billing_prefix_rate_low={billing_rate}")
        canon = int(egress.get("canonical_cli_egress_ua") or 0)
        canon_rate = canon / egress_count if egress_count else 0.0
        if canon_rate < CANONICAL_UA_RATE_MIN:
            alerts.append(f"canonical_ua_rate_low={round(canon_rate, 3)}")

    verdict = (probe.get("verdict") or {}).get("code")
    if (
        sdk_count >= MIN_SDK_INGRESS
        and verdict == "ingress_sdk_seen_no_egress_fingerprint_logs"
        and "ingress_sdk_no_egress_fingerprint_logs" not in alerts
    ):
        alerts.append("ingress_sdk_no_egress_fingerprint_logs")

    if alerts:
        return [f"{edge}:{item}" for item in alerts]
    return []


def aggregate(probes: list[dict[str, Any]]) -> dict[str, Any]:
    per_edge: list[dict[str, Any]] = []
    fleet_alerts: list[str] = []
    eligible_edges: list[str] = []

    for probe in probes:
        edge = str(probe.get("edge_id") or "unknown")
        if edge == "_fleet" and probe.get("probe_error") == "no_schedulable_anthropic_oauth_edges":
            return {
                "eligible_edge_count": 0,
                "scanned_edge_count": 0,
                "per_edge": [],
                "alerts": [],
                "has_actionable_drift": False,
                "skip_reason": "no_schedulable_anthropic_oauth_edges",
            }
        if probe.get("schedulable_anthropic_oauth_edge"):
            eligible_edges.append(edge)
        usage = probe.get("usage_logs_ingress") or {}
        egress = probe.get("egress_oauth_mimic") or {}
        edge_alerts_list = edge_alerts(probe)
        fleet_alerts.extend(edge_alerts_list)
        per_edge.append(
            {
                "edge_id": edge,
                "verdict": (probe.get("verdict") or {}).get("code"),
                "oauth_openai_python_count": usage.get("oauth_openai_python_count", 0),
                "egress_oauth_mimic_count": egress.get("count", 0),
                "billing_prefix_rate": egress.get("billing_prefix_rate"),
                "canonical_cli_egress_ua": egress.get("canonical_cli_egress_ua"),
                "alerts": edge_alerts_list,
                "probe_error": probe.get("probe_error"),
            }
        )

    actionable = [a for a in fleet_alerts if not a.endswith(":probe_error=no_schedulable_anthropic_oauth_edges")]
    return {
        "eligible_edge_count": len(eligible_edges),
        "scanned_edge_count": len(per_edge),
        "eligible_edges": eligible_edges,
        "per_edge": per_edge,
        "alerts": actionable,
        "has_actionable_drift": bool(actionable),
    }


def render_markdown(report: dict[str, Any]) -> str:
    summary = report.get("summary") or {}
    meta = report.get("meta") or {}
    lines = [
        "# OAuth mimic edge aggregate",
        "",
        f"- Since: `{meta.get('since', 'n/a')}`",
        f"- Window minutes: `{meta.get('window_minutes', 'n/a')}`",
        f"- Eligible edges: `{summary.get('eligible_edge_count', 0)}`",
        f"- Scanned edges: `{summary.get('scanned_edge_count', 0)}`",
        f"- Actionable drift: `{summary.get('has_actionable_drift', False)}`",
        "",
    ]
    if summary.get("skip_reason"):
        lines.append(f"_Skipped: {summary['skip_reason']}_")
        lines.append("")
    alerts = summary.get("alerts") or []
    if alerts:
        lines.extend(["## Alerts", ""])
        lines.extend(f"- {item}" for item in alerts)
        lines.append("")
    lines.extend(["## Per edge", ""])
    lines.append("| Edge | SDK ingress | Egress mimic logs | Billing rate | Verdict |")
    lines.append("|---|---:|---:|---:|---|")
    for row in summary.get("per_edge") or []:
        lines.append(
            f"| `{row.get('edge_id')}` | {row.get('oauth_openai_python_count', 0)} "
            f"| {row.get('egress_oauth_mimic_count', 0)} "
            f"| {row.get('billing_prefix_rate', 'n/a')} "
            f"| `{row.get('verdict') or row.get('probe_error') or 'n/a'}` |"
        )
    return "\n".join(lines) + "\n"


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--input", type=Path, help="JSONL from scan-oauth-mimic-chain.sh")
    ap.add_argument("--report-json", type=Path)
    ap.add_argument("--report-md", type=Path)
    ap.add_argument("--since", default="24h")
    ap.add_argument("--window-minutes", default="1440")
    ap.add_argument("--selftest", action="store_true")
    args = ap.parse_args(argv)

    if args.selftest:
        fixtures = [
            {
                "edge_id": "us3",
                "schedulable_anthropic_oauth_edge": True,
                "usage_logs_ingress": {"oauth_openai_python_count": 40},
                "egress_oauth_mimic": {
                    "count": 10,
                    "billing_prefix_rate": 1.0,
                    "canonical_cli_egress_ua": 10,
                },
                "egress_prompt_fingerprint": {"count": 0},
                "verdict": {"code": "mimicry_chain_complete"},
            },
            {
                "edge_id": "uk",
                "schedulable_anthropic_oauth_edge": True,
                "usage_logs_ingress": {"oauth_openai_python_count": 20},
                "egress_oauth_mimic": {"count": 0},
                "egress_prompt_fingerprint": {"count": 0},
                "verdict": {"code": "ingress_sdk_seen_no_egress_fingerprint_logs"},
            },
        ]
        summary = aggregate(fixtures)
        assert summary["has_actionable_drift"] is True
        assert any("ingress_sdk_no_egress" in a for a in summary["alerts"])
        clear = aggregate([fixtures[0]])
        assert clear["has_actionable_drift"] is False
        print("oauth_mimic_aggregate selftest ok")
        return 0

    if not args.input or not args.input.is_file():
        print("oauth_mimic_aggregate: missing --input", file=sys.stderr)
        return 2

    probes = load_jsonl(args.input)
    summary = aggregate(probes)
    report = {
        "meta": {"since": args.since, "window_minutes": args.window_minutes},
        "summary": summary,
    }
    if args.report_json:
        args.report_json.parent.mkdir(parents=True, exist_ok=True)
        args.report_json.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    if args.report_md:
        args.report_md.parent.mkdir(parents=True, exist_ok=True)
        args.report_md.write_text(render_markdown(report), encoding="utf-8")
    return 1 if summary.get("has_actionable_drift") else 0


if __name__ == "__main__":
    raise SystemExit(main())
