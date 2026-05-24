import subprocess
import sys
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]
RENDER = REPO_ROOT / "deploy/aws/lightsail/render-bootstrap.sh"
GENERATED = REPO_ROOT / "deploy/aws/lightsail/generated-launch-script.sh"


class RenderBootstrapTests(unittest.TestCase):
    def test_check_passes_for_committed_artifact(self):
        proc = subprocess.run(
            ["bash", str(RENDER), "--check"],
            capture_output=True,
            text=True,
        )
        self.assertEqual(
            proc.returncode,
            0,
            f"render-bootstrap.sh --check FAILED.\nstdout:\n{proc.stdout}\nstderr:\n{proc.stderr}",
        )

    def test_generated_artifact_carries_failfast_register_branch(self):
        content = GENERATED.read_text(encoding="utf-8")
        self.assertIn(
            "BOOTSTRAP_FAIL: amazon-ssm-agent -register failed",
            content,
            "Generated launch script must surface SSM register failures fail-fast (R-003).",
        )
        self.assertIn(
            "BOOTSTRAP_FAIL: amazon-ssm-agent failed to stay active after register",
            content,
        )

    def test_generated_artifact_does_not_silently_swallow_register(self):
        content = GENERATED.read_text(encoding="utf-8")
        self.assertNotIn(
            "amazon-ssm-agent -register -y \\\n  -id",
            content.replace("\r\n", "\n").replace("-region \"${LIGHTSAIL_REGION}\" || true", "REGISTER_OK"),
        )
        self.assertNotIn(
            "-region \"${LIGHTSAIL_REGION}\" || true",
            content,
            "register must not be guarded by || true (R-003).",
        )

    def test_generated_artifact_skips_docker_login_when_pat_empty(self):
        # Default: GHCR is public → bootstrap must take the anonymous path.
        content = GENERATED.read_text(encoding="utf-8")
        self.assertIn(
            'if [ -n "${GHCR_PAT_SSM_NAME:-}" ]; then',
            content,
            "bootstrap must gate docker login on GHCR_PAT_SSM_NAME being non-empty",
        )
        self.assertIn(
            "relying on anonymous pull for public image",
            content,
            "bootstrap must log the anonymous-pull branch when PAT is unset",
        )
        # The PRIVATE branch must still exist for the case GHCR turns private.
        self.assertIn(
            'docker login ghcr.io -u "${GHCR_PULL_USER}" --password-stdin',
            content,
            "bootstrap must retain the private-image docker login path",
        )

    def test_generated_artifact_does_not_require_ghcr_pat_ssm_name(self):
        # The strict `: "${GHCR_PAT_SSM_NAME:?...}"` guard would break the
        # anonymous-default invariant by aborting bootstrap when no PAT is
        # configured. Assert it's gone.
        content = GENERATED.read_text(encoding="utf-8")
        self.assertNotIn(
            '"${GHCR_PAT_SSM_NAME:?',
            content,
            "GHCR_PAT_SSM_NAME must not be a required env var (default = anonymous pull)",
        )


if __name__ == "__main__":
    unittest.main()
