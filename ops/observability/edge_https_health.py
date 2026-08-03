#!/usr/bin/env python3
"""edge_https_health.py — external HTTPS /health probe for Stage0 edges/prod.

Why (2026-08-03 edge-us6): a Lightsail guest hang left SSM ConnectionLost and
NetworkOut=0 for ~16h. App-local Feishu alerts cannot fire when the box is
wedged, and prod failover masks single-edge death. An outside GET /health is the
cheap leading signal that does not depend on the guest agent.

Deterministic helpers (no AWS). Used by scan-edge-health.sh before SSM, and by
unit tests via --selftest.
"""
from __future__ import annotations

import argparse
import json
import pathlib
import ssl
import sys
import urllib.error
import urllib.request

_LIGHTSAIL_MATRIX = (
    pathlib.Path(__file__).resolve().parents[2]
    / "deploy"
    / "aws"
    / "lightsail"
    / "edge-targets-lightsail.json"
)


def resolve_health_url(target: str, matrix_path: pathlib.Path | None = None) -> str:
    """Return https://<domain>/health for prod or edge:<id> / bare edge id."""
    t = (target or "").strip()
    if t in ("prod", "edge:prod"):
        return "https://api.tokenkey.dev/health"
    edge = t[5:] if t.startswith("edge:") else t
    if not edge or edge == "prod":
        raise ValueError(f"unresolvable target: {target!r}")
    path = matrix_path or _LIGHTSAIL_MATRIX
    data = json.loads(path.read_text(encoding="utf-8"))
    targets = data.get("targets") or data.get("edges") or {}
    row = targets.get(edge) or {}
    domain = str(row.get("domain") or "").strip()
    if not domain:
        raise ValueError(f"no domain for edge {edge!r} in {path}")
    return f"https://{domain}/health"


def probe_health(
    url: str,
    *,
    timeout_sec: float = 8.0,
    opener=None,
) -> dict:
    """GET url.

    ``reachable`` means *transport* succeeded (TCP/TLS + an HTTP response),
    including non-200. Only timeouts / connect failures set reachable=false
    and http_code=0 — that is the sole signal scan-edge-health uses to skip
    SSM. Deploy-time 502/503 must still fall through to SSM.
    ``healthy`` is HTTP 200.
    """
    open_url = opener or urllib.request.urlopen
    try:
        req = urllib.request.Request(url, method="GET")
        with open_url(req, timeout=timeout_sec, context=ssl.create_default_context()) as resp:
            code = int(getattr(resp, "status", None) or resp.getcode())
            body = resp.read(64)
            return {
                "url": url,
                "reachable": True,
                "healthy": code == 200,
                "http_code": code,
                "body_prefix": body.decode("utf-8", errors="replace")[:64],
                "error": "",
            }
    except urllib.error.HTTPError as exc:
        # urllib raises on 4xx/5xx — still a transport-level success.
        try:
            body = exc.read(64) or b""
        except Exception:  # noqa: BLE001 — best-effort body snip
            body = b""
        code = int(getattr(exc, "code", 0) or 0)
        return {
            "url": url,
            "reachable": True,
            "healthy": False,
            "http_code": code,
            "body_prefix": body.decode("utf-8", errors="replace")[:64],
            "error": f"HTTPError: {code}",
        }
    except Exception as exc:  # noqa: BLE001 — timeout/connect/DNS => unreachable
        return {
            "url": url,
            "reachable": False,
            "healthy": False,
            "http_code": 0,
            "body_prefix": "",
            "error": f"{type(exc).__name__}: {exc}",
        }


def _selftest() -> int:
    failures = 0

    def check(name: str, cond: bool) -> None:
        nonlocal failures
        if cond:
            print(f"  ok: {name}")
        else:
            print(f"  FAIL: {name}", file=sys.stderr)
            failures += 1

    url = resolve_health_url("edge:us6")
    check("us6 domain resolves", url == "https://api-us6.tokenkey.dev/health")
    check("prod domain resolves", resolve_health_url("prod") == "https://api.tokenkey.dev/health")
    try:
        resolve_health_url("edge:does-not-exist-zz")
        check("missing edge raises", False)
    except ValueError:
        check("missing edge raises", True)

    class _Resp:
        status = 200

        def read(self, _n: int) -> bytes:
            return b'{"status":"ok"}'

        def __enter__(self):
            return self

        def __exit__(self, *args):
            return False

        def getcode(self) -> int:
            return 200

    def ok_open(_req, timeout=0, context=None):  # noqa: ARG001
        return _Resp()

    got = probe_health("https://example.test/health", opener=ok_open)
    check(
        "200 => reachable+healthy",
        got["reachable"] is True and got.get("healthy") is True and got["http_code"] == 200,
    )

    def boom_open(_req, timeout=0, context=None):  # noqa: ARG001
        raise TimeoutError("connect timed out")

    bad = probe_health("https://example.test/health", opener=boom_open)
    check(
        "timeout => unreachable http_code=0",
        bad["reachable"] is False and bad["http_code"] == 0 and bad.get("healthy") is False,
    )

    def http503_open(_req, timeout=0, context=None):  # noqa: ARG001
        raise urllib.error.HTTPError(
            "https://example.test/health", 503, "Service Unavailable", hdrs=None, fp=None
        )

    soft = probe_health("https://example.test/health", opener=http503_open)
    check(
        "503 => reachable (do not skip SSM) but not healthy",
        soft["reachable"] is True and soft.get("healthy") is False and soft["http_code"] == 503,
    )

    if failures:
        print(f"edge_https_health selftest: {failures} FAILED", file=sys.stderr)
        return 1
    print("edge_https_health selftest: all cases passed", file=sys.stderr)
    return 0


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--selftest", action="store_true")
    p.add_argument("--target", help="prod | edge:<id> | <id>")
    p.add_argument("--probe", action="store_true", help="GET /health and print JSON")
    p.add_argument("--timeout-seconds", type=float, default=8.0)
    p.add_argument("--matrix", type=pathlib.Path, default=None)
    args = p.parse_args(argv)
    if args.selftest:
        return _selftest()
    if not args.target:
        p.error("--target is required unless --selftest")
    url = resolve_health_url(args.target, matrix_path=args.matrix)
    if not args.probe:
        print(url)
        return 0
    print(json.dumps(probe_health(url, timeout_sec=args.timeout_seconds), ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
