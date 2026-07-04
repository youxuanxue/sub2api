#!/usr/bin/env python3
"""Unit tests for scripts/release_main_push_route.py."""
from __future__ import annotations

import importlib.util
import pathlib
import unittest

_MODULE = pathlib.Path(__file__).resolve().parent / "release_main_push_route.py"
_spec = importlib.util.spec_from_file_location("release_main_push_route", _MODULE)
assert _spec and _spec.loader
_mod = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_mod)
decide_route = _mod.decide_route


class ReleaseMainPushRouteTest(unittest.TestCase):
    def test_org_bypass_user_direct_push(self) -> None:
        prot = {
            "required_pull_request_reviews": {
                "bypass_pull_request_allowances": {
                    "users": [{"login": "alice"}],
                },
            },
        }
        meta = {"owner_type": "Organization", "admin": False}
        self.assertEqual(decide_route(prot, meta, "alice"), "direct-push")

    def test_org_non_bypass_user_pr_path(self) -> None:
        prot = {
            "required_pull_request_reviews": {
                "bypass_pull_request_allowances": {
                    "users": [{"login": "alice"}],
                },
            },
        }
        meta = {"owner_type": "Organization", "admin": True}
        self.assertEqual(decide_route(prot, meta, "bob"), "bump-via-pr")

    def test_personal_admin_enforce_admins_off(self) -> None:
        prot = {"enforce_admins": {"enabled": False}}
        meta = {"owner_type": "User", "admin": True}
        self.assertEqual(decide_route(prot, meta, "owner"), "direct-push")

    def test_personal_admin_enforce_admins_on(self) -> None:
        prot = {"enforce_admins": {"enabled": True}}
        meta = {"owner_type": "User", "admin": True}
        self.assertEqual(decide_route(prot, meta, "owner"), "bump-via-pr")

    def test_personal_non_admin(self) -> None:
        prot = {"enforce_admins": {"enabled": False}}
        meta = {"owner_type": "User", "admin": False}
        self.assertEqual(decide_route(prot, meta, "collab"), "bump-via-pr")


if __name__ == "__main__":
    unittest.main()
