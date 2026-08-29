#!/usr/bin/env python3
"""Guard the release Go cache contract between warm and tag restore."""

from __future__ import annotations

import argparse
import pathlib
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
WARM_WORKFLOW = REPO_ROOT / ".github" / "workflows" / "warm-release-cache-main.yml"
RELEASE = REPO_ROOT / ".github" / "workflows" / "release.yml"
SHARED_KEY_PREFIX = "${{ runner.os }}-go-release-v1-"
SAVE_ACTION = "actions/cache/save@v6"
RESTORE_ACTION = "actions/cache/restore@v6"


def _fail(quiet: bool, msg: str) -> None:
    if not quiet:
        sys.stderr.write(f"  FAIL: {msg}\n")


def _actions_for_marker(text: str, marker: str) -> list[str]:
    actions: list[str] = []
    lines = text.splitlines()
    current_uses = ""
    for line in lines:
        stripped = line.strip()
        if stripped.startswith("uses:") or stripped.startswith("- uses:"):
            current_uses = stripped.split("uses:", 1)[1].strip()
        if marker in line and not stripped.startswith("#") and current_uses:
            actions.append(current_uses)
    return actions


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--quiet", action="store_true")
    args = parser.parse_args()
    errors = 0

    ci_text = WARM_WORKFLOW.read_text()
    rel_text = RELEASE.read_text()
    if SHARED_KEY_PREFIX not in ci_text:
        _fail(args.quiet, f"warm-release-cache-main.yml missing '{SHARED_KEY_PREFIX}'")
        errors += 1
    if SHARED_KEY_PREFIX not in rel_text:
        _fail(args.quiet, f"release.yml missing '{SHARED_KEY_PREFIX}'")
        errors += 1

    warm_actions = _actions_for_marker(ci_text, "go-release-v1")
    release_actions = _actions_for_marker(rel_text, "go-release-v1")
    if SAVE_ACTION not in warm_actions or RESTORE_ACTION not in warm_actions:
        _fail(
            args.quiet,
            f"warm workflow must restore then save release cache; found {warm_actions}",
        )
        errors += 1
    if release_actions != [RESTORE_ACTION] and set(release_actions) != {RESTORE_ACTION}:
        _fail(
            args.quiet,
            f"release.yml must restore-only the release cache; found {release_actions}",
        )
        errors += 1
    if "github.run_id" in ci_text or "github.run_id" in rel_text:
        _fail(args.quiet, "release cache keys must not include github.run_id")
        errors += 1

    if errors:
        return 1
    if not args.quiet:
        print("  ok: release/warm cache key prefix + directionality in sync")
    return 0


if __name__ == "__main__":
    sys.exit(main())
