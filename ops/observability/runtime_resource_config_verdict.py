#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import sys

# usw matches deploy/aws/stage0/docker-compose.yml. domestic matches
# deploy/domestic/compose.override.yml (smaller disk). Callers pass layout;
# an absent layout stays usw so the existing prod probe does not go quiet.
LOG_POLICIES = {
    "usw": {"driver": "json-file", "max_size": "100m", "max_file": "5"},
    "domestic": {"driver": "json-file", "max_size": "20m", "max_file": "3"},
}


def compute(snapshot: dict) -> dict:
    violations: list[dict[str, str]] = []
    layout = snapshot.get("layout") or "usw"
    policy = LOG_POLICIES.get(layout)
    if policy is None:
        return {
            "verdict": "unknown",
            "violations": [{
                "kind": "unknown_log_layout",
                "summary": f"runtime resource layout {layout!r} is not usw or domestic",
            }],
        }
    expected = f"json-file max-size={policy['max_size']} max-file={policy['max_file']}"
    logs = snapshot.get("docker_logs") or {}
    for owner in ("caddy", "app"):
        cfg = logs.get(owner) or {}
        if any(cfg.get(key) != value for key, value in policy.items()):
            violations.append(
                {
                    "kind": f"{owner}_log_policy_drift",
                    "summary": (
                        f"{owner} Docker logging differs from {layout} canonical {expected}"
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
    if result["verdict"] != "warning" or kinds != {"caddy_log_policy_drift", "redis_aof_disabled"}:
        return 1
    domestic = json.loads(json.dumps(bounded))
    domestic["layout"] = "domestic"
    domestic["docker_logs"]["caddy"]["max_size"] = "20m"
    domestic["docker_logs"]["caddy"]["max_file"] = "3"
    domestic["docker_logs"]["app"]["max_size"] = "20m"
    domestic["docker_logs"]["app"]["max_file"] = "3"
    if compute(domestic)["verdict"] != "green":
        return 1
    # A domestic host must not be judged against the usw 100m policy.
    if compute({**domestic, "layout": "usw"})["verdict"] == "green":
        return 1
    return 0 if compute({"layout": "edge"})["verdict"] == "unknown" else 1


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
