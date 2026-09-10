#!/usr/bin/env python3
"""Behavioral coverage for independent releases and split cutover/completion."""
from __future__ import annotations

import copy
import base64
import contextlib
import io
import importlib.util
import json
import os
import shlex
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(Path(__file__).resolve().parent))
import bluegreen_completion as completion
import prod_release_plan as planner
import prod_release_state as release_state

_install_spec = importlib.util.spec_from_file_location("qa_runtime_install", ROOT / "ops/stage0/qa-runtime-install.py")
installer = importlib.util.module_from_spec(_install_spec)
_install_spec.loader.exec_module(installer)


class ComponentPlanTest(unittest.TestCase):
    def setUp(self):
        self.state = {"runtime": {"tag": "1.8.200", "id": "pin", "host_sha": "host"},
                      "verified": {"schema_version": planner.SCHEMA, "gateway_tag": "1.8.214",
                                   "maintenance_tag": "1.8.200", "runtime_id": "pin", "runtime_host_sha": "host",
                                   "worker_image": planner.REPOSITORY + ":1.8.183", "publisher_tag": "1.8.214"}}

    def resolve(self, paths=(), *, state=None, worker_paths=None, maintenance_paths=None):
        def diff(repo, before, after):
            if before == "1.8.183" and worker_paths is not None:
                return worker_paths
            if before == "1.8.200" and maintenance_paths is not None:
                return maintenance_paths
            return list(paths)
        with patch.object(planner, "changed", side_effect=diff):
            return planner.plan(ROOT, "1.8.215", "1.8.214", planner.REPOSITORY + ":1.8.183",
                                self.state if state is None else state, ROOT / "ops/qa/deploy_rollout.yaml")

    def test_us051_gateway_only_never_restarts_or_runs_qa(self):
        result = self.resolve(["backend/internal/service/gateway_service.go"])
        self.assertTrue(result["deploy_gateway"])
        self.assertFalse(result["deploy_worker"])
        self.assertFalse(result["deploy_maintenance"])
        self.assertFalse(result["run_canary"])

    def test_us051_old_worker_does_not_repeat_verified_publisher_canary(self):
        result = self.resolve(["backend/internal/service/gateway_service.go"],
                              worker_paths=["backend/internal/observability/qa/service_bundle_canary.go"],
                              maintenance_paths=[])
        self.assertFalse(result["run_canary"])
        self.assertEqual(result["worker_image"], planner.REPOSITORY + ":1.8.183")

    def test_us051_worker_only_preserves_running_gateway_and_maintenance(self):
        result = self.resolve(["backend/cmd/server/qa_bundle_worker.go"])
        self.assertFalse(result["deploy_gateway"])
        self.assertEqual(result["gateway_tag"], "1.8.214")
        self.assertTrue(result["deploy_worker"])
        self.assertFalse(result["deploy_maintenance"])
        self.assertTrue(result["run_canary"])

    def test_us051_maintenance_only_preserves_gateway(self):
        result = self.resolve(["backend/cmd/server/qa_maintenance.go"])
        self.assertFalse(result["deploy_gateway"])
        self.assertFalse(result["deploy_worker"])
        self.assertTrue(result["deploy_maintenance"])

    def test_us051_shared_protocol_or_build_dependencies_coordinate(self):
        for path in ("backend/go.mod", "Dockerfile", "backend/internal/config/config.go",
                     "backend/migrations/tk_next.sql", "backend/internal/observability/qa/bundle/job.go"):
            with self.subTest(path=path):
                result = self.resolve([path])
                self.assertTrue(result["deploy_gateway"])
                self.assertTrue(result["deploy_worker"])
                self.assertTrue(result["deploy_maintenance"])
                self.assertTrue(result["run_canary"])

    def test_us051_missing_or_drifted_receipt_requires_full_qa_acceptance(self):
        for field in ("runtime_id", "runtime_host_sha", "worker_image", "gateway_tag"):
            state = copy.deepcopy(self.state)
            state["verified"][field] = "drift"
            with self.subTest(field=field):
                result = self.resolve(state=state)
                self.assertTrue(result["deploy_maintenance"])
                self.assertTrue(result["run_canary"])
        result = self.resolve(state={})
        self.assertTrue(result["deploy_maintenance"])
        self.assertTrue(result["run_canary"])

    def test_us051_legacy_rollback_keeps_qa_pins_and_pauses_drop(self):
        with tempfile.TemporaryDirectory() as directory:
            rollout = Path(directory) / "rollout.yaml"
            rollout.write_text("prod: {user_export: {bundle_runtime_contract: phase3_v1}}\n")
            result = planner.plan(ROOT, "1.8.150", "1.8.214", planner.REPOSITORY + ":1.8.183", self.state, rollout)
            self.assertTrue(result["legacy_rollback"])
            self.assertEqual(result["mode"], "legacy_rollback")
            self.assertFalse(result["deploy_worker"])
            self.assertFalse(result["deploy_maintenance"])
            with self.assertRaises(ValueError):
                planner.plan(ROOT, "1.8.150", "1.8.214", planner.REPOSITORY + ":1.8.183", {}, rollout)

    def test_nonruntime_files_do_not_trigger_components(self):
        for path in ("docs/example.md", "backend/cmd/server/VERSION", "backend/internal/service/gateway_test.go"):
            self.assertFalse(planner.runtime_path(path))

    def test_unknown_runtime_contract_is_rejected_before_planning_mutations(self):
        with tempfile.TemporaryDirectory() as directory:
            rollout = Path(directory) / "rollout.yaml"
            rollout.write_text("prod: {user_export: {bundle_runtime_contract: phase3_v1}, component_release: {runtime_contract: independent_v2}}\n")
            with self.assertRaisesRegex(ValueError, "unsupported independent"):
                planner.plan(ROOT, "1.8.215", "1.8.214", planner.REPOSITORY + ":1.8.183", self.state, rollout)


class BlueGreenCompletionTest(unittest.TestCase):
    token = "a" * 32

    def test_us051_cutover_returns_while_original_deploy_is_draining(self):
        receipt = {"token": self.token, "tag": "1.8.215", "cutover_at": "2026-09-10T02:59:01Z"}
        with patch.object(completion, "invocation", return_value={"Status": "InProgress"}) as inspect, \
             patch.object(completion.ssm_execution, "run_shell_b64", return_value=json.dumps(receipt)):
            result = completion.wait("instance", "command", self.token, "1.8.215", "cutover", 10)
        self.assertEqual(result, receipt["cutover_at"])
        self.assertEqual(inspect.call_count, 1)

    def test_us051_stale_receipt_cannot_claim_cutover(self):
        receipt = {"token": "b" * 32, "tag": "1.8.215", "cutover_at": "2026-09-10T02:59:01Z"}
        with self.assertRaises(ValueError):
            completion.validate_receipt(receipt, self.token, "1.8.215")

    def test_us051_completion_waits_and_propagates_drain_failure(self):
        with patch.object(completion, "invocation", side_effect=[{"Status": "InProgress"}, {"Status": "Failed", "ResponseCode": 1}]), \
             patch.object(completion.time, "sleep"):
            with self.assertRaises(RuntimeError):
                completion.wait("instance", "command", self.token, "1.8.215", "complete", 10)

    def test_us051_completion_requires_successful_remote_exit(self):
        with patch.object(completion, "invocation", return_value={"Status": "Success", "ResponseCode": 0}):
            self.assertIsNone(completion.wait("instance", "command", self.token, "1.8.215", "complete", 10))
        with patch.object(completion, "invocation", return_value={"Status": "Success", "ResponseCode": 1}):
            with self.assertRaises(RuntimeError):
                completion.wait("instance", "command", self.token, "1.8.215", "complete", 10)


class QARuntimeResolverTest(unittest.TestCase):
    def resolve(self, network="tokenkey-data", contract="independent-v1"):
        script = (ROOT / "deploy/aws/stage0/qa-runtime.sh").read_text()
        script += r'''
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
'''
        return subprocess.run(["bash"], input=script, text=True, capture_output=True,
                              env={**os.environ, "TEST_NETWORK": network, "TEST_CONTRACT": contract,
                                   "ACTIVE_COLOR_FILE": "/missing-gateway-color"})

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
    def run_install(self, *, fail_promotion=False, keep_previous=False):
        image = planner.REPOSITORY + ":1.8.215"
        names = {"tokenkey-qa-runtime": "old"}
        captured_env = []

        def command(*args):
            if args[:3] == ("docker", "inspect", "tokenkey-postgres"):
                return json.dumps([{"NetworkSettings": {"Networks": {"data": {}}}}])
            if args[:2] == ("docker", "compose"):
                return json.dumps({"services": {"runtime": {"image": image, "labels": {},
                                                            "environment": {"DATABASE_PASSWORD": "private", "QA_ARCHIVE_SEAL_DELAY_MINUTES": "15"}}}})
            if args[:2] == ("docker", "pull"):
                return image
            if args[:2] == ("docker", "ps"):
                return names.get("tokenkey-qa-runtime", "")
            if args[:2] == ("docker", "create"):
                names[args[3]] = "new"
                captured_env.append(Path(args[args.index("--env-file") + 1]).read_text())
                self.assertEqual(args[args.index("--network") + 1], "data")
                return "new"
            if args[:2] == ("docker", "inspect"):
                return json.dumps([{"Id": names[args[2]], "Image": "sha256:new", "Config": {"Image": image}, "State": {"Running": False}}])
            if args[:2] == ("docker", "rename"):
                if fail_promotion and names[args[2]] == "new":
                    raise RuntimeError("promotion failed")
                names[args[3]] = names.pop(args[2])
                return ""
            if args[:2] == ("docker", "rm"):
                names.pop(args[2])
                return ""
            self.fail(f"unexpected command {args[:2]}")

        with patch.object(installer, "command", side_effect=command), \
             patch.object(installer.subprocess, "run"), patch.dict(os.environ), \
             contextlib.redirect_stdout(io.StringIO()):
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

    def test_runtime_rejects_mutable_image_before_docker_calls(self):
        with patch.object(installer, "command") as command:
            with self.assertRaises(ValueError):
                installer.install(planner.REPOSITORY + ":latest", "template")
            command.assert_not_called()


class ReleaseStateTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.source = release_state.REMOTE.replace("/var/lib/tokenkey", str(self.root))
        for original in ("/usr/local/bin/tokenkey-qa-maintenance.sh", "/usr/local/bin/tokenkey-qa-boundary.sh",
                         "/usr/local/lib/tokenkey/qa-runtime.sh"):
            local = self.root / Path(original).name
            local.write_text("verified host artifact")
            self.source = self.source.replace(original, str(local))
        self.pin = {"Id": "pin", "Image": "sha256:pin", "State": {"Running": False},
                    "Config": {"Labels": {"dev.tokenkey.qa-runtime": "independent-v1", "dev.tokenkey.release-tag": "1.8.200"}}}

    def run_remote(self, operation, plan=None):
        def docker(args, **kwargs):
            if args[1] == "ps":
                return "pin"
            if args[2] == "tokenkey-qa-runtime":
                return json.dumps([self.pin])
            if args[2] == "tokenkey-green":
                return json.dumps([{"Config": {"Image": planner.REPOSITORY + ":1.8.215"}}])
            self.fail(f"unexpected docker operation {args[:3]}")
        output = io.StringIO()
        with patch.object(sys, "argv", ["-", operation, base64.b64encode(json.dumps(plan or {}).encode()).decode()]), \
             patch.dict(os.environ, TK_RELEASE_ACTIVE_CONTAINER="tokenkey-green"), \
             patch("subprocess.check_output", side_effect=docker), contextlib.redirect_stdout(output):
            exec(self.source, {})
        return json.loads(output.getvalue())

    def test_us051_failed_acceptance_cannot_advance_receipt_or_clear_pause(self):
        self.run_remote("pause")
        pin = self.run_remote("read")["runtime"]
        plan = {"gateway_tag": "1.8.215", "maintenance_tag": "1.8.200", "runtime_id": "drift", "runtime_host_sha": pin["host_sha"]}
        with self.assertRaisesRegex(ValueError, "changed after acceptance"):
            self.run_remote("record", plan)
        state = self.run_remote("read")
        self.assertTrue(state["pause_drop"])
        self.assertEqual(state["verified"], {})

    def test_us051_accepted_combination_clears_pause_only_for_compatible_release(self):
        self.run_remote("pause")
        pin = self.run_remote("read")["runtime"]
        plan = {"gateway_tag": "1.8.215", "target_tag": "1.8.215", "maintenance_tag": "1.8.200",
                "runtime_id": pin["id"], "runtime_host_sha": pin["host_sha"], "legacy_rollback": True}
        self.assertTrue(self.run_remote("record", plan)["pause_drop"])
        plan["legacy_rollback"] = False
        result = self.run_remote("record", plan)
        self.assertFalse(result["pause_drop"])
        self.assertEqual(result["verified"]["publisher_tag"], "1.8.215")

    def test_resolver_failure_is_not_masked_by_export(self):
        result = subprocess.run(["bash"], input=release_state.remote_script("read", {}, "tk_resolve_app_container() { return 42; }"),
                                text=True, capture_output=True)
        self.assertEqual(result.returncode, 42)


class QARuntimeTransactionTest(unittest.TestCase):
    def test_us051_failed_install_restores_host_files_and_runtime_together(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = (ROOT / "deploy/aws/stage0/qa-runtime-transaction.sh").read_text()
            for prefix in ("/var/lib/tokenkey", "/usr/local/bin", "/usr/local/lib/tokenkey", "/etc/systemd/system"):
                local = root / prefix.removeprefix("/")
                local.mkdir(parents=True, exist_ok=True)
                source = source.replace(prefix, str(local))
            script = root / "usr/local/bin/tokenkey-qa-maintenance.sh"
            script.write_text("old runner")
            pointer = root / "current"
            pointer.write_text("old-id")
            harness = f'''set -euo pipefail
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
'''
            result = subprocess.run(["bash"], input=harness, text=True, capture_output=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(script.read_text(), "old runner")
            self.assertEqual(pointer.read_text(), "old-id")
            self.assertEqual(list((root / "var/lib/tokenkey").iterdir()), [])


if __name__ == "__main__":
    unittest.main()
