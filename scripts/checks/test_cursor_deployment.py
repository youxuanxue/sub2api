"""Cursor runs inside the Go gateway, with no SDK process or inference store."""

from copy import deepcopy
from pathlib import Path
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[2]


def deployment_errors(services):
    errors = []
    for name, service in services.items():
        if "cursor" in name.lower() and name != "tokenkey":
            errors.append("Cursor must not require a separate runtime")
        environment = service.get("environment", {})
        names = environment if isinstance(environment, dict) else [
            value.split("=", 1)[0] for value in environment
        ]
        if any(name.startswith(("CURSOR_BRIDGE_", "CURSOR_RELAY_")) for name in names):
            errors.append("Cursor must not require bridge credentials")
    return errors


class CursorDeploymentTest(unittest.TestCase):
    def test_release_services_have_no_cursor_runtime_dependency(self):
        for path in sorted((ROOT / "deploy/aws/stage0").glob("docker-compose*.yml")):
            with self.subTest(path=path.name):
                with path.open() as source:
                    document = yaml.safe_load(source)
                self.assertEqual([], deployment_errors(document.get("services", {})))

    def test_bridge_service_and_secrets_are_rejected(self):
        baseline = {"tokenkey": {"image": "tokenkey:test"}}
        for mutation in [
            {"cursor-bridge": {"image": "cursor:test"}},
            {"tokenkey": {"environment": {"CURSOR_BRIDGE_SECRET": "test"}}},
            {"tokenkey": {"environment": ["CURSOR_RELAY_SECRET=test"]}},
        ]:
            with self.subTest(mutation=mutation):
                services = deepcopy(baseline)
                services.update(mutation)
                self.assertTrue(deployment_errors(services))


if __name__ == "__main__":
    unittest.main()
