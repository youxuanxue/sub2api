#!/usr/bin/env python3
"""Build Claude Code-shaped /v1/messages JSON for smoke and account probes.

Mirrors backend account_test_service.createTestPayload (random metadata.user_id,
canonical system prompt, cache_control blocks). Used by post_deploy_smoke,
probe_account_model, probe-servable-models, and edge native oauth smoke.
"""
from __future__ import annotations

import argparse
import json
import secrets
import uuid

CC_SYSTEM = "You are Claude Code, Anthropic's official CLI for Claude."


def build_metadata_user_id(session_id: str | None = None) -> str:
    device_id = secrets.token_hex(32)
    sid = session_id or str(uuid.uuid4())
    return json.dumps(
        {"device_id": device_id, "account_uuid": "", "session_id": sid},
        separators=(",", ":"),
    )


def build_messages_payload(
    model: str,
    max_tokens: int = 32,
    prompt: str = "hi",
    stream: bool = False,
    session_id: str | None = None,
) -> dict:
    user_id = build_metadata_user_id(session_id)
    return {
        "model": model,
        "max_tokens": max_tokens,
        "stream": stream,
        "temperature": 1,
        "system": [
            {
                "type": "text",
                "text": CC_SYSTEM,
                "cache_control": {"type": "ephemeral"},
            },
        ],
        "messages": [
            {
                "role": "user",
                "content": [
                    {
                        "type": "text",
                        "text": prompt,
                        "cache_control": {"type": "ephemeral"},
                    },
                ],
            },
        ],
        "metadata": {"user_id": user_id},
    }


def main() -> None:
    parser = argparse.ArgumentParser(description="Build realistic Anthropic messages JSON")
    parser.add_argument("--model", required=True)
    parser.add_argument("--max-tokens", type=int, default=32)
    parser.add_argument("--prompt", default="hi")
    parser.add_argument("--stream", action="store_true")
    parser.add_argument("--session-id", default="")
    args = parser.parse_args()
    sid = args.session_id.strip() or None
    payload = build_messages_payload(
        args.model,
        max_tokens=args.max_tokens,
        prompt=args.prompt,
        stream=args.stream,
        session_id=sid,
    )
    print(json.dumps(payload, ensure_ascii=False, separators=(",", ":")))


if __name__ == "__main__":
    main()
