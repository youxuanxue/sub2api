#!/usr/bin/env python3
"""Monthly Kiro OAuth recovery and live verification, delivered through SSM.

The module is intentionally stdlib-only so run-probe.sh can ship it to a live
host.  It is also importable for offline tests; importing it performs no remote
I/O.
"""

from __future__ import annotations

import argparse
import concurrent.futures
import datetime as dt
import json
import os
import pathlib
import re
import subprocess
from typing import Any
from zoneinfo import ZoneInfo


DEFAULT_MODEL = "claude-sonnet-4.5"
APPLY_GUARD = "yes-kiro-monthly-recovery"
MAX_WORKERS_CAP = 8
BEIJING = ZoneInfo("Asia/Shanghai")


def failure_class(verdict: str, http_status: Any, error: str | None = None) -> str | None:
    """Map probe outcomes to a bounded operator-facing failure category."""
    if verdict in {"servable", "dry_run"}:
        return None if verdict == "servable" else "dry_run"
    if verdict in {"probe_timeout", "probe_transport_error", "inconclusive_transport"}:
        return "transport"
    if verdict in {"setup_error", "probe_no_result", "catalog_unavailable"}:
        return "configuration"
    if verdict in {"recover_failed", "enable_failed"}:
        return "runtime_state"
    if http_status in {401, 403}:
        return "oauth"
    if http_status in {402, 429}:
        return "quota"
    if verdict == "upstream_rejected" or (isinstance(http_status, int) and http_status >= 500):
        return "upstream"
    return "unknown"


def classify_probe_output(raw: str, account_id: int, model: str) -> dict[str, Any]:
    """Extract one bounded, credential-free result from probe JSON lines."""
    rows: list[dict[str, Any]] = []
    for line in raw.splitlines():
        try:
            value = json.loads(line)
        except json.JSONDecodeError:
            continue
        if isinstance(value, dict):
            rows.append(value)

    catalog = next((row for row in rows if row.get("verdict") == "catalog_snapshot"), None)
    probe = next(
        (
            row
            for row in rows
            if row.get("model") == model
            and row.get("verdict")
            in {"servable", "upstream_rejected", "inconclusive_transport", "setup_error"}
        ),
        None,
    )
    if probe is None:
        probe = next((row for row in rows if row.get("model") == model), None)

    if probe is None:
        details = [
            str(row.get("error") or row.get("verdict"))
            for row in rows
            if row.get("verdict") in {"catalog_unavailable", "setup_error"}
        ]
        probe = {
            "verdict": "probe_no_result",
            "http_status": None,
            "error": "; ".join(details)[:240] or "no structured probe result",
        }

    error = re.sub(r"\s+", " ", str(probe.get("error") or "")).strip()[:240]
    verdict = str(probe.get("verdict") or "probe_no_result")
    http_status = probe.get("http_status")
    return {
        "account_id": account_id,
        "model": model,
        "verdict": verdict,
        "http_status": http_status,
        "catalog_has_model": bool(catalog and model in (catalog.get("models") or [])),
        "error": error or None,
        "failure_class": failure_class(verdict, http_status, error),
    }


def summarize_results(results: list[dict[str, Any]]) -> dict[str, Any]:
    counts: dict[str, int] = {}
    for result in results:
        verdict = str(result.get("verdict") or "unknown")
        counts[verdict] = counts.get(verdict, 0) + 1
    failures = [
        result
        for result in results
        if result.get("verdict") != "servable"
    ]
    dry_run = bool(results) and all(result.get("verdict") == "dry_run" for result in results)
    if dry_run:
        verdict = "dry_run"
        failures = []
    else:
        verdict = "ok" if not failures else "partial_failure"
    return {
        "schema_version": 1,
        "verdict": verdict,
        "model": results[0].get("model") if results else DEFAULT_MODEL,
        "total": len(results),
        "counts": counts,
        "failures": failures,
        "results": results,
    }


def _run(command: list[str], *, check: bool = True, timeout: int = 60) -> subprocess.CompletedProcess[str]:
    return subprocess.run(command, check=check, text=True, capture_output=True, timeout=timeout)


def _container(function_name: str) -> str:
    command = [
        "bash",
        "-lc",
        f". /tmp/resolve-app-container.sh && {function_name} auto",
    ]
    return _run(command, timeout=30).stdout.strip()


def _psql(postgres: str, sql: str) -> str:
    result = _run(
        [
            "docker",
            "exec",
            postgres,
            "psql",
            "-U",
            "tokenkey",
            "-d",
            "tokenkey",
            "-X",
            "-A",
            "-t",
            "-v",
            "ON_ERROR_STOP=1",
            "-c",
            sql,
        ],
        timeout=60,
    )
    return result.stdout.strip()


def _admin_post(app: str, admin_key: str, path: str, body: dict[str, Any]) -> tuple[bool, str]:
    result = subprocess.run(
        [
            "docker",
            "exec",
            app,
            "wget",
            "-qO-",
            "--timeout=45",
            f"--header=x-api-key: {admin_key}",
            "--header=Content-Type: application/json",
            f"--post-data={json.dumps(body, separators=(',', ':'))}",
            f"http://127.0.0.1:8080/api/v1/admin{path}",
        ],
        text=True,
        capture_output=True,
        timeout=60,
        check=False,
    )
    if result.returncode == 0:
        return True, ""
    return False, "admin request failed"


def _probe_one(account_id: int, model: str, timeout: int) -> dict[str, Any]:
    env = dict(os.environ)
    env.update(
        {
            "ACCOUNT_ID": str(account_id),
            "MODELS": model,
            "REQUEST_TIMEOUT_SECONDS": str(timeout),
        }
    )
    result = subprocess.run(
        ["bash", "/tmp/probe_kiro_upstream_models.sh"],
        text=True,
        capture_output=True,
        timeout=timeout + 30,
        check=False,
        env=env,
    )
    return classify_probe_output(result.stdout + "\n" + result.stderr, account_id, model)


def _month_key() -> str:
    return dt.datetime.now(BEIJING).strftime("%Y-%m")


def _marker_path(month: str) -> pathlib.Path:
    return pathlib.Path("/var/lib/tokenkey/kiro-monthly-recovery") / f"{month}.json"


def _load_ids(postgres: str) -> list[int]:
    raw = _psql(
        postgres,
        "SELECT id FROM accounts WHERE platform='kiro' AND type='oauth' "
        "AND deleted_at IS NULL ORDER BY id;",
    )
    return [int(line) for line in raw.splitlines() if line.strip().isdigit()]


def _recover_and_probe(
    account_id: int,
    app: str,
    admin_key: str,
    model: str,
    timeout: int,
    apply: bool,
) -> dict[str, Any]:
    base = {"account_id": account_id, "model": model}
    if not apply:
        return {**base, "verdict": "dry_run"}

    try:
        recovered, recover_error = _admin_post(
            app, admin_key, f"/accounts/{account_id}/recover-state", {}
        )
    except (OSError, subprocess.SubprocessError) as exc:
        recovered, recover_error = False, f"recover-state transport failure: {type(exc).__name__}"
    if not recovered:
        try:
            disabled, _ = _admin_post(
                app, admin_key, f"/accounts/{account_id}/schedulable", {"schedulable": False}
            )
        except (OSError, subprocess.SubprocessError):
            disabled = False
        return {
            **base,
            "verdict": "recover_failed",
            "failure_class": "runtime_state",
            "error": recover_error or "recover-state failed",
            "disabled": disabled,
        }

    try:
        result = _probe_one(account_id, model, timeout)
    except subprocess.TimeoutExpired:
        result = {
            **base,
            "verdict": "probe_timeout",
            "http_status": None,
            "catalog_has_model": False,
            "error": "probe timed out",
            "failure_class": "transport",
        }
    except (OSError, subprocess.SubprocessError) as exc:
        result = {
            **base,
            "verdict": "probe_transport_error",
            "http_status": None,
            "catalog_has_model": False,
            "error": f"probe transport failure: {type(exc).__name__}",
            "failure_class": "transport",
        }
    if result.get("verdict") == "servable":
        try:
            enabled, enable_error = _admin_post(
                app, admin_key, f"/accounts/{account_id}/schedulable", {"schedulable": True}
            )
        except (OSError, subprocess.SubprocessError) as exc:
            enabled, enable_error = False, f"schedulable transport failure: {type(exc).__name__}"
        if not enabled:
            result["verdict"] = "enable_failed"
            result["failure_class"] = "runtime_state"
            result["error"] = enable_error or "failed to restore schedulable state"
            result["enabled"] = False
        else:
            result["enabled"] = True
    if result.get("verdict") != "servable":
        try:
            disabled, _ = _admin_post(
                app, admin_key, f"/accounts/{account_id}/schedulable", {"schedulable": False}
            )
        except (OSError, subprocess.SubprocessError):
            disabled = False
        result["disabled"] = disabled
    return result


def run(*, mode: str, model: str, timeout: int, max_workers: int, force: bool) -> dict[str, Any]:
    apply = mode == "apply"
    if mode not in {"apply", "dry-run"}:
        raise SystemExit("MODE must be apply or dry-run")
    if apply and os.environ.get("APPLY") != APPLY_GUARD:
        raise SystemExit("APPLY guard is required for apply mode")

    month = _month_key()
    started_at = dt.datetime.now(dt.UTC).replace(microsecond=0).isoformat().replace("+00:00", "Z")
    target_at = f"{month}-01T00:00:05Z"
    marker = _marker_path(month)
    if apply and marker.exists() and not force:
        return {
            "schema_version": 1,
            "verdict": "already_complete",
            "month": month,
            "marker": str(marker),
            "started_at": started_at,
            "target_at": target_at,
        }

    postgres = _container("tk_require_postgres_container")
    ids = _load_ids(postgres)
    if not ids:
        result = {
            "schema_version": 1,
            "verdict": "no_accounts",
            "month": month,
            "target": os.environ.get("TARGET_NAME", "unknown"),
            "total": 0,
            "results": [],
            "started_at": started_at,
            "target_at": target_at,
        }
        if apply:
            marker.parent.mkdir(parents=True, exist_ok=True)
            marker.write_text(json.dumps(result, ensure_ascii=False), encoding="utf-8")
        return result

    effective_workers = min(max(1, max_workers), MAX_WORKERS_CAP)
    if not apply:
        results = [{"account_id": account_id, "model": model, "verdict": "dry_run"} for account_id in ids]
        summary = summarize_results(results)
        summary.update(
            {
                "month": month,
                "target": os.environ.get("TARGET_NAME", "unknown"),
                "max_workers": effective_workers,
                "started_at": started_at,
                "target_at": target_at,
            }
        )
        return summary

    app = _container("tk_resolve_app_container")
    admin_key = _psql(postgres, "SELECT value FROM settings WHERE key='admin_api_key' LIMIT 1;")
    if not admin_key:
        raise SystemExit("admin api key unavailable")

    def worker(account_id: int) -> dict[str, Any]:
        return _recover_and_probe(account_id, app, admin_key, model, timeout, apply)

    with concurrent.futures.ThreadPoolExecutor(max_workers=effective_workers) as executor:
        results = list(executor.map(worker, ids))
    results.sort(key=lambda value: int(value["account_id"]))
    summary = summarize_results(results)
    summary.update(
        {
            "month": month,
            "target": os.environ.get("TARGET_NAME", "unknown"),
            "max_workers": effective_workers,
            "started_at": started_at,
            "target_at": target_at,
        }
    )
    if apply and summary["verdict"] == "ok":
        marker.parent.mkdir(parents=True, exist_ok=True)
        marker.write_text(json.dumps(summary, ensure_ascii=False), encoding="utf-8")
    return summary


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--mode", choices=("apply", "dry-run"), default=os.environ.get("MODE", "apply"))
    parser.add_argument("--model", default=os.environ.get("MODEL", DEFAULT_MODEL))
    parser.add_argument("--timeout", type=int, default=int(os.environ.get("REQUEST_TIMEOUT_SECONDS", "60")))
    parser.add_argument("--max-workers", type=int, default=int(os.environ.get("MAX_WORKERS", "4")))
    parser.add_argument("--force", action="store_true", default=os.environ.get("FORCE", "false") == "true")
    args = parser.parse_args()
    result = run(
        mode=args.mode,
        model=args.model,
        timeout=max(1, args.timeout),
        max_workers=max(1, args.max_workers),
        force=args.force,
    )
    print(json.dumps(result, ensure_ascii=False, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
