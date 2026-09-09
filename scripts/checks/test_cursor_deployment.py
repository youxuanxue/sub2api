"""Cursor runs inside the Go gateway, with no SDK process or inference store."""

from copy import deepcopy
from pathlib import Path
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[2]


def retired_contract_errors(sources):
    retired = ("cursor_sdk_messages", "cursor-sdk-service.md")
    return [
        f"{path}: retired Cursor contract {marker}"
        for path, content in sources.items()
        for marker in retired
        if marker in content
    ]


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
    def test_current_docs_do_not_advertise_retired_cursor_contracts(self):
        paths = [ROOT / "CLAUDE.md", *(ROOT / "docs").rglob("*.md")]
        sources = {str(path.relative_to(ROOT)): path.read_text() for path in paths}
        self.assertEqual([], retired_contract_errors(sources))

    def test_retired_contract_references_are_rejected(self):
        current = "Use cursor_oauth_messages; see cursor-oauth-service.md"
        self.assertEqual([], retired_contract_errors({"design.md": current}))
        for before, after in [
            ("cursor_oauth_messages", "cursor_sdk_messages"),
            ("cursor-oauth-service.md", "cursor-sdk-service.md"),
        ]:
            with self.subTest(retired=after):
                self.assertTrue(retired_contract_errors({"design.md": current.replace(before, after)}))

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
