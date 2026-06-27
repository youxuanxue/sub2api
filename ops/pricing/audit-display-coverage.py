#!/usr/bin/env python3
"""audit-display-coverage.py — servable ⇒ displayable completeness audit.

The #1030 failure class: a model is in the Go servable allowlist
(pricing_catalog_supported_models_tk.go) and bills correctly (fallbackPrices /
family floor), but shows a BLANK price on /pricing because no DISPLAY source
carries it. The display catalog is built from the upstream remote mirror
(Wei-Shaw/model-price-repo, live-fetched) UNIONed with the TK overlay
(tk_pricing_overlay.json, which INJECTS models the source lacks). The bundled
backend/resources/model-pricing file is only a SHADOWED fallback prod does not
read, and it is hand-maintained — so it is NOT a reliable "will it display"
signal. The overlay is the only prod-reliable, repo-checkable display source.

This tool asserts the invariant operators actually care about:

    every model in the Go servable allowlist resolves to a NON-ZERO display
    price, via the overlay (repo truth) OR the live prod /pricing (remote truth).

"Display price" is media-aware: token models need input/output > 0; image
models need output_cost_per_image > 0; video models need output_cost_per_second
> 0.

Coverage sources, in order:
  - overlay  : tk_pricing_overlay.json carries the model with a non-zero price
               (token OR media). Reliable in prod regardless of remote lag.
  - live     : with --live, GET <base>/api/v1/public/pricing and treat a model
               shown there with a non-zero token price as covered (this is how
               standard upstream models — claude/gpt-5/… — are legitimately
               sourced from the remote mirror, NOT the overlay).

A model covered by NEITHER is a GAP (the #1030 shape). Antigravity/grok models
are filtered out of the PUBLIC catalog by the vendor allowlist, so for those the
overlay is the only coverage signal — exactly why #1029's antigravity image
additions show as gaps until overlay-priced.

Subcommands:
  check        report gaps (exit 0 clean / 1 gaps / 2 error). --live to also
               credit models displayed on prod. --platform to scope.
  selftest     offline unit tests (no network, no repo reads).

This is the LIVE CLOSE-OUT of model onboarding: run `check --live` after a
catalog change is live and require 0 gaps. It is also a safe (read-only)
periodic prod audit.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import sys
import urllib.request
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
GO_ALLOWLIST = REPO / "backend/internal/service/pricing_catalog_supported_models_tk.go"
OVERLAY = REPO / "backend/internal/service/tk_pricing_overlay.json"
DEFAULT_BASE = os.environ.get("TOKENKEY_BASE_URL", "https://api.tokenkey.dev")
PLATFORMS = ("anthropic", "openai", "gemini", "antigravity", "grok")


def parse_allowlist(go_text: str) -> dict[str, set[str]]:
    """Extract {platform: {model_id, …}} from the splice-marked Go maps."""
    out: dict[str, set[str]] = {}
    for plat in PLATFORMS:
        m = re.search(
            r"servable-allowlist:begin %s(.*?)servable-allowlist:end %s" % (plat, plat),
            go_text,
            re.S,
        )
        out[plat] = set(re.findall(r'"([^"]+)":\s*\{\}', m.group(1))) if m else set()
    return out


def overlay_priced(entry: dict) -> bool:
    """True iff the overlay entry carries a non-zero price (token OR media)."""
    if not isinstance(entry, dict):
        return False
    return (
        (entry.get("input_cost_per_token") or 0) > 0
        or (entry.get("output_cost_per_token") or 0) > 0
        or (entry.get("output_cost_per_image") or 0) > 0
        or (entry.get("output_cost_per_second") or 0) > 0
    )


def overlay_covered(overlay: dict) -> set[str]:
    return {k for k, v in overlay.items() if k != "_meta" and overlay_priced(v)}


def _row_token_priced(row: dict) -> bool:
    pr = row.get("pricing") or {}
    tiers = pr.get("tiers") or []
    base = tiers[0] if tiers else pr
    return (base.get("input_per_1k_tokens") or 0) > 0 or (base.get("output_per_1k_tokens") or 0) > 0


def live_token_priced(payload: dict) -> set[str]:
    """Model ids that the live public catalog shows with a non-zero TOKEN price.
    Media coverage is judged via the overlay (reliable), so the live signal only
    needs to credit token models sourced from the remote mirror."""
    out: set[str] = set()
    for row in payload.get("data", []):
        mid = row.get("model_id") or row.get("id")
        if mid and _row_token_priced(row):
            out.add(mid)
    return out


def fetch_live(base_url: str) -> dict:
    url = base_url.rstrip("/") + "/api/v1/public/pricing"
    with urllib.request.urlopen(url, timeout=25) as r:  # noqa: S310 (fixed prod URL)
        return json.loads(r.read().decode())


def audit(
    allowlist: dict[str, set[str]],
    covered_overlay: set[str],
    covered_live: set[str],
) -> dict[str, list[tuple[str, str]]]:
    """Return {platform: [(model, reason), …]} for allowlisted-but-uncovered."""
    gaps: dict[str, list[tuple[str, str]]] = {}
    for plat, ids in allowlist.items():
        miss = []
        for mid in sorted(ids):
            if mid in covered_overlay:
                continue
            if mid in covered_live:
                continue
            reason = "no overlay entry; not displayed on live /pricing" if covered_live else "no overlay entry"
            miss.append((mid, reason))
        if miss:
            gaps[plat] = miss
    return gaps


def cmd_check(args) -> int:
    try:
        allowlist = parse_allowlist(GO_ALLOWLIST.read_text(encoding="utf-8"))
        overlay = json.loads(OVERLAY.read_text(encoding="utf-8"))
    except Exception as e:  # noqa: BLE001
        print(f"ERROR: cannot read repo sources: {e}", file=sys.stderr)
        return 2
    covered_overlay = overlay_covered(overlay)
    covered_live: set[str] = set()
    if args.live:
        try:
            covered_live = live_token_priced(fetch_live(args.base_url))
        except Exception as e:  # noqa: BLE001
            print(f"ERROR: live /pricing fetch failed ({e}); rerun without --live for repo-only audit", file=sys.stderr)
            return 2
    if args.platform:
        allowlist = {args.platform: allowlist.get(args.platform, set())}

    gaps = audit(allowlist, covered_overlay, covered_live)
    mode = "overlay ∪ live prod /pricing" if args.live else "overlay only (repo) — pass --live to credit remote-displayed models"
    print(f"=== display-coverage audit (source: {mode}) ===")
    total_models = sum(len(v) for v in allowlist.values())
    total_gaps = sum(len(v) for v in gaps.values())
    for plat, ids in allowlist.items():
        plat_gaps = gaps.get(plat, [])
        print(f"  {plat}: {len(plat_gaps)}/{len(ids)} uncovered")
        for mid, why in plat_gaps:
            print(f"      GAP {mid} — {why}")
    print(f"TOTAL: {total_gaps} display gap(s) across {total_models} allowlisted model(s)")
    if total_gaps:
        print("\nfix: add a priced entry to tk_pricing_overlay.json (the prod-reliable display", file=sys.stderr)
        print("source) for each GAP — use ops/pricing/apply-pricing-hotfix.py stage-overlay.", file=sys.stderr)
    return 1 if total_gaps else 0


def cmd_selftest(_args) -> int:
    # parse_allowlist
    go = (
        "x\n// servable-allowlist:begin openai\n"
        '\t"gpt-5": {},\n\t"gpt-5.6-sol": {},\n'
        "// servable-allowlist:end openai\n"
        "// servable-allowlist:begin gemini\n"
        '\t"imagen-4.0-generate-001": {},\n\t"gemini-2.5-pro": {},\n'
        "// servable-allowlist:end gemini\n"
    )
    al = parse_allowlist(go)
    assert al["openai"] == {"gpt-5", "gpt-5.6-sol"}, al["openai"]
    assert al["gemini"] == {"imagen-4.0-generate-001", "gemini-2.5-pro"}, al["gemini"]
    assert al["anthropic"] == set() and al["grok"] == set(), al

    # overlay_priced: token / image / video / zero / missing
    assert overlay_priced({"input_cost_per_token": 5e-6})
    assert overlay_priced({"output_cost_per_image": 0.04})
    assert overlay_priced({"output_cost_per_second": 0.6})
    assert not overlay_priced({"input_cost_per_token": 0, "output_cost_per_token": 0})
    assert not overlay_priced({"litellm_provider": "openai"})  # no price fields
    ov = {
        "_meta": {"note": "x"},
        "imagen-4.0-generate-001": {"output_cost_per_image": 0.04},  # media → covered
        "veo-3.1-generate-001": {"output_cost_per_second": 0.6},     # media → covered
        "zero-model": {"input_cost_per_token": 0},                   # zero → NOT covered
    }
    assert overlay_covered(ov) == {"imagen-4.0-generate-001", "veo-3.1-generate-001"}, overlay_covered(ov)

    # live_token_priced: tiers + flat, media-zero excluded
    live = {"data": [
        {"model_id": "gpt-5", "pricing": {"tiers": [{"input_per_1k_tokens": 0.01, "output_per_1k_tokens": 0.03}]}},
        {"model_id": "imagen-4.0-generate-001", "pricing": {"tiers": [{"input_per_1k_tokens": 0, "output_per_1k_tokens": 0}]}},
        {"model_id": "gpt-flat", "pricing": {"input_per_1k_tokens": 0.002}},
    ]}
    lt = live_token_priced(live)
    assert lt == {"gpt-5", "gpt-flat"}, lt  # imagen has zero token price → not credited here (overlay covers media)

    # audit: the #1030 + #1029-antigravity shapes
    allowlist = {
        "openai": {"gpt-5", "gpt-5.6-sol"},          # gpt-5 via live; gpt-5.6-sol uncovered
        "gemini": {"imagen-4.0-generate-001"},        # media via overlay
        "antigravity": {"gemini-3.5-flash"},          # uncovered (no overlay, filtered from public)
    }
    covered_overlay = {"imagen-4.0-generate-001"}
    covered_live = {"gpt-5"}
    g = audit(allowlist, covered_overlay, covered_live)
    assert g.get("openai") == [("gpt-5.6-sol", "no overlay entry; not displayed on live /pricing")], g
    assert "gemini" not in g, g            # imagen covered by overlay (media)
    assert g.get("antigravity") == [("gemini-3.5-flash", "no overlay entry; not displayed on live /pricing")], g

    # repo-only mode (no live): gpt-5 also a gap (only overlay credited)
    g2 = audit(allowlist, covered_overlay, set())
    assert ("gpt-5", "no overlay entry") in g2["openai"], g2
    print("audit-display-coverage selftest: PASS")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description="servable ⇒ displayable completeness audit")
    sub = ap.add_subparsers(dest="cmd", required=True)
    ck = sub.add_parser("check", help="report allowlisted-but-undisplayable models")
    ck.add_argument("--live", action="store_true", help="also credit models shown priced on prod /pricing")
    ck.add_argument("--base-url", default=DEFAULT_BASE, help=f"prod base (default {DEFAULT_BASE})")
    ck.add_argument("--platform", choices=PLATFORMS, help="scope to one platform")
    ck.set_defaults(func=cmd_check)
    st = sub.add_parser("selftest", help="offline unit tests")
    st.set_defaults(func=cmd_selftest)
    args = ap.parse_args()
    return args.func(args)


if __name__ == "__main__":
    raise SystemExit(main())
