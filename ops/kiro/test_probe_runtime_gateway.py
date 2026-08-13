#!/usr/bin/env python3
"""Tests for the synthetic, non-evidence Kiro upstream probe."""
from __future__ import annotations

import json
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import probe_runtime_gateway as probe  # noqa: E402

IDENTITY = {
    "user_agent": "aws-sdk-rust/1.3.10 app/AmazonQ-For-CLI",
    "x_amz_user_agent": "aws-sdk-rust/1.3.10 m/F app/AmazonQ-For-CLI",
}
TOKEN = {
    "access_token": "access-secret-token",
    "profile_arn": "arn:aws:codewhisperer:us-east-1:1:profile/private",
}


class TokenAndIdentityTests(unittest.TestCase):
    def test_load_token_reads_but_error_messages_do_not_expose_it(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "auth.json"
            path.write_text(json.dumps({"accessToken": TOKEN["access_token"], "profileArn": TOKEN["profile_arn"]}), encoding="utf-8")
            self.assertEqual(probe.load_local_token(path), TOKEN)

    def test_identity_comes_from_cli_canonical_profile(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "profile.json"
            path.write_text(json.dumps({"name": "tk_canonical_kiro_cli", "observed": IDENTITY}), encoding="utf-8")
            self.assertEqual(probe.load_cli_identity(path), IDENTITY)

    def test_retired_profile_is_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "profile.json"
            path.write_text(json.dumps({"name": "tk_canonical_kiro_ide", "observed": IDENTITY}), encoding="utf-8")
            with self.assertRaises(probe.ProbeEnvError):
                probe.load_cli_identity(path)


class RequestShapeTests(unittest.TestCase):
    def test_headers_use_only_cli_identity(self):
        headers = probe.build_headers(host="runtime.us-east-1.kiro.dev", bearer_token="secret", content_type="application/json", identity=IDENTITY)
        self.assertEqual(headers["User-Agent"], IDENTITY["user_agent"])
        self.assertEqual(headers["x-amz-user-agent"], IDENTITY["x_amz_user_agent"])
        self.assertEqual(headers["x-amzn-codewhisperer-optout"], "false")

    def test_parser_has_no_header_style_or_machine_id_compatibility(self):
        parser = probe.build_parser()
        with self.assertRaises(SystemExit):
            parser.parse_args(["--header-style", "ide"])
        with self.assertRaises(SystemExit):
            parser.parse_args(["--machine-id", "legacy"])

    def test_protocol_specs_remain_synthetic_compatibility_requests(self):
        runtime = probe.build_runtime_chat_spec(token=TOKEN, identity=IDENTITY, message="ping", model_id="auto")
        self.assertEqual(runtime.url, "https://runtime.us-east-1.kiro.dev/generateAssistantResponse")
        self.assertEqual(runtime.headers["X-Amz-Target"], probe.X_AMZ_TARGET_STREAMING_CHAT)
        management = probe.build_management_usage_spec(token=TOKEN, identity=IDENTITY)
        self.assertIn("profileArn=", management.url)
        legacy = probe.build_legacy_q_usage_spec(token=TOKEN, identity=IDENTITY)
        self.assertEqual(legacy.url, "https://q.us-east-1.amazonaws.com/")


class OutputRedactionTests(unittest.TestCase):
    def test_safe_shape_emits_no_profile_arn_body_or_token(self):
        spec = probe.build_runtime_chat_spec(token=TOKEN, identity=IDENTITY, message="private prompt", model_id="private-model")
        encoded = json.dumps(probe.safe_shape(spec))
        for forbidden in (TOKEN["access_token"], TOKEN["profile_arn"], "profileArn", "private prompt", "private-model"):
            self.assertNotIn(forbidden, encoded)
        self.assertIn('"evidence_eligible": false', encoded)

    def test_result_redacts_url_query_auth_and_never_has_response_body(self):
        result = probe.ProbeResult(
            "management", True, 200, "GET",
            f"https://management.us-east-1.kiro.dev/Get?profileArn={TOKEN['profile_arn']}",
            {"Authorization": f"Bearer {TOKEN['access_token']}", "User-Agent": IDENTITY["user_agent"]},
        ).to_dict()
        encoded = json.dumps(result)
        self.assertNotIn(TOKEN["profile_arn"], encoded)
        self.assertNotIn(TOKEN["access_token"], encoded)
        self.assertNotIn("body", result)
        self.assertFalse(result["evidence_eligible"])


class ParseTests(unittest.TestCase):
    def test_parse_profile_arns(self):
        self.assertEqual(probe.parse_profile_arns({"profiles": [{"arn": "arn:a"}, {"profileName": "x"}]}), ["arn:a"])

    def test_parse_model_ids(self):
        self.assertEqual(probe.parse_model_ids({"models": [{"modelId": "m"}, {}]}), ["m"])


if __name__ == "__main__":
    unittest.main()
