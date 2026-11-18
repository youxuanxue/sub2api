#!/usr/bin/env python3
"""Ban new AWS OIDC→SSM Tencent remotes and SecureString ops-key writes.

SSOT: docs/approved/design-tencent-ops-control-plane.md

Legacy paths are frozen in
ops/tencent-ops/legacy-aws-control-plane-allowlist.json. A hit outside the
allowlist fails closed. An allowlist entry that no longer matches (or is
missing on disk) also fails so migrations must shrink the list in the same PR.

Usage:
  python3 scripts/checks/tencent-ops-control-plane-gate.py [--quiet]
  python3 scripts/checks/tencent-ops-control-plane-gate.py --selftest
"""

from __future__ import annotations

import argparse
import json
import re
import sys
import tempfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
ALLOWLIST = ROOT / "ops/tencent-ops/legacy-aws-control-plane-allowlist.json"
SSOT = "docs/approved/design-tencent-ops-control-plane.md"

# No leading \\b: Stage0 wraps as aws_ssm send-command (underscore is a word char).
SSM_SEND = re.compile(
    r"ssm\s+send-command\b|"
    r"""["']ssm["']\s*,\s*["']send-command["']|ssm\.send_command\b""",
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


def load_allowlist(path: Path = ALLOWLIST) -> dict[str, list[str]]:
    data = json.loads(path.read_text(encoding="utf-8"))
    if data.get("ssot") != SSOT:
        raise ValueError(f"allowlist ssot must be {SSOT}")
    out: dict[str, list[str]] = {}
    for key in KEYS:
        value = data.get(key)
        if not isinstance(value, list) or not all(isinstance(item, str) and item for item in value):
            raise ValueError(f"allowlist {key} must be a list of non-empty strings")
        out[key] = sorted(set(value))
    return out


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
            if SSM_SEND.search(text) or any(candidate in text for candidate in ssm):
                oidc_ssm.add(rel)
    return {
        "ssm_remote_paths": ssm,
        "securestring_write_paths": secure,
        "oidc_ssm_workflow_paths": oidc_ssm,
    }


def evaluate(root: Path, allowlist: dict[str, list[str]]) -> list[str]:
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
    return failures


def selftest() -> list[str]:
    failures: list[str] = []
    try:
        allowlist = load_allowlist()
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        return [f"cannot load allowlist: {exc}"]

    baseline = evaluate(ROOT, allowlist)
    if baseline:
        failures.append("baseline repository must pass before selftest mutations")
        failures.extend(baseline)
        return failures

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
        legacy = staging / "ops/stage0/legacy-ssm.sh"
        legacy.write_text("#!/bin/bash\naws ssm send-command --instance-ids i-1\n", encoding="utf-8")
        new_script = staging / "ops/stage0/brand-new-ssm.sh"
        new_script.write_text("#!/bin/bash\naws ssm send-command --instance-ids i-2\n", encoding="utf-8")
        new_secret = staging / "ops/stage0/brand-new-secret.sh"
        new_secret.write_text(
            "#!/bin/bash\naws ssm put-parameter --name /x --type SecureString --value secret\n",
            encoding="utf-8",
        )
        new_wf = staging / ".github/workflows/brand-new-oidc-ssm.yml"
        new_wf.write_text(
            "jobs:\n  x:\n    steps:\n"
            "      - uses: aws-actions/configure-aws-credentials@v6\n"
            "      - run: bash ops/stage0/legacy-ssm.sh\n",
            encoding="utf-8",
        )
        tiny_allow = {
            "ssm_remote_paths": ["ops/stage0/legacy-ssm.sh"],
            "securestring_write_paths": [],
            "oidc_ssm_workflow_paths": [],
        }
        found = evaluate(staging, tiny_allow)
        need = (
            "new ssm_remote_paths hit not on allowlist: ops/stage0/brand-new-ssm.sh",
            "new securestring_write_paths hit not on allowlist: ops/stage0/brand-new-secret.sh",
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

    failures = evaluate(ROOT, allowlist)
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

    counts = ", ".join(f"{key}={len(allowlist[key])}" for key in KEYS)
    msg = f"ok: tencent-ops control-plane ban ({counts})"
    print(msg if not args.quiet else msg)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
