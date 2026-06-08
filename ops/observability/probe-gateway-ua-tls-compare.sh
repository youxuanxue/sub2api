#!/bin/bash
# probe-gateway-ua-tls-compare.sh — compare recent gateway request fingerprints
# (UA / TLS profile / client IP / protocol) across edges. Read-only via SSM.
#
# Env:
#   LIMIT          rows per source (default 500)
#   SINCE          docker logs window (default 48h)
#   WINDOW_MINUTES if set, usage_logs/ops filter to last N minutes (overrides LIMIT ordering scope)
#   CONTAINER      gateway container (default tokenkey)
set -u

LIMIT="${LIMIT:-500}"
SINCE="${SINCE:-48h}"
WINDOW_MINUTES="${WINDOW_MINUTES:-}"
CONTAINER="${CONTAINER:-tokenkey}"

python3 - "$LIMIT" "$SINCE" "$CONTAINER" "$WINDOW_MINUTES" <<'PY'
import json
import re
import subprocess
import sys
from collections import Counter

limit = int(sys.argv[1])
since = sys.argv[2]
container = sys.argv[3]
window_minutes = sys.argv[4].strip() if len(sys.argv) > 4 else ""
time_where_ul = ""
time_where_ops = ""
if window_minutes.isdigit() and int(window_minutes) > 0:
    interval = f"{int(window_minutes)} minutes"
    time_where_ul = f"WHERE ul.created_at > now() - interval '{interval}'"
    time_where_ops = f"AND created_at > now() - interval '{interval}'"

marker_completed = "http request completed"
json_re = re.compile(r"\{.*\}\s*$")
ua_tls_keys = (
    "user_agent",
    "request_user_agent",
    "User-Agent",
    "tls_fingerprint",
    "tls_fingerprint_profile",
    "tls_fingerprint_profile_id",
    "tls_fingerprint_profile_name",
    "tls_profile",
    "enable_tls_fingerprint",
    "ingress user_agent",
    "ingress_ua",
    "x-stainless",
    "anthropic-beta",
    "fingerprint_applied",
    "protocol",
    "client_ip",
)


def psql_json(sql: str) -> list[dict]:
    cmd = [
        "docker",
        "exec",
        "tokenkey-postgres",
        "psql",
        "-U",
        "tokenkey",
        "-d",
        "tokenkey",
        "-X",
        "-A",
        "-t",
        "-c",
        sql,
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
            rows.append({"raw": line[:500]})
    return rows


usage_sql = f"""
SELECT row_to_json(t) FROM (
  SELECT
    ul.id,
    ul.created_at,
    ul.request_id,
    ul.model,
    ul.requested_model,
    ul.user_agent,
    ul.ip_address AS client_ip,
    ul.account_id,
    ul.stream,
    ul.duration_ms,
    a.platform,
    a.type AS account_type,
    COALESCE(a.extra->>'enable_tls_fingerprint', '') AS enable_tls_fingerprint,
    COALESCE(a.extra->>'tls_fingerprint_profile_id', '') AS tls_profile_id,
    COALESCE(tfp.name, '') AS tls_profile_name
  FROM usage_logs ul
  JOIN accounts a ON a.id = ul.account_id
  -- ops-allow-soft-deleted: enrich recent requests with their account's fingerprint config; keep requests whose account was since soft-deleted (real in-window traffic)
  LEFT JOIN tls_fingerprint_profiles tfp
    ON tfp.id = NULLIF(a.extra->>'tls_fingerprint_profile_id', '')::bigint
  {time_where_ul}
  ORDER BY ul.id DESC
  LIMIT {limit}
) t;
"""

ops_window = time_where_ops or "AND created_at > now() - interval '48 hours'"
ops_sql = f"""
SELECT row_to_json(t) FROM (
  SELECT
    id,
    created_at,
    level,
    component,
    message,
    request_id,
    platform,
    model,
    extra
  FROM ops_system_logs
  WHERE 1=1
    {ops_window}
    AND (
      component ILIKE '%gateway%'
      OR component ILIKE '%http.access%'
      OR message ILIKE '%user_agent%'
      OR message ILIKE '%tls%'
      OR message ILIKE '%canonical%'
      OR extra::text ILIKE '%user_agent%'
      OR extra::text ILIKE '%tls_fingerprint%'
    )
  ORDER BY id DESC
  LIMIT {limit}
) t;
"""

usage_rows = psql_json(usage_sql)
ops_rows = psql_json(ops_sql)

proc = subprocess.run(
    ["docker", "logs", container, "--since", since],
    capture_output=True,
    text=True,
    check=False,
)
docker_err = proc.stderr.strip() if proc.returncode != 0 else ""

completed = []
ua_tls_lines = []
for line in proc.stdout.splitlines():
    if marker_completed in line:
        m = json_re.search(line)
        if m:
            try:
                completed.append(json.loads(m.group(0)))
            except json.JSONDecodeError:
                pass
    lower = line.lower()
    if any(k.lower() in lower for k in ua_tls_keys):
        ua_tls_lines.append(line[-2000:])

completed_tail = completed[-limit:]
ua_tls_tail = ua_tls_lines[-limit:]


def classify_ua(ua: str) -> str:
    u = (ua or "").lower()
    if not u:
        return "empty"
    if "claude-cli" in u or "claude-code" in u:
        return "claude-cli"
    if any(x in u for x in ("openai", "httpx", "python/", "axios", "go-http")):
        return "sdk"
    if "curl" in u:
        return "curl"
    if "mozilla" in u or "chrome" in u or "safari" in u:
        return "browser"
    return "other"


def summarize_usage(rows: list[dict]) -> dict:
    if rows and rows[0].get("error"):
        return {"error": rows[0]["error"]}
    uas = Counter((r.get("user_agent") or "(empty)") for r in rows)
    tls = Counter((r.get("tls_profile_name") or "(none)") for r in rows)
    platforms = Counter((r.get("platform") or "?") for r in rows)
    models = Counter((r.get("model") or "?") for r in rows)
    acct_types = Counter((r.get("account_type") or "?") for r in rows)
    enable_tls = Counter(str(r.get("enable_tls_fingerprint") or "") for r in rows)
    ua_class = Counter(classify_ua(r.get("user_agent") or "") for r in rows)
    client_ips = Counter((r.get("client_ip") or "(empty)") for r in rows)
    distinct_ua = sorted({u for u in uas if u != "(empty)"})
    return {
        "count": len(rows),
        "user_agent_top": uas.most_common(20),
        "distinct_user_agent_count": len(distinct_ua),
        "distinct_user_agents": distinct_ua[:30],
        "user_agent_class": dict(ua_class),
        "client_ip_top": client_ips.most_common(15),
        "tls_profile_top": tls.most_common(15),
        "platform_top": platforms.most_common(10),
        "model_top": models.most_common(10),
        "account_type_top": acct_types.most_common(10),
        "enable_tls_fingerprint": dict(enable_tls),
        "time_range": (
            (rows[-1].get("created_at"), rows[0].get("created_at"))
            if rows
            else None
        ),
    }


def extract_ops_ua_tls(rows: list[dict]) -> dict:
    if rows and rows[0].get("error"):
        return {"error": rows[0]["error"], "samples": []}
    ua_hits = []
    for r in rows:
        extra = r.get("extra") or {}
        if isinstance(extra, str):
            try:
                extra = json.loads(extra)
            except json.JSONDecodeError:
                extra = {}
        hits = {}
        blob = json.dumps({**r, "extra": extra}, ensure_ascii=False)
        for k in ua_tls_keys:
            if k in blob:
                hits[k] = True
        if hits or "user_agent" in blob or "tls" in blob.lower():
            ua_hits.append(
                {
                    "created_at": r.get("created_at"),
                    "component": r.get("component"),
                    "message": r.get("message"),
                    "request_id": r.get("request_id"),
                    "extra_keys": sorted(extra.keys()) if isinstance(extra, dict) else [],
                    "request_user_agent": extra.get("request_user_agent") if isinstance(extra, dict) else None,
                    "user_agent": extra.get("user_agent") if isinstance(extra, dict) else None,
                    "tls_fingerprint_profile_name": extra.get("tls_fingerprint_profile_name") if isinstance(extra, dict) else None,
                    "protocol": extra.get("protocol") if isinstance(extra, dict) else None,
                    "client_ip": extra.get("client_ip") if isinstance(extra, dict) else None,
                }
            )
    return {"count": len(rows), "ua_tls_hits": len(ua_hits), "samples": ua_hits[:8]}


def summarize_completed(rows: list[dict]) -> dict:
    paths = Counter(r.get("path", "?") for r in rows)
    gateway = [r for r in rows if str(r.get("path", "")).startswith(("/v1/", "/v1beta/", "/images/"))]
    protos = Counter(r.get("protocol") or "?" for r in rows)
    ips = Counter(r.get("client_ip") or "?" for r in rows)
    return {
        "count": len(rows),
        "path_top": paths.most_common(15),
        "gateway_paths": len(gateway),
        "gateway_status": dict(Counter(str(r.get("status_code")) for r in gateway)),
        "protocol_top": protos.most_common(5),
        "client_ip_top": ips.most_common(15),
    }


def sample_rows(rows: list[dict], n: int = 30) -> list[dict]:
    """Keep compact samples for SSM stdout budget."""
    if len(rows) <= n:
        return rows
    # head + tail spread
    half = n // 2
    return rows[:half] + rows[-half:]


def gateway_usage_samples(rows: list[dict], n: int = 40) -> list[dict]:
    gw = [
        r
        for r in rows
        if r.get("model") and "claude" in str(r.get("model", "")).lower()
    ]
    pick = gw[-n:] if len(gw) > n else gw
    out = []
    for r in pick:
        out.append(
            {
                "created_at": r.get("created_at"),
                "request_id": r.get("request_id"),
                "model": r.get("model"),
                "user_agent": r.get("user_agent"),
                "client_ip": r.get("client_ip"),
                "account_id": r.get("account_id"),
                "platform": r.get("platform"),
                "account_type": r.get("account_type"),
                "enable_tls_fingerprint": r.get("enable_tls_fingerprint"),
                "tls_profile_name": r.get("tls_profile_name"),
                "duration_ms": r.get("duration_ms"),
            }
        )
    return out


# SSM StandardOutputContent is ~24KiB; keep stdout compact (summary only).
out = {
    "meta": {
        "limit": limit,
        "since": since,
        "container": container,
        "window_minutes": int(window_minutes) if window_minutes.isdigit() else None,
    },
    "usage_logs": {
        "summary": summarize_usage(usage_rows),
        "gateway_samples": gateway_usage_samples(usage_rows, n=12),
    },
    "ops_system_logs": extract_ops_ua_tls(ops_rows),
    "docker_access": {
        "summary": summarize_completed(completed_tail),
        "docker_error": docker_err or None,
        "matched_total": len(completed),
    },
    "docker_ua_tls_lines": {
        "count": len(ua_tls_tail),
        "keyword_hits": Counter(
            kw
            for ln in ua_tls_tail
            for kw in ua_tls_keys
            if kw.lower() in ln.lower()
        ).most_common(20),
        "samples": [ln[-400:] for ln in ua_tls_tail[-8:]],
    },
}
# Full payload for local pull if needed (not returned via SSM).
try:
    with open("/tmp/tk-gateway-ua-tls-full.json", "w", encoding="utf-8") as fh:
        json.dump(
            {
                **out,
                "usage_logs_full_count": len(usage_rows),
                "docker_completed_count": len(completed_tail),
            },
            fh,
            indent=2,
        )
except OSError:
    pass
print(json.dumps(out, separators=(",", ":"), sort_keys=True))
PY
