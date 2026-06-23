#!/usr/bin/env bash
# probe-admin-ui-perf.sh — read-only admin UI/access latency aggregation.
set -euo pipefail

SINCE="${SINCE:-24h}"
CONTAINER="${CONTAINER:-tokenkey}"
TOP_LIMIT="${TOP_LIMIT:-80}"
SLOW_LIMIT="${SLOW_LIMIT:-30}"

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

docker logs "$CONTAINER" --since "$SINCE" >"$tmp" 2>&1 || true

python3 - "$tmp" "$SINCE" "$TOP_LIMIT" "$SLOW_LIMIT" "$CONTAINER" <<'PY'
import json
import re
import sys
from collections import Counter, defaultdict
from urllib.parse import urlsplit

log_path, since, top_limit_s, slow_limit_s, container = sys.argv[1:6]
top_limit = int(top_limit_s)
slow_limit = int(slow_limit_s)

json_re = re.compile(r"\{.*\}")
rows = []
asset_rows = []

def norm_path(path: str) -> str:
    if not path:
        return ""
    path = urlsplit(path).path
    if path.startswith("/api/v1/admin/edge-accounts/"):
        parts = path.split("/")
        if len(parts) >= 7:
            parts[5] = ":edge"
            if len(parts) >= 8 and parts[6] == "accounts":
                parts[7] = ":account"
            return "/".join(parts)
    patterns = [
        (r"/api/v1/admin/users/\d+/api-keys$", "/api/v1/admin/users/:id/api-keys"),
        (r"/api/v1/admin/users/\d+/usage$", "/api/v1/admin/users/:id/usage"),
        (r"/api/v1/admin/users/\d+/platform-quotas$", "/api/v1/admin/users/:id/platform-quotas"),
        (r"/api/v1/admin/users/\d+/balance-history$", "/api/v1/admin/users/:id/balance-history"),
        (r"/api/v1/admin/users/\d+$", "/api/v1/admin/users/:id"),
        (r"/api/v1/admin/accounts/\d+/usage$", "/api/v1/admin/accounts/:id/usage"),
        (r"/api/v1/admin/accounts/\d+/stats$", "/api/v1/admin/accounts/:id/stats"),
        (r"/api/v1/admin/accounts/\d+/quota$", "/api/v1/admin/accounts/:id/quota"),
        (r"/api/v1/admin/accounts/\d+$", "/api/v1/admin/accounts/:id"),
        (r"/api/v1/admin/groups/\d+/api-keys$", "/api/v1/admin/groups/:id/api-keys"),
        (r"/api/v1/admin/groups/\d+/stats$", "/api/v1/admin/groups/:id/stats"),
        (r"/api/v1/admin/groups/\d+/models-list-candidates$", "/api/v1/admin/groups/:id/models-list-candidates"),
        (r"/api/v1/admin/groups/\d+$", "/api/v1/admin/groups/:id"),
        (r"/api/v1/admin/proxies/\d+/accounts$", "/api/v1/admin/proxies/:id/accounts"),
        (r"/api/v1/admin/proxies/\d+/stats$", "/api/v1/admin/proxies/:id/stats"),
        (r"/api/v1/admin/proxies/\d+$", "/api/v1/admin/proxies/:id"),
        (r"/api/v1/admin/redeem-codes/\d+/stats$", "/api/v1/admin/redeem-codes/:id/stats"),
        (r"/api/v1/admin/redeem-codes/\d+$", "/api/v1/admin/redeem-codes/:id"),
        (r"/api/v1/admin/subscriptions/\d+/progress$", "/api/v1/admin/subscriptions/:id/progress"),
        (r"/api/v1/admin/subscriptions/\d+$", "/api/v1/admin/subscriptions/:id"),
        (r"/api/v1/admin/channels/\d+$", "/api/v1/admin/channels/:id"),
        (r"/api/v1/admin/channel-monitors/\d+/history$", "/api/v1/admin/channel-monitors/:id/history"),
        (r"/api/v1/admin/channel-monitors/\d+$", "/api/v1/admin/channel-monitors/:id"),
        (r"/api/v1/admin/channel-monitor-templates/\d+/monitors$", "/api/v1/admin/channel-monitor-templates/:id/monitors"),
        (r"/api/v1/admin/channel-monitor-templates/\d+$", "/api/v1/admin/channel-monitor-templates/:id"),
        (r"/api/v1/admin/payment/orders/\d+$", "/api/v1/admin/payment/orders/:id"),
    ]
    for pat, repl in patterns:
        if re.fullmatch(pat, path):
            return repl
    return path

def percentile(vals, pct):
    if not vals:
        return None
    vals = sorted(vals)
    idx = int(round((pct / 100) * (len(vals) - 1)))
    return vals[idx]

with open(log_path, "r", errors="replace") as f:
    for line in f:
        if "http request completed" not in line:
            continue
        m = json_re.search(line)
        if not m:
            continue
        try:
            obj = json.loads(m.group(0))
        except Exception:
            continue
        path = obj.get("path") or ""
        if not isinstance(path, str):
            continue
        lat = obj.get("latency_ms")
        status = obj.get("status_code")
        if not isinstance(lat, (int, float)) or not isinstance(status, int):
            continue
        rec = {
            "completed_at": obj.get("completed_at"),
            "request_id": obj.get("request_id"),
            "method": obj.get("method"),
            "path": path,
            "endpoint": norm_path(path),
            "status_code": status,
            "latency_ms": int(lat),
        }
        if path.startswith("/api/v1/admin"):
            rows.append(rec)
        elif path.startswith("/admin") or path.startswith("/assets/") or path in ("/", "/favicon.ico"):
            asset_rows.append(rec)

def aggregate(items):
    by_ep = defaultdict(list)
    by_status = Counter()
    for r in items:
        by_ep[(r["method"], r["endpoint"])].append(r)
        by_status[str(r["status_code"])] += 1
    out = []
    for (method, endpoint), rs in by_ep.items():
        vals = [r["latency_ms"] for r in rs]
        statuses = Counter(str(r["status_code"]) for r in rs)
        out.append({
            "method": method,
            "endpoint": endpoint,
            "count": len(rs),
            "status_counts": dict(sorted(statuses.items())),
            "p50_ms": percentile(vals, 50),
            "p90_ms": percentile(vals, 90),
            "p95_ms": percentile(vals, 95),
            "p99_ms": percentile(vals, 99),
            "max_ms": max(vals),
        })
    out.sort(key=lambda x: (x["p95_ms"] or 0, x["max_ms"] or 0, x["count"]), reverse=True)
    return out, dict(sorted(by_status.items()))

admin_agg, admin_status = aggregate(rows)
asset_agg, asset_status = aggregate(asset_rows)
slow = sorted(rows, key=lambda r: r["latency_ms"], reverse=True)[:slow_limit]
slow_assets = sorted(asset_rows, key=lambda r: r["latency_ms"], reverse=True)[:slow_limit]

print(json.dumps({
    "meta": {
        "container": container,
        "since": since,
        "admin_rows": len(rows),
        "frontend_rows": len(asset_rows),
        "top_limit": top_limit,
        "slow_limit": slow_limit,
    },
    "admin_status_counts": admin_status,
    "frontend_status_counts": asset_status,
    "admin_by_endpoint_top": admin_agg[:top_limit],
    "frontend_by_path_top": asset_agg[:40],
    "slow_admin_samples": slow,
    "slow_frontend_samples": slow_assets,
}, ensure_ascii=False, sort_keys=True))
PY
