#!/usr/bin/env python3
"""Tests for the prod-only image/video concurrency-cap injection in deploy_via_ssm.sh.

Mirrors test_deploy_via_ssm_media_storage.py: deploy_via_ssm.sh self-heals the
image/video generation concurrency limiter config
(GATEWAY_IMAGE_CONCURRENCY_{ENABLED,MAX_CONCURRENT_REQUESTS,OVERFLOW_MODE}) onto a
LIVE prod host on every deploy, anchored to the tokenkey-unique
SERVER_FRONTEND_URL compose line. The limiter ships disabled in code (a no-op),
so /v1/images/generations + /v1/video/generations share account concurrency with
text. Prod injection keeps the knob present but ENABLED=false (upsert every
deploy so a previous true cannot stick). Edges (Lightsail mi-*) never get this
injection. MAX/OVERFLOW remain additive defaults for an operator who re-enables
the limiter. All three knobs are env-overridable.

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
    out_dir = tempfile.mkdtemp(prefix="image-concurrency-render-")
    env = {**os.environ, "STAGE0_RENDER_ONLY": "1", "STAGE0_SSM_OUTPUT_DIR": out_dir}
    if env_extra:
        env.update(env_extra)
    proc = subprocess.run(
        ["bash", str(_SCRIPT), tag, instance_id],
        env=env, capture_output=True, text=True, check=False,
    )
    params = json.loads((pathlib.Path(out_dir) / "ssm-params.json").read_text())
    return proc, params["commands"]


def _ic_cmds(commands: list[str]) -> list[str]:
    return [c for c in commands if "GATEWAY_IMAGE_CONCURRENCY" in c]


class ImageConcurrencyInjectionRenderTest(unittest.TestCase):
    def test_prod_injects_exactly_two_guarded_commands(self) -> None:
        proc, commands = _render(_PROD_IID)
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        ic = _ic_cmds(commands)
        self.assertEqual(len(ic), 2, msg=f"expected 2 image-concurrency cmds, got {len(ic)}: {ic}")

        env_cmd = next(c for c in ic if "/var/lib/tokenkey/.env" in c and "docker-compose" not in c)
        # default prod values: limiter OFF so images share account.concurrency with text.
        self.assertIn("ic_e='false'", env_cmd)
        self.assertIn("ic_m='8'", env_cmd)
        self.assertIn("ic_o='reject'", env_cmd)
        # ENABLED is upserted; MAX/OVERFLOW stay additive.
        self.assertIn('grep -q "^${key}=" /var/lib/tokenkey/.env', env_cmd)
        self.assertIn("GATEWAY_IMAGE_CONCURRENCY_ENABLED", env_cmd)
        self.assertIn("tee -a /var/lib/tokenkey/.env", env_cmd)

        compose_cmd = next(c for c in ic if "docker-compose.yml" in c)
        # anchored to the tokenkey-unique SERVER_FRONTEND_URL line (not per-service TZ)
        self.assertIn("/^      - SERVER_FRONTEND_URL=/a\\", compose_cmd)
        self.assertNotIn("/^      - TZ=/a\\", compose_cmd)
        self.assertIn("image-concurrency-before-1.8.99", compose_cmd)
        self.assertIn('grep -q "${key}=" "$CF"', compose_cmd)

    def test_edge_gets_no_image_concurrency_injection(self) -> None:
        proc, commands = _render(_EDGE_IID, env_extra={"EDGE_ID": "us2"})
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        self.assertEqual(_ic_cmds(commands), [])

    def test_values_are_env_overridable(self) -> None:
        proc, commands = _render(_PROD_IID, env_extra={
            "GATEWAY_IMAGE_CONCURRENCY_MAX_CONCURRENT_REQUESTS": "16",
            "GATEWAY_IMAGE_CONCURRENCY_OVERFLOW_MODE": "wait",
        })
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        env_cmd = next(c for c in _ic_cmds(commands) if "/var/lib/tokenkey/.env" in c and "docker-compose" not in c)
        self.assertIn("ic_m='16'", env_cmd)
        self.assertIn("ic_o='wait'", env_cmd)


@unittest.skipUnless(
    platform.system() == "Linux",
    "compose insertion uses GNU sed `a\\` one-line syntax (prod is Linux; BSD sed differs)",
)
class ImageConcurrencyInjectionExecuteTest(unittest.TestCase):
    """Execute the two prod image-concurrency commands against a fake host."""

    def _run_ic_cmds_against(self, host: pathlib.Path) -> None:
        _, commands = _render(_PROD_IID)
        script = "\n".join(_ic_cmds(commands))
        script = script.replace("/var/lib/tokenkey", str(host)).replace("sudo ", "")
        subprocess.run(["bash", "-e", "-c", script], check=True, capture_output=True, text=True)

    @staticmethod
    def _ic_mapping_count(lines: list[str]) -> int:
        return sum(1 for ln in lines if "- GATEWAY_IMAGE_CONCURRENCY_" in ln)

    def test_injection_targets_only_tokenkey_and_is_idempotent(self) -> None:
        host = pathlib.Path(tempfile.mkdtemp(prefix="image-concurrency-exec-"))
        (host / ".env").write_text("APP_ENV=prod\nAPI_DOMAIN=api.tokenkey.dev\n")
        (host / "docker-compose.yml").write_text(
            "services:\n"
            "  caddy:\n    environment:\n      - TZ=${TZ:-UTC}\n"
            "  tokenkey:\n    environment:\n      - SERVER_FRONTEND_URL=${SERVER_FRONTEND_URL:-}\n      - TZ=${TZ:-UTC}\n"
            "  postgres:\n    environment:\n      - TZ=${TZ:-UTC}\n"
            "  redis:\n    environment:\n      - TZ=${TZ:-UTC}\n"
        )

        self._run_ic_cmds_against(host)

        env_txt = (host / ".env").read_text()
        for k, v in (
            ("ENABLED", "false"),
            ("MAX_CONCURRENT_REQUESTS", "8"),
            ("OVERFLOW_MODE", "reject"),
        ):
            self.assertIn(f"GATEWAY_IMAGE_CONCURRENCY_{k}={v}\n", env_txt)

        lines = (host / "docker-compose.yml").read_text().splitlines()
        self.assertEqual(self._ic_mapping_count(lines), 3)  # ENABLED/MAX/OVERFLOW
        # all 3 land in the tokenkey block (anchored on its unique SERVER_FRONTEND_URL)
        in_tokenkey = False
        tokenkey_ic = 0
        for ln in lines:
            is_header = ln.startswith("  ") and not ln.startswith("    ") and ln.rstrip().endswith(":")
            if is_header:
                in_tokenkey = ln.strip() == "tokenkey:"
                continue
            if in_tokenkey and "- GATEWAY_IMAGE_CONCURRENCY_" in ln:
                tokenkey_ic += 1
        self.assertEqual(tokenkey_ic, 3, msg="image-concurrency mappings must all land in the tokenkey service block")
        self.assertTrue(list(host.glob("docker-compose.yml.image-concurrency-before-*")))

        # idempotent: a second deploy adds nothing.
        self._run_ic_cmds_against(host)
        self.assertEqual(
            self._ic_mapping_count((host / "docker-compose.yml").read_text().splitlines()), 3)
        self.assertEqual(
            sum(1 for ln in (host / ".env").read_text().splitlines()
                if "GATEWAY_IMAGE_CONCURRENCY_" in ln), 3)

    def test_existing_enabled_true_is_overwritten_to_false(self) -> None:
        host = pathlib.Path(tempfile.mkdtemp(prefix="image-concurrency-overwrite-"))
        (host / ".env").write_text(
            "APP_ENV=prod\nGATEWAY_IMAGE_CONCURRENCY_ENABLED=true\n"
            "GATEWAY_IMAGE_CONCURRENCY_MAX_CONCURRENT_REQUESTS=8\n"
            "GATEWAY_IMAGE_CONCURRENCY_OVERFLOW_MODE=reject\n"
        )
        (host / "docker-compose.yml").write_text(
            "services:\n"
            "  tokenkey:\n    environment:\n"
            "      - SERVER_FRONTEND_URL=${SERVER_FRONTEND_URL:-}\n"
            "      - GATEWAY_IMAGE_CONCURRENCY_ENABLED=${GATEWAY_IMAGE_CONCURRENCY_ENABLED:-}\n"
            "      - GATEWAY_IMAGE_CONCURRENCY_MAX_CONCURRENT_REQUESTS=${GATEWAY_IMAGE_CONCURRENCY_MAX_CONCURRENT_REQUESTS:-}\n"
            "      - GATEWAY_IMAGE_CONCURRENCY_OVERFLOW_MODE=${GATEWAY_IMAGE_CONCURRENCY_OVERFLOW_MODE:-}\n"
        )
        self._run_ic_cmds_against(host)
        env_txt = (host / ".env").read_text()
        self.assertIn("GATEWAY_IMAGE_CONCURRENCY_ENABLED=false\n", env_txt)
        self.assertNotIn("GATEWAY_IMAGE_CONCURRENCY_ENABLED=true\n", env_txt)


if __name__ == "__main__":
    unittest.main()
