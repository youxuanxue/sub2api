#!/usr/bin/env python3
"""Deterministic Codex (OpenAI platform) client-fingerprint alignment for TokenKey.

Ground truth = the locally-installed Codex CLI (``codex --version`` + the native
binary's strings). Alignment target = ``DefaultOpenAICodexVersion``, the sole
editable TK Go version owner for the forged / fallback Codex fingerprint on the
OpenAI OAuth path. The UA, gateway version, and probe version are derived aliases;
admin-UI placeholders are examples, not fingerprint pins.

Unlike the cc / kiro / antigravity engines this needs NO mitmproxy / pcap: the
Codex CLI ships its fingerprint locally, so the on-wire identity is read straight
off the installed binary and diffed against the pinned TK literals. The
NON-version pins (``originator=codex_cli_rs``, ``OpenAI-Beta: responses=experimental``)
are sanity-checked against the binary's strings; a change there is reported as
``needs_investigation`` (manual judgement, follow the SKILL), never an auto-bump.

The OS / terminal segment of the User-Agent (``Mac OS 26.3.1; arm64`` /
``iTerm.app/3.6.11``) is the operator's REFERENCE environment, not load-bearing:
the engine only treats the codex VERSION token as the alignment target and keeps
the rest of the literal verbatim when emitting a bump.

Subcommands:
  check-env          Verify the Codex CLI is installed + locate its native binary.
  show-baseline      Print the TK version owner/aliases + installed codex version.
  diff               Human drift report (installed codex vs owner/aliases).
  check              diff + exit 1 on any version drift, 2 on env error.
  check-consistency  Exit 1 when aliases stop deriving from the version owner.
                     Does NOT need codex
                     installed, never compares to a moving upstream version —
                     this is the preflight-safe gate.
  emit-edits         Print the exact owner bump (mechanizes the PR).

stdlib-only.
"""
from __future__ import annotations

import argparse
import json
import re
import shutil
import subprocess
import sys
from dataclasses import dataclass, field
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]

# --- alignment targets (single source of truth for "where the version lives") ---
SETTING_GO = REPO_ROOT / "backend/internal/service/setting_gateway_runtime.go"
GATEWAY_GO = REPO_ROOT / "backend/internal/service/openai_gateway_service.go"
USAGE_GO = REPO_ROOT / "backend/internal/service/account_usage_service.go"
NON_VERSION_PIN_GO_FILES = (
    REPO_ROOT / "backend/internal/service/openai_gateway_scheduling.go",
    REPO_ROOT / "backend/internal/service/openai_gateway_forward.go",
    REPO_ROOT / "backend/internal/service/openai_gateway_passthrough.go",
)

# Non-version pins verified (not bumped) against the installed binary's strings.
EXPECTED_ORIGINATOR = "codex_cli_rs"
EXPECTED_BETA = "responses=experimental"

_VER = r"\d+\.\d+\.\d+(?:-[0-9A-Za-z.]+)?"


@dataclass
class Pin:
    """The editable Codex version owner or one of its derived aliases."""

    key: str
    path: Path
    kind: str  # "bare" (editable owner) | "alias" (must derive from owner)
    raw: str = ""  # the owner literal or alias expression as found
    version: str = ""  # the extracted codex version
    derivation_complete: bool = True
    found: bool = True

    @property
    def rel(self) -> str:
        try:
            return str(self.path.relative_to(REPO_ROOT))
        except ValueError:
            return str(self.path)


@dataclass
class Row:
    field: str
    pinned: str
    installed: str
    status: str  # match | mismatch | info | needs_investigation | missing
    critical: bool = False
    note: str = ""


@dataclass
class Baseline:
    pins: list[Pin] = field(default_factory=list)
    originator_pinned: bool = False
    beta_pinned: bool = False

    @property
    def versions(self) -> list[str]:
        return [p.version for p in self.pins if p.found and p.version]

    def consensus(self) -> str:
        """The version shared by the owner and aliases, or '' on disagreement."""
        vs = self.versions
        if vs and len(set(vs)) == 1:
            return vs[0]
        return ""


# --------------------------------------------------------------------------- #
# parsing helpers (pure — exercised directly by the unit tests)
# --------------------------------------------------------------------------- #
def parse_codex_version(version_output: str) -> str:
    """Extract the semver from ``codex --version`` output (``codex-cli 0.142.2``)."""
    m = re.search(r"(" + _VER + r")", version_output or "")
    return m.group(1) if m else ""


def _read(path: Path) -> str:
    try:
        return path.read_text(encoding="utf-8")
    except OSError:
        return ""


def _find1(text: str, pattern: str) -> str:
    m = re.search(pattern, text)
    return m.group(1) if m else ""


def _alias_pin(key: str, path: Path, symbol: str, text: str, source_version: str) -> Pin:
    literal = _find1(text, rf'{symbol}\s*=\s*"([^"]+)"')
    if literal:
        return Pin(
            key,
            path,
            "alias",
            raw=literal,
            version=literal,
            derivation_complete=False,
        )
    if re.search(rf"{symbol}\s*=\s*DefaultOpenAICodexVersion\b", text):
        return Pin(
            key,
            path,
            "alias",
            raw="DefaultOpenAICodexVersion",
            version=source_version,
            found=bool(source_version),
        )
    return Pin(key, path, "alias", found=False)


# --------------------------------------------------------------------------- #
# baseline (read the live repo pins)
# --------------------------------------------------------------------------- #
def load_baseline() -> Baseline:
    bl = Baseline()

    setting_txt = _read(SETTING_GO)
    source_version = _find1(setting_txt, r'DefaultOpenAICodexVersion\s*=\s*"([^"]+)"')
    bl.pins.append(Pin("version_source", SETTING_GO, "bare", raw=source_version, version=source_version, found=bool(source_version)))

    ua_expr = _find1(setting_txt, r"DefaultOpenAICodexUserAgent\s*=\s*([^\n]+)")
    ua_uses_source = ua_expr.count("DefaultOpenAICodexVersion") >= 2
    bl.pins.append(Pin(
        "ua_default",
        SETTING_GO,
        "alias",
        raw=ua_expr,
        version=source_version if ua_uses_source else "",
        found=bool(ua_expr),
        derivation_complete=ua_uses_source,
    ))

    service_txt = "\n".join((setting_txt, _read(GATEWAY_GO), _read(USAGE_GO)))
    bl.pins.append(_alias_pin("gateway_version", SETTING_GO, "codexCLIVersion", service_txt, source_version))
    bl.pins.append(_alias_pin("probe_version", SETTING_GO, "openAICodexProbeVersion", service_txt, source_version))

    # Non-version pins (sanity, not bumped). These live in thin companion files
    # after the upstream merge split the gateway hot path.
    non_version_txt = "\n".join(_read(path) for path in NON_VERSION_PIN_GO_FILES)
    bl.originator_pinned = ('"' + EXPECTED_ORIGINATOR + '"') in non_version_txt
    bl.beta_pinned = EXPECTED_BETA in non_version_txt
    return bl


# --------------------------------------------------------------------------- #
# installed codex CLI (ground truth)
# --------------------------------------------------------------------------- #
def installed_codex_version() -> str:
    exe = shutil.which("codex")
    if not exe:
        return ""
    try:
        out = subprocess.run(
            [exe, "--version"], capture_output=True, text=True, timeout=20, check=False
        )
    except (OSError, subprocess.SubprocessError):
        return ""
    return parse_codex_version((out.stdout or "") + (out.stderr or ""))


def locate_codex_binary() -> Path | None:
    """Best-effort path to the native (Rust) codex binary behind the npm wrapper.

    Bounded on purpose: the recursive ``**`` glob is scoped to the codex package
    root (the nearest ancestor ``@openai/codex/``), NEVER an open-ended ascent that
    could glob from the filesystem root and scan the whole disk. A missing binary
    just yields None (binary-strings sanity is then skipped, handled by callers).
    """
    exe = shutil.which("codex")
    if not exe:
        return None
    real = Path(exe).resolve()
    # Standalone / Homebrew native binary: the resolved target is already it.
    if real.is_file() and real.suffix != ".js":
        return real
    # npm layout: .../@openai/codex/bin/codex.js -> native pkg under a nested
    # node_modules. Search ONLY inside the @openai/codex package root.
    for anc in real.parents:
        if anc.name == "codex" and anc.parent.name == "@openai":
            for cand in anc.glob("**/@openai/codex-*/vendor/*/bin/codex"):
                if cand.is_file():
                    return cand
            return None  # found the package root but no native binary — stop here
    return None


def binary_markers(binary: Path) -> dict[str, bool] | None:
    """Check the non-version pins survive in the binary's strings. None if unreadable."""
    try:
        data = binary.read_bytes()
    except OSError:
        return None
    return {
        "originator": EXPECTED_ORIGINATOR.encode() in data,
        "beta": EXPECTED_BETA.encode() in data,
    }


# --------------------------------------------------------------------------- #
# diff
# --------------------------------------------------------------------------- #
def diff_pins(bl: Baseline, installed: str) -> list[Row]:
    rows: list[Row] = []
    for p in bl.pins:
        if not p.found:
            rows.append(Row(p.key, "", installed, "missing", critical=True,
                            note=f"could not read pin in {p.rel}"))
            continue
        if not p.derivation_complete:
            rows.append(Row(p.key, p.version, installed, "mismatch", critical=True,
                            note="version derivation is incomplete (half-done edit)"))
            continue
        if not installed:
            rows.append(Row(p.key, p.version, "", "info", note="codex CLI not installed"))
            continue
        status = "match" if p.version == installed else "mismatch"
        rows.append(Row(p.key, p.version, installed, status, critical=(status == "mismatch")))
    return rows


def has_drift(rows: list[Row]) -> bool:
    return any(r.status in ("mismatch", "missing") for r in rows)


def consistency_rows(bl: Baseline) -> list[Row]:
    """Pins vs the internal consensus — the preflight-safe (no-CLI) view."""
    consensus = bl.consensus()
    rows: list[Row] = []
    for p in bl.pins:
        if not p.found:
            rows.append(Row(p.key, "", consensus, "missing", critical=True,
                            note=f"could not read pin in {p.rel}"))
            continue
        if not p.derivation_complete:
            rows.append(Row(p.key, p.version, consensus, "mismatch", critical=True,
                            note="version derivation is incomplete"))
            continue
        status = "match" if consensus and p.version == consensus else "mismatch"
        rows.append(Row(p.key, p.version, consensus, status, critical=(status != "match")))
    return rows


def emit_edits(bl: Baseline, new_version: str) -> list[dict]:
    edits = []
    for p in bl.pins:
        if not p.found or p.version == new_version and p.derivation_complete:
            continue
        if p.kind == "alias":
            continue
        edits.append({"file": p.rel, "old": p.raw, "new": new_version})
    return edits


# --------------------------------------------------------------------------- #
# rendering
# --------------------------------------------------------------------------- #
def _print_rows(rows: list[Row], stream=sys.stdout) -> None:
    width = max((len(r.field) for r in rows), default=0)
    for r in rows:
        mark = {"match": "✓", "mismatch": "✗", "info": "·",
                "needs_investigation": "?", "missing": "✗"}.get(r.status, "·")
        line = f"  {mark} {r.field.ljust(width)}  pinned={r.pinned or '-'}  installed={r.installed or '-'}  [{r.status}]"
        if r.note:
            line += f"  ({r.note})"
        print(line, file=stream)


def _print_non_version(bl: Baseline, installed_markers: dict[str, bool] | None) -> None:
    print("\nnon-version pins (verified, never auto-bumped):")
    print(f"  · originator pinned in source = {EXPECTED_ORIGINATOR!r}: "
          f"{'yes' if bl.originator_pinned else 'NO — investigate'}")
    print(f"  · OpenAI-Beta pinned in source = {EXPECTED_BETA!r}: "
          f"{'yes' if bl.beta_pinned else 'NO — investigate'}")
    if installed_markers is None:
        print("  · binary strings: not checked (native binary not located/readable)")
        return
    # Binary-strings is a best-effort POSITIVE confirmation only: a Rust binary may
    # build a header value at runtime (concat / format!) so it is NOT stored as one
    # contiguous literal. So 'present' = confirmed; 'absent' = inconclusive (NOT a
    # drift signal) — only investigate the non-version pins if upstream actually
    # starts rejecting forged requests.
    o = installed_markers["originator"]
    b = installed_markers["beta"]
    print(f"  · binary contains {EXPECTED_ORIGINATOR!r}: "
          f"{'yes (confirmed)' if o else 'not found (inconclusive — may be runtime-built)'}")
    print(f"  · binary contains {EXPECTED_BETA!r}: "
          f"{'yes (confirmed)' if b else 'not found (inconclusive — beta value is runtime-built; verify only if upstream 400s)'}")


# --------------------------------------------------------------------------- #
# subcommands
# --------------------------------------------------------------------------- #
def cmd_check_env(_args) -> int:
    exe = shutil.which("codex")
    if not exe:
        print("  ✗ codex CLI NOT found on PATH (install: npm i -g @openai/codex / brew)")
        return 2
    ver = installed_codex_version()
    print(f"  ✓ codex CLI present ({exe})")
    print(f"  {'✓' if ver else '✗'} codex --version -> {ver or 'unparseable'}")
    binary = locate_codex_binary()
    if binary:
        print(f"  ✓ native binary located ({binary})")
    else:
        print("  · native binary not located (version diff still works; "
              "binary-strings sanity for non-version pins will be skipped)")
    if not ver:
        return 2
    print("check env: ok")
    return 0


def cmd_show_baseline(_args) -> int:
    bl = load_baseline()
    installed = installed_codex_version()
    print(f"installed codex version: {installed or '(not installed)'}")
    print("TK version owner and derived aliases:")
    width = max((len(p.key) for p in bl.pins), default=0)
    for p in bl.pins:
        v = p.version or "(not found)"
        extra = "" if p.derivation_complete else "  [derivation incomplete]"
        print(f"  {p.key.ljust(width)}  {v}  <- {p.rel}{extra}")
    print(f"derivation consensus: {bl.consensus() or '(owner/aliases disagree)'}")
    _print_non_version(bl, None)
    return 0


def cmd_diff(args, gate: bool = False) -> int:
    bl = load_baseline()
    installed = installed_codex_version()
    if not installed:
        print("codex CLI not installed / version unparseable — run check-env.", file=sys.stderr)
        if gate:
            return 2
    rows = diff_pins(bl, installed)
    print(f"Codex fingerprint diff (installed={installed or '-'}):")
    _print_rows(rows)
    binary = locate_codex_binary()
    _print_non_version(bl, binary_markers(binary) if binary else None)
    if has_drift(rows):
        consensus = bl.consensus()
        target = installed or consensus
        if target:
            print(f"\nsuggested bump -> {target}:")
            for e in emit_edits(bl, target):
                print(f"  {e['file']}")
                print(f"    - {e['old']}")
                print(f"    + {e['new']}")
        print("\nfollow ops/openai SKILL: tokenkey-codex-fingerprint-alignment")
    if gate:
        return 1 if has_drift(rows) else 0
    return 0


def cmd_check(args) -> int:
    return cmd_diff(args, gate=True)


def cmd_check_consistency(_args) -> int:
    bl = load_baseline()
    rows = consistency_rows(bl)
    drift = any(r.status != "match" for r in rows)
    if drift:
        print("Codex version owner/aliases are NOT consistent:", file=sys.stderr)
        _print_rows(rows, sys.stderr)
        print(
            "\nRestore the UA default, gateway version, and probe version aliases "
            "to derive from DefaultOpenAICodexVersion. Use emit-edits only to bump "
            "the version owner after this derivation gate passes.",
            file=sys.stderr,
        )
        return 1
    print(f"codex version derivation consistent: source = {bl.consensus()}")
    return 0


def cmd_emit_edits(args) -> int:
    bl = load_baseline()
    target = args.version or installed_codex_version()
    if not target:
        print("no target version (pass --version X.Y.Z or install the codex CLI)", file=sys.stderr)
        return 2
    edits = emit_edits(bl, target)
    if args.json:
        print(json.dumps({"version": target, "edits": edits}, indent=2))
        return 0
    if not edits:
        print(f"codex version owner already at {target} — nothing to edit")
        return 0
    print(f"edits to align the codex version contract -> {target}:")
    for e in edits:
        print(f"  {e['file']}")
        print(f"    - {e['old']}")
        print(f"    + {e['new']}")
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="TokenKey Codex fingerprint alignment engine")
    sub = parser.add_subparsers(dest="cmd", required=True)
    sub.add_parser("check-env")
    sub.add_parser("show-baseline")
    sub.add_parser("diff")
    sub.add_parser("check")
    sub.add_parser("check-consistency")
    pe = sub.add_parser("emit-edits")
    pe.add_argument("--version", default="", help="target version (default: installed codex)")
    pe.add_argument("--json", action="store_true")
    args = parser.parse_args(argv)

    dispatch = {
        "check-env": cmd_check_env,
        "show-baseline": cmd_show_baseline,
        "diff": cmd_diff,
        "check": cmd_check,
        "check-consistency": cmd_check_consistency,
        "emit-edits": cmd_emit_edits,
    }
    return dispatch[args.cmd](args)


if __name__ == "__main__":
    sys.exit(main())
