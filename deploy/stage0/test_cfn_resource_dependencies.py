#!/usr/bin/env python3
"""Behavior tests for the CloudFormation DependsOn preflight guard."""
from __future__ import annotations

import importlib.util
import pathlib
import tempfile
import unittest


_SCRIPT = pathlib.Path(__file__).resolve().parent / "cfn-resource-dependencies.py"
_SPEC = importlib.util.spec_from_file_location("cfn_resource_dependencies", _SCRIPT)
assert _SPEC and _SPEC.loader
_MODULE = importlib.util.module_from_spec(_SPEC)
_SPEC.loader.exec_module(_MODULE)


class CfnResourceDependenciesTest(unittest.TestCase):
    def _check(self, template: str) -> list[str]:
        with tempfile.NamedTemporaryFile("w", suffix=".yaml", delete=False) as handle:
            handle.write(template)
            path = pathlib.Path(handle.name)
        try:
            return _MODULE.check(path)
        finally:
            path.unlink(missing_ok=True)

    def test_accepts_scalar_inline_and_block_dependencies(self) -> None:
        errors = self._check(
            """\
Resources:
  Network:
    Type: AWS::EC2::VPC
  Storage:
    Type: AWS::EC2::Volume
    DependsOn: Network
  App:
    Type: AWS::EC2::Instance
    DependsOn: [Network, Storage]
  Alarm:
    Type: AWS::CloudWatch::Alarm
    DependsOn:
      - App
      - Storage
Outputs:
  AppId:
    Value: !Ref App
"""
        )

        self.assertEqual(errors, [])

    def test_rejects_undefined_block_dependency(self) -> None:
        errors = self._check(
            """\
Resources:
  App:
    Type: AWS::EC2::Instance
    DependsOn:
      - DeletedParameter
"""
        )

        self.assertEqual(
            errors,
            ["line 5: DependsOn references undefined resource 'DeletedParameter'"],
        )

    def test_repository_templates_have_resolvable_dependencies(self) -> None:
        failures = []
        for path in sorted((*_MODULE.CFN_DIR.rglob("*.yaml"), *_MODULE.CFN_DIR.rglob("*.yml"))):
            failures.extend(f"{path}: {error}" for error in _MODULE.check(path))
        self.assertEqual(failures, [])


if __name__ == "__main__":
    unittest.main()
