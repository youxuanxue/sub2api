#!/usr/bin/env python3
"""Plan and evaluate the +5min post-release check from live tag → new tag.

Owner for "did every change/PR between the serving version and this release
behave as expected on live?" Model-invented HOOK_PATTERNS are not a plan:
this module enumerates first-parent commits, extracts PR numbers, and derives
observables from the actual diff plus a path table.

Subcommands:
  plan --live vX.Y.Z --new vA.B.C [--repo DIR]
  hook-patterns --plan-file PATH
  evaluate --plan-file PATH --tick-file PATH [--phase immediate|delayed]
  wait --since RFC3339 [--minimum-seconds 300]
  summary --evaluation-file PATH --immediate-evaluation-file PATH
  gate --evaluation-file PATH --immediate-evaluation-file PATH
"""
from __future__ import annotations

import argparse
import json
import math
import re
import subprocess
import sys
import time
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any

NOISE_SUBJECT = re.compile(r"bump VERSION|sync.version.file", re.I)
PR_SUBJECT = re.compile(r"\(#(\d+)\)\s*$")
ERROR_CTOR = re.compile(
    r'(?:TooManyRequests|Forbidden|NotFound|BadRequest|Conflict)\("([A-Z][A-Z0-9_]+)"'
)
HTTP_STATUS = re.compile(r"\b([45]\d\d)\b")
FAILOVER_PATH = re.compile(r"gateway_forward|upstream_errors|failover", re.I)
SUBSCRIPTION_PATH = re.compile(r"subscription|universal_routing")
SUBSCRIPTION_CODES = (
    "WEEKLY_LIMIT_EXCEEDED",
    "DAILY_LIMIT_EXCEEDED",
    "MONTHLY_LIMIT_EXCEEDED",
    "SUBSCRIPTION_EXPIRED",
)
FAILOVER_LOG = "[Forward] Upstream error (failover)"
ERROR_STORM_THRESHOLD = 20
SKIP_PREFIXES = ("docs/",)
SKIP_NAMES = {"backend/cmd/server/VERSION"}
REGRESSION_RULES = (
    {
        "paths": (re.compile(r"backend/internal/service/openai_responses_lite_tools\.go$"),),
        "markers": ("parallel_tool_calls",),
        "pattern": "parallel_tool_calls",
        "traffic_paths": ("/responses", "/v1/responses"),
    },
    {
        "paths": (
            re.compile(r"backend/internal/service/openai_gateway_cc_pipeline\.go$"),
            re.compile(r"backend/internal/service/openai_gateway_messages_tk\.go$"),
            re.compile(r"backend/internal/service/openai_official_upstream_tk\.go$"),
        ),
        "markers": (
            "ErrForeignCredentialOfficialOpenAIFallback",
            "newAPIBridgeOwnsAnthropicMessages",
        ),
        "pattern": "Incorrect API key provided",
        "traffic_paths": ("/v1/messages",),
    },
)


def _run_git(repo: Path, *args: str) -> str:
    proc = subprocess.run(
        ["git", *args],
        cwd=repo,
        capture_output=True,
        text=True,
        check=False,
    )
    if proc.returncode != 0:
        raise RuntimeError(proc.stderr.strip() or f"git {' '.join(args)} failed")
    return proc.stdout


def _norm_tag(tag: str) -> str:
    tag = tag.strip()
    if not tag:
        raise ValueError("empty tag")
    return tag if tag.startswith("v") else f"v{tag}"


def _is_skipped_path(path: str) -> bool:
    return path in SKIP_NAMES or path.startswith(SKIP_PREFIXES)


def _commit_files(repo: Path, sha: str) -> list[str]:
    out = _run_git(repo, "diff-tree", "--no-commit-id", "--name-only", "-r", sha)
    return [line for line in out.splitlines() if line]


def _commit_diff(repo: Path, sha: str) -> str:
    return _commit_diff_for(repo, sha, None)


def _commit_diff_for(repo: Path, sha: str, paths: list[str] | None) -> str:
    args = ["show", "--format=", "-U0", sha]
    if paths:
        args.extend(["--", *paths])
    return _run_git(repo, *args)


def _is_test_path(path: str) -> bool:
    return path.endswith("_test.go") or "/testdata/" in path


def _statuses_added(diff_text: str) -> list[str]:
    """Statuses newly present on `case` arms — ignore comments and test fixtures."""
    old: set[str] = set()
    new: set[str] = set()
    for line in diff_text.splitlines():
        if line.startswith("+++") or line.startswith("---"):
            continue
        if not re.search(r"\bcase\b", line):
            continue
        if line.startswith("+"):
            new.update(HTTP_STATUS.findall(line))
        elif line.startswith("-"):
            old.update(HTTP_STATUS.findall(line))
    return sorted(new - old)


def _added_error_codes(diff_text: str) -> list[str]:
    codes: set[str] = set()
    for line in diff_text.splitlines():
        if line.startswith("+") and not line.startswith("+++"):
            codes.update(ERROR_CTOR.findall(line))
    return sorted(codes)


def _change_id(pr: int | None, sha: str) -> str:
    return f"#{pr}" if pr is not None else sha[:12]


def _check(
    *,
    source: str,
    kind: str,
    pattern: str,
    expect: str,
    extra: dict[str, Any] | None = None,
) -> dict[str, Any]:
    item = {
        "id": f"{source.lstrip('#') if source.startswith('#') else source}-{pattern}".replace(
            " ", "_"
        ),
        "source": source,
        "kind": kind,
        "pattern": pattern,
        "expect": expect,
    }
    if source.startswith("#"):
        item["id"] = f"pr-{source[1:]}-{pattern}".replace(" ", "_")
    if extra:
        item.update(extra)
    return item


def _derive_checks(source: str, files: list[str], diff_text: str) -> list[dict[str, Any]]:
    checks: list[dict[str, Any]] = []
    seen: set[str] = set()

    def add(item: dict[str, Any]) -> None:
        if item["id"] in seen:
            return
        seen.add(item["id"])
        checks.append(item)

    failover_files = [p for p in files if FAILOVER_PATH.search(p) and not _is_test_path(p)]
    if failover_files:
        for status in _statuses_added(diff_text):
            add(
                _check(
                    source=source,
                    kind="failover_status",
                    pattern=f"Status={status}",
                    expect="failover_if_present",
                )
            )

    for code in _added_error_codes(diff_text):
        add(_check(source=source, kind="error_absent", pattern=code, expect="not_storming"))

    if any(SUBSCRIPTION_PATH.search(p) for p in files):
        for code in SUBSCRIPTION_CODES:
            add(_check(source=source, kind="observe", pattern=code, expect="report_only"))

    for rule in REGRESSION_RULES:
        if not any(path_pattern.search(path) for path_pattern in rule["paths"] for path in files):
            continue
        if not any(marker in diff_text for marker in rule["markers"]):
            continue
        add(
            _check(
                source=source,
                kind="regression_absent",
                pattern=rule["pattern"],
                expect="absent_when_path_observed",
                extra={"traffic_paths": list(rule["traffic_paths"])},
            )
        )

    return checks


def build_plan(repo: Path, live: str, new: str) -> dict[str, Any]:
    live_ref = _norm_tag(live)
    new_ref = _norm_tag(new)
    shas = _run_git(
        repo,
        "rev-list",
        "--reverse",
        "--first-parent",
        f"{live_ref}..{new_ref}",
    )
    changes: list[dict[str, Any]] = []
    checks: list[dict[str, Any]] = []
    for sha in shas.splitlines():
        if not sha.strip():
            continue
        subject = _run_git(repo, "log", "-1", "--pretty=%s", sha).strip()
        if NOISE_SUBJECT.search(subject):
            continue
        files = [p for p in _commit_files(repo, sha) if not _is_skipped_path(p)]
        if not files:
            continue
        pr_match = PR_SUBJECT.search(subject)
        pr = int(pr_match.group(1)) if pr_match else None
        source = _change_id(pr, sha)
        change = {
            "sha": sha,
            "subject": subject,
            "pr": pr,
            "files": files,
        }
        changes.append(change)
        impl_files = [p for p in files if not _is_test_path(p)]
        impl_diff = _commit_diff_for(repo, sha, impl_files) if impl_files else ""
        checks.extend(_derive_checks(source, impl_files, impl_diff))

    seen: set[str] = set()
    unique_checks: list[dict[str, Any]] = []
    for item in checks:
        if item["id"] in seen:
            continue
        seen.add(item["id"])
        unique_checks.append(item)

    return {
        "range": {"live": live_ref, "new": new_ref},
        "changes": changes,
        "checks": unique_checks,
    }


def hook_patterns(plan: dict[str, Any]) -> list[str]:
    patterns: list[str] = []
    for item in plan.get("checks") or []:
        pat = item.get("pattern")
        if pat and pat not in patterns:
            patterns.append(pat)
        if item.get("kind") == "failover_status" and FAILOVER_LOG not in patterns:
            patterns.append(FAILOVER_LOG)
    return patterns


def parse_tick_stdout(stdout: str) -> dict[str, Any]:
    hooks: dict[str, int] = {}
    panic = 0
    traffic: dict[str, Any] = {
        "completed_total": 0,
        "status_5xx": {},
        "top_paths": [],
        "path_counts": {},
    }
    section = ""
    for raw in stdout.splitlines():
        line = raw.strip()
        if line.startswith("=== ") and line.endswith(" ==="):
            section = line.strip("= ").strip()
            continue
        if not line.startswith("{"):
            continue
        try:
            obj = json.loads(line)
        except json.JSONDecodeError:
            continue
        if section == "hooks" and "pattern" in obj:
            hooks[str(obj["pattern"])] = int(obj.get("count") or 0)
        elif section == "panic":
            panic = int(obj.get("count") or 0)
        elif section == "traffic":
            traffic = {
                "completed_total": int(obj.get("completed_total") or 0),
                "status_5xx": obj.get("status_5xx") or {},
                "top_paths": obj.get("top_paths") or [],
                "path_counts": obj.get("path_counts") or {},
            }
    return {
        "hooks": hooks,
        "panic": panic,
        "status_5xx": traffic["status_5xx"],
        "completed_total": traffic["completed_total"],
        "top_paths": traffic["top_paths"],
        "path_counts": traffic["path_counts"],
    }


def evaluate(
    plan: dict[str, Any],
    tick: dict[str, Any],
    *,
    control_plane_ok: bool,
    phase: str = "delayed",
) -> dict[str, Any]:
    if phase not in {"immediate", "delayed"}:
        raise ValueError(f"unsupported phase: {phase}")
    hooks = tick.get("hooks") or {}
    results: list[dict[str, Any]] = []
    worst = "green"

    def bump(level: str) -> None:
        nonlocal worst
        rank = {"green": 0, "yellow": 1, "red": 2}
        if rank[level] > rank[worst]:
            worst = level

    if not control_plane_ok:
        results.append(
            {
                "id": "control-plane",
                "source": "baseline",
                "verdict": "fail",
                "observed": {"ok": False},
            }
        )
        bump("red")
    else:
        results.append(
            {
                "id": "control-plane",
                "source": "baseline",
                "verdict": "pass",
                "observed": {"ok": True},
            }
        )

    panic = int(tick.get("panic") or 0)
    if panic:
        results.append(
            {
                "id": "panic",
                "source": "baseline",
                "verdict": "fail",
                "observed": {"count": panic},
            }
        )
        bump("red")
    else:
        results.append(
            {
                "id": "panic",
                "source": "baseline",
                "verdict": "pass",
                "observed": {"count": 0},
            }
        )

    status_5xx = tick.get("status_5xx") or {}
    if phase == "delayed":
        status_5xx_total = sum(int(count or 0) for count in status_5xx.values())
        status_5xx_verdict = (
            "fail" if status_5xx_total >= ERROR_STORM_THRESHOLD else "observe"
        )
        results.append(
            {
                "id": "status-5xx",
                "source": "baseline",
                "verdict": status_5xx_verdict,
                "observed": {
                    "status_5xx": status_5xx,
                    "total": status_5xx_total,
                    "storm_threshold": ERROR_STORM_THRESHOLD,
                },
            }
        )
        if status_5xx_verdict == "fail":
            bump("red")

    path_counts = {
        str(path): int(count or 0) for path, count in (tick.get("path_counts") or {}).items()
    }
    if not path_counts:
        path_counts = {
            str(row.get("path")): int(row.get("n") or 0)
            for row in (tick.get("top_paths") or [])
            if row.get("path") is not None
        }

    failover_count = int(hooks.get(FAILOVER_LOG) or 0)
    for item in plan.get("checks") or []:
        pattern = item["pattern"]
        count = int(hooks.get(pattern) or 0)
        kind = item["kind"]
        verdict = "pass"
        if kind == "observe":
            verdict = "observe"
        elif kind == "error_absent":
            verdict = "fail" if count >= ERROR_STORM_THRESHOLD else "pass"
        elif kind == "regression_absent":
            relevant_requests = sum(path_counts.get(path, 0) for path in item.get("traffic_paths") or [])
            if count > 0:
                verdict = "fail"
            elif relevant_requests == 0:
                verdict = "inconclusive"
            else:
                verdict = "pass"
        elif kind == "failover_status":
            if count == 0:
                verdict = "inconclusive"
            elif failover_count == 0:
                verdict = "fail"
            else:
                verdict = "pass"
        row = {
            "id": item["id"],
            "source": item.get("source"),
            "kind": kind,
            "pattern": pattern,
            "verdict": verdict,
            "observed": {"count": count},
        }
        if kind == "failover_status":
            row["observed"]["failover_count"] = failover_count
        if kind == "regression_absent":
            row["observed"]["relevant_requests"] = relevant_requests
            row["observed"]["traffic_paths"] = item.get("traffic_paths") or []
        results.append(row)
        if verdict == "fail":
            bump("red")
        # inconclusive = path not hit in the window. Report it; do not redden.

    return {
        "phase": phase,
        "verdict": worst,
        "range": plan.get("range"),
        "changes": plan.get("changes") or [],
        "checks": results,
        "traffic": {
            "completed_total": int(tick.get("completed_total") or 0),
            "status_5xx": status_5xx,
            "top_paths": tick.get("top_paths") or [],
        },
    }


def _markdown_cell(value: Any) -> str:
    return str(value).replace("|", "\\|").replace("\n", " ")


def _pr_checks(evaluation: dict[str, Any]) -> list[dict[str, Any]]:
    return [item for item in evaluation.get("checks") or [] if str(item.get("source", "")).startswith("#")]


def seconds_until_window(
    since: str,
    minimum_seconds: int,
    *,
    now: datetime | None = None,
) -> int:
    if minimum_seconds < 0:
        raise ValueError("minimum_seconds must be non-negative")
    normalized = since.strip()
    if normalized.endswith("Z"):
        normalized = f"{normalized[:-1]}+00:00"
    try:
        cutover = datetime.fromisoformat(normalized)
    except ValueError as exc:
        raise ValueError(f"invalid RFC3339 timestamp: {since}") from exc
    if cutover.tzinfo is None:
        raise ValueError(f"timestamp must include timezone: {since}")
    current = now or datetime.now(timezone.utc)
    if current.tzinfo is None:
        raise ValueError("now must include timezone")
    remaining = cutover + timedelta(seconds=minimum_seconds) - current
    return max(0, math.ceil(remaining.total_seconds()))


def evaluation_gate_errors(
    immediate: dict[str, Any] | None,
    delayed: dict[str, Any] | None,
) -> list[str]:
    errors: list[str] = []
    for expected_phase, evaluation in (("immediate", immediate), ("delayed", delayed)):
        if not isinstance(evaluation, dict):
            errors.append(f"{expected_phase} evaluation is missing or invalid")
            continue
        phase = evaluation.get("phase")
        verdict = evaluation.get("verdict")
        if phase != expected_phase:
            errors.append(
                f"{expected_phase} evaluation phase mismatch: expected {expected_phase}, got {phase!r}"
            )
        if verdict != "green":
            errors.append(
                f"{expected_phase} evaluation verdict must be green, got {verdict!r}"
            )
    return errors


def _read_evaluation(path: str, expected_phase: str) -> tuple[dict[str, Any] | None, str | None]:
    source = Path(path)
    if not source.is_file():
        return None, f"missing evaluation file: {source}"
    try:
        data = json.loads(source.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError) as exc:
        return None, f"invalid evaluation file {source}: {exc}"
    if not isinstance(data, dict):
        return None, f"invalid evaluation object in {source}"
    phase = data.get("phase")
    verdict = data.get("verdict")
    if expected_phase == "delayed" and verdict == "skip" and not data.get("changes"):
        return data, None
    if phase != expected_phase:
        return None, f"{expected_phase} evaluation phase mismatch in {source}: got {phase!r}"
    if verdict not in {"green", "red"}:
        return None, f"{expected_phase} evaluation verdict invalid in {source}: got {verdict!r}"
    return data, None


def _baseline_failures(evaluation: dict[str, Any]) -> list[dict[str, Any]]:
    return [
        item
        for item in evaluation.get("checks") or []
        if item.get("source") == "baseline" and item.get("verdict") == "fail"
    ]


def _pr_evidence_counts(evaluation: dict[str, Any]) -> str:
    counts = {"pass": 0, "inconclusive": 0, "fail": 0}
    for item in _pr_checks(evaluation):
        verdict = item.get("verdict")
        if verdict in counts:
            counts[verdict] += 1
    return ", ".join(f"{verdict}={counts[verdict]}" for verdict in counts)


def render_summary(
    delayed: dict[str, Any] | None,
    *,
    immediate: dict[str, Any] | None = None,
    evidence_errors: dict[str, str] | None = None,
) -> str:
    release_range = (delayed or {}).get("range") or (immediate or {}).get("range") or {}
    lines = [
        "## post-release-check",
        f"- live (was serving): `{release_range.get('live', 'unknown')}`",
        f"- new: `{release_range.get('new', 'unknown')}`",
    ]
    if delayed is not None and delayed.get("verdict") == "skip" and not delayed.get("changes"):
        lines.extend(
            [
                "- verdict: `skip`",
                f"- reason: {_markdown_cell(delayed.get('reason', 'no product commits'))}",
                "",
                "### Changes",
                "- none",
            ]
        )
        return "\n".join(lines) + "\n"

    errors = evidence_errors or {}
    immediate_status = immediate.get("verdict", "unknown") if immediate is not None else "invalid"
    delayed_status = delayed.get("verdict", "unknown") if delayed is not None else "invalid"
    lines.append(f"- immediate PR hooks: `{immediate_status}`")
    lines.append(f"- +5 min traffic / errors: `{delayed_status}`")
    if immediate is not None:
        lines.append(f"- immediate PR evidence: `{_pr_evidence_counts(immediate)}`")
    if delayed is not None:
        lines.append(f"- +5 min PR evidence: `{_pr_evidence_counts(delayed)}`")

    if errors:
        lines.extend(["", "### Evidence problems"])
        for phase in ("immediate", "delayed"):
            if phase in errors:
                lines.append(f"- `{phase}`: {_markdown_cell(errors[phase])}")

    traffic = (delayed or {}).get("traffic") or {}
    statuses = traffic.get("status_5xx") or {}
    status_total = sum(int(count or 0) for count in statuses.values())
    status_detail = ", ".join(
        f"{status}: {count}" for status, count in sorted(statuses.items(), key=lambda item: str(item[0]))
    ) or "none"
    lines.extend(["", "### Traffic / 5xx (+5 min)"])
    if delayed is None:
        lines.append(f"- unavailable: {_markdown_cell(errors.get('delayed', 'delayed evaluation missing'))}")
    else:
        lines.extend(
            [
                f"- completed requests: `{int(traffic.get('completed_total') or 0)}`",
                f"- 5xx: `{status_total}` (`{status_detail}`)",
                "",
                "| Top path | Requests |",
                "| --- | ---: |",
            ]
        )
        top_paths = traffic.get("top_paths") or []
        if top_paths:
            for row in top_paths:
                lines.append(f"| `{_markdown_cell(row.get('path', '<none>'))}` | {int(row.get('n') or 0)} |")
        else:
            lines.append("| _none observed_ | 0 |")

    baseline_rows: list[tuple[str, dict[str, Any]]] = []
    if immediate is not None:
        baseline_rows.extend(("immediate", item) for item in _baseline_failures(immediate))
    if delayed is not None:
        baseline_rows.extend(("+5 min", item) for item in _baseline_failures(delayed))
    if baseline_rows:
        lines.extend(
            [
                "",
                "### Baseline failures",
                "",
                "| Phase | Check | Verdict | Observed |",
                "| --- | --- | --- | --- |",
            ]
        )
        for phase_label, item in baseline_rows:
            observed = json.dumps(item.get("observed") or {}, sort_keys=True, separators=(",", ":"))
            lines.append(
                f"| {phase_label} | `{_markdown_cell(item.get('id', 'unknown'))}` | "
                f"`{_markdown_cell(item.get('verdict', 'unknown'))}` | `{_markdown_cell(observed)}` |"
            )

    lines.extend(["", "### PR checks", "", "| Phase | PR | Verdict | Pattern | Observed |", "| --- | --- | --- | --- | --- |"])
    phase_rows: list[tuple[str, dict[str, Any]]] = []
    if immediate is not None:
        phase_rows.extend(("immediate", item) for item in _pr_checks(immediate))
    if delayed is not None:
        phase_rows.extend(("+5 min", item) for item in _pr_checks(delayed))
    if phase_rows:
        for phase_label, item in phase_rows:
            observed = json.dumps(item.get("observed") or {}, sort_keys=True, separators=(",", ":"))
            lines.append(
                f"| {phase_label} | {_markdown_cell(item.get('source', 'unknown'))} | "
                f"`{_markdown_cell(item.get('verdict', 'unknown'))}` | "
                f"`{_markdown_cell(item.get('pattern', item.get('id', 'unknown')))}` | "
                f"`{_markdown_cell(observed)}` |"
            )
    else:
        lines.append("| - | - | `not-derived` | No PR-specific observable matched this release | `{}` |")

    lines.extend(["", "### Changes"])
    changes = (delayed or {}).get("changes") or (immediate or {}).get("changes") or []
    if changes:
        for change in changes:
            source = f"#{change['pr']}" if change.get("pr") else str(change.get("sha", ""))[:12]
            lines.append(f"- {source} {_markdown_cell(change.get('subject', ''))}")
    else:
        lines.append("- none")
    return "\n".join(lines) + "\n"


def _load_json(path: str) -> dict[str, Any]:
    return json.loads(Path(path).read_text(encoding="utf-8"))


def _print(obj: Any) -> None:
    json.dump(obj, sys.stdout, indent=2, sort_keys=True)
    sys.stdout.write("\n")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Plan/evaluate post-release live checks")
    sub = parser.add_subparsers(dest="cmd", required=True)

    p_plan = sub.add_parser("plan")
    p_plan.add_argument("--live", required=True)
    p_plan.add_argument("--new", required=True)
    p_plan.add_argument("--repo", default=".")

    p_hooks = sub.add_parser("hook-patterns")
    p_hooks.add_argument("--plan-file", required=True)

    p_eval = sub.add_parser("evaluate")
    p_eval.add_argument("--plan-file", required=True)
    p_eval.add_argument("--tick-file", required=True)
    p_eval.add_argument("--control-plane-ok", choices=("true", "false"), default="true")
    p_eval.add_argument("--phase", choices=("immediate", "delayed"), default="delayed")

    p_wait = sub.add_parser("wait")
    p_wait.add_argument("--since", required=True)
    p_wait.add_argument("--minimum-seconds", type=int, default=300)

    p_summary = sub.add_parser("summary")
    p_summary.add_argument("--evaluation-file", required=True)
    p_summary.add_argument("--immediate-evaluation-file", required=True)

    p_gate = sub.add_parser("gate")
    p_gate.add_argument("--evaluation-file", required=True)
    p_gate.add_argument("--immediate-evaluation-file", required=True)

    args = parser.parse_args(argv)
    if args.cmd == "plan":
        _print(build_plan(Path(args.repo), args.live, args.new))
        return 0
    if args.cmd == "hook-patterns":
        plan = _load_json(args.plan_file)
        print(",".join(hook_patterns(plan)))
        return 0
    if args.cmd == "wait":
        remaining = seconds_until_window(args.since, args.minimum_seconds)
        print(f"waiting {remaining}s for cutover+{args.minimum_seconds}s observation window")
        time.sleep(remaining)
        return 0
    if args.cmd == "summary":
        immediate, immediate_error = _read_evaluation(
            args.immediate_evaluation_file, "immediate"
        )
        delayed, delayed_error = _read_evaluation(args.evaluation_file, "delayed")
        evidence_errors = {
            phase: error
            for phase, error in (
                ("immediate", immediate_error),
                ("delayed", delayed_error),
            )
            if error is not None
        }
        sys.stdout.write(
            render_summary(
                delayed,
                immediate=immediate,
                evidence_errors=evidence_errors,
            )
        )
        return 0
    if args.cmd == "gate":
        immediate, immediate_error = _read_evaluation(
            args.immediate_evaluation_file, "immediate"
        )
        delayed, delayed_error = _read_evaluation(args.evaluation_file, "delayed")
        errors = [error for error in (immediate_error, delayed_error) if error is not None]
        if immediate_error is None and delayed_error is None:
            errors.extend(evaluation_gate_errors(immediate, delayed))
        for error in errors:
            print(f"::error::post-release {error}")
        return 1 if errors else 0

    plan = _load_json(args.plan_file)
    tick_raw = Path(args.tick_file).read_text(encoding="utf-8")
    try:
        tick = json.loads(tick_raw)
        if "hooks" not in tick:
            tick = parse_tick_stdout(tick_raw)
    except json.JSONDecodeError:
        tick = parse_tick_stdout(tick_raw)
    result = evaluate(
        plan,
        tick,
        control_plane_ok=args.control_plane_ok == "true",
        phase=args.phase,
    )
    _print(result)
    return 1 if result["verdict"] == "red" else 0


if __name__ == "__main__":
    raise SystemExit(main())
