#!/usr/bin/env python3
"""Prove the app-container resolver has exactly one owner.

Active-color and candidate-loop resolution has one owner:
ops/lib/resolve-app-container.sh (shell) and ops/lib/resolve_app_container.py
(python heredocs / inline SSM). A local copy that only checks whether a
container exists can select a STOPPED container during blue/green and report
stale env as live runtime. This check fails any file that re-derives those
rules instead of calling the owner.

Exit 0 = single owner intact. Exit 1 = a copy reappeared.
"""

from __future__ import annotations

import pathlib
import re
import sys


REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]

SHELL_OWNER = "ops/lib/resolve-app-container.sh"
PY_OWNER = "ops/lib/resolve_app_container.py"

# The two canonical owners are the only files allowed to spell the rules out.
OWNERS = {SHELL_OWNER, PY_OWNER}

SEARCH_DIRS = ("ops", ".github/workflows", "scripts", "deploy")

# The candidate list is the fingerprint of a re-derived resolver: any site that
# enumerates all three container names is choosing among them itself.
CANDIDATE_LOOP = re.compile(
    r"tokenkey[\"'\s,)\]]+.{0,40}tokenkey-blue.{0,40}tokenkey-green",
    re.DOTALL,
)

# Reading active-color and mapping blue|green to a container name is the other half.
ACTIVE_COLOR_MAP = re.compile(r"blue\s*\|\s*green|\(\s*[\"']blue[\"']\s*,\s*[\"']green[\"']\s*\)")

# Existence / first-match selection between the Stage0 name and the Tencent
# layout-exception name. `docker ps` preference and `$CANDIDATES` loops are
# different shapes and are not this fingerprint.
_DATA_CONTAINER_COPIES = (
    re.compile(r"for\s+[A-Za-z_][A-Za-z0-9_]*\s+in\s+tokenkey-postgres\s+tk-archive-standby\b"),
    re.compile(r"for\s+[A-Za-z_][A-Za-z0-9_]*\s+in\s+tokenkey-redis\s+tk-cutover-prod-redis\b"),
    re.compile(
        r"\btokenkey-postgres\b[^;\n]{0,80};\s*(?:sudo\s+)?docker\s+inspect\b"
        r"[^;\n]*\|\|\s*[A-Za-z_][A-Za-z0-9_]*=tk-archive-standby\b"
    ),
)

SKIP_SUFFIXES = (".pyc", ".png", ".jpg", ".gz", ".zst", ".lock")
SKIP_PARTS = ("__pycache__", "node_modules", ".git")


def candidate_files() -> list[pathlib.Path]:
    found: list[pathlib.Path] = []
    for directory in SEARCH_DIRS:
        base = REPO_ROOT / directory
        if not base.is_dir():
            continue
        for path in base.rglob("*"):
            if not path.is_file():
                continue
            if path.suffix in SKIP_SUFFIXES:
                continue
            if any(part in SKIP_PARTS for part in path.parts):
                continue
            found.append(path)
    return found


def relative(path: pathlib.Path) -> str:
    return path.relative_to(REPO_ROOT).as_posix()


def data_container_selection_copy(text: str) -> str | None:
    """Return a reason when text re-derives postgres/redis selection."""
    for pattern in _DATA_CONTAINER_COPIES:
        if pattern.search(text):
            return "re-derives postgres/redis selection by existence or first match"
    return None


def _assert_data_container_detector() -> str | None:
    """Prove the fingerprint matches the bug and misses the neighboring shapes."""
    pg = "tokenkey-postgres"
    standby = "tk-archive-standby"
    redis = "tokenkey-redis"
    cutover = "tk-cutover-prod-redis"
    bad = (
        f"for c in {pg} {standby}; do sudo docker inspect \"$c\" && break; done",
        f"for c in {redis} {cutover}; do sudo docker inspect \"$c\" && break; done",
        f"PG={pg}; sudo docker inspect \"$PG\" >/dev/null 2>&1 || PG={standby}",
    )
    ok = (
        "for candidate in $TK_POSTGRES_CONTAINER_CANDIDATES; do",
        (
            f"if docker ps --format '{{{{.Names}}}}' | grep -qx {pg}; then POSTGRES_CONTAINER={pg}; "
            f"elif docker ps --format '{{{{.Names}}}}' | grep -qx {standby}; then POSTGRES_CONTAINER={standby}; fi"
        ),
    )
    for sample in bad:
        if data_container_selection_copy(sample) is None:
            return f"data-container detector missed a selection copy: {sample}"
    for sample in ok:
        if data_container_selection_copy(sample) is not None:
            return f"data-container detector flagged a non-copy: {sample}"
    return None


def main() -> int:
    detector_error = _assert_data_container_detector()
    if detector_error:
        print(f"  FAIL: {detector_error}")
        return 1

    for owner in OWNERS:
        if not (REPO_ROOT / owner).is_file():
            print(f"  FAIL: canonical resolver missing: {owner}")
            print("        - the single owner must exist before consumers can share it")
            return 1

    violations: list[tuple[str, str]] = []

    for path in candidate_files():
        rel = relative(path)
        if rel in OWNERS:
            continue
        try:
            text = path.read_text(encoding="utf-8")
        except (OSError, UnicodeDecodeError):
            continue

        # A test may legitimately name the containers while faking docker; what it
        # must not do is re-implement the selection. Require BOTH fingerprints.
        has_loop = CANDIDATE_LOOP.search(text) is not None
        has_color_map = ACTIVE_COLOR_MAP.search(text) is not None
        if has_loop and has_color_map:
            # Consuming the owner is the whole point; a consumer that also mentions the
            # names (docstrings, fake docker stubs) is fine.
            uses_owner = (
                "resolve-app-container.sh" in text
                or "resolve_app_container" in text
                or "tk_resolve_app_container" in text
                or "remote_shell_snippet" in text
            )
            if not uses_owner:
                violations.append((rel, "re-derives active-color + candidate selection"))

        copy = data_container_selection_copy(text)
        if copy:
            violations.append((rel, copy))

    if violations:
        print("  FAIL: app-container resolution logic duplicated outside its owner")
        for rel, why in sorted(violations):
            print(f"        - {rel}: {why}")
        print(f"        fix: consume {SHELL_OWNER} (shell) or {PY_OWNER} (python / inline SSM)")
        print("        why: the copies drifted into existence-only checks that could")
        print("             select a STOPPED container and report it as live runtime")
        return 1

    print("  ok: single app-container resolver owner (no re-derived copies)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
