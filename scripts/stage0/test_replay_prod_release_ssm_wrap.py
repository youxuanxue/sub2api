#!/usr/bin/env python3
"""Replay SSM payload must be dash-safe (Hybrid hosts reject set -o pipefail)."""
from __future__ import annotations

import importlib.util
import json
import pathlib
import sys
import unittest
from unittest import mock

ROOT = pathlib.Path(__file__).resolve().parents[2]
_MOD_PATH = ROOT / "scripts/stage0/replay-prod-release.py"


def _load():
    spec = importlib.util.spec_from_file_location("replay_prod_release", _MOD_PATH)
    mod = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    sys.modules[spec.name] = mod
    spec.loader.exec_module(mod)
    return mod


class ReplayProdReleaseSsmWrapTest(unittest.TestCase):
    def test_remote_wraps_commands_for_dash(self) -> None:
        mod = _load()
        captured: list[list[str]] = []

        def fake_check_output(argv, text=True):
            if argv[-2] == "--parameters" or "--parameters" in argv:
                idx = argv.index("--parameters")
                params = json.loads(argv[idx + 1])
                captured.append(params["commands"])
            return "cmd-id-1\n"

        invocation = {
            "Status": "Success",
            "ResponseCode": 0,
            "StandardOutputContent": json.dumps({"ok": True, "needs_prepare": False}),
        }

        with mock.patch.object(mod.subprocess, "check_output", side_effect=fake_check_output), \
             mock.patch.object(mod.subprocess, "run", return_value=mock.Mock(
                 returncode=0, stdout=json.dumps(invocation), stderr="")), \
             mock.patch.object(mod, "PROD_REGION", "eu-west-2"):
            out = mod.remote("mi-test", "status", "1.8.314", timeout=30)

        self.assertEqual(out, {"ok": True, "needs_prepare": False})
        self.assertEqual(len(captured), 1)
        self.assertEqual(len(captured[0]), 1)
        self.assertTrue(
            captured[0][0].startswith("echo ") and captured[0][0].endswith(" | base64 -d | bash -s"),
            msg=captured[0][0][:120],
        )
        self.assertNotIn("set -euo pipefail", captured[0][0])


if __name__ == "__main__":
    unittest.main()
