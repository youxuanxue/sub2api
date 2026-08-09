#!/usr/bin/env python3
"""Open, update, or close GitHub issues for Prod Ops diagnostics findings."""
from __future__ import annotations

import argparse
import hashlib
import json
import pathlib
import re
import subprocess
import sys

from ops.observability.daily_error_report import issue_analysis_markdown
from ops.observability.prod_ops_issue_decision import decide_issue_action  # ops/observability/prod_ops_issue_decision.py

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
DEFAULT_CACHE_DIR = REPO_ROOT / ".cache/observability/prod-ops-issue"

BASE_LABELS = {
    "prod-ops": ("BFD4F2", "Prod Ops signal"),
    "automated": ("C5DEF5", "Automated signal"),
    "needs-triage": ("FBCA04", "Needs human triage"),
}


def label_safe(value: str) -> str:
    value = re.sub(r"[^A-Za-z0-9_.:-]+", "-", value)[:50]
    return value or "unknown"


def signature_label(signature: str) -> str:
    sig_short = hashlib.sha256(signature.encode()).hexdigest()[:12]
    return f"ops-sig:{sig_short}"


def sh(args: list[str], *, check: bool = True) -> subprocess.CompletedProcess[str]:
    return subprocess.run(args, text=True, check=check, capture_output=True)


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


def recover_body(*, run_url: str, run_id: str) -> str:
    return "\n".join([
        "Latest Prod Ops diagnostics no longer reports this finding signature.",
        "",
        f"- Watchdog run: {run_url or 'n/a'}",
        f"- Artifact: `prod-ops-report-{run_id or 'n/a'}`",
        "",
        "Closing because the signal cleared on the most recent scheduled probe.",
    ]) + "\n"


def close_issue(number: str, comment: str) -> dict[str, object]:
    sh(["gh", "issue", "close", number, "--comment", comment])
    url = sh(["gh", "issue", "view", number, "--json", "url", "--jq", ".url"]).stdout.strip()
    print(f"closed issue #{number}")
    return {"kind": "issue", "number": int(number), "url": url, "status": "closed"}


def finding_labels(finding: dict[str, object]) -> tuple[str, str, str, str, list[str]]:
    target_id = str(finding.get("target_id") or "unknown")
    kind = str(finding.get("kind") or "unknown")
    signature = str(
        finding.get("signature") or f"{target_id}|{kind}|{finding.get('title', '')}"
    )
    ops_label = signature_label(signature)
    target_label = f"target:{label_safe(target_id)}"
    finding_label = f"finding:{label_safe(kind)}"
    ensure_label(ops_label, "BFD4F2", "Prod Ops finding signature")
    ensure_label(target_label, "D4C5F9", "Prod Ops target")
    ensure_label(finding_label, "F9D0C4", "Prod Ops finding kind")
    labels = ["prod-ops", "automated", "needs-triage", ops_label, target_label, finding_label]
    if kind == "error_cluster":
        cluster_short = hashlib.sha256(signature.encode()).hexdigest()[:12]
        legacy_cluster_label = f"cluster-sig:{cluster_short}"
        ensure_label(legacy_cluster_label, "BFD4F2", "cluster signature for cooldown")
        labels.append(legacy_cluster_label)
    return signature, ops_label, target_id, kind, labels


def finding_body(
    finding: dict[str, object],
    *,
    report: dict[str, object],
    daily_report: dict[str, object],
) -> str:
    target_id = str(finding.get("target_id") or "unknown")
    kind = str(finding.get("kind") or "unknown")
    signature = str(
        finding.get("signature") or f"{target_id}|{kind}|{finding.get('title', '')}"
    )
    body_lines = [
        "## Prod Ops finding",
        "",
        f"- Run: {report.get('run_url')}",
        f"- Target: `{target_id}`",
        f"- Kind: `{kind}`",
        f"- Status: `{finding.get('status')}`",
        f"- Severity: `{finding.get('severity')}`",
        f"- Signature: `{signature}`",
        "",
        "## Summary",
        "",
        str(finding.get("summary") or finding.get("title") or "No summary."),
        "",
        "## Evidence",
        "",
        f"See artifact `prod-ops-report-{report.get('run_id')}` for `ops-report.json` and per-target reports.",
    ]
    analysis = issue_analysis_markdown(daily_report, target_id) if kind == "daily_error_report" else ""
    if analysis:
        body_lines += ["", "## Deterministic error analysis", "", analysis]
    return "\n".join(body_lines) + "\n"


def sync_issues(
    report: dict[str, object],
    daily_report: dict[str, object],
    *,
    cache_dir: pathlib.Path = DEFAULT_CACHE_DIR,
) -> list[dict[str, object]]:
    """Open/update active findings and close recovered prod-ops signatures."""
    ensure_base_labels()
    cache_dir.mkdir(parents=True, exist_ok=True)
    links: list[dict[str, object]] = []
    findings = report.get("issue_candidates") or []
    active_sig_labels: set[str] = set()

    for index, finding in enumerate(findings, 1):
        if not isinstance(finding, dict):
            continue
        signature, ops_label, target_id, kind, labels = finding_labels(finding)
        active_sig_labels.add(ops_label)
        title = f"[prod-ops] {target_id} {kind}: {finding.get('title', 'finding')}"[:250]
        body = finding_body(finding, report=report, daily_report=daily_report)
        body_file = cache_dir / f"issue-{index}.md"
        body_file.write_text(body, encoding="utf-8")

        existing = json.loads(sh([
            "gh", "issue", "list", "--label", ops_label, "--state", "all",
            "--json", "number,state,closedAt,createdAt", "--limit", "100",
        ]).stdout or "[]")
        decision = decide_issue_action(existing)
        if decision["action"] == "update":
            number = str(decision["number"])
            sh(["gh", "issue", "comment", number, "--body-file", str(body_file)])
            print(f"updated issue #{number} for {ops_label}")
            links.append({"kind": "issue", "number": int(number), "status": "updated", "signature": signature})
        elif decision["action"] == "suppress":
            if decision.get("closed_at") == "unknown":
                print(
                    f"suppressed {ops_label}: issue #{decision['number']} is closed with "
                    f"unknown closedAt; fail-closed suppress until timestamp is available"
                )
            else:
                print(
                    f"suppressed {ops_label}: issue #{decision['number']} was closed at "
                    f"{decision['closed_at']} and remains in the 7-day cooldown"
                )
        else:
            created_url = sh([
                "gh", "issue", "create",
                "--title", title,
                "--body-file", str(body_file),
                "--label", ",".join(labels),
            ]).stdout.strip()
            number_match = re.search(r"/issues/(\d+)(?:$|[?#])", created_url)
            number = int(number_match.group(1)) if number_match else None
            print(f"created issue for {ops_label}")
            links.append({"kind": "issue", "number": number, "status": "created", "signature": signature})

    open_raw = sh([
        "gh", "issue", "list", "--label", "prod-ops", "--state", "open",
        "--json", "number,labels", "--limit", "200",
    ]).stdout.strip()
    if not open_raw or open_raw == "[]":
        return links

    comment = recover_body(
        run_url=str(report.get("run_url") or ""),
        run_id=str(report.get("run_id") or ""),
    )
    for row in json.loads(open_raw):
        number = str(row["number"])
        labels = {lbl["name"] for lbl in row.get("labels") or []}
        sig_labels = sorted(lbl for lbl in labels if lbl.startswith("ops-sig:"))
        if not sig_labels:
            continue
        if any(sig in active_sig_labels for sig in sig_labels):
            continue
        link = close_issue(number, comment)
        link["signature"] = sig_labels[0]
        links.append(link)

    return links


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--ops-report", type=pathlib.Path, default=pathlib.Path("ops-report.json"))
    ap.add_argument(
        "--daily-error-report",
        type=pathlib.Path,
        default=pathlib.Path("daily-error-report.json"),
    )
    ap.add_argument("--links-json", type=pathlib.Path)
    ap.add_argument(
        "--cache-dir",
        type=pathlib.Path,
        default=DEFAULT_CACHE_DIR,
        help="Directory for ephemeral issue body files (not repo root)",
    )
    args = ap.parse_args(argv)

    if not args.ops_report.is_file():
        print(f"missing report: {args.ops_report}", file=sys.stderr)
        return 2

    report = json.loads(args.ops_report.read_text(encoding="utf-8"))
    daily_report = {}
    if args.daily_error_report.is_file():
        daily_report = json.loads(args.daily_error_report.read_text(encoding="utf-8"))

    links = sync_issues(report, daily_report, cache_dir=args.cache_dir)
    if args.links_json:
        args.links_json.parent.mkdir(parents=True, exist_ok=True)
        args.links_json.write_text(
            json.dumps({"links": links}, ensure_ascii=False, indent=2) + "\n",
            encoding="utf-8",
        )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
