"""Regression: SSM Hybrid discovery filters (provision-edge.sh wait loop)."""

import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]
PROVISION_EDGE = REPO_ROOT / "deploy/aws/lightsail/provision-edge.sh"


class ProvisionEdgeSSMDiscoveryTests(unittest.TestCase):
    def test_uses_activation_id_filter_primary(self):
        txt = PROVISION_EDGE.read_text(encoding="utf-8")
        self.assertIn(
            'Key=ActivationIds,Values=${activation_id}',
            txt,
            "Hybrid MI must be resolved via the minted ActivationId "
            "(reliable registration_limit=1 linkage).",
        )

    def test_does_not_combine_tag_filter_with_resource_type(self):
        # DescribeInstanceInformation: tag filters cannot be mixed with non-tag filters.
        # Prior bug: Key=tag:EdgeId,... Key=ResourceType,Values=ManagedInstance → empty/error.
        txt = PROVISION_EDGE.read_text(encoding="utf-8")
        self.assertNotRegex(
            txt,
            r"tag:\s*EdgeId.*ResourceType,Values=\s*ManagedInstance",
            "Illegal tag + ResourceType filter combo breaks describe-instance-information.",
        )


if __name__ == "__main__":
    unittest.main()
