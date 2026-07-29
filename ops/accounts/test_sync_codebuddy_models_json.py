#!/usr/bin/env python3
from __future__ import annotations

import importlib.util
import json
import sys
import unittest
from pathlib import Path
from unittest.mock import patch

HERE = Path(__file__).resolve().parent
MODULE_PATH = HERE / "sync-codebuddy-models-json.py"


def load_module():
    spec = importlib.util.spec_from_file_location("sync_codebuddy_models_json", MODULE_PATH)
    assert spec and spec.loader
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


MOD = load_module()


class SyncCodebuddyModelsJsonTest(unittest.TestCase):
    def test_is_media_only_filters_image_and_video_ids(self) -> None:
        self.assertTrue(MOD.is_media_only("gemini-2.5-flash-image"))
        self.assertTrue(MOD.is_media_only("imagen-4.0-generate-001"))
        self.assertTrue(MOD.is_media_only("doubao-seedream-5-0-260128"))
        self.assertTrue(MOD.is_media_only("veo-3.1-generate-001"))
        self.assertTrue(MOD.is_media_only("moonshot-v1-8k-vision-preview"))
        self.assertFalse(MOD.is_media_only("gpt-5.4-mini"))
        self.assertFalse(MOD.is_media_only("claude-sonnet-4-6"))

    def test_build_model_entry_uses_env_var_and_chat_url(self) -> None:
        entry = MOD.build_model_entry(
            "gpt-5.4-mini",
            base_url="https://api.tokenkey.dev",
            env_var="TK_FULLTEST_KEY",
            chat_rows={"gpt-5.4-mini": {"vendor": "openai"}},
            pricing_by_id={
                "gpt-5.4-mini": {
                    "context_window": 400000,
                    "max_output_tokens": 128000,
                    "capabilities": ["tool_use", "reasoning"],
                },
            },
        )
        self.assertEqual(entry["apiKey"], "${TK_FULLTEST_KEY}")
        self.assertEqual(entry["url"], "https://api.tokenkey.dev/v1/chat/completions")
        self.assertEqual(entry["vendor"], "OpenAI")
        self.assertTrue(entry["supportsToolCall"])
        self.assertTrue(entry["supportsReasoning"])

    def test_build_payload_intersects_live_models_and_skips_media(self) -> None:
        live = {"gpt-5.4-mini", "gemini-2.5-flash-image", "claude-sonnet-4-6"}
        chat_rows = {
            "gpt-5.4-mini": {"vendor": "openai"},
            "claude-sonnet-4-6": {"vendor": "anthropic"},
        }
        pricing = {
            "gpt-5.4-mini": {"vendor": "openai", "capabilities": ["tool_use"]},
            "claude-sonnet-4-6": {"vendor": "anthropic", "capabilities": ["tool_use"]},
        }

        with patch.object(MOD, "list_live_model_ids", return_value=live), patch.object(
            MOD, "load_chat_matrix_rows", return_value=chat_rows
        ), patch.object(MOD, "load_pricing_by_id", return_value=pricing):
            payload = MOD.build_payload(
                base_url="https://api.tokenkey.dev",
                api_key="sk-test",
                env_var="TK_FULLTEST_KEY",
                timeout=5,
            )

        self.assertEqual(payload["availableModels"], ["claude-sonnet-4-6", "gpt-5.4-mini"])
        self.assertEqual(len(payload["models"]), 2)


if __name__ == "__main__":
    unittest.main()
