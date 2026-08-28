#!/usr/bin/env python3
"""Run Go unit tests with a shared compile graph and sharded service package."""

from __future__ import annotations

import argparse
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
# Keep the established service fan-out while allowing four other packages to
# make progress without launching every test binary at once on hosted runners.
DEFAULT_MAX_PARALLEL = DEFAULT_MIN_SHARDS + 4
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
) -> tuple[list[str], list[str], list[str], bool]:
    output = _run_checked(["go", "list", "-json", "-tags=unit", "./..."], root)
    service_dir = (root / service_package.removeprefix("./")).resolve()
    packages: list[str] = []
    test_packages: list[str] = []
    service_test_files: list[Path] | None = None
    for package in _decode_go_list(output):
        package_dir = Path(str(package["Dir"])).resolve()
        try:
            relative = package_dir.relative_to(root)
        except ValueError as exc:
            raise RunnerError(f"package outside backend root: {package_dir}") from exc
        package_path = f"./{relative.as_posix()}"
        packages.append(package_path)
        filenames = [
            *package.get("TestGoFiles", []),
            *package.get("XTestGoFiles", []),
        ]
        if filenames:
            test_packages.append(package_path)
        if package_dir == service_dir:
            service_test_files = [package_dir / str(name) for name in filenames]
    if service_test_files is None:
        raise RunnerError(f"service package not found: {service_package}")
    tests, has_test_main = _test_entries(root, service_test_files)
    if not tests and not has_test_main:
        raise RunnerError("no service unit tests discovered")
    return (
        sorted(set(packages)),
        sorted(set(test_packages)),
        tests,
        has_test_main,
    )


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
) -> tuple[list[str], list[str], list[str], list[str], bool]:
    packages, test_packages, service_tests, has_test_main = discover_test_plan(
        root,
        service_package,
    )
    if has_test_main:
        return packages, test_packages, service_tests, [], True
    patterns = shard_service_tests(
        service_tests,
        min_shards,
        max_regex_bytes,
    )
    return packages, test_packages, service_tests, patterns, False


def _shared_compile_layout(
    build_root: Path,
    packages: list[str],
    test_packages: list[str],
) -> tuple[list[list[str]], dict[str, Path]]:
    """Lay out the minimum compile batches without test-binary collisions."""

    all_packages = sorted(set(packages))
    runnable_packages = sorted(set(test_packages))
    all_package_set = set(all_packages)
    runnable_package_set = set(runnable_packages)
    unknown = sorted(runnable_package_set - all_package_set)
    if unknown:
        raise RunnerError(
            "test packages missing from unit package discovery: " + ", ".join(unknown)
        )

    by_binary_name: dict[str, list[str]] = {}
    for package in all_packages:
        binary_name = f"{Path(package).name}.test"
        by_binary_name.setdefault(binary_name, []).append(package)

    batch_count = max((len(group) for group in by_binary_name.values()), default=1)
    batches: list[list[str]] = [[] for _ in range(batch_count)]
    binaries: dict[str, Path] = {}
    for binary_name, owners in sorted(by_binary_name.items()):
        for batch_index, package in enumerate(sorted(owners)):
            batch_dir = build_root / f"batch-{batch_index}"
            batches[batch_index].append(package)
            if package in runnable_package_set:
                binaries[package] = batch_dir / binary_name
    for batch_index, batch in enumerate(batches):
        (build_root / f"batch-{batch_index}").mkdir(parents=True, exist_ok=True)
        batch.sort()
    return batches, binaries


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
    binaries: dict[str, Path],
    test_packages: list[str],
    patterns: list[str],
    has_test_main: bool,
) -> list[Command]:
    commands: list[Command] = []
    service_binary = binaries[service_package]
    service_dir = root / service_package.removeprefix("./")
    if has_test_main:
        commands.append(
            Command(
                "service-all",
                (
                    str(service_binary),
                    "-test.timeout=10m",
                    "-test.paniconexit0",
                ),
                service_dir,
            )
        )
    else:
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

    for package in test_packages:
        if package == service_package:
            continue
        package_label = package.removeprefix("./").replace("/", "-")
        commands.append(
            Command(
                f"other-{package_label}",
                (
                    str(binaries[package]),
                    "-test.timeout=10m",
                    "-test.paniconexit0",
                ),
                root / package.removeprefix("./"),
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
    max_parallel: int | None = None,
) -> int:
    if max_parallel is not None and max_parallel < 1:
        raise RunnerError("max parallel commands must be positive")
    with tempfile.TemporaryDirectory(prefix=temporary_prefix) as temporary:
        log_dir = Path(temporary)
        initial = list(initial_running or [])
        if max_parallel is not None and len(initial) > max_parallel:
            raise RunnerError("initial commands exceed max parallel commands")
        running: list[tuple[int, RunningCommand]] = [
            (index - len(initial), item) for index, item in enumerate(initial)
        ]
        pending = list(enumerate(commands))
        handles = []
        results: list[tuple[int, Command, int, Path, float]] = []
        parallelism = max_parallel or max(1, len(running) + len(pending))
        try:
            while pending or running:
                while pending and len(running) < parallelism:
                    index, command = pending.pop(0)
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
                    running.append(
                        (
                            index,
                            (command, process, log_path, time.monotonic()),
                        )
                    )

                completed: list[tuple[int, RunningCommand, int]] = []
                while not completed:
                    for index, item in running:
                        returncode = item[1].poll()
                        if returncode is not None:
                            completed.append((index, item, returncode))
                    if not completed:
                        time.sleep(0.01)
                completed_indexes = {index for index, _, _ in completed}
                running = [
                    (index, item)
                    for index, item in running
                    if index not in completed_indexes
                ]
                for index, item, returncode in completed:
                    command, _, log_path, started_at = item
                    results.append(
                        (
                            index,
                            command,
                            returncode,
                            log_path,
                            time.monotonic() - started_at,
                        )
                    )
            failed = any(returncode != 0 for _, _, returncode, _, _ in results)
        except BaseException:
            _terminate_processes([item for _, item in running])
            raise
        finally:
            for handle in handles:
                handle.close()

        for _, command, returncode, log_path, elapsed in sorted(results):
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
    discovery_started_at = time.monotonic()
    packages, test_packages, service_tests, patterns, has_test_main = build_test_plan(
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

    with tempfile.TemporaryDirectory(prefix="sub2api-unit-build-") as temporary:
        build_root = Path(temporary)
        compile_batches, binaries = _shared_compile_layout(
            build_root,
            packages,
            test_packages,
        )
        compile_started_at = time.monotonic()
        for batch_index, batch in enumerate(compile_batches):
            batch_started_at = time.monotonic()
            _run_checked(
                [
                    "go",
                    "test",
                    "-c",
                    "-tags=unit",
                    "-o",
                    str(build_root / f"batch-{batch_index}"),
                    *batch,
                ],
                root,
            )
            print(
                f"unit-test-runner: DETAIL shared-compile-batch-{batch_index} "
                f"packages={len(batch)} ({time.monotonic() - batch_started_at:.1f}s)",
                flush=True,
            )
        print(
            f"unit-test-runner: STAGE shared-compile "
            f"({time.monotonic() - compile_started_at:.1f}s)",
            flush=True,
        )

        service_binary = binaries[service_package]
        service_dir = root / service_package.removeprefix("./")
        registry_started_at = time.monotonic()
        if not has_test_main:
            verify_binary_registry(service_binary, service_dir, service_tests)
        print(
            f"unit-test-runner: STAGE registry-check "
            f"({time.monotonic() - registry_started_at:.1f}s)",
            flush=True,
        )
        commands = build_commands(
            root,
            service_package,
            binaries,
            test_packages,
            patterns,
            has_test_main,
        )
        execution_started_at = time.monotonic()
        returncode = run_commands(commands, max_parallel=DEFAULT_MAX_PARALLEL)
        print(
            f"unit-test-runner: STAGE test-execution "
            f"({time.monotonic() - execution_started_at:.1f}s)",
            flush=True,
        )
        return returncode


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
