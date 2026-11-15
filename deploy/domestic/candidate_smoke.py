#!/usr/bin/env python3
"""Probe a prepared cn-prod candidate and write the promote smoke report.

`deploy_via_ssm.sh candidate-smoke` runs this on the host after `prepare`.
It does not change the public route. Failed cases stay in the report.

Single-container mode (`--container tokenkey --port 18080 --revision SHA`) is
used by rolling.py on app nodes. With `--expect-draining` the node was started
behind the start-draining marker, so /health must be 503 "draining" and the
Docker healthcheck (which probes /health) cannot be healthy yet; process
liveness is checked instead. Blue/green mode is unchanged.
"""
from __future__ import annotations

import argparse
import json
import subprocess
import sys
from pathlib import Path

SMOKE_PATH = Path("/private/candidate-smoke.json")
PORTS = {"tokenkey-blue": 18081, "tokenkey-green": 18082}


def published_port(target: str) -> int:
    try:
        return PORTS[target]
    except KeyError:
        raise ValueError(f"unknown candidate color: {target}") from None


def evaluate(receipt: dict, inspected: dict, responses: dict[str, tuple[int, str]]) -> dict:
    target = receipt["target"]
    revision = receipt["revision"]
    port = published_port(target)
    health_code, health_body = responses[f"http://127.0.0.1:{port}/health"]
    settings_code, settings_body = responses[f"http://127.0.0.1:{port}/api/v1/settings/public"]
    label = (
        inspected.get("Config", {})
        .get("Labels", {})
        .get("org.opencontainers.image.revision", "")
    )
    docker_health = ((inspected.get("State") or {}).get("Health") or {}).get("Status") or "none"
    cases = [
        {
            "case": f"candidate /health on 127.0.0.1:{port}",
            "passed": health_code == 200 and '"status":"ok"' in health_body.replace(" ", ""),
            "http": health_code,
            "body": health_body[:200],
        },
        {
            "case": f"candidate /api/v1/settings/public on 127.0.0.1:{port}",
            "passed": settings_code == 200 and settings_body.lstrip().startswith("{"),
            "http": settings_code,
            "body": settings_body[:200],
        },
        {
            "case": "candidate image revision",
            "passed": bool(revision) and label == revision,
            "revision": label,
        },
        {
            "case": "candidate docker health",
            "passed": docker_health == "healthy",
            "health": docker_health,
        },
    ]
    return {"revision": revision, "container_id": inspected["Id"], "cases": cases}


def evaluate_single(container: str, port: int, revision: str, inspected: dict,
                    responses: dict[str, tuple[int, str]], expect_draining: bool) -> dict:
    health_code, health_body = responses[f"http://127.0.0.1:{port}/health"]
    live_code, _ = responses[f"http://127.0.0.1:{port}/health/live"]
    settings_code, settings_body = responses[f"http://127.0.0.1:{port}/api/v1/settings/public"]
    label = (inspected.get("Config", {}).get("Labels", {}) or {}).get("org.opencontainers.image.revision", "")
    state = inspected.get("State") or {}
    compact = health_body.replace(" ", "")
    if expect_draining:
        health_ok = health_code == 503 and '"status":"draining"' in compact
    else:
        health_ok = health_code == 200 and '"status":"ok"' in compact
    docker_health = (state.get("Health") or {}).get("Status") or "none"
    cases = [
        {
            "case": f"{container} /health on 127.0.0.1:{port} ({'draining' if expect_draining else 'serving'})",
            "passed": health_ok,
            "http": health_code,
            "body": health_body[:200],
        },
        {
            "case": f"{container} /health/live on 127.0.0.1:{port}",
            "passed": live_code == 200,
            "http": live_code,
        },
        {
            "case": f"{container} /api/v1/settings/public on 127.0.0.1:{port}",
            "passed": settings_code == 200 and settings_body.lstrip().startswith("{"),
            "http": settings_code,
            "body": settings_body[:200],
        },
        {
            "case": f"{container} image revision",
            "passed": bool(revision) and label == revision,
            "revision": label,
        },
        {
            # Docker's HEALTHCHECK probes /health, which is 503 while drained.
            "case": f"{container} process running",
            "passed": state.get("Running") is True and not state.get("Restarting")
            and (expect_draining or docker_health in ("healthy", "none")),
            "health": docker_health,
            "restart_count": inspected.get("RestartCount", 0),
        },
    ]
    return {"revision": revision, "container_id": inspected["Id"], "container": container,
            "draining": expect_draining, "cases": cases}


def probe_single(container: str, port: int, revision: str, expect_draining: bool) -> dict:
    inspected = _inspect(container)
    responses = {
        url: _http_get(url)
        for url in (
            f"http://127.0.0.1:{port}/health",
            f"http://127.0.0.1:{port}/health/live",
            f"http://127.0.0.1:{port}/api/v1/settings/public",
        )
    }
    return evaluate_single(container, port, revision, inspected, responses, expect_draining)


def report_passed(report: dict) -> bool:
    cases = report.get("cases") or []
    return bool(cases) and all(case.get("passed") is True for case in cases)


def _inspect(name: str) -> dict:
    raw = subprocess.check_output(["docker", "inspect", name], text=True)
    return json.loads(raw)[0]


def _http_get(url: str) -> tuple[int, str]:
    proc = subprocess.run(
        ["curl", "-sS", "--max-time", "10", "-w", "\n%{http_code}", url],
        text=True,
        capture_output=True,
        check=False,
    )
    if proc.returncode != 0 and not proc.stdout:
        return 0, (proc.stderr or "").strip()[:200]
    body, _, code = proc.stdout.rpartition("\n")
    try:
        status = int(code.strip())
    except ValueError:
        status = 0
    return status, body


def load_and_probe(state: Path) -> dict:
    receipt = json.loads((state / "prepared.json").read_text())
    port = published_port(receipt["target"])
    inspected = _inspect(receipt["target"])
    responses = {
        url: _http_get(url)
        for url in (
            f"http://127.0.0.1:{port}/health",
            f"http://127.0.0.1:{port}/api/v1/settings/public",
        )
    }
    return evaluate(receipt, inspected, responses)


def write_report(report: dict, out: Path) -> None:
    out.parent.mkdir(parents=True, exist_ok=True)
    text = json.dumps(report, ensure_ascii=False, indent=2) + "\n"
    out.write_text(text)
    out.chmod(0o600)
    return text


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="probe a prepared cn-prod candidate")
    parser.add_argument("--state", type=Path, help="blue/green release state dir")
    parser.add_argument("--container", help="single-container mode: container name")
    parser.add_argument("--port", type=int, help="single-container mode: loopback port")
    parser.add_argument("--revision", help="single-container mode: expected image revision")
    parser.add_argument("--expect-draining", action="store_true",
                        help="single-container mode: node started drained (/health 503)")
    parser.add_argument("--out", type=Path, default=SMOKE_PATH)
    args = parser.parse_args(argv)
    if args.container:
        if args.state or not args.port or not args.revision:
            parser.error("--container needs --port and --revision, and excludes --state")
        report = probe_single(args.container, args.port, args.revision, args.expect_draining)
    elif args.state:
        if args.port or args.revision or args.expect_draining:
            parser.error("--port/--revision/--expect-draining need --container")
        report = load_and_probe(args.state)
    else:
        parser.error("one of --state or --container is required")
    sys.stdout.write(write_report(report, args.out))
    return 0 if report_passed(report) else 1


if __name__ == "__main__":
    raise SystemExit(main())
