#!/usr/bin/env python3
"""Validate and render TokenKey's static client identity/evidence registry."""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any

REPO_ROOT = Path(__file__).resolve().parents[2]
DEFAULT_REGISTRY = Path(__file__).with_name("client_identity_registry.json")
VALID_EVIDENCE_MODES = {
    "wire_tls_http",
    "wire_http",
    "local_binary_static",
    "version_only",
    "advisory_release",
}
VALID_SOURCE_KINDS = {"github_release", "npm", "brew_cask"}


def load_registry(path: Path = DEFAULT_REGISTRY) -> dict[str, Any]:
    return json.loads(path.read_text(encoding="utf-8"))


def implemented_pin_reader_keys() -> set[str]:
    import client_release_watch

    return set(client_release_watch.PIN_READERS)


def validate_registry(
    registry: dict[str, Any],
    *,
    repo_root: Path = REPO_ROOT,
    pin_reader_keys: set[str] | None = None,
) -> list[str]:
    errors: list[str] = []
    if registry.get("schema_version") != 1:
        errors.append("schema_version must be 1")
    identities = registry.get("identities")
    if not isinstance(identities, list) or not identities:
        return [*errors, "identities must be a non-empty array"]

    ids = [str(row.get("id") or "") for row in identities if isinstance(row, dict)]
    if len(ids) != len(set(ids)):
        errors.append("identity ids must be unique")
    known_ids = set(ids)

    for index, row in enumerate(identities):
        prefix = f"identities[{index}]"
        if not isinstance(row, dict):
            errors.append(f"{prefix} must be an object")
            continue
        identity_id = str(row.get("id") or "")
        if not identity_id:
            errors.append(f"{prefix}.id is required")
        for key in ("name", "skill", "pin_reader"):
            if not isinstance(row.get(key), str) or not row[key].strip():
                errors.append(f"{prefix}.{key} must be a non-empty string")
        if pin_reader_keys is not None and row.get("pin_reader") not in pin_reader_keys:
            errors.append(f"{identity_id}: unknown pin_reader {row.get('pin_reader')!r}")

        owner = row.get("compile_owner")
        runtime = row.get("runtime_identity")
        for label, value in (("compile_owner", owner), ("runtime_identity", runtime)):
            if not isinstance(value, dict):
                errors.append(f"{identity_id}: {label} must be an object")
                continue
            rel_path = str(value.get("path") or "")
            symbol = str(value.get("symbol") or "")
            path = repo_root / rel_path
            if not rel_path or not path.is_file():
                errors.append(f"{identity_id}: {label} path does not exist: {rel_path}")
            elif not symbol:
                errors.append(f"{identity_id}: {label}.symbol is required")
            else:
                try:
                    text = path.read_text(encoding="utf-8")
                except OSError as exc:
                    errors.append(f"{identity_id}: cannot read {rel_path}: {exc}")
                else:
                    leaf = symbol.rsplit(".", 1)[-1]
                    if symbol not in text and leaf not in text:
                        errors.append(f"{identity_id}: symbol {symbol!r} missing from {rel_path}")

        mode = row.get("evidence_mode")
        if mode not in VALID_EVIDENCE_MODES:
            errors.append(f"{identity_id}: invalid evidence_mode {mode!r}")
        capture_tool = row.get("capture_tool")
        production_observer = row.get("production_observer")
        if capture_tool is not None and not (repo_root / str(capture_tool)).is_file():
            errors.append(f"{identity_id}: capture_tool does not exist: {capture_tool}")
        if production_observer is not None and not (repo_root / str(production_observer)).is_file():
            errors.append(f"{identity_id}: production_observer does not exist: {production_observer}")
        if mode in {"version_only", "advisory_release"} and production_observer is not None:
            errors.append(f"{identity_id}: {mode} cannot claim production observation")
        if mode == "version_only" and capture_tool is not None:
            errors.append(f"{identity_id}: version_only cannot claim a capture tool")

        companion = row.get("companion_identity")
        if companion is not None and companion not in known_ids:
            errors.append(f"{identity_id}: unknown companion_identity {companion!r}")
        if companion == identity_id:
            errors.append(f"{identity_id}: cannot be its own companion")

        sources = row.get("release_sources")
        if not isinstance(sources, list) or not sources:
            errors.append(f"{identity_id}: release_sources must be non-empty")
            continue
        for source in sources:
            if not isinstance(source, dict) or source.get("kind") not in VALID_SOURCE_KINDS:
                errors.append(f"{identity_id}: invalid release source")
                continue
            if not source.get("label"):
                errors.append(f"{identity_id}: release source label is required")
            required = {"github_release": "repo", "npm": "package", "brew_cask": "cask"}[source["kind"]]
            if not source.get(required):
                errors.append(f"{identity_id}: {source['kind']} source requires {required}")
    return errors


def coverage_rows(registry: dict[str, Any]) -> list[dict[str, str]]:
    rows: list[dict[str, str]] = []
    for identity in registry.get("identities") or []:
        mode = str(identity.get("evidence_mode") or "")
        rows.append({
            "id": str(identity.get("id") or ""),
            "release": "advisory" if not identity.get("actionable", True) else "watched",
            "static": "yes" if mode in {"local_binary_static", "version_only"} else "no",
            "wire": "tls+http" if mode == "wire_tls_http" else "http" if mode == "wire_http" else "no",
            "runtime": "declared" if identity.get("runtime_identity") else "no",
            "production": "configured" if identity.get("production_observer") else "no",
        })
    return rows


def render_coverage(registry: dict[str, Any]) -> str:
    lines = [
        "identity         release   static wire     runtime  production",
        "---------------- --------- ------ -------- -------- ----------",
    ]
    for row in coverage_rows(registry):
        lines.append(
            f"{row['id']:<16} {row['release']:<9} {row['static']:<6} {row['wire']:<8} "
            f"{row['runtime']:<8} {row['production']}"
        )
    return "\n".join(lines)


def selftest() -> None:
    registry = {
        "schema_version": 1,
        "identities": [{
            "id": "sample",
            "name": "Sample",
            "skill": "sample-skill",
            "pin_reader": "sample",
            "compile_owner": {"path": "scripts/fingerprint/check_client_identity_registry.py", "symbol": "VALID_EVIDENCE_MODES"},
            "runtime_identity": {"path": "scripts/fingerprint/check_client_identity_registry.py", "symbol": "coverage_rows", "override": None},
            "evidence_mode": "version_only",
            "capture_tool": None,
            "production_observer": None,
            "companion_identity": None,
            "release_sources": [{"kind": "npm", "label": "npm sample", "package": "sample"}],
        }],
    }
    assert not validate_registry(registry, pin_reader_keys={"sample"})
    registry["identities"][0]["production_observer"] = "ops/observability/probe-kiro-tls-profile-parity.sh"
    assert any("cannot claim production" in error for error in validate_registry(registry, pin_reader_keys={"sample"}))
    registry["identities"][0]["production_observer"] = None
    registry["identities"][0]["pin_reader"] = "missing"
    assert any("unknown pin_reader" in error for error in validate_registry(registry, pin_reader_keys={"sample"}))
    print("client identity registry selftest ok")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--registry", type=Path, default=DEFAULT_REGISTRY)
    parser.add_argument("--coverage", action="store_true")
    parser.add_argument("--quiet", action="store_true")
    parser.add_argument("--selftest", action="store_true")
    args = parser.parse_args(argv)
    if args.selftest:
        selftest()
        return 0

    registry = load_registry(args.registry)
    errors = validate_registry(registry, pin_reader_keys=implemented_pin_reader_keys())
    if errors:
        for error in errors:
            print(f"ERROR: {error}", file=sys.stderr)
        return 1
    if args.coverage:
        print(render_coverage(registry))
    elif not args.quiet:
        print(f"client identity registry ok: {len(registry['identities'])} identities")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
