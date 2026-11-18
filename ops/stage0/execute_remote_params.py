#!/usr/bin/env python3
"""Execute an SSM-shaped ``commands[]`` params file via daily TAT (or SSM break-glass).

Legacy Stage0 shells still emit ``ssm-params.json`` for the host-parse gate.
Daily transport is ``ssm_execution`` (``TOKENKEY_REMOTE_VIA`` default ``tat``).
"""
from __future__ import annotations

import argparse
import base64
import importlib.util
import json
import pathlib
import sys


def _load_ssm_execution(repo: pathlib.Path):
    path = repo / "ops" / "stage0" / "ssm_execution.py"
    spec = importlib.util.spec_from_file_location("tk_ssm_execution_exec", path)
    if spec is None or spec.loader is None:
        raise SystemExit(f"cannot load {path}")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def _load_wrap(repo: pathlib.Path):
    path = repo / "ops" / "stage0" / "ssm_wrap_bash_commands.py"
    spec = importlib.util.spec_from_file_location("tk_ssm_wrap_exec", path)
    if spec is None or spec.loader is None:
        raise SystemExit(f"cannot load {path}")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--params-file", required=True, type=pathlib.Path)
    parser.add_argument("--comment", required=True)
    parser.add_argument("--stdout-file", type=pathlib.Path)
    parser.add_argument("--stderr-file", type=pathlib.Path)
    parser.add_argument(
        "--instance-id",
        default="",
        help="Break-glass SSM pin; TAT path uses PROD_SSM_NODE / matrix",
    )
    args = parser.parse_args(argv)

    repo = pathlib.Path(__file__).resolve().parents[2]
    wrap = _load_wrap(repo)
    ssm = _load_ssm_execution(repo)

    doc = json.loads(args.params_file.read_text(encoding="utf-8"))
    commands = doc.get("commands")
    if not isinstance(commands, list) or not commands:
        print("execute_remote_params: commands[] missing", file=sys.stderr)
        return 2
    # Match AWS-RunShellScript join, then unwrap bash -lc / b64 wrappers.
    joined = "\n".join(str(c) for c in commands)
    unwrapped = wrap.unwrap_commands([joined])
    script = "\n".join(unwrapped) if unwrapped else joined
    if not script.endswith("\n"):
        script += "\n"

    instance_id = (args.instance_id or "").strip()
    if not instance_id:
        instance_id = ssm.resolve_prod_instance()
    shell_b64 = base64.b64encode(script.encode("utf-8")).decode("ascii")
    try:
        out = ssm.run_shell_b64(instance_id, shell_b64, args.comment)
    except SystemExit:
        raise
    except Exception as exc:  # noqa: BLE001 — surface transport errors to callers
        print(f"execute_remote_params: {exc}", file=sys.stderr)
        return 1

    text = out if out.endswith("\n") else out + "\n"
    if args.stdout_file is not None:
        args.stdout_file.write_text(text, encoding="utf-8")
    else:
        sys.stdout.write(text)
    if args.stderr_file is not None:
        args.stderr_file.write_text("", encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
