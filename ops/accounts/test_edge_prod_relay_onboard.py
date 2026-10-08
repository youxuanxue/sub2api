#!/usr/bin/env python3
"""Unit tests for edge→prod relay onboard profiles/planning helpers."""

from __future__ import annotations

import os
import unittest
from unittest import mock

import edge_prod_relay_onboard as mod
import relay_onboard_profiles as profiles


class ProfileTests(unittest.TestCase):
    def test_antigravity_implemented(self) -> None:
        p = profiles.require_implemented("antigravity")
        self.assertEqual(p["pool_platform"], "antigravity")
        self.assertEqual(
            profiles.format_name(p["prod_stub_name"], edge_id="uk3"),
            "antigravity-uk3",
        )
        self.assertEqual(
            profiles.format_name(p["relay_key_name"], edge_id="uk3"),
            "relay-antigravity-uk3",
        )
        self.assertEqual(p["probe"]["endpoint"], "gemini")
        self.assertIn("gemini_generate_content", p["edge_capability"]["required_protocols"])

    def test_placeholders_reject_apply(self) -> None:
        for platform in ("gemini-web", "kiro", "openai", "anthropic"):
            with self.assertRaises(ValueError) as ctx:
                profiles.require_implemented(platform)
            self.assertIn("placeholder", str(ctx.exception))

    def test_list_platforms_includes_status(self) -> None:
        rows = {r["platform"]: r for r in profiles.list_platforms()}
        self.assertEqual(rows["antigravity"]["status"], "implemented")
        self.assertEqual(rows["kiro"]["status"], "placeholder")


class PlanTests(unittest.TestCase):
    def test_build_plan_marks_existing_stub_skip(self) -> None:
        fake_groups = [
            {"id": 16, "name": "Google-Vertex"},
            {"id": 21, "name": "Google-Antigravity"},
        ]
        parity_account = {
            "id": 85,
            "name": "antigravity-us6",
            "credentials": {
                "model_mapping": {"gemini-3-flash": "gemini-3.8-flash-high"},
                "base_url": "https://api-us6.tokenkey.dev",
            },
            "extra": {
                "mixed_scheduling": True,
                "supported_protocols": ["gemini_generate_content"],
            },
        }
        existing_stub = {
            "id": 215,
            "name": "antigravity-uk1",
            "platform": "antigravity",
            "type": "apikey",
            "credentials": {
                "api_key": "sk-x",
                "base_url": "https://api-uk1.tokenkey.dev",
                "pool_mode": True,
            },
        }

        with (
            mock.patch.object(mod, "list_deployable_edges", return_value=[{"edge_id": "uk1"}]),
            mock.patch.object(mod, "list_groups", return_value=fake_groups),
            mock.patch.object(
                mod,
                "find_account_by_name",
                return_value={"id": 85, "name": "antigravity-us6"},
            ),
            mock.patch.object(
                mod,
                "http_data",
                return_value=parity_account,
            ),
            mock.patch.object(
                mod,
                "list_admin_accounts",
                return_value=[existing_stub],
            ),
        ):
            plan = mod.build_plan(
                platform="antigravity",
                edge_ids=["uk1"],
                prod_base="https://api.tokenkey.dev",
                prod_key="admin-test",
            )
        self.assertEqual(plan["prod_group_ids"], [16, 21])
        self.assertEqual(plan["edges"][0]["prod_action"], "skip")
        self.assertEqual(plan["edges"][0]["prod_stub_name"], "antigravity-uk1")
        self.assertEqual(plan["prod_parity"]["mapping_n"], 1)

    def test_unknown_edge_rejected(self) -> None:
        with mock.patch.object(mod, "list_deployable_edges", return_value=[{"edge_id": "uk1"}]):
            with self.assertRaises(SystemExit):
                mod.build_plan(
                    platform="antigravity",
                    edge_ids=["nope"],
                    prod_base="https://api.tokenkey.dev",
                    prod_key="admin-test",
                )


class CapabilitySQLTests(unittest.TestCase):
    def test_sql_uses_profile_platform_and_filters_deleted(self) -> None:
        profile = profiles.require_implemented("antigravity")
        sql, platform = mod.build_edge_capability_sql(profile)
        self.assertEqual(platform, "antigravity")
        self.assertIn("a.platform = 'antigravity'", sql)
        self.assertIn("a.deleted_at IS NULL", sql)
        self.assertIn("$proto$", sql)
        self.assertIn("$evid$", sql)
        self.assertIn("gemini_generate_content", sql)
        self.assertNotIn("'[\"gemini_generate_content\"]'", sql)

    def test_sql_rejects_unsafe_platform(self) -> None:
        profile = profiles.require_implemented("antigravity")
        profile["pool_platform"] = "antigravity'; DROP TABLE accounts;--"
        with self.assertRaises(ValueError):
            mod.build_edge_capability_sql(profile)


class EdgeKeyTests(unittest.TestCase):
    def test_multi_edge_rejects_shared_env_key(self) -> None:
        args = mock.Mock(
            edge_admin_key="",
            fetch_edge_admin_key=False,
        )
        with mock.patch.dict(os.environ, {"TOKENKEY_EDGE_ADMIN_API_KEY": "admin-x"}):
            with self.assertRaises(SystemExit):
                mod.resolve_edge_key("uk1", args, multi_edge=True)

    def test_relay_key_group_mismatch_rejected(self) -> None:
        profile = profiles.require_implemented("antigravity")
        with mock.patch.object(
            mod,
            "list_admin_user_api_keys",
            return_value=[
                {
                    "id": 9,
                    "name": "relay-antigravity-uk1",
                    "key": "sk-real",
                    "group_id": 99,
                }
            ],
        ):
            with self.assertRaises(SystemExit):
                mod.ensure_edge_relay_key(
                    "https://api-uk1.tokenkey.dev",
                    "admin-x",
                    profile,
                    edge_id="uk1",
                    group_id=1,
                    dry_run=False,
                )


class StubSyncTests(unittest.TestCase):
    def test_sync_prod_stub_api_key_when_stale(self) -> None:
        existing = {"id": 215, "name": "antigravity-uk1"}
        with mock.patch.object(
            mod,
            "http_data",
            side_effect=[
                {"id": 215, "credentials": {"api_key": "sk-old", "pool_mode": True}},
                {"id": 215},
            ],
        ) as http:
            out = mod.sync_prod_stub_api_key(
                "https://api.tokenkey.dev",
                "admin-p",
                existing,
                edge_api_key="sk-new",
                dry_run=False,
            )
        self.assertEqual(out["action"], "synced_api_key")
        self.assertEqual(http.call_args_list[1].kwargs["method"], "PUT")
        self.assertEqual(
            http.call_args_list[1].kwargs["payload"]["credentials"]["api_key"],
            "sk-new",
        )

    def test_usage_matches_stub(self) -> None:
        self.assertTrue(mod.usage_matches_stub(215, 215))
        self.assertTrue(mod.usage_matches_stub("215", 215))
        self.assertFalse(mod.usage_matches_stub(216, 215))
        self.assertFalse(mod.usage_matches_stub(None, 215))
        self.assertFalse(mod.usage_matches_stub("x", 215))


if __name__ == "__main__":
    unittest.main()
