"""Tests for scripts/checks/cfn-template-version.py.

stdlib-only.
"""
from __future__ import annotations

import pathlib
import shutil
import subprocess
import sys
import tempfile
import unittest

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = REPO_ROOT / "scripts/checks/cfn-template-version.py"


def _stage_fake_repo(tmp: pathlib.Path, templates: dict[str, str]) -> pathlib.Path:
    fake = tmp / "repo"
    (fake / "scripts/checks").mkdir(parents=True)
    (fake / "deploy/aws/cloudformation").mkdir(parents=True)
    shutil.copy(SCRIPT, fake / "scripts/checks/cfn-template-version.py")
    for name, content in templates.items():
        (fake / "deploy/aws/cloudformation" / name).write_text(content, encoding="utf-8")
    return fake


def _run(fake_root: pathlib.Path) -> subprocess.CompletedProcess:
    return subprocess.run(
        [sys.executable, str(fake_root / "scripts/checks/cfn-template-version.py")],
        capture_output=True,
        text=True,
    )


class CfnTemplateVersionTests(unittest.TestCase):
    def test_canonical_version_passes(self):
        with tempfile.TemporaryDirectory() as tmp:
            fake = _stage_fake_repo(
                pathlib.Path(tmp),
                {"a.yaml": 'AWSTemplateFormatVersion: "2010-09-09"\nResources: {}\n'},
            )
            proc = _run(fake)
            self.assertEqual(proc.returncode, 0, proc.stderr)

    def test_typo_october_fails(self):
        with tempfile.TemporaryDirectory() as tmp:
            fake = _stage_fake_repo(
                pathlib.Path(tmp),
                {"bad.yaml": 'AWSTemplateFormatVersion: "2010-10-09"\nResources: {}\n'},
            )
            proc = _run(fake)
            self.assertEqual(proc.returncode, 1, proc.stdout)
            self.assertIn("2010-10-09", proc.stderr)
            self.assertIn("must be '2010-09-09'", proc.stderr)

    def test_other_typo_year_fails(self):
        with tempfile.TemporaryDirectory() as tmp:
            fake = _stage_fake_repo(
                pathlib.Path(tmp),
                {"bad.yaml": 'AWSTemplateFormatVersion: "2024-09-09"\nResources: {}\n'},
            )
            proc = _run(fake)
            self.assertEqual(proc.returncode, 1, proc.stdout)
            self.assertIn("2024-09-09", proc.stderr)

    def test_missing_version_passes(self):
        # Omitting the version is legal per AWS spec.
        with tempfile.TemporaryDirectory() as tmp:
            fake = _stage_fake_repo(
                pathlib.Path(tmp),
                {"no-version.yaml": "Resources:\n  Foo:\n    Type: AWS::S3::Bucket\n"},
            )
            proc = _run(fake)
            self.assertEqual(proc.returncode, 0, proc.stderr)

    def test_unquoted_canonical_passes(self):
        with tempfile.TemporaryDirectory() as tmp:
            fake = _stage_fake_repo(
                pathlib.Path(tmp),
                {"unquoted.yaml": "AWSTemplateFormatVersion: 2010-09-09\nResources: {}\n"},
            )
            proc = _run(fake)
            self.assertEqual(proc.returncode, 0, proc.stderr)

    def test_single_quoted_canonical_passes(self):
        with tempfile.TemporaryDirectory() as tmp:
            fake = _stage_fake_repo(
                pathlib.Path(tmp),
                {"sq.yaml": "AWSTemplateFormatVersion: '2010-09-09'\nResources: {}\n"},
            )
            proc = _run(fake)
            self.assertEqual(proc.returncode, 0, proc.stderr)

    def test_multiple_files_only_one_bad(self):
        with tempfile.TemporaryDirectory() as tmp:
            fake = _stage_fake_repo(
                pathlib.Path(tmp),
                {
                    "good.yaml": 'AWSTemplateFormatVersion: "2010-09-09"\nResources: {}\n',
                    "bad.yaml": 'AWSTemplateFormatVersion: "2010-10-09"\nResources: {}\n',
                },
            )
            proc = _run(fake)
            self.assertEqual(proc.returncode, 1, proc.stdout)
            self.assertIn("bad.yaml", proc.stderr)
            self.assertNotIn("good.yaml", proc.stderr)

    def test_real_repo_templates_are_clean(self):
        """Smoke: every CFN template currently in the repo must use the canonical
        version. If this fails, the fix branch reverted the typo correction or
        a new template shipped with another typo."""
        proc = subprocess.run(
            [sys.executable, str(SCRIPT)],
            capture_output=True,
            text=True,
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)


if __name__ == "__main__":
    unittest.main()
