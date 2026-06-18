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
        self.assertIn("qa_b='tokenkey-prod-qa-exports-682751977094'", env_cmd)
        self.assertIn("qa_p='traj-exports'", env_cmd)
        # guarded + additive (must not clobber an existing value)
        self.assertIn('grep -q "^${key}=" /var/lib/tokenkey/.env', env_cmd)
        self.assertIn("tee -a /var/lib/tokenkey/.env", env_cmd)
        # credentials are NEVER written to .env (instance role + bucket policy)
        self.assertNotIn("ACCESS_KEY", env_cmd)
        self.assertNotIn("SECRET", env_cmd)

        compose_cmd = next(c for c in qa if "docker-compose.yml" in c)
        # anchored to the tokenkey-unique SERVER_FRONTEND_URL line, NOT the per-service
        # TZ line (every service has a TZ line → TZ anchor injected once per service).
        self.assertIn("/^      - SERVER_FRONTEND_URL=/a\\", compose_cmd)
        self.assertNotIn("/^      - TZ=/a\\", compose_cmd)
        self.assertIn("qa-export-before-1.8.99", compose_cmd)    # tagged backup ($CF.qa-export-before-<tag>)
        self.assertIn('grep -q "${key}=" "$CF"', compose_cmd)    # guarded insertion

    def test_edge_gets_no_qa_injection(self) -> None:
        # mi-* + EDGE_ID is the edge arm: the command list must be byte-identical
        # to the pre-change baseline (empty injection array).
        proc, commands = _render(_EDGE_IID, env_extra={"EDGE_ID": "us2"})
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        self.assertEqual(_qa_cmds(commands), [])

    def test_prod_minus_edge_is_only_the_prod_only_injections(self) -> None:
        # The only difference between prod and edge is the prod-only injected
        # commands: 2 QA-export + 2 media-storage (both gated to the EC2 i-* node;
        # edges are Lightsail mi-* and get an empty injection array). Computed from
        # the actual injected-command counts so adding another prod-only block
        # updates here in one place.
        _, prod = _render(_PROD_IID)
        _, edge = _render(_EDGE_IID, env_extra={"EDGE_ID": "us2"})
        injected = sum(
            1 for c in prod
            if "QA_CAPTURE_EXPORT_STORAGE" in c or "MEDIA_STORAGE_" in c
        )
        self.assertEqual(injected, 4)
        self.assertEqual(len(prod) - len(edge), injected)

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

    @staticmethod
    def _qa_mapping_count(lines: list[str]) -> int:
        return sum(1 for ln in lines if "- QA_CAPTURE_EXPORT_STORAGE_" in ln)

    @staticmethod
    def _service_block(lines: list[str], name: str) -> list[str]:
        # lines belonging to `name:` — from its 2-space header to the next service header.
        out, inside = [], False
        for ln in lines:
            is_header = ln.startswith("  ") and not ln.startswith("    ") and ln.rstrip().endswith(":")
            if is_header:
                inside = ln.strip() == f"{name}:"
                continue
            if inside:
                out.append(ln)
        return out

    def test_injection_targets_only_tokenkey_and_is_idempotent(self) -> None:
        # EVERY service carries a `- TZ=` line, but only tokenkey has SERVER_FRONTEND_URL.
        # The old TZ anchor injected the 4 mappings once PER service (the prod 1.8.11 bug);
        # the SERVER_FRONTEND_URL anchor must inject them exactly once, in tokenkey only.
        host = pathlib.Path(tempfile.mkdtemp(prefix="qa-export-exec-"))
        (host / ".env").write_text("APP_ENV=prod\nAPI_DOMAIN=api.tokenkey.dev\n")
        (host / "docker-compose.yml").write_text(
            "services:\n"
            "  caddy:\n"
            "    environment:\n"
            "      - TZ=${TZ:-UTC}\n"
            "  tokenkey:\n"
            "    environment:\n"
            "      - SERVER_FRONTEND_URL=${SERVER_FRONTEND_URL:-}\n"
            "      - TZ=${TZ:-UTC}\n"
            "  postgres:\n"
            "    environment:\n"
            "      - TZ=${TZ:-UTC}\n"
            "  redis:\n"
            "    environment:\n"
            "      - TZ=${TZ:-UTC}\n"
        )

        self._run_prod_cmds_against(host)

        env_txt = (host / ".env").read_text()
        for k, v in (
            ("DRIVER", "s3"), ("REGION", "us-east-1"),
            ("BUCKET", "tokenkey-prod-qa-exports-682751977094"), ("PREFIX", "traj-exports"),
        ):
            self.assertIn(f"QA_CAPTURE_EXPORT_STORAGE_{k}={v}\n", env_txt)

        lines = (host / "docker-compose.yml").read_text().splitlines()
        # exactly 4 mappings total — ALL in the tokenkey block, none leaked to the
        # other TZ-bearing services (the exact regression the anchor change fixes).
        self.assertEqual(self._qa_mapping_count(lines), 4)
        self.assertEqual(self._qa_mapping_count(self._service_block(lines, "tokenkey")), 4)
        for svc in ("caddy", "postgres", "redis"):
            self.assertEqual(self._qa_mapping_count(self._service_block(lines, svc)), 0,
                             msg=f"QA mappings leaked into {svc}")
        self.assertTrue(list(host.glob("docker-compose.yml.qa-export-before-*")))

        # idempotent: a second deploy adds nothing (still 4 compose mappings + 4 .env lines).
        self._run_prod_cmds_against(host)
        self.assertEqual(
            self._qa_mapping_count((host / "docker-compose.yml").read_text().splitlines()), 4)
        self.assertEqual(
            sum(1 for ln in (host / ".env").read_text().splitlines()
                if "QA_CAPTURE_EXPORT_STORAGE_" in ln), 4)


if __name__ == "__main__":
    unittest.main()
