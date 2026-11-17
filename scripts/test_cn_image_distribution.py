"""Include domestic distribution behavior in the existing preflight scripts suite."""
from pathlib import Path
import unittest

from deploy.domestic import test_deploy_via_ssm, test_image_distribution, test_warm_fanout

ROOT = Path(__file__).resolve().parents[1]
WORKFLOWS = ROOT / ".github" / "workflows"


class ReleaseWorkflowCredentialBoundaryTest(unittest.TestCase):
    def test_prod_rolling_job_uses_prod_environment_for_oidc_and_approval(self):
        text = (WORKFLOWS / "prod-rolling-deploy.yml").read_text()
        self.assertNotIn("needs: approval", text)
        self.assertIn("environment: prod", text)
        self.assertNotIn("environment: tencent-ops", text)
        self.assertIn("aws-actions/configure-aws-credentials", text)

    def test_cn_rolling_approval_and_tencent_credentials_are_separate(self):
        text = (WORKFLOWS / "cn-prod-rolling-deploy.yml").read_text()
        self.assertIn("  approval:\n", text)
        self.assertIn("    environment: prod\n", text)
        self.assertIn("  rolling:\n    needs: approval", text)
        self.assertIn("    environment: tencent-ops", text)
        self.assertIn("TENCENTCLOUD_SECRET_ID", text)

    def test_cn_distribution_uses_tencent_cos_for_publish_and_warm(self):
        text = (WORKFLOWS / "distribute-cn-image.yml").read_text()
        publish, warm = text.split("  warm:\n", 1)
        self.assertIn("  publish:\n", publish)
        self.assertIn("environment: tencent-ops", publish)
        self.assertIn("TENCENTCLOUD_SECRET_ID", publish)
        self.assertNotIn("id-token: write", publish)
        self.assertNotIn("configure-aws-credentials", publish)
        self.assertIn("python3 deploy/domestic/warm_fanout.py render-all", publish)
        self.assertIn("name: cn-image-render", publish)
        self.assertIn("retention-days: 1", publish)
        self.assertIn("--channel tat", publish)
        self.assertNotIn("--channel auto", publish)

        self.assertIn("needs: publish", warm)
        self.assertIn("environment: tencent-ops", warm)
        self.assertIn("ref: ${{ needs.publish.outputs.source_sha }}", warm)
        self.assertIn("python3 deploy/domestic/warm_fanout.py dispatch-rendered", warm)
        self.assertIn("TENCENTCLOUD_SECRET_ID", warm)
        self.assertNotIn("configure-aws-credentials", warm)
        self.assertNotIn("pip install boto3", warm)
        self.assertIn("--channel tat", warm)
        self.assertNotIn("--channel auto", warm)


def load_tests(loader, tests, pattern):
    return unittest.TestSuite([
        loader.loadTestsFromTestCase(ReleaseWorkflowCredentialBoundaryTest),
        loader.loadTestsFromModule(test_image_distribution),
        loader.loadTestsFromModule(test_deploy_via_ssm),
        loader.loadTestsFromModule(test_warm_fanout),
    ])
