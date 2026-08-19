#!/usr/bin/env python3
"""Guard the load-bearing scheduled edge-health scan and delivery call sites."""
from __future__ import annotations

import pathlib
import re
import subprocess
import sys


REPO = pathlib.Path(__file__).resolve().parents[2]
WORKFLOW = REPO / ".github" / "workflows" / "edge-health-watch.yml"
RESOLVE = REPO / "deploy" / "aws" / "stage0" / "resolve-edge-target.py"


def _workflow_int(text: str, pattern: str, label: str) -> int:
    match = re.search(pattern, text, flags=re.MULTILINE)
    if match is None:
        raise ValueError(f"cannot resolve {label}")
    return int(match.group(1))


def main() -> int:
    try:
        text = WORKFLOW.read_text(encoding="utf-8")
    except OSError as exc:
        print(f"FAIL: edge-health-watch workflow unavailable: {exc}", file=sys.stderr)
        return 1

    checks = {
        "scheduled trigger": re.search(r"(?m)^\s+schedule:\s*$", text) is not None,
        "five-minute cadence": 'cron: "2,7,12,17,22,27,32,37,42,47,52,57 * * * *"' in text,
        "OIDC credentials": "aws-actions/configure-aws-credentials@" in text,
        "fleet scan call site": "bash ops/observability/scan-edge-health.sh --with-prod" in text,
        "structured terminal scan": "--alert-json > terminal-buckets.jsonl" in text,
        "scan status propagated": "|| scan_status=$?" in text and 'exit "$scan_status"' in text,
        "model-unit evaluator call site": "python3 ops/observability/edge_model_health_alert.py" in text,
        "delivery owner call site": "python3 ops/observability/edge_health_delivery.py" in text,
        "structured state path": "STATEFILE=.edge-health-state/state.json" in text,
        "structured state delivery": '--state-file "$STATEFILE"' in text,
        "state restore": "actions/cache/restore@" in text,
        "state save": "actions/cache/save@" in text,
        "dry-run state guard": "inputs.dry_run != 'true'" in text,
        "single-instance concurrency": "group: edge-health-watch" in text and "cancel-in-progress: false" in text,
        "legacy account verdict removed": "edge-health-alert.py" not in text,
    }
    failures = [name for name, passed in checks.items() if not passed]
    if failures:
        for name in failures:
            print(f"FAIL: edge-health-watch contract missing {name}", file=sys.stderr)
        return 1

    scan_pos = text.index("bash ops/observability/scan-edge-health.sh --with-prod")
    evaluator_pos = text.index("python3 ops/observability/edge_model_health_alert.py")
    delivery_pos = text.index("python3 ops/observability/edge_health_delivery.py")
    save_pos = text.index("actions/cache/save@")
    if not scan_pos < evaluator_pos < delivery_pos < save_pos:
        print("FAIL: edge-health-watch order must be scan -> evaluator -> delivery -> state save", file=sys.stderr)
        return 1

    try:
        job_timeout = _workflow_int(text, r"^\s+timeout-minutes:\s*(\d+)\s*$", "job timeout")
        ssm_timeout = _workflow_int(
            text, r'^\s+SCAN_PROBE_TIMEOUT:\s*"(\d+)"\s*$', "SSM timeout"
        )
        https_timeout = _workflow_int(
            text, r'^\s+SCAN_HTTPS_TIMEOUT:\s*"(\d+)"\s*$', "HTTPS timeout"
        )
        resolved = subprocess.run(
            [sys.executable, str(RESOLVE), "--list-deployable"],
            check=True,
            capture_output=True,
            text=True,
        ).stdout.splitlines()
    except (ValueError, OSError, subprocess.CalledProcessError) as exc:
        print(f"FAIL: edge-health-watch timeout contract unavailable: {exc}", file=sys.stderr)
        return 1

    target_count = len([edge for edge in resolved if edge.strip()]) + 1  # include prod
    setup_headroom_seconds = 120
    required_seconds = target_count * (ssm_timeout + https_timeout) + setup_headroom_seconds
    if required_seconds >= job_timeout * 60:
        print(
            "FAIL: edge-health-watch timeout cannot cover current targets: "
            f"required={required_seconds}s budget={job_timeout * 60}s",
            file=sys.stderr,
        )
        return 1

    print("edge-health-watch contract: ok")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
