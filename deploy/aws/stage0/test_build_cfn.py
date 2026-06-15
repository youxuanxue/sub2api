#!/usr/bin/env python3
"""Size gates for deploy/aws/stage0/build-cfn.sh outputs (stdlib-only)."""
from __future__ import annotations

import gzip
import pathlib
import re
import subprocess
import unittest

_REPO = pathlib.Path(__file__).resolve().parents[3]
STAGE0 = _REPO / "deploy/aws/stage0"
CFN_MAIN = _REPO / "deploy/aws/cloudformation/stage0-single-ec2.yaml"

EC2_USERDATA_LIMIT = 16384
SSM_STANDARD_LIMIT = 4096


def _extract_userdata_body(cfn_text: str) -> str:
    m = re.search(
        r"UserData:\s*\n\s*Fn::Base64: !Sub \|\s*\n(.*?)(?=\n\n  # -+\n  # Persistent data volume|\n  [A-Z])",
        cfn_text,
        re.S,
    )
    if not m:
        raise AssertionError("UserData block not found")
    return m.group(1)


class BuildCfnSizeTest(unittest.TestCase):
    def test_prod_userdata_under_ec2_limit(self) -> None:
        body = _extract_userdata_body(CFN_MAIN.read_text())
        self.assertLessEqual(
            len(body.encode()),
            EC2_USERDATA_LIMIT,
            f"prod UserData body is {len(body.encode())} bytes; EC2 limit is {EC2_USERDATA_LIMIT}",
        )

    def test_prod_userdata_shebang_is_first_line(self) -> None:
        body = _extract_userdata_body(CFN_MAIN.read_text())
        first = next((ln.strip() for ln in body.splitlines() if ln.strip()), "")
        self.assertEqual(
            first,
            "#!/bin/bash",
            "cloud-init only runs UserData as a shell script when shebang is the first non-empty line",
        )

    def test_bootstrap_gzip_b64_fits_two_ssm_standard_parts(self) -> None:
        raw = (STAGE0 / "stage0-ec2-bootstrap.sh").read_bytes()
        b64 = __import__("base64").b64encode(gzip.compress(raw, 9)).decode()
        self.assertLessEqual(len(b64), SSM_STANDARD_LIMIT * 2)
        self.assertLessEqual(len(b64[:SSM_STANDARD_LIMIT]), SSM_STANDARD_LIMIT)
        self.assertLessEqual(len(b64[SSM_STANDARD_LIMIT:]), SSM_STANDARD_LIMIT)

    def test_build_cfn_check_passes(self) -> None:
        proc = subprocess.run(
            ["bash", str(STAGE0 / "build-cfn.sh"), "--check"],
            cwd=_REPO,
            capture_output=True,
            text=True,
            check=False,
        )
        self.assertEqual(
            proc.returncode,
            0,
            msg=f"build-cfn --check failed:\nstdout={proc.stdout}\nstderr={proc.stderr}",
        )

    def test_build_cfn_check_detects_source_drift(self) -> None:
        # Negative path: the content-based --check must FAIL when a source script
        # changes but its embedded CFN blob is not regenerated — that drift gate is
        # the whole point of --check. (Decodes the committed blob and compares to the
        # now-tampered source, so it stays robust to gzip/zlib *version* differences.)
        src = STAGE0 / "tokenkey-pgdump.sh"
        original = src.read_bytes()
        try:
            src.write_bytes(original + b"\n# build-cfn drift sentinel\n")
            proc = subprocess.run(
                ["bash", str(STAGE0 / "build-cfn.sh"), "--check"],
                cwd=_REPO,
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertNotEqual(
                proc.returncode,
                0,
                msg="build-cfn --check passed despite a tampered source; the drift gate is broken",
            )
        finally:
            src.write_bytes(original)

    def test_cfn_has_bootstrap_ssm_markers(self) -> None:
        text = CFN_MAIN.read_text()
        for marker in (
            "BOOTSTRAP_GZB64_SSM_PART1 START",
            "BOOTSTRAP_GZB64_SSM_PART2 START",
            "USERDATA_LAUNCHER markers",
        ):
            self.assertIn(marker, text)


if __name__ == "__main__":
    unittest.main()
