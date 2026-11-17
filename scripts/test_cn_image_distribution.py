"""Include domestic distribution behavior in the existing preflight scripts suite."""
from pathlib import Path
import unittest

from deploy.domestic import test_deploy_via_ssm, test_image_distribution, test_warm_fanout

ROOT = Path(__file__).resolve().parents[1]
WORKFLOWS = ROOT / ".github" / "workflows"


class ReleaseWorkflowCredentialBoundaryTest(unittest.TestCase):
    def test_prod_rolling_approval_and_tencent_credentials_are_separate(self):
        text = (WORKFLOWS / "prod-rolling-deploy.yml").read_text()
        self.assertIn("  approval:\n", text)
        self.assertIn("    environment: prod\n", text)
        self.assertIn("  rolling:\n    needs: approval", text)
        self.assertIn("    environment: tencent-ops", text)
        self.assertIn("TENCENTCLOUD_SECRET_ID", text)
        self.assertIn("--via tat", text)
        self.assertNotIn("--via ssm", text)
        self.assertNotIn("aws-actions/configure-aws-credentials", text)
        self.assertNotIn("id-token: write", text)

    def test_cn_rolling_approval_and_tencent_credentials_are_separate(self):
        text = (WORKFLOWS / "cn-prod-rolling-deploy.yml").read_text()
        self.assertIn("  approval:\n", text)
        self.assertIn("    environment: prod\n", text)
        self.assertIn("  rolling:\n    needs: approval", text)
        self.assertIn("    environment: tencent-ops", text)
        self.assertIn("TENCENTCLOUD_SECRET_ID", text)

    def test_cn_distribution_uses_tencent_cos_for_publish_and_warm(self):
        text = (WORKFLOWS / "distribute-cn-image.yml").read_text()
        self.assertIn("  resolve:\n", text)
        self.assertIn("default: prod", text)
        self.assertIn("  publish:\n", text)
        self.assertIn("  publish-from-prod:\n", text)
        self.assertIn("  warm:\n", text)
        self.assertIn("  warm-ghcr:\n", text)

        # Legacy GHA→COS job: tencent secrets only (no AWS OIDC on this job).
        legacy = text.split("  publish:\n", 1)[1].split("  publish-from-prod:\n", 1)[0]
        self.assertIn("environment: tencent-ops", legacy)
        self.assertIn("TENCENTCLOUD_SECRET_ID", legacy)
        self.assertIn("python3 deploy/domestic/image_distribution.py publish", legacy)
        self.assertIn("python3 deploy/domestic/warm_fanout.py render-all", legacy)
        self.assertIn("name: cn-image-render", legacy)
        self.assertIn("retention-days: 1", legacy)
        self.assertIn("--channel tat", legacy)
        self.assertNotIn("--channel auto", legacy)
        self.assertIn("needs.resolve.outputs.transport == 's3'", legacy)
        self.assertNotIn("id-token: write", legacy)
        self.assertNotIn("configure-aws-credentials", legacy)

        # Standard prod→COS: AWS OIDC only for SSM to prod; COS still TENCENT.
        prod_pub = text.split("  publish-from-prod:\n", 1)[1].split("  warm:\n", 1)[0]
        self.assertIn("environment: tencent-ops", prod_pub)
        self.assertIn("TENCENTCLOUD_SECRET_ID", prod_pub)
        self.assertIn("id-token: write", prod_pub)
        self.assertIn("configure-aws-credentials", prod_pub)
        self.assertIn("probe_publish_cn_from_prod.sh", prod_pub)
        self.assertIn("python3 deploy/domestic/warm_fanout.py render-all", prod_pub)
        self.assertIn("--channel tat", prod_pub)
        self.assertNotIn("--channel auto", prod_pub)

        warm = text.split("  warm:\n", 1)[1].split("  warm-ghcr:\n", 1)[0]
        self.assertIn("needs: [resolve, publish, publish-from-prod]", warm)
        self.assertIn("environment: tencent-ops", warm)
        self.assertIn("ref: ${{ needs.resolve.outputs.source_sha }}", warm)
        self.assertIn("python3 deploy/domestic/warm_fanout.py dispatch-rendered", warm)
        self.assertIn("TENCENTCLOUD_SECRET_ID", warm)
        self.assertNotIn("configure-aws-credentials", warm)
        self.assertNotIn("pip install boto3", warm)
        self.assertIn("--channel tat", warm)
        self.assertNotIn("--channel auto", warm)

        warm_ghcr = text.split("  warm-ghcr:\n", 1)[1]
        self.assertIn("inputs.transport == 'ghcr'", warm_ghcr)
        self.assertIn("python3 deploy/domestic/warm_fanout.py warm-all", warm_ghcr)
        self.assertIn("--transport ghcr", warm_ghcr)
        self.assertIn("--channel tat", warm_ghcr)
        self.assertNotIn("--channel auto", warm_ghcr)
        self.assertIn("environment: tencent-ops", warm_ghcr)
        self.assertNotIn("configure-aws-credentials", warm_ghcr)


def load_tests(loader, tests, pattern):
    return unittest.TestSuite([
        loader.loadTestsFromTestCase(ReleaseWorkflowCredentialBoundaryTest),
        loader.loadTestsFromModule(test_image_distribution),
        loader.loadTestsFromModule(test_deploy_via_ssm),
        loader.loadTestsFromModule(test_warm_fanout),
    ])
