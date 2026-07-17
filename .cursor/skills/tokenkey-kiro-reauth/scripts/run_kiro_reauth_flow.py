#!/usr/bin/env python3
"""Orchestrate the full Kiro reauth flow with deterministic sub-steps.

This wrapper composes the bundled scripts instead of re-implementing their logic:
  1. local summary / admin payload
  2. pre-apply edge auth summary
  3. optional apply
  4. post-apply edge auth summary
  5. summary compare
  6. optional real Kiro request probe
"""

from __future__ import annotations

import argparse
import json
import os
import shlex
import subprocess
import sys
import tempfile
from pathlib import Path
from typing import Any


HERE = Path(__file__).resolve().parent
REPO_ROOT = HERE.parents[3]

LOCAL_CREDS = HERE / "local_kiro_credentials.py"
APPLY_EDGE = HERE / "apply_edge_kiro_oauth.py"
COMPARE = HERE / "compare_auth_summaries.py"
EDGE_SUMMARY = HERE / "probe_edge_auth_summary.sh"
REAL_PROBE = HERE / "probe_real_kiro_request.sh"
RUN_PROBE = REPO_ROOT / "ops" / "observability" / "run-probe.sh"
EDGE_RESOLVE = REPO_ROOT / "ops" / "stage0" / "edge_ssm_execution.py"


def run_command(
    argv: list[str],
    *,
    stdin_text: str | None = None,
    extra_env: dict[str, str] | None = None,
) -> tuple[int, str, str]:
    env = os.environ.copy()
    if extra_env:
        env.update(extra_env)
    proc = subprocess.run(
        argv,
        input=stdin_text,
        text=True,
        capture_output=True,
        env=env,
        cwd=str(REPO_ROOT),
    )
    return proc.returncode, proc.stdout, proc.stderr


def must_json(stdout: str, label: str) -> dict[str, Any]:
    try:
        parsed = json.loads(stdout)
    except json.JSONDecodeError as exc:
        raise SystemExit(f"{label} returned invalid JSON: {exc}\nstdout:\n{stdout[:1000]}")
    if not isinstance(parsed, dict):
        raise SystemExit(f"{label} returned non-object JSON")
    return parsed


def run_json(argv: list[str], *, stdin_text: str | None = None, label: str, extra_env: dict[str, str] | None = None) -> dict[str, Any]:
    code, stdout, stderr = run_command(argv, stdin_text=stdin_text, extra_env=extra_env)
    if code != 0:
        raise SystemExit(f"{label} failed with exit {code}\nstderr:\n{stderr}\nstdout:\n{stdout}")
    return must_json(stdout, label)


def normalize_edge_id(value: str) -> str:
    edge_id = value.strip()
    if edge_id.startswith("edge:"):
        edge_id = edge_id.removeprefix("edge:")
    if edge_id.startswith("edge-"):
        edge_id = edge_id.removeprefix("edge-")
    return edge_id


def default_admin_password_file(edge_id: str, explicit_path: str) -> str:
    if explicit_path:
        return explicit_path
    for candidate in (
        Path.home() / "Codes" / "keys" / f"tokenkey-{edge_id}-admin-password.txt",
        Path.home() / "Codes" / "keys" / f"tokenkey-edge-{edge_id}-admin-password.txt",
    ):
        if candidate.exists():
            return str(candidate)
    return ""


def require_probe_success(obj: dict[str, Any], label: str) -> None:
    if obj.get("error"):
        raise SystemExit(f"{label} returned error: {json.dumps(obj, ensure_ascii=False)}")


def resolve_edge(edge_id: str) -> dict[str, Any]:
    return run_json(
        [sys.executable, str(EDGE_RESOLVE), "--repo-root", ".", "--edge-id", edge_id, "--format", "json"],
        label="edge_resolve",
    )


def local_summary(refresh: bool) -> dict[str, Any]:
    argv = [sys.executable, str(LOCAL_CREDS), "--mode", "summary"]
    if refresh:
        argv.append("--refresh")
    return run_json(argv, label="local_summary")


def local_admin_payload(refresh: bool) -> dict[str, Any]:
    argv = [sys.executable, str(LOCAL_CREDS), "--mode", "admin-payload"]
    if refresh:
        argv.append("--refresh")
    return run_json(argv, label="local_admin_payload")


def edge_summary(edge_id: str, account_id: int | None, account_name: str) -> dict[str, Any]:
    argv = [
        "bash",
        str(RUN_PROBE),
        "--target",
        f"edge:{edge_id}",
        "--script",
        str(EDGE_SUMMARY),
        "--env",
        f"ACCOUNT_NAME={account_name}",
    ]
    if account_id is not None:
        argv.extend(["--env", f"ACCOUNT_ID={account_id}"])
    return run_json(argv, label="edge_summary")


def apply_edge(
    *,
    base_url: str,
    account_id: int,
    account_name: str,
    payload: dict[str, Any],
    admin_api_key: str,
    admin_email: str,
    admin_password: str,
    admin_password_file: str,
    ensure_schedulable: bool,
    dry_run: bool,
) -> dict[str, Any]:
    argv = [
        sys.executable,
        str(APPLY_EDGE),
        "--base-url",
        base_url,
        "--account-id",
        str(account_id),
        "--expected-account-name",
        account_name,
        "--payload-file",
        "-",
    ]
    if ensure_schedulable:
        argv.append("--ensure-schedulable")
    if dry_run:
        argv.append("--dry-run")
    if admin_api_key:
        argv.extend(["--admin-api-key", admin_api_key])
    if admin_email:
        argv.extend(["--admin-email", admin_email])
    if admin_password:
        argv.extend(["--admin-password", admin_password])
    if admin_password_file:
        argv.extend(["--admin-password-file", admin_password_file])
    return run_json(argv, stdin_text=json.dumps(payload), label="apply_edge")


def compare_summaries(local_obj: dict[str, Any], edge_obj: dict[str, Any]) -> dict[str, Any]:
    with tempfile.TemporaryDirectory(prefix="kiro-compare-") as tmpdir:
        local_path = Path(tmpdir) / "local.json"
        edge_path = Path(tmpdir) / "edge.json"
        local_path.write_text(json.dumps(local_obj, ensure_ascii=False), encoding="utf-8")
        edge_path.write_text(json.dumps(edge_obj, ensure_ascii=False), encoding="utf-8")
        return run_json(
            [
                sys.executable,
                str(COMPARE),
                "--local",
                str(local_path),
                "--edge",
                str(edge_path),
            ],
            label="compare_summaries",
        )


def real_probe(edge_id: str, account_id: int, group_name: str, model: str, prompt_text: str, log_window: str) -> dict[str, Any]:
    argv = [
        "bash",
        str(RUN_PROBE),
        "--target",
        f"edge:{edge_id}",
        "--script",
        str(REAL_PROBE),
        "--env",
        f"ACCOUNT_ID={account_id}",
        "--env",
        f"GROUP_NAME={group_name}",
        "--env",
        f"MODEL={model}",
        "--env",
        f"PROMPT_TEXT={prompt_text}",
        "--env",
        f"LOG_WINDOW={log_window}",
    ]
    return run_json(argv, label="real_probe")


def render_plan(
    args: argparse.Namespace,
    edge_info: dict[str, Any],
    *,
    input_edge_id: str,
    account_id: int,
    account_name: str,
    admin_password_file: str,
) -> dict[str, Any]:
    return {
        "input_edge_id": input_edge_id,
        "edge_id": args.edge_id,
        "base_url": args.base_url or f"https://{edge_info['domain']}",
        "account_id": args.account_id,
        "resolved_account_id": account_id,
        "account_name": args.account_name,
        "resolved_account_name": account_name,
        "local_refresh": args.local_refresh,
        "do_apply": args.apply,
        "do_real_probe": args.verify_real_request,
        "ensure_schedulable": args.ensure_schedulable,
        "admin_password_file": admin_password_file,
        "group_name": args.group_name,
        "model": args.model,
        "log_window": args.log_window,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--edge-id", default="")
    parser.add_argument("--account-id", type=int, default=None, help="optional when --account-name uniquely identifies a Kiro OAuth account")
    parser.add_argument("--account-name", default="")
    parser.add_argument("--base-url", default="", help="override base URL; defaults to https://<resolved-domain>")

    parser.add_argument("--local-refresh", action="store_true", help="mint a fresh local Kiro access token first")
    parser.add_argument("--apply", action="store_true", help="apply local credentials to the edge")
    parser.add_argument("--verify-real-request", action="store_true", help="run a real Kiro /v1/messages request after compare")
    parser.add_argument("--ensure-schedulable", action="store_true", help="force schedulable=true after apply if needed")
    parser.add_argument("--plan-only", action="store_true", help="resolve targets and print the intended plan only")

    parser.add_argument("--group-name", default="kiro")
    parser.add_argument("--model", default="claude-opus-4-8")
    parser.add_argument("--prompt-text", default="Say hello in one short sentence.")
    parser.add_argument("--log-window", default="3m")

    parser.add_argument("--admin-api-key", default="")
    parser.add_argument("--admin-email", default="")
    parser.add_argument("--admin-password", default="")
    parser.add_argument("--admin-password-file", default="")
    parser.add_argument("shorthand", nargs="*", help="optional shorthand: <account-name> <edge-id>")
    args = parser.parse_args()

    if args.shorthand:
        if len(args.shorthand) != 2:
            raise SystemExit("shorthand usage is: <account-name> <edge-id>")
        args.account_name = args.account_name or args.shorthand[0]
        args.edge_id = args.edge_id or args.shorthand[1]

    if not args.edge_id:
        raise SystemExit("--edge-id is required, or use shorthand: <account-name> <edge-id>")
    if not args.account_name:
        raise SystemExit("--account-name is required, or use shorthand: <account-name> <edge-id>")

    input_edge_id = args.edge_id
    args.edge_id = normalize_edge_id(args.edge_id)
    args.admin_password_file = default_admin_password_file(args.edge_id, args.admin_password_file)

    edge_info = resolve_edge(args.edge_id)
    base_url = args.base_url or f"https://{edge_info['domain']}"

    pre_edge = edge_summary(args.edge_id, args.account_id, args.account_name)
    require_probe_success(pre_edge, "edge_summary")
    resolved_account_id = int(pre_edge["id"])
    resolved_account_name = str(pre_edge["name"])

    output: dict[str, Any] = {
        "edge": edge_info,
        "plan": render_plan(
            args,
            edge_info,
            input_edge_id=input_edge_id,
            account_id=resolved_account_id,
            account_name=resolved_account_name,
            admin_password_file=args.admin_password_file,
        ),
    }

    if args.plan_only:
        output["resolved_edge_summary"] = pre_edge
        json.dump(output, sys.stdout, ensure_ascii=False, indent=2)
        sys.stdout.write("\n")
        return 0

    local_summary_obj = local_summary(args.local_refresh)
    output["local_summary"] = local_summary_obj

    output["pre_apply_edge_summary"] = pre_edge

    if args.apply:
        payload = local_admin_payload(args.local_refresh)
        apply_result = apply_edge(
            base_url=base_url,
            account_id=resolved_account_id,
            account_name=resolved_account_name,
            payload=payload,
            admin_api_key=args.admin_api_key,
            admin_email=args.admin_email,
            admin_password=args.admin_password,
            admin_password_file=args.admin_password_file,
            ensure_schedulable=args.ensure_schedulable,
            dry_run=False,
        )
        output["apply_result"] = apply_result

    compare_edge = pre_edge
    output["compare_edge_source"] = "pre_apply_edge_summary"
    if args.apply:
        post_edge = edge_summary(args.edge_id, resolved_account_id, resolved_account_name)
        require_probe_success(post_edge, "post_apply_edge_summary")
        output["post_apply_edge_summary"] = post_edge
        compare_edge = post_edge
        output["compare_edge_source"] = "post_apply_edge_summary"
    output["compare"] = compare_summaries(local_summary_obj, compare_edge)

    if args.verify_real_request:
        output["real_request_probe"] = real_probe(
            args.edge_id,
            resolved_account_id,
            args.group_name,
            args.model,
            args.prompt_text,
            args.log_window,
        )

    json.dump(output, sys.stdout, ensure_ascii=False, indent=2)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
