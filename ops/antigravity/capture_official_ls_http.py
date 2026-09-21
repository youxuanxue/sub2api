#!/usr/bin/env python3
"""Capture official Antigravity language-server HTTP identity locally.

This is a non-forwarding HTTP sink for a language-server launched with local
endpoint overrides. It records only request metadata: User-Agent, selected
identity headers, path, and JSON key/IDE metadata. Authorization is represented
as a boolean and request bodies are never written to disk.
"""
from __future__ import annotations

import argparse
import json
import threading
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any


def _utc_now() -> str:
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def summarize_body(raw: bytes) -> dict[str, Any]:
    """Return safe structural metadata without retaining request values."""
    result: dict[str, Any] = {"bytes": len(raw)}
    try:
        value = json.loads(raw)
    except (UnicodeDecodeError, json.JSONDecodeError):
        result["json"] = False
        return result
    result["json"] = True
    if not isinstance(value, dict):
        result["top_level_type"] = type(value).__name__
        return result
    result["top_level_keys"] = sorted(str(key) for key in value)
    metadata = value.get("metadata")
    if isinstance(metadata, dict):
        result["metadata_keys"] = sorted(str(key) for key in metadata)
        for key in ("ideType", "ideVersion", "ideName", "platform"):
            if isinstance(metadata.get(key), (str, int, float, bool)):
                result[key] = metadata[key]
    return result


def summarize_headers(handler: BaseHTTPRequestHandler) -> dict[str, Any]:
    allowed = (
        "User-Agent",
        "X-Goog-Api-Client",
        "X-Client-Name",
        "X-Client-Version",
        "X-Machine-Id",
        "X-Vscode-Sessionid",
        "Content-Type",
    )
    result = {name: handler.headers.get(name) for name in allowed if handler.headers.get(name)}
    result["Authorization-Present"] = bool(handler.headers.get("Authorization"))
    return result


def bounded_content_length(value: str | None) -> int:
    try:
        return max(0, min(int(value or "0"), 2 * 1024 * 1024))
    except ValueError:
        return 0


class SinkHandler(BaseHTTPRequestHandler):
    server_version = "TokenKeyOfficialLSSink/1"

    def log_message(self, _format: str, *args: object) -> None:
        return

    def do_POST(self) -> None:  # noqa: N802
        length = bounded_content_length(self.headers.get("Content-Length"))
        raw = self.rfile.read(length)
        event = {
            "captured_at": _utc_now(),
            "method": "POST",
            "path": self.path.split("?", 1)[0],
            "headers": summarize_headers(self),
            "body": summarize_body(raw),
        }
        output = getattr(self.server, "output_path", None)
        if output is not None:
            with output.open("a", encoding="utf-8") as stream:
                stream.write(json.dumps(event, ensure_ascii=False, sort_keys=True) + "\n")
        body = b"{}"
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def serve(args: argparse.Namespace) -> int:
    args.out.parent.mkdir(parents=True, exist_ok=True)
    server = ThreadingHTTPServer((args.host, args.port), SinkHandler)
    server.output_path = args.out
    print(f"listening=http://{args.host}:{server.server_address[1]} out={args.out}", flush=True)
    timer = threading.Timer(args.duration, server.shutdown)
    timer.daemon = True
    timer.start()
    try:
        server.serve_forever()
    finally:
        timer.cancel()
        server.server_close()
    return 0


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    sub = ap.add_subparsers(dest="command", required=True)
    srv = sub.add_parser("serve")
    srv.add_argument("--host", default="127.0.0.1")
    srv.add_argument("--port", type=int, default=18081)
    srv.add_argument("--out", type=Path, required=True)
    srv.add_argument("--duration", type=float, default=120.0)
    srv.set_defaults(func=serve)
    args = ap.parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    raise SystemExit(main())
