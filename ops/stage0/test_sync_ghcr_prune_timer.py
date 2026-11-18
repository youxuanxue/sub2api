"""Unit tests for TAT-default GHCR prune timer sync."""

from __future__ import annotations

import json
import tempfile
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
import sys

if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

from deploy.domestic import remote_exec
from ops.stage0 import sync_ghcr_prune_timer as sync


class SyncGhcrPruneTimerTest(unittest.TestCase):
    def test_build_sync_script_installs_canonical_units(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            daily = Path(tmp) / "tokenkey-ghcr-prune-daily.sh"
            daily.write_text("#!/bin/bash\necho hi\n", encoding="utf-8")
            body = sync.build_sync_script(daily=daily, template_sha="abc123")
        self.assertIn("--install-units", body)
        self.assertIn("--selftest", body)
        self.assertIn("tokenkey-ghcr-prune-daily.timer", body)
        self.assertIn("deploy/stage0@abc123", body)
        self.assertIn("base64 -d | sudo tee /usr/local/bin/tokenkey-ghcr-prune-daily.sh", body)

    def test_sync_apps_fans_out_via_tat_and_fails_closed(self) -> None:
        nodes = [
            remote_exec.Node(
                name="app-a",
                role="app",
                lighthouse_instance_id="lhins-aaa",
                private_ip="10.0.0.1",
                ssm_instance_id="mi-aaa",
                tat_region="na-siliconvalley",
            ),
            remote_exec.Node(
                name="app-b",
                role="app",
                lighthouse_instance_id="lhins-bbb",
                private_ip="10.0.0.2",
                ssm_instance_id="mi-bbb",
                tat_region="na-siliconvalley",
            ),
        ]
        calls: list[tuple[str, str]] = []

        def fake_run(node, script, *, timeout, via, runner=None):
            calls.append((node.name, via))
            status = "Success" if node.name == "app-a" else "Failed"
            return remote_exec.Result("tat", status, 0 if status == "Success" else 1, "", "boom")

        with mock.patch.object(remote_exec, "app_nodes", return_value=nodes), mock.patch.object(
            remote_exec, "run", side_effect=fake_run
        ), tempfile.TemporaryDirectory() as tmp:
            daily = Path(tmp) / "daily.sh"
            daily.write_text("#!/bin/bash\n", encoding="utf-8")
            with mock.patch.object(sync, "DAILY_SRC", daily), self.assertRaises(
                remote_exec.RemoteExecError
            ) as raised:
                sync.sync_apps(via="tat", template_sha="deadbeef")
        self.assertEqual(calls, [("app-a", "tat"), ("app-b", "tat")])
        self.assertIn("app-b", str(raised.exception))

    def test_main_writes_receipt(self) -> None:
        rows = [{"node": "app-old", "channel": "tat", "status": "Success", "exit_code": 0}]
        with tempfile.TemporaryDirectory() as tmp:
            receipt = Path(tmp) / "receipt.json"
            with mock.patch.object(sync, "sync_apps", return_value=rows):
                rc = sync.main(["--receipt-out", str(receipt), "--via", "tat"])
            self.assertEqual(rc, 0)
            payload = json.loads(receipt.read_text(encoding="utf-8"))
        self.assertEqual(payload["via"], "tat")
        self.assertEqual(payload["nodes"], rows)


if __name__ == "__main__":
    unittest.main()
