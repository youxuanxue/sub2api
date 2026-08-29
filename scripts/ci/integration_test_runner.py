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
    RunnerError,
    RunningCommand,
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


def _test_binary_paths(
    build_dir: Path,
    packages: list[str],
) -> dict[str, Path]:
    binaries: dict[str, Path] = {}
    owners: dict[str, str] = {}
    for package in packages:
        binary_name = f"{Path(package).name}.test"
        if binary_name in owners:
            raise RunnerError(
                f"integration packages share test binary name {binary_name}: "
                f"{owners[binary_name]}, {package}"
            )
        owners[binary_name] = package
        binaries[package] = build_dir / binary_name
    return binaries


def list_integration_packages(root: Path, repository_package: str) -> list[str]:
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
    return packages


def discover_repository_entries(
    root: Path,
    repository_package: str,
) -> tuple[Path, list[str], list[str]]:
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
    return repository_dir, all_entries, integration_entries


def discover_test_plan(
    root: Path,
    repository_package: str,
) -> tuple[list[str], Path, list[str], list[str]]:
    packages = list_integration_packages(root, repository_package)
    repository_dir, all_entries, integration_entries = discover_repository_entries(
        root,
        repository_package,
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
    packages = list_integration_packages(root, repository_package)
    other_packages = [package for package in packages if package != repository_package]
    compile_packages = [repository_package, *other_packages]
    with tempfile.TemporaryDirectory(prefix="sub2api-integration-build-") as temporary:
        build_dir = Path(temporary)
        binaries = _test_binary_paths(build_dir, compile_packages)
        compile_command = Command(
            "shared-compile",
            (
                "go",
                "test",
                "-c",
                "-tags=integration",
                "-o",
                str(build_dir),
                *compile_packages,
            ),
            root,
        )
        compile_log = build_dir / "shared-compile.log"
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
        try:
            discovery_started_at = time.monotonic()
            repository_dir, all_entries, integration_entries = (
                discover_repository_entries(root, repository_package)
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
                raise RunnerError(f"{' '.join(compile_command.argv)}: {detail}")
            print(
                f"integration-test-runner: STAGE shared-compile "
                f"({compile_elapsed:.1f}s)",
                flush=True,
            )
            background.clear()

            repository_binary = binaries[repository_package]
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

            commands = [
                Command(
                    f"other-{Path(package).name}",
                    (
                        str(binaries[package]),
                        "-test.v",
                        "-test.timeout=10m",
                        "-test.paniconexit0",
                    ),
                    root / package.removeprefix("./"),
                )
                for package in other_packages
            ]
            width = len(str(len(patterns) - 1))
            for index, pattern in enumerate(patterns):
                commands.append(
                    Command(
                        f"repository-shard-{index:0{width}d}",
                        (
                            str(repository_binary),
                            "-test.v",
                            "-test.run",
                            pattern,
                            "-test.timeout=10m",
                            "-test.paniconexit0",
                        ),
                        repository_dir,
                    )
                )
            return run_commands(
                commands,
                runner_name="integration-test-runner",
                temporary_prefix="sub2api-integration-",
                slow_test_limit=5,
            )
        except BaseException:
            _terminate_processes(background)
            raise
        finally:
            compile_handle.close()


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
