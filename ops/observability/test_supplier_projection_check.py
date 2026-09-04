#!/usr/bin/env python3
from __future__ import annotations

import hashlib
import hmac
import importlib.util
import sys
import unittest
from pathlib import Path


HERE = Path(__file__).resolve().parent
MODULE_PATH = HERE / "supplier_projection_check.py"
SPEC = importlib.util.spec_from_file_location("supplier_projection_check", MODULE_PATH)
assert SPEC and SPEC.loader
MOD = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = MOD
SPEC.loader.exec_module(MOD)


class SupplierProjectionCheckTest(unittest.TestCase):
    key = "test-fingerprint-key"

    def fingerprint(self, credential: str) -> str:
        return "hmac-sha256:" + hmac.new(
            self.key.encode(), credential.encode(), hashlib.sha256
        ).hexdigest()

    def source(self, *, channel_type: int = 1) -> dict:
        return {
            "id": 9,
            "supplier_name": "cloudwise",
            "supplier_lane": "default",
            "channel_type": channel_type,
            "endpoint": "https://supplier.example/v1/",
            "credential_fingerprint": self.fingerprint("secret"),
            "base_priority": 200,
            "account_concurrency": 1000,
            "models": [
                {
                    "client_model_id": "model",
                    "upstream_model_id": "upstream-model",
                    "purchase_ratio": 0.3,
                }
            ],
        }

    def account(self, *, channel_type: int = 1) -> dict:
        protocol = "chat_completions"
        return {
            "id": 126,
            "name": "cloudwise/default · 档位 2",
            "platform": "newapi",
            "type": "apikey",
            "channel_type": channel_type,
            "credentials": {
                "api_key": "secret",
                "base_url": "https://supplier.example/v1",
                "model_mapping": {"model": "upstream-model"},
                "protocol_endpoints_exclusive": True,
                "api_base_urls": {protocol: "https://supplier.example/v1"},
            },
            "extra": {"supplier_source_id": 9, "supplier_discount_band": 2},
            "priority": 220,
            "concurrency": 1000,
            "status": "error",
            "schedulable": False,
            "capability_id": 7,
            "capability_key": "capability",
            "capability_identity": {
                "platform": "newapi",
                "endpoint_profile": "custom_api_key",
                "channel_type": str(channel_type),
                "protocol_endpoints": {
                    protocol: {
                        "url": "https://supplier.example/v1/chat/completions"
                    }
                },
                "upstream_request_profile": "openai_json_v1",
                "routing_headers": {},
            },
            "supported_protocols": [protocol],
            "probe_evidence": {"initial_probe_completed": True},
            "capability_identity_conflict": False,
        }

    def test_runtime_scheduling_state_does_not_create_drift(self) -> None:
        report = MOD.evaluate_snapshot(
            {"sources": [self.source()], "accounts": [self.account()]}, self.key
        )

        self.assertEqual(report["verdict"], "aligned")
        self.assertEqual(report["ignored_runtime_fields"], ["status", "schedulable"])

    def test_structural_and_credential_drift_is_field_named_and_redacted(self) -> None:
        account = self.account()
        account["priority"] = 999
        account["credentials"]["api_key"] = "wrong-secret"

        report = MOD.evaluate_snapshot(
            {"sources": [self.source()], "accounts": [account]}, self.key
        )

        self.assertEqual(report["verdict"], "drift")
        differences = report["sources"][0]["accounts"][0]["differences"]
        self.assertIn("credential", differences)
        self.assertIn("priority", differences)
        self.assertNotIn("wrong-secret", str(report))

    def test_media_only_projection_needs_no_text_capability(self) -> None:
        source = self.source(channel_type=54)
        account = self.account(channel_type=54)
        account["capability_id"] = None
        account["capability_key"] = None
        account["capability_identity"] = None
        account["supported_protocols"] = []
        account["probe_evidence"] = None

        report = MOD.evaluate_snapshot({"sources": [source], "accounts": [account]}, self.key)

        self.assertEqual(report["verdict"], "aligned")

    def test_media_only_stale_text_capability_is_drift(self) -> None:
        source = self.source(channel_type=54)
        account = self.account(channel_type=54)

        report = MOD.evaluate_snapshot({"sources": [source], "accounts": [account]}, self.key)

        self.assertEqual(report["verdict"], "drift")
        self.assertEqual(
            report["sources"][0]["accounts"][0]["differences"],
            ["protocol_capability_link"],
        )

    def test_missing_band_and_orphan_account_are_reported(self) -> None:
        orphan = self.account()
        orphan["id"] = 999
        orphan["extra"]["supplier_source_id"] = 404

        report = MOD.evaluate_snapshot(
            {"sources": [self.source()], "accounts": [orphan]}, self.key
        )

        self.assertEqual(report["verdict"], "drift")
        self.assertEqual(report["summary"]["orphan_account_count"], 1)
        self.assertEqual(report["sources"][0]["issues"][0]["code"], "missing_account")


if __name__ == "__main__":
    unittest.main()
