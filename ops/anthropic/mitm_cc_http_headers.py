"""mitmproxy addon: log Claude Code HTTP headers on Anthropic / TokenKey /v1/messages.

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


# Anthropic `system` is either a string or an array of {type:"text", text:...}
# blocks. Keep only the leading slice of each block's text so the capture stays
# small and never persists user/session content.
_ANCHOR_HEAD_LEN = 160


def _extract_system_anchors(system) -> list[dict]:
    anchors: list[dict] = []
    if isinstance(system, str):
        text = system.strip()
        if text:
            anchors.append({"index": 0, "text_head": text[:_ANCHOR_HEAD_LEN]})
        return anchors
    if isinstance(system, list):
        for i, entry in enumerate(system):
            text = ""
            if isinstance(entry, dict):
                text = str(entry.get("text") or "")
            elif isinstance(entry, str):
                text = entry
            text = text.strip()
            if text:
                anchors.append({"index": i, "text_head": text[:_ANCHOR_HEAD_LEN]})
    return anchors


def request(flow: http.HTTPFlow) -> None:
    host = flow.request.host or ""
    if "anthropic.com" not in host and "tokenkey.dev" not in host:
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
    system_anchors: list[dict] = []
    try:
        import json as _json

        parsed = _json.loads(body) if body.strip().startswith("{") else {}
        model = str(parsed.get("model") or "")
        system_anchors = _extract_system_anchors(parsed.get("system"))
    except Exception:
        model = ""
        system_anchors = []

    record = {
        "host": host,
        "path": path,
        "model": model,
        "user_agent": hdrs.get("user-agent", ""),
        "anthropic_beta": hdrs.get("anthropic-beta", ""),
        "x_stainless": stainless,
        "x_app": hdrs.get("x-app", ""),
        # Stable head of each system block, for the system-prompt anchor axis of
        # /tokenkey-cc-fingerprint-alignment (identity banner + billing prefix).
        # Only the head is kept — the full CC system prompt is dynamic
        # (cwd/git/date/env) and must NOT be byte-aligned.
        "system_anchors": system_anchors,
    }
    line = json.dumps(record, ensure_ascii=False, sort_keys=True)

    log_path = _log_path()
    if log_path:
        with open(log_path, "a", encoding="utf-8") as fh:
            fh.write(line + "\n")
    print("CC_CAPTURE " + line, flush=True)
