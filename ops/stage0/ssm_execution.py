#!/usr/bin/env python3
"""Shared prod SSM execution glue for TokenKey ops tools (stdlib-only, no edge-routing deps).

Centralizes the two things ops/pricing/manage-overlay-runtime.py and
ops/newapi/apply-model-mapping-live.py both need (and previously copy-pasted):

  - resolve_prod_instance(): cutover-aware prod SSM target (aws i-* or Hybrid mi-*).
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

Prod target resolution delegates to ``resolve_prod_ssm_target.py`` (same owner as
run-probe / deploy-stage0): ``PROD_SSM_TARGET`` / cutover SSM param selects aws vs
hetzner. ``resolve_prod_instance`` updates module ``PROD_REGION`` to the target's
``ssm_region`` so subsequent ``run_shell_b64`` calls hit the right registration region.
"""
from __future__ import annotations

import importlib.util
import json
import pathlib
import re
import subprocess
import sys
from typing import NoReturn

PROD_REGION = "us-east-1"
PROD_STACK = "tokenkey-prod-stage0"
# AWS SSM get-command-invocation inlines stdout/stderr up to ~2500 chars; cap our captured
# stderr below that so a failure message is preserved without truncating the JSON envelope.
_STDERR_CAP = 2000
_INSTANCE_RE = re.compile(r"^(?:i|mi)-[0-9a-f]{8,17}$")
_RESOLVE_MOD = None


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


def resolve_prod_identity() -> tuple[str, str]:
    """Return ``(instance_id, ssm_region)`` via cutover-aware resolve_prod_ssm_target."""
    mod = _resolve_mod()
    mode = mod.resolve_mode("auto")
    if mode == "aws":
        outputs = mod.resolve_aws(PROD_STACK)
    else:
        outputs = mod.resolve_hetzner(mod.DEFAULT_MATRIX)
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
    """SSM region for an explicit pin without re-resolving a different instance.

    ``mi-*`` Hybrid nodes register in the Hetzner matrix region; ``i-*`` stays on
    the legacy Stage0 AWS region. Callers that pin an id must not keep a stale
    module ``PROD_REGION`` from a prior resolve.
    """
    instance_id = instance_id.strip()
    if not _INSTANCE_RE.match(instance_id):
        fail(f"invalid EC2/SSM-managed instance id {instance_id!r}")
    if instance_id.startswith("mi-"):
        mod = _resolve_mod()
        matrix = mod.load_hetzner_matrix(mod.DEFAULT_MATRIX)
        region = str(matrix.get("ssm_region") or "eu-west-2").strip()
        if not region:
            fail("hetzner matrix missing ssm_region")
        return region
    return "us-east-1"


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
