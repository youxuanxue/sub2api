#!/usr/bin/env python3
"""Smoke tests for ops/stage0/verify-edge-lightsail-network.sh — validation only."""
from __future__ import annotations

import pathlib
import subprocess
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "verify-edge-lightsail-network.sh"
_PROVISION = (
    pathlib.Path(__file__).resolve().parents[2]
    / "deploy"
    / "aws"
    / "lightsail"
    / "provision-edge.sh"
)
_SHADOW = (
    pathlib.Path(__file__).resolve().parents[1]
    / "lightsail"
    / "provision-shadow-small.sh"
)

# Edge Lightsail hardened baseline port set (must stay in sync across provision + verify).
_UDP_34567 = "fromPort=34567,toPort=34567,protocol=udp,cidrs=0.0.0.0/0"
_TCP_443 = "fromPort=443,toPort=443,protocol=tcp,cidrs=0.0.0.0/0"
_TCP_8443 = "fromPort=8443,toPort=8443,protocol=tcp,cidrs=0.0.0.0/0"


class VerifyEdgeLightsailNetworkTest(unittest.TestCase):
    def test_syntax_clean(self) -> None:
        proc = subprocess.run(
            ["bash", "-n", str(_SCRIPT)],
            capture_output=True, text=True, check=False,
        )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)

    def test_help_exits_zero(self) -> None:
        proc = subprocess.run(
            ["bash", str(_SCRIPT), "--help"],
            capture_output=True, text=True, check=False,
        )
        self.assertEqual(proc.returncode, 0)
        self.assertIn("verify-edge-lightsail-network.sh", proc.stdout)
        self.assertIn("UDP 34567", proc.stdout)

    def test_missing_arg_shows_usage(self) -> None:
        proc = subprocess.run(
            ["bash", str(_SCRIPT)],
            capture_output=True, text=True, check=False,
        )
        self.assertEqual(proc.returncode, 1)

    def test_baseline_includes_udp_34567(self) -> None:
        """Positive: enforce/provision declare UDP 34567 in the public port set."""
        for path in (_SCRIPT, _PROVISION, _SHADOW):
            text = path.read_text(encoding="utf-8")
            self.assertIn(_TCP_443, text, msg=f"{path.name} missing TCP 443")
            self.assertIn(_TCP_8443, text, msg=f"{path.name} missing TCP 8443")
            self.assertIn(_UDP_34567, text, msg=f"{path.name} missing UDP 34567")

    def test_baseline_does_not_open_public_22_or_80(self) -> None:
        """Negative: put-instance-public-ports must not declare public TCP 22/80."""
        for path in (_SCRIPT, _PROVISION, _SHADOW):
            text = path.read_text(encoding="utf-8")
            # Only look at put-instance-public-ports blocks (avoid comment noise).
            blocks = []
            lines = text.splitlines()
            for i, line in enumerate(lines):
                if "put-instance-public-ports" in line:
                    blocks.append("\n".join(lines[i : i + 12]))
            self.assertTrue(blocks, msg=f"{path.name}: no put-instance-public-ports")
            for block in blocks:
                self.assertNotIn("fromPort=22,", block, msg=f"{path.name}: opens 22")
                self.assertNotIn("fromPort=80,", block, msg=f"{path.name}: opens 80")


if __name__ == "__main__":
    unittest.main()
