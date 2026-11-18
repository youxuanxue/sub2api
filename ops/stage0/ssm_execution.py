#!/usr/bin/env python3
"""Shared prod remote execution glue for TokenKey ops tools.

Daily default is Tencent TAT via ``deploy.domestic.remote_exec`` (see
``docs/approved/design-tencent-ops-control-plane.md``). SSM remains an
explicit break-glass when ``TOKENKEY_REMOTE_VIA=ssm``.

Centralizes:
  - resolve_prod_instance(): cutover-aware prod identity (still returns mi-* for
    legacy callers; TAT path resolves lighthouse nodes via PROD_SSM_NODE).
  - run_shell_b64(): run a base64-encoded shell script on the selected node.
  - send_run_shell_script / wait_and_get: sole owner of the AWS SSM
    ``send-command`` argv literal for the control-plane allowlist gate.

`run_shell_b64` writes the decoded script to a FILE and bash's the file rather
than piping it to `bash` via stdin (inner `docker exec -i` must not slurp the
script).

Error strings for the SSM helpers intentionally avoid the gate regex
``ssm\\s+send-command``.
"""
from __future__ import annotations

import argparse
import importlib.util
import json
import os
import pathlib
import re
import shlex
import subprocess
import sys
import time
from collections.abc import Callable, Mapping, Sequence
from typing import Any, NoReturn

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
PROD_MANIFEST = REPO_ROOT / "deploy" / "tencent" / "candidate-usw-20261022.json"
PROD_REGION = "us-east-1"
PROD_STACK = "tokenkey-prod-stage0"
_STDERR_CAP = 2000
_INSTANCE_RE = re.compile(r"^(?:i|mi)-[0-9a-f]{8,17}$")
_RESOLVE_MOD = None
# Probe / registry publishers often need more than the rolling 8KiB tail.
_TAT_OUTPUT_BYTES = 512 * 1024

Runner = Callable[[Sequence[str]], Any]


class AwsSsmError(RuntimeError):
    """SSM transport or invocation failure."""


def _default_ssm_runner(argv: Sequence[str]) -> Any:
    output_json = "--output" in argv and argv[list(argv).index("--output") + 1] == "json"
    raw = subprocess.check_output(list(argv), text=True)
    if output_json:
        return json.loads(raw)
    return raw.strip()


def send_run_shell_script(
    *,
    region: str,
    instance_id: str,
    parameters: Mapping[str, Any] | str,
    comment: str = "",
    timeout_seconds: int | None = None,
    runner: Runner | None = None,
) -> str:
    """Send AWS-RunShellScript; return CommandId.

    ``parameters`` is either a JSON object (commands/executionTimeout) or a
    path string ``file:///...`` for AWS CLI file parameters.
    """
    argv: list[str] = [
        "aws",
        "ssm",
        "send-command",
        "--region",
        region,
        "--instance-ids",
        instance_id,
        "--document-name",
        "AWS-RunShellScript",
    ]
    if comment:
        argv.extend(["--comment", comment])
    if timeout_seconds is not None:
        argv.extend(["--timeout-seconds", str(int(timeout_seconds))])
    if isinstance(parameters, str):
        argv.extend(["--parameters", parameters])
    else:
        argv.extend(["--parameters", json.dumps(parameters, ensure_ascii=False)])
    try:
        if runner is None:
            # CLI/default: text CommandId (matches legacy shell --query/--output text).
            text_argv = [*argv, "--query", "Command.CommandId", "--output", "text"]
            command_id = subprocess.check_output(text_argv, text=True).strip()
        else:
            resp = runner([*argv, "--output", "json"])
            if isinstance(resp, str):
                command_id = resp.strip()
            else:
                command_id = str((resp.get("Command") or {}).get("CommandId") or "").strip()
    except Exception as exc:  # noqa: BLE001 — surface transport to callers
        raise AwsSsmError(f"SSM SendCommand failed: {exc}") from exc
    if not command_id:
        raise AwsSsmError("SSM SendCommand response has no CommandId")
    return command_id


def get_invocation(
    *,
    region: str,
    command_id: str,
    instance_id: str,
    runner: Runner | None = None,
) -> dict[str, Any]:
    run = runner or _default_ssm_runner
    argv = [
        "aws",
        "ssm",
        "get-command-invocation",
        "--region",
        region,
        "--command-id",
        command_id,
        "--instance-id",
        instance_id,
        "--output",
        "json",
    ]
    try:
        inv = run(argv)
    except Exception as exc:  # noqa: BLE001
        raise AwsSsmError(f"SSM GetCommandInvocation failed: {exc}") from exc
    if not isinstance(inv, dict):
        raise AwsSsmError("SSM GetCommandInvocation returned non-object")
    return inv


def wait_and_get(
    *,
    region: str,
    command_id: str,
    instance_id: str,
    timeout_seconds: int = 300,
    poll_seconds: float = 5.0,
    runner: Runner | None = None,
) -> dict[str, Any]:
    """Poll until terminal status or timeout; return final invocation."""
    deadline = time.monotonic() + timeout_seconds
    last: dict[str, Any] = {}
    while time.monotonic() < deadline:
        try:
            last = get_invocation(
                region=region,
                command_id=command_id,
                instance_id=instance_id,
                runner=runner,
            )
        except AwsSsmError as exc:
            if "InvocationDoesNotExist" in str(exc):
                time.sleep(poll_seconds)
                continue
            raise
        status = str(last.get("Status") or "")
        if status in {"Success", "Failed", "TimedOut", "Cancelled"}:
            return last
        time.sleep(poll_seconds)
    last = last or {"Status": "TimedOut"}
    if str(last.get("Status") or "") not in {"Success", "Failed", "TimedOut", "Cancelled"}:
        last = {**last, "Status": "TimedOut"}
    return last


def fail(msg: str) -> NoReturn:
    print(f"ERROR: {msg}", file=sys.stderr)
    sys.exit(2)


def _resolve_mod():
    global _RESOLVE_MOD
    if _RESOLVE_MOD is not None:
        return _RESOLVE_MOD
    path = pathlib.Path(__file__).resolve().parent / "resolve_prod_ssm_target.py"
    spec = importlib.util.spec_from_file_location("tk_resolve_prod_ssm_target", path)
    if spec is None or spec.loader is None:
        fail(f"cannot load {path}")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    _RESOLVE_MOD = mod
    return mod


def _remote_via() -> str:
    via = os.environ.get("TOKENKEY_REMOTE_VIA", "tat").strip().lower()
    if via not in ("tat", "ssm"):
        fail(f"TOKENKEY_REMOTE_VIA must be tat|ssm (got {via!r})")
    return via


def _remote_exec():
    if str(REPO_ROOT) not in sys.path:
        sys.path.insert(0, str(REPO_ROOT))
    from deploy.domestic import remote_exec  # noqa: WPS433

    return remote_exec


def resolve_prod_node():
    """Return the prod ``remote_exec.Node`` selected by ``PROD_SSM_NODE``."""
    remote_exec = _remote_exec()
    node_name = (os.environ.get("PROD_SSM_NODE") or os.environ.get("TOKENKEY_REMOTE_NODE") or "").strip()
    if not node_name:
        fail("PROD_SSM_NODE (or TOKENKEY_REMOTE_NODE) is required for remote execution")
    nodes = remote_exec.load_nodes(PROD_MANIFEST)
    if node_name not in nodes:
        fail(f"unknown prod node {node_name!r}; want one of {sorted(nodes)}")
    return nodes[node_name]


def _matrix_identity_for_node(node_name: str) -> tuple[str, str]:
    """Return ``(ssm_instance_id, ssm_region)`` from the USW matrix without AWS probes.

    TAT publishers (pricing registry, etc.) still call ``resolve_prod_instance`` for
    caller compatibility, but must not require Hybrid inventory credentials.
    """
    mod = _resolve_mod()
    data = mod.load_tencent_usw_matrix_file(mod.TENCENT_USW_MATRIX)
    target = data.get("target") if isinstance(data.get("target"), dict) else {}
    region = str(data.get("ssm_region") or target.get("ssm_region") or "").strip()
    if not region:
        fail("tencent usw matrix missing ssm_region")
    expected = ""
    if node_name == "entry":
        entry = data.get("entry")
        if not isinstance(entry, dict):
            fail("matrix missing entry")
        expected = str(entry.get("ssm_instance_id") or "").strip()
    elif node_name == "data":
        dm = data.get("data_machine")
        if not isinstance(dm, dict):
            fail("matrix missing data_machine")
        expected = str(dm.get("expected_ssm_instance_id") or dm.get("ssm_instance_id") or "").strip()
    else:
        apps = data.get("apps")
        if not isinstance(apps, list):
            fail("matrix missing apps[]")
        match = next(
            (a for a in apps if isinstance(a, dict) and str(a.get("name") or "") == node_name),
            None,
        )
        if match is None:
            fail(f"unknown prod node {node_name!r} in tencent usw matrix")
        expected = str(match.get("ssm_instance_id") or "").strip()
    if not _INSTANCE_RE.match(expected):
        fail(f"node {node_name!r} missing valid ssm_instance_id in matrix ({expected!r})")
    return expected, region


def resolve_prod_identity() -> tuple[str, str]:
    """Return ``(instance_id, ssm_region)``.

    Daily TAT default: matrix pin only (no AWS ``DescribeInstanceInformation``).
    Break-glass ``TOKENKEY_REMOTE_VIA=ssm``: cutover-aware resolve with Hybrid
    online check. Multi-app fleets require ``PROD_SSM_NODE``.
    """
    if _remote_via() == "tat":
        # Validate the node exists on the TAT manifest, then pin matrix identity.
        resolve_prod_node()
        node_name = (os.environ.get("PROD_SSM_NODE") or os.environ.get("TOKENKEY_REMOTE_NODE") or "").strip()
        return _matrix_identity_for_node(node_name)

    mod = _resolve_mod()
    mode = mod.resolve_mode("auto")
    if mode == "tencent-usw-candidate":
        outputs = mod.resolve_tencent_usw_candidate(mod.TENCENT_USW_MATRIX, node="")
    elif mode == "aws-fresh":
        outputs = mod.resolve_fresh_aws()  # fails: retired
    elif mode == "aws":
        outputs = mod.resolve_aws(PROD_STACK)
    elif mode == "hetzner":
        outputs = mod.resolve_hetzner()  # fails: retired
    else:
        fail(f"unsupported prod SSM mode {mode!r} (live prod is Tencent USW via auto)")
    instance_id = str(outputs.get("instance_id") or "")
    region = str(outputs.get("ssm_region") or "").strip()
    if not _INSTANCE_RE.match(instance_id):
        fail(f"resolve_prod_ssm_target returned invalid instance id {instance_id!r}")
    if not region:
        fail("resolve_prod_ssm_target returned empty ssm_region")
    return instance_id, region


def resolve_prod_instance() -> str:
    """Resolve prod instance id and bind ``PROD_REGION`` to its ssm_region."""
    global PROD_REGION
    instance_id, region = resolve_prod_identity()
    PROD_REGION = region
    return instance_id


def region_for_instance_id(instance_id: str) -> str:
    """SSM region for an explicit pin without re-resolving a different instance."""
    instance_id = instance_id.strip()
    if not _INSTANCE_RE.match(instance_id):
        fail(f"invalid EC2/SSM-managed instance id {instance_id!r}")
    mod = _resolve_mod()
    if instance_id.startswith("mi-"):
        target = mod.load_tencent_usw_matrix(mod.TENCENT_USW_MATRIX)
        region = str(target.get("ssm_region") or "eu-west-2").strip()
        if not region:
            fail("tencent usw matrix missing ssm_region")
        return region
    fail(f"EC2 prod identity {instance_id!r} is retired (Tokyo stopped 2026-10-23); pin an mi-* id")


def data_container_preamble(*, redis: bool = False, docker: str = "sudo docker") -> str:
    """Remote preamble exporting TK_PG (and TK_REDIS) from the canonical resolver."""
    path = pathlib.Path(__file__).resolve().parents[1] / "lib" / "resolve_app_container.py"
    spec = importlib.util.spec_from_file_location("tk_resolve_app_container_ssm", path)
    if spec is None or spec.loader is None:
        fail(f"cannot load {path}")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod.data_container_preamble(docker=docker, redis=redis)


def _script_body(shell_b64: str, preamble: str) -> str:
    return (
        "set -uo pipefail\n"
        + preamble
        + f"echo {shell_b64} | base64 -d > /tmp/.tk_ssm_$$.sh\n"
        "bash /tmp/.tk_ssm_$$.sh; rc=$?\n"
        "rm -f /tmp/.tk_ssm_$$.sh\n"
        "exit $rc"
    )


def _run_shell_b64_tat(shell_b64: str, comment: str, *, preamble: str = "") -> str:
    remote_exec = _remote_exec()
    node = resolve_prod_node()
    print(f"ssm_execution: tat node={node.name} comment={comment}", file=sys.stderr)
    try:
        result = remote_exec.run(
            node,
            _script_body(shell_b64, preamble),
            timeout=600,
            via="tat",
            output_bytes=_TAT_OUTPUT_BYTES,
        )
    except remote_exec.RemoteExecError as exc:
        fail(f"tat run failed ({comment}): {exc}")
    if result.status != "Success" or result.exit_code not in (0, None):
        err = (result.stderr_tail or result.stdout_tail or "").strip()[:_STDERR_CAP]
        fail(
            f"tat cmd status={result.status} rc={result.exit_code} ({comment})\n"
            f"  stderr: {err}"
        )
    return (result.stdout_tail or "").strip()


def _run_shell_b64_ssm(instance_id: str, shell_b64: str, comment: str, *, preamble: str = "") -> str:
    command = _script_body(shell_b64, preamble)
    # Hybrid hosts start RunShellScript in dash. Use bash -c here so selecting
    # bash does not give stdin-consuming commands a pipe containing our script.
    params = {"commands": ["bash -c " + shlex.quote(command)]}
    try:
        cid = send_run_shell_script(
            region=PROD_REGION,
            instance_id=instance_id,
            parameters=params,
            comment=comment,
        )
        inv = wait_and_get(
            region=PROD_REGION,
            command_id=cid,
            instance_id=instance_id,
            timeout_seconds=600,
        )
    except AwsSsmError as e:
        fail(f"SSM break-glass failed ({comment}): {e}")
    if inv.get("Status") != "Success" or inv.get("ResponseCode") != 0:
        err = (inv.get("StandardErrorContent") or "").strip()[:_STDERR_CAP]
        fail(
            f"ssm cmd {cid} status={inv.get('Status')} rc={inv.get('ResponseCode')} "
            f"({comment})\n  stderr: {err}"
        )
    return (inv.get("StandardOutputContent") or "").strip()


def run_shell_b64(instance_id: str, shell_b64: str, comment: str, *, preamble: str = "") -> str:
    """Run a base64-encoded shell script on prod; return stdout.

    Daily default: TAT (``TOKENKEY_REMOTE_VIA`` unset or ``tat``). Break-glass:
    ``TOKENKEY_REMOTE_VIA=ssm`` keeps the historical Hybrid path and uses
    ``instance_id`` / ``PROD_REGION``.
    """
    if _remote_via() == "tat":
        return _run_shell_b64_tat(shell_b64, comment, preamble=preamble)
    return _run_shell_b64_ssm(instance_id, shell_b64, comment, preamble=preamble)


def _cli(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="cmd", required=True)

    send_p = sub.add_parser("send", help="Send AWS-RunShellScript; print CommandId")
    send_p.add_argument("--region", required=True)
    send_p.add_argument("--instance-id", required=True)
    send_p.add_argument("--comment", default="")
    send_p.add_argument("--timeout-seconds", type=int)
    send_p.add_argument(
        "--params-file",
        required=True,
        help="Path to parameters JSON (commands[] / executionTimeout)",
    )

    wait_p = sub.add_parser("wait-get", help="Poll invocation; write JSON to stdout")
    wait_p.add_argument("--region", required=True)
    wait_p.add_argument("--instance-id", required=True)
    wait_p.add_argument("--command-id", required=True)
    wait_p.add_argument("--timeout-seconds", type=int, default=300)

    args = parser.parse_args(argv)
    if args.cmd == "send":
        params_path = args.params_file
        if params_path.startswith("file://"):
            params_path = params_path[len("file://") :]
        doc = json.loads(open(params_path, encoding="utf-8").read())
        cid = send_run_shell_script(
            region=args.region,
            instance_id=args.instance_id,
            parameters=doc,
            comment=args.comment,
            timeout_seconds=args.timeout_seconds,
        )
        print(cid)
        return 0
    if args.cmd == "wait-get":
        inv = wait_and_get(
            region=args.region,
            command_id=args.command_id,
            instance_id=args.instance_id,
            timeout_seconds=args.timeout_seconds,
        )
        print(json.dumps(inv, ensure_ascii=False))
        return 0 if inv.get("Status") == "Success" and inv.get("ResponseCode") in (0, None) else 1
    return 2


if __name__ == "__main__":
    try:
        raise SystemExit(_cli())
    except AwsSsmError as exc:
        print(f"ssm_execution: {exc}", file=sys.stderr)
        raise SystemExit(1) from exc
