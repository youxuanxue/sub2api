#!/usr/bin/env python3
"""Behavior tests for the compile-once Go unit test runner."""

from __future__ import annotations

import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import textwrap
import unittest
from unittest import mock


SCRIPT = Path(__file__).resolve().parent / "unit_test_runner.py"
DISCOVERY_HELPER = Path(__file__).resolve().parent / "list_go_tests.go"
RUNNER_SPEC = importlib.util.spec_from_file_location("ci_unit_test_runner", SCRIPT)
assert RUNNER_SPEC and RUNNER_SPEC.loader
unit_test_runner = importlib.util.module_from_spec(RUNNER_SPEC)
sys.modules[RUNNER_SPEC.name] = unit_test_runner
RUNNER_SPEC.loader.exec_module(unit_test_runner)


class UnitTestRunnerTest(unittest.TestCase):
    def test_compiles_service_once_and_runs_all_entries_in_stable_binary_shards(self) -> None:
        test_names = [f"TestCase{i:02d}_{'x' * 24}" for i in range(22)] + [
            "ExampleService",
            "FuzzServiceInput",
            "TestCommentSeparated",
        ]
        with self._fake_go(test_names) as fixture:
            first = self._run(fixture, "--min-shards", "2", "--max-regex-bytes", "180")
            first_events = self._events(fixture.events)

            fixture.events.write_text("", encoding="utf-8")
            second = self._run(fixture, "--min-shards", "2", "--max-regex-bytes", "180")
            second_events = self._events(fixture.events)

        self.assertEqual(first.returncode, 0, first.stderr)
        self.assertEqual(second.returncode, 0, second.stderr)
        self.assertEqual(self._binary_patterns(first_events), self._binary_patterns(second_events))

        go_calls = self._go_calls(first_events)
        self.assertEqual(
            [call for call in go_calls if call and call[0] == "list"],
            [["list", "-json", "-tags=unit", "./..."]],
        )
        self.assertEqual(
            [call for call in go_calls if "./internal/other" in call],
            [["test", "-tags=unit", "./internal/other"]],
        )
        compile_calls = [call for call in go_calls if "-c" in call]
        self.assertEqual(len(compile_calls), 1, go_calls)
        self.assertIn("./internal/service", compile_calls[0])
        self.assertFalse(
            [
                call
                for call in go_calls
                if "./internal/service" in call and "-c" not in call
            ],
            go_calls,
        )

        registry_events = [
            event
            for event in self._binary_events(first_events)
            if "-test.list" in event["args"]
        ]
        self.assertEqual(len(registry_events), 1)
        self.assertEqual(
            Path(registry_events[0]["cwd"]).resolve(),
            (fixture.root / "internal" / "service").resolve(),
        )

        binary_events = self._binary_run_events(first_events)
        self.assertGreater(len(binary_events), 2)
        matched: list[str] = []
        for event in binary_events:
            args = event["args"]
            pattern = args[args.index("-test.run") + 1]
            self.assertNotIn("TestMain", pattern)
            self.assertNotIn("TestPhantom", pattern)
            self.assertLessEqual(len(pattern.encode("utf-8")), 180)
            self.assertIn("-test.timeout=10m", args)
            self.assertIn("-test.paniconexit0", args)
            self.assertEqual(
                Path(event["cwd"]).resolve(),
                (fixture.root / "internal" / "service").resolve(),
            )
            matched.extend(name for name in test_names if re.fullmatch(pattern, name))
        self.assertCountEqual(matched, test_names)
        self.assertEqual(len(matched), len(set(matched)))

    def test_test_main_falls_back_to_one_native_go_test(self) -> None:
        with self._fake_go(["TestOne"], has_test_main=True) as fixture:
            result = self._run(fixture)
            events = self._events(fixture.events)

        self.assertEqual(result.returncode, 0, result.stderr)
        go_calls = self._go_calls(events)
        self.assertIn(["test", "-tags=unit", "./..."], go_calls)
        self.assertFalse([call for call in go_calls if "-c" in call])
        self.assertFalse(self._binary_events(events))

    def test_fails_closed_when_ast_discovery_differs_from_binary_registry(self) -> None:
        with self._fake_go(
            ["TestVisible"],
            binary_test_names=["TestVisible", "TestHidden"],
        ) as fixture:
            result = self._run(fixture)
            events = self._events(fixture.events)

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("service test registry mismatch", result.stderr)
        self.assertIn("missing from AST discovery: TestHidden", result.stderr)
        self.assertFalse(self._binary_run_events(events))

    def test_fails_closed_when_binary_registry_omits_discovered_test(self) -> None:
        with self._fake_go(["TestMissing"], binary_test_names=[]) as fixture:
            result = self._run(fixture)
            events = self._events(fixture.events)

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("service test registry mismatch", result.stderr)
        self.assertIn("missing from binary registry: TestMissing", result.stderr)
        self.assertFalse(self._binary_run_events(events))

    def test_go_ast_discovery_matches_testing_registration_rules(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            test_file = Path(temporary) / "fixture_test.go"
            test_file.write_text(
                textwrap.dedent(
                    '''\
                    package fixture

                    import "testing"

                    type fixture struct{}
                    var phantom = `
                    func TestPhantom(t *testing.T) {}
                    `

                    func /* comment */ TestCommentSeparated(t *testing.T) {}
                    func FuzzInput(f *testing.F) {}
                    func (fixture) TestMethod(t *testing.T) {}
                    func ExampleRegistered() {
                        // Output: ok
                    }
                    func ExampleWithoutOutput() {}
                    func TestMain(m *testing.M) {}
                    '''
                ),
                encoding="utf-8",
            )
            result = subprocess.run(
                ["go", "run", str(DISCOVERY_HELPER), "--", str(test_file)],
                check=False,
                capture_output=True,
                text=True,
            )

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(
            json.loads(result.stdout),
            {
                "entries": [
                    "ExampleRegistered",
                    "FuzzInput",
                    "TestCommentSeparated",
                ],
                "has_test_main": True,
            },
        )

    def test_compile_failure_stops_before_any_service_shard(self) -> None:
        with self._fake_go(["TestOne", "TestTwo"], fail_compile=True) as fixture:
            result = self._run(fixture, "--min-shards", "2")
            events = self._events(fixture.events)

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("intentional compile failure", result.stderr)
        self.assertEqual(len([call for call in self._go_calls(events) if "-c" in call]), 1)
        self.assertFalse(self._binary_events(events))

    def test_partial_start_failure_terminates_started_processes(self) -> None:
        class FakeProcess:
            def __init__(self) -> None:
                self.terminated = False
                self.waited = False

            def poll(self) -> int | None:
                return -15 if self.terminated else None

            def terminate(self) -> None:
                self.terminated = True

            def wait(self, timeout: float | None = None) -> int:
                self.waited = True
                return -15

        process = FakeProcess()
        commands = [
            unit_test_runner.Command("started", ("first",), Path.cwd()),
            unit_test_runner.Command("spawn-fails", ("second",), Path.cwd()),
        ]
        with mock.patch.object(
            unit_test_runner.subprocess,
            "Popen",
            side_effect=[process, OSError("intentional spawn failure")],
        ):
            with self.assertRaisesRegex(OSError, "intentional spawn failure"):
                unit_test_runner.run_commands(commands)

        self.assertTrue(process.terminated)
        self.assertTrue(process.waited)

    def test_failed_shard_fails_the_run_and_expands_only_its_log(self) -> None:
        with self._fake_go(
            ["TestPass", "TestFail"],
            fail_test="TestFail",
        ) as fixture:
            result = self._run(fixture, "--min-shards", "2")

        combined = result.stdout + result.stderr
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("intentional TestFail failure", combined)
        self.assertNotIn("successful shard noise", combined)
        self.assertIn("unit-test-runner: FAIL", combined)

    def test_fails_closed_when_service_test_discovery_is_empty(self) -> None:
        with self._fake_go([]) as fixture:
            result = self._run(fixture)

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("no service unit tests discovered", result.stderr)

    def test_reports_each_shard_duration_instead_of_the_slowest_duration(self) -> None:
        with self._fake_go(
            ["TestFast2", "TestSlow"],
            slow_test="TestFast2",
        ) as fixture:
            result = self._run(fixture, "--min-shards", "2")

        self.assertEqual(result.returncode, 0, result.stderr)
        durations = [
            float(match)
            for match in re.findall(r"service-shard-\d+ \(([0-9.]+)s\)", result.stdout)
        ]
        self.assertEqual(len(durations), 2, result.stdout)
        self.assertLess(min(durations), 0.5)
        self.assertGreaterEqual(max(durations), 0.7)

    def _run(self, fixture: "FakeGoFixture", *args: str) -> subprocess.CompletedProcess[str]:
        env = os.environ.copy()
        env["PATH"] = f"{fixture.bin_dir}{os.pathsep}{env['PATH']}"
        env["FAKE_GO_EVENTS"] = str(fixture.events)
        env["FAKE_GO_ROOT"] = str(fixture.root)
        env["FAKE_GO_TESTS"] = json.dumps(fixture.test_names)
        env["FAKE_GO_BINARY_TESTS"] = json.dumps(fixture.binary_test_names)
        if fixture.has_test_main:
            env["FAKE_GO_HAS_TEST_MAIN"] = "1"
        if fixture.fail_compile:
            env["FAKE_GO_FAIL_COMPILE"] = "1"
        if fixture.fail_test:
            env["FAKE_GO_FAIL_TEST"] = fixture.fail_test
        if fixture.slow_test:
            env["FAKE_GO_SLOW_TEST"] = fixture.slow_test
        return subprocess.run(
            ["python3", str(SCRIPT), "--root", str(fixture.root), *args],
            check=False,
            capture_output=True,
            text=True,
            env=env,
        )

    @staticmethod
    def _events(path: Path) -> list[dict[str, object]]:
        return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines()]

    @staticmethod
    def _go_calls(events: list[dict[str, object]]) -> list[list[str]]:
        return [event["args"] for event in events if event["kind"] == "go"]

    @staticmethod
    def _binary_events(events: list[dict[str, object]]) -> list[dict[str, object]]:
        return [event for event in events if event["kind"] == "binary"]

    @classmethod
    def _binary_run_events(cls, events: list[dict[str, object]]) -> list[dict[str, object]]:
        return [
            event
            for event in cls._binary_events(events)
            if "-test.run" in event["args"]
        ]

    @classmethod
    def _binary_patterns(cls, events: list[dict[str, object]]) -> list[str]:
        patterns = []
        for event in cls._binary_run_events(events):
            args = event["args"]
            patterns.append(args[args.index("-test.run") + 1])
        return sorted(patterns)

    def _fake_go(
        self,
        test_names: list[str],
        *,
        has_test_main: bool = False,
        binary_test_names: list[str] | None = None,
        fail_compile: bool = False,
        fail_test: str | None = None,
        slow_test: str | None = None,
    ) -> "FakeGoFixtureContext":
        return FakeGoFixtureContext(
            test_names,
            has_test_main,
            binary_test_names,
            fail_compile,
            fail_test,
            slow_test,
        )


class FakeGoFixture:
    def __init__(
        self,
        temporary: tempfile.TemporaryDirectory[str],
        test_names: list[str],
        has_test_main: bool,
        binary_test_names: list[str] | None,
        fail_compile: bool,
        fail_test: str | None,
        slow_test: str | None,
    ) -> None:
        self._temporary = temporary
        self.root = Path(temporary.name)
        self.bin_dir = self.root / "bin"
        self.bin_dir.mkdir()
        self.events = self.root / "events.jsonl"
        self.events.write_text("", encoding="utf-8")
        self.test_names = test_names
        self.binary_test_names = test_names if binary_test_names is None else binary_test_names
        self.has_test_main = has_test_main
        self.fail_compile = fail_compile
        self.fail_test = fail_test
        self.slow_test = slow_test
        service_dir = self.root / "internal" / "service"
        service_dir.mkdir(parents=True)
        declarations = []
        for name in test_names:
            if name == "TestCommentSeparated":
                declarations.append(
                    "func /* comment */ TestCommentSeparated(t *testing.T) {}"
                )
            else:
                declarations.append(f"func {name}(t *testing.T) {{}}")
        (service_dir / "service_test.go").write_text(
            "package service\n\n"
            'import "testing"\n\n'
            "var phantom = `\n"
            "func TestPhantom(t *testing.T) {}\n"
            "`\n\n"
            + "\n".join(declarations)
            + "\n",
            encoding="utf-8",
        )


class FakeGoFixtureContext:
    def __init__(
        self,
        test_names: list[str],
        has_test_main: bool,
        binary_test_names: list[str] | None,
        fail_compile: bool,
        fail_test: str | None,
        slow_test: str | None,
    ) -> None:
        self.test_names = test_names
        self.has_test_main = has_test_main
        self.binary_test_names = binary_test_names
        self.fail_compile = fail_compile
        self.fail_test = fail_test
        self.slow_test = slow_test
        self.temporary: tempfile.TemporaryDirectory[str] | None = None

    def __enter__(self) -> FakeGoFixture:
        self.temporary = tempfile.TemporaryDirectory()
        fixture = FakeGoFixture(
            self.temporary,
            self.test_names,
            self.has_test_main,
            self.binary_test_names,
            self.fail_compile,
            self.fail_test,
            self.slow_test,
        )
        fake_binary_source = textwrap.dedent(
            """\
            #!/usr/bin/env python3
            import json
            import os
            from pathlib import Path
            import sys
            import time

            args = sys.argv[1:]
            with Path(os.environ["FAKE_GO_EVENTS"]).open("a", encoding="utf-8") as output:
                output.write(json.dumps({"kind": "binary", "args": args, "cwd": os.getcwd()}) + "\\n")
            if "-test.list" in args:
                for name in json.loads(os.environ["FAKE_GO_BINARY_TESTS"]):
                    print(name)
                raise SystemExit(0)
            pattern = args[args.index("-test.run") + 1]
            fail_test = os.environ.get("FAKE_GO_FAIL_TEST", "")
            slow_test = os.environ.get("FAKE_GO_SLOW_TEST", "")
            if slow_test and slow_test in pattern:
                time.sleep(0.75)
            if fail_test and fail_test in pattern:
                print(f"intentional {fail_test} failure")
                raise SystemExit(1)
            print("successful shard noise")
            print("PASS")
            """
        )
        fake_go_source = textwrap.dedent(
            """\
                #!/usr/bin/env python3
                import json
                import os
                from pathlib import Path
                import sys

                args = sys.argv[1:]
                events = Path(os.environ["FAKE_GO_EVENTS"])
                with events.open("a", encoding="utf-8") as output:
                    output.write(json.dumps({"kind": "go", "args": args}) + "\\n")

                if args and args[0] == "run":
                    print(json.dumps({
                        "entries": json.loads(os.environ["FAKE_GO_TESTS"]),
                        "has_test_main": os.environ.get("FAKE_GO_HAS_TEST_MAIN") == "1",
                    }))
                    raise SystemExit(0)

                if args == ["list", "-json", "-tags=unit", "./..."]:
                    root = Path(os.environ["FAKE_GO_ROOT"])
                    print(json.dumps({
                        "Dir": str(root / "internal" / "service"),
                        "ImportPath": "example.com/fixture/internal/service",
                        "TestGoFiles": ["service_test.go"],
                    }))
                    print(json.dumps({
                        "Dir": str(root / "internal" / "other"),
                        "ImportPath": "example.com/fixture/internal/other",
                    }))
                    raise SystemExit(0)

                if "-c" in args:
                    if os.environ.get("FAKE_GO_FAIL_COMPILE") == "1":
                        print("intentional compile failure", file=sys.stderr)
                        raise SystemExit(1)
                    binary = Path(args[args.index("-o") + 1])
                    binary.write_text(__BINARY_SOURCE__, encoding="utf-8")
                    binary.chmod(0o755)
                    raise SystemExit(0)

                print("ok  fake/package  0.001s")
                """
        ).replace("__BINARY_SOURCE__", repr(fake_binary_source))
        fake_go = fixture.bin_dir / "go"
        fake_go.write_text(fake_go_source, encoding="utf-8")
        fake_go.chmod(0o755)
        return fixture

    def __exit__(self, exc_type: object, exc: object, traceback: object) -> None:
        assert self.temporary is not None
        self.temporary.cleanup()


if __name__ == "__main__":
    unittest.main()
