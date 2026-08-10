#!/usr/bin/env python3
"""Tests for scripts/check_endpoint_compat_baseline_freshness.py."""
from __future__ import annotations

import importlib.util
import pathlib
import unittest

_CHECK = pathlib.Path(__file__).resolve().parent / "check_endpoint_compat_baseline_freshness.py"


def _load_check_module():
    spec = importlib.util.spec_from_file_location("check_endpoint_compat_baseline_freshness", _CHECK)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"unable to load {_CHECK}")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class CheckEndpointCompatBaselineFreshnessTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.mod = _load_check_module()

    def test_valid_anchor_row_matches_version(self) -> None:
        text = (
            "| Runtime code anchor | `v1.8.142` release (`backend/cmd/server/VERSION`); "
            "last live deploy `v1.8.141`. canary failed (#1611). |\n"
        )
        self.assertIsNone(self.mod.validate_baseline_freshness(text, "1.8.142"))

    def test_pending_deploy_wording_fails_syncable_shape(self) -> None:
        text = (
            "| Runtime code anchor | `v1.8.142` release (`backend/cmd/server/VERSION`); "
            "last live deploy pending `v1.8.142` (prod still on `v1.8.141`). |\n"
        )
        err = self.mod.validate_baseline_freshness(text, "1.8.142")
        self.assertIsNotNone(err)
        self.assertIn("not syncable", err)

    def test_release_tag_must_match_version_file(self) -> None:
        text = (
            "| Runtime code anchor | `v1.8.141` release (`backend/cmd/server/VERSION`); "
            "last live deploy `v1.8.140`. |\n"
        )
        err = self.mod.validate_baseline_freshness(text, "1.8.142")
        self.assertIsNotNone(err)
        self.assertIn("v1.8.141", err)
        self.assertIn("v1.8.142", err)


if __name__ == "__main__":
    unittest.main()
