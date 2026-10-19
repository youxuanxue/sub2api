#!/usr/bin/env python3
"""Unit tests for scripts/checks/frontend-release-assets.py lazy-chunk discovery."""
from __future__ import annotations

import importlib.util
import sys
import unittest
from unittest.mock import patch
from pathlib import Path

_MODULE_PATH = Path(__file__).resolve().parent / "checks" / "frontend-release-assets.py"
_spec = importlib.util.spec_from_file_location("frontend_release_assets", _MODULE_PATH)
_mod = importlib.util.module_from_spec(_spec)
assert _spec and _spec.loader
_spec.loader.exec_module(_mod)


class FrontendReleaseAssetsLazyChunkTest(unittest.TestCase):
    def test_url_follows_shared_router_and_still_checks_create_fields(self) -> None:
        base = "https://api.example.dev/"
        mount = ('name:"server" channelType: baseUrl: apiKey: '
                 '"channel-type-options": "channel-types-loading": variant:"create" '
                 'newApiPlatform.channelType newApiPlatform.baseUrl newApiPlatform.apiKey')
        assets = {
            base: '<script src="/assets/index-main.js"></script>',
            base + 'assets/index-main.js': 'import{router}from"./index-router.js";',
            base + 'assets/index-router.js': 'deps=["assets/AccountsView-1.js"]',
            base + 'assets/AccountsView-1.js': 'deps=["assets/admin-account-modals-1.js"]',
            base + 'assets/admin-account-modals-1.js': mount,
        }
        with patch.object(_mod, 'read_url', side_effect=assets.__getitem__):
            self.assertEqual(_mod.check_url(base), [])
            assets[base + 'assets/admin-account-modals-1.js'] = mount.replace('apiKey:', '')
            errors = _mod.check_url(base)
            self.assertTrue(any('missing props: apiKey:' in error for error in errors), errors)
            assets[base + 'assets/admin-account-modals-1.js'] = 'no account create UI'
            errors = _mod.check_url(base)
            self.assertTrue(any('do not contain' in error for error in errors), errors)

    def test_find_create_mount_follows_lazy_import_from_accounts_view(self) -> None:
        accounts_view = (
            'm=__vite__mapDeps,d=(m.f||(m.f=["assets/CreateAccountModal-abc123.js","assets/vendor-vue.js"]))'
        )
        create_modal = (
            'name:"server" channelType: baseUrl: apiKey: '
            '"channel-type-options": "channel-types-loading": variant:"create"'
        )
        assets = {
            "https://api.example.dev/assets/AccountsView-1.js": accounts_view,
            "https://api.example.dev/assets/CreateAccountModal-abc123.js": create_modal,
        }

        def fetch(url: str) -> str:
            return assets[url]

        asset, path, errors = _mod.find_create_mount_asset(
            "https://api.example.dev/",
            ["/assets/AccountsView-1.js"],
            fetch,
        )
        self.assertEqual(errors, [])
        self.assertIn('variant:"create"', asset)
        self.assertEqual(path, "/assets/CreateAccountModal-abc123.js")

    def test_find_create_mount_reports_missing_when_lazy_chain_has_no_mount(self) -> None:
        accounts_view = 'm=__vite__mapDeps,d=(m.f||(m.f=["assets/OtherChunk.js"]))'
        assets = {
            "https://api.example.dev/assets/AccountsView-1.js": accounts_view,
            "https://api.example.dev/assets/OtherChunk.js": "no create mount here",
        }

        def fetch(url: str) -> str:
            return assets[url]

        asset, path, errors = _mod.find_create_mount_asset(
            "https://api.example.dev/",
            ["/assets/AccountsView-1.js"],
            fetch,
        )
        self.assertEqual(asset, "")
        self.assertEqual(path, "")
        self.assertEqual(errors, [])


if __name__ == "__main__":
    unittest.main()
