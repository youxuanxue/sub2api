#!/usr/bin/env python3
"""Behavior tests for the compile-once Go integration test runner."""

from __future__ import annotations

import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import textwrap
import unittest


SCRIPT = Path(__file__).resolve().parent / "integration_test_runner.py"


class IntegrationTestRunnerTest(unittest.TestCase):
    def test_compiles_repository_once_and_runs_only_integration_entries_in_three_shards(
        self,
    ) -> None:
        integration_tests = [f"TestIntegration{i}" for i in range(9)]
        with FakeGoFixture(integration_tests) as fixture:
            first = fixture.run()
            first_events = fixture.read_events()

            fixture.events.write_text("", encoding="utf-8")
            second = fixture.run()
            second_events = fixture.read_events()

        self.assertEqual(first.returncode, 0, first.stderr)
        self.assertEqual(second.returncode, 0, second.stderr)

        go_calls = [event["args"] for event in first_events if event["kind"] == "go"]
        compile_calls = [call for call in go_calls if "-c" in call]
        self.assertEqual(len(compile_calls), 1, go_calls)
        self.assertIn("./internal/repository", compile_calls[0])
        self.assertIn(
            ["test", "-tags=integration", "./internal/other"],
            go_calls,
        )

        registry_events = [
            event
            for event in first_events
            if event["kind"] == "binary" and "-test.list" in event["args"]
        ]
        self.assertEqual(len(registry_events), 1, first_events)
        self.assertEqual(registry_events[0]["registry_only"], "1")

        first_patterns = self._binary_patterns(first_events)
        second_patterns = self._binary_patterns(second_events)
        self.assertEqual(first_patterns, second_patterns)
        self.assertEqual(len(first_patterns), 3, first_patterns)

        matched: list[str] = []
        for pattern in first_patterns:
            self.assertNotIn("TestBaseline", pattern)
            self.assertNotIn("TestMain", pattern)
            matched.extend(name for name in integration_tests if re.fullmatch(pattern, name))
        self.assertCountEqual(matched, integration_tests)
        self.assertEqual(len(matched), len(set(matched)))

    def test_fails_closed_when_binary_registry_omits_an_integration_entry(self) -> None:
        with FakeGoFixture(
            ["TestVisible", "TestMissing"],
            binary_tests=["TestBaseline", "TestVisible"],
        ) as fixture:
            result = fixture.run()
            events = fixture.read_events()

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("repository test registry mismatch", result.stderr)
        self.assertIn("missing from binary registry: TestMissing", result.stderr)
        self.assertFalse(
            [
                event
                for event in events
                if event["kind"] == "binary" and "-test.run" in event["args"]
            ]
        )

    def test_failed_shard_expands_only_its_log_with_integration_label(self) -> None:
        with FakeGoFixture(
            ["TestPass", "TestFail"],
            fail_test="TestFail",
        ) as fixture:
            result = fixture.run("--shards", "2")

        combined = result.stdout + result.stderr
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("intentional TestFail failure", combined)
        self.assertNotIn("successful shard noise", combined)
        self.assertIn("integration-test-runner: FAIL", combined)

    def test_overlaps_compile_with_discovery_and_other_packages(self) -> None:
        with FakeGoFixture(
            ["TestOne", "TestTwo", "TestThree"],
            compile_delay=4.0,
            discovery_delay=0.5,
        ) as fixture:
            result = fixture.run()
            events = fixture.read_events()

        self.assertEqual(result.returncode, 0, result.stderr)
        compile_started = next(
            event["at"]
            for event in events
            if event["kind"] == "go" and "-c" in event["args"]
        )
        discovery_finished = max(
            event["at"]
            for event in events
            if event["kind"] == "go-finished"
            and event["args"]
            and event["args"][0] in {"list", "run"}
        )
        compile_finished = next(
            event["at"]
            for event in events
            if event["kind"] == "go-finished" and "-c" in event["args"]
        )
        other_started = next(
            event["at"]
            for event in events
            if event["kind"] == "go" and "./internal/other" in event["args"]
        )
        self.assertLess(compile_started, discovery_finished, events)
        self.assertLess(other_started, compile_finished, events)

    @staticmethod
    def _binary_patterns(events: list[dict[str, object]]) -> list[str]:
        patterns = []
        for event in events:
            args = event["args"]
            if event["kind"] != "binary" or "-test.run" not in args:
                continue
            patterns.append(args[args.index("-test.run") + 1])
        return sorted(patterns)


class FakeGoFixture:
    def __init__(
        self,
        integration_tests: list[str],
        *,
        binary_tests: list[str] | None = None,
        fail_test: str | None = None,
        compile_delay: float = 0.0,
        discovery_delay: float = 0.0,
    ) -> None:
        self.integration_tests = integration_tests
        self.binary_tests = (
            ["TestBaseline", *integration_tests]
            if binary_tests is None
            else binary_tests
        )
        self.fail_test = fail_test
        self.compile_delay = compile_delay
        self.discovery_delay = discovery_delay
        self.temporary: tempfile.TemporaryDirectory[str] | None = None

    def __enter__(self) -> "FakeGoFixture":
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.bin_dir = self.root / "bin"
        self.bin_dir.mkdir()
        self.events = self.root / "events.jsonl"
        self.events.write_text("", encoding="utf-8")

        repository = self.root / "internal" / "repository"
        repository.mkdir(parents=True)
        (repository / "baseline_test.go").write_text(
            'package repository\n\nimport "testing"\n\n'
            "func TestBaseline(t *testing.T) {}\n",
            encoding="utf-8",
        )
        declarations = "\n".join(
            f"func {name}(t *testing.T) {{}}" for name in self.integration_tests
        )
        (repository / "repository_integration_test.go").write_text(
            "//go:build integration\n\n"
            'package repository\n\nimport "testing"\n\n'
            "func TestMain(m *testing.M) {}\n"
            f"{declarations}\n",
            encoding="utf-8",
        )

        other = self.root / "internal" / "other"
        other.mkdir(parents=True)
        (other / "other.go").write_text("package other\n", encoding="utf-8")
        (other / "other_integration_test.go").write_text(
            "//go:build integration\n\n"
            'package other\n\nimport "testing"\n\n'
            "func TestOtherIntegration(t *testing.T) {}\n",
            encoding="utf-8",
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
                output.write(json.dumps({
                    "kind": "binary",
                    "args": args,
                    "cwd": os.getcwd(),
                    "at": time.monotonic(),
                    "registry_only": os.environ.get("SUB2API_TEST_REGISTRY_ONLY", ""),
                }) + "\\n")
            if "-test.list" in args:
                for name in json.loads(os.environ["FAKE_GO_BINARY_TESTS"]):
                    print(name)
                raise SystemExit(0)
            pattern = args[args.index("-test.run") + 1]
            fail_test = os.environ.get("FAKE_GO_FAIL_TEST", "")
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
            import time

            args = sys.argv[1:]
            events = Path(os.environ["FAKE_GO_EVENTS"])
            root = Path(os.environ["FAKE_GO_ROOT"])
            with events.open("a", encoding="utf-8") as output:
                output.write(json.dumps({"kind": "go", "args": args, "at": time.monotonic()}) + "\\n")

            repository = {
                "Dir": str(root / "internal" / "repository"),
                "ImportPath": "example.com/fixture/internal/repository",
                "TestGoFiles": ["baseline_test.go"],
            }
            other = {
                "Dir": str(root / "internal" / "other"),
                "ImportPath": "example.com/fixture/internal/other",
            }
            if "-tags=integration" in args:
                repository["TestGoFiles"] = [
                    "baseline_test.go",
                    "repository_integration_test.go",
                ]
                other["TestGoFiles"] = ["other_integration_test.go"]

            if args and args[0] == "list":
                time.sleep(float(os.environ.get("FAKE_GO_DISCOVERY_DELAY", "0")))
                print(json.dumps(repository))
                if args[-1] == "./...":
                    print(json.dumps(other))
                with events.open("a", encoding="utf-8") as output:
                    output.write(json.dumps({"kind": "go-finished", "args": args, "at": time.monotonic()}) + "\\n")
                raise SystemExit(0)

            if args and args[0] == "run":
                time.sleep(float(os.environ.get("FAKE_GO_DISCOVERY_DELAY", "0")))
                filenames = {Path(value).name for value in args if value.endswith("_test.go")}
                entries = []
                if "baseline_test.go" in filenames:
                    entries.append("TestBaseline")
                if "repository_integration_test.go" in filenames:
                    entries.extend(json.loads(os.environ["FAKE_GO_INTEGRATION_TESTS"]))
                print(json.dumps({
                    "entries": entries,
                    "has_test_main": "repository_integration_test.go" in filenames,
                }))
                with events.open("a", encoding="utf-8") as output:
                    output.write(json.dumps({"kind": "go-finished", "args": args, "at": time.monotonic()}) + "\\n")
                raise SystemExit(0)

            if "-c" in args:
                time.sleep(float(os.environ.get("FAKE_GO_COMPILE_DELAY", "0")))
                binary = Path(args[args.index("-o") + 1])
                binary.write_text(__BINARY_SOURCE__, encoding="utf-8")
                binary.chmod(0o755)
                with events.open("a", encoding="utf-8") as output:
                    output.write(json.dumps({"kind": "go-finished", "args": args, "at": time.monotonic()}) + "\\n")
                raise SystemExit(0)

            print("ok  fake/package  0.001s")
            """
        ).replace("__BINARY_SOURCE__", repr(fake_binary_source))
        fake_go = self.bin_dir / "go"
        fake_go.write_text(fake_go_source, encoding="utf-8")
        fake_go.chmod(0o755)
        return self

    def run(self, *args: str) -> subprocess.CompletedProcess[str]:
        env = os.environ.copy()
        env["PATH"] = f"{self.bin_dir}{os.pathsep}{env['PATH']}"
        env["FAKE_GO_EVENTS"] = str(self.events)
        env["FAKE_GO_ROOT"] = str(self.root)
        env["FAKE_GO_INTEGRATION_TESTS"] = json.dumps(self.integration_tests)
        env["FAKE_GO_BINARY_TESTS"] = json.dumps(self.binary_tests)
        if self.fail_test:
            env["FAKE_GO_FAIL_TEST"] = self.fail_test
        if self.compile_delay:
            env["FAKE_GO_COMPILE_DELAY"] = str(self.compile_delay)
        if self.discovery_delay:
            env["FAKE_GO_DISCOVERY_DELAY"] = str(self.discovery_delay)
        return subprocess.run(
            ["python3", str(SCRIPT), "--root", str(self.root), *args],
            check=False,
            capture_output=True,
            text=True,
            env=env,
        )

    def read_events(self) -> list[dict[str, object]]:
        return [
            json.loads(line)
            for line in self.events.read_text(encoding="utf-8").splitlines()
            if line
        ]

    def __exit__(self, exc_type: object, exc: object, traceback: object) -> None:
        assert self.temporary is not None
        self.temporary.cleanup()


if __name__ == "__main__":
    unittest.main()
