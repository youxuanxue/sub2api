#!/usr/bin/env python3
"""
check-redaction-version.py — ensure sensitive-key drift bumps QA redaction_version.

Source of truth lives in `scripts/redaction-sentinels.json`:
- `sensitive_keys` is the approved snapshot of logredact's default sensitive keys.
- `redaction_version` is the expected outward version string that evidence records write.
- `version_sources` are files that must still contain that version literal.

Failure modes this catches:
1. A developer adds/removes a default sensitive key in logredact but forgets to bump
   the outward redaction_version contract.
2. A developer bumps the version in one place but not the others.

Exit codes:
  0  — redaction key snapshot and version sources are consistent.
  1  — at least one consistency check failed.
  2  — registry or source parsing failed.
"""
from __future__ import annotations

import argparse
import ast
import json
import re
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
REGISTRY_PATH = REPO_ROOT / "scripts" / "redaction-sentinels.json"
LOGREDACT_PATH = REPO_ROOT / "backend" / "internal" / "util" / "logredact" / "redact.go"

KEY_MAP_RE = re.compile(r"var\s+defaultSensitiveKeys\s*=\s*map\[string\]struct\{\}\s*\{(?P<body>.*?)\n\}", re.S)
KEY_LIST_RE = re.compile(r"var\s+defaultSensitiveKeyList\s*=\s*\[\]string\s*\{(?P<body>.*?)\n\}", re.S)
STRING_LITERAL_RE = re.compile(r'"((?:[^"\\]|\\.)*)"')


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


def parse_go_string_literals(body: str) -> list[str]:
    values: list[str] = []
    for match in STRING_LITERAL_RE.finditer(body):
        literal = '"' + match.group(1) + '"'
        try:
            values.append(ast.literal_eval(literal))
        except Exception as exc:  # pragma: no cover - defensive parser failure
            fatal(f"cannot parse Go string literal {literal!r}: {exc}")
    return values


def extract_sensitive_keys() -> tuple[list[str], list[str]]:
    if not LOGREDACT_PATH.is_file():
        fatal(f"logredact source file missing: {LOGREDACT_PATH.relative_to(REPO_ROOT)}")
    content = LOGREDACT_PATH.read_text(encoding="utf-8", errors="replace")

    map_match = KEY_MAP_RE.search(content)
    if not map_match:
        fatal("could not locate defaultSensitiveKeys map in logredact/redact.go")
    list_match = KEY_LIST_RE.search(content)
    if not list_match:
        fatal("could not locate defaultSensitiveKeyList slice in logredact/redact.go")

    map_keys = sorted(set(parse_go_string_literals(map_match.group("body"))))
    list_keys = parse_go_string_literals(list_match.group("body"))
    return map_keys, list_keys


def check_version_sources(version: str, sources: list[str]) -> list[str]:
    failures: list[str] = []
    needle = f'"{version}"'
    for source in sources:
        file_path = REPO_ROOT / source
        if not file_path.is_file():
            failures.append(f"file missing: {source}")
            continue
        content = file_path.read_text(encoding="utf-8", errors="replace")
        if needle not in content:
            failures.append(f"missing version literal {needle} in {source}")
    return failures


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--quiet", action="store_true", help="only print failures")
    parser.add_argument("--json", action="store_true", help="emit machine-readable JSON")
    args = parser.parse_args()

    registry = load_registry()
    expected_version = registry.get("redaction_version")
    expected_keys = registry.get("sensitive_keys")
    version_sources = registry.get("version_sources")

    if not isinstance(expected_version, str) or not expected_version.strip():
        fatal("registry missing non-empty string field 'redaction_version'")
    if not isinstance(expected_keys, list) or not all(isinstance(k, str) for k in expected_keys):
        fatal("registry missing string array field 'sensitive_keys'")
    if not isinstance(version_sources, list) or not all(isinstance(p, str) for p in version_sources):
        fatal("registry missing string array field 'version_sources'")

    expected_keys_sorted = sorted(set(expected_keys))
    map_keys, list_keys = extract_sensitive_keys()

    failures: list[str] = []
    if map_keys != expected_keys_sorted:
        failures.append(
            "defaultSensitiveKeys drifted from scripts/redaction-sentinels.json; "
            "update the registry and bump redaction_version in the same commit"
        )
    if sorted(set(list_keys)) != expected_keys_sorted:
        failures.append(
            "defaultSensitiveKeyList drifted from scripts/redaction-sentinels.json; "
            "keep the list/map snapshot aligned and bump redaction_version in the same commit"
        )
    if list_keys != sorted(set(list_keys)):
        failures.append(
            "defaultSensitiveKeyList must stay sorted for stable reviewable diffs"
        )
    version_failures = check_version_sources(expected_version.strip(), version_sources)
    failures.extend(version_failures)

    report = {
        "registry": str(REGISTRY_PATH.relative_to(REPO_ROOT)),
        "logredact_source": str(LOGREDACT_PATH.relative_to(REPO_ROOT)),
        "redaction_version": expected_version,
        "expected_keys": expected_keys_sorted,
        "map_keys": map_keys,
        "list_keys": list_keys,
        "failures": failures,
    }

    if args.json:
        json.dump(report, sys.stdout, indent=2)
        sys.stdout.write("\n")
    else:
        if not args.quiet:
            print(
                f"redaction version check: {REGISTRY_PATH.relative_to(REPO_ROOT)} against "
                f"{LOGREDACT_PATH.relative_to(REPO_ROOT)}"
            )
        if failures:
            print("  FAIL: redaction version contract drift detected")
            for failure in failures:
                print(f"        - {failure}")
            print(
                "        - fix path: update scripts/redaction-sentinels.json and bump "
                "redaction_version in backend/internal/observability/qa/service.go plus "
                "backend/ent/schema/qa_record.go in the same commit"
            )
        elif not args.quiet:
            print("  ok: sensitive-key snapshot and redaction_version sources are aligned")

    return 0 if not failures else 1


if __name__ == "__main__":
    sys.exit(main())
