#!/usr/bin/env python3
"""
check-cc-version-sync.py — keep the Claude Code CLI version string in lockstep
across every place that carries it, with a single source of truth.

Source of truth (the ONE field a cc patch bump should edit):
  deploy/aws/stage0/anthropic-http-mimicry-baselines.json  ->  cc_version

This is the value `manage-anthropic-config.py sync-runtime` pushes to the live
fleet, so it is the authoritative cc version. Every other copy must agree with
it. Before this guard a cc bump (e.g. 2.1.158 -> 2.1.159, PR #482) required
hand-editing ~10 files and 5 of those edits were wrong — dead snapshots and
comments drifted because nothing mechanically checked them.

Two classes of derived copy:

1. Go compile-time fallback defaults — PARSED and compared, never auto-written
   (go:embed cannot reach deploy/ from the backend module, and these are the
   last-resort defaults a brand-new deploy boots with, so they stay hand-edited
   on a cc bump; this guard just refuses to let them drift):
     - backend/internal/pkg/claude/constants.go
         CLICurrentVersion             = "X.Y.Z"
         DefaultHeaders["User-Agent"]  = "claude-cli/X.Y.Z (external, sdk-cli)"
     - backend/internal/service/identity_service.go
         defaultFingerprint.UserAgent  = "claude-cli/X.Y.Z (external, sdk-cli)"
     - backend/internal/service/identity_service_tk_canonical_http.go
         DefaultClaudeCodeUserAgentVersion = "X.Y.Z"

2. Pure dead snapshots — compared, and AUTO-REGENERATED with --write so a cc
   bump no longer hand-touches them:
     - deploy/aws/stage0/tk_canonical_cc_oauth.json   observed.user_agent
     - ops/stage0/smoke_lib.sh                        smoke_default_claude_user_agent default

Exit codes:
  0  — every copy agrees with cc_version.
  1  — drift detected (some copy != cc_version).
  2  — file missing, parse failure, or required symbol not found.

Usage:
  python3 scripts/sentinels/check-cc-version-sync.py            # check, verbose
  python3 scripts/sentinels/check-cc-version-sync.py --quiet    # check, print only on failure
  python3 scripts/sentinels/check-cc-version-sync.py --json     # machine-readable
  python3 scripts/sentinels/check-cc-version-sync.py --write    # regenerate dead snapshots from source of truth
  python3 scripts/sentinels/check-cc-version-sync.py --selftest # run built-in fixture self-test

cc bump workflow (see tokenkey-cc-fingerprint-alignment skill):
  1. edit cc_version in anthropic-http-mimicry-baselines.json
  2. run this script with --write  (regenerates the dead snapshots)
  3. hand-edit the 4 Go compile defaults listed above
  4. re-run this script (--check)  — must exit 0; preflight enforces it
"""
from __future__ import annotations

import argparse
import json
import re
import sys
import tempfile
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent.parent
SOURCE_JSON = REPO_ROOT / "deploy" / "aws" / "stage0" / "anthropic-http-mimicry-baselines.json"

CONSTANTS_GO = REPO_ROOT / "backend" / "internal" / "pkg" / "claude" / "constants.go"
IDENTITY_GO = REPO_ROOT / "backend" / "internal" / "service" / "identity_service.go"
CANONICAL_HTTP_GO = (
    REPO_ROOT / "backend" / "internal" / "service" / "identity_service_tk_canonical_http.go"
)
CANONICAL_TLS_JSON = REPO_ROOT / "deploy" / "aws" / "stage0" / "tk_canonical_cc_oauth.json"
SMOKE_LIB_SH = REPO_ROOT / "ops" / "stage0" / "smoke_lib.sh"

SEMVER_RE = re.compile(r"\d+\.\d+\.\d+")

# Each parsed copy: one regex with a single capture group around the bare semver
# (so the field name stays anchored in the pattern — no column-number reads).
# label -> (file, compiled regex). The regex MUST capture exactly the X.Y.Z.
_PARSE_TARGETS = {
    "constants.go CLICurrentVersion": (
        CONSTANTS_GO,
        re.compile(r'const\s+CLICurrentVersion\s*=\s*"(\d+\.\d+\.\d+)"'),
    ),
    'constants.go DefaultHeaders["User-Agent"]': (
        CONSTANTS_GO,
        re.compile(r'"User-Agent":\s*"claude-cli/(\d+\.\d+\.\d+) \(external, sdk-cli\)"'),
    ),
    "identity_service.go defaultFingerprint.UserAgent": (
        IDENTITY_GO,
        re.compile(r'UserAgent:\s*"claude-cli/(\d+\.\d+\.\d+) \(external, sdk-cli\)"'),
    ),
    "identity_service_tk_canonical_http.go DefaultClaudeCodeUserAgentVersion": (
        CANONICAL_HTTP_GO,
        re.compile(r'DefaultClaudeCodeUserAgentVersion\s*=\s*"(\d+\.\d+\.\d+)"'),
    ),
}

# Dead snapshots: (file, parse regex, rewrite-template builder). The builder
# takes the new version and returns the full replacement line text. The parse
# regex captures the current semver for comparison.
_SMOKE_RE = re.compile(
    r'(printf \'%s\' "\$\{TK_SMOKE_CLAUDE_USER_AGENT:-claude-cli/)(\d+\.\d+\.\d+)( \(external, sdk-cli\)\}")'
)
_OBSERVED_UA_RE = re.compile(
    r'("user_agent":\s*"claude-cli/)(\d+\.\d+\.\d+)( \(external, sdk-cli\)")'
)


class CheckError(Exception):
    pass


def _read(path: Path) -> str:
    if not path.is_file():
        raise CheckError(f"file not found: {path.relative_to(REPO_ROOT)}")
    return path.read_text(encoding="utf-8")


def load_source_version(source_json: Path = SOURCE_JSON) -> str:
    text = _read(source_json)
    try:
        data = json.loads(text)
    except json.JSONDecodeError as exc:
        raise CheckError(f"{source_json.relative_to(REPO_ROOT)} parse error: {exc}") from exc
    cc = data.get("cc_version")
    if not isinstance(cc, str) or not SEMVER_RE.fullmatch(cc.strip()):
        raise CheckError(
            f"{source_json.relative_to(REPO_ROOT)} cc_version missing or not X.Y.Z: {cc!r}"
        )
    return cc.strip()


def parse_copy(label: str, path: Path, regex: re.Pattern[str]) -> str:
    m = regex.search(_read(path))
    if not m:
        raise CheckError(
            f"{label}: pattern not found in {path.relative_to(REPO_ROOT)} "
            "(symbol renamed or reformatted? update check-cc-version-sync.py)"
        )
    return m.group(1)


def _parse_dead(label: str, path: Path, regex: re.Pattern[str], group: int) -> str:
    m = regex.search(_read(path))
    if not m:
        raise CheckError(
            f"{label}: pattern not found in {path.relative_to(REPO_ROOT)} "
            "(reformatted? update check-cc-version-sync.py)"
        )
    return m.group(group)


def gather(
    expected: str,
    *,
    source_json: Path = SOURCE_JSON,
    parse_targets: dict[str, tuple[Path, re.Pattern[str]]] = _PARSE_TARGETS,
    smoke_path: Path = SMOKE_LIB_SH,
    canonical_tls_path: Path = CANONICAL_TLS_JSON,
) -> list[dict[str, object]]:
    findings: list[dict[str, object]] = []
    for label, (path, regex) in parse_targets.items():
        found = parse_copy(label, path, regex)
        findings.append(
            {"label": label, "kind": "go-default", "found": found, "ok": found == expected}
        )
    smoke_found = _parse_dead(
        "smoke_lib.sh smoke_default_claude_user_agent", smoke_path, _SMOKE_RE, 2
    )
    findings.append(
        {"label": "smoke_lib.sh smoke_default_claude_user_agent", "kind": "dead",
         "found": smoke_found, "ok": smoke_found == expected}
    )
    observed_found = _parse_dead(
        "tk_canonical_cc_oauth.json observed.user_agent", canonical_tls_path, _OBSERVED_UA_RE, 2
    )
    findings.append(
        {"label": "tk_canonical_cc_oauth.json observed.user_agent", "kind": "dead",
         "found": observed_found, "ok": observed_found == expected}
    )
    return findings


def _rel(path: Path) -> str:
    try:
        return str(path.relative_to(REPO_ROOT))
    except ValueError:
        return str(path)


def write_dead_snapshots(
    expected: str,
    *,
    smoke_path: Path = SMOKE_LIB_SH,
    canonical_tls_path: Path = CANONICAL_TLS_JSON,
) -> list[str]:
    """Rewrite the dead snapshots to `expected`. Returns list of changed files."""
    changed: list[str] = []

    smoke_text = _read(smoke_path)
    new_smoke, n = _SMOKE_RE.subn(lambda m: m.group(1) + expected + m.group(3), smoke_text)
    if n == 0:
        raise CheckError(f"smoke_lib.sh: nothing to rewrite (pattern not found)")
    if new_smoke != smoke_text:
        smoke_path.write_text(new_smoke, encoding="utf-8")
        changed.append(_rel(smoke_path))

    tls_text = _read(canonical_tls_path)
    new_tls, n = _OBSERVED_UA_RE.subn(lambda m: m.group(1) + expected + m.group(3), tls_text)
    if n == 0:
        raise CheckError(f"tk_canonical_cc_oauth.json: nothing to rewrite (pattern not found)")
    if new_tls != tls_text:
        canonical_tls_path.write_text(new_tls, encoding="utf-8")
        changed.append(_rel(canonical_tls_path))

    return changed


def run_check(args: argparse.Namespace) -> int:
    expected = load_source_version()
    findings = gather(expected)
    drift = [f for f in findings if not f["ok"]]

    if args.json:
        print(json.dumps(
            {"expected": expected, "ok": not drift, "findings": findings},
            indent=2, sort_keys=True,
        ))
        return 1 if drift else 0

    if not drift:
        if not args.quiet:
            print(
                f"OK: cc_version={expected} consistent across "
                f"{len(findings)} copies (source: {SOURCE_JSON.relative_to(REPO_ROOT)})"
            )
        return 0

    print(
        f"FAIL: cc version DRIFT — source of truth "
        f"{SOURCE_JSON.relative_to(REPO_ROOT)} cc_version={expected}",
        file=sys.stderr,
    )
    has_dead = False
    for f in drift:
        print(f"  {f['label']}: found {f['found']} != {expected}", file=sys.stderr)
        if f["kind"] == "dead":
            has_dead = True
    if has_dead:
        print(
            "Fix dead snapshots: python3 scripts/sentinels/check-cc-version-sync.py --write",
            file=sys.stderr,
        )
    if any(f["kind"] == "go-default" for f in drift):
        print(
            "Fix Go compile defaults by hand (cannot be auto-derived: go:embed "
            "cannot reach deploy/): edit the listed constants to "
            f"{expected}.",
            file=sys.stderr,
        )
    return 1


def run_write(args: argparse.Namespace) -> int:
    expected = load_source_version()
    changed = write_dead_snapshots(expected)
    if changed:
        for c in changed:
            print(f"wrote {c} -> cc_version={expected}")
    else:
        print(f"no change: dead snapshots already at cc_version={expected}")
    # Re-check Go defaults and report (write does not touch them).
    findings = gather(expected)
    go_drift = [f for f in findings if f["kind"] == "go-default" and not f["ok"]]
    if go_drift:
        print(
            "NOTE: Go compile defaults still drift (hand-edit required):",
            file=sys.stderr,
        )
        for f in go_drift:
            print(f"  {f['label']}: {f['found']} != {expected}", file=sys.stderr)
        return 1
    return 0


# ---------------------------------------------------------------------------
# Built-in self-test: build a throwaway fixture tree, prove check + write.
# Runs alongside preflight so the guard's own logic stays honest.
# ---------------------------------------------------------------------------
def run_selftest() -> int:
    failures: list[str] = []

    def expect(cond: bool, msg: str) -> None:
        if not cond:
            failures.append(msg)

    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        src = root / "src.json"
        smoke = root / "smoke_lib.sh"
        tls = root / "tk.json"
        go_const = root / "constants.go"

        src.write_text(json.dumps({"schema_version": 1, "cc_version": "9.9.9"}), encoding="utf-8")
        smoke.write_text(
            'printf \'%s\' "${TK_SMOKE_CLAUDE_USER_AGENT:-claude-cli/1.0.0 (external, sdk-cli)}"\n',
            encoding="utf-8",
        )
        tls.write_text(
            json.dumps({"observed": {"user_agent": "claude-cli/1.0.0 (external, sdk-cli)"}}, indent=2),
            encoding="utf-8",
        )
        go_const.write_text('const CLICurrentVersion = "1.0.0"\n', encoding="utf-8")

        expected = load_source_version(src)
        expect(expected == "9.9.9", f"source version parse: got {expected!r}")

        targets = {
            "constants.go CLICurrentVersion": (
                go_const,
                re.compile(r'const\s+CLICurrentVersion\s*=\s*"(\d+\.\d+\.\d+)"'),
            ),
        }

        # before write: drift expected (smoke/tls/go all 1.0.0 != 9.9.9)
        findings = gather(expected, parse_targets=targets, smoke_path=smoke, canonical_tls_path=tls)
        expect(all(not f["ok"] for f in findings), "pre-write: all copies should drift")

        # --write regenerates the two dead snapshots
        changed = write_dead_snapshots(expected, smoke_path=smoke, canonical_tls_path=tls)
        expect(len(changed) == 2, f"write should change 2 files, got {changed}")

        findings = gather(expected, parse_targets=targets, smoke_path=smoke, canonical_tls_path=tls)
        dead_ok = [f for f in findings if f["kind"] == "dead"]
        expect(all(f["ok"] for f in dead_ok), "post-write: dead snapshots should match")
        go_f = [f for f in findings if f["kind"] == "go-default"]
        expect(all(not f["ok"] for f in go_f), "post-write: go default still drifts (hand-edit)")

        # fix go default by hand -> all green
        go_const.write_text('const CLICurrentVersion = "9.9.9"\n', encoding="utf-8")
        findings = gather(expected, parse_targets=targets, smoke_path=smoke, canonical_tls_path=tls)
        expect(all(f["ok"] for f in findings), "after hand-edit: all copies should match")

    if failures:
        print("SELFTEST FAIL:", file=sys.stderr)
        for f in failures:
            print(f"  - {f}", file=sys.stderr)
        return 1
    print("ok: check-cc-version-sync self-test passed")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description="cc version single-source guard")
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--write", action="store_true", help="regenerate dead snapshots from cc_version")
    mode.add_argument("--selftest", action="store_true", help="run built-in fixture self-test")
    parser.add_argument("--quiet", action="store_true", help="check mode: print only on failure")
    parser.add_argument("--json", action="store_true", help="check mode: machine-readable report")
    args = parser.parse_args()

    if args.json and args.quiet:
        print("FATAL: --json and --quiet are mutually exclusive", file=sys.stderr)
        return 2

    try:
        if args.selftest:
            return run_selftest()
        if args.write:
            return run_write(args)
        return run_check(args)
    except CheckError as exc:
        print(f"FATAL: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
