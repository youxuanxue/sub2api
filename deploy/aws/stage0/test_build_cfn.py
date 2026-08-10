#!/usr/bin/env python3
"""Size gates for deploy/aws/stage0/build-cfn.sh outputs (stdlib-only)."""
from __future__ import annotations

import gzip
import os
import pathlib
import re
import subprocess
import tempfile
import unittest

_REPO = pathlib.Path(__file__).resolve().parents[3]
STAGE0 = _REPO / "deploy/aws/stage0"
CFN_MAIN = _REPO / "deploy/aws/cloudformation/stage0-single-ec2.yaml"
CFN_EDGE = _REPO / "deploy/aws/cloudformation/stage0-edge-ec2.yaml"
BOOTSTRAP = STAGE0 / "stage0-ec2-bootstrap.sh"
USERDATA_LAUNCHER = STAGE0 / "stage0-ec2-userdata-launcher.sub.sh"

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


def _extract_instance_body(cfn_text: str) -> str:
    match = re.search(
        r"^  Instance:\n(.*?)(?=^  [A-Za-z][A-Za-z0-9]*:\n|^Outputs:\n|\Z)",
        cfn_text,
        re.M | re.S,
    )
    if not match:
        raise AssertionError("Instance resource not found")
    return match.group(1)


class BuildCfnSizeTest(unittest.TestCase):
    def test_bootstrap_and_userdata_launchers_do_not_enable_xtrace(self) -> None:
        artifacts = {
            "bootstrap": BOOTSTRAP.read_text(encoding="utf-8"),
            "launcher": USERDATA_LAUNCHER.read_text(encoding="utf-8"),
            "prod UserData": _extract_userdata_body(CFN_MAIN.read_text(encoding="utf-8")),
            "edge UserData": _extract_userdata_body(CFN_EDGE.read_text(encoding="utf-8")),
        }
        for label, script in artifacts.items():
            with self.subTest(artifact=label):
                option_sets = re.findall(r"(?m)^\s*set -([A-Za-z]+)", script)
                self.assertTrue(option_sets, f"{label} has no fail-closed shell options")
                self.assertTrue(
                    all("x" not in options for options in option_sets),
                    f"{label} enables xtrace and can expose bootstrap secrets: {option_sets}",
                )

    def test_cloudwatch_agent_install_is_required(self) -> None:
        script = BOOTSTRAP.read_text(encoding="utf-8")
        install_line = next(
            line for line in script.splitlines()
            if "latest/amazon-cloudwatch-agent.rpm" in line
        )
        self.assertNotIn(
            "|| true",
            install_line,
            "bootstrap must fail before writing agent config when the required package cannot install",
        )

    def test_cloudwatch_agent_start_is_required(self) -> None:
        script = BOOTSTRAP.read_text(encoding="utf-8")
        collapsed = re.sub(r"\\\n\s*", " ", script)
        self.assertNotIn(
            "if [ -x /opt/aws/amazon-cloudwatch-agent/bin/amazon-cloudwatch-agent-ctl ]; then",
            script,
        )
        self.assertNotRegex(
            collapsed,
            r"amazon-cloudwatch-agent-ctl .* -s \|\| true",
            "BOOTSTRAP_DONE must require CloudWatch agent startup to succeed",
        )

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

    def test_edge_userdata_under_ec2_limit(self) -> None:
        body = _extract_userdata_body(CFN_EDGE.read_text())
        self.assertLessEqual(
            len(body.encode()),
            EC2_USERDATA_LIMIT,
            f"edge UserData body is {len(body.encode())} bytes; EC2 limit is {EC2_USERDATA_LIMIT}",
        )

    def test_edge_userdata_shebang_is_first_line(self) -> None:
        body = _extract_userdata_body(CFN_EDGE.read_text())
        first = next((line.strip() for line in body.splitlines() if line.strip()), "")
        self.assertEqual(first, "#!/bin/bash")

    def test_edge_userdata_uses_only_edge_stage0_prefix(self) -> None:
        body = _extract_userdata_body(CFN_EDGE.read_text())
        prefix_exports = [
            line.strip()
            for line in body.splitlines()
            if line.strip().startswith("export TK_STAGE0_PREFIX=")
        ]
        self.assertEqual(
            prefix_exports,
            ["export TK_STAGE0_PREFIX='/${ProjectName}/edge/${EdgeId}/stage0'"],
            "edge UserData must not briefly expose the prod SSM prefix before overriding it",
        )

    def test_bootstrap_gzip_b64_fits_three_ssm_standard_parts(self) -> None:
        # The bootstrap gzip|base64 blob is split across SSM Standard parameters
        # (each <= 4096 chars) and reassembled by the UserData launcher. The 2-part
        # budget was exhausted by the 2026-06-17 swap + memory-pressure-alert
        # additions, so the template now carries 3 part slots (see build-cfn.sh
        # split_b64_for_ssm + the BOOTSTRAP_GZB64_SSM_PART3 markers).
        raw = (STAGE0 / "stage0-ec2-bootstrap.sh").read_bytes()
        b64 = __import__("base64").b64encode(gzip.compress(raw, 9)).decode()
        parts = [b64[i:i + SSM_STANDARD_LIMIT] for i in range(0, len(b64), SSM_STANDARD_LIMIT)]
        self.assertLessEqual(
            len(parts),
            3,
            f"bootstrap needs {len(parts)} SSM parts; template has 3 slots — add part4 plumbing",
        )
        for part in parts:
            self.assertLessEqual(len(part), SSM_STANDARD_LIMIT)

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

    def test_qa_orphan_helper_is_distributed_within_ssm_standard_limits(self) -> None:
        """The generated template must ship both parts of stale-cleanup ownership."""
        original_main = CFN_MAIN.read_bytes()
        with tempfile.TemporaryDirectory() as temp_dir:
            cfn_copy = pathlib.Path(temp_dir) / "stage0-single-ec2.yaml"
            cfn_copy.write_text(
                re.sub(
                    r"(# >>> QA_EXPORT_ORPHAN_GZB64_SSM_PART1 START[^\n]*\n\s*Value: )'[^']*'",
                    r"\1''",
                    original_main.decode(),
                    count=1,
                ),
                encoding="utf-8",
            )
            proc = subprocess.run(
                ["bash", str(STAGE0 / "build-cfn.sh")],
                cwd=_REPO,
                env={**os.environ, "CFN_FILE": str(cfn_copy)},
                capture_output=True,
                text=True,
                check=False,
            )
            cfn_text = cfn_copy.read_text(encoding="utf-8")
        self.assertEqual(
            proc.returncode,
            0,
            msg=f"build-cfn failed:\nstdout={proc.stdout}\nstderr={proc.stderr}",
        )
        self.assertEqual(CFN_MAIN.read_bytes(), original_main)
        parts = []
        for part in (1, 2):
            helper = re.search(
                rf"# >>> QA_EXPORT_ORPHAN_GZB64_SSM_PART{part} START[^\n]*\n\s*Value: '([^']*)'\n"
                rf"\s*# >>> QA_EXPORT_ORPHAN_GZB64_SSM_PART{part} END",
                cfn_text,
            )
            self.assertIsNotNone(helper, "CFN must carry the export-orphan helper payload")
            assert helper is not None
            self.assertTrue(helper.group(1), "build must replace the temporary CFN helper payload")
            self.assertLessEqual(len(helper.group(1)), SSM_STANDARD_LIMIT)
            parts.append(helper.group(1))
        helper_bytes = gzip.decompress(__import__("base64").b64decode("".join(parts)))
        self.assertEqual(
            helper_bytes,
            (STAGE0 / "tokenkey-qa-export-orphan.py").read_bytes(),
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
        for template in (CFN_MAIN, CFN_EDGE):
            with self.subTest(template=template.name):
                text = template.read_text()
                for marker in (
                    "BOOTSTRAP_GZB64_SSM_PART1 START",
                    "BOOTSTRAP_GZB64_SSM_PART2 START",
                    "BOOTSTRAP_GZB64_SSM_PART3 START",
                    ">>> USERDATA_LAUNCHER START",
                    ">>> USERDATA_LAUNCHER END",
                ):
                    self.assertIn(marker, text)

    def test_instances_wait_for_all_bootstrap_parameters(self) -> None:
        for template in (CFN_MAIN, CFN_EDGE):
            with self.subTest(template=template.name):
                instance = _extract_instance_body(template.read_text())
                for part in (1, 2, 3):
                    self.assertIn(
                        f"TokenkeyStage0BootstrapGzipB64Part{part}Parameter",
                        instance,
                        f"{template.name} can boot before bootstrap part {part} exists",
                    )


if __name__ == "__main__":
    unittest.main()
