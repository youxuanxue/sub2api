#!/usr/bin/env python3
from __future__ import annotations

import unittest

import edge_relay as er


class EdgeRelayTest(unittest.TestCase):
    def test_edge_id_from_base_url(self) -> None:
        self.assertEqual(er.edge_id_from_base_url("https://api-us6.tokenkey.dev"), "us6")
        self.assertEqual(er.edge_id_from_base_url("https://api-us6.tokenkey.dev/"), "us6")
        self.assertIsNone(er.edge_id_from_base_url("https://api.anthropic.com"))

    def test_find_prod_relay_stub_by_edge_and_pool(self) -> None:
        accounts = [
            {
                "id": 1,
                "name": "cc-us4",
                "platform": "anthropic",
                "type": "apikey",
                "credentials": {
                    "base_url": "https://api-us4.tokenkey.dev",
                    "api_key": "tk_a",
                },
            },
            {
                "id": 2,
                "name": "ag-us6",
                "platform": "antigravity",
                "type": "apikey",
                "credentials": {
                    "base_url": "https://api-us6.tokenkey.dev",
                    "api_key": "tk_b",
                },
            },
            {
                "id": 3,
                "name": "kiro-us6",
                "platform": "anthropic",
                "type": "apikey",
                "credentials": {
                    "base_url": "https://api-us6.tokenkey.dev",
                    "api_key": "tk_c",
                    "mirror_platform": "kiro",
                },
            },
        ]
        self.assertIsNone(er.find_prod_relay_stub(accounts, edge_id="us6", pool_platform="anthropic"))
        hit = er.find_prod_relay_stub(accounts, edge_id="us6", pool_platform="antigravity")
        self.assertIsNotNone(hit)
        assert hit is not None
        self.assertEqual(hit["id"], 2)
        kiro = er.find_prod_relay_stub(accounts, edge_id="us6", pool_platform="kiro")
        self.assertIsNotNone(kiro)
        assert kiro is not None
        self.assertEqual(kiro["id"], 3)

    def test_plan_skips_prod_when_stub_exists(self) -> None:
        doc = {
            "import_profile": "edge_oauth_relay",
            "edge_id": "us6",
            "pool_platform": "antigravity",
            "edge_oauth": {
                "platform": "antigravity",
                "type": "oauth",
                "name": "ag-oauth",
                "credentials": {"access_token": "a", "refresh_token": "r", "project_id": "p"},
            },
        }

        def list_prod_accounts() -> list[dict]:
            return [
                {
                    "id": 9,
                    "name": "ag-us6",
                    "platform": "antigravity",
                    "type": "apikey",
                    "credentials": {
                        "base_url": "https://api-us6.tokenkey.dev",
                        "api_key": "tk_existing",
                    },
                }
            ]

        plan = er.plan_edge_oauth_relay(doc, list_prod_accounts=list_prod_accounts)
        self.assertEqual(plan["prod_action"], "skip")
        self.assertIsNone(plan["prod_create"])

    def test_plan_requires_edge_api_key_source_for_new_stub(self) -> None:
        doc = {
            "import_profile": "edge_oauth_relay",
            "edge_id": "us6",
            "pool_platform": "anthropic",
            "edge_oauth": {
                "platform": "anthropic",
                "type": "oauth",
                "name": "edge-oauth",
                "credentials": {"access_token": "a", "refresh_token": "r"},
            },
            "prod_relay": {"group_ids": [1]},
        }
        with self.assertRaises(ValueError):
            er.plan_edge_oauth_relay(doc, list_prod_accounts=list)

    def test_plan_allows_auto_issue_user_id(self) -> None:
        doc = {
            "import_profile": "edge_oauth_relay",
            "edge_id": "us6",
            "pool_platform": "antigravity",
            "edge_oauth": {
                "platform": "antigravity",
                "type": "oauth",
                "name": "ag-oauth",
                "credentials": {"access_token": "a", "refresh_token": "r", "project_id": "p"},
            },
            "prod_relay": {"edge_api_key_user_id": 1, "group_ids": [1]},
        }
        plan = er.plan_edge_oauth_relay(doc, list_prod_accounts=list)
        self.assertEqual(plan["prod_action"], "create")
        self.assertEqual(plan["prod_create"]["credentials"]["api_key"], "tk_PENDING_EDGE_ISSUE")

    def test_build_prod_relay_create_spec_kiro(self) -> None:
        spec = er.build_prod_relay_create_spec(
            edge_id="us6",
            pool_platform="kiro",
            prod_relay={"name": "kiro-us6", "group_ids": [2]},
            edge_api_key="tk_edge",
        )
        self.assertEqual(spec["platform"], "anthropic")
        self.assertEqual(spec["credentials"]["mirror_platform"], "kiro")
        self.assertEqual(spec["credentials"]["base_url"], "https://api-us6.tokenkey.dev")
        self.assertTrue(spec["credentials"]["pool_mode"])


if __name__ == "__main__":
    unittest.main()
