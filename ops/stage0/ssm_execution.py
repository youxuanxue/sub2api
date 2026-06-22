#!/usr/bin/env python3
"""Shared prod SSM execution glue for TokenKey ops tools (stdlib-only, no edge-routing deps).

Centralizes the two things ops/pricing/manage-overlay-runtime.py and
ops/newapi/apply-model-mapping-live.py both need (and previously copy-pasted):

  - resolve_prod_instance(): the prod Stage0 CFN stack's InstanceId (format-validated).
  - run_shell_b64(): run a base64-encoded shell script on prod via SSM, return stdout.

`run_shell_b64` writes the decoded script to a FILE and bash's the file rather than piping
it to `bash` via stdin. A `docker exec -i ... psql` inside the script otherwise shares the
shell's stdin; when stdin is the decode pipe it SLURPS the rest of the script, silently
truncating everything after the first psql call while still reporting Success (rc=0).
Keeping this — plus the get-command-invocation guard and the stderr-capture limit — in ONE
place means those fixes cannot regress per-tool.

Distinct from ops/stage0/edge_ssm_execution.py (which resolves EC2/Lightsail EDGE targets
via the edge routing matrix); this module is the prod-control-plane counterpart with no
edge dependencies, so it importlib-loads cleanly from any ops tool.
"""
from __future__ import annotations

import json
import re
import subprocess
import sys
from typing import NoReturn

PROD_REGION = "us-east-1"
PROD_STACK = "tokenkey-prod-stage0"
# AWS SSM get-command-invocation inlines stdout/stderr up to ~2500 chars; cap our captured
# stderr below that so a failure message is preserved without truncating the JSON envelope.
_STDERR_CAP = 2000


def fail(msg: str) -> NoReturn:
    print(f"ERROR: {msg}", file=sys.stderr)
    sys.exit(2)


def resolve_prod_instance() -> str:
    try:
        out = subprocess.check_output(
            ["aws", "cloudformation", "describe-stacks", "--region", PROD_REGION,
             "--stack-name", PROD_STACK,
             "--query", "Stacks[0].Outputs[?OutputKey=='InstanceId'].OutputValue",
             "--output", "text"], text=True).strip()
    except subprocess.CalledProcessError as e:
        fail(f"describe-stacks failed for {PROD_STACK}/{PROD_REGION}: {e}")
    if not re.match(r"^i-[0-9a-f]{8,}$", out):
        fail(f"no valid InstanceId for {PROD_STACK}/{PROD_REGION} (got {out!r})")
    return out


def run_shell_b64(instance_id: str, shell_b64: str, comment: str) -> str:
    """Run a base64-encoded shell script on prod via SSM; return stdout.

    File-backed exec (not pipe-to-bash) so an inner `docker exec -i` cannot slurp the script
    from stdin. `set -uo pipefail` (no -e) so a non-zero inner script still lets us capture
    rc, clean up, and propagate the exit code.
    """
    command = (
        "set -uo pipefail\n"
        f"echo {shell_b64} | base64 -d > /tmp/.tk_ssm_$$.sh\n"
        "bash /tmp/.tk_ssm_$$.sh; rc=$?\n"
        "rm -f /tmp/.tk_ssm_$$.sh\n"
        "exit $rc"
    )
    params = json.dumps({"commands": [command]}, ensure_ascii=False)
    try:
        cid = subprocess.check_output(
            ["aws", "ssm", "send-command", "--region", PROD_REGION,
             "--instance-ids", instance_id, "--document-name", "AWS-RunShellScript",
             "--comment", comment, "--parameters", params,
             "--query", "Command.CommandId", "--output", "text"], text=True).strip()
    except subprocess.CalledProcessError as e:
        fail(f"ssm send-command failed ({comment}): {e}")
    subprocess.run(["aws", "ssm", "wait", "command-executed", "--region", PROD_REGION,
                    "--command-id", cid, "--instance-id", instance_id], check=False)
    try:
        inv = json.loads(subprocess.check_output(
            ["aws", "ssm", "get-command-invocation", "--region", PROD_REGION,
             "--command-id", cid, "--instance-id", instance_id, "--output", "json"], text=True))
    except (subprocess.CalledProcessError, ValueError) as e:
        fail(f"ssm get-command-invocation failed ({comment}): {e}")
    if inv.get("Status") != "Success" or inv.get("ResponseCode") != 0:
        err = (inv.get("StandardErrorContent") or "").strip()[:_STDERR_CAP]
        fail(f"ssm cmd {cid} status={inv.get('Status')} rc={inv.get('ResponseCode')} "
             f"({comment})\n  stderr: {err}")
    return (inv.get("StandardOutputContent") or "").strip()
