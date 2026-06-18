#!/usr/bin/env python3
"""Tests for the prod-only media-storage injection in ops/stage0/deploy_via_ssm.sh.

Mirrors test_deploy_via_ssm_qa_export.py: deploy_via_ssm.sh self-heals the
generated-media S3 offload config (MEDIA_STORAGE_DRIVER/REGION/BUCKET) onto a LIVE
prod host on every deploy, anchored to the tokenkey-unique SERVER_FRONTEND_URL
compose line. Edges (Lightsail mi-*) are pure cc-relay mirrors that never serve
media generation, so the injection is gated to the prod EC2 (i-*) node. Credentials
are never written — the prod instance role + the media bucket policy grant S3
access, so presigned links are signed by the instance role (no long-lived key).

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
_PROD_IID = "i-0prod000000000000"
_EDGE_IID = "mi-0edge000000000000"


def _render(instance_id: str, tag: str = "1.8.99", env_extra: dict | None = None):
    out_dir = tempfile.mkdtemp(prefix="media-storage-render-")
    env = {**os.environ, "STAGE0_RENDER_ONLY": "1", "STAGE0_SSM_OUTPUT_DIR": out_dir}
    if env_extra:
        env.update(env_extra)
    proc = subprocess.run(
        ["bash", str(_SCRIPT), tag, instance_id],
        env=env, capture_output=True, text=True, check=False,
    )
    params = json.loads((pathlib.Path(out_dir) / "ssm-params.json").read_text())
    return proc, params["commands"]


def _media_cmds(commands: list[str]) -> list[str]:
    return [c for c in commands if "MEDIA_STORAGE_" in c]


class MediaStorageInjectionRenderTest(unittest.TestCase):
    def test_prod_injects_exactly_two_guarded_commands(self) -> None:
        proc, commands = _render(_PROD_IID)
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        media = _media_cmds(commands)
        self.assertEqual(len(media), 2, msg=f"expected 2 media cmds, got {len(media)}: {media}")

        env_cmd = next(c for c in media if "/var/lib/tokenkey/.env" in c and "docker-compose" not in c)
        self.assertIn("ms_d='s3'", env_cmd)
        self.assertIn("ms_r='us-east-1'", env_cmd)
        self.assertIn("ms_b='tokenkey-prod-media-682751977094'", env_cmd)
        # guarded + additive
        self.assertIn('grep -q "^${key}=" /var/lib/tokenkey/.env', env_cmd)
        self.assertIn("tee -a /var/lib/tokenkey/.env", env_cmd)
        # credentials are NEVER written (instance role + bucket policy)
        self.assertNotIn("ACCESS_KEY", env_cmd)
        self.assertNotIn("SECRET", env_cmd)

        compose_cmd = next(c for c in media if "docker-compose.yml" in c)
        self.assertIn("/^      - SERVER_FRONTEND_URL=/a\\", compose_cmd)
        self.assertNotIn("/^      - TZ=/a\\", compose_cmd)
        self.assertIn("media-storage-before-1.8.99", compose_cmd)
        self.assertIn('grep -q "${key}=" "$CF"', compose_cmd)

    def test_edge_gets_no_media_injection(self) -> None:
        proc, commands = _render(_EDGE_IID, env_extra={"EDGE_ID": "us2"})
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        self.assertEqual(_media_cmds(commands), [])

    def test_bucket_is_env_overridable(self) -> None:
        proc, commands = _render(_PROD_IID, env_extra={"MEDIA_STORAGE_BUCKET": "custom-media-bucket"})
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        env_cmd = next(c for c in _media_cmds(commands) if "tee -a" in c)
        self.assertIn("ms_b='custom-media-bucket'", env_cmd)


@unittest.skipUnless(
    platform.system() == "Linux",
    "compose insertion uses GNU sed `a\\` one-line syntax (prod is Linux; BSD sed differs)",
)
class MediaStorageInjectionExecuteTest(unittest.TestCase):
    """Execute the two prod media commands against a fake host and assert results."""

    def _run_media_cmds_against(self, host: pathlib.Path) -> None:
        _, commands = _render(_PROD_IID)
        script = "\n".join(_media_cmds(commands))
        script = script.replace("/var/lib/tokenkey", str(host)).replace("sudo ", "")
        subprocess.run(["bash", "-e", "-c", script], check=True, capture_output=True, text=True)

    @staticmethod
    def _media_mapping_count(lines: list[str]) -> int:
        return sum(1 for ln in lines if "- MEDIA_STORAGE_" in ln)

    def test_injection_targets_only_tokenkey_and_is_idempotent(self) -> None:
        host = pathlib.Path(tempfile.mkdtemp(prefix="media-storage-exec-"))
        (host / ".env").write_text("APP_ENV=prod\nAPI_DOMAIN=api.tokenkey.dev\n")
        (host / "docker-compose.yml").write_text(
            "services:\n"
            "  caddy:\n    environment:\n      - TZ=${TZ:-UTC}\n"
            "  tokenkey:\n    environment:\n      - SERVER_FRONTEND_URL=${SERVER_FRONTEND_URL:-}\n      - TZ=${TZ:-UTC}\n"
            "  postgres:\n    environment:\n      - TZ=${TZ:-UTC}\n"
            "  redis:\n    environment:\n      - TZ=${TZ:-UTC}\n"
        )

        self._run_media_cmds_against(host)

        env_txt = (host / ".env").read_text()
        for k, v in (("DRIVER", "s3"), ("REGION", "us-east-1"), ("BUCKET", "tokenkey-prod-media-682751977094")):
            self.assertIn(f"MEDIA_STORAGE_{k}={v}\n", env_txt)
        self.assertNotIn("ACCESS_KEY", env_txt)

        lines = (host / "docker-compose.yml").read_text().splitlines()
        self.assertEqual(self._media_mapping_count(lines), 3)  # DRIVER/REGION/BUCKET
        # all 3 land in the tokenkey block (anchored on its unique SERVER_FRONTEND_URL)
        in_tokenkey = False
        tokenkey_media = 0
        for ln in lines:
            is_header = ln.startswith("  ") and not ln.startswith("    ") and ln.rstrip().endswith(":")
            if is_header:
                in_tokenkey = ln.strip() == "tokenkey:"
                continue
            if in_tokenkey and "- MEDIA_STORAGE_" in ln:
                tokenkey_media += 1
        self.assertEqual(tokenkey_media, 3, msg="media mappings must all land in the tokenkey service block")
        self.assertTrue(list(host.glob("docker-compose.yml.media-storage-before-*")))

        # idempotent: a second deploy adds nothing.
        self._run_media_cmds_against(host)
        self.assertEqual(
            self._media_mapping_count((host / "docker-compose.yml").read_text().splitlines()), 3)
        self.assertEqual(
            sum(1 for ln in (host / ".env").read_text().splitlines() if "MEDIA_STORAGE_" in ln), 3)


if __name__ == "__main__":
    unittest.main()
