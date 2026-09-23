from __future__ import annotations

import json
from pathlib import Path
import subprocess
import sys
import unittest

import yaml

from gemini_web_relay_check import evaluate

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]


def relay(**overrides):
    return {"id": 200, "platform": "gemini", "type": "apikey", "declared_web": True,
            "marker": True, **overrides}


class GeminiWebRelayCheckTest(unittest.TestCase):
    def test_declared_disabled_relay_requires_boolean_capability(self):
        for marker in (None, False, "true", 1):
            with self.subTest(marker=marker):
                report = evaluate({"accounts": [relay(marker=marker, schedulable=False)]})
                self.assertEqual("review", report["verdict"])
                self.assertIn("missing_web_relay_capability", report["violations"][0]["reasons"])
                self.assertEqual(200, report["violations"][0]["account_id"])

    def test_fixed_relay_and_native_accounts_are_aligned(self):
        self.assertEqual("aligned", evaluate({"accounts": [relay()]})["verdict"])
        self.assertEqual("aligned", evaluate({"accounts": []})["verdict"])
        self.assertEqual("aligned", evaluate({"accounts": [relay(
            declared_web=False, marker=None, name="gemini-web", base_url="https://api-us4.tokenkey.dev",
        )]})["verdict"], "names and URLs must not become capability evidence")

    def test_wrong_platform_and_malformed_marker_fail(self):
        for account in (relay(platform="openai"), relay(type="oauth"), relay(marker="true")):
            self.assertEqual("review", evaluate({"accounts": [account]})["verdict"])

    def test_cli_exit_status_and_redaction(self):
        for snapshot, code, verdict in (
            ({"accounts": [relay(marker=None, credentials={"api_key": "do-not-print"})]}, 1, "review"),
            ({"accounts": [relay()]}, 0, "aligned"),
            ({"accounts": [None]}, 2, "setup_error"),
            ({}, 2, "setup_error"),
            ([], 2, "setup_error"),
        ):
            result = subprocess.run([sys.executable, str(HERE / "gemini_web_relay_check.py")],
                                    input=json.dumps(snapshot), text=True, capture_output=True, check=False)
            self.assertEqual(code, result.returncode, result.stderr)
            self.assertEqual(verdict, json.loads(result.stdout)["verdict"])
            self.assertNotIn("do-not-print", result.stdout + result.stderr)

    def test_workflow_blocks_before_prod_mutation_and_preserves_old_rollback(self):
        workflow = yaml.safe_load((ROOT / ".github/workflows/deploy-stage0.yml").read_text())
        steps = workflow["jobs"]["deploy"]["steps"]
        gate_index = next(i for i, step in enumerate(steps)
                          if step.get("name") == "Validate Gemini Web relay declarations before deployment")
        gate = steps[gate_index]
        self.assertFalse(gate.get("continue-on-error", False))
        self.assertEqual("hashFiles('qa-target-release/backend/internal/service/gemini_web_request_tk.go') != ''",
                         gate["if"])
        self.assertIn("--expected-instance-id", gate["run"])
        self.assertIn("--script ops/observability/probe-gemini-web-relay-declarations.sh", gate["run"])
        for step_id in ("legacy_pause", "legacy_boundary", "ssm"):
            mutation_index = next(i for i, step in enumerate(steps) if step.get("id") == step_id)
            self.assertLess(gate_index, mutation_index)


if __name__ == "__main__":
    unittest.main()
