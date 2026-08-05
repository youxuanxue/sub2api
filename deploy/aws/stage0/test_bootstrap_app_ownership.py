#!/usr/bin/env python3
"""Contract test for Stage0 persistent app directory ownership."""
from __future__ import annotations

import pathlib
import unittest


BOOTSTRAP = pathlib.Path(__file__).with_name("stage0-ec2-bootstrap.sh")


class BootstrapAppOwnershipTest(unittest.TestCase):
    def test_app_dir_is_prepared_for_uid_gid_1000(self) -> None:
        text = BOOTSTRAP.read_text(encoding="utf-8")
        self.assertIn(
            "install -d -m 0755 -o 1000 -g 1000 /var/lib/tokenkey/app",
            text,
        )


if __name__ == "__main__":
    unittest.main()
