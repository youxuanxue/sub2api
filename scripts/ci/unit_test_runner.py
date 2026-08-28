#!/usr/bin/env python3
"""Run Go unit tests while sharding the large internal/service package."""

from __future__ import annotations

import argparse
from concurrent.futures import ThreadPoolExecutor
from dataclasses import dataclass
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import tempfile
import time


DEFAULT_SERVICE_PACKAGE = "./internal/service"
DEFAULT_MIN_SHARDS = 8
TEST_DISCOVERY_HELPER = Path(__file__).resolve().with_name("list_go_tests.go")
# Linux limits a single argv entry to 128 KiB. Keep 32 KiB of headroom while
# avoiding unnecessary shard/link fan-out at the current service test scale.
DEFAULT_MAX_REGEX_BYTES = 96 * 1024
GO_TEST_RESULT_RE = re.compile(
    r"^\s*--- (?:PASS|FAIL|SKIP): (?P<name>\S+) \((?P<seconds>[0-9.]+)s\)\s*$"
)
INTEGRATION_STAGE_RE = re.compile(
    r"^(?:\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2} )?"
    r"(?P<detail>[a-z0-9-]+-integration: STAGE .+)$"
)


class RunnerError(RuntimeError):
    """Raised when discovery cannot produce a safe test plan."""


@dataclass(frozen=True)
class Command:
    label: str
    argv: tuple[str, ...]
    cwd: Path


RunningCommand = tuple[Command, subprocess.Popen[bytes], Path, float]


def _run_checked(
    argv: list[str],
    root: Path,
    environment: dict[str, str] | None = None,
) -> str:
    run_environment = None
    if environment:
        run_environment = os.environ.copy()
        run_environment.update(environment)
    result = subprocess.run(
        argv,
        cwd=root,
        check=False,
        capture_output=True,
        text=True,
        env=run_environment,
    )
    if result.returncode != 0:
        detail = result.stderr.strip() or result.stdout.strip() or "command failed"
        raise RunnerError(f"{' '.join(argv)}: {detail}")
    return result.stdout


def _decode_go_list(output: str) -> list[dict[str, object]]:
    packages: list[dict[str, object]] = []
    decoder = json.JSONDecoder()
    offset = 0
    while offset < len(output):
        while offset < len(output) and output[offset].isspace():
            offset += 1
        if offset >= len(output):
            break
        package, offset = decoder.raw_decode(output, offset)
        packages.append(package)
    return packages


def _test_entries(root: Path, test_files: list[Path]) -> tuple[list[str], bool]:
    output = _run_checked(
        [
            "go",
            "run",
            str(TEST_DISCOVERY_HELPER),
            "--",
            *(str(test_file) for test_file in test_files),
        ],
        root,
    )
    try:
        discovery = json.loads(output)
        entries = discovery["entries"]
        has_test_main = discovery["has_test_main"]
    except (KeyError, TypeError, json.JSONDecodeError) as exc:
        raise RunnerError("invalid Go test discovery output") from exc
    if (
        not isinstance(entries, list)
        or not all(isinstance(entry, str) for entry in entries)
        or not isinstance(has_test_main, bool)
    ):
        raise RunnerError("invalid Go test discovery output")
    return sorted(set(entries)), has_test_main


def discover_test_plan(
    root: Path,
    service_package: str,
) -> tuple[list[str], list[str], bool]:
    output = _run_checked(["go", "list", "-json", "-tags=unit", "./..."], root)
    service_dir = (root / service_package.removeprefix("./")).resolve()
    packages: list[str] = []
    service_test_files: list[Path] | None = None
    for package in _decode_go_list(output):
        package_dir = Path(str(package["Dir"])).resolve()
        try:
            relative = package_dir.relative_to(root)
        except ValueError as exc:
            raise RunnerError(f"package outside backend root: {package_dir}") from exc
        if package_dir == service_dir:
            filenames = [
                *package.get("TestGoFiles", []),
                *package.get("XTestGoFiles", []),
            ]
            service_test_files = [package_dir / str(name) for name in filenames]
            continue
        packages.append(f"./{relative.as_posix()}")
    if service_test_files is None:
        raise RunnerError(f"service package not found: {service_package}")
    tests, has_test_main = _test_entries(root, service_test_files)
    if not tests and not has_test_main:
        raise RunnerError("no service unit tests discovered")
    return sorted(set(packages)), tests, has_test_main


def _test_pattern(test_names: list[str]) -> str:
    return "^(" + "|".join(re.escape(name) for name in sorted(test_names)) + ")$"


def shard_service_tests(
    test_names: list[str],
    min_shards: int,
    max_regex_bytes: int,
) -> list[str]:
    if min_shards < 1:
        raise RunnerError("min shards must be positive")
    if max_regex_bytes < 1:
        raise RunnerError("max regex bytes must be positive")

    shard_count = min(min_shards, len(test_names))
    while True:
        shards: list[list[str]] = [[] for _ in range(shard_count)]
        for test_name in test_names:
            digest = hashlib.sha256(test_name.encode("utf-8")).digest()
            shard_index = int.from_bytes(digest[:8], "big") % shard_count
            shards[shard_index].append(test_name)
        patterns = [_test_pattern(shard) for shard in shards if shard]
        if all(len(pattern.encode("utf-8")) <= max_regex_bytes for pattern in patterns):
            return patterns
        if shard_count == len(test_names):
            raise RunnerError(
                f"a single test name exceeds max regex bytes ({max_regex_bytes})"
            )
        shard_count = min(len(test_names), shard_count * 2)


def build_test_plan(
    root: Path,
    service_package: str,
    min_shards: int,
    max_regex_bytes: int,
) -> tuple[list[str], list[str], list[str], bool]:
    other_packages, service_tests, has_test_main = discover_test_plan(
        root,
        service_package,
    )
    if has_test_main:
        return other_packages, service_tests, [], True
    patterns = shard_service_tests(
        service_tests,
        min_shards,
        max_regex_bytes,
    )
    return other_packages, service_tests, patterns, False


def verify_binary_registry(
    service_binary: Path,
    service_dir: Path,
    discovered_tests: list[str],
    subject: str = "service",
    environment: dict[str, str] | None = None,
) -> None:
    output = _run_checked(
        [str(service_binary), "-test.list", "^(Test|Fuzz|Example)"],
        service_dir,
        environment,
    )
    registered_tests = sorted(
        {line.strip() for line in output.splitlines() if line.strip()}
    )
    discovered = set(discovered_tests)
    registered = set(registered_tests)
    if discovered == registered:
        return

    details = []
    missing = sorted(registered - discovered)
    unexpected = sorted(discovered - registered)
    if missing:
        details.append("missing from AST discovery: " + ", ".join(missing))
    if unexpected:
        details.append("missing from binary registry: " + ", ".join(unexpected))
    raise RunnerError(f"{subject} test registry mismatch; " + "; ".join(details))


def build_commands(
    root: Path,
    service_package: str,
    service_binary: Path,
    other_packages: list[str],
    patterns: list[str],
) -> list[Command]:
    commands: list[Command] = []
    if other_packages:
        commands.append(
            Command(
                "other-packages",
                tuple(["go", "test", "-tags=unit", *other_packages]),
                root,
            )
        )
    service_dir = root / service_package.removeprefix("./")
    width = len(str(len(patterns) - 1))
    for index, pattern in enumerate(patterns):
        commands.append(
            Command(
                f"service-shard-{index:0{width}d}",
                (
                    str(service_binary),
                    "-test.run",
                    pattern,
                    "-test.timeout=10m",
                    "-test.paniconexit0",
                ),
                service_dir,
            )
        )
    return commands


def _last_summary(output: str) -> str:
    lines = [line for line in output.splitlines() if line.strip()]
    return lines[-1] if lines else "no output"


def _slow_top_level_tests(output: str, limit: int) -> list[tuple[float, str]]:
    tests: list[tuple[float, str]] = []
    for line in output.splitlines():
        match = GO_TEST_RESULT_RE.match(line)
        if not match:
            continue
        name = match.group("name")
        if "/" in name:
            continue
        seconds = float(match.group("seconds"))
        if seconds <= 0:
            continue
        tests.append((seconds, name))
    return sorted(tests, reverse=True)[:limit]


def _integration_stage_lines(output: str) -> list[str]:
    details = []
    for line in output.splitlines():
        match = INTEGRATION_STAGE_RE.match(line)
        if match:
            details.append(match.group("detail"))
    return details


def _terminate_processes(
    running: list[RunningCommand],
) -> None:
    for _, process, _, _ in running:
        try:
            os.killpg(process.pid, signal.SIGTERM)
        except AttributeError:
            if process.poll() is None:
                process.terminate()
        except (PermissionError, ProcessLookupError):
            if process.poll() is None:
                process.terminate()
    for _, process, _, _ in running:
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except (AttributeError, PermissionError, ProcessLookupError):
                process.kill()
            process.wait()
    for _, process, _, _ in running:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except (AttributeError, PermissionError, ProcessLookupError):
            pass


def run_commands(
    commands: list[Command],
    initial_running: list[RunningCommand] | None = None,
    runner_name: str = "unit-test-runner",
    temporary_prefix: str = "sub2api-unit-",
    slow_test_limit: int = 0,
) -> int:
    with tempfile.TemporaryDirectory(prefix=temporary_prefix) as temporary:
        log_dir = Path(temporary)
        running = list(initial_running or [])
        handles = []
        try:
            for index, command in enumerate(commands):
                log_path = log_dir / f"{index:02d}-{command.label}.log"
                handle = log_path.open("wb")
                handles.append(handle)
                process = subprocess.Popen(
                    command.argv,
                    cwd=command.cwd,
                    stdout=handle,
                    stderr=subprocess.STDOUT,
                    start_new_session=True,
                )
                running.append((command, process, log_path, time.monotonic()))

            def wait_for_process(
                item: tuple[Command, subprocess.Popen[bytes], Path, float],
            ) -> tuple[Command, int, Path, float]:
                command, process, log_path, started_at = item
                returncode = process.wait()
                elapsed = time.monotonic() - started_at
                return command, returncode, log_path, elapsed

            with ThreadPoolExecutor(max_workers=len(running)) as executor:
                results = list(executor.map(wait_for_process, running))
            failed = any(returncode != 0 for _, returncode, _, _ in results)
        except BaseException:
            _terminate_processes(running)
            raise
        finally:
            for handle in handles:
                handle.close()

        for command, returncode, log_path, elapsed in results:
            output = log_path.read_text(encoding="utf-8", errors="replace")
            if returncode == 0:
                print(
                    f"{runner_name}: PASS {command.label} "
                    f"({elapsed:.1f}s): {_last_summary(output)}"
                )
                if slow_test_limit > 0:
                    for detail in _integration_stage_lines(output):
                        print(f"{runner_name}: DETAIL {command.label}: {detail}")
                    for test_elapsed, test_name in _slow_top_level_tests(
                        output,
                        slow_test_limit,
                    ):
                        print(
                            f"{runner_name}: SLOW {command.label} "
                            f"({test_elapsed:.3f}s): {test_name}"
                        )
                continue
            print(
                f"{runner_name}: FAIL {command.label} ({elapsed:.1f}s)",
                file=sys.stderr,
            )
            print(output.rstrip(), file=sys.stderr)
        return 1 if failed else 0


def run_unit_tests(
    root: Path,
    service_package: str,
    min_shards: int,
    max_regex_bytes: int,
) -> int:
    with tempfile.TemporaryDirectory(prefix="sub2api-service-test-") as temporary:
        service_binary = Path(temporary) / "service.test"
        with tempfile.TemporaryDirectory(prefix="sub2api-unit-overlap-") as overlap_temp:
            compile_command = Command(
                "service-compile",
                (
                    "go",
                    "test",
                    "-c",
                    "-tags=unit",
                    "-o",
                    str(service_binary),
                    service_package,
                ),
                root,
            )
            compile_log = Path(overlap_temp) / "service-compile.log"
            compile_handle = compile_log.open("wb")
            try:
                compile_process = subprocess.Popen(
                    compile_command.argv,
                    cwd=compile_command.cwd,
                    stdout=compile_handle,
                    stderr=subprocess.STDOUT,
                    start_new_session=True,
                )
            except BaseException:
                compile_handle.close()
                raise
            compile_running: RunningCommand = (
                compile_command,
                compile_process,
                compile_log,
                time.monotonic(),
            )
            background: list[RunningCommand] = [compile_running]
            other_handle = None
            try:
                discovery_started_at = time.monotonic()
                other_packages, service_tests, patterns, has_test_main = build_test_plan(
                    root,
                    service_package,
                    min_shards,
                    max_regex_bytes,
                )
                print(
                    f"unit-test-runner: STAGE discovery "
                    f"({time.monotonic() - discovery_started_at:.1f}s)",
                    flush=True,
                )
                if has_test_main:
                    _terminate_processes(background)
                    background.clear()
                    print("unit-test-runner: TestMain detected; using native go test")
                    return subprocess.run(
                        ["go", "test", "-tags=unit", "./..."],
                        cwd=root,
                        check=False,
                    ).returncode

                commands = build_commands(
                    root,
                    service_package,
                    service_binary,
                    other_packages,
                    patterns,
                )
                other_commands = [
                    command for command in commands if command.label == "other-packages"
                ]
                service_commands = [
                    command for command in commands if command.label != "other-packages"
                ]
                if other_commands:
                    other_command = other_commands[0]
                    other_log = Path(overlap_temp) / "other-packages.log"
                    other_handle = other_log.open("wb")
                    other_process = subprocess.Popen(
                        other_command.argv,
                        cwd=other_command.cwd,
                        stdout=other_handle,
                        stderr=subprocess.STDOUT,
                        start_new_session=True,
                    )
                    background.append(
                        (other_command, other_process, other_log, time.monotonic())
                    )

                compile_returncode = compile_process.wait()
                compile_elapsed = time.monotonic() - compile_running[3]
                if compile_returncode != 0:
                    compile_output = compile_log.read_text(
                        encoding="utf-8",
                        errors="replace",
                    )
                    detail = (
                        compile_output.strip()
                        or f"command exited with status {compile_returncode}"
                    )
                    raise RunnerError(
                        f"{' '.join(compile_command.argv)}: {detail}"
                    )
                background = background[1:]
                print(
                    f"unit-test-runner: STAGE service-compile "
                    f"({compile_elapsed:.1f}s)",
                    flush=True,
                )
                service_dir = root / service_package.removeprefix("./")
                registry_started_at = time.monotonic()
                verify_binary_registry(service_binary, service_dir, service_tests)
                print(
                    f"unit-test-runner: STAGE registry-check "
                    f"({time.monotonic() - registry_started_at:.1f}s)",
                    flush=True,
                )
                return run_commands(service_commands, background)
            except BaseException:
                _terminate_processes(background)
                raise
            finally:
                compile_handle.close()
                if other_handle is not None:
                    other_handle.close()


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=Path.cwd())
    parser.add_argument("--service-package", default=DEFAULT_SERVICE_PACKAGE)
    parser.add_argument("--min-shards", type=int, default=DEFAULT_MIN_SHARDS)
    parser.add_argument(
        "--max-regex-bytes",
        type=int,
        default=DEFAULT_MAX_REGEX_BYTES,
    )
    args = parser.parse_args(argv)
    root = args.root.resolve()
    try:
        return run_unit_tests(
            root,
            args.service_package,
            args.min_shards,
            args.max_regex_bytes,
        )
    except (OSError, RunnerError) as exc:
        print(f"unit-test-runner: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
