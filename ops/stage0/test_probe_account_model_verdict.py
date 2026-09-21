#!/usr/bin/env python3
"""Behavioral tests for probe_account_model_verdict.classify_probe_verdict."""

from __future__ import annotations

import unittest
import json
import base64
import io
import importlib.util

from probe_account_model_verdict import classify_probe_verdict, embedding_response_valid


class ProbeAccountModelVerdictTest(unittest.TestCase):
    @unittest.skipUnless(importlib.util.find_spec('PIL'), 'image decoder installed by Gemini Web CI job')
    def test_gemini_images_require_matching_mime_and_full_decode(self):
        from PIL import Image
        output = io.BytesIO()
        Image.new('RGB', (4, 4), 'blue').save(output, 'PNG')
        valid = output.getvalue()
        for data, mime, expected in [
            (valid, 'image/png', 'servable'),
            (valid, 'image/jpeg', 'uncorrelated_success'),
            (b'\x89PNG\r\n\x1a\n' + b'garbage' * 10, 'image/png', 'uncorrelated_success'),
            (valid[:40], 'image/png', 'uncorrelated_success'),
        ]:
            body = json.dumps({'candidates': [{'finishReason': 'STOP', 'content': {'parts': [
                {'inlineData': {'mimeType': mime, 'data': base64.b64encode(data).decode()}}]}}]})
            self.assertEqual(classify_probe_verdict(endpoint='gemini_image', http_code='200',
                body_text=body, target_account_id=90, usage_row={'account_id': 90}, curl_err=''), expected)

    def test_gemini_requires_complete_response_and_exact_account(self) -> None:
        body = json.dumps({"candidates": [{"finishReason": "STOP", "content": {"parts": [{"text": "ok"}]}}]})
        for endpoint, payload, usage, expected in [
            ("gemini", body, {"account_id": 90}, "servable"),
            ("gemini", body, {"account_id": 91}, "wrong_account"),
            ("gemini", body, None, "uncorrelated_success"),
            ("gemini_image", body, {"account_id": 90}, "uncorrelated_success"),
            ("gemini", '{}', {"account_id": 90}, "uncorrelated_success"),
            ("gemini", body.replace('STOP', 'MAX_TOKENS'), {"account_id": 90}, "uncorrelated_success"),
        ]:
            self.assertEqual(classify_probe_verdict(endpoint=endpoint, http_code="200",
                body_text=payload, target_account_id=90, usage_row=usage, curl_err=""), expected)

    def test_transcriptions_require_text_and_target_account_usage(self) -> None:
        cases = [
            ('{"text":"recognized"}', {"account_id": 90}, "servable"),
            ('{"text":""}', {"account_id": 90}, "servable"),
            ('{"text":"recognized"}', {"account_id": 39}, "wrong_account"),
            ('{"text":"recognized"}', None, "uncorrelated_success"),
            ('{"text":null}', {"account_id": 90}, "uncorrelated_success"),
            ('{"error":"upstream failed"}', {"account_id": 90}, "uncorrelated_success"),
            ('[]', {"account_id": 90}, "uncorrelated_success"),
            ('not JSON', {"account_id": 90}, "uncorrelated_success"),
        ]
        for body, usage, expected in cases:
            with self.subTest(body=body, usage=usage):
                self.assertEqual(classify_probe_verdict(
                    endpoint="transcriptions", http_code="200", body_text=body,
                    target_account_id=90, usage_row=usage, curl_err="",
                ), expected)

    def test_embedding_response_valid_requires_data_embedding(self) -> None:
        body = '{"object":"list","data":[{"object":"embedding","embedding":[0.1]}]}'
        self.assertTrue(embedding_response_valid(body))
        self.assertFalse(embedding_response_valid('{"data":[]}'))
        self.assertFalse(embedding_response_valid('{"data":[{"object":"embedding"}]}'))

    def test_embeddings_servable_without_usage_when_body_valid(self) -> None:
        body = '{"data":[{"object":"embedding","embedding":[0.1]}]}'
        verdict = classify_probe_verdict(
            endpoint="embeddings",
            http_code="200",
            body_text=body,
            target_account_id=90,
            usage_row=None,
            curl_err="",
        )
        self.assertEqual(verdict, "servable")

    def test_embeddings_wrong_account_when_usage_points_elsewhere(self) -> None:
        body = '{"data":[{"object":"embedding","embedding":[0.1]}]}'
        verdict = classify_probe_verdict(
            endpoint="embeddings",
            http_code="200",
            body_text=body,
            target_account_id=90,
            usage_row={"account_id": 39},
            curl_err="",
        )
        self.assertEqual(verdict, "wrong_account")

    def test_embeddings_servable_when_usage_matches_target(self) -> None:
        body = '{"data":[{"object":"embedding","embedding":[0.1]}]}'
        verdict = classify_probe_verdict(
            endpoint="embeddings",
            http_code="200",
            body_text=body,
            target_account_id=90,
            usage_row={"account_id": 90},
            curl_err="",
        )
        self.assertEqual(verdict, "servable")

    def test_chat_wrong_account_unchanged(self) -> None:
        verdict = classify_probe_verdict(
            endpoint="chat",
            http_code="200",
            body_text='{"choices":[{"message":{"content":"ok"}}]}',
            target_account_id=90,
            usage_row={"account_id": 39},
            curl_err="",
        )
        self.assertEqual(verdict, "wrong_account")


if __name__ == "__main__":
    unittest.main()
