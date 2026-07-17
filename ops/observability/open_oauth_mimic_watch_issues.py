#!/usr/bin/env python3
"""Open/update/close GitHub issues for OAuth mimic edge watch signals."""
from __future__ import annotations

import argparse
import json
import pathlib
import re
import subprocess
import sys

SIG_OAUTH_MIMIC_DRIFT = "om-sig:edge-oauth-mimic-drift"

LABEL_CLIENT_FIDELITY = "client-fidelity-watch"

BASE_LABELS = {
    "oauth-mimic-watch": ("F9D0A4", "OAuth mimic egress watch signal"),
    LABEL_CLIENT_FIDELITY: ("1D76DB", "Client fidelity umbrella watch"),
    "automated": ("C5DEF5", "Automated signal"),
    "needs-triage": ("FBCA04", "Needs human triage"),
    "oauth-mimic:edge-drift": ("D73A4A", "Edge OAuth mimic ratio drift"),
    "oauth-mimic:aligned": ("0E8A16", "OAuth mimic watch clear"),
    SIG_OAUTH_MIMIC_DRIFT: ("D73A4A", "OAuth mimic edge drift signature"),
}


def issue_body_path(sig_label: str) -> pathlib.Path:
    slug = re.sub(r"[^A-Za-z0-9_.-]+", "-", sig_label)[:80] or "unknown"
    return pathlib.Path(f".cache/oauth-mimic-watch/issue-{slug}.md")


def sh(args: list[str], *, check: bool = True) -> subprocess.CompletedProcess[str]:
    return subprocess.run(args, text=True, check=check, capture_output=True)


def issue_url(number: str) -> str:
    return sh(["gh", "issue", "view", number, "--json", "url", "--jq", ".url"]).stdout.strip()


def ensure_label(name: str, color: str, description: str) -> None:
    subprocess.run(
        ["gh", "label", "create", name, "--color", color, "--description", description[:100]],
        text=True,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )


def ensure_base_labels() -> None:
    for name, (color, desc) in BASE_LABELS.items():
        ensure_label(name, color, desc)


def find_open_issue(sig_label: str) -> str:
    proc = sh([
        "gh", "issue", "list", "--state", "open", "--label", sig_label,
        "--json", "number", "--limit", "1", "--jq", ".[0].number // empty",
    ])
    return proc.stdout.strip()


def drift_body(report: dict, run_url: str, report_md: str | None) -> str:
    summary = report.get("summary") or {}
    meta = report.get("meta") or {}
    lines = [
        "## Edge OAuth mimic egress drift",
        "",
        "Daily client-fidelity-watch detected actionable OAuth mimic ratio drift on one or more edges with schedulable Anthropic OAuth.",
        "",
        f"- Watchdog run: {run_url or 'n/a'}",
        f"- Since: `{meta.get('since', 'n/a')}`",
        f"- Eligible edges: `{summary.get('eligible_edge_count', 0)}`",
        f"- Signature: `{SIG_OAUTH_MIMIC_DRIFT}`",
        "",
        "## Alerts",
        "",
    ]
    alerts = summary.get("alerts") or []
    if alerts:
        lines.extend(f"- {item}" for item in alerts)
    else:
        lines.append("- (none listed)")
    lines.extend(["", "## Per-edge summary", ""])
    for row in summary.get("per_edge") or []:
        lines.append(
            f"- `{row.get('edge_id')}`: sdk_ingress={row.get('oauth_openai_python_count', 0)} "
            f"egress_logs={row.get('egress_oauth_mimic_count', 0)} "
            f"billing_rate={row.get('billing_prefix_rate', 'n/a')} "
            f"verdict={row.get('verdict') or row.get('probe_error') or 'n/a'}"
        )
    if report_md:
        lines.extend(["", "## Aggregate detail", "", report_md.strip()])
    lines.extend([
        "",
        "## Expected follow-up",
        "",
        "1. `bash ops/observability/run-probe.sh --target edge:<id> --script ops/observability/probe-oauth-mimicry-chain.sh --env PLATFORM=anthropic`",
        "2. Skill `tokenkey-cc-fingerprint-alignment` — HTTP/TLS/system mimic remediation.",
        "3. Confirm edge binary includes `gateway.anthropic_oauth_mimic_egress` logging.",
    ])
    return "\n".join(lines) + "\n"


def recover_body(run_url: str, report: dict) -> str:
    summary = report.get("summary") or {}
    return "\n".join([
        "Watchdog now reports **no actionable OAuth mimic edge drift**.",
        "",
        f"- Watchdog run: {run_url or 'n/a'}",
        f"- Eligible edges: `{summary.get('eligible_edge_count', 0)}`",
        f"- actionable_drift: `{summary.get('has_actionable_drift', False)}`",
    ]) + "\n"


def open_or_update_issue(*, sig_label: str, title: str, body: str, drift_labels: list[str]) -> dict[str, object]:
    ensure_base_labels()
    labels_csv = ",".join(drift_labels)
    existing = find_open_issue(sig_label)
    body_path = issue_body_path(sig_label)
    body_path.parent.mkdir(parents=True, exist_ok=True)
    body_path.write_text(body, encoding="utf-8")
    if existing:
        sh(["gh", "issue", "comment", existing, "--body-file", str(body_path)])
        sh(["gh", "issue", "edit", existing, "--add-label", labels_csv])
        print(f"updated issue #{existing} ({sig_label})")
        return {
            "kind": "issue",
            "title": title[:250],
            "number": int(existing),
            "url": issue_url(existing),
            "status": "updated",
        }
    created_url = sh([
        "gh", "issue", "create", "--title", title[:250], "--body-file", str(body_path), "--label", labels_csv,
    ]).stdout.strip()
    number_match = re.search(r"/issues/(\d+)(?:$|[?#])", created_url)
    print(f"created issue ({sig_label})")
    return {
        "kind": "issue",
        "title": title[:250],
        "number": int(number_match.group(1)) if number_match else None,
        "url": created_url,
        "status": "created",
    }


def close_issue(number: str, comment: str) -> dict[str, object]:
    url = issue_url(number)
    sh(["gh", "issue", "comment", number, "--body", comment])
    subprocess.run(
        ["gh", "issue", "edit", number, "--add-label", "oauth-mimic:aligned", "--remove-label", "oauth-mimic:edge-drift"],
        text=True,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    sh(["gh", "issue", "close", number, "--comment", "Closing because the watch signal cleared."])
    print(f"closed issue #{number}")
    return {"kind": "issue", "number": int(number), "url": url, "status": "closed"}


def drift_labels_for(base: list[str], *, umbrella: bool) -> list[str]:
    if umbrella and LABEL_CLIENT_FIDELITY not in base:
        return [base[0], LABEL_CLIENT_FIDELITY, *base[1:]]
    return base


def write_links(path: pathlib.Path | None, links: list[dict[str, object]]) -> None:
    if path is None:
        return
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps({"links": links}, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def cmd_edge_sync(args: argparse.Namespace) -> int:
    report = json.loads(pathlib.Path(args.report_json).read_text(encoding="utf-8"))
    report_md = None
    if args.report_md and args.report_md.is_file():
        report_md = args.report_md.read_text(encoding="utf-8")
    summary = report.get("summary") or {}
    if summary.get("has_actionable_drift"):
        body = drift_body(report, args.run_url, report_md)
        link = open_or_update_issue(
            sig_label=SIG_OAUTH_MIMIC_DRIFT,
            title="[oauth-mimic] edge egress ratio drift",
            body=body,
            drift_labels=drift_labels_for([
                "oauth-mimic-watch", "automated", "needs-triage",
                "oauth-mimic:edge-drift", SIG_OAUTH_MIMIC_DRIFT,
            ], umbrella=args.umbrella),
        )
        write_links(args.links_json, [{**link, "signal_type": "oauth-mimic-drift"}])
        return 0
    ensure_base_labels()
    existing = find_open_issue(SIG_OAUTH_MIMIC_DRIFT)
    if not existing:
        print("no open oauth mimic drift issue")
        write_links(args.links_json, [])
        return 0
    link = close_issue(existing, recover_body(args.run_url, report))
    write_links(args.links_json, [{**link, "signal_type": "oauth-mimic-drift"}])
    return 0


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="command", required=True)

    p_edge = sub.add_parser("edge-sync")
    p_edge.add_argument("--report-json", type=pathlib.Path, required=True)
    p_edge.add_argument("--report-md", type=pathlib.Path)
    p_edge.add_argument("--run-url", default="")
    p_edge.add_argument("--umbrella", action="store_true")
    p_edge.add_argument("--links-json", type=pathlib.Path)

    args = ap.parse_args(argv)
    if args.command == "edge-sync":
        return cmd_edge_sync(args)
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
