"""Tests for scripts/checks/lightsail-oidc-perm-coverage.py.

stdlib-only.
"""
from __future__ import annotations

import importlib.util
import pathlib
import subprocess
import sys
import tempfile
import textwrap
import unittest

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
SCRIPT = REPO_ROOT / "scripts/checks/lightsail-oidc-perm-coverage.py"


def _load_module():
    """Load the script as a module (its file name uses dashes, so importlib)."""
    spec = importlib.util.spec_from_file_location("lightsail_perm_coverage", SCRIPT)
    mod = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(mod)
    return mod


class LightsailOidcPermCoverageTests(unittest.TestCase):
    """The coverage gate is text-level. Tests pin: action list non-empty,
    detection of missing actions, real repo passes."""

    def setUp(self):
        self.mod = _load_module()

    def test_expected_actions_nonempty(self):
        """Forgetting to populate EXPECTED_ACTIONS would make every coverage
        check trivially pass — guard against that emptying."""
        self.assertGreaterEqual(len(self.mod.EXPECTED_ACTIONS), 10,
                                "EXPECTED_ACTIONS must enumerate the real action set")

    def test_expected_action_format(self):
        """Each entry must be (action_string, notes) and the action must look
        like an IAM action — `service:Action`."""
        for entry in self.mod.EXPECTED_ACTIONS:
            self.assertIsInstance(entry, tuple)
            self.assertEqual(len(entry), 2)
            action, notes = entry
            self.assertRegex(action, r"^[a-z][a-z0-9-]+:[A-Z][A-Za-z]+$",
                             f"action {action!r} doesn't look like an IAM action")
            self.assertIsInstance(notes, str)

    def test_missing_action_detected(self):
        # Synthetic empty policies → every expected action reported as missing.
        missing = self.mod._missing_actions(
            self.mod.EXPECTED_ACTIONS,
            addon_text="(no policies)",
            base_text="(no policies)",
        )
        self.assertEqual(len(missing), len(self.mod.EXPECTED_ACTIONS))

    def test_addon_covers_passes(self):
        # Synthetic "addon" contains every action; base empty. All ok.
        synth = "\n".join(a for a, _ in self.mod.EXPECTED_ACTIONS)
        missing = self.mod._missing_actions(
            self.mod.EXPECTED_ACTIONS,
            addon_text=synth,
            base_text="(empty)",
        )
        self.assertEqual(missing, [])

    def test_base_covers_passes(self):
        # Inverse: base contains everything, addon is empty.
        synth = "\n".join(a for a, _ in self.mod.EXPECTED_ACTIONS)
        missing = self.mod._missing_actions(
            self.mod.EXPECTED_ACTIONS,
            addon_text="(empty)",
            base_text=synth,
        )
        self.assertEqual(missing, [])

    def test_partial_coverage_reports_only_missing(self):
        # Half in addon, half in base, one truly missing.
        expected = list(self.mod.EXPECTED_ACTIONS)
        if len(expected) < 3:
            self.skipTest("need at least 3 actions")
        addon_text = "\n".join(a for a, _ in expected[:1])
        base_text = "\n".join(a for a, _ in expected[1:-1])
        missing = self.mod._missing_actions(expected, addon_text, base_text)
        self.assertEqual(len(missing), 1)
        self.assertEqual(missing[0][0], expected[-1][0])

    def test_real_repo_passes(self):
        """Smoke: every expected action must be granted by the actual
        committed policies. If this fails, the addon or base CFN dropped an
        action our workflow depends on (or the workflow gained an action that
        wasn't added to EXPECTED_ACTIONS)."""
        proc = subprocess.run(
            [sys.executable, str(SCRIPT)],
            capture_output=True, text=True, check=False,
        )
        self.assertEqual(proc.returncode, 0, f"stdout:{proc.stdout}\nstderr:{proc.stderr}")

    def test_real_repo_passes_via_subprocess(self):
        """Verify the script's CLI surface (not just internals) returns 0 on
        the real repo."""
        proc = subprocess.run(
            [sys.executable, str(SCRIPT), "--quiet"],
            capture_output=True, text=True, check=False,
        )
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn("ok:", proc.stdout)


if __name__ == "__main__":
    unittest.main()
