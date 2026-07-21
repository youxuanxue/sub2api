#!/usr/bin/env python3
from __future__ import annotations

import importlib.util
import json
import sys
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
MODULE_PATH = HERE / "import-accounts.py"


def load_module():
    spec = importlib.util.spec_from_file_location("tk_import_accounts", MODULE_PATH)
    assert spec and spec.loader
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


MOD = load_module()


class ImportAccountsTest(unittest.TestCase):
    def test_detect_antigravity_export(self) -> None:
        doc = {
            "type": "antigravity",
            "access_token": "a",
            "project_id": "p",
            "email": "e@example.com",
        }
        self.assertEqual(MOD.detect_route(doc), "antigravity_oauth")

    def test_detect_codex_session(self) -> None:
        doc = {"accessToken": "token", "email": "u@example.com"}
        self.assertEqual(MOD.detect_route(doc), "codex_session")

    def test_detect_create_account(self) -> None:
        doc = {
            "name": "x",
            "platform": "anthropic",
            "type": "apikey",
            "credentials": {"api_key": "k"},
        }
        self.assertEqual(MOD.detect_route(doc), "create_account")

    def test_detect_grok_sso(self) -> None:
        doc = {"import_profile": "grok_sso", "sso_tokens": ["t1"]}
        self.assertEqual(MOD.detect_route(doc), "grok_sso")

    def test_detect_edge_oauth_relay(self) -> None:
        doc = {
            "import_profile": "edge_oauth_relay",
            "edge_id": "us6",
            "pool_platform": "antigravity",
            "edge_oauth": {"platform": "antigravity", "type": "oauth", "name": "x"},
        }
        self.assertEqual(MOD.detect_route(doc), "edge_oauth_relay")

    def test_validate_newapi_requires_channel_type(self) -> None:
        doc = {
            "name": "n",
            "platform": "newapi",
            "type": "apikey",
            "credentials": {"api_key": "k", "base_url": "https://x"},
        }
        with self.assertRaises(SystemExit):
            MOD.validate_create_account(doc)

    def test_validate_kiro_requires_tos(self) -> None:
        doc = {
            "name": "n",
            "platform": "kiro",
            "type": "oauth",
            "credentials": {
                "access_token": "a",
                "refresh_token": "r",
                "region": "us-east-1",
                "auth_method": "social",
            },
        }
        with self.assertRaises(SystemExit):
            MOD.validate_create_account(doc)

    def test_build_antigravity_payload_wraps_content(self) -> None:
        doc = {"type": "antigravity", "access_token": "a", "project_id": "p", "name": "ops"}
        payload = MOD.build_antigravity_import_payload(doc)
        self.assertIn("content", payload)
        wrapped = json.loads(payload["content"])
        self.assertEqual(wrapped["project_id"], "p")

    def test_antigravity_export_to_create_spec(self) -> None:
        doc = {
            "type": "antigravity",
            "access_token": "access-1",
            "refresh_token": "refresh-1",
            "email": "user@example.com",
            "project_id": "proj-1",
            "expired": "2099-07-21T03:08:39Z",
        }
        spec = MOD.antigravity_export_to_create_spec(doc)
        self.assertEqual(spec["platform"], "antigravity")
        self.assertEqual(spec["type"], "oauth")
        self.assertEqual(spec["credentials"]["project_id"], "proj-1")
        self.assertEqual(spec["credentials"]["access_token"], "access-1")
        self.assertIn("expires_at", spec["credentials"])

    def test_examples_validate(self) -> None:
        examples = sorted((HERE / "examples").glob("*.json"))
        self.assertGreaterEqual(len(examples), 10)
        for path in examples:
            doc = MOD.load_json_file(path)
            route = MOD.detect_route(doc)
            self.assertIn(
                route,
                {
                    "create_account",
                    "antigravity_oauth",
                    "codex_session",
                    "grok_sso",
                    "batch_bundle",
                    "list",
                    "edge_oauth_relay",
                },
                msg=f"{path.name} -> {route}",
            )
            if route == "create_account":
                MOD.validate_create_account(doc, path_hint=path.name)
            elif route == "edge_oauth_relay":
                MOD.validate_edge_oauth_relay(doc, path_hint=path.name)
            elif route == "batch_bundle":
                for index, item in enumerate(doc.get("accounts") or [], start=1):
                    item_route = MOD.detect_route(item)
                    if item_route == "create_account":
                        MOD.validate_create_account(
                            MOD._merge_bundle_defaults(doc, item),
                            path_hint=f"{path.name}[{index}]",
                        )


if __name__ == "__main__":
    raise SystemExit(unittest.main())
