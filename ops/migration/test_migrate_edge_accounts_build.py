#!/usr/bin/env python3
"""Unit tests for migrate-edge-accounts build (full-clone flags, no live SSM)."""
from __future__ import annotations

import json
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

REPO = Path(__file__).resolve().parents[2]
import importlib.util

_spec = importlib.util.spec_from_file_location(
    "migrate_edge_accounts",
    REPO / "ops" / "migration" / "migrate-edge-accounts.py",
)
mig = importlib.util.module_from_spec(_spec)
assert _spec.loader is not None
_spec.loader.exec_module(mig)


def _minimal_payload(*, with_keys: bool = False) -> dict:
    schema = {
        "accounts": [
            {"column_name": "name", "data_type": "character varying"},
            {"column_name": "platform", "data_type": "character varying"},
            {"column_name": "type", "data_type": "character varying"},
            {"column_name": "status", "data_type": "character varying"},
            {"column_name": "schedulable", "data_type": "boolean"},
            {"column_name": "credentials", "data_type": "jsonb"},
            {"column_name": "extra", "data_type": "jsonb"},
            {"column_name": "concurrency", "data_type": "integer"},
            {"column_name": "priority", "data_type": "integer"},
            {"column_name": "created_at", "data_type": "timestamp with time zone"},
            {"column_name": "updated_at", "data_type": "timestamp with time zone"},
        ],
        "groups": [
            {"column_name": "name", "data_type": "character varying"},
            {"column_name": "platform", "data_type": "character varying"},
            {"column_name": "status", "data_type": "character varying"},
            {"column_name": "rate_multiplier", "data_type": "numeric"},
            {"column_name": "is_exclusive", "data_type": "boolean"},
            {"column_name": "subscription_type", "data_type": "character varying"},
            {"column_name": "default_validity_days", "data_type": "integer"},
            {"column_name": "claude_code_only", "data_type": "boolean"},
            {"column_name": "model_routing_enabled", "data_type": "boolean"},
            {"column_name": "created_at", "data_type": "timestamp with time zone"},
            {"column_name": "updated_at", "data_type": "timestamp with time zone"},
        ],
    }
    if with_keys:
        schema["api_keys"] = [
            {"column_name": "user_id", "data_type": "bigint"},
            {"column_name": "key", "data_type": "character varying"},
            {"column_name": "name", "data_type": "character varying"},
            {"column_name": "group_id", "data_type": "bigint"},
            {"column_name": "status", "data_type": "character varying"},
            {"column_name": "quota", "data_type": "numeric"},
            {"column_name": "quota_used", "data_type": "numeric"},
            {"column_name": "rate_limit_5h", "data_type": "numeric"},
            {"column_name": "rate_limit_1d", "data_type": "numeric"},
            {"column_name": "rate_limit_7d", "data_type": "numeric"},
            {"column_name": "usage_5h", "data_type": "numeric"},
            {"column_name": "usage_1d", "data_type": "numeric"},
            {"column_name": "usage_7d", "data_type": "numeric"},
            {"column_name": "routing_mode", "data_type": "character varying"},
            {"column_name": "created_at", "data_type": "timestamp with time zone"},
            {"column_name": "updated_at", "data_type": "timestamp with time zone"},
        ]
    schema["protocol_endpoint_capabilities"] = [
        {"column_name": "capability_key", "data_type": "character varying"},
        {"column_name": "identity", "data_type": "jsonb"},
        {"column_name": "supported_protocols", "data_type": "jsonb"},
        {"column_name": "probe_evidence", "data_type": "jsonb"},
        {"column_name": "revision", "data_type": "integer"},
        {"column_name": "identity_conflict", "data_type": "boolean"},
    ]
    payload = {
        "schema": schema,
        "groups": [
            {
                "id": 6,
                "name": "kiro",
                "platform": "kiro",
                "status": "active",
                "rate_multiplier": 1,
                "is_exclusive": False,
                "subscription_type": "",
                "default_validity_days": 0,
                "claude_code_only": False,
                "model_routing_enabled": False,
            }
        ],
        "accounts": [
            {
                "id": 16,
                "name": "kiro-a",
                "platform": "kiro",
                "type": "oauth",
                "status": "active",
                "schedulable": True,
                "credentials": {"access_token": "SECRET"},
                "extra": {},
                "concurrency": 1,
                "priority": 1,
            },
            {
                "id": 5,
                "name": "gpt-34",
                "platform": "openai",
                "type": "oauth",
                "status": "active",
                "schedulable": True,
                "credentials": {"access_token": "SECRET2"},
                "extra": {},
                "concurrency": 1,
                "priority": 1,
                "protocol_endpoint_capability_id": 2,
            },
        ],
        "bindings": [
            {"account_id": 16, "group_id": 6, "priority": 1},
        ],
        "protocol_endpoint_capabilities": [
            {
                "id": 2,
                "capability_key": "openai-pec-key",
                "identity": {"platform": "openai", "endpoint_profile": "openai_codex_official"},
                "supported_protocols": ["responses"],
                "probe_evidence": {"official_seed": True},
                "revision": 2,
                "identity_conflict": False,
            }
        ],
        "pec_links": [
            {"account_name": "gpt-34", "capability_key": "openai-pec-key"},
        ],
    }
    if with_keys:
        payload["api_keys"] = [
            {
                "id": 5,
                "user_id": 1,
                "key": "sk-secret-do-not-print",
                "name": "prod-kiro-key",
                "group_id": 6,
                "status": "active",
                "quota": 0,
                "quota_used": 9,
                "rate_limit_5h": 0,
                "rate_limit_1d": 0,
                "rate_limit_7d": 0,
                "usage_5h": 1,
                "usage_1d": 1,
                "usage_7d": 1,
                "routing_mode": "group",
            }
        ]
    return payload


class BuildFullCloneTests(unittest.TestCase):
    def test_preserve_schedulable_and_api_keys_sql(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            state = Path(tmp)
            payload_path = state / "payload.json"
            payload_path.write_text(json.dumps(_minimal_payload(with_keys=True)))
            with mock.patch.object(mig, "STATE_DIR", state):
                args = mock.Mock(
                    rename=[],
                    rename_group=[],
                    preserve_schedulable=True,
                    replace_target=True,
                )
                mig.cmd_build(args)
                sql = (state / "migrate.sql").read_text()
        self.assertIn("UPDATE api_keys SET deleted_at=now()", sql)
        self.assertIn("UPDATE accounts SET deleted_at=now()", sql)
        self.assertIn("INSERT INTO api_keys", sql)
        self.assertIn("admin_uid", sql)
        # schedulable taken from jsonb source, not forced false
        self.assertIn("(v->>'schedulable')::boolean", sql)
        # secret must live only inside jsonb assignment, never as a bare SQL string
        self.assertNotIn("'sk-secret-do-not-print'", sql)
        self.assertIn("verify_key", sql)
        self.assertIn("INSERT INTO protocol_endpoint_capabilities", sql)
        self.assertIn("ON CONFLICT (capability_key) DO UPDATE", sql)
        self.assertIn("protocol_endpoint_capability_id =", sql)
        self.assertIn("openai-pec-key", sql)
        self.assertIn("gpt-34", sql)

    def test_summary_omits_key_material(self) -> None:
        payload = _minimal_payload(with_keys=True)
        # Capture print summary path used after extract
        import io
        from contextlib import redirect_stdout

        buf = io.StringIO()
        with redirect_stdout(buf):
            mig._print_payload_summary(payload)
        out = buf.getvalue()
        self.assertIn("prod-kiro-key", out)
        self.assertNotIn("sk-secret-do-not-print", out)
        self.assertNotIn("SECRET", out)


if __name__ == "__main__":
    unittest.main()
