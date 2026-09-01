#!/bin/bash
# probe-tail-gateway-logs.sh — tail recent TokenKey gateway "http request completed"
# lines from docker logs, sanitize, emit JSON array. Read-only; runs on host via SSM.
#
# Env:
#   LIMIT       max rows (default 50)
#   SINCE       docker logs --since (default 24h)
#   UNTIL       optional docker logs --until (RFC3339 or docker duration)
#   REQUEST_IDS comma-separated request_id / client_request_id pins
#   PATH_FILTER optional exact JSON path filter (e.g. /v1/chat/completions)
#   STATUS_CODE optional exact status_code filter
#   CONNECT_HOSTS optional comma-separated api-<id>.tokenkey.dev hosts
#               to DNS+GET from the host and the app container
#               (GET /health + /v1/messages; host also POSTs empty JSON
#               over HTTP/1.1 and HTTP/2 to /v1/messages?beta=true; no auth)
#   CONTAINER   gateway container (default auto). auto resolves
#               /var/lib/tokenkey/active-color to tokenkey-blue/green and
#               falls back to the legacy tokenkey container.
#   ACTIVE_COLOR_FILE
#               active-color file path for CONTAINER=auto
#               (default /var/lib/tokenkey/active-color; test seam).
set -u

LIMIT="${LIMIT:-50}"
SINCE="${SINCE:-24h}"
UNTIL="${UNTIL:-}"
REQUEST_IDS="${REQUEST_IDS:-}"
PATH_FILTER="${PATH_FILTER:-}"
STATUS_CODE="${STATUS_CODE:-}"
MESSAGE_CONTAINS="${MESSAGE_CONTAINS:-}"
CONNECT_HOSTS="${CONNECT_HOSTS:-}"
CONTAINER="${CONTAINER:-auto}"
ACTIVE_COLOR_FILE="${ACTIVE_COLOR_FILE:-/var/lib/tokenkey/active-color}"

if [ -n "$STATUS_CODE" ]; then
  case "$STATUS_CODE" in
    *[!0-9]*) echo "[probe-tail-gateway-logs] ERROR: STATUS_CODE not int: '$STATUS_CODE'" >&2; exit 2 ;;
  esac
fi
case "$LIMIT" in
  ''|*[!0-9]*) echo "[probe-tail-gateway-logs] ERROR: LIMIT not int: '$LIMIT'" >&2; exit 2 ;;
esac
if [ -n "$REQUEST_IDS" ]; then
  case "$REQUEST_IDS" in
    *[!0-9a-fA-F,-]*) echo "[probe-tail-gateway-logs] ERROR: REQUEST_IDS must be comma-separated hex/UUID" >&2; exit 2 ;;
  esac
fi
if [ -n "$CONNECT_HOSTS" ]; then
  case "$CONNECT_HOSTS" in
    *[!a-zA-Z0-9.,-]*) echo "[probe-tail-gateway-logs] ERROR: CONNECT_HOSTS must be comma-separated hostnames" >&2; exit 2 ;;
  esac
fi

# Where the canonical resolver lives. run-probe.sh uploads
# resolve_app_container.py next to this probe (/tmp); a repo checkout has it
# under ops/lib. Exported so the python heredoc below resolves the same owner.
TK_LIB_DIR="${TK_LIB_DIR:-$(cd "$(dirname "$0")" && pwd)}"
export TK_LIB_DIR

TK_PROBE_DIR="$(cd "$(dirname "$0")" && pwd)"
export TK_PROBE_DIR UNTIL REQUEST_IDS PATH_FILTER STATUS_CODE MESSAGE_CONTAINS CONNECT_HOSTS
python3 - "$LIMIT" "$SINCE" "$CONTAINER" "$ACTIVE_COLOR_FILE" <<'PY'
import json
import os
import pathlib
import re
import subprocess
import sys
import time

limit = int(sys.argv[1])
since = sys.argv[2]
container_arg = sys.argv[3]
active_color_file = sys.argv[4]
until = os.environ.get("UNTIL", "").strip()
path_filter = os.environ.get("PATH_FILTER", "").strip()
status_code_filter = os.environ.get("STATUS_CODE", "").strip()
request_ids = [x.strip() for x in os.environ.get("REQUEST_IDS", "").split(",") if x.strip()]
request_id_set = set(request_ids)
message_contains = os.environ.get("MESSAGE_CONTAINS", "").strip()
connect_hosts = [x.strip().lower() for x in os.environ.get("CONNECT_HOSTS", "").split(",") if x.strip()]
connect_host_re = re.compile(r"^api-[a-z0-9]+\.tokenkey\.dev$")
rejected_hosts = [h for h in connect_hosts if not connect_host_re.match(h)]
if rejected_hosts:
    print(json.dumps({
        "error": "CONNECT_HOSTS not allowlisted (api-<id>.tokenkey.dev only)",
        "rejected": rejected_hosts,
    }))
    sys.exit(2)

marker = "http request completed"
json_re = re.compile(r"\{.*\}\s*$")



# Canonical resolver (ops/lib/resolve_app_container.py). run-probe.sh uploads it
# next to the probe; the repo path covers local runs. Importing beats re-deriving:
# the copies this replaced had drifted into existence-only checks that happily
# selected a STOPPED container during a blue/green window.
import importlib.util as _ilu
import os as _os

# TK_PROBE_DIR is exported by this script so the heredoc knows where it lives:
# python reading a heredoc has no __file__. run-probe.sh drops the resolver next
# to the probe on the host; the ../lib hop covers running from the repo.
_tk_probe_dir = _os.environ.get("TK_PROBE_DIR", "")
_tk_resolver_candidates = [
    pathlib.Path(_tk_probe_dir) / "resolve_app_container.py",
    pathlib.Path(_tk_probe_dir) / ".." / "lib" / "resolve_app_container.py",
    pathlib.Path("/tmp/resolve_app_container.py"),
    pathlib.Path("ops/lib/resolve_app_container.py"),
]
_spec = None
for _cand in _tk_resolver_candidates:
    if _cand.is_file():
        _spec = _ilu.spec_from_file_location("tk_resolve_app_container", str(_cand))
        break
if _spec is None:
    raise SystemExit("canonical app-container resolver not found (ops/lib/resolve_app_container.py)")
_tk = _ilu.module_from_spec(_spec)
_spec.loader.exec_module(_tk)


def resolve_container(container):
    """Thin adapter over the canonical resolver, preserving this probe's
    (name, notes) output contract. Unresolved yields (None, notes) so callers
    report unknown instead of guessing a container that may not be running."""
    return _tk.resolve(container, active_color_file=active_color_file)


container, resolution = resolve_container(container_arg)
# Unresolved is an explicit unknown. Falling through would hand None/"" to
# `docker logs`, whose empty output is indistinguishable from a quiet window.
if container is None:
    print(json.dumps({
        "error": "app container unresolved (no single running candidate)",
        "container_input": container_arg,
        "container": None,
        "container_resolution": resolution,
    }))
    sys.exit(1)


docker_cmd = ["docker", "logs", container, "--since", since]
if until:
    docker_cmd += ["--until", until]
proc = subprocess.run(
    docker_cmd,
    capture_output=True,
    text=True,
    check=False,
)
if proc.returncode != 0:
    print(json.dumps({
        "error": proc.stderr.strip() or "docker logs failed",
        "container_input": container_arg,
        "container": container,
        "container_resolution": resolution,
    }))
    sys.exit(1)

SAFE_KEYS = (
    "path",
    "model",
    "status_code",
    "latency_ms",
    "completed_at",
    "request_id",
    "client_request_id",
    "platform",
    "account_id",
    "group_id",
    "user_id",
    "api_key_id",
    "method",
    "upstream_status_code",
    "error_kind",
    "error_message",
    "billing_platform",
    "msg",
    "message",
    "error",
    "errorVerbose",
    "reason",
    "event",
    "fallback_error_response_written",
    "upstream_error_response_already_written",
)

rows = []
related = []
for line in proc.stdout.splitlines():
    if message_contains and message_contains not in line and not request_id_set:
        continue
    m = json_re.search(line)
    if not m:
        if message_contains and message_contains in line:
            related.append({"log_event": "unparsed", "raw": line[:300]})
        continue
    if not m:
        continue
    try:
        obj = json.loads(m.group(0))
    except json.JSONDecodeError:
        continue
    # Redact / trim fields that may carry secrets or huge payloads
    safe = {}
    for k in SAFE_KEYS:
        if k in obj and obj[k] is not None and obj[k] != "":
            val = obj[k]
            if isinstance(val, str) and len(val) > 240:
                val = val[:240]
            safe[k] = val
    prefix = line[:m.start()].strip()
    if prefix:
        parts = prefix.split()
        if parts:
            safe["log_event"] = parts[-1][:160]
    req = obj.get("request")
    if isinstance(req, dict):
        for src, dst in (
            ("uri", "caddy_uri"),
            ("method", "caddy_method"),
            ("proto", "caddy_proto"),
            ("remote_ip", "caddy_remote_ip"),
        ):
            if req.get(src):
                safe[dst] = req[src]
    if "status" in obj and obj["status"] not in (None, ""):
        safe.setdefault("status_code", obj["status"])
    rid = str(safe.get("request_id") or obj.get("request_id") or "")
    cid = str(safe.get("client_request_id") or obj.get("client_request_id") or "")
    if request_id_set or message_contains:
        line_hit = False
        if request_id_set:
            raw_ids = {rid, cid, str(obj.get("request_id") or ""), str(obj.get("client_request_id") or "")}
            line_hit = bool(raw_ids & request_id_set) or any(x in line for x in request_id_set)
        if message_contains and message_contains in line:
            line_hit = True
        if not line_hit:
            continue
        if marker not in line:
            related.append(safe)
            continue
    elif marker not in line:
        continue
    if path_filter and str(safe.get("path") or "") != path_filter:
        continue
    if status_code_filter and str(safe.get("status_code") or "") != status_code_filter:
        continue
    rows.append(safe)

tail = rows[-limit:] if len(rows) > limit else rows

def _psql_json_rows(sql):
    proc = subprocess.run(
        [
            "docker", "exec", "tokenkey-postgres",
            "psql", "-U", "tokenkey", "-d", "tokenkey", "-X", "-A", "-t",
            "-c", sql,
        ],
        capture_output=True,
        text=True,
        check=False,
    )
    if proc.returncode != 0:
        return {"error": (proc.stderr or proc.stdout or "psql failed").strip()}
    out = []
    for line in proc.stdout.splitlines():
        line = line.strip()
        if not line:
            continue
        try:
            out.append(json.loads(line))
        except json.JSONDecodeError:
            out.append({"raw": line})
    return out

db = None
if request_ids:
    id_list = ",".join("'" + rid.replace("'", "") + "'" for rid in request_ids)
    db = {
        "usage_logs": _psql_json_rows(
            "SELECT row_to_json(t) FROM ("
            "SELECT created_at, request_id, user_id, group_id, account_id, "
            "model, requested_model, upstream_model, duration_ms "
            "FROM usage_logs "
            f"WHERE request_id IN ({id_list}) "
            "ORDER BY created_at"
            ") t"
        ),
        "ops_error_logs": _psql_json_rows(
            "SELECT row_to_json(t) FROM ("
            "SELECT created_at, request_id, client_request_id, user_id, group_id, "
            "account_id, model, status_code, upstream_status_code, "
            "left(error_message, 160) AS error_message, "
            "inbound_endpoint, upstream_endpoint, stream, duration_ms "
            "FROM ops_error_logs "
            f"WHERE request_id IN ({id_list}) OR client_request_id IN ({id_list}) "
            "ORDER BY created_at"
            ") t"
        ),
        "accounts": _psql_json_rows(
            "SELECT row_to_json(t) FROM ("
            "SELECT a.id, a.name, a.platform, a.type, a.status, a.schedulable, "
            "a.proxy_id, left(COALESCE(a.credentials->>'base_url',''), 80) AS base_url "
            "FROM accounts a "
            "WHERE a.deleted_at IS NULL "
            "AND a.id IN ("
            "SELECT DISTINCT account_id FROM ops_error_logs "
            f"WHERE request_id IN ({id_list}) OR client_request_id IN ({id_list})"
            ") "
            "ORDER BY a.id"
            ") t"
        ),
    }

def _run_cmd(cmd, timeout=8):
    proc = subprocess.run(cmd, capture_output=True, text=True, check=False, timeout=timeout)
    out = ((proc.stdout or "") + "\n" + (proc.stderr or "")).strip()
    return proc.returncode, out[:400]


def _resolve_ips(host, via_container=None):
    cmd = ["getent", "ahosts", host]
    if via_container:
        cmd = ["docker", "exec", via_container] + cmd
    try:
        rc, text = _run_cmd(cmd)
    except (FileNotFoundError, subprocess.TimeoutExpired) as exc:
        return {"error": str(exc)[:160]}
    ips = []
    for line in text.splitlines():
        parts = line.split()
        if parts and parts[0] not in ips:
            ips.append(parts[0])
    return {"rc": rc, "ips": ips[:8], "error": None if ips else text[:160]}


def _http_get(url, via_container=None):
    return _http_probe("GET", url, via_container=via_container)


def _http_probe(method, url, via_container=None, http_version=None):
    method = method.upper()
    if via_container:
        cmd = [
            "docker", "exec", via_container,
            "wget", "-S", "-O", "/dev/null", "--timeout=5",
        ]
        if method == "POST":
            cmd += [
                "--post-data={}",
                "--header=Content-Type: application/json",
            ]
        cmd.append(url)
    else:
        cmd = [
            "curl", "-sS", "-o", "/dev/null",
            "-w", "%{http_code} %{time_total} %{errormsg} %{http_version}",
            "--max-time", "5", "-X", method,
        ]
        if http_version == "1.1":
            cmd.append("--http1.1")
        elif http_version == "2":
            cmd.append("--http2")
        if method == "POST":
            cmd += ["-H", "content-type: application/json", "--data", "{}"]
        cmd.append(url)
    started = time.monotonic()
    try:
        rc, text = _run_cmd(cmd, timeout=8)
    except (FileNotFoundError, subprocess.TimeoutExpired) as exc:
        return {"error": str(exc)[:160]}
    elapsed_ms = int((time.monotonic() - started) * 1000)
    http_code = None
    errormsg = ""
    negotiated = None
    if via_container:
        codes = re.findall(r"HTTP/\S+\s+(\d{3})\b", text)
        if codes:
            http_code = int(codes[-1])
        if rc != 0:
            errormsg = text.splitlines()[-1][:160] if text else "wget failed"
    else:
        parts = text.split()
        if parts and parts[0].isdigit():
            http_code = int(parts[0])
        if len(parts) >= 4:
            negotiated = parts[-1]
            errormsg = " ".join(parts[2:-1]).strip()
        elif len(parts) >= 3:
            errormsg = " ".join(parts[2:]).strip()
    row = {
        "rc": rc,
        "http_code": http_code,
        "elapsed_ms": elapsed_ms,
        "error": errormsg or None,
    }
    if negotiated:
        row["http_version"] = negotiated
    return row


connect = None
if connect_hosts:
    connect = {"hosts": []}
    for host in connect_hosts:
        connect["hosts"].append({
            "host": host,
            "dns_host": _resolve_ips(host),
            "dns_container": _resolve_ips(host, via_container=container),
            "gets": {
                path: {
                    "host": _http_get(f"https://{host}{path}"),
                    "container": _http_get(f"https://{host}{path}", via_container=container),
                }
                for path in ("/health", "/v1/messages")
            },
            "posts": {
                spec: _http_probe(
                    "POST",
                    f"https://{host}{path}",
                    http_version=ver,
                )
                for spec, path, ver in (
                    ("http1_messages", "/v1/messages", "1.1"),
                    ("http2_messages", "/v1/messages", "2"),
                    ("http1_messages_beta", "/v1/messages?beta=true", "1.1"),
                    ("http2_messages_beta", "/v1/messages?beta=true", "2"),
                )
            },
            "container_post_beta": _http_probe(
                "POST",
                f"https://{host}/v1/messages?beta=true",
                via_container=container,
            ),
        })
    base_urls = ",".join("'" + ("https://" + h).replace("'", "") + "'" for h in connect_hosts)
    sibling = _psql_json_rows(
        "SELECT row_to_json(t) FROM ("
        "SELECT a.id, a.name, a.platform, a.type, a.status, a.schedulable, "
        "left(COALESCE(a.credentials->>'base_url',''), 80) AS base_url, "
        "(SELECT count(*) FROM usage_logs ul "
        " WHERE ul.account_id=a.id AND ul.created_at >= now() - interval '6 hours') AS usage_6h, "
        "(SELECT max(ul.created_at) FROM usage_logs ul "
        " WHERE ul.account_id=a.id AND ul.created_at >= now() - interval '6 hours') AS last_usage_6h, "
        "(SELECT count(*) FROM ops_error_logs el "
        " WHERE el.account_id=a.id AND el.created_at >= now() - interval '6 hours' "
        "   AND el.status_code=502) AS err502_6h, "
        "(SELECT max(el.created_at) FROM ops_error_logs el "
        " WHERE el.account_id=a.id AND el.created_at >= now() - interval '6 hours' "
        "   AND el.status_code=502) AS last_err502_6h "
        "FROM accounts a "
        "WHERE a.deleted_at IS NULL "
        f"AND left(COALESCE(a.credentials->>'base_url',''), 80) IN ({base_urls}) "
        "ORDER BY a.id"
        ") t"
    )
    if db is None:
        db = {}
    db["sibling_accounts"] = sibling

out = {
    "meta": {
        "container_input": container_arg,
        "container": container,
        "container_resolution": resolution,
        "since": since,
        "until": until or None,
        "request_ids": request_ids or None,
        "message_contains": message_contains or None,
        "path_filter": path_filter or None,
        "status_code": status_code_filter or None,
        "connect_hosts": connect_hosts or None,
        "limit": limit,
        "matched_total": len(rows),
        "returned": len(tail),
    },
    "requests": tail,
}
if related:
    out["related"] = related[-limit:] if len(related) > limit else related
    out["meta"]["related_total"] = len(related)
    out["meta"]["related_returned"] = len(out["related"])
if db is not None:
    out["db"] = db
if connect is not None:
    out["connect"] = connect
print(json.dumps(out, indent=2, sort_keys=True))
PY
