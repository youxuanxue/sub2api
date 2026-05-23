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


if __name__ == "__main__":
    unittest.main()
