#!/usr/bin/env python3
"""Query pnpm advisories with bounded retries and fail-closed output handling."""
from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import time
from pathlib import Path
from typing import Callable, Sequence


RunCommand = Callable[..., subprocess.CompletedProcess[str]]
Sleep = Callable[[float], None]


def parse_audit_report(raw: str) -> tuple[dict | None, str]:
    try:
        data = json.loads(raw)
    except (TypeError, json.JSONDecodeError) as exc:
        return None, f"response is not valid JSON ({exc})"

    if not isinstance(data, dict):
        return None, "response is not a JSON object"

    vulnerabilities = data.get("advisories")
    if not isinstance(vulnerabilities, dict):
        vulnerabilities = data.get("vulnerabilities")
    if not isinstance(vulnerabilities, dict) or not isinstance(data.get("metadata"), dict):
        detail = data.get("message") or data.get("error")
        suffix = f": {detail}" if detail else ""
        return None, f"response lacks vulnerability and metadata objects{suffix}"

    return data, ""


def run_audit(
    output: Path,
    *,
    attempts: int,
    retry_delay_seconds: float,
    timeout_seconds: float,
    pnpm_bin: str = "pnpm",
    runner: RunCommand = subprocess.run,
    sleeper: Sleep = time.sleep,
) -> int:
    output.unlink(missing_ok=True)
    temporary_output = output.with_name(f".{output.name}.tmp")
    temporary_output.unlink(missing_ok=True)

    command: Sequence[str] = (
        pnpm_bin,
        "audit",
        "--registry=https://registry.npmjs.org/",
        "--prod",
        "--audit-level=high",
        "--json",
    )
    env = os.environ.copy()
    env.update(
        {
            "npm_config_fetch_retries": "0",
            "npm_config_fetch_timeout": str(int(timeout_seconds * 1000)),
        }
    )

    last_error = "audit did not run"
    for attempt in range(1, attempts + 1):
        try:
            completed = runner(
                command,
                capture_output=True,
                text=True,
                timeout=timeout_seconds,
                check=False,
                env=env,
            )
            report, parse_error = parse_audit_report(completed.stdout)
            if report is not None:
                output.parent.mkdir(parents=True, exist_ok=True)
                temporary_output.write_text(
                    json.dumps(report, ensure_ascii=True, indent=2) + "\n",
                    encoding="utf-8",
                )
                temporary_output.replace(output)
                return 0

            stderr = completed.stderr.strip().splitlines()
            stderr_detail = f"; stderr: {stderr[-1][:500]}" if stderr else ""
            last_error = f"{parse_error}; exit={completed.returncode}{stderr_detail}"
        except subprocess.TimeoutExpired:
            last_error = f"request exceeded {timeout_seconds:g}s timeout"

        if attempt < attempts:
            print(
                f"::warning title=pnpm audit::attempt {attempt}/{attempts} failed: "
                f"{last_error}; retrying",
                file=sys.stderr,
            )
            sleeper(retry_delay_seconds * attempt)

    temporary_output.unlink(missing_ok=True)
    print(
        f"::error title=pnpm audit::all {attempts} attempts failed: {last_error}",
        file=sys.stderr,
    )
    return 1


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--attempts", type=int, default=3)
    parser.add_argument("--retry-delay-seconds", type=float, default=5)
    parser.add_argument("--timeout-seconds", type=float, default=60)
    parser.add_argument("--pnpm-bin", default="pnpm")
    args = parser.parse_args()

    if args.attempts < 1:
        parser.error("--attempts must be at least 1")
    if args.retry_delay_seconds < 0:
        parser.error("--retry-delay-seconds must be non-negative")
    if args.timeout_seconds <= 0:
        parser.error("--timeout-seconds must be positive")

    return run_audit(
        args.output,
        attempts=args.attempts,
        retry_delay_seconds=args.retry_delay_seconds,
        timeout_seconds=args.timeout_seconds,
        pnpm_bin=args.pnpm_bin,
    )


if __name__ == "__main__":
    raise SystemExit(main())
