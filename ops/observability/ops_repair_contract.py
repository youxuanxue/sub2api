#!/usr/bin/env python3
"""Fail-closed validation for report-driven ops Draft PRs."""
from __future__ import annotations

import argparse
import json
import os
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
ALLOWED_CODE_PREFIXES = ("backend/internal/", "frontend/src/")
FRONTEND_TEST_PATH_RE = re.compile(r"\.(?:test|spec)\.[cm]?[jt]sx?$")
FRONTEND_CODE_PATH_RE = re.compile(r"\.(?:vue|[cm]?[jt]sx?)$")
REPRO_CWD_RE = re.compile(r"^cd (backend|frontend) && (.+)$")
SAFE_TOKEN_RE = re.compile(r"^[a-z0-9][a-z0-9_-]{0,79}$")
SAFE_ERROR_TYPES = {
    "api_error",
    "authentication_error",
    "billing_error",
    "content_filter_error",
    "forbidden_error",
    "invalid_request_error",
    "not_found_error",
    "overloaded_error",
    "rate_limit_error",
    "subscription_error",
    "upstream_error",
}
SAFE_PLATFORMS = {"anthropic", "antigravity", "gemini", "grok", "kiro", "newapi", "openai"}
SAFE_STATES = {"new", "regressed", "persistent"}
SENSITIVE_ENV_NAME_RE = re.compile(
    r"(?:token|secret|password|credential|authorization|access_key|private_key|api_key)",
    re.IGNORECASE,
)


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


def safe_token(value: Any, allowed: set[str] | None = None) -> str:
    token = str(value or "").strip().lower()
    if not SAFE_TOKEN_RE.fullmatch(token):
        return "other"
    if allowed is not None and token not in allowed:
        return "other"
    return token


def endpoint_family(value: Any) -> str:
    path = str(value or "").strip().lower().split("?", 1)[0]
    if path == "/v1/messages":
        return "messages"
    if path == "/v1/chat/completions":
        return "chat_completions"
    if path == "/v1/responses":
        return "responses"
    if path.startswith("/v1/images"):
        return "images"
    if path.startswith("/v1/videos"):
        return "videos"
    if ":generatecontent" in path or ":streamgeneratecontent" in path:
        return "generate_content"
    if path.startswith("/api/"):
        return "admin_api"
    return "other"


def test_environment() -> dict[str, str]:
    return {
        key: value
        for key, value in os.environ.items()
        if not SENSITIVE_ENV_NAME_RE.search(key)
    }


def prompt_candidate(candidate: dict[str, Any]) -> dict[str, Any]:
    signature = str(candidate.get("signature") or "")
    if not re.fullmatch(r"daily-error\|[0-9a-f]{16}", signature):
        raise ContractError("candidate signature is invalid")
    if (
        not candidate.get("repair_eligible")
        or candidate.get("confidence") != "high"
        or candidate.get("owner") != "platform"
        or candidate.get("phase") != "internal"
        or int(candidate.get("status_code") or 0) < 500
    ):
        raise ContractError("candidate is not an internal platform-owned final 5xx")
    return {
        "schema_version": 1,
        "signature": signature,
        "target_id": safe_token(candidate.get("target_id")),
        "repair_eligible": True,
        "confidence": "high",
        "owner": "platform",
        "phase": "internal",
        "error_type": safe_token(candidate.get("error_type"), SAFE_ERROR_TYPES),
        "platform": safe_token(candidate.get("platform"), SAFE_PLATFORMS),
        "endpoint_family": endpoint_family(candidate.get("endpoint")),
        "status_code": int(candidate.get("status_code") or 0),
        "state": safe_token(candidate.get("state"), SAFE_STATES),
        "current_count": max(0, int(candidate.get("current_count") or 0)),
        "previous_count": max(0, int(candidate.get("previous_count") or 0)),
        "active_days_7d": max(0, int(candidate.get("active_days_7d") or 0)),
        "max_count_5m": max(0, int(candidate.get("max_count_5m") or 0)),
    }


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

    if cwd == pathlib.Path("backend") and argv[:2] == ["go", "test"]:
        allowed_flags = {"-count=1", "-tags=unit"}
        packages = []
        index = 2
        saw_run = False
        while index < len(argv):
            token = argv[index]
            if token == "-run":
                index += 1
                if index >= len(argv) or not re.fullmatch(r"[A-Za-z0-9_^$/.|()*+?-]{1,160}", argv[index]):
                    raise ContractError("go reproduction has an invalid -run filter")
                saw_run = True
            elif token.startswith("-") and token not in allowed_flags:
                raise ContractError(f"go reproduction flag is not allowed: {token}")
            elif not token.startswith("-"):
                package = pathlib.PurePosixPath(token)
                if (
                    not re.fullmatch(r"\./internal/[A-Za-z0-9_./-]+", token)
                    or ".." in package.parts
                ):
                    raise ContractError("go reproduction must target backend/internal packages")
                packages.append(token.rstrip("/"))
            index += 1
        if len(packages) != 1:
            raise ContractError("go reproduction must target exactly one backend/internal package")
        if not saw_run:
            raise ContractError("go reproduction must include a focused -run filter")
    elif cwd == pathlib.Path("frontend") and argv[:4] == ["pnpm", "exec", "vitest", "run"]:
        if len(argv) != 5:
            raise ContractError("frontend reproduction must name exactly one test file")
        test_path = pathlib.PurePosixPath(argv[4])
        if (
            test_path.is_absolute()
            or ".." in test_path.parts
            or not str(test_path).startswith("src/")
            or not FRONTEND_TEST_PATH_RE.search(str(test_path))
        ):
            raise ContractError("frontend reproduction test path is invalid")
    else:
        raise ContractError("reproduction_command is outside the focused test-command allowlist")
    return cwd, argv


def validate(candidate: dict[str, Any], result: dict[str, Any], changed: list[str], pr_body: str) -> None:
    signature = str(candidate.get("signature") or "")
    if not re.fullmatch(r"daily-error\|[0-9a-f]{16}", signature):
        raise ContractError("candidate signature is invalid")
    if not candidate.get("repair_eligible") or candidate.get("confidence") != "high":
        raise ContractError("candidate is not high-confidence and repair-eligible")
    if (
        candidate.get("owner") != "platform"
        or candidate.get("phase") != "internal"
        or int(candidate.get("status_code") or 0) < 500
    ):
        raise ContractError("candidate is not an internal platform-owned final 5xx")

    if result.get("status") != "fixed":
        raise ContractError("agent result status must be fixed when files changed")
    if result.get("candidate_signature") != signature:
        raise ContractError("agent result does not match the selected candidate")
    before = int(result.get("before_exit_code") or 0)
    after = int(result.get("after_exit_code") if result.get("after_exit_code") is not None else -1)
    if before == 0 or after != 0:
        raise ContractError("reproduction evidence must show nonzero before and zero after")
    repro_cwd, repro_argv = reproduction_invocation(str(result.get("reproduction_command") or ""))

    if not changed:
        raise ContractError("agent reported a fix without changed files")
    for value in changed:
        if value in PROTECTED_FILES or value.startswith(PROTECTED_PREFIXES):
            raise ContractError(f"agent modified protected path: {value}")
        if not value.startswith(ALLOWED_CODE_PREFIXES):
            raise ContractError(f"agent modified path outside backend/internal or frontend/src: {value}")
    if repro_cwd == pathlib.Path("backend"):
        if not all(value.startswith("backend/internal/") for value in changed):
            raise ContractError("backend reproduction may only accompany backend/internal changes")
        tests = [value for value in changed if value.endswith("_test.go")]
        implementations = [value for value in changed if value.endswith(".go") and not value.endswith("_test.go")]
        package = next(token for token in repro_argv[2:] if token.startswith("./internal/"))
        expected_prefix = "backend/" + package.removeprefix("./").rstrip("/") + "/"
        if not any(value.startswith(expected_prefix) for value in tests):
            raise ContractError("go reproduction package must contain a changed regression test")
        if not implementations:
            raise ContractError("backend repair diff must include Go implementation code")
    else:
        if not all(value.startswith("frontend/src/") for value in changed):
            raise ContractError("frontend reproduction may only accompany frontend/src changes")
        tests = [value for value in changed if FRONTEND_TEST_PATH_RE.search(value)]
        implementations = [
            value
            for value in changed
            if FRONTEND_CODE_PATH_RE.search(value) and not FRONTEND_TEST_PATH_RE.search(value)
        ]
        expected_test = "frontend/" + repro_argv[4]
        if expected_test not in tests:
            raise ContractError("vitest reproduction must name a changed regression test")
        if not implementations:
            raise ContractError("frontend repair diff must include implementation code")
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
    command_parser = sub.add_parser("run-command")
    command_parser.add_argument("--command", dest="reproduction_command", required=True)
    prompt_parser = sub.add_parser("prompt-candidate")
    prompt_parser.add_argument("--candidate", required=True, type=pathlib.Path)
    prompt_parser.add_argument("--output", required=True, type=pathlib.Path)
    args = parser.parse_args()
    try:
        if args.command == "validate":
            result = load_object(args.result)
            validate(
                load_object(args.candidate),
                result,
                normalize_paths(args.changed_files),
                args.pr_body.read_text(encoding="utf-8"),
            )
        elif args.command == "run-reproduction":
            result = load_object(args.result)
            cwd, argv = reproduction_invocation(str(result.get("reproduction_command") or ""))
            completed = subprocess.run(
                argv,
                cwd=args.repo_root.resolve() / cwd,
                env=test_environment(),
                check=False,
            )
            if completed.returncode != 0:
                raise ContractError(f"reproduction command failed with exit code {completed.returncode}")
        elif args.command == "run-command":
            cwd, argv = reproduction_invocation(args.reproduction_command)
            repo_root = pathlib.Path(__file__).resolve().parents[2]
            return subprocess.run(
                argv,
                cwd=repo_root / cwd,
                env=test_environment(),
                check=False,
            ).returncode
        else:
            args.output.write_text(
                json.dumps(prompt_candidate(load_object(args.candidate)), indent=2, sort_keys=True) + "\n",
                encoding="utf-8",
            )
    except (OSError, json.JSONDecodeError, ContractError, TypeError, ValueError) as exc:
        print(f"[ops-repair-contract] ERROR: {exc}", file=sys.stderr)
        return 2
    print("ops repair contract: ok")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
