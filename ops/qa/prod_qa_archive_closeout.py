#!/usr/bin/env python3
"""Run guarded QA archive inspect/verify/restore/repair commands on prod via SSM."""

from __future__ import annotations

import argparse
import datetime as dt
import json
import pathlib
import shlex
import subprocess
import sys
import time
from typing import Any

ROOT = pathlib.Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "ops" / "lib"))
import resolve_app_container  # noqa: E402

PROD_REGION = "us-east-1"
PROD_STACK = "tokenkey-prod-stage0"
REPAIR_CONFIRMATION_PREFIX = "tokenkey-prod-qa-archive-repair-v1"
RESTORE_CONFIRMATION_PREFIX = "tokenkey-prod-qa-archive-restore-v1"
SAFETY_SCHEMA = "qa-archive-safety-v1"
READ_COMMANDS = {"inspect", "verify", "repair-plan"}
POLL_ATTEMPTS = 100
POLL_INTERVAL_SECONDS = 3


class QAArchiveCloseoutError(RuntimeError):
    pass


def _parse_window(value: str) -> dt.datetime:
    try:
        parsed = dt.datetime.strptime(value, "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=dt.timezone.utc)
    except ValueError as exc:
        raise QAArchiveCloseoutError("window must be an exact UTC RFC3339 hour") from exc
    if parsed.minute or parsed.second or parsed.microsecond:
        raise QAArchiveCloseoutError("window must be an exact UTC RFC3339 hour")
    return parsed


def _window_token(prefix: str, window: dt.datetime) -> str:
    return f"{prefix}:{window.strftime('%Y-%m-%dT%H:%M:%SZ')}"


def _validate_restore_output(value: str) -> str:
    path = pathlib.PurePosixPath(value)
    root = pathlib.PurePosixPath("/app/data/qa_archive_restore")
    if (
        not path.is_absolute()
        or ".." in path.parts
        or "." in path.parts
        or path == root
        or path.parent != root
    ):
        raise QAArchiveCloseoutError(
            "restore output must be a child of /app/data/qa_archive_restore/"
        )
    return str(path)


def _safety_proof(window: dt.datetime, checked_at: dt.datetime) -> str:
    payload = {
        "schema_version": SAFETY_SCHEMA,
        "window_start": window.strftime("%Y-%m-%dT%H:%M:%SZ"),
        "checked_at": checked_at.strftime("%Y-%m-%dT%H:%M:%SZ"),
        "maintenance_disabled": True,
        "maintenance_inactive": True,
        "stale_cleanup_disabled": True,
        "stale_cleanup_inactive": True,
        "cleanup_runtime_disabled": True,
        "cleanup_lock_inactive": True,
    }
    return json.dumps(payload, sort_keys=True, separators=(",", ":"))


def _aws_json(args: list[str]) -> dict[str, Any]:
    completed = subprocess.run(args, capture_output=True, text=True, check=False)
    if completed.returncode != 0:
        detail = (completed.stderr or completed.stdout or "aws failed").strip()
        raise QAArchiveCloseoutError(detail[:600])
    try:
        payload = json.loads(completed.stdout)
    except json.JSONDecodeError as exc:
        raise QAArchiveCloseoutError("aws returned invalid JSON") from exc
    if not isinstance(payload, dict):
        raise QAArchiveCloseoutError("aws JSON is not an object")
    return payload


def _resolve_instance() -> str:
    payload = _aws_json([
        "aws", "cloudformation", "describe-stacks", "--region", PROD_REGION,
        "--stack-name", PROD_STACK, "--output", "json",
    ])
    stacks = payload.get("Stacks")
    if not isinstance(stacks, list) or not stacks:
        raise QAArchiveCloseoutError("prod stack missing")
    for item in stacks[0].get("Outputs", []):
        if isinstance(item, dict) and item.get("OutputKey") == "InstanceId":
            value = item.get("OutputValue")
            if isinstance(value, str) and value.startswith("i-"):
                return value
    raise QAArchiveCloseoutError("prod InstanceId missing")


def _timer_guard_shell() -> list[str]:
    return [
        'maintenance_file=$(sudo systemctl show tokenkey-qa-maintenance.timer -p UnitFileState --value) || { echo "repair refused: cannot read maintenance timer enablement" >&2; exit 41; }',
        'maintenance_active=$(sudo systemctl show tokenkey-qa-maintenance.timer -p ActiveState --value) || { echo "repair refused: cannot read maintenance timer state" >&2; exit 41; }',
        'stale_file=$(sudo systemctl show tokenkey-qa-stale-cleanup.timer -p UnitFileState --value) || { echo "repair refused: cannot read stale cleanup timer enablement" >&2; exit 42; }',
        'stale_active=$(sudo systemctl show tokenkey-qa-stale-cleanup.timer -p ActiveState --value) || { echo "repair refused: cannot read stale cleanup timer state" >&2; exit 42; }',
        'if [ "$maintenance_file:$maintenance_active" != "disabled:inactive" ]; then echo "repair refused: maintenance timer is not disabled/inactive" >&2; exit 41; fi',
        'if [ "$stale_file:$stale_active" != "disabled:inactive" ]; then echo "repair refused: stale cleanup timer is not disabled/inactive" >&2; exit 42; fi',
        'app_logs=$(sudo docker logs --since 24h "$APP_CONTAINER" 2>&1) || { echo "repair refused: cannot read app cleanup runtime logs" >&2; exit 43; }',
        'runtime_tail=$(printf "%s\\n" "$app_logs" | grep -E "cleanup_enabled=(true|false)|cleanup reload after advanced-settings update failed" | tail -1 || true)',
        'if ! printf "%s" "$runtime_tail" | grep -q "cleanup_enabled=false"; then echo "repair refused: cleanup runtime disabled state is unproven" >&2; exit 43; fi',
        'cleanup_lock=$(sudo docker exec tokenkey-redis redis-cli --raw GET ops:cleanup:leader) || { echo "repair refused: cannot read cleanup leader lock" >&2; exit 44; }',
        'if [ -n "$cleanup_lock" ]; then echo "repair refused: cleanup leader lock is active" >&2; exit 44; fi',
    ]


def _remote_command(command: str, window: dt.datetime, output: str, confirm: str) -> str:
    window_text = window.strftime("%Y-%m-%dT%H:%M:%SZ")
    cli = ["/app/qa-archive", command, "--window-start", window_text]
    script = [
        "set -euo pipefail",
        *resolve_app_container.remote_shell_snippet(docker="sudo docker"),
        'if [ -z "$APP_CONTAINER" ]; then echo "qa archive refused: ambiguous app container" >&2; exit 40; fi',
    ]
    if command == "restore":
        cli.extend(["--output", output, "--confirm", confirm])
    elif command == "repair-apply":
        script.extend(_timer_guard_shell())
        script.extend([
            "checked_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)",
            "proof_file=$(mktemp /run/tokenkey-qa-archive-proof.XXXXXX)",
            "trap 'rm -f -- \"$proof_file\"' EXIT",
            f"python3 -c {shlex.quote(_remote_proof_python(window_text))} \"$checked_at\" >\"$proof_file\"",
            "chmod 0444 \"$proof_file\"",
        ])
        cli.extend(["--confirm", confirm])
    script.extend([
        'image=$(sudo docker inspect --format \'{{.Image}}\' "$APP_CONTAINER")',
        'if [ -z "$image" ]; then echo "qa archive refused: active image unavailable" >&2; exit 45; fi',
        'sudo docker exec "$APP_CONTAINER" /bin/sh -c \'mkdir -p /app/data/qa_archive_tmp && chmod 700 /app/data/qa_archive_tmp\'',
        'env_file=$(mktemp /run/tokenkey-qa-archive-env.XXXXXX)',
        'chmod 600 "$env_file"',
        'if [ -n "${proof_file:-}" ]; then trap \'rm -f -- "$env_file" "$proof_file"\' EXIT; else trap \'rm -f -- "$env_file"\' EXIT; fi',
        'sudo docker inspect --format \'{{range .Config.Env}}{{println .}}{{end}}\' "$APP_CONTAINER" >"$env_file"',
    ])
    quoted_cli = " ".join(shlex.quote(item) for item in cli)
    proof_mount = (
        '--volume="$proof_file:/run/tokenkey-qa-archive-safety-proof.json:ro" '
        if command == "repair-apply"
        else ""
    )
    script.append(
        'sudo docker run --rm --name "tokenkey-qa-archive-op-$$" '
        '--user=1000:1000 --read-only --cap-drop=ALL --security-opt=no-new-privileges '
        '--memory=1g --memory-swap=1g --cpus=0.20 --pids-limit=128 '
        '--network="container:$APP_CONTAINER" --volumes-from="$APP_CONTAINER:rw" '
        f'{proof_mount}'
        '--env-file="$env_file" --env TMPDIR=/app/data/qa_archive_tmp '
        f'"$image" {quoted_cli}'
    )
    return "sudo bash -c " + shlex.quote("\n".join(script))


def _remote_proof_python(window_text: str) -> str:
    payload = {
        "schema_version": SAFETY_SCHEMA,
        "window_start": window_text,
        "maintenance_disabled": True,
        "maintenance_inactive": True,
        "stale_cleanup_disabled": True,
        "stale_cleanup_inactive": True,
        "cleanup_runtime_disabled": True,
        "cleanup_lock_inactive": True,
    }
    return (
        "import json,sys;"
        f"p={payload!r};"
        "p['checked_at']=sys.argv[1];"
        "print(json.dumps(p,sort_keys=True,separators=(',',':')))"
    )


def _poll(command_id: str, instance_id: str) -> str:
    for _ in range(POLL_ATTEMPTS):
        payload = _aws_json([
            "aws", "ssm", "get-command-invocation", "--region", PROD_REGION,
            "--command-id", command_id, "--instance-id", instance_id, "--output", "json",
        ])
        status = payload.get("Status")
        if status in {"Pending", "InProgress", "Delayed"}:
            time.sleep(POLL_INTERVAL_SECONDS)
            continue
        if status != "Success" or payload.get("ResponseCode") != 0:
            detail = str(payload.get("StandardErrorContent") or "").strip()
            raise QAArchiveCloseoutError(f"ssm failed status={status!r}: {detail[:600]}")
        stdout = payload.get("StandardOutputContent")
        if not isinstance(stdout, str):
            raise QAArchiveCloseoutError("ssm success without stdout")
        return stdout
    raise QAArchiveCloseoutError("ssm did not finish")


def _validate_receipt(stdout: str, command: str, window: dt.datetime) -> dict[str, Any]:
    lines = [line.strip() for line in stdout.splitlines() if line.strip()]
    if not lines:
        raise QAArchiveCloseoutError("remote command returned no receipt")
    try:
        payload = json.loads(lines[-1])
    except json.JSONDecodeError as exc:
        raise QAArchiveCloseoutError("remote receipt is invalid JSON") from exc
    expected_window = window.strftime("%Y-%m-%dT%H:%M:%SZ")
    if (
        not isinstance(payload, dict)
        or payload.get("ok") is not True
        or payload.get("command") != command
        or payload.get("window_start") != expected_window
        or payload.get("deletion_authorized") is not False
        or payload.get("cleanup_eligible") is not False
    ):
        raise QAArchiveCloseoutError("remote receipt failed safety validation")
    if command == "repair-apply" and (
        payload.get("cleanup_hold_active") is not True
        or payload.get("maintenance_timer_disabled") is not True
        or payload.get("maintenance_timer_inactive") is not True
        or payload.get("stale_cleanup_timer_disabled") is not True
        or payload.get("stale_cleanup_timer_inactive") is not True
        or payload.get("cleanup_runtime_disabled") is not True
        or payload.get("cleanup_lock_inactive") is not True
    ):
        raise QAArchiveCloseoutError("repair receipt lacks active hold evidence")
    return payload


def run(command: str, window_text: str, *, output: str = "", confirm: str = "") -> dict[str, Any]:
    window = _parse_window(window_text)
    if command not in READ_COMMANDS | {"restore", "repair-apply"}:
        raise QAArchiveCloseoutError(f"unsupported command {command!r}")
    if command == "restore":
        expected = _window_token(RESTORE_CONFIRMATION_PREFIX, window)
        output = _validate_restore_output(output)
        if confirm != expected:
            raise QAArchiveCloseoutError("window-bound restore confirmation required")
    if command == "repair-apply":
        expected = _window_token(REPAIR_CONFIRMATION_PREFIX, window)
        if confirm != expected:
            raise QAArchiveCloseoutError("window-bound repair confirmation required")
    instance_id = _resolve_instance()
    remote = _remote_command(command, window, output, confirm)
    payload = _aws_json([
        "aws", "ssm", "send-command", "--region", PROD_REGION,
        "--instance-ids", instance_id, "--document-name", "AWS-RunShellScript",
        "--comment", f"TokenKey QA archive {command}",
        "--parameters", json.dumps({"commands": [remote]}, separators=(",", ":")),
        "--output", "json",
    ])
    command_id = payload["Command"]["CommandId"]
    receipt = _validate_receipt(_poll(command_id, instance_id), command, window)
    return {"instance_id": instance_id, "command_id": command_id, "remote_receipt": receipt}


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=sorted(READ_COMMANDS | {"restore", "repair-apply"}))
    parser.add_argument("--window-start", required=True)
    parser.add_argument("--output", default="")
    parser.add_argument("--confirm", default="")
    args = parser.parse_args()
    try:
        result = run(args.command, args.window_start, output=args.output, confirm=args.confirm)
    except QAArchiveCloseoutError as exc:
        print(f"qa archive closeout refused: {exc}", file=sys.stderr)
        return 2
    print(json.dumps(result, ensure_ascii=True, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
