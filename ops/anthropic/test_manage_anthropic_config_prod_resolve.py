#!/usr/bin/env python3
"""Prod resolve in manage-anthropic-config must be cutover-aware (Hybrid mi-*)."""
from __future__ import annotations

import importlib.util
import pathlib
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
_SPEC = importlib.util.spec_from_file_location(
    "tk_manage_anthropic_config_prod_resolve",
    ROOT / "ops/anthropic/manage-anthropic-config.py",
)
assert _SPEC is not None and _SPEC.loader is not None
mgr = importlib.util.module_from_spec(_SPEC)
_SPEC.loader.exec_module(mgr)


class ProdResolveCutoverTest(unittest.TestCase):
    def test_resolve_prod_target_uses_ssm_identity(self) -> None:
        orig = mgr._PROD_SSM.resolve_prod_identity
        mgr._PROD_SSM.resolve_prod_identity = lambda: (
            "mi-0123456789abcdef0",
            "eu-west-2",
        )
        try:
            region, instance_id, label = mgr._resolve_prod_target()
        finally:
            mgr._PROD_SSM.resolve_prod_identity = orig
        self.assertEqual(region, "eu-west-2")
        self.assertEqual(instance_id, "mi-0123456789abcdef0")
        self.assertEqual(label, mgr.PROD_TARGET["label"])


if __name__ == "__main__":
    unittest.main()
