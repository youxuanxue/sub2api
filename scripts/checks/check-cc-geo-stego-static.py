#!/usr/bin/env python3
"""Verify CC geo-stego / prompt-surface static anchors vs pinned cc_version."""
from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
REGISTRY = REPO_ROOT / "scripts" / "sentinels" / "cc-geo-stego-static.json"
CLI_VERSION_GO = REPO_ROOT / "backend/internal/pkg/claude/constants.go"
BASELINES = REPO_ROOT / "deploy/aws/stage0/anthropic-http-mimicry-baselines.json"


def read_cli_version() -> str:
    text = CLI_VERSION_GO.read_text(encoding="utf-8")
    m = re.search(r'CLICurrentVersion\s*=\s*"([^"]+)"', text)
    if not m:
        raise RuntimeError("CLICurrentVersion not found")
    return m.group(1)


def read_baseline_version() -> str:
    return str(json.loads(BASELINES.read_text(encoding="utf-8")).get("cc_version") or "")


def check_go_sentinel(entry: dict) -> list[str]:
    path = REPO_ROOT / str(entry.get("path") or "")
    if not path.is_file():
        return [f"missing file {entry.get('path')}"]
    text = path.read_text(encoding="utf-8", errors="replace")
    return [
        f"missing `{needle}` in {entry.get('path')}"
        for needle in (entry.get("must_contain") or [])
        if needle not in text
    ]


def check_binary(registry: dict, cc_version: str) -> tuple[list[str], list[str]]:
    failures: list[str] = []
    notes: list[str] = []
    path_str = str(registry.get("binary_default_path") or "").replace("{cc_version}", cc_version)
    path = Path.home() / path_str[2:] if path_str.startswith("~/") else Path(path_str)
    if not path.is_file():
        notes.append(f"skip binary check: {path} not installed")
        return failures, notes
    data = path.read_bytes()
    for needle in registry.get("binary_must_contain") or []:
        if needle.encode("utf-8") not in data:
            failures.append(f"CC binary missing marker `{needle}` at {path}")
    return failures, notes


def run(quiet: bool) -> int:
    registry = json.loads(REGISTRY.read_text(encoding="utf-8"))
    pinned = str(registry.get("cc_version") or "")
    failures: list[str] = []
    notes: list[str] = []
    go_ver = read_cli_version()
    base_ver = read_baseline_version()
    if pinned != go_ver:
        failures.append(f"registry cc_version {pinned} != CLICurrentVersion {go_ver}")
    if pinned != base_ver:
        failures.append(f"registry cc_version {pinned} != baselines cc_version {base_ver}")
    for entry in registry.get("go_sentinels") or []:
        failures.extend(check_go_sentinel(entry))
    bin_failures, bin_notes = check_binary(registry, pinned)
    failures.extend(bin_failures)
    notes.extend(bin_notes)
    if failures:
        if not quiet:
            for item in failures:
                print(f"FAIL: {item}", file=sys.stderr)
        return 1
    if not quiet:
        for item in notes:
            print(f"note: {item}")
        print(f"ok: cc geo-stego static anchors (cc_version={pinned})")
    return 0


def run_selftest() -> int:
    json.loads(REGISTRY.read_text(encoding="utf-8"))
    registry = json.loads(REGISTRY.read_text(encoding="utf-8"))
    needles = registry.get("binary_must_contain") or []
    if not needles:
        print("FAIL: binary_must_contain empty", file=sys.stderr)
        return 1
    # Hermetic: synthetic blob must satisfy marker logic without a real cc install.
    blob = b"\n".join(n.encode("utf-8") for n in needles)
    for needle in needles:
        if needle.encode("utf-8") not in blob:
            print(f"FAIL: selftest blob missing {needle!r}", file=sys.stderr)
            return 1
    failures, notes = check_binary(registry, registry.get("cc_version", "0.0.0"))
    if failures:
        for item in failures:
            print(f"FAIL: {item}", file=sys.stderr)
        return 1
    if notes:
        print(f"note: {notes[0]} (expected in selftest — no real cc binary)")
    print("ok: check-cc-geo-stego-static selftest")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--quiet", action="store_true")
    ap.add_argument("--selftest", action="store_true")
    args = ap.parse_args()
    if args.selftest:
        return run_selftest()
    return run(args.quiet)


if __name__ == "__main__":
    raise SystemExit(main())
