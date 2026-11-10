#!/usr/bin/env python3
"""log_dump reads Postgres/Redis logs on the data machine, the gateway on the app host."""
from __future__ import annotations

import json
import pathlib
import subprocess
import sys
import tempfile
import unittest

HERE = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import log_dump_target as target  # noqa: E402

APP = ("mi-080ed76dc6d97d8b5", "eu-west-2")


class LogDumpTargetTest(unittest.TestCase):
    def test_data_containers_go_to_the_matrix_data_machine(self) -> None:
        data = json.loads(target.MATRIX.read_text())["data_machine"]
        for container in ("tokenkey-postgres", "tokenkey-redis"):
            with self.subTest(container=container):
                out = target.resolve(container, *APP)
                self.assertEqual(out["id"], data["expected_ssm_instance_id"])
                self.assertEqual(out["ssm_region"], data["ssm_region"])
                self.assertNotEqual(out["id"], APP[0])

    def test_gateway_and_caddy_stay_on_the_app_host(self) -> None:
        for container in ("tokenkey", "tokenkey-caddy"):
            with self.subTest(container=container):
                self.assertEqual(target.resolve(container, *APP)["id"], APP[0])

    def test_missing_data_identity_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            matrix = pathlib.Path(tmp) / "m.json"
            matrix.write_text(json.dumps({"target": {}}))
            with self.assertRaisesRegex(ValueError, "data machine SSM identity missing"):
                target.resolve("tokenkey-postgres", *APP, matrix=matrix)
            with self.assertRaisesRegex(ValueError, "unknown log_dump container"):
                target.resolve("tokenkey-other", *APP, matrix=matrix)

    def test_cli_writes_one_id_per_step_output(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            output = pathlib.Path(tmp) / "out"
            proc = subprocess.run(
                [sys.executable, str(HERE / "log_dump_target.py"), "--container", "tokenkey-redis",
                 "--app-instance", APP[0], "--app-region", APP[1], "--github-output", str(output)],
                capture_output=True, text=True, check=False)
            self.assertEqual(proc.returncode, 0, proc.stderr)
            lines = output.read_text().splitlines()
        self.assertEqual([line.split("=")[0] for line in lines], ["id", "ssm_region"])
        self.assertIn("-> data host", proc.stdout)

    def test_workflow_sends_log_dump_through_the_routed_instance(self) -> None:
        workflow = (HERE.parents[1] / ".github/workflows/ops-daily-diagnostics.yml").read_text()
        self.assertIn("python3 ops/observability/log_dump_target.py", workflow)
        self.assertIn("INSTANCE_ID: ${{ steps.logtarget.outputs.id }}", workflow)
        self.assertIn("AWS_REGION: ${{ steps.logtarget.outputs.ssm_region }}", workflow)

    def test_daily_capacity_probe_runs_on_the_data_machine(self) -> None:
        # The runway is the data machine's disk; the app host df is the retired
        # data directory and is only a fallback guard in the verdict.
        workflow = (HERE.parents[1] / ".github/workflows/ops-daily-diagnostics.yml").read_text()
        line = next(l for l in workflow.splitlines() if "probe-data-layer-capacity.sh" in l and "run-probe.sh" in l)
        self.assertIn('--target "${PROBE_TARGET}-data"', line)
        targets = json.loads((HERE / "diagnostics-targets.json").read_text())["include"]
        self.assertEqual({t["probe_target"] for t in targets}, {"prod", "cn-prod"})


if __name__ == "__main__":
    unittest.main()
