#!/usr/bin/env python3
"""
check-trajectory-hooks.py — ensure gateway trajectory/QA capture hooks stay wired.

Source of truth lives in `scripts/trajectory-sentinels.json`:
- `route_source` is the gateway route registration file that must keep the
  canonical trajectory_id + QA capture middleware hooks on main gateway scopes.
- `capture_source` is the QA middleware that must still terminate in
  `Service.CaptureFromContext` after teeing request/response bodies.
- `required_route_hooks` / `required_capture_hooks` are literal substrings that
  must remain present.

Failure modes this catches:
1. A new refactor or upstream merge drops trajectory_id / qaCapture from a main
   gateway scope, silently disabling Evidence Spine capture for that traffic.
2. QACapture middleware stops calling `CaptureFromContext`, so requests appear
   wired at the route layer but no terminal evidence is persisted.

Exit codes:
  0  — route and capture hooks are intact.
  1  — at least one required hook is missing.
  2  — registry or source parsing failed.
"""
from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
REGISTRY_PATH = REPO_ROOT / "scripts" / "trajectory-sentinels.json"


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


def check_required_literals(source: str, required: list[str]) -> list[str]:
    failures: list[str] = []
    file_path = REPO_ROOT / source
    if not file_path.is_file():
        return [f"file missing: {source}"]
    content = file_path.read_text(encoding="utf-8", errors="replace")
    for needle in required:
        if needle not in content:
            failures.append(f"missing literal in {source}: {needle}")
    return failures


def check_route_contracts(contracts: list[dict[str, object]]) -> list[str]:
    failures: list[str] = []
    for contract in contracts:
        if not isinstance(contract, dict):
            failures.append("semantic_route_contracts entry must be an object/dict")
            continue
        source = str(contract.get("source", ""))
        file_path = REPO_ROOT / source
        if not source or not file_path.is_file():
            failures.append(f"route contract source missing: {source}")
            continue
        content = file_path.read_text(encoding="utf-8", errors="replace")
        compact = re.sub(r"\s+", "", content)

        required = contract.get("required_calls", [])
        if not isinstance(required, list):
            failures.append(f"route contract in {source} has invalid required_calls")
            continue

        for item in required:
            if not isinstance(item, dict):
                failures.append(f"route contract in {source} has invalid required_calls item")
                continue
            name = str(item.get("name", ""))
            args = item.get("args", [])
            if not isinstance(args, list) or not all(isinstance(v, str) for v in args):
                failures.append(f"route contract {name} in {source} has invalid args")
                continue
            receiver, method = name.split(".", 1) if "." in name else ("", name)
            literal_args = ",".join(json.dumps(arg, ensure_ascii=False) for arg in args)
            needle = f"{receiver}.{method}({literal_args}"
            if needle not in compact:
                failures.append(f"missing route contract in {source}: {name}({', '.join(args)})")
    return failures


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--quiet", action="store_true", help="only print failures")
    parser.add_argument("--json", action="store_true", help="emit machine-readable JSON")
    args = parser.parse_args()

    registry = load_registry()
    route_source = registry.get("route_source")
    capture_source = registry.get("capture_source")
    required_route_hooks = registry.get("required_route_hooks")
    required_capture_hooks = registry.get("required_capture_hooks")

    semantic_route_contracts = registry.get("semantic_route_contracts", [])

    if not isinstance(route_source, str) or not route_source.strip():
        fatal("registry missing non-empty string field 'route_source'")
    if not isinstance(capture_source, str) or not capture_source.strip():
        fatal("registry missing non-empty string field 'capture_source'")
    if not isinstance(required_route_hooks, list) or not all(isinstance(v, str) for v in required_route_hooks):
        fatal("registry missing string array field 'required_route_hooks'")
    if not isinstance(required_capture_hooks, list) or not all(isinstance(v, str) for v in required_capture_hooks):
        fatal("registry missing string array field 'required_capture_hooks'")
    if not isinstance(semantic_route_contracts, list):
        fatal("registry field 'semantic_route_contracts' must be an array when present")

    failures: list[str] = []
    failures.extend(check_required_literals(route_source, required_route_hooks))
    failures.extend(check_required_literals(capture_source, required_capture_hooks))
    failures.extend(check_route_contracts(semantic_route_contracts))

    report = {
        "registry": str(REGISTRY_PATH.relative_to(REPO_ROOT)),
        "route_source": route_source,
        "capture_source": capture_source,
        "required_route_hooks": required_route_hooks,
        "required_capture_hooks": required_capture_hooks,
        "semantic_route_contracts": semantic_route_contracts,
        "failures": failures,
    }

    if args.json:
        json.dump(report, sys.stdout, indent=2)
        sys.stdout.write("\n")
    else:
        if not args.quiet:
            print(
                f"trajectory hook check: {REGISTRY_PATH.relative_to(REPO_ROOT)} against "
                f"{route_source} + {capture_source}"
            )
        if failures:
            print("  FAIL: trajectory / QA capture hook drift detected")
            for failure in failures:
                print(f"        - {failure}")
            print(
                "        - fix path: restore trajectory_id + qaCapture wiring in "
                "backend/internal/server/routes/gateway.go and keep "
                "backend/internal/observability/qa/sse_tee.go calling "
                "Service.CaptureFromContext"
            )
        elif not args.quiet:
            print("  ok: gateway trajectory hooks and QA terminal capture are aligned")

    return 0 if not failures else 1


if __name__ == "__main__":
    sys.exit(main())
