#!/usr/bin/env python3
"""Merge client-release + prompt-surface watch outputs into one daily fidelity report."""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any

REPO_ROOT = Path(__file__).resolve().parents[2]
DEFAULT_OUT_DIR = REPO_ROOT / ".cache/fingerprint/client-fidelity-watch"

ROUTING_TABLE = [
    {
        "signal_type": "release-drift",
        "label": "client-release:drift",
        "skill": "tokenkey-fingerprint-alignment-all or per-platform skill",
        "next_command": "bash ops/fingerprint/client-release-watch.sh plan",
    },
    {
        "signal_type": "registry-failure",
        "label": "prompt-surface:registry-failure",
        "skill": "(inline fix — registry + fixture gateway tests)",
        "next_command": "python3 ops/anthropic/probe_prompt_surfaces.py --check-registry",
    },
    {
        "signal_type": "prod-drift",
        "label": "prompt-surface:prod-drift",
        "skill": "(compare prod fingerprints vs prompt_surface_registry.json)",
        "next_command": "bash ops/observability/run-probe.sh --target prod --script ops/observability/probe-prompt-surface-fingerprints.sh --env SINCE=24h --env LIMIT=40",
    },
]


def load_json(path: Path | None) -> dict[str, Any] | None:
    if path is None or not path.is_file():
        return None
    return json.loads(path.read_text(encoding="utf-8"))


def collect_signals(
    *,
    release_report: dict[str, Any] | None,
    prompt_prod_report: dict[str, Any] | None,
    registry_gate_result: str,
    prod_aggregate_result: str,
) -> list[dict[str, Any]]:
    signals: list[dict[str, Any]] = []

    if registry_gate_result == "failure":
        signals.append({
            "signal_type": "registry-failure",
            "status": "actionable",
            "detail": "registry-gate job failed (registry, fixture gateway, or unit tests)",
        })

    if release_report:
        for item in release_report.get("platforms") or []:
            if item.get("drift") and not item.get("issue_suppressed"):
                signals.append({
                    "signal_type": "release-drift",
                    "status": "actionable",
                    "platform_id": item.get("id"),
                    "name": item.get("name"),
                    "pinned": item.get("pinned"),
                    "upstream_latest": item.get("upstream_latest"),
                    "skill": item.get("skill"),
                })

    if prompt_prod_report:
        summary = prompt_prod_report.get("summary") or {}
        if summary.get("has_actionable_drift"):
            signals.append({
                "signal_type": "prod-drift",
                "status": "actionable",
                "alerts": summary.get("alerts") or [],
                "rows": summary.get("count", 0),
            })
    elif prod_aggregate_result == "failure" and registry_gate_result == "success":
        signals.append({
            "signal_type": "prod-drift",
            "status": "actionable",
            "detail": "prod-aggregate job failed (probe/aggregate error or actionable drift)",
        })

    if not signals and registry_gate_result == "success" and prod_aggregate_result in {"success", "skipped"}:
        signals.append({"signal_type": "aligned", "status": "clear", "detail": "no actionable fidelity signals"})

    return signals


def render_markdown(payload: dict[str, Any]) -> str:
    lines = [
        "# Client fidelity watch — daily report",
        "",
        f"- Generated: `{payload.get('generated_at')}`",
        f"- Run: {payload.get('run_url') or 'n/a'}",
        f"- Workflow blocking: `{payload.get('workflow_should_fail')}`",
        "",
        "## Job results",
        "",
    ]
    for name, result in (payload.get("job_results") or {}).items():
        lines.append(f"- `{name}`: **{result}**")
    lines.extend(["", "## Signals", ""])
    for sig in payload.get("signals") or []:
        st = sig.get("signal_type")
        if st == "release-drift":
            lines.append(
                f"- **release-drift** `{sig.get('platform_id')}`: pin `{sig.get('pinned')}` < upstream `{sig.get('upstream_latest')}` → skill `{sig.get('skill')}`"
            )
        elif st == "registry-failure":
            lines.append(f"- **registry-failure**: {sig.get('detail')}")
        elif st == "prod-drift":
            alerts = sig.get("alerts") or []
            if alerts:
                lines.append(f"- **prod-drift**: {', '.join(alerts)}")
            else:
                lines.append(f"- **prod-drift**: {sig.get('detail', 'actionable drift')}")
        elif st == "aligned":
            lines.append("- **aligned**: no actionable signals")
    lines.extend(["", "## Issue routing (by signal type)", ""])
    lines.append("| Signal type | GitHub label | Next command |")
    lines.append("|---|---|---|")
    for row in ROUTING_TABLE:
        lines.append(f"| `{row['signal_type']}` | `{row['label']}` | `{row['next_command']}` |")
    lines.append("")
    release_md = payload.get("sections", {}).get("release_markdown")
    if release_md:
        lines.extend(["## Client release watch (detail)", "", release_md.strip(), ""])
    prompt_md = payload.get("sections", {}).get("prompt_prod_markdown")
    if prompt_md:
        lines.extend(["## Prompt surface prod aggregate (detail)", "", prompt_md.strip(), ""])
    return "\n".join(lines) + "\n"


def build_payload(
    *,
    run_url: str,
    release_report: dict[str, Any] | None,
    release_markdown: str | None,
    prompt_prod_report: dict[str, Any] | None,
    prompt_prod_markdown: str | None,
    registry_gate_result: str,
    prod_aggregate_result: str,
) -> dict[str, Any]:
    from datetime import datetime, timezone

    signals = collect_signals(
        release_report=release_report,
        prompt_prod_report=prompt_prod_report,
        registry_gate_result=registry_gate_result,
        prod_aggregate_result=prod_aggregate_result,
    )
    workflow_should_fail = any(
        sig.get("signal_type") in {"registry-failure", "prod-drift"} and sig.get("status") == "actionable"
        for sig in signals
    ) or registry_gate_result == "failure" or prod_aggregate_result == "failure"

    return {
        "schema_version": 1,
        "generated_at": datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z"),
        "run_url": run_url,
        "workflow_should_fail": workflow_should_fail,
        "job_results": {
            "release-scan": "success",
            "registry-gate": registry_gate_result,
            "prod-aggregate": prod_aggregate_result,
        },
        "signals": signals,
        "routing_table": ROUTING_TABLE,
        "sections": {
            "release_markdown": release_markdown,
            "prompt_prod_markdown": prompt_prod_markdown,
        },
        "release_summary": (release_report or {}).get("summary"),
        "prompt_prod_summary": (prompt_prod_report or {}).get("summary") if prompt_prod_report else None,
    }


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--release-report-json", type=Path)
    ap.add_argument("--release-report-md", type=Path)
    ap.add_argument("--prompt-prod-report-json", type=Path)
    ap.add_argument("--prompt-prod-report-md", type=Path)
    ap.add_argument("--registry-gate-result", default="unknown")
    ap.add_argument("--prod-aggregate-result", default="unknown")
    ap.add_argument("--run-url", default="")
    ap.add_argument("--report-json", type=Path, default=DEFAULT_OUT_DIR / "report.json")
    ap.add_argument("--report-md", type=Path, default=DEFAULT_OUT_DIR / "report.md")
    ap.add_argument("--selftest", action="store_true")
    args = ap.parse_args(argv)

    if args.selftest:
        payload = build_payload(
            run_url="https://example/run/1",
            release_report={"platforms": [{"id": "claude-code", "drift": True, "issue_suppressed": False, "name": "CC", "pinned": "1", "upstream_latest": "2", "skill": "tokenkey-cc-fingerprint-alignment"}], "summary": {}},
            release_markdown=None,
            prompt_prod_report={"summary": {"has_actionable_drift": True, "alerts": ["x=1"], "count": 1}},
            prompt_prod_markdown="- alert",
            registry_gate_result="success",
            prod_aggregate_result="failure",
        )
        assert payload["workflow_should_fail"] is True
        assert any(s["signal_type"] == "release-drift" for s in payload["signals"])
        assert any(s["signal_type"] == "prod-drift" for s in payload["signals"])
        md = render_markdown(payload)
        assert "release-drift" in md and "prod-drift" in md
        print("client-fidelity-watch report selftest ok")
        return 0

    release_report = load_json(args.release_report_json)
    prompt_prod_report = load_json(args.prompt_prod_report_json)
    release_md = args.release_report_md.read_text(encoding="utf-8") if args.release_report_md and args.release_report_md.is_file() else None
    prompt_md = args.prompt_prod_report_md.read_text(encoding="utf-8") if args.prompt_prod_report_md and args.prompt_prod_report_md.is_file() else None

    payload = build_payload(
        run_url=args.run_url,
        release_report=release_report,
        release_markdown=release_md,
        prompt_prod_report=prompt_prod_report,
        prompt_prod_markdown=prompt_md,
        registry_gate_result=args.registry_gate_result,
        prod_aggregate_result=args.prod_aggregate_result,
    )
    args.report_json.parent.mkdir(parents=True, exist_ok=True)
    args.report_json.write_text(json.dumps(payload, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    args.report_md.write_text(render_markdown(payload), encoding="utf-8")
    print(render_markdown(payload))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
