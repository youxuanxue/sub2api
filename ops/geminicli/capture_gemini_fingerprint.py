#!/usr/bin/env python3
"""Deterministic Gemini CLI fingerprint alignment for TokenKey.

Ground truth = locally-installed ``@google/gemini-cli`` (``gemini --version``).
Alignment target = ``GeminiCLIUserAgent`` in ``backend/internal/pkg/geminicli/constants.go``.

Subcommands:
  check-env     Verify gemini CLI is installed and version is parseable.
  show-baseline Print TK pin + installed version.
  diff          Human drift report.
  check         diff + exit 1 on version drift, 2 on env error.
  capture       Write a timestamped bundle under .cache/fingerprint/gemini-cli/.

stdlib-only.
"""
from __future__ import annotations

import argparse
import json
import re
import shutil
import subprocess
import sys
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
CONSTANTS_GO = REPO_ROOT / "backend/internal/pkg/geminicli/constants.go"
OUT_DIR = REPO_ROOT / ".cache/fingerprint/gemini-cli"
_VER = r"\d+\.\d+\.\d+(?:-[0-9A-Za-z.]+)?"
_UA_RE = re.compile(r'GeminiCLIUserAgent\s*=\s*"([^"]+)"')
_VER_IN_UA_RE = re.compile(r"GeminiCLI/(" + _VER + r")/")


@dataclass
class Row:
    field: str
    pinned: str
    installed: str
    status: str
    note: str = ""


def _read(path: Path) -> str:
    try:
        return path.read_text(encoding="utf-8")
    except OSError:
        return ""


def load_pinned_ua() -> tuple[str, str]:
    text = _read(CONSTANTS_GO)
    m = _UA_RE.search(text)
    if not m:
        return "", ""
    ua = m.group(1)
    vm = _VER_IN_UA_RE.search(ua)
    return ua, vm.group(1) if vm else ""


def installed_gemini_version() -> str:
    exe = shutil.which("gemini")
    if not exe:
        return ""
    try:
        out = subprocess.run(
            [exe, "--version"], capture_output=True, text=True, timeout=20, check=False
        )
    except (OSError, subprocess.SubprocessError):
        return ""
    m = re.search("(" + _VER + ")", (out.stdout or "") + (out.stderr or ""))
    return m.group(1) if m else ""


def diff_rows(pinned_ver: str, installed: str) -> list[Row]:
    rows: list[Row] = []
    if not pinned_ver:
        rows.append(Row("user_agent_version", "", installed, "missing", note="GeminiCLIUserAgent not found"))
        return rows
    if not installed:
        rows.append(Row("user_agent_version", pinned_ver, "", "info", note="gemini CLI not installed"))
        return rows
    status = "match" if pinned_ver == installed else "mismatch"
    rows.append(Row("user_agent_version", pinned_ver, installed, status))
    return rows


def has_drift(rows: list[Row]) -> bool:
    return any(r.status in ("mismatch", "missing") for r in rows)


def _print_rows(rows: list[Row]) -> None:
    print(f"Gemini CLI fingerprint diff (installed={installed_gemini_version() or '-'}):")
    for r in rows:
        mark = {"match": "✓", "mismatch": "✗", "info": "·", "missing": "✗"}.get(r.status, "·")
        line = f"  {mark} {r.field}  pinned={r.pinned or '-'}  installed={r.installed or '-'}  [{r.status}]"
        if r.note:
            line += f"  ({r.note})"
        print(line)


def cmd_check_env(_args) -> int:
    exe = shutil.which("gemini")
    if not exe:
        print("  ✗ gemini CLI NOT found on PATH (install: npm i -g @google/gemini-cli)")
        return 2
    ver = installed_gemini_version()
    print(f"  ✓ gemini CLI present ({exe})")
    print(f"  {'✓' if ver else '✗'} gemini --version -> {ver or 'unparseable'}")
    return 0 if ver else 2


def cmd_show_baseline(_args) -> int:
    ua, ver = load_pinned_ua()
    installed = installed_gemini_version()
    print(f"installed gemini version: {installed or '(not installed)'}")
    print(f"TK GeminiCLIUserAgent: {ua or '(not found)'}")
    print(f"TK pinned version: {ver or '(not found)'}")
    return 0


def cmd_diff(_args, gate: bool = False) -> int:
    ua, pinned_ver = load_pinned_ua()
    installed = installed_gemini_version()
    if gate and not installed:
        print("gemini CLI not installed — run: npm i -g @google/gemini-cli@<target>", file=sys.stderr)
        return 2
    rows = diff_rows(pinned_ver, installed)
    _print_rows(rows)
    if has_drift(rows):
        print("\nfollow skill: tokenkey-gemini-fingerprint-alignment")
        print(f"  bump GeminiCLIUserAgent in {CONSTANTS_GO.relative_to(REPO_ROOT)}")
        print(f"  go test -tags=unit ./internal/pkg/geminicli/...")
    elif ua:
        print(f"\nUA literal (reference): {ua}")
    return 1 if has_drift(rows) else 0


def cmd_capture(args) -> int:
    ua, pinned_ver = load_pinned_ua()
    installed = installed_gemini_version()
    if not installed:
        print("gemini CLI not installed — cannot capture", file=sys.stderr)
        return 2
    rows = diff_rows(pinned_ver, installed)
    stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    out_dir = Path(args.out_dir) if args.out_dir else OUT_DIR
    out_dir.mkdir(parents=True, exist_ok=True)
    bundle = {
        "schema_version": 1,
        "platform": "gemini-cli",
        "captured_at": stamp,
        "installed_cli": shutil.which("gemini") or "",
        "installed_version": installed,
        "pinned_user_agent": ua,
        "pinned_version": pinned_ver,
        "rows": [{"field": r.field, "pinned": r.pinned, "installed": r.installed, "status": r.status} for r in rows],
        "aligned": not has_drift(rows),
    }
    path = out_dir / f"{stamp}-gemini-capture.bundle.json"
    path.write_text(json.dumps(bundle, indent=2) + "\n", encoding="utf-8")
    print(f"bundle={path}")
    _print_rows(rows)
    return 1 if has_drift(rows) else 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="TokenKey Gemini CLI fingerprint alignment")
    sub = parser.add_subparsers(dest="cmd", required=True)
    sub.add_parser("check-env")
    sub.add_parser("show-baseline")
    sub.add_parser("diff")
    sub.add_parser("check")
    cap = sub.add_parser("capture")
    cap.add_argument("--out-dir", default="", help=f"default: {OUT_DIR.relative_to(REPO_ROOT)}")
    args = parser.parse_args(argv)
    if args.cmd == "check-env":
        return cmd_check_env(args)
    if args.cmd == "show-baseline":
        return cmd_show_baseline(args)
    if args.cmd == "diff":
        return cmd_diff(args)
    if args.cmd == "check":
        return cmd_diff(args, gate=True)
    if args.cmd == "capture":
        return cmd_capture(args)
    return 2


if __name__ == "__main__":
    sys.exit(main())
