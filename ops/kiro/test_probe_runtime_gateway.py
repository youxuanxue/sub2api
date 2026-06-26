#!/usr/bin/env python3
"""Unit tests for probe_runtime_gateway.py (stdlib unittest).

  python3 ops/kiro/test_probe_runtime_gateway.py
"""
from __future__ import annotations

import json
import sys
import tempfile
import time
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import probe_runtime_gateway as probe  # noqa: E402


class TokenLoadTests(unittest.TestCase):
    def test_load_local_token_reads_access_token(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "kiro-auth-token.json"
            path.write_text(
                json.dumps(
                    {
                        "accessToken": "access-secret-token",
                        "refreshToken": "refresh",
                        "region": "us-east-1",
                        "profileArn": "arn:aws:codewhisperer:us-east-1:123:profile/abc",
                    }
                ),
                encoding="utf-8",
            )
            got = probe.load_local_token(path)
            self.assertEqual(got["access_token"], "access-secret-token")
            self.assertEqual(got["profile_arn"], "arn:aws:codewhisperer:us-east-1:123:profile/abc")

    def test_load_local_token_missing_file_raises(self):
        with self.assertRaises(probe.ProbeEnvError):
            probe.load_local_token(Path("/tmp/does-not-exist-kiro-token.json"))

    def test_load_local_token_missing_access_token_raises(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "kiro-auth-token.json"
            path.write_text(json.dumps({"refreshToken": "x"}), encoding="utf-8")
            with self.assertRaises(probe.ProbeEnvError):
                probe.load_local_token(path)


class HeaderTests(unittest.TestCase):
    def test_ide_headers_include_bearer_and_target(self):
        headers = probe.build_headers(
            style="ide",
            host="runtime.us-east-1.kiro.dev",
            bearer_token="tok",
            content_type="application/x-amz-json-1.0",
            extra={"X-Amz-Target": probe.X_AMZ_TARGET_RUNTIME_USAGE},
            machine_id="abc123",
        )
        self.assertTrue(headers["Authorization"].endswith("tok"))
        self.assertEqual(headers["Host"], "runtime.us-east-1.kiro.dev")
        self.assertIn("KiroIDE", headers["User-Agent"])
        self.assertIn("abc123", headers["User-Agent"])
        self.assertEqual(headers["X-Amz-Target"], probe.X_AMZ_TARGET_RUNTIME_USAGE)

    def test_tokenkey_headers_include_amz_user_agent(self):
        headers = probe.build_headers(
            style="tokenkey",
            host="runtime.us-east-1.kiro.dev",
            bearer_token="tok",
            content_type="application/json",
        )
        self.assertIn("aws-sdk-js/", headers["User-Agent"])
        self.assertIn("x-amz-user-agent", headers)
        self.assertEqual(headers["x-amzn-codewhisperer-optout"], "true")


class ProbeSpecTests(unittest.TestCase):
    TOKEN = {
        "access_token": "access-token-value",
        "profile_arn": "arn:aws:codewhisperer:us-east-1:1:profile/x",
    }

    def test_legacy_q_usage_posts_to_q_root(self):
        spec = probe.build_legacy_q_usage_spec(
            token=self.TOKEN, style="ide", machine_id="m1"
        )
        self.assertEqual(spec.method, "POST")
        self.assertEqual(spec.url, "https://q.us-east-1.amazonaws.com/")
        body = json.loads(spec.body.decode("utf-8"))
        self.assertEqual(body["origin"], "AI_EDITOR")
        self.assertEqual(body["profileArn"], self.TOKEN["profile_arn"])

    def test_runtime_chat_targets_generate_assistant_response(self):
        spec = probe.build_runtime_chat_spec(
            token=self.TOKEN,
            style="ide",
            machine_id="m1",
            message="hello",
            model_id="claude-sonnet-4.5",
        )
        self.assertIn("/generateAssistantResponse", spec.url)
        self.assertEqual(
            spec.headers["X-Amz-Target"], probe.X_AMZ_TARGET_STREAMING_CHAT
        )
        body = json.loads(spec.body.decode("utf-8"))
        self.assertEqual(
            body["conversationState"]["currentMessage"]["userInputMessage"]["content"],
            "hello",
        )

    def test_management_usage_is_get_with_query(self):
        spec = probe.build_management_usage_spec(
            token=self.TOKEN, style="ide", machine_id="m1"
        )
        self.assertEqual(spec.method, "GET")
        self.assertIn("management.us-east-1.kiro.dev", spec.url)
        self.assertIn("origin=AI_EDITOR", spec.url)
        self.assertIn("profileArn=", spec.url)


class RedactionTests(unittest.TestCase):
    def test_redact_headers_hides_bearer_token(self):
        redacted = probe.redact_headers({"Authorization": "Bearer super-secret-access"})
        self.assertNotIn("super-secret-access", redacted["Authorization"])
        self.assertTrue(redacted["Authorization"].startswith("Bearer "))


class ParseApiTests(unittest.TestCase):
    def test_parse_profile_arns_extracts_arn_list(self):
        payload = {
            "profiles": [
                {"arn": "arn:aws:codewhisperer:us-east-1:1:profile/A"},
                {"arn": ""},
                {"profileName": "missing-arn"},
            ]
        }
        self.assertEqual(
            probe.parse_profile_arns(payload),
            ["arn:aws:codewhisperer:us-east-1:1:profile/A"],
        )

    def test_parse_model_ids_extracts_model_id_list(self):
        payload = {
            "models": [
                {"modelId": "qwen3-coder-next", "modelName": "Qwen"},
                {"modelName": "no-id"},
            ]
        }
        self.assertEqual(probe.parse_model_ids(payload), ["qwen3-coder-next"])


class RefreshTokenTests(unittest.TestCase):
    def test_latest_idc_registration_picks_newest_file(self):
        with tempfile.TemporaryDirectory() as tmp:
            cache = Path(tmp)
            old = cache / "old.json"
            new = cache / "new.json"
            old.write_text(
                json.dumps({"clientId": "old-id", "clientSecret": "old-secret"}),
                encoding="utf-8",
            )
            new.write_text(
                json.dumps({"clientId": "new-id", "clientSecret": "new-secret"}),
                encoding="utf-8",
            )
            old.touch()
            time.sleep(0.01)
            new.touch()
            got = probe.latest_idc_registration(cache)
            self.assertIsNotNone(got)
            assert got is not None
            self.assertEqual(got["client_id"], "new-id")

if __name__ == "__main__":
    unittest.main()
