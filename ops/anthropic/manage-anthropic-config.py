#!/usr/bin/env python3
"""
TokenKey Anthropic OAuth tier-baseline + prod stub pool-mode orchestrator.

One entrypoint, plan JSON as the only file between stages.  Four write
surfaces, all JSON-derived (no static SQL templates, no operator-written
SQL):

  A. edge OAuth account tier baseline
     — per ``anthropic-oauth-stability-baselines-tiered.json``
     — action.kind = ``edge_account_tier``
  B. prod anthropic api-key stub pool_mode (mirror stubs, base_url = api-*.tokenkey.dev)
     — per ``anthropic-stub-pool-baselines.json``
     — action.kind = ``prod_stub_pool``
  C. prod stub concurrency mirror (the "two-hop" capacity cascade)
     — derived from live ``schedulable=true`` concurrency, no baseline file
     — action.kind = ``edge_operator_concurrency`` (per edge) /
                      ``prod_concurrency_mirror`` (one prod tx)
  D. anthropic group Claude Code client restriction (Claude Code only)
     — per ``anthropic-group-claude-code-baselines.json`` (``claude_code_only: true``)
     — action.kind = ``anthropic_group_claude_code_only`` (one tx per edge + prod)
  E. edge operator (``users.id=1``) balance floor
     — per ``anthropic-edge-operator-balance-baselines.json``
     — action.kind = ``edge_operator_balance`` (edge only; when live balance < threshold → default)

Topology (which edges exist, and which prod stub maps to which edge) is read
from ``deploy/aws/stage0/edge-targets.json`` (each edge's ``domain`` field
is the authoritative prod-stub↔edge link) plus ``deploy/aws/lightsail/edge-targets-lightsail.json``
when an edge is live on Lightsail (auto route: LS ``deployable=true`` wins per
``ops/stage0/edge_routing_matrix.py``). Prod is pinned separately as ``PROD_TARGET`` —
never re-inferred from account names or ad-hoc slug parsing.

Stages
------
  1. snapshot — pull each deployable edge's anthropic OAuth accounts AND
                prod's anthropic api-key mirror stubs into one JSON; also
                each target's operator (users.id=1) concurrency + live
                Σ schedulable anthropic concurrency (surface C inputs)
  2. check    — invoke check-edge-oauth-stability.py for each edge × account
  3a. plan-edge-account-tier — declare an edge OAuth tier change
  3b. plan-tier-bump         — re-apply a tier baseline to every matching edge account
  3c. plan-stub-pool         — enable pool_mode on every prod stub matching the
                               base_url policy (idempotent; live-matched stubs skip)
  3d. plan-concurrency-mirror — align edge operator concurrency + prod stub
                               concurrency + prod operator concurrency to live
                               Σ schedulable anthropic (idempotent)
  3e. plan-group-claude-code-only — set claude_code_only=true on every
                                   anthropic group on each deployable edge
                                   and prod (admin UI: Claude Code only)
  3f. plan-edge-operator-balance — top up edge users.id=1 balance when
                                   live balance < min_balance_threshold
  4. apply    — render apply SQL from JSON, run via SSM, parse output
               (optional ``--sync-runtime`` post-step: settings UA + Redis
               fingerprint cache flush on affected edges + prod)
  5. verify   — re-snapshot, diff each expected_after vs live

Post-apply runtime sync (settings + Redis, no hand-written /tmp scripts):
  sync-runtime — upsert ``claude_code_user_agent_version`` + DEL
                 ``fingerprint:{oauth_account_id}`` on prod / edge targets

Guard-drift remediation (replaces manual plan-tier-bump × N + merge):
  plan-guard-drift-fix — read ``check`` guard output; emit one multi-action
                         plan with ``--force-template-rewrite`` for every
                         account whose guard status is ``drift``
  remediate-guard-drift — snapshot → check → plan-guard-drift-fix → apply
                          (--sync-runtime) → verify → check
                          (P0: bundle snapshot + batch guard + parallel edges;
                          --parallel-edges N default 6; --legacy-guard for旧路径)

History
-------
Prior to 2026-05-21 this orchestrator also covered prod-side cascading
writes (stub concurrency mirror, stub `extra.declared_rpm`, group
`rpm_limit` derived as Σ stub.declared_rpm, edge group cap derived as
Σ(base_rpm + rpm_sticky_buffer)).  That entire "account → group
aggregation" model was retired because layered SUM caps left no
headroom for sticky-buffer burst on the upstream OAuth pool — upstream
quota was being throttled before real traffic could exercise it.
Group `rpm_limit` is now set independently in the admin UI; this
orchestrator no longer writes to any group.

2026-05-23: the stub *concurrency* mirror was re-wired back into the
pipeline as surface C (``plan-concurrency-mirror``). It is NOT a revival
of the retired group-RPM aggregation — only the concurrency cascade
returns, and its basis changed from "Σ all anthropic concurrency" to
"Σ ``schedulable=true`` anthropic concurrency". The four-hop cascade is:
(1) edge account tier config (surface A); (2) edge ``users.id=1`` =
that edge's Σ schedulable; (3) each prod mirror stub's ``concurrency`` =
its edge's Σ schedulable (edge resolved via the stub's ``base_url`` matched
against ``edge-targets.json`` ``domain``); (4) prod ``users.id=1`` = prod's
Σ schedulable (computed after step 3 in the same prod transaction).

Each successful edge ``apply`` transaction also sets ``users.id=1``
``concurrency`` to the sum of ``concurrency`` on every ``schedulable=true``
``anthropic`` account row (not soft-deleted) on that same database — oauth
and api-key types — so operator default tracks live schedulable Anthropic
capacity (admin/diagnostic accounts parked at ``schedulable=false`` are
excluded, matching the scheduler's own view).

Exit codes
----------
  0  command succeeded; for check/verify, no violations / no drift
  1  command ran but reported violations / drift / apply step failure
  2  setup / SSM / target-resolution / schema error

Usage
-----
  manage-anthropic-config.py snapshot --out snap.json
  manage-anthropic-config.py check --snapshot snap.json
  manage-anthropic-config.py plan-edge-account-tier \\
      --edge uk1 --account en-ld-ec2-16-1-b --tier l2 \\
      --snapshot snap.json --out plan.json
  manage-anthropic-config.py apply --plan plan.json \\
      --confirm yes-apply-anthropic-config-cascade
  manage-anthropic-config.py verify --plan plan.json
"""
from __future__ import annotations

import argparse
import base64
import datetime as _dt
import importlib.util
import json
import os
import pathlib
import re
import subprocess
import sys
from collections import defaultdict
from concurrent.futures import ThreadPoolExecutor, as_completed
from typing import Any, Callable

REPO_ROOT = pathlib.Path(__file__).resolve().parents[2]
EDGE_MATRIX = REPO_ROOT / "deploy/aws/stage0/edge-targets.json"
TIER_BASELINES = REPO_ROOT / "deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json"
STUB_POOL_BASELINES = REPO_ROOT / "deploy/aws/stage0/anthropic-stub-pool-baselines.json"
GROUP_CLAUDE_CODE_BASELINES = (
    REPO_ROOT / "deploy/aws/stage0/anthropic-group-claude-code-baselines.json"
)
EDGE_OPERATOR_BALANCE_BASELINES = (
    REPO_ROOT / "deploy/aws/stage0/anthropic-edge-operator-balance-baselines.json"
)
CANONICAL_UA_JSON = REPO_ROOT / "deploy/aws/stage0/tk_canonical_cc_oauth.json"
HTTP_MIMICRY_BASELINES = REPO_ROOT / "deploy/aws/stage0/anthropic-http-mimicry-baselines.json"
RUNTIME_SYNC_SETTING_KEY = "claude_code_user_agent_version"
RUNTIME_SYNC_MIMICRY_SETTING_KEY = "claude_code_http_mimicry_manifest"
OPS_DIR = REPO_ROOT / "ops/anthropic"

# prod is not an entry in edge-targets.json (which is the edge matrix).
# Pin it explicitly so plan-stub-pool / apply / verify can resolve the prod
# Postgres without operators discovering CFN stack names from memory.
PROD_TARGET = {
    "region": "us-east-1",
    "stack": "tokenkey-prod-stage0",
    "domain": "api.tokenkey.dev",
    "label": "prod",
}


def _load_guard_module():
    """Import the sibling stability guard (hyphenated filename → importlib).

    The guard owns the single JSON-derived apply-SQL generator (generate_sql)
    and the shared shared+tier merge (effective_baseline_for_tier). Reusing them
    here keeps the tier baseline values in exactly one place: the JSON file.
    """
    path = OPS_DIR / "check-edge-oauth-stability.py"
    spec = importlib.util.spec_from_file_location("tk_edge_oauth_stability_guard", path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load guard module from {path}")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


_GUARD = _load_guard_module()

EDGE_SSM_SPEC = importlib.util.spec_from_file_location(
    "tk_edge_ssm_execution",
    REPO_ROOT / "ops/stage0/edge_ssm_execution.py",
)
if EDGE_SSM_SPEC is None or EDGE_SSM_SPEC.loader is None:
    raise RuntimeError("cannot load ops/stage0/edge_ssm_execution.py")
_EDGE_SSM = importlib.util.module_from_spec(EDGE_SSM_SPEC)
sys.modules.setdefault(EDGE_SSM_SPEC.name, _EDGE_SSM)
EDGE_SSM_SPEC.loader.exec_module(_EDGE_SSM)

ROUTING_SPEC = importlib.util.spec_from_file_location(
    "tk_edge_routing_matrix",
    REPO_ROOT / "ops/stage0/edge_routing_matrix.py",
)
if ROUTING_SPEC is None or ROUTING_SPEC.loader is None:
    raise RuntimeError("cannot load ops/stage0/edge_routing_matrix.py")
_EDGE_ROUTING = importlib.util.module_from_spec(ROUTING_SPEC)
sys.modules.setdefault(ROUTING_SPEC.name, _EDGE_ROUTING)
ROUTING_SPEC.loader.exec_module(_EDGE_ROUTING)

# Reuse the embed sentinel's JSON->effective-tier-row mapping as the single
# source for expected tier-table values. check-tier-baseline-embed.py already
# guarantees this JSON == Go embed == tk_012 migration seed (preflight gate),
# so "live tiers table vs effective_tiers_from_json(...)" == "live vs git".
TIER_EMBED_SPEC = importlib.util.spec_from_file_location(
    "tk_tier_baseline_embed",
    REPO_ROOT / "scripts/sentinels/check-tier-baseline-embed.py",
)
if TIER_EMBED_SPEC is None or TIER_EMBED_SPEC.loader is None:
    raise RuntimeError("cannot load scripts/sentinels/check-tier-baseline-embed.py")
_TIER_EMBED = importlib.util.module_from_spec(TIER_EMBED_SPEC)
sys.modules.setdefault(TIER_EMBED_SPEC.name, _TIER_EMBED)
TIER_EMBED_SPEC.loader.exec_module(_TIER_EMBED)

CONFIRM_CODE = "yes-apply-anthropic-config-cascade"

PLAN_VERSION = 4
SNAPSHOT_VERSION = 7

# After each OAuth tier-baseline apply on an edge Postgres, bump the operator
# (admin/default) user's row concurrency to match Σ anthropic account concurrency
# (all types incl. api-key) on that same DB — avoids drift when Anthropic pool sizing changes.

ADMIN_OPERATOR_USER_CONCURRENCY_SYNC_ID = 1
DEFAULT_PARALLEL_EDGES = 6


# --------------------------------------------------------------------------
# Utility
# --------------------------------------------------------------------------

def fail(msg: str, code: int = 2) -> None:
    print(f"::error::{msg}", file=sys.stderr)
    sys.exit(code)


def now_utc_iso() -> str:
    return _dt.datetime.now(_dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def load_json_file(path: pathlib.Path, what: str) -> Any:
    if not path.exists():
        fail(f"{what} not found: {path}")
    try:
        return json.loads(path.read_text())
    except json.JSONDecodeError as e:
        fail(f"{what} parse error ({path}): {e}")
    return None  # unreachable


# --------------------------------------------------------------------------
# AWS / SSM plumbing
# --------------------------------------------------------------------------

def resolve_instance_id(region: str, stack: str) -> str:
    try:
        out = subprocess.check_output(
            [
                "aws", "cloudformation", "describe-stacks",
                "--region", region,
                "--stack-name", stack,
                "--query", "Stacks[0].Outputs[?OutputKey=='InstanceId'].OutputValue",
                "--output", "text",
            ],
            text=True,
        ).strip()
    except subprocess.CalledProcessError as e:
        fail(f"describe-stacks failed for {stack}/{region}: {e}")
    if not out or out == "None":
        try:
            out = subprocess.check_output(
                [
                    "aws", "cloudformation", "describe-stack-resources",
                    "--region", region,
                    "--stack-name", stack,
                    "--query", "StackResources[?ResourceType=='AWS::EC2::Instance'].PhysicalResourceId | [0]",
                    "--output", "text",
                ],
                text=True,
            ).strip()
        except subprocess.CalledProcessError as e:
            fail(f"describe-stack-resources fallback failed for {stack}/{region}: {e}")
    if not out or out == "None":
        fail(f"no InstanceId resolvable for stack {stack}/{region}")
    return out


def ssm_run_sql(region: str, instance_id: str, sql: str, comment: str) -> tuple[str, str]:
    """Pipe SQL via SSM. Returns (stdout, command_id)."""
    remote = "sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -t -A -v ON_ERROR_STOP=1"
    command = f"set -euo pipefail\n{remote} <<'SQL'\n{sql}\nSQL"
    params = json.dumps({"commands": [command]}, ensure_ascii=False)
    try:
        cid = subprocess.check_output(
            [
                "aws", "ssm", "send-command",
                "--region", region,
                "--instance-ids", instance_id,
                "--document-name", "AWS-RunShellScript",
                "--comment", comment,
                "--parameters", params,
                "--query", "Command.CommandId",
                "--output", "text",
            ],
            text=True,
        ).strip()
    except subprocess.CalledProcessError as e:
        fail(f"ssm send-command failed ({comment}): {e}")
    subprocess.run(
        [
            "aws", "ssm", "wait", "command-executed",
            "--region", region,
            "--command-id", cid,
            "--instance-id", instance_id,
        ],
        check=False,
    )
    inv = json.loads(
        subprocess.check_output(
            [
                "aws", "ssm", "get-command-invocation",
                "--region", region,
                "--command-id", cid,
                "--instance-id", instance_id,
                "--output", "json",
            ],
            text=True,
        )
    )
    if inv.get("Status") != "Success" or inv.get("ResponseCode") != 0:
        err = (inv.get("StandardErrorContent") or "").strip()[:1200]
        out_preview = (inv.get("StandardOutputContent") or "").strip()[:600]
        fail(
            f"ssm cmd {cid} status={inv.get('Status')} rc={inv.get('ResponseCode')} ({comment})\n"
            f"  stderr: {err}\n  stdout: {out_preview}"
        )
    return (inv.get("StandardOutputContent") or "").strip(), cid


def ssm_run_sql_b64(region: str, instance_id: str, sql_b64: str, comment: str
                     ) -> tuple[str, str, bool, str]:
    """Apply path: base64-encoded SQL so embedded quotes / heredocs don't escape.

    Returns (stdout, ssm_command_id, success, stderr_preview).  cid is always
    a valid SSM CommandId, so callers can feed it to
    ``aws ssm get-command-invocation`` for after-the-fact debugging even on
    failure.
    """
    command = (
        "set -euo pipefail\n"
        f"echo {sql_b64} | base64 -d | sudo docker exec -i tokenkey-postgres "
        "psql -U tokenkey -d tokenkey -v ON_ERROR_STOP=1 -X -A -t"
    )
    params = json.dumps({"commands": [command]}, ensure_ascii=False)
    try:
        cid = subprocess.check_output(
            [
                "aws", "ssm", "send-command",
                "--region", region,
                "--instance-ids", instance_id,
                "--document-name", "AWS-RunShellScript",
                "--comment", comment,
                "--parameters", params,
                "--query", "Command.CommandId",
                "--output", "text",
            ],
            text=True,
        ).strip()
    except subprocess.CalledProcessError as e:
        fail(f"ssm send-command failed ({comment}): {e}")
    subprocess.run(
        [
            "aws", "ssm", "wait", "command-executed",
            "--region", region,
            "--command-id", cid,
            "--instance-id", instance_id,
        ],
        check=False,
    )
    inv = json.loads(
        subprocess.check_output(
            [
                "aws", "ssm", "get-command-invocation",
                "--region", region,
                "--command-id", cid,
                "--instance-id", instance_id,
                "--output", "json",
            ],
            text=True,
        )
    )
    stdout = (inv.get("StandardOutputContent") or "").strip()
    stderr = (inv.get("StandardErrorContent") or "").strip()[:1200]
    success = inv.get("Status") == "Success" and inv.get("ResponseCode") == 0
    if not success:
        stdout = stdout[:600]
    return stdout, cid, success, stderr


def ssm_run_shell(region: str, instance_id: str, shell_script: str, comment: str
                  ) -> tuple[str, str, bool, str]:
    """Run an arbitrary read/write shell script on the remote host via SSM."""
    command = f"set -euo pipefail\n{shell_script}"
    params = json.dumps({"commands": [command]}, ensure_ascii=False)
    try:
        cid = subprocess.check_output(
            [
                "aws", "ssm", "send-command",
                "--region", region,
                "--instance-ids", instance_id,
                "--document-name", "AWS-RunShellScript",
                "--comment", comment,
                "--parameters", params,
                "--query", "Command.CommandId",
                "--output", "text",
            ],
            text=True,
        ).strip()
    except subprocess.CalledProcessError as e:
        fail(f"ssm send-command failed ({comment}): {e}")
    subprocess.run(
        [
            "aws", "ssm", "wait", "command-executed",
            "--region", region,
            "--command-id", cid,
            "--instance-id", instance_id,
        ],
        check=False,
    )
    inv = json.loads(
        subprocess.check_output(
            [
                "aws", "ssm", "get-command-invocation",
                "--region", region,
                "--command-id", cid,
                "--instance-id", instance_id,
                "--output", "json",
            ],
            text=True,
        )
    )
    stdout = (inv.get("StandardOutputContent") or "").strip()
    stderr = (inv.get("StandardErrorContent") or "").strip()[:1200]
    success = inv.get("Status") == "Success" and inv.get("ResponseCode") == 0
    if not success:
        stdout = stdout[:600]
    return stdout, cid, success, stderr


def _parallel_edges_workers(requested: int | None) -> int:
    if requested is None or requested < 1:
        return DEFAULT_PARALLEL_EDGES
    return requested


def _run_parallel_ordered(
    items: list[Any],
    fn: Callable[[Any], Any],
    workers: int,
    *,
    label: str,
) -> list[Any]:
    """Run ``fn`` over ``items`` with a thread pool; return results in input order."""
    if not items:
        return []
    if workers <= 1 or len(items) == 1:
        return [fn(item) for item in items]
    workers = min(workers, len(items))
    out: list[Any | None] = [None] * len(items)
    with ThreadPoolExecutor(max_workers=workers) as pool:
        futures = {pool.submit(fn, item): idx for idx, item in enumerate(items)}
        for fut in as_completed(futures):
            idx = futures[fut]
            try:
                out[idx] = fut.result()
            except SystemExit:
                raise
            except Exception as exc:
                fail(f"{label} parallel task {idx} failed: {exc}")
    return out  # type: ignore[return-value]


# --------------------------------------------------------------------------
# Runtime sync (settings UA + Redis fingerprint cache)
# --------------------------------------------------------------------------

def _canonical_claude_code_ua_version(override: str | None = None) -> str:
    """Semver from tk_canonical_cc_oauth.json observed.user_agent (single source)."""
    if override is not None and str(override).strip():
        ver = str(override).strip()
    else:
        canon = load_json_file(CANONICAL_UA_JSON, "canonical UA json")
        ua = str(((canon.get("observed") or {}).get("user_agent") or ""))
        m = re.search(r"(\d+\.\d+\.\d+)", ua)
        if not m:
            fail(f"cannot parse semver from {CANONICAL_UA_JSON} observed.user_agent")
        ver = m.group(1)
    if not re.fullmatch(r"\d+\.\d+\.\d+", ver):
        fail(f"claude code UA version must be semver x.y.z, got {ver!r}")
    return ver


def _sql_quote_literal(value: str) -> str:
    return str(value).replace("'", "''")


def _load_http_mimicry_baseline() -> dict:
    data = load_json_file(HTTP_MIMICRY_BASELINES, "http mimicry baselines")
    for field in ("schema_version", "cc_version", "sonnet_opus", "haiku"):
        if field not in data:
            fail(f"{HTTP_MIMICRY_BASELINES} missing {field!r}")
    if int(data["schema_version"]) < 1:
        fail(f"{HTTP_MIMICRY_BASELINES} schema_version must be >= 1")
    ver = str(data["cc_version"]).strip()
    if not re.fullmatch(r"\d+\.\d+\.\d+", ver):
        fail(f"cc_version must be semver, got {ver!r}")
    for label in ("sonnet_opus", "haiku"):
        tokens = data[label]
        if not isinstance(tokens, list) or not tokens:
            fail(f"{label} must be a non-empty list in {HTTP_MIMICRY_BASELINES}")
        for tok in tokens:
            if not isinstance(tok, str) or not re.fullmatch(
                r"[a-z0-9][a-z0-9-]*[a-z0-9]|[a-z0-9]", tok.strip(),
            ):
                fail(f"invalid beta token in {label}: {tok!r}")
    return data


def _http_mimicry_manifest_json(override: dict | None = None) -> str:
    """Compact JSON for settings.claude_code_http_mimicry_manifest."""
    if override is not None:
        data = override
    else:
        baseline = _load_http_mimicry_baseline()
        data = {
            "schema_version": int(baseline["schema_version"]),
            "cc_version": str(baseline["cc_version"]).strip(),
            "sonnet_opus": list(baseline["sonnet_opus"]),
            "haiku": list(baseline["haiku"]),
        }
    return json.dumps(data, separators=(",", ":"), ensure_ascii=False)


def _settings_upsert_sql(key: str, value: str, returning: str) -> str:
    """One settings UPSERT statement with the value SQL-quoted as a literal."""
    return (
        "INSERT INTO settings (key, value, updated_at) VALUES "
        f"('{key}', '{_sql_quote_literal(value)}', NOW()) "
        "ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW() "
        f"RETURNING {returning};"
    )


def render_runtime_sync_shell(ua_version: str, mimicry_manifest_json: str | None = None) -> str:
    """Remote shell: upsert UA + HTTP mimicry manifest + DEL fingerprint:*.

    The two settings UPSERTs are delivered as base64-encoded SQL piped to psql
    over stdin — NOT inlined into ``psql -c "..."``. The mimicry manifest is
    compact JSON whose double-quotes would otherwise collide with the
    surrounding ``-c "..."`` shell double-quote and be stripped, persisting an
    invalid-JSON value (the http_ua_drift false-positive that no sync-runtime
    re-run could ever clear). Mirrors the apply path's ssm_run_sql_b64 base64
    delivery; `docker exec -i` keeps stdin open for the pipe.
    """
    ua_key = RUNTIME_SYNC_SETTING_KEY
    manifest_json = mimicry_manifest_json if mimicry_manifest_json is not None else _http_mimicry_manifest_json()
    mimicry_key = RUNTIME_SYNC_MIMICRY_SETTING_KEY
    ua_sql_b64 = base64.b64encode(
        _settings_upsert_sql(ua_key, ua_version, "key, value").encode("utf-8")
    ).decode("ascii")
    mimicry_sql_b64 = base64.b64encode(
        _settings_upsert_sql(mimicry_key, manifest_json, "key").encode("utf-8")
    ).decode("ascii")
    return (
        "# Generated by manage-anthropic-config.py sync-runtime\n"
        "set -u\n"
        "PSQL='sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1'\n"
        "RC='env -u REDISCLI_AUTH sudo docker exec tokenkey-redis redis-cli'\n"
        "echo '=== settings_upsert_ua (claude_code_user_agent_version) ==='\n"
        f"echo {ua_sql_b64} | base64 -d | $PSQL\n"
        "echo '=== settings_upsert_http_mimicry (claude_code_http_mimicry_manifest) ==='\n"
        f"echo {mimicry_sql_b64} | base64 -d | $PSQL\n"
        "echo '=== redis_fingerprint_del ==='\n"
        "ids=\"$($PSQL -c \"SELECT id FROM accounts WHERE platform='anthropic' "
        "AND type='oauth' AND deleted_at IS NULL AND schedulable=true ORDER BY id;\" "
        "| tr -d ' ' | grep -E '^[0-9]+$' || true)\"\n"
        "if [ -z \"$ids\" ]; then echo 'no oauth accounts; skip fingerprint del'; else\n"
        "  for id in $ids; do fk=\"fingerprint:${id}\"; out=\"$($RC DEL \"$fk\" 2>&1 || true)\"; "
        "echo \"DEL ${fk} => ${out}\"; done\n"
        "fi\n"
        "echo '=== settings_after ==='\n"
        f"$PSQL -c \"SELECT row_to_json(t) FROM (SELECT key, value FROM settings "
        f"WHERE key IN ('{ua_key}', '{mimicry_key}')) t;\"\n"
    )


def _resolve_runtime_sync_target(target: str) -> tuple[str, str, str]:
    """Return (label, region, instance_id) for prod or edge:<id>."""
    if target == "prod":
        region, instance_id, label = _resolve_prod_target()
        return label, region, instance_id
    if target.startswith("edge:"):
        edge_id = target.split(":", 1)[1]
        region, instance_id, label = _resolve_edge_target(edge_id)
        return label, region, instance_id
    fail(f"sync-runtime --target must be prod or edge:<id>, got {target!r}")


def _runtime_sync_targets_from_plan(plan: dict, *, include_prod: bool) -> list[str]:
    """Collect prod + edge targets touched by edge_account_tier actions."""
    targets: set[str] = set()
    if include_prod:
        targets.add("prod")
    for action in plan.get("actions") or []:
        if action.get("kind") != "edge_account_tier":
            continue
        edge_id = (action.get("target") or {}).get("edge_id")
        if edge_id:
            targets.add(f"edge:{edge_id}")
    return sorted(targets, key=lambda t: (0 if t == "prod" else 1, t))


def _deployable_edge_targets_from_snapshot(snap: dict) -> list[str]:
    out: list[str] = []
    for edge_id, edge in sorted((snap.get("edges") or {}).items()):
        if edge.get("error") or edge.get("skipped_reason"):
            continue
        if edge.get("deployable") is False:
            continue
        out.append(f"edge:{edge_id}")
    return out


def _runtime_sync_one_target(
    target: str,
    ua_version: str,
    job_dir: pathlib.Path | None,
    *,
    mimicry_manifest_json: str | None,
) -> dict[str, Any]:
    label, region, instance_id = _resolve_runtime_sync_target(target)
    shell = render_runtime_sync_shell(ua_version, mimicry_manifest_json)
    if job_dir is not None:
        safe = target.replace(":", "-")
        (job_dir / f"sync-runtime-{safe}.sh").write_text(shell)
    stdout, cid, ok, stderr = ssm_run_shell(
        region, instance_id, shell,
        f"sync-runtime {label} ua={ua_version}",
    )
    if not ok:
        print(f"sync-runtime: FAILED on {label} cid={cid}", file=sys.stderr)
    return {
        "target": target,
        "target_label": label,
        "ssm_command_id": cid,
        "ok": ok,
        "stdout_preview": stdout[-1200:],
        "stderr_preview": stderr,
    }


def _run_runtime_sync(
    targets: list[str],
    ua_version: str,
    job_dir: pathlib.Path | None,
    *,
    mimicry_manifest_json: str | None = None,
    parallel_edges: int | None = None,
) -> tuple[bool, list[dict]]:
    if not targets:
        return True, []
    workers = _parallel_edges_workers(parallel_edges)
    print(f"sync-runtime: {len(targets)} target(s) parallel_workers={workers}", file=sys.stderr)

    def _work(target: str) -> dict[str, Any]:
        return _runtime_sync_one_target(
            target, ua_version, job_dir, mimicry_manifest_json=mimicry_manifest_json,
        )

    results = _run_parallel_ordered(targets, _work, workers, label="sync-runtime")
    ok_all = all(r.get("ok") for r in results)
    return ok_all, results


def cmd_plan_http_mimicry_sync(args: argparse.Namespace) -> int:
    """Emit an audit plan for HTTP mimicry runtime sync (apply via sync-runtime)."""
    baseline = _load_http_mimicry_baseline()
    manifest = {
        "schema_version": int(baseline["schema_version"]),
        "cc_version": str(baseline["cc_version"]).strip(),
        "sonnet_opus": list(baseline["sonnet_opus"]),
        "haiku": list(baseline["haiku"]),
    }
    plan = {
        "version": PLAN_VERSION,
        "planned_at": now_utc_iso(),
        "intent": {
            "kind": "http_mimicry_runtime_sync",
            "source": str(HTTP_MIMICRY_BASELINES.relative_to(REPO_ROOT)),
            "cc_version": manifest["cc_version"],
            "apply_via": "sync-runtime",
        },
        "noop": False,
        "actions": [{
            "kind": "http_mimicry_runtime_sync",
            "target": {"scope": "all-deployable-and-prod"},
            "expected_after": manifest,
        }],
    }
    if args.out:
        pathlib.Path(args.out).write_text(json.dumps(plan, indent=2, ensure_ascii=False))
    else:
        print(json.dumps(plan, indent=2, ensure_ascii=False))
    return 0


def cmd_sync_runtime(args: argparse.Namespace) -> int:
    ua_version = _canonical_claude_code_ua_version(getattr(args, "ua_version", None))
    if args.target == "all-deployable-and-prod":
        if not args.snapshot:
            fail("sync-runtime --target all-deployable-and-prod requires --snapshot")
        snap = _load_snapshot_or_die(args.snapshot)
        targets = ["prod"] + _deployable_edge_targets_from_snapshot(snap)
    else:
        targets = [args.target]
    job_dir = pathlib.Path(args.job_dir) if args.job_dir else None
    if job_dir:
        job_dir.mkdir(parents=True, exist_ok=True)
    manifest_json = _http_mimicry_manifest_json()
    ok, results = _run_runtime_sync(
        targets,
        ua_version,
        job_dir,
        mimicry_manifest_json=manifest_json,
        parallel_edges=getattr(args, "parallel_edges", None),
    )
    report = {
        "version": 1,
        "synced_at": now_utc_iso(),
        "ua_version": ua_version,
        "http_mimicry_manifest": json.loads(manifest_json),
        "targets": targets,
        "success": ok,
        "results": results,
    }
    if args.out:
        pathlib.Path(args.out).write_text(json.dumps(report, indent=2, ensure_ascii=False))
    if args.json:
        print(json.dumps(report, indent=2, ensure_ascii=False))
    else:
        cc_ver = json.loads(manifest_json).get("cc_version", "?")
        print(
            f"sync-runtime: success={ok} targets={len(targets)} "
            f"ua={ua_version} cc={cc_ver}",
        )
        for r in results:
            tag = "OK" if r["ok"] else "FAIL"
            print(f"  [{tag}] {r['target_label']}  cid={r['ssm_command_id']}")
    return 0 if ok else 1


# --------------------------------------------------------------------------
# Stage 1 — snapshot
# --------------------------------------------------------------------------

EDGE_ACCOUNTS_SQL = """
SELECT COALESCE(jsonb_agg(jsonb_build_object(
  'id', a.id, 'name', a.name, 'platform', a.platform, 'type', a.type,
  'status', a.status, 'schedulable', a.schedulable,
  'concurrency', a.concurrency, 'priority', a.priority,
  'channel_type', a.channel_type, 'rate_multiplier', a.rate_multiplier,
  'auto_pause_on_expired', a.auto_pause_on_expired,
  'stability_tier', a.extra->>'stability_tier',
  'rpm_strategy', a.extra->>'rpm_strategy',
  'base_rpm', NULLIF(a.extra->>'base_rpm', '')::int,
  'rpm_sticky_buffer', NULLIF(a.extra->>'rpm_sticky_buffer', '')::int,
  'max_sessions', NULLIF(a.extra->>'max_sessions', '')::int,
  'session_idle_timeout_minutes', NULLIF(a.extra->>'session_idle_timeout_minutes', '')::int,
  'window_cost_limit', NULLIF(a.extra->>'window_cost_limit', '')::int,
  'window_cost_sticky_reserve', NULLIF(a.extra->>'window_cost_sticky_reserve', '')::int,
  'cache_ttl_override_enabled', NULLIF(a.extra->>'cache_ttl_override_enabled', '')::boolean
) ORDER BY a.id), '[]'::jsonb)
FROM accounts a
WHERE a.platform = 'anthropic'
  AND a.type = 'oauth'
  AND a.deleted_at IS NULL;
"""

# Prod stubs: anthropic api-key accounts whose credentials.base_url points at
# a tokenkey edge domain. snapshot pulls them so plan-stub-pool can match by
# regex without re-querying. We deliberately include every anthropic api-key
# stub (not just base_url-matching ones); plan-stub-pool filters in Python
# using the JSON-driven policy regex — this keeps the SQL stable when we
# evolve which patterns are in scope.
PROD_STUBS_SQL = """
SELECT COALESCE(jsonb_agg(jsonb_build_object(
  'id', a.id, 'name', a.name, 'platform', a.platform, 'type', a.type,
  'status', a.status, 'schedulable', a.schedulable,
  'concurrency', a.concurrency, 'priority', a.priority,
  'cred_base_url',              a.credentials->>'base_url',
  'cred_pool_mode',             NULLIF(a.credentials->>'pool_mode', '')::boolean,
  'cred_pool_mode_retry_count', NULLIF(a.credentials->>'pool_mode_retry_count', '')::int
) ORDER BY a.id), '[]'::jsonb)
FROM accounts a
WHERE a.platform = 'anthropic'
  AND a.type = 'apikey'
  AND a.deleted_at IS NULL;
"""

# Surface-C inputs, identical on every target (edge or prod): the operator
# (users.id=1) row concurrency, and the live Σ schedulable anthropic concurrency
# the scheduler actually sees. Both numbers are computed authoritatively in SQL
# (never re-summed in Python) so the planner trusts one source. The sum mirrors
# render_admin_operator_concurrency_sync_sql exactly (same platform + schedulable
# + deleted_at predicate) — keep them in lockstep.
ANTHROPIC_GROUPS_SQL = """
SELECT COALESCE(jsonb_agg(jsonb_build_object(
  'id', g.id, 'name', g.name, 'platform', g.platform,
  'status', g.status, 'claude_code_only', g.claude_code_only,
  'fallback_group_id', g.fallback_group_id
) ORDER BY g.id), '[]'::jsonb)
FROM groups g
WHERE g.platform = 'anthropic'
  AND g.deleted_at IS NULL;
"""

# tiers reference table (PR #472): the single per-node source for tier strategy
# values (base_rpm / max_sessions / ...). Seeded from the Go embed baseline on
# startup (ensureSeededFromBaseline, git->DB), admin-editable via PUT
# /api/v1/admin/tiers/:id. snapshot pulls it so the tier_table_drift check can
# compare live rows vs the git baseline without re-querying. Column names mirror
# the tiers table / check-tier-baseline-embed.effective_tiers_from_json.
TIERS_SQL = """
SELECT COALESCE(jsonb_agg(jsonb_build_object(
  'name', t.name,
  'concurrency', t.concurrency,
  'priority', t.priority,
  'rate_multiplier', t.rate_multiplier,
  'base_rpm', t.base_rpm,
  'max_sessions', t.max_sessions,
  'rpm_sticky_buffer', t.rpm_sticky_buffer,
  'session_idle_timeout_minutes', t.session_idle_timeout_minutes,
  'window_cost_limit', t.window_cost_limit,
  'window_cost_sticky_reserve', t.window_cost_sticky_reserve,
  'cache_ttl_override_enabled', t.cache_ttl_override_enabled,
  'cache_ttl_override_target', t.cache_ttl_override_target,
  'tls_profile_name', t.tls_profile_name
) ORDER BY t.name), '[]'::jsonb)
FROM tiers t;
"""


def _read_tiers(region: str, instance_id: str, label: str) -> list[dict]:
    """Pull the live `tiers` reference-table rows for one node (edge or prod)."""
    raw, _ = ssm_run_sql(region, instance_id, TIERS_SQL, f"snapshot: {label} tiers table")
    try:
        return json.loads(raw) if raw else []
    except json.JSONDecodeError:
        return []


OPERATOR_CONCURRENCY_SQL = f"""
SELECT jsonb_build_object(
  'operator_user_concurrency',
    (SELECT concurrency FROM users
      WHERE id = {ADMIN_OPERATOR_USER_CONCURRENCY_SYNC_ID} AND deleted_at IS NULL),
  'schedulable_concurrency_sum',
    (SELECT COALESCE(SUM(a.concurrency), 0)::int FROM accounts a
      WHERE a.platform = 'anthropic'
        AND a.schedulable = true
        AND a.deleted_at IS NULL)
);
"""


def _read_operator_concurrency(region: str, instance_id: str, label: str) -> dict:
    """Pull operator_user_concurrency + schedulable_concurrency_sum for one target.
    Both are SQL-authoritative ints; missing/parse failures degrade to None so the
    planner can fail-loud rather than silently treat them as 0."""
    raw, _ = ssm_run_sql(region, instance_id, OPERATOR_CONCURRENCY_SQL,
                         f"snapshot: {label} operator concurrency")
    try:
        obj = json.loads(raw) if raw else {}
    except json.JSONDecodeError:
        obj = {}
    return {
        "operator_user_concurrency": obj.get("operator_user_concurrency"),
        "schedulable_concurrency_sum": obj.get("schedulable_concurrency_sum"),
    }


OPERATOR_BALANCE_SQL = f"""
SELECT jsonb_build_object(
  'operator_user_balance',
    (SELECT balance::float8 FROM users
      WHERE id = {ADMIN_OPERATOR_USER_CONCURRENCY_SYNC_ID} AND deleted_at IS NULL),
  'operator_user_exists',
    EXISTS(SELECT 1 FROM users WHERE id = {ADMIN_OPERATOR_USER_CONCURRENCY_SYNC_ID}
      AND deleted_at IS NULL)
);
"""


def _read_operator_balance(region: str, instance_id: str, label: str) -> dict:
    """Pull users.id=1 balance for edge operator balance surface (E)."""
    raw, _ = ssm_run_sql(region, instance_id, OPERATOR_BALANCE_SQL,
                         f"snapshot: {label} operator balance")
    try:
        obj = json.loads(raw) if raw else {}
    except json.JSONDecodeError:
        obj = {}
    bal = obj.get("operator_user_balance")
    if bal is not None:
        try:
            bal = float(bal)
        except (TypeError, ValueError):
            bal = None
    return {
        "operator_user_balance": bal,
        "operator_user_exists": bool(obj.get("operator_user_exists")),
    }


def _sql_as_subquery(sql: str) -> str:
    """Normalize a standalone ``SELECT … ;`` statement into a scalar-subquery body.

    The leading ``SELECT`` is kept (so the fragment is a valid subquery once
    wrapped in ``(...)`` inside ``jsonb_build_object``) and the trailing
    statement terminator is dropped — a ``;`` inside ``(...)`` is a syntax error.
    """
    body = sql.strip()
    if body.endswith(";"):
        body = body[:-1].rstrip()
    return body


EDGE_CAPTURE_BUNDLE_SQL = f"""
SELECT jsonb_build_object(
  'oauth_accounts', ({_sql_as_subquery(EDGE_ACCOUNTS_SQL)}),
  'anthropic_groups', ({_sql_as_subquery(ANTHROPIC_GROUPS_SQL)}),
  'tiers', ({_sql_as_subquery(TIERS_SQL)}),
  'operator_concurrency', ({_sql_as_subquery(OPERATOR_CONCURRENCY_SQL)}),
  'operator_balance', ({_sql_as_subquery(OPERATOR_BALANCE_SQL)})
);
""".strip()

PROD_CAPTURE_BUNDLE_SQL = f"""
SELECT jsonb_build_object(
  'anthropic_stubs', ({_sql_as_subquery(PROD_STUBS_SQL)}),
  'anthropic_groups', ({_sql_as_subquery(ANTHROPIC_GROUPS_SQL)}),
  'tiers', ({_sql_as_subquery(TIERS_SQL)}),
  'operator_concurrency', ({_sql_as_subquery(OPERATOR_CONCURRENCY_SQL)})
);
""".strip()


def _parse_operator_balance_obj(obj: dict) -> dict:
    bal = obj.get("operator_user_balance")
    if bal is not None:
        try:
            bal = float(bal)
        except (TypeError, ValueError):
            bal = None
    return {
        "operator_user_balance": bal,
        "operator_user_exists": bool(obj.get("operator_user_exists")),
    }


def _capture_edge_bundle(
    eid: str,
    ident: Any,
    *,
    ec2_stack: str,
) -> dict[str, Any]:
    """One SSM round-trip per edge for snapshot fields."""
    raw, _ = ssm_run_sql(
        ident.region,
        ident.instance_id,
        EDGE_CAPTURE_BUNDLE_SQL,
        f"snapshot bundle: edge {eid}",
    )
    try:
        bundle = json.loads(raw) if raw else {}
    except json.JSONDecodeError:
        bundle = {}
    op = bundle.get("operator_concurrency") or {}
    bal = _parse_operator_balance_obj(bundle.get("operator_balance") or {})
    tiers_raw = bundle.get("tiers")
    return {
        "deployable": True,
        "instance_id": ident.instance_id,
        "region": ident.region,
        "stack": ident.ec2_stack or ec2_stack or "",
        "domain": ident.domain,
        "ssm_routing": ident.routing,
        "oauth_accounts": bundle.get("oauth_accounts") or [],
        "anthropic_groups": bundle.get("anthropic_groups") or [],
        "tiers": tiers_raw if isinstance(tiers_raw, list) else [],
        "operator_user_concurrency": op.get("operator_user_concurrency"),
        "schedulable_concurrency_sum": op.get("schedulable_concurrency_sum"),
        "operator_user_balance": bal["operator_user_balance"],
        "operator_user_exists": bal["operator_user_exists"],
    }


def _capture_prod_bundle(region: str, prod_inst: str) -> dict[str, Any]:
    raw, _ = ssm_run_sql(
        region,
        prod_inst,
        PROD_CAPTURE_BUNDLE_SQL,
        "snapshot bundle: prod",
    )
    try:
        bundle = json.loads(raw) if raw else {}
    except json.JSONDecodeError:
        bundle = {}
    op = bundle.get("operator_concurrency") or {}
    tiers_raw = bundle.get("tiers")
    return {
        "instance_id": prod_inst,
        "region": region,
        "stack": PROD_TARGET["stack"],
        "domain": PROD_TARGET["domain"],
        "anthropic_stubs": bundle.get("anthropic_stubs") or [],
        "anthropic_groups": bundle.get("anthropic_groups") or [],
        "tiers": tiers_raw if isinstance(tiers_raw, list) else [],
        "operator_user_concurrency": op.get("operator_user_concurrency"),
        "schedulable_concurrency_sum": op.get("schedulable_concurrency_sum"),
    }


def _snapshot_edge_task(task: tuple[str, dict | None, dict | None, bool, bool]) -> tuple[str, dict]:
    """Worker for parallel snapshot: (eid, ec2_t, ls_t, deploy, allow_planned)."""
    eid, ec2_t, ls_t, deploy, allow_planned = task
    region = (ec2_t or {}).get("region") or (ls_t or {}).get("lightsail_region")
    stack = (ec2_t or {}).get("stack") or ""
    if not deploy and not allow_planned:
        return eid, {
            "deployable": False,
            "skipped_reason": f"edge {eid} is planned; pass --allow-planned to include",
            "region": region,
            "stack": stack,
        }
    if not deploy and allow_planned:
        return eid, {
            "deployable": False,
            "skipped_reason": f"edge {eid} is planned (--allow-planned)",
            "region": region,
            "stack": stack,
        }
    try:
        ident = _EDGE_SSM.resolve_edge_execution_identity(REPO_ROOT, eid)
    except SystemExit:
        return eid, {"error": f"could not resolve SSM instance for edge {eid}"}
    print(
        f"snapshot: edge {eid} instance={ident.instance_id} routing={ident.routing} (bundle)",
        file=sys.stderr,
    )
    return eid, _capture_edge_bundle(eid, ident, ec2_stack=stack)


def cmd_snapshot(args: argparse.Namespace) -> int:
    edge_matrix = load_json_file(EDGE_MATRIX, "edge matrix")
    ls_targets = _EDGE_ROUTING.load_lightsail_targets(REPO_ROOT)
    ec2_targets = edge_matrix.get("targets") or {}

    edges: dict[str, dict] = {}
    merged = _EDGE_ROUTING.merged_edge_ids(edge_matrix, ls_targets)
    workers = _parallel_edges_workers(getattr(args, "parallel_edges", None))
    tasks: list[tuple[str, dict | None, dict | None, bool, bool]] = []
    for eid in merged:
        ec2_t = ec2_targets.get(eid)
        ls_t = ls_targets.get(eid)
        deploy = _EDGE_ROUTING.edge_effective_deployable(ec2_t, ls_t)
        tasks.append((eid, ec2_t, ls_t, deploy, bool(args.allow_planned)))
    if tasks:
        print(f"snapshot: {len(tasks)} edge(s) parallel_workers={workers}", file=sys.stderr)
        for eid, edge in _run_parallel_ordered(
            tasks, _snapshot_edge_task, workers, label="snapshot",
        ):
            edges[eid] = edge

    prod_view: dict[str, Any]
    if getattr(args, "skip_prod", False):
        prod_view = {"skipped_reason": "--skip-prod passed"}
    else:
        try:
            prod_inst = resolve_instance_id(PROD_TARGET["region"], PROD_TARGET["stack"])
            print(f"snapshot: prod instance={prod_inst} (bundle)", file=sys.stderr)
            prod_view = _capture_prod_bundle(PROD_TARGET["region"], prod_inst)
        except SystemExit:
            print("snapshot: prod failed to resolve instance (skipping prod view; "
                  "plan-stub-pool will fail-loud if invoked)", file=sys.stderr)
            prod_view = {"error": "could not resolve instance for prod"}

    snapshot = {
        "version": SNAPSHOT_VERSION,
        "captured_at": now_utc_iso(),
        "edges": edges,
        "prod": prod_view,
    }

    out_str = json.dumps(snapshot, indent=2, ensure_ascii=False)
    if args.out:
        pathlib.Path(args.out).write_text(out_str)
        print(f"snapshot: written {args.out} ({len(out_str)} bytes)", file=sys.stderr)
    else:
        print(out_str)
    return 0


# --------------------------------------------------------------------------
# Stage 2 — check
# --------------------------------------------------------------------------

def _run_guard(argv: list[str], description: str) -> dict[str, Any]:
    """Run a sub-guard with --json and return parsed dict + exit code."""
    try:
        proc = subprocess.run(argv, capture_output=True, text=True, check=False)
    except FileNotFoundError as e:
        return {"error": str(e), "exit_code": 127, "raw_stdout": "", "raw_stderr": ""}
    stdout = proc.stdout.strip()
    stderr = proc.stderr.strip()
    out: dict[str, Any] = {"exit_code": proc.returncode, "description": description}
    if stdout:
        try:
            out["report"] = json.loads(stdout)
        except json.JSONDecodeError:
            out["raw_stdout"] = stdout[:2000]
    if stderr:
        out["raw_stderr"] = stderr[:2000]
    return out


def _load_operator_balance_policy() -> dict:
    """Parse anthropic-edge-operator-balance-baselines.json."""
    raw = load_json_file(EDGE_OPERATOR_BALANCE_BASELINES, "edge operator balance baselines")
    if not isinstance(raw, dict):
        fail("edge operator balance baselines: top-level must be an object")
    if raw.get("schema_version") != 1:
        fail(f"edge operator balance baselines: schema_version {raw.get('schema_version')!r} != 1")
    pol = raw.get("policy")
    if not isinstance(pol, dict):
        fail("edge operator balance baselines: policy must be an object")
    uid = int(pol.get("operator_user_id", ADMIN_OPERATOR_USER_CONCURRENCY_SYNC_ID))
    if uid != ADMIN_OPERATOR_USER_CONCURRENCY_SYNC_ID:
        fail(f"edge operator balance baselines: operator_user_id must be {ADMIN_OPERATOR_USER_CONCURRENCY_SYNC_ID}")
    try:
        threshold = float(pol["min_balance_threshold"])
        default_bal = float(pol["default_balance"])
    except (KeyError, TypeError, ValueError) as e:
        fail(f"edge operator balance baselines: invalid policy numbers: {e}")
    if threshold < 0 or default_bal < threshold:
        fail("edge operator balance baselines: default_balance must be >= min_balance_threshold")
    return {
        "operator_user_id": uid,
        "min_balance_threshold": threshold,
        "default_balance": default_bal,
    }


def _operator_balance_needs_top_up(live_balance: float | None, *, threshold: float) -> bool:
    if live_balance is None:
        return True
    return float(live_balance) < float(threshold)


def _balance_violations_from_snapshot(snap: dict, policy: dict) -> list[dict]:
    """Surface E check: deployable edges whose operator balance is below threshold."""
    threshold = policy["min_balance_threshold"]
    out: list[dict] = []
    for edge_id, edge in sorted(snap.get("edges", {}).items()):
        if edge.get("error") or edge.get("skipped_reason") or not edge.get("deployable"):
            continue
        if edge.get("operator_user_exists") is False:
            out.append({
                "edge_id": edge_id,
                "status": "error",
                "reason": f"users.id={policy['operator_user_id']} missing",
            })
            continue
        bal = edge.get("operator_user_balance")
        if _operator_balance_needs_top_up(bal, threshold=threshold):
            out.append({
                "edge_id": edge_id,
                "status": "violation",
                "operator_user_balance": bal,
                "min_balance_threshold": threshold,
                "default_balance": policy["default_balance"],
            })
    return out


def _run_balance_checks_live(edge_ids: list[str], policy: dict) -> list[dict]:
    """Live SSM balance read per edge (used when check runs without a snapshot)."""
    out: list[dict] = []
    threshold = policy["min_balance_threshold"]
    for eid in edge_ids:
        try:
            ident = _EDGE_SSM.resolve_edge_execution_identity(REPO_ROOT, eid)
        except SystemExit:
            out.append({"edge_id": eid, "status": "error", "reason": "could not resolve SSM instance"})
            continue
        live = _read_operator_balance(ident.region, ident.instance_id, f"edge {eid}")
        if not live.get("operator_user_exists"):
            out.append({
                "edge_id": eid,
                "status": "error",
                "reason": f"users.id={policy['operator_user_id']} missing",
            })
            continue
        bal = live.get("operator_user_balance")
        if _operator_balance_needs_top_up(bal, threshold=threshold):
            out.append({
                "edge_id": eid,
                "status": "violation",
                "operator_user_balance": bal,
                "min_balance_threshold": threshold,
                "default_balance": policy["default_balance"],
            })
    return out


# --------------------------------------------------------------------------
# tier_table_drift: live `tiers` reference table vs git baseline
# --------------------------------------------------------------------------
# Post-#472 the per-tier strategy values (base_rpm / max_sessions / ...) live in
# the per-node `tiers` table, seeded from the Go embed baseline on startup and
# overlaid onto accounts at read time. The table is admin-editable
# (PUT /api/v1/admin/tiers/:id), and such edits go live fleet-wide via pub/sub
# but are silently reverted on the next restart/deploy by ensureSeededFromBaseline.
# This surface REVEALS such backend edits (live tier row != git) and WARNS that a
# deploy will revert them. We compare exactly the keys OverlayExtra writes
# (_GUARD.TIER_MANAGED_EXTRA_KEYS) — the runtime-authoritative tier strategy —
# against effective_tiers_from_json (the same JSON the embed sentinel pins to the
# Go embed + tk_012 migration seed, so expected == git).

TIER_TABLE_COMPARE_KEYS = sorted(_GUARD.TIER_MANAGED_EXTRA_KEYS)


def _tier_val_eq(expected: Any, actual: Any) -> bool:
    """Numeric-aware equality (28 == 28.0); exact otherwise (bools/strings)."""
    if isinstance(expected, bool) or isinstance(actual, bool):
        return expected == actual
    try:
        return float(expected) == float(actual)
    except (TypeError, ValueError):
        return expected == actual


def _load_expected_tiers() -> dict[str, dict]:
    """Effective per-tier rows from the git baseline JSON (single source, anchored
    to Go embed + migration by scripts/sentinels/check-tier-baseline-embed.py)."""
    doc = load_json_file(TIER_BASELINES, "tier baseline")
    return _TIER_EMBED.effective_tiers_from_json(doc)


def _tier_table_drift_items(node_label: str, live_tiers: list[dict] | None,
                            expected_by_tier: dict[str, dict]) -> list[dict]:
    """Diff one node's live tiers rows vs the git baseline. Returns drift/missing/
    extra items, each carrying a human warning. Empty list == node matches git."""
    items: list[dict] = []
    live_by_name = {t.get("name"): t for t in (live_tiers or []) if isinstance(t, dict)}
    for tier_name, exp in sorted(expected_by_tier.items()):
        live = live_by_name.get(tier_name)
        if live is None:
            items.append({
                "node": node_label, "tier": tier_name, "status": "missing",
                "warning": (f"tier {tier_name} on {node_label} is missing from the live "
                            f"tiers table; the git baseline defines it — a fresh seed "
                            f"(restart/deploy) will recreate it"),
            })
            continue
        diffs = [
            {"path": f"/{k}", "expected": exp.get(k), "actual": live.get(k)}
            for k in TIER_TABLE_COMPARE_KEYS
            if not _tier_val_eq(exp.get(k), live.get(k))
        ]
        if diffs:
            summary = ", ".join(f"{d['path'].lstrip('/')} {d['expected']}->{d['actual']}" for d in diffs)
            items.append({
                "node": node_label, "tier": tier_name, "status": "drift", "diffs": diffs,
                "warning": (f"tier {tier_name} on {node_label} modified via backend admin "
                            f"({summary}); diverges from git single-source; will be reverted "
                            f"on next restart/deploy (ensureSeededFromBaseline)"),
            })
    for tier_name in sorted(set(live_by_name) - set(expected_by_tier)):
        items.append({
            "node": node_label, "tier": tier_name, "status": "extra",
            "warning": (f"tier {tier_name} on {node_label} exists in the live tiers table "
                        f"but not in git baseline; created via backend admin; it will not "
                        f"survive a fresh seed"),
        })
    return items


def _tier_table_drift_from_snapshot(snap: dict, expected_by_tier: dict[str, dict]) -> list[dict]:
    items: list[dict] = []
    for edge_id, edge in sorted(snap.get("edges", {}).items()):
        if edge.get("error") or edge.get("skipped_reason") or not edge.get("deployable"):
            continue
        items.extend(_tier_table_drift_items(f"edge:{edge_id}", edge.get("tiers"), expected_by_tier))
    prod = snap.get("prod") or {}
    if not prod.get("error") and not prod.get("skipped_reason"):
        items.extend(_tier_table_drift_items("prod", prod.get("tiers"), expected_by_tier))
    return items


def _run_tier_table_checks_live(edge_ids: list[str], expected_by_tier: dict[str, dict]) -> list[dict]:
    """Live SSM tiers-table read per node (used when check runs without a snapshot)."""
    items: list[dict] = []
    for eid in edge_ids:
        try:
            ident = _EDGE_SSM.resolve_edge_execution_identity(REPO_ROOT, eid)
        except SystemExit:
            items.append({"node": f"edge:{eid}", "tier": "*", "status": "error",
                          "warning": f"could not resolve SSM instance for edge {eid}"})
            continue
        live = _read_tiers(ident.region, ident.instance_id, f"edge {eid}")
        items.extend(_tier_table_drift_items(f"edge:{eid}", live, expected_by_tier))
    try:
        prod_inst = resolve_instance_id(PROD_TARGET["region"], PROD_TARGET["stack"])
        prod_live = _read_tiers(PROD_TARGET["region"], prod_inst, "prod")
        items.extend(_tier_table_drift_items("prod", prod_live, expected_by_tier))
    except SystemExit:
        items.append({"node": "prod", "tier": "*", "status": "error",
                      "warning": "could not resolve SSM instance for prod"})
    return items


# --------------------------------------------------------------------------
# http_ua_drift: live settings UA / mimicry manifest vs baseline JSON
# --------------------------------------------------------------------------
# `sync-runtime` pushes settings.claude_code_user_agent_version +
# settings.claude_code_http_mimicry_manifest to each node; nothing READ them
# back, so `check` could stay all-green while the fleet's live UA was a stale
# cc version (observed 2026-06: fleet on 2.1.158 while baseline was 2.1.159,
# check green throughout). This surface reads the same two setting keys live
# and diffs them against the single-source baseline JSON, so a fleet that has
# not been sync-runtime'd after a cc bump now fails check. Always reads LIVE
# (even with --snapshot): UA is a deploy-level runtime knob updated out of band
# from snapshot, and a stale snapshot is exactly how the blind spot hid.

# Mirror render_runtime_sync_shell's settings read (row_to_json beside values,
# no column-number reads). One row per present key.
SETTINGS_UA_SQL = f"""
SELECT COALESCE(jsonb_object_agg(key, value), '{{}}'::jsonb)
FROM (
  SELECT key, value FROM settings
  WHERE key IN ('{RUNTIME_SYNC_SETTING_KEY}', '{RUNTIME_SYNC_MIMICRY_SETTING_KEY}')
) t;
""".strip()


def _http_ua_drift_items(node_label: str, live_settings: dict, expected: dict) -> list[dict]:
    """Diff one node's live UA setting + mimicry manifest vs the baseline JSON.
    Returns drift items (status=drift), each with the offending field. Empty == match."""
    items: list[dict] = []
    exp_ver = str(expected["cc_version"]).strip()

    live_ua = live_settings.get(RUNTIME_SYNC_SETTING_KEY)
    if live_ua is None:
        items.append({
            "node": node_label, "field": RUNTIME_SYNC_SETTING_KEY, "status": "drift",
            "expected": exp_ver, "actual": None,
            "warning": (f"{node_label}: settings.{RUNTIME_SYNC_SETTING_KEY} unset; "
                        f"run sync-runtime (expected {exp_ver})"),
        })
    elif str(live_ua).strip() != exp_ver:
        items.append({
            "node": node_label, "field": RUNTIME_SYNC_SETTING_KEY, "status": "drift",
            "expected": exp_ver, "actual": str(live_ua).strip(),
            "warning": (f"{node_label}: live UA {str(live_ua).strip()} != baseline {exp_ver}; "
                        f"run sync-runtime"),
        })

    # Manifest is stored as a JSON string in settings.value; parse and compare
    # the version-relevant fields (cc_version + the two beta lists).
    live_manifest_raw = live_settings.get(RUNTIME_SYNC_MIMICRY_SETTING_KEY)
    exp_manifest = {
        "cc_version": exp_ver,
        "sonnet_opus": list(expected["sonnet_opus"]),
        "haiku": list(expected["haiku"]),
    }
    if live_manifest_raw is None:
        items.append({
            "node": node_label, "field": RUNTIME_SYNC_MIMICRY_SETTING_KEY, "status": "drift",
            "expected": exp_manifest, "actual": None,
            "warning": (f"{node_label}: settings.{RUNTIME_SYNC_MIMICRY_SETTING_KEY} unset; "
                        f"run sync-runtime"),
        })
    else:
        try:
            live_manifest = (json.loads(live_manifest_raw)
                             if isinstance(live_manifest_raw, str) else live_manifest_raw)
        except (json.JSONDecodeError, TypeError):
            live_manifest = None
        if not isinstance(live_manifest, dict):
            items.append({
                "node": node_label, "field": RUNTIME_SYNC_MIMICRY_SETTING_KEY, "status": "drift",
                "expected": exp_manifest, "actual": str(live_manifest_raw)[:120],
                "warning": (f"{node_label}: {RUNTIME_SYNC_MIMICRY_SETTING_KEY} not valid JSON object; "
                            f"run sync-runtime"),
            })
        else:
            mismatched = [
                k for k in ("cc_version", "sonnet_opus", "haiku")
                if live_manifest.get(k) != exp_manifest[k]
            ]
            if mismatched:
                items.append({
                    "node": node_label, "field": RUNTIME_SYNC_MIMICRY_SETTING_KEY, "status": "drift",
                    "expected": {k: exp_manifest[k] for k in mismatched},
                    "actual": {k: live_manifest.get(k) for k in mismatched},
                    "warning": (f"{node_label}: mimicry manifest drift ({', '.join(mismatched)}); "
                                f"run sync-runtime"),
                })
    return items


# --------------------------------------------------------------------------
# redis_cache_drift: Redis cached config blobs vs DB authoritative tables
# --------------------------------------------------------------------------
# tls_fingerprint_profiles and tiers are each cached in Redis as ONE serialized
# blob (backend/internal/repository/tls_fingerprint_profile_cache.go key
# "tls_fingerprint_profiles"; tier_cache.go key "tiers"; both TTL 24h, both with
# a pub/sub invalidation channel). The runtime reads the blob, not the table:
# ResolveTLSProfile serves whatever the cache holds. If a row is written to the
# DB table without the matching cache Invalidate()/NotifyUpdate() (a bare SQL
# INSERT/UPDATE, or a refactor that drops the invalidation call), the cache goes
# stale and the gateway silently falls back to the built-in default ClientHello
# -- DB-correct, runtime-wrong. `check` and the OAuth stability guard both read
# DB only, so this exact failure mode (the observed tls_profile_cache silent
# fallback) was invisible to every drift surface. This one reads the Redis blob
# AND the authoritative DB table live per node and flags: a profile/tier present
# in one but not the other (key-set), a name mismatch for a shared id, or a DB
# row whose updated_at is newer than the cached copy (stale). A COLD cache (key
# absent) is NOT drift: read-through repopulates from DB on the next access.
# Always live (even with --snapshot): Redis state is not captured by snapshot,
# and a stale snapshot is exactly how the blind spot would hide.

REDIS_DRIFT_STALE_TOLERANCE_S = 2.0  # absorb sub-second Go-JSON vs PG to_char jitter
REDIS_DRIFT_TLS_CACHE_KEY = "tls_fingerprint_profiles"
REDIS_DRIFT_TIERS_CACHE_KEY = "tiers"
REDIS_DRIFT_CACHE_NAMES = [REDIS_DRIFT_TLS_CACHE_KEY, REDIS_DRIFT_TIERS_CACHE_KEY]

# Authoritative DB reads. updated_at rendered as UTC ISO so it lines up with the
# Go time.Time JSON in the cached blob; jsonb_agg keeps field names beside values.
REDIS_DRIFT_TLS_DB_SQL = """
SELECT COALESCE(jsonb_agg(jsonb_build_object(
  'id', id,
  'name', name,
  'updated_at', to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
) ORDER BY id), '[]'::jsonb)
FROM tls_fingerprint_profiles;
""".strip()

REDIS_DRIFT_TIERS_DB_SQL = """
SELECT COALESCE(jsonb_agg(jsonb_build_object(
  'name', name,
  'updated_at', to_char(updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
) ORDER BY name), '[]'::jsonb)
FROM tiers;
""".strip()

# One psql round-trip per node gathers every DB-side live-drift input -- the UA /
# mimicry settings (http_ua_drift) AND the authoritative tls/tier tables
# (redis_cache_drift) -- as a single jsonb object, mirroring EDGE_CAPTURE_BUNDLE_SQL.
# Folding these into one query (and one SSM shell, see render_live_node_probe_shell)
# is the perf lever: http_ua_drift and redis_cache_drift used to each open their
# own per-node SSM wave (~7-8s/node round-trip each), so a check paid two full
# fleet-wide SSM passes for data that lives on the same box.
LIVE_NODE_DB_BUNDLE_SQL = f"""
SELECT jsonb_build_object(
  'settings', ({_sql_as_subquery(SETTINGS_UA_SQL)}),
  'tls_db', ({_sql_as_subquery(REDIS_DRIFT_TLS_DB_SQL)}),
  'tiers_db', ({_sql_as_subquery(REDIS_DRIFT_TIERS_DB_SQL)})
);
""".strip()

_LIVE_PROBE_MARKER = "@@LIVE:"


def render_live_node_probe_shell() -> str:
    """Remote read-only shell: ONE SSM round-trip that gathers every always-live
    drift input for a node -- the two Redis config blobs (redis_cache_drift) plus
    a single psql bundle of settings + tls/tier DB tables (http_ua_drift +
    redis_cache_drift's authoritative side). @@LIVE: markers delimit sections.

    DB SQL is base64-piped to psql (never inlined into -c "...") so the embedded
    double-quotes in to_char(...) survive -- same delivery as render_runtime_sync_shell.

    The redis reads run bare -- no exit-status suppression, no stderr redirect.
    Under the wrapper's `set -euo pipefail` a redis-cli failure (container down /
    auth / wrong host) MUST abort the shell so the SSM invocation fails and the
    node degrades to status=error. Suppressing the exit status would turn an
    unreachable Redis into an empty blob that parses as a (clean) cold cache -- a
    false all-green, the exact silent-failure class redis_cache_drift exists to
    catch. A genuinely missing key returns empty with exit 0, so the cold-cache
    path holds."""
    db_bundle_b64 = base64.b64encode(LIVE_NODE_DB_BUNDLE_SQL.encode("utf-8")).decode("ascii")
    m = _LIVE_PROBE_MARKER
    return (
        "# Generated by manage-anthropic-config.py check (live node probe: http_ua + redis_cache_drift)\n"
        "PSQL='sudo docker exec -i tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t -v ON_ERROR_STOP=1'\n"
        "RC='env -u REDISCLI_AUTH sudo docker exec tokenkey-redis redis-cli'\n"
        f"echo '{m}tls_redis'\n"
        f"$RC GET {REDIS_DRIFT_TLS_CACHE_KEY}\n"
        f"echo '{m}tiers_redis'\n"
        f"$RC GET {REDIS_DRIFT_TIERS_CACHE_KEY}\n"
        f"echo '{m}db_bundle'\n"
        f"echo {db_bundle_b64} | base64 -d | $PSQL\n"
        f"echo '{m}end'\n"
    )


def _parse_live_probe_sections(out: str) -> dict[str, str]:
    """Split @@LIVE:-delimited output into {section: text}. Content between a
    marker line and the next marker (or EOF) is that section's body, stripped."""
    sections: dict[str, list[str]] = {}
    current: str | None = None
    for line in (out or "").splitlines():
        stripped = line.strip()
        if stripped.startswith(_LIVE_PROBE_MARKER):
            current = stripped[len(_LIVE_PROBE_MARKER):]
            sections.setdefault(current, [])
            continue
        if current is not None:
            sections[current].append(line)
    return {k: "\n".join(v).strip() for k, v in sections.items()}


def _parse_utc_ts(value: str | None) -> float | None:
    """Parse an ISO-8601 UTC timestamp (Go RFC3339Nano or PG to_char form) to an
    epoch float. Returns None if absent/unparseable (caller skips staleness)."""
    if not value:
        return None
    s = value.strip().strip('"')
    if not s:
        return None
    if s.endswith("Z"):
        s = s[:-1] + "+00:00"
    try:
        dt = _dt.datetime.fromisoformat(s)
    except ValueError:
        for fmt in ("%Y-%m-%dT%H:%M:%S.%f%z", "%Y-%m-%dT%H:%M:%S%z",
                    "%Y-%m-%dT%H:%M:%S.%f", "%Y-%m-%dT%H:%M:%S"):
            try:
                dt = _dt.datetime.strptime(s, fmt)
                break
            except ValueError:
                continue
        else:
            return None
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=_dt.timezone.utc)
    return dt.timestamp()


def _load_redis_drift_blob(raw: str) -> "tuple[list[dict] | None, str | None]":
    """(parsed_list, error). Empty raw -> (None, None) == cold cache (not drift).
    Non-JSON / non-array -> (None, "<reason>")."""
    s = (raw or "").strip()
    if not s:
        return None, None
    try:
        parsed = json.loads(s)
    except json.JSONDecodeError:
        return None, f"not JSON ({s[:60]})"
    if not isinstance(parsed, list):
        return None, f"not a JSON array ({type(parsed).__name__})"
    return parsed, None


def _redis_one_cache_drift(node_label: str, cache_key: str, key_field: str,
                           redis_raw: str, db_raw: str) -> list[dict]:
    """Diff one Redis cache blob vs its authoritative DB table. Pure."""
    items: list[dict] = []
    redis_list, rerr = _load_redis_drift_blob(redis_raw)
    if rerr is not None:
        items.append({"node": node_label, "cache": cache_key, "field": "blob", "status": "error",
                      "warning": f"{node_label}: redis {cache_key} blob {rerr}"})
        return items
    if redis_list is None:
        # cold cache: key absent. read-through repopulates from DB -> not drift.
        return items
    db_list, derr = _load_redis_drift_blob(db_raw)
    if derr is not None or db_list is None:
        items.append({"node": node_label, "cache": cache_key, "field": "db", "status": "error",
                      "warning": f"{node_label}: {cache_key} authoritative DB read failed ({derr or 'empty'})"})
        return items
    rmap = {r.get(key_field): r for r in redis_list if r.get(key_field) is not None}
    dmap = {d.get(key_field): d for d in db_list if d.get(key_field) is not None}
    exp_keys = [str(x) for x in sorted(dmap, key=str)]
    act_keys = [str(x) for x in sorted(rmap, key=str)]
    missing = sorted(set(dmap) - set(rmap), key=str)  # in DB, not in cache
    extra = sorted(set(rmap) - set(dmap), key=str)    # in cache, not in DB
    if missing:
        items.append({"node": node_label, "cache": cache_key, "field": "key-set", "status": "drift",
                      "expected": exp_keys, "actual": act_keys,
                      "warning": (f"{node_label}: {cache_key} cache MISSING {key_field}(s) "
                                  f"{[str(x) for x in missing]} present in DB -> stale cache, runtime "
                                  f"falls back to built-in default; DEL {cache_key} + PUBLISH "
                                  f"{cache_key}_updated (or restart)")})
    if extra:
        items.append({"node": node_label, "cache": cache_key, "field": "key-set", "status": "drift",
                      "expected": exp_keys, "actual": act_keys,
                      "warning": (f"{node_label}: {cache_key} cache has EXTRA {key_field}(s) "
                                  f"{[str(x) for x in extra]} absent from DB -> stale cache; "
                                  f"DEL {cache_key} + PUBLISH {cache_key}_updated")})
    for k in sorted(set(rmap) & set(dmap), key=str):
        r, d = rmap[k], dmap[k]
        if "name" in d and r.get("name") != d.get("name"):
            items.append({"node": node_label, "cache": cache_key, "field": f"{key_field}={k}:name",
                          "status": "drift", "expected": d.get("name"), "actual": r.get("name"),
                          "warning": (f"{node_label}: {cache_key} {key_field}={k} name redis="
                                      f"{r.get('name')} != db={d.get('name')} -> stale cache")})
        rt = _parse_utc_ts(r.get("updated_at"))
        dt = _parse_utc_ts(d.get("updated_at"))
        if rt is not None and dt is not None and dt - rt > REDIS_DRIFT_STALE_TOLERANCE_S:
            label = d.get("name") or k
            items.append({"node": node_label, "cache": cache_key, "field": f"{key_field}={k}:updated_at",
                          "status": "drift", "expected": d.get("updated_at"), "actual": r.get("updated_at"),
                          "warning": (f"{node_label}: {cache_key} '{label}' STALE -- DB updated_at "
                                      f"{d.get('updated_at')} newer than cached {r.get('updated_at')} by "
                                      f"{int(dt - rt)}s -> runtime serves old config; DEL {cache_key} + "
                                      f"PUBLISH {cache_key}_updated")})
    return items


def _redis_cache_drift_items(node_label: str, state: dict) -> list[dict]:
    """Pure diff of one node's Redis cached blobs vs authoritative DB tables.
    Empty == in sync (or cold cache). Unit-tested without SSM."""
    items: list[dict] = []
    items.extend(_redis_one_cache_drift(node_label, REDIS_DRIFT_TLS_CACHE_KEY, "id",
                                        state.get("tls_redis", ""), state.get("tls_db", "")))
    items.extend(_redis_one_cache_drift(node_label, REDIS_DRIFT_TIERS_CACHE_KEY, "name",
                                        state.get("tiers_redis", ""), state.get("tiers_db", "")))
    return items


# --------------------------------------------------------------------------
# Unified live-node probe: one parallel SSM pass feeds BOTH http_ua_drift and
# redis_cache_drift (they are both always-live, per-node, and read from the same
# box). Folding them avoids paying two fleet-wide SSM waves per check.
# --------------------------------------------------------------------------
_PROBE_PROD_SENTINEL = "\x00prod"  # node id distinct from any real edge id


def _live_bundle_from_output(out: str) -> dict:
    """Pure parse of one node's probe stdout into the bundle consumed by the two
    drift surfaces: {settings:{...}, tls_redis, tls_db, tiers_redis, tiers_db}.
    The DB arrays arrive parsed inside the psql jsonb bundle; they are
    re-serialized to JSON strings so _redis_cache_drift_items' string-in contract
    stays unchanged. Unit-tested without SSM."""
    sec = _parse_live_probe_sections(out)
    try:
        db = json.loads(sec.get("db_bundle", "") or "{}")
    except json.JSONDecodeError:
        db = {}
    if not isinstance(db, dict):
        db = {}
    settings = db.get("settings")
    return {
        "settings": settings if isinstance(settings, dict) else {},
        "tls_redis": sec.get("tls_redis", ""),
        "tiers_redis": sec.get("tiers_redis", ""),
        "tls_db": json.dumps(db.get("tls_db") if db.get("tls_db") is not None else []),
        "tiers_db": json.dumps(db.get("tiers_db") if db.get("tiers_db") is not None else []),
    }


def _read_live_node_bundle(region: str, instance_id: str, label: str) -> "dict | None":
    """One SSM round-trip per node -> parsed bundle (via _live_bundle_from_output),
    or None on SSM failure."""
    out, _cid, success, _err = ssm_run_shell(
        region, instance_id, render_live_node_probe_shell(), f"check: {label} live node probe")
    if not success:
        return None
    return _live_bundle_from_output(out)


def _run_live_node_checks(edge_ids: list[str], ua_expected: dict, *,
                          parallel_edges: int | None = None) -> "tuple[list[dict], list[dict]]":
    """ONE parallel SSM pass over all nodes (edges + prod) computing BOTH
    http_ua_drift and redis_cache_drift from a single per-node probe -- replaces
    the two former independent, per-node SSM waves (http_ua was also sequential).
    SSM failure degrades BOTH surfaces to status=error for that node (never
    silently in-sync). Always live: probe inputs are not captured by snapshot.
    Returns (http_ua_items, redis_items)."""
    nodes = list(edge_ids) + [_PROBE_PROD_SENTINEL]

    def _probe(node: str) -> "tuple[list[dict], list[dict]]":
        label = "prod" if node == _PROBE_PROD_SENTINEL else f"edge:{node}"
        try:
            if node == _PROBE_PROD_SENTINEL:
                region = PROD_TARGET["region"]
                instance_id = resolve_instance_id(region, PROD_TARGET["stack"])
            else:
                ident = _EDGE_SSM.resolve_edge_execution_identity(REPO_ROOT, node)
                region, instance_id = ident.region, ident.instance_id
            bundle = _read_live_node_bundle(region, instance_id, label)
        except (SystemExit, Exception):  # noqa: BLE001 - degrade, never abort the whole check
            bundle = None
        if bundle is None:
            err = f"live node probe SSM read failed for {label}"
            return (
                [{"node": label, "field": "*", "status": "error", "warning": err}],
                [{"node": label, "cache": "*", "field": "*", "status": "error", "warning": err}],
            )
        return (
            _http_ua_drift_items(label, bundle["settings"], ua_expected),
            _redis_cache_drift_items(label, bundle),
        )

    workers = _parallel_edges_workers(parallel_edges)
    results = _run_parallel_ordered(nodes, _probe, workers, label="live-node-probe")
    http_items: list[dict] = []
    redis_items: list[dict] = []
    for hu, rd in results:
        http_items.extend(hu or [])
        redis_items.extend(rd or [])
    return http_items, redis_items


def cmd_check(args: argparse.Namespace) -> int:
    snapshot = load_json_file(pathlib.Path(args.snapshot), "snapshot") if args.snapshot else None
    edge_ids = _edge_ids_for_check(snapshot, bool(args.allow_planned))
    report = _run_check_guards(
        edge_ids,
        bool(args.allow_planned),
        parallel_edges=getattr(args, "parallel_edges", None),
        legacy_guard=bool(getattr(args, "legacy_guard", False)),
    )
    policy = _load_operator_balance_policy()
    if snapshot is not None:
        if snapshot.get("version") != SNAPSHOT_VERSION:
            fail(f"check --snapshot: version {snapshot.get('version')} != {SNAPSHOT_VERSION} "
                 "(re-run snapshot for operator_user_balance)")
        balance_items = _balance_violations_from_snapshot(snapshot, policy)
    else:
        balance_items = _run_balance_checks_live(edge_ids, policy)
    balance_violation = any(x.get("status") == "violation" for x in balance_items)
    balance_error = any(x.get("status") == "error" for x in balance_items)
    report["operator_balance"] = {
        "policy_source": EDGE_OPERATOR_BALANCE_BASELINES.name,
        "min_balance_threshold": policy["min_balance_threshold"],
        "default_balance": policy["default_balance"],
        "violation_count": sum(1 for x in balance_items if x.get("status") == "violation"),
        "error_count": sum(1 for x in balance_items if x.get("status") == "error"),
        "items": balance_items,
    }
    # tier_table_drift: live tiers table vs git baseline (reveal backend edits)
    expected_tiers = _load_expected_tiers()
    if snapshot is not None:
        tier_items = _tier_table_drift_from_snapshot(snapshot, expected_tiers)
    else:
        tier_items = _run_tier_table_checks_live(edge_ids, expected_tiers)
    tier_table_violation = any(x.get("status") in ("drift", "missing", "extra", "error") for x in tier_items)
    report["tier_table_drift"] = {
        "policy_source": TIER_BASELINES.name,
        "compare_keys": TIER_TABLE_COMPARE_KEYS,
        "violation_count": sum(1 for x in tier_items if x.get("status") in ("drift", "missing", "extra")),
        "error_count": sum(1 for x in tier_items if x.get("status") == "error"),
        "items": tier_items,
    }
    # http_ua_drift + redis_cache_drift share ONE parallel per-node SSM probe
    # (both always-live, both read the same box). Always live: the probe inputs
    # (settings UA/manifest, Redis blobs) are updated out of band from snapshot,
    # and a stale snapshot is exactly how each blind spot hid. A cold Redis cache
    # (key absent) is not drift.
    http_ua_expected = _load_http_mimicry_baseline()
    http_ua_items, redis_items = _run_live_node_checks(
        edge_ids, http_ua_expected, parallel_edges=getattr(args, "parallel_edges", None))
    http_ua_violation = any(x.get("status") in ("drift", "error") for x in http_ua_items)
    report["http_ua_drift"] = {
        "policy_source": HTTP_MIMICRY_BASELINES.name,
        "expected_cc_version": str(http_ua_expected["cc_version"]).strip(),
        "violation_count": sum(1 for x in http_ua_items if x.get("status") == "drift"),
        "error_count": sum(1 for x in http_ua_items if x.get("status") == "error"),
        "items": http_ua_items,
    }
    redis_violation = any(x.get("status") in ("drift", "error") for x in redis_items)
    report["redis_cache_drift"] = {
        "caches": REDIS_DRIFT_CACHE_NAMES,
        "stale_tolerance_seconds": REDIS_DRIFT_STALE_TOLERANCE_S,
        "violation_count": sum(1 for x in redis_items if x.get("status") == "drift"),
        "error_count": sum(1 for x in redis_items if x.get("status") == "error"),
        "items": redis_items,
    }
    report["any_violation"] = (
        bool(report.get("any_violation")) or balance_violation or balance_error
        or tier_table_violation or http_ua_violation or redis_violation
    )
    if args.json:
        print(json.dumps(report, indent=2, ensure_ascii=False))
    else:
        any_violation = report.get("any_violation")
        print(f"check: any_violation={any_violation} guards_run={len(report.get('guards', []))} "
              f"edges={edge_ids} balance_violations={report['operator_balance']['violation_count']} "
              f"tier_table_drift={report['tier_table_drift']['violation_count']} "
              f"http_ua_drift={report['http_ua_drift']['violation_count']} "
              f"redis_cache_drift={report['redis_cache_drift']['violation_count']}")
        for sr in report.get("guards", []):
            ec = sr.get("exit_code")
            status = "OK" if ec == 0 else (sr.get("skipped_reason", "?") if ec is None else f"FAIL exit={ec}")
            print(f"  [{status}] {sr.get('description')}")
        for item in balance_items:
            st = item.get("status")
            if st == "violation":
                print(f"  [BALANCE] edge={item['edge_id']} balance={item.get('operator_user_balance')} "
                      f"< threshold={policy['min_balance_threshold']}")
            elif st == "error":
                print(f"  [BALANCE-ERR] edge={item['edge_id']}: {item.get('reason')}")
        for item in tier_items:
            print(f"  [TIER-{item.get('status', '?').upper()}] {item.get('warning')}")
        for item in http_ua_items:
            print(f"  [UA-{item.get('status', '?').upper()}] {item.get('warning')}")
        for item in redis_items:
            print(f"  [REDIS-{item.get('status', '?').upper()}] {item.get('warning')}")
    return 1 if report.get("any_violation") else 0


def _edge_ids_for_check(snapshot: dict | None, allow_planned: bool) -> list[str]:
    if snapshot is not None:
        return sorted([
            eid for eid, e in snapshot.get("edges", {}).items()
            if e.get("deployable") is not False and "error" not in e
        ])
    edge_matrix = load_json_file(EDGE_MATRIX, "edge matrix")
    ls_targets = _EDGE_ROUTING.load_lightsail_targets(REPO_ROOT)
    if allow_planned:
        return _EDGE_ROUTING.merged_edge_ids(edge_matrix, ls_targets)
    return _EDGE_ROUTING.iter_effective_deployable_edge_ids(edge_matrix, ls_targets)


def _guard_items_from_batch(
    edge_id: str,
    edge_meta: dict[str, Any],
    batch_rows: list[Any],
    baseline: dict[str, Any],
    *,
    default_tier: str = "",
) -> list[dict[str, Any]]:
    items: list[dict[str, Any]] = []
    if not batch_rows:
        items.append({
            "edge": {**edge_meta},
            "account_name": "all",
            "status": "ok",
            "diff_count": 0,
            "diffs": [],
            "accounts_found": 0,
            "sql_path": None,
        })
        return items
    for row in batch_rows:
        if not isinstance(row, dict):
            continue
        account_name = str(
            row.get("account_name") or (row.get("account") or {}).get("name") or ""
        ).strip()
        live = {k: v for k, v in row.items() if k != "account_name"}
        try:
            effective = _GUARD.resolve_effective_baseline(
                baseline,
                live,
                edge_id=edge_id,
                account_name=account_name,
                default_tier=default_tier,
            )
            diffs = _GUARD.compare_live_to_baseline(live, effective)
            items.append({
                "edge": {**edge_meta},
                "account_name": account_name,
                "account_stability_tier": (live.get("account") or {}).get("stability_tier"),
                "baseline_tier": effective.get("selected_tier"),
                "tier_source": effective.get("tier_source"),
                "status": "ok" if not diffs else "drift",
                "diff_count": len(diffs),
                "diffs": diffs,
                "sql_path": None,
            })
        except _GUARD.CheckError as exc:
            items.append({
                "edge": {**edge_meta},
                "account_name": account_name,
                "status": "error",
                "diff_count": 0,
                "diffs": [],
                "error_message": exc.args[0] if exc.args else "unknown error",
                "sql_path": None,
            })
    return items


def _run_guard_batch_for_edge(eid: str, allow_planned: bool) -> dict[str, Any]:
    """One SSM round-trip per edge; local compare (same semantics as guard --account-name all)."""
    baseline = load_json_file(TIER_BASELINES, "tier baseline")
    try:
        ident = _EDGE_SSM.resolve_edge_execution_identity(REPO_ROOT, eid)
    except SystemExit:
        return {
            "exit_code": 2,
            "description": f"edge-oauth-stability edge={eid}",
            "report": None,
        }
    raw, cid = ssm_run_sql(
        ident.region,
        ident.instance_id,
        _GUARD.build_all_oauth_guard_live_batch_query(),
        f"guard batch edge={eid}",
    )
    try:
        batch_rows = json.loads(raw) if raw else []
    except json.JSONDecodeError:
        batch_rows = []
    if not isinstance(batch_rows, list):
        batch_rows = []
    edge_meta = {
        "edge_id": eid,
        "region": ident.region,
        "instance_id": ident.instance_id,
        "allow_planned": allow_planned,
    }
    items = _guard_items_from_batch(eid, edge_meta, batch_rows, baseline)
    drift_count = sum(1 for x in items if x.get("status") == "drift")
    error_count = sum(1 for x in items if x.get("status") == "error")
    exit_code = 1 if drift_count or error_count else 0
    report = {
        "mode": "batch",
        "selector": {
            "edge_id": eid,
            "account_name": "all",
            "allow_planned": allow_planned,
        },
        "summary": {
            "edge_total": 1,
            "excluded_edge_total": 0,
            "account_result_total": len(items),
            "ok_count": sum(1 for x in items if x.get("status") == "ok"),
            "drift_count": drift_count,
            "error_count": error_count,
        },
        "excluded_edges": [],
        "items": items,
        "ssm_command_id": cid,
    }
    return {
        "exit_code": exit_code,
        "description": f"edge-oauth-stability edge={eid}",
        "report": report,
    }


def _run_check_guards(
    edge_ids: list[str],
    allow_planned: bool,
    *,
    parallel_edges: int | None = None,
    legacy_guard: bool = False,
) -> dict:
    workers = _parallel_edges_workers(parallel_edges)

    def _legacy_one(eid: str) -> dict[str, Any]:
        return _run_guard(
            ["python3", str(OPS_DIR / "check-edge-oauth-stability.py"),
             "--edge-id", eid, "--account-name", "all", "--json"]
            + (["--allow-planned"] if allow_planned else []),
            f"edge-oauth-stability edge={eid}",
        )

    def _batch_one(eid: str) -> dict[str, Any]:
        return _run_guard_batch_for_edge(eid, allow_planned)

    if legacy_guard:
        print(
            f"check-guards: legacy subprocess {len(edge_ids)} edge(s) "
            f"parallel_workers={workers}",
            file=sys.stderr,
        )
        sub_results = _run_parallel_ordered(
            edge_ids, _legacy_one, workers, label="guard-legacy",
        )
        guard_mode = "legacy-subprocess"
    else:
        print(
            f"check-guards: batch-ssm {len(edge_ids)} edge(s) parallel_workers={workers}",
            file=sys.stderr,
        )
        sub_results = _run_parallel_ordered(
            edge_ids, _batch_one, workers, label="guard-batch",
        )
        guard_mode = "batch-ssm"

    any_violation = any(sr.get("exit_code", 0) not in (0, None) for sr in sub_results)
    return {
        "version": 2,
        "checked_at": now_utc_iso(),
        "edges_in_scope": edge_ids,
        "any_violation": any_violation,
        "guards": sub_results,
        "guard_mode": guard_mode,
    }


def _iter_guard_drift_accounts(check_report: dict) -> list[dict]:
    """Parse guard JSON for accounts with status=drift (deduped, stable order)."""
    seen: set[tuple[str, str]] = set()
    out: list[dict] = []
    for guard in check_report.get("guards") or []:
        report = guard.get("report") or {}
        edge_id = (report.get("selector") or {}).get("edge_id")
        for item in report.get("items") or []:
            if item.get("status") != "drift":
                continue
            edge_meta = item.get("edge") or {}
            eid = str(edge_id or edge_meta.get("edge_id") or "").strip()
            name = str(item.get("account_name") or "").strip()
            tier = str(
                item.get("baseline_tier") or item.get("account_stability_tier") or ""
            ).lower().strip()
            if not eid or not name or not tier:
                continue
            key = (eid, name)
            if key in seen:
                continue
            seen.add(key)
            out.append({
                "edge_id": eid,
                "account_name": name,
                "tier": tier,
                "diff_count": item.get("diff_count"),
                "diff_paths": [
                    d.get("path") for d in (item.get("diffs") or []) if d.get("path")
                ],
            })
    return out


def cmd_plan_guard_drift_fix(args: argparse.Namespace) -> int:
    """Build one multi-action plan forcing template rewrite for every guard drift account."""
    snap = _load_snapshot_or_die(args.snapshot)
    if args.check_report:
        check_report = load_json_file(pathlib.Path(args.check_report), "check report")
    else:
        edge_ids = _edge_ids_for_check(snap, bool(args.allow_planned))
        check_report = _run_check_guards(
            edge_ids,
            bool(args.allow_planned),
            parallel_edges=getattr(args, "parallel_edges", None),
            legacy_guard=bool(getattr(args, "legacy_guard", False)),
        )
    drift_accounts = _iter_guard_drift_accounts(check_report)
    tiers = _load_tier_baselines()
    actions: list[dict] = []
    befores: list[dict] = []
    for drift in drift_accounts:
        edge_id = drift["edge_id"]
        account_name = drift["account_name"]
        tier_key = drift["tier"]
        if tier_key not in tiers:
            fail(f"guard drift account {account_name!r} on {edge_id} has unknown tier {tier_key!r}")
        edge = _find_edge(snap, edge_id)
        account = _find_edge_account(edge, account_name)
        baseline = tiers[tier_key]
        step = len(actions) + 1
        actions.append(_build_tier_action(edge_id, account_name, baseline, tier_key, step))
        befores.append({"edge_id": edge_id, "drift": drift, **_account_before(account)})

    plan = {
        "version": PLAN_VERSION,
        "kind": "edge_account_tier_change",
        "confirm_code": CONFIRM_CODE,
        "intent": {
            "guard_drift_fix": True,
            "force_template_rewrite": True,
            "drift_accounts": len(drift_accounts),
        },
        "snapshot_captured_at": snap.get("captured_at"),
        "plan_built_at": now_utc_iso(),
        "noop": len(actions) == 0,
        "summary": {
            "total_steps": len(actions),
            "edge_changes": len(actions),
            "drift_accounts": len(drift_accounts),
        },
        "live_inputs": {"edge_accounts_before": befores},
        "actions": actions,
        "check_report": {
            "checked_at": check_report.get("checked_at"),
            "any_violation": check_report.get("any_violation"),
            "edges_in_scope": check_report.get("edges_in_scope"),
        },
    }
    out_str = json.dumps(plan, indent=2, ensure_ascii=False)
    if args.out:
        pathlib.Path(args.out).write_text(out_str)
        print(f"plan-guard-drift-fix: written {args.out} ({len(actions)} step(s), "
              f"drift_accounts={len(drift_accounts)})", file=sys.stderr)
    else:
        print(out_str)
    if not actions:
        print("plan-guard-drift-fix: no guard drift accounts — empty plan (noop)",
              file=sys.stderr)
    return 0


def cmd_remediate_guard_drift(args: argparse.Namespace) -> int:
    """End-to-end: snapshot → check → plan-guard-drift-fix → apply → sync-runtime → verify → check."""
    job_dir = pathlib.Path(args.job_dir) if args.job_dir else pathlib.Path(
        f"/tmp/anthropic-remediate-{_dt.datetime.now().strftime('%Y%m%d-%H%M%S')}-{os.getpid()}"
    )
    job_dir.mkdir(parents=True, exist_ok=True)
    snap_path = job_dir / "snap.json"
    check_path = job_dir / "check.json"
    plan_path = job_dir / "plan-guard-drift-fix.json"

    parallel = getattr(args, "parallel_edges", None)
    legacy_guard = bool(getattr(args, "legacy_guard", False))
    print(
        f"remediate: job_dir={job_dir} parallel_edges={_parallel_edges_workers(parallel)} "
        f"guard_mode={'legacy-subprocess' if legacy_guard else 'batch-ssm'}",
        file=sys.stderr,
    )
    rc = cmd_snapshot(argparse.Namespace(
        out=str(snap_path),
        allow_planned=args.allow_planned,
        skip_prod=False,
        parallel_edges=parallel,
    ))
    if rc != 0:
        return rc

    snap = load_json_file(snap_path, "snapshot")
    edge_ids = _edge_ids_for_check(snap, bool(args.allow_planned))
    check_report = _run_check_guards(
        edge_ids,
        bool(args.allow_planned),
        parallel_edges=parallel,
        legacy_guard=legacy_guard,
    )
    check_path.write_text(json.dumps(check_report, indent=2, ensure_ascii=False))
    if not check_report.get("any_violation"):
        print("remediate: check clean — nothing to apply", file=sys.stderr)
        return 0

    rc = cmd_plan_guard_drift_fix(argparse.Namespace(
        snapshot=str(snap_path),
        check_report=str(check_path),
        allow_planned=args.allow_planned,
        out=str(plan_path),
    ))
    if rc != 0:
        return rc
    plan = load_json_file(plan_path, "plan")
    if plan.get("noop"):
        print("remediate: plan noop after drift enumeration — nothing to apply", file=sys.stderr)
        return 0

    if args.dry_run:
        print(f"remediate: dry-run stop before apply; plan={plan_path}", file=sys.stderr)
        return 0

    if args.confirm != CONFIRM_CODE:
        fail(
            f"--confirm mismatch.\n  Got:      {args.confirm!r}\n  Required: {CONFIRM_CODE!r}",
            code=2,
        )

    rc = cmd_apply(argparse.Namespace(
        plan=str(plan_path),
        confirm=args.confirm,
        job_dir=str(job_dir / "apply"),
        json=False,
        sync_runtime=not args.skip_runtime_sync,
        skip_prod_runtime_sync=args.skip_prod_runtime_sync,
        sync_runtime_ua_version=None,
        parallel_edges=parallel,
    ))
    if rc != 0:
        return rc

    rc = cmd_verify(argparse.Namespace(
        plan=str(plan_path),
        snapshot_out=str(job_dir / "snap-after-verify.json"),
        allow_planned=args.allow_planned,
        skip_prod=False,
        json=False,
        parallel_edges=parallel,
    ))
    if rc != 0:
        return rc

    final_check = _run_check_guards(
        edge_ids,
        bool(args.allow_planned),
        parallel_edges=parallel,
        legacy_guard=legacy_guard,
    )
    (job_dir / "check-after.json").write_text(
        json.dumps(final_check, indent=2, ensure_ascii=False)
    )
    if final_check.get("any_violation"):
        print("remediate: post-verify check still reports violations", file=sys.stderr)
        return 1
    print("remediate: complete — check clean after apply + verify", file=sys.stderr)
    return 0


# --------------------------------------------------------------------------
# Stage 3 — plan
# --------------------------------------------------------------------------

def _load_snapshot_or_die(path: str) -> dict:
    snap = load_json_file(pathlib.Path(path), "snapshot")
    v = snap.get("version")
    if v != SNAPSHOT_VERSION:
        fail(f"snapshot version {v} != expected {SNAPSHOT_VERSION} "
             f"(snapshot v1 cascaded prod stub fields aggregated from edges; "
             f"v2 dropped that, v3 re-added a prod READ view + stub pool-mode WRITE; "
             f"v4 added per-target operator_user_concurrency + schedulable_concurrency_sum "
             f"for the concurrency-mirror surface; "
             f"v5 added per-target anthropic_groups for group claude-code-only surface; "
             f"v6 added per-edge operator_user_balance + operator_user_exists for surface E; "
             f"v7 added per-node tiers table rows for the tier_table_drift surface "
             f"(live tiers vs git baseline; post-#472 tier reference-table model))")
    return snap


def _load_group_claude_code_policy() -> dict:
    """Parse anthropic-group-claude-code-baselines.json — single source for
    claude_code_only target value on anthropic groups."""
    raw = load_json_file(GROUP_CLAUDE_CODE_BASELINES, "group claude code baselines")
    if not isinstance(raw, dict):
        fail("group claude code baselines: top-level must be an object")
    if raw.get("schema_version") != 1:
        fail(f"group claude code baselines: schema_version {raw.get('schema_version')!r} != 1")
    pol = raw.get("policy")
    if not isinstance(pol, dict):
        fail("group claude code baselines: missing policy object")
    if pol.get("platform") != "anthropic":
        fail("group claude code baselines: policy.platform must be 'anthropic'")
    if pol.get("claude_code_only") is not True:
        fail("group claude code baselines: policy.claude_code_only must be true "
             "(Claude Code client only)")
    return pol


def _load_stub_pool_policy() -> dict:
    """Parse anthropic-stub-pool-baselines.json once. The policy is the single
    source of truth for which prod anthropic stubs get pool_mode enabled — both
    the regex matcher and the retry-count value live here, so apply SQL is
    derived (not hand-written) and verify can compare against the same field
    set the plan declared. Schema is checked strictly to catch typos early."""
    raw = load_json_file(STUB_POOL_BASELINES, "stub pool baselines")
    if not isinstance(raw, dict):
        fail("stub pool baselines: top-level must be an object")
    if raw.get("schema_version") != 1:
        fail(f"stub pool baselines: schema_version {raw.get('schema_version')!r} != 1")
    policy = raw.get("policy")
    if not isinstance(policy, dict):
        fail("stub pool baselines: missing 'policy' object")
    required = ("base_url_pattern", "platform", "account_type",
                "pool_mode_enabled", "pool_mode_retry_count")
    for k in required:
        if k not in policy:
            fail(f"stub pool baselines: policy missing required field {k!r}")
    if policy["platform"] != "anthropic":
        fail(f"stub pool baselines: policy.platform must be 'anthropic', got {policy['platform']!r} "
             "(this orchestrator handles anthropic only)")
    if policy["account_type"] != "apikey":
        fail(f"stub pool baselines: policy.account_type must be 'apikey', got {policy['account_type']!r} "
             "(pool_mode is only meaningful for IsAPIKeyOrBedrock() accounts; oauth cannot enable it)")
    retry = policy["pool_mode_retry_count"]
    if not isinstance(retry, int) or retry < 0 or retry > 10:
        fail(f"stub pool baselines: policy.pool_mode_retry_count must be int in [0,10], got {retry!r}")
    try:
        import re
        policy["_compiled_pattern"] = re.compile(policy["base_url_pattern"])
    except re.error as e:
        fail(f"stub pool baselines: policy.base_url_pattern is not a valid regex: {e}")
    return policy


def _load_tier_baselines() -> dict[str, dict]:
    """Return {tier: flattened_fields} keyed by 'l1'..'l5'.

    Flattens the nested ``tiers[lN].baseline.{account,extra}.<field>`` shape
    so callers see one dict per tier with both account fields and extra
    fields at the top level (the keys used by the tier baseline template).
    """
    raw = load_json_file(TIER_BASELINES, "tier baselines")
    out: dict[str, dict] = {}
    items = raw.get("tiers") if isinstance(raw, dict) and "tiers" in raw else raw
    src_iter = items.items() if isinstance(items, dict) else (
        ((t.get("stability_tier") or t.get("tier")), t) for t in items
    )
    for key, t in src_iter:
        if not key:
            continue
        baseline = t.get("baseline") if isinstance(t, dict) else None
        flat: dict[str, Any] = {"tier": str(key).lower()}
        if isinstance(baseline, dict):
            for sub in ("account", "extra"):
                d = baseline.get(sub)
                if isinstance(d, dict):
                    flat[sub] = dict(d)
                    flat.update(d)
        for k, v in (t.items() if isinstance(t, dict) else []):
            if k == "baseline":
                continue
            flat.setdefault(k, v)
        out[str(key).lower()] = flat
    return out


def _find_edge(snap: dict, edge_id: str) -> dict:
    edges = snap.get("edges", {})
    if edge_id not in edges:
        fail(f"edge {edge_id!r} not in snapshot; re-run snapshot")
    edge = edges[edge_id]
    if edge.get("error") or edge.get("skipped_reason"):
        fail(f"edge {edge_id!r} not snapshotted: {edge.get('error') or edge.get('skipped_reason')}")
    return edge


def _find_edge_account(edge: dict, account_name: str) -> dict:
    for a in edge.get("oauth_accounts", []):
        if a.get("name") == account_name:
            return a
    fail(f"account {account_name!r} not found in edge oauth_accounts")
    return {}  # unreachable


# Tier-baseline value fields that the apply SQL writes AND that this pipeline
# owns end-to-end. The skip-as-noop gate (_tier_fields_match) and Stage-5 verify
# must both cover this WHOLE set — otherwise a bump touching only one of them
# (e.g. window_cost_limit-only) is silently skipped by plan-tier-bump and then
# falsely verified clean, defeating the "no account left at the old value"
# guarantee. snapshot already carries all of these.
#
# `priority` is deliberately EXCLUDED: it is owned by the separate
# rebalance-anthropic-priority pipeline. Pulling it in here would make
# plan-tier-bump fight the rebalancer (spurious actions on every rebalanced
# account) and make verify flag false drift whenever priority was rebalanced.
# (apply still writes the baseline priority — an existing cross-pipeline
# interaction, out of scope here.) rate_multiplier is fixed at 1.0 by policy and
# is likewise not a field anyone bumps, so it is left out to avoid float-equality
# fragility. credentials-side fields (e.g. temp_unschedulable_rules) are not
# tracked by snapshot/verify either — that is what --force-template-rewrite covers.
_TIER_BASELINE_FIELDS = (
    "base_rpm", "rpm_sticky_buffer", "concurrency", "max_sessions",
    "window_cost_limit", "session_idle_timeout_minutes", "window_cost_sticky_reserve",
)
_ACCOUNT_BEFORE_FIELDS = (
    "id", "name", "concurrency", "stability_tier", "base_rpm",
    "rpm_sticky_buffer", "max_sessions", "session_idle_timeout_minutes",
    "window_cost_limit", "window_cost_sticky_reserve", "status",
)


def _tier_fields_match(account: dict, baseline: dict) -> bool:
    return all(account.get(k) == baseline.get(k) for k in _TIER_BASELINE_FIELDS)


def _tier_expected_after(baseline: dict, tier_key: str) -> dict:
    """The post-apply field values Stage-5 verify compares against live. Carries
    stability_tier + every field this pipeline owns (_TIER_BASELINE_FIELDS), so
    verify can confirm each one actually landed."""
    exp = {"stability_tier": tier_key}
    exp.update({k: baseline.get(k) for k in _TIER_BASELINE_FIELDS})
    return exp


def _build_tier_action(edge_id: str, account_name: str, baseline: dict,
                       tier_key: str, step: int) -> dict:
    """One apply action re-rendering ``account_name`` at ``tier_key``. The SQL is
    derived from the JSON baseline at apply time; ``expected_after`` is what Stage
    5 verify compares against live."""
    return {
        "step": step,
        "kind": "edge_account_tier",
        "target": {"env": "edge", "edge_id": edge_id, "account_name": account_name},
        "sql_source": "rendered-from-anthropic-oauth-stability-baselines-tiered.json",
        "variables": {"account_name": account_name, "stability_tier": tier_key},
        "expected_after": _tier_expected_after(baseline, tier_key),
    }


def _account_before(account: dict) -> dict:
    return {k: account.get(k) for k in _ACCOUNT_BEFORE_FIELDS}


def cmd_plan_edge_account_tier(args: argparse.Namespace) -> int:
    snap = _load_snapshot_or_die(args.snapshot)
    tiers = _load_tier_baselines()
    tier_key = args.tier.lower()
    if tier_key not in tiers:
        fail(f"tier {args.tier!r} not in baselines (available: {sorted(tiers)})")
    baseline = tiers[tier_key]

    edge = _find_edge(snap, args.edge_id)
    target_account = _find_edge_account(edge, args.account_name)

    current_tier = (target_account.get("stability_tier") or "").lower()
    fields_match = _tier_fields_match(target_account, baseline)
    force_rewrite = bool(getattr(args, "force_template_rewrite", False))
    if current_tier == tier_key and fields_match and not force_rewrite:
        print(
            f"plan: account {args.account_name!r} on edge {args.edge_id} is already at "
            f"tier {tier_key} with matching baseline fields — emitting empty plan.",
            file=sys.stderr,
        )
        empty_plan = {
            "version": PLAN_VERSION,
            "kind": "edge_account_tier_change",
            "confirm_code": CONFIRM_CODE,
            "intent": {"edge_id": args.edge_id, "account_name": args.account_name,
                       "new_tier": tier_key,
                       "force_template_rewrite": force_rewrite},
            "snapshot_captured_at": snap.get("captured_at"),
            "plan_built_at": now_utc_iso(),
            "noop": True,
            "noop_reason": "current tier and baseline fields already match target",
            "summary": {"total_steps": 0, "edge_changes": 0},
            "live_inputs": {"edge_account_before": target_account},
            "actions": [],
        }
        out_str = json.dumps(empty_plan, indent=2, ensure_ascii=False)
        if args.out:
            pathlib.Path(args.out).write_text(out_str)
            print(f"plan: written {args.out} (noop)", file=sys.stderr)
        else:
            print(out_str)
        return 0

    action = _build_tier_action(args.edge_id, args.account_name, baseline, tier_key, 1)

    plan = {
        "version": PLAN_VERSION,
        "kind": "edge_account_tier_change",
        "confirm_code": CONFIRM_CODE,
        "intent": {"edge_id": args.edge_id, "account_name": args.account_name,
                   "new_tier": tier_key,
                   "force_template_rewrite": force_rewrite},
        "snapshot_captured_at": snap.get("captured_at"),
        "plan_built_at": now_utc_iso(),
        "summary": {"total_steps": 1, "edge_changes": 1},
        "live_inputs": {
            "edge_account_before": _account_before(target_account),
        },
        "actions": [action],
    }

    out_str = json.dumps(plan, indent=2, ensure_ascii=False)
    if args.out:
        pathlib.Path(args.out).write_text(out_str)
        print(f"plan: written {args.out} (1 step)", file=sys.stderr)
    else:
        print(out_str)
    return 0


def cmd_plan_tier_bump(args: argparse.Namespace) -> int:
    """Re-apply a (just-edited) tier baseline value to *every* account currently
    on that tier across all snapshotted deployable edges, in one multi-action
    plan. This is the correct shape for a tier-VALUE bump (e.g. L5 max_sessions
    50→60): edit the baseline JSON first, then this enumerates the tier's whole
    live population so no account is silently left at the old value. apply/verify
    already iterate the actions list, so one apply + one verify covers the batch.
    Contrast plan-edge-account-tier, which MOVES a single account to a tier."""
    snap = _load_snapshot_or_die(args.snapshot)
    tiers = _load_tier_baselines()
    tier_key = args.tier.lower()
    if tier_key not in tiers:
        fail(f"tier {args.tier!r} not in baselines (available: {sorted(tiers)})")
    baseline = tiers[tier_key]
    force = bool(getattr(args, "force_template_rewrite", False))

    actions: list[dict] = []
    befores: list[dict] = []
    skipped: list[dict] = []
    for edge_id, edge in sorted(snap.get("edges", {}).items()):
        if edge.get("error") or edge.get("skipped_reason"):
            continue
        for a in edge.get("oauth_accounts", []):
            if (a.get("stability_tier") or "").lower() != tier_key:
                continue
            if _tier_fields_match(a, baseline) and not force:
                skipped.append({"edge_id": edge_id, "account_name": a.get("name"),
                                "reason": "live fields already match baseline"})
                continue
            step = len(actions) + 1
            actions.append(_build_tier_action(edge_id, a.get("name"), baseline, tier_key, step))
            befores.append({"edge_id": edge_id, **_account_before(a)})

    plan = {
        "version": PLAN_VERSION,
        "kind": "edge_account_tier_change",
        "confirm_code": CONFIRM_CODE,
        "intent": {"tier_bump": tier_key, "scope": "all-snapshotted-deployable-edges",
                   "force_template_rewrite": force},
        "snapshot_captured_at": snap.get("captured_at"),
        "plan_built_at": now_utc_iso(),
        "noop": len(actions) == 0,
        "summary": {"total_steps": len(actions), "edge_changes": len(actions),
                    "skipped": len(skipped)},
        "live_inputs": {"edge_accounts_before": befores, "skipped": skipped},
        "actions": actions,
    }

    out_str = json.dumps(plan, indent=2, ensure_ascii=False)
    if args.out:
        pathlib.Path(args.out).write_text(out_str)
        print(f"plan: written {args.out} ({len(actions)} step(s), {len(skipped)} skipped)",
              file=sys.stderr)
    else:
        print(out_str)
    if not actions:
        print(f"plan: no account on tier {tier_key} needs rewriting "
              f"(skipped={len(skipped)}; pass --force-template-rewrite to rewrite anyway)",
              file=sys.stderr)
    return 0


# --------------------------------------------------------------------------
# Stage 3c — plan-stub-pool (prod anthropic mirror stubs)
# --------------------------------------------------------------------------

_STUB_POOL_FIELDS = ("cred_pool_mode", "cred_pool_mode_retry_count")


def _stub_pool_expected_after(policy: dict) -> dict:
    """Field values Stage-5 verify compares against live after pool_mode is set.
    Carries exactly what apply writes; verify reads them straight off the
    re-snapshot's prod.anthropic_stubs entries (same JSON keys)."""
    return {
        "cred_pool_mode": bool(policy["pool_mode_enabled"]),
        "cred_pool_mode_retry_count": int(policy["pool_mode_retry_count"]),
    }


def _stub_pool_fields_match(stub: dict, policy: dict) -> bool:
    exp = _stub_pool_expected_after(policy)
    return all(stub.get(k) == exp[k] for k in _STUB_POOL_FIELDS)


def _stub_before(stub: dict) -> dict:
    keep = ("id", "name", "type", "status", "schedulable", "concurrency",
            "cred_base_url", "cred_pool_mode", "cred_pool_mode_retry_count")
    return {k: stub.get(k) for k in keep}


def cmd_plan_stub_pool(args: argparse.Namespace) -> int:
    """Enumerate every prod anthropic stub whose credentials.base_url matches
    the policy regex; emit one plan action per stub that is not already at the
    declared (pool_mode_enabled, pool_mode_retry_count) tuple. Idempotent — a
    second run after apply produces noop=true. Per skill design 2026-05-23:
    no edge-fan-out gate (see policy.notes.no_min_account_gate in the baseline)."""
    snap = _load_snapshot_or_die(args.snapshot)
    policy = _load_stub_pool_policy()
    pattern = policy["_compiled_pattern"]
    force = bool(getattr(args, "force_template_rewrite", False))

    prod = snap.get("prod") or {}
    if prod.get("error") or prod.get("skipped_reason"):
        fail(f"snapshot.prod not captured: {prod.get('error') or prod.get('skipped_reason')}; "
             "re-run snapshot without --skip-prod")
    stubs = prod.get("anthropic_stubs") or []

    actions: list[dict] = []
    befores: list[dict] = []
    skipped_unmatched: list[dict] = []
    skipped_noop: list[dict] = []
    for stub in stubs:
        base_url = stub.get("cred_base_url") or ""
        if not pattern.match(base_url):
            skipped_unmatched.append({
                "id": stub.get("id"), "name": stub.get("name"),
                "cred_base_url": base_url,
                "reason": "base_url does not match policy.base_url_pattern",
            })
            continue
        if _stub_pool_fields_match(stub, policy) and not force:
            skipped_noop.append({
                "id": stub.get("id"), "name": stub.get("name"),
                "reason": "pool_mode + retry_count already match policy",
            })
            continue
        step = len(actions) + 1
        actions.append({
            "step": step,
            "kind": "prod_stub_pool",
            "target": {
                "env": "prod",
                "account_id": stub.get("id"),
                "account_name": stub.get("name"),
            },
            "sql_source": "rendered-from-anthropic-stub-pool-baselines.json",
            "variables": {
                "account_id": stub.get("id"),
                "pool_mode_enabled": bool(policy["pool_mode_enabled"]),
                "pool_mode_retry_count": int(policy["pool_mode_retry_count"]),
            },
            "expected_after": _stub_pool_expected_after(policy),
        })
        befores.append(_stub_before(stub))

    plan = {
        "version": PLAN_VERSION,
        "kind": "prod_stub_pool_mode",
        "confirm_code": CONFIRM_CODE,
        "intent": {
            "scope": "all-prod-anthropic-stubs-matching-policy",
            "policy_source": STUB_POOL_BASELINES.name,
            "base_url_pattern": policy["base_url_pattern"],
            "pool_mode_enabled": bool(policy["pool_mode_enabled"]),
            "pool_mode_retry_count": int(policy["pool_mode_retry_count"]),
            "force_template_rewrite": force,
        },
        "snapshot_captured_at": snap.get("captured_at"),
        "plan_built_at": now_utc_iso(),
        "noop": len(actions) == 0,
        "summary": {
            "total_steps": len(actions),
            "prod_changes": len(actions),
            "skipped_unmatched": len(skipped_unmatched),
            "skipped_noop": len(skipped_noop),
        },
        "live_inputs": {
            "prod_stubs_before": befores,
            "skipped_unmatched": skipped_unmatched,
            "skipped_noop": skipped_noop,
        },
        "actions": actions,
    }

    out_str = json.dumps(plan, indent=2, ensure_ascii=False)
    if args.out:
        pathlib.Path(args.out).write_text(out_str)
        print(f"plan-stub-pool: written {args.out} "
              f"({len(actions)} step(s), {len(skipped_noop)} noop, "
              f"{len(skipped_unmatched)} unmatched)", file=sys.stderr)
    else:
        print(out_str)
    return 0


# --------------------------------------------------------------------------
# Stage 3d — plan-concurrency-mirror (the prod stub concurrency cascade)
# --------------------------------------------------------------------------

def _normalize_base_url(base_url: str) -> str:
    """Strip scheme + trailing slash so a stub's credentials.base_url can be matched
    against an edge's bare ``domain`` (e.g. ``https://api-us1.tokenkey.dev/`` →
    ``api-us1.tokenkey.dev``). No slug parsing — the whole host is the key."""
    s = (base_url or "").strip()
    for scheme in ("https://", "http://"):
        if s.startswith(scheme):
            s = s[len(scheme):]
            break
    return s.rstrip("/")


def _build_domain_to_edge(
    edge_matrix: dict,
    ls_targets: dict[str, dict] | None = None,
) -> dict[str, str]:
    """Authoritative prod-stub↔edge link from EC2 + Lightsail edge matrices.

    Map each edge's ``domain`` to its edge id. Lightsail-only edges (e.g. uk1
    after EC2 decommission) contribute from the Lightsail matrix; EC2-only
    edges (e.g. us1 before cutover) come from edge-targets.json. Never inferred
    from account names or ad-hoc slugs."""
    out: dict[str, str] = {}
    for eid, tgt in (edge_matrix.get("targets") or {}).items():
        dom = _normalize_base_url(tgt.get("domain") or "")
        if dom:
            out[dom] = eid
    for eid, tgt in (ls_targets or {}).items():
        dom = _normalize_base_url(tgt.get("domain") or "")
        if dom:
            out[dom] = eid
    return out


def cmd_plan_concurrency_mirror(args: argparse.Namespace) -> int:
    """Surface C planner — the four-hop schedulable-concurrency cascade.

    Hop 1 (edge account tier) is surface A's job; this planner aligns hops 2-4:
      2. each deployable edge's ``users.id=1`` → that edge's Σ schedulable
         anthropic concurrency  (action.kind = edge_operator_concurrency)
      3. each prod mirror stub's ``concurrency`` → the Σ schedulable of the edge
         its base_url points at  (folded into one prod_concurrency_mirror action)
      4. prod's ``users.id=1`` → prod's Σ schedulable, computed after hop 3 in the
         same prod transaction  (same prod_concurrency_mirror action)

    Edge resolution for hop 3 is purely from edge-targets.json ``domain`` — no
    name/slug inference. Idempotent: a second run after apply is noop. Safety
    rail: an edge whose Σ schedulable is 0 is skipped loud for hop 3 (we never
    write a stub concurrency of 0)."""
    snap = _load_snapshot_or_die(args.snapshot)
    edge_matrix = load_json_file(EDGE_MATRIX, "edge matrix")
    ls_targets = _EDGE_ROUTING.load_lightsail_targets(REPO_ROOT)
    domain_to_edge = _build_domain_to_edge(edge_matrix, ls_targets)
    force = bool(getattr(args, "force_template_rewrite", False))

    actions: list[dict] = []
    edge_synced: list[dict] = []
    edge_skipped: list[dict] = []

    # --- hop 2: edge operator concurrency ---
    for edge_id, edge in sorted(snap.get("edges", {}).items()):
        if edge.get("error") or edge.get("skipped_reason") or not edge.get("deployable"):
            continue
        sched_sum = edge.get("schedulable_concurrency_sum")
        op_cur = edge.get("operator_user_concurrency")
        if sched_sum is None or op_cur is None:
            edge_skipped.append({"edge_id": edge_id,
                                 "reason": "snapshot lacks operator/schedulable concurrency "
                                           "(re-run snapshot with v4+)"})
            continue
        if sched_sum == 0:
            edge_skipped.append({"edge_id": edge_id,
                                 "reason": "edge Σ schedulable anthropic = 0; refusing to "
                                           "drive operator concurrency to 0"})
            continue
        if op_cur == sched_sum and not force:
            continue
        step = len(actions) + 1
        actions.append({
            "step": step,
            "kind": "edge_operator_concurrency",
            "target": {"env": "edge", "edge_id": edge_id},
            "sql_source": "rendered-from-live-schedulable-concurrency",
            "variables": {"edge_id": edge_id, "schedulable_concurrency_sum": sched_sum},
            "expected_after": {"operator_user_concurrency": sched_sum},
        })
        edge_synced.append({"edge_id": edge_id, "before": op_cur, "after": sched_sum})

    # --- hop 3+4: prod stub concurrency mirror + prod operator ---
    prod = snap.get("prod") or {}
    prod_skipped_unmatched: list[dict] = []
    prod_skipped_zero: list[dict] = []
    stub_updates: list[dict] = []
    stub_befores: list[dict] = []
    if prod.get("error") or prod.get("skipped_reason"):
        fail(f"snapshot.prod not captured: {prod.get('error') or prod.get('skipped_reason')}; "
             "re-run snapshot without --skip-prod")

    prod_live_sum = prod.get("schedulable_concurrency_sum")
    prod_op_cur = prod.get("operator_user_concurrency")
    if prod_live_sum is None or prod_op_cur is None:
        fail("snapshot.prod lacks operator_user_concurrency / schedulable_concurrency_sum; "
             "re-run snapshot with v4+")

    delta = 0  # change to prod Σ schedulable from the stub concurrency writes
    for stub in prod.get("anthropic_stubs", []):
        dom = _normalize_base_url(stub.get("cred_base_url") or "")
        edge_id = domain_to_edge.get(dom)
        if not edge_id:
            prod_skipped_unmatched.append({
                "id": stub.get("id"), "name": stub.get("name"),
                "cred_base_url": stub.get("cred_base_url"),
                "reason": "base_url does not match any edge domain in edge-targets.json",
            })
            continue
        edge = snap.get("edges", {}).get(edge_id) or {}
        edge_sum = edge.get("schedulable_concurrency_sum")
        if edge_sum is None:
            prod_skipped_unmatched.append({
                "id": stub.get("id"), "name": stub.get("name"),
                "cred_base_url": stub.get("cred_base_url"), "matched_edge": edge_id,
                "reason": f"edge {edge_id} not snapshotted with schedulable sum (planned/skipped?)",
            })
            continue
        if edge_sum == 0:
            prod_skipped_zero.append({
                "id": stub.get("id"), "name": stub.get("name"), "matched_edge": edge_id,
                "reason": f"edge {edge_id} Σ schedulable = 0; refusing to write stub concurrency 0",
            })
            continue
        cur_conc = stub.get("concurrency")
        if cur_conc != edge_sum or force:
            stub_updates.append({"id": stub.get("id"), "name": stub.get("name"),
                                 "concurrency": edge_sum, "matched_edge": edge_id})
            stub_befores.append(_stub_before(stub))
            # Only schedulable stubs contribute to prod Σ schedulable.
            if stub.get("schedulable") is True:
                delta += edge_sum - (cur_conc or 0)

    expected_prod_operator = prod_live_sum + delta
    prod_operator_needs_change = (prod_op_cur != expected_prod_operator) or force

    if stub_updates or prod_operator_needs_change:
        step = len(actions) + 1
        # expected_after.stub_concurrency keyed by stub id (str) for verify lookup.
        exp_stub = {str(u["id"]): u["concurrency"] for u in stub_updates}
        actions.append({
            "step": step,
            "kind": "prod_concurrency_mirror",
            "target": {"env": "prod"},
            "sql_source": "rendered-from-live-schedulable-concurrency",
            "variables": {
                "stub_updates": [
                    {"id": u["id"], "name": u["name"], "concurrency": u["concurrency"],
                     "matched_edge": u["matched_edge"]}
                    for u in stub_updates
                ],
            },
            "expected_after": {
                "stub_concurrency": exp_stub,
                "operator_user_concurrency": expected_prod_operator,
            },
        })

    plan = {
        "version": PLAN_VERSION,
        "kind": "concurrency_mirror",
        "confirm_code": CONFIRM_CODE,
        "intent": {
            "scope": "edge-operator + prod-stub-concurrency + prod-operator cascade",
            "basis": "Σ schedulable=true anthropic concurrency (live, per target)",
            "topology_source": EDGE_MATRIX.name,
            "force_template_rewrite": force,
        },
        "snapshot_captured_at": snap.get("captured_at"),
        "plan_built_at": now_utc_iso(),
        "noop": len(actions) == 0,
        "summary": {
            "total_steps": len(actions),
            "edge_synced": len(edge_synced),
            "stub_updates": len(stub_updates),
            "prod_operator_change": bool(prod_operator_needs_change),
            "skipped_edges": len(edge_skipped),
            "skipped_unmatched_stubs": len(prod_skipped_unmatched),
            "skipped_zero_edges": len(prod_skipped_zero),
        },
        "live_inputs": {
            "edge_synced": edge_synced,
            "edge_skipped": edge_skipped,
            "prod_operator_before": prod_op_cur,
            "prod_operator_expected": expected_prod_operator,
            "prod_schedulable_sum_before": prod_live_sum,
            "stub_before": stub_befores,
            "skipped_unmatched_stubs": prod_skipped_unmatched,
            "skipped_zero_edges": prod_skipped_zero,
        },
        "actions": actions,
    }

    out_str = json.dumps(plan, indent=2, ensure_ascii=False)
    if args.out:
        pathlib.Path(args.out).write_text(out_str)
        print(f"plan-concurrency-mirror: written {args.out} "
              f"({len(actions)} step(s); edge_synced={len(edge_synced)}, "
              f"stub_updates={len(stub_updates)}, "
              f"unmatched={len(prod_skipped_unmatched)})", file=sys.stderr)
    else:
        print(out_str)
    return 0


def _groups_needing_claude_code_policy(anthropic_groups: list[dict], *,
                                       want_claude_code_only: bool,
                                       force: bool) -> list[dict]:
    """Return anthropic groups that still need claude_code_only aligned to policy."""
    out: list[dict] = []
    for g in anthropic_groups or []:
        cur = g.get("claude_code_only")
        if cur is not want_claude_code_only or force:
            out.append({
                "id": g.get("id"),
                "name": g.get("name"),
                "claude_code_only_before": cur,
            })
    return out


def cmd_plan_group_claude_code_only(args: argparse.Namespace) -> int:
    """Surface D — set claude_code_only=true on every anthropic group on each
    deployable edge and on prod (admin UI: Claude Code client only)."""
    snap = _load_snapshot_or_die(args.snapshot)
    policy = _load_group_claude_code_policy()
    want = bool(policy["claude_code_only"])
    force = bool(getattr(args, "force_template_rewrite", False))

    actions: list[dict] = []
    edge_changes: list[dict] = []
    prod_changes: list[dict] = []

    for edge_id, edge in sorted(snap.get("edges", {}).items()):
        if edge.get("error") or edge.get("skipped_reason") or not edge.get("deployable"):
            continue
        groups = edge.get("anthropic_groups")
        if groups is None:
            fail(f"snapshot.edges.{edge_id} lacks anthropic_groups; re-run snapshot v5+")
        need = _groups_needing_claude_code_policy(
            groups, want_claude_code_only=want, force=force,
        )
        if not need:
            continue
        step = len(actions) + 1
        actions.append({
            "step": step,
            "kind": "anthropic_group_claude_code_only",
            "target": {"env": "edge", "edge_id": edge_id},
            "sql_source": GROUP_CLAUDE_CODE_BASELINES.name,
            "variables": {
                "claude_code_only": want,
                "groups_to_fix": need,
            },
            "expected_after": {
                "anthropic_groups": {
                    str(g["id"]): {"claude_code_only": want} for g in need
                },
            },
        })
        edge_changes.append({"edge_id": edge_id, "group_count": len(need),
                             "names": [g["name"] for g in need]})

    prod = snap.get("prod") or {}
    if prod.get("error") or prod.get("skipped_reason"):
        fail(f"snapshot.prod not captured: {prod.get('error') or prod.get('skipped_reason')}; "
             "re-run snapshot without --skip-prod")
    prod_groups = prod.get("anthropic_groups")
    if prod_groups is None:
        fail("snapshot.prod lacks anthropic_groups; re-run snapshot v5+")
    prod_need = _groups_needing_claude_code_policy(
        prod_groups, want_claude_code_only=want, force=force,
    )
    if prod_need:
        step = len(actions) + 1
        actions.append({
            "step": step,
            "kind": "anthropic_group_claude_code_only",
            "target": {"env": "prod"},
            "sql_source": GROUP_CLAUDE_CODE_BASELINES.name,
            "variables": {
                "claude_code_only": want,
                "groups_to_fix": prod_need,
            },
            "expected_after": {
                "anthropic_groups": {
                    str(g["id"]): {"claude_code_only": want} for g in prod_need
                },
            },
        })
        prod_changes.append({"group_count": len(prod_need),
                             "names": [g["name"] for g in prod_need]})

    plan = {
        "version": PLAN_VERSION,
        "kind": "anthropic_group_claude_code_only",
        "confirm_code": CONFIRM_CODE,
        "intent": {
            "scope": "all-anthropic-groups-on-each-target",
            "policy_source": GROUP_CLAUDE_CODE_BASELINES.name,
            "claude_code_only": want,
            "force_template_rewrite": force,
        },
        "snapshot_captured_at": snap.get("captured_at"),
        "plan_built_at": now_utc_iso(),
        "noop": len(actions) == 0,
        "summary": {
            "total_steps": len(actions),
            "edge_targets": len(edge_changes),
            "prod_targets": 1 if prod_changes else 0,
            "groups_to_fix_total": sum(c["group_count"] for c in edge_changes)
            + sum(c["group_count"] for c in prod_changes),
        },
        "live_inputs": {
            "edge_before": edge_changes,
            "prod_before": prod_changes,
        },
        "actions": actions,
    }

    out_str = json.dumps(plan, indent=2, ensure_ascii=False)
    if args.out:
        pathlib.Path(args.out).write_text(out_str)
        print(f"plan-group-claude-code-only: written {args.out} "
              f"({len(actions)} step(s); groups_to_fix={plan['summary']['groups_to_fix_total']})",
              file=sys.stderr)
    else:
        print(out_str)
    return 0


# --------------------------------------------------------------------------
# Stage 3f — plan-edge-operator-balance (surface E)
# --------------------------------------------------------------------------

def render_edge_operator_balance_sql(
    balance: float,
    *,
    user_id: int = ADMIN_OPERATOR_USER_CONCURRENCY_SYNC_ID,
) -> str:
    """Set users.id=1 balance when below policy threshold (edge control planes only)."""
    bal_lit = f"{float(balance):.8f}".rstrip("0").rstrip(".")
    return (
        f"-- Auto-generated by manage-anthropic-config.py at {now_utc_iso()}\n"
        f"-- source of truth: {EDGE_OPERATOR_BALANCE_BASELINES.name}\n"
        f"-- kind: edge_operator_balance\n"
        "BEGIN;\n"
        "DO $$\n"
        "DECLARE rows int;\n"
        "BEGIN\n"
        f"  UPDATE users SET balance = {bal_lit}::numeric, updated_at = NOW()\n"
        f"    WHERE id = {int(user_id)} AND deleted_at IS NULL;\n"
        "  GET DIAGNOSTICS rows = ROW_COUNT;\n"
        f"  IF rows = 0 THEN RAISE EXCEPTION 'users.id={int(user_id)} not found'; END IF;\n"
        "END $$;\n"
        f"SELECT id, balance::float8 AS after_balance FROM users "
        f"WHERE id = {int(user_id)} AND deleted_at IS NULL;\n"
        "COMMIT;\n"
    )


def _invalidate_operator_balance_cache(region: str, instance_id: str, user_id: int, label: str) -> bool:
    """DEL billing:balance:{user_id} so the next read picks up the DB value."""
    shell = (
        "# Generated by manage-anthropic-config.py (post edge_operator_balance apply)\n"
        "set -u\n"
        "RC='env -u REDISCLI_AUTH sudo docker exec tokenkey-redis redis-cli'\n"
        f"echo \"=== billing_balance_del user_id={user_id} ===\"\n"
        f"$RC DEL \"billing:balance:{int(user_id)}\" || true\n"
    )
    _stdout, _cid, ok, _stderr = ssm_run_shell(region, instance_id, shell, label)
    return ok


def cmd_plan_edge_operator_balance(args: argparse.Namespace) -> int:
    """Surface E — top up edge ``users.id=1`` balance when live < min_balance_threshold."""
    snap = _load_snapshot_or_die(args.snapshot)
    policy = _load_operator_balance_policy()
    threshold = policy["min_balance_threshold"]
    default_bal = policy["default_balance"]
    force = bool(getattr(args, "force_template_rewrite", False))

    actions: list[dict] = []
    edge_changes: list[dict] = []
    skipped: list[dict] = []

    for edge_id, edge in sorted(snap.get("edges", {}).items()):
        if edge.get("error") or edge.get("skipped_reason") or not edge.get("deployable"):
            continue
        if edge.get("operator_user_exists") is False:
            fail(f"snapshot.edges.{edge_id}: users.id={policy['operator_user_id']} missing; "
                 "create admin user before balance top-up")
        live_bal = edge.get("operator_user_balance")
        if live_bal is None and "operator_user_balance" not in edge:
            fail(f"snapshot.edges.{edge_id} lacks operator_user_balance; re-run snapshot v6+")
        needs = _operator_balance_needs_top_up(live_bal, threshold=threshold)
        if not needs and not force:
            skipped.append({"edge_id": edge_id, "balance": live_bal, "reason": "already >= threshold"})
            continue
        if not needs and force:
            skipped.append({"edge_id": edge_id, "balance": live_bal, "reason": "force skipped: already >= threshold"})
            continue
        step = len(actions) + 1
        actions.append({
            "step": step,
            "kind": "edge_operator_balance",
            "target": {"env": "edge", "edge_id": edge_id},
            "sql_source": EDGE_OPERATOR_BALANCE_BASELINES.name,
            "variables": {
                "operator_user_id": policy["operator_user_id"],
                "default_balance": default_bal,
                "min_balance_threshold": threshold,
                "live_balance_before": live_bal,
            },
            "expected_after": {"operator_user_balance": default_bal},
        })
        edge_changes.append({"edge_id": edge_id, "before": live_bal, "after": default_bal})

    plan = {
        "version": PLAN_VERSION,
        "kind": "edge_operator_balance",
        "confirm_code": CONFIRM_CODE,
        "intent": {
            "scope": "deployable-edge-operator-balance-floor",
            "policy_source": EDGE_OPERATOR_BALANCE_BASELINES.name,
            "min_balance_threshold": threshold,
            "default_balance": default_bal,
            "force_template_rewrite": force,
        },
        "snapshot_captured_at": snap.get("captured_at"),
        "plan_built_at": now_utc_iso(),
        "noop": len(actions) == 0,
        "summary": {
            "total_steps": len(actions),
            "edges_to_top_up": len(edge_changes),
            "skipped_ok": len(skipped),
        },
        "live_inputs": {"edge_changes": edge_changes, "skipped": skipped},
        "actions": actions,
    }

    out_str = json.dumps(plan, indent=2, ensure_ascii=False)
    if args.out:
        pathlib.Path(args.out).write_text(out_str)
        print(f"plan-edge-operator-balance: written {args.out} "
              f"({len(actions)} step(s); skipped_ok={len(skipped)})", file=sys.stderr)
    else:
        print(out_str)
    return 0


# --------------------------------------------------------------------------
# Stage 4 — apply
# --------------------------------------------------------------------------

def render_anthropic_group_claude_code_sql(claude_code_only: bool) -> str:
    """Bulk-flip anthropic groups to the policy claude_code_only value."""
    val = "true" if claude_code_only else "false"
    return (
        f"-- Auto-generated by manage-anthropic-config.py at {now_utc_iso()}\n"
        f"-- source of truth: {GROUP_CLAUDE_CODE_BASELINES.name}\n"
        "BEGIN;\n"
        f"UPDATE groups SET claude_code_only = {val}, updated_at = NOW()\n"
        "WHERE platform = 'anthropic'\n"
        "  AND claude_code_only IS DISTINCT FROM " + val + "\n"
        "  AND deleted_at IS NULL;\n"
        "COMMIT;\n"
    )


def render_admin_operator_concurrency_sync_sql() -> str:
    """Sync ``users.concurrency`` for the operator account to the live schedulable
    Anthropic pool.

    Sums ``concurrency`` over non-soft-deleted ``accounts`` rows with
    ``platform='anthropic'`` AND ``schedulable=true`` — both ``oauth`` and
    ``api-key`` types. The ``schedulable=true`` filter matches the scheduler's own
    view: admin/diagnostic accounts parked unschedulable do not contribute serving
    capacity, so the operator default must not count them. Runs in the same
    transaction as tier-baseline SQL (surface A) and standalone for surface C
    (``edge_operator_concurrency``)."""
    uid = ADMIN_OPERATOR_USER_CONCURRENCY_SYNC_ID
    return (
        f"-- Align users.id={uid} concurrency to Σ schedulable anthropic account concurrency\n"
        "UPDATE users u SET concurrency = agg.total::int, updated_at = NOW()\n"
        "FROM (\n"
        "  SELECT COALESCE(SUM(a.concurrency), 0)::bigint AS total\n"
        "  FROM accounts a\n"
        "  WHERE a.platform = 'anthropic'\n"
        "    AND a.schedulable = true\n"
        "    AND a.deleted_at IS NULL\n"
        ") agg\n"
        f"WHERE u.id = {uid} AND u.deleted_at IS NULL;"
    )

def _inject_sql_before_commit(transaction_sql: str, fragment: str) -> str:
    """Append ``fragment`` immediately before the final ``COMMIT;``."""
    sentinel = "\nCOMMIT;"
    pos = transaction_sql.rfind(sentinel)
    if pos == -1:
        fail("generated tier SQL missing final COMMIT; cannot inject admin concurrency sync")
    sep = "\n" if fragment and not fragment.startswith("\n") else ""
    return transaction_sql[:pos] + sep + fragment.rstrip() + sentinel + transaction_sql[pos + len(sentinel):]


def render_edge_account_tier_sql(account_name: str, stability_tier: str, edge_id: str = "") -> str:
    """Render the apply SQL for one account at one tier, fully derived from the
    JSON baseline (the single source of truth). Reuses the guard's effective-baseline
    merge + generate_sql so the tier values live in exactly one file."""
    baseline_json = load_json_file(TIER_BASELINES, "tier baselines")
    effective = _GUARD.effective_baseline_for_tier(baseline_json, stability_tier)
    header = (
        f"-- Auto-generated by manage-anthropic-config.py at {now_utc_iso()}\n"
        f"-- source of truth: {TIER_BASELINES.name} (tier={stability_tier})\n"
    )
    body = _GUARD.generate_sql({"edge_id": edge_id or "(orchestrator)"}, account_name, effective)
    body = _inject_sql_before_commit(body, render_admin_operator_concurrency_sync_sql())
    return header + body


def _resolve_edge_target(edge_id: str) -> tuple[str, str, str]:
    ident = _EDGE_SSM.resolve_edge_execution_identity(REPO_ROOT, edge_id)
    return ident.region, ident.instance_id, f"edge:{edge_id}"


def _resolve_prod_target() -> tuple[str, str, str]:
    return (
        PROD_TARGET["region"],
        resolve_instance_id(PROD_TARGET["region"], PROD_TARGET["stack"]),
        PROD_TARGET["label"],
    )


def render_prod_stub_pool_sql(account_id: int, account_name: str,
                              pool_mode_enabled: bool, pool_mode_retry_count: int) -> str:
    """Render the apply SQL for one prod anthropic stub. credentials is jsonb;
    we merge two keys with ``||`` so any sibling keys (api_key, base_url, …)
    survive untouched. ``id + name + platform + type`` form the WHERE so a
    typo never silently lands on a different row. ON_ERROR_STOP=1 is set by
    the SSM wrapper. Reason embedded in a comment for audit. (No
    ``users.id=1`` concurrency-sum sync here — that runs after edge tier-baseline
    apply only; this surface does not touch concurrency.)"""
    # Defence-in-depth: name/account_id are PK-typed but we still parameterise
    # via a constant-ish SQL literal because the orchestrator owns both ends.
    # If you find yourself wanting to f"" untrusted strings here, stop and add
    # a quoting helper instead.
    if not isinstance(account_id, int):
        fail(f"render_prod_stub_pool_sql: account_id must be int, got {type(account_id).__name__}")
    if not isinstance(account_name, str) or not account_name:
        fail(f"render_prod_stub_pool_sql: account_name required, got {account_name!r}")
    # SQL-quote the name: replace ' with '' (defensive only — stub names are
    # admin-set ascii identifiers in practice).
    quoted_name = account_name.replace("'", "''")
    enabled_lit = "true" if pool_mode_enabled else "false"
    return (
        f"-- Auto-generated by manage-anthropic-config.py at {now_utc_iso()}\n"
        f"-- source of truth: {STUB_POOL_BASELINES.name}\n"
        f"-- kind: prod_stub_pool, account_id={account_id}, name='{quoted_name}'\n"
        "BEGIN;\n"
        "UPDATE accounts SET\n"
        "  credentials = credentials || jsonb_build_object(\n"
        f"    'pool_mode', {enabled_lit}::boolean,\n"
        f"    'pool_mode_retry_count', {int(pool_mode_retry_count)}::int\n"
        "  ),\n"
        "  updated_at = NOW()\n"
        f"WHERE id = {int(account_id)}\n"
        f"  AND name = '{quoted_name}'\n"
        "  AND platform = 'anthropic'\n"
        "  AND type = 'apikey'\n"
        "  AND deleted_at IS NULL\n"
        "RETURNING id, name,\n"
        "  credentials->>'pool_mode' AS after_pool_mode,\n"
        "  credentials->>'pool_mode_retry_count' AS after_retry;\n"
        "COMMIT;"
    )


def render_edge_operator_concurrency_sql(edge_id: str = "") -> str:
    """Surface C, hop 2 (standalone): align an edge's ``users.id=1`` concurrency to
    that edge's live Σ schedulable anthropic concurrency. Reuses the exact same
    helper surface A injects into its tier transaction, so there is one rule for
    the operator-concurrency value — running plan-concurrency-mirror without a tier
    apply still self-heals edge operator drift."""
    return (
        f"-- Auto-generated by manage-anthropic-config.py at {now_utc_iso()}\n"
        f"-- kind: edge_operator_concurrency, edge={edge_id or '(orchestrator)'}\n"
        "BEGIN;\n"
        f"{render_admin_operator_concurrency_sync_sql()}\n"
        "COMMIT;"
    )


def render_prod_concurrency_mirror_sql(stub_updates: list[dict]) -> str:
    """Surface C, hops 3+4 in ONE prod transaction: first set each mirror stub's
    ``concurrency`` to its edge's Σ schedulable (literal ints from the snapshot),
    then set prod ``users.id=1`` to prod's Σ schedulable — the operator sync runs
    LAST so its subquery sees the just-written stub concurrencies (authoritative,
    no Python re-sum). Ordering + atomicity matter: a crash mid-way leaves prod
    consistent (all-or-nothing).

    ``stub_updates`` items: ``{"id": int, "name": str, "concurrency": int}``.
    Per-stub WHERE pins id + name + platform + type + deleted_at so a typo can
    never land on the wrong row (same defence as render_prod_stub_pool_sql). An
    empty ``stub_updates`` is allowed: it renders the operator sync alone, which
    self-heals a prod ``users.id=1`` that drifted without any stub change."""
    parts = [
        f"-- Auto-generated by manage-anthropic-config.py at {now_utc_iso()}\n"
        f"-- kind: prod_concurrency_mirror, stub_updates={len(stub_updates)}\n"
        "BEGIN;\n"
    ]
    for upd in stub_updates:
        sid = upd.get("id")
        sname = upd.get("name")
        sconc = upd.get("concurrency")
        if not isinstance(sid, int):
            fail(f"render_prod_concurrency_mirror_sql: stub id must be int, got {sid!r}")
        if not isinstance(sname, str) or not sname:
            fail(f"render_prod_concurrency_mirror_sql: stub name required, got {sname!r}")
        if not isinstance(sconc, int) or sconc < 1:
            fail(f"render_prod_concurrency_mirror_sql: stub concurrency must be int >= 1, "
                 f"got {sconc!r} (refusing to ever write 0 — that would silently drain the stub)")
        quoted_name = sname.replace("'", "''")
        parts.append(
            f"-- stub id={sid} name='{quoted_name}' → concurrency={sconc}\n"
            "UPDATE accounts SET\n"
            f"  concurrency = {sconc},\n"
            "  updated_at = NOW()\n"
            f"WHERE id = {sid}\n"
            f"  AND name = '{quoted_name}'\n"
            "  AND platform = 'anthropic'\n"
            "  AND type = 'apikey'\n"
            "  AND deleted_at IS NULL\n"
            "RETURNING id, name, concurrency AS after_concurrency;\n"
        )
    # hop 4: prod operator sync — MUST run after the stub updates above.
    parts.append(render_admin_operator_concurrency_sync_sql() + "\n")
    parts.append("COMMIT;")
    return "".join(parts)


def _apply_group_instance_key(action: dict) -> tuple[str, str, str]:
    """Group key (region, instance_id, label) — same instance runs steps serially."""
    kind = action["kind"]
    tgt = action["target"]
    if kind == "edge_account_tier":
        edge_id = tgt["edge_id"]
        region, instance_id, label = _resolve_edge_target(edge_id)
        return region, instance_id, label
    if kind == "edge_operator_concurrency":
        edge_id = tgt["edge_id"]
        region, instance_id, label = _resolve_edge_target(edge_id)
        return region, instance_id, label
    if kind == "edge_operator_balance":
        edge_id = tgt["edge_id"]
        region, instance_id, label = _resolve_edge_target(edge_id)
        return region, instance_id, label
    if kind in ("prod_stub_pool", "prod_concurrency_mirror"):
        region, instance_id, label = _resolve_prod_target()
        return region, instance_id, label
    if kind == "anthropic_group_claude_code_only":
        if tgt.get("env") == "edge":
            edge_id = tgt["edge_id"]
            region, instance_id, label = _resolve_edge_target(edge_id)
            return region, instance_id, label
        region, instance_id, label = _resolve_prod_target()
        return region, instance_id, label
    fail(f"unknown action.kind {kind!r}")


def _execute_apply_action(action: dict, job_dir: pathlib.Path) -> dict[str, Any]:
    step = action["step"]
    kind = action["kind"]
    tgt = action["target"]
    v = action.get("variables", {})
    if kind == "edge_account_tier":
        edge_id = tgt["edge_id"]
        account_name = tgt["account_name"]
        label = f"step{step:02d}-edge-{edge_id}-{kind}-{account_name}".replace("/", "-")
        sql = render_edge_account_tier_sql(v["account_name"], v["stability_tier"], edge_id)
        region, instance_id, target_label = _resolve_edge_target(edge_id)
    elif kind == "prod_stub_pool":
        account_id = v["account_id"]
        account_name = tgt["account_name"]
        label = f"step{step:02d}-prod-{kind}-{account_name}".replace("/", "-")
        sql = render_prod_stub_pool_sql(
            int(account_id), account_name,
            bool(v["pool_mode_enabled"]), int(v["pool_mode_retry_count"]),
        )
        region, instance_id, target_label = _resolve_prod_target()
    elif kind == "edge_operator_concurrency":
        edge_id = tgt["edge_id"]
        label = f"step{step:02d}-edge-{edge_id}-{kind}".replace("/", "-")
        sql = render_edge_operator_concurrency_sql(edge_id)
        region, instance_id, target_label = _resolve_edge_target(edge_id)
    elif kind == "prod_concurrency_mirror":
        label = f"step{step:02d}-prod-{kind}".replace("/", "-")
        sql = render_prod_concurrency_mirror_sql(v.get("stub_updates") or [])
        region, instance_id, target_label = _resolve_prod_target()
    elif kind == "anthropic_group_claude_code_only":
        env = tgt.get("env")
        want = bool(v.get("claude_code_only", True))
        if env == "edge":
            edge_id = tgt["edge_id"]
            label = f"step{step:02d}-edge-{edge_id}-{kind}".replace("/", "-")
            sql = render_anthropic_group_claude_code_sql(want)
            region, instance_id, target_label = _resolve_edge_target(edge_id)
        elif env == "prod":
            label = f"step{step:02d}-prod-{kind}".replace("/", "-")
            sql = render_anthropic_group_claude_code_sql(want)
            region, instance_id, target_label = _resolve_prod_target()
        else:
            fail(f"anthropic_group_claude_code_only: unknown target.env {env!r}")
    elif kind == "edge_operator_balance":
        edge_id = tgt["edge_id"]
        label = f"step{step:02d}-edge-{edge_id}-{kind}".replace("/", "-")
        default_bal = float(v["default_balance"])
        sql = render_edge_operator_balance_sql(
            default_bal,
            user_id=int(v.get("operator_user_id", ADMIN_OPERATOR_USER_CONCURRENCY_SYNC_ID)),
        )
        region, instance_id, target_label = _resolve_edge_target(edge_id)
    else:
        fail(f"unknown action.kind {kind!r} (orchestrator handles edge_account_tier | "
             f"prod_stub_pool | edge_operator_concurrency | prod_concurrency_mirror | "
             f"anthropic_group_claude_code_only | edge_operator_balance)")

    sql_path = job_dir / f"{label}.sql"
    sql_path.write_text(sql)
    sql_b64 = base64.b64encode(sql.encode("utf-8")).decode("ascii")
    print(f"apply: step{step:02d} {kind} → {target_label}  (sql={sql_path})",
          file=sys.stderr)
    stdout, cid, ssm_ok, stderr = ssm_run_sql_b64(
        region, instance_id, sql_b64,
        f"apply step {step} {kind} on {target_label}",
    )
    result: dict[str, Any] = {
        "step": step,
        "kind": kind,
        "target_label": target_label,
        "sql_path": str(sql_path),
        "ssm_command_id": cid,
        "ssm_ok": ssm_ok,
        "stdout_preview": stdout[-1200:],
        "stderr_preview": stderr,
        "expected_after": action.get("expected_after"),
        "ok": ssm_ok,
    }
    if not ssm_ok:
        result["error"] = (
            "SSM ResponseCode != 0; remote SQL likely failed (DO-block RAISE or "
            "psql ON_ERROR_STOP). Inspect via: "
            f"aws ssm get-command-invocation --region {region} "
            f"--instance-id {instance_id} --command-id {cid}"
        )
    if result["ok"] and kind == "edge_operator_balance":
        uid = int(v.get("operator_user_id", ADMIN_OPERATOR_USER_CONCURRENCY_SYNC_ID))
        cache_ok = _invalidate_operator_balance_cache(
            region, instance_id, uid,
            f"apply step {step} edge_operator_balance cache flush on {target_label}",
        )
        result["billing_cache_invalidate_ok"] = cache_ok
        if not cache_ok:
            result["ok"] = False
            result["error"] = "billing:balance Redis DEL failed after balance UPDATE"
    if not result["ok"]:
        print(f"apply: step{step:02d} FAILED. cid={cid}", file=sys.stderr)
    return result


def _execute_apply_group(actions: list[dict], job_dir: pathlib.Path) -> list[dict]:
    """Serial apply on one Postgres instance (one edge or prod)."""
    ordered = sorted(actions, key=lambda a: a["step"])
    results: list[dict] = []
    for action in ordered:
        result = _execute_apply_action(action, job_dir)
        results.append(result)
        if not result["ok"]:
            break
    return results


def cmd_apply(args: argparse.Namespace) -> int:
    plan_path = pathlib.Path(args.plan)
    plan = load_json_file(plan_path, "plan")
    if plan.get("version") != PLAN_VERSION:
        fail(f"plan version {plan.get('version')} != expected {PLAN_VERSION}")
    if args.confirm != CONFIRM_CODE:
        fail(
            f"--confirm mismatch.\n  Got:      {args.confirm!r}\n  Required: {CONFIRM_CODE!r}",
            code=2,
        )

    job_dir = pathlib.Path(args.job_dir) if args.job_dir else pathlib.Path(
        f"/tmp/anthropic-apply-{_dt.datetime.now().strftime('%Y%m%d-%H%M%S')}-{os.getpid()}"
    )
    job_dir.mkdir(parents=True, exist_ok=True)
    print(f"apply: job_dir={job_dir}", file=sys.stderr)

    actions = sorted(plan.get("actions") or [], key=lambda a: a["step"])
    groups: dict[tuple[str, str, str], list[dict]] = defaultdict(list)
    for action in actions:
        groups[_apply_group_instance_key(action)].append(action)

    workers = _parallel_edges_workers(getattr(args, "parallel_edges", None))
    group_items = list(groups.items())
    if len(group_items) > 1 and workers > 1:
        apply_workers = min(workers, len(group_items))
        print(
            f"apply: {len(actions)} step(s) in {len(group_items)} instance group(s) "
            f"parallel_workers={apply_workers}",
            file=sys.stderr,
        )
        group_results = _run_parallel_ordered(
            group_items,
            lambda item: _execute_apply_group(item[1], job_dir),
            apply_workers,
            label="apply",
        )
    else:
        group_results = [
            _execute_apply_group(item[1], job_dir) for item in group_items
        ]

    results: list[dict] = []
    for gr in group_results:
        results.extend(gr)
    results.sort(key=lambda r: r["step"])

    success = all(r["ok"] for r in results) and len(results) == len(actions)
    report = {
        "version": 2,
        "applied_at": now_utc_iso(),
        "job_dir": str(job_dir),
        "plan_path": str(plan_path),
        "plan_kind": plan.get("kind"),
        "intent": plan.get("intent"),
        "total_steps": len(actions),
        "completed_steps": len(results),
        "success": success,
        "results": results,
    }
    runtime_sync_report: dict | None = None
    if success and getattr(args, "sync_runtime", False):
        ua_version = _canonical_claude_code_ua_version(
            getattr(args, "sync_runtime_ua_version", None)
        )
        rt_targets = _runtime_sync_targets_from_plan(
            plan,
            include_prod=not bool(getattr(args, "skip_prod_runtime_sync", False)),
        )
        if rt_targets:
            manifest_json = _http_mimicry_manifest_json()
            print(
                f"apply: sync-runtime targets={rt_targets} ua={ua_version} "
                f"cc={json.loads(manifest_json).get('cc_version')}",
                file=sys.stderr,
            )
            rt_ok, rt_results = _run_runtime_sync(
                rt_targets,
                ua_version,
                job_dir,
                mimicry_manifest_json=manifest_json,
                parallel_edges=getattr(args, "parallel_edges", None),
            )
            runtime_sync_report = {
                "ua_version": ua_version,
                "http_mimicry_manifest": json.loads(manifest_json),
                "targets": rt_targets,
                "success": rt_ok,
                "results": rt_results,
            }
            report["runtime_sync"] = runtime_sync_report
            if not rt_ok:
                success = False
                report["success"] = False
    out_str = json.dumps(report, indent=2, ensure_ascii=False)
    (job_dir / "apply-report.json").write_text(out_str)
    if args.json:
        print(out_str)
    else:
        print(f"apply: success={success} completed={len(results)}/{len(actions)} job_dir={job_dir}")
        for r in results:
            tag = "OK" if r["ok"] else "FAIL"
            print(f"  [{tag}] step{r['step']:02d} {r['kind']} → {r['target_label']}  cid={r['ssm_command_id']}")
    return 0 if success else 1


# --------------------------------------------------------------------------
# Stage 5 — verify
# --------------------------------------------------------------------------

def cmd_verify(args: argparse.Namespace) -> int:
    plan_path = pathlib.Path(args.plan)
    plan = load_json_file(plan_path, "plan")

    snap_path = pathlib.Path(args.snapshot_out) if args.snapshot_out else pathlib.Path(
        f"/tmp/anthropic-verify-snap-{_dt.datetime.now().strftime('%Y%m%d-%H%M%S')}.json"
    )
    print(f"verify: capturing fresh snapshot → {snap_path}", file=sys.stderr)
    plan_needs_prod = any(
        a.get("kind") in (
            "prod_stub_pool",
            "prod_concurrency_mirror",
            "anthropic_group_claude_code_only",
        )
        for a in (plan.get("actions") or [])
    )
    snap_args = argparse.Namespace(
        out=str(snap_path),
        allow_planned=args.allow_planned,
        skip_prod=not plan_needs_prod and bool(getattr(args, "skip_prod", False)),
        parallel_edges=getattr(args, "parallel_edges", None),
    )
    rc = cmd_snapshot(snap_args)
    if rc != 0:
        fail(f"verify: re-snapshot exited {rc}")
    snap = load_json_file(snap_path, "verify snapshot")

    drift: list[dict] = []
    for action in plan.get("actions") or []:
        kind = action.get("kind")
        tgt = action["target"]
        exp = action.get("expected_after") or {}
        live: dict | None = None
        diffs: list[str] = []
        if kind == "edge_account_tier":
            edge = snap.get("edges", {}).get(tgt["edge_id"], {})
            for a in edge.get("oauth_accounts", []):
                if a.get("name") == tgt["account_name"]:
                    live = a
                    break
            if live is None:
                diffs.append("target not found in live snapshot")
            else:
                for k, want in exp.items():
                    if live.get(k) != want:
                        diffs.append(f"{k}: live={live.get(k)} expected={want}")
        elif kind == "prod_stub_pool":
            prod = snap.get("prod") or {}
            if prod.get("error") or prod.get("skipped_reason"):
                diffs.append(f"verify snapshot lacks prod view: "
                             f"{prod.get('error') or prod.get('skipped_reason')}")
            else:
                # account_id is the trustworthy PK; name is also checked downstream
                # in apply WHERE, but here we match by id since it cannot collide.
                want_id = tgt.get("account_id")
                for s in prod.get("anthropic_stubs", []):
                    if s.get("id") == want_id:
                        live = s
                        break
                if live is None:
                    diffs.append("target not found in live snapshot")
                else:
                    for k, want in exp.items():
                        if live.get(k) != want:
                            diffs.append(f"{k}: live={live.get(k)} expected={want}")
        elif kind == "edge_operator_concurrency":
            edge = snap.get("edges", {}).get(tgt["edge_id"], {})
            if edge.get("error") or edge.get("skipped_reason") or not edge:
                diffs.append(f"edge {tgt['edge_id']} not in live snapshot")
            else:
                want = exp.get("operator_user_concurrency")
                got = edge.get("operator_user_concurrency")
                if got != want:
                    diffs.append(f"operator_user_concurrency: live={got} expected={want}")
        elif kind == "prod_concurrency_mirror":
            prod = snap.get("prod") or {}
            if prod.get("error") or prod.get("skipped_reason"):
                diffs.append(f"verify snapshot lacks prod view: "
                             f"{prod.get('error') or prod.get('skipped_reason')}")
            else:
                by_id = {s.get("id"): s for s in prod.get("anthropic_stubs", [])}
                for sid_str, want_conc in (exp.get("stub_concurrency") or {}).items():
                    sid = int(sid_str)
                    s = by_id.get(sid)
                    if s is None:
                        diffs.append(f"stub id={sid} not found in live prod snapshot")
                    elif s.get("concurrency") != want_conc:
                        diffs.append(f"stub id={sid} concurrency: live={s.get('concurrency')} "
                                     f"expected={want_conc}")
                want_op = exp.get("operator_user_concurrency")
                got_op = prod.get("operator_user_concurrency")
                if got_op != want_op:
                    diffs.append(f"prod operator_user_concurrency: live={got_op} expected={want_op}")
        elif kind == "anthropic_group_claude_code_only":
            exp_groups = exp.get("anthropic_groups") or {}
            if tgt.get("env") == "edge":
                view = snap.get("edges", {}).get(tgt.get("edge_id"), {})
            elif tgt.get("env") == "prod":
                view = snap.get("prod") or {}
            else:
                diffs.append(f"unknown target.env {tgt.get('env')!r}")
                view = {}
            if view.get("error") or view.get("skipped_reason"):
                diffs.append(f"verify snapshot lacks target view: "
                             f"{view.get('error') or view.get('skipped_reason')}")
            else:
                by_id = {g.get("id"): g for g in view.get("anthropic_groups", [])}
                for gid_str, want_fields in exp_groups.items():
                    gid = int(gid_str)
                    g = by_id.get(gid)
                    if g is None:
                        diffs.append(f"group id={gid} not found in live snapshot")
                        continue
                    for fk, want_val in want_fields.items():
                        if g.get(fk) != want_val:
                            diffs.append(f"group id={gid} {fk}: live={g.get(fk)} expected={want_val}")
        elif kind == "edge_operator_balance":
            edge = snap.get("edges", {}).get(tgt["edge_id"], {})
            if edge.get("error") or edge.get("skipped_reason") or not edge:
                diffs.append(f"edge {tgt['edge_id']} not in live snapshot")
            else:
                want = float(exp.get("operator_user_balance"))
                got = edge.get("operator_user_balance")
                if got is None:
                    diffs.append("operator_user_balance missing in live snapshot")
                elif abs(float(got) - want) > 1e-6:
                    diffs.append(f"operator_user_balance: live={got} expected={want}")
        else:
            diffs.append(f"verify: unknown action.kind {kind!r}")
        if diffs:
            drift.append({
                "step": action["step"],
                "kind": kind,
                "target": tgt,
                "diffs": diffs,
            })

    report = {
        "version": 2,
        "verified_at": now_utc_iso(),
        "plan_path": str(plan_path),
        "snapshot_path": str(snap_path),
        "total_actions": len(plan.get("actions") or []),
        "drift_count": len(drift),
        "drift": drift,
    }
    if args.json:
        print(json.dumps(report, indent=2, ensure_ascii=False))
    else:
        print(f"verify: drift_count={report['drift_count']}/{report['total_actions']}")
        for d in drift:
            tgt = d["target"]
            loc = " ".join(f"{k}={v}" for k, v in tgt.items() if k != "env")
            print(f"  [DRIFT] step{d['step']:02d} {d['kind']} {loc}".rstrip())
            for diff in d["diffs"]:
                print(f"      {diff}")
    return 1 if drift else 0


# --------------------------------------------------------------------------
# SQL self-check registry
#
# Every SQL-generating symbol in this module MUST appear in iter_self_check_sql()
# (with representative args) or in SELF_CHECK_EXEMPT — scripts/checks/
# ops-sql-coverage.py fails otherwise, so a new generator cannot ship without a
# real-Postgres execution test (ops/anthropic/test_ops_sql_execute.py). This is
# the fleet-wide guard for the PR #563 class: generated SQL that is syntactically
# invalid but passes mocked/substring tests.
# --------------------------------------------------------------------------
SELF_CHECK_EXEMPT: dict[str, str] = {
    "ssm_run_sql": "executes SQL over SSM, does not build it",
}


def iter_self_check_sql() -> list[tuple[str, str]]:
    """(label, rendered_sql) for every SQL generator, with representative args
    that exercise the escaping paths. Header timestamps vary per call — consumers
    validate structure / executability, never golden bytes."""
    nasty = "weird'name"
    return [
        ("EDGE_ACCOUNTS_SQL", EDGE_ACCOUNTS_SQL),
        ("PROD_STUBS_SQL", PROD_STUBS_SQL),
        ("ANTHROPIC_GROUPS_SQL", ANTHROPIC_GROUPS_SQL),
        ("TIERS_SQL", TIERS_SQL),
        ("OPERATOR_CONCURRENCY_SQL", OPERATOR_CONCURRENCY_SQL),
        ("OPERATOR_BALANCE_SQL", OPERATOR_BALANCE_SQL),
        ("SETTINGS_UA_SQL", SETTINGS_UA_SQL),
        ("REDIS_DRIFT_TLS_DB_SQL", REDIS_DRIFT_TLS_DB_SQL),
        ("REDIS_DRIFT_TIERS_DB_SQL", REDIS_DRIFT_TIERS_DB_SQL),
        ("LIVE_NODE_DB_BUNDLE_SQL", LIVE_NODE_DB_BUNDLE_SQL),
        ("EDGE_CAPTURE_BUNDLE_SQL", EDGE_CAPTURE_BUNDLE_SQL),
        ("PROD_CAPTURE_BUNDLE_SQL", PROD_CAPTURE_BUNDLE_SQL),
        ("render_edge_operator_balance_sql", render_edge_operator_balance_sql(123.45)),
        ("render_anthropic_group_claude_code_sql", render_anthropic_group_claude_code_sql(True)),
        ("render_admin_operator_concurrency_sync_sql", render_admin_operator_concurrency_sync_sql()),
        ("render_edge_account_tier_sql", render_edge_account_tier_sql(nasty, "l5", "us1")),
        ("render_prod_stub_pool_sql", render_prod_stub_pool_sql(42, nasty, True, 3)),
        ("render_edge_operator_concurrency_sql", render_edge_operator_concurrency_sql("us1")),
        ("render_prod_concurrency_mirror_sql",
         render_prod_concurrency_mirror_sql([{"id": 42, "name": nasty, "concurrency": 10}])),
    ]


# --------------------------------------------------------------------------
# Dispatch
# --------------------------------------------------------------------------

def _add_parallel_edges_arg(sp: argparse.ArgumentParser) -> None:
    sp.add_argument(
        "--parallel-edges",
        type=int,
        default=None,
        metavar="N",
        help=(
            f"max concurrent edge/target workers for SSM I/O "
            f"(default {DEFAULT_PARALLEL_EDGES})"
        ),
    )


def _add_legacy_guard_arg(sp: argparse.ArgumentParser) -> None:
    sp.add_argument(
        "--legacy-guard",
        action="store_true",
        help="use per-account subprocess guard (slow); default is one batch SSM per edge",
    )


def main() -> int:
    ap = argparse.ArgumentParser(
        description=__doc__.split("\n\n", 1)[0] if __doc__ else "",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    sub = ap.add_subparsers(dest="cmd", required=True)

    sp = sub.add_parser("snapshot",
                        help="pull each deployable edge's anthropic OAuth accounts + prod anthropic api-key stubs into one JSON")
    sp.add_argument("--out", help="write snapshot JSON to this path (otherwise stdout)")
    sp.add_argument("--allow-planned", action="store_true",
                    help="include planned edges from merged EC2 + Lightsail matrix keys")
    sp.add_argument("--skip-prod", action="store_true",
                    help="skip the prod stub query (offline / lab runs that only need edge data)")
    _add_parallel_edges_arg(sp)
    sp.set_defaults(handler=cmd_snapshot)

    sp = sub.add_parser("check", help="run edge OAuth stability guard for each edge in scope")
    sp.add_argument("--snapshot", help="snapshot JSON path; used to discover edge IDs in scope")
    sp.add_argument("--allow-planned", action="store_true",
                    help="expand edge IDs in scope via merged matrices (see snapshot)")
    sp.add_argument("--json", action="store_true")
    _add_parallel_edges_arg(sp)
    _add_legacy_guard_arg(sp)
    sp.set_defaults(handler=cmd_check)

    sp = sub.add_parser("plan-edge-account-tier",
                        help="declare an edge OAuth tier change; emit plan JSON")
    sp.add_argument("--edge-id", "--edge", dest="edge_id", required=True)
    sp.add_argument("--account-name", "--account", dest="account_name", required=True)
    sp.add_argument("--tier", required=True, help="l1 / l2 / l3 / l4 / l5")
    sp.add_argument("--snapshot", required=True)
    sp.add_argument("--out", help="write plan JSON (otherwise stdout)")
    sp.add_argument(
        "--force-template-rewrite",
        action="store_true",
        help=(
            "skip the fields_match noop short-circuit and always emit an action. "
            "Required when re-applying the same tier to overwrite credentials-side "
            "fields (e.g. temp_unschedulable_rules) that snapshot/verify do not "
            "track but the apply template rewrites unconditionally."
        ),
    )
    sp.set_defaults(handler=cmd_plan_edge_account_tier)

    sp = sub.add_parser(
        "plan-tier-bump",
        help="re-apply a tier's baseline to every account on that tier (one multi-action plan)")
    sp.add_argument("--tier", required=True, help="l1 / l2 / l3 / l4 / l5")
    sp.add_argument("--snapshot", required=True)
    sp.add_argument("--out", help="write plan JSON (otherwise stdout)")
    sp.add_argument(
        "--force-template-rewrite",
        action="store_true",
        help=(
            "include accounts whose live fields already match the baseline "
            "(otherwise they are skipped as no-ops). Use when only credentials-side "
            "fields changed."
        ),
    )
    sp.set_defaults(handler=cmd_plan_tier_bump)

    sp = sub.add_parser(
        "plan-stub-pool",
        help="enable pool_mode on every prod anthropic stub matching the base_url policy")
    sp.add_argument("--snapshot", required=True)
    sp.add_argument("--out", help="write plan JSON (otherwise stdout)")
    sp.add_argument(
        "--force-template-rewrite",
        action="store_true",
        help=(
            "skip the live-fields-match noop short-circuit and re-emit an action "
            "for every matching stub. Use if you suspect the snapshot is stale "
            "or want to rewrite credentials regardless of the live JSONB shape."
        ),
    )
    sp.set_defaults(handler=cmd_plan_stub_pool)

    sp = sub.add_parser(
        "plan-concurrency-mirror",
        help="align edge operator + prod stub + prod operator concurrency to live "
             "Σ schedulable anthropic (the four-hop capacity cascade)")
    sp.add_argument("--snapshot", required=True)
    sp.add_argument("--out", help="write plan JSON (otherwise stdout)")
    sp.add_argument(
        "--force-template-rewrite",
        action="store_true",
        help=(
            "skip the already-aligned noop short-circuit and emit actions for every "
            "edge/stub regardless of current concurrency. Use to force a re-sync."
        ),
    )
    sp.set_defaults(handler=cmd_plan_concurrency_mirror)

    sp = sub.add_parser(
        "plan-group-claude-code-only",
        help="set claude_code_only=true on every anthropic group (Claude Code client only)",
    )
    sp.add_argument("--snapshot", required=True)
    sp.add_argument("--out", help="write plan JSON (otherwise stdout)")
    sp.add_argument(
        "--force-template-rewrite",
        action="store_true",
        help="re-emit actions even when live groups already have claude_code_only=true",
    )
    sp.set_defaults(handler=cmd_plan_group_claude_code_only)

    sp = sub.add_parser(
        "plan-edge-operator-balance",
        help="top up edge users.id=1 balance when below policy min_balance_threshold",
    )
    sp.add_argument("--snapshot", required=True)
    sp.add_argument("--out", help="write plan JSON (otherwise stdout)")
    sp.add_argument(
        "--force-template-rewrite",
        action="store_true",
        help="reserved; no-op when balance already >= threshold (use plan only when check reports violation)",
    )
    sp.set_defaults(handler=cmd_plan_edge_operator_balance)

    sp = sub.add_parser("apply",
                        help="execute a plan: render apply SQL from JSON, run via SSM")
    sp.add_argument("--plan", required=True)
    sp.add_argument("--confirm", required=True,
                    help=f"must be exactly: {CONFIRM_CODE}")
    sp.add_argument("--job-dir", help="where to write rendered SQL + apply-report.json")
    sp.add_argument("--json", action="store_true")
    sp.add_argument(
        "--sync-runtime",
        action="store_true",
        help=(
            "after a successful apply, upsert claude_code_user_agent_version + "
            "claude_code_http_mimicry_manifest in settings and DEL "
            "Redis fingerprint:{oauth_account_id} on affected edge targets "
            "(from edge_account_tier actions) plus prod by default"
        ),
    )
    sp.add_argument(
        "--skip-prod-runtime-sync",
        action="store_true",
        help="with --sync-runtime, skip prod (edges only)",
    )
    sp.add_argument(
        "--sync-runtime-ua-version",
        help="override UA semver for sync-runtime (default: parse tk_canonical_cc_oauth.json)",
    )
    _add_parallel_edges_arg(sp)
    sp.set_defaults(handler=cmd_apply)

    sp = sub.add_parser(
        "plan-http-mimicry-sync",
        help="emit audit plan for HTTP mimicry runtime sync (apply via sync-runtime)",
    )
    sp.add_argument("--out", help="write plan JSON (otherwise stdout)")
    sp.set_defaults(handler=cmd_plan_http_mimicry_sync)

    sp = sub.add_parser(
        "sync-runtime",
        help=(
            "upsert claude_code_user_agent_version + claude_code_http_mimicry_manifest "
            "+ flush Redis OAuth fingerprint cache"
        ),
    )
    sp.add_argument(
        "--target",
        required=True,
        help="prod | edge:<id> | all-deployable-and-prod",
    )
    sp.add_argument(
        "--snapshot",
        help="required when --target all-deployable-and-prod",
    )
    sp.add_argument("--ua-version", help="override semver (default: tk_canonical_cc_oauth.json)")
    sp.add_argument("--job-dir", help="write rendered remote shell scripts here")
    sp.add_argument("--out", help="write JSON report")
    sp.add_argument("--json", action="store_true")
    _add_parallel_edges_arg(sp)
    sp.set_defaults(handler=cmd_sync_runtime)

    sp = sub.add_parser(
        "plan-guard-drift-fix",
        help="emit one force-template-rewrite plan for every account with guard status=drift",
    )
    sp.add_argument("--snapshot", required=True)
    sp.add_argument(
        "--check-report",
        help="use an existing check JSON; default re-runs guards for snapshot edges",
    )
    sp.add_argument("--allow-planned", action="store_true")
    sp.add_argument("--out", help="write plan JSON")
    sp.set_defaults(handler=cmd_plan_guard_drift_fix)

    sp = sub.add_parser(
        "remediate-guard-drift",
        help="snapshot → check → plan-guard-drift-fix → apply --sync-runtime → verify → check",
    )
    sp.add_argument("--confirm", required=True, help=f"must be exactly: {CONFIRM_CODE}")
    sp.add_argument("--job-dir", help="scratch dir for snap/check/plan/apply artifacts")
    sp.add_argument("--allow-planned", action="store_true")
    sp.add_argument(
        "--dry-run",
        action="store_true",
        help="stop after plan-guard-drift-fix (no apply)",
    )
    sp.add_argument(
        "--skip-runtime-sync",
        action="store_true",
        help="apply without --sync-runtime post-step",
    )
    sp.add_argument(
        "--skip-prod-runtime-sync",
        action="store_true",
        help="sync-runtime on edges only (omit prod)",
    )
    _add_parallel_edges_arg(sp)
    _add_legacy_guard_arg(sp)
    sp.set_defaults(handler=cmd_remediate_guard_drift)

    sp = sub.add_parser("verify",
                        help="re-snapshot and compare every action's expected_after vs live")
    sp.add_argument("--plan", required=True)
    sp.add_argument("--snapshot-out", help="path to write the fresh snapshot used for verify")
    sp.add_argument("--allow-planned", action="store_true",
                    help="re-snapshot using merged-matrix planned-edge inclusion rule")
    sp.add_argument("--skip-prod", action="store_true",
                    help="skip the re-snapshot's prod query when the plan has no prod_stub_pool actions")
    sp.add_argument("--json", action="store_true")
    _add_parallel_edges_arg(sp)
    sp.set_defaults(handler=cmd_verify)

    args = ap.parse_args()
    return args.handler(args)


if __name__ == "__main__":
    sys.exit(main())
