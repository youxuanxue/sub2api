#!/usr/bin/env python3
"""Guard the user persistent-shell layout invariant (UserShellView / router/user.tk.ts).

UserShellView hoists <AppLayout> into a single persistent shell and nests console
pages as children (router/user.tk.ts), mirroring AdminShellView (PR #935). Failure
modes this catches:

  1. A user view ships wrapped in its own <AppLayout> → doubled layout / sidebar remount.
  2. UserShellView stops rendering AppLayout for authenticated chrome.

Scope: frontend/src/views/user/**/*.vue except UserShellView.vue.
"""

from __future__ import annotations

import argparse
import re
import sys
import tempfile
from pathlib import Path

SHELL_REL = "frontend/src/views/user/UserShellView.vue"
USER_VIEWS_REL = "frontend/src/views/user"

APPLAYOUT_RE = re.compile(r"<AppLayout(?=[\s/>])")


def scan(repo_root: Path) -> list[str]:
    errors: list[str] = []
    shell = repo_root / SHELL_REL
    if not shell.is_file():
        return [f"{SHELL_REL}: persistent user shell is missing"]

    shell_text = shell.read_text(encoding="utf-8", errors="replace")
    if "AppLayout" not in shell_text:
        errors.append(
            f"{SHELL_REL}: user shell no longer references AppLayout — "
            f"the persistent shell must own authenticated layout chrome"
        )

    user_views = repo_root / USER_VIEWS_REL
    for vue in sorted(user_views.rglob("*.vue")):
        if vue.resolve() == shell.resolve():
            continue
        text = vue.read_text(encoding="utf-8", errors="replace")
        if APPLAYOUT_RE.search(text):
            rel = vue.relative_to(repo_root)
            errors.append(
                f"{rel}: user views must NOT wrap <AppLayout> (layout comes from "
                f"UserShellView). Strip the wrapper and register the route under "
                f"frontend/src/router/user.tk.ts"
            )
    return errors


def _selftest() -> int:
    failures = []
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        (root / USER_VIEWS_REL).mkdir(parents=True)
        shell = root / SHELL_REL
        shell.write_text("<template>\n  <AppLayout />\n</template>\n")
        (root / USER_VIEWS_REL / "GoodView.vue").write_text("<template>\n  <div/>\n</template>\n")
        if scan(root):
            failures.append("baseline should pass")

        bad = root / USER_VIEWS_REL / "BadView.vue"
        bad.write_text("<template>\n  <AppLayout>\n    <div/>\n  </AppLayout>\n</template>\n")
        if not scan(root):
            failures.append("view wrapping AppLayout should fail")
        bad.unlink()

        shell.write_text("<template>\n  <div/>\n</template>\n")
        if not scan(root):
            failures.append("shell without AppLayout should fail")

    for f in failures:
        print(f"SELFTEST FAIL: {f}", file=sys.stderr)
    if failures:
        return 1
    print("ok: user-shell-layout selftest (3/3 cases passed)")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--repo-root",
        type=Path,
        default=Path(__file__).resolve().parents[2],
        help="repository root (defaults to two levels up from this script)",
    )
    parser.add_argument("--selftest", action="store_true", help="run built-in fixtures and exit")
    args = parser.parse_args()

    if args.selftest:
        return _selftest()

    errors = scan(args.repo_root)
    if errors:
        for err in errors:
            print(f"FAIL: {err}", file=sys.stderr)
        return 1
    print("ok: user views rely on the UserShellView persistent shell (no per-view <AppLayout>)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
