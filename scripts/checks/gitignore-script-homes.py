#!/usr/bin/env python3
"""gitignore-script-homes — tracked homes stay committable, secrets stay ignored.

Upstream's `.gitignore` carries a bare `scripts` rule. A pattern without a slash
matches at ANY depth, so it hid `scripts/`, `skills/*/scripts/` and
`.cursor/skills/*/scripts/` wholesale. Every one of the ~230 tracked files in those
directories had to be force-added, which means a NEW preflight checker, sentinel or
skill script is silently dropped from commits — the failure is invisible at commit
time and only surfaces when CI cannot find the file. TK re-includes those homes.

This gate is the ratchet on that fix, and it cuts both ways:

  1. COMMITTABLE. A new file in a script home must not be ignored. If the
     re-includes are dropped (upstream merge, cleanup), new tooling goes back to
     being silently uncommittable.

  2. STILL GUARDED. Re-including the directories removed the blanket rule's
     incidental secret coverage, so local operator config and key material are now
     ignored by explicit patterns. This asserts those patterns hold, so the fix
     cannot trade a commit bug for a leaked credential.

  3. NO SHADOWED TRACKED FILES, REPO-WIDE. A tracked file matching an ignore rule is
     a trap: edits are committable but a fresh clone plus `git add` behaves
     differently. `scripts` was one instance of a pattern-less upstream rule matching
     at any depth; `docs/*`, `CLAUDE.md`, `AGENTS.md`, `.claude` and `*.env` shadowed
     104 more tracked files the same way, so this assertion scans every tracked path
     rather than only the script homes.

  4. TRACKED-HOME COMMITTABILITY. The homes in `TRACKED_HOMES` hold load-bearing
     content that a fresh clone must be able to extend — a new approved design doc, a
     new agent hook. Assert a plausible new file in each is committable, so a
     reintroduced blanket ignore cannot silently swallow new work.

The fix's shape changed once and the reason is worth keeping: the first version
narrowed each upstream rule and then re-included the tracked surface, which produced
`docs/*` plus fourteen negations to undo itself. Measurement showed those rules
ignored NOTHING on disk (`docs/` is 104/104 tracked; `scripts/`'s only untracked
files are `.pyc` already covered by `__pycache__/`), so for a directory we fully
track the ignore was pure complexity and is now simply absent. `.claude/*` keeps the
allowlist shape, because there most of the directory really is untracked local state.
Assertion 2's patterns are global for the same reason: scoping key material to
`scripts/**` implied that directory was special while leaving `ops/deploy.key`
committable.

Probe paths are hypothetical — nothing is created on disk; `git check-ignore`
answers from the rules alone.

Exit: 0 ok, 1 gate fail, 2 error.
"""
from __future__ import annotations

import argparse
import subprocess
import sys
from pathlib import Path

# Directories whose new files MUST be committable. Each entry is a real script home
# with tracked content today.
SCRIPT_HOMES = (
    "scripts",
    "scripts/checks",
    "scripts/sentinels",
    "scripts/ci",
    "backend/scripts",
    "skills/sub2api-admin/scripts",
    ".cursor/skills/tokenkey-kiro-reauth/scripts",
)

# A plausible new-tooling filename per home: what an agent would actually add.
COMMITTABLE_PROBES = ("new_check.py", "new-check.sh", "registry.json")

# Non-script homes that upstream ignores wholesale but TokenKey tracks. Upstream keeps
# almost no docs and no agent contract in git; TK's are load-bearing and consumed by
# preflight gates, so a new file in any of them must be committable. Probe filenames
# are per-home because the trap is extension-agnostic — the rules that caused it
# matched directories, not suffixes.
TRACKED_HOMES = {
    "docs/approved": ("new-design.md",),
    "docs/global": ("new-guide.md",),
    "docs/ops": ("new-changelog.md",),
    "docs/operator": ("new-runbook.md",),
    "docs/spec-delta": ("new-delta.md",),
    ".claude/hooks": ("new-hook.sh",),
    ".cursor/skills": ("new-skill/SKILL.md",),
}

# Root-level tracked files that pattern-less upstream rules used to match at any depth.
# Reintroducing any of those rules — most likely by taking upstream's side in a merge
# conflict — makes the repo's own agent contract and the embedded-dist manifest
# invisible again, so assert each stays committable.
ROOTED_TRACKED_FILES = (
    "CLAUDE.md",
    "AGENTS.md",
    ".cursor/cloud-agent.env",
    "backend/internal/web/dist/frontend-source.json",
)

# Paths that MUST stay ignored even after the re-includes above — the wall each
# narrowed rule re-establishes. A re-include written one level too broad (`!docs/`
# instead of `!docs/*/`, or `!/.claude/` without the `/.claude/*` reset) would let
# these through, which is exactly the leak this pairing prevents. None exists on disk.
NARROWED_RULE_PROBES = (
    "docs/_scratch/notes.md",  # script-ref-allow-missing
    # Agent-local state the `.claude/*` wall is the ONLY .gitignore coverage for. Do not
    # substitute a `tmp/` path here: the `tmp/` rule covers that independently, so such a
    # probe passes even with the wall deleted and asserts nothing.
    ".claude/mcp.local.json",  # script-ref-allow-missing
    ".claude/history.jsonl",  # script-ref-allow-missing
    ".claude/worktrees/wt/main.go",  # script-ref-allow-missing
    ".cursor/operator.env",  # script-ref-allow-missing
    "backend/internal/web/dist/index.html",  # script-ref-allow-missing
)

# Key material and local operator config, anywhere in the tree. These were once scoped
# to `scripts/**`, which left `ops/deploy.key` and `backend/tls.pem` committable; the
# patterns are global now and this asserts they stay that way. Extension-based on
# purpose — a `*secret*` substring glob would shadow ~48 real tracked sources.
GLOBAL_SECRET_PROBES = (
    "ops/deploy.key",  # script-ref-allow-missing
    "backend/tls.pem",  # script-ref-allow-missing
    "frontend/id_rsa",  # script-ref-allow-missing
    "ops/stage0/bundle.p12",  # script-ref-allow-missing
    "backend/config.local.json",  # script-ref-allow-missing
)

# Secret-shaped paths under the re-included homes that MUST stay ignored. These are
# hypothetical probes: none of them exists, and none may ever be committed — hence
# the absence markers for the stale-script-reference gate.
SECRET_PROBES = (
    "scripts/config.yaml",  # script-ref-allow-missing
    "scripts/config.yml",  # script-ref-allow-missing
    "scripts/checks/config.local.json",  # script-ref-allow-missing
    "scripts/tls.pem",  # script-ref-allow-missing
    "scripts/deploy.key",  # script-ref-allow-missing
    "scripts/id_rsa",  # script-ref-allow-missing
    "scripts/ci/id_rsa.pub",  # script-ref-allow-missing
    "scripts/.env",  # script-ref-allow-missing
    "scripts/local.env",  # script-ref-allow-missing
)


def check_ignored(root: Path, paths: list[str]) -> dict[str, bool]:
    """Ask git which of `paths` are ignored, without touching the filesystem."""
    if not paths:
        return {}
    proc = subprocess.run(
        ["git", "-C", str(root), "check-ignore", "--no-index", "--stdin"],
        input="\n".join(paths),
        text=True,
        capture_output=True,
    )
    # check-ignore exits 0 when something matched, 1 when nothing did; both are fine.
    if proc.returncode not in (0, 1):
        raise RuntimeError(proc.stderr.strip() or "git check-ignore failed")
    ignored = {line.strip() for line in proc.stdout.splitlines() if line.strip()}
    return {path: path in ignored for path in paths}


def tracked_files(root: Path, directories: tuple[str, ...] | None = None) -> list[str]:
    """Tracked paths, scoped to `directories` or repo-wide when omitted."""
    if directories is None:
        present: list[str] = []
    else:
        present = [d for d in directories if (root / d).is_dir()]
        if not present:
            return []
    proc = subprocess.run(
        ["git", "-C", str(root), "ls-files", *present],
        text=True,
        capture_output=True,
    )
    if proc.returncode != 0:
        raise RuntimeError(proc.stderr.strip() or "git ls-files failed")
    return [line.strip() for line in proc.stdout.splitlines() if line.strip()]


def check(root: Path) -> list[str]:
    errors: list[str] = []

    probes = [
        f"{home}/{name}"
        for home in SCRIPT_HOMES
        if (root / home).is_dir()
        for name in COMMITTABLE_PROBES
    ]
    for path, ignored in check_ignored(root, probes).items():
        if ignored:
            errors.append(
                f".gitignore: a new file at `{path}` would be IGNORED. Script homes "
                "must stay committable — restore the `!/scripts/`-style re-includes "
                "next to the bare `scripts` rule, or new tooling is silently dropped "
                "from commits."
            )

    home_probes = [
        f"{home}/{name}"
        for home, names in TRACKED_HOMES.items()
        if (root / home).is_dir()
        for name in names
    ]
    for path, ignored in check_ignored(root, home_probes).items():
        if ignored:
            errors.append(
                f".gitignore: a new file at `{path}` would be IGNORED. TokenKey tracks "
                "this directory even though upstream ignores it wholesale — keep the "
                "narrowing re-include, or new content there is silently dropped from "
                "commits."
            )

    for path, ignored in check_ignored(root, list(ROOTED_TRACKED_FILES)).items():
        if ignored and (root / path).exists():
            errors.append(
                f".gitignore: tracked root file `{path}` is IGNORED. A pattern-less "
                "upstream rule matches it at any depth; restore the rooted `!/` "
                "re-include rather than dropping the upstream line."
            )

    secrets = [*SECRET_PROBES, *NARROWED_RULE_PROBES, *GLOBAL_SECRET_PROBES]
    for path, ignored in check_ignored(root, secrets).items():
        if not ignored:
            errors.append(
                f".gitignore: `{path}` is NOT ignored. Narrowing an upstream rule must "
                "re-include only the tracked surface — this path is scratch, generated "
                "or credential-shaped and has to stay ignored, so the re-include above "
                "it is one level too broad."
            )

    shadowed = [
        path
        for path, ignored in check_ignored(root, tracked_files(root)).items()
        if ignored
    ]
    for path in sorted(shadowed):
        errors.append(
            f".gitignore: tracked file `{path}` is shadowed by an ignore rule. It stays "
            "editable but behaves differently in a fresh clone; narrow the rule or stop "
            "tracking the file."
        )
    return errors


def selftest() -> int:
    import tempfile

    failures: list[str] = []

    def expect(condition: bool, label: str) -> None:
        if not condition:
            failures.append(label)

    def make_repo(ignore_text: str) -> Path:
        root = Path(tempfile.mkdtemp())
        subprocess.run(["git", "-C", str(root), "init", "-q"], check=True)
        (root / ".gitignore").write_text(ignore_text, encoding="utf-8")
        for home in (*SCRIPT_HOMES, *TRACKED_HOMES):
            (root / home).mkdir(parents=True, exist_ok=True)
            (root / home / "keep.py").write_text("x\n", encoding="utf-8")
        for name in ROOTED_TRACKED_FILES:
            path = root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("x\n", encoding="utf-8")
        return root

    # Fixture .gitignore text, mirroring the real rules. The paths inside are fixture
    # content in a temp repo, never this repo's files.
    good = (
        "scripts/config.*\nconfig.local.*\n"  # script-ref-allow-missing
        "*.pem\n*.key\n*.p12\n*.pfx\nid_rsa*\nid_ed25519*\n"
        ".env\n*.env\n!.cursor/cloud-agent.env\n"
        "backend/internal/web/dist/*\n!backend/internal/web/dist/frontend-source.json\n"
        ".claude/*\n!/.claude/hooks/\n"
        "docs/_scratch/\n"
    )
    root = make_repo(good)
    expect(check(root) == [], f"healthy .gitignore passes (got {check(root)[:1]})")

    # Regression 1: the upstream blanket `scripts` ignore comes back (the likely shape
    # of a bad merge resolution) -> new tooling silently uncommittable.
    root = make_repo(good + "scripts\n")
    expect(any("would be IGNORED" in e for e in check(root)),
           "reintroduced blanket scripts ignore is reported")

    # Regression 2: a global key pattern dropped -> key material committable anywhere.
    root = make_repo(good.replace("*.key\n", ""))
    expect(any("ops/deploy.key" in e for e in check(root)),
           "dropped global key pattern is reported")

    # Regression 2b: patterns re-scoped to `scripts/**` -> the exact gap that shape had.
    root = make_repo(good.replace("*.pem\n", "scripts/**/*.pem\n"))
    expect(any("backend/tls.pem" in e for e in check(root)),
           "key patterns rescoped to scripts/ are reported")

    # Regression 3: upstream `docs/*` comes back -> a new approved doc goes invisible.
    root = make_repo(good + "docs/*\n")
    expect(any("docs/approved/new-design.md" in e for e in check(root)),
           "reintroduced docs/* ignore is reported")

    # Regression 4: upstream's pattern-less `AGENTS.md` comes back -> the repo's own
    # contract goes invisible again.
    root = make_repo(good + "AGENTS.md\n")
    expect(any("tracked root file `AGENTS.md`" in e for e in check(root)),
           "reintroduced AGENTS.md ignore is reported")

    # Regression 5: the docs scratch home stops being ignored -> local notes become
    # commit noise, which is what drove the original ignore-by-default shape.
    root = make_repo(good.replace("docs/_scratch/\n", ""))
    expect(any("docs/_scratch/notes.md" in e for e in check(root)),
           "missing docs scratch ignore is reported")

    # Regression 6: `.claude/*` wall removed -> agent-local state becomes visible.
    root = make_repo(good.replace(".claude/*\n", ""))
    expect(any(".claude/mcp.local.json" in e for e in check(root)),
           "missing .claude wall is reported")

    # Regression 7: a tracked file shadowed by a rule. Created inside the temp repo.
    root = make_repo(good)
    shadowed_fixture = "scripts/checks/local.key"  # script-ref-allow-missing
    (root / shadowed_fixture).write_text("x\n", encoding="utf-8")
    subprocess.run(
        ["git", "-C", str(root), "add", "-f", shadowed_fixture], check=True
    )
    expect(any("shadowed" in e for e in check(root)), "shadowed tracked file is reported")

    if failures:
        for failure in failures:
            print(f"FAIL selftest: {failure}", file=sys.stderr)
        return 1
    print("ok: gitignore-script-homes selftests pass")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[2])
    parser.add_argument("--quiet", action="store_true")
    parser.add_argument("--selftest", action="store_true")
    args = parser.parse_args()
    if args.selftest:
        return selftest()
    try:
        errors = check(args.root.resolve())
    except (OSError, RuntimeError, subprocess.CalledProcessError) as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 2
    if errors:
        for error in errors:
            print(f"FAIL: {error}", file=sys.stderr)
        return 1
    if not args.quiet:
        print("ok: script homes committable, script-home secrets ignored")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
