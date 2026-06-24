#!/usr/bin/env bash
# probe-admin-ui-api-timing.sh — read-only local timing for selected admin APIs.
set -euo pipefail

BASE_URL="${BASE_URL:-https://api.tokenkey.dev/api/v1}"
HOST_HEADER="${HOST_HEADER:-}"
PSQL="${PSQL:-docker exec tokenkey-postgres psql -U tokenkey -d tokenkey -X -A -t}"
TIMEOUT="${TIMEOUT:-20}"

ADMIN_KEY="$($PSQL -c "SELECT value FROM settings WHERE key='admin_api_key' LIMIT 1" 2>/dev/null | head -n1 || true)"
if [ -z "$ADMIN_KEY" ]; then
  echo '{"error":"admin_api_key_not_found"}'
  exit 0
fi

python3 - "$BASE_URL" "$ADMIN_KEY" "$TIMEOUT" "$HOST_HEADER" <<'PY'
import datetime as dt
import json
import subprocess
import sys
import time
import urllib.parse
from zoneinfo import ZoneInfo

base, key, timeout_s, host_header = sys.argv[1:5]
timeout = int(timeout_s)
today = dt.datetime.now(ZoneInfo("Asia/Shanghai")).date()
week_start = today - dt.timedelta(days=6)
ui_default_start = today - dt.timedelta(days=1)

def day(d):
    return d.isoformat()

def call(label, method, path, body=None):
    url = base.rstrip("/") + path
    cmd = [
        "curl", "-sS", "-o", "/dev/null",
        "-w", "http_code=%{http_code} namelookup=%{time_namelookup} connect=%{time_connect} ttfb=%{time_starttransfer} total=%{time_total} size=%{size_download}",
        "--max-time", str(timeout),
        "-k",
        "-H", f"x-api-key: {key}",
        "-H", "Content-Type: application/json",
    ]
    if host_header:
        cmd += ["-H", f"Host: {host_header}"]
    if method != "GET":
        cmd += ["-X", method]
    if body is not None:
        cmd += ["--data", json.dumps(body, separators=(",", ":"))]
    cmd.append(url)
    start = time.time()
    try:
        cp = subprocess.run(cmd, text=True, capture_output=True, timeout=timeout + 2)
        elapsed = time.time() - start
        out = cp.stdout.strip()
        parsed = {}
        for part in out.split():
            if "=" in part:
                k, v = part.split("=", 1)
                parsed[k] = v
        return {
            "label": label,
            "method": method,
            "path": path,
            "exit_code": cp.returncode,
            "elapsed_ms": round(elapsed * 1000),
            "curl": parsed,
            "stderr": cp.stderr.strip()[:300],
        }
    except subprocess.TimeoutExpired:
        return {"label": label, "method": method, "path": path, "timeout": True}

def q(params):
    return "?" + urllib.parse.urlencode(params, doseq=True)

# Keep this small: one probe per endpoint shape, no mutating calls.
requests = [
    ("dashboard.snapshot.7d.stats-only", "GET", "/admin/dashboard/snapshot-v2" + q({
        "start_date": day(week_start),
        "end_date": day(today),
        "granularity": "day",
        "include_stats": "true",
        "include_trend": "false",
        "include_model_stats": "false",
        "include_group_stats": "false",
        "include_users_trend": "false",
        "timezone": "Asia/Shanghai",
    }), None),
    ("dashboard.snapshot.7d.trend-only", "GET", "/admin/dashboard/snapshot-v2" + q({
        "start_date": day(week_start),
        "end_date": day(today),
        "granularity": "day",
        "include_stats": "false",
        "include_trend": "true",
        "include_model_stats": "false",
        "include_group_stats": "false",
        "include_users_trend": "false",
        "timezone": "Asia/Shanghai",
    }), None),
    ("dashboard.snapshot.7d.models-only", "GET", "/admin/dashboard/snapshot-v2" + q({
        "start_date": day(week_start),
        "end_date": day(today),
        "granularity": "day",
        "include_stats": "false",
        "include_trend": "false",
        "include_model_stats": "true",
        "include_group_stats": "false",
        "include_users_trend": "false",
        "timezone": "Asia/Shanghai",
    }), None),
    ("dashboard.snapshot.7d.charts-no-users", "GET", "/admin/dashboard/snapshot-v2" + q({
        "start_date": day(week_start),
        "end_date": day(today),
        "granularity": "day",
        "include_stats": "false",
        "include_trend": "true",
        "include_model_stats": "true",
        "include_group_stats": "false",
        "include_users_trend": "false",
        "timezone": "Asia/Shanghai",
    }), None),
    ("dashboard.snapshot.7d.full", "GET", "/admin/dashboard/snapshot-v2" + q({
        "start_date": day(week_start),
        "end_date": day(today),
        "granularity": "day",
        "include_stats": "true",
        "include_trend": "true",
        "include_model_stats": "true",
        "include_group_stats": "false",
        "include_users_trend": "true",
        "users_trend_limit": "12",
        "timezone": "Asia/Shanghai",
    }), None),
    ("dashboard.snapshot.7d.users-trend-only", "GET", "/admin/dashboard/snapshot-v2" + q({
        "start_date": day(week_start),
        "end_date": day(today),
        "granularity": "day",
        "include_stats": "false",
        "include_trend": "false",
        "include_model_stats": "false",
        "include_group_stats": "false",
        "include_users_trend": "true",
        "users_trend_limit": "12",
        "timezone": "Asia/Shanghai",
    }), None),
    ("dashboard.users-ranking.7d", "GET", "/admin/dashboard/users-ranking" + q({
        "start_date": day(week_start),
        "end_date": day(today),
        "limit": "10",
        "timezone": "Asia/Shanghai",
    }), None),
    ("dashboard.models.7d", "GET", "/admin/dashboard/models" + q({
        "start_date": day(week_start),
        "end_date": day(today),
        "timezone": "Asia/Shanghai",
    }), None),
    ("usage.list.default", "GET", "/admin/usage" + q({
        "page": "1",
        "page_size": "20",
        "exact_total": "false",
        "sort_by": "created_at",
        "sort_order": "desc",
        "timezone": "Asia/Shanghai",
    }), None),
    ("usage.stats.default", "GET", "/admin/usage/stats" + q({
        "timezone": "Asia/Shanghai",
    }), None),
    ("usage.stats.summary-only", "GET", "/admin/usage/stats" + q({
        "include_endpoints": "0",
        "timezone": "Asia/Shanghai",
    }), None),
    ("usage.stats.summary-only.ui-default-range", "GET", "/admin/usage/stats" + q({
        "start_date": day(ui_default_start),
        "end_date": day(today),
        "include_endpoints": "0",
        "timezone": "Asia/Shanghai",
    }), None),
    ("usage.stats.endpoints-only", "GET", "/admin/usage/stats" + q({
        "include_summary": "0",
        "include_endpoints": "1",
        "timezone": "Asia/Shanghai",
    }), None),
    ("usage.stats.endpoints-only.ui-default-range", "GET", "/admin/usage/stats" + q({
        "start_date": day(ui_default_start),
        "end_date": day(today),
        "include_summary": "0",
        "include_endpoints": "1",
        "nocache": "1",
        "timezone": "Asia/Shanghai",
    }), None),
    ("usage.stats.endpoint-source.inbound.ui-default-range", "GET", "/admin/usage/stats" + q({
        "start_date": day(ui_default_start),
        "end_date": day(today),
        "include_summary": "0",
        "include_endpoints": "1",
        "endpoint_source": "inbound",
        "nocache": "1",
        "timezone": "Asia/Shanghai",
    }), None),
    ("usage.stats.endpoint-source.upstream.ui-default-range", "GET", "/admin/usage/stats" + q({
        "start_date": day(ui_default_start),
        "end_date": day(today),
        "include_summary": "0",
        "include_endpoints": "1",
        "endpoint_source": "upstream",
        "nocache": "1",
        "timezone": "Asia/Shanghai",
    }), None),
    ("usage.stats.endpoint-source.path.ui-default-range", "GET", "/admin/usage/stats" + q({
        "start_date": day(ui_default_start),
        "end_date": day(today),
        "include_summary": "0",
        "include_endpoints": "1",
        "endpoint_source": "path",
        "nocache": "1",
        "timezone": "Asia/Shanghai",
    }), None),
    ("usage.chart.trend-only.7d", "GET", "/admin/dashboard/snapshot-v2" + q({
        "start_date": day(week_start),
        "end_date": day(today),
        "granularity": "day",
        "include_stats": "false",
        "include_trend": "true",
        "include_model_stats": "false",
        "include_group_stats": "false",
        "include_users_trend": "false",
        "timezone": "Asia/Shanghai",
    }), None),
    ("usage.chart.groups-only.7d", "GET", "/admin/dashboard/snapshot-v2" + q({
        "start_date": day(week_start),
        "end_date": day(today),
        "granularity": "day",
        "include_stats": "false",
        "include_trend": "false",
        "include_model_stats": "false",
        "include_group_stats": "true",
        "include_users_trend": "false",
        "timezone": "Asia/Shanghai",
    }), None),
    ("usage.chart.snapshot.7d", "GET", "/admin/dashboard/snapshot-v2" + q({
        "start_date": day(week_start),
        "end_date": day(today),
        "granularity": "day",
        "include_stats": "false",
        "include_trend": "true",
        "include_model_stats": "false",
        "include_group_stats": "true",
        "include_users_trend": "false",
        "timezone": "Asia/Shanghai",
    }), None),
    ("accounts.list.lite", "GET", "/admin/accounts" + q({
        "page": "1",
        "page_size": "20",
        "lite": "1",
        "sort_by": "id",
        "sort_order": "desc",
        "timezone": "Asia/Shanghai",
    }), None),
    ("accounts.today-stats.batch.small", "POST", "/admin/accounts/today-stats/batch", {"account_ids":[1,2,3,4,5,6,7,8,9,10]}),
    ("accounts.passive-usage.batch.small", "POST", "/admin/accounts/usage/batch", {"account_ids":[1,2,3,4,5,6,7,8,9,10]}),
    ("groups.list", "GET", "/admin/groups" + q({
        "page": "1",
        "page_size": "20",
        "sort_by": "sort_order",
        "sort_order": "asc",
        "timezone": "Asia/Shanghai",
    }), None),
    ("groups.usage-summary", "GET", "/admin/groups/usage-summary" + q({
        "timezone": "Asia/Shanghai",
    }), None),
    ("groups.capacity-summary", "GET", "/admin/groups/capacity-summary" + q({
        "timezone": "Asia/Shanghai",
    }), None),
    ("edge-accounts", "GET", "/admin/edge-accounts" + q({
        "platform": "all",
        "timezone": "Asia/Shanghai",
    }), None),
    ("users.list", "GET", "/admin/users" + q({
        "page": "1",
        "page_size": "20",
        "sort_by": "id",
        "sort_order": "desc",
    }), None),
    ("user-attributes.list", "GET", "/admin/user-attributes", None),
    ("subscriptions.list", "GET", "/admin/subscriptions" + q({
        "page": "1",
        "page_size": "20",
        "sort_by": "id",
        "sort_order": "desc",
    }), None),
    ("channels.list", "GET", "/admin/channels" + q({
        "page": "1",
        "page_size": "20",
        "sort_by": "id",
        "sort_order": "desc",
    }), None),
    ("channel-types", "GET", "/admin/channel-types", None),
    ("channel-monitors.list", "GET", "/admin/channel-monitors" + q({
        "page": "1",
        "page_size": "20",
    }), None),
    ("channel-monitor-templates.list", "GET", "/admin/channel-monitor-templates" + q({
        "page": "1",
        "page_size": "20",
    }), None),
    ("announcements.list", "GET", "/admin/announcements" + q({
        "page": "1",
        "page_size": "20",
        "sort_by": "created_at",
        "sort_order": "desc",
    }), None),
    ("proxies.list", "GET", "/admin/proxies" + q({
        "page": "1",
        "page_size": "20",
        "sort_by": "id",
        "sort_order": "desc",
    }), None),
    ("proxies.all", "GET", "/admin/proxies/all", None),
    ("redeem-codes.list", "GET", "/admin/redeem-codes" + q({
        "page": "1",
        "page_size": "20",
        "sort_by": "id",
        "sort_order": "desc",
    }), None),
    ("redeem-codes.stats", "GET", "/admin/redeem-codes/stats", None),
    ("promo-codes.list", "GET", "/admin/promo-codes" + q({
        "page": "1",
        "page_size": "20",
        "sort_by": "id",
        "sort_order": "desc",
    }), None),
    ("settings", "GET", "/admin/settings", None),
    ("payment.config", "GET", "/admin/payment/config", None),
    ("payment.dashboard.7d", "GET", "/admin/payment/dashboard" + q({
        "days": "7",
    }), None),
    ("payment.orders.list", "GET", "/admin/payment/orders" + q({
        "page": "1",
        "page_size": "20",
    }), None),
    ("payment.plans", "GET", "/admin/payment/plans", None),
    ("payment.providers", "GET", "/admin/payment/providers", None),
    ("risk-control.config", "GET", "/admin/risk-control/config", None),
    ("risk-control.status", "GET", "/admin/risk-control/status", None),
    ("risk-control.logs", "GET", "/admin/risk-control/logs" + q({
        "page": "1",
        "page_size": "20",
    }), None),
    ("affiliates.users", "GET", "/admin/affiliates/users" + q({
        "page": "1",
        "page_size": "20",
    }), None),
    ("affiliates.invites", "GET", "/admin/affiliates/invites" + q({
        "page": "1",
        "page_size": "20",
        "timezone": "Asia/Shanghai",
    }), None),
    ("affiliates.rebates", "GET", "/admin/affiliates/rebates" + q({
        "page": "1",
        "page_size": "20",
        "timezone": "Asia/Shanghai",
    }), None),
    ("ops.dashboard.snapshot.1h", "GET", "/admin/ops/dashboard/snapshot-v2" + q({
        "time_range": "1h",
        "mode": "auto",
    }), None),
    ("ops.concurrency", "GET", "/admin/ops/concurrency", None),
    ("ops.account-availability", "GET", "/admin/ops/account-availability", None),
    ("ops.realtime-traffic.5m", "GET", "/admin/ops/realtime-traffic" + q({
        "window": "5m",
    }), None),
    ("ops.errors.list", "GET", "/admin/ops/errors" + q({
        "page": "1",
        "page_size": "20",
        "time_range": "1h",
    }), None),
    ("ops.requests.list", "GET", "/admin/ops/requests" + q({
        "page": "1",
        "page_size": "20",
        "time_range": "1h",
    }), None),
    ("ops.alert-rules", "GET", "/admin/ops/alert-rules", None),
    ("ops.alert-events", "GET", "/admin/ops/alert-events" + q({
        "page": "1",
        "page_size": "20",
    }), None),
    ("ops.system-logs.health", "GET", "/admin/ops/system-logs/health", None),
]

results = [call(*req) for req in requests]

def total_seconds(row):
    try:
        return float(row.get("curl", {}).get("total", "0") or 0)
    except (TypeError, ValueError):
        return 0.0

top_slow = [
    {
        "label": row.get("label"),
        "http_code": row.get("curl", {}).get("http_code"),
        "ttfb": row.get("curl", {}).get("ttfb"),
        "total": row.get("curl", {}).get("total"),
        "size": row.get("curl", {}).get("size"),
        "elapsed_ms": row.get("elapsed_ms"),
    }
    for row in sorted(results, key=total_seconds, reverse=True)[:12]
]
non_2xx = [
    {
        "label": row.get("label"),
        "http_code": row.get("curl", {}).get("http_code"),
        "total": row.get("curl", {}).get("total"),
        "elapsed_ms": row.get("elapsed_ms"),
        "stderr": row.get("stderr"),
    }
    for row in results
    if not str(row.get("curl", {}).get("http_code", "")).startswith("2")
]

print(json.dumps({"top_slow": top_slow, "non_2xx": non_2xx, "results": results}, ensure_ascii=False, sort_keys=True))
PY
