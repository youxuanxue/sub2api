#!/usr/bin/env python3
from __future__ import annotations

import copy
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(REPO_ROOT / "scripts" / "fingerprint"))
import check_client_identity_registry as registry  # noqa: E402
import client_release_watch as release_watch  # noqa: E402


class ClientIdentityRegistryTest(unittest.TestCase):
    def setUp(self) -> None:
        self.data = registry.load_registry()

    def test_live_registry_is_valid_against_pin_readers(self) -> None:
        errors = registry.validate_registry(
            self.data,
            pin_reader_keys=set(release_watch.PIN_READERS),
        )
        self.assertEqual([], errors)

    def test_each_release_watched_identity_is_unique(self) -> None:
        ids = [row["id"] for row in self.data["identities"]]
        self.assertEqual(len(ids), len(set(ids)))
        self.assertEqual(set(ids), {spec.id for spec in release_watch.PLATFORM_SPECS})

    def test_non_wire_modes_do_not_claim_wire_or_production(self) -> None:
        coverage = {row["id"]: row for row in registry.coverage_rows(self.data)}
        for identity in self.data["identities"]:
            if identity["evidence_mode"] in {"version_only", "advisory_release", "local_binary_static"}:
                self.assertEqual("no", coverage[identity["id"]]["wire"])
            if identity["evidence_mode"] in {"version_only", "advisory_release"}:
                self.assertEqual("no", coverage[identity["id"]]["production"])

    def test_validator_rejects_missing_owner_symbol(self) -> None:
        bad = copy.deepcopy(self.data)
        bad["identities"][0]["compile_owner"]["symbol"] = "DefinitelyMissingOwnerSymbol"
        errors = registry.validate_registry(bad, pin_reader_keys=set(release_watch.PIN_READERS))
        self.assertTrue(any("DefinitelyMissingOwnerSymbol" in error for error in errors))

    def test_cli_rejects_unknown_pin_reader(self) -> None:
        bad = copy.deepcopy(self.data)
        bad["identities"][0]["pin_reader"] = "definitely-missing"
        with tempfile.TemporaryDirectory() as temp_dir:
            path = Path(temp_dir) / "registry.json"
            path.write_text(json.dumps(bad), encoding="utf-8")
            result = subprocess.run(
                [sys.executable, str(registry.__file__), "--registry", str(path), "--quiet"],
                text=True,
                capture_output=True,
                check=False,
            )
        self.assertEqual(1, result.returncode)
        self.assertIn("unknown pin_reader", result.stderr)

    def test_registry_contains_no_current_version_state(self) -> None:
        rendered = json.dumps(self.data)
        self.assertNotRegex(rendered, r'"(?:current_version|pinned|status|drift)"\s*:')


if __name__ == "__main__":
    unittest.main()
