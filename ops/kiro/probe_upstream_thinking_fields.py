#!/usr/bin/env python3
"""Capture Kiro upstream Event Stream field shapes (secrets redacted).

Reads OAuth credentials JSON (TokenKey account.credentials shape or CLI cache),
calls generateAssistantResponse with thinking enabled, and reports event types +
payload keys + signature lengths. Safe to run on edge via SSM.
"""
from __future__ import annotations

import argparse
import json
import ssl
import struct
import sys
import uuid
from pathlib import Path
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.request import HTTPSHandler, ProxyHandler, Request, build_opener

RUNTIME = "https://runtime.us-east-1.kiro.dev/generateAssistantResponse"
MANAGEMENT = "https://management.us-east-1.kiro.dev"
X_AMZ_TARGET = "AmazonCodeWhispererStreamingService.GenerateAssistantResponse"
THINKING_PREFIX = (
    "<thinking_mode>enabled</thinking_mode>\n"
    "<max_thinking_length>32000</max_thinking_length>\n\n"
)
DEFAULT_UA = "kiro-cli/2.18.0"
DEFAULT_AMZ_UA = "aws-sdk-js/1.0.0 KiroIDE-0.0.0"


def load_credentials(path: Path) -> dict[str, Any]:
    payload = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(payload, dict):
        raise ValueError("credentials must be an object")
    access = str(payload.get("access_token") or payload.get("accessToken") or "").strip()
    if not access:
        raise ValueError("missing access_token")
    profile_arn = str(payload.get("profile_arn") or payload.get("profileArn") or "").strip()
    return {"access_token": access, "profile_arn": profile_arn}


def http_json(method: str, url: str, token: str, body: dict | None = None, extra: dict | None = None) -> tuple[int, bytes]:
    host = url.split("/")[2]
    headers = {
        "Authorization": f"Bearer {token['access_token']}",
        "Content-Type": "application/json",
        "Accept": "*/*",
        "Host": host,
        "User-Agent": DEFAULT_UA,
        "x-amz-user-agent": DEFAULT_AMZ_UA,
        "x-amzn-codewhisperer-optout": "false",
    }
    if extra:
        headers.update(extra)
    data = None if body is None else json.dumps(body, separators=(",", ":")).encode()
    req = Request(url, data=data, method=method, headers=headers)
    opener = build_opener(HTTPSHandler(context=ssl.create_default_context()))
    try:
        with opener.open(req, timeout=60) as resp:
            return resp.status, resp.read()
    except HTTPError as exc:
        return exc.code, exc.read()


def resolve_profile_arn(token: dict[str, Any]) -> str:
    if token.get("profile_arn"):
        return token["profile_arn"]
    code, raw = http_json("POST", f"{MANAGEMENT}/List-Available-Profiles", token, {"maxResults": 10})
    if code != 200:
        raise RuntimeError(f"List-Available-Profiles HTTP {code}: {raw[:200]!r}")
    payload = json.loads(raw)
    profiles = payload.get("profiles") or []
    if not profiles:
        raise RuntimeError("List-Available-Profiles returned no profiles")
    return str(profiles[0].get("arn") or "")


def extract_event_type(headers: bytes) -> str:
    i = 0
    while i < len(headers):
        name_len = headers[i]
        i += 1
        name = headers[i : i + name_len].decode("utf-8", "replace")
        i += name_len
        vtype = headers[i]
        i += 1
        if vtype == 7:
            vlen = struct.unpack(">H", headers[i : i + 2])[0]
            i += 2
            val = headers[i : i + vlen].decode("utf-8", "replace")
            i += vlen
        else:
            break
        if name == ":event-type":
            return val
    return ""


def parse_event_stream(raw: bytes) -> list[dict[str, Any]]:
    events: list[dict[str, Any]] = []
    off = 0
    while off + 12 <= len(raw):
        total = struct.unpack(">I", raw[off : off + 4])[0]
        if total < 16 or off + total > len(raw):
            break
        headers_len = struct.unpack(">I", raw[off + 4 : off + 8])[0]
        payload = raw[off + 12 + headers_len : off + total - 4]
        event_type = extract_event_type(raw[off + 12 : off + 12 + headers_len])
        keys: list[str] = []
        redacted: dict[str, Any] = {}
        if payload:
            try:
                obj = json.loads(payload)
                if isinstance(obj, dict):
                    keys = sorted(obj.keys())
                    for key, val in obj.items():
                        if key == "signature" and val:
                            redacted[key] = {"len": len(str(val)), "prefix": str(val)[:16]}
                        elif key in ("text", "content", "reasoningText"):
                            if isinstance(val, dict):
                                redacted[key] = {"keys": sorted(val.keys())}
                            else:
                                redacted[key] = {"len": len(str(val)) if val is not None else 0}
                        else:
                            redacted[key] = {"type": type(val).__name__}
            except json.JSONDecodeError:
                redacted = {"raw_len": len(payload)}
        events.append({"event_type": event_type, "payload_keys": keys, "payload_redacted": redacted})
        off += total
    return events


def probe(token: dict[str, Any], model_id: str, message: str) -> dict[str, Any]:
    profile_arn = resolve_profile_arn(token)
    body = {
        "conversationState": {
            "chatTriggerType": "MANUAL",
            "conversationId": f"tk-upstream-probe-{uuid.uuid4()}",
            "currentMessage": {
                "userInputMessage": {
                    "content": THINKING_PREFIX + message,
                    "modelId": model_id,
                    "origin": "AI_EDITOR",
                }
            },
        },
        "profileArn": profile_arn,
        "additionalModelRequestFields": {
            "output_config": {"effort": "high"},
            "thinking": {"type": "adaptive", "display": "summarized"},
            "max_tokens": 32000,
        },
    }
    code, raw = http_json(
        "POST",
        RUNTIME,
        token,
        body,
        extra={
            "X-Amz-Target": X_AMZ_TARGET,
            "x-amzn-kiro-agent-mode": "vibe",
        },
    )
    report: dict[str, Any] = {
        "http_status": code,
        "model_id": model_id,
        "profile_arn_present": bool(profile_arn),
        "body_len": len(raw),
    }
    if code != 200:
        report["error_body_prefix"] = raw[:300].decode("utf-8", "replace")
        return report
    events = parse_event_stream(raw)
    report["frame_count"] = len(events)
    report["event_types"] = [e["event_type"] for e in events]
    report["payload_keys_by_event_type"] = {
        et: sorted({k for e in events if e["event_type"] == et for k in e["payload_keys"]})
        for et in sorted({e["event_type"] for e in events})
    }
    sig_frames = [e for e in events if "signature" in e.get("payload_keys", [])]
    report["frames_with_signature_key"] = len(sig_frames)
    if sig_frames:
        report["signature_frame_samples"] = [
            {"event_type": e["event_type"], **e["payload_redacted"].get("signature", {})}
            for e in sig_frames[:3]
        ]
    reasoning = [e for e in events if e["event_type"] == "reasoningContentEvent"]
    report["reasoningContentEvent_count"] = len(reasoning)
    if reasoning:
        report["first_reasoning_frame"] = reasoning[0]
        report["last_reasoning_frame"] = reasoning[-1]
    return report


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("credentials", type=Path, help="path to credentials JSON")
    parser.add_argument("--model", default="auto")
    parser.add_argument(
        "--message",
        default="What is 17+25? Reply with only the number.",
    )
    args = parser.parse_args()
    try:
        token = load_credentials(args.credentials)
        report = probe(token, args.model, args.message)
    except (ValueError, RuntimeError, URLError, json.JSONDecodeError) as exc:
        print(json.dumps({"verdict": "setup_error", "error": str(exc)}, ensure_ascii=False))
        return 1
    print(json.dumps(report, ensure_ascii=False, indent=2))
    return 0 if report.get("http_status") == 200 else 1


if __name__ == "__main__":
    raise SystemExit(main())
