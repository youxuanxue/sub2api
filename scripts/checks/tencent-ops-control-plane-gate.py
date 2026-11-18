#!/usr/bin/env python3
"""Ban new AWS OIDC→SSM Tencent remotes and SecureString ops-key writes.

SSOT: docs/approved/design-tencent-ops-control-plane.md

Legacy paths are frozen in
ops/tencent-ops/legacy-aws-control-plane-allowlist.json. A hit outside the
allowlist fails closed. An allowlist entry that no longer matches (or is
missing on disk) also fails so migrations must shrink the list in the same PR.

Allowlist growth vs PREFLIGHT_BASE/origin/main is refused (no「先写再迁」switch).
First landing of the allowlist file on a base that lacks it is the only bootstrap.

Usage:
  python3 scripts/checks/tencent-ops-control-plane-gate.py [--quiet] [--base REF]
  python3 scripts/checks/tencent-ops-control-plane-gate.py --selftest
"""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
ALLOWLIST_REL = "ops/tencent-ops/legacy-aws-control-plane-allowlist.json"
ALLOWLIST = ROOT / ALLOWLIST_REL
SSOT = "docs/approved/design-tencent-ops-control-plane.md"

# No leading \\b: Stage0 wraps as aws_ssm send-command (underscore is a word char).
SSM_SEND = re.compile(
    r"ssm\s+send-command\b|"
    r"""["']ssm["']\s*,\s*["']send-command["']|ssm\.send_command\b""",
    re.I,
)
# OIDC workflows that drive Tencent hosts via shared runners even when the
# workflow YAML itself has no literal send-command (closeout A15 gap).
OIDC_REMOTE_HINT = re.compile(
    r"run-probe\.sh|ssm_execution|manage-overlay-runtime\.py|SendCommand",
    re.I,
)
SECURE_WRITE = re.compile(
    r"""put-parameter[\s\S]{0,240}?--type\s+["']?SecureString|--type\s+["']?SecureString""",
    re.I,
)
OIDC = re.compile(r"configure-aws-credentials|AWS_OIDC_ROLE_ARN")
SKIP_PARTS = {"__pycache__", "node_modules", ".git", ".tmp-ops"}
CODE_SUFFIX = {".sh", ".py", ".yml", ".yaml"}
KEYS = ("ssm_remote_paths", "securestring_write_paths", "oidc_ssm_workflow_paths")
# This file names the forbidden literals; do not treat it as a live writer.
SKIP_FILES = {"scripts/checks/tencent-ops-control-plane-gate.py"}


def is_test(path: Path) -> bool:
    name = path.name
    return name.startswith("test_") or name.endswith("_test.py") or name.endswith("_test.sh")


def parse_allowlist_payload(data: object) -> dict[str, list[str]]:
    if not isinstance(data, dict):
        raise ValueError("allowlist must be a JSON object")
    if data.get("ssot") != SSOT:
        raise ValueError(f"allowlist ssot must be {SSOT}")
    out: dict[str, list[str]] = {}
    for key in KEYS:
        value = data.get(key)
        if not isinstance(value, list) or not all(isinstance(item, str) and item for item in value):
            raise ValueError(f"allowlist {key} must be a list of non-empty strings")
        out[key] = sorted(set(value))
    return out


def load_allowlist(path: Path = ALLOWLIST) -> dict[str, list[str]]:
    return parse_allowlist_payload(json.loads(path.read_text(encoding="utf-8")))


def _git(*args: str) -> str:
    return subprocess.check_output(
        ["git", "-C", str(ROOT), *args],
        text=True,
        stderr=subprocess.DEVNULL,
    )


def read_allowlist_at(base: str) -> dict[str, list[str]] | None:
    """Return parsed allowlist at ``base``, or None if the file is absent."""
    try:
        text = _git("show", f"{base}:{ALLOWLIST_REL}")
    except subprocess.CalledProcessError:
        return None
    if not text.strip():
        return None
    return parse_allowlist_payload(json.loads(text))


def allowlist_growth(base_list: dict[str, list[str]], head_list: dict[str, list[str]]) -> list[str]:
    failures: list[str] = []
    for key in KEYS:
        added = sorted(set(head_list[key]) - set(base_list[key]))
        for rel in added:
            failures.append(f"allowlist {key} grew vs base (refused): {rel}")
    return failures


def iter_code_files(root: Path) -> list[Path]:
    found: list[Path] = []
    for base in (root / ".github/workflows", root / "ops", root / "scripts", root / "deploy"):
        if not base.is_dir():
            continue
        for path in base.rglob("*"):
            if not path.is_file() or path.suffix not in CODE_SUFFIX:
                continue
            if any(part in SKIP_PARTS for part in path.parts):
                continue
            if is_test(path) or "cloudformation" in path.parts:
                continue
            rel = path.relative_to(root).as_posix()
            if rel in SKIP_FILES:
                continue
            found.append(path)
    return found


def scan_hits(root: Path) -> dict[str, set[str]]:
    ssm: set[str] = set()
    secure: set[str] = set()
    for path in iter_code_files(root):
        try:
            text = path.read_text(encoding="utf-8")
        except (OSError, UnicodeDecodeError):
            continue
        rel = path.relative_to(root).as_posix()
        if SSM_SEND.search(text):
            ssm.add(rel)
        if SECURE_WRITE.search(text):
            secure.add(rel)

    oidc_ssm: set[str] = set()
    workflows = root / ".github/workflows"
    if workflows.is_dir():
        for path in sorted(workflows.glob("*.yml")):
            try:
                text = path.read_text(encoding="utf-8")
            except (OSError, UnicodeDecodeError):
                continue
            if not OIDC.search(text):
                continue
            rel = path.relative_to(root).as_posix()
            if (
                SSM_SEND.search(text)
                or OIDC_REMOTE_HINT.search(text)
                or any(candidate in text for candidate in ssm)
            ):
                oidc_ssm.add(rel)
    return {
        "ssm_remote_paths": ssm,
        "securestring_write_paths": secure,
        "oidc_ssm_workflow_paths": oidc_ssm,
    }


def evaluate(
    root: Path,
    allowlist: dict[str, list[str]],
    *,
    base_allowlist: dict[str, list[str]] | None = None,
) -> list[str]:
    failures: list[str] = []
    hits = scan_hits(root)
    for key in KEYS:
        allowed = set(allowlist[key])
        actual = hits[key]
        for rel in sorted(actual - allowed):
            failures.append(f"new {key} hit not on allowlist: {rel}")
        for rel in sorted(allowed - actual):
            path = root / rel
            if not path.is_file():
                failures.append(f"allowlist {key} missing on disk (remove from allowlist): {rel}")
            else:
                failures.append(
                    f"allowlist {key} no longer matches (shrink allowlist after migration): {rel}"
                )
    if base_allowlist is not None:
        failures.extend(allowlist_growth(base_allowlist, allowlist))
    return failures


def selftest() -> list[str]:
    failures: list[str] = []
    try:
        allowlist = load_allowlist()
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        return [f"cannot load allowlist: {exc}"]

    # Bootstrap base (no prior allowlist): current tree must still match inventory.
    baseline = evaluate(ROOT, allowlist, base_allowlist=None)
    if baseline:
        failures.append("baseline repository must pass before selftest mutations")
        failures.extend(baseline)
        return failures

    # Growth vs a frozen base must fail; shrink must pass.
    grown = {key: list(values) for key, values in allowlist.items()}
    grown["ssm_remote_paths"] = sorted(
        set(grown["ssm_remote_paths"]) | {"ops/stage0/brand-new-ssm.sh"}  # script-ref-allow-missing: selftest fixture
    )
    growth_hits = allowlist_growth(allowlist, grown)
    if not growth_hits:
        failures.append("allowlist growth must fail closed")
    shrunk = {key: list(values) for key, values in allowlist.items()}
    if shrunk["oidc_ssm_workflow_paths"]:
        shrunk["oidc_ssm_workflow_paths"] = shrunk["oidc_ssm_workflow_paths"][:-1]
    if allowlist_growth(allowlist, shrunk):
        failures.append("allowlist shrink must be allowed")

    with tempfile.TemporaryDirectory() as tmp:
        staging = Path(tmp)
        # Minimal tree: copy allowlist only; empty scan should flag every allowlist entry missing.
        (staging / "ops/tencent-ops").mkdir(parents=True)
        (staging / "ops/tencent-ops/legacy-aws-control-plane-allowlist.json").write_text(
            ALLOWLIST.read_text(encoding="utf-8"), encoding="utf-8"
        )
        empty = evaluate(staging, allowlist)
        if not any("missing on disk" in item for item in empty):
            failures.append("empty tree must report missing allowlist paths")

    with tempfile.TemporaryDirectory() as tmp:
        staging = Path(tmp)
        (staging / ".github/workflows").mkdir(parents=True)
        (staging / "ops/stage0").mkdir(parents=True)
        # Seed allowlisted file so oidc workflow can reference a real ssm path.
        legacy = staging / "ops/stage0/legacy-ssm.sh"  # script-ref-allow-missing: selftest fixture
        legacy.write_text("#!/bin/bash\naws ssm send-command --instance-ids i-1\n", encoding="utf-8")
        new_script = staging / "ops/stage0/brand-new-ssm.sh"  # script-ref-allow-missing: selftest fixture
        new_script.write_text("#!/bin/bash\naws ssm send-command --instance-ids i-2\n", encoding="utf-8")
        new_secret = staging / "ops/stage0/brand-new-secret.sh"  # script-ref-allow-missing: selftest fixture
        new_secret.write_text(
            "#!/bin/bash\naws ssm put-parameter --name /x --type SecureString --value secret\n",
            encoding="utf-8",
        )
        new_wf = staging / ".github/workflows/brand-new-oidc-ssm.yml"
        new_wf.write_text(
            "jobs:\n  x:\n    steps:\n"
            "      - uses: aws-actions/configure-aws-credentials@v6\n"
            "      - run: bash ops/stage0/legacy-ssm.sh\n",  # script-ref-allow-missing: selftest fixture
            encoding="utf-8",
        )
        tiny_allow = {
            "ssm_remote_paths": ["ops/stage0/legacy-ssm.sh"],  # script-ref-allow-missing: selftest fixture
            "securestring_write_paths": [],
            "oidc_ssm_workflow_paths": [],
        }
        found = evaluate(staging, tiny_allow)
        need = (
            "new ssm_remote_paths hit not on allowlist: ops/stage0/brand-new-ssm.sh",  # script-ref-allow-missing: selftest fixture
            "new securestring_write_paths hit not on allowlist: ops/stage0/brand-new-secret.sh",  # script-ref-allow-missing: selftest fixture
            "new oidc_ssm_workflow_paths hit not on allowlist: .github/workflows/brand-new-oidc-ssm.yml",
        )
        for needle in need:
            if needle not in found:
                failures.append(f"selftest missing expected failure: {needle}")

    return failures


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--quiet", action="store_true")
    parser.add_argument("--selftest", action="store_true")
    parser.add_argument(
        "--base",
        default=os.environ.get("PREFLIGHT_BASE", "origin/main"),
        help="git ref for allowlist monotonic-shrink check (default: PREFLIGHT_BASE or origin/main)",
    )
    args = parser.parse_args()

    if args.selftest:
        failures = selftest()
        if failures:
            print("FAIL: tencent-ops control-plane gate selftest", file=sys.stderr)
            for item in failures:
                print(f"  - {item}", file=sys.stderr)
            return 1
        print("ok: tencent-ops control-plane gate selftest")
        return 0

    try:
        allowlist = load_allowlist()
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        print(f"FAIL: cannot load {ALLOWLIST.relative_to(ROOT)}: {exc}", file=sys.stderr)
        return 2

    try:
        base_allowlist = read_allowlist_at(args.base)
    except (ValueError, json.JSONDecodeError) as exc:
        print(f"FAIL: cannot parse allowlist at {args.base}: {exc}", file=sys.stderr)
        return 2

    failures = evaluate(ROOT, allowlist, base_allowlist=base_allowlist)
    if failures:
        print(
            "FAIL: new AWS OIDC→SSM Tencent remote or SecureString write outside allowlist",
            file=sys.stderr,
        )
        for item in failures:
            print(f"  - {item}", file=sys.stderr)
        print(
            f"  SSOT: {SSOT}; shrink allowlist when migrating; do not expand for new defaults",
            file=sys.stderr,
        )
        return 1

    if not args.quiet:
        counts = ", ".join(f"{key}={len(allowlist[key])}" for key in KEYS)
        print(f"ok: tencent-ops control-plane ban ({counts})")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
