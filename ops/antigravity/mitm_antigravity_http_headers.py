"""mitmproxy addon: log real Antigravity IDE HTTP requests on the cloudcode-pa
endpoints.

Sibling of ops/anthropic/mitm_cc_http_headers.py and ops/kiro/
mitm_kiro_http_headers.py. For Antigravity the HTTP layer is the load-bearing
fingerprint, so this addon is the PRIMARY capture path (not best-effort like
kiro). It records, per `v1internal:*` request:
  - the on-wire User-Agent header (carries the `antigravity/<ver> <os>/<arch>` UA)
  - X-Goog-Api-Client / Client-Metadata headers (if present)
  - the request-body identity fields: top-level `userAgent` literal, project,
    model, requestId, and metadata.ideType/ideName/platform/pluginType
    (loadCodeAssist / onboardUser carry the metadata; streamGenerateContent
    carries the body userAgent + project/model).

IMPORTANT — the real Antigravity IDE (a VS Code / Electron fork) must be
configured to egress through this proxy AND trust its CA, e.g. set the IDE
`http.proxy` to http://127.0.0.1:<port> with `http.proxyStrictSSL=false`, or
launch it with HTTPS_PROXY + NODE_EXTRA_CA_CERTS pointing at the mitmproxy CA. If
no line is ever logged, the IDE is bypassing the proxy — fall back to passive
pcap for the (non-load-bearing) JA3 and confirm the UA manually.

Writes one JSON object per line to env ANTIGRAVITY_CAPTURE_HTTP_LOG (append mode).
"""
from __future__ import annotations

import json
import os

from mitmproxy import http

_TARGET_HOST_SUBSTR = "cloudcode-pa.googleapis.com"
_TARGET_PATH_SUBSTR = "v1internal:"

# Header names whose VALUE is a secret (OAuth bearer, cookies, api keys). We keep
# the name + wire position (that ordering/presence IS fingerprint signal) but never
# persist the value. Matched case-insensitively by exact name or substring.
_SECRET_HEADER_EXACT = frozenset({"authorization", "cookie", "proxy-authorization", "set-cookie"})
_SECRET_HEADER_SUBSTR = ("auth", "token", "secret", "api-key", "apikey")


def _redact(name: str, value: str) -> str:
    low = name.lower()
    if low in _SECRET_HEADER_EXACT or any(s in low for s in _SECRET_HEADER_SUBSTR):
        return f"<redacted:{len(value)}b>"
    return value


def _ordered_headers(hdrs) -> list:
    """Full request header list in on-wire order, secret values redacted. The
    ordered set (names + order + non-secret values) is the comprehensive HTTP
    fingerprint — analog of the cc addon's UA/beta/stainless axes, but we don't
    yet know which antigravity headers are load-bearing, so we keep them all."""
    return [[k, _redact(k, v)] for k, v in hdrs.items(multi=True)]


def _log_path() -> str | None:
    path = os.environ.get("ANTIGRAVITY_CAPTURE_HTTP_LOG", "").strip()
    return path or None


def _body_json(flow: http.HTTPFlow) -> dict:
    try:
        return json.loads(flow.request.get_text() or "{}")
    except (ValueError, UnicodeDecodeError):
        return {}


def request(flow: http.HTTPFlow) -> None:
    host = flow.request.host or ""
    if _TARGET_HOST_SUBSTR not in host:
        return
    path = flow.request.path.split("?", 1)[0]
    if _TARGET_PATH_SUBSTR not in path:
        return

    hdrs = flow.request.headers
    body = _body_json(flow)
    meta = body.get("metadata", {}) if isinstance(body, dict) else {}

    record = {
        "host": host,
        "path": path,
        "user_agent": hdrs.get("user-agent", ""),
        "x_goog_api_client": hdrs.get("x-goog-api-client", ""),
        "client_metadata": hdrs.get("client-metadata", ""),
        # body identity fields (present depends on the endpoint hit)
        "body_user_agent": body.get("userAgent", "") if isinstance(body, dict) else "",
        "project": body.get("project", "") if isinstance(body, dict) else "",
        "model": body.get("model", "") if isinstance(body, dict) else "",
        "request_id": body.get("requestId", "") if isinstance(body, dict) else "",
        "ide_type": meta.get("ideType", "") if isinstance(meta, dict) else "",
        "ide_name": meta.get("ideName", "") if isinstance(meta, dict) else "",
        "platform": meta.get("platform", "") if isinstance(meta, dict) else "",
        "plugin_type": meta.get("pluginType", "") if isinstance(meta, dict) else "",
        # Comprehensive axis: every request header in on-wire order (secret values
        # redacted). Lets the diff see headers beyond the named ones above and
        # confirm header ORDER, which the named extraction loses.
        "headers_ordered": _ordered_headers(hdrs),
    }
    line = json.dumps(record, ensure_ascii=False, sort_keys=True)

    log_path = _log_path()
    if log_path:
        with open(log_path, "a", encoding="utf-8") as fh:
            fh.write(line + "\n")
    print("ANTIGRAVITY_CAPTURE " + line, flush=True)
