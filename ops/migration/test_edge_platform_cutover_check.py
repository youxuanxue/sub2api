#!/usr/bin/env python3
"""Behavior tests for the single-edge platform cutover gate."""

from __future__ import annotations

import copy
import datetime as dt
import importlib.util
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import unittest


REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
MODULE_PATH = REPO_ROOT / "ops/migration/edge_platform_cutover_check.py"
WRAPPER = REPO_ROOT / "ops/migration/edge-platform-cutover-check.sh"
REMOTE_PROBE = REPO_ROOT / "ops/migration/probe-edge-platform-cutover.sh"
SPEC = importlib.util.spec_from_file_location("edge_platform_cutover_check", MODULE_PATH)
assert SPEC and SPEC.loader
CUTOVER = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = CUTOVER
SPEC.loader.exec_module(CUTOVER)

NOW = dt.datetime(2026, 8, 10, 1, 0, tzinfo=dt.timezone.utc)


def base_observation() -> dict:
    return {
        "schema_version": 1,
        "edge_id": "us4",
        "source_ipv4": "198.51.100.10",
        "target_ipv4": "203.0.113.20",
        "matrix": {
            "lightsail_deployable": True,
            "ec2_deployable": False,
            "ec2_migration_candidate": True,
        },
        "dns": {
            "authoritative_ipv4": ["198.51.100.10"],
            "public_ipv4": ["198.51.100.10"],
        },
        "source": {
            "ssm_online": True,
            "docker_healthy": True,
            "health_ok": True,
            "account_total": 1,
            "schedulable_accounts": 1,
            "business_requests": 0,
            "served_requests": 0,
            "server_errors": 0,
            "p95_latency_ms": 110.0,
            "app_tag": "1.8.141",
        },
        "target": {
            "ssm_online": True,
            "docker_healthy": True,
            "health_ok": True,
            "public_health_ok": True,
            "oauth_model_smoke_ok": True,
            "cpu_credits": "unlimited",
            "instance_type": "t4g.small",
            "account_total": 1,
            "schedulable_accounts": 0,
            "business_requests": 100,
            "served_requests": 99,
            "server_errors": 0,
            "p95_latency_ms": 150.0,
            "app_tag": "1.8.141",
        },
        "baseline": {
            "business_requests": 100,
            "served_requests": 100,
            "p95_latency_ms": 100.0,
        },
        "alerts": {
            "p0_p1_open": 0,
            "edge_alert_open": False,
            "recovery_required": True,
            "feishu_recovery_delivered": True,
        },
    }


def healthy_candidate() -> dict:
    value = base_observation()
    value["observation_started_at"] = "2026-08-10T00:00:00Z"
    return value


def healthy_plan() -> dict:
    value = healthy_candidate()
    value["candidate_observation_started_at"] = value.pop("observation_started_at")
    value["rollback_ipv4"] = value["source_ipv4"]
    return value


def healthy_post_dns() -> dict:
    value = base_observation()
    value["observation_started_at"] = "2026-08-10T00:50:00Z"
    value["matrix"].update(
        lightsail_deployable=False,
        ec2_deployable=True,
        ec2_migration_candidate=False,
    )
    value["dns"].update(
        authoritative_ipv4=[value["target_ipv4"]],
        public_ipv4=[value["target_ipv4"]],
    )
    value["target"]["schedulable_accounts"] = 1
    return value


def healthy_rollback_ready() -> dict:
    value = healthy_post_dns()
    value["rollback_ipv4"] = value["source_ipv4"]
    return value


class EdgePlatformCutoverCheckTests(unittest.TestCase):
    maxDiff = None

    def assert_blocked(self, phase: str, observation: dict, code: str) -> dict:
        report = CUTOVER.evaluate_observation(phase, observation, NOW)
        self.assertIn(code, report["blockers"], report)
        return report

    def test_candidate_accepts_a_complete_one_hour_window(self) -> None:
        fixture = healthy_candidate()
        fixture["target"]["public_health_ok"] = False
        report = CUTOVER.evaluate_observation("candidate", fixture, NOW)
        self.assertEqual([], report["blockers"])

    def test_candidate_rejects_short_observation(self) -> None:
        fixture = healthy_candidate()
        fixture["observation_started_at"] = "2026-08-10T00:00:01Z"
        self.assert_blocked("candidate", fixture, "candidate_observation_under_1h")

    def test_plan_rejects_target_account_enabled_before_cutover(self) -> None:
        fixture = healthy_plan()
        fixture["target"]["schedulable_accounts"] = 1
        self.assert_blocked(
            "plan",
            fixture,
            "target_accounts_schedulable_before_cutover",
        )

    def test_candidate_rejects_active_p0_or_p1_alert(self) -> None:
        fixture = healthy_candidate()
        fixture["alerts"]["p0_p1_open"] = 1
        self.assert_blocked("candidate", fixture, "active_p0_p1_alerts")

    def test_plan_requires_a_healthy_source_and_matching_rollback_ip(self) -> None:
        fixture = healthy_plan()
        fixture["source"]["ssm_online"] = False
        fixture["rollback_ipv4"] = "192.0.2.55"
        report = CUTOVER.evaluate_observation("plan", fixture, NOW)
        self.assertIn("rollback_source_unavailable", report["blockers"])
        self.assertIn("rollback_ipv4_mismatch", report["blockers"])

    def test_plan_accepts_verified_zero_account_posture_without_fake_smoke(self) -> None:
        fixture = healthy_plan()
        fixture["target"].update(account_total=0, schedulable_accounts=0)
        del fixture["target"]["oauth_model_smoke_ok"]
        report = CUTOVER.evaluate_observation("plan", fixture, NOW)
        self.assertEqual([], report["blockers"])

    def test_all_phases_reject_dual_owner(self) -> None:
        fixture = healthy_plan()
        fixture["matrix"]["ec2_deployable"] = True
        self.assert_blocked("plan", fixture, "matrix_dual_owner")

    def test_post_dns_accepts_a_complete_ten_minute_window(self) -> None:
        report = CUTOVER.evaluate_observation("post-dns", healthy_post_dns(), NOW)
        self.assertEqual([], report["blockers"])

    def test_post_dns_accepts_verified_zero_account_idle_window(self) -> None:
        fixture = healthy_post_dns()
        fixture["target"].update(
            account_total=0,
            schedulable_accounts=0,
            business_requests=0,
            served_requests=0,
            p95_latency_ms=None,
        )
        fixture["baseline"].update(
            business_requests=0,
            served_requests=0,
            p95_latency_ms=None,
        )
        del fixture["target"]["oauth_model_smoke_ok"]
        report = CUTOVER.evaluate_observation("post-dns", fixture, NOW)
        self.assertEqual([], report["blockers"])

    def test_post_dns_rejects_source_traffic_and_short_window(self) -> None:
        fixture = healthy_post_dns()
        fixture["source"]["business_requests"] = 1
        fixture["observation_started_at"] = "2026-08-10T00:50:01Z"
        report = CUTOVER.evaluate_observation("post-dns", fixture, NOW)
        self.assertIn("source_business_traffic_present", report["blockers"])
        self.assertIn("cutover_observation_under_10m", report["blockers"])

    def test_post_dns_rejects_dns_not_unique_to_target(self) -> None:
        fixture = healthy_post_dns()
        fixture["dns"]["public_ipv4"].append(fixture["source_ipv4"])
        self.assert_blocked("post-dns", fixture, "dns_not_target_only")

    def test_target_ssm_and_unlimited_are_required(self) -> None:
        fixture = healthy_candidate()
        fixture["target"]["ssm_online"] = False
        fixture["target"]["cpu_credits"] = "standard"
        report = CUTOVER.evaluate_observation("candidate", fixture, NOW)
        self.assertIn("target_ssm_offline", report["blockers"])
        self.assertIn("target_cpu_credits_not_unlimited", report["blockers"])

    def test_post_dns_rejects_served_ratio_drop_over_five_points(self) -> None:
        fixture = healthy_post_dns()
        fixture["target"]["served_requests"] = 94
        self.assert_blocked("post-dns", fixture, "served_ratio_drop_over_5pp")

    def test_post_dns_rejects_p95_over_twice_source_baseline(self) -> None:
        fixture = healthy_post_dns()
        fixture["target"]["p95_latency_ms"] = 200.01
        self.assert_blocked("post-dns", fixture, "p95_latency_over_2x_source")

    def test_post_dns_requires_alert_recovery_and_feishu_delivery(self) -> None:
        fixture = healthy_post_dns()
        fixture["alerts"]["edge_alert_open"] = True
        fixture["alerts"]["feishu_recovery_delivered"] = False
        report = CUTOVER.evaluate_observation("post-dns", fixture, NOW)
        self.assertIn("edge_alert_not_recovered", report["blockers"])
        self.assertIn("feishu_recovery_missing", report["blockers"])

    def test_rollback_ready_rejects_unavailable_source(self) -> None:
        fixture = healthy_rollback_ready()
        fixture["source"]["health_ok"] = False
        self.assert_blocked("rollback-ready", fixture, "rollback_source_unavailable")

    def test_missing_signal_is_a_blocker_and_cli_exits_nonzero(self) -> None:
        fixture = healthy_candidate()
        del fixture["target"]["docker_healthy"]
        report = CUTOVER.evaluate_observation("candidate", fixture, NOW)
        self.assertIn("missing:target.docker_healthy", report["blockers"])

        with tempfile.TemporaryDirectory() as tmp:
            fixture_path = pathlib.Path(tmp) / "fixture.json"
            fixture_path.write_text(json.dumps(fixture), encoding="utf-8")
            completed = subprocess.run(
                [
                    "bash",
                    str(WRAPPER),
                    "--phase",
                    "candidate",
                    "--fixture",
                    str(fixture_path),
                    "--now",
                    "2026-08-10T01:00:00Z",
                ],
                cwd=REPO_ROOT,
                text=True,
                capture_output=True,
                check=False,
            )
        self.assertEqual(1, completed.returncode, completed.stderr)
        self.assertIn("missing:target.docker_healthy", completed.stdout)

    def test_remote_probe_verifies_the_latest_logical_backup(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = pathlib.Path(tmp)
            bin_dir = root / "bin"
            lib_dir = root / "lib"
            bin_dir.mkdir()
            lib_dir.mkdir()
            (lib_dir / "resolve-app-container.sh").write_text(
                "tk_resolve_app_container() { echo tokenkey-app; }\n",
                encoding="utf-8",
            )
            tools = {
                "docker": r'''#!/usr/bin/env bash
case "$*" in
  *".State.Running"*) echo true ;;
  *".State.Health"*) echo healthy ;;
  *".Config.Image"*) echo ghcr.io/tokenkey/sub2api:1.8.141 ;;
  "exec tokenkey-postgres"*) echo '0|0' ;;
  "logs tokenkey-app"*) echo '{"msg":"http request completed","status_code":200,"latency_ms":10}' ;;
  "exec tokenkey-app wget"*) exit 0 ;;
  *) echo "unexpected docker call: $*" >&2; exit 90 ;;
esac
''',
                "find": "#!/usr/bin/env bash\necho /var/lib/tokenkey/pgdump/tokenkey.sql.gz\n",
                "stat": "#!/usr/bin/env bash\necho 4096\n",
                "gzip": "#!/usr/bin/env bash\nexit 0\n",
                "sha256sum": "#!/usr/bin/env bash\necho 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  '$2\n",
            }
            for name, body in tools.items():
                path = bin_dir / name
                path.write_text(body, encoding="utf-8")
                path.chmod(0o755)
            env = os.environ.copy()
            env.update({
                "PATH": f"{bin_dir}:{env['PATH']}",
                "TK_LIB_DIR": str(lib_dir),
            })
            completed = subprocess.run(
                ["bash", str(REMOTE_PROBE)],
                cwd=REPO_ROOT,
                env=env,
                text=True,
                capture_output=True,
                check=False,
            )
        self.assertEqual(0, completed.returncode, completed.stderr)
        payload = json.loads(completed.stdout)
        self.assertEqual(
            {
                "verified": True,
                "path": "/var/lib/tokenkey/pgdump/tokenkey.sql.gz",
                "size_bytes": 4096,
                "checksum": "sha256:" + "a" * 64,
            },
            payload.get("logical_backup"),
        )


if __name__ == "__main__":
    unittest.main()
