"""Unit tests for OpenRouter chain probe catalog-driven picks (no live network)."""
from __future__ import annotations

import importlib.util
import pathlib
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "probe-openrouter-provider-chain.py"


def _load():
    import sys

    name = "or_chain_probe_under_test"
    spec = importlib.util.spec_from_file_location(name, _SCRIPT)
    mod = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    sys.modules[name] = mod
    spec.loader.exec_module(mod)
    return mod


class CatalogDrivenPicksTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.mod = _load()

    def test_text_only_catalog_media_picks_are_none(self) -> None:
        catalog = [
            {
                "id": "tokenkey/deepseek-v4-flash",
                "input_modalities": [{"type": "text"}],
                "output_modalities": [{"type": "text"}],
            }
        ]
        picks = self.mod.pick_models(catalog)
        self.assertEqual(picks["chat"], "tokenkey/deepseek-v4-flash")
        self.assertIsNone(picks["image"])
        self.assertIsNone(picks["imagen"])
        self.assertIsNone(picks["video"])
        self.assertIsNone(picks["audio"])
        counts = self.mod.catalog_output_modality_counts(catalog)
        self.assertEqual(counts["text_only"], 1)
        self.assertEqual(counts["image"], 0)

    def test_image_and_video_rows_are_picked(self) -> None:
        catalog = [
            {
                "id": "tokenkey/chat",
                "input_modalities": [{"type": "text"}],
                "output_modalities": [{"type": "text"}],
            },
            {
                "id": "tokenkey/gemini-3.1-flash-image",
                "input_modalities": [{"type": "text"}],
                "output_modalities": [{"type": "image"}],
            },
            {
                "id": "tokenkey/seedance-video",
                "input_modalities": [{"type": "text"}],
                "output_modalities": [{"type": "video"}],
            },
        ]
        picks = self.mod.pick_models(catalog)
        self.assertEqual(picks["image"], "tokenkey/gemini-3.1-flash-image")
        self.assertEqual(picks["video"], "tokenkey/seedance-video")
        counts = self.mod.catalog_output_modality_counts(catalog)
        self.assertEqual(counts["image"], 1)
        self.assertEqual(counts["video"], 1)

    def test_add_catalog_pick_marks_na_without_failing(self) -> None:
        report = self.mod.Report()
        self.mod.add_catalog_pick(report, "pick.video_model", None)
        self.mod.add_catalog_pick(report, "pick.gemini_image", "tokenkey/x")
        self.assertTrue(report.passed)
        self.assertEqual(report.checks[0].detail, "N/A: none in catalog")
        self.assertEqual(report.checks[1].detail, "tokenkey/x")


if __name__ == "__main__":
    unittest.main()
