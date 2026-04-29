#!/usr/bin/env python3
"""Verify release frontend assets carry critical admin-account UI fixes."""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path
from urllib.error import HTTPError, URLError
from urllib.parse import urljoin
from urllib.request import Request, urlopen


def read_url(url: str) -> str:
    req = Request(url, headers={"User-Agent": "tokenkey-frontend-asset-check/1.0"})
    try:
        with urlopen(req, timeout=15) as resp:
            return resp.read().decode("utf-8", errors="replace")
    except HTTPError as exc:
        raise RuntimeError(f"GET {url} failed: HTTP {exc.code}") from exc
    except URLError as exc:
        raise RuntimeError(f"GET {url} failed: {exc.reason}") from exc


def read_file(path: Path) -> str:
    try:
        return path.read_text(encoding="utf-8", errors="replace")
    except OSError as exc:
        raise RuntimeError(f"read {path} failed: {exc}") from exc


def account_assets_from_index(index_html: str) -> list[str]:
    return re.findall(r'src="(/assets/AccountsView-[^"]+\.js)"', index_html)


def vite_entry_script_from_index(index_html: str) -> str | None:
    m = re.search(r'src="(/assets/index-[^"]+\.js)"', index_html)
    return m.group(1) if m else None


def account_assets_from_vite_entry(entry_js: str) -> list[str]:
    """Resolve AccountsView chunk paths embedded in the main Vite bundle (mapDeps, imports)."""
    paths = re.findall(r'assets/(AccountsView-[^"\'\\]+\.js)', entry_js)
    seen: list[str] = []
    for name in paths:
        path = "/assets/" + name
        if path not in seen:
            seen.append(path)
    return seen


def check_account_asset(asset: str, source: str) -> list[str]:
    errors: list[str] = []
    platform_idx = asset.find("Extension Engine")
    if platform_idx < 0:
        errors.append(f"{source}: missing Extension Engine platform label")
        return errors

    create_mount_idx = asset.find("variant:\"create\"", platform_idx)
    if create_mount_idx < 0:
        errors.append(f"{source}: missing create-mode Extension Engine field mount near platform picker")
        return errors

    mount_window = asset[platform_idx:create_mount_idx]
    required_props = [
        "channelType:",
        "baseUrl:",
        "apiKey:",
        '"channel-type-options":',
        '"channel-types-loading":',
    ]
    missing = [prop for prop in required_props if prop not in mount_window]
    if missing:
        errors.append(f"{source}: create-mode Extension Engine field mount is missing props: {', '.join(missing)}")

    required_labels = [
        "newApiPlatform.channelType",
        "newApiPlatform.baseUrl",
        "newApiPlatform.apiKey",
    ]
    label_positions = {label: asset.find(label) for label in required_labels}
    missing_labels = [label for label, idx in label_positions.items() if idx < 0]
    if missing_labels:
        errors.append(f"{source}: shared NewAPI field component is missing labels: {', '.join(missing_labels)}")

    ordered_labels = [label_positions[label] for label in required_labels]
    if all(idx >= 0 for idx in ordered_labels) and ordered_labels != sorted(ordered_labels):
        errors.append(f"{source}: shared NewAPI channel/base-url/api-key labels are out of order")

    account_type_idx = asset.find("admin.accounts.accountType", platform_idx)
    if account_type_idx >= 0 and account_type_idx < create_mount_idx:
        errors.append(f"{source}: account-type block appears before create-mode Extension Engine field mount")

    quota_idx = asset.find("quotaControl.title", platform_idx)
    if quota_idx >= 0 and quota_idx < create_mount_idx:
        errors.append(f"{source}: quota controls appear before create-mode Extension Engine field mount")

    if create_mount_idx - platform_idx > 5000:
        errors.append(
            f"{source}: create-mode Extension Engine field mount is too far from platform picker ({create_mount_idx - platform_idx} bytes)"
        )

    return errors


def check_dist(dist: Path) -> list[str]:
    errors: list[str] = []
    accounts_assets = sorted((dist / "assets").glob("AccountsView-*.js"))
    if not accounts_assets:
        return [f"{dist}: missing assets/AccountsView-*.js"]

    checked = 0
    for path in accounts_assets:
        asset = read_file(path)
        if "Extension Engine" not in asset:
            continue
        checked += 1
        errors.extend(check_account_asset(asset, str(path)))

    if checked == 0:
        errors.append(f"{dist}: no AccountsView asset contains Extension Engine")
    return errors


def check_url(base_url: str) -> list[str]:
    base = base_url.rstrip("/") + "/"
    index = read_url(base)
    assets = account_assets_from_index(index)
    if not assets:
        entry_rel = vite_entry_script_from_index(index)
        if not entry_rel:
            return [f"{base}: index.html has no direct AccountsView script and no /assets/index-*.js entry"]
        entry_url = urljoin(base, entry_rel.lstrip("/"))
        try:
            entry_js = read_url(entry_url)
        except RuntimeError as exc:
            return [f"{base}: could not load Vite entry for AccountsView discovery: {exc}"]
        assets = account_assets_from_vite_entry(entry_js)
        if not assets:
            return [f"{base}: Vite entry {entry_rel} does not reference an AccountsView chunk"]

    errors: list[str] = []
    for asset_path in assets:
        asset_url = urljoin(base, asset_path.lstrip("/"))
        asset = read_url(asset_url)
        if "Extension Engine" not in asset:
            continue
        errors.extend(check_account_asset(asset, asset_url))
        return errors

    errors.append(f"{base}: referenced AccountsView assets do not contain Extension Engine")
    return errors


def main() -> int:
    parser = argparse.ArgumentParser()
    group = parser.add_mutually_exclusive_group(required=True)
    group.add_argument("--dist", type=Path, help="frontend dist directory to inspect")
    group.add_argument("--url", help="deployed TokenKey base URL to inspect")
    args = parser.parse_args()

    try:
        errors = check_dist(args.dist) if args.dist else check_url(args.url)
    except RuntimeError as exc:
        print(f"FAIL: {exc}", file=sys.stderr)
        return 1

    if errors:
        for err in errors:
            print(f"FAIL: {err}", file=sys.stderr)
        return 1

    print("ok: Extension Engine channel-type field is mounted adjacent to the platform picker")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
