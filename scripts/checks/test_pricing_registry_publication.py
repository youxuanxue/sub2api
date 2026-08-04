#!/usr/bin/env python3
"""Behavior tests for the pricing-registry publication boundary."""

from __future__ import annotations

import importlib.util
import pathlib
import shutil
import tempfile
import unittest


MODULE_PATH = pathlib.Path(__file__).with_name("pricing-registry-publication.py")
SPEC = importlib.util.spec_from_file_location("pricing_registry_publication", MODULE_PATH)
assert SPEC and SPEC.loader
CHECK = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(CHECK)


class PricingRegistryPublicationGateTest(unittest.TestCase):
    def setUp(self) -> None:
        self.tempdir = tempfile.TemporaryDirectory()
        self.root = pathlib.Path(self.tempdir.name)
        for relative in (
            ".github/workflows/pricing-registry-publish.yml",
            ".github/workflows/pricing-registry-sensor.yml",
            ".github/workflows/deploy-stage0.yml",
            "ops/pricing/pricing-registry-sensor.py",
        ):
            source = CHECK.REPO_ROOT / relative
            target = self.root / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(source, target)

    def tearDown(self) -> None:
        self.tempdir.cleanup()

    def _replace(self, relative: str, old: str, new: str) -> None:
        path = self.root / relative
        text = path.read_text(encoding="utf-8")
        self.assertIn(old, text)
        path.write_text(text.replace(old, new, 1), encoding="utf-8")

    def test_accepts_current_protected_boundary(self) -> None:
        self.assertEqual(CHECK.validate_publication_boundary(self.root), [])

    def test_rejects_non_main_or_multi_file_publication_trigger(self) -> None:
        workflow = ".github/workflows/pricing-registry-publish.yml"
        self._replace(workflow, "branches: [main]", "branches: [feature]")
        self._replace(
            workflow,
            "      - backend/internal/service/tk_pricing_overlay.json",
            "      - backend/internal/service/tk_pricing_overlay.json\n      - backend/internal/service/pricing_service.go",
        )
        errors = CHECK.validate_publication_boundary(self.root)
        self.assertTrue(any("branches" in error for error in errors), errors)
        self.assertTrue(any("paths" in error for error in errors), errors)

    def test_rejects_deploy_price_write(self) -> None:
        deploy = self.root / ".github/workflows/deploy-stage0.yml"
        with deploy.open("a", encoding="utf-8") as handle:
            handle.write("\n# python3 ops/pricing/manage-overlay-runtime.py sync-runtime\n")
        errors = CHECK.validate_publication_boundary(self.root)
        self.assertTrue(any("deploy workflow" in error for error in errors), errors)

    def test_rejects_sensor_aws_or_runtime_publication_capability(self) -> None:
        sensor = self.root / "ops/pricing/pricing-registry-sensor.py"
        with sensor.open("a", encoding="utf-8") as handle:
            handle.write("\nimport boto3  # unauthorized publication capability\n")
        errors = CHECK.validate_publication_boundary(self.root)
        self.assertTrue(any("sensor may not publish" in error for error in errors), errors)


if __name__ == "__main__":
    unittest.main()
