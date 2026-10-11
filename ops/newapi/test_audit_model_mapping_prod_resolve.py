#!/usr/bin/env python3
"""audit-model-mapping prod resolve must be cutover-aware."""
from __future__ import annotations

import importlib.util
import pathlib
import unittest

ROOT = pathlib.Path(__file__).resolve().parents[2]
_SPEC = importlib.util.spec_from_file_location(
    "tk_audit_model_mapping_prod_resolve",
    ROOT / "ops/newapi/audit-model-mapping.py",
)
assert _SPEC is not None and _SPEC.loader is not None
mod = importlib.util.module_from_spec(_SPEC)
_SPEC.loader.exec_module(mod)


class AuditModelMappingProdResolveTest(unittest.TestCase):
    def test_main_uses_resolve_prod_identity(self) -> None:
        calls: list[tuple[str, str]] = []

        def fake_identity() -> tuple[str, str]:
            return ("mi-0123456789abcdef0", "eu-west-2")

        def fake_sql(region: str, instance_id: str, sql: str, comment: str) -> str:
            calls.append((region, instance_id))
            self.assertIn("platform = 'newapi'", sql)
            return "[]"

        orig_identity = mod._SSM.resolve_prod_identity
        orig_sql = mod.ssm_run_sql
        mod._SSM.resolve_prod_identity = fake_identity  # type: ignore[method-assign]
        mod.ssm_run_sql = fake_sql  # type: ignore[assignment]
        try:
            # argparse Namespace via sys.argv
            import sys

            argv = sys.argv
            sys.argv = ["audit-model-mapping.py", "--json"]
            try:
                code = mod.main()
            finally:
                sys.argv = argv
        finally:
            mod._SSM.resolve_prod_identity = orig_identity  # type: ignore[method-assign]
            mod.ssm_run_sql = orig_sql  # type: ignore[assignment]

        self.assertEqual(code, 0)
        self.assertEqual(calls, [("eu-west-2", "mi-0123456789abcdef0")])


if __name__ == "__main__":
    unittest.main()
