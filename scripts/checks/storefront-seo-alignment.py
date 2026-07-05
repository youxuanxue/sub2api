#!/usr/bin/env python3
"""Verify storefront SEO copy stays aligned across TS, Go prerender, and index.html."""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
TS_PATH = REPO_ROOT / "frontend/src/constants/storefrontSeo.tk.ts"
GO_PATH = REPO_ROOT / "backend/internal/web/prerender_seo_tk.go"
INDEX_PATH = REPO_ROOT / "frontend/index.html"
PRERENDER_PATH = REPO_ROOT / "backend/internal/web/prerender.go"

# label -> (TS section or None for top-level, TS field, Go const)
ALIGNMENT_KEYS: list[tuple[str, str | None, str, str]] = [
    ("siteTitle", None, "siteTitle", "storefrontSiteTitle"),
    ("canonicalOrigin", None, "canonicalOrigin", "storefrontCanonicalOrigin"),
    ("ogImageUrl", None, "ogImageUrl", "storefrontOGImageURL"),
    ("zh.metaDescription", "zh", "metaDescription", "storefrontZHMetaDescription"),
    ("zh.ogDescription", "zh", "ogDescription", "storefrontZHOGDescription"),
    ("zh.heroTitle", "zh", "heroTitle", "storefrontZHHeroTitle"),
    ("zh.heroSubtitle", "zh", "heroSubtitle", "storefrontZHHeroSubtitle"),
    ("zh.freeTrialZh", "zh", "freeTrialZh", "storefrontZHFreeTrial"),
    ("en.twitterDescription", "en", "twitterDescription", "storefrontENTwitterDescription"),
    ("en.freeTrialEn", "en", "freeTrialEn", "storefrontENFreeTrial"),
]


def extract_ts_string(section: str | None, name: str, text: str) -> str:
    haystack = text
    if section:
        match = re.search(rf"{re.escape(section)}:\s*\{{", text)
        if not match:
            raise ValueError(f"missing TS section {section}")
        depth = 0
        start = match.start()
        for idx in range(match.end() - 1, len(text)):
            ch = text[idx]
            if ch == "{":
                depth += 1
            elif ch == "}":
                depth -= 1
                if depth == 0:
                    haystack = text[match.end() - 1 : idx + 1]
                    break
        else:
            raise ValueError(f"unterminated TS section {section}")

    pattern = rf"{re.escape(name)}:\s*(?:\n\s*)?'((?:\\'|[^'])*)'"
    match = re.search(pattern, haystack)
    if not match:
        raise ValueError(f"missing TS field {section + '.' if section else ''}{name}")
    return match.group(1).replace("\\'", "'")


def extract_go_const(name: str, text: str) -> str:
    pattern = rf"{re.escape(name)}\s*=\s*\"((?:\\.|[^\"\\])*)\""
    match = re.search(pattern, text)
    if not match:
        raise ValueError(f"missing Go const {name}")
    return match.group(1)


def check() -> int:
    ts_text = TS_PATH.read_text(encoding="utf-8")
    go_text = GO_PATH.read_text(encoding="utf-8")
    index_text = INDEX_PATH.read_text(encoding="utf-8")
    prerender_text = PRERENDER_PATH.read_text(encoding="utf-8")
    surface_blob = index_text + prerender_text + go_text

    errors: list[str] = []
    for label, section, ts_name, go_name in ALIGNMENT_KEYS:
        try:
            ts_val = extract_ts_string(section, ts_name, ts_text)
            go_val = extract_go_const(go_name, go_text)
        except ValueError as exc:
            errors.append(str(exc))
            continue
        if ts_val != go_val:
            errors.append(f"{label}: TS/Go mismatch\n  ts:  {ts_val!r}\n  go:  {go_val!r}")
            continue
        if ts_val not in surface_blob:
            errors.append(f"{label}: value missing from index.html, prerender.go, and prerender_seo_tk.go")

    if "/og-cover.png" not in index_text:
        errors.append("ogImagePath: /og-cover.png missing from index.html")
    og_file = REPO_ROOT / "frontend/public/og-cover.png"
    if not og_file.is_file():
        errors.append("og-cover.png missing from frontend/public/")

    if errors:
        for err in errors:
            print(f"FAIL: {err}", file=sys.stderr)
        return 1

    print("ok: storefront SEO copy aligned (TS ↔ Go ↔ index/prerender surfaces)")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--selftest", action="store_true")
    args = parser.parse_args()
    if args.selftest:
        return 0 if check() == 0 else 1
    return check()


if __name__ == "__main__":
    raise SystemExit(main())
