#!/usr/bin/env python3
"""Open/update/close GitHub issues for model-release-watch findings."""
from __future__ import annotations

import argparse
import json
import pathlib
import re
import subprocess
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
DEFAULT_REPORT = REPO_ROOT / ".cache/model-release-watch/report.json"
DEFAULT_CACHE_DIR = REPO_ROOT / ".cache/model-release-watch"

LABEL_UMBRELLA = "model-surface-watch"

BASE_LABELS = {
    "model-release-watch": ("BFD4F2", "Model release watch signal"),
    LABEL_UMBRELLA: ("1D76DB", "Model surface umbrella watch"),
    "automated": ("C5DEF5", "Automated signal"),
    "needs-triage": ("FBCA04", "Needs human triage"),
    "model-release:missing": ("D73A4A", "Upstream model has no explicit TokenKey supply key"),
    "model-release:unpriced": ("E99695", "Supply key exists but price owner missing"),
    "model-release:narrow": ("F9D0C4", "Served only on non-primary supply surfaces"),
    "model-release:resolved": ("0E8A16", "Model release finding resolved"),
}

STATUS_LABEL = {
    "missing": "model-release:missing",
    "unpriced": "model-release:unpriced",
    "narrow": "model-release:narrow",
}

ACTIONABLE = frozenset(STATUS_LABEL)


def label_safe(value: str) -> str:
    return re.sub(r"[^A-Za-z0-9_.:-]+", "-", value)[:50] or "unknown"


def filename_safe(value: str) -> str:
    return re.sub(r"[^A-Za-z0-9_.-]+", "-", value)[:80] or "unknown"


def sh(args: list[str], *, check: bool = True) -> subprocess.completedProcess[str]:
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


def issue_body_path(cache_dir: pathlib.Path, vendor: str, model_id: str) -> pathlib.Path:
    return cache_dir / f"issue-{filename_safe(vendor)}-{filename_safe(model_id)}.md"


def issue_url(number: str) -> str:
    return sh(["gh", "issue", "view", number, "--json", "url", "--jq", ".url"]).stdout.strip()


def build_body(item: dict, report: dict) -> str:
    status = item["status"]
    lines = [
        "## Model release watch finding",
        "",
        f"- Status: `{status}`",
        f"- Vendor: `{item['vendor']}`",
        f"- Model ID: `{item['model_id']}`",
        f"- Priced: `{item.get('priced')}`",
        f"- Primary coverage: `{item.get('primary_coverage')}`",
        f"- Surfaces: `{', '.join(item.get('surfaces') or []) or 'none'}`",
        f"- Upstream source: {item.get('source_url') or 'n/a'}",
        f"- Watchdog run: {report.get('run_url') or 'n/a'}",
        f"- Signature: `model-release-{item['vendor']}-{item['model_id']}-{status}`",
        "",
        "## Notes",
        "",
        item.get("notes") or "_None._",
        "",
        "## Expected follow-up",
        "",
        "1. Read-only plan: `python3 ops/pricing/modelops.py plan` (or hub skill `tokenkey-modelops-planner`)",
        "2. Choose supply channel (kiro / cursor / tokensea / cloudwise / bedrock / native / curated)",
        "3. Onboard/activate via `tokenkey-onboard-model` or catalog refresh — do not edit allowlists from announcement text alone.",
        "4. Wildcard supplier mappings do not count as served.",
    ]
    return "\n".join(lines) + "\n"


def close_resolved_issues(report: dict) -> None:
    """Close open actionable issues whose signature is no longer actionable."""
    actionable_sigs = {
        label_safe(f"mr-sig:{f['vendor']}-{f['model_id']}-{f['status']}")
        for f in report.get("findings") or []
        if f.get("status") in ACTIONABLE
    }
    raw = sh([
        "gh", "issue", "list", "--state", "open", "--label", "model-release-watch",
        "--json", "number,labels", "--limit", "100",
    ]).stdout.strip()
    if not raw or raw == "[]":
        return
    for row in json.loads(raw):
        labels = {lbl["name"] for lbl in row.get("labels") or []}
        sig_labels = {lbl for lbl in labels if lbl.startswith("mr-sig:")}
        if not sig_labels:
            continue
        if sig_labels & actionable_sigs:
            continue
        if not (labels & set(STATUS_LABEL.values())):
            continue
        number = str(row["number"])
        comment = "\n".join([
            "Model-release watchdog no longer reports this signature as actionable.",
            "",
            f"- Watchdog run: {report.get('run_url') or 'n/a'}",
        ]) + "\n"
        sh(["gh", "issue", "comment", number, "--body", comment])
        subprocess.run(
            [
                "gh", "issue", "edit", number,
                "--add-label", "model-release:resolved",
                "--remove-label", ",".join(sorted(labels & set(STATUS_LABEL.values()))),
            ],
            text=True,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        sh(["gh", "issue", "close", number, "--comment", "Closing because the model-release finding is resolved or superseded."])
        print(f"closed resolved issue #{number}")


def sync_issues(report: dict, *, cache_dir: pathlib.Path, umbrella: bool) -> list[dict[str, object]]:
    ensure_base_labels()
    links: list[dict[str, object]] = []
    cache_dir.mkdir(parents=True, exist_ok=True)

    for item in report.get("findings") or []:
        status = item.get("status")
        if status not in ACTIONABLE:
            continue
        # Prefer explicit actionable flag from narrowed watch report.
        if "actionable" in item and not item.get("actionable"):
            continue
        if item.get("issue_suppressed") or item.get("in_scope") is False:
            continue
        vendor = item["vendor"]
        model_id = item["model_id"]
        vendor_label = f"model-release:{label_safe(vendor)}"
        ensure_label(vendor_label, "BFD4F2", f"Model release watch vendor {vendor}")
        status_label = STATUS_LABEL[status]
        sig = f"{vendor}-{model_id}-{status}"
        sig_label = label_safe(f"mr-sig:{sig}")
        ensure_label(sig_label, "BFD4F2", f"Model release watch signature {sig}"[:100])

        title = f"[model-release] {vendor} {model_id} {status}"[:250]
        body = build_body(item, report)
        body_path = issue_body_path(cache_dir, vendor, model_id)
        body_path.write_text(body, encoding="utf-8")

        labels = [
            "model-release-watch",
            "automated",
            "needs-triage",
            status_label,
            vendor_label,
            sig_label,
        ]
        if umbrella:
            labels.append(LABEL_UMBRELLA)
        labels_csv = ",".join(labels)

        existing = sh([
            "gh", "issue", "list", "--state", "open", "--label", sig_label,
            "--json", "number", "--limit", "1", "--jq", ".[0].number // empty",
        ]).stdout.strip()
        if existing:
            sh(["gh", "issue", "comment", existing, "--body-file", str(body_path)])
            sh(["gh", "issue", "edit", existing, "--add-label", labels_csv])
            links.append({
                "kind": "issue",
                "status": "updated",
                "vendor": vendor,
                "model_id": model_id,
                "finding_status": status,
                "number": int(existing),
                "url": issue_url(existing),
                "title": title,
            })
            print(f"updated issue #{existing} for {vendor}/{model_id}")
        else:
            created_url = sh([
                "gh", "issue", "create",
                "--title", title,
                "--body-file", str(body_path),
                "--label", labels_csv,
            ]).stdout.strip()
            number_match = re.search(r"/issues/(\d+)(?:$|[?#])", created_url)
            number = int(number_match.group(1)) if number_match else None
            links.append({
                "kind": "issue",
                "status": "created",
                "vendor": vendor,
                "model_id": model_id,
                "finding_status": status,
                "number": number,
                "url": created_url,
                "title": title,
            })
            print(f"created issue for {vendor}/{model_id}")

    close_resolved_issues(report)
    return links


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--report-json", type=pathlib.Path, default=DEFAULT_REPORT)
    ap.add_argument("--cache-dir", type=pathlib.Path, default=DEFAULT_CACHE_DIR)
    ap.add_argument("--umbrella", action="store_true")
    ap.add_argument("--links-json", type=pathlib.Path)
    ap.add_argument("--dry-run", action="store_true", help="Print bodies only; do not call gh")
    args = ap.parse_args(argv)
    if not args.report_json.is_file():
        print(f"missing report: {args.report_json}", file=sys.stderr)
        return 2
    report = json.loads(args.report_json.read_text(encoding="utf-8"))
    if args.dry_run:
        for item in report.get("findings") or []:
            if item.get("status") not in ACTIONABLE:
                continue
            if "actionable" in item and not item.get("actionable"):
                continue
            if item.get("issue_suppressed") or item.get("in_scope") is False:
                continue
            print(build_body(item, report))
            print("---")
        return 0
    links = sync_issues(report, cache_dir=args.cache_dir, umbrella=args.umbrella)
    links_json = args.links_json or (args.cache_dir / "links.json")
    links_json.parent.mkdir(parents=True, exist_ok=True)
    links_json.write_text(json.dumps({"links": links}, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
