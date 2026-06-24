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


# The admin account create/edit modals chunk. PR #935 split these modals out of
# AccountsView into their own Vite manualChunk named "admin-account-modals" (see
# frontend/vite.config.ts -> manualChunks). The Extension Engine / NewAPI field
# mount validated below now ships in that chunk, NOT in AccountsView. Keep the
# legacy "AccountsView" name as a fallback so the --url path still works against
# older deploys built before #935. The --dist gate selects chunks by CONTENT (see
# asset_has_account_create_mount) so it is immune to future chunk renames; this
# name list only drives URL-based discovery where we cannot scan every chunk.
ACCOUNT_MODAL_CHUNK_NAMES = ("admin-account-modals", "AccountsView")
_CHUNK_NAME_ALT = "|".join(ACCOUNT_MODAL_CHUNK_NAMES)


def account_assets_from_index(index_html: str) -> list[str]:
    return re.findall(rf'src="(/assets/(?:{_CHUNK_NAME_ALT})-[^"]+\.js)"', index_html)


def vite_entry_script_from_index(index_html: str) -> str | None:
    m = re.search(r'src="(/assets/index-[^"]+\.js)"', index_html)
    return m.group(1) if m else None


def account_assets_from_vite_entry(entry_js: str) -> list[str]:
    """Resolve account-modal chunk paths embedded in the main Vite bundle (mapDeps, imports)."""
    paths = re.findall(rf'assets/((?:{_CHUNK_NAME_ALT})-[^"\'\\]+\.js)', entry_js)
    seen: list[str] = []
    for name in paths:
        path = "/assets/" + name
        if path not in seen:
            seen.append(path)
    return seen


def js_asset_paths_from_vite_asset(asset: str) -> list[str]:
    """Resolve JS chunk paths embedded in a Vite chunk's dependency map/imports."""
    names = re.findall(r'assets/([^"\'\\]+\.js)', asset)
    names.extend(re.findall(r'from"\./([^"\'\\]+\.js)"', asset))
    seen: list[str] = []
    for name in names:
        path = "/assets/" + name
        if path not in seen:
            seen.append(path)
    return seen


def asset_has_account_create_mount(asset: str) -> bool:
    """Content fingerprint of the chunk that carries the account create-mode NewAPI
    field mount, independent of the chunk's filename (rename-proof)."""
    return 'variant:"create"' in asset and '"channel-type-options":' in asset


def find_platform_picker_before(asset: str, before_idx: int) -> int:
    """Locate the NewAPI/Extension Engine platform picker nearest *before* the
    create-mode mount. Minified bundling (post-#935 the modals chunk also carries
    the shared field component + edit/bulk modals) scatters the "Extension Engine"
    label far from the picker, so we anchor on the picker markers closest to the
    create mount rather than the globally-first occurrence."""
    markers = ['text-cyan-600', 'name:"server"', "Extension Engine"]
    positions = [pos for marker in markers if (pos := asset.rfind(marker, 0, before_idx)) >= 0]
    return max(positions) if positions else -1


def check_account_asset(asset: str, source: str) -> list[str]:
    errors: list[str] = []
    create_mount_idx = asset.find("variant:\"create\"")
    if create_mount_idx < 0:
        errors.append(f"{source}: missing create-mode Extension Engine field mount (no variant:\"create\")")
        return errors

    platform_idx = find_platform_picker_before(asset, create_mount_idx)
    if platform_idx < 0:
        errors.append(f"{source}: missing Extension Engine/newapi platform picker before create-mode field mount")
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


REQUIRED_NEWAPI_LABELS = [
    "newApiPlatform.channelType",
    "newApiPlatform.baseUrl",
    "newApiPlatform.apiKey",
]


def check_newapi_labels(asset: str, source: str) -> list[str]:
    label_positions = {label: asset.find(label) for label in REQUIRED_NEWAPI_LABELS}
    if not all(idx >= 0 for idx in label_positions.values()):
        return []

    ordered_labels = [label_positions[label] for label in REQUIRED_NEWAPI_LABELS]
    if ordered_labels != sorted(ordered_labels):
        return [f"{source}: shared NewAPI channel/base-url/api-key labels are out of order"]
    return []


def check_dist(dist: Path) -> list[str]:
    # Rename-proof: scan every JS chunk and validate the one(s) that carry the
    # account create-mode NewAPI field mount, identified by content rather than by
    # a hard-coded chunk filename. This is what keeps the release gate from
    # breaking again the next time frontend/vite.config.ts reshuffles manualChunks.
    errors: list[str] = []
    checked = 0
    labels_checked = 0
    all_js_assets = [(path, read_file(path)) for path in sorted((dist / "assets").glob("*.js"))]
    for path, asset in all_js_assets:
        if not asset_has_account_create_mount(asset):
            continue
        checked += 1
        errors.extend(check_account_asset(asset, str(path)))

    for path, asset in all_js_assets:
        if not all(label in asset for label in REQUIRED_NEWAPI_LABELS):
            continue
        labels_checked += 1
        errors.extend(check_newapi_labels(asset, str(path)))

    if checked == 0:
        errors.extend([
            f"{dist}: no JS chunk carries the admin account create-mode Extension Engine "
            f"field mount (variant:\"create\" + channel-type-options)"
        ])
    if labels_checked == 0:
        errors.append(
            f"{dist}: no JS chunk carries the shared NewAPI field labels: {', '.join(REQUIRED_NEWAPI_LABELS)}"
        )
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
    account_asset = ""
    account_asset_path = ""
    for asset_path in assets:
        asset_url = urljoin(base, asset_path.lstrip("/"))
        asset = read_url(asset_url)
        if not asset_has_account_create_mount(asset):
            continue
        account_asset = asset
        account_asset_path = asset_path
        errors.extend(check_account_asset(asset, asset_url))
        break

    if not account_asset:
        errors.append(f"{base}: referenced account-modal assets do not contain the Extension Engine/newapi create-mode field mount")
        return errors

    label_errors = check_newapi_labels(account_asset, urljoin(base, account_asset_path.lstrip("/")))
    if not label_errors and all(label in account_asset for label in REQUIRED_NEWAPI_LABELS):
        return errors

    for dep_path in js_asset_paths_from_vite_asset(account_asset):
        dep_url = urljoin(base, dep_path.lstrip("/"))
        dep_asset = read_url(dep_url)
        if not all(label in dep_asset for label in REQUIRED_NEWAPI_LABELS):
            continue
        errors.extend(check_newapi_labels(dep_asset, dep_url))
        return errors

    errors.append(f"{base}: no account-modal dependency carries the shared NewAPI field labels: {', '.join(REQUIRED_NEWAPI_LABELS)}")
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
