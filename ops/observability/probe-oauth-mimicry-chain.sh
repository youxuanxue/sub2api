#!/bin/bash
# probe-oauth-mimicry-chain.sh — correlate ingress SDK UA with edge OAuth mimicry
# egress (HTTP headers + system prompt fingerprint). Read-only via SSM.
#
# Topology under test:
#   User OpenAI/Python → prod anthropic apikey passthrough → edge anthropic oauth
#   mimicry (shouldMimicClaudeCode=true) → api.anthropic.com
#
# Env:
#   WINDOW_MINUTES   usage_logs / docker log window (default 1440 = 24h)
#   LIMIT            max usage rows (default 800)
#   SINCE            docker logs --since (default 24h)
#   CONTAINER        gateway container (default auto)
#   PLATFORM         usage_logs platform filter (default anthropic)
set -u

WINDOW_MINUTES="${WINDOW_MINUTES:-1440}"
LIMIT="${LIMIT:-800}"
SINCE="${SINCE:-24h}"
CONTAINER="${CONTAINER:-auto}"
PLATFORM="${PLATFORM:-anthropic}"

python3 - "$WINDOW_MINUTES" "$LIMIT" "$SINCE" "$CONTAINER" "$PLATFORM" <<'PY'
import json
import pathlib
import re
import subprocess
import sys
from collections import Counter

window_minutes = int(sys.argv[1])
limit = int(sys.argv[2])
since = sys.argv[3]
container_arg = sys.argv[4]
platform_filter = sys.argv[5].strip().lower() or "anthropic"

json_re = re.compile(r"\{.*\}\s*$")
prompt_fp_marker = "gateway.anthropic_prompt_fingerprint"
mimic_egress_marker = "gateway.anthropic_oauth_mimic_egress"
mimic_debug_marker = "[ClaudeMimicDebug]"
upstream_marker = "UPSTREAM_FORWARD"
client_orig_marker = "CLIENT_ORIGINAL"

canonical_ua_re = re.compile(r"claude-cli/\d+\.\d+\.\d+\s+\(external,\s*cli\)", re.I)
sdk_ua_re = re.compile(r"openai/python|httpx/|python-requests/", re.I)
billing_re = re.compile(r"x-anthropic-billing-header", re.I)


def docker_inspect_exists(name: str) -> bool:
    proc = subprocess.run(
        ["docker", "inspect", name, "--format", "{{.Name}}"],
        capture_output=True,
        text=True,
        check=False,
    )
    return proc.returncode == 0


def resolve_container(container: str) -> tuple[str, list[str]]:
    notes: list[str] = []
    if container != "auto":
        return container, ["explicit"]
    active = pathlib.Path("/var/lib/tokenkey/active-color")
    if active.is_file():
        color = active.read_text(encoding="utf-8", errors="ignore").strip()
        notes.append(f"active-color={color or '<empty>'}")
        if color in ("blue", "green"):
            cand = f"tokenkey-{color}"
            if docker_inspect_exists(cand):
                return cand, notes + ["active-color container exists"]
            notes.append(f"{cand} missing")
    for cand in ("tokenkey", "tokenkey-blue", "tokenkey-green"):
        if docker_inspect_exists(cand):
            return cand, notes + [f"fallback={cand}"]
    return "tokenkey", notes + ["fallback=tokenkey-unverified"]


def psql_json(sql: str) -> list[dict]:
    cmd = [
        "docker", "exec", "tokenkey-postgres",
        "psql", "-U", "tokenkey", "-d", "tokenkey", "-X", "-A", "-t", "-c", sql,
    ]
    proc = subprocess.run(cmd, capture_output=True, text=True, check=False)
    if proc.returncode != 0:
        return [{"error": proc.stderr.strip() or "psql failed"}]
    rows = []
    for line in proc.stdout.splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            rows.append(json.loads(line))
        except json.JSONDecodeError:
            rows.append({"raw": line[:400]})
    return rows


def classify_ingress_ua(ua: str) -> str:
    u = (ua or "").lower()
    if not u:
        return "empty"
    if "claude-cli" in u or "claude-code" in u:
        return "claude_code"
    if sdk_ua_re.search(u):
        return "openai_python_sdk"
    if any(x in u for x in ("httpx", "axios", "go-http", "curl")):
        return "other_sdk"
    return "other"


interval = f"{window_minutes} minutes"
usage_sql = f"""
SELECT row_to_json(t) FROM (
  SELECT
    ul.id,
    ul.created_at,
    ul.request_id,
    ul.model,
    ul.user_agent,
    ul.ip_address AS client_ip,
    ul.account_id,
    a.name AS account_name,
    a.platform,
    a.type AS account_type,
    COALESCE(a.extra->>'enable_tls_fingerprint', '') AS enable_tls_fingerprint,
    COALESCE(tfp.name, '') AS tls_profile_name
  FROM usage_logs ul
  JOIN accounts a ON a.id = ul.account_id
  LEFT JOIN tls_fingerprint_profiles tfp
    ON tfp.id = NULLIF(a.extra->>'tls_fingerprint_profile_id', '')::bigint
  WHERE ul.created_at > now() - interval '{interval}'
    AND lower(a.platform) = '{platform_filter}'
  ORDER BY ul.id DESC
  LIMIT {limit}
) t;
"""

usage_rows = psql_json(usage_sql)
container, resolution = resolve_container(container_arg)
proc = subprocess.run(
    ["docker", "logs", container, "--since", since],
    capture_output=True,
    text=True,
    check=False,
)

prompt_fps: list[dict] = []
mimic_egress: list[dict] = []
mimic_debug: list[str] = []
upstream_samples: list[str] = []
client_orig_samples: list[str] = []

if proc.returncode == 0:
    for line in proc.stdout.splitlines():
        if prompt_fp_marker in line:
            m = json_re.search(line)
            if m:
                try:
                    prompt_fps.append(json.loads(m.group(0)))
                except json.JSONDecodeError:
                    pass
        if mimic_egress_marker in line:
            m = json_re.search(line)
            if m:
                try:
                    mimic_egress.append(json.loads(m.group(0)))
                except json.JSONDecodeError:
                    pass
        if mimic_debug_marker in line:
            mimic_debug.append(line[-2500:])
        if upstream_marker in line:
            upstream_samples.append(line[-2500:])
        if client_orig_marker in line:
            client_orig_samples.append(line[-2000:])

# Summaries
usage_summary: dict
if usage_rows and usage_rows[0].get("error"):
    usage_summary = {"error": usage_rows[0]["error"]}
else:
    ingress_class = Counter(classify_ingress_ua(r.get("user_agent") or "") for r in usage_rows)
    acct_types = Counter(r.get("account_type") or "?" for r in usage_rows)
    tls = Counter(r.get("tls_profile_name") or "(none)" for r in usage_rows)
    models = Counter(r.get("model") or "?" for r in usage_rows)
    uas = Counter((r.get("user_agent") or "(empty)") for r in usage_rows)
    oauth_rows = [r for r in usage_rows if str(r.get("account_type")).lower() == "oauth"]
    oauth_sdk = [r for r in oauth_rows if classify_ingress_ua(r.get("user_agent") or "") == "openai_python_sdk"]
    usage_summary = {
        "count": len(usage_rows),
        "platform_filter": platform_filter,
        "account_type": dict(acct_types),
        "ingress_ua_class": dict(ingress_class),
        "ingress_ua_top": uas.most_common(12),
        "tls_profile_top": tls.most_common(8),
        "model_top": models.most_common(10),
        "oauth_count": len(oauth_rows),
        "oauth_openai_python_count": len(oauth_sdk),
        "time_range": (
            (usage_rows[-1].get("created_at"), usage_rows[0].get("created_at"))
            if usage_rows
            else None
        ),
    }

def summarize_prompt_fps(rows: list[dict]) -> dict:
    if not rows:
        return {"count": 0}
    billing = sum(1 for r in rows if r.get("billing_prefix_present"))
    anchors = Counter(r.get("identity_anchor_id") or "?" for r in rows)
    normalize = Counter()
    for r in rows:
        nc = (r.get("normalize_changes") or "").strip()
        if nc:
            for part in nc.split(","):
                normalize[part.strip()] += 1
    unknown = sum(1 for r in rows if (r.get("unknown_surfaces") or "").strip())
    return {
        "count": len(rows),
        "billing_prefix_present": billing,
        "billing_prefix_rate": round(billing / len(rows), 3) if rows else 0,
        "identity_anchor_top": anchors.most_common(8),
        "normalize_changes_top": normalize.most_common(12),
        "unknown_surfaces_count": unknown,
        "samples": rows[:6],
    }


def summarize_mimic_egress(rows: list[dict]) -> dict:
    if not rows:
        return {"count": 0}
    ingress = Counter(r.get("ingress_ua_class") or "?" for r in rows)
    billing = sum(1 for r in rows if r.get("billing_prefix_present"))
    cli_ua = sum(
        1 for r in rows
        if canonical_ua_re.search(str(r.get("egress_user_agent") or ""))
    )
    stainless = sum(1 for r in rows if (r.get("egress_stainless_package_version") or "").strip())
    beta = sum(1 for r in rows if (r.get("egress_anthropic_beta") or "").strip())
    return {
        "count": len(rows),
        "ingress_ua_class_top": ingress.most_common(8),
        "billing_prefix_present": billing,
        "billing_prefix_rate": round(billing / len(rows), 3) if rows else 0,
        "canonical_cli_egress_ua": cli_ua,
        "with_stainless_pkg": stainless,
        "with_anthropic_beta": beta,
        "samples": rows[:6],
    }


def parse_mimic_debug(lines: list[str]) -> dict:
    mimic_true = 0
    canonical_ua = 0
    billing_in_preview = 0
    samples = []
    for ln in lines:
        if "mimic=true" in ln or "mimic=%t" in ln:
            mimic_true += 1
        if canonical_ua_re.search(ln):
            canonical_ua += 1
        if billing_re.search(ln):
            billing_in_preview += 1
        if len(samples) < 4:
            samples.append(ln[-1200:])
    return {
        "count": len(lines),
        "mimic_true_lines": mimic_true,
        "canonical_cli_ua_lines": canonical_ua,
        "billing_in_system_preview": billing_in_preview,
        "samples": samples,
    }


def parse_upstream_headers(lines: list[str]) -> dict:
    has_beta = 0
    has_stainless = 0
    has_cli_ua = 0
    for ln in lines:
        low = ln.lower()
        if "anthropic-beta=" in low:
            has_beta += 1
        if "x-stainless-package-version=" in low:
            has_stainless += 1
        if canonical_ua_re.search(ln):
            has_cli_ua += 1
    return {
        "count": len(lines),
        "with_anthropic_beta": has_beta,
        "with_stainless_pkg": has_stainless,
        "with_canonical_cli_ua": has_cli_ua,
        "samples": [ln[-900:] for ln in lines[-3:]],
    }


verdict = "insufficient_data"
notes: list[str] = []
egress_summary = summarize_mimic_egress(mimic_egress)
if usage_summary.get("oauth_openai_python_count", 0) > 0:
    notes.append(
        f"ingress: {usage_summary['oauth_openai_python_count']} oauth rows with OpenAI/Python UA in window"
    )
if egress_summary.get("billing_prefix_rate", 0) >= 0.8:
    notes.append("egress: oauth_mimic_egress logs show billing block + canonical headers on sampled forwards")
elif summarize_prompt_fps(prompt_fps).get("billing_prefix_rate", 0) >= 0.8:
    notes.append("egress: prompt fingerprint logs show billing block present on sampled oauth forwards")
if egress_summary.get("count", 0) > 0:
    notes.append(f"egress: {egress_summary['count']} gateway.anthropic_oauth_mimic_egress rows in docker window")
if parse_mimic_debug(mimic_debug).get("mimic_true_lines", 0) > 0:
    notes.append("egress: ClaudeMimicDebug lines confirm mimic=true upstream forwards")
if usage_summary.get("oauth_openai_python_count", 0) > 0 and egress_summary.get("count", 0) == 0 and summarize_prompt_fps(prompt_fps).get("count", 0) == 0:
    verdict = "ingress_sdk_seen_no_egress_fingerprint_logs"
elif usage_summary.get("oauth_openai_python_count", 0) > 0 and egress_summary.get("billing_prefix_rate", 0) >= 0.5:
    verdict = "mimicry_chain_complete"
elif usage_summary.get("count", 0) == 0:
    verdict = "no_platform_traffic_in_window"
else:
    verdict = "mixed_or_cc_native_dominant"

out = {
    "meta": {
        "window_minutes": window_minutes,
        "limit": limit,
        "since": since,
        "container": container,
        "container_resolution": resolution,
        "platform_filter": platform_filter,
    },
    "usage_logs_ingress": usage_summary,
    "egress_prompt_fingerprint": summarize_prompt_fps(prompt_fps),
    "egress_oauth_mimic": egress_summary,
    "egress_claude_mimic_debug": parse_mimic_debug(mimic_debug),
    "egress_upstream_forward": parse_upstream_headers(upstream_samples),
    "ingress_client_original_samples": {
        "count": len(client_orig_samples),
        "samples": [ln[-700:] for ln in client_orig_samples[-3:]],
    },
    "verdict": {
        "code": verdict,
        "notes": notes,
        "fingerprint_scope": "ingress=usage_logs.user_agent; egress=TLS profile + HTTP headers (User-Agent, anthropic-beta, x-stainless-*) + system blocks (billing/identity anchors); see gateway.anthropic_oauth_mimic_egress",
    },
}
print(json.dumps(out, separators=(",", ":"), sort_keys=True))
PY
