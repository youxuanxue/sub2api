#!/usr/bin/env python3
"""Tests for the prod-only QA-export injection in ops/stage0/deploy_via_ssm.sh.

deploy_via_ssm.sh self-heals the QA trajectory export config (the 4
QA_CAPTURE_EXPORT_STORAGE_* env vars) onto a LIVE prod host on every deploy,
mirroring the SERVER_FRONTEND_URL backfill. The vars are deliberately NOT in the
shared stage0 compose because that file is also embedded in the edge Lightsail
launch script, which sits ~46 B under Lightsail's 16 KB user-data cap. Edges are
pure cc-relay mirrors that never export QA, so the injection must be gated to the
prod EC2 (`i-*`) node and never reach a Lightsail edge (`mi-*`).

The script exposes a STAGE0_RENDER_ONLY seam: it writes the SSM command document
and exits before any AWS call, so the rendered command list can be asserted with
no live infra. On Linux (= CI, GNU sed) we additionally execute the two injected
commands against a fake .env + compose and assert the resulting files, proving the
guarded, idempotent .env append + the `/^      - TZ=/a\\` compose insertion.

stdlib-only; no AWS, no network.
"""
from __future__ import annotations

import json
import os
import pathlib
import platform
import subprocess
import tempfile
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "deploy_via_ssm.sh"

# Every edge in the fleet is a Lightsail managed instance (mi-*); prod is the
# only EC2 (i-*) node. These fakes exercise both arms of the gate.
_PROD_IID = "i-0prod000000000000"
_EDGE_IID = "mi-0edge000000000000"


def _render(instance_id: str, tag: str = "1.8.99", env_extra: dict | None = None):
    """Run the script in render-only mode; return (proc, commands list)."""
    out_dir = tempfile.mkdtemp(prefix="qa-export-render-")
    env = {
        **os.environ,
        "STAGE0_RENDER_ONLY": "1",
        "STAGE0_SSM_OUTPUT_DIR": out_dir,
    }
    if env_extra:
        env.update(env_extra)
    proc = subprocess.run(
        ["bash", str(_SCRIPT), tag, instance_id],
        env=env, capture_output=True, text=True, check=False,
    )
    params = json.loads((pathlib.Path(out_dir) / "ssm-params.json").read_text())
    return proc, params["commands"]


def _qa_cmds(commands: list[str]) -> list[str]:
    return [c for c in commands if "QA_CAPTURE_EXPORT_STORAGE" in c]


class QAExportInjectionRenderTest(unittest.TestCase):
    def test_prod_injects_exactly_two_guarded_commands(self) -> None:
        proc, commands = _render(_PROD_IID)
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        qa = _qa_cmds(commands)
        self.assertEqual(len(qa), 2, msg=f"expected 2 QA cmds, got {len(qa)}: {qa}")

        env_cmd = next(c for c in qa if "/var/lib/tokenkey/.env" in c and "docker-compose" not in c)
        # default prod values, supplied via @sh-quoted shell vars
        self.assertIn("qa_d='s3'", env_cmd)
        self.assertIn("qa_r='us-east-1'", env_cmd)
        self.assertIn("qa_b='tokenkey-prod-qa-exports'", env_cmd)
        self.assertIn("qa_p='traj-exports'", env_cmd)
        # guarded + additive (must not clobber an existing value)
        self.assertIn('grep -q "^${key}=" /var/lib/tokenkey/.env', env_cmd)
        self.assertIn("tee -a /var/lib/tokenkey/.env", env_cmd)
        # credentials are NEVER written to .env (instance role + bucket policy)
        self.assertNotIn("ACCESS_KEY", env_cmd)
        self.assertNotIn("SECRET", env_cmd)

        compose_cmd = next(c for c in qa if "docker-compose.yml" in c)
        self.assertIn("/^      - TZ=/a\\", compose_cmd)          # anchored to the TZ line
        self.assertIn("qa-export-before-1.8.99", compose_cmd)    # tagged backup ($CF.qa-export-before-<tag>)
        self.assertIn('grep -q "${key}=" "$CF"', compose_cmd)    # guarded insertion

    def test_edge_gets_no_qa_injection(self) -> None:
        # mi-* + EDGE_ID is the edge arm: the command list must be byte-identical
        # to the pre-change baseline (empty injection array).
        proc, commands = _render(_EDGE_IID, env_extra={"EDGE_ID": "us2"})
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        self.assertEqual(_qa_cmds(commands), [])

    def test_prod_minus_qa_equals_edge_command_count(self) -> None:
        # The only difference between prod and edge is the 2 injected commands.
        _, prod = _render(_PROD_IID)
        _, edge = _render(_EDGE_IID, env_extra={"EDGE_ID": "us2"})
        self.assertEqual(len(prod) - len(edge), 2)

    def test_values_are_env_overridable(self) -> None:
        proc, commands = _render(_PROD_IID, env_extra={
            "QA_CAPTURE_EXPORT_STORAGE_BUCKET": "custom-bucket",
            "QA_CAPTURE_EXPORT_STORAGE_PREFIX": "custom/prefix",
        })
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        env_cmd = next(c for c in _qa_cmds(commands) if "tee -a" in c)
        self.assertIn("qa_b='custom-bucket'", env_cmd)
        self.assertIn("qa_p='custom/prefix'", env_cmd)


@unittest.skipUnless(
    platform.system() == "Linux",
    "compose insertion uses GNU sed `a\\` one-line syntax (prod is Linux; BSD sed differs)",
)
class QAExportInjectionExecuteTest(unittest.TestCase):
    """Execute the two prod commands against a fake host and assert the result."""

    def _run_prod_cmds_against(self, host: pathlib.Path) -> None:
        _, commands = _render(_PROD_IID)
        script = "\n".join(_qa_cmds(commands))
        # retarget the hardcoded host paths into the temp dir; drop sudo
        script = script.replace("/var/lib/tokenkey", str(host)).replace("sudo ", "")
        subprocess.run(["bash", "-e", "-c", script], check=True,
                       capture_output=True, text=True)

    def test_injection_is_correct_and_idempotent(self) -> None:
        host = pathlib.Path(tempfile.mkdtemp(prefix="qa-export-exec-"))
        (host / ".env").write_text("APP_ENV=prod\nAPI_DOMAIN=api.tokenkey.dev\n")
        (host / "docker-compose.yml").write_text(
            "services:\n"
            "  tokenkey:\n"
            "    environment:\n"
            "      - SERVER_FRONTEND_URL=${SERVER_FRONTEND_URL:-}\n"
            "      - TZ=${TZ:-UTC}\n"
            "    ports:\n"
            '      - "8080:8080"\n'
        )

        self._run_prod_cmds_against(host)

        env_txt = (host / ".env").read_text()
        for k, v in (
            ("DRIVER", "s3"), ("REGION", "us-east-1"),
            ("BUCKET", "tokenkey-prod-qa-exports"), ("PREFIX", "traj-exports"),
        ):
            self.assertIn(f"QA_CAPTURE_EXPORT_STORAGE_{k}={v}\n", env_txt)

        compose_txt = (host / "docker-compose.yml").read_text()
        for k in ("DRIVER", "REGION", "BUCKET", "PREFIX"):
            mapping = f"      - QA_CAPTURE_EXPORT_STORAGE_{k}=${{QA_CAPTURE_EXPORT_STORAGE_{k}:-}}\n"
            self.assertIn(mapping, compose_txt)
        # inserted after the TZ line, not before it
        self.assertLess(compose_txt.index("- TZ="),
                        compose_txt.index("QA_CAPTURE_EXPORT_STORAGE_DRIVER"))
        self.assertTrue(list(host.glob("docker-compose.yml.qa-export-before-*")))

        # second run is a no-op: exactly 4 .env lines + 4 compose mappings, no dupes.
        # (count by line, not substring: a compose mapping mentions the prefix twice.)
        self._run_prod_cmds_against(host)

        def _qa_lines(text: str, needle: str) -> int:
            return sum(1 for ln in text.splitlines() if needle in ln)

        self.assertEqual(_qa_lines((host / ".env").read_text(), "QA_CAPTURE_EXPORT_STORAGE_"), 4)
        self.assertEqual(
            _qa_lines((host / "docker-compose.yml").read_text(), "- QA_CAPTURE_EXPORT_STORAGE_"), 4)


if __name__ == "__main__":
    unittest.main()
