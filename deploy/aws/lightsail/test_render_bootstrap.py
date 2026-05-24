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

    def test_generated_artifact_under_lightsail_user_data_cap(self):
        # Lightsail user-data hard limit is 16384 bytes. provision-edge.sh
        # prepends ~500 bytes of `export VAR=...` env at dispatch time.
        # Safety cap at 14336 bytes leaves room for env prefix to stay under.
        # Phase 2 4th attempt hit InvalidInputException because the file was
        # 15720 bytes — this regression test prevents recurrence.
        size = GENERATED.stat().st_size
        self.assertLessEqual(size, 14336,
                             f"generated-launch-script.sh is {size} bytes; "
                             "Lightsail user-data hard limit is 16384 bytes and "
                             "provision-edge.sh adds ~500 bytes of env prefix, "
                             "so stay under 14336 here. Options: gzip more aggressively, "
                             "trim comments, or move payloads to SSM Parameter Store.")

    def test_qa_and_prune_scripts_are_gzipped(self):
        # Both ops scripts are now gzipped before base64-encoding (was raw
        # base64 in earlier revisions, contributing ~3.7 KB of overhead).
        # The decoder pipe `base64 -d | gunzip` is the test contract; if a
        # future edit drops gunzip the bootstrap silently fails at runtime.
        content = GENERATED.read_text(encoding="utf-8")
        self.assertIn(
            'printf \'%s\' "$QA_B64" | base64 -d | gunzip > /usr/local/bin/tokenkey-qa-stale-cleanup.sh',
            content,
            "QA_B64 must be gzip-decoded; otherwise user-data bloats past 16KB",
        )
        self.assertIn(
            'printf \'%s\' "$PRUNE_B64" | base64 -d | gunzip > /usr/local/bin/tokenkey-prune-ghcr-app-tags-core.sh',
            content,
            "PRUNE_B64 must be gzip-decoded; otherwise user-data bloats past 16KB",
        )


if __name__ == "__main__":
    unittest.main()
