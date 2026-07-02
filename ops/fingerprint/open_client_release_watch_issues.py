#!/usr/bin/env python3
"""Open/update/close GitHub issues for client-release-watch drift signals."""
from __future__ import annotations

import argparse
import json
import pathlib
import re
import subprocess
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
DEFAULT_REPORT = REPO_ROOT / ".cache/fingerprint/client-release-watch/report.json"
DEFAULT_CACHE_DIR = REPO_ROOT / ".cache/fingerprint/client-release-watch"

LABEL_CLIENT_FIDELITY = "client-fidelity-watch"

BASE_LABELS = {
    "client-release-watch": ("BFD4F2", "Client release watch signal"),
    LABEL_CLIENT_FIDELITY: ("1D76DB", "Client fidelity umbrella watch"),
    "automated": ("C5DEF5", "Automated signal"),
    "needs-triage": ("FBCA04", "Needs human triage"),
    "client-release:drift": ("D73A4A", "Upstream client newer than TokenKey pin"),
    "client-release:aligned": ("0E8A16", "Client release aligned with pin"),
}


def label_safe(value: str) -> str:
    return re.sub(r"[^A-Za-z0-9_.:-]+", "-", value)[:50] or "unknown"


def filename_safe(value: str) -> str:
    """Filesystem-safe slug (no colons — artifact upload rejects them)."""
    return re.sub(r"[^A-Za-z0-9_.-]+", "-", value)[:80] or "unknown"


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


def issue_body_path(cache_dir: pathlib.Path, platform_id: str) -> pathlib.Path:
    return cache_dir / f"issue-{filename_safe(platform_id)}.md"


def issue_url(number: str) -> str:
    return sh(["gh", "issue", "view", number, "--json", "url", "--jq", ".url"]).stdout.strip()


def sync_issues(report: dict, *, cache_dir: pathlib.Path, umbrella: bool) -> list[dict[str, object]]:
    ensure_base_labels()
    links: list[dict[str, object]] = []
    for item in report.get("platforms") or []:
        platform_id = item["id"]
        platform_label = f"client-release:{label_safe(platform_id)}"
        ensure_label(platform_label, "BFD4F2", f"Client release watch platform {platform_id}")

        upstream = item.get("upstream_latest") or "unknown"
        issue_signature = f"client-release-{platform_id}-{upstream}"
        sig = f"{platform_id}-{upstream}"
        sig_label = label_safe(f"cr-sig:{sig}")
        ensure_label(sig_label, "BFD4F2", f"Client release watch signature {sig}")

        title = (
            f"[client-release] {item.get('name')} upstream {upstream} > pin {item.get('pinned') or 'n/a'}"
        )[:250]
        body_lines = [
            "## Client release watch finding",
            "",
            f"- Signal type: `release-drift`",
            f"- Platform: `{platform_id}`",
            f"- Pinned: `{item.get('pinned')}`",
            f"- Upstream latest: `{upstream}`",
            f"- Status: `{item.get('status')}`",
            f"- Pin path: `{item.get('pin_path')}`",
            f"- Alignment skill: `{item.get('skill')}`",
            f"- Watchdog run: {report.get('run_url') or 'n/a'}",
            f"- Signature: `{issue_signature}`",
            "",
            "## Upstream sources",
            "",
        ]
        for label, info in (item.get("upstream_sources") or {}).items():
            body_lines.append(f"- {label}: `{info.get('version')}` — {info.get('url')}")
        if item.get("fetch_errors"):
            body_lines.extend(["", "## Fetch errors", ""])
            body_lines.extend(f"- {err}" for err in item["fetch_errors"])
        body_lines.extend([
            "",
            "## Expected follow-up",
            "",
            "1. Local routing: `bash ops/fingerprint/client-release-watch.sh plan`",
            f"2. Load skill in Cursor: `{item.get('skill')}` (or `tokenkey-fingerprint-alignment-all` for one PR)",
            "3. Run that skill's capture commands — do not bump pins from release metadata alone.",
        ])
        body_path = issue_body_path(cache_dir, platform_id)
        body_path.parent.mkdir(parents=True, exist_ok=True)
        body_path.write_text("\n".join(body_lines) + "\n", encoding="utf-8")

        if item.get("drift") and not item.get("issue_suppressed"):
            labels = [
                "client-release-watch",
                LABEL_CLIENT_FIDELITY,
                "automated",
                "needs-triage",
                "client-release:drift",
                platform_label,
                sig_label,
            ]
            if not umbrella:
                labels = [lbl for lbl in labels if lbl != LABEL_CLIENT_FIDELITY]
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
                    "signal_type": "release-drift",
                    "platform_id": platform_id,
                    "title": title,
                    "number": int(existing),
                    "url": issue_url(existing),
                    "status": "updated",
                })
                print(f"updated drift issue #{existing} for {platform_id}")
            else:
                created_url = sh([
                    "gh", "issue", "create", "--title", title, "--body-file", str(body_path), "--label", labels_csv,
                ]).stdout.strip()
                number_match = re.search(r"/issues/(\d+)(?:$|[?#])", created_url)
                links.append({
                    "kind": "issue",
                    "signal_type": "release-drift",
                    "platform_id": platform_id,
                    "title": title,
                    "number": int(number_match.group(1)) if number_match else None,
                    "url": created_url,
                    "status": "created",
                })
                print(f"created drift issue for {platform_id}")
            continue

        existing_raw = sh([
            "gh", "issue", "list", "--state", "open", "--label", platform_label,
            "--json", "number,labels", "--limit", "20",
        ]).stdout.strip()
        if not existing_raw or existing_raw == "[]":
            continue
        for row in json.loads(existing_raw):
            number = str(row["number"])
            labels = {lbl["name"] for lbl in row.get("labels") or []}
            if "client-release:drift" not in labels:
                continue
            comment = "\n".join([
                "Watchdog now reports this platform as aligned with upstream (or not ahead of the pin).",
                "",
                f"- Platform: `{platform_id}`",
                f"- Pinned: `{item.get('pinned')}`",
                f"- Upstream latest: `{upstream}`",
                f"- Watchdog run: {report.get('run_url') or 'n/a'}",
            ]) + "\n"
            sh(["gh", "issue", "comment", number, "--body", comment])
            subprocess.run([
                "gh", "issue", "edit", number,
                "--add-label", "client-release:aligned",
                "--remove-label", "client-release:drift",
            ], text=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            sh(["gh", "issue", "close", number, "--comment", "Closing because upstream is no longer ahead of the TokenKey pin."])
            print(f"closed drift issue #{number} for {platform_id}")
    return links


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--report-json", type=pathlib.Path, default=DEFAULT_REPORT)
    ap.add_argument("--cache-dir", type=pathlib.Path, default=DEFAULT_CACHE_DIR)
    ap.add_argument(
        "--umbrella",
        action="store_true",
        help="Tag issues with client-fidelity-watch (umbrella workflow)",
    )
    ap.add_argument(
        "--links-json",
        type=pathlib.Path,
        help="Write opened/updated issue links for downstream daily reports",
    )
    args = ap.parse_args(argv)
    if not args.report_json.is_file():
        print(f"missing report: {args.report_json}", file=sys.stderr)
        return 2
    report = json.loads(args.report_json.read_text(encoding="utf-8"))
    links = sync_issues(report, cache_dir=args.cache_dir, umbrella=args.umbrella)
    links_json = args.links_json or (args.cache_dir / "links.json")
    links_json.parent.mkdir(parents=True, exist_ok=True)
    links_json.write_text(json.dumps({"links": links}, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
