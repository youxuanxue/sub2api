#!/usr/bin/env python3
"""Tests for the source-redacting Kiro CLI mitm addon."""
from __future__ import annotations

import json
import os
import sys
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
import mitm_kiro_http_logger as logger  # noqa: E402


class FakeHeaders:
    def __init__(self, values: dict[str, str]) -> None:
        self.values = values

    def __iter__(self):
        return iter(self.values)

    def get(self, key: str, default: str = "") -> str:
        for candidate, value in self.values.items():
            if candidate.lower() == key.lower():
                return value
        return default


class FakeRequest:
    def __init__(self, *, host: str, path: str = "/", method: str = "POST", headers: dict[str, str] | None = None, body: str = "") -> None:
        self.host = host
        self.path = path
        self.method = method
        self.headers = FakeHeaders(headers or {})
        self.body = body

    def get_text(self, strict: bool = False) -> str:
        return self.body


def flow(*, host: str, path: str = "/", headers: dict[str, str] | None = None, body: str = "", status: int = 200):
    return SimpleNamespace(
        request=FakeRequest(host=host, path=path, headers=headers, body=body),
        response=SimpleNamespace(status_code=status),
    )


class TLSParsingTests(unittest.TestCase):
    def test_parses_only_tls_structure_and_discards_key_bytes(self):
        key_bytes = b"private-key-share-material"
        hello = SimpleNamespace(
            sni="codewhisperer.us-east-1.amazonaws.com",
            cipher_suites=[4866, 4865, 255],
            extensions=[
                (10, b"\x00\x06\x00\x1d\x00\x17\x00\x18"),
                (11, b"\x01\x00"),
                (13, b"\x00\x04\x05\x03\x04\x03"),
                (43, b"\x04\x03\x04\x03\x03"),
                (45, b"\x01\x01"),
                (51, b"\x00" + bytes([len(key_bytes) + 4]) + b"\x00\x1d" + len(key_bytes).to_bytes(2, "big") + key_bytes),
            ],
        )
        record = logger.parse_client_hello(hello)
        self.assertEqual(record["curves"], [29, 23, 24])
        self.assertEqual(record["point_formats"], [0])
        self.assertEqual(record["signature_algorithms"], [1283, 1027])
        self.assertEqual(record["supported_versions"], [772, 771])
        self.assertEqual(record["psk_modes"], [1])
        self.assertEqual(record["key_share_groups"], [29])
        encoded = json.dumps(record)
        self.assertNotIn(key_bytes.hex(), encoded)
        self.assertNotIn("private-key", encoded)

    def test_parses_alpn_when_present(self):
        hello = SimpleNamespace(
            sni="runtime.us-east-1.kiro.dev",
            cipher_suites=[],
            extensions=[(16, b"\x00\x0c\x02h2\x08http/1.1")],
        )
        self.assertEqual(logger.parse_client_hello(hello)["alpn_protocols"], ["h2", "http/1.1"])

    def test_tls_hook_filters_non_kiro_sni(self):
        hello = SimpleNamespace(sni="cognito-identity.us-east-1.amazonaws.com")
        with mock.patch.object(logger, "_append_jsonl") as append:
            logger.tls_clienthello(SimpleNamespace(client_hello=hello))
        append.assert_not_called()


class HTTPRedactionTests(unittest.TestCase):
    def test_filters_unknown_host_and_requires_response(self):
        self.assertIsNone(logger.build_http_record(flow(host="api.anthropic.com")))
        no_response = SimpleNamespace(request=FakeRequest(host="runtime.us-east-1.kiro.dev"), response=None)
        self.assertIsNone(logger.build_http_record(no_response))

    def test_emits_allowlisted_headers_status_and_safe_body_shape(self):
        record = logger.build_http_record(flow(
            host="codewhisperer.us-east-1.amazonaws.com",
            path="/?profileArn=arn:aws:secret",
            headers={
                "Authorization": "Bearer super-secret",
                "Cookie": "session=secret",
                "User-Agent": "aws-sdk-rust/1.3.10 app/AmazonQ-For-CLI",
                "x-amz-user-agent": "aws-sdk-rust/1.3.10 m/F app/AmazonQ-For-CLI",
                "X-Amz-Target": "AmazonCodeWhispererService.GenerateCompletions",
                "Content-Type": "application/x-amz-json-1.0",
                "x-private": "never-log-me",
            },
            body=json.dumps({
                "profileArn": "arn:aws:codewhisperer:us-east-1:1:profile/private",
                "fileContext": {"filename": "private.py", "leftFileContent": "secret source"},
                "maxResults": 1,
            }),
            status=200,
        ))
        assert record is not None
        self.assertEqual(record["path"], "/")
        self.assertEqual(record["status_code"], 200)
        self.assertTrue(record["success"])
        self.assertEqual(record["body_keys"], ["fileContext", "maxResults"])
        encoded = json.dumps(record)
        for forbidden in ("authorization", "cookie", "super-secret", "profileArn", "arn:aws:", "private.py", "secret source", "never-log-me"):
            self.assertNotIn(forbidden, encoded)

    def test_never_emits_response_body(self):
        test_flow = flow(host="runtime.us-east-1.kiro.dev", status=403)
        test_flow.response.content = b"upstream secret response"
        record = logger.build_http_record(test_flow)
        assert record is not None
        self.assertEqual(record["status_code"], 403)
        self.assertFalse(record["success"])
        self.assertNotIn("upstream secret", json.dumps(record))

    def test_nested_protocol_metadata_excludes_user_content(self):
        record = logger.build_http_record(flow(
            host="runtime.us-east-1.kiro.dev",
            body=json.dumps({"conversationState": {
                "chatTriggerType": "MANUAL",
                "conversationId": "private-conversation-id",
                "currentMessage": {"userInputMessage": {
                    "content": "private prompt",
                    "modelId": "claude-sonnet-4.5",
                    "origin": "AI_EDITOR",
                }},
            }}),
        ))
        assert record is not None
        self.assertEqual(record["origin"], "AI_EDITOR")
        self.assertEqual(record["model_id"], "claude-sonnet-4.5")
        self.assertEqual(record["chat_trigger_type"], "MANUAL")
        self.assertNotIn("private prompt", json.dumps(record))
        self.assertNotIn("private-conversation-id", json.dumps(record))


class OutputTests(unittest.TestCase):
    def test_response_hook_writes_one_sanitized_record(self):
        with tempfile.TemporaryDirectory() as tmp:
            target = Path(tmp) / "http.jsonl"
            with mock.patch.dict(os.environ, {"KIRO_CAPTURE_HTTP_LOG": str(target)}):
                logger.response(flow(host="q.us-east-1.amazonaws.com", status=204))
            record = json.loads(target.read_text(encoding="utf-8"))
            self.assertEqual(record["status_code"], 204)

    def test_allowlists_exclude_credentials(self):
        self.assertFalse(logger.HEADER_ALLOWLIST & logger.SECRET_HEADER_NAMES)
        self.assertNotIn("profileArn", logger.BODY_KEY_ALLOWLIST)
        self.assertEqual(logger.KIRO_HOSTS, {
            "runtime.us-east-1.kiro.dev",
            "management.us-east-1.kiro.dev",
            "codewhisperer.us-east-1.amazonaws.com",
            "q.us-east-1.amazonaws.com",
        })


if __name__ == "__main__":
    unittest.main()
