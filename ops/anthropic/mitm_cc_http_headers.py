"""mitmproxy addon: log Claude Code HTTP headers on api.anthropic.com /v1/messages.

Used by ops/anthropic/capture-cc-fingerprint.sh. Writes one JSON object per line
to the path in env CC_CAPTURE_HTTP_LOG (append mode).
"""
from __future__ import annotations

import json
import os

from mitmproxy import http


def _log_path() -> str | None:
    path = os.environ.get("CC_CAPTURE_HTTP_LOG", "").strip()
    return path or None


def request(flow: http.HTTPFlow) -> None:
    if "anthropic.com" not in (flow.request.host or ""):
        return
    path = flow.request.path.split("?", 1)[0]
    if path != "/v1/messages" and not path.startswith("/v1/messages"):
        return

    hdrs = flow.request.headers
    stainless = {
        k: hdrs[k]
        for k in hdrs
        if k.lower().startswith("x-stainless")
    }
    body = flow.request.get_text(strict=False) or ""
    model = ""
    try:
        import json as _json

        parsed = _json.loads(body) if body.strip().startswith("{") else {}
        model = str(parsed.get("model") or "")
    except Exception:
        model = ""

    record = {
        "path": path,
        "model": model,
        "user_agent": hdrs.get("user-agent", ""),
        "anthropic_beta": hdrs.get("anthropic-beta", ""),
        "x_stainless": stainless,
        "x_app": hdrs.get("x-app", ""),
    }
    line = json.dumps(record, ensure_ascii=False, sort_keys=True)

    log_path = _log_path()
    if log_path:
        with open(log_path, "a", encoding="utf-8") as fh:
            fh.write(line + "\n")
    print("CC_CAPTURE " + line, flush=True)
