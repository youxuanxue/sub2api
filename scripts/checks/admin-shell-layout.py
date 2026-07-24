#!/usr/bin/env python3
"""Guard the admin persistent-shell layout invariant (PR #935 / router/admin.tk.ts).

PR #935 hoisted <AppLayout> into a single persistent shell (AdminShellView.vue) and
removed the per-view <AppLayout> wrapper from every admin page, nesting them under
`/admin -> AdminShellView` children (the nested tree now lives in
frontend/src/router/admin.tk.ts). Two failure modes can silently regress this and
are exactly what an upstream merge tends to reintroduce:

  1. A new (or merged-in) admin view ships wrapped in its own <AppLayout>, so it
     renders a doubled layout / breaks the persistent shell.
  2. AdminShellView stops rendering AppLayout, so the whole admin area loses chrome.

This check fails on either, turning the recurring "strip <AppLayout> from new admin
views" merge chore into a mechanical gate instead of a thing to remember.

Scope: frontend/src/views/admin/**/*.vue. AdminShellView.vue is the ONE allowed
owner of AppLayout for admin pages. User console pages use UserShellView instead
(guarded by scripts/checks/user-shell-layout.py).
"""

from __future__ import annotations

import argparse
import re
import sys
import tempfile
from pathlib import Path

SHELL_REL = "frontend/src/views/admin/AdminShellView.vue"
ADMIN_VIEWS_REL = "frontend/src/views/admin"

# Match only a RENDERED opening tag <AppLayout> / <AppLayout ...> / <AppLayout/>.
# That is the exact regression we guard (an admin view re-rendering its own
# layout). Deliberately NOT matching imports or bare "AppLayout.vue" text: those
# appear in comments (e.g. EdgeHandoffView's "...no AppLayout. Consumes...") and
# would false-fail CI without indicating a real double-layout. `</AppLayout>` is
# not matched because `<` is not immediately followed by `AppLayout` there.
APPLAYOUT_RE = re.compile(r"<AppLayout(?=[\s/>])")


def scan(repo_root: Path) -> list[str]:
    errors: list[str] = []
    shell = repo_root / SHELL_REL
    if not shell.is_file():
        return [f"{SHELL_REL}: persistent admin shell is missing (PR #935 invariant)"]

    shell_text = shell.read_text(encoding="utf-8", errors="replace")
    if "AppLayout" not in shell_text:
        errors.append(
            f"{SHELL_REL}: admin shell no longer references AppLayout — the persistent "
            f"shell must own the admin layout"
        )

    admin_views = repo_root / ADMIN_VIEWS_REL
    for vue in sorted(admin_views.rglob("*.vue")):
        if vue.resolve() == shell.resolve():
            continue
        text = vue.read_text(encoding="utf-8", errors="replace")
        if APPLAYOUT_RE.search(text):
            rel = vue.relative_to(repo_root)
            errors.append(
                f"{rel}: admin views must NOT wrap <AppLayout> (layout comes from "
                f"AdminShellView). Strip the wrapper and register the route under the "
                f"AdminShellView children in frontend/src/router/admin.tk.ts"
            )
    return errors


def _selftest() -> int:
    failures = []
    with tempfile.TemporaryDirectory() as d:
        root = Path(d)
        (root / ADMIN_VIEWS_REL).mkdir(parents=True)
        shell = root / SHELL_REL
        # good baseline: shell owns AppLayout, one clean view
        shell.write_text("<template>\n  <component :is=\"AppLayout\" />\n</template>\n")
        (root / ADMIN_VIEWS_REL / "GoodView.vue").write_text("<template>\n  <div/>\n</template>\n")
        # a view that only MENTIONS AppLayout in a comment / closing tag must NOT trip
        # the guard (regression must be a rendered opening tag), mirroring EdgeHandoffView.
        (root / ADMIN_VIEWS_REL / "CommentView.vue").write_text(
            "<template>\n  <!-- chrome-less: no AppLayout, see AppLayout.vue -->\n  <div/>\n</template>\n"
        )
        if scan(root):
            failures.append("baseline (shell owns layout, clean view, comment-only mention) should pass")

        # bad: a view reintroduces <AppLayout>
        bad = root / ADMIN_VIEWS_REL / "BadView.vue"
        bad.write_text("<template>\n  <AppLayout>\n    <div/>\n  </AppLayout>\n</template>\n")
        if not scan(root):
            failures.append("view wrapping <AppLayout> should fail")
        bad.unlink()

        # bad: shell loses AppLayout
        shell.write_text("<template>\n  <div/>\n</template>\n")
        if not scan(root):
            failures.append("shell without AppLayout should fail")

    for f in failures:
        print(f"SELFTEST FAIL: {f}", file=sys.stderr)
    if failures:
        return 1
    print("ok: admin-shell-layout selftest (3/3 cases passed)")
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
    print("ok: admin views rely on the AdminShellView persistent shell (no per-view <AppLayout>)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
