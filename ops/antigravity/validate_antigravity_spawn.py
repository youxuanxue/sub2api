#!/usr/bin/env python3
"""Validate Antigravity IDE fingerprint from language_server spawn args (no mitm).

When the IDE's Go language_server direct-dials Google and bypasses HTTP proxies,
mitmproxy cannot intercept on-wire traffic. The spawn command line is the
authoritative identity source (see docs/antigravity-fingerprint-changelog.md
2026-06-12 ide-validate; skill tokenkey-antigravity-fingerprint-alignment).

Subcommands:
  check-env   language_server process + Antigravity.app present
  check       Compare spawn args vs TK Go constants; exit 1 on drift
  capture     Write bundle JSON under .cache/fingerprint/antigravity-spawn/

stdlib-only.
"""
from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
OUT_DIR = REPO_ROOT / ".cache/fingerprint/antigravity-spawn"
CAPTURE_PY = REPO_ROOT / "ops/antigravity/capture_antigravity_fingerprint.py"

DEFAULT_LS = Path("/Applications/Antigravity.app/Contents/Resources/bin/language_server")
DEFAULT_APP_INFO = Path("/Applications/Antigravity.app/Contents/Info.plist")


@dataclass
class Row:
    field: str
    expected: str
    observed: str
    status: str
    note: str = ""


def _import_baseline():
    import sys

    cap_dir = str(CAPTURE_PY.parent)
    if cap_dir not in sys.path:
        sys.path.insert(0, cap_dir)
    import capture_antigravity_fingerprint as ag_mod  # noqa: E402

    return ag_mod.load_antigravity_baseline(), ag_mod


def _run_ps() -> str:
    try:
        out = subprocess.run(
            ["ps", "-ax", "-o", "args="],
            capture_output=True,
            text=True,
            timeout=10,
            check=False,
        )
    except (OSError, subprocess.SubprocessError):
        return ""
    return out.stdout or ""


def find_language_server_cmdline(ps_text: str | None = None) -> tuple[str, Path | None]:
    text = ps_text if ps_text is not None else _run_ps()
    for line in text.splitlines():
        if "language_server" not in line or "override_ide_name" not in line:
            continue
        if "antigravity" not in line.lower():
            continue
        parts = line.strip().split(None, 1)
        if len(parts) < 2:
            continue
        binary = Path(parts[0])
        return line.strip(), binary if binary.is_file() else None
    return "", None


def parse_spawn_args(cmdline: str) -> dict[str, str]:
    tokens = cmdline.split()
    out: dict[str, str] = {}
    i = 0
    while i < len(tokens):
        tok = tokens[i]
        if not tok.startswith("--"):
            i += 1
            continue
        if "=" in tok:
            key, val = tok[2:].split("=", 1)
            out[key] = val
        elif i + 1 < len(tokens) and not tokens[i + 1].startswith("--"):
            out[tok[2:]] = tokens[i + 1]
            i += 1
        else:
            out[tok[2:]] = "true"
        i += 1
    return out


def read_app_version() -> str:
    if not DEFAULT_APP_INFO.is_file():
        return ""
    try:
        out = subprocess.run(
            ["defaults", "read", str(DEFAULT_APP_INFO.parent / "Info"), "CFBundleShortVersionString"],
            capture_output=True,
            text=True,
            timeout=5,
            check=False,
        )
    except (OSError, subprocess.SubprocessError):
        return ""
    return (out.stdout or "").strip()


def diff_spawn(baseline: dict, spawn: dict[str, str], app_version: str) -> list[Row]:
    rows: list[Row] = []
    want_ver = str(baseline.get("ua_version") or "")
    got_ver = spawn.get("override_ide_version") or app_version
    rows.append(
        Row(
            "ua_version",
            want_ver,
            got_ver,
            "match" if want_ver and got_ver == want_ver else "mismatch",
            note="override_ide_version from language_server (or CFBundleShortVersionString fallback)",
        )
    )
    want_sub = "hub"
    got_sub = spawn.get("subclient_type", "")
    rows.append(
        Row(
            "subclient_type",
            want_sub,
            got_sub,
            "match" if got_sub == want_sub else "mismatch",
            note="UA carries antigravity/hub/<ver> when hub",
        )
    )
    want_name = str(baseline.get("body_user_agent") or "antigravity")
    got_name = spawn.get("override_user_agent_name") or spawn.get("override_ide_name", "")
    rows.append(
        Row(
            "user_agent_name",
            want_name,
            got_name,
            "match" if got_name == want_name else "mismatch",
        )
    )
    want_ide = str(baseline.get("ide_type") or "ANTIGRAVITY")
    # spawn uses lowercase override_ide_name; compare case-insensitively to IDEType constant
    got_ide = spawn.get("override_ide_name", "")
    rows.append(
        Row(
            "ide_name_spawn",
            want_ide.lower(),
            got_ide.lower(),
            "match" if got_ide.lower() == want_ide.lower() else "mismatch",
        )
    )
    return rows


def has_drift(rows: list[Row]) -> bool:
    return any(r.status == "mismatch" for r in rows)


def _print_rows(rows: list[Row]) -> None:
    for r in rows:
        mark = "✓" if r.status == "match" else "✗"
        line = f"  {mark} {r.field}  expected={r.expected or '-'}  observed={r.observed or '-'}  [{r.status}]"
        if r.note:
            line += f"  ({r.note})"
        print(line)


def cmd_check_env(_args) -> int:
    cmdline, binary = find_language_server_cmdline()
    if DEFAULT_LS.is_file():
        print(f"  ✓ language_server binary present ({DEFAULT_LS})")
    else:
        print(f"  ✗ language_server not found at {DEFAULT_LS}")
    app_ver = read_app_version()
    if app_ver:
        print(f"  ✓ Antigravity.app version {app_ver}")
    else:
        print("  · Antigravity.app version unreadable (app not installed?)")
    if cmdline:
        print("  ✓ language_server process found (spawn validation available)")
        if binary:
            print(f"    binary={binary}")
    else:
        print("  ✗ no antigravity language_server process — launch Antigravity IDE first")
        return 2
    return 0


def cmd_check(_args, write_bundle: bool = False) -> int:
    baseline, ag_mod = _import_baseline()
    cmdline, binary = find_language_server_cmdline()
    if not cmdline:
        print("error: antigravity language_server not running", file=sys.stderr)
        print("  Launch Antigravity IDE, then retry.", file=sys.stderr)
        return 2
    spawn = parse_spawn_args(cmdline)
    app_ver = read_app_version()
    rows = diff_spawn(baseline, spawn, app_ver)
    expected_ua = ag_mod.expected_user_agent(baseline)
    print("Antigravity spawn validation (no mitm):")
    print(f"  expected UA shape: {expected_ua}")
    print(f"  spawn: {cmdline[:200]}{'...' if len(cmdline) > 200 else ''}")
    _print_rows(rows)
    if write_bundle:
        stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
        OUT_DIR.mkdir(parents=True, exist_ok=True)
        path = OUT_DIR / f"{stamp}-antigravity-spawn.bundle.json"
        payload = {
            "schema_version": 1,
            "platform": "antigravity-ide-spawn",
            "captured_at": stamp,
            "method": "language_server_spawn_args",
            "cmdline": cmdline,
            "binary": str(binary or DEFAULT_LS),
            "app_version": app_ver,
            "baseline_ua": expected_ua,
            "spawn": spawn,
            "rows": [
                {"field": r.field, "expected": r.expected, "observed": r.observed, "status": r.status}
                for r in rows
            ],
            "aligned": not has_drift(rows),
        }
        path.write_text(json.dumps(payload, indent=2) + "\n", encoding="utf-8")
        print(f"\nbundle={path}")
    if has_drift(rows):
        print("\nspawn drift — bump DefaultUserAgentVersion or investigate format change.")
        print("For format-breaking drift, use TUN on-wire capture (2026-06-13) or update constants.")
        return 1
    print("\nRESULT: spawn-aligned (version-only bumps validated without mitm).")
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Antigravity spawn-arg validator (mitm alternative)")
    sub = parser.add_subparsers(dest="cmd", required=True)
    sub.add_parser("check-env")
    sub.add_parser("check")
    sub.add_parser("capture")
    args = parser.parse_args(argv)
    if args.cmd == "check-env":
        return cmd_check_env(args)
    if args.cmd == "check":
        return cmd_check(args)
    if args.cmd == "capture":
        return cmd_check(args, write_bundle=True)
    return 2


if __name__ == "__main__":
    sys.exit(main())
