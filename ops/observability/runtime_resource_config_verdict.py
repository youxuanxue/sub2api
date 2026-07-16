#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import sys

CANONICAL_LOG_POLICY = {
    "driver": "json-file",
    "max_size": "100m",
    "max_file": "5",
}


def compute(snapshot: dict) -> dict:
    violations: list[dict[str, str]] = []
    logs = snapshot.get("docker_logs") or {}
    for owner in ("caddy", "app"):
        cfg = logs.get(owner) or {}
        if any(cfg.get(key) != value for key, value in CANONICAL_LOG_POLICY.items()):
            violations.append(
                {
                    "kind": f"{owner}_log_policy_drift",
                    "summary": (
                        f"{owner} Docker logging differs from canonical "
                        "json-file max-size=100m max-file=5"
                    ),
                }
            )

    redis = snapshot.get("redis") or {}
    if redis.get("appendonly") != "yes":
        violations.append({
            "kind": "redis_aof_disabled",
            "summary": "live Redis appendonly is not yes; runtime differs from canonical compose",
        })

    return {
        "verdict": "warning" if violations else "green",
        "violations": violations,
        "redis_maxmemory": redis.get("maxmemory"),
        "redis_maxmemory_policy": redis.get("maxmemory_policy"),
    }


def selftest() -> int:
    bounded = {
        "docker_logs": {
            "caddy": {"driver": "json-file", "max_size": "100m", "max_file": "5"},
            "app": {"driver": "json-file", "max_size": "100m", "max_file": "5"},
        },
        "redis": {"appendonly": "yes", "maxmemory": "0", "maxmemory_policy": "noeviction"},
    }
    if compute(bounded)["verdict"] != "green":
        return 1
    drifted = json.loads(json.dumps(bounded))
    drifted["docker_logs"]["caddy"]["max_size"] = "1g"
    drifted["docker_logs"]["caddy"]["max_file"] = "2"
    drifted["redis"]["appendonly"] = "no"
    result = compute(drifted)
    kinds = {item["kind"] for item in result["violations"]}
    return (
        0
        if result["verdict"] == "warning"
        and kinds == {"caddy_log_policy_drift", "redis_aof_disabled"}
        else 1
    )


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--selftest", action="store_true")
    args = parser.parse_args()
    if args.selftest:
        return selftest()
    try:
        snapshot = json.load(sys.stdin)
    except (json.JSONDecodeError, TypeError):
        print(json.dumps({"verdict": "unknown", "violations": [{"kind": "invalid_probe", "summary": "runtime resource probe did not emit valid JSON"}]}))
        return 0
    print(json.dumps(compute(snapshot), separators=(",", ":")))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
