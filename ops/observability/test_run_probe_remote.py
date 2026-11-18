#!/usr/bin/env python3
"""Unit tests for TAT transport helper used by run-probe."""

from __future__ import annotations

import importlib.util
import io
import pathlib
import sys
import unittest
from unittest import mock

ROOT = pathlib.Path(__file__).resolve().parents[2]
if str(ROOT) not in sys.path:
    sys.path.insert(0, str(ROOT))

from deploy.domestic import remote_exec  # noqa: E402

_SPEC = importlib.util.spec_from_file_location(
    "run_probe_remote", pathlib.Path(__file__).with_name("run_probe_remote.py")
)
assert _SPEC and _SPEC.loader
mod = importlib.util.module_from_spec(_SPEC)
_SPEC.loader.exec_module(mod)


class RunProbeRemoteTest(unittest.TestCase):
    def test_tat_success_prints_stdout(self) -> None:
        node = remote_exec.Node(
            name="app-old",
            role="app",
            lighthouse_instance_id="lhins-fcwpwymt",
            private_ip="10.4.0.16",
            ssm_instance_id="mi-080ed76dc6d97d8b5",
            tat_region="na-siliconvalley",
            ssm_region="eu-west-2",
        )
        result = remote_exec.Result("tat", "Success", 0, '{"ok":true}', "")
        stdin = io.StringIO("echo hi\n")
        with mock.patch.object(mod.remote_exec, "load_nodes", return_value={"app-old": node}), mock.patch.object(
            mod.remote_exec, "run", return_value=result
        ) as run, mock.patch("sys.stdin", stdin), mock.patch(
            "sys.argv",
            ["run_probe_remote.py", "--target", "prod", "--node", "app-old", "--via", "tat"],
        ):
            rc = mod.main()
        self.assertEqual(rc, 0)
        run.assert_called_once()
        kwargs = run.call_args.kwargs
        self.assertEqual(kwargs["via"], "tat")
        self.assertGreater(kwargs["output_bytes"], 8 * 1024)

    def test_data_target_selects_data_node(self) -> None:
        node = remote_exec.Node(
            name="data",
            role="data",
            lighthouse_instance_id="lhins-p43i2kxh",
            private_ip="10.4.0.3",
            ssm_instance_id="mi-044ba9da6453c1557",
            tat_region="na-siliconvalley",
            ssm_region="eu-west-2",
        )
        result = remote_exec.Result("tat", "Success", 0, "ok", "")
        stdin = io.StringIO("true\n")
        with mock.patch.object(mod.remote_exec, "load_nodes", return_value={"data": node}), mock.patch.object(
            mod.remote_exec, "run", return_value=result
        ) as run, mock.patch("sys.stdin", stdin), mock.patch(
            "sys.argv",
            ["run_probe_remote.py", "--target", "prod-data", "--via", "tat"],
        ):
            rc = mod.main()
        self.assertEqual(rc, 0)
        self.assertEqual(run.call_args.args[0].name, "data")


if __name__ == "__main__":
    unittest.main()
