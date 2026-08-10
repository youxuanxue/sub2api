#!/usr/bin/env python3
"""Tests for ops/stage0/deploy_via_ssm_bluegreen.sh.

The prod blue/green deploy primitive renders a compact SSM command list that
base64-delivers the real host script. These tests use the STAGE0_RENDER_ONLY
seam so they can assert the generated contract without touching AWS.
"""
from __future__ import annotations

import json
import os
import pathlib
import shlex
import subprocess
import tempfile
import unittest

_SCRIPT = pathlib.Path(__file__).resolve().parent / "deploy_via_ssm_bluegreen.sh"
_PROD_IID = "i-0prod000000000000"
_EDGE_IID = "mi-0edge000000000000"


def _render(instance_id: str = _PROD_IID, tag: str = "1.8.99", env_extra: dict | None = None):
    out_dir = pathlib.Path(tempfile.mkdtemp(prefix="bluegreen-render-"))
    env = {
        **os.environ,
        "STAGE0_RENDER_ONLY": "1",
        "STAGE0_SSM_OUTPUT_DIR": str(out_dir),
    }
    if env_extra:
        env.update(env_extra)
    proc = subprocess.run(
        ["bash", str(_SCRIPT), tag, instance_id],
        env=env,
        capture_output=True,
        text=True,
        check=False,
    )
    params = None
    remote = None
    params_path = out_dir / "ssm-params.json"
    remote_path = out_dir / "bluegreen-remote.sh"
    if params_path.exists():
        params = json.loads(params_path.read_text())
    if remote_path.exists():
        remote = remote_path.read_text()
    return proc, params, remote


def _run_wait_healthy(remote: str, statuses: list[str]) -> tuple[int, int, str]:
    start = remote.index("wait_healthy() {")
    end = remote.index("\n}\n\nwait_ready()", start) + len("\n}\n")
    function = remote[start:end]
    with tempfile.TemporaryDirectory(prefix="bluegreen-health-") as tmp:
        root = pathlib.Path(tmp)
        status_file = root / "statuses"
        count_file = root / "count"
        status_file.write_text("\n".join(statuses) + "\n")
        count_file.write_text("1\n")
        script = f"""{function}
log() {{ :; }}
sleep() {{ :; }}
sudo() {{ :; }}
container_health() {{
  local n
  n="$(cat {shlex.quote(str(count_file))})"
  sed -n "${{n}}p" {shlex.quote(str(status_file))}
  printf '%s\\n' "$((n + 1))" > {shlex.quote(str(count_file))}
}}
TOKENKEY_BLUEGREEN_HEALTH_TRIES=60
TOKENKEY_BLUEGREEN_HEALTH_DELAY_SECONDS=0
wait_healthy tokenkey-green
"""
        proc = subprocess.run(
            ["bash"],
            input=script,
            capture_output=True,
            text=True,
            check=False,
        )
        calls = int(count_file.read_text().strip()) - 1
        return proc.returncode, calls, proc.stdout + proc.stderr


def _run_target_is_reusable(
    remote: str,
    *,
    image: str = "ghcr.io/youxuanxue/sub2api:1.8.134",
    expected_image: str = "ghcr.io/youxuanxue/sub2api:1.8.134",
    health: str = "healthy",
    skip_chown: str = "1",
    expected_hash: str = "expected-hash",
    actual_hash: str = "expected-hash",
    ready: bool = True,
) -> tuple[int, str]:
    start = remote.index("target_is_reusable() {")
    end = remote.index("\n}\n\ndrain_container()", start) + len("\n}\n")
    function = remote[start:end]
    script = f"""{function}
log() {{ :; }}
container_image() {{ printf '%s\\n' {shlex.quote(image)}; }}
container_health() {{ printf '%s\\n' {shlex.quote(health)}; }}
compose_bg() {{ printf 'tokenkey-green %s\\n' {shlex.quote(expected_hash)}; }}
wait_ready() {{ return {0 if ready else 1}; }}
sudo() {{
  case "$*" in
    *'range .Config.Env'*) printf 'SKIP_DATA_CHOWN=%s\\n' {shlex.quote(skip_chown)} ;;
    *'com.docker.compose.config-hash'*) printf '%s\\n' {shlex.quote(actual_hash)} ;;
    *) return 1 ;;
  esac
}}
target_is_reusable tokenkey-green {shlex.quote(expected_image)}
"""
    proc = subprocess.run(
        ["bash"],
        input=script,
        capture_output=True,
        text=True,
        check=False,
    )
    return proc.returncode, proc.stdout + proc.stderr


class BlueGreenRenderTest(unittest.TestCase):
    def test_rejects_lightsail_edge_ids(self) -> None:
        proc, params, remote = _render(_EDGE_IID, env_extra={"EDGE_ID": "us2"})
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("prod-only primitive", proc.stderr)
        self.assertIsNone(params)
        self.assertIsNone(remote)

    def test_remote_script_parses(self) -> None:
        proc, params, remote = _render()
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        self.assertIsNotNone(params)
        self.assertIsNotNone(remote)
        parsed = subprocess.run(
            ["bash", "-n"],
            input=remote,
            text=True,
            capture_output=True,
            check=False,
        )
        self.assertEqual(parsed.returncode, 0, msg=parsed.stderr)

    def test_renders_bluegreen_contract(self) -> None:
        proc, params, remote = _render()
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        assert params is not None
        assert remote is not None
        commands = params["commands"]
        joined = "\n".join(commands)

        self.assertEqual(params.get("executionTimeout"), ["1200"])
        self.assertIn("/tmp/tokenkey-bluegreen-deploy.sh", joined)
        self.assertIn("TAG='1.8.99'", joined)
        self.assertIn("QA_CAPTURE_EXPORT_STORAGE_BUCKET='tokenkey-prod-qa-exports-682751977094'", joined)
        self.assertIn("QA_ARCHIVE_ENABLED='false'", joined)
        self.assertIn("QA_ARCHIVE_STORAGE_BUCKET='tokenkey-prod-qa-raw-archive-682751977094'", joined)
        self.assertIn("TELEMETRY_ARCHIVE_ENABLED=''", joined)
        self.assertIn("TELEMETRY_ARCHIVE_BUCKET='tokenkey-prod-archive-682751977094'", joined)
        self.assertIn("TELEMETRY_ARCHIVE_QUEUE_MAX_BYTES='33554432'", joined)
        self.assertIn("MEDIA_STORAGE_BUCKET='tokenkey-prod-media-682751977094'", joined)
        self.assertIn("GATEWAY_IMAGE_CONCURRENCY_MAX_CONCURRENT_REQUESTS='8'", joined)

        self.assertIn("docker-compose.bluegreen.yml", remote)
        self.assertIn("active-color", remote)
        self.assertIn("tokenkey-blue", remote)
        self.assertIn("tokenkey-green", remote)
        self.assertIn("TOKENKEY_IMAGE_BLUE", remote)
        self.assertIn("TOKENKEY_IMAGE_GREEN", remote)
        self.assertIn("write_caddy_for_color", remote)
        self.assertIn("render_caddy_with_upstream", remote)
        self.assertIn("could not rewrite exactly one reverse_proxy upstream", remote)
        self.assertIn("wait_ready", remote)
        self.assertIn("http://localhost:8080/health", remote)
        self.assertIn("drain_container \"${active_container}\"", remote)
        self.assertIn("sudo docker rm -f tokenkey", remote)
        self.assertIn("DATABASE_HOST=tokenkey-postgres", remote)
        self.assertIn("REDIS_HOST=tokenkey-redis", remote)
        self.assertIn("x-tokenkey-logging: &tokenkey-logging", remote)
        self.assertEqual(remote.count("logging: *tokenkey-logging"), 2)
        self.assertIn('max-size: "100m"', remote)
        self.assertIn('max-file: "5"', remote)
        self.assertEqual(remote.count("- SKIP_DATA_CHOWN=1"), 2)
        self.assertEqual(remote.count("QA_ARCHIVE_ENABLED="), 2)
        self.assertEqual(remote.count("- TELEMETRY_ARCHIVE_ENABLED="), 2)
        self.assertIn("env_default TELEMETRY_ARCHIVE_ENABLED false", remote)
        self.assertIn('TOKENKEY_BLUEGREEN_HEALTH_TRIES:-60', remote)
        self.assertIn('TOKENKEY_BLUEGREEN_UNHEALTHY_LIMIT:-3', remote)
        self.assertIn('entered terminal state ${status}; failing health wait immediately', remote)
        self.assertIn('remained unhealthy for ${unhealthy_streak} consecutive checks', remote)
        self.assertIn('if target_is_reusable "${target_container}" "${new_img}"; then', remote)
        self.assertIn('compose_bg config --hash "${container}"', remote)
        self.assertIn("com.docker.compose.config-hash", remote)
        self.assertIn('reusing healthy target ${target_container}', remote)
        self.assertIn('compose_bg up -d --no-deps --force-recreate "${target_container}"', remote)
        self.assertIn('preserving target ${TARGET_CONTAINER} for retry', remote)
        self.assertNotIn('removed failed target ${TARGET_CONTAINER}', remote)

    def test_telemetry_enable_is_explicitly_delivered(self) -> None:
        proc, params, remote = _render(env_extra={"TELEMETRY_ARCHIVE_ENABLED": "true"})
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        assert params is not None
        assert remote is not None
        self.assertIn("TELEMETRY_ARCHIVE_ENABLED='true'", "\n".join(params["commands"]))
        self.assertIn('env_set TELEMETRY_ARCHIVE_ENABLED "${TELEMETRY_ARCHIVE_ENABLED}"', remote)

    def test_health_wait_fails_fast_only_for_terminal_or_repeated_unhealthy_states(self) -> None:
        proc, _, remote = _render()
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        assert remote is not None

        cases = [
            (["exited"], 1, 1),
            (["dead"], 1, 1),
            (["unhealthy", "unhealthy", "unhealthy"], 1, 3),
            (["unhealthy", "starting", "unhealthy", "unhealthy", "healthy"], 0, 5),
        ]
        for statuses, expected_rc, expected_calls in cases:
            with self.subTest(statuses=statuses):
                rc, calls, output = _run_wait_healthy(remote, statuses)
                self.assertEqual(rc, expected_rc, msg=output)
                self.assertEqual(calls, expected_calls, msg=output)

    def test_target_reuse_requires_every_runtime_contract_to_match(self) -> None:
        proc, _, remote = _render()
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        assert remote is not None

        cases = [
            ({}, 0),
            ({"image": "ghcr.io/youxuanxue/sub2api:old"}, 1),
            ({"health": "unhealthy"}, 1),
            ({"skip_chown": "0"}, 1),
            ({"actual_hash": "stale-hash"}, 1),
            ({"expected_hash": ""}, 1),
            ({"ready": False}, 1),
        ]
        for overrides, expected_rc in cases:
            with self.subTest(overrides=overrides):
                rc, output = _run_target_is_reusable(remote, **overrides)
                self.assertEqual(rc, expected_rc, msg=output)

    def test_values_are_env_overridable(self) -> None:
        proc, params, _ = _render(env_extra={
            "QA_CAPTURE_EXPORT_STORAGE_BUCKET": "custom-qa",
            "MEDIA_STORAGE_BUCKET": "custom-media",
            "GATEWAY_IMAGE_CONCURRENCY_MAX_CONCURRENT_REQUESTS": "16",
        })
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        assert params is not None
        joined = "\n".join(params["commands"])
        self.assertIn("QA_CAPTURE_EXPORT_STORAGE_BUCKET='custom-qa'", joined)
        self.assertIn("MEDIA_STORAGE_BUCKET='custom-media'", joined)
        self.assertIn("GATEWAY_IMAGE_CONCURRENCY_MAX_CONCURRENT_REQUESTS='16'", joined)


if __name__ == "__main__":
    unittest.main()
