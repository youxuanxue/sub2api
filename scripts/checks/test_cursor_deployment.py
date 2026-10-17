"""Retired Cursor support must not leave a dedicated runtime or bridge secrets."""

from copy import deepcopy
from pathlib import Path
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[2]


RETIRED_CONTRACTS = (
    "cursor_sdk_messages", "cursor_oauth_messages", "cursor-sdk-service.md",
    "cursor-oauth-service.md", "design-cursor-tool-gateway-compat.md",
    "task-breakdown-cursor-tool-gateway-compat.md", "spec-delta-cursor-fable-display.md",
    "cursor-errors-ssot-20260915.md", "cursor-messages-validation-20260914.md",
    "cursor-upstream-revalidation-20260916.md", "US-060-cursor-client-only-tools.md",
    "cursorResponsesUnsupportedFields", "TestCursorResponsesUnsupportedFields",
    "scripts/cursor/", "deploy/cursor/", "internal/integration/cursor/",
)


def retired_contract_errors(sources):
    return [
        f"{path}: retired Cursor contract {marker}"
        for path, content in sources.items()
        # The deletion ledger is historical evidence, never an execution guide.
        if path != "docs/DEPRECATIONS.md"
        for marker in RETIRED_CONTRACTS
        if marker in path or marker in content
    ]


def compose_sources(root):
    for path in sorted({*root.rglob("*.yml"), *root.rglob("*.yaml")}):
        # Inspect keys and strings only; CloudFormation tags in other deployment
        # YAML must remain inert rather than requiring custom constructors.
        document = yaml.load(path.read_text(), Loader=yaml.BaseLoader)
        if isinstance(document, dict) and isinstance(document.get("services"), dict):
            yield path, document["services"]


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
        paths = [ROOT / "CLAUDE.md", ROOT / "AGENTS.md", *ROOT.glob("README*.md")]
        for directory in ("docs", ".testing", ".cursor/skills"):
            paths.extend((ROOT / directory).rglob("*.md"))
        paths.extend((ROOT / "ops/issue-watchdog").glob("*.json"))
        sources = {str(path.relative_to(ROOT)): path.read_text() for path in paths}
        self.assertEqual([], retired_contract_errors(sources))

    def test_retired_contract_references_are_rejected(self):
        current = "Use the shared protocol router; see cursor-retirement.md"
        self.assertEqual([], retired_contract_errors({"design.md": current}))
        for marker in RETIRED_CONTRACTS:
            with self.subTest(retired=marker):
                source = f"Use {marker}"
                self.assertEqual(
                    [f"design.md: retired Cursor contract {marker}"],
                    retired_contract_errors({"design.md": source}),
                )
                self.assertTrue(retired_contract_errors({
                    "history.md": f"---\nstatus: archived\n---\n{source}",
                }))
                self.assertEqual([], retired_contract_errors({"docs/DEPRECATIONS.md": source}))
                self.assertEqual(
                    [f"design.md: retired Cursor contract {marker}"],
                    retired_contract_errors({
                        "design.md": f"---\nstatus: approved\n---\n{source}",
                    }),
                )

    def test_release_services_have_no_cursor_runtime_dependency(self):
        for path, services in compose_sources(ROOT / "deploy"):
            with self.subTest(path=path.relative_to(ROOT)):
                self.assertEqual([], deployment_errors(services))

    def test_local_and_nested_compose_configs_are_checked(self):
        from tempfile import TemporaryDirectory

        with TemporaryDirectory() as temp:
            root = Path(temp)
            (root / "nested").mkdir()
            for name in ("local.compose.yml", "nested/compose.yaml"):
                (root / name).write_text("services:\n  cursor-bridge:\n    image: cursor:test\n")
            (root / "inventory.yaml").write_text("targets: []\n")
            (root / "cloudformation.yaml").write_text("Outputs:\n  role: !Ref Role\n")
            rejected = {str(path.relative_to(root)): deployment_errors(services)
                        for path, services in compose_sources(root)}
            self.assertEqual(rejected, {
                "local.compose.yml": ["Retired Cursor runtime must not be deployed"],
                "nested/compose.yaml": ["Retired Cursor runtime must not be deployed"],
            })

    def test_old_document_filename_and_agent_navigation_are_rejected(self):
        for path, content in (
            ("docs/ops/cursor-upstream-revalidation-20260916.md", "Restore account 150"),
            ("CLAUDE.md", "Contract: docs/approved/cursor-oauth-service.md#output-limit-compatibility"),
            ("ops/issue-watchdog/upstream.json", "cursorResponsesUnsupportedFields"),
        ):
            with self.subTest(path=path):
                self.assertTrue(retired_contract_errors({path: content}))
        self.assertEqual([], retired_contract_errors({
            "AGENTS.md": "Read .cursor/rules/product-dev.mdc",
            "README.md": "Codex VSCode clients; Grok grok-composer-2.5-fast",
        }))

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
