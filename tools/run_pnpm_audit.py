#!/usr/bin/env python3
"""Audit installed production dependencies through OSV with bounded retries."""
from __future__ import annotations

import argparse
import concurrent.futures
import json
import subprocess
import sys
import time
import urllib.parse
from pathlib import Path
from typing import Callable, Sequence


OSV_QUERY_URL = "https://api.osv.dev/v1/querybatch"
OSV_VULN_URL = "https://api.osv.dev/v1/vulns/"
OSV_BATCH_SIZE = 1000
MAX_RESPONSE_BYTES = 16 * 1024 * 1024
SEVERITIES = ("info", "low", "moderate", "high", "critical")

RunCommand = Callable[..., subprocess.CompletedProcess[str]]
JsonRequest = Callable[[str, object | None, float], object]
Sleep = Callable[[float], None]


class AuditRequestError(RuntimeError):
    """Raised when dependency inventory or vulnerability data is unusable."""


def request_json(url: str, data: object | None, timeout_seconds: float) -> object:
    command = [
        "curl",
        "--fail-with-body",
        "--silent",
        "--show-error",
        "--max-time",
        f"{timeout_seconds:g}",
        "--max-filesize",
        str(MAX_RESPONSE_BYTES),
        "--header",
        "Accept: application/json",
        "--header",
        "Content-Type: application/json",
        "--header",
        "User-Agent: TokenKey-frontend-audit/1",
    ]
    request_body = None
    if data is not None:
        command.extend(("--data-binary", "@-"))
        request_body = json.dumps(data, separators=(",", ":"))
    command.append(url)
    try:
        completed = subprocess.run(
            command,
            input=request_body,
            capture_output=True,
            text=True,
            timeout=timeout_seconds + 5,
            check=False,
        )
    except subprocess.TimeoutExpired as exc:
        raise AuditRequestError(f"OSV request exceeded {timeout_seconds:g}s") from exc
    if completed.returncode != 0:
        detail = completed.stderr.strip().splitlines()
        message = detail[-1][:500] if detail else f"curl exit={completed.returncode}"
        raise AuditRequestError(f"OSV request failed: {message}")

    if len(completed.stdout.encode("utf-8")) > MAX_RESPONSE_BYTES:
        raise AuditRequestError("OSV response exceeds the 16 MiB safety limit")
    try:
        return json.loads(completed.stdout)
    except json.JSONDecodeError as exc:
        raise AuditRequestError(f"OSV response is not valid JSON: {exc}") from exc


def parse_dependency_inventory(raw: str) -> list[tuple[str, str]]:
    try:
        roots = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise AuditRequestError(f"pnpm list did not return valid JSON: {exc}") from exc
    if not isinstance(roots, list):
        raise AuditRequestError("pnpm list response is not a JSON array")

    versions: dict[str, set[str]] = {}

    def visit(dependencies: object) -> None:
        if dependencies is None:
            return
        if not isinstance(dependencies, dict):
            raise AuditRequestError("pnpm dependency tree contains invalid dependencies")
        for name, node in dependencies.items():
            if not isinstance(name, str) or not isinstance(node, dict):
                raise AuditRequestError("pnpm dependency tree contains an invalid package")
            version = node.get("version")
            if not isinstance(version, str) or not version:
                raise AuditRequestError(f"pnpm dependency {name!r} has no version")
            versions.setdefault(name, set()).add(version)
            visit(node.get("dependencies"))

    for root in roots:
        if not isinstance(root, dict):
            raise AuditRequestError("pnpm list response contains an invalid root")
        visit(root.get("dependencies"))

    inventory = sorted(
        (name, version)
        for name, package_versions in versions.items()
        for version in package_versions
    )
    if not inventory:
        raise AuditRequestError("pnpm list returned an empty production dependency tree")
    return inventory


def query_osv(
    inventory: Sequence[tuple[str, str]],
    *,
    timeout_seconds: float,
    requester: JsonRequest,
) -> dict:
    matches: dict[str, dict[str, set[str]]] = {}
    for offset in range(0, len(inventory), OSV_BATCH_SIZE):
        batch = inventory[offset : offset + OSV_BATCH_SIZE]
        payload = {
            "queries": [
                {"package": {"name": name, "ecosystem": "npm"}, "version": version}
                for name, version in batch
            ]
        }
        response = requester(OSV_QUERY_URL, payload, timeout_seconds)
        if not isinstance(response, dict) or not isinstance(response.get("results"), list):
            raise AuditRequestError("OSV batch response lacks a results array")
        results = response["results"]
        if len(results) != len(batch):
            raise AuditRequestError("OSV batch response length does not match the query")

        for (name, version), result in zip(batch, results, strict=True):
            if not isinstance(result, dict):
                raise AuditRequestError("OSV batch response contains an invalid result")
            vulnerabilities = result.get("vulns", [])
            if not isinstance(vulnerabilities, list):
                raise AuditRequestError("OSV batch result contains invalid vulnerabilities")
            for vulnerability in vulnerabilities:
                vulnerability_id = (
                    vulnerability.get("id") if isinstance(vulnerability, dict) else None
                )
                if not isinstance(vulnerability_id, str) or not vulnerability_id:
                    raise AuditRequestError("OSV batch result contains a vulnerability without id")
                matches.setdefault(vulnerability_id, {}).setdefault(name, set()).add(version)

    advisories: dict[str, dict] = {}
    severity_counts = {severity: 0 for severity in SEVERITIES}

    def load_detail(vulnerability_id: str) -> tuple[str, dict]:
        encoded_id = urllib.parse.quote(vulnerability_id, safe="")
        detail = requester(f"{OSV_VULN_URL}{encoded_id}", None, timeout_seconds)
        if not isinstance(detail, dict) or detail.get("id") != vulnerability_id:
            raise AuditRequestError(f"OSV detail response is invalid for {vulnerability_id}")
        return vulnerability_id, detail

    vulnerability_ids = sorted(matches)
    with concurrent.futures.ThreadPoolExecutor(max_workers=8) as executor:
        details = dict(executor.map(load_detail, vulnerability_ids))

    for vulnerability_id in vulnerability_ids:
        detail = details[vulnerability_id]
        encoded_id = urllib.parse.quote(vulnerability_id, safe="")
        database_specific = detail.get("database_specific")
        severity = (
            database_specific.get("severity", "").lower()
            if isinstance(database_specific, dict)
            else ""
        )
        if severity not in severity_counts:
            raise AuditRequestError(f"OSV vulnerability {vulnerability_id} has no known severity")

        aliases = detail.get("aliases")
        aliases = aliases if isinstance(aliases, list) else []
        identifiers = [vulnerability_id, *[item for item in aliases if isinstance(item, str)]]
        github_advisory_id = next(
            (item for item in identifiers if item.startswith("GHSA-")), vulnerability_id
        )
        cves = [item for item in identifiers if item.startswith("CVE-")]
        packages = matches[vulnerability_id]
        for name in sorted(packages):
            advisory_key = vulnerability_id if len(packages) == 1 else f"{vulnerability_id}:{name}"
            advisories[advisory_key] = {
                "id": vulnerability_id,
                "module_name": name,
                "severity": severity,
                "github_advisory_id": github_advisory_id,
                "cves": cves,
                "title": detail.get("summary") or vulnerability_id,
                "url": f"https://osv.dev/vulnerability/{encoded_id}",
                "findings": [
                    {"version": version, "paths": []}
                    for version in sorted(packages[name])
                ],
            }
            severity_counts[severity] += 1

    dependency_count = len(inventory)
    return {
        "advisories": advisories,
        "metadata": {
            "vulnerabilities": severity_counts,
            "dependencies": dependency_count,
            "devDependencies": 0,
            "optionalDependencies": 0,
            "totalDependencies": dependency_count,
        },
    }


def run_audit(
    output: Path,
    *,
    attempts: int,
    retry_delay_seconds: float,
    timeout_seconds: float,
    pnpm_bin: str = "pnpm",
    runner: RunCommand = subprocess.run,
    requester: JsonRequest = request_json,
    sleeper: Sleep = time.sleep,
) -> int:
    output.unlink(missing_ok=True)
    temporary_output = output.with_name(f".{output.name}.tmp")
    temporary_output.unlink(missing_ok=True)

    try:
        completed = runner(
            (pnpm_bin, "list", "--prod", "--depth", "Infinity", "--json"),
            capture_output=True,
            text=True,
            timeout=timeout_seconds,
            check=False,
        )
        if completed.returncode != 0:
            stderr = completed.stderr.strip().splitlines()
            detail = stderr[-1][:500] if stderr else f"exit={completed.returncode}"
            raise AuditRequestError(f"pnpm list failed: {detail}")
        inventory = parse_dependency_inventory(completed.stdout)
    except (AuditRequestError, subprocess.TimeoutExpired) as exc:
        print(f"::error title=frontend audit::{exc}", file=sys.stderr)
        return 1

    last_error = "OSV audit did not run"
    for attempt in range(1, attempts + 1):
        try:
            report = query_osv(
                inventory,
                timeout_seconds=timeout_seconds,
                requester=requester,
            )
            output.parent.mkdir(parents=True, exist_ok=True)
            temporary_output.write_text(
                json.dumps(report, ensure_ascii=True, indent=2) + "\n",
                encoding="utf-8",
            )
            temporary_output.replace(output)
            return 0
        except AuditRequestError as exc:
            last_error = str(exc)

        if attempt < attempts:
            print(
                f"::warning title=frontend audit::attempt {attempt}/{attempts} failed: "
                f"{last_error}; retrying",
                file=sys.stderr,
            )
            sleeper(retry_delay_seconds * attempt)

    temporary_output.unlink(missing_ok=True)
    print(
        f"::error title=frontend audit::all {attempts} attempts failed: {last_error}",
        file=sys.stderr,
    )
    return 1


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--attempts", type=int, default=3)
    parser.add_argument("--retry-delay-seconds", type=float, default=5)
    parser.add_argument("--timeout-seconds", type=float, default=30)
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
