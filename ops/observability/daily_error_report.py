#!/usr/bin/env python3
"""Build deterministic daily error reports from the read-only probe output."""
from __future__ import annotations

import argparse
import datetime as dt
import hashlib
import json
import pathlib
import re
import sys
from collections import defaultdict
from typing import Any

SECTION_RE = re.compile(r"^=== ([a-z0-9_]+) ===$")
REPAIR_EXCLUDED = re.compile(
    r"(?:rate.?limit|quota|capacity|no.?available|empty.?pool|cooldown|cancel|auth|permission|billing)",
    re.IGNORECASE,
)


class ReportError(ValueError):
    pass


def clean_text(value: Any, limit: int = 120) -> str:
    text = re.sub(r"[\x00-\x1f\x7f]+", " ", str(value or "")).strip()
    return text[:limit] or "unknown"


def as_int(value: Any) -> int:
    try:
        return int(value or 0)
    except (TypeError, ValueError):
        return 0


def parse_probe(text: str) -> dict[str, list[dict[str, Any]]]:
    sections: dict[str, list[dict[str, Any]]] = defaultdict(list)
    current = ""
    for raw_line in text.splitlines():
        line = raw_line.strip()
        match = SECTION_RE.match(line)
        if match:
            current = match.group(1)
            continue
        if not line or not current:
            continue
        try:
            item = json.loads(line)
        except json.JSONDecodeError as exc:
            raise ReportError(f"invalid JSON in section {current}: {line[:160]}") from exc
        if not isinstance(item, dict):
            raise ReportError(f"section {current} must contain JSON objects")
        sections[current].append(item)
    if not sections.get("meta"):
        raise ReportError("probe output is missing the meta section")
    return dict(sections)


def cluster_key(item: dict[str, Any], source: str = "ops_error_logs") -> dict[str, Any]:
    if source == "access_log":
        return {
            "source": source,
            "status_code": as_int(item.get("status_code")),
            "owner": "unknown",
            "phase": "access",
            "error_type": "uncaptured_access_error",
            "platform": "unknown",
            "model": clean_text(item.get("model")),
            "endpoint": clean_text(item.get("endpoint")),
        }
    return {
        "source": source,
        "status_code": as_int(item.get("status_code")),
        "owner": clean_text(item.get("owner"), 32).lower(),
        "phase": clean_text(item.get("phase"), 48).lower(),
        "error_type": clean_text(item.get("error_type"), 96).lower(),
        "platform": clean_text(item.get("platform"), 64).lower(),
        "model": clean_text(item.get("model")),
        "endpoint": clean_text(item.get("endpoint")),
    }


def signature_for(key: dict[str, Any], target_id: str) -> str:
    canonical = json.dumps(
        {"target_id": clean_text(target_id, 80), **key},
        sort_keys=True,
        separators=(",", ":"),
    )
    return "daily-error|" + hashlib.sha256(canonical.encode()).hexdigest()[:16]


def classify_state(current: int, previous: int, active_days: int) -> str:
    if previous == 0:
        return "new"
    if current >= max(previous * 2, previous + 5):
        return "regressed"
    if active_days >= 3:
        return "persistent"
    return "ongoing"


def classify_cluster(item: dict[str, Any], max_count_5m: int, target_id: str) -> dict[str, Any]:
    key = cluster_key(item)
    current = as_int(item.get("current_count"))
    previous = as_int(item.get("previous_count"))
    active_days = as_int(item.get("active_days_7d"))
    state = classify_state(current, previous, active_days)
    status = key["status_code"]
    owner = key["owner"]
    excluded = bool(REPAIR_EXCLUDED.search(f"{key['phase']} {key['error_type']}"))
    code_owned = owner == "platform" and key["phase"] == "internal" and status >= 500 and not excluded
    confidence = "high" if code_owned and current >= 5 and state in {"new", "regressed", "persistent"} else (
        "medium" if owner in {"platform", "provider"} and current >= 3 else "low"
    )
    repair_eligible = code_owned and confidence == "high"
    anomaly = False
    if owner in {"platform", "provider"} and status != 499:
        anomaly = (
            (state in {"new", "regressed"} and current >= 5)
            or max_count_5m >= 10
            or (state == "persistent" and current >= 10)
            or (status >= 500 and current >= 3)
        )
    severity = "error" if owner == "platform" and status >= 500 else "warning"
    priority = (
        (100 if repair_eligible else 0)
        + (30 if status >= 500 else 0)
        + (20 if state == "new" else 15 if state == "regressed" else 5)
        + min(current, 50)
        + min(max_count_5m, 20)
    )
    return {
        **key,
        "signature": signature_for(key, target_id),
        "state": state,
        "severity": severity,
        "current_count": current,
        "previous_count": previous,
        "baseline_7d_count": as_int(item.get("baseline_7d_count")),
        "active_days_7d": active_days,
        "max_count_5m": max_count_5m,
        "first_seen_7d": item.get("first_seen_7d"),
        "last_seen": item.get("last_seen"),
        "account_ids": [as_int(v) for v in (item.get("account_ids") or [])][:5],
        "group_ids": [as_int(v) for v in (item.get("group_ids") or [])][:5],
        "anomaly": anomaly,
        "code_owned": code_owned,
        "confidence": confidence,
        "repair_eligible": repair_eligible,
        "repair_reason": (
            "platform-owned final 5xx with repeat evidence and non-operational classification"
            if repair_eligible
            else "not a high-confidence code-owned final 5xx"
        ),
        "priority": priority,
    }


def access_coverage_key(item: dict[str, Any]) -> tuple[int, str, str]:
    def dimension(value: Any) -> str:
        text = clean_text(value).lower()
        return "unknown" if text in {"unknown", "(unknown)"} else text

    return (
        as_int(item.get("status_code")),
        dimension(item.get("model")),
        dimension(item.get("endpoint")),
    )


def classify_access_cluster(
    item: dict[str, Any], target_id: str, captured_count: int = 0
) -> dict[str, Any] | None:
    observed = as_int(item.get("current_count"))
    status = as_int(item.get("status_code"))
    captured = min(max(captured_count, 0), observed)
    current = observed - captured
    if current <= 0 or status < 400:
        return None
    key = cluster_key(item, source="access_log")
    anomaly = status >= 500 and current >= 3
    return {
        **key,
        "signature": signature_for(key, target_id),
        "state": "observed",
        "severity": "warning",
        "observed_count": observed,
        "captured_count": captured,
        "current_count": current,
        "previous_count": 0,
        "baseline_7d_count": 0,
        "active_days_7d": 0,
        "max_count_5m": min(as_int(item.get("max_count_1m")), current),
        "first_seen_7d": None,
        "last_seen": None,
        "account_ids": [],
        "group_ids": [],
        "anomaly": anomaly,
        "code_owned": False,
        "confidence": "low",
        "repair_eligible": False,
        "repair_reason": "access-log-only errors require ownership capture before code repair",
        "priority": 35 + min(current, 50) if anomaly else min(current, 20),
    }


def build_report(probe_text: str, target_id: str) -> dict[str, Any]:
    sections = parse_probe(probe_text)
    meta = dict(sections["meta"][0])
    meta["target_id"] = clean_text(target_id, 80)
    target_id = meta["target_id"]
    if sections.get("skip"):
        reason = clean_text(sections["skip"][0].get("reason"), 200)
        return {
            "schema_version": 1,
            "target_id": meta["target_id"],
            "status": "skip",
            "meta": meta,
            "summary": f"daily error ledger skipped: {reason}",
            "totals": {},
            "clusters": [],
            "recovered_upstream": [],
            "issue_candidates": [],
            "repair_candidates": [],
        }

    totals = dict((sections.get("totals") or [{}])[0])
    burst_by_signature: dict[str, int] = {}
    for burst in sections.get("bursts", []):
        sig = signature_for(cluster_key(burst), target_id)
        burst_by_signature[sig] = max(burst_by_signature.get(sig, 0), as_int(burst.get("max_count_5m")))

    clusters = []
    captured_by_surface: dict[tuple[int, str, str], int] = defaultdict(int)
    for raw in sections.get("clusters", []):
        sig = signature_for(cluster_key(raw), target_id)
        peak = max(as_int(raw.get("max_count_5m")), burst_by_signature.get(sig, 0))
        classified = classify_cluster(raw, peak, target_id)
        clusters.append(classified)
        captured_by_surface[access_coverage_key(classified)] += as_int(classified.get("current_count"))
    for raw in sections.get("access_clusters", []):
        surface = access_coverage_key(raw)
        covered = min(captured_by_surface.get(surface, 0), as_int(raw.get("current_count")))
        captured_by_surface[surface] -= covered
        classified = classify_access_cluster(raw, target_id, covered)
        if classified:
            clusters.append(classified)
    clusters.sort(key=lambda row: (-as_int(row["priority"]), row["signature"]))

    issues = [row for row in clusters if row["anomaly"]]
    repairs = [row for row in clusters if row["repair_eligible"]]
    current_total = as_int(totals.get("current_request_total"))
    current_sla = as_int(totals.get("current_error_sla"))
    summary = (
        f"{as_int(meta.get('window_hours'))}h: requests={current_total}, "
        f"sla_errors={current_sla}, anomalies={len(issues)}, repair_candidates={len(repairs)}"
    )
    return {
        "schema_version": 1,
        "target_id": meta["target_id"],
        "status": "issue_candidate" if issues else "ok",
        "meta": meta,
        "summary": summary,
        "totals": totals,
        "clusters": clusters,
        "recovered_upstream": sections.get("recovered", []),
        "issue_candidates": issues,
        "repair_candidates": repairs,
    }


def markdown_report(report: dict[str, Any]) -> str:
    def cell(value: Any) -> str:
        return clean_text(value, 120).replace("|", "\\|")

    lines = [
        "# Daily Error Report",
        "",
        f"- Target: `{cell(report.get('target_id'))}`",
        f"- Status: `{cell(report.get('status'))}`",
        f"- Summary: {cell(report.get('summary'))}",
        "",
    ]
    if report.get("status") == "skip":
        return "\n".join(lines) + "\n"
    totals = report.get("totals") or {}
    lines += [
        "## SLA",
        "",
        "| Requests | SLA errors | Client faults | Recovered upstream | SLA |",
        "| ---: | ---: | ---: | ---: | ---: |",
        "| {requests} | {sla} | {client} | {recovered} | {percent}% |".format(
            requests=as_int(totals.get("current_request_total")),
            sla=as_int(totals.get("current_error_sla")),
            client=as_int(totals.get("current_client_faults")),
            recovered=as_int(totals.get("current_recovered_requests")),
            percent=totals.get("current_sla_percent", 0),
        ),
        "",
        "## Error Clusters",
        "",
        "| State | Owner | Status | Platform | Model | Endpoint | Current | Previous | Peak | Repair |",
        "| --- | --- | ---: | --- | --- | --- | ---: | ---: | ---: | --- |",
    ]
    for row in (report.get("clusters") or [])[:30]:
        lines.append(
            "| {state} | {owner} | {status} | {platform} | {model} | {endpoint} | {current} | {previous} | {peak} | {repair} |".format(
                state=cell(row.get("state")), owner=cell(row.get("owner")), status=as_int(row.get("status_code")),
                platform=cell(row.get("platform")), model=cell(row.get("model")), endpoint=cell(row.get("endpoint")),
                current=as_int(row.get("current_count")), previous=as_int(row.get("previous_count")),
                peak=as_int(row.get("max_count_5m")), repair="yes" if row.get("repair_eligible") else "no",
            )
        )
    lines.append("")
    return "\n".join(lines) + "\n"


def aggregate_reports(paths: list[pathlib.Path], run_id: str, run_url: str) -> dict[str, Any]:
    reports = [json.loads(path.read_text(encoding="utf-8")) for path in sorted(paths)]
    issues = []
    repairs = []
    for report in reports:
        target_id = clean_text(report.get("target_id"), 80)
        for item in report.get("issue_candidates") or []:
            issues.append({**item, "target_id": target_id})
        for item in report.get("repair_candidates") or []:
            repairs.append({**item, "target_id": target_id})
    issues.sort(key=lambda row: (-as_int(row.get("priority")), row.get("signature", "")))
    repairs.sort(key=lambda row: (-as_int(row.get("priority")), row.get("signature", "")))
    return {
        "schema_version": 1,
        "run_id": clean_text(run_id, 40),
        "run_url": clean_text(run_url, 240),
        "generated_at": dt.datetime.now(dt.timezone.utc).isoformat().replace("+00:00", "Z"),
        "summary": f"targets={len(reports)}, anomalies={len(issues)}, repair_candidates={len(repairs)}",
        "reports": reports,
        "issue_candidates": issues,
        "repair_candidates": repairs,
    }


def aggregate_markdown(report: dict[str, Any]) -> str:
    lines = [
        "# Daily Error Report",
        "",
        f"- Run: {report.get('run_url')}",
        f"- Summary: {report.get('summary')}",
        "",
        "## Targets",
        "",
        "| Target | Status | Requests | SLA errors | Client faults | Recovered | Anomalies | Repair |",
        "| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |",
    ]
    for target in report.get("reports") or []:
        totals = target.get("totals") or {}
        lines.append(
            "| {target} | {status} | {requests} | {sla} | {client} | {recovered} | {issues} | {repairs} |".format(
                target=clean_text(target.get("target_id"), 80).replace("|", "\\|"),
                status=clean_text(target.get("status"), 32),
                requests=as_int(totals.get("current_request_total")),
                sla=as_int(totals.get("current_error_sla")),
                client=as_int(totals.get("current_client_faults")),
                recovered=as_int(totals.get("current_recovered_requests")),
                issues=len(target.get("issue_candidates") or []),
                repairs=len(target.get("repair_candidates") or []),
            )
        )
    lines += [
        "",
        "## Actionable Anomalies",
        "",
        "| Target | State | Owner | Status | Type | Model | Endpoint | Count | Repair |",
        "| --- | --- | --- | ---: | --- | --- | --- | ---: | --- |",
    ]
    for item in (report.get("issue_candidates") or [])[:30]:
        fields = {key: clean_text(item.get(key), 120).replace("|", "\\|") for key in (
            "target_id", "state", "owner", "error_type", "model", "endpoint"
        )}
        lines.append(
            "| {target_id} | {state} | {owner} | {status} | {error_type} | {model} | {endpoint} | {count} | {repair} |".format(
                **fields,
                status=as_int(item.get("status_code")),
                count=as_int(item.get("current_count")),
                repair="yes" if item.get("repair_eligible") else "no",
            )
        )
    lines.append("")
    return "\n".join(lines) + "\n"


def select_candidate(report: dict[str, Any], signature: str) -> dict[str, Any]:
    matches = [item for item in report.get("repair_candidates") or [] if item.get("signature") == signature]
    if len(matches) != 1:
        raise ReportError(f"repair candidate {signature!r} was not found exactly once")
    candidate = matches[0]
    if not candidate.get("repair_eligible") or candidate.get("confidence") != "high":
        raise ReportError("candidate is not high-confidence and repair-eligible")
    if (
        candidate.get("owner") != "platform"
        or candidate.get("phase") != "internal"
        or as_int(candidate.get("status_code")) < 500
    ):
        raise ReportError("candidate is not an internal platform-owned final 5xx")
    return candidate


def write_json(path: pathlib.Path, value: dict[str, Any]) -> None:
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def main() -> int:
    parser = argparse.ArgumentParser()
    sub = parser.add_subparsers(dest="command", required=True)
    build = sub.add_parser("build")
    build.add_argument("--probe", required=True, type=pathlib.Path)
    build.add_argument("--target-id", required=True)
    build.add_argument("--output-json", required=True, type=pathlib.Path)
    build.add_argument("--output-markdown", required=True, type=pathlib.Path)
    aggregate = sub.add_parser("aggregate")
    aggregate.add_argument("--root", required=True, type=pathlib.Path)
    aggregate.add_argument("--run-id", required=True)
    aggregate.add_argument("--run-url", required=True)
    aggregate.add_argument("--output-json", required=True, type=pathlib.Path)
    aggregate.add_argument("--output-markdown", required=True, type=pathlib.Path)
    select = sub.add_parser("select")
    select.add_argument("--report", required=True, type=pathlib.Path)
    select.add_argument("--signature", required=True)
    select.add_argument("--output", required=True, type=pathlib.Path)
    args = parser.parse_args()

    try:
        if args.command == "build":
            report = build_report(args.probe.read_text(encoding="utf-8", errors="replace"), args.target_id)
            write_json(args.output_json, report)
            args.output_markdown.write_text(markdown_report(report), encoding="utf-8")
        elif args.command == "aggregate":
            paths = list(args.root.glob("**/daily-error-report.json"))
            report = aggregate_reports(paths, args.run_id, args.run_url)
            write_json(args.output_json, report)
            args.output_markdown.write_text(aggregate_markdown(report), encoding="utf-8")
        else:
            report = json.loads(args.report.read_text(encoding="utf-8"))
            write_json(args.output, select_candidate(report, args.signature))
    except (OSError, json.JSONDecodeError, ReportError) as exc:
        print(f"[daily-error-report] ERROR: {exc}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
