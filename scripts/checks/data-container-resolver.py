#!/usr/bin/env python3
"""Fail when a host-side script `docker exec`s a hardcoded PG/Redis container name.

Stage0 names the data containers tokenkey-postgres / tokenkey-redis; the Tencent
USW prod layout keeps tk-archive-standby / tk-cutover-prod-redis and only adds
network aliases, which never work for a host-side `docker exec` / `docker inspect`.
After the 2026-10-22 cutover every hardcoded name silently targeted a container
that does not exist. The owner is ops/lib/resolve-app-container.sh
(tk_resolve_postgres_container / tk_resolve_redis_container) and
ops/lib/resolve_app_container.py (resolve_postgres / resolve_redis /
data_container_shell_snippet).

Exit 0 = no hardcoded host-side reference outside the allowlist. Exit 1 otherwise.
"""

from __future__ import annotations

import pathlib
import re
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
SEARCH_DIRS = ("ops", "scripts", ".github/workflows")

# A docker exec/inspect/logs/stats call followed (within a short window, possibly
# across a line continuation) by a literal data-container name as an argument.
HARDCODED = re.compile(
    r"""docker["']?,?\s+["']?(?:exec|inspect|logs|stats)\b[^\n]{0,160}?(?:\\\n[^\n]{0,160}?){0,3}"""
    r"""(?<![\w./-])["']?tokenkey-(?:postgres|redis)["']?(?=[\s,\])"'\\]|$)""",
    re.MULTILINE,
)
# Python argv lists: "docker", "exec", ..., "tokenkey-postgres"
HARDCODED_ARGV = re.compile(
    r"""["']docker["']\s*,\s*["'](?:exec|inspect|stats)["'][^\]]{0,240}?["']tokenkey-(?:postgres|redis)["']""",
    re.DOTALL,
)

# Scope boundaries: other hosts / other teams / single-purpose retired-host tools.
ALLOWED_PREFIXES = (
    "ops/lib/",  # the owners spell out the candidate lists
    "deploy/domestic/",  # cn-prod: independent UCloud layout
)
ALLOWED_FILES = {
    # cn-prod-only sampler (tpm_burst_bundle.sh always targets cn-prod).
    "ops/observability/tpm_burst_monitor_remote.sh",
    # Hostname-pinned to retired AWS fresh-candidate / Tokyo hosts (Stage0 names).
    "ops/migration/check-fresh-host-network.sh",
    "ops/migration/hold-fresh-candidate.sh",
    "ops/migration/fresh_host_delta.py",
    "ops/migration/repair-tokyo-usage-20261017.sh",
    # Owned by the QA phase2 health workstream (separate change set); remove
    # from this list once it consumes the shared resolver.
    "ops/observability/probe-qa-phase2-live-health.sh",
    # This checker names the literals it forbids.
    "scripts/checks/data-container-resolver.py",
}
SKIP_PARTS = ("__pycache__", "node_modules", ".git")
SKIP_NAME = re.compile(r"(^test_|_test\.sh$|probe-cn-prod|postgres_backup|physical|qa_phase2|phase2_live_health)")


def candidate_files() -> list[pathlib.Path]:
    found: list[pathlib.Path] = []
    for directory in SEARCH_DIRS:
        base = REPO_ROOT / directory
        if not base.is_dir():
            continue
        for path in base.rglob("*"):
            if not path.is_file() or path.suffix not in {".sh", ".py", ".yml", ".yaml", ""}:
                continue
            if any(part in SKIP_PARTS for part in path.parts) or SKIP_NAME.search(path.name):
                continue
            found.append(path)
    return found


def violations(paths: list[pathlib.Path]) -> list[tuple[str, int]]:
    out: list[tuple[str, int]] = []
    for path in paths:
        rel = path.relative_to(REPO_ROOT).as_posix()
        if rel in ALLOWED_FILES or rel.startswith(ALLOWED_PREFIXES):
            continue
        try:
            text = path.read_text(encoding="utf-8")
        except (OSError, UnicodeDecodeError):
            continue
        for pattern in (HARDCODED, HARDCODED_ARGV):
            for match in pattern.finditer(text):
                out.append((rel, text.count("\n", 0, match.start()) + 1))
    return sorted(set(out))


def main() -> int:
    found = violations(candidate_files())
    if found:
        print("  FAIL: host-side docker call hardcodes a PG/Redis container name")
        for rel, line in found:
            print(f"        - {rel}:{line}")
        print("        fix: tk_require_postgres_container / tk_require_redis_container")
        print("             (ops/lib/resolve-app-container.sh) or resolve_postgres /")
        print("             data_container_shell_snippet (ops/lib/resolve_app_container.py)")
        print("        why: the Tencent USW layout keeps tk-archive-standby /")
        print("             tk-cutover-prod-redis; network aliases do not work for docker exec")
        return 1
    print("  ok: no hardcoded host-side PG/Redis container names")
    return 0


if __name__ == "__main__":
    sys.exit(main())
