"""Include domestic distribution behavior in the existing preflight scripts suite."""
import unittest

from deploy.domestic import test_deploy_via_ssm, test_image_distribution


def load_tests(loader, tests, pattern):
    return unittest.TestSuite([
        loader.loadTestsFromModule(test_image_distribution),
        loader.loadTestsFromModule(test_deploy_via_ssm),
    ])
