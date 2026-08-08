#!/usr/bin/env python3
"""Behavioral tests for probe_account_model_verdict.classify_probe_verdict."""

from __future__ import annotations

import unittest

from probe_account_model_verdict import classify_probe_verdict, embedding_response_valid


class ProbeAccountModelVerdictTest(unittest.TestCase):
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
