#!/usr/bin/env python3
"""Guard against typos in CloudFormation `AWSTemplateFormatVersion`.

There is exactly ONE valid value: "2010-09-09". The value is frozen by AWS
forever; any other date string makes `aws cloudformation deploy /
validate-template` fail with:

    Template format error: <typo> is not a supported value for AWSTemplateFormatVersion

This is a particularly nasty failure mode because:
  1. The template parses as valid YAML.
  2. The typo only surfaces at AWS deploy time — local lint won't catch it.
  3. The error message is misleading (suggests the version field is "weird"
     rather than telling you "use 2010-09-09 exactly").

Real incident: PR #380's `cicd-oidc-lightsail-addon.yaml` shipped with the
typo `2010-10-09` (off by one month). It was missed in two /xj-review
rounds. The bug only surfaced during the Phase 1 migration setup when
`aws cloudformation deploy` was first run. This check makes the typo a
preflight FAIL so future templates cannot ship the same regression.

stdlib-only.
"""
from __future__ import annotations

import pathlib
import re
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
CFN_DIR = REPO_ROOT / "deploy/aws/cloudformation"

VALID_VERSION = "2010-09-09"
VERSION_RE = re.compile(
    r'^\s*AWSTemplateFormatVersion\s*:\s*[\'"]?([0-9]{4}-[0-9]{2}-[0-9]{2})[\'"]?\s*$'
)


def check(path: pathlib.Path) -> tuple[bool, str]:
    """Return (ok, message). ok=True means the file is fine."""
    try:
        text = path.read_text(encoding="utf-8")
    except OSError as exc:
        return False, f"cannot read: {exc}"
    for lineno, line in enumerate(text.splitlines(), 1):
        m = VERSION_RE.match(line)
        if m:
            actual = m.group(1)
            if actual == VALID_VERSION:
                return True, f"line {lineno}: {actual} (valid)"
            return False, (
                f"line {lineno}: AWSTemplateFormatVersion={actual!r}, "
                f"must be {VALID_VERSION!r} (the only value AWS accepts)"
            )
    # No version line at all is fine — AWS treats it as optional, defaulting
    # to current. We only fail on a present-but-wrong value.
    return True, "no AWSTemplateFormatVersion declared"


def main() -> int:
    if not CFN_DIR.is_dir():
        # No templates in this project; nothing to check.
        return 0

    errors: list[tuple[pathlib.Path, str]] = []
    checked = 0
    for path in sorted(CFN_DIR.rglob("*.yaml")):
        checked += 1
        ok, msg = check(path)
        if not ok:
            errors.append((path, msg))
    for path in sorted(CFN_DIR.rglob("*.yml")):
        checked += 1
        ok, msg = check(path)
        if not ok:
            errors.append((path, msg))

    if not errors:
        print(f"ok: {checked} CloudFormation template(s) carry the valid version")
        return 0

    print("FAIL: invalid AWSTemplateFormatVersion in CloudFormation template(s):", file=sys.stderr)
    for path, msg in errors:
        rel = path.relative_to(REPO_ROOT)
        print(f"  - {rel} → {msg}", file=sys.stderr)
    print(
        f"  Fix: every CFN template must declare AWSTemplateFormatVersion: \"{VALID_VERSION}\" "
        "(or omit it entirely). This is the only value AWS supports.",
        file=sys.stderr,
    )
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
