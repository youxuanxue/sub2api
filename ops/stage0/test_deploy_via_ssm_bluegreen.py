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


def _run_with_fake_aws(
    ssm_stdout: str,
    *,
    cutover_stdout: str = "",
    cutover_statuses: tuple[str, ...] = ("Success",),
) -> tuple[subprocess.CompletedProcess[str], str]:
    with tempfile.TemporaryDirectory(prefix="bluegreen-aws-") as tmp:
        root = pathlib.Path(tmp)
        bin_dir = root / "bin"
        out_dir = root / "out"
        bin_dir.mkdir()
        out_dir.mkdir()
        stdout_fixture = root / "ssm-stdout.txt"
        stdout_fixture.write_text(ssm_stdout)
        cutover_fixture = root / "cutover-stdout.txt"
        cutover_fixture.write_text(cutover_stdout)
        cutover_status_fixture = root / "cutover-status.txt"
        cutover_status_fixture.write_text("\n".join(cutover_statuses) + "\n")
        send_count = root / "send-count.txt"
        send_count.write_text("0\n")
        github_output = root / "github-output.txt"
        fake_aws = bin_dir / "aws"
        fake_aws.write_text(
            "#!/usr/bin/env bash\n"
            "case \"$*\" in\n"
            "  *'ssm send-command'*)\n"
            "    n=$(cat \"$FAKE_SEND_COUNT\")\n"
            "    n=$((n + 1))\n"
            "    printf '%s\\n' \"$n\" > \"$FAKE_SEND_COUNT\"\n"
            "    if [ \"$n\" -eq 1 ]; then echo deploy-command-id; else echo cutover-command-id; fi ;;\n"
            "  *'--query Status'*)\n"
            "    if [[ \"$*\" == *'cutover-command-id'* ]]; then\n"
            "      status=$(sed -n '1p' \"$FAKE_CUTOVER_STATUS\")\n"
            "      sed '1d' \"$FAKE_CUTOVER_STATUS\" > \"$FAKE_CUTOVER_STATUS.tmp\"\n"
            "      mv \"$FAKE_CUTOVER_STATUS.tmp\" \"$FAKE_CUTOVER_STATUS\"\n"
            "      echo \"${status:-Success}\"\n"
            "    else echo Success; fi ;;\n"
            "  *'--query StandardOutputContent'*)\n"
            "    if [[ \"$*\" == *'cutover-command-id'* ]]; then cat \"$FAKE_CUTOVER_STDOUT\"; else cat \"$FAKE_SSM_STDOUT\"; fi ;;\n"
            "  *'--query StandardErrorContent'*) : ;;\n"
            "  *) echo \"unexpected aws call: $*\" >&2; exit 2 ;;\n"
            "esac\n"
        )
        fake_aws.chmod(0o755)
        env = {
            **os.environ,
            "PATH": f"{bin_dir}:{os.environ['PATH']}",
            "FAKE_SSM_STDOUT": str(stdout_fixture),
            "FAKE_CUTOVER_STDOUT": str(cutover_fixture),
            "FAKE_CUTOVER_STATUS": str(cutover_status_fixture),
            "FAKE_SEND_COUNT": str(send_count),
            "AWS_REGION": "us-east-1",
            "GITHUB_OUTPUT": str(github_output),
            "STAGE0_SSM_OUTPUT_DIR": str(out_dir),
        }
        proc = subprocess.run(
            ["bash", str(_SCRIPT), "1.8.99", _PROD_IID],
            env=env,
            capture_output=True,
            text=True,
            check=False,
        )
        output = github_output.read_text() if github_output.exists() else ""
        return proc, output


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


def _extract_shell_function(remote: str, name: str) -> str:
    start = remote.index(f"{name}() {{")
    end = remote.index("\n}\n\n", start) + len("\n}\n")
    return remote[start:end]


def _run_commit_cutover_state(remote: str, color: str) -> subprocess.CompletedProcess[str]:
    try:
        function = _extract_shell_function(remote, "commit_cutover_state")
    except ValueError:
        return subprocess.CompletedProcess(
            args=["bash"],
            returncode=127,
            stdout="",
            stderr="commit_cutover_state is missing",
        )
    script = f"""{function}
write_active_color() {{ printf 'active:%s:%s\\n' "$CUTOVER_COMMITTED" "$1"; }}
record_cutover() {{ printf 'record:%s\\n' "$CUTOVER_COMMITTED"; }}
CUTOVER_COMMITTED=0
commit_cutover_state {shlex.quote(color)}
printf 'final:%s\\n' "$CUTOVER_COMMITTED"
"""
    return subprocess.run(
        ["bash"],
        input=script,
        capture_output=True,
        text=True,
        check=False,
    )


def _run_cutover_path(remote: str, function_name: str) -> subprocess.CompletedProcess[str]:
    function = _extract_shell_function(remote, function_name)
    common = """
log() { :; }
die() { printf 'die:%s\n' "$*"; return 1; }
sudo() { return 0; }
backup_env() { :; }
env_set() { :; }
write_bluegreen_compose() { :; }
compose_bg() { :; }
wait_healthy() { :; }
wait_ready() { :; }
install_bluegreen_systemd_unit() { :; }
write_caddy_for_color() { printf 'caddy:%s\n' "$1"; }
commit_cutover_state() { CUTOVER_COMMITTED=1; printf 'commit:%s\n' "$1"; }
record_cutover() { printf 'raw-record\n'; }
write_active_color() { printf 'raw-active:%s\n' "$1"; }
drain_container() { printf 'drain:%s\n' "$1"; }
TARGET_CONTAINER=""
CUTOVER_COMMITTED=0
"""
    if function_name == "ensure_legacy_cutover":
        setup = """
read_active_color() { :; }
container_image() { printf 'ghcr.io/youxuanxue/sub2api:1.8.98\n'; }
env_get() { printf 'ghcr.io/youxuanxue/sub2api:1.8.98\n'; }
ensure_legacy_cutover
"""
    else:
        setup = """
read_active_color() { printf 'blue\n'; }
other_color() { printf 'green\n'; }
color_container() { printf 'tokenkey-%s\n' "$1"; }
container_image() { printf 'ghcr.io/youxuanxue/sub2api:1.8.98\n'; }
image_repo() { printf 'ghcr.io/youxuanxue/sub2api\n'; }
env_get() { printf 'ghcr.io/youxuanxue/sub2api:1.8.98\n'; }
target_is_reusable() { return 0; }
TAG=1.8.99
TELEMETRY_ARCHIVE_ENABLED=""
deploy_target_color
"""
    return subprocess.run(
        ["bash"],
        input=function + common + setup,
        capture_output=True,
        text=True,
        check=False,
    )


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
        self.assertIn("QA_BUNDLE_STORAGE_BUCKET=''", joined)
        self.assertIn("QA_BUNDLE_STORAGE_BUCKET_SET=false", joined)
        self.assertIn("QA_ARCHIVE_ENABLED='true'", joined)
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
        self.assertEqual(remote.count("start_period: 120s"), 2)
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
        self.assertIn("last-cutover-at", remote)

    def test_commit_cutover_state_persists_live_state_in_safe_order(self) -> None:
        proc, _, remote = _render()
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        assert remote is not None
        committed = _run_commit_cutover_state(remote, "green")
        self.assertEqual(committed.returncode, 0, msg=committed.stderr)
        self.assertEqual(
            committed.stdout.splitlines(),
            ["active:1:green", "record:1", "final:1"],
        )

    def test_legacy_and_regular_cutovers_share_committed_state_owner(self) -> None:
        proc, _, remote = _render()
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        assert remote is not None

        cases = [
            ("ensure_legacy_cutover", ["caddy:blue", "commit:blue", "drain:tokenkey"]),
            ("deploy_target_color", ["caddy:green", "commit:green", "drain:tokenkey-blue"]),
        ]
        for function_name, expected in cases:
            with self.subTest(function_name=function_name):
                cutover = _run_cutover_path(remote, function_name)
                self.assertEqual(cutover.returncode, 0, msg=cutover.stderr)
                self.assertEqual(cutover.stdout.splitlines(), expected)

    def test_exports_remote_cutover_timestamp_to_github_output(self) -> None:
        proc, github_output = _run_with_fake_aws(
            "x" * 24000,
            cutover_stdout="2026-08-24T07:00:11Z\n",
        )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
        self.assertIn("cutover_at=2026-08-24T07:00:11Z\n", github_output)

    def test_waits_for_cutover_timestamp_command(self) -> None:
        proc, github_output = _run_with_fake_aws(
            "x" * 24000,
            cutover_stdout="2026-08-24T07:00:11Z\n",
            cutover_statuses=("InProgress", "Success"),
        )
        self.assertEqual(proc.returncode, 0, msg=proc.stderr + proc.stdout)
        self.assertIn("cutover_at=2026-08-24T07:00:11Z\n", github_output)

    def test_success_without_cutover_timestamp_fails_closed(self) -> None:
        proc, github_output = _run_with_fake_aws(
            "deploy completed without marker\n",
            cutover_stdout="missing\n",
        )
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("cutover timestamp", proc.stderr)
        self.assertNotIn("cutover_at=", github_output)

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
            "QA_BUNDLE_STORAGE_BUCKET": "custom-qa",
            "MEDIA_STORAGE_BUCKET": "custom-media",
            "GATEWAY_IMAGE_CONCURRENCY_MAX_CONCURRENT_REQUESTS": "16",
        })
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        assert params is not None
        joined = "\n".join(params["commands"])
        self.assertIn("QA_BUNDLE_STORAGE_BUCKET='custom-qa'", joined)
        self.assertIn("MEDIA_STORAGE_BUCKET='custom-media'", joined)
        self.assertIn("GATEWAY_IMAGE_CONCURRENCY_MAX_CONCURRENT_REQUESTS='16'", joined)

    def test_bundle_desired_value_replaces_a_stale_host_value(self) -> None:
        proc, params, remote = _render(env_extra={
            "QA_BUNDLE_QUEUE_URL": "https://sqs.us-east-1.amazonaws.com/123456789012/new-queue",
        })
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        assert params is not None
        assert remote is not None
        joined = "\n".join(params["commands"])
        self.assertIn("QA_BUNDLE_QUEUE_URL='https://sqs.us-east-1.amazonaws.com/123456789012/new-queue'", joined)
        self.assertIn("QA_BUNDLE_QUEUE_URL_SET=true", joined)
        self.assertIn("env_apply_if_supplied", remote)
        self.assertIn("env_set \"${key}\" \"${desired}\"", remote)

    def test_unset_bundle_values_preserve_the_host_without_prod_fallbacks(self) -> None:
        proc, _, remote = _render()
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        assert remote is not None
        self.assertIn("not supplied; preserving existing host value", remote)
        self.assertNotIn(
            "https://sqs.us-east-1.amazonaws.com/682751977094/tokenkey-prod-qa-bundle",
            remote,
        )
        self.assertNotIn("tokenkey-prod-qa-bundles-682751977094", remote)

    def test_explicit_bundle_enable_requires_complete_configuration(self) -> None:
        incomplete, params, remote = _render(env_extra={"QA_BUNDLE_ENABLED": "true"})
        self.assertNotEqual(incomplete.returncode, 0)
        self.assertIn("QA_BUNDLE_QUEUE_URL is required", incomplete.stderr)
        self.assertIsNone(params)
        self.assertIsNone(remote)

        complete_env = {
            "QA_BUNDLE_ENABLED": "true",
            "QA_BUNDLE_QUEUE_URL": "https://sqs.example/queue",
            "QA_BUNDLE_STORAGE_DRIVER": "s3",
            "QA_BUNDLE_STORAGE_REGION": "us-east-1",
            "QA_BUNDLE_STORAGE_BUCKET": "qa-bucket",
            "QA_BUNDLE_STORAGE_PREFIX": "user-qa",
        }
        complete, params, remote = _render(env_extra=complete_env)
        self.assertEqual(complete.returncode, 0, msg=complete.stderr)
        self.assertIsNotNone(params)
        self.assertIsNotNone(remote)


if __name__ == "__main__":
    unittest.main()
