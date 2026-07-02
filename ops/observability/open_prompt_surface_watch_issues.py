#!/usr/bin/env python3
"""Open/update/close GitHub issues for prompt-surface-watch signals."""
from __future__ import annotations

import argparse
import json
import pathlib
import re
import subprocess
import sys

SIG_REGISTRY = "ps-sig:registry-gate-failure"
SIG_PROD_DRIFT = "ps-sig:prod-drift"

LABEL_CLIENT_FIDELITY = "client-fidelity-watch"

BASE_LABELS = {
    "prompt-surface-watch": ("BFD4F2", "Prompt surface watch signal"),
    LABEL_CLIENT_FIDELITY: ("1D76DB", "Client fidelity umbrella watch"),
    "automated": ("C5DEF5", "Automated signal"),
    "needs-triage": ("FBCA04", "Needs human triage"),
    "prompt-surface:registry-failure": ("D73A4A", "Registry/fixture gate failed"),
    "prompt-surface:prod-drift": ("D73A4A", "Prod fingerprint actionable drift"),
    "prompt-surface:aligned": ("0E8A16", "Prompt surface watch clear"),
    SIG_REGISTRY: ("D73A4A", "Registry gate failure signature"),
    SIG_PROD_DRIFT: ("D73A4A", "Prod drift signature"),
}


def label_safe(value: str) -> str:
    return re.sub(r"[^A-Za-z0-9_.:-]+", "-", value)[:50] or "unknown"


def filename_safe(value: str) -> str:
    """Filesystem-safe slug (GitHub artifact upload rejects colons in paths)."""
    return re.sub(r"[^A-Za-z0-9_.-]+", "-", value)[:80] or "unknown"


def issue_body_path(sig_label: str) -> pathlib.Path:
    return pathlib.Path(f".cache/prompt-surface-watch/issue-{filename_safe(sig_label)}.md")


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


def find_open_issue(sig_label: str) -> str:
    proc = sh([
        "gh", "issue", "list", "--state", "open", "--label", sig_label,
        "--json", "number", "--limit", "1", "--jq", ".[0].number // empty",
    ])
    return proc.stdout.strip()


def registry_failure_body(run_url: str) -> str:
    lines = [
        "## Prompt surface registry-gate failed",
        "",
        "The daily `registry-gate` job failed (registry drift, fixture gateway, or unit tests).",
        "",
        f"- Watchdog run: {run_url or 'n/a'}",
        f"- Signature: `{SIG_REGISTRY}`",
        "",
        "## Expected follow-up",
        "",
        "1. Open the failed Actions run and read the first failing step.",
        "2. Locally: `python3 ops/anthropic/probe_prompt_surfaces.py --check-registry`",
        "3. Locally: `python3 ops/anthropic/probe_prompt_surfaces.py --check-fixture-gateway`",
        "4. Fix drift or update `ops/anthropic/prompt_surface_registry.json` with review.",
    ]
    return "\n".join(lines) + "\n"


def prod_drift_body(report: dict, run_url: str, report_md: str | None) -> str:
    summary = report.get("summary") or {}
    meta = report.get("meta") or {}
    lines = [
        "## Prompt surface prod fingerprint drift",
        "",
        "Prod aggregate reported actionable drift vs `prompt_surface_registry.json`.",
        "",
        f"- Watchdog run: {run_url or 'n/a'}",
        f"- Container: `{meta.get('container', 'n/a')}`",
        f"- Since: `{meta.get('since', 'n/a')}`",
        f"- Rows sampled: `{summary.get('count', 0)}`",
        f"- Signature: `{SIG_PROD_DRIFT}`",
        "",
        "## Alerts",
        "",
    ]
    alerts = summary.get("alerts") or []
    if alerts:
        lines.extend(f"- {item}" for item in alerts)
    else:
        lines.append("- (none listed)")
    lines.extend(["", "## Aggregate summary", ""])
    if report_md:
        lines.append(report_md.strip())
    else:
        lines.append(json.dumps(summary, ensure_ascii=False, indent=2))
    lines.extend([
        "",
        "## Expected follow-up",
        "",
        "1. `bash ops/observability/run-probe.sh --target prod --script ops/observability/probe-prompt-surface-fingerprints.sh --env SINCE=24h --env LIMIT=40`",
        "2. Compare with `ops/anthropic/prompt_surface_registry.json` and recent CC client changes.",
        "3. Fix normalize/registry or confirm benign drift before closing this issue.",
    ])
    return "\n".join(lines) + "\n"


def prod_recover_body(run_url: str, report: dict) -> str:
    summary = report.get("summary") or {}
    return "\n".join([
        "Watchdog now reports **no actionable prod fingerprint drift**.",
        "",
        f"- Watchdog run: {run_url or 'n/a'}",
        f"- Rows sampled: `{summary.get('count', 0)}`",
        f"- actionable_drift: `{summary.get('has_actionable_drift', False)}`",
    ]) + "\n"


def open_or_update_issue(
    *,
    sig_label: str,
    title: str,
    body: str,
    drift_labels: list[str],
) -> None:
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
        return
    sh(["gh", "issue", "create", "--title", title[:250], "--body-file", str(body_path), "--label", labels_csv])
    print(f"created issue ({sig_label})")


def close_issue(number: str, comment: str) -> None:
    sh(["gh", "issue", "comment", number, "--body", comment])
    subprocess.run(
        ["gh", "issue", "edit", number, "--add-label", "prompt-surface:aligned", "--remove-label", "prompt-surface:prod-drift,prompt-surface:registry-failure"],
        text=True,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    sh(["gh", "issue", "close", number, "--comment", "Closing because the watch signal cleared."])
    print(f"closed issue #{number}")


def drift_labels_for(base: list[str], *, umbrella: bool) -> list[str]:
    if umbrella and LABEL_CLIENT_FIDELITY not in base:
        return [base[0], LABEL_CLIENT_FIDELITY, *base[1:]]
    return base


def cmd_registry_failure(args: argparse.Namespace) -> int:
    body = registry_failure_body(args.run_url)
    open_or_update_issue(
        sig_label=SIG_REGISTRY,
        title="[prompt-surface] registry-gate failed",
        body=body,
        drift_labels=drift_labels_for([
            "prompt-surface-watch", "automated", "needs-triage",
            "prompt-surface:registry-failure", SIG_REGISTRY,
        ], umbrella=args.umbrella),
    )
    return 0


def cmd_registry_recover(args: argparse.Namespace) -> int:
    ensure_base_labels()
    existing = find_open_issue(SIG_REGISTRY)
    if not existing:
        print("no open registry-gate failure issue")
        return 0
    comment = "\n".join([
        "Registry-gate passed on the latest watchdog run.",
        "",
        f"- Watchdog run: {args.run_url or 'n/a'}",
    ]) + "\n"
    close_issue(existing, comment)
    return 0


def cmd_prod_sync(args: argparse.Namespace) -> int:
    report = json.loads(pathlib.Path(args.report_json).read_text(encoding="utf-8"))
    report_md = None
    if args.report_md:
        report_md = pathlib.Path(args.report_md).read_text(encoding="utf-8")
    summary = report.get("summary") or {}
    if summary.get("has_actionable_drift"):
        body = prod_drift_body(report, args.run_url, report_md)
        open_or_update_issue(
            sig_label=SIG_PROD_DRIFT,
            title="[prompt-surface] prod fingerprint actionable drift",
            body=body,
            drift_labels=drift_labels_for([
                "prompt-surface-watch", "automated", "needs-triage",
                "prompt-surface:prod-drift", SIG_PROD_DRIFT,
            ], umbrella=args.umbrella),
        )
        return 0
    ensure_base_labels()
    existing = find_open_issue(SIG_PROD_DRIFT)
    if not existing:
        print("no open prod drift issue")
        return 0
    close_issue(existing, prod_recover_body(args.run_url, report))
    return 0


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="command", required=True)

    p_fail = sub.add_parser("registry-failure")
    p_fail.add_argument("--run-url", default="")
    p_fail.add_argument("--umbrella", action="store_true")

    p_rec = sub.add_parser("registry-recover")
    p_rec.add_argument("--run-url", default="")
    p_rec.add_argument("--umbrella", action="store_true")

    p_prod = sub.add_parser("prod-sync")
    p_prod.add_argument("--report-json", type=pathlib.Path, required=True)
    p_prod.add_argument("--report-md", type=pathlib.Path)
    p_prod.add_argument("--run-url", default="")
    p_prod.add_argument("--umbrella", action="store_true")

    args = ap.parse_args(argv)
    if args.command == "registry-failure":
        return cmd_registry_failure(args)
    if args.command == "registry-recover":
        return cmd_registry_recover(args)
    if args.command == "prod-sync":
        return cmd_prod_sync(args)
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
