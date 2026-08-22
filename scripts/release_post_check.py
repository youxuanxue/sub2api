#!/usr/bin/env python3
"""Plan and evaluate the +5min post-release check from live tag → new tag.

Owner for "did every change/PR between the serving version and this release
behave as expected on live?" Model-invented HOOK_PATTERNS are not a plan:
this module enumerates first-parent commits, extracts PR numbers, and derives
observables from the actual diff plus a path table.

Subcommands:
  plan --live vX.Y.Z --new vA.B.C [--repo DIR]
  hook-patterns --plan-file PATH
  evaluate --plan-file PATH --tick-file PATH [--control-plane-ok true|false]
"""
from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
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
AUTH_PATH = re.compile(r"api_key_auth|internal/server/middleware/middleware\.go")
SUBSCRIPTION_CODES = (
    "WEEKLY_LIMIT_EXCEEDED",
    "DAILY_LIMIT_EXCEEDED",
    "MONTHLY_LIMIT_EXCEEDED",
    "SUBSCRIPTION_EXPIRED",
)
FAILOVER_LOG = "[Forward] Upstream error (failover)"
ERROR_STORM_THRESHOLD = 3
SKIP_PREFIXES = ("docs/",)
SKIP_NAMES = {"backend/cmd/server/VERSION"}


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
        item["id"] = f"pr-{source[1:]}-{pattern}"
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
            add(_check(source=source, kind="error_absent", pattern=code, expect="not_storming"))

    if any(AUTH_PATH.search(p) for p in files):
        add(
            _check(
                source=source,
                kind="error_absent",
                pattern="error: code=",
                expect="not_storming",
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
    traffic: dict[str, Any] = {"completed_total": 0, "status_5xx": {}, "top_paths": []}
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
            }
    return {
        "hooks": hooks,
        "panic": panic,
        "status_5xx": traffic["status_5xx"],
        "completed_total": traffic["completed_total"],
        "top_paths": traffic["top_paths"],
    }


def evaluate(
    plan: dict[str, Any],
    tick: dict[str, Any],
    *,
    control_plane_ok: bool,
) -> dict[str, Any]:
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
    five_xx_count = sum(int(v) for v in status_5xx.values())
    if five_xx_count:
        results.append(
            {
                "id": "status-5xx",
                "source": "baseline",
                "verdict": "fail",
                "observed": {"status_5xx": status_5xx},
            }
        )
        bump("red")
    else:
        results.append(
            {
                "id": "status-5xx",
                "source": "baseline",
                "verdict": "pass",
                "observed": {"status_5xx": {}},
            }
        )

    failover_count = int(hooks.get(FAILOVER_LOG) or 0)
    for item in plan.get("checks") or []:
        pattern = item["pattern"]
        count = int(hooks.get(pattern) or 0)
        kind = item["kind"]
        verdict = "pass"
        if kind == "error_absent":
            verdict = "fail" if count >= ERROR_STORM_THRESHOLD else "pass"
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
        results.append(row)
        if verdict == "fail":
            bump("red")
        # inconclusive = path not hit in the window. Report it; do not redden.

    return {
        "verdict": worst,
        "range": plan.get("range"),
        "changes": plan.get("changes") or [],
        "checks": results,
        "traffic": {
            "completed_total": int(tick.get("completed_total") or 0),
            "top_paths": tick.get("top_paths") or [],
        },
    }


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

    args = parser.parse_args(argv)
    if args.cmd == "plan":
        _print(build_plan(Path(args.repo), args.live, args.new))
        return 0
    if args.cmd == "hook-patterns":
        plan = _load_json(args.plan_file)
        print(",".join(hook_patterns(plan)))
        return 0

    plan = _load_json(args.plan_file)
    tick_raw = Path(args.tick_file).read_text(encoding="utf-8")
    try:
        tick = json.loads(tick_raw)
        if "hooks" not in tick:
            tick = parse_tick_stdout(tick_raw)
    except json.JSONDecodeError:
        tick = parse_tick_stdout(tick_raw)
    result = evaluate(plan, tick, control_plane_ok=args.control_plane_ok == "true")
    _print(result)
    return 1 if result["verdict"] == "red" else 0


if __name__ == "__main__":
    raise SystemExit(main())
