"""mitmproxy addon: log real Kiro IDE HTTP headers on the CodeWhisperer endpoints.

Sibling of ops/anthropic/mitm_cc_http_headers.py. Captures the on-wire
User-Agent / x-amz-user-agent / x-amzn-* headers so the canonical UA rebuilt from
backend/internal/pkg/kiro/constants.go can be diffed against ground truth.

IMPORTANT — best-effort only: unlike Claude Code, the real Kiro IDE may NOT honor
HTTP_PROXY or accept a MITM CA (the AWS SDK pins / ignores proxies in some
builds). If this addon never logs a line, fall back to TLS-only alignment (the
JA3 from passive pcap is the load-bearing signal); the UA is already known from
the repo constants and only needs occasional manual confirmation.

Writes one JSON object per line to env KIRO_CAPTURE_HTTP_LOG (append mode).
"""
from __future__ import annotations

import json
import os

from mitmproxy import http

_TARGET_PATH = "generateAssistantResponse"


def _log_path() -> str | None:
    path = os.environ.get("KIRO_CAPTURE_HTTP_LOG", "").strip()
    return path or None


def request(flow: http.HTTPFlow) -> None:
    host = flow.request.host or ""
    if "amazonaws.com" not in host:
        return
    path = flow.request.path.split("?", 1)[0]
    if _TARGET_PATH.lower() not in path.lower():
        return

    hdrs = flow.request.headers
    x_amzn = {k: hdrs[k] for k in hdrs if k.lower().startswith("x-amzn")}

    record = {
        "host": host,
        "path": path,
        "user_agent": hdrs.get("user-agent", ""),
        "x_amz_user_agent": hdrs.get("x-amz-user-agent", ""),
        "x_amz_target": hdrs.get("x-amz-target", ""),
        "x_amzn": x_amzn,
    }
    line = json.dumps(record, ensure_ascii=False, sort_keys=True)

    log_path = _log_path()
    if log_path:
        with open(log_path, "a", encoding="utf-8") as fh:
            fh.write(line + "\n")
    print("KIRO_CAPTURE " + line, flush=True)
