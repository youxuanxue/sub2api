#!/usr/bin/env python3
"""Unit tests for check-host-bbr-ssot.py (repo wiring, no AWS)."""

from __future__ import annotations

import importlib.util
import pathlib
import tempfile
import unittest

REPO = pathlib.Path(__file__).resolve().parents[2]
CHECK_PATH = REPO / "scripts/checks/check-host-bbr-ssot.py"


def _load():
    spec = importlib.util.spec_from_file_location("check_host_bbr_ssot", CHECK_PATH)
    mod = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(mod)
    return mod


class HostBbrSsotTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.mod = _load()

    def test_repo_wiring_is_green(self) -> None:
        self.assertEqual(self.mod.check(quiet=True), 0)

    def test_ec2_section_is_compact_not_full_script(self) -> None:
        body = (REPO / "deploy/aws/stage0/stage0-ec2-bootstrap.sh").read_text(encoding="utf-8")
        section = self.mod._ec2_bbr_section(body)
        self.assertIsNotNone(section)
        self.assertNotIn("<<'BBREOF'", section)
        self.assertIn("net.ipv4.tcp_congestion_control=bbr", section)
        self.assertIn("modprobe tcp_bbr", section)

    def test_missing_ec2_section_fails(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            tmp_path = pathlib.Path(tmp)
            fake_boot = tmp_path / "stage0-ec2-bootstrap.sh"
            fake_boot.write_text("# no bbr here\n", encoding="utf-8")
            orig = self.mod.EC2_BOOTSTRAP
            try:
                self.mod.EC2_BOOTSTRAP = fake_boot
                self.assertEqual(self.mod.check(quiet=True), 1)
            finally:
                self.mod.EC2_BOOTSTRAP = orig


if __name__ == "__main__":
    unittest.main()
