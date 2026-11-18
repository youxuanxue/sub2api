#!/usr/bin/env python3
"""cn-prod remote execution: Tencent TAT by default, AWS SSM only when asked.

Design: docs/approved/design-cn-prod-rolling-deploy.md §1 (identity) and §3.

- TAT is the default channel. SSM runs only with ``via='ssm'``; there is no
  automatic fallback. A TAT poll timeout returns ``TimedOut`` and stops,
  because a timeout cannot prove the command never started, and running it
  again over SSM could apply the same change twice.
- Every script is prefixed with an identity check: ``hostname -I`` must list
  the node's private IP, otherwise the host exits 97 and we raise.
- The script travels inside a quoted bash heredoc that is written to a temp
  file and run with stdin closed. Nothing is spliced into a redirect, and the
  script cannot swallow its own body from stdin. (Hand-written
  ``base64 -d > ...`` one-liners truncated /usr/bin/bash on 2026-10-25.)
- Credentials: tccli reads TENCENTCLOUD_SECRET_ID/KEY from the environment and
  aws uses its own chain. This module never reads, passes or prints them.

CLI:
  python3 deploy/domestic/remote_exec.py nodes
  python3 deploy/domestic/remote_exec.py run --node app-b --script FILE [--via ssm] [--timeout N]
  python3 deploy/domestic/remote_exec.py put-file --node app-b --local F --remote P [--mode 600]
"""
from __future__ import annotations

import argparse
import base64
import gzip
import hashlib
import ipaddress
import json
import re
import shlex
import subprocess
import sys
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Callable

DEFAULT_MANIFEST = Path(__file__).resolve().parent / "prod-target.json"

OUTPUT_TAIL_BYTES = 8 * 1024
# TAT caps Content; counted on the base64-encoded command we submit.
TAT_MAX_CONTENT_B64 = 64 * 1024
# AWS-RunShellScript caps the whole document at ~97KB; keep headroom for JSON.
SSM_MAX_COMMAND_BYTES = 90 * 1024
PUT_FILE_MAX_BYTES = 48 * 1024
PUT_FILE_ROOTS = ("/opt/tokenkey/", "/var/lib/tokenkey/", "/private/")
IDENTITY_MISMATCH_EXIT = 97

POLL_INTERVAL_SECONDS = 3
# Extra local wait beyond the remote timeout, for delivery and result upload.
POLL_GRACE_SECONDS = 60
MAX_TIMEOUT_SECONDS = 86400

HEREDOC_DELIMITER = "TOKENKEY_REMOTE_EXEC_EOF"

_TAT_PENDING = {"PENDING", "DELIVERING", "DELIVER_DELAYED", "RUNNING", "CANCELLING"}
_TAT_TIMED_OUT = {"TIMEOUT", "TASK_TIMEOUT"}
_SSM_PENDING = {"Pending", "InProgress", "Delayed", "Cancelling"}

_LHINS_RE = re.compile(r"^lhins-[a-z0-9]+$")
_SSM_ID_RE = re.compile(r"^mi-[0-9a-f]+$")
_NAME_RE = re.compile(r"^[a-z0-9][a-z0-9-]{0,30}$")
_REGION_RE = re.compile(r"^[a-z]{2}(-[a-z]+)+(-[0-9]+)?$")
_MODE_RE = re.compile(r"^[0-7]{3,4}$")

Runner = Callable[..., str]


class RemoteExecError(Exception):
    """Refused before dispatch, dispatch failed, or the host failed identity."""


@dataclass(frozen=True)
class Node:
    name: str
    role: str  # 'entry' | 'app' | 'data'
    lighthouse_instance_id: str
    private_ip: str
    ssm_instance_id: str | None
    # Additions beyond the agreed contract; defaulted so positional
    # construction with the five contract fields keeps working.
    tat_region: str = "ap-beijing"
    ssm_region: str | None = None


@dataclass(frozen=True)
class Result:
    channel: str  # 'tat' | 'ssm'
    status: str  # 'Success' | 'Failed' | 'TimedOut'
    exit_code: int | None
    stdout_tail: str
    stderr_tail: str


# --------------------------------------------------------------------------
# manifest


def _require(cond: bool, msg: str) -> None:
    if not cond:
        raise RemoteExecError(msg)


def _check_ip(value: object, where: str) -> str:
    _require(isinstance(value, str) and value != "", f"{where}: private_ip missing")
    try:
        ip = ipaddress.ip_address(value)  # type: ignore[arg-type]
    except ValueError as exc:
        raise RemoteExecError(f"{where}: private_ip {value!r} is not an IP address") from exc
    _require(ip.is_private, f"{where}: private_ip {value} is not a private address")
    return str(ip)


def _check_lhins(value: object, where: str) -> str:
    _require(
        isinstance(value, str) and bool(_LHINS_RE.match(value)),
        f"{where}: lighthouse_instance_id missing or malformed ({value!r})",
    )
    return value  # type: ignore[return-value]


def _check_ssm(value: object, where: str) -> str | None:
    if value in (None, ""):
        return None
    _require(
        isinstance(value, str) and bool(_SSM_ID_RE.match(value)),
        f"{where}: ssm_instance_id malformed ({value!r})",
    )
    return value  # type: ignore[return-value]


def _check_region(value: object, where: str) -> str:
    _require(
        isinstance(value, str) and bool(_REGION_RE.match(value)),
        f"{where} missing or malformed ({value!r})",
    )
    return value  # type: ignore[return-value]


def _flatten_data_machine(data: dict, manifest: Path) -> None:
    """Accept prod nested data_machine{} by projecting to the flat cn-prod keys."""
    nested = data.get("data_machine")
    if not isinstance(nested, dict):
        return
    mapping = (
        ("lighthouse_instance_id", "data_lighthouse_instance_id"),
        ("private_ip", "data_private_ip"),
        ("expected_ssm_instance_id", "data_ssm_instance_id"),
        ("ssm_region", "data_ssm_region"),
    )
    for src, dest in mapping:
        if dest not in data and nested.get(src) not in (None, ""):
            data[dest] = nested[src]
    if "ssm_region" not in data and nested.get("ssm_region"):
        data["ssm_region"] = nested["ssm_region"]


def load_nodes(manifest: Path = DEFAULT_MANIFEST) -> dict[str, Node]:
    """Return nodes keyed 'entry', each apps[].name, and 'data'."""
    try:
        data = json.loads(Path(manifest).read_text())
    except (OSError, ValueError) as exc:
        raise RemoteExecError(f"{manifest}: cannot read manifest: {exc}") from exc
    _require(isinstance(data, dict), f"{manifest}: manifest must be a JSON object")
    _flatten_data_machine(data, manifest)

    tat_region = _check_region(data.get("tat_region"), f"{manifest}: tat_region")
    ssm_region = data.get("ssm_region")
    ssm_region = _check_region(ssm_region, f"{manifest}: ssm_region") if ssm_region else None
    data_ssm_region = data.get("data_ssm_region") or ssm_region
    if data_ssm_region:
        data_ssm_region = _check_region(data_ssm_region, f"{manifest}: data_ssm_region")

    nodes: dict[str, Node] = {}

    entry = data.get("entry")
    _require(
        isinstance(entry, dict),
        f"{manifest}: 'entry' object missing; rolling deploy needs the entry host identity",
    )
    nodes["entry"] = Node(
        name="entry",
        role="entry",
        lighthouse_instance_id=_check_lhins(entry.get("lighthouse_instance_id"), "entry"),
        private_ip=_check_ip(entry.get("private_ip"), "entry"),
        ssm_instance_id=_check_ssm(entry.get("ssm_instance_id"), "entry"),
        tat_region=tat_region,
        ssm_region=ssm_region,
    )

    apps = data.get("apps")
    _require(
        isinstance(apps, list) and len(apps) > 0,
        f"{manifest}: 'apps' list missing or empty; rolling deploy needs at least one app node",
    )
    for i, app in enumerate(apps):
        where = f"apps[{i}]"
        _require(isinstance(app, dict), f"{where}: must be an object")
        name = app.get("name")
        _require(
            isinstance(name, str) and bool(_NAME_RE.match(name)),
            f"{where}: name missing or malformed ({name!r})",
        )
        _require(name not in ("entry", "data"), f"{where}: name {name!r} is reserved")
        _require(name not in nodes, f"{where}: duplicate app name {name!r}")
        nodes[name] = Node(
            name=name,
            role="app",
            lighthouse_instance_id=_check_lhins(app.get("lighthouse_instance_id"), where),
            private_ip=_check_ip(app.get("private_ip"), where),
            ssm_instance_id=_check_ssm(app.get("ssm_instance_id"), where),
            tat_region=tat_region,
            ssm_region=ssm_region,
        )

    nodes["data"] = Node(
        name="data",
        role="data",
        lighthouse_instance_id=_check_lhins(data.get("data_lighthouse_instance_id"), "data_lighthouse_instance_id"),
        private_ip=_check_ip(data.get("data_private_ip"), "data_private_ip"),
        ssm_instance_id=_check_ssm(data.get("data_ssm_instance_id"), "data_ssm_instance_id"),
        tat_region=tat_region,
        ssm_region=data_ssm_region,
    )

    ips = [n.private_ip for n in nodes.values()]
    _require(len(ips) == len(set(ips)), f"{manifest}: private_ip values must be unique ({ips})")
    ids = [n.lighthouse_instance_id for n in nodes.values()]
    _require(len(ids) == len(set(ids)), f"{manifest}: lighthouse_instance_id values must be unique")
    return nodes


def app_nodes(manifest: Path = DEFAULT_MANIFEST) -> list[Node]:
    """App nodes in apps[] order, which is the rolling order."""
    return [n for n in load_nodes(manifest).values() if n.role == "app"]


# --------------------------------------------------------------------------
# script assembly


def _identity_preamble(node: Node) -> str:
    ip = shlex.quote(node.private_ip)
    # node.name and private_ip are regex/ipaddress-validated by load_nodes.
    return (
        f"if ! hostname -I 2>/dev/null | tr ' ' '\\n' | grep -qxF {ip}; then\n"
        f"  echo \"remote_exec: identity mismatch: expected {node.private_ip} for node {node.name}, "
        f"host has: $(hostname -I 2>/dev/null)\" >&2\n"
        f"  exit {IDENTITY_MISMATCH_EXIT}\n"
        "fi\n"
    )


def build_remote_command(node: Node, script: str) -> str:
    """Wrap ``script`` with the identity check inside a quoted bash heredoc.

    The heredoc lands in a temp file which bash runs with stdin closed, so the
    outer shell can be /bin/sh (dash) and the script cannot read its own body.
    """
    body = _identity_preamble(node) + script
    if not body.endswith("\n"):
        body += "\n"
    if any(line == HEREDOC_DELIMITER for line in body.splitlines()):
        raise RemoteExecError(f"script contains the heredoc delimiter line {HEREDOC_DELIMITER}")
    return (
        'TK_REMOTE_SCRIPT="$(mktemp /tmp/tokenkey-remote-exec.XXXXXX)" || exit 1\n'
        f"cat > \"$TK_REMOTE_SCRIPT\" <<'{HEREDOC_DELIMITER}'\n"
        f"{body}"
        f"{HEREDOC_DELIMITER}\n"
        'bash "$TK_REMOTE_SCRIPT" </dev/null\n'
        "TK_REMOTE_RC=$?\n"
        'rm -f "$TK_REMOTE_SCRIPT"\n'
        'exit "$TK_REMOTE_RC"\n'
    )


def _tail(raw: bytes | str | None, limit: int = OUTPUT_TAIL_BYTES) -> str:
    if not raw:
        return ""
    if isinstance(raw, str):
        raw = raw.encode("utf-8", errors="replace")
    if limit <= 0:
        return raw.decode("utf-8", errors="replace")
    return raw[-limit:].decode("utf-8", errors="replace")


# --------------------------------------------------------------------------
# runners


def _default_runner(argv: list[str], *, input: str | None = None) -> str:
    proc = subprocess.run(argv, input=input, capture_output=True, text=True, check=True)
    return proc.stdout


def _call(runner: Runner, argv: list[str], what: str) -> dict:
    try:
        out = runner(argv)
    except subprocess.CalledProcessError as exc:
        # Never echo argv: it carries the whole script.
        raise RemoteExecError(f"{what} failed (rc={exc.returncode}): {_tail(exc.stderr)[-1024:]}") from exc
    except FileNotFoundError as exc:
        raise RemoteExecError(f"{what}: CLI not found ({exc.filename})") from exc
    try:
        return json.loads(out)
    except ValueError as exc:
        raise RemoteExecError(f"{what}: non-JSON response") from exc


def _poll_budget(timeout: int) -> int:
    return -(-(timeout + POLL_GRACE_SECONDS) // POLL_INTERVAL_SECONDS)


def _run_tat(
    node: Node,
    command: str,
    timeout: int,
    runner: Runner,
    sleep: Callable[[float], None],
    *,
    output_bytes: int = OUTPUT_TAIL_BYTES,
) -> Result:
    content = base64.b64encode(command.encode("utf-8")).decode("ascii")
    if len(content) > TAT_MAX_CONTENT_B64:
        raise RemoteExecError(
            f"TAT Content is {len(content)} bytes after base64, over the {TAT_MAX_CONTENT_B64} limit; "
            "ship large payloads via the S3 prewarm path instead"
        )
    resp = _call(
        runner,
        [
            "tccli", "tat", "RunCommand",
            "--region", node.tat_region,
            "--CommandType", "SHELL",
            "--Content", content,
            "--InstanceIds", json.dumps([node.lighthouse_instance_id]),
            "--Timeout", str(timeout),
            "--CommandName", f"tokenkey-remote-exec-{node.name}",
            "--output", "json",
        ],
        "tccli tat RunCommand",
    )
    invocation_id = resp.get("InvocationId")
    if not isinstance(invocation_id, str) or not invocation_id:
        raise RemoteExecError("tccli tat RunCommand: response has no InvocationId")
    print(f"remote_exec: tat node={node.name} invocation-id={invocation_id}", file=sys.stderr)

    describe = [
        "tccli", "tat", "DescribeInvocationTasks",
        "--region", node.tat_region,
        "--Filters", json.dumps([{"Name": "invocation-id", "Values": [invocation_id]}]),
        "--HideOutput", "False",
        "--output", "json",
    ]
    task: dict = {}
    for _ in range(_poll_budget(timeout)):
        sleep(POLL_INTERVAL_SECONDS)
        try:
            tasks = _call(runner, describe, "tccli tat DescribeInvocationTasks").get("InvocationTaskSet") or []
        except RemoteExecError:
            continue  # transient API error: keep polling the same invocation
        if tasks:
            task = tasks[0]
        if task.get("TaskStatus") and task["TaskStatus"] not in _TAT_PENDING:
            break
    else:
        # Local poll budget exhausted. Do not retry and do not switch channel.
        return Result("tat", "TimedOut", None, _tat_output(task, output_bytes), "")

    status = task["TaskStatus"]
    result = task.get("TaskResult") or {}
    exit_code = result.get("ExitCode")
    exit_code = int(exit_code) if isinstance(exit_code, (int, str)) and str(exit_code).lstrip("-").isdigit() else None
    if status == "SUCCESS" and exit_code == 0:
        mapped = "Success"
    elif status in _TAT_TIMED_OUT:
        mapped = "TimedOut"
    else:
        mapped = "Failed"
    # TAT returns one combined stream; it goes to stdout_tail.
    return Result("tat", mapped, exit_code, _tat_output(task, output_bytes), "")


def _tat_output(task: dict, output_bytes: int = OUTPUT_TAIL_BYTES) -> str:
    raw = (task.get("TaskResult") or {}).get("Output")
    if not raw:
        return ""
    try:
        return _tail(base64.b64decode(raw), output_bytes)
    except ValueError:
        return _tail(raw, output_bytes)


def _run_ssm(
    node: Node,
    command: str,
    timeout: int,
    runner: Runner,
    sleep: Callable[[float], None],
    *,
    output_bytes: int = OUTPUT_TAIL_BYTES,
) -> Result:
    if not node.ssm_instance_id:
        raise RemoteExecError(f"node {node.name} has no ssm_instance_id; SSM is not available for it")
    if not node.ssm_region:
        raise RemoteExecError(f"node {node.name} has no ssm_region")
    size = len(command.encode("utf-8"))
    if size > SSM_MAX_COMMAND_BYTES:
        raise RemoteExecError(f"SSM command is {size} bytes, over the {SSM_MAX_COMMAND_BYTES} limit")
    params = json.dumps({"commands": [command], "executionTimeout": [str(timeout)]})
    # Parameters go inline as JSON in argv (no temp file, no shell quoting).
    resp = _call(
        runner,
        [
            "aws", "ssm", "send-command",
            "--region", node.ssm_region,
            "--instance-ids", node.ssm_instance_id,
            "--document-name", "AWS-RunShellScript",
            "--comment", f"tokenkey remote-exec {node.name}",
            "--parameters", params,
            "--output", "json",
        ],
        "aws ssm send-command",
    )
    command_id = (resp.get("Command") or {}).get("CommandId")
    if not isinstance(command_id, str) or not command_id:
        raise RemoteExecError("aws ssm send-command: response has no CommandId")
    print(f"remote_exec: ssm node={node.name} command-id={command_id}", file=sys.stderr)

    get = [
        "aws", "ssm", "get-command-invocation",
        "--region", node.ssm_region,
        "--command-id", command_id,
        "--instance-id", node.ssm_instance_id,
        "--output", "json",
    ]
    inv: dict = {}
    for _ in range(_poll_budget(timeout)):
        sleep(POLL_INTERVAL_SECONDS)
        try:
            inv = _call(runner, get, "aws ssm get-command-invocation")
        except RemoteExecError:
            continue  # InvocationDoesNotExist right after send-command, or transient
        if inv.get("Status") and inv["Status"] not in _SSM_PENDING:
            break
    else:
        return Result(
            "ssm",
            "TimedOut",
            None,
            _tail(inv.get("StandardOutputContent"), output_bytes),
            _tail(inv.get("StandardErrorContent"), output_bytes),
        )

    status = inv["Status"]
    code = inv.get("ResponseCode")
    exit_code = code if isinstance(code, int) and code >= 0 else None
    mapped = {"Success": "Success", "TimedOut": "TimedOut"}.get(status, "Failed")
    return Result(
        "ssm",
        mapped,
        exit_code,
        _tail(inv.get("StandardOutputContent"), output_bytes),
        _tail(inv.get("StandardErrorContent"), output_bytes),
    )


# --------------------------------------------------------------------------
# public API


def run(
    node: Node,
    script: str,
    *,
    timeout: int = 600,
    via: str = "tat",
    runner: Runner | None = None,
    sleep: Callable[[float], None] = time.sleep,
    output_bytes: int = OUTPUT_TAIL_BYTES,
) -> Result:
    """Run ``script`` on ``node`` once. Never retries, never switches channel.

    ``output_bytes`` caps returned stdout/stderr tails (default 8KiB). Pass a
    larger positive value for probe-style callers that need more of the stream;
    ``0`` keeps the full decoded payload (subject to cloud API truncation).
    """
    if via not in ("tat", "ssm"):
        raise RemoteExecError(f"via must be 'tat' or 'ssm' (got {via!r})")
    if not isinstance(timeout, int) or not 1 <= timeout <= MAX_TIMEOUT_SECONDS:
        raise RemoteExecError(f"timeout must be 1..{MAX_TIMEOUT_SECONDS} seconds (got {timeout!r})")
    if not isinstance(output_bytes, int) or output_bytes < 0:
        raise RemoteExecError(f"output_bytes must be >= 0 (got {output_bytes!r})")
    if via == "ssm" and not node.ssm_instance_id:
        raise RemoteExecError(f"node {node.name} has no ssm_instance_id; SSM is not available for it")
    command = build_remote_command(node, script)
    runner = runner or _default_runner
    if via == "tat":
        result = _run_tat(node, command, timeout, runner, sleep, output_bytes=output_bytes)
    else:
        result = _run_ssm(node, command, timeout, runner, sleep, output_bytes=output_bytes)
    if result.exit_code == IDENTITY_MISMATCH_EXIT:
        raise RemoteExecError(
            f"identity mismatch on node {node.name}: host does not own {node.private_ip} "
            f"({node.lighthouse_instance_id}); output: {result.stderr_tail or result.stdout_tail}".rstrip()
        )
    return result


def validate_remote_path(remote: str) -> str:
    if not isinstance(remote, str) or not remote:
        raise RemoteExecError("remote path is empty")
    if ".." in remote:
        raise RemoteExecError(f"remote path must not contain '..': {remote}")
    if any(ord(c) < 0x20 or c == "\x7f" for c in remote):
        raise RemoteExecError("remote path contains control characters")
    if remote.endswith("/") or "//" in remote or "/./" in remote or remote.endswith("/."):
        raise RemoteExecError(f"remote path must name a file: {remote}")
    if not remote.startswith(PUT_FILE_ROOTS):
        raise RemoteExecError(f"remote path outside permitted roots {PUT_FILE_ROOTS}: {remote}")
    return remote


def build_put_file_script(remote: str, payload: bytes, mode: str) -> str:
    """Shell that writes ``payload`` to ``remote`` only if its sha256 matches.

    Decode into a temp file next to the target, verify, ``install -m`` into a
    second temp file, then ``mv -f`` (rename(2)) over the target. On any
    failure the target is left untouched and the temp files are removed.
    """
    want = hashlib.sha256(payload).hexdigest()
    b64 = base64.b64encode(gzip.compress(payload, mtime=0)).decode("ascii")
    return "\n".join([
        "set -euo pipefail",
        f"DEST={shlex.quote(remote)}",
        f"MODE={shlex.quote(mode)}",
        f"WANT={want}",
        'DIR="$(dirname "$DEST")"',
        'mkdir -p "$DIR"',
        'TMP="$(mktemp "$DIR/.remote-exec.XXXXXX")"',
        'READY="$TMP.ready"',
        "trap 'rm -f \"$TMP\" \"$READY\"' EXIT",
        f"printf %s {shlex.quote(b64)} | base64 -d | gzip -dc > \"$TMP\"",
        'GOT="$(sha256sum "$TMP" | cut -d" " -f1)"',
        'if [ "$GOT" != "$WANT" ]; then',
        '  echo "put_file: sha256 mismatch for $DEST (got $GOT want $WANT); target untouched" >&2',
        "  exit 3",
        "fi",
        'install -m "$MODE" "$TMP" "$READY"',
        'mv -f "$READY" "$DEST"',
        "printf 'put_file ok path=%s mode=%s sha256=%s\\n' \"$DEST\" \"$MODE\" \"$GOT\"",
        "",
    ])


def put_file(
    node: Node,
    local: Path,
    remote: str,
    *,
    mode: str = "600",
    via: str = "tat",
    runner: Runner | None = None,
) -> Result:
    """Copy a small local file to ``remote`` on ``node`` with sha256 verification."""
    validate_remote_path(remote)
    if not isinstance(mode, str) or not _MODE_RE.match(mode):
        raise RemoteExecError(f"mode must be 3-4 octal digits (got {mode!r})")
    local = Path(local)
    if not local.is_file():
        raise RemoteExecError(f"local file not found: {local}")
    size = local.stat().st_size
    if size > PUT_FILE_MAX_BYTES:
        raise RemoteExecError(
            f"{local} is {size} bytes, over the {PUT_FILE_MAX_BYTES} put_file limit; images go via S3 prewarm"
        )
    payload = local.read_bytes()
    return run(node, build_put_file_script(remote, payload, mode), timeout=120, via=via, runner=runner)


# --------------------------------------------------------------------------
# CLI


def _print_result(result: Result) -> None:
    print(f"remote_exec: channel={result.channel} status={result.status} exit_code={result.exit_code}", file=sys.stderr)
    if result.stdout_tail:
        print("--- stdout (last 8KB) ---", file=sys.stderr)
        sys.stdout.write(result.stdout_tail)
        if not result.stdout_tail.endswith("\n"):
            sys.stdout.write("\n")
        sys.stdout.flush()
    if result.stderr_tail:
        print("--- stderr (last 8KB) ---", file=sys.stderr)
        sys.stderr.write(result.stderr_tail)
        if not result.stderr_tail.endswith("\n"):
            sys.stderr.write("\n")


def main(argv: list[str] | None = None, *, runner: Runner | None = None) -> int:
    parser = argparse.ArgumentParser(description="cn-prod remote execution (TAT default, SSM explicit)")
    parser.add_argument("--manifest", type=Path, default=DEFAULT_MANIFEST)
    sub = parser.add_subparsers(dest="cmd", required=True)
    sub.add_parser("nodes", help="print node table (no secrets)")
    p_run = sub.add_parser("run", help="run a script file on one node")
    p_run.add_argument("--node", required=True)
    p_run.add_argument("--script", required=True, type=Path)
    p_run.add_argument("--via", choices=("tat", "ssm"), default="tat")
    p_run.add_argument("--timeout", type=int, default=600)
    p_put = sub.add_parser("put-file", help="copy a small file to one node")
    p_put.add_argument("--node", required=True)
    p_put.add_argument("--local", required=True, type=Path)
    p_put.add_argument("--remote", required=True)
    p_put.add_argument("--mode", default="600")
    p_put.add_argument("--via", choices=("tat", "ssm"), default="tat")
    args = parser.parse_args(argv)

    try:
        nodes = load_nodes(args.manifest)
        if args.cmd == "nodes":
            print(f"{'NAME':<8} {'ROLE':<6} {'LIGHTHOUSE':<16} {'PRIVATE_IP':<15} {'TAT_REGION':<12} SSM")
            for n in nodes.values():
                print(f"{n.name:<8} {n.role:<6} {n.lighthouse_instance_id:<16} {n.private_ip:<15} "
                      f"{n.tat_region:<12} {n.ssm_instance_id or '-'}")
            return 0
        node = nodes.get(args.node)
        if node is None:
            raise RemoteExecError(f"unknown node {args.node!r}; known: {', '.join(nodes)}")
        if args.cmd == "run":
            try:
                script = args.script.read_text()
            except OSError as exc:
                raise RemoteExecError(f"cannot read script {args.script}: {exc}") from exc
            result = run(node, script, timeout=args.timeout, via=args.via, runner=runner)
        else:
            result = put_file(node, args.local, args.remote, mode=args.mode, via=args.via, runner=runner)
    except RemoteExecError as exc:
        print(f"remote_exec: error: {exc}", file=sys.stderr)
        return 2
    _print_result(result)
    return 0 if result.status == "Success" else 1


if __name__ == "__main__":
    sys.exit(main())
