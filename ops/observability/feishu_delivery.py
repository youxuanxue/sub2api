"""Shared signed Feishu delivery and atomic state helpers."""
from __future__ import annotations

import base64
import hashlib
import hmac
import json
import os
import pathlib
import tempfile
import time
import urllib.request


class DeliveryError(RuntimeError):
    pass


def _atomic_write(path: pathlib.Path, value: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temp_path = None
    try:
        with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8", dir=path.parent, prefix=f".{path.name}.", delete=False) as fh:
            fh.write(value)
            temp_path = pathlib.Path(fh.name)
        os.replace(temp_path, path)
    finally:
        if temp_path is not None and temp_path.exists():
            temp_path.unlink()


def _post(payload: dict, *, webhook_url: str, signing_secret: str, opener=None, now: int | None = None) -> None:
    if not webhook_url or not signing_secret:
        raise DeliveryError("Feishu webhook URL and signing secret are required")
    timestamp = str(now if now is not None else int(time.time()))
    sign = base64.b64encode(hmac.new(f"{timestamp}\n{signing_secret}".encode(), digestmod=hashlib.sha256).digest()).decode()
    body = dict(payload, timestamp=timestamp, sign=sign)
    request = urllib.request.Request(webhook_url, data=json.dumps(body).encode(), headers={"Content-Type": "application/json"})
    try:
        with (opener or urllib.request.urlopen)(request, timeout=15) as response:
            status = int(getattr(response, "status", None) or response.getcode())
            response_body = response.read(4096)
    except Exception as exc:
        raise DeliveryError(f"Feishu request failed: {type(exc).__name__}") from exc
    if status < 200 or status >= 300:
        raise DeliveryError(f"Feishu HTTP status {status}")
    try:
        result = json.loads(response_body.decode())
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise DeliveryError("Feishu returned a non-JSON response") from exc
    code = result.get("code", result.get("StatusCode"))
    if code not in (0, "0"):
        raise DeliveryError(f"Feishu rejected the message with code {code!r}")


def post_feishu(message: str, *, webhook_url: str, signing_secret: str, opener=None, now: int | None = None) -> None:
    _post({"msg_type": "text", "content": {"text": message}}, webhook_url=webhook_url, signing_secret=signing_secret, opener=opener, now=now)


def post_feishu_card(card: dict, *, webhook_url: str, signing_secret: str, opener=None, now: int | None = None) -> None:
    if not isinstance(card, dict) or not card:
        raise DeliveryError("Feishu card payload must be a non-empty object")
    _post({"msg_type": "interactive", "card": card}, webhook_url=webhook_url, signing_secret=signing_secret, opener=opener, now=now)
