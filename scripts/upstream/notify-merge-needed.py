#!/usr/bin/env python3
"""Open or refresh a human-facing issue when TokenKey is behind upstream/main.

CI entry:  python3 scripts/upstream/notify-merge-needed.py apply ...
Local:     python3 scripts/upstream/notify-merge-needed.py --selftest

This tool never merges, never opens a PR, and never runs an agent.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
import tempfile
from pathlib import Path
from typing import Any, Protocol

LABEL = "upstream-merge-needed"
LEGACY_LABEL = "upstream-merge-agent"
LEGACY_LABELS = (LEGACY_LABEL, "upstream-drift")
AUTOMATED_LABEL = "automated"
LABEL_BLOCKED = "upstream-merge-blocked"
LABEL_IN_PROGRESS = "upstream-merge-in-progress"
LABEL_READY = "upstream-merge-ready"
STATUS_LABELS = (LABEL_BLOCKED, LABEL_IN_PROGRESS, LABEL_READY)
TITLE_PREFIX = "[upstream-merge]"
COMMIT_LIMIT = 30
LABEL_SPECS = (
    (LABEL, "D93F0B", "Forge-actionable: TokenKey is behind Wei-Shaw/sub2api"),
    (AUTOMATED_LABEL, "C5DEF5", "Automated signal"),
    (LABEL_BLOCKED, "B60205", "Upstream merge blocked; Forge must not auto-start"),
    (LABEL_IN_PROGRESS, "FBCA04", "Upstream merge in progress; Forge must not duplicate"),
    (LABEL_READY, "0E8A16", "Upstream merge PR ready for human review/merge"),
)
# Path substring → TokenKey risk bucket for conflict classification.
RISK_PATH_RULES: tuple[tuple[str, tuple[str, ...]], ...] = (
    ("billing", ("billing", "usage", "pricing", "quota", "redeem", "invoice")),
    ("auth", ("auth", "oauth", "jwt", "session", "login", "apikey", "api_key")),
    ("routing", ("gateway", "relay", "protocol", "router", "dispatch", "converter")),
    ("accounts", ("/account", "accounts", "subscription")),
    ("concurrency", ("concurren", "ratelimit", "rate_limit", "sticky", "scheduler", "saturation")),
    ("sentinel", ("scripts/sentinels", "_tk_", ".tk.", "upstream-override")),
    ("frontend", ("frontend/",)),
    ("backend", ("backend/",)),
    ("deploy", ("deploy/", "stage0", ".github/workflows/release", "goreleaser")),
)
WORKFLOW_RELPATH = ".github/workflows/upstream-merge-notify.yml"
FORBIDDEN_WORKFLOW_NEEDLES = (
    "run-headless-agent",
    "ANTHROPIC_AUTH_TOKEN",
    "git merge --no-ff",
    "gh workflow run",
    "claude -p",
    "git remote set-url origin",
    "merge-state.sh",
    # Private-repo origin fetch needs the checkout token. The old daily
    # agent rewrote origin with a PAT after persist-credentials: false;
    # this workflow must not drop checkout credentials or reuse the PAT for git.
    "persist-credentials: false",
)
REQUIRED_WORKFLOW_NEEDLES = (
    "secrets.UPSTREAM_MERGE_GH_TOKEN",
    "unset GITHUB_TOKEN",
)


class IssueHost(Protocol):
    def list_open(self, label: str) -> list[int]: ...
    def list_open_tracking(self) -> list[int]: ...
    def create(self, title: str, body: str, labels: list[str] | None = None) -> int: ...
    def update(self, number: int, title: str, body: str) -> None: ...
    def comment(self, number: int, body: str) -> None: ...
    def close(self, number: int, comment: str) -> None: ...
    def ensure_labels(self, specs: list[tuple[str, str, str]]) -> list[str]: ...
    def attach_labels(self, number: int, specs: list[tuple[str, str, str]]) -> list[str]: ...
    def list_labels(self, number: int) -> set[str]: ...
    def remove_labels(self, number: int, names: list[str]) -> None: ...
    def get_body(self, number: int) -> str: ...


def is_tracking_title(title: str) -> bool:
    return str(title).startswith(TITLE_PREFIX)


def label_is_ready(
    *,
    exists_before: bool,
    create_ok: bool,
    create_err: str,
    exists_after: bool,
) -> bool:
    if exists_before or exists_after or create_ok:
        return True
    return "already exists" in (create_err or "").lower()


def decide_action(*, behind: int, existing_open: bool) -> str:
    if behind < 0:
        raise ValueError("behind must be >= 0")
    if behind > 0:
        return "update" if existing_open else "create"
    return "close" if existing_open else "noop"


def render_title(behind: int) -> str:
    unit = "commit" if behind == 1 else "commits"
    return f"{TITLE_PREFIX} Wei-Shaw/sub2api is {behind} {unit} ahead of TokenKey"


def parse_merge_tree_conflicts(raw: str) -> list[str]:
    """Parse `git merge-tree --write-tree --name-only --no-messages` output.

    First line is the merged tree OID; remaining non-empty lines are conflicted paths.
    """
    lines = [line for line in raw.splitlines() if line.strip()]
    if not lines:
        return []
    first = lines[0].strip()
    if not re.fullmatch(r"[0-9a-f]{40,64}", first):
        raise ValueError("merge-tree output missing leading tree OID")
    return lines[1:]


def truncate_commits(commits: list[str], limit: int = COMMIT_LIMIT) -> tuple[list[str], int]:
    cleaned = [line.rstrip("\n") for line in commits if line.strip()]
    if len(cleaned) <= limit:
        return cleaned, 0
    return cleaned[:limit], len(cleaned) - limit


def active_status_labels(labels: set[str]) -> list[str]:
    return [name for name in STATUS_LABELS if name in labels]


def forge_may_auto_start(labels: set[str]) -> bool:
    """Forge only auto-starts when needed is present and no blocking status label is set."""
    if any(name in labels for name in STATUS_LABELS):
        return False
    return LABEL in labels


def classify_conflict_risks(conflicts: list[str]) -> dict[str, list[str]]:
    buckets: dict[str, list[str]] = {name: [] for name, _ in RISK_PATH_RULES}
    buckets["other"] = []
    for path in conflicts:
        lowered = path.lower()
        matched = False
        for bucket, needles in RISK_PATH_RULES:
            if any(needle in lowered for needle in needles):
                buckets[bucket].append(path)
                matched = True
        if not matched:
            buckets["other"].append(path)
    return {key: value for key, value in buckets.items() if value}


def parse_facts_from_body(body: str) -> dict[str, str]:
    """Best-effort parse of previous sticky-board facts for refresh comments."""
    facts: dict[str, str] = {}
    patterns = {
        "behind": r"TokenKey behind:\s*(\d+)\s+commits?",
        "ahead": r"TokenKey ahead:\s*(\d+)\s+commits?",
        "upstream_head": r"upstream/main:\s*`([^`]+)`",
        "origin_head": r"origin/main:\s*`([^`]+)`",
    }
    for key, pattern in patterns.items():
        match = re.search(pattern, body or "")
        if match:
            facts[key] = match.group(1)
    return facts


def render_refresh_comment(
    *,
    drift: dict[str, Any],
    commits: list[str],
    previous: dict[str, str],
    run_url: str,
    status_labels: list[str],
) -> str:
    behind = int(drift.get("behind") or 0)
    upstream_head = str(drift.get("upstream_head") or "")
    origin_head = str(drift.get("origin_head") or "")
    shown, hidden = truncate_commits(commits)
    commit_block = "\n".join(shown) if shown else "(none)"
    if hidden:
        commit_block += f"\n... ({hidden} more)"

    prev_behind = previous.get("behind")
    delta_line = f"- Current behind: `{behind}`"
    if prev_behind is not None and str(prev_behind) != str(behind):
        delta_line = f"- Behind changed: `{prev_behind}` → `{behind}`"
    prev_up = previous.get("upstream_head")
    up_line = f"- upstream/main: `{upstream_head}`"
    if prev_up and prev_up != upstream_head:
        up_line = f"- upstream/main moved: `{prev_up}` → `{upstream_head}`"

    status_line = ", ".join(f"`{name}`" for name in status_labels) if status_labels else "`upstream-merge-needed` (Forge-actionable)"
    return "\n".join(
        [
            f"Refresh by {run_url or 'n/a'} (history only — body holds current facts).",
            "",
            delta_line,
            up_line,
            f"- origin/main: `{origin_head}`",
            f"- Status labels: {status_line}",
            "",
            "### New upstream commits (snapshot)",
            "",
            "```",
            commit_block,
            "```",
            "",
        ]
    )


def render_body(
    *,
    drift: dict[str, Any],
    commits: list[str],
    conflicts: list[str],
    open_prs: list[dict[str, Any]],
    run_url: str,
    status_labels: list[str] | None = None,
    hidden_commit_count: int = 0,
) -> str:
    del commits, hidden_commit_count  # commit lists live in refresh comments, not the sticky body
    behind = int(drift.get("behind") or 0)
    ahead = int(drift.get("ahead") or 0)
    upstream_head = str(drift.get("upstream_head") or "")
    origin_head = str(drift.get("origin_head") or "")
    status = list(status_labels or [])

    if conflicts:
        conflict_block = "\n".join(f"- `{path}`" for path in conflicts)
    else:
        conflict_block = "none (clean merge-tree)"

    if open_prs:
        pr_lines = [
            "- Do not treat leftover `merge/upstream-*` PRs as human-approved.",
            "",
        ]
        for pr in open_prs:
            number = pr.get("number")
            title = pr.get("title") or ""
            url = pr.get("url") or ""
            head = pr.get("headRefName") or ""
            pr_lines.append(f"- #{number} `{head}` {title} {url}".rstrip())
        pr_block = "\n".join(pr_lines)
    else:
        pr_block = "none"

    risks = classify_conflict_risks(conflicts)
    if risks:
        risk_lines: list[str] = []
        for bucket, paths in risks.items():
            joined = ", ".join(f"`{path}`" for path in paths[:12])
            extra = f" (+{len(paths) - 12} more)" if len(paths) > 12 else ""
            risk_lines.append(f"- **{bucket}**: {joined}{extra}")
        risk_block = "\n".join(risk_lines)
    else:
        risk_block = "- none from merge-tree conflicts"

    hot = sorted(
        name
        for name in ("billing", "auth", "routing", "accounts", "concurrency")
        if name in risks
    )
    hot_line = ", ".join(f"`{name}`" for name in hot) if hot else "none detected from conflict paths"

    if status:
        forge_line = (
            f"- Active status: {', '.join(f'`{name}`' for name in status)} — "
            "Forge must **not** auto-start a duplicate merge."
        )
    else:
        forge_line = (
            f"- Action label: `{LABEL}` — Forge may auto-start "
            "(only while no blocked/in-progress/ready label is set)."
        )

    return "\n".join(
        [
            "> Auto-generated by `.github/workflows/upstream-merge-notify.yml`.",
            "> Detection only: this workflow does **not** merge, open a PR, or run an agent.",
            "> Body is overwritten on every refresh; comments keep historical snapshots.",
            "",
            "## Current",
            "",
            f"- TokenKey behind: {behind} commits (`origin/main..upstream/main`)",
            f"- TokenKey ahead: {ahead} commits (`upstream/main..origin/main`)",
            f"- upstream/main: `{upstream_head}`",
            f"- origin/main: `{origin_head}`",
            f"- Open `merge/upstream-*` PRs: {len(open_prs)}",
            f"- merge-tree conflicts: {len(conflicts)}",
            f"- run: {run_url or 'n/a'}",
            "",
            "## Open merge/upstream-* PRs",
            "",
            pr_block,
            "",
            "## merge-tree conflict files (read-only)",
            "",
            conflict_block,
            "",
            "## TokenKey risk scope",
            "",
            "- prod / cn-prod deploy status: **not probed** by this notify workflow (detection-only).",
            f"- Hot buckets touched by conflicts: {hot_line}",
            "- TokenKey-specific surfaces (sentinel / `*_tk_*` / `.tk.*`) and FE/BE split from conflicts:",
            risk_block,
            "",
            "## Forge / agent signals",
            "",
            forge_line,
            "- Keep a single sticky `[upstream-merge]` issue; do not open per-drift tickets.",
            "- Before starting: check open `merge/upstream-*` PRs above, status labels, and whether drift SHA changed since the last refresh comment.",
            "- On start: remove `upstream-merge-needed`, add `upstream-merge-in-progress`.",
            "- On block: `upstream-merge-blocked`. On PR ready for humans: `upstream-merge-ready`.",
            "",
            "## Expected verification",
            "",
            "1. `bash scripts/upstream/check-drift.sh`",
            "2. Follow `.cursor/skills/tokenkey-upstream-merge/SKILL.md` (loads generic `upstream-merge`).",
            "3. `./scripts/preflight.sh` and `make test` on the merge branch.",
            "4. Shape gate: `.github/workflows/upstream-merge-pr-shape.yml`.",
            "",
            "## Human next steps",
            "",
            "1. Decide whether this batch is worth merging. Hotspot-heavy batches can wait.",
            "2. `git fetch upstream && git fetch origin`",
            "3. `git checkout -b merge/upstream-$(date -u +%Y%m%d) origin/main`",
            "4. `git merge --no-ff upstream/main`",
            "5. Resolve conflicts; keep TokenKey companions/sentinels; open `merge/upstream-*` PR.",
            "",
        ]
    ) + "\n"


def apply(
    *,
    drift: dict[str, Any],
    commits: list[str],
    conflicts: list[str],
    open_prs: list[dict[str, Any]],
    run_url: str,
    host: IssueHost,
) -> dict[str, Any]:
    if "behind" not in drift:
        raise ValueError("drift JSON missing 'behind'")
    behind = int(drift["behind"])
    existing = host.list_open_tracking()
    action = decide_action(behind=behind, existing_open=bool(existing))
    primary: int | None = existing[0] if existing else None
    label_warnings: list[str] = []

    if action in {"create", "update"}:
        current_labels: set[str] = set()
        previous_facts: dict[str, str] = {}
        if action == "update" and primary is not None:
            current_labels = set(host.list_labels(primary))
            previous_facts = parse_facts_from_body(host.get_body(primary))
        status = active_status_labels(current_labels)
        title = render_title(behind)
        body = render_body(
            drift=drift,
            commits=commits,
            conflicts=conflicts,
            open_prs=open_prs,
            run_url=run_url,
            status_labels=status,
        )
        if action == "create":
            create_specs = [
                spec for spec in LABEL_SPECS if spec[0] in {LABEL, AUTOMATED_LABEL}
            ]
            create_labels: list[str] = []
            if isinstance(host, GhIssueHost):
                create_labels = host.labels_ready_for_create(create_specs)
            else:
                create_labels = [name for name, _, _ in create_specs]
            primary = host.create(title, body, create_labels or None)
            current_labels = set(create_labels)
        else:
            assert primary is not None
            host.update(primary, title, body)
            host.comment(
                primary,
                render_refresh_comment(
                    drift=drift,
                    commits=commits,
                    previous=previous_facts,
                    run_url=run_url,
                    status_labels=status,
                ),
            )
            for extra in existing[1:]:
                host.close(extra, f"Duplicate of #{primary}.")
        if primary is not None:
            # Ensure status label *definitions* exist; never force-add status labels onto the issue.
            label_warnings = host.ensure_labels(list(LABEL_SPECS))
            current_labels = set(host.list_labels(primary)) | current_labels
            status = active_status_labels(current_labels)
            if status:
                # Human/Forge ownership: strip needed so Forge does not double-start.
                if LABEL in current_labels:
                    host.remove_labels(primary, [LABEL])
                host.attach_labels(
                    primary,
                    [(AUTOMATED_LABEL, "C5DEF5", "Automated signal")],
                )
            else:
                host.attach_labels(
                    primary,
                    [
                        (LABEL, "D93F0B", "Forge-actionable: TokenKey is behind Wei-Shaw/sub2api"),
                        (AUTOMATED_LABEL, "C5DEF5", "Automated signal"),
                    ],
                )
    elif action == "close":
        close_comment = (
            f"origin/main now contains upstream/main (`{drift.get('upstream_head') or ''}`). "
            f"{run_url}".rstrip()
        )
        for number in existing:
            host.close(number, close_comment)
        primary = None

    legacy_msg = "The Daily Upstream Merge Agent no longer auto-merges."
    if primary is not None:
        legacy_msg += f" See #{primary}."
    else:
        legacy_msg += (
            " A new `upstream-merge-needed` issue will open when TokenKey is behind upstream/main."
        )
    closed_legacy = []
    for legacy in LEGACY_LABELS:
        for number in host.list_open(legacy):
            host.close(number, legacy_msg)
            closed_legacy.append(number)

    return {
        "action": action,
        "issue": primary,
        "closed_legacy": closed_legacy,
        "label_warnings": label_warnings,
        "forge_actionable": forge_may_auto_start(
            set(host.list_labels(primary)) if primary is not None else set()
        )
        if primary is not None
        else False,
    }


class GhIssueHost:
    def __init__(self, runner: Any | None = None) -> None:
        self._run = runner or _run_gh

    def list_open(self, label: str) -> list[int]:
        raw = self._run(
            [
                "gh",
                "issue",
                "list",
                "--state",
                "open",
                "--label",
                label,
                "--json",
                "number",
                "--jq",
                "[.[].number]",
            ]
        )
        data = json.loads(raw or "[]")
        return [int(item) for item in data]

    def list_open_tracking(self) -> list[int]:
        raw = self._run(
            [
                "gh",
                "issue",
                "list",
                "--state",
                "open",
                "--limit",
                "100",
                "--json",
                "number,title",
            ]
        )
        items = json.loads(raw or "[]")
        by_title = [
            int(item["number"])
            for item in items
            if is_tracking_title(str(item.get("title") or ""))
        ]
        try:
            by_label = self.list_open(LABEL)
        except RuntimeError:
            by_label = []
        seen: list[int] = []
        for number in by_title + by_label:
            if number not in seen:
                seen.append(number)
        return seen

    def create(self, title: str, body: str, labels: list[str] | None = None) -> int:
        body_path = ""
        try:
            with tempfile.NamedTemporaryFile("w", suffix=".md", delete=False, encoding="utf-8") as handle:
                handle.write(body)
                body_path = handle.name
            args = [
                "gh",
                "issue",
                "create",
                "--title",
                title,
                "--body-file",
                body_path,
            ]
            repo = os.environ.get("GH_REPO") or os.environ.get("GITHUB_REPOSITORY") or ""
            if repo.strip():
                args.extend(["--repo", repo.strip()])
            ready = [label for label in (labels or []) if label]
            if ready:
                args.extend(["--label", ",".join(ready)])
            raw = self._run(args)
        finally:
            if body_path:
                Path(body_path).unlink(missing_ok=True)
        match = re.search(r"/issues/(\d+)", raw)
        if not match:
            raise RuntimeError(f"gh issue create did not return an issue URL: {raw!r}")
        return int(match.group(1))

    def update(self, number: int, title: str, body: str) -> None:
        body_path = ""
        try:
            with tempfile.NamedTemporaryFile("w", encoding="utf-8", delete=False) as handle:
                handle.write(body)
                body_path = handle.name
            self._run(
                [
                    "gh",
                    "issue",
                    "edit",
                    str(number),
                    "--title",
                    title,
                    "--body-file",
                    body_path,
                ]
            )
        finally:
            if body_path:
                Path(body_path).unlink(missing_ok=True)

    def comment(self, number: int, body: str) -> None:
        body_path = ""
        try:
            with tempfile.NamedTemporaryFile("w", encoding="utf-8", delete=False) as handle:
                handle.write(body)
                body_path = handle.name
            self._run(["gh", "issue", "comment", str(number), "--body-file", body_path])
        finally:
            if body_path:
                Path(body_path).unlink(missing_ok=True)

    def close(self, number: int, comment: str) -> None:
        self._run(["gh", "issue", "close", str(number), "--comment", comment])

    def list_labels(self, number: int) -> set[str]:
        raw = self._run(
            [
                "gh",
                "issue",
                "view",
                str(number),
                "--json",
                "labels",
                "--jq",
                "[.labels[].name]",
            ]
        )
        data = json.loads(raw or "[]")
        return {str(item) for item in data}

    def get_body(self, number: int) -> str:
        return self._run(
            ["gh", "issue", "view", str(number), "--json", "body", "--jq", ".body"]
        )

    def remove_labels(self, number: int, names: list[str]) -> None:
        for name in names:
            self._run(
                ["gh", "issue", "edit", str(number), "--remove-label", name],
                allow_failure=True,
            )

    def ensure_labels(self, specs: list[tuple[str, str, str]]) -> list[str]:
        warnings: list[str] = []
        for name, color, description in specs:
            if not self.ensure_label(name, color, description):
                warnings.append(f"could not ensure label {name!r}")
        return warnings

    def _label_exists(self, name: str) -> bool:
        raw = self._run(
            [
                "gh",
                "label",
                "list",
                "--search",
                name,
                "--limit",
                "20",
                "--json",
                "name",
            ]
        )
        items = json.loads(raw or "[]")
        return any(item.get("name") == name for item in items)

    def ensure_label(self, name: str, color: str, description: str) -> bool:
        exists_before = self._label_exists(name)
        create_ok = True
        create_err = ""
        if not exists_before:
            try:
                self._run(
                    [
                        "gh",
                        "label",
                        "create",
                        name,
                        "--color",
                        color,
                        "--description",
                        description[:100],
                    ]
                )
            except RuntimeError as exc:
                create_ok = False
                create_err = str(exc)
        exists_after = exists_before or self._label_exists(name)
        return label_is_ready(
            exists_before=exists_before,
            create_ok=create_ok,
            create_err=create_err,
            exists_after=exists_after,
        )

    def attach_labels(self, number: int, specs: list[tuple[str, str, str]]) -> list[str]:
        warnings: list[str] = []
        for name, color, description in specs:
            if not self.ensure_label(name, color, description):
                warnings.append(f"could not ensure label {name!r}")
                continue
            try:
                self._run(["gh", "issue", "edit", str(number), "--add-label", name])
            except RuntimeError as exc:
                warnings.append(str(exc))
        return warnings

    def labels_ready_for_create(self, specs: list[tuple[str, str, str]]) -> list[str]:
        ready: list[str] = []
        for name, color, description in specs:
            if self.ensure_label(name, color, description):
                ready.append(name)
        return ready


def _run_gh(args: list[str], allow_failure: bool = False) -> str:
    result = subprocess.run(args, text=True, capture_output=True, check=False)
    if result.returncode != 0 and not allow_failure:
        raise RuntimeError(
            f"{args[0]} failed ({result.returncode}): {result.stderr.strip() or result.stdout.strip()}"
        )
    return result.stdout.strip()


def _load_json(path: Path, default: Any) -> Any:
    if not path.exists():
        return default
    text = path.read_text(encoding="utf-8").strip()
    if not text:
        return default
    return json.loads(text)


def _load_lines(path: Path) -> list[str]:
    if not path.exists():
        return []
    return [line.rstrip("\n") for line in path.read_text(encoding="utf-8").splitlines() if line.strip()]


def cmd_apply(args: argparse.Namespace) -> int:
    drift = _load_json(Path(args.drift), {})
    if not isinstance(drift, dict) or "behind" not in drift:
        print("FAIL: --drift must be JSON with a 'behind' field", file=sys.stderr)
        return 2
    commits = _load_lines(Path(args.commits)) if args.commits else []
    conflicts_raw = Path(args.conflicts).read_text(encoding="utf-8") if args.conflicts else ""
    conflicts = parse_merge_tree_conflicts(conflicts_raw) if conflicts_raw.strip() else []
    open_prs = _load_json(Path(args.open_prs), []) if args.open_prs else []
    if not isinstance(open_prs, list):
        print("FAIL: --open-prs must be a JSON array", file=sys.stderr)
        return 2
    result = apply(
        drift=drift,
        commits=commits,
        conflicts=conflicts,
        open_prs=open_prs,
        run_url=args.run_url,
        host=GhIssueHost(),
    )
    print(json.dumps(result, ensure_ascii=False))
    if result.get("label_warnings"):
        print("WARN: " + "; ".join(result["label_warnings"]), file=sys.stderr)
    return 0


def assert_workflow_is_notify_only(text: str) -> None:
    for needle in FORBIDDEN_WORKFLOW_NEEDLES:
        if needle in text:
            raise AssertionError(f"notify workflow must not contain {needle!r}")
    for needle in REQUIRED_WORKFLOW_NEEDLES:
        if needle not in text:
            raise AssertionError(f"notify workflow must contain {needle!r}")
    if "notify-merge-needed.py" not in text:
        raise AssertionError("notify workflow must invoke notify-merge-needed.py")
    if "check-drift.sh" not in text:
        raise AssertionError("notify workflow must invoke check-drift.sh")


class MemoryHost:
    def __init__(
        self,
        open_issues: dict[str, list[int]] | None = None,
        titles: dict[int, str] | None = None,
        bodies: dict[int, str] | None = None,
        issue_labels: dict[int, set[str]] | None = None,
        attach_fail: bool = False,
    ) -> None:
        self.open_issues = {key: list(value) for key, value in (open_issues or {}).items()}
        self.titles = dict(titles or {})
        self.bodies = dict(bodies or {})
        self.issue_labels = {key: set(value) for key, value in (issue_labels or {}).items()}
        for number in self.open_issues.get(LABEL, []):
            self.titles.setdefault(number, f"{TITLE_PREFIX} seeded #{number}")
            self.issue_labels.setdefault(number, {LABEL, AUTOMATED_LABEL})
        self.created: list[tuple[str, str, list[str]]] = []
        self.updated: list[tuple[int, str, str]] = []
        self.comments: list[tuple[int, str]] = []
        self.closed: list[tuple[int, str]] = []
        self.labels: list[str] = []
        self.attached: list[tuple[int, str]] = []
        self.removed: list[tuple[int, str]] = []
        self.attach_fail = attach_fail
        self._next_number = 9000

    def list_open(self, label: str) -> list[int]:
        return list(self.open_issues.get(label, []))

    def list_open_tracking(self) -> list[int]:
        open_ids: set[int] = set()
        for values in self.open_issues.values():
            open_ids.update(values)
        return [number for number in sorted(open_ids) if is_tracking_title(self.titles.get(number, ""))]

    def create(self, title: str, body: str, labels: list[str] | None = None) -> int:
        self._next_number += 1
        number = self._next_number
        ready = list(labels or [])
        self.created.append((title, body, ready))
        self.titles[number] = title
        self.bodies[number] = body
        self.issue_labels[number] = set(ready)
        self.open_issues.setdefault(LABEL, []).append(number)
        return number

    def update(self, number: int, title: str, body: str) -> None:
        self.updated.append((number, title, body))
        self.titles[number] = title
        self.bodies[number] = body

    def comment(self, number: int, body: str) -> None:
        self.comments.append((number, body))

    def close(self, number: int, comment: str) -> None:
        self.closed.append((number, comment))
        for values in self.open_issues.values():
            if number in values:
                values.remove(number)

    def list_labels(self, number: int) -> set[str]:
        return set(self.issue_labels.get(number, set()))

    def get_body(self, number: int) -> str:
        return self.bodies.get(number, "")

    def remove_labels(self, number: int, names: list[str]) -> None:
        current = self.issue_labels.setdefault(number, set())
        for name in names:
            current.discard(name)
            self.removed.append((number, name))

    def ensure_labels(self, specs: list[tuple[str, str, str]]) -> list[str]:
        if self.attach_fail:
            return [f"could not ensure label {name!r}" for name, _, _ in specs]
        for name, _, _ in specs:
            self.labels.append(name)
        return []

    def attach_labels(self, number: int, specs: list[tuple[str, str, str]]) -> list[str]:
        if self.attach_fail:
            return [f"could not ensure label {name!r}" for name, _, _ in specs]
        current = self.issue_labels.setdefault(number, set())
        for name, _, _ in specs:
            self.labels.append(name)
            self.attached.append((number, name))
            current.add(name)
        return []


def run_selftest() -> int:
    failures: list[str] = []
    expect_count = 0

    def expect(name: str, cond: bool) -> None:
        nonlocal expect_count
        expect_count += 1
        print(f"{'PASS' if cond else 'FAIL'} {name}")
        if not cond:
            failures.append(name)

    expect("decide_create", decide_action(behind=3, existing_open=False) == "create")
    expect("decide_update", decide_action(behind=3, existing_open=True) == "update")
    expect("decide_close", decide_action(behind=0, existing_open=True) == "close")
    expect("decide_noop", decide_action(behind=0, existing_open=False) == "noop")
    expect("decide_single_commit_title", "1 commit ahead" in render_title(1))
    expect("decide_plural_title", "2 commits ahead" in render_title(2))

    raised = False
    try:
        decide_action(behind=-1, existing_open=False)
    except ValueError:
        raised = True
    expect("decide_rejects_negative", raised)

    tree = "0123456789abcdef0123456789abcdef01234567\nbackend/foo.go\nfrontend/bar.vue\n"
    expect("parse_conflicts", parse_merge_tree_conflicts(tree) == ["backend/foo.go", "frontend/bar.vue"])
    expect("parse_clean_tree", parse_merge_tree_conflicts("0123456789abcdef0123456789abcdef01234567\n") == [])
    parse_bad = False
    try:
        parse_merge_tree_conflicts("not-a-tree\nfile.go\n")
    except ValueError:
        parse_bad = True
    expect("parse_rejects_missing_oid", parse_bad)

    shown, hidden = truncate_commits([f"c{i}" for i in range(32)], limit=30)
    expect("truncate_keeps_30", shown == [f"c{i}" for i in range(30)] and hidden == 2)

    body = render_body(
        drift={"behind": 4, "ahead": 12, "upstream_head": "abc1234", "origin_head": "def5678"},
        commits=["aaaaaaa first", "bbbbbbb second"],
        conflicts=["backend/internal/service/gateway.go"],
        open_prs=[{"number": 1775, "title": "agent leftover", "url": "https://example.test/1775", "headRefName": "merge/upstream-2026-08-21"}],
        run_url="https://example.test/run/1",
    )
    expect("body_has_range", "origin/main..upstream/main" in body)
    expect("body_has_skill", "tokenkey-upstream-merge" in body)
    expect("body_has_conflict", "backend/internal/service/gateway.go" in body)
    expect("body_warns_leftover_pr", "Do not treat leftover" in body)
    expect("body_no_agent", "run-headless-agent" not in body)
    expect("body_has_current_section", "## Current" in body)
    expect("body_has_risk_scope", "## TokenKey risk scope" in body)
    expect("body_has_routing_bucket", "**routing**" in body)
    expect("body_omits_commit_dump", "aaaaaaa first" not in body)
    expect("body_detection_only", "Detection only" in body)
    clean_body = render_body(
        drift={"behind": 1, "ahead": 0, "upstream_head": "a", "origin_head": "b"},
        commits=[],
        conflicts=[],
        open_prs=[],
        run_url="",
    )
    expect("body_clean_merge_tree", "none (clean merge-tree)" in clean_body)

    risks = classify_conflict_risks(
        [
            "backend/internal/service/gateway.go",
            "scripts/sentinels/gateway-tk.json",
            "frontend/src/views/x.vue",
            "docs/readme.md",
        ]
    )
    expect("classify_routing", "backend/internal/service/gateway.go" in risks.get("routing", []))
    expect("classify_sentinel", "scripts/sentinels/gateway-tk.json" in risks.get("sentinel", []))
    expect("classify_frontend", "frontend/src/views/x.vue" in risks.get("frontend", []))
    expect("classify_other", "docs/readme.md" in risks.get("other", []))
    expect("forge_blocks_in_progress", not forge_may_auto_start({LABEL, LABEL_IN_PROGRESS}))
    expect("forge_allows_needed_only", forge_may_auto_start({LABEL, AUTOMATED_LABEL}))
    parsed = parse_facts_from_body(body)
    expect("parse_behind", parsed.get("behind") == "4")
    expect("parse_upstream", parsed.get("upstream_head") == "abc1234")

    created_host = MemoryHost(open_issues={LEGACY_LABEL: [1776]})
    created = apply(
        drift={"behind": 5, "ahead": 1, "upstream_head": "up", "origin_head": "tk"},
        commits=["deadbeef feat"],
        conflicts=[],
        open_prs=[],
        run_url="https://example.test/run/2",
        host=created_host,
    )
    expect("apply_create", created["action"] == "create" and created["issue"] == 9001)
    expect("apply_create_closes_legacy", created["closed_legacy"] == [1776])
    expect("apply_create_mentions_new_issue", any("See #9001" in comment for _, comment in created_host.closed))

    updated_host = MemoryHost(
        open_issues={LABEL: [10, 11]},
        bodies={10: render_body(
            drift={"behind": 9, "ahead": 0, "upstream_head": "oldup", "origin_head": "oldtk"},
            commits=[],
            conflicts=[],
            open_prs=[],
            run_url="https://example.test/run/old",
        )},
    )
    updated = apply(
        drift={"behind": 2, "ahead": 0, "upstream_head": "up", "origin_head": "tk"},
        commits=["c1 feat"],
        conflicts=[],
        open_prs=[],
        run_url="https://example.test/run/3",
        host=updated_host,
    )
    expect("apply_update", updated["action"] == "update" and updated["issue"] == 10)
    expect("apply_update_closes_duplicate", any(number == 11 for number, _ in updated_host.closed))
    expect("apply_update_overwrites_body", any("TokenKey behind: 2 commits" in body for _, _, body in updated_host.updated))
    expect(
        "apply_update_history_comment",
        any("Behind changed: `9` → `2`" in comment and "c1 feat" in comment for _, comment in updated_host.comments),
    )
    expect("apply_update_forge_actionable", updated.get("forge_actionable") is True)

    blocked_host = MemoryHost(
        open_issues={LABEL: [20]},
        issue_labels={20: {LABEL, AUTOMATED_LABEL, LABEL_IN_PROGRESS}},
        bodies={20: "## Current\n\n- TokenKey behind: 3 commits\n"},
    )
    blocked = apply(
        drift={"behind": 3, "ahead": 0, "upstream_head": "up", "origin_head": "tk"},
        commits=["c2"],
        conflicts=["backend/internal/service/billing.go"],
        open_prs=[],
        run_url="https://example.test/run/3b",
        host=blocked_host,
    )
    expect("apply_update_strips_needed_when_in_progress", LABEL not in blocked_host.issue_labels[20])
    expect("apply_update_keeps_in_progress", LABEL_IN_PROGRESS in blocked_host.issue_labels[20])
    expect("apply_update_not_forge_actionable", blocked.get("forge_actionable") is False)
    expect(
        "apply_update_body_mentions_status",
        any("upstream-merge-in-progress" in body for _, _, body in blocked_host.updated),
    )

    closed_host = MemoryHost(open_issues={LABEL: [10]})
    closed = apply(
        drift={"behind": 0, "ahead": 3, "upstream_head": "up", "origin_head": "tk"},
        commits=[],
        conflicts=[],
        open_prs=[],
        run_url="https://example.test/run/4",
        host=closed_host,
    )
    expect("apply_close", closed["action"] == "close" and closed["issue"] is None)
    expect("apply_close_comment", any("now contains upstream/main" in comment for _, comment in closed_host.closed))

    noop_host = MemoryHost()
    noop = apply(
        drift={"behind": 0, "ahead": 0, "upstream_head": "up", "origin_head": "tk"},
        commits=[],
        conflicts=[],
        open_prs=[],
        run_url="https://example.test/run/5",
        host=noop_host,
    )
    expect("apply_noop", noop["action"] == "noop" and not noop_host.created and not noop_host.closed)

    missing = False
    try:
        apply(drift={}, commits=[], conflicts=[], open_prs=[], run_url="", host=MemoryHost())
    except ValueError:
        missing = True
    expect("apply_rejects_missing_behind", missing)

    expect("title_is_tracking", is_tracking_title(render_title(4)))
    expect("title_not_tracking", not is_tracking_title("Daily Upstream Merge Agent leftover"))
    expect(
        "label_ready_exists_before",
        label_is_ready(exists_before=True, create_ok=False, create_err="denied", exists_after=False),
    )
    expect(
        "label_ready_create_ok",
        label_is_ready(exists_before=False, create_ok=True, create_err="", exists_after=False),
    )
    expect(
        "label_ready_already_exists",
        label_is_ready(
            exists_before=False,
            create_ok=False,
            create_err="gh failed (1): label already exists",
            exists_after=False,
        ),
    )
    expect(
        "label_ready_forbidden",
        not label_is_ready(
            exists_before=False,
            create_ok=False,
            create_err="could not add label: 'upstream-merge-needed' not found",
            exists_after=False,
        ),
    )

    unlabeled_host = MemoryHost(
        open_issues={"other": [12]},
        titles={12: render_title(9)},
    )
    unlabeled = apply(
        drift={"behind": 2, "ahead": 0, "upstream_head": "up", "origin_head": "tk"},
        commits=["c1"],
        conflicts=[],
        open_prs=[],
        run_url="https://example.test/run/6",
        host=unlabeled_host,
    )
    expect(
        "apply_update_by_title_without_label",
        unlabeled["action"] == "update" and unlabeled["issue"] == 12 and not unlabeled_host.created,
    )

    label_fail_host = MemoryHost(attach_fail=True)
    label_fail = apply(
        drift={"behind": 6, "ahead": 0, "upstream_head": "up", "origin_head": "tk"},
        commits=["deadbeef feat"],
        conflicts=[],
        open_prs=[],
        run_url="https://example.test/run/7",
        host=label_fail_host,
    )
    expect(
        "apply_create_survives_label_failure",
        label_fail["action"] == "create" and label_fail["issue"] == 9001 and label_fail_host.created,
    )
    expect(
        "apply_create_records_label_warnings",
        bool(label_fail["label_warnings"]) and not label_fail_host.labels,
    )

    gh_calls: list[list[str]] = []

    def fake_gh(args: list[str], allow_failure: bool = False) -> str:
        gh_calls.append(list(args))
        if args[:3] == ["gh", "issue", "create"]:
            return "https://github.com/youxuanxue/sub2api/issues/42"
        if args[:3] == ["gh", "issue", "edit"]:
            return ""
        if args[:3] == ["gh", "issue", "list"]:
            return "[]"
        if args[:3] == ["gh", "label", "list"]:
            return "[]"
        raise RuntimeError("gh failed (1): HTTP 403: Resource not accessible by integration")

    gh_host = GhIssueHost(runner=fake_gh)
    created_number = gh_host.create(render_title(1), "body")
    create_call = next(call for call in gh_calls if call[:3] == ["gh", "issue", "create"])
    expect("gh_create_returns_number", created_number == 42)
    expect("gh_create_uses_body_file", "--body-file" in create_call)
    gh_host.update(42, render_title(2), "fresh-body")
    update_call = next(call for call in gh_calls if call[:3] == ["gh", "issue", "edit"] and "--body-file" in call)
    expect("gh_update_uses_body_file", "--body-file" in update_call)
    attach_warnings = gh_host.attach_labels(42, [(LABEL, "D93F0B", "x"), (AUTOMATED_LABEL, "C5DEF5", "y")])
    expect("gh_attach_warns_when_label_create_denied", bool(attach_warnings))

    repo_root = Path(__file__).resolve().parents[2]
    workflow = repo_root / WORKFLOW_RELPATH
    expect("workflow_file_exists", workflow.is_file())
    if workflow.is_file():
        try:
            assert_workflow_is_notify_only(workflow.read_text(encoding="utf-8"))
            expect("workflow_is_notify_only", True)
        except AssertionError as exc:
            print(f"FAIL workflow_is_notify_only: {exc}")
            failures.append("workflow_is_notify_only")
            expect("workflow_is_notify_only", False)
    preflight = (repo_root / "scripts/preflight.sh").read_text(encoding="utf-8")
    expect(
        "preflight_runs_selftest",
        "scripts/upstream/notify-merge-needed.py --selftest" in preflight,
    )
    expect(
        "preflight_dropped_daily_agent",
        "upstream-merge-agent-daily.yml" not in preflight,
    )

    passed = expect_count - len(failures)
    print(f"notify-merge-needed self-test ({passed}/{expect_count} cases passed)")
    return 1 if failures else 0


def main() -> int:
    parser = argparse.ArgumentParser(description="Create or refresh the upstream-merge-needed issue.")
    parser.add_argument("--selftest", action="store_true")
    sub = parser.add_subparsers(dest="command")
    apply_parser = sub.add_parser("apply", help="create/update/close the tracking issue via gh")
    apply_parser.add_argument("--drift", required=True, help="path to check-drift.sh --json output")
    apply_parser.add_argument("--commits", default="", help="path to git log origin/main..upstream/main")
    apply_parser.add_argument("--conflicts", default="", help="path to git merge-tree --name-only output")
    apply_parser.add_argument("--open-prs", default="", help="path to leftover merge/upstream-* PR JSON")
    apply_parser.add_argument("--run-url", default="", help="CI run URL written into the issue body")
    args = parser.parse_args()

    if args.selftest:
        return run_selftest()
    if args.command == "apply":
        return cmd_apply(args)
    parser.error("one of apply / --selftest is required")
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
