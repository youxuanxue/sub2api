#!/usr/bin/env python3
"""Merge client-release + prompt-surface watch outputs into one daily fidelity report."""
from __future__ import annotations

import argparse
import json
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
        "signal_type": "release-scan-failure",
        "label": "client-release-watch",
        "skill": "(inspect client release watch run)",
        "next_command": "python3 scripts/fingerprint/client_release_watch.py --selftest",
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


def load_links(path: Path | None) -> list[dict[str, Any]]:
    data = load_json(path)
    if not data:
        return []
    links = data.get("links")
    return links if isinstance(links, list) else []


def find_link(
    links: list[dict[str, Any]],
    *,
    signal_type: str,
    platform_id: str | None = None,
    kind: str | None = None,
) -> dict[str, Any] | None:
    for link in links:
        if link.get("signal_type") != signal_type:
            continue
        if platform_id is not None and link.get("platform_id") != platform_id:
            continue
        if kind is not None and link.get("kind") != kind:
            continue
        return link
    return None


def collect_signals(
    *,
    release_report: dict[str, Any] | None,
    prompt_prod_report: dict[str, Any] | None,
    tracking_links: list[dict[str, Any]],
    release_scan_result: str,
    registry_gate_result: str,
    prod_aggregate_result: str,
) -> list[dict[str, Any]]:
    signals: list[dict[str, Any]] = []

    if release_scan_result == "failure":
        sig = {
            "signal_type": "release-scan-failure",
            "status": "actionable",
            "detail": "release-scan job failed before producing a usable client release report",
        }
        link = find_link(tracking_links, signal_type="release-scan-failure", kind="issue")
        if link:
            sig["tracking"] = link
        signals.append(sig)

    if registry_gate_result == "failure":
        sig = {
            "signal_type": "registry-failure",
            "status": "actionable",
            "detail": "registry-gate job failed (registry, fixture gateway, or unit tests)",
        }
        link = find_link(tracking_links, signal_type="registry-failure", kind="issue")
        if link:
            sig["tracking"] = link
        signals.append(sig)

    if release_report:
        for item in release_report.get("platforms") or []:
            if item.get("drift") and not item.get("issue_suppressed"):
                platform_id = item.get("id")
                sig = {
                    "signal_type": "release-drift",
                    "status": "actionable",
                    "platform_id": platform_id,
                    "name": item.get("name"),
                    "pinned": item.get("pinned"),
                    "upstream_latest": item.get("upstream_latest"),
                    "skill": item.get("skill"),
                }
                link = find_link(tracking_links, signal_type="release-drift", platform_id=platform_id, kind="issue")
                if link:
                    sig["tracking"] = link
                signals.append(sig)

    if prompt_prod_report:
        summary = prompt_prod_report.get("summary") or {}
        if summary.get("has_actionable_drift"):
            sig = {
                "signal_type": "prod-drift",
                "status": "actionable",
                "alerts": summary.get("alerts") or [],
                "rows": summary.get("count", 0),
            }
            link = find_link(tracking_links, signal_type="prod-drift", kind="issue")
            if link:
                sig["tracking"] = link
            signals.append(sig)
    elif prod_aggregate_result == "failure" and registry_gate_result == "success":
        sig = {
            "signal_type": "prod-drift",
            "status": "actionable",
            "detail": "prod-aggregate job failed (probe/aggregate error or actionable drift)",
        }
        link = find_link(tracking_links, signal_type="prod-drift", kind="issue")
        if link:
            sig["tracking"] = link
        signals.append(sig)

    if (
        not signals
        and release_scan_result == "success"
        and registry_gate_result == "success"
        and prod_aggregate_result in {"success", "skipped"}
    ):
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
        tracking = sig.get("tracking") or {}
        tracking_text = f" — tracking: {tracking.get('url')}" if tracking.get("url") else ""
        if st == "release-drift":
            lines.append(
                f"- **release-drift** `{sig.get('platform_id')}`: pin `{sig.get('pinned')}` < upstream `{sig.get('upstream_latest')}` → skill `{sig.get('skill')}`{tracking_text}"
            )
        elif st == "release-scan-failure":
            lines.append(f"- **release-scan-failure**: {sig.get('detail')}{tracking_text}")
        elif st == "registry-failure":
            lines.append(f"- **registry-failure**: {sig.get('detail')}{tracking_text}")
        elif st == "prod-drift":
            alerts = sig.get("alerts") or []
            if alerts:
                lines.append(f"- **prod-drift**: {', '.join(alerts)}{tracking_text}")
            else:
                lines.append(f"- **prod-drift**: {sig.get('detail', 'actionable drift')}{tracking_text}")
        elif st == "aligned":
            lines.append("- **aligned**: no actionable signals")
    tracking_links = payload.get("tracking_links") or []
    cache_pr_url = payload.get("cache_pr_url") or ""
    if tracking_links or cache_pr_url:
        lines.extend(["", "## Tracking links", ""])
        lines.append("| Type | Target | Status | Link |")
        lines.append("|---|---|---|---|")
        for link in tracking_links:
            target = link.get("platform_id") or link.get("signal_type") or link.get("title") or "n/a"
            number = f"#{link.get('number')}" if link.get("number") else link.get("title", "n/a")
            url = link.get("url") or "n/a"
            lines.append(f"| `{link.get('kind', 'link')}` | `{target}` | `{link.get('status', 'n/a')}` | [{number}]({url}) |")
        if cache_pr_url:
            lines.append(f"| `pr` | `client-release-watch-cache` | `open/update` | [cache PR]({cache_pr_url}) |")
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
    tracking_links: list[dict[str, Any]],
    cache_pr_url: str,
    release_scan_result: str,
    registry_gate_result: str,
    prod_aggregate_result: str,
) -> dict[str, Any]:
    from datetime import datetime, timezone

    signals = collect_signals(
        release_report=release_report,
        prompt_prod_report=prompt_prod_report,
        tracking_links=tracking_links,
        release_scan_result=release_scan_result,
        registry_gate_result=registry_gate_result,
        prod_aggregate_result=prod_aggregate_result,
    )
    workflow_should_fail = any(
        sig.get("signal_type") in {"release-scan-failure", "registry-failure", "prod-drift"}
        and sig.get("status") == "actionable"
        for sig in signals
    ) or release_scan_result == "failure" or registry_gate_result == "failure" or prod_aggregate_result == "failure"

    return {
        "schema_version": 1,
        "generated_at": datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z"),
        "run_url": run_url,
        "workflow_should_fail": workflow_should_fail,
        "job_results": {
            "release-scan": release_scan_result,
            "registry-gate": registry_gate_result,
            "prod-aggregate": prod_aggregate_result,
        },
        "signals": signals,
        "tracking_links": tracking_links,
        "cache_pr_url": cache_pr_url,
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
    ap.add_argument("--release-links-json", type=Path)
    ap.add_argument("--prompt-links-json", type=Path)
    ap.add_argument("--cache-pr-url", default="")
    ap.add_argument("--release-scan-result", default="success")
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
            tracking_links=[
                {
                    "kind": "issue",
                    "signal_type": "release-drift",
                    "platform_id": "claude-code",
                    "number": 1,
                    "url": "https://example/issues/1",
                    "status": "updated",
                },
                {
                    "kind": "issue",
                    "signal_type": "prod-drift",
                    "number": 2,
                    "url": "https://example/issues/2",
                    "status": "updated",
                },
            ],
            cache_pr_url="https://example/pull/3",
            release_scan_result="success",
            registry_gate_result="success",
            prod_aggregate_result="failure",
        )
        assert payload["workflow_should_fail"] is True
        assert any(s["signal_type"] == "release-drift" for s in payload["signals"])
        assert any(s["signal_type"] == "prod-drift" for s in payload["signals"])
        assert payload["signals"][0]["tracking"]["url"] == "https://example/issues/1"
        assert payload["cache_pr_url"] == "https://example/pull/3"
        md = render_markdown(payload)
        assert "release-drift" in md and "prod-drift" in md and "Tracking links" in md
        failed_release = build_payload(
            run_url="https://example/run/2",
            release_report=None,
            release_markdown=None,
            prompt_prod_report=None,
            prompt_prod_markdown=None,
            tracking_links=[],
            cache_pr_url="",
            release_scan_result="failure",
            registry_gate_result="skipped",
            prod_aggregate_result="skipped",
        )
        assert failed_release["workflow_should_fail"] is True
        assert failed_release["job_results"]["release-scan"] == "failure"
        assert any(s["signal_type"] == "release-scan-failure" for s in failed_release["signals"])
        print("client-fidelity-watch report selftest ok")
        return 0

    release_report = load_json(args.release_report_json)
    prompt_prod_report = load_json(args.prompt_prod_report_json)
    tracking_links = [
        *load_links(args.release_links_json),
        *load_links(args.prompt_links_json),
    ]
    release_md = args.release_report_md.read_text(encoding="utf-8") if args.release_report_md and args.release_report_md.is_file() else None
    prompt_md = args.prompt_prod_report_md.read_text(encoding="utf-8") if args.prompt_prod_report_md and args.prompt_prod_report_md.is_file() else None

    payload = build_payload(
        run_url=args.run_url,
        release_report=release_report,
        release_markdown=release_md,
        prompt_prod_report=prompt_prod_report,
        prompt_prod_markdown=prompt_md,
        tracking_links=tracking_links,
        cache_pr_url=args.cache_pr_url,
        release_scan_result=args.release_scan_result,
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
