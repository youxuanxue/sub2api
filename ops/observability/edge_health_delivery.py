#!/usr/bin/env python3
"""Deliver an edge-health decision and atomically commit structured state."""
from __future__ import annotations

import argparse
import base64
import hashlib
import hmac
import json
import os
import pathlib
import sys
import tempfile
import time
import urllib.request

try:
    from .edge_model_health_alert import AlertContractError, validate_state
except ImportError:  # Direct script execution from the repository root.
    from edge_model_health_alert import AlertContractError, validate_state


class DeliveryError(RuntimeError):
    """A decision could not be delivered safely."""


def _atomic_write(path: pathlib.Path, value: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temp_path: pathlib.Path | None = None
    try:
        with tempfile.NamedTemporaryFile(
            mode="w",
            encoding="utf-8",
            dir=path.parent,
            prefix=f".{path.name}.",
            delete=False,
        ) as fh:
            fh.write(value)
            temp_path = pathlib.Path(fh.name)
        os.replace(temp_path, path)
    finally:
        if temp_path is not None and temp_path.exists():
            temp_path.unlink()


def post_feishu(
    message: str,
    *,
    webhook_url: str,
    signing_secret: str,
    opener=None,
    now: int | None = None,
) -> None:
    """Post one signed message and require Feishu application-level success."""
    if not webhook_url or not signing_secret:
        raise DeliveryError("Feishu webhook URL and signing secret are required")

    timestamp = str(now if now is not None else int(time.time()))
    string_to_sign = f"{timestamp}\n{signing_secret}"
    sign = base64.b64encode(
        hmac.new(string_to_sign.encode("utf-8"), digestmod=hashlib.sha256).digest()
    ).decode("utf-8")
    body = json.dumps(
        {
            "timestamp": timestamp,
            "sign": sign,
            "msg_type": "text",
            "content": {"text": message},
        }
    ).encode("utf-8")
    request = urllib.request.Request(
        webhook_url,
        data=body,
        headers={"Content-Type": "application/json"},
    )
    open_url = opener or urllib.request.urlopen
    try:
        with open_url(request, timeout=15) as response:
            status = int(getattr(response, "status", None) or response.getcode())
            response_body = response.read(4096)
    except Exception as exc:  # noqa: BLE001 - normalize transport errors without leaking URL
        raise DeliveryError(f"Feishu request failed: {type(exc).__name__}") from exc

    if status < 200 or status >= 300:
        raise DeliveryError(f"Feishu HTTP status {status}")
    try:
        payload = json.loads(response_body.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise DeliveryError("Feishu returned a non-JSON response") from exc

    code = payload.get("code", payload.get("StatusCode"))
    if code not in (0, "0"):
        raise DeliveryError(f"Feishu rejected the message with code {code!r}")


def apply_decision(
    decision: dict,
    *,
    state_file: pathlib.Path,
    dry_run: bool,
    webhook_url: str,
    signing_secret: str,
    opener=None,
) -> str:
    """Apply one decision without acknowledging an undelivered alert."""
    should_alert = decision.get("should_alert")
    message = decision.get("message")
    if decision.get("schema_version") != 1 or not isinstance(should_alert, bool) or not isinstance(message, str):
        raise DeliveryError("decision must contain schema_version, boolean should_alert, and string message")
    try:
        state = validate_state(decision.get("state"))
    except AlertContractError as exc:
        raise DeliveryError(f"invalid decision state: {exc}") from exc

    if dry_run:
        if should_alert:
            print(message)
        return "dry-run"

    if should_alert:
        if not message:
            raise DeliveryError("alert decision has an empty message")
        post_feishu(
            message,
            webhook_url=webhook_url,
            signing_secret=signing_secret,
            opener=opener,
        )

    canonical_state = json.dumps(
        state, ensure_ascii=False, separators=(",", ":"), sort_keys=True
    ) + "\n"
    _atomic_write(state_file, canonical_state)
    return "delivered" if should_alert else "unchanged"


def _selftest() -> int:
    class Response:
        status = 200

        def __init__(self, payload: dict):
            self.payload = payload

        def read(self, _limit: int = -1) -> bytes:
            return json.dumps(self.payload).encode("utf-8")

        def getcode(self) -> int:
            return self.status

        def __enter__(self):
            return self

        def __exit__(self, *_args):
            return False

    def opener_for(payload: dict):
        def open_url(_request, timeout=0):  # noqa: ARG001
            return Response(payload)

        return open_url

    failures = 0

    def check(name: str, condition: bool) -> None:
        nonlocal failures
        if condition:
            print(f"  ok: {name}")
        else:
            print(f"  FAIL: {name}", file=sys.stderr)
            failures += 1

    state = {"schema_version": 1, "model_units": [], "hosts": [], "telemetry": []}
    decision = {"schema_version": 1, "should_alert": True, "state": state, "message": "page us6"}
    with tempfile.TemporaryDirectory() as tmp:
        state_file = pathlib.Path(tmp) / "state" / "state.json"
        state_file.parent.mkdir()
        state_file.write_text('{"old":true}\n', encoding="utf-8")

        try:
            apply_decision(
                decision,
                state_file=state_file,
                dry_run=False,
                webhook_url="https://example.test/hook",
                signing_secret="secret",
                opener=opener_for({"code": 19021}),
            )
            check("rejected delivery raises", False)
        except DeliveryError:
            check("rejected delivery raises", True)
        check(
            "rejected delivery keeps previous state",
            state_file.read_text(encoding="utf-8") == '{"old":true}\n',
        )

        result = apply_decision(
            decision,
            state_file=state_file,
            dry_run=True,
            webhook_url="",
            signing_secret="",
        )
        check(
            "dry-run does not advance state",
            result == "dry-run" and state_file.read_text(encoding="utf-8") == '{"old":true}\n',
        )

        result = apply_decision(
            decision,
            state_file=state_file,
            dry_run=False,
            webhook_url="https://example.test/hook",
            signing_secret="secret",
            opener=opener_for({"code": 0}),
        )
        check(
            "confirmed delivery advances state",
            result == "delivered"
            and json.loads(state_file.read_text(encoding="utf-8")) == state,
        )

        result = apply_decision(
            {"schema_version": 1, "should_alert": False, "state": state, "message": ""},
            state_file=state_file,
            dry_run=False,
            webhook_url="",
            signing_secret="",
        )
        check(
            "unchanged decision persists successful evaluation",
            result == "unchanged" and json.loads(state_file.read_text(encoding="utf-8")) == state,
        )

        try:
            apply_decision(
                decision,
                state_file=state_file,
                dry_run=False,
                webhook_url="",
                signing_secret="",
            )
            check("missing credentials fail closed", False)
        except DeliveryError:
            check("missing credentials fail closed", True)

    if failures:
        print(f"edge_health_delivery selftest: {failures} FAILED", file=sys.stderr)
        return 1
    print("edge_health_delivery selftest: all cases passed", file=sys.stderr)
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--decision", type=pathlib.Path)
    parser.add_argument("--state-file", type=pathlib.Path)
    parser.add_argument("--dry-run", action="store_true")
    parser.add_argument("--selftest", action="store_true")
    args = parser.parse_args(argv)

    if args.selftest:
        return _selftest()
    if args.decision is None or args.state_file is None:
        parser.error("--decision and --state-file are required unless --selftest")

    try:
        decision = json.loads(args.decision.read_text(encoding="utf-8"))
        result = apply_decision(
            decision,
            state_file=args.state_file,
            dry_run=args.dry_run,
            webhook_url=os.environ.get("FEISHU_WEBHOOK_URL", ""),
            signing_secret=os.environ.get("FEISHU_SIGNING_SECRET", ""),
        )
    except (OSError, json.JSONDecodeError, DeliveryError) as exc:
        print(f"edge-health delivery failed: {exc}", file=sys.stderr)
        return 1

    print(f"edge-health delivery: {result}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
