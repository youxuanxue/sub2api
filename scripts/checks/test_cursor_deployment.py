"""Keep the optional Cursor supply out of the gateway's startup dependencies."""

from copy import deepcopy
from pathlib import Path
import unittest

import yaml


ROOT = Path(__file__).resolve().parents[2]


def deployment_errors(services):
    errors = []
    pending = ["tokenkey"]
    visited = set()
    while pending:
        name = pending.pop()
        if name in visited:
            continue
        visited.add(name)
        if name == "cursor-bridge":
            errors.append("gateway startup depends on Cursor")
        pending.extend(services.get(name, {}).get("depends_on", {}))
    bridge = services["cursor-bridge"]
    if not 0 < float(bridge.get("cpus", 0)) <= 1:
        errors.append("Cursor CPU budget must be bounded")
    if not 0 < int(bridge.get("pids_limit", 0)) <= 256:
        errors.append("Cursor process budget must be bounded")
    if bridge.get("mem_limit") != "2g":
        errors.append("Cursor memory budget must be 2g")
    if bridge.get("ports") or bridge.get("network_mode") == "host":
        errors.append("Cursor must remain internal")
    if bridge.get("read_only") is not True or bridge.get("cap_drop") != ["ALL"]:
        errors.append("Cursor container privileges changed")
    if "${CURSOR_BRIDGE_IMAGE:?" not in bridge.get("image", ""):
        errors.append("Cursor requires an explicit image selection")
    return errors


class CursorDeploymentTest(unittest.TestCase):
    def setUp(self):
        with (ROOT / "deploy/aws/stage0/docker-compose.cursor.yml").open() as source:
            self.services = yaml.safe_load(source)["services"]

    def test_optional_supply_cannot_block_gateway_startup(self):
        self.assertEqual([], deployment_errors(self.services))

    def test_direct_and_transitive_startup_dependencies_are_rejected(self):
        for dependency in ["cursor-bridge", "proxy"]:
            with self.subTest(dependency=dependency):
                services = deepcopy(self.services)
                services["tokenkey"]["depends_on"] = {dependency: {"condition": "service_healthy"}}
                services["proxy"] = {"depends_on": ["cursor-bridge"]}
                self.assertIn("gateway startup depends on Cursor", deployment_errors(services))

    def test_unbounded_resources_and_public_exposure_are_rejected(self):
        for key, value in [("cpus", 0), ("pids_limit", -1), ("mem_limit", "8g"),
                           ("ports", ["3927:3927"]), ("network_mode", "host"),
                           ("read_only", False), ("image", "cursor:latest")]:
            with self.subTest(key=key):
                services = deepcopy(self.services)
                services["cursor-bridge"][key] = value
                self.assertTrue(deployment_errors(services))


if __name__ == "__main__":
    unittest.main()
