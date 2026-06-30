"""mitmproxy addon: capture /v1/messages body shape for CC geo-stego probe.

Records system[], messages[] text blocks (incl. <system-reminder>), and
date_change attachments — the surfaces TokenKey gateway_request_tk_cc_geo_stego.go
must normalize before US edge egress.
"""
from __future__ import annotations

import json
import os
import re

from mitmproxy import http

LOG_PATH = os.environ.get("CC_GEO_PROBE_LOG", "").strip()
SCENARIO = os.environ.get("CC_GEO_PROBE_SCENARIO", "unknown")
TZ = os.environ.get("CC_GEO_PROBE_TZ", "")
PROXY = os.environ.get("CC_GEO_PROBE_PROXY", "")
BASE_URL = os.environ.get("CC_GEO_PROBE_BASE_URL", "")

SYSTEM_REMINDER = re.compile(r"<system-reminder>", re.I)
CURRENT_DATE = re.compile(r"#\s*currentDate", re.I)
DATE_LINE = re.compile(
    r"(Today.?s date is(?: now)?|The current date is|Current date:?)\s*",
    re.IGNORECASE,
)


def _capture_hosts() -> tuple[str, ...]:
    raw = os.environ.get("CC_GEO_PROBE_HOSTS", "anthropic.com,tokenkey.dev,aicodemirror.com").strip()
    return tuple(h.strip() for h in raw.split(",") if h.strip())


def _host_matches(host: str) -> bool:
    host = host or ""
    return any(marker in host for marker in _capture_hosts())


def _text_blocks(content) -> list[str]:
    texts: list[str] = []
    if isinstance(content, str):
        if content.strip():
            texts.append(content)
        return texts
    if isinstance(content, list):
        for block in content:
            if isinstance(block, dict):
                if block.get("type") == "text":
                    t = str(block.get("text") or "")
                    if t.strip():
                        texts.append(t)
    return texts


def _summarize_messages(messages) -> list[dict]:
    out: list[dict] = []
    if not isinstance(messages, list):
        return out
    for mi, msg in enumerate(messages):
        if not isinstance(msg, dict):
            continue
        role = str(msg.get("role") or "")
        content = msg.get("content")
        entry: dict = {"index": mi, "role": role, "text_blocks": []}
        for ti, text in enumerate(_text_blocks(content)):
            block = {
                "index": ti,
                "has_system_reminder": bool(SYSTEM_REMINDER.search(text)),
                "has_current_date": bool(CURRENT_DATE.search(text)),
                "date_lines": [
                    ln.strip()[:240]
                    for ln in text.splitlines()
                    if DATE_LINE.search(ln)
                ],
                "head": text[:160].replace("\n", "\\n"),
            }
            entry["text_blocks"].append(block)
        if isinstance(content, list):
            for ci, block in enumerate(content):
                if not isinstance(block, dict):
                    continue
                att = block.get("attachment")
                if isinstance(att, dict) and att.get("type") == "date_change":
                    entry.setdefault("date_change_attachments", []).append(
                        {
                            "content_index": ci,
                            "newDate": att.get("newDate"),
                        }
                    )
        if entry["text_blocks"] or entry.get("date_change_attachments"):
            out.append(entry)
    return out


def request(flow: http.HTTPFlow) -> None:
    if not LOG_PATH:
        return
    if not _host_matches(flow.request.host or ""):
        return
    path = flow.request.path.split("?", 1)[0]
    if path != "/v1/messages" and not path.startswith("/v1/messages"):
        return
    body_text = flow.request.get_text(strict=False) or ""
    try:
        body = json.loads(body_text) if body_text.strip().startswith("{") else {}
    except Exception:
        body = {}
    record = {
        "scenario": SCENARIO,
        "tz": TZ,
        "proxy": PROXY,
        "base_url": BASE_URL,
        "host": flow.request.host,
        "path": path,
        "model": body.get("model"),
        "body": {
            "system": body.get("system"),
            "messages": _summarize_messages(body.get("messages")),
        },
    }
    with open(LOG_PATH, "a", encoding="utf-8") as f:
        f.write(json.dumps(record, ensure_ascii=False) + "\n")
