#!/usr/bin/env python3
"""
check-engine-facade-hooks.py — ensure key dispatch paths stay routed through Engine facade truth.

Source of truth lives in `scripts/engine-facade-sentinels.json`:
- each entry names a source file whose Engine-facade contract is load-bearing
- `required_literals` are exact substrings that must remain present

Failure modes this catches:
1. A refactor reintroduces bridge/provider branching directly inside hotspot service
   files instead of going through the shared engine facade gate.
2. The canonical engine facade entrypoint stops owning dispatch-plan selection,
   leaving the Engine Spine nominal only.

Exit codes:
  0  — engine facade sources are aligned.
  1  — at least one required literal is missing.
  2  — registry or source parsing failed.
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
REGISTRY_PATH = REPO_ROOT / "scripts" / "engine-facade-sentinels.json"


def fatal(msg: str) -> None:
    print(f"FATAL: {msg}", file=sys.stderr)
    sys.exit(2)


def load_registry() -> dict:
    if not REGISTRY_PATH.is_file():
        fatal(f"registry file not found: {REGISTRY_PATH.relative_to(REPO_ROOT)}")
    try:
        return json.loads(REGISTRY_PATH.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        fatal(f"registry file is not valid JSON: {exc}")


def check_entry(source: str, required_literals: list[str]) -> list[str]:
    failures: list[str] = []
    file_path = REPO_ROOT / source
    if not file_path.is_file():
        return [f"file missing: {source}"]
    content = file_path.read_text(encoding="utf-8", errors="replace")
    for needle in required_literals:
        if needle not in content:
            failures.append(f"missing literal in {source}: {needle}")
    return failures


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--quiet", action="store_true", help="only print failures")
    parser.add_argument("--json", action="store_true", help="emit machine-readable JSON")
    args = parser.parse_args()

    registry = load_registry()
    checks = registry.get("checks")
    if not isinstance(checks, list) or not checks:
        fatal("registry missing non-empty array field 'checks'")

    failures: list[str] = []
    normalized_checks: list[dict[str, object]] = []
    for entry in checks:
        if not isinstance(entry, dict):
            fatal("registry 'checks' entries must be objects")
        name = entry.get("name")
        source = entry.get("source")
        required_literals = entry.get("required_literals")
        if not isinstance(name, str) or not name.strip():
            fatal("each check must have non-empty string field 'name'")
        if not isinstance(source, str) or not source.strip():
            fatal(f"check {name!r} missing non-empty string field 'source'")
        if not isinstance(required_literals, list) or not all(isinstance(v, str) for v in required_literals):
            fatal(f"check {name!r} missing string array field 'required_literals'")
        normalized_checks.append({
            "name": name,
            "source": source,
            "required_literals": required_literals,
        })
        failures.extend(check_entry(source, required_literals))

    report = {
        "registry": str(REGISTRY_PATH.relative_to(REPO_ROOT)),
        "checks": normalized_checks,
        "failures": failures,
    }

    if args.json:
        json.dump(report, sys.stdout, indent=2)
        sys.stdout.write("\n")
    else:
        if not args.quiet:
            print(f"engine facade check: {REGISTRY_PATH.relative_to(REPO_ROOT)}")
        if failures:
            print("  FAIL: engine facade contract drift detected")
            for failure in failures:
                print(f"        - {failure}")
            print(
                "        - fix path: restore engine.BuildDispatchPlan gating in "
                "backend/internal/service/gateway_bridge_dispatch.go and keep "
                "OpenAI gateway dispatch paths routing through the shared helper"
            )
        elif not args.quiet:
            print("  ok: key dispatch paths still route through Engine facade truth")

    return 0 if not failures else 1


if __name__ == "__main__":
    sys.exit(main())
