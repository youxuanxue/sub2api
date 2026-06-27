#!/usr/bin/env python3
"""display-coverage-gate.py — forward guard against the #1030 / #1029 failure class.

Both #1030 (gpt-5.6) and #1029 (antigravity gemini-* image/3.5-flash) added models
to the Go servable allowlist (pricing_catalog_supported_models_tk.go) and to the
BUNDLED litellm mirror (backend/resources/model-pricing/…), reasoning "it's priced
in the mirror, so it displays." It does NOT: prod's /pricing catalog is built from
the live UPSTREAM remote mirror (Wei-Shaw/model-price-repo) ∪ the TK overlay
(tk_pricing_overlay.json). The bundled mirror is a hand-maintained, SHADOWED
fallback prod never reads — so a model present only there shows a BLANK price.
The overlay is the only prod-reliable, repo-checkable display source.

This gate is DIFF-SCOPED: it only fires on models a PR ADDS to the allowlist
(base..HEAD), so it passes on a clean main and never retroactively fails. For each
newly-allowlisted model it requires display coverage in the overlay (non-zero,
token OR media). The escape hatch is a falsifiable assertion, not a rubber stamp:
a commit message carrying `display-via-remote-verified` declares the author
confirmed the model already displays priced via the upstream remote (the only
legitimate non-overlay source). The live backstop is
ops/pricing/audit-display-coverage.py check --live (catches a wrong assertion).

Exit: 0 clean / 1 gap (uncovered new allowlist entry, no marker) / 2 error.
"""
from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
GO_REL = "backend/internal/service/pricing_catalog_supported_models_tk.go"
OVERLAY_REL = "backend/internal/service/tk_pricing_overlay.json"
PLATFORMS = ("anthropic", "openai", "gemini", "antigravity", "grok")
MARKER = "display-via-remote-verified"


def parse_allowlist(go_text: str) -> dict[str, set[str]]:
    out: dict[str, set[str]] = {}
    for plat in PLATFORMS:
        m = re.search(
            r"servable-allowlist:begin %s(.*?)servable-allowlist:end %s" % (plat, plat),
            go_text,
            re.S,
        )
        out[plat] = set(re.findall(r'"([^"]+)":\s*\{\}', m.group(1))) if m else set()
    return out


def overlay_priced(entry: object) -> bool:
    if not isinstance(entry, dict):
        return False
    return (
        (entry.get("input_cost_per_token") or 0) > 0
        or (entry.get("output_cost_per_token") or 0) > 0
        or (entry.get("output_cost_per_image") or 0) > 0
        or (entry.get("output_cost_per_second") or 0) > 0
    )


def covered_overlay_ids(overlay: dict) -> set[str]:
    return {k for k, v in overlay.items() if k != "_meta" and overlay_priced(v)}


def _git(*args: str) -> str:
    return subprocess.check_output(["git", "-C", str(REPO), *args], text=True)


def added_allowlist_models(base: str) -> dict[str, set[str]]:
    """Models present in HEAD's allowlist but not base's, per platform."""
    try:
        base_go = _git("show", f"{base}:{GO_REL}")
    except subprocess.CalledProcessError:
        base_go = ""  # file absent at base → everything is "added"
    head_go = (REPO / GO_REL).read_text(encoding="utf-8")
    base_al = parse_allowlist(base_go)
    head_al = parse_allowlist(head_go)
    return {p: head_al.get(p, set()) - base_al.get(p, set()) for p in PLATFORMS}


def has_marker(base: str) -> bool:
    try:
        msgs = _git("log", "--format=%B", f"{base}..HEAD")
    except subprocess.CalledProcessError:
        return False
    return MARKER in msgs


def _base_resolves(base: str) -> bool:
    try:
        _git("rev-parse", "--verify", "--quiet", f"{base}^{{commit}}")
        return True
    except subprocess.CalledProcessError:
        return False


def cmd_check(args) -> int:
    if not _base_resolves(args.base):
        # Offline / shallow clone without the base ref: skip rather than treat the
        # whole allowlist as "added" (a false-fail). CI fetches origin/main, so the
        # gate is enforced there.
        print(f"display-coverage-gate: skip (base {args.base!r} not resolvable locally)")
        return 0
    try:
        overlay = json.loads((REPO / OVERLAY_REL).read_text(encoding="utf-8"))
        added = added_allowlist_models(args.base)
    except Exception as e:  # noqa: BLE001
        print(f"display-coverage-gate: error: {e}", file=sys.stderr)
        return 2
    covered = covered_overlay_ids(overlay)
    uncovered: list[tuple[str, str]] = []
    for plat, ids in added.items():
        for mid in sorted(ids):
            if mid not in covered:
                uncovered.append((plat, mid))
    if not uncovered:
        print("display-coverage-gate: ok (no new allowlist entries lack overlay display coverage)")
        return 0
    if has_marker(args.base):
        print(f"display-coverage-gate: ok ({MARKER} present; "
              f"{len(uncovered)} new allowlist entr(ies) declared remote-displayed)")
        for plat, mid in uncovered:
            print(f"    (remote-verified) {plat}: {mid}")
        return 0
    print("display-coverage-gate: FAIL — new servable-allowlist entr(ies) with NO display "
          "price source (overlay). The bundled mirror does NOT display in prod.", file=sys.stderr)
    for plat, mid in uncovered:
        print(f"    {plat}: {mid}", file=sys.stderr)
    print(f"\nfix: add a priced tk_pricing_overlay.json entry for each (ops/pricing/"
          f"apply-pricing-hotfix.py stage-overlay), OR if it genuinely displays via the\n"
          f"upstream remote mirror, put `{MARKER}` in a commit message after confirming with\n"
          f"ops/pricing/audit-display-coverage.py check --live.", file=sys.stderr)
    return 1


def cmd_selftest(_args) -> int:
    base_go = (
        "// servable-allowlist:begin openai\n"
        '\t"gpt-5": {},\n'
        "// servable-allowlist:end openai\n"
    )
    head_go = (
        "// servable-allowlist:begin openai\n"
        '\t"gpt-5": {},\n\t"gpt-5.6-sol": {},\n\t"gpt-6": {},\n'
        "// servable-allowlist:end openai\n"
    )
    bal, hal = parse_allowlist(base_go), parse_allowlist(head_go)
    added = {p: hal.get(p, set()) - bal.get(p, set()) for p in PLATFORMS}
    assert added["openai"] == {"gpt-5.6-sol", "gpt-6"}, added["openai"]

    overlay = {"_meta": {}, "gpt-6": {"input_cost_per_token": 1e-5, "output_cost_per_token": 3e-5}}
    covered = covered_overlay_ids(overlay)
    assert covered == {"gpt-6"}, covered
    # gpt-5.6-sol uncovered (the #1030 shape) → would FAIL without marker; gpt-6 covered.
    uncovered = [(p, m) for p, ids in added.items() for m in ids if m not in covered]
    assert uncovered == [("openai", "gpt-5.6-sol")], uncovered

    # media coverage counts
    assert overlay_priced({"output_cost_per_image": 0.04})
    assert overlay_priced({"output_cost_per_second": 0.6})
    assert not overlay_priced({"input_cost_per_token": 0})
    assert not overlay_priced({"litellm_provider": "openai"})
    print("display-coverage-gate selftest: PASS")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description="forward guard: new allowlist entry ⇒ overlay display coverage")
    sub = ap.add_subparsers(dest="cmd", required=True)
    ck = sub.add_parser("check")
    ck.add_argument("--base", default="origin/main", help="diff base (default origin/main)")
    ck.set_defaults(func=cmd_check)
    st = sub.add_parser("selftest")
    st.set_defaults(func=cmd_selftest)
    args = ap.parse_args()
    return args.func(args)


if __name__ == "__main__":
    raise SystemExit(main())
