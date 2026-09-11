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


def _extract_shell_function(remote: str, name: str) -> str:
    start = remote.index(f"{name}() {{")
    end = remote.index("\n}\n\n", start) + len("\n}\n")
    return remote[start:end]


def _run_bytes_from_docker_mem(remote: str, value: str) -> subprocess.CompletedProcess[str]:
    function = _extract_shell_function(remote, "bytes_from_docker_mem")
    return subprocess.run(
        ["bash"],
        input=f"{function}\nbytes_from_docker_mem {shlex.quote(value)}\n",
        capture_output=True,
        text=True,
        check=False,
    )


def _run_clear_edge_profile_overrides(
    remote: str, profile: str
) -> subprocess.CompletedProcess[str]:
    function = _extract_shell_function(remote, "clear_edge_profile_overrides")
    script = f"""{function}
DEPLOY_PROFILE={shlex.quote(profile)}
QA_ARCHIVE_ENABLED=host-qa
TELEMETRY_ARCHIVE_BUCKET=host-telemetry
MEDIA_STORAGE_BUCKET=host-media
GATEWAY_IMAGE_CONCURRENCY_MAX_CONCURRENT_REQUESTS=host-limit
clear_edge_profile_overrides
printf 'qa=%s telemetry=%s media=%s limit=%s\\n' \
  "${{QA_ARCHIVE_ENABLED-unset}}" \
  "${{TELEMETRY_ARCHIVE_BUCKET-unset}}" \
  "${{MEDIA_STORAGE_BUCKET-unset}}" \
  "${{GATEWAY_IMAGE_CONCURRENCY_MAX_CONCURRENT_REQUESTS-unset}}"
"""
    return subprocess.run(
        ["bash"], input=script, capture_output=True, text=True, check=False
    )


def _run_commit_cutover_record_failure(
    remote: str,
    *,
    rollback_reload_fails: bool = False,
    live_write_fails: bool = False,
    target_reload_fails: bool = False,
    restore_write_fails: bool = False,
    active_write_fails: bool = False,
    active_restore_fails: bool = False,
    errexit: bool = False,
) -> subprocess.CompletedProcess[str]:
    function = "\n".join((
        _extract_shell_function(remote, "preserve_target_cutover"),
        _extract_shell_function(remote, "preserve_unconfirmed_reload"),
        _extract_shell_function(remote, "commit_cutover"),
    ))
    with tempfile.TemporaryDirectory(prefix="bluegreen-commit-") as tmp:
        root = pathlib.Path(tmp)
        caddy_dir = root / "caddy"
        caddy_dir.mkdir()
        live = caddy_dir / "Caddyfile"
        active = root / "active-color"
        live.write_text("old-route\n")
        active.write_text("blue\n")
        script = f"""{function}
color_container() {{ printf 'tokenkey-%s' "$1"; }}
die() {{ return 1; }}
log() {{ :; }}
render_caddy_with_upstream() {{ printf 'new-route:%s\\n' "$1" > "$2"; }}
write_active_color() {{
  [ "$ACTIVE_WRITE_FAILS" = 0 ] || return 1
  printf '%s\\n' "$1" > "$ACTIVE_FILE"
}}
record_cutover() {{ return 1; }}
sudo() {{
  if [ "$1" = mv ] && [ "$ACTIVE_RESTORE_FAILS" = 1 ]; then return 1; fi
  if [ "$1" = sh ]; then
    SH_WRITE_COUNT=$((SH_WRITE_COUNT + 1))
    if [ "$LIVE_WRITE_FAILS" = 1 ] && [ "$SH_WRITE_COUNT" -eq 1 ]; then return 1; fi
    if [ "$RESTORE_WRITE_FAILS" = 1 ] && [ "$SH_WRITE_COUNT" -eq 2 ]; then return 1; fi
  fi
  if [ "$1" = docker ]; then
    if [ "$2" = exec ]; then
      RELOAD_COUNT=$((RELOAD_COUNT + 1))
      if [ "$TARGET_RELOAD_FAILS" = 1 ] && [ "$RELOAD_COUNT" -eq 1 ]; then return 1; fi
      if [ "$ROLLBACK_RELOAD_FAILS" = 1 ] && [ "$RELOAD_COUNT" -eq 2 ]; then return 1; fi
    fi
    return 0
  fi
  command "$@"
}}
CADDY_DIR={shlex.quote(str(caddy_dir))}
LIVE_CADDY={shlex.quote(str(live))}
ACTIVE_FILE={shlex.quote(str(active))}
CUTOVER_COMMITTED=0
ROUTE_SWITCHED=0
RELOAD_COUNT=0
SH_WRITE_COUNT=0
ROLLBACK_RELOAD_FAILS={1 if rollback_reload_fails else 0}
LIVE_WRITE_FAILS={1 if live_write_fails else 0}
TARGET_RELOAD_FAILS={1 if target_reload_fails else 0}
RESTORE_WRITE_FAILS={1 if restore_write_fails else 0}
ACTIVE_WRITE_FAILS={1 if active_write_fails else 0}
ACTIVE_RESTORE_FAILS={1 if active_restore_fails else 0}
{'set -e' if errexit else ''}
active_state() {{ if [ -f "$ACTIVE_FILE" ]; then cat "$ACTIVE_FILE"; else printf '<missing>'; fi; }}
trap 'printf "rc=%s committed=%s live=%s active=%s\\n" "$?" "$CUTOVER_COMMITTED" "$(cat "$LIVE_CADDY")" "$(active_state)"' EXIT
commit_cutover green
rc=$?
trap - EXIT
printf 'rc=%s committed=%s live=%s active=%s\\n' "$rc" "$CUTOVER_COMMITTED" "$(cat "$LIVE_CADDY")" "$(active_state)"
"""
        return subprocess.run(
            ["bash"], input=script, capture_output=True, text=True, check=False
        )


def _run_systemd_start(
    remote: str, *, active: str | None, caddy_upstream: str | None
) -> subprocess.CompletedProcess[str]:
    marker = (
        "sudo tee /usr/local/bin/tokenkey-bluegreen-systemd-start.sh "
        ">/dev/null <<'SH'\n"
    )
    start = remote.index(marker) + len(marker)
    end = remote.index(
        "\nSH\n  sudo tee /usr/local/bin/tokenkey-bluegreen-systemd-stop.sh", start
    )
    systemd_start = remote[start:end]
    with tempfile.TemporaryDirectory(prefix="bluegreen-systemd-") as tmp:
        root = pathlib.Path(tmp)
        bin_dir = root / "bin"
        caddy_dir = root / "caddy"
        bin_dir.mkdir()
        caddy_dir.mkdir()
        if active is not None:
            (root / "active-color").write_text(f"{active}\n")
        if caddy_upstream is not None:
            (caddy_dir / "Caddyfile").write_text(
                f"reverse_proxy {caddy_upstream} {{\n}}\n"
            )
        fake_docker = bin_dir / "docker"
        fake_docker.write_text(
            "#!/usr/bin/env bash\n"
            "printf '%s\\n' \"$*\"\n"
        )
        fake_docker.chmod(0o755)
        runnable = systemd_start.replace("ROOT=/var/lib/tokenkey", f"ROOT={root}")
        env = {
            **os.environ,
            "PATH": f"{bin_dir}:{os.environ['PATH']}",
        }
        return subprocess.run(
            ["bash"], input=runnable, env=env, capture_output=True, text=True, check=False
        )


def _run_cutover_path(remote: str, function_name: str, stage: str = "deploy") -> subprocess.CompletedProcess[str]:
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
commit_cutover() { CUTOVER_COMMITTED=1; printf 'commit:%s\n' "$1"; }
observe_routed_health() { printf 'observe:%s\n' "$1"; }
drain_container() { printf 'drain:%s\n' "$1"; }
record_pending_drain() { printf 'pending:%s\n' "$1"; }
PENDING_DRAIN=/unused/pending
admit_edge_candidate() { :; }
assert_active_route_consistent() { :; }
write_prepared_receipt() { printf 'prepared:%s:%s\n' "$1" "$2"; }
TARGET_CONTAINER=""
CUTOVER_COMMITTED=0
DEPLOY_PROFILE=prod
TAG=1.8.99
"""
    if function_name == "ensure_legacy_cutover":
        setup = """
read_active_color() { :; }
container_image() { printf 'ghcr.io/youxuanxue/sub2api:1.8.98\n'; }
image_repo() { printf 'ghcr.io/youxuanxue/sub2api\n'; }
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
TELEMETRY_ARCHIVE_ENABLED=""
deploy_target_color
"""
    return subprocess.run(
        ["bash"],
        input="set -euo pipefail\nSTAGE=" + shlex.quote(stage) + "\n" + function + common + setup,
        capture_output=True,
        text=True,
        check=False,
    )


def _run_active_route_consistency(
    remote: str, active: str, caddy_upstream: str
) -> subprocess.CompletedProcess[str]:
    functions = "\n".join((
        _extract_shell_function(remote, "caddy_active_color"),
        _extract_shell_function(remote, "assert_active_route_consistent"),
    ))
    with tempfile.TemporaryDirectory(prefix="bluegreen-route-") as tmp:
        live = pathlib.Path(tmp) / "Caddyfile"
        live.write_text(f"reverse_proxy {caddy_upstream} {{\n}}\n")
        script = f"""{functions}
sudo() {{ command "$@"; }}
die() {{ printf 'die:%s\\n' "$*"; return 1; }}
LIVE_CADDY={shlex.quote(str(live))}
assert_active_route_consistent {shlex.quote(active)}
"""
        return subprocess.run(
            ["bash"], input=script, capture_output=True, text=True, check=False
        )


def _run_colored_route_without_active_color(
    remote: str, function_name: str
) -> subprocess.CompletedProcess[str]:
    function = _extract_shell_function(remote, function_name)
    extras = "\n".join((
        _extract_shell_function(remote, "caddy_active_color"),
        _extract_shell_function(remote, "read_active_color"),
        _extract_shell_function(remote, "assert_active_route_consistent"),
    ))
    with tempfile.TemporaryDirectory(prefix="bluegreen-recovery-") as tmp:
        root = pathlib.Path(tmp)
        caddy_dir = root / "caddy"
        caddy_dir.mkdir()
        live = caddy_dir / "Caddyfile"
        live.write_text("reverse_proxy tokenkey-green:8080 {\n}\n")
        script = f"""{extras}
{function}
log() {{ :; }}
die() {{ printf 'die:%s\\n' "$*"; exit 1; }}
sudo() {{
  if [ "$1" = docker ] && [ "$2" = inspect ] && [ "$3" = tokenkey ]; then return 0; fi
  command "$@"
}}
backup_env() {{ printf 'backup\\n'; }}
env_set() {{ :; }}
write_bluegreen_compose() {{ :; }}
compose_bg() {{ printf 'compose:%s\\n' "$*"; }}
wait_healthy() {{ :; }}
wait_ready() {{ :; }}
install_bluegreen_systemd_unit() {{ :; }}
commit_cutover() {{ printf 'commit:%s\\n' "$1"; }}
observe_routed_health() {{ :; }}
drain_container() {{ :; }}
admit_edge_candidate() {{ :; }}
container_image() {{ printf 'ghcr.io/youxuanxue/sub2api:1.8.98\\n'; }}
image_repo() {{ printf 'ghcr.io/youxuanxue/sub2api\\n'; }}
env_get() {{ printf 'ghcr.io/youxuanxue/sub2api:1.8.98\\n'; }}
other_color() {{ printf 'green\\n'; }}
color_container() {{ printf 'tokenkey-%s\\n' "$1"; }}
ROOT={shlex.quote(str(root))}
ACTIVE_FILE={shlex.quote(str(root / "active-color"))}
LIVE_CADDY={shlex.quote(str(live))}
TARGET_CONTAINER=""
CUTOVER_COMMITTED=0
DEPLOY_PROFILE=prod
TAG=1.8.99
TELEMETRY_ARCHIVE_ENABLED=""
{function_name}
"""
        return subprocess.run(
            ["bash"], input=script, capture_output=True, text=True, check=False
        )


class BlueGreenRenderTest(unittest.TestCase):
    def test_drain_requires_verified_zero_before_stopping(self) -> None:
        proc, _, remote = _render()
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        function = _extract_shell_function(remote, "drain_container")
        cases = (
            ([3, 3, 3, 3, 2, 1, 0], "healthy", True),
            ([3, 3, 3, 3, 3, 3, 3], "healthy", False),
            (["bad"] * 7, "healthy", False),
            ([{"draining": False, "in_flight": 0}] * 7, "healthy", False),
            ([{"draining": True, "in_flight": "0"}] * 7, "healthy", False),
            ([0], "unhealthy", False),
        )
        for values, health, should_stop in cases:
            with self.subTest(values=values, health=health), tempfile.TemporaryDirectory() as tmp:
                root = pathlib.Path(tmp)
                samples = [json.dumps({"draining": True, "in_flight": v}) if isinstance(v, int)
                           else json.dumps(v) if isinstance(v, dict) else v for v in values]
                (root / "samples").write_text("\n".join(samples) + "\n")
                script = f"""set -euo pipefail
{function}
cd {shlex.quote(tmp)}
log() {{ :; }}
die() {{ echo "$*"; return 1; }}
container_health() {{ echo {shlex.quote(health)}; }}
seq() {{ command seq 1 {len(values)}; }}
sleep() {{ :; }}
sudo() {{
  case "$*" in
    'docker kill -s USR1 old') : ;;
    'docker exec old '* )
      head -1 samples
      sed '1d' samples > next
      mv next samples ;;
    *) echo "unexpected command: $*" >&2; return 1 ;;
  esac
}}
drain_container old
echo STOP_ALLOWED
"""
                result = subprocess.run(["bash"], input=script, capture_output=True, text=True)
                self.assertEqual(result.returncode == 0, should_stop, msg=result.stdout + result.stderr)
                self.assertEqual("STOP_ALLOWED" in result.stdout, should_stop)

    def test_prepare_stops_before_route_switch_and_drain(self) -> None:
        proc, params, remote = _render(env_extra={"STAGE0_BLUEGREEN_STAGE": "prepare"})
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn("STAGE='prepare'", params["commands"][-1])
        result = _run_cutover_path(remote, "deploy_target_color", "prepare")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("prepared:blue:green", result.stdout)
        self.assertNotIn("commit:", result.stdout)
        self.assertNotIn("drain:", result.stdout)

    def test_stages_require_valid_approval_and_existing_prod_layout(self) -> None:
        for env, instance in (
            ({"STAGE0_BLUEGREEN_STAGE": "invalid"}, _PROD_IID),
            ({"STAGE0_BLUEGREEN_STAGE": "promote"}, _PROD_IID),
            ({"STAGE0_BLUEGREEN_REPLACE_RECEIPT": "f" * 64}, _PROD_IID),
            ({"STAGE0_BLUEGREEN_STAGE": "prepare", "STAGE0_BLUEGREEN_REPLACE_RECEIPT": "invalid"}, _PROD_IID),
            ({"STAGE0_BLUEGREEN_STAGE": "prepare"}, _EDGE_IID),
        ):
            with self.subTest(env=env, instance=instance):
                proc, _, _ = _render(instance, env_extra=env)
                self.assertNotEqual(proc.returncode, 0)
        _, _, remote = _render()
        function = _extract_shell_function(remote, "run_bluegreen_stage")
        with tempfile.TemporaryDirectory() as tmp:
            receipt = pathlib.Path(tmp) / "receipt.json"
            for stage, exists in (("prepare", False), ("prepare", True), ("deploy", True)):
                if exists:
                    receipt.write_text("{}")
                script = f"""set -euo pipefail
{function}
STAGE={stage}
PREPARED_FILE={shlex.quote(str(receipt))}
die() {{ echo "$*"; exit 1; }}
read_active_color() {{ :; }}
ensure_profile_environment() {{ echo MUTATION; }}
deploy_target_color() {{ echo MUTATION; }}
ensure_legacy_cutover() {{ echo MUTATION; }}
run_bluegreen_stage
"""
                result = subprocess.run(["bash"], input=script, text=True, capture_output=True)
                self.assertNotEqual(result.returncode, 0)
                self.assertNotIn("MUTATION", result.stdout)

    def test_replacement_validates_old_tag_and_preserves_pending_guard(self) -> None:
        proc, params, remote = _render(env_extra={
            "STAGE0_BLUEGREEN_STAGE": "prepare",
            "STAGE0_BLUEGREEN_REPLACE_RECEIPT": "f" * 64,
        })
        self.assertEqual(proc.returncode, 0, proc.stderr)
        self.assertIn("REPLACE_RECEIPT='" + "f" * 64 + "'", params["commands"][-1])
        function = _extract_shell_function(remote, "validate_candidate_replacement")
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            receipt = root / "receipt.json"
            receipt.write_text(json.dumps({"tag": "1.8.98"}))
            for invalid in (False, True):
                script = f"""set -euo pipefail
{function}
ROOT={shlex.quote(tmp)}
PREPARED_FILE={shlex.quote(str(receipt))}
REPLACE_RECEIPT={'f' * 64}
TAG=1.8.99
read_active_color() {{ echo blue; }}
other_color() {{ echo green; }}
assert_active_route_consistent() {{ :; }}
validate_prepared_receipt() {{ echo "validate:$TAG:$*"; return {1 if invalid else 0}; }}
sudo() {{ command "$@"; }}
die() {{ echo "$*"; exit 1; }}
validate_candidate_replacement
echo "next-tag:$TAG"
"""
                result = subprocess.run(["bash"], input=script, text=True, capture_output=True)
                self.assertEqual(result.returncode == 0, not invalid, result.stderr)
                self.assertIn("validate:1.8.98:blue green " + "f" * 64, result.stdout)
                self.assertTrue(receipt.exists())
                if not invalid:
                    self.assertIn("next-tag:1.8.99", result.stdout)
                    self.assertEqual((root / ("bluegreen-replaced-" + "f" * 64 + ".json")).read_bytes(), receipt.read_bytes())

    def test_promote_reuses_validated_container_without_starting_one(self) -> None:
        _, _, remote = _render()
        function = _extract_shell_function(remote, "promote_prepared_color")
        for validation_fails in (False, True):
            with self.subTest(validation_fails=validation_fails):
                script = f"""set -euo pipefail
{function}
ROOT=/unused
PREPARED_FILE=/unused/receipt
PENDING_DRAIN=/unused/pending
record_pending_drain() {{ :; }}
read_active_color() {{ echo blue; }}
other_color() {{ echo green; }}
assert_active_route_consistent() {{ :; }}
validate_prepared_receipt() {{ echo validate; return {1 if validation_fails else 0}; }}
validate_replay_gate() {{ :; }}
wait_healthy() {{ echo healthy; }}
wait_ready() {{ echo ready; }}
backup_env() {{ :; }}
env_set() {{ echo env-set; }}
container_image() {{ echo repo:1.8.99; }}
commit_cutover() {{ echo cutover; }}
sudo() {{ echo "sudo:$*"; }}
observe_routed_health() {{ :; }}
install_bluegreen_systemd_unit() {{ :; }}
drain_container() {{ echo drain; }}
log() {{ :; }}
promote_prepared_color
"""
                result = subprocess.run(["bash"], input=script, text=True, capture_output=True)
                self.assertEqual(result.returncode == 0, not validation_fails, result.stderr)
                self.assertEqual("cutover" in result.stdout, not validation_fails)
                self.assertNotIn("pull", result.stdout)
                self.assertNotIn("up -d", result.stdout)
                if not validation_fails:
                    self.assertEqual(result.stdout.count("validate"), 2)
                    self.assertLess(result.stdout.index("cutover"), result.stdout.index("drain"))

    def test_failed_replay_gate_blocks_direct_promote_before_cutover(self) -> None:
        _, _, remote = _render()
        function = _extract_shell_function(remote, "promote_prepared_color")
        script = """set -euo pipefail
%s
read_active_color() { echo blue; }
other_color() { echo green; }
assert_active_route_consistent() { :; }
validate_prepared_receipt() { :; }
validate_replay_gate() { echo replay-rejected; return 1; }
commit_cutover() { echo CUTOVER; }
promote_prepared_color
""" % function
        result = subprocess.run(["bash"], input=script, text=True, capture_output=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("replay-rejected", result.stdout)
        self.assertNotIn("CUTOVER", result.stdout)

    def test_prepared_receipt_rejects_approval_and_runtime_drift(self) -> None:
        import hashlib

        _, _, remote = _render()
        function = _extract_shell_function(remote, "validate_prepared_receipt")
        with tempfile.TemporaryDirectory() as tmp:
            receipt = pathlib.Path(tmp) / "receipt.json"
            snapshot = {"tag": "1.8.99", "target_container": "tested-id image start 0", "env_sha": "original"}
            receipt.write_text(json.dumps({**snapshot, "prepared_at": "2026-09-08T00:00:00Z"}))
            approved = hashlib.sha256(receipt.read_bytes()).hexdigest()
            for mutation in ("none", "approval", "container", "env", "missing"):
                with self.subTest(mutation=mutation):
                    current = dict(snapshot)
                    if mutation == "container":
                        current["target_container"] = "replacement-id image start 0"
                    if mutation == "env":
                        current["env_sha"] = "changed"
                    script = f"""set -euo pipefail
{function}
TAG=1.8.99
PREPARED_FILE={shlex.quote(str(receipt) + ('.missing' if mutation == 'missing' else ''))}
APPROVED_RECEIPT={approved if mutation != 'approval' else '0' * 64}
sudo() {{ command "$@"; }}
die() {{ echo "$*"; exit 1; }}
staged_snapshot() {{ printf '%s\\n' {shlex.quote(json.dumps(current))}; }}
container_image() {{ echo repo:1.8.99; }}
validate_prepared_receipt blue green
"""
                    result = subprocess.run(["bash"], input=script, text=True, capture_output=True)
                    self.assertEqual(result.returncode == 0, mutation == "none", result.stdout + result.stderr)

    def test_renders_lightsail_edge_profile_without_prod_defaults(self) -> None:
        proc, params, remote = _render(_EDGE_IID, env_extra={"EDGE_ID": "us2"})
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        assert params is not None
        assert remote is not None
        joined = "\n".join(params["commands"])
        self.assertIn("DEPLOY_PROFILE='edge'", joined)
        self.assertIn("QA_ARCHIVE_ENABLED=''", joined)
        self.assertIn("QA_ARCHIVE_STORAGE_BUCKET=''", joined)
        self.assertIn("TELEMETRY_ARCHIVE_BUCKET=''", joined)
        self.assertIn("MEDIA_STORAGE_BUCKET=''", joined)
        self.assertIn("EDGE_MIN_MEM_AVAILABLE_BYTES='335544320'", joined)
        self.assertIn("EDGE_ACTIVE_APP_HEADROOM_BYTES='134217728'", joined)
        self.assertIn("EDGE_MIN_ROOT_DISK_AVAILABLE_BYTES='5368709120'", joined)
        self.assertIn('${EDGE_MIN_MEM_AVAILABLE_BYTES:?}', remote)
        self.assertIn('${EDGE_ACTIVE_APP_HEADROOM_BYTES:?}', remote)
        self.assertIn('${EDGE_MIN_ROOT_DISK_AVAILABLE_BYTES:?}', remote)
        self.assertNotIn('memory_floor_bytes=335544320', remote)
        self.assertIn('ops/stage0/bluegreen-capacity-policy.env', remote)
        cleared = _run_clear_edge_profile_overrides(remote, "edge")
        self.assertEqual(cleared.returncode, 0, msg=cleared.stderr)
        self.assertEqual(
            cleared.stdout.strip(),
            "qa=unset telemetry=unset media=unset limit=unset",
        )

        preserved = _run_clear_edge_profile_overrides(remote, "prod")
        self.assertEqual(preserved.returncode, 0, msg=preserved.stderr)
        self.assertEqual(
            preserved.stdout.strip(),
            "qa=host-qa telemetry=host-telemetry media=host-media limit=host-limit",
        )

    def test_rejects_unknown_instance_id_shape(self) -> None:
        proc, params, remote = _render("x-invalid")
        self.assertNotEqual(proc.returncode, 0)
        self.assertIn("requires EC2 i-* or managed-instance mi-*", proc.stderr)
        self.assertIsNone(params)
        self.assertIsNone(remote)

    def test_edge_profile_explicitly_disables_qa_capture_for_both_colors(self) -> None:
        proc, _, remote = _render(_EDGE_IID, env_extra={"EDGE_ID": "us5"})
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        assert remote is not None

        ensure_profile = _extract_shell_function(remote, "ensure_profile_environment")
        edge_branch_start = ensure_profile.index(
            'if [[ "${DEPLOY_PROFILE}" = edge ]]; then'
        )
        edge_branch_end = ensure_profile.index("  fi", edge_branch_start)
        edge_branch = ensure_profile[edge_branch_start:edge_branch_end]
        self.assertIn("env_set QA_CAPTURE_ENABLED false", edge_branch)
        self.assertIn("enforced QA_CAPTURE_ENABLED=false", edge_branch)
        self.assertEqual(
            remote.count("- QA_CAPTURE_ENABLED=${QA_CAPTURE_ENABLED:-}"),
            2,
        )

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
        self.assertIn("DEPLOY_PROFILE='prod'", joined)
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
        self.assertIn("commit_cutover", remote)
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
        self.assertNotIn('target_is_reusable()', remote)
        self.assertIn('compose_bg up -d --no-deps --force-recreate "${target_container}"', remote)
        self.assertLess(
            remote.index('admit_edge_candidate "${active_container}"'),
            remote.index('compose_bg pull "${target_container}"'),
        )
        self.assertNotIn('docker stop -t 30 "${active_container}" >/dev/null 2>&1 || true', remote)
        self.assertIn('removed failed target ${TARGET_CONTAINER}', remote)
        self.assertIn('trap cleanup_on_exit EXIT', remote)
        self.assertNotIn('trap on_err ERR', remote)
        self.assertIn('observe_routed_health', remote)
        self.assertIn('TOKENKEY_BLUEGREEN_OBSERVE_SECONDS:-30', remote)
        self.assertIn('curl -fsS --max-time 5 --resolve "${domain}:443:127.0.0.1"', remote)
        self.assertIn('"https://${domain}/health"', remote)
        self.assertNotIn('"http://${upstream}/health"', remote)
        self.assertNotIn("--no-check-certificate", remote)
        self.assertIn("last-cutover-at", remote)

    def test_commit_cutover_marks_committed_only_after_route_and_state_persist(self) -> None:
        proc, _, remote = _render()
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        assert remote is not None
        function = _extract_shell_function(remote, "commit_cutover")
        success_branch = function.index('if write_active_color "${color}" && record_cutover; then')
        durable_commit = function.index("CUTOVER_COMMITTED=1", success_branch)
        self.assertLess(success_branch, durable_commit)
        self.assertLess(
            function.index('sudo cp -a "${tmp}" "${target_config}"'),
            function.index('cat \'${tmp}\' > \'${LIVE_CADDY}\''),
        )
        self.assertIn("restore previous Caddyfile", function)

        live_write_failed = _run_commit_cutover_record_failure(
            remote, live_write_fails=True
        )
        self.assertEqual(live_write_failed.returncode, 0, msg=live_write_failed.stderr)
        self.assertIn("rc=1 committed=0 live=old-route active=blue", live_write_failed.stdout)

        failed = _run_commit_cutover_record_failure(remote)
        self.assertEqual(failed.returncode, 0, msg=failed.stderr)
        self.assertIn("rc=1 committed=0 live=old-route active=blue", failed.stdout)

        rollback_reload_failed = _run_commit_cutover_record_failure(
            remote, rollback_reload_fails=True
        )
        self.assertEqual(rollback_reload_failed.returncode, 0, msg=rollback_reload_failed.stderr)
        self.assertIn(
            "rc=1 committed=1 live=new-route:tokenkey-green:8080 active=green",
            rollback_reload_failed.stdout,
        )

        target_reload_restored = _run_commit_cutover_record_failure(
            remote,
            target_reload_fails=True,
        )
        self.assertEqual(target_reload_restored.returncode, 0, msg=target_reload_restored.stderr)
        self.assertIn(
            "rc=1 committed=0 live=old-route active=blue",
            target_reload_restored.stdout,
        )

        restore_reload_failed = _run_commit_cutover_record_failure(
            remote,
            target_reload_fails=True,
            rollback_reload_fails=True,
            errexit=True,
        )
        self.assertNotEqual(restore_reload_failed.returncode, 0)
        self.assertIn(
            "rc=1 committed=1 live=old-route active=<missing>",
            restore_reload_failed.stdout,
        )

        restore_write_failed = _run_commit_cutover_record_failure(
            remote,
            target_reload_fails=True,
            restore_write_fails=True,
            errexit=True,
        )
        self.assertNotEqual(restore_write_failed.returncode, 0)
        self.assertIn(
            "rc=1 committed=1 live=new-route:tokenkey-green:8080 active=<missing>",
            restore_write_failed.stdout,
        )

        commit_restore_write_failed = _run_commit_cutover_record_failure(
            remote,
            restore_write_fails=True,
            active_write_fails=True,
            errexit=True,
        )
        self.assertNotEqual(commit_restore_write_failed.returncode, 0)
        self.assertIn(
            "rc=1 committed=1 live=new-route:tokenkey-green:8080 active=blue",
            commit_restore_write_failed.stdout,
        )

        active_restore_failed = _run_commit_cutover_record_failure(
            remote,
            active_restore_fails=True,
            errexit=True,
        )
        self.assertNotEqual(active_restore_failed.returncode, 0)
        self.assertIn(
            "rc=1 committed=1 live=new-route:tokenkey-green:8080 active=green",
            active_restore_failed.stdout,
        )

    def test_systemd_start_preserves_route_when_durable_state_disagrees(self) -> None:
        proc, _, remote = _render()
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        assert remote is not None

        matching = _run_systemd_start(
            remote, active="blue", caddy_upstream="tokenkey-blue:8080"
        )
        self.assertEqual(matching.returncode, 0, msg=matching.stderr)
        self.assertIn("up -d --no-deps tokenkey-blue", matching.stdout)
        self.assertNotIn("up -d --no-deps tokenkey-green", matching.stdout)

        mismatch = _run_systemd_start(
            remote, active="blue", caddy_upstream="tokenkey-green:8080"
        )
        self.assertEqual(mismatch.returncode, 0, msg=mismatch.stderr)
        self.assertIn(
            "up -d --no-deps tokenkey-blue tokenkey-green", mismatch.stdout
        )

        unknown = _run_systemd_start(remote, active="invalid", caddy_upstream=None)
        self.assertEqual(unknown.returncode, 0, msg=unknown.stderr)
        self.assertIn(
            "up -d --no-deps tokenkey-blue tokenkey-green", unknown.stdout
        )

    def test_unconfirmed_target_reload_clears_active_color_and_blocks_next_deploy(self) -> None:
        proc, _, remote = _render()
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        assert remote is not None

        unconfirmed = _run_commit_cutover_record_failure(
            remote,
            target_reload_fails=True,
            restore_write_fails=True,
        )
        self.assertEqual(unconfirmed.returncode, 0, msg=unconfirmed.stderr)
        self.assertIn(
            "rc=1 committed=1 live=new-route:tokenkey-green:8080 active=<missing>",
            unconfirmed.stdout,
        )

        blocked_legacy = _run_colored_route_without_active_color(
            remote, "ensure_legacy_cutover"
        )
        self.assertNotEqual(blocked_legacy.returncode, 0)
        self.assertIn("colored Caddy route", blocked_legacy.stdout)
        self.assertIn("no active-color", blocked_legacy.stdout)
        self.assertNotIn("commit:", blocked_legacy.stdout)

        blocked_target = _run_colored_route_without_active_color(
            remote, "deploy_target_color"
        )
        self.assertNotEqual(blocked_target.returncode, 0)
        self.assertIn("invalid or missing active color", blocked_target.stdout)
        self.assertNotIn("commit:", blocked_target.stdout)

    def test_active_color_must_match_caddy_before_target_selection(self) -> None:
        proc, _, remote = _render()
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        assert remote is not None

        matching = _run_active_route_consistency(remote, "blue", "tokenkey-blue:8080")
        self.assertEqual(matching.returncode, 0, msg=matching.stderr + matching.stdout)

        mismatch = _run_active_route_consistency(remote, "blue", "tokenkey-green:8080")
        self.assertNotEqual(mismatch.returncode, 0)
        self.assertIn("disagrees with Caddy route green", mismatch.stdout)

    def test_legacy_and_regular_cutovers_share_committed_state_owner(self) -> None:
        proc, _, remote = _render()
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        assert remote is not None

        cases = [
            ("ensure_legacy_cutover", ["pending:tokenkey", "commit:blue", "observe:blue", "drain:tokenkey"]),
            ("deploy_target_color", ["pending:tokenkey-blue", "commit:green", "observe:green", "drain:tokenkey-blue"]),
        ]
        for function_name, expected in cases:
            with self.subTest(function_name=function_name):
                cutover = _run_cutover_path(remote, function_name)
                self.assertEqual(cutover.returncode, 0, msg=cutover.stderr)
                self.assertEqual(cutover.stdout.splitlines(), expected)

    def test_pending_drain_survives_failure_and_rejects_live_or_replaced_container(self) -> None:
        _, _, remote = _render()
        function = _extract_shell_function(remote, "finish_pending_drain")
        for active, identity, drain_rc, succeeds in (
            ("green", "original", 0, True), ("blue", "original", 0, False),
            ("green", "replacement", 0, False), ("green", "original", 1, False),
        ):
            with self.subTest(active=active, identity=identity, drain_rc=drain_rc), tempfile.TemporaryDirectory() as directory:
                pending = pathlib.Path(directory) / "pending.json"
                pending.write_text(json.dumps({"container": "tokenkey-blue", "identity": "original"}))
                script = f'''set -euo pipefail
{function}
PENDING_DRAIN={shlex.quote(str(pending))}
read_active_color() {{ echo {active}; }}
color_container() {{ echo "tokenkey-$1"; }}
die() {{ echo "$*" >&2; exit 1; }}
drain_container() {{ echo drain; return {drain_rc}; }}
sudo() {{
  if [[ "$1" != docker ]]; then "$@"; return; fi
  case "$*" in
    *State.Running*) echo true ;;
    *inspect*) echo {identity} ;;
    *stop*) echo stop >&2 ;;
    *) return 42 ;;
  esac
}}
finish_pending_drain
'''
                result = subprocess.run(["bash"], input=script, text=True, capture_output=True)
                self.assertEqual(result.returncode == 0, succeeds, result.stderr)
                self.assertEqual(pending.exists(), not succeeds)
                self.assertEqual("stop" in result.stderr, succeeds)

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

    def test_docker_memory_units_parse_without_host_specific_numfmt(self) -> None:
        proc, _, remote = _render()
        self.assertEqual(proc.returncode, 0, msg=proc.stderr)
        assert remote is not None
        cases = {
            "213.4MiB": "223766118",
            "1.5GiB": "1610612736",
            "500MB": "500000000",
        }
        for raw, expected in cases.items():
            with self.subTest(raw=raw):
                parsed = _run_bytes_from_docker_mem(remote, raw)
                self.assertEqual(parsed.returncode, 0, msg=parsed.stderr)
                self.assertEqual(parsed.stdout.strip(), expected)

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
