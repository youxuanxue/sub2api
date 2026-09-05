#!/usr/bin/env python3
"""Unit tests for manage-openrouter-provider-config list-field merge helpers."""
from __future__ import annotations

import importlib.util
import pathlib
import sys
import unittest

_MOD_PATH = pathlib.Path(__file__).resolve().parent / "manage-openrouter-provider-config.py"
_spec = importlib.util.spec_from_file_location("manage_openrouter_provider_config", _MOD_PATH)
mgr = importlib.util.module_from_spec(_spec)
assert _spec and _spec.loader
sys.modules[_spec.name] = mgr
_spec.loader.exec_module(mgr)


class MergeMissingListFieldsTest(unittest.TestCase):
    EXAMPLE = mgr._load_default_config()

    def test_fills_missing_and_null_from_example(self) -> None:
        live = {"enabled": True, "catalog_excluded_model_ids": None}
        merged = mgr._merge_missing_list_fields(live, self.EXAMPLE)
        self.assertEqual(merged["catalog_excluded_model_ids"], self.EXAMPLE["catalog_excluded_model_ids"])
        self.assertEqual(merged["stream_only_model_ids"], self.EXAMPLE["stream_only_model_ids"])
        self.assertTrue(merged["enabled"])

    def test_preserves_explicit_empty_lists(self) -> None:
        live = {
            "catalog_excluded_model_ids": [],
            "stream_only_model_ids": [],
        }
        merged = mgr._merge_missing_list_fields(live, self.EXAMPLE)
        self.assertEqual(merged["catalog_excluded_model_ids"], [])
        self.assertEqual(merged["stream_only_model_ids"], [])

    def test_preserves_explicit_ops_overrides(self) -> None:
        live = {
            "catalog_excluded_model_ids": ["custom-exclude"],
            "stream_only_model_ids": ["custom-stream"],
        }
        merged = mgr._merge_missing_list_fields(live, self.EXAMPLE)
        self.assertEqual(merged["catalog_excluded_model_ids"], ["custom-exclude"])
        self.assertEqual(merged["stream_only_model_ids"], ["custom-stream"])


class ParseSnapshotKeyIDTest(unittest.TestCase):
    def test_prefers_openrouter_name(self) -> None:
        raw = "11|other\n22|openrouter\n33|another"
        self.assertEqual(mgr._parse_snapshot_key_id(raw), 22)

    def test_falls_back_to_any_key(self) -> None:
        # Boundary: ops label missing → any billing-user key is enough.
        raw = "11|legacy-name\n33|another"
        self.assertEqual(mgr._parse_snapshot_key_id(raw), 11)

    def test_empty(self) -> None:
        self.assertEqual(mgr._parse_snapshot_key_id(""), 0)


if __name__ == "__main__":
    unittest.main()
