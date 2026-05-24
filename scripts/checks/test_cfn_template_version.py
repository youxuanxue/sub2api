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


class LightsailAddonContractTests(unittest.TestCase):
    """The Lightsail addon CFN template carries two load-bearing resources
    operations depend on. Regression-guard them at the structural level so a
    future edit cannot silently drop the SSM Hybrid IAM role (which would make
    `aws ssm create-activation --iam-role` fail at provision time)."""

    ADDON = REPO_ROOT / "deploy/aws/cloudformation/cicd-oidc-lightsail-addon.yaml"

    def setUp(self):
        self.text = self.ADDON.read_text(encoding="utf-8")

    def test_ssm_hybrid_role_resource_present(self):
        self.assertIn("LightsailSsmHybridRole:", self.text,
                      "addon must declare the SSM Hybrid managed-instance role")
        self.assertIn("Service: ssm.amazonaws.com", self.text,
                      "trust policy must allow ssm.amazonaws.com")
        self.assertIn("arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore", self.text,
                      "role must attach AmazonSSMManagedInstanceCore managed policy")

    def test_passrole_for_ssm_activation_present(self):
        # create-activation embeds the role into the activation, which requires
        # the caller to have iam:PassRole on it (scoped to ssm.amazonaws.com).
        self.assertIn("iam:PassRole", self.text,
                      "OIDC role inline policy must grant iam:PassRole on the Hybrid role")
        self.assertIn("iam:PassedToService: ssm.amazonaws.com", self.text,
                      "PassRole must be scoped to ssm.amazonaws.com")

    def test_hybrid_role_name_param_matches_provision_default(self):
        # provision-edge.sh defaults SSM_HYBRID_ROLE_NAME to this value; if the
        # addon's role name parameter default drifts, the dispatch will fail.
        self.assertIn("tokenkey-lightsail-ssm-hybrid", self.text,
                      "default role name must match provision-edge.sh fallback")


if __name__ == "__main__":
    unittest.main()


if __name__ == "__main__":
    unittest.main()
