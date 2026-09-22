#!/usr/bin/env python3
"""Export a Gemini Web session from one running AdsPower profile via CDP.

The output is a local secret.  It contains the complete CDP cookie records for
Google/Gemini image domains and the browser's actual User-Agent.  Never print it
or pass it as a command-line argument to another process.
"""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import tempfile
import urllib.request
from session_contract import ALLOWED_COOKIE_DOMAINS

try:
    import websocket
except ModuleNotFoundError as exc:
    raise SystemExit(
        "Missing websocket-client. Run with: "
        "uv run --with-requirements ops/gemini-web/export-requirements.txt "
        "python3 ops/gemini-web/export_adspower_session.py ..."
    ) from exc


ADSPower_API = "http://127.0.0.1:50325"


def request_json(url: str) -> object:
    with urllib.request.urlopen(url, timeout=20) as response:
        value = json.load(response)
    return value


def cdp_call(socket: websocket.WebSocket, sequence: list[int], method: str, params: dict | None = None) -> dict:
    sequence[0] += 1
    socket.send(json.dumps({"id": sequence[0], "method": method, "params": params or {}}))
    while True:
        message = json.loads(socket.recv())
        if message.get("id") != sequence[0]:
            continue
        if "error" in message:
            raise RuntimeError(f"CDP {method} failed: {message['error'].get('message', 'unknown error')}")
        result = message.get("result", {})
        if not isinstance(result, dict):
            raise RuntimeError(f"CDP {method} returned an invalid result")
        return result


def select_gemini_page(targets: list[dict]) -> dict:
    for target in targets:
        if target.get("type") == "page" and "gemini.google.com" in target.get("url", ""):
            if target.get("webSocketDebuggerUrl"):
                return target
    raise RuntimeError("No open Gemini page found in this AdsPower profile; open Gemini first")


def export_session(user_id: str, api_url: str = ADSPower_API) -> tuple[dict, dict]:
    started = request_json(f"{api_url}/api/v1/browser/start?user_id={user_id}")
    if not isinstance(started, dict):
        raise RuntimeError("AdsPower returned an invalid browser-start response")
    port = ((started.get("data") or {}).get("debug_port"))
    if not port:
        raise RuntimeError("AdsPower did not return a CDP debug port")
    targets = request_json(f"http://127.0.0.1:{port}/json/list")
    if not isinstance(targets, list):
        raise RuntimeError("CDP returned an invalid target list")
    page = select_gemini_page(targets)
    socket = websocket.create_connection(page["webSocketDebuggerUrl"], timeout=20, suppress_origin=True)
    sequence = [0]
    try:
        user_agent = cdp_call(
            socket,
            sequence,
            "Runtime.evaluate",
            {"expression": "navigator.userAgent", "returnByValue": True},
        ).get("result", {}).get("value")
        if not isinstance(user_agent, str) or not user_agent:
            raise RuntimeError("Could not read the browser User-Agent")
        cookies = cdp_call(socket, sequence, "Network.getAllCookies").get("cookies", [])
    finally:
        socket.close()
    if not isinstance(cookies, list):
        raise RuntimeError("CDP returned an invalid cookie list")

    selected = []
    for cookie in cookies:
        if not isinstance(cookie, dict):
            continue
        domain = cookie.get("domain", "")
        if domain.lstrip(".") not in ALLOWED_COOKIE_DOMAINS:
            continue
        # Preserve every field returned by CDP.  Unknown future fields are kept
        # too, so an export does not silently lose a browser binding attribute.
        selected.append(dict(cookie))
    if not selected:
        raise RuntimeError("No Google/Gemini cookies found; confirm the profile is logged in")

    bundle = {
        "format": "tokenkey-gemini-web-session-v1",
        "source": {"provider": "adspower", "profile_id": user_id},
        "user_agent": user_agent,
        "cookies": selected,
    }
    summary = {
        "profile_id": user_id,
        "page_url": page.get("url", ""),
        "user_agent": user_agent,
        "cookie_count": len(selected),
        "cookie_domains": sorted({cookie["domain"] for cookie in selected}),
        "cookie_names": sorted({cookie["name"] for cookie in selected}),
    }
    return bundle, summary


def write_secret(path: Path, value: dict) -> None:
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    fd, temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent, text=True)
    try:
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, "w", encoding="utf-8") as output:
            json.dump(value, output, ensure_ascii=False, separators=(",", ":"))
            output.write("\n")
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, path)
    finally:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass


def main() -> int:
    parser = argparse.ArgumentParser(description="Export one AdsPower Gemini Web session as a protected JSON file")
    parser.add_argument("profile_id", help="AdsPower user_id, not the visible serial number")
    parser.add_argument("--output", required=True, type=Path, help="New or replacement secret output file")
    parser.add_argument("--adspower-api", default=ADSPower_API, help=argparse.SUPPRESS)
    args = parser.parse_args()
    bundle, summary = export_session(args.profile_id, args.adspower_api)
    write_secret(args.output, bundle)
    print(json.dumps({"output": str(args.output), **summary}, ensure_ascii=False, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
