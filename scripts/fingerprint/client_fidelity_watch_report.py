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
    {
        "signal_type": "oauth-mimic-drift",
        "label": "oauth-mimic:edge-drift",
        "skill": "tokenkey-cc-fingerprint-alignment",
        "next_command": "bash ops/observability/scan-oauth-mimic-chain.sh --since 24h",
    },
    {
        "signal_type": "kiro-production-config-drift",
        "label": "client-fidelity-watch",
        "skill": "tokenkey-kiro-fingerprint-alignment",
        "next_command": "python3 ops/kiro/check_kiro_tls_profile_parity.py --snapshot <read-only-db-snapshot>",
    },
    {
        "signal_type": "observer-failure",
        "label": "client-fidelity-watch",
        "skill": "(inspect the named observer and its artifact)",
        "next_command": "gh run view <run-id> --log-failed",
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


def _normalize_observation_status(status: str) -> str:
    normalized = str(status or "unknown").strip().lower().replace("-", "_")
    return normalized if normalized in {"observed", "skipped", "missing_report", "observer_failed"} else "unknown"


def collect_signals(
    *,
    release_report: dict[str, Any] | None,
    prompt_prod_report: dict[str, Any] | None,
    oauth_mimic_report: dict[str, Any] | None,
    kiro_profile_report: dict[str, Any] | None,
    tracking_links: list[dict[str, Any]],
    release_scan_result: str,
    registry_gate_result: str,
    prod_aggregate_result: str,
    edge_oauth_mimic_result: str,
    kiro_profile_result: str,
    prod_observation_status: str = "unknown",
    edge_oauth_mimic_observation_status: str = "unknown",
    kiro_profile_observation_status: str = "unknown",
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

    prod_observation_status = _normalize_observation_status(prod_observation_status)
    edge_oauth_mimic_observation_status = _normalize_observation_status(edge_oauth_mimic_observation_status)
    kiro_profile_observation_status = _normalize_observation_status(kiro_profile_observation_status)

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
    elif prod_observation_status == "observer_failed" or (
        prod_aggregate_result == "failure" and registry_gate_result == "success"
    ):
        signals.append({
            "signal_type": "observer-failure",
            "status": "actionable",
            "observer": "prod-aggregate",
            "detail": "prod observer failed before producing a usable aggregate report",
        })

    if oauth_mimic_report:
        summary = oauth_mimic_report.get("summary") or {}
        if summary.get("has_actionable_drift"):
            sig = {
                "signal_type": "oauth-mimic-drift",
                "status": "actionable",
                "alerts": summary.get("alerts") or [],
                "eligible_edges": summary.get("eligible_edges") or [],
            }
            link = find_link(tracking_links, signal_type="oauth-mimic-drift", kind="issue")
            if link:
                sig["tracking"] = link
            signals.append(sig)
    elif edge_oauth_mimic_observation_status == "observer_failed" or (
        edge_oauth_mimic_result == "failure" and registry_gate_result == "success"
    ):
        signals.append({
            "signal_type": "observer-failure",
            "status": "actionable",
            "observer": "edge-oauth-mimic",
            "detail": "edge OAuth mimic observer failed before producing a usable aggregate report",
        })

    if kiro_profile_report and kiro_profile_report.get("status") == "drift":
        signals.append({
            "signal_type": "kiro-production-config-drift",
            "status": "actionable",
            "evidence_level": "production_configured",
            "detail": "production DB profile differs from the captured Kiro canonical profile",
            "field_diffs": kiro_profile_report.get("field_diffs") or {},
        })
    elif kiro_profile_observation_status == "observer_failed" or (
        kiro_profile_result == "failure" and registry_gate_result == "success"
    ):
        signals.append({
            "signal_type": "observer-failure",
            "status": "actionable",
            "observer": "kiro-production-configured",
            "detail": "Kiro production-configured parity observer failed before producing a usable report",
        })

    return signals


def derive_pipeline_health(
    *,
    release_report: dict[str, Any] | None,
    release_scan_result: str,
    registry_gate_result: str,
    prod_aggregate_result: str,
    edge_oauth_mimic_result: str,
    kiro_profile_result: str,
    prod_observation_status: str,
    edge_oauth_mimic_observation_status: str,
    kiro_profile_observation_status: str,
) -> dict[str, Any]:
    job_results = {
        "release-scan": release_scan_result,
        "registry-gate": registry_gate_result,
        "prod-aggregate": prod_aggregate_result,
        "edge-oauth-mimic": edge_oauth_mimic_result,
        "kiro-production-configured": kiro_profile_result,
    }
    observations = {
        "prod-aggregate": _normalize_observation_status(prod_observation_status),
        "edge-oauth-mimic": _normalize_observation_status(edge_oauth_mimic_observation_status),
        "kiro-production-configured": _normalize_observation_status(kiro_profile_observation_status),
    }
    failed_jobs = [
        name
        for name, result in job_results.items()
        if result == "failure" and observations.get(name) != "observed"
    ]
    incomplete_jobs = [name for name, result in job_results.items() if result in {"skipped", "unknown", "cancelled"}]
    if release_scan_result == "success" and release_report is None:
        incomplete_jobs.append("release-scan-report")
    failed_observers = [name for name, status in observations.items() if status == "observer_failed"]
    incomplete_observers = [name for name, status in observations.items() if status in {"skipped", "missing_report", "unknown"}]
    if failed_jobs or failed_observers:
        status = "failed"
    elif incomplete_jobs or incomplete_observers:
        status = "degraded"
    else:
        status = "healthy"
    return {
        "status": status,
        "failed_jobs": failed_jobs,
        "incomplete_jobs": incomplete_jobs,
        "failed_observers": failed_observers,
        "incomplete_observers": incomplete_observers,
    }


def derive_fidelity_verdict(
    *,
    release_report: dict[str, Any] | None,
    prompt_prod_report: dict[str, Any] | None,
    oauth_mimic_report: dict[str, Any] | None,
    kiro_profile_report: dict[str, Any] | None,
    release_scan_result: str,
    registry_gate_result: str,
    prod_observation_status: str,
    edge_oauth_mimic_observation_status: str,
    kiro_profile_observation_status: str,
) -> dict[str, Any]:
    observations: list[dict[str, Any]] = []
    reasons: list[str] = []
    candidates: set[str] = set()

    if release_scan_result == "failure" or registry_gate_result == "failure":
        candidates.add("observer_failed")
        reasons.append("required release or registry observer failed")

    if release_report is None:
        candidates.add("incomplete")
        reasons.append("release report is missing")
        observations.append({"name": "release-metadata", "status": "missing_report"})
    else:
        platforms = release_report.get("platforms") or []
        unknown_ids = [str(item.get("id")) for item in platforms if item.get("status") == "unknown"]
        stale_ids = [str(item.get("id")) for item in platforms if item.get("drift")]
        if not platforms:
            candidates.add("incomplete")
            reasons.append("release report contains no platform observations")
        if unknown_ids:
            candidates.add("incomplete")
            reasons.append("release metadata is unknown for: " + ", ".join(unknown_ids))
        if stale_ids:
            candidates.add("stale")
            reasons.append("newer releases require recapture or revalidation: " + ", ".join(stale_ids))
        observations.append({
            "name": "release-metadata",
            "status": "stale" if stale_ids else "incomplete" if unknown_ids else "observed",
            "unknown_platforms": unknown_ids,
            "newer_release_platforms": stale_ids,
        })

    for name, status, report in (
        ("prod-aggregate", _normalize_observation_status(prod_observation_status), prompt_prod_report),
        ("edge-oauth-mimic", _normalize_observation_status(edge_oauth_mimic_observation_status), oauth_mimic_report),
    ):
        if status == "observer_failed":
            candidates.add("observer_failed")
            reasons.append(f"{name} observer failed")
        elif status in {"skipped", "missing_report", "unknown"}:
            candidates.add("incomplete")
            reasons.append(f"{name} observation is {status}")
        elif report is None:
            candidates.add("incomplete")
            reasons.append(f"{name} report is missing")
            status = "missing_report"
        elif (report.get("summary") or {}).get("has_actionable_drift"):
            candidates.add("drift")
            reasons.append(f"{name} measured actionable drift")
            status = "drift"
        observations.append({"name": name, "status": status})

    kiro_status = _normalize_observation_status(kiro_profile_observation_status)
    if kiro_status == "observer_failed":
        candidates.add("observer_failed")
        reasons.append("Kiro production-configured parity observer failed")
    elif kiro_status in {"skipped", "missing_report", "unknown"}:
        candidates.add("incomplete")
        reasons.append(f"Kiro production-configured parity observation is {kiro_status}")
    elif kiro_profile_report is None:
        candidates.add("incomplete")
        reasons.append("Kiro production-configured parity report is missing")
        kiro_status = "missing_report"
    elif kiro_profile_report.get("status") == "drift":
        candidates.add("drift")
        reasons.append("Kiro production DB profile differs from the captured canonical profile")
        kiro_status = "drift"
    elif kiro_profile_report.get("status") == "missing":
        candidates.add("incomplete")
        reasons.append("Kiro canonical production DB profile is missing")
        kiro_status = "missing_report"
    elif kiro_profile_report.get("status") == "observer_failed":
        candidates.add("observer_failed")
        reasons.append("Kiro production-configured parity report records observer failure")
        kiro_status = "observer_failed"
    observations.append({
        "name": "kiro-production-configured",
        "status": kiro_status,
        "evidence_level": "production_configured",
    })

    precedence = ["observer_failed", "drift", "stale", "incomplete"]
    status = next((candidate for candidate in precedence if candidate in candidates), "healthy")
    if status == "healthy":
        reasons.append("all required observations completed with no measured drift or newer release")
    return {"status": status, "reasons": reasons, "observations": observations}


def render_markdown(payload: dict[str, Any]) -> str:
    lines = [
        "# Client fidelity watch — daily report",
        "",
        f"- Generated: `{payload.get('generated_at')}`",
        f"- Run: {payload.get('run_url') or 'n/a'}",
        f"- Workflow blocking: `{payload.get('workflow_should_fail')}`",
        f"- Pipeline health: **{(payload.get('pipeline_health') or {}).get('status', 'unknown')}**",
        f"- Fidelity verdict: **{(payload.get('fidelity_verdict') or {}).get('status', 'unknown')}**",
        "",
        "## Fidelity reasons",
        "",
    ]
    for reason in (payload.get("fidelity_verdict") or {}).get("reasons") or []:
        lines.append(f"- {reason}")
    lines.extend([
        "",
        "## Job results",
        "",
    ])
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
        elif st == "oauth-mimic-drift":
            alerts = sig.get("alerts") or []
            edges = sig.get("eligible_edges") or []
            if alerts:
                edge_hint = f" edges={edges}" if edges else ""
                lines.append(f"- **oauth-mimic-drift**: {', '.join(alerts)}{edge_hint}{tracking_text}")
            else:
                lines.append(f"- **oauth-mimic-drift**: {sig.get('detail', 'actionable drift')}{tracking_text}")
        elif st == "kiro-production-config-drift":
            fields = sorted((sig.get("field_diffs") or {}).keys())
            lines.append(
                f"- **kiro-production-config-drift** (`production_configured`, not wire-observed): "
                f"{sig.get('detail')} fields={fields}"
            )
        elif st == "observer-failure":
            lines.append(f"- **observer-failure** `{sig.get('observer')}`: {sig.get('detail')}")
    tracking_links = payload.get("tracking_links") or []
    if tracking_links:
        lines.extend(["", "## Tracking links", ""])
        lines.append("| Type | Target | Status | Link |")
        lines.append("|---|---|---|---|")
        for link in tracking_links:
            target = link.get("platform_id") or link.get("signal_type") or link.get("title") or "n/a"
            number = f"#{link.get('number')}" if link.get("number") else link.get("title", "n/a")
            url = link.get("url") or "n/a"
            lines.append(f"| `{link.get('kind', 'link')}` | `{target}` | `{link.get('status', 'n/a')}` | [{number}]({url}) |")
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
    oauth_md = payload.get("sections", {}).get("oauth_mimic_markdown")
    if oauth_md:
        lines.extend(["## OAuth mimic edge aggregate (detail)", "", oauth_md.strip(), ""])
    return "\n".join(lines) + "\n"


def build_payload(
    *,
    run_url: str,
    release_report: dict[str, Any] | None,
    release_markdown: str | None,
    prompt_prod_report: dict[str, Any] | None,
    prompt_prod_markdown: str | None,
    oauth_mimic_report: dict[str, Any] | None,
    oauth_mimic_markdown: str | None,
    kiro_profile_report: dict[str, Any] | None,
    tracking_links: list[dict[str, Any]],
    release_scan_result: str,
    registry_gate_result: str,
    prod_aggregate_result: str,
    edge_oauth_mimic_result: str,
    kiro_profile_result: str,
    prod_observation_status: str = "unknown",
    edge_oauth_mimic_observation_status: str = "unknown",
    kiro_profile_observation_status: str = "unknown",
) -> dict[str, Any]:
    from datetime import datetime, timezone

    signals = collect_signals(
        release_report=release_report,
        prompt_prod_report=prompt_prod_report,
        oauth_mimic_report=oauth_mimic_report,
        kiro_profile_report=kiro_profile_report,
        tracking_links=tracking_links,
        release_scan_result=release_scan_result,
        registry_gate_result=registry_gate_result,
        prod_aggregate_result=prod_aggregate_result,
        edge_oauth_mimic_result=edge_oauth_mimic_result,
        kiro_profile_result=kiro_profile_result,
        prod_observation_status=prod_observation_status,
        edge_oauth_mimic_observation_status=edge_oauth_mimic_observation_status,
        kiro_profile_observation_status=kiro_profile_observation_status,
    )
    pipeline_health = derive_pipeline_health(
        release_report=release_report,
        release_scan_result=release_scan_result,
        registry_gate_result=registry_gate_result,
        prod_aggregate_result=prod_aggregate_result,
        edge_oauth_mimic_result=edge_oauth_mimic_result,
        kiro_profile_result=kiro_profile_result,
        prod_observation_status=prod_observation_status,
        edge_oauth_mimic_observation_status=edge_oauth_mimic_observation_status,
        kiro_profile_observation_status=kiro_profile_observation_status,
    )
    fidelity_verdict = derive_fidelity_verdict(
        release_report=release_report,
        prompt_prod_report=prompt_prod_report,
        oauth_mimic_report=oauth_mimic_report,
        kiro_profile_report=kiro_profile_report,
        release_scan_result=release_scan_result,
        registry_gate_result=registry_gate_result,
        prod_observation_status=prod_observation_status,
        edge_oauth_mimic_observation_status=edge_oauth_mimic_observation_status,
        kiro_profile_observation_status=kiro_profile_observation_status,
    )
    workflow_should_fail = any(
        sig.get("signal_type") in {
            "release-scan-failure",
            "registry-failure",
            "prod-drift",
            "oauth-mimic-drift",
            "kiro-production-config-drift",
            "observer-failure",
        }
        and sig.get("status") == "actionable"
        for sig in signals
    ) or release_scan_result == "failure" or registry_gate_result == "failure" or prod_aggregate_result == "failure" or edge_oauth_mimic_result == "failure" or kiro_profile_result == "failure"

    return {
        "schema_version": 1,
        "generated_at": datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z"),
        "run_url": run_url,
        "workflow_should_fail": workflow_should_fail,
        "pipeline_health": pipeline_health,
        "fidelity_verdict": fidelity_verdict,
        "job_results": {
            "release-scan": release_scan_result,
            "registry-gate": registry_gate_result,
            "prod-aggregate": prod_aggregate_result,
            "edge-oauth-mimic": edge_oauth_mimic_result,
            "kiro-production-configured": kiro_profile_result,
        },
        "observation_statuses": {
            "prod-aggregate": _normalize_observation_status(prod_observation_status),
            "edge-oauth-mimic": _normalize_observation_status(edge_oauth_mimic_observation_status),
            "kiro-production-configured": _normalize_observation_status(kiro_profile_observation_status),
        },
        "signals": signals,
        "tracking_links": tracking_links,
        "routing_table": ROUTING_TABLE,
        "sections": {
            "release_markdown": release_markdown,
            "prompt_prod_markdown": prompt_prod_markdown,
            "oauth_mimic_markdown": oauth_mimic_markdown,
        },
        "release_summary": (release_report or {}).get("summary"),
        "prompt_prod_summary": (prompt_prod_report or {}).get("summary") if prompt_prod_report else None,
        "oauth_mimic_summary": (oauth_mimic_report or {}).get("summary") if oauth_mimic_report else None,
        "kiro_production_configured": kiro_profile_report,
    }


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--release-report-json", type=Path)
    ap.add_argument("--release-report-md", type=Path)
    ap.add_argument("--prompt-prod-report-json", type=Path)
    ap.add_argument("--prompt-prod-report-md", type=Path)
    ap.add_argument("--oauth-mimic-report-json", type=Path)
    ap.add_argument("--oauth-mimic-report-md", type=Path)
    ap.add_argument("--kiro-profile-report-json", type=Path)
    ap.add_argument("--release-links-json", type=Path)
    ap.add_argument("--prompt-links-json", type=Path)
    ap.add_argument("--oauth-mimic-links-json", type=Path)
    ap.add_argument("--release-scan-result", default="success")
    ap.add_argument("--registry-gate-result", default="unknown")
    ap.add_argument("--prod-aggregate-result", default="unknown")
    ap.add_argument("--edge-oauth-mimic-result", default="unknown")
    ap.add_argument("--kiro-profile-result", default="unknown")
    ap.add_argument("--prod-observation-status", default="unknown")
    ap.add_argument("--edge-oauth-mimic-observation-status", default="unknown")
    ap.add_argument("--kiro-profile-observation-status", default="unknown")
    ap.add_argument("--run-url", default="")
    ap.add_argument("--report-json", type=Path, default=DEFAULT_OUT_DIR / "report.json")
    ap.add_argument("--report-md", type=Path, default=DEFAULT_OUT_DIR / "report.md")
    ap.add_argument("--selftest", action="store_true")
    args = ap.parse_args(argv)

    if args.selftest:
        def fixture(**overrides: Any) -> dict[str, Any]:
            values: dict[str, Any] = {
                "run_url": "https://example/run/1",
                "release_report": {
                    "platforms": [
                        {"id": "claude-code", "status": "aligned", "drift": False, "issue_suppressed": False}
                    ],
                    "summary": {},
                },
                "release_markdown": None,
                "prompt_prod_report": {"summary": {"has_actionable_drift": False, "alerts": [], "count": 1}},
                "prompt_prod_markdown": None,
                "oauth_mimic_report": {"summary": {"has_actionable_drift": False, "alerts": [], "eligible_edges": ["us3"]}},
                "oauth_mimic_markdown": None,
                "kiro_profile_report": {
                    "status": "healthy",
                    "evidence_level": "production_configured",
                    "field_diffs": {},
                },
                "tracking_links": [],
                "release_scan_result": "success",
                "registry_gate_result": "success",
                "prod_aggregate_result": "success",
                "edge_oauth_mimic_result": "success",
                "kiro_profile_result": "success",
                "prod_observation_status": "observed",
                "edge_oauth_mimic_observation_status": "observed",
                "kiro_profile_observation_status": "observed",
            }
            values.update(overrides)
            return build_payload(**values)

        healthy = fixture()
        assert healthy["pipeline_health"]["status"] == "healthy"
        assert healthy["fidelity_verdict"]["status"] == "healthy"
        assert not any(s["signal_type"] == "aligned" for s in healthy["signals"])

        release_unknown = fixture(
            release_report={"platforms": [{"id": "codex", "status": "unknown", "drift": False}], "summary": {}}
        )
        assert release_unknown["fidelity_verdict"]["status"] == "incomplete"

        release_newer = fixture(
            release_report={
                "platforms": [
                    {
                        "id": "claude-code",
                        "name": "CC",
                        "status": "drift",
                        "drift": True,
                        "issue_suppressed": False,
                        "pinned": "1",
                        "upstream_latest": "2",
                        "skill": "tokenkey-cc-fingerprint-alignment",
                    }
                ],
                "summary": {},
            }
        )
        assert release_newer["fidelity_verdict"]["status"] == "stale"
        assert any(s["signal_type"] == "release-drift" for s in release_newer["signals"])

        oidc_skipped = fixture(
            prod_observation_status="skipped",
            prod_aggregate_result="success",
            prompt_prod_report=None,
        )
        assert oidc_skipped["pipeline_health"]["status"] == "degraded"
        assert oidc_skipped["fidelity_verdict"]["status"] == "incomplete"

        missing_report = fixture(
            edge_oauth_mimic_observation_status="missing_report",
            oauth_mimic_report=None,
        )
        assert missing_report["fidelity_verdict"]["status"] == "incomplete"

        observer_failure = fixture(
            prod_observation_status="observer_failed",
            prod_aggregate_result="failure",
            prompt_prod_report=None,
        )
        assert observer_failure["pipeline_health"]["status"] == "failed"
        assert observer_failure["fidelity_verdict"]["status"] == "observer_failed"
        assert any(s["signal_type"] == "observer-failure" for s in observer_failure["signals"])
        assert not any(s["signal_type"] == "prod-drift" for s in observer_failure["signals"])

        measured_drift = fixture(
            prompt_prod_report={"summary": {"has_actionable_drift": True, "alerts": ["x=1"], "count": 1}}
        )
        assert measured_drift["fidelity_verdict"]["status"] == "drift"
        assert any(s["signal_type"] == "prod-drift" for s in measured_drift["signals"])

        kiro_config_drift = fixture(
            kiro_profile_report={
                "status": "drift",
                "evidence_level": "production_configured",
                "field_diffs": {"extensions": {"canonical": [1], "database": [2]}},
            }
        )
        assert kiro_config_drift["fidelity_verdict"]["status"] == "drift"
        assert any(s["signal_type"] == "kiro-production-config-drift" for s in kiro_config_drift["signals"])
        assert kiro_config_drift["kiro_production_configured"]["evidence_level"] == "production_configured"

        kiro_missing = fixture(
            kiro_profile_report={"status": "missing", "evidence_level": "production_configured"}
        )
        assert kiro_missing["fidelity_verdict"]["status"] == "incomplete"

        failed_release = fixture(
            release_report=None,
            release_scan_result="failure",
            registry_gate_result="skipped",
            prod_aggregate_result="skipped",
            edge_oauth_mimic_result="skipped",
            prod_observation_status="skipped",
            edge_oauth_mimic_observation_status="skipped",
            kiro_profile_result="skipped",
            kiro_profile_observation_status="skipped",
            prompt_prod_report=None,
            oauth_mimic_report=None,
            kiro_profile_report=None,
        )
        assert failed_release["workflow_should_fail"] is True
        assert failed_release["fidelity_verdict"]["status"] == "observer_failed"
        assert any(s["signal_type"] == "release-scan-failure" for s in failed_release["signals"])

        for payload in (
            release_unknown,
            release_newer,
            oidc_skipped,
            missing_report,
            observer_failure,
            measured_drift,
            kiro_config_drift,
            kiro_missing,
            failed_release,
        ):
            assert payload["fidelity_verdict"]["status"] != "healthy"
        md = render_markdown(observer_failure)
        assert "Pipeline health: **failed**" in md and "Fidelity verdict: **observer_failed**" in md
        print("client-fidelity-watch report selftest ok")
        return 0

    release_report = load_json(args.release_report_json)
    prompt_prod_report = load_json(args.prompt_prod_report_json)
    oauth_mimic_report = load_json(args.oauth_mimic_report_json)
    kiro_profile_report = load_json(args.kiro_profile_report_json)
    tracking_links = [
        *load_links(args.release_links_json),
        *load_links(args.prompt_links_json),
        *load_links(args.oauth_mimic_links_json),
    ]
    release_md = args.release_report_md.read_text(encoding="utf-8") if args.release_report_md and args.release_report_md.is_file() else None
    prompt_md = args.prompt_prod_report_md.read_text(encoding="utf-8") if args.prompt_prod_report_md and args.prompt_prod_report_md.is_file() else None
    oauth_md = args.oauth_mimic_report_md.read_text(encoding="utf-8") if args.oauth_mimic_report_md and args.oauth_mimic_report_md.is_file() else None

    payload = build_payload(
        run_url=args.run_url,
        release_report=release_report,
        release_markdown=release_md,
        prompt_prod_report=prompt_prod_report,
        prompt_prod_markdown=prompt_md,
        oauth_mimic_report=oauth_mimic_report,
        oauth_mimic_markdown=oauth_md,
        kiro_profile_report=kiro_profile_report,
        tracking_links=tracking_links,
        release_scan_result=args.release_scan_result,
        registry_gate_result=args.registry_gate_result,
        prod_aggregate_result=args.prod_aggregate_result,
        edge_oauth_mimic_result=args.edge_oauth_mimic_result,
        kiro_profile_result=args.kiro_profile_result,
        prod_observation_status=args.prod_observation_status,
        edge_oauth_mimic_observation_status=args.edge_oauth_mimic_observation_status,
        kiro_profile_observation_status=args.kiro_profile_observation_status,
    )
    args.report_json.parent.mkdir(parents=True, exist_ok=True)
    args.report_json.write_text(json.dumps(payload, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    args.report_md.write_text(render_markdown(payload), encoding="utf-8")
    print(render_markdown(payload))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
