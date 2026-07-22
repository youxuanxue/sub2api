#!/usr/bin/env python3
"""Fail-closed validation for report-driven ops Draft PRs."""
from __future__ import annotations

import argparse
import json
import pathlib
import re
import shlex
import subprocess
import sys
from typing import Any

PROTECTED_PREFIXES = (
    ".github/",
    ".cursor/",
    "deploy/",
    "dev-rules/",
    "docs/approved/",
    "backend/migrations/",
    "backend/ent/",
    "ops/",
    "scripts/",
)
PROTECTED_FILES = {"AGENTS.md", "CLAUDE.md"}
TEST_PATH_RE = re.compile(r"(?:_test\.go$|(^|/)test_[^/]+\.py$|\.(?:test|spec)\.[cm]?[jt]sx?$)")
CODE_PATH_RE = re.compile(r"^(?:backend|frontend)/")
REPRO_CWD_RE = re.compile(r"^cd (backend|frontend) && (.+)$")


class ContractError(ValueError):
    pass


def load_object(path: pathlib.Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(value, dict):
        raise ContractError(f"{path} must contain a JSON object")
    return value


def normalize_paths(path: pathlib.Path) -> list[str]:
    values = []
    for raw in path.read_text(encoding="utf-8", errors="strict").splitlines():
        value = raw.strip()
        if not value:
            continue
        if ".." in pathlib.PurePosixPath(value).parts or value.startswith("/"):
            raise ContractError(f"changed path escapes repository: {value!r}")
        if any(ord(char) < 32 or ord(char) == 127 for char in value):
            raise ContractError("changed paths may not contain control characters")
        values.append(value)
    return sorted(set(values))


def reproduction_invocation(command: str) -> tuple[pathlib.Path, list[str]]:
    command = command.strip()
    if not command or len(command) > 500 or "\n" in command or "\r" in command:
        raise ContractError("reproduction_command is missing or malformed")

    cwd = pathlib.Path(".")
    match = REPRO_CWD_RE.fullmatch(command)
    if match:
        cwd = pathlib.Path(match.group(1))
        command = match.group(2)
    try:
        argv = shlex.split(command, posix=True)
    except ValueError as exc:
        raise ContractError("reproduction_command has invalid quoting") from exc
    if not argv:
        raise ContractError("reproduction_command is empty")
    if any(re.search(r"[;&|`<>]|\$\(", token) for token in argv):
        raise ContractError("reproduction_command contains a shell operator")

    allowed = False
    if argv[:2] == ["go", "test"]:
        allowed = True
    elif len(argv) >= 3 and argv[0] in {"python", "python3"} and argv[1:3] == ["-m", "unittest"]:
        allowed = True
    elif len(argv) >= 2 and argv[0] in {"python", "python3"} and argv[1].endswith(".py"):
        script = pathlib.PurePosixPath(argv[1])
        allowed = not script.is_absolute() and ".." not in script.parts
    elif argv[:2] in (["pnpm", "test"], ["npm", "test"]):
        allowed = True
    elif len(argv) >= 3 and argv[:2] == ["pnpm", "exec"] and argv[2] in {"vitest", "jest"}:
        allowed = True
    if not allowed:
        raise ContractError("reproduction_command is outside the test-command allowlist")
    return cwd, argv


def validate(candidate: dict[str, Any], result: dict[str, Any], changed: list[str], pr_body: str) -> None:
    signature = str(candidate.get("signature") or "")
    if not re.fullmatch(r"daily-error\|[0-9a-f]{16}", signature):
        raise ContractError("candidate signature is invalid")
    if not candidate.get("repair_eligible") or candidate.get("confidence") != "high":
        raise ContractError("candidate is not high-confidence and repair-eligible")
    if candidate.get("owner") != "platform" or int(candidate.get("status_code") or 0) < 500:
        raise ContractError("candidate is not a platform-owned final 5xx")

    if result.get("status") != "fixed":
        raise ContractError("agent result status must be fixed when files changed")
    if result.get("candidate_signature") != signature:
        raise ContractError("agent result does not match the selected candidate")
    before = int(result.get("before_exit_code") or 0)
    after = int(result.get("after_exit_code") if result.get("after_exit_code") is not None else -1)
    if before == 0 or after != 0:
        raise ContractError("reproduction evidence must show nonzero before and zero after")
    reproduction_invocation(str(result.get("reproduction_command") or ""))

    if not changed:
        raise ContractError("agent reported a fix without changed files")
    for value in changed:
        if value in PROTECTED_FILES or value.startswith(PROTECTED_PREFIXES):
            raise ContractError(f"agent modified protected path: {value}")
    if not any(TEST_PATH_RE.search(value) for value in changed):
        raise ContractError("repair diff must include a regression test")
    if not any(CODE_PATH_RE.search(value) and not TEST_PATH_RE.search(value) for value in changed):
        raise ContractError("repair diff must include backend or frontend implementation code")
    backend_changed = any(value.startswith("backend/") for value in changed)
    frontend_changed = any(value.startswith("frontend/") for value in changed)
    if backend_changed and not frontend_changed and not re.search(r"no-web-impact|Web impact:\s*none", pr_body, re.IGNORECASE):
        raise ContractError("backend-only repair PR body must include an explicit no-web-impact justification")

    for heading in ("## 摘要", "## 风险", "## 验证", "## 提交"):
        if heading not in pr_body:
            raise ContractError(f"PR body is missing {heading}")
    if signature not in pr_body:
        raise ContractError("PR body must carry the daily error signature")
    if not re.search(r"Draft|草案|人工", pr_body, re.IGNORECASE):
        raise ContractError("PR body must state that human review is required")


def main() -> int:
    parser = argparse.ArgumentParser()
    sub = parser.add_subparsers(dest="command", required=True)
    validate_parser = sub.add_parser("validate")
    validate_parser.add_argument("--candidate", required=True, type=pathlib.Path)
    validate_parser.add_argument("--result", required=True, type=pathlib.Path)
    validate_parser.add_argument("--changed-files", required=True, type=pathlib.Path)
    validate_parser.add_argument("--pr-body", required=True, type=pathlib.Path)
    run_parser = sub.add_parser("run-reproduction")
    run_parser.add_argument("--result", required=True, type=pathlib.Path)
    run_parser.add_argument("--repo-root", default=pathlib.Path("."), type=pathlib.Path)
    args = parser.parse_args()
    try:
        result = load_object(args.result)
        if args.command == "validate":
            validate(
                load_object(args.candidate),
                result,
                normalize_paths(args.changed_files),
                args.pr_body.read_text(encoding="utf-8"),
            )
        else:
            cwd, argv = reproduction_invocation(str(result.get("reproduction_command") or ""))
            completed = subprocess.run(argv, cwd=args.repo_root.resolve() / cwd, check=False)
            if completed.returncode != 0:
                raise ContractError(f"reproduction command failed with exit code {completed.returncode}")
    except (OSError, json.JSONDecodeError, ContractError, TypeError, ValueError) as exc:
        print(f"[ops-repair-contract] ERROR: {exc}", file=sys.stderr)
        return 2
    print("ops repair contract: ok")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
