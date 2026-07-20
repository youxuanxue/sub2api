#!/usr/bin/env python3
"""Validate the TK served-models MANIFEST against the mechanisms it must agree with.

Source of truth: backend/internal/service/tk_served_models.json — a THIN declarative
intent layer ("TK serves model M on platform P via an account credentials.model_mapping
whitelist, at price π, display=yes/no") that sits ABOVE three already-existing
mechanisms and must AGREE with each:

  (1) per-account credentials.model_mapping — the runtime servable WHITELIST, written
      by tk_NNN_*.sql migrations (and admin-UI edits). The DECLARED-served signal.
  (2) tk_pricing_overlay.json + the runtime litellm mirror — the PRICE.
  (3) display intent — newapi uses this manifest's `display` bit directly; native
      platforms still use the Go servable-allowlist maps in
      pricing_catalog_supported_models_tk.go.

This guard hardens the #812-class regression (a model priced + advertised-as-intended
but never actually wired onto the serving account's model_mapping => empty pool =>
429/503 in prod) into a mechanical CI gate (CLAUDE.md §「升级原则」). It asserts:

  A0 PARSE        manifest + overlay parse; every entry has the required fields/types.
  A1 PRICE        every entry resolves to a non-zero price in its declared price_source
                  (overlay key > 0 for the mode; mirror => overlay does not zero it;
                  channel => notes documents the channel source).            HARD-FAIL
  A2 DISPLAY      native display==true => model_id present in the platform's Go
                  allowlist map. newapi display is owned by this manifest.     HARD
  A3 SERVED_ON    every served_on account => some tk_*.sql model_mapping migration maps
                  model_id onto that account (quoted id co-occurring with `id = <A>`).
                  This is the #812-catching direction.                        HARD-FAIL
                  Escape hatch: an entry whose notes contain the literal
                  `served-via-admin-ui` is downgraded to WARN (model_mapping applied
                  out-of-band of any migration — a reviewable, greppable opt-out).
  A4 ENUMERATION  (advisory WARN) every dashscope/deepseek chat overlay key SHOULD be a
                  manifest entry (catch a priced+served-but-forgotten model).

Usage: python3 scripts/checks/catalog-serving-drift.py [--quiet] [--selftest]
Exit 0 ok, 1 drift, 2 missing dep / file / unparseable.
"""

from __future__ import annotations

import json
import pathlib
import re
import sys

REPO_ROOT = pathlib.Path(__file__).resolve().parent.parent.parent
SERVICE_DIR = REPO_ROOT / "backend" / "internal" / "service"
MANIFEST = SERVICE_DIR / "tk_served_models.json"
OVERLAY = SERVICE_DIR / "tk_pricing_overlay.json"
ALLOWLIST_GO = SERVICE_DIR / "pricing_catalog_supported_models_tk.go"
MIGRATIONS_DIR = REPO_ROOT / "backend" / "migrations"
# Broad on purpose: model_mapping JSON lives in several tk_*.sql files whose NAME does
# not contain "model_mapping" (tk_020/tk_021/tk_022 set mappings too). A3 still scopes
# by the quoted model_id co-occurring with the account guard, so widening the glob only
# adds files that cannot false-match (they lack the quoted id).
MIGRATION_GLOB = "tk_*.sql"

# Price mode -> the field(s) that MUST be > 0 for that mode (mirrors pricing-overlay.py
# MODE_FIELDS so the overlay arm of A1 is byte-identical to a check already green).
MODE_FIELDS = {
    "image_generation": ("output_cost_per_image",),
    "video_generation": ("output_cost_per_second",),
    "chat": ("input_cost_per_token", "output_cost_per_token"),
}

# Native platforms that actually have a Go servable-allowlist map.
ALLOWLIST_PLATFORMS = ("anthropic", "openai", "gemini", "antigravity")

# A4 advisory: overlay vendors whose chat models are the manifest's curated long-tail.
ENUMERATION_PROVIDERS = {"dashscope", "deepseek", "moonshot", "volcengine", "zhipu", "bigmodel", "zai"}

# Recognized literal escape hatch in an entry's notes (A3 migration-scan opt-out).
ADMIN_UI_OPT_OUT = "served-via-admin-ui"

REQUIRED_FIELDS = {
    "platform": str,
    "model_id": str,
    "served_on": list,
    "channel_type": int,
    "price_source": str,
    "price_key": str,
    "display": bool,
}
VALID_PRICE_SOURCES = {"overlay", "mirror", "channel"}


# --------------------------------------------------------------------------- helpers


def _is_pos_number(v) -> bool:
    return isinstance(v, (int, float)) and not isinstance(v, bool) and v > 0


def parse_allowlist_maps(go_text: str) -> dict[str, set[str]]:
    """Extract {platform: {quoted keys}} from the marker-delimited Go map literals.

    Mirrors ops/pricing/refresh-servable-allowlist.py's splice anchors:
    `// servable-allowlist:begin <platform>` ... `// servable-allowlist:end <platform>`.
    Keys are pulled with the `"<id>":` map-key regex (comment lines have no `":`).
    """
    out: dict[str, set[str]] = {}
    for platform in ALLOWLIST_PLATFORMS:
        begin = re.search(
            r"//\s*servable-allowlist:begin\s+" + re.escape(platform) + r"\b", go_text
        )
        end = re.search(
            r"//\s*servable-allowlist:end\s+" + re.escape(platform) + r"\b", go_text
        )
        if not begin or not end or end.start() <= begin.end():
            # Missing/corrupted marker block: leave platform absent so A2 can flag it
            # only if some entry claims display=true on it (vacuous for the newapi seed).
            continue
        block = go_text[begin.end() : end.start()]
        out[platform] = set(re.findall(r'"([^"]+)"\s*:', block))
    return out


def migration_maps_model(files: dict[str, str], account: str, model_id: str) -> bool:
    """True iff some migration file contains BOTH the account guard `id = <account>`
    and the QUOTED model id `"<model_id>"` (file-level co-occurrence).

    Quoting defeats the prefix false-match (`"qwen3.7-max"` does NOT match inside
    `"qwen3.7-max-preview"` because of the trailing quote). The seed shares no model_id
    across accounts 39/60, so file-level (vs per-statement) scoping cannot bleed; a
    future migration that maps the same name onto two accounts in one file would need
    per-`id`-statement scoping — documented in the guardFalsePositiveAnalysis.
    """
    guard = re.compile(r"\bid\s*=\s*" + re.escape(account) + r"\b")
    quoted = '"' + model_id + '"'
    for text in files.values():
        if quoted in text and guard.search(text):
            return True
    return False


# --------------------------------------------------------------------------- core


def evaluate(
    manifest: dict,
    overlay: dict,
    allowlist: dict[str, set[str]],
    migration_files: dict[str, str],
) -> tuple[list[str], list[str]]:
    """Return (hard_errors, warnings). Pure — no I/O — so --selftest can drive it."""
    errors: list[str] = []
    warnings: list[str] = []

    entries = manifest.get("entries")
    if not isinstance(entries, dict):
        return (["manifest has no top-level `entries` object"], warnings)

    overlay_entries = {k: v for k, v in overlay.items() if not k.startswith("_")}
    manifest_model_ids: set[str] = set()

    for key, entry in entries.items():
        if key.startswith("_"):
            continue
        # ---- A0 structural ------------------------------------------------------
        if not isinstance(entry, dict):
            errors.append(f"{key}: entry is not an object")
            continue
        bad_field = False
        for field, typ in REQUIRED_FIELDS.items():
            if field not in entry:
                errors.append(f"{key}: missing required field {field!r}")
                bad_field = True
                continue
            val = entry[field]
            # bool is a subclass of int — guard channel_type explicitly.
            if typ is int and isinstance(val, bool):
                errors.append(f"{key}: field {field!r} must be int, got bool")
                bad_field = True
            elif not isinstance(val, typ):
                errors.append(
                    f"{key}: field {field!r} must be {typ.__name__}, got {type(val).__name__}"
                )
                bad_field = True
        if bad_field:
            continue
        if any(not isinstance(a, str) for a in entry["served_on"]):
            errors.append(f"{key}: served_on must be a list of account-id strings")
            continue
        if entry["price_source"] not in VALID_PRICE_SOURCES:
            errors.append(
                f"{key}: price_source {entry['price_source']!r} not in {sorted(VALID_PRICE_SOURCES)}"
            )
            continue

        platform = entry["platform"]
        model_id = entry["model_id"]
        price_source = entry["price_source"]
        price_key = entry["price_key"]
        notes = entry.get("notes", "") or ""
        manifest_model_ids.add(model_id)

        # ---- A1 price-resolvable -----------------------------------------------
        if price_source == "overlay":
            po = overlay_entries.get(price_key)
            if not isinstance(po, dict):
                errors.append(
                    f"{key}: price_source=overlay but price_key {price_key!r} absent from "
                    f"tk_pricing_overlay.json"
                )
            else:
                mode = po.get("mode")
                fields = MODE_FIELDS.get(mode)
                if fields is None:
                    errors.append(
                        f"{key}: overlay entry {price_key!r} has unrecognized mode {mode!r}"
                    )
                else:
                    for f in fields:
                        if not _is_pos_number(po.get(f)):
                            errors.append(
                                f"{key}: overlay {price_key!r} (mode={mode}) requires {f} > 0, "
                                f"got {po.get(f)!r}"
                            )
        elif price_source == "mirror":
            # Static guard cannot read the live mirror; only prove the overlay does NOT
            # zero it out (a $0 overlay row would shadow the mirror price). Deliberately
            # weak arm (deepseek-chat/reasoner): can false-NEGATIVE, never false-POSITIVE.
            po = overlay_entries.get(price_key)
            if isinstance(po, dict):
                mode = po.get("mode")
                fields = MODE_FIELDS.get(mode, ())
                if fields and all(not _is_pos_number(po.get(f)) for f in fields):
                    errors.append(
                        f"{key}: price_source=mirror but a $0 overlay row for {price_key!r} "
                        f"shadows the mirror price (remove the zero overlay row or correct it)"
                    )
        elif price_source == "channel":
            if "channel" not in notes.lower():
                errors.append(
                    f"{key}: price_source=channel requires a notes substring documenting "
                    f"the channel_model_pricing source (static guard cannot read the DB)"
                )

        # ---- A2 display => allowlist -------------------------------------------
        if entry["display"] and platform != "newapi":
            if platform not in ALLOWLIST_PLATFORMS:
                errors.append(
                    f"{key}: display=true on platform {platform!r} which has NO Go "
                    f"servable-allowlist map or manifest display owner — display must "
                    f"be false (false promise guard)"
                )
            elif model_id not in allowlist.get(platform, set()):
                errors.append(
                    f"{key}: display=true but {model_id!r} absent from the {platform} "
                    f"servable-allowlist map in pricing_catalog_supported_models_tk.go"
                )

        # ---- A3 served_on => migration -----------------------------------------
        admin_ui = ADMIN_UI_OPT_OUT in notes
        for account in entry["served_on"]:
            if migration_maps_model(migration_files, account, model_id):
                continue
            msg = (
                f"{key}: served_on lists account {account} but NO tk_*.sql migration maps "
                f'"{model_id}" onto it (`id = {account}` + quoted id). #812-class gap: the '
                f"runtime pool is empty for this model => 429/503. Land a tk_NNN_*_model_mapping "
                f"migration advertising it on account {account}, or remove this row."
            )
            if admin_ui:
                warnings.append(
                    msg + f"  [downgraded to WARN: notes carries '{ADMIN_UI_OPT_OUT}']"
                )
            else:
                errors.append(msg)

    # ---- A4 enumeration completeness (advisory) --------------------------------
    for k, v in overlay_entries.items():
        if not isinstance(v, dict):
            continue
        if v.get("litellm_provider") in ENUMERATION_PROVIDERS and v.get("mode") == "chat":
            if k not in manifest_model_ids:
                warnings.append(
                    f"overlay chat model {k!r} (provider={v.get('litellm_provider')}) is not a "
                    f"manifest entry — confirm it is intentionally not account-mapping-served, "
                    f"or add it (advisory)."
                )

    return (errors, warnings)


# --------------------------------------------------------------------------- selftest


def _selftest() -> int:
    """Synthetic pass+fail fixtures, no repo I/O — proves each assertion fires."""
    overlay = {
        "_meta": {"note": "x"},
        "good-chat": {
            "litellm_provider": "dashscope",
            "mode": "chat",
            "input_cost_per_token": 1e-7,
            "output_cost_per_token": 2e-7,
        },
        "zero-chat": {
            "litellm_provider": "deepseek",
            "mode": "chat",
            "input_cost_per_token": 0,
            "output_cost_per_token": 0,
        },
        "forgotten-chat": {
            "litellm_provider": "dashscope",
            "mode": "chat",
            "input_cost_per_token": 1e-7,
            "output_cost_per_token": 2e-7,
        },
    }
    allowlist = {"anthropic": {"claude-opus-4-8"}, "openai": set(), "gemini": set(),
                 "antigravity": set()}
    migrations = {
        "tk_900_x.sql": 'UPDATE accounts ... WHERE id = 60 ... "good-chat": "good-chat"',
        # account 60 also gets the mirror + admin-ui entries below
        "tk_901_y.sql": 'WHERE id = 60 ... "mirror-chat" "adminui-chat"',
    }

    def run(entries):
        return evaluate({"entries": entries}, overlay, allowlist, migrations)

    failures: list[str] = []

    # --- PASS fixture: every assertion satisfied -------------------------------
    pass_entries = {
        "newapi/good-chat": {
            "platform": "newapi", "model_id": "good-chat", "served_on": ["60"],
            "channel_type": 17, "price_source": "overlay", "price_key": "good-chat",
            "display": True, "notes": "ok",
        },
        "newapi/mirror-chat": {
            "platform": "newapi", "model_id": "mirror-chat", "served_on": ["60"],
            "channel_type": 43, "price_source": "mirror", "price_key": "mirror-chat",
            "display": False, "notes": "mirror priced",
        },
        "newapi/adminui-chat": {
            "platform": "newapi", "model_id": "adminui-chat", "served_on": ["60"],
            "channel_type": 17, "price_source": "overlay", "price_key": "good-chat",
            "display": False, "notes": "applied out-of-band served-via-admin-ui",
        },
    }
    errs, warns = run(pass_entries)
    # forgotten-chat triggers the A4 advisory warning only (not an error).
    if errs:
        failures.append(f"PASS fixture produced hard errors: {errs}")
    if not any("forgotten-chat" in w for w in warns):
        failures.append("PASS fixture: expected A4 advisory warning for forgotten-chat")

    # --- A0: missing field / wrong type ----------------------------------------
    errs, _ = run({"newapi/bad": {"platform": "newapi", "model_id": "good-chat"}})
    if not any("missing required field" in e for e in errs):
        failures.append("A0: missing-field not flagged")
    errs, _ = run({"newapi/bad": {
        "platform": "newapi", "model_id": "good-chat", "served_on": ["60"],
        "channel_type": True, "price_source": "overlay", "price_key": "good-chat",
        "display": False,
    }})
    if not any("must be int, got bool" in e for e in errs):
        failures.append("A0: channel_type bool-as-int not flagged")

    # --- A1: overlay price missing / zero --------------------------------------
    errs, _ = run({"newapi/absent": {
        "platform": "newapi", "model_id": "nope", "served_on": ["60"],
        "channel_type": 17, "price_source": "overlay", "price_key": "nonexistent",
        "display": False, "notes": "served-via-admin-ui",
    }})
    if not any("absent from" in e for e in errs):
        failures.append("A1: missing overlay key not flagged")
    errs, _ = run({"newapi/zero": {
        "platform": "newapi", "model_id": "zero-chat", "served_on": ["60"],
        "channel_type": 43, "price_source": "overlay", "price_key": "zero-chat",
        "display": False, "notes": "served-via-admin-ui",
    }})
    if not any("requires" in e and "> 0" in e for e in errs):
        failures.append("A1: $0 overlay price not flagged")
    # mirror shadowed by a $0 overlay row
    errs, _ = run({"newapi/mzero": {
        "platform": "newapi", "model_id": "zero-chat", "served_on": ["60"],
        "channel_type": 43, "price_source": "mirror", "price_key": "zero-chat",
        "display": False, "notes": "served-via-admin-ui",
    }})
    if not any("shadows the mirror" in e for e in errs):
        failures.append("A1: $0 overlay shadowing a mirror entry not flagged")

    # --- A2: display=true on a no-map platform, and missing from a real map -----
    errs, _ = run({"other/disp": {
        "platform": "bedrock", "model_id": "good-chat", "served_on": ["60"],
        "channel_type": 17, "price_source": "overlay", "price_key": "good-chat",
        "display": True, "notes": "served-via-admin-ui",
    }})
    if not any("has NO Go" in e for e in errs):
        failures.append("A2: display=true on no-map platform not flagged")
    errs, _ = run({"anthropic/disp": {
        "platform": "anthropic", "model_id": "claude-ghost", "served_on": ["1"],
        "channel_type": 0, "price_source": "mirror", "price_key": "claude-ghost",
        "display": True, "notes": "served-via-admin-ui",
    }})
    if not any("absent from the anthropic servable-allowlist" in e for e in errs):
        failures.append("A2: display=true absent from real map not flagged")

    # --- A3: served_on with no migration (hard) vs admin-ui opt-out (warn) ------
    errs, _ = run({"newapi/gap": {
        "platform": "newapi", "model_id": "unmapped", "served_on": ["60"],
        "channel_type": 17, "price_source": "overlay", "price_key": "good-chat",
        "display": False, "notes": "known gap",
    }})
    if not any("#812-class gap" in e for e in errs):
        failures.append("A3: unmapped served_on not hard-flagged")
    errs, warns = run({"newapi/oob": {
        "platform": "newapi", "model_id": "unmapped", "served_on": ["60"],
        "channel_type": 17, "price_source": "overlay", "price_key": "good-chat",
        "display": False, "notes": "applied out-of-band served-via-admin-ui",
    }})
    if any("#812-class gap" in e for e in errs):
        failures.append("A3: admin-ui opt-out still hard-failed")
    if not any("#812-class gap" in w for w in warns):
        failures.append("A3: admin-ui opt-out did not downgrade to WARN")

    # --- A3 prefix-quote: "good-chat" must NOT match "good-chat-preview" --------
    pmig = {"tk_902.sql": 'WHERE id = 60 ... "good-chat-preview": "good-chat-preview"'}
    errs, _ = evaluate(
        {"entries": {"newapi/pfx": {
            "platform": "newapi", "model_id": "good-chat", "served_on": ["60"],
            "channel_type": 17, "price_source": "overlay", "price_key": "good-chat",
            "display": False, "notes": "n",
        }}},
        overlay, allowlist, pmig,
    )
    if not any("#812-class gap" in e for e in errs):
        failures.append("A3: quoted-id prefix isolation broken (matched a -preview key)")

    if failures:
        print("SELFTEST FAILED:", flush=True)
        for f in failures:
            print(f"  - {f}", flush=True)
        return 1
    print("selftest ok: A0/A1/A2/A3/A4 pass+fail fixtures all behave", flush=True)
    return 0


# --------------------------------------------------------------------------- main


def main() -> int:
    quiet = "--quiet" in sys.argv
    if "--selftest" in sys.argv:
        return _selftest()

    for path, label in ((MANIFEST, "served-models manifest"),
                        (OVERLAY, "pricing overlay"),
                        (ALLOWLIST_GO, "servable-allowlist Go file")):
        if not path.is_file():
            print(f"  FAIL: {label} not found: {path}", flush=True)
            return 2
    if not MIGRATIONS_DIR.is_dir():
        print(f"  FAIL: migrations dir not found: {MIGRATIONS_DIR}", flush=True)
        return 2

    try:
        manifest = json.loads(MANIFEST.read_text(encoding="utf-8"))
        overlay = json.loads(OVERLAY.read_text(encoding="utf-8"))
    except (ValueError, OSError) as exc:
        print(f"  FAIL: manifest/overlay unparseable: {exc}", flush=True)
        return 2
    try:
        allowlist = parse_allowlist_maps(ALLOWLIST_GO.read_text(encoding="utf-8"))
        migration_files = {
            p.name: p.read_text(encoding="utf-8", errors="replace")
            for p in MIGRATIONS_DIR.glob(MIGRATION_GLOB)
        }
    except OSError as exc:
        print(f"  FAIL: reading allowlist/migrations: {exc}", flush=True)
        return 2

    errors, warnings = evaluate(manifest, overlay, allowlist, migration_files)

    if warnings and not quiet:
        print(f"  note: {len(warnings)} advisory warning(s):", flush=True)
        for w in warnings:
            print(f"    ~ {w}", flush=True)

    if errors:
        print(f"  FAIL: served-models manifest drift ({len(errors)} issue(s)):", flush=True)
        for e in errors:
            print(f"    - {e}", flush=True)
        return 1

    if not quiet:
        n = len([k for k in manifest.get("entries", {}) if not k.startswith("_")])
        print(f"  ok: {n} served-models manifest entries agree with price/display/migration",
              flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
