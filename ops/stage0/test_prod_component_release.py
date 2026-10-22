#!/usr/bin/env python3
"""Behavioral coverage for independent releases and split cutover/completion."""

from __future__ import annotations

import base64
import contextlib
import importlib.util
import io
import json
import os
import shlex
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(Path(__file__).resolve().parent))
import bluegreen_completion as completion
import prod_release_plan as planner

REPOSITORY = "ghcr.io/youxuanxue/sub2api"
import prod_release_state as release_state

_install_spec = importlib.util.spec_from_file_location(
    "qa_runtime_install", ROOT / "ops/stage0/qa-runtime-install.py"
)
installer = importlib.util.module_from_spec(_install_spec)
_install_spec.loader.exec_module(installer)


class RawArchiveReleasePlanTest(unittest.TestCase):
    def test_rollout_is_raw_archive_contract(self):
        self.assertEqual(
            planner.release_mode(ROOT / "ops/qa/deploy_rollout.yaml"), "raw_archive"
        )

    def test_unknown_contract_fails_closed(self):
        with tempfile.TemporaryDirectory() as d:
            p = Path(d) / "rollout.yaml"
            p.write_text(
                "prod: {component_release: {runtime_contract: old_worker_v1}}\n"
            )
            with self.assertRaisesRegex(ValueError, "unsupported"):
                planner.release_mode(p)

    def test_missing_and_malformed_contracts_fail_closed(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "rollout.yaml"
            for body in (
                "{}",
                "[]",
                "prod: []",
                "prod: {component_release: []}",
                "prod: {component_release: {runtime_contract: independent_v1}}",
            ):
                with self.subTest(body=body):
                    path.write_text(body)
                    with self.assertRaisesRegex(
                        ValueError, "unsupported runtime contract"
                    ):
                        planner.release_mode(path)

    def test_cli_rejects_legacy_before_publishing_workflow_output(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "rollout.yaml"
            path.write_text(
                "prod: {component_release: {runtime_contract: independent_v1}}"
            )
            result = subprocess.run(
                [
                    sys.executable,
                    str(ROOT / "ops/stage0/prod_release_plan.py"),
                    "--operation",
                    "classify",
                    "--rollout",
                    str(path),
                ],
                text=True,
                capture_output=True,
                check=False,
            )
            self.assertEqual(result.returncode, 2)
            self.assertEqual(result.stdout, "")
            self.assertIn("raw archive compatibility", result.stderr)


class BlueGreenCompletionTest(unittest.TestCase):
    token = "a" * 32

    def test_us051_cutover_returns_while_original_deploy_is_draining(self):
        receipt = {
            "token": self.token,
            "tag": "1.8.215",
            "cutover_at": "2026-09-10T02:59:01Z",
        }
        with (
            patch.object(
                completion, "invocation", return_value={"Status": "InProgress"}
            ) as inspect,
            patch.object(
                completion.ssm_execution,
                "run_shell_b64",
                return_value=json.dumps(receipt),
            ),
        ):
            result = completion.wait(
                "instance", "command", self.token, "1.8.215", "cutover", 10
            )
        self.assertEqual(result, receipt["cutover_at"])
        self.assertEqual(inspect.call_count, 1)

    def test_us051_stale_receipt_cannot_claim_cutover(self):
        receipt = {
            "token": "b" * 32,
            "tag": "1.8.215",
            "cutover_at": "2026-09-10T02:59:01Z",
        }
        with self.assertRaises(ValueError):
            completion.validate_receipt(receipt, self.token, "1.8.215")

    def test_us051_completion_waits_and_propagates_drain_failure(self):
        with (
            patch.object(
                completion,
                "invocation",
                side_effect=[
                    {"Status": "InProgress"},
                    {"Status": "Failed", "ResponseCode": 1},
                ],
            ),
            patch.object(completion.time, "sleep"),
        ):
            with self.assertRaises(RuntimeError):
                completion.wait(
                    "instance", "command", self.token, "1.8.215", "complete", 10
                )

    def test_us051_completion_requires_successful_remote_exit(self):
        with patch.object(
            completion,
            "invocation",
            return_value={"Status": "Success", "ResponseCode": 0},
        ):
            self.assertIsNone(
                completion.wait(
                    "instance", "command", self.token, "1.8.215", "complete", 10
                )
            )
        with patch.object(
            completion,
            "invocation",
            return_value={"Status": "Success", "ResponseCode": 1},
        ):
            with self.assertRaises(RuntimeError):
                completion.wait(
                    "instance", "command", self.token, "1.8.215", "complete", 10
                )


class QARuntimeResolverTest(unittest.TestCase):
    def resolve(self, network="tokenkey-data", contract="independent-v1"):
        script = (ROOT / "deploy/aws/stage0/qa-runtime.sh").read_text()
        script += r"""
qa_docker() {
  case "$*" in
    *dev.tokenkey.qa-runtime*) printf '%s\n' "$TEST_CONTRACT" ;;
    *HostConfig.NetworkMode*) printf '%s\n' "$TEST_NETWORK" ;;
    *State.Running*) echo false ;;
    'network inspect '*) return 0 ;;
    *) return 42 ;;
  esac
}
tk_resolve_qa_runtime
"""
        return subprocess.run(
            ["bash"],
            input=script,
            text=True,
            capture_output=True,
            env={
                **os.environ,
                "TEST_NETWORK": network,
                "TEST_CONTRACT": contract,
                "ACTIVE_COLOR_FILE": "/missing-gateway-color",
            },
        )

    def test_us051_runtime_does_not_depend_on_gateway_color(self):
        result = self.resolve()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), "tokenkey-qa-runtime")

    def test_us051_missing_pin_and_gateway_network_are_rejected(self):
        for network in ("host", "none", "bridge", "container:tokenkey-green", ""):
            with self.subTest(network=network):
                self.assertNotEqual(self.resolve(network=network).returncode, 0)
        self.assertNotEqual(self.resolve(contract="").returncode, 0)


class QARuntimeInstallTest(unittest.TestCase):
    def run_install(self, *, fail_promotion=False, keep_previous=False, live_postgres="tokenkey-postgres",
                    networks=None, expected_network="data"):
        image = REPOSITORY + ":1.8.215"
        names = {"tokenkey-qa-runtime": "old"}
        captured_env = []
        networks = {"data": {}} if networks is None else networks

        def command(*args):
            if args[:2] == ("docker", "inspect") and "{{.State.Running}}" in args:
                if args[-1] == live_postgres:
                    return "true"
                raise installer.subprocess.CalledProcessError(1, args)
            if args[:3] == ("docker", "inspect", live_postgres):
                return json.dumps([{"NetworkSettings": {"Networks": networks}}])
            if args[:2] == ("docker", "compose"):
                return json.dumps(
                    {
                        "services": {
                            "runtime": {
                                "image": image,
                                "labels": {},
                                "environment": {
                                    "DATABASE_PASSWORD": "private",
                                    "QA_ARCHIVE_SEAL_DELAY_MINUTES": "15",
                                },
                            }
                        }
                    }
                )
            if args[:2] == ("docker", "pull"):
                return image
            if args[:2] == ("docker", "ps"):
                return names.get("tokenkey-qa-runtime", "")
            if args[:2] == ("docker", "create"):
                names[args[3]] = "new"
                captured_env.append(
                    Path(args[args.index("--env-file") + 1]).read_text()
                )
                self.assertEqual(args[args.index("--network") + 1], expected_network)
                return "new"
            if args[:2] == ("docker", "inspect"):
                return json.dumps(
                    [
                        {
                            "Id": names[args[2]],
                            "Image": "sha256:new",
                            "Config": {"Image": image},
                            "State": {"Running": False},
                        }
                    ]
                )
            if args[:2] == ("docker", "rename"):
                if fail_promotion and names[args[2]] == "new":
                    raise RuntimeError("promotion failed")
                names[args[3]] = names.pop(args[2])
                return ""
            if args[:2] == ("docker", "rm"):
                names.pop(args[2])
                return ""
            self.fail(f"unexpected command {args[:2]}")

        with (
            patch.object(installer, "command", side_effect=command),
            patch.object(installer.subprocess, "run"),
            patch.dict(os.environ),
            contextlib.redirect_stdout(io.StringIO()),
        ):
            if fail_promotion:
                with self.assertRaisesRegex(RuntimeError, "promotion failed"):
                    installer.install(image, "template", keep_previous=keep_previous)
            else:
                installer.install(image, "template", keep_previous=keep_previous)
        return names, captured_env

    def test_us051_runtime_promotion_failure_restores_previous_pin(self):
        names, _ = self.run_install(fail_promotion=True)
        self.assertEqual(names["tokenkey-qa-runtime"], "old")

    def test_us051_success_pins_config_and_retains_previous_until_host_commit(self):
        names, captured_env = self.run_install(keep_previous=True)
        self.assertEqual(names["tokenkey-qa-runtime"], "new")
        self.assertIn("old", names.values())
        self.assertIn("QA_ARCHIVE_SEAL_DELAY_MINUTES=15\n", captured_env[0])

    def test_tencent_layout_postgres_network_is_resolved(self):
        names, _ = self.run_install(live_postgres="tk-archive-standby")
        self.assertEqual(names["tokenkey-qa-runtime"], "new")

    def test_multi_network_postgres_pins_the_tokenkey_postgres_alias_network(self):
        # Tencent prod: tk-archive-standby sits on two networks; DATABASE_HOST
        # tokenkey-postgres only resolves on the one that advertises the alias.
        names, _ = self.run_install(
            live_postgres="tk-archive-standby",
            networks={
                "tk-cutover-prod": {"Aliases": ["tk-archive-standby", "postgres"]},
                "tokenkey_tokenkey-network": {"Aliases": ["postgres", "tokenkey-postgres"]},
            },
            expected_network="tokenkey_tokenkey-network",
        )
        self.assertEqual(names["tokenkey-qa-runtime"], "new")

    def test_multi_network_postgres_without_alias_is_rejected_before_pin_change(self):
        with self.assertRaisesRegex(ValueError, "database network is ambiguous"):
            self.run_install(networks={"a": {"Aliases": ["postgres"]}, "b": {}})

    def test_postgres_candidates_match_canonical_resolver(self):
        sys.path.insert(0, str(ROOT / "ops/lib"))
        self.addCleanup(sys.path.remove, str(ROOT / "ops/lib"))
        import resolve_app_container

        self.assertEqual(installer.POSTGRES_CANDIDATES, resolve_app_container.POSTGRES_CANDIDATES)

    def test_runtime_rejects_mutable_image_before_docker_calls(self):
        with patch.object(installer, "command") as command:
            with self.assertRaises(ValueError):
                installer.install(REPOSITORY + ":latest", "template")
            command.assert_not_called()


class ReleaseStateTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.source = release_state.REMOTE.replace("/var/lib/tokenkey", str(self.root))
        for original in (
            "/usr/local/bin/tokenkey-qa-maintenance.sh",
            "/usr/local/bin/tokenkey-qa-boundary.sh",
            "/usr/local/lib/tokenkey/qa-runtime.sh",
        ):
            local = self.root / Path(original).name
            local.write_text("verified host artifact")
            self.source = self.source.replace(original, str(local))
        self.pin = {
            "Id": "pin",
            "Image": "sha256:pin",
            "State": {"Running": False},
            "Config": {
                "Labels": {
                    "dev.tokenkey.qa-runtime": "independent-v1",
                    "dev.tokenkey.release-tag": "1.8.200",
                }
            },
        }

    def run_remote(self, operation, plan=None):
        def docker(args, **kwargs):
            if args[1] == "ps":
                return "pin"
            if args[2] == "tokenkey-qa-runtime":
                return json.dumps([self.pin])
            if args[2] == "tokenkey-green":
                return json.dumps([{"Config": {"Image": REPOSITORY + ":1.8.215"}}])
            self.fail(f"unexpected docker operation {args[:3]}")

        output = io.StringIO()
        with (
            patch.object(
                sys,
                "argv",
                [
                    "-",
                    operation,
                    base64.b64encode(json.dumps(plan or {}).encode()).decode(),
                ],
            ),
            patch.dict(os.environ, TK_RELEASE_ACTIVE_CONTAINER="tokenkey-green"),
            patch("subprocess.check_output", side_effect=docker),
            contextlib.redirect_stdout(output),
        ):
            exec(self.source, {})
        return json.loads(output.getvalue())

    def test_us051_failed_acceptance_cannot_advance_receipt_or_clear_pause(self):
        self.run_remote("pause")
        pin = self.run_remote("read")["runtime"]
        plan = {
            "gateway_tag": "1.8.215",
            "maintenance_tag": "1.8.200",
            "runtime_id": "drift",
            "runtime_host_sha": pin["host_sha"],
        }
        with self.assertRaisesRegex(ValueError, "changed after acceptance"):
            self.run_remote("record", plan)
        state = self.run_remote("read")
        self.assertTrue(state["pause_drop"])
        self.assertEqual(state["verified"], {})

    def test_us051_accepted_combination_clears_pause_only_for_compatible_release(self):
        self.run_remote("pause")
        pin = self.run_remote("read")["runtime"]
        plan = {
            "gateway_tag": "1.8.215",
            "target_tag": "1.8.215",
            "maintenance_tag": "1.8.200",
            "runtime_id": pin["id"],
            "runtime_host_sha": pin["host_sha"],
            "legacy_rollback": True,
        }
        self.assertTrue(self.run_remote("record", plan)["pause_drop"])
        plan["legacy_rollback"] = False
        result = self.run_remote("record", plan)
        self.assertFalse(result["pause_drop"])
        self.assertEqual(result["verified"]["publisher_tag"], "1.8.215")

    def test_resolver_failure_is_not_masked_by_export(self):
        result = subprocess.run(
            ["bash"],
            input=release_state.remote_script(
                "read", {}, "tk_resolve_app_container() { return 42; }"
            ),
            text=True,
            capture_output=True,
        )
        self.assertEqual(result.returncode, 42)

    def test_qa_acceptance_records_tested_publisher_not_requested_worker(self):
        self.run_remote("pause", {"reason": "standalone_qa_acceptance"})
        pin = self.run_remote("read")["runtime"]
        plan = {
            "gateway_tag": "1.8.215",
            "target_tag": "1.8.228",
            "canary_tag": "1.8.215",
            "maintenance_tag": "1.8.200",
            "runtime_id": pin["id"],
            "runtime_host_sha": pin["host_sha"],
            "legacy_rollback": False,
        }
        result = self.run_remote("record", plan)
        self.assertFalse(result["pause_drop"])
        self.assertEqual(result["verified"]["publisher_tag"], "1.8.215")
        self.assertEqual(result["verified"]["target_tag"], "1.8.228")
        self.run_remote("pause")
        plan["gateway_tag"] = "1.8.214"
        with self.assertRaisesRegex(ValueError, "gateway changed"):
            self.run_remote("record", plan)
        self.assertTrue(self.run_remote("read")["pause_drop"])
        self.assertEqual(self.run_remote("read")["verified"], result["verified"])

    def test_record_takes_gateway_lock_before_resolving_active_color(self):
        script = release_state.remote_script(
            "record",
            {},
            "tk_resolve_app_container() { echo reached-resolver >&2; return 42; }",
        )
        script = script.replace("/var/lib/tokenkey", str(self.root))
        result = subprocess.run(
            ["bash", "-c", "flock() { return 1; };\n" + script],
            capture_output=True,
            text=True,
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("gateway deployment in progress", result.stderr)
        self.assertNotIn("reached-resolver", result.stderr)


class QARuntimeTransactionTest(unittest.TestCase):
    def test_us051_failed_install_restores_host_files_and_runtime_together(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = (ROOT / "deploy/aws/stage0/qa-runtime-transaction.sh").read_text()
            for prefix in (
                "/var/lib/tokenkey",
                "/usr/local/bin",
                "/usr/local/lib/tokenkey",
                "/etc/systemd/system",
            ):
                local = root / prefix.removeprefix("/")
                local.mkdir(parents=True, exist_ok=True)
                source = source.replace(prefix, str(local))
            script = root / "usr/local/bin/tokenkey-qa-maintenance.sh"
            script.write_text("old runner")
            pointer = root / "current"
            pointer.write_text("old-id")
            harness = f"""set -euo pipefail
{source}
POINTER={shlex.quote(str(pointer))}
sudo() {{
  if [[ "$1" = systemctl ]]; then return 0; fi
  if [[ "$1" != docker ]]; then "$@"; return; fi
  case "$2" in
    ps) cat "$POINTER" ;;
    rm) : > "$POINTER" ;;
    rename) printf %s "$3" > "$POINTER" ;;
    *) return 42 ;;
  esac
}}
qa_runtime_backup
printf 'new runner' > {shlex.quote(str(script))}
printf new-id > "$POINTER"
qa_runtime_rollback
qa_runtime_cleanup
"""
            result = subprocess.run(
                ["bash"], input=harness, text=True, capture_output=True
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(script.read_text(), "old runner")
            self.assertEqual(pointer.read_text(), "old-id")
            self.assertEqual(list((root / "var/lib/tokenkey").iterdir()), [])


if __name__ == "__main__":
    unittest.main()
