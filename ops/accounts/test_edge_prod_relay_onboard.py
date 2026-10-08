#!/usr/bin/env python3
"""Unit tests for edge→prod relay onboard profiles/planning helpers."""

from __future__ import annotations

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


if __name__ == "__main__":
    unittest.main()
