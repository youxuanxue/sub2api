#!/usr/bin/env python3
"""Deterministic Grok CLI fingerprint alignment for TokenKey.

Ground truth = locally-installed ``@xai-official/grok`` (``grok --version``).
Alignment target = ``DefaultGrokCLIVersion`` in ``backend/internal/pkg/xai/oauth.go``.

Subcommands:
  check-env     Verify grok CLI is installed and version is parseable.
  show-baseline Print TK pin + installed version.
  diff          Human drift report.
  check         diff + exit 1 on version drift, 2 on env error.
  capture       Write a timestamped bundle under .cache/fingerprint/grok-cli/.

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
OAUTH_GO = REPO_ROOT / "backend/internal/pkg/xai/oauth.go"
OUT_DIR = REPO_ROOT / ".cache/fingerprint/grok-cli"
_VER = r"\d+\.\d+\.\d+(?:-[0-9A-Za-z.]+)?"
_PIN_RE = re.compile(r'DefaultGrokCLIVersion\s*=\s*"([^"]+)"')


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


def load_pinned_version() -> str:
    m = _PIN_RE.search(_read(OAUTH_GO))
    return m.group(1) if m else ""


def installed_grok_version() -> str:
    exe = shutil.which("grok")
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


def diff_rows(pinned: str, installed: str) -> list[Row]:
    rows: list[Row] = []
    if not pinned:
        rows.append(Row("default_grok_cli_version", "", installed, "missing", note="DefaultGrokCLIVersion not found"))
        return rows
    if not installed:
        rows.append(Row("default_grok_cli_version", pinned, "", "info", note="grok CLI not installed"))
        return rows
    status = "match" if pinned == installed else "mismatch"
    rows.append(Row("default_grok_cli_version", pinned, installed, status))
    return rows


def has_drift(rows: list[Row]) -> bool:
    return any(r.status in ("mismatch", "missing") for r in rows)


def _print_rows(rows: list[Row]) -> None:
    print(f"Grok CLI fingerprint diff (installed={installed_grok_version() or '-'}):")
    for r in rows:
        mark = {"match": "✓", "mismatch": "✗", "info": "·", "missing": "✗"}.get(r.status, "·")
        line = f"  {mark} {r.field}  pinned={r.pinned or '-'}  installed={r.installed or '-'}  [{r.status}]"
        if r.note:
            line += f"  ({r.note})"
        print(line)


def cmd_check_env(_args) -> int:
    exe = shutil.which("grok")
    if not exe:
        print("  ✗ grok CLI NOT found on PATH (install: npm i -g @xai-official/grok)")
        return 2
    ver = installed_grok_version()
    print(f"  ✓ grok CLI present ({exe})")
    print(f"  {'✓' if ver else '✗'} grok --version -> {ver or 'unparseable'}")
    return 0 if ver else 2


def cmd_show_baseline(_args) -> int:
    pinned = load_pinned_version()
    installed = installed_grok_version()
    print(f"installed grok version: {installed or '(not installed)'}")
    print(f"TK DefaultGrokCLIVersion: {pinned or '(not found)'}")
    return 0


def cmd_diff(_args, gate: bool = False) -> int:
    pinned = load_pinned_version()
    installed = installed_grok_version()
    if gate and not installed:
        print("grok CLI not installed — run: npm i -g @xai-official/grok@<target>", file=sys.stderr)
        return 2
    rows = diff_rows(pinned, installed)
    _print_rows(rows)
    if has_drift(rows):
        print("\nfollow skill: tokenkey-grok-fingerprint-alignment")
        print(f"  bump DefaultGrokCLIVersion in {OAUTH_GO.relative_to(REPO_ROOT)}")
        print("  go test -tags=unit ./internal/pkg/xai/...")
    return 1 if has_drift(rows) else 0


def cmd_capture(args) -> int:
    pinned = load_pinned_version()
    installed = installed_grok_version()
    if not installed:
        print("grok CLI not installed — cannot capture", file=sys.stderr)
        return 2
    rows = diff_rows(pinned, installed)
    stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    out_dir = Path(args.out_dir) if args.out_dir else OUT_DIR
    out_dir.mkdir(parents=True, exist_ok=True)
    bundle = {
        "schema_version": 1,
        "platform": "grok-cli",
        "captured_at": stamp,
        "installed_cli": shutil.which("grok") or "",
        "installed_version": installed,
        "pinned_version": pinned,
        "rows": [{"field": r.field, "pinned": r.pinned, "installed": r.installed, "status": r.status} for r in rows],
        "aligned": not has_drift(rows),
    }
    path = out_dir / f"{stamp}-grok-capture.bundle.json"
    path.write_text(json.dumps(bundle, indent=2) + "\n", encoding="utf-8")
    print(f"bundle={path}")
    _print_rows(rows)
    return 1 if has_drift(rows) else 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="TokenKey Grok CLI fingerprint alignment")
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
