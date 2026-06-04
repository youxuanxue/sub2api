#!/usr/bin/env python3
"""Gate B (static half): ops SQL generator coverage.

Every SQL-generating symbol in an ops module must be reachable from a
real-Postgres execution test, or no test ever proves the generated SQL parses.
PR #563 shipped two generators whose SQL Postgres rejected outright, green the
whole way because the tests only mocked the DB and asserted substrings.

This check is the *static* forcing function (no DB needed, runs in preflight):
for every ops module that contains SQL-generator-shaped symbols, it requires the
module to define ``iter_self_check_sql()`` + ``SELF_CHECK_EXEMPT`` and asserts
that EVERY shaped symbol is either enumerated (so the execution test in
``ops/anthropic/test_ops_sql_execute.py`` runs it against a real Postgres) or
explicitly exempted with a reason. A new generator therefore cannot ship
without being wired into the execution gate.

"Shaped" symbol (the heuristic for "this looks like a SQL builder", keyed off the
repo's naming convention that SQL builders end ``_sql`` / ``_query`` / ``_SQL``):
  * a module-level constant whose name ends with ``_SQL``; or
  * a module-level ``def`` (not underscore-prefixed) whose name ends with
    ``_sql`` or ``_query``.
Runners/helpers that match the shape but don't *build* SQL (e.g. ``ssm_run_sql``,
``run_remote_query``) go in SELF_CHECK_EXEMPT — the point is forced
classification, not zero false positives. ``iter_self_check_sql`` (the registry
accessor itself) is ignored.

Discovery is automatic: any non-test ``ops/**/*.py`` with shaped symbols is
pulled in, so a brand-new SQL module is covered the day it lands.

Exit codes: 0 ok · 1 a module is uncovered/misconfigured · 2 import/parse error.
stdlib-only.
"""
from __future__ import annotations

import ast
import importlib.util
import pathlib
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
OPS_DIR = REPO_ROOT / "ops"

_FUNC_SUFFIXES = ("_sql", "_query")
_IGNORE = {"iter_self_check_sql"}  # the registry accessor, not a generator


def _shaped_symbols(tree: ast.Module) -> set[str]:
    """Names of module-level SQL-generator-shaped symbols."""
    out: set[str] = set()
    for node in tree.body:
        if isinstance(node, (ast.Assign, ast.AnnAssign)):
            targets = node.targets if isinstance(node, ast.Assign) else [node.target]
            for t in targets:
                if isinstance(t, ast.Name) and t.id.endswith("_SQL"):
                    out.add(t.id)
        elif isinstance(node, ast.FunctionDef):
            name = node.name
            if name.startswith("_") or name in _IGNORE:
                continue
            if name.endswith(_FUNC_SUFFIXES):
                out.add(name)
    return out


def _import_module(path: pathlib.Path):
    # ops modules import siblings by bare name (e.g. edge_routing_matrix);
    # make every ops subpackage dir importable, like the runtime callers do.
    for sub in sorted(p for p in OPS_DIR.iterdir() if p.is_dir()):
        if str(sub) not in sys.path:
            sys.path.insert(0, str(sub))
    mod_name = "opssql_" + path.stem.replace("-", "_")
    spec = importlib.util.spec_from_file_location(mod_name, path)
    if not spec or not spec.loader:
        raise ImportError(f"cannot load spec for {path}")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def main() -> int:
    failures: list[str] = []
    checked = 0

    for path in sorted(OPS_DIR.rglob("*.py")):
        if path.name.startswith("test_") or path.name == "__init__.py":
            continue
        try:
            tree = ast.parse(path.read_text(encoding="utf-8"))
        except SyntaxError as exc:
            print(f"::error::cannot parse {path}: {exc}", file=sys.stderr)
            return 2

        shaped = _shaped_symbols(tree)
        if not shaped:
            continue  # not a SQL-generating module
        rel = path.relative_to(REPO_ROOT)
        checked += 1

        try:
            mod = _import_module(path)
        except Exception as exc:  # noqa: BLE001 — surface any import failure
            print(f"::error::importing {rel} failed: {type(exc).__name__}: {exc}", file=sys.stderr)
            return 2

        if not hasattr(mod, "iter_self_check_sql") or not hasattr(mod, "SELF_CHECK_EXEMPT"):
            failures.append(
                f"{rel}: defines SQL generators {sorted(shaped)} but no "
                f"iter_self_check_sql() + SELF_CHECK_EXEMPT registry"
            )
            continue

        try:
            enumerated = {label for label, _ in mod.iter_self_check_sql()}
        except Exception as exc:  # noqa: BLE001
            failures.append(f"{rel}: iter_self_check_sql() raised {type(exc).__name__}: {exc}")
            continue
        exempt = dict(mod.SELF_CHECK_EXEMPT)
        covered = enumerated | set(exempt)

        missing = sorted(shaped - covered)
        if missing:
            failures.append(
                f"{rel}: SQL generator(s) not covered: {missing}. "
                f"Add each to iter_self_check_sql() (so the real-Postgres execution "
                f"test runs it) or to SELF_CHECK_EXEMPT with a reason."
            )
        stale = sorted(k for k in exempt if k not in shaped)
        if stale:
            failures.append(
                f"{rel}: SELF_CHECK_EXEMPT has stale/typo key(s) {stale} that are "
                f"not SQL-generator-shaped symbols in this module"
            )

    if failures:
        print("FAIL: ops SQL generator coverage", file=sys.stderr)
        for f in failures:
            print(f"  - {f}", file=sys.stderr)
        return 1

    print(f"ok: every SQL generator across {checked} ops module(s) is enumerated or exempted")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
