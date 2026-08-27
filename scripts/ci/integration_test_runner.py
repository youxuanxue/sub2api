#!/usr/bin/env python3
"""Run integration tests while sharding the large repository package."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import time

from unit_test_runner import (
    Command,
    DEFAULT_MAX_REGEX_BYTES,
    RunningCommand,
    RunnerError,
    _run_checked,
    _terminate_processes,
    _test_entries,
    run_commands,
    shard_service_tests,
    verify_binary_registry,
)


DEFAULT_REPOSITORY_PACKAGE = "./internal/repository"
DEFAULT_SHARDS = 3
INTEGRATION_PACKAGES_HELPER = Path(__file__).resolve().with_name(
    "integration-packages.py"
)


def _go_list_package(root: Path, package: str, tags: str | None = None) -> dict[str, object]:
    argv = ["go", "list", "-json"]
    if tags:
        argv.append(f"-tags={tags}")
    argv.append(package)
    output = _run_checked(argv, root)
    try:
        result = json.loads(output)
    except json.JSONDecodeError as exc:
        raise RunnerError(f"invalid go list output for {package}") from exc
    if not isinstance(result, dict):
        raise RunnerError(f"invalid go list output for {package}")
    return result


def _test_files(package: dict[str, object]) -> set[str]:
    return {
        *(str(name) for name in package.get("TestGoFiles", [])),
        *(str(name) for name in package.get("XTestGoFiles", [])),
    }


def discover_test_plan(
    root: Path,
    repository_package: str,
) -> tuple[list[str], Path, list[str], list[str]]:
    package_output = _run_checked(
        ["python3", str(INTEGRATION_PACKAGES_HELPER), "--root", str(root)],
        root,
    )
    packages = sorted({line.strip() for line in package_output.splitlines() if line.strip()})
    if repository_package not in packages:
        raise RunnerError(
            f"repository package not found in integration package discovery: "
            f"{repository_package}"
        )

    baseline = _go_list_package(root, repository_package)
    tagged = _go_list_package(root, repository_package, "integration")
    try:
        repository_dir = Path(str(tagged["Dir"])).resolve()
    except KeyError as exc:
        raise RunnerError(f"go list omitted Dir for {repository_package}") from exc

    baseline_files = _test_files(baseline)
    tagged_files = _test_files(tagged)
    integration_files = tagged_files - baseline_files
    if not integration_files:
        raise RunnerError(
            f"no integration-only test files discovered for {repository_package}"
        )

    all_entries, _ = _test_entries(
        root,
        [repository_dir / name for name in sorted(tagged_files)],
    )
    integration_entries, _ = _test_entries(
        root,
        [repository_dir / name for name in sorted(integration_files)],
    )
    if not integration_entries:
        raise RunnerError(
            f"no integration test entries discovered for {repository_package}"
        )
    return (
        [package for package in packages if package != repository_package],
        repository_dir,
        all_entries,
        integration_entries,
    )


def run_integration_tests(
    root: Path,
    repository_package: str,
    shard_count: int,
    max_regex_bytes: int,
) -> int:
    with tempfile.TemporaryDirectory(prefix="sub2api-repository-integration-") as temporary:
        repository_binary = Path(temporary) / "repository.test"
        with tempfile.TemporaryDirectory(
            prefix="sub2api-integration-overlap-"
        ) as overlap_temp:
            compile_command = Command(
                "repository-compile",
                (
                    "go",
                    "test",
                    "-c",
                    "-tags=integration",
                    "-o",
                    str(repository_binary),
                    repository_package,
                ),
                root,
            )
            compile_log = Path(overlap_temp) / "repository-compile.log"
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
                other_packages, repository_dir, all_entries, integration_entries = (
                    discover_test_plan(root, repository_package)
                )
                patterns = shard_service_tests(
                    integration_entries,
                    shard_count,
                    max_regex_bytes,
                )
                print(
                    f"integration-test-runner: STAGE discovery "
                    f"({time.monotonic() - discovery_started_at:.1f}s)",
                    flush=True,
                )

                if other_packages:
                    other_command = Command(
                        "other-packages",
                        tuple(["go", "test", "-tags=integration", *other_packages]),
                        root,
                    )
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
                    f"integration-test-runner: STAGE repository-compile "
                    f"({compile_elapsed:.1f}s)",
                    flush=True,
                )

                registry_started_at = time.monotonic()
                verify_binary_registry(
                    repository_binary,
                    repository_dir,
                    all_entries,
                    subject="repository",
                    environment={"SUB2API_TEST_REGISTRY_ONLY": "1"},
                )
                print(
                    f"integration-test-runner: STAGE registry-check "
                    f"({time.monotonic() - registry_started_at:.1f}s)",
                    flush=True,
                )

                repository_commands: list[Command] = []
                width = len(str(len(patterns) - 1))
                for index, pattern in enumerate(patterns):
                    repository_commands.append(
                        Command(
                            f"repository-shard-{index:0{width}d}",
                            (
                                str(repository_binary),
                                "-test.run",
                                pattern,
                                "-test.timeout=10m",
                                "-test.paniconexit0",
                            ),
                            repository_dir,
                        )
                    )
                return run_commands(
                    repository_commands,
                    background,
                    runner_name="integration-test-runner",
                    temporary_prefix="sub2api-integration-",
                )
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
    parser.add_argument(
        "--repository-package",
        default=DEFAULT_REPOSITORY_PACKAGE,
    )
    parser.add_argument("--shards", type=int, default=DEFAULT_SHARDS)
    parser.add_argument(
        "--max-regex-bytes",
        type=int,
        default=DEFAULT_MAX_REGEX_BYTES,
    )
    args = parser.parse_args(argv)
    try:
        return run_integration_tests(
            args.root.resolve(),
            args.repository_package,
            args.shards,
            args.max_regex_bytes,
        )
    except (OSError, RunnerError) as exc:
        print(f"integration-test-runner: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
