#!/usr/bin/env python3
"""External HTTPS /health probe for the production gateway."""
from __future__ import annotations

import argparse
import json
import ssl
import sys
import urllib.error
import urllib.request


def resolve_health_url(target: str) -> str:
    if (target or "").strip() != "prod":
        raise ValueError("only the prod target is supported")
    return "https://api.tokenkey.dev/health"


def probe_health(url: str, *, timeout_sec: float = 8.0, opener=None) -> dict:
    open_url = opener or urllib.request.urlopen
    try:
        request = urllib.request.Request(url, method="GET")
        with open_url(request, timeout=timeout_sec, context=ssl.create_default_context()) as response:
            code = int(getattr(response, "status", None) or response.getcode())
            body = response.read(4096)
            try:
                payload = json.loads(body)
                valid = isinstance(payload, dict) and payload.get("status") == "ok"
            except (ValueError, UnicodeDecodeError):
                valid = False
            return {
                "url": url,
                "reachable": True,
                "healthy": code == 200 and valid,
                "http_code": code,
                "body_prefix": body.decode("utf-8", errors="replace")[:64],
                "error": "" if code == 200 and valid else "unexpected health response",
            }
    except urllib.error.HTTPError as exc:
        body = exc.read(64) if exc.fp else b""
        return {
            "url": url,
            "reachable": True,
            "healthy": False,
            "http_code": int(exc.code),
            "body_prefix": body.decode("utf-8", errors="replace")[:64],
            "error": f"HTTPError: {exc.code}",
        }
    except Exception as exc:  # noqa: BLE001
        return {
            "url": url,
            "reachable": False,
            "healthy": False,
            "http_code": 0,
            "body_prefix": "",
            "error": f"{type(exc).__name__}: {exc}",
        }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--selftest", action="store_true")
    parser.add_argument("--target")
    parser.add_argument("--probe", action="store_true")
    parser.add_argument("--timeout-seconds", type=float, default=8.0)
    args = parser.parse_args(argv)
    if args.selftest:
        assert resolve_health_url("prod") == "https://api.tokenkey.dev/health"
        print("https_health selftest: all cases passed", file=sys.stderr)
        return 0
    if not args.target:
        parser.error("--target is required unless --selftest")
    url = resolve_health_url(args.target)
    if args.probe:
        print(json.dumps(probe_health(url, timeout_sec=args.timeout_seconds), ensure_ascii=False))
    else:
        print(url)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
