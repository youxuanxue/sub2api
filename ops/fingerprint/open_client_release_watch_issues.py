#!/usr/bin/env python3
"""Open/update/close one sticky GitHub board for client-release-watch drift."""
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
LABEL_BOARD = "client-release:board"
BOARD_TITLE = "[client-release] Fingerprint drift board"

BASE_LABELS = {
    "client-release-watch": ("BFD4F2", "Client release watch signal"),
    LABEL_CLIENT_FIDELITY: ("1D76DB", "Client fidelity umbrella watch"),
    LABEL_BOARD: ("5319E7", "Single sticky client-release drift board"),
    "automated": ("C5DEF5", "Automated signal"),
    "needs-triage": ("FBCA04", "Needs human triage"),
    "client-release:drift": ("D73A4A", "Upstream client newer than TokenKey pin"),
    "client-release:aligned": ("0E8A16", "Client release aligned with pin"),
}


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


def board_body_path(cache_dir: pathlib.Path) -> pathlib.Path:
    return cache_dir / "issue-board.md"


def issue_body_path(cache_dir: pathlib.Path, platform_id: str = "board") -> pathlib.Path:
    """Compatibility alias — board mode writes a single body file."""
    if platform_id == "board":
        return board_body_path(cache_dir)
    return cache_dir / f"issue-{filename_safe(platform_id)}.md"


def issue_url(number: str) -> str:
    return sh(["gh", "issue", "view", number, "--json", "url", "--jq", ".url"]).stdout.strip()


def actionable_drift(platforms: list[dict]) -> list[dict]:
    return [
        item
        for item in platforms
        if item.get("drift") and not item.get("issue_suppressed")
    ]


def board_title(drift_count: int) -> str:
    if drift_count <= 0:
        return f"{BOARD_TITLE} (aligned)"[:250]
    return f"{BOARD_TITLE} ({drift_count} drifting)"[:250]


def build_board_body(report: dict) -> str:
    platforms = list(report.get("platforms") or [])
    drift = actionable_drift(platforms)
    aligned = [
        item
        for item in platforms
        if not (item.get("drift") and not item.get("issue_suppressed"))
    ]
    run_url = report.get("run_url") or "n/a"
    lines = [
        "## Client release fingerprint board",
        "",
        "Single sticky tracking issue for upstream client release drift.",
        "Work unit is a fingerprint-alignment PR — do not open per-client issues for routine bumps.",
        "",
        f"- Watchdog run: {run_url}",
        f"- Drifting: `{len(drift)}`",
        f"- Aligned / other: `{len(aligned)}`",
        "",
        "## Current drift",
        "",
    ]
    if drift:
        lines.append("| Platform | Pin | Upstream | Skill | Sources |")
        lines.append("| --- | --- | --- | --- | --- |")
        for item in drift:
            sources = "; ".join(
                f"{label} `{info.get('version')}`"
                for label, info in (item.get("upstream_sources") or {}).items()
            ) or "n/a"
            lines.append(
                "| `{pid}` | `{pin}` | `{up}` | `{skill}` | {sources} |".format(
                    pid=item.get("id") or "?",
                    pin=item.get("pinned") or "n/a",
                    up=item.get("upstream_latest") or "unknown",
                    skill=item.get("skill") or "n/a",
                    sources=sources,
                )
            )
    else:
        lines.append("_None — all watched platforms are aligned (or suppressed)._")
    lines.extend(["", "## Aligned / non-actionable", ""])
    if aligned:
        lines.append("| Platform | Pin | Upstream | Status |")
        lines.append("| --- | --- | --- | --- |")
        for item in aligned:
            lines.append(
                "| `{pid}` | `{pin}` | `{up}` | `{status}` |".format(
                    pid=item.get("id") or "?",
                    pin=item.get("pinned") or "n/a",
                    up=item.get("upstream_latest") or "unknown",
                    status=item.get("status") or ("suppressed" if item.get("issue_suppressed") else "aligned"),
                )
            )
    else:
        lines.append("_None._")

    fetch_errors = [
        (item.get("id"), err)
        for item in platforms
        for err in (item.get("fetch_errors") or [])
    ]
    if fetch_errors:
        lines.extend(["", "## Fetch errors", ""])
        for platform_id, err in fetch_errors:
            lines.append(f"- `{platform_id}`: {err}")

    lines.extend(
        [
            "",
            "## Expected follow-up",
            "",
            "1. Local routing: `bash ops/fingerprint/client-release-watch.sh plan`",
            "2. Load the listed skill(s), or `tokenkey-fingerprint-alignment-all` for one PR",
            "3. Capture on-wire evidence — do not bump pins from release metadata alone",
            "4. Open a dedicated issue only when a platform is blocked / multi-day / needs discussion",
            "   (do **not** add the `automated` label — the watchdog only auto-closes automated legacy tickets)",
        ]
    )
    return "\n".join(lines) + "\n"


def find_board_issue() -> tuple[str | None, str | None]:
    """Return (number, state) for the sticky board, preferring an open issue."""
    raw = sh([
        "gh", "issue", "list",
        "--state", "all",
        "--label", LABEL_BOARD,
        "--json", "number,state,updatedAt",
        "--limit", "20",
    ]).stdout.strip()
    if not raw or raw == "[]":
        return None, None
    rows = json.loads(raw)
    open_rows = [row for row in rows if row.get("state") == "OPEN"]
    if open_rows:
        open_rows.sort(key=lambda row: row.get("updatedAt") or "", reverse=True)
        return str(open_rows[0]["number"]), "OPEN"
    rows.sort(key=lambda row: row.get("updatedAt") or "", reverse=True)
    return str(rows[0]["number"]), str(rows[0].get("state") or "CLOSED")


def board_labels(*, umbrella: bool, drifting: bool) -> list[str]:
    labels = [
        "client-release-watch",
        LABEL_BOARD,
        "automated",
    ]
    if umbrella:
        labels.append(LABEL_CLIENT_FIDELITY)
    if drifting:
        labels.extend(["needs-triage", "client-release:drift"])
    else:
        labels.append("client-release:aligned")
    return labels


_RESERVED_CLIENT_RELEASE_LABELS = {
    "client-release:drift",
    "client-release:aligned",
    LABEL_BOARD,
}


def _is_automated_legacy_drift_issue(labels: set[str]) -> bool:
    """True for old bot per-platform/per-signature tickets; false for human exceptions."""
    if LABEL_BOARD in labels:
        return False
    if "automated" not in labels:
        return False
    if "client-release-watch" not in labels and "client-fidelity-watch" not in labels:
        return False
    has_cr_sig = any(name.startswith("cr-sig:") for name in labels)
    has_platform = any(
        name.startswith("client-release:") and name not in _RESERVED_CLIENT_RELEASE_LABELS
        for name in labels
    )
    return has_cr_sig or has_platform


def close_legacy_per_platform_issues(*, board_number: str | None, run_url: str) -> None:
    """Close open automated per-platform drift tickets superseded by the sticky board."""
    existing_raw = sh([
        "gh", "issue", "list",
        "--state", "open",
        "--label", "client-release:drift",
        "--json", "number,labels,title",
        "--limit", "100",
    ]).stdout.strip()
    if not existing_raw or existing_raw == "[]":
        return
    board_line = f"- Canonical board: #{board_number}" if board_number else "- Canonical board: pending create on next drift scan"
    comment = "\n".join([
        "Superseded by the single sticky client-release drift board.",
        "",
        board_line,
        f"- Watchdog run: {run_url}",
        "",
        "Routine upstream bumps are tracked on the board; work unit is the alignment PR.",
    ]) + "\n"
    for row in json.loads(existing_raw):
        number = str(row["number"])
        if board_number and number == board_number:
            continue
        labels = {lbl["name"] for lbl in row.get("labels") or []}
        if not _is_automated_legacy_drift_issue(labels):
            continue
        sh(["gh", "issue", "close", number, "--comment", comment])
        print(f"closed legacy per-platform drift issue #{number}")


def sync_issues(report: dict, *, cache_dir: pathlib.Path, umbrella: bool) -> list[dict[str, object]]:
    ensure_base_labels()
    platforms = list(report.get("platforms") or [])
    drift = actionable_drift(platforms)
    run_url = report.get("run_url") or "n/a"
    body = build_board_body(report)
    body_path = board_body_path(cache_dir)
    body_path.parent.mkdir(parents=True, exist_ok=True)
    body_path.write_text(body, encoding="utf-8")

    title = board_title(len(drift))
    labels = board_labels(umbrella=umbrella, drifting=bool(drift))
    labels_csv = ",".join(labels)
    board_number, board_state = find_board_issue()
    links: list[dict[str, object]] = []

    if drift:
        if board_number and board_state == "OPEN":
            sh(["gh", "issue", "edit", board_number, "--title", title, "--body-file", str(body_path)])
            sh(["gh", "issue", "edit", board_number, "--add-label", labels_csv])
            subprocess.run(
                ["gh", "issue", "edit", board_number, "--remove-label", "client-release:aligned"],
                text=True,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
            )
            status = "updated"
            print(f"updated drift board #{board_number}")
        elif board_number:
            sh(["gh", "issue", "reopen", board_number])
            sh(["gh", "issue", "edit", board_number, "--title", title, "--body-file", str(body_path)])
            sh(["gh", "issue", "edit", board_number, "--add-label", labels_csv])
            subprocess.run(
                ["gh", "issue", "edit", board_number, "--remove-label", "client-release:aligned"],
                text=True,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
            )
            status = "reopened"
            print(f"reopened drift board #{board_number}")
        else:
            created_url = sh([
                "gh", "issue", "create",
                "--title", title,
                "--body-file", str(body_path),
                "--label", labels_csv,
            ]).stdout.strip()
            number_match = re.search(r"/issues/(\d+)(?:$|[?#])", created_url)
            board_number = number_match.group(1) if number_match else ""
            status = "created"
            print(f"created drift board #{board_number or '?'}")

        if board_number:
            close_legacy_per_platform_issues(board_number=board_number, run_url=run_url)
            url = issue_url(board_number)
            for item in drift:
                links.append({
                    "kind": "issue",
                    "signal_type": "release-drift",
                    "platform_id": item.get("id"),
                    "title": title,
                    "number": int(board_number),
                    "url": url,
                    "status": status,
                    "board": True,
                })
        return links

    if board_number and board_state == "OPEN":
        sh(["gh", "issue", "edit", board_number, "--title", title, "--body-file", str(body_path)])
        sh(["gh", "issue", "edit", board_number, "--add-label", labels_csv])
        subprocess.run(
            [
                "gh", "issue", "edit", board_number,
                "--remove-label", "client-release:drift",
                "--remove-label", "needs-triage",
            ],
            text=True,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        sh([
            "gh", "issue", "close", board_number,
            "--comment", "Closing because all watched client platforms are aligned (or suppressed).",
        ])
        print(f"closed aligned drift board #{board_number}")

    # Still retire any leftover per-platform tickets even when already aligned.
    close_legacy_per_platform_issues(board_number=board_number, run_url=run_url)
    return links


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--report-json", type=pathlib.Path, default=DEFAULT_REPORT)
    ap.add_argument("--cache-dir", type=pathlib.Path, default=DEFAULT_CACHE_DIR)
    ap.add_argument(
        "--umbrella",
        action="store_true",
        help="Tag the board with client-fidelity-watch (umbrella workflow)",
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
