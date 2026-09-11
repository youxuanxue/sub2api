#!/usr/bin/env python3
"""Fail when a workflow step calls the `gh` CLI without a resolvable repository.

`gh` infers its target repository from git remotes. A job that never runs
actions/checkout has no git repository, so every `gh` subcommand that needs a
repo dies with:

    failed to run git: fatal: not a git repository (or any of the parent
    directories): .git

That is not a transient failure and it is invisible until the branch of the
workflow that uses it actually fires. `ops-daily-diagnostics.yml`'s
`queue-repair-draft` job hit exactly this: it only runs on days that produce a
repair candidate, so `ops-repair-draft.yml` was never once dispatched while the
job reported a red run.

A step is considered safe when its job checks out the repository, or when
`GH_REPO` is set at workflow, job, or step level.
"""

from __future__ import annotations

import argparse
import re
import sys
import tempfile
from pathlib import Path

try:
    import yaml
except ImportError:  # pragma: no cover - CI installs PyYAML
    print("gh-cli-repo-context: PyYAML is required", file=sys.stderr)
    raise SystemExit(2)


# Subcommands that resolve a repository. `gh auth`, `gh version` and friends do
# not, so they must not be flagged.
REPO_SCOPED = re.compile(
    r"(?<![\w./-])gh\s+(?:api|browse|cache|issue|label|pr|release|run|workflow)(?![\w-])"
)

WORKFLOW_DIR = Path(".github/workflows")


def _steps_check_out(steps: list) -> bool:
    for step in steps:
        if not isinstance(step, dict):
            continue
        if "checkout" in str(step.get("uses") or ""):
            return True
    return False


def _env_names(*scopes: object) -> set[str]:
    names: set[str] = set()
    for scope in scopes:
        if isinstance(scope, dict):
            names.update(scope.keys())
    return names


def audit(path: Path) -> list[str]:
    try:
        document = yaml.safe_load(path.read_text(encoding="utf-8"))
    except yaml.YAMLError as exc:
        return [f"{path}: unparseable YAML: {exc}"]
    if not isinstance(document, dict):
        return []

    workflow_env = document.get("env")
    findings: list[str] = []
    for job_name, job in (document.get("jobs") or {}).items():
        if not isinstance(job, dict):
            continue
        steps = job.get("steps")
        if not isinstance(steps, list):
            continue
        if _steps_check_out(steps):
            continue
        job_env = job.get("env")
        for step in steps:
            if not isinstance(step, dict):
                continue
            run = step.get("run")
            if not isinstance(run, str) or not REPO_SCOPED.search(run):
                continue
            if "GH_REPO" in _env_names(workflow_env, job_env, step.get("env")):
                continue
            label = step.get("name") or "(unnamed step)"
            findings.append(
                f"{path}: job '{job_name}', step '{label}' calls the gh CLI with "
                "neither actions/checkout nor GH_REPO; gh cannot resolve the "
                "repository and the step will fail at runtime"
            )
    return findings


_SELFTEST_CASES: tuple[tuple[str, str, int], ...] = (
    (
        "no checkout, no GH_REPO",
        """
jobs:
  dispatch:
    steps:
      - name: fire
        run: gh workflow run other.yml
""",
        1,
    ),
    (
        "GH_REPO at step level",
        """
jobs:
  dispatch:
    steps:
      - name: fire
        env:
          GH_REPO: owner/repo
        run: gh workflow run other.yml
""",
        0,
    ),
    (
        "GH_REPO at job level",
        """
jobs:
  dispatch:
    env:
      GH_REPO: owner/repo
    steps:
      - name: fire
        run: gh workflow run other.yml
""",
        0,
    ),
    (
        "GH_REPO at workflow level",
        """
env:
  GH_REPO: owner/repo
jobs:
  dispatch:
    steps:
      - name: fire
        run: gh workflow run other.yml
""",
        0,
    ),
    (
        "job checks out the repository",
        """
jobs:
  dispatch:
    steps:
      - uses: actions/checkout@v6
      - name: fire
        run: gh workflow run other.yml
""",
        0,
    ),
    (
        "repo-independent gh subcommand",
        """
jobs:
  dispatch:
    steps:
      - name: whoami
        run: gh auth status
""",
        0,
    ),
    (
        "gh substring inside another word",
        """
jobs:
  dispatch:
    steps:
      - name: unrelated
        run: ./tools/highlight run --workflow x
""",
        0,
    ),
)


def _selftest() -> int:
    failures = 0
    with tempfile.TemporaryDirectory() as tmp:
        for label, body, expected in _SELFTEST_CASES:
            path = Path(tmp) / "wf.yml"
            path.write_text(body, encoding="utf-8")
            found = len(audit(path))
            if (1 if found else 0) != expected:
                print(
                    f"gh-cli-repo-context selftest FAIL: {label} "
                    f"(expected {'a finding' if expected else 'no finding'}, got {found})",
                    file=sys.stderr,
                )
                failures += 1
    if failures:
        return 1
    print(f"gh-cli-repo-context: selftest OK ({len(_SELFTEST_CASES)} cases)")
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--quiet", action="store_true", help="print nothing on success")
    parser.add_argument("--selftest", action="store_true", help="run built-in cases and exit")
    args = parser.parse_args(argv)

    if args.selftest:
        return _selftest()

    if not WORKFLOW_DIR.is_dir():
        print(f"gh-cli-repo-context: {WORKFLOW_DIR} not found", file=sys.stderr)
        return 2

    findings: list[str] = []
    for path in sorted(WORKFLOW_DIR.glob("*.yml")) + sorted(WORKFLOW_DIR.glob("*.yaml")):
        findings.extend(audit(path))

    if findings:
        print("gh CLI steps without a resolvable repository:", file=sys.stderr)
        for finding in findings:
            print(f"  - {finding}", file=sys.stderr)
        print(
            "\nFix: add `GH_REPO: ${{ github.repository }}` to the step env, or "
            "check the repository out in that job.",
            file=sys.stderr,
        )
        return 1

    if not args.quiet:
        print("gh-cli-repo-context: OK")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
