#!/usr/bin/env python3
"""check-local-aws-pyexpat.py — detect and repair a local Homebrew aws/pyexpat breakage.

This helper exists for the macOS/Homebrew failure mode where `aws` crashes before
any real work starts because the Python runtime behind Homebrew `awscli` cannot
load `pyexpat` (typically because the module is still linked against the system
`/usr/lib/libexpat.1.dylib` while Homebrew Python/expat expects the newer
Homebrew dylib instead).

It is intentionally single-purpose:
- default mode only checks for this exact local issue;
- `--apply` only mutates the machine when that exact issue is detected;
- unrelated `aws` failures are reported as "not this issue" rather than guessed.

Usage:
  python3 scripts/checks/check-local-aws-pyexpat.py
  python3 scripts/checks/check-local-aws-pyexpat.py --apply
  python3 scripts/checks/check-local-aws-pyexpat.py --selftest

Exit codes:
  0  healthy / not applicable / repair succeeded
  1  exact actionable aws+pyexpat issue detected (or repair did not clear it)
  2  helper misuse / unexpected local execution failure
"""
from __future__ import annotations

import argparse
import json
import re
import shlex
import shutil
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Sequence

REPO_ROOT = Path(__file__).resolve().parents[2]
SELF_APPLY_CMD = "python3 scripts/checks/check-local-aws-pyexpat.py --apply"
SYSTEM_EXPAT = "/usr/lib/libexpat.1.dylib"
BREW_PREFIX_MARKERS = ("/opt/homebrew/", "/usr/local/")
ISSUE_PATTERNS = (
    re.compile(r"No module named 'pyexpat'", re.IGNORECASE),
    re.compile(r"ImportError:.*pyexpat", re.IGNORECASE | re.DOTALL),
    re.compile(r"dlopen\(.*pyexpat", re.IGNORECASE | re.DOTALL),
    re.compile(r"Library not loaded: .*libexpat", re.IGNORECASE | re.DOTALL),
    re.compile(r"Symbol not found: .*expat", re.IGNORECASE | re.DOTALL),
)


@dataclass
class CommandResult:
    argv: list[str]
    returncode: int
    stdout: str
    stderr: str

    @property
    def output(self) -> str:
        parts = [self.stdout.strip(), self.stderr.strip()]
        return "\n".join(part for part in parts if part).strip()


@dataclass
class LocalAwsState:
    platform: str
    aws_path: Path | None
    aws_realpath: Path | None
    aws_result: CommandResult | None
    shebang: str | None
    python_path: Path | None
    python_formula: str | None
    pyexpat_path: Path | None
    pyexpat_links: list[str]


def run_command(argv: Sequence[str]) -> CommandResult:
    proc = subprocess.run(list(argv), capture_output=True, text=True)
    return CommandResult(list(argv), proc.returncode, proc.stdout, proc.stderr)


def first_line(path: Path) -> str | None:
    try:
        with path.open("r", encoding="utf-8", errors="replace") as handle:
            line = handle.readline().rstrip("\n")
    except OSError:
        return None
    return line or None


def infer_python_path(shebang: str | None) -> Path | None:
    if not shebang or not shebang.startswith("#!"):
        return None
    try:
        parts = shlex.split(shebang[2:].strip())
    except ValueError:
        return None
    if not parts:
        return None
    if parts[0].endswith("/env"):
        if len(parts) < 2:
            return None
        resolved = shutil.which(parts[1])
        return Path(resolved) if resolved else None
    return Path(parts[0])


def infer_formula(path: Path | None) -> str | None:
    if path is None:
        return None
    text = str(path)
    match = re.search(r"^/(?:opt/homebrew|usr/local)/(?:opt|Cellar)/([^/]+)(?:/|$)", text)
    if match:
        return match.group(1)
    return None


def detect_pyexpat_path(python_path: Path | None) -> Path | None:
    if python_path is None:
        return None
    probe = run_command(
        [
            str(python_path),
            "-c",
            (
                "import json,sys,sysconfig;"
                "print(json.dumps({"
                "'base_prefix': sys.base_prefix,"
                "'version': f'{sys.version_info.major}.{sys.version_info.minor}',"
                "'ext_suffix': sysconfig.get_config_var('EXT_SUFFIX') or ''"
                "}))"
            ),
        ]
    )
    if probe.returncode != 0:
        return None
    try:
        payload = json.loads(probe.stdout.strip())
    except json.JSONDecodeError:
        return None
    base_prefix = payload.get("base_prefix")
    version = payload.get("version")
    ext_suffix = payload.get("ext_suffix")
    if not base_prefix or not version:
        return None
    dynload_dir = Path(base_prefix) / "lib" / f"python{version}" / "lib-dynload"
    if ext_suffix:
        exact = dynload_dir / f"pyexpat{ext_suffix}"
        if exact.is_file():
            return exact.resolve()
    candidates = sorted(dynload_dir.glob("pyexpat*.so"))
    return candidates[0].resolve() if candidates else None


def otool_links(path: Path | None) -> list[str]:
    if path is None or shutil.which("otool") is None:
        return []
    result = run_command(["otool", "-L", str(path)])
    if result.returncode != 0:
        return []
    links: list[str] = []
    for line in result.stdout.splitlines()[1:]:
        stripped = line.strip()
        if not stripped:
            continue
        links.append(stripped.split(" ", 1)[0])
    return links


def gather_state() -> LocalAwsState:
    aws_exec = shutil.which("aws")
    aws_path = Path(aws_exec).resolve() if aws_exec else None
    aws_realpath = aws_path.resolve() if aws_path else None
    aws_result = run_command([str(aws_path), "--version"]) if aws_path else None
    shebang = first_line(aws_realpath) if aws_realpath else None
    python_path = infer_python_path(shebang)
    pyexpat_path = detect_pyexpat_path(python_path)
    return LocalAwsState(
        platform=sys.platform,
        aws_path=aws_path,
        aws_realpath=aws_realpath,
        aws_result=aws_result,
        shebang=shebang,
        python_path=python_path,
        python_formula=infer_formula(python_path),
        pyexpat_path=pyexpat_path,
        pyexpat_links=otool_links(pyexpat_path),
    )


def is_homebrew_managed(state: LocalAwsState) -> bool:
    paths = [state.aws_realpath, state.python_path]
    return any(path and str(path).startswith(BREW_PREFIX_MARKERS) for path in paths)


def output_matches_issue(output: str) -> bool:
    return any(pattern.search(output) for pattern in ISSUE_PATTERNS)


def classify_state(state: LocalAwsState) -> str:
    if state.platform != "darwin":
        return "not_applicable"
    if state.aws_path is None or state.aws_result is None:
        return "not_applicable"
    if state.aws_result.returncode == 0:
        return "healthy"
    if not is_homebrew_managed(state):
        return "not_applicable"
    output = state.aws_result.output
    if output_matches_issue(output) and SYSTEM_EXPAT in state.pyexpat_links:
        return "homebrew_pyexpat_expat_mismatch"
    return "not_applicable"


def helper_relpath() -> str:
    return str(Path(__file__).resolve().relative_to(REPO_ROOT))


def apply_cmd() -> str:
    return f"python3 {helper_relpath()} --apply"


def format_issue_report(state: LocalAwsState, *, quiet: bool) -> str:
    if quiet:
        return (
            "FAIL: detected local Homebrew aws/pyexpat libexpat mismatch.\n"
            f"Run: {apply_cmd()}"
        )
    lines = [
        "FAIL: detected local Homebrew aws/pyexpat libexpat mismatch.",
        "This is the macOS/Homebrew issue where awscli's Python cannot load pyexpat before any real AWS call starts.",
    ]
    if state.aws_path:
        lines.append(f"aws path: {state.aws_path}")
    if state.aws_realpath and state.aws_realpath != state.aws_path:
        lines.append(f"aws realpath: {state.aws_realpath}")
    if state.python_path:
        formula = f" ({state.python_formula})" if state.python_formula else ""
        lines.append(f"python path: {state.python_path}{formula}")
    if state.pyexpat_path:
        lines.append(f"pyexpat path: {state.pyexpat_path}")
    if SYSTEM_EXPAT in state.pyexpat_links:
        lines.append(f"current expat link: {SYSTEM_EXPAT}")
    if state.aws_result and state.aws_result.output:
        lines.append("aws --version output:")
        lines.extend(f"  {line}" for line in state.aws_result.output.splitlines())
    lines.append(f"Repair: {apply_cmd()}")
    return "\n".join(lines)


def describe_non_issue(state: LocalAwsState) -> str:
    if state.platform != "darwin":
        return "ok: local aws/pyexpat check not applicable on non-macOS"
    if state.aws_path is None:
        return "ok: local aws/pyexpat check not applicable because aws is not on PATH"
    if state.aws_result and state.aws_result.returncode == 0:
        return "ok: local aws/pyexpat check passed (aws --version healthy)"
    if not is_homebrew_managed(state):
        return "ok: local aws/pyexpat check not applicable because aws is not Homebrew-managed"
    return "ok: aws is unhealthy, but it is not the targeted Homebrew pyexpat/libexpat mismatch"


def brew_expat_dylib() -> Path | None:
    if shutil.which("brew") is None:
        return None
    result = run_command(["brew", "--prefix", "expat"])
    if result.returncode != 0:
        return None
    prefix = result.stdout.strip()
    if not prefix:
        return None
    dylib = Path(prefix) / "lib" / "libexpat.1.dylib"
    return dylib if dylib.is_file() else None


def repair(state: LocalAwsState) -> int:
    if classify_state(state) != "homebrew_pyexpat_expat_mismatch":
        print(
            "FAIL: auto-repair refused because the exact Homebrew aws/pyexpat mismatch was not detected.",
            file=sys.stderr,
        )
        print("Re-run without --apply to inspect the current state.", file=sys.stderr)
        return 1
    if state.pyexpat_path is None or not state.pyexpat_path.is_file():
        print("FAIL: could not locate the pyexpat extension to repair.", file=sys.stderr)
        return 1
    expat_dylib = brew_expat_dylib()
    if expat_dylib is None:
        print("FAIL: could not resolve Homebrew expat dylib via `brew --prefix expat`.", file=sys.stderr)
        return 1
    if shutil.which("install_name_tool") is None:
        print("FAIL: install_name_tool is required for repair but is not available.", file=sys.stderr)
        return 1
    if shutil.which("codesign") is None:
        print("FAIL: codesign is required for repair but is not available.", file=sys.stderr)
        return 1

    print(f"Repairing pyexpat linkage: {state.pyexpat_path}")
    print(f"  from: {SYSTEM_EXPAT}")
    print(f"  to:   {expat_dylib}")
    relink = run_command(
        [
            "install_name_tool",
            "-change",
            SYSTEM_EXPAT,
            str(expat_dylib),
            str(state.pyexpat_path),
        ]
    )
    if relink.returncode != 0:
        print(relink.output or "install_name_tool failed", file=sys.stderr)
        return 1

    sign = run_command(["codesign", "-f", "-s", "-", str(state.pyexpat_path)])
    if sign.returncode != 0:
        print(sign.output or "codesign failed", file=sys.stderr)
        return 1

    after = gather_state()
    status = classify_state(after)
    if status != "healthy":
        print("FAIL: repair ran but aws is still not healthy.", file=sys.stderr)
        if after.aws_result and after.aws_result.output:
            print(after.aws_result.output, file=sys.stderr)
        return 1

    print("ok: aws/pyexpat repair succeeded; aws --version is healthy again")
    return 0


def run_selftest() -> int:
    cases: list[tuple[str, bool]] = []
    cases.append(
        (
            "shebang direct python path",
            infer_python_path("#!/opt/homebrew/opt/python@3.14/bin/python3.14")
            == Path("/opt/homebrew/opt/python@3.14/bin/python3.14"),
        )
    )
    cases.append(
        (
            "formula inferred from opt path",
            infer_formula(Path("/opt/homebrew/opt/python@3.14/bin/python3.14")) == "python@3.14",
        )
    )
    cases.append(
        (
            "formula inferred from Cellar path",
            infer_formula(Path("/opt/homebrew/Cellar/awscli/2.35.15/bin/aws")) == "awscli",
        )
    )
    cases.append(
        (
            "match no-module-named-pyexpat",
            output_matches_issue("ModuleNotFoundError: No module named 'pyexpat'"),
        )
    )
    cases.append(
        (
            "match dlopen pyexpat expat symbol",
            output_matches_issue("dlopen(/x/pyexpat.so, 0x0002): Symbol not found: _XML_SetFoo"),
        )
    )
    cases.append(
        (
            "ignore unrelated aws auth failure",
            not output_matches_issue("An error occurred (AccessDenied) when calling the DescribeStacks operation"),
        )
    )
    healthy_state = LocalAwsState(
        platform="darwin",
        aws_path=Path("/opt/homebrew/bin/aws"),
        aws_realpath=Path("/opt/homebrew/Cellar/awscli/2.35.15/bin/aws"),
        aws_result=CommandResult(["aws", "--version"], 0, "aws-cli/2.35.15", ""),
        shebang="#!/opt/homebrew/opt/python@3.14/bin/python3.14",
        python_path=Path("/opt/homebrew/opt/python@3.14/bin/python3.14"),
        python_formula="python@3.14",
        pyexpat_path=Path("/opt/homebrew/Cellar/python@3.14/3.14.6/.../pyexpat.so"),
        pyexpat_links=[SYSTEM_EXPAT],
    )
    issue_state = LocalAwsState(
        platform="darwin",
        aws_path=Path("/opt/homebrew/bin/aws"),
        aws_realpath=Path("/opt/homebrew/Cellar/awscli/2.35.15/bin/aws"),
        aws_result=CommandResult(
            ["aws", "--version"],
            1,
            "",
            "ImportError: dlopen(/x/pyexpat.so): Symbol not found: _XML_SetAllocTrackerActivationThreshold",
        ),
        shebang="#!/opt/homebrew/opt/python@3.14/bin/python3.14",
        python_path=Path("/opt/homebrew/opt/python@3.14/bin/python3.14"),
        python_formula="python@3.14",
        pyexpat_path=Path("/opt/homebrew/Cellar/python@3.14/3.14.6/.../pyexpat.so"),
        pyexpat_links=[SYSTEM_EXPAT],
    )
    unrelated_state = LocalAwsState(
        platform="darwin",
        aws_path=Path("/opt/homebrew/bin/aws"),
        aws_realpath=Path("/opt/homebrew/Cellar/awscli/2.35.15/bin/aws"),
        aws_result=CommandResult(
            ["aws", "--version"],
            255,
            "",
            "An error occurred (AccessDenied) when calling the DescribeStacks operation",
        ),
        shebang="#!/opt/homebrew/opt/python@3.14/bin/python3.14",
        python_path=Path("/opt/homebrew/opt/python@3.14/bin/python3.14"),
        python_formula="python@3.14",
        pyexpat_path=Path("/opt/homebrew/Cellar/python@3.14/3.14.6/.../pyexpat.so"),
        pyexpat_links=[SYSTEM_EXPAT],
    )
    cases.append(("classify healthy", classify_state(healthy_state) == "healthy"))
    cases.append(
        (
            "classify targeted mismatch",
            classify_state(issue_state) == "homebrew_pyexpat_expat_mismatch",
        )
    )
    cases.append(("classify unrelated failure as non-issue", classify_state(unrelated_state) == "not_applicable"))

    ok = True
    for name, passed in cases:
        print(f"  {'PASS' if passed else 'FAIL'} {name}")
        ok = ok and passed
    if ok:
        print(f"ok: {Path(__file__).name} selftest ({len(cases)}/{len(cases)} cases passed)")
        return 0
    print(f"FAIL: {Path(__file__).name} selftest", file=sys.stderr)
    return 1


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--apply", action="store_true", help="apply the targeted repair when the exact issue is detected")
    parser.add_argument("--quiet", action="store_true", help="suppress success / not-applicable chatter")
    parser.add_argument("--selftest", action="store_true", help="run hermetic helper selftests")
    args = parser.parse_args()

    if args.selftest:
        return run_selftest()

    try:
        state = gather_state()
    except Exception as exc:  # pragma: no cover - defensive shell helper path
        print(f"FATAL: unexpected helper failure: {exc}", file=sys.stderr)
        return 2

    if args.apply:
        return repair(state)

    status = classify_state(state)
    if status == "homebrew_pyexpat_expat_mismatch":
        print(format_issue_report(state, quiet=args.quiet), file=sys.stderr)
        return 1
    if not args.quiet:
        print(describe_non_issue(state))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
