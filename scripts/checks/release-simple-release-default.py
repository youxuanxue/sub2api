#!/usr/bin/env python3
"""Verify .github/workflows/release.yml keeps simple_release input default = false.

Prod (api.tokenkey.dev) and Edge Stage0 hosts run on AWS Graviton (arm64).
`simple_release=true` makes GoReleaser build linux/amd64 ONLY and then overwrites
the shared `:latest` / `:X` / `:X.Y` / `:X.Y.Z` tags with that single-arch image.
Any ARM host pulling those tags crashes immediately with `exec format error`.

CLAUDE.md §9.1 ("simple_release MUST stay false") prohibits flipping the default.
But "prose rule" = "depends on human memory" = OPC anti-pattern. This check
mechanizes the rule: if anyone flips the workflow_dispatch input default to true
(or removes the default entirely), preflight + CI fail before merge.

The fix path is also encoded in the release workflow itself: a `Warn on
simple_release mode` step prints a `::warning::` banner when SIMPLE_RELEASE
evaluates true at runtime. That covers in-flight misuse; this check covers
the at-rest default.

Usage: python3 scripts/checks/release-simple-release-default.py [--quiet]
Exit 0 ok, 1 violation, 2 missing dep / file / unparseable structure.
"""

from __future__ import annotations

import sys
import pathlib

try:
    import yaml  # PyYAML
except ImportError:
    print(
        "  err: PyYAML not installed (required to parse release.yml).\n"
        "       fix: python3 -m pip install --user pyyaml",
        flush=True,
    )
    sys.exit(2)

REPO_ROOT = pathlib.Path(__file__).resolve().parent.parent.parent
RELEASE_YML = REPO_ROOT / ".github" / "workflows" / "release.yml"


def main(argv: list[str]) -> int:
    quiet = "--quiet" in argv
    if not RELEASE_YML.is_file():
        print(f"  err: {RELEASE_YML.relative_to(REPO_ROOT)} not found", flush=True)
        return 2
    try:
        doc = yaml.safe_load(RELEASE_YML.read_text())
    except yaml.YAMLError as exc:
        print(f"  err: cannot parse release.yml: {exc}", flush=True)
        return 2

    # YAML parses the bare key `on:` as boolean True (Norway problem cousin),
    # so accept both string "on" and True.
    on_block = None
    if isinstance(doc, dict):
        on_block = doc.get("on") if "on" in doc else doc.get(True)
    if not isinstance(on_block, dict):
        print("  err: release.yml has no `on:` block (or unexpected shape)", flush=True)
        return 2

    dispatch = on_block.get("workflow_dispatch")
    if not isinstance(dispatch, dict):
        print(
            "  err: release.yml on.workflow_dispatch missing — cannot verify simple_release default",
            flush=True,
        )
        return 2

    inputs = dispatch.get("inputs")
    if not isinstance(inputs, dict) or "simple_release" not in inputs:
        print(
            "  err: release.yml on.workflow_dispatch.inputs.simple_release missing — "
            "the input itself was deleted",
            flush=True,
        )
        return 1

    spec = inputs["simple_release"]
    if not isinstance(spec, dict):
        print(
            "  err: release.yml simple_release input is not a mapping (cannot read default)",
            flush=True,
        )
        return 2

    if "default" not in spec:
        print(
            "  err: release.yml simple_release input has no `default:` field. "
            "Without a default, an empty dispatch falls back to the input type's zero "
            "value (False for boolean) — but relying on that is fragile, set "
            "`default: false` explicitly.",
            flush=True,
        )
        return 1

    default = spec["default"]
    # PyYAML parses `default: false` → Python False; `default: "false"` → "false"
    if default is False:
        if not quiet:
            print("  ok: release.yml simple_release default = false (Graviton-safe)", flush=True)
        return 0

    print(
        f"  err: release.yml on.workflow_dispatch.inputs.simple_release.default = {default!r} "
        "(expected literal `false`).",
        flush=True,
    )
    print("", flush=True)
    print(
        "  Why this matters (CLAUDE.md §9.1): prod + all Edge Stage0 hosts run on AWS",
        flush=True,
    )
    print(
        "  Graviton (arm64). simple_release=true builds linux/amd64 only and OVERWRITES",
        flush=True,
    )
    print(
        "  the shared `:latest` / `:X` / `:X.Y` / `:X.Y.Z` tags. Any ARM host pulling",
        flush=True,
    )
    print(
        "  those tags crashes immediately with `exec format error`. Revert the default",
        flush=True,
    )
    print(
        "  to `false`; if a one-off amd64 release is truly needed, dispatch the workflow",
        flush=True,
    )
    print(
        "  manually with simple_release=true rather than changing the at-rest default.",
        flush=True,
    )
    return 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
