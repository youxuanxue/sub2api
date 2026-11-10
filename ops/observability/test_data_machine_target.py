#!/usr/bin/env python3
"""Data-machine SSM identity comes from the site matrix and is verified before use."""
from __future__ import annotations

import json
import pathlib
import subprocess
import sys
import tempfile
import unittest

HERE = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import data_machine_target as target  # noqa: E402

WANT = {
    "prod": ("mi-044ba9da6453c1557", "tokenkey-usw-data"),
    "cn-prod": ("mi-084a456b3dee71bb4", "tokenkey-cn-data"),
}
APP_INSTANCE = {"prod": "mi-080ed76dc6d97d8b5", "cn-prod": "mi-06568963b4b3c3239"}


def ssm(name: str, status: str = "Online", returncode: int = 0):
    def run(cmd, **_):
        body = {"InstanceInformationList": [{"PingStatus": status, "Name": name, "PlatformType": "Linux"}]}
        return subprocess.CompletedProcess(cmd, returncode, json.dumps(body), "denied" if returncode else "")
    return run


class DataMachineTargetTest(unittest.TestCase):
    def test_matrices_name_a_data_machine_distinct_from_the_app_host(self) -> None:
        for site, (instance, name) in WANT.items():
            with self.subTest(site=site):
                out = target.resolve(site)
                self.assertEqual((out["id"], out["ssm_name"]), (instance, name))
                self.assertEqual(out["ssm_region"], "eu-west-2")
                self.assertNotEqual(out["id"], APP_INSTANCE[site])

    def test_prod_data_hostname_agrees_across_matrix_sections(self) -> None:
        doc = json.loads(target.MATRICES["prod"].read_text())
        self.assertEqual(doc["data_machine"]["hostname"], doc["host_watch"]["data_hostname"])

    def test_incomplete_matrix_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            matrix = pathlib.Path(tmp) / "m.json"
            matrix.write_text(json.dumps({"data_machine": {"expected_ssm_instance_id": "i-0abc"}}))
            with self.assertRaisesRegex(target.TargetError, "identity missing"):
                target.resolve("prod", matrix)
            matrix.write_text(json.dumps({"data_ssm_instance_id": WANT["cn-prod"][0]}))
            with self.assertRaisesRegex(target.TargetError, "identity missing"):
                target.resolve("cn-prod", matrix)

    def test_verify_requires_the_registered_name_online(self) -> None:
        resolved = target.resolve("prod")
        target.verify(resolved, run=ssm("tokenkey-usw-data"))
        for run, message in (
            (ssm("tokenkey-usw-rehearsal-20261021"), "want=tokenkey-usw-data"),
            (ssm("tokenkey-usw-data", status="ConnectionLost"), "status=ConnectionLost"),
            (ssm("tokenkey-usw-data", returncode=255), "lookup failed"),
        ):
            with self.subTest(message=message), self.assertRaisesRegex(target.NotReady, message):
                target.verify(resolved, run=run)

    def test_cli_github_output_carries_only_id_and_region(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            output = pathlib.Path(tmp) / "out"
            proc = subprocess.run(
                [sys.executable, str(HERE / "data_machine_target.py"), "--site", "cn-prod",
                 "--github-output", str(output)],
                capture_output=True, text=True, check=False)
            self.assertEqual(proc.returncode, 0, proc.stderr)
            self.assertEqual(output.read_text(), f"id={WANT['cn-prod'][0]}\nssm_region=eu-west-2\n")

    def test_workflow_reads_data_logs_and_capacity_on_the_data_machine(self) -> None:
        workflow = (HERE.parents[1] / ".github/workflows/ops-daily-diagnostics.yml").read_text()
        self.assertIn("python3 ops/observability/data_machine_target.py --site prod --verify --github-output", workflow)
        self.assertIn("INSTANCE_ID: ${{ steps.logtarget.outputs.id }}", workflow)
        cap = next(l for l in workflow.splitlines() if "probe-data-layer-capacity.sh" in l and "run-probe.sh" in l)
        self.assertIn('--target "${PROBE_TARGET}-data"', cap)
        safety = next(l for l in workflow.splitlines() if "probe-data-layer-safety.sh" in l and "run-probe.sh" in l)
        self.assertIn('--target "${PROBE_TARGET}-data"', safety)
        self.assertIn('data_machine_target.py --site "$PROBE_TARGET" --verify', workflow)
        targets = json.loads((HERE / "diagnostics-targets.json").read_text())["include"]
        self.assertEqual({t["probe_target"] for t in targets}, set(WANT))


if __name__ == "__main__":
    unittest.main()
