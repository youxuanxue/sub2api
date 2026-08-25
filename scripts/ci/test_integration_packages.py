#!/usr/bin/env python3
"""Behavior tests for integration test package discovery."""

from __future__ import annotations

from pathlib import Path
import subprocess
import tempfile
import textwrap
import unittest


SCRIPT = Path(__file__).resolve().parent / "integration-packages.py"


class IntegrationPackagesTest(unittest.TestCase):
    def test_lists_only_packages_that_gain_tests_from_the_integration_tag(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "go.mod").write_text("module example.com/integrationfixture\n\ngo 1.26\n")
            self._write_package(root, "plain", "plain_test.go", "", "TestPlain")
            self._write_package(
                root,
                "repository",
                "repository_integration_test.go",
                "//go:build integration\n\n",
                "TestRepositoryIntegration",
            )
            self._write_package(
                root,
                "routes",
                "routes_integration_test.go",
                "//go:build integration && (linux || darwin)\n\n",
                "TestRoutesIntegration",
            )
            self._write_package(
                root,
                "negated",
                "negated_test.go",
                "//go:build !integration\n\n",
                "TestNegated",
            )

            result = subprocess.run(
                ["python3", str(SCRIPT), "--root", str(root)],
                check=False,
                capture_output=True,
                text=True,
            )

            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout.splitlines(), ["./repository", "./routes"])

    def test_fails_when_no_integration_tagged_tests_exist(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "go.mod").write_text("module example.com/plainfixture\n\ngo 1.26\n")
            self._write_package(root, "plain", "plain_test.go", "", "TestPlain")

            result = subprocess.run(
                ["python3", str(SCRIPT), "--root", str(root)],
                check=False,
                capture_output=True,
                text=True,
            )

            self.assertNotEqual(result.returncode, 0)
            self.assertIn("no integration-tagged test packages found", result.stderr)

    @staticmethod
    def _write_package(
        root: Path,
        package: str,
        test_file: str,
        build_constraint: str,
        test_name: str,
    ) -> None:
        package_dir = root / package
        package_dir.mkdir(parents=True, exist_ok=True)
        (package_dir / f"{package}.go").write_text(
            f"package {package}\n", encoding="utf-8"
        )
        (package_dir / test_file).write_text(
            build_constraint
            + textwrap.dedent(
                f"""
                package {package}

                import "testing"

                func {test_name}(t *testing.T) {{}}
                """
            ),
            encoding="utf-8",
        )


if __name__ == "__main__":
    unittest.main()
