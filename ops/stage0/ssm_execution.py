#!/usr/bin/env python3
"""Shared prod remote execution glue for TokenKey ops tools.

Daily default is Tencent TAT via ``deploy.domestic.remote_exec`` (see
``docs/approved/design-tencent-ops-control-plane.md``). SSM remains an
explicit break-glass when ``TOKENKEY_REMOTE_VIA=ssm``.

Centralizes:
  - resolve_prod_instance(): cutover-aware prod identity (still returns mi-* for
    legacy callers; TAT path resolves lighthouse nodes via PROD_SSM_NODE).
  - run_shell_b64(): run a base64-encoded shell script on the selected node.

`run_shell_b64` writes the decoded script to a FILE and bash's the file rather
than piping it to `bash` via stdin (inner `docker exec -i` must not slurp the
script).
"""
from __future__ import annotations

import importlib.util
import json
import os
import pathlib
import re
import shlex
import subprocess
import sys
from typing import NoReturn

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
PROD_MANIFEST = REPO_ROOT / "deploy" / "tencent" / "candidate-usw-20261022.json"
PROD_REGION = "us-east-1"
PROD_STACK = "tokenkey-prod-stage0"
_STDERR_CAP = 2000
_INSTANCE_RE = re.compile(r"^(?:i|mi)-[0-9a-f]{8,17}$")
_RESOLVE_MOD = None
# Probe / registry publishers often need more than the rolling 8KiB tail.
_TAT_OUTPUT_BYTES = 512 * 1024


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


def resolve_prod_identity() -> tuple[str, str]:
    """Return ``(instance_id, ssm_region)`` via cutover-aware resolve_prod_ssm_target.

    Multi-app fleets require ``PROD_SSM_NODE`` (entry|app-old|app-cand|data).
    Kept for break-glass SSM callers and identity checks.
    """
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
    params = json.dumps({"commands": ["bash -c " + shlex.quote(command)]}, ensure_ascii=False)
    try:
        cid = subprocess.check_output(
            [
                "aws",
                "ssm",
                "send-command",
                "--region",
                PROD_REGION,
                "--instance-ids",
                instance_id,
                "--document-name",
                "AWS-RunShellScript",
                "--comment",
                comment,
                "--parameters",
                params,
                "--query",
                "Command.CommandId",
                "--output",
                "text",
            ],
            text=True,
        ).strip()
    except subprocess.CalledProcessError as e:
        fail(f"ssm send-command failed ({comment}): {e}")
    subprocess.run(
        [
            "aws",
            "ssm",
            "wait",
            "command-executed",
            "--region",
            PROD_REGION,
            "--command-id",
            cid,
            "--instance-id",
            instance_id,
        ],
        check=False,
    )
    try:
        inv = json.loads(
            subprocess.check_output(
                [
                    "aws",
                    "ssm",
                    "get-command-invocation",
                    "--region",
                    PROD_REGION,
                    "--command-id",
                    cid,
                    "--instance-id",
                    instance_id,
                    "--output",
                    "json",
                ],
                text=True,
            )
        )
    except (subprocess.CalledProcessError, ValueError) as e:
        fail(f"ssm get-command-invocation failed ({comment}): {e}")
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
