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
LABEL_DRIFT = "client-release:drift"  # Forge action trigger (only this)
LABEL_ALIGNED = "client-release:aligned"
LABEL_IN_PROGRESS = "client-release:in-progress"
LABEL_BLOCKED = "client-release:blocked"
LABEL_NEEDS_TRIAGE = "needs-triage"
LABEL_WATCH = "client-release-watch"
LABEL_AUTOMATED = "automated"
UMBRELLA_SKILL = "tokenkey-fingerprint-alignment-all"
BOARD_TITLE = "[client-release] Fingerprint drift board"

# Labels that mean a human/Forge agent already owns the board — do not re-arm drift.
AGENT_STATUS_LABELS = frozenset({LABEL_IN_PROGRESS, LABEL_BLOCKED})

BASE_LABELS = {
    LABEL_WATCH: ("BFD4F2", "Client release watch signal (not a Forge trigger)"),
    LABEL_CLIENT_FIDELITY: ("1D76DB", "Client fidelity umbrella watch (not a Forge trigger)"),
    LABEL_BOARD: ("5319E7", "Single sticky client-release drift board (not a Forge trigger)"),
    LABEL_AUTOMATED: ("C5DEF5", "Automated signal"),
    LABEL_NEEDS_TRIAGE: ("FBCA04", "Blocked / repeated failure — not a permanent label"),
    LABEL_DRIFT: ("D73A4A", "Forge-actionable: upstream client newer than TokenKey pin"),
    LABEL_ALIGNED: ("0E8A16", "All watched clients aligned with pins"),
    LABEL_IN_PROGRESS: ("FBCA04", "Fingerprint alignment in progress; Forge must not duplicate"),
    LABEL_BLOCKED: ("B60205", "Alignment blocked; Forge must not auto-start"),
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


def list_issue_labels(number: str) -> set[str]:
    raw = sh([
        "gh", "issue", "view", number,
        "--json", "labels",
        "--jq", "[.labels[].name]",
    ]).stdout.strip()
    return {str(item) for item in json.loads(raw or "[]")}


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


def platform_evidence_status(item: dict) -> str:
    explicit = item.get("evidence_status")
    if explicit:
        return str(explicit)
    if item.get("fetch_errors"):
        return "fetch-error"
    if item.get("issue_suppressed"):
        return "suppressed"
    if item.get("drift"):
        return "release-metadata-only"
    return "aligned"


def platform_block_reason(item: dict) -> str:
    explicit = item.get("block_reason")
    if explicit:
        return str(explicit)
    errors = item.get("fetch_errors") or []
    if errors:
        return "; ".join(str(err) for err in errors)
    return "—"


def build_board_body(report: dict, *, status_labels: list[str] | None = None) -> str:
    platforms = list(report.get("platforms") or [])
    drift = actionable_drift(platforms)
    aligned = [
        item
        for item in platforms
        if not (item.get("drift") and not item.get("issue_suppressed"))
    ]
    run_url = report.get("run_url") or "n/a"
    status = list(status_labels or [])

    if status:
        forge_line = (
            f"- Active status: {', '.join(f'`{name}`' for name in status)} — "
            f"Forge must **not** auto-start (trigger is only `{LABEL_DRIFT}`)."
        )
    elif drift:
        forge_line = (
            f"- Action label: `{LABEL_DRIFT}` — Forge may auto-start "
            f"`{UMBRELLA_SKILL}` for one PR covering all drifting clients."
        )
    else:
        forge_line = f"- Aligned — `{LABEL_DRIFT}` must be absent so Forge does not start."

    lines = [
        "## Client release fingerprint board",
        "",
        "Single sticky tracking issue for upstream client release drift.",
        "Do **not** open per-client issues for routine bumps.",
        "",
        f"- Watchdog run: {run_url}",
        f"- Drifting: `{len(drift)}`",
        f"- Aligned / other: `{len(aligned)}`",
        "",
        "## Forge trigger",
        "",
        forge_line,
        f"- Forge must only act on `{LABEL_DRIFT}`.",
        "- These labels are **not** Forge start signals: "
        f"`{LABEL_WATCH}`, `{LABEL_BOARD}`, `{LABEL_CLIENT_FIDELITY}`, "
        f"`{LABEL_ALIGNED}`, `{LABEL_NEEDS_TRIAGE}`.",
        "",
        "## Execute",
        "",
        f"- Skill: `{UMBRELLA_SKILL}`",
        "- One task / one PR for all drifting clients — do not split per-client PRs.",
        "",
        "## Platforms",
        "",
        "| Client | Pin | Upstream | Drift | Evidence | Block |",
        "| --- | --- | --- | --- | --- | --- |",
    ]
    for item in platforms:
        is_drift = bool(item.get("drift") and not item.get("issue_suppressed"))
        lines.append(
            "| `{pid}` | `{pin}` | `{up}` | `{drift}` | `{evidence}` | {block} |".format(
                pid=item.get("id") or "?",
                pin=item.get("pinned") or "n/a",
                up=item.get("upstream_latest") or "unknown",
                drift="yes" if is_drift else "no",
                evidence=platform_evidence_status(item),
                block=platform_block_reason(item),
            )
        )

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
            "## When NOT to open a PR",
            "",
            "1. Release metadata alone is **not** enough to bump pins.",
            "2. Require real CLI / on-wire fingerprint evidence before changing pins.",
            "3. If the local CLI / capture prerequisites are missing → mark "
            f"`{LABEL_BLOCKED}` (optional `{LABEL_NEEDS_TRIAGE}`); do not open a PR.",
            "4. If capture shows no actual fingerprint drift → do not open a PR "
            "(version-only / no-op).",
            "5. Multiple clients drifting → one PR via "
            f"`{UMBRELLA_SKILL}`, not one PR per client.",
            "",
            "## Label lifecycle",
            "",
            f"- Any actionable drift + no agent status → `{LABEL_DRIFT}` "
            f"(remove `{LABEL_ALIGNED}` / `{LABEL_NEEDS_TRIAGE}`).",
            f"- Agent start → `{LABEL_IN_PROGRESS}` and remove `{LABEL_DRIFT}` / "
            f"`{LABEL_NEEDS_TRIAGE}`.",
            f"- Blocked / repeated failure → `{LABEL_BLOCKED}` "
            f"(+ `{LABEL_NEEDS_TRIAGE}` only while blocked).",
            f"- All aligned → `{LABEL_ALIGNED}`, remove `{LABEL_DRIFT}` / "
            f"`{LABEL_NEEDS_TRIAGE}` / agent status, close board.",
            "",
            "## Expected follow-up",
            "",
            "1. `bash ops/fingerprint/client-release-watch.sh plan`",
            f"2. Load `{UMBRELLA_SKILL}` in Cursor",
            "3. Capture on-wire evidence — never bump from release metadata alone",
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


def identity_labels(*, umbrella: bool) -> list[str]:
    labels = [LABEL_WATCH, LABEL_BOARD, LABEL_AUTOMATED]
    if umbrella:
        labels.append(LABEL_CLIENT_FIDELITY)
    return labels


def converge_board_labels(
    number: str,
    *,
    umbrella: bool,
    drifting: bool,
    current: set[str],
) -> set[str]:
    """Apply Forge-safe label convergence; return resulting label set."""
    labels = set(current)
    for name in identity_labels(umbrella=umbrella):
        labels.add(name)
        sh(["gh", "issue", "edit", number, "--add-label", name], check=False)

    agent_status = sorted(labels & AGENT_STATUS_LABELS)
    if drifting:
        sh(["gh", "issue", "edit", number, "--remove-label", LABEL_ALIGNED], check=False)
        labels.discard(LABEL_ALIGNED)
        if LABEL_BLOCKED in labels:
            sh(["gh", "issue", "edit", number, "--add-label", LABEL_NEEDS_TRIAGE], check=False)
            sh(["gh", "issue", "edit", number, "--remove-label", LABEL_DRIFT], check=False)
            labels.add(LABEL_NEEDS_TRIAGE)
            labels.discard(LABEL_DRIFT)
        elif LABEL_IN_PROGRESS in labels:
            for name in (LABEL_DRIFT, LABEL_NEEDS_TRIAGE):
                sh(["gh", "issue", "edit", number, "--remove-label", name], check=False)
                labels.discard(name)
        else:
            sh(["gh", "issue", "edit", number, "--add-label", LABEL_DRIFT], check=False)
            sh(["gh", "issue", "edit", number, "--remove-label", LABEL_NEEDS_TRIAGE], check=False)
            labels.add(LABEL_DRIFT)
            labels.discard(LABEL_NEEDS_TRIAGE)
    else:
        sh(["gh", "issue", "edit", number, "--add-label", LABEL_ALIGNED], check=False)
        labels.add(LABEL_ALIGNED)
        for name in (LABEL_DRIFT, LABEL_NEEDS_TRIAGE, LABEL_IN_PROGRESS, LABEL_BLOCKED):
            sh(["gh", "issue", "edit", number, "--remove-label", name], check=False)
            labels.discard(name)
    _ = agent_status
    return labels


_RESERVED_CLIENT_RELEASE_LABELS = {
    LABEL_DRIFT,
    LABEL_ALIGNED,
    LABEL_IN_PROGRESS,
    LABEL_BLOCKED,
    LABEL_BOARD,
}


def _is_automated_legacy_drift_issue(labels: set[str]) -> bool:
    """True for old bot per-platform/per-signature tickets; false for human exceptions."""
    if LABEL_BOARD in labels:
        return False
    if LABEL_AUTOMATED not in labels:
        return False
    if LABEL_WATCH not in labels and LABEL_CLIENT_FIDELITY not in labels:
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
        "--label", LABEL_DRIFT,
        "--json", "number,labels,title",
        "--limit", "100",
    ]).stdout.strip()
    if not existing_raw or existing_raw == "[]":
        return
    board_line = (
        f"- Canonical board: #{board_number}"
        if board_number
        else "- Canonical board: pending create on next drift scan"
    )
    comment = "\n".join([
        "Superseded by the single sticky client-release drift board.",
        "",
        board_line,
        f"- Watchdog run: {run_url}",
        "",
        f"Routine upstream bumps are tracked on the board; work unit is `{UMBRELLA_SKILL}`.",
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
    board_number, board_state = find_board_issue()
    current_labels: set[str] = set()
    if board_number and board_state == "OPEN":
        current_labels = list_issue_labels(board_number)
    status = sorted(current_labels & AGENT_STATUS_LABELS)

    body = build_board_body(report, status_labels=status)
    body_path = board_body_path(cache_dir)
    body_path.parent.mkdir(parents=True, exist_ok=True)
    body_path.write_text(body, encoding="utf-8")

    title = board_title(len(drift))
    links: list[dict[str, object]] = []

    if drift:
        create_labels = identity_labels(umbrella=umbrella) + [LABEL_DRIFT]
        if board_number and board_state == "OPEN":
            sh(["gh", "issue", "edit", board_number, "--title", title, "--body-file", str(body_path)])
            current_labels = converge_board_labels(
                board_number,
                umbrella=umbrella,
                drifting=True,
                current=current_labels,
            )
            status_code = "updated"
            print(f"updated drift board #{board_number}")
        elif board_number:
            sh(["gh", "issue", "reopen", board_number])
            sh(["gh", "issue", "edit", board_number, "--title", title, "--body-file", str(body_path)])
            current_labels = converge_board_labels(
                board_number,
                umbrella=umbrella,
                drifting=True,
                current=list_issue_labels(board_number),
            )
            status_code = "reopened"
            print(f"reopened drift board #{board_number}")
        else:
            created_url = sh([
                "gh", "issue", "create",
                "--title", title,
                "--body-file", str(body_path),
                "--label", ",".join(create_labels),
            ]).stdout.strip()
            number_match = re.search(r"/issues/(\d+)(?:$|[?#])", created_url)
            board_number = number_match.group(1) if number_match else ""
            status_code = "created"
            print(f"created drift board #{board_number or '?'}")
            if board_number:
                current_labels = converge_board_labels(
                    board_number,
                    umbrella=umbrella,
                    drifting=True,
                    current=set(create_labels),
                )

        if board_number:
            close_legacy_per_platform_issues(board_number=board_number, run_url=run_url)
            url = issue_url(board_number)
            forge_armed = LABEL_DRIFT in current_labels
            for item in drift:
                links.append({
                    "kind": "issue",
                    "signal_type": "release-drift",
                    "platform_id": item.get("id"),
                    "title": title,
                    "number": int(board_number),
                    "url": url,
                    "status": status_code,
                    "board": True,
                    "forge_actionable": forge_armed,
                })
        return links

    if board_number and board_state == "OPEN":
        sh(["gh", "issue", "edit", board_number, "--title", title, "--body-file", str(body_path)])
        converge_board_labels(
            board_number,
            umbrella=umbrella,
            drifting=False,
            current=current_labels,
        )
        sh([
            "gh", "issue", "close", board_number,
            "--comment", "Closing because all watched client platforms are aligned (or suppressed).",
        ])
        print(f"closed aligned drift board #{board_number}")

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
