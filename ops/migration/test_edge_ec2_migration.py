#!/usr/bin/env python3
from __future__ import annotations

import json
import pathlib
import subprocess
import sys
import tempfile
import threading
import unittest

from ops.migration.edge_ec2_migration import (
    Binding,
    MigrationError,
    MigrationOrchestrator,
    SSMRemoteRunner,
    build_parser,
    dns_confirmation_token,
)


class FakeClock:
    def __init__(self, value: float = 1_800_000_000.0) -> None:
        self.value = value

    def now(self) -> float:
        return self.value

    def advance(self, seconds: float) -> None:
        self.value += seconds


class FakeRunner:
    def __init__(
        self,
        clock: FakeClock,
        *,
        durations: dict[str, float] | None = None,
        fail_on: str = "",
        interrupt_on: str = "",
    ) -> None:
        self.clock = clock
        self.durations = durations or {}
        self.fail_on = fail_on
        self.interrupt_on = interrupt_on
        self.calls: list[dict] = []
        self.preflights: list[tuple[str, bool]] = []

    def preflight(self, command: str, *, require_reverse: bool = False) -> None:
        self.preflights.append((command, require_reverse))

    def run(self, endpoint: str, action: str, **kwargs: object) -> dict:
        self.calls.append({"endpoint": endpoint, "action": action, **kwargs})
        self.clock.advance(self.durations.get(action, 0.0))
        if action == self.interrupt_on:
            raise KeyboardInterrupt(f"injected interruption: {action}")
        if action == self.fail_on:
            raise MigrationError(f"injected failure: {action}")
        if action in {"prepare-source", "freeze-source", "freeze-target"}:
            return {"ok": True, "bundle_sha256": "b" * 64, "manifest_digest": "a" * 64}
        return {"ok": True}


def binding(**overrides: object) -> Binding:
    values: dict[str, object] = {
        "edge_id": "us4",
        "source_region": "us-west-2",
        "source_instance_id": "mi-source",
        "target_region": "us-west-2",
        "target_instance_id": "i-0123456789abcdef0",
        "target_eip": "203.0.113.8",
        "source_public_ip": "35.81.204.18",
        "domain": "api-us4.tokenkey.dev",
        "commit": "36c1e52bab6ba4f124bf1ce8c3bd0b9fbee06341",
        "manifest_digest": "a" * 64,
    }
    values.update(overrides)
    return Binding(**values)  # type: ignore[arg-type]


class EdgeEC2MigrationContractTest(unittest.TestCase):
    def test_python_entrypoint_runs_from_outside_repo(self) -> None:
        script = pathlib.Path(__file__).with_name("edge_ec2_migration.py")
        result = subprocess.run(
            [sys.executable, str(script), "--help"],
            cwd="/tmp",
            capture_output=True,
            text=True,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("prepare", result.stdout)

    def test_only_four_state_commands_are_public(self) -> None:
        parser = build_parser()
        action = next(item for item in parser._actions if item.dest == "command")
        self.assertEqual(
            set(action.choices),
            {"prepare", "cutover", "observe", "mark-stable", "rollback"},
        )

    def test_unsupported_state_version_fails_before_remote_call(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            state_path = pathlib.Path(raw) / "state.json"
            state_path.write_text(
                json.dumps({"state_version": 2, "state": "prepared"}),
                encoding="utf-8",
            )
            clock = FakeClock()
            runner = FakeRunner(clock)
            orchestrator = MigrationOrchestrator(state_path, runner=runner, now=clock.now, sleep=clock.advance)

            with self.assertRaisesRegex(MigrationError, "state file"):
                orchestrator.run("prepare", binding(), execute=True)

            self.assertEqual(runner.calls, [])

    def test_plan_only_does_not_call_remote_or_write_state(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            state_path = pathlib.Path(raw) / "state.json"
            clock = FakeClock()
            runner = FakeRunner(clock)
            orchestrator = MigrationOrchestrator(state_path, runner=runner, now=clock.now, sleep=clock.advance)

            result = orchestrator.run("prepare", binding(), execute=False)

            self.assertEqual(result["mode"], "plan")
            self.assertEqual([step["action"] for step in result["steps"]], [
                "prepare-source", "restore-target",
            ])
            self.assertEqual(runner.calls, [])
            self.assertFalse(state_path.exists())

    def test_mark_stable_plan_discloses_the_final_remote_health_checks(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            orchestrator = MigrationOrchestrator(
                pathlib.Path(raw) / "state.json",
                runner=FakeRunner(FakeClock()),
            )

            result = orchestrator.run("mark-stable", binding(), execute=False)

            self.assertEqual(result["steps"], [
                {"local": "require-completed-continuous-observation"},
                {"endpoint": "target", "action": "verify-target"},
                {"endpoint": "target", "action": "verify-source-proxy"},
            ])

    def test_same_edge_execute_is_serialized_across_controllers(self) -> None:
        class BlockingRunner(FakeRunner):
            def __init__(self, clock: FakeClock) -> None:
                super().__init__(clock)
                self.started = threading.Event()
                self.release = threading.Event()

            def run(self, endpoint: str, action: str, **kwargs: object) -> dict:
                if action == "prepare-source":
                    self.started.set()
                    if not self.release.wait(timeout=5):
                        raise RuntimeError("test controller did not release")
                return super().run(endpoint, action, **kwargs)

        with tempfile.TemporaryDirectory() as raw:
            state_path = pathlib.Path(raw) / "state.json"
            first_runner = BlockingRunner(FakeClock())
            second_runner = FakeRunner(FakeClock())
            first = MigrationOrchestrator(state_path, runner=first_runner)
            second = MigrationOrchestrator(state_path, runner=second_runner)
            first_errors: list[BaseException] = []

            def execute_first() -> None:
                try:
                    first.run("prepare", binding(), execute=True)
                except BaseException as exc:
                    first_errors.append(exc)

            worker = threading.Thread(target=execute_first)
            worker.start()
            self.assertTrue(first_runner.started.wait(timeout=2))
            try:
                with self.assertRaisesRegex(MigrationError, "already running"):
                    second.run("prepare", binding(), execute=True)
                self.assertEqual(second_runner.calls, [])
            finally:
                first_runner.release.set()
                worker.join(timeout=5)

            self.assertFalse(worker.is_alive())
            self.assertEqual(first_errors, [])

    def test_prepare_writes_bound_checkpoint_and_repeated_run_is_noop(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            state_path = pathlib.Path(raw) / "state.json"
            clock = FakeClock()
            runner = FakeRunner(clock, durations={"prepare-source": 20, "restore-target": 30})
            orchestrator = MigrationOrchestrator(state_path, runner=runner, now=clock.now, sleep=clock.advance)

            result = orchestrator.run("prepare", binding(), execute=True)
            repeated = orchestrator.run("prepare", binding(), execute=True)

            self.assertEqual(result["state"], "prepared")
            self.assertEqual(repeated["mode"], "noop")
            self.assertEqual(len(runner.calls), 2)
            stored = json.loads(state_path.read_text(encoding="utf-8"))
            self.assertEqual(stored["binding"]["edge_id"], "us4")
            self.assertEqual(stored["binding"]["source_instance_id"], "mi-source")
            self.assertEqual(stored["binding"]["target_instance_id"], "i-0123456789abcdef0")
            self.assertEqual(stored["binding"]["commit"], binding().commit)
            self.assertEqual(stored["binding"]["manifest_digest"], "a" * 64)
            self.assertEqual(stored["rehearsal_seconds"], 50)

    def test_prepare_requires_retained_rollback_proxy_to_be_released_first(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            b = binding(source_public_ip="35.81.204.18")
            state_path = pathlib.Path(raw) / "state.json"
            clock = FakeClock()
            runner = FakeRunner(clock)
            orchestrator = MigrationOrchestrator(state_path, runner=runner, now=clock.now, sleep=clock.advance)
            orchestrator.run("prepare", b, execute=True)
            orchestrator.run("cutover", b, execute=True)
            orchestrator.run("rollback", b, execute=True)
            orchestrator.run(
                "rollback",
                b,
                execute=True,
                confirm_dns=dns_confirmation_token(b, rollback=True),
                observed_dns_ip=b.source_public_ip,
            )
            runner.calls.clear()

            with self.assertRaisesRegex(MigrationError, "retained rollback proxy"):
                orchestrator.run("prepare", b, execute=True)
            self.assertEqual(runner.calls, [])

            released = orchestrator.run("rollback", b, execute=True)
            self.assertEqual(released["state"], "prepared")
            self.assertEqual(
                [call["action"] for call in runner.calls],
                ["release-target-candidate", "resume-source"],
            )
            runner.calls.clear()
            repeated = orchestrator.run("prepare", b, execute=True)

            self.assertEqual(repeated["mode"], "executed")
            self.assertEqual(
                [call["action"] for call in runner.calls],
                ["prepare-source", "restore-target"],
            )
            stored = json.loads(state_path.read_text(encoding="utf-8"))
            self.assertEqual(stored["state"], "prepared")
            self.assertNotIn("last_rollback", stored["checkpoints"])

    def test_prepare_auto_binds_source_manifest_digest(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            state_path = pathlib.Path(raw) / "state.json"
            clock = FakeClock()
            runner = FakeRunner(clock)
            orchestrator = MigrationOrchestrator(state_path, runner=runner, now=clock.now, sleep=clock.advance)

            result = orchestrator.run("prepare", binding(manifest_digest="auto"), execute=True)

            self.assertEqual(result["manifest_digest"], "a" * 64)
            stored = json.loads(state_path.read_text(encoding="utf-8"))
            self.assertEqual(stored["binding"]["manifest_digest"], "a" * 64)

    def test_prepare_rejects_source_manifest_mismatch(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            state_path = pathlib.Path(raw) / "state.json"
            clock = FakeClock()
            runner = FakeRunner(clock)
            orchestrator = MigrationOrchestrator(state_path, runner=runner, now=clock.now, sleep=clock.advance)

            with self.assertRaisesRegex(MigrationError, "manifest digest mismatch"):
                orchestrator.run("prepare", binding(manifest_digest="f" * 64), execute=True)

            self.assertFalse(state_path.exists())

    def test_binding_drift_is_rejected_before_remote_call(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            state_path = pathlib.Path(raw) / "state.json"
            clock = FakeClock()
            runner = FakeRunner(clock)
            orchestrator = MigrationOrchestrator(state_path, runner=runner, now=clock.now, sleep=clock.advance)
            orchestrator.run("prepare", binding(), execute=True)
            call_count = len(runner.calls)

            with self.assertRaisesRegex(MigrationError, "binding mismatch"):
                orchestrator.run("cutover", binding(target_eip="203.0.113.9"), execute=True)

            self.assertEqual(len(runner.calls), call_count)

    def test_binding_rejects_invalid_migration_ips(self) -> None:
        with self.assertRaisesRegex(MigrationError, "target_eip"):
            binding(target_eip="203.0.113.8; touch /tmp/injected").validate()
        with self.assertRaisesRegex(MigrationError, "source_public_ip"):
            binding(source_public_ip="35.81.204.18$(touch /tmp/injected)").validate()
        with self.assertRaisesRegex(MigrationError, "IPv4"):
            binding(target_eip="2001:db8::8").validate()
        with self.assertRaisesRegex(MigrationError, "IPv4"):
            binding(source_public_ip="2001:db8::18").validate()

    def test_illegal_transition_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            clock = FakeClock()
            runner = FakeRunner(clock)
            orchestrator = MigrationOrchestrator(
                pathlib.Path(raw) / "state.json",
                runner=runner,
                now=clock.now,
                sleep=clock.advance,
            )
            with self.assertRaisesRegex(MigrationError, "requires state cutting_over"):
                orchestrator.run(
                    "observe",
                    binding(),
                    execute=True,
                    confirm_dns=dns_confirmation_token(binding()),
                    observed_dns_ip=binding().target_eip,
                )


class EdgeEC2MigrationCutoverTest(unittest.TestCase):
    def _prepared(
        self,
        raw: str,
        *,
        durations: dict[str, float] | None = None,
        fail_on: str = "",
        binding_value: Binding | None = None,
    ) -> tuple[MigrationOrchestrator, FakeRunner, FakeClock, pathlib.Path]:
        state_path = pathlib.Path(raw) / "state.json"
        clock = FakeClock()
        runner = FakeRunner(clock, durations=durations)
        orchestrator = MigrationOrchestrator(state_path, runner=runner, now=clock.now, sleep=clock.advance)
        orchestrator.run("prepare", binding_value or binding(), execute=True)
        runner.calls.clear()
        runner.fail_on = fail_on
        return orchestrator, runner, clock, state_path

    def test_cutover_stops_before_enable_when_deadline_headroom_is_too_small(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            orchestrator, runner, _, state_path = self._prepared(
                raw,
                durations={"freeze-source": 50, "restore-target": 56},
            )

            with self.assertRaisesRegex(MigrationError, "deadline"):
                orchestrator.run("cutover", binding(), execute=True)

            actions = [call["action"] for call in runner.calls]
            self.assertEqual(
                actions,
                ["freeze-source", "restore-target", "release-target-candidate", "resume-source"],
            )
            self.assertNotIn("enable-target", actions)
            self.assertEqual(json.loads(state_path.read_text(encoding="utf-8"))["state"], "prepared")

    def test_cutover_preflights_reverse_transport_before_source_freeze(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            orchestrator, runner, _, _ = self._prepared(raw)

            orchestrator.run("cutover", binding(), execute=True)

            self.assertEqual(runner.preflights[-1], ("cutover", True))
            self.assertEqual(runner.calls[0]["action"], "freeze-source")

    def test_cutover_requires_old_public_ip_before_any_remote_write(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            missing_source_ip = binding(source_public_ip="")
            orchestrator, runner, _, _ = self._prepared(
                raw,
                binding_value=missing_source_ip,
            )

            with self.assertRaisesRegex(MigrationError, "source_public_ip"):
                orchestrator.run("cutover", missing_source_ip, execute=True)

            self.assertEqual(runner.calls, [])

    def test_prewrite_rollback_plan_matches_the_candidate_release_sequence(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            orchestrator, _, _, _ = self._prepared(raw)

            result = orchestrator.run("rollback", binding(), execute=False)

            self.assertEqual(
                [step["action"] for step in result["steps"]],
                ["release-target-candidate", "resume-source"],
            )

    def test_each_cutover_remote_call_is_bounded_by_remaining_deadline(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            orchestrator, runner, _, _ = self._prepared(
                raw,
                durations={"freeze-source": 20, "restore-target": 30, "enable-target": 5, "proxy-source": 5},
            )

            result = orchestrator.run("cutover", binding(), execute=True)

            bounded = [call for call in runner.calls if call["action"] in {"freeze-source", "restore-target", "enable-target", "proxy-source"}]
            self.assertEqual([call["timeout_seconds"] for call in bounded], [120, 100, 70, 65])
            self.assertEqual(result["state"], "cutting_over")
            self.assertEqual(result["final_manifest_digest"], "a" * 64)
            self.assertEqual(result["dns_change"]["value"], binding().target_eip)
            self.assertEqual(result["dns_change"]["confirmation"], dns_confirmation_token(binding()))

    def test_failure_before_first_write_resumes_lightsail_directly(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            orchestrator, runner, _, state_path = self._prepared(raw, fail_on="restore-target")

            with self.assertRaisesRegex(MigrationError, "restore-target"):
                orchestrator.run("cutover", binding(), execute=True)

            actions = [call["action"] for call in runner.calls]
            self.assertEqual(
                actions,
                ["freeze-source", "restore-target", "release-target-candidate", "resume-source"],
            )
            self.assertNotIn("freeze-target", actions)
            stored = json.loads(state_path.read_text(encoding="utf-8"))
            self.assertEqual(stored["state"], "prepared")
            self.assertIsNone(stored["target_accepts_writes_at"])

    def test_failure_after_first_write_reverse_syncs_before_source_resume(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            orchestrator, runner, _, state_path = self._prepared(raw, fail_on="proxy-source")

            with self.assertRaisesRegex(MigrationError, "proxy-source"):
                orchestrator.run("cutover", binding(), execute=True)

            actions = [call["action"] for call in runner.calls]
            self.assertEqual(actions, [
                "freeze-source", "restore-target", "enable-target", "proxy-source",
                "freeze-target", "restore-source", "resume-source", "release-target-candidate",
            ])
            stored = json.loads(state_path.read_text(encoding="utf-8"))
            self.assertEqual(stored["state"], "prepared")
            self.assertIsNone(stored["target_accepts_writes_at"])
            self.assertEqual(stored["checkpoints"]["last_rollback"]["path"], "post_write_reverse_sync")

    def test_interrupted_automatic_reverse_rollback_never_restores_twice(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            b = binding(source_public_ip="35.81.204.18")
            state_path = pathlib.Path(raw) / "state.json"
            clock = FakeClock()
            runner = FakeRunner(clock, fail_on="proxy-source")
            orchestrator = MigrationOrchestrator(state_path, runner=runner, now=clock.now, sleep=clock.advance)
            orchestrator.run("prepare", b, execute=True)
            runner.calls.clear()
            runner.interrupt_on = "resume-source"

            with self.assertRaises(KeyboardInterrupt):
                orchestrator.run("cutover", b, execute=True)

            interrupted = json.loads(state_path.read_text(encoding="utf-8"))
            self.assertEqual(interrupted["state"], "cutting_over")
            self.assertIsNotNone(interrupted["target_accepts_writes_at"])
            self.assertIn("rollback_source_restored", interrupted["checkpoints"])
            runner.fail_on = ""
            runner.interrupt_on = ""
            runner.calls.clear()
            pending = orchestrator.run("rollback", b, execute=True)
            self.assertEqual(pending["state"], "cutting_over")
            self.assertEqual(
                [call["action"] for call in runner.calls],
                ["resume-source", "proxy-target"],
            )
            self.assertNotIn("restore-source", [call["action"] for call in runner.calls])

    def test_cutover_checkpoint_binds_final_manifest_digest(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            orchestrator, _, _, state_path = self._prepared(raw)
            orchestrator.run("cutover", binding(), execute=True)
            stored = json.loads(state_path.read_text(encoding="utf-8"))
            source_frozen = stored["checkpoints"]["source_frozen"]
            self.assertEqual(source_frozen["manifest_digest"], "a" * 64)

    def test_interrupted_cutover_requires_explicit_rollback_before_retry(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            orchestrator, runner, _, state_path = self._prepared(raw)
            stored = json.loads(state_path.read_text(encoding="utf-8"))
            stored["state"] = "cutting_over"
            stored["checkpoints"]["cutover_started"] = {"at": "2027-01-15T08:00:00Z"}
            state_path.write_text(json.dumps(stored), encoding="utf-8")

            with self.assertRaisesRegex(MigrationError, "interrupted.*explicit rollback"):
                orchestrator.run("cutover", binding(), execute=True)

            self.assertEqual(runner.calls, [])
            result = orchestrator.run("rollback", binding(), execute=True)
            self.assertEqual(result, {"mode": "executed", "state": "prepared"})
            self.assertEqual(
                [call["action"] for call in runner.calls],
                ["release-target-candidate", "resume-source"],
            )
            recovered = json.loads(state_path.read_text(encoding="utf-8"))
            self.assertEqual(recovered["state"], "prepared")
            self.assertIsNone(recovered["target_accepts_writes_at"])

    def test_new_cutover_discards_checkpoints_from_a_previous_rolled_back_attempt(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            b = binding(source_public_ip="35.81.204.18")
            state_path = pathlib.Path(raw) / "state.json"
            clock = FakeClock()
            runner = FakeRunner(clock)
            orchestrator = MigrationOrchestrator(state_path, runner=runner, now=clock.now, sleep=clock.advance)
            orchestrator.run("prepare", b, execute=True)
            orchestrator.run("cutover", b, execute=True)
            orchestrator.run("rollback", b, execute=True)
            orchestrator.run(
                "rollback",
                b,
                execute=True,
                confirm_dns=dns_confirmation_token(b, rollback=True),
                observed_dns_ip=b.source_public_ip,
            )
            runner.interrupt_on = "freeze-source"

            with self.assertRaises(KeyboardInterrupt):
                orchestrator.run("cutover", b, execute=True)

            stored = json.loads(state_path.read_text(encoding="utf-8"))
            self.assertEqual(stored["state"], "cutting_over")
            self.assertIn("prepare_complete", stored["checkpoints"])
            self.assertIn("cutover_started", stored["checkpoints"])
            self.assertNotIn("source_proxy_ready", stored["checkpoints"])
            self.assertNotIn("rollback_source_ready", stored["checkpoints"])

    def test_proxy_cannot_complete_after_cutover_deadline(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            orchestrator, runner, _, state_path = self._prepared(
                raw,
                durations={"freeze-source": 20, "restore-target": 30, "enable-target": 5, "proxy-source": 66},
            )
            with self.assertRaisesRegex(MigrationError, "deadline"):
                orchestrator.run("cutover", binding(), execute=True)
            actions = [call["action"] for call in runner.calls]
            self.assertEqual(actions[-4:], [
                "freeze-target", "restore-source", "resume-source", "release-target-candidate",
            ])
            self.assertEqual(json.loads(state_path.read_text(encoding="utf-8"))["state"], "prepared")

    def test_observe_requires_exact_dns_confirmation_and_observed_eip(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            orchestrator, _, _, state_path = self._prepared(raw)
            orchestrator.run("cutover", binding(), execute=True)

            with self.assertRaisesRegex(MigrationError, "confirmation"):
                orchestrator.run("observe", binding(), execute=True, confirm_dns="wrong", observed_dns_ip=binding().target_eip)
            with self.assertRaisesRegex(MigrationError, "does not resolve"):
                orchestrator.run(
                    "observe", binding(), execute=True,
                    confirm_dns=dns_confirmation_token(binding()), observed_dns_ip="203.0.113.9",
                )

            result = orchestrator.run(
                "observe", binding(), execute=True,
                confirm_dns=dns_confirmation_token(binding()), observed_dns_ip=binding().target_eip,
            )
            self.assertEqual(result["state"], "observing")
            self.assertEqual(json.loads(state_path.read_text(encoding="utf-8"))["state"], "observing")

    def test_mark_stable_enforces_ten_minute_observation(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            b = binding(source_public_ip="35.81.204.18")
            state_path = pathlib.Path(raw) / "state.json"
            clock = FakeClock()
            runner = FakeRunner(
                clock,
                durations={"verify-target": 4, "verify-source-proxy": 4},
            )
            orchestrator = MigrationOrchestrator(
                state_path,
                runner=runner,
                now=clock.now,
                sleep=clock.advance,
            )
            orchestrator.run("prepare", b, execute=True)
            runner.calls.clear()
            orchestrator.run("cutover", b, execute=True)
            started = clock.now()

            result = orchestrator.run(
                "observe", b, execute=True,
                confirm_dns=dns_confirmation_token(b), observed_dns_ip=b.target_eip,
            )

            self.assertEqual(result["state"], "observing")
            self.assertGreaterEqual(clock.now() - started, 600)
            probe_actions = [
                call["action"]
                for call in runner.calls
                if call["action"] in {"verify-target", "verify-source-proxy"}
            ]
            self.assertEqual(probe_actions.count("verify-target"), 21)
            self.assertEqual(
                probe_actions,
                ["verify-target", "verify-source-proxy"]
                * (len(probe_actions) // 2),
            )
            persisted = json.loads(state_path.read_text(encoding="utf-8"))
            self.assertGreaterEqual(
                persisted["checkpoints"]["observation_complete"]["duration_seconds"],
                600,
            )
            calls_before_stable = len(runner.calls)
            stable = orchestrator.run("mark-stable", b, execute=True)
            self.assertEqual(stable["state"], "stable")
            self.assertEqual(
                [call["action"] for call in runner.calls[calls_before_stable:]],
                ["verify-target", "verify-source-proxy"],
            )

    def test_failed_observation_restarts_the_full_window(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            b = binding(source_public_ip="35.81.204.18")
            state_path = pathlib.Path(raw) / "state.json"
            clock = FakeClock()
            runner = FakeRunner(clock)
            orchestrator = MigrationOrchestrator(
                state_path,
                runner=runner,
                now=clock.now,
                sleep=clock.advance,
            )
            orchestrator.run("prepare", b, execute=True)
            orchestrator.run("cutover", b, execute=True)
            runner.fail_on = "verify-source-proxy"

            with self.assertRaisesRegex(MigrationError, "verify-source-proxy"):
                orchestrator.run(
                    "observe", b, execute=True,
                    confirm_dns=dns_confirmation_token(b),
                    observed_dns_ip=b.target_eip,
                )

            failed = json.loads(state_path.read_text(encoding="utf-8"))
            self.assertEqual(failed["state"], "observing")
            self.assertNotIn("observation_complete", failed["checkpoints"])
            failed_at = clock.now()
            with self.assertRaisesRegex(MigrationError, "continuous observation"):
                orchestrator.run("mark-stable", b, execute=True)

            runner.fail_on = ""
            orchestrator.run(
                "observe", b, execute=True,
                confirm_dns=dns_confirmation_token(b),
                observed_dns_ip=b.target_eip,
            )
            self.assertEqual(clock.now() - failed_at, 600)

    def test_post_write_manual_rollback_waits_for_source_dns_confirmation(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            b = binding(source_public_ip="35.81.204.18")
            state_path = pathlib.Path(raw) / "state.json"
            clock = FakeClock()
            runner = FakeRunner(clock)
            orchestrator = MigrationOrchestrator(state_path, runner=runner, now=clock.now, sleep=clock.advance)
            orchestrator.run("prepare", b, execute=True)
            orchestrator.run("cutover", b, execute=True)
            orchestrator.run(
                "observe", b, execute=True,
                confirm_dns=dns_confirmation_token(b), observed_dns_ip=b.target_eip,
            )
            runner.calls.clear()

            pending = orchestrator.run("rollback", b, execute=True)
            self.assertEqual(pending["state"], "cutting_over")
            self.assertEqual(pending["dns_change"]["value"], b.source_public_ip)
            self.assertEqual([call["action"] for call in runner.calls], [
                "freeze-target", "restore-source", "resume-source", "proxy-target",
            ])
            stored = json.loads(state_path.read_text(encoding="utf-8"))
            self.assertIsNotNone(stored["target_accepts_writes_at"])
            with self.assertRaisesRegex(MigrationError, "confirmation"):
                orchestrator.run("rollback", b, execute=True, confirm_dns="wrong", observed_dns_ip=b.source_public_ip)

            completed = orchestrator.run(
                "rollback", b, execute=True,
                confirm_dns=dns_confirmation_token(b, rollback=True),
                observed_dns_ip=b.source_public_ip,
            )
            self.assertEqual(completed["state"], "prepared")
            self.assertEqual(runner.calls[-1]["action"], "proxy-target")

    def test_post_write_rollback_retry_never_restores_over_resumed_source(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            b = binding(source_public_ip="35.81.204.18")
            state_path = pathlib.Path(raw) / "state.json"
            clock = FakeClock()
            runner = FakeRunner(clock)
            orchestrator = MigrationOrchestrator(state_path, runner=runner, now=clock.now, sleep=clock.advance)
            orchestrator.run("prepare", b, execute=True)
            orchestrator.run("cutover", b, execute=True)
            runner.calls.clear()
            runner.fail_on = "resume-source"

            with self.assertRaisesRegex(MigrationError, "resume-source"):
                orchestrator.run("rollback", b, execute=True)

            interrupted = json.loads(state_path.read_text(encoding="utf-8"))
            self.assertIn("rollback_source_restored", interrupted["checkpoints"])
            runner.fail_on = ""
            runner.calls.clear()
            pending = orchestrator.run("rollback", b, execute=True)
            self.assertEqual(pending["state"], "cutting_over")
            self.assertEqual(
                [call["action"] for call in runner.calls],
                ["resume-source", "proxy-target"],
            )

    def test_dns_rollback_confirmation_does_not_require_expired_transfer_urls(self) -> None:
        with tempfile.TemporaryDirectory() as raw:
            b = binding(source_public_ip="35.81.204.18")
            state_path = pathlib.Path(raw) / "state.json"
            clock = FakeClock()
            runner = FakeRunner(clock)
            orchestrator = MigrationOrchestrator(state_path, runner=runner, now=clock.now, sleep=clock.advance)
            orchestrator.run("prepare", b, execute=True)
            orchestrator.run("cutover", b, execute=True)
            orchestrator.run("rollback", b, execute=True)
            runner.preflights.clear()

            orchestrator.run(
                "rollback",
                b,
                execute=True,
                confirm_dns=dns_confirmation_token(b, rollback=True),
                observed_dns_ip=b.source_public_ip,
            )

            self.assertEqual(runner.preflights, [])


class SSMRemoteRunnerTest(unittest.TestCase):
    def test_ssm_payload_shell_quotes_presigned_urls(self) -> None:
        calls: list[list[str]] = []

        def run(args: list[str], **_: object) -> subprocess.CompletedProcess[str]:
            calls.append(args)
            payload = (
                {"Command": {"CommandId": "cmd-quoted"}}
                if "send-command" in args
                else {
                    "CommandId": "cmd-quoted",
                    "InstanceId": "mi-source",
                    "Status": "Success",
                    "ResponseCode": 0,
                    "StandardOutputContent": "FREEZE_SOURCE_OK bundle_sha256=" + "c" * 64 + " manifest_digest=" + "a" * 64,
                    "StandardErrorContent": "",
                }
            )
            return subprocess.CompletedProcess(args, 0, stdout=json.dumps(payload), stderr="")

        helper_url = "https://helper.invalid/get?name=one'$(touch /tmp/helper-injected)"
        transfer_url = "https://bundle.invalid/put?name=two'$(touch /tmp/bundle-injected)"
        runner = SSMRemoteRunner(
            binding(),
            helper_get_url=helper_url,
            helper_sha256="d" * 64,
            forward_put_url=transfer_url,
            forward_get_url="https://bundle.invalid/get",
            run=run,
            sleep=lambda _: None,
        )

        runner.run("source", "freeze-source")

        send = calls[0]
        parameters = json.loads(send[send.index("--parameters") + 1])
        self.assertEqual(parameters["commands"][0], "export TK_HELPER_URL='https://helper.invalid/get?name=one'\"'\"'$(touch /tmp/helper-injected)'")
        self.assertEqual(parameters["commands"][1], "export TK_MIGRATION_TRANSFER_URL='https://bundle.invalid/put?name=two'\"'\"'$(touch /tmp/bundle-injected)'")

    def test_ssm_payload_serializes_remote_actions_per_edge_host(self) -> None:
        calls: list[list[str]] = []

        def run(args: list[str], **_: object) -> subprocess.CompletedProcess[str]:
            calls.append(args)
            payload = (
                {"Command": {"CommandId": "cmd-locked"}}
                if "send-command" in args
                else {
                    "CommandId": "cmd-locked",
                    "InstanceId": "mi-source",
                    "Status": "Success",
                    "ResponseCode": 0,
                    "StandardOutputContent": "RESUME_SOURCE_OK",
                    "StandardErrorContent": "",
                }
            )
            return subprocess.CompletedProcess(args, 0, stdout=json.dumps(payload), stderr="")

        runner = SSMRemoteRunner(
            binding(),
            helper_get_url="https://helper.invalid/get",
            helper_sha256="d" * 64,
            forward_put_url="https://bundle.invalid/put",
            forward_get_url="https://bundle.invalid/get",
            run=run,
            sleep=lambda _: None,
        )

        runner.run("source", "resume-source")

        send = calls[0]
        parameters = json.loads(send[send.index("--parameters") + 1])
        remote = parameters["commands"][2]
        self.assertIn("exec 9>/var/lib/tokenkey/migration/.action.lock", remote)
        self.assertIn("flock -n 9", remote)
        self.assertLess(remote.index("flock -n 9"), remote.index("curl -fsS"))

    def test_prepare_restore_marks_the_remote_action_as_rehearsal(self) -> None:
        calls: list[list[str]] = []

        def run(args: list[str], **_: object) -> subprocess.CompletedProcess[str]:
            calls.append(args)
            payload = (
                {"Command": {"CommandId": "cmd-rehearsal"}}
                if "send-command" in args
                else {
                    "CommandId": "cmd-rehearsal",
                    "InstanceId": "i-0123456789abcdef0",
                    "Status": "Success",
                    "ResponseCode": 0,
                    "StandardOutputContent": "RESTORE_TARGET_OK",
                    "StandardErrorContent": "",
                }
            )
            return subprocess.CompletedProcess(args, 0, stdout=json.dumps(payload), stderr="")

        runner = SSMRemoteRunner(
            binding(),
            helper_get_url="https://helper.invalid/get",
            helper_sha256="d" * 64,
            forward_put_url="https://bundle.invalid/put",
            forward_get_url="https://bundle.invalid/get",
            run=run,
            sleep=lambda _: None,
        )

        runner.run("target", "restore-target", bundle_sha256="a" * 64, rehearsal=True)

        send = calls[0]
        parameters = json.loads(send[send.index("--parameters") + 1])
        self.assertIn("--rehearsal", parameters["commands"][2])

    def test_execute_rejects_missing_transfer_url_before_aws(self) -> None:
        calls: list[list[str]] = []

        def run(args: list[str], **_: object) -> subprocess.CompletedProcess[str]:
            calls.append(args)
            raise AssertionError("AWS must not be called")

        runner = SSMRemoteRunner(
            binding(),
            helper_get_url="https://helper.invalid/get",
            helper_sha256="d" * 64,
            forward_put_url="",
            forward_get_url="https://bundle.invalid/get",
            run=run,
        )
        with self.assertRaisesRegex(MigrationError, "transfer URL"):
            runner.run("source", "freeze-source")
        self.assertEqual(calls, [])

    def test_cutover_transport_preflight_requires_reverse_urls_before_source_freeze(self) -> None:
        runner = SSMRemoteRunner(
            binding(),
            helper_get_url="https://helper.invalid/get",
            helper_sha256="d" * 64,
            forward_put_url="https://bundle.invalid/forward-put",
            forward_get_url="https://bundle.invalid/forward-get",
        )

        with self.assertRaisesRegex(MigrationError, "reverse"):
            runner.preflight("cutover", require_reverse=True)

    def test_source_action_targets_exact_instance_and_polls_same_command(self) -> None:
        calls: list[list[str]] = []

        def run(args: list[str], **_: object) -> subprocess.CompletedProcess[str]:
            calls.append(args)
            if "send-command" in args:
                stdout = json.dumps({"Command": {"CommandId": "cmd-123"}})
            else:
                stdout = json.dumps({
                    "CommandId": "cmd-123",
                    "InstanceId": "mi-source",
                    "Status": "Success",
                    "ResponseCode": 0,
                    "StandardOutputContent": "FREEZE_SOURCE_OK bundle_sha256=" + "c" * 64 + " manifest_digest=" + "a" * 64,
                    "StandardErrorContent": "",
                })
            return subprocess.CompletedProcess(args, 0, stdout=stdout, stderr="")

        runner = SSMRemoteRunner(
            binding(),
            helper_get_url="https://helper.invalid/get?secret=one",
            helper_sha256="d" * 64,
            forward_put_url="https://bundle.invalid/put?secret=two",
            forward_get_url="https://bundle.invalid/get?secret=three",
            run=run,
            sleep=lambda _: None,
        )
        result = runner.run("source", "freeze-source", timeout_seconds=120)

        self.assertEqual(result["bundle_sha256"], "c" * 64)
        self.assertEqual(result["manifest_digest"], "a" * 64)
        send = calls[0]
        self.assertEqual(send[send.index("--region") + 1], "us-west-2")
        self.assertEqual(send[send.index("--instance-ids") + 1], "mi-source")
        self.assertEqual(send[send.index("--timeout-seconds") + 1], "120")
        parameters = json.loads(send[send.index("--parameters") + 1])
        self.assertEqual(parameters["executionTimeout"], ["120"])
        poll = calls[1]
        self.assertEqual(poll[poll.index("--command-id") + 1], "cmd-123")
        self.assertEqual(poll[poll.index("--instance-id") + 1], "mi-source")

    def test_short_execution_deadline_keeps_aws_delivery_timeout_valid(self) -> None:
        calls: list[list[str]] = []

        def run(args: list[str], **_: object) -> subprocess.CompletedProcess[str]:
            calls.append(args)
            payload = (
                {"Command": {"CommandId": "cmd-short"}}
                if "send-command" in args
                else {
                    "CommandId": "cmd-short",
                    "InstanceId": "i-0123456789abcdef0",
                    "Status": "Success",
                    "ResponseCode": 0,
                    "StandardOutputContent": "ENABLE_TARGET_OK",
                    "StandardErrorContent": "",
                }
            )
            return subprocess.CompletedProcess(args, 0, stdout=json.dumps(payload), stderr="")

        runner = SSMRemoteRunner(
            binding(),
            helper_get_url="https://helper.invalid/get",
            helper_sha256="d" * 64,
            forward_put_url="https://bundle.invalid/put",
            forward_get_url="https://bundle.invalid/get",
            run=run,
            sleep=lambda _: None,
            epoch=lambda: 1000,
        )

        runner.run("target", "enable-target", timeout_seconds=12)

        send = calls[0]
        self.assertEqual(send[send.index("--timeout-seconds") + 1], "30")
        parameters = json.loads(send[send.index("--parameters") + 1])
        self.assertEqual(parameters["executionTimeout"], ["12"])
        remote = parameters["commands"][2]
        self.assertIn("ACTION_DEADLINE_EPOCH=1012", remote)
        self.assertIn("migration action delivery exceeded deadline", remote)
        self.assertLess(
            remote.index("migration action delivery exceeded deadline"),
            remote.index("python3 /tmp/edge_ec2_remote.py"),
        )

    def test_transient_missing_ssm_invocation_is_retried(self) -> None:
        polls = 0

        def run(args: list[str], **_: object) -> subprocess.CompletedProcess[str]:
            nonlocal polls
            if "send-command" in args:
                return subprocess.CompletedProcess(
                    args, 0, stdout=json.dumps({"Command": {"CommandId": "cmd-late"}}), stderr=""
                )
            polls += 1
            if polls == 1:
                raise subprocess.CalledProcessError(254, args, stderr="InvocationDoesNotExist")
            return subprocess.CompletedProcess(
                args,
                0,
                stdout=json.dumps({
                    "CommandId": "cmd-late",
                    "InstanceId": "mi-source",
                    "Status": "Success",
                    "ResponseCode": 0,
                    "StandardOutputContent": "FREEZE_SOURCE_OK bundle_sha256=" + "c" * 64 + " manifest_digest=" + "a" * 64,
                    "StandardErrorContent": "",
                }),
                stderr="",
            )

        runner = SSMRemoteRunner(
            binding(),
            helper_get_url="https://helper.invalid/get",
            helper_sha256="d" * 64,
            forward_put_url="https://bundle.invalid/put",
            forward_get_url="https://bundle.invalid/get",
            run=run,
            sleep=lambda _: None,
        )

        result = runner.run("source", "freeze-source", timeout_seconds=120)

        self.assertEqual(result["bundle_sha256"], "c" * 64)
        self.assertEqual(polls, 2)

    def test_remote_failure_redacts_presigned_urls(self) -> None:
        secret = "https://bundle.invalid/put?X-Amz-Signature=top-secret"
        responses = iter([
            {"Command": {"CommandId": "cmd-456"}},
            {
                "CommandId": "cmd-456",
                "InstanceId": "mi-source",
                "Status": "Failed",
                "ResponseCode": 1,
                "StandardOutputContent": "",
                "StandardErrorContent": f"curl failed for {secret}",
            },
        ])

        def run(args: list[str], **_: object) -> subprocess.CompletedProcess[str]:
            return subprocess.CompletedProcess(args, 0, stdout=json.dumps(next(responses)), stderr="")

        runner = SSMRemoteRunner(
            binding(),
            helper_get_url="https://helper.invalid/get?signature=helper-secret",
            helper_sha256="d" * 64,
            forward_put_url=secret,
            forward_get_url="https://bundle.invalid/get?signature=get-secret",
            run=run,
            sleep=lambda _: None,
        )
        with self.assertRaises(MigrationError) as raised:
            runner.run("source", "freeze-source")
        self.assertNotIn("top-secret", str(raised.exception))
        self.assertIn("[REDACTED_URL]", str(raised.exception))


if __name__ == "__main__":
    unittest.main()
