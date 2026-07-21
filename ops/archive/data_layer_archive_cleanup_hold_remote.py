#!/usr/bin/env python3
"""Remote host implementation for the production archive cleanup hold."""

from __future__ import annotations

import argparse
import copy
import datetime as dt
import hashlib
import json
import subprocess
import sys
import urllib.error
import urllib.request
from collections.abc import Iterable
from typing import Any


ADMIN_URL = "https://api.tokenkey.dev/api/v1/admin/ops/advanced-settings"
HOLD_CONFIRMATION = "tokenkey-prod-archive-cleanup-hold-v1"
RELEASE_CONFIRMATION = "tokenkey-prod-archive-cleanup-release-v1"
PG_CONTAINER = "tokenkey-postgres"
APP_CONTAINERS = ("tokenkey", "tokenkey-blue", "tokenkey-green")
TIMEOUT_SECONDS = 20


class HoldError(RuntimeError):
    """Fail-closed cleanup hold error."""


def _canonical_json(value: Any) -> str:
    return json.dumps(value, ensure_ascii=True, separators=(",", ":"), sort_keys=True)


def _sha256(value: Any) -> str:
    return hashlib.sha256(_canonical_json(value).encode("utf-8")).hexdigest()


def _run(
    command: list[str], *, timeout: int = TIMEOUT_SECONDS, include_stderr: bool = False
) -> str:
    try:
        completed = subprocess.run(
            command,
            capture_output=True,
            text=True,
            timeout=timeout,
            check=False,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise HoldError(f"command failed to execute: {command[0]}") from exc
    if completed.returncode != 0:
        detail = (completed.stderr or completed.stdout or "command failed").strip()
        raise HoldError(f"command failed ({command[0]}): {detail[:300]}")
    return completed.stdout + (completed.stderr if include_stderr else "")


def _psql(sql: str) -> list[str]:
    pgoptions = (
        "-c default_transaction_read_only=on "
        "-c lock_timeout=100ms "
        f"-c statement_timeout={TIMEOUT_SECONDS}s"
    )
    output = _run(
        [
            "docker",
            "exec",
            "-i",
            "-e",
            f"PGOPTIONS={pgoptions}",
            PG_CONTAINER,
            "psql",
            "-U",
            "tokenkey",
            "-d",
            "tokenkey",
            "-X",
            "-q",
            "-t",
            "-A",
            "-P",
            "pager=off",
            "-v",
            "ON_ERROR_STOP=1",
            "-c",
            f"BEGIN READ ONLY; {sql}; COMMIT;",
        ]
    )
    return [line for line in output.splitlines() if line.strip()]


def _admin_key() -> str:
    lines = _psql("SELECT value FROM settings WHERE key='admin_api_key' LIMIT 1")
    if len(lines) != 1 or not lines[0].strip():
        raise HoldError("admin_api_key is unavailable")
    return lines[0].strip()


def _admin_request(method: str, key: str, body: dict[str, Any] | None = None) -> dict[str, Any]:
    encoded = None if body is None else _canonical_json(body).encode("utf-8")
    request = urllib.request.Request(
        ADMIN_URL,
        data=encoded,
        method=method,
        headers={
            "x-api-key": key,
            "Content-Type": "application/json",
            "Accept": "application/json",
        },
    )
    try:
        with urllib.request.urlopen(request, timeout=TIMEOUT_SECONDS) as response:
            status = response.status
            raw = response.read(256 * 1024)
    except (OSError, urllib.error.URLError) as exc:
        raise HoldError(f"admin API {method} failed") from exc
    if status != 200:
        raise HoldError(f"admin API {method} returned HTTP {status}")
    try:
        envelope = json.loads(raw)
        data = envelope["data"]
    except (KeyError, TypeError, json.JSONDecodeError) as exc:
        raise HoldError(f"admin API {method} returned an invalid envelope") from exc
    if envelope.get("code") != 0 or not isinstance(data, dict):
        raise HoldError(f"admin API {method} refused the request")
    retention = data.get("data_retention")
    if not isinstance(retention, dict) or not isinstance(
        retention.get("cleanup_enabled"), bool
    ):
        raise HoldError("advanced settings are missing data_retention.cleanup_enabled")
    return data


def _database_state() -> dict[str, Any]:
    sql = """
SELECT json_build_object(
  'server_clock', to_char(clock_timestamp() AT TIME ZONE 'UTC',
    'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
  'database_cleanup_enabled',
    COALESCE((s.value::jsonb #>> '{data_retention,cleanup_enabled}')::boolean, false),
  'last_cleanup_run_at', (
    SELECT to_char(last_run_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
    FROM ops_job_heartbeats WHERE job_name='ops_cleanup'
  ),
  'last_cleanup_success_at', (
    SELECT to_char(last_success_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
    FROM ops_job_heartbeats WHERE job_name='ops_cleanup'
  ),
  'last_cleanup_error_at', (
    SELECT to_char(last_error_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
    FROM ops_job_heartbeats WHERE job_name='ops_cleanup'
  ),
  'last_cleanup_result', (
    SELECT last_result FROM ops_job_heartbeats WHERE job_name='ops_cleanup'
  )
)::text
FROM settings s
WHERE s.key='ops_advanced_settings'
LIMIT 1
""".strip()
    lines = _psql(sql)
    if len(lines) != 1:
        raise HoldError("ops_advanced_settings database state is unavailable")
    try:
        state = json.loads(lines[0])
    except json.JSONDecodeError as exc:
        raise HoldError("cleanup database state is invalid") from exc
    if not isinstance(state, dict) or not isinstance(
        state.get("database_cleanup_enabled"), bool
    ):
        raise HoldError("cleanup database state is incomplete")
    return state


def _cleanup_lock_active() -> bool:
    owner = _run(
        [
            "docker",
            "exec",
            "tokenkey-redis",
            "redis-cli",
            "--raw",
            "GET",
            "ops:cleanup:leader",
        ],
        timeout=10,
    ).strip()
    return bool(owner)


def _read_state() -> dict[str, Any]:
    key = _admin_key()
    settings = _admin_request("GET", key)
    database = _database_state()
    api_enabled = settings["data_retention"]["cleanup_enabled"]
    database_enabled = database["database_cleanup_enabled"]
    if api_enabled != database_enabled:
        raise HoldError("admin API and database cleanup settings disagree")
    return {
        **database,
        "api_cleanup_enabled": api_enabled,
        "cleanup_lock_active": _cleanup_lock_active(),
        "settings_sha256": _sha256(settings),
        "_admin_key": key,
        "_settings": settings,
    }


def _public_state(state: dict[str, Any]) -> dict[str, Any]:
    return {key: value for key, value in state.items() if not key.startswith("_")}


def _app_containers() -> list[str]:
    names = [
        line.strip()
        for line in _run(["docker", "ps", "--format", "{{.Names}}"], timeout=10).splitlines()
        if line.strip()
    ]
    selected = [name for name in names if name in APP_CONTAINERS]
    if not selected:
        raise HoldError("running TokenKey application container is unavailable")
    return selected


def _reload_proof(since: str, *, enabled: bool) -> bool:
    logs = ""
    for container in _app_containers():
        logs += _run(
            ["docker", "logs", "--since", since, container],
            timeout=TIMEOUT_SECONDS,
            include_stderr=True,
        )
    if "cleanup reload after advanced-settings update failed" in logs:
        raise HoldError("cleanup runtime reload reported an error")
    marker = "[OpsCleanup] scheduled" if enabled else "[OpsCleanup] cron disabled by settings"
    return marker in logs


def _runtime_disabled_since(hold_started_at: str) -> bool:
    try:
        started = dt.datetime.fromisoformat(hold_started_at.replace("Z", "+00:00"))
    except (TypeError, ValueError) as exc:
        raise HoldError("cleanup hold timestamp is invalid") from exc
    since = (started - dt.timedelta(minutes=5)).isoformat().replace("+00:00", "Z")
    disabled_marker = "[OpsCleanup] cron disabled by settings"
    scheduled_marker = "[OpsCleanup] scheduled"
    reload_error = "cleanup reload after advanced-settings update failed"
    for container in _app_containers():
        logs = _run(
            ["docker", "logs", "--since", since, container],
            timeout=TIMEOUT_SECONDS,
            include_stderr=True,
        )
        disabled_at = logs.rfind(disabled_marker)
        if disabled_at < 0 or logs.rfind(scheduled_marker) > disabled_at:
            return False
        if logs.rfind(reload_error) > disabled_at:
            return False
    return True


def _hold_status(state: dict[str, Any], *, hold_started_at: str | None) -> dict[str, Any]:
    active = (
        state["api_cleanup_enabled"] is False
        and state["database_cleanup_enabled"] is False
        and state["cleanup_lock_active"] is False
    )
    no_cleanup_after_hold: bool | None = None
    if hold_started_at:
        try:
            started = dt.datetime.fromisoformat(hold_started_at.replace("Z", "+00:00"))
            server_clock = dt.datetime.fromisoformat(
                str(state["server_clock"]).replace("Z", "+00:00")
            )
            cleanup_events = [
                dt.datetime.fromisoformat(str(raw).replace("Z", "+00:00"))
                for raw in (
                    state.get("last_cleanup_run_at"),
                    state.get("last_cleanup_success_at"),
                    state.get("last_cleanup_error_at"),
                )
                if raw is not None
            ]
        except (TypeError, ValueError) as exc:
            raise HoldError("cleanup hold timestamps are invalid") from exc
        if started > server_clock:
            raise HoldError("cleanup hold receipt is from the future")
        no_cleanup_after_hold = not cleanup_events or max(cleanup_events) <= started
    return {
        **_public_state(state),
        "hold_active": active,
        "no_cleanup_after_hold": no_cleanup_after_hold,
    }


def _verified_hold_status(
    state: dict[str, Any], *, hold_started_at: str
) -> dict[str, Any]:
    status = _hold_status(state, hold_started_at=hold_started_at)
    if not status["hold_active"]:
        raise HoldError("cleanup hold is not active")
    if status["no_cleanup_after_hold"] is not True:
        raise HoldError("cleanup ran after the archive hold started")
    if not _runtime_disabled_since(hold_started_at):
        raise HoldError("cleanup runtime disable is not currently proven")
    return status


def plan() -> dict[str, Any]:
    return {
        "mode": "prod_archive_cleanup_hold_plan",
        "environment": "prod",
        **_hold_status(_read_state(), hold_started_at=None),
        "required_confirmation": HOLD_CONFIRMATION,
        "settings_mutated": False,
        "deletion_authorized": False,
    }


def apply_hold(confirmation: str) -> dict[str, Any]:
    if confirmation != HOLD_CONFIRMATION:
        raise HoldError("cleanup hold confirmation token is invalid")
    before = _read_state()
    if before["api_cleanup_enabled"] is not True:
        raise HoldError("cleanup hold is already active; verify the existing receipt")
    started_at = dt.datetime.now(dt.timezone.utc).isoformat().replace("+00:00", "Z")
    updated = copy.deepcopy(before["_settings"])
    updated["data_retention"]["cleanup_enabled"] = False
    response = _admin_request("PUT", before["_admin_key"], updated)
    if response["data_retention"]["cleanup_enabled"] is not False:
        raise HoldError("admin API did not disable cleanup")
    after = _read_state()
    reload_proven = _reload_proof(started_at, enabled=False)
    status = _hold_status(after, hold_started_at=started_at)
    if not status["hold_active"] or not reload_proven:
        raise HoldError("cleanup hold is persisted but runtime disable was not proven")
    return {
        "mode": "prod_archive_cleanup_hold",
        "action": "apply",
        "environment": "prod",
        **status,
        "hold_started_at": started_at,
        "previous_cleanup_enabled": before["api_cleanup_enabled"],
        "settings_sha256_before": before["settings_sha256"],
        "settings_sha256_after": after["settings_sha256"],
        "reload_proven": True,
        "settings_mutated": before["settings_sha256"] != after["settings_sha256"],
        "deletion_authorized": False,
    }


def verify_hold(hold_started_at: str) -> dict[str, Any]:
    status = _verified_hold_status(_read_state(), hold_started_at=hold_started_at)
    return {
        "mode": "prod_archive_cleanup_hold_verify",
        "environment": "prod",
        **status,
        "hold_started_at": hold_started_at,
        "runtime_disabled_proven": True,
        "settings_mutated": False,
        "deletion_authorized": False,
    }


def release_hold(
    confirmation: str,
    *,
    previous_cleanup_enabled: bool,
    hold_started_at: str,
) -> dict[str, Any]:
    if confirmation != RELEASE_CONFIRMATION:
        raise HoldError("cleanup release confirmation token is invalid")
    before = _read_state()
    status = _verified_hold_status(before, hold_started_at=hold_started_at)
    if not previous_cleanup_enabled:
        return {
            "mode": "prod_archive_cleanup_hold_release",
            "environment": "prod",
            **status,
            "restored_cleanup_enabled": False,
            "reload_proven": True,
            "settings_mutated": False,
            "deletion_authorized": False,
        }
    started_at = dt.datetime.now(dt.timezone.utc).isoformat().replace("+00:00", "Z")
    updated = copy.deepcopy(before["_settings"])
    updated["data_retention"]["cleanup_enabled"] = True
    response = _admin_request("PUT", before["_admin_key"], updated)
    if response["data_retention"]["cleanup_enabled"] is not True:
        raise HoldError("admin API did not restore cleanup")
    after = _read_state()
    reload_proven = _reload_proof(started_at, enabled=True)
    if after["api_cleanup_enabled"] is not True or not reload_proven:
        raise HoldError("cleanup release was not proven active")
    return {
        "mode": "prod_archive_cleanup_hold_release",
        "environment": "prod",
        **_public_state(after),
        "restored_cleanup_enabled": True,
        "reload_proven": True,
        "settings_mutated": True,
        "deletion_authorized": False,
    }


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    commands.add_parser("plan")
    apply_parser = commands.add_parser("apply")
    apply_parser.add_argument("--confirm", required=True)
    verify_parser = commands.add_parser("verify")
    verify_parser.add_argument("--hold-started-at", required=True)
    release_parser = commands.add_parser("release")
    release_parser.add_argument("--confirm", required=True)
    release_parser.add_argument(
        "--previous-cleanup-enabled", choices=("true", "false"), required=True
    )
    release_parser.add_argument("--hold-started-at", required=True)
    return parser


def main(argv: Iterable[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        if args.command == "plan":
            payload = plan()
        elif args.command == "apply":
            payload = apply_hold(args.confirm)
        elif args.command == "verify":
            payload = verify_hold(args.hold_started_at)
        elif args.command == "release":
            payload = release_hold(
                args.confirm,
                previous_cleanup_enabled=args.previous_cleanup_enabled == "true",
                hold_started_at=args.hold_started_at,
            )
        else:  # pragma: no cover
            raise HoldError(f"unsupported command: {args.command}")
        print(_canonical_json(payload))
    except HoldError as exc:
        print(f"production archive cleanup hold refused: {exc}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
