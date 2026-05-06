#!/usr/bin/env python3
"""Verify .github/workflows/*.yml does not reference env.* in job-level if.

GitHub Actions does NOT allow the env context in job-level if expressions —
env is unavailable when GitHub evaluates job-level if (it runs before the
job is set up). Such references cause the ENTIRE workflow to fail to parse
with HTTP 422 "Unrecognized named-value: 'env'", silently breaking every
tag-push / workflow_dispatch trigger that uses that workflow.

History: PR #120 introduced this pattern in release.yml's queue-prod-deploy
job. The defect was invisible until v1.7.17 tag-push (2026-05-06), when it
blocked the prod release pipeline entirely. PR #122 fixed it; this preflight
check stops the pattern from recurring.

Step-level if expressions DO support env; only job-level if (jobs.<name>.if)
is flagged.

Usage: python3 scripts/check-workflow-job-if-env.py [--quiet]
Exit 0 ok, 1 violation, 2 missing dep / unparseable.
"""

from __future__ import annotations

import re
import sys
import pathlib

try:
    import yaml  # PyYAML
except ImportError:
    print(
        "  err: PyYAML not installed (required to parse .github/workflows/*.yml).\n"
        "       fix: python3 -m pip install --user pyyaml",
        flush=True,
    )
    sys.exit(2)

REPO_ROOT = pathlib.Path(__file__).resolve().parent.parent
WORKFLOW_DIR = REPO_ROOT / ".github" / "workflows"
ENV_REF = re.compile(r"\benv\.")


def find_violations(yml_path: pathlib.Path) -> list[tuple[pathlib.Path, str, str]]:
    try:
        doc = yaml.safe_load(yml_path.read_text())
    except yaml.YAMLError as exc:
        return [(yml_path, "<parse-error>", f"YAML parse error: {exc}")]
    if not isinstance(doc, dict):
        return []
    jobs = doc.get("jobs")
    if not isinstance(jobs, dict):
        return []
    out: list[tuple[pathlib.Path, str, str]] = []
    for job_name, job_def in jobs.items():
        if not isinstance(job_def, dict):
            continue
        # NB: jobs.<name>.if is the ONE forbidden surface. Do NOT recurse into
        # job_def["steps"] — step-level if can use env legitimately.
        if_expr = job_def.get("if")
        if isinstance(if_expr, str) and ENV_REF.search(if_expr):
            out.append((yml_path, str(job_name), if_expr.strip()))
    return out


def main(argv: list[str]) -> int:
    quiet = "--quiet" in argv
    if not WORKFLOW_DIR.is_dir():
        if not quiet:
            print(f"  skip: no {WORKFLOW_DIR}", flush=True)
        return 0
    violations: list[tuple[pathlib.Path, str, str]] = []
    for path in sorted(list(WORKFLOW_DIR.glob("*.yml")) + list(WORKFLOW_DIR.glob("*.yaml"))):
        violations.extend(find_violations(path))
    if violations:
        for path, job, expr in violations:
            rel = path.relative_to(REPO_ROOT)
            print(f"  err: {rel}: jobs.{job}.if references env.* (forbidden at job level)", flush=True)
            print(f"       expr: {expr}", flush=True)
        print("", flush=True)
        print(
            "  GitHub Actions rejects env in job-level if (HTTP 422 "
            "\"Unrecognized named-value: 'env'\").",
            flush=True,
        )
        print(
            "  Use vars.* / github.event.inputs.* / needs.<job>.outputs.* instead. "
            "See PR #122 for the canonical fix.",
            flush=True,
        )
        return 1
    if not quiet:
        print("  ok: no env references in job-level if expressions", flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
