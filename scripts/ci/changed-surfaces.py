#!/usr/bin/env python3
"""Classify changed paths into the expensive CI surfaces they can affect."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import sys
from typing import Iterable


KEYS = (
    "backend",
    "frontend",
    "deploy",
    "ops",
    "contracts",
    "service_unit_cold",
    "all",
)

SERVICE_UNIT_COLD_FILES = {
    ".github/workflows/backend-ci.yml",
    ".new-api-ref",
    "backend/Makefile",
    "backend/go.mod",
    "backend/go.sum",
    "scripts/ci/list_go_tests.go",
    "scripts/ci/test_unit_test_runner.py",
    "scripts/ci/unit_test_runner.py",
}

CONTRACT_FILES = {
    "backend/internal/domain/constants.go",
    "backend/internal/server/router.go",
    "docs/agent_integration.md",
    "scripts/export_agent_contract.py",
    "scripts/export_agent_contract_routes.go",
    "scripts/test_export_agent_contract.py",
    "ops/pricing/modelops.py",
    "ops/pricing/manage-account-model-mapping-runtime.py",
    "ops/archive/data_layer_archive_cleanup_hold.py",
    "ops/archive/data_layer_archive_prod_export.py",
    "ops/archive/data_layer_archive_promote_batch.py",
    "ops/archive/data_layer_archive_closeout.py",
    "ops/migration/usage_logs_daily_partition.py",
}

NEUTRAL_TOP_LEVEL = {
    ".gitignore",
    ".gitattributes",
    ".main-ancestry-anchor",
    "AGENTS.md",
    "CLAUDE.md",
    "DEPRECATIONS.md",
    "LICENSE",
    "README.md",
}


def _starts(path: str, prefixes: Iterable[str]) -> bool:
    return any(path.startswith(prefix) for prefix in prefixes)


def classify(paths: Iterable[str]) -> dict[str, bool]:
    result = {key: False for key in KEYS}
    for raw_path in paths:
        path = raw_path.replace("\\", "/")
        if not path:
            continue

        if path in SERVICE_UNIT_COLD_FILES or (
            path.startswith("backend/") and path.endswith(".go")
        ):
            result["service_unit_cold"] = True

        if path == ".github/workflows/backend-ci.yml" or _starts(
            path,
            (".github/actions/", "scripts/ci/"),
        ):
            result["all"] = True
            continue

        matched = False

        if path == ".new-api-ref" or _starts(path, ("backend/",)):
            result["backend"] = True
            matched = True

        if _starts(path, ("frontend/",)):
            result["frontend"] = True
            matched = True

        if path == "backend/internal/domain/constants.go":
            result["frontend"] = True

        if _starts(path, ("deploy/", "ops/stage0/", "ops/lightsail/")) or (
            path.startswith(".github/workflows/")
            and Path(path).name.startswith(("deploy-", "edge-", "ops-stage0-", "warm-image-stage0"))
        ):
            result["deploy"] = True
            matched = True

        if _starts(
            path,
            ("ops/qa/", "ops/archive/", "ops/stage0/", "backend/internal/observability/qa/"),
        ) or path == "scripts/checks/data-layer-archive-ssot.py":
            result["ops"] = True
            matched = True

        if path in CONTRACT_FILES or _starts(path, ("backend/internal/server/routes/",)):
            result["contracts"] = True
            matched = True

        if path == "scripts/preflight.sh":
            result["ops"] = True
            result["contracts"] = True
            matched = True

        if matched:
            continue

        if (
            path in NEUTRAL_TOP_LEVEL
            or _starts(
                path,
                (
                    ".cache/",
                    "docs/",
                    ".cursor/",
                    ".testing/",
                    "dev-rules/",
                    "ops/",
                    "scripts/checks/",
                    "scripts/sentinels/",
                ),
            )
            or path.endswith((".md", ".txt"))
        ):
            continue

        result["all"] = True

    return result


def _read_paths(null_delimited: bool) -> list[str]:
    if null_delimited:
        return [item.decode("utf-8") for item in sys.stdin.buffer.read().split(b"\0") if item]
    return [line.rstrip("\n") for line in sys.stdin]


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--all", action="store_true", help="mark every expensive surface as required")
    parser.add_argument("--null", action="store_true", help="read NUL-delimited paths from stdin")
    parser.add_argument("--github-output", type=Path, help="append boolean outputs to this GitHub output file")
    args = parser.parse_args(argv)

    result = {key: True for key in KEYS} if args.all else classify(_read_paths(args.null))
    if args.github_output:
        with args.github_output.open("a", encoding="utf-8") as output:
            for key in KEYS:
                output.write(f"{key}={'true' if result[key] else 'false'}\n")
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
