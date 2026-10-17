"""Retired Cursor support must not leave a dedicated runtime or bridge secrets."""

from copy import deepcopy
from pathlib import Path
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[2]


def retired_contract_errors(sources):
    retired = ("cursor_sdk_messages", "cursor_oauth_messages", "cursor-sdk-service.md")
    return [
        f"{path}: retired Cursor contract {marker}"
        for path, content in sources.items()
        if not is_archived(content)
        for marker in retired
        if marker in content
    ]


def is_archived(content):
    if not content.startswith("---\n"):
        return False
    sections = content.split("---", 2)
    if len(sections) != 3:
        return False
    metadata = yaml.safe_load(sections[1])
    return isinstance(metadata, dict) and metadata.get("status") == "archived"


def deployment_errors(services):
    errors = []
    for name, service in services.items():
        if "cursor" in name.lower():
            errors.append("Retired Cursor runtime must not be deployed")
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
        current = "Use the shared protocol router; see cursor-retirement.md"
        self.assertEqual([], retired_contract_errors({"design.md": current}))
        for marker in ("cursor_oauth_messages", "cursor_sdk_messages", "cursor-sdk-service.md"):
            with self.subTest(retired=marker):
                source = f"Use {marker}"
                self.assertEqual(
                    [f"design.md: retired Cursor contract {marker}"],
                    retired_contract_errors({"design.md": source}),
                )
                self.assertEqual([], retired_contract_errors({
                    "history.md": f"---\nstatus: archived\n---\n{source}",
                }))
                self.assertEqual(
                    [f"design.md: retired Cursor contract {marker}"],
                    retired_contract_errors({
                        "design.md": f"---\nstatus: approved\n---\n{source}",
                    }),
                )

    def test_release_services_have_no_cursor_runtime_dependency(self):
        for path in sorted((ROOT / "deploy").rglob("docker-compose*.yml")):
            with self.subTest(path=path.relative_to(ROOT)):
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
