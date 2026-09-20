#!/usr/bin/env python3
"""Capture official Antigravity LS ClientHello records through a local sink.

`serve` is a deliberately non-forwarding CONNECT proxy: it acknowledges the
CONNECT, reads only the first complete TLS record, stores that record per
connection, and closes the socket.  It never stores proxy headers, cookies, or
TLS payloads beyond the ClientHello record.  `report` parses the resulting
records with official_ls_clienthello.py.
"""
from __future__ import annotations

import argparse
import json
import socket
import struct
import sys
import time
from datetime import datetime, timezone
from pathlib import Path

import official_ls_clienthello as parser


def _utc_now() -> str:
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def _read_until_headers(conn: socket.socket) -> tuple[str, bytes]:
    data = bytearray()
    while b"\r\n\r\n" not in data and len(data) < 8192:
        chunk = conn.recv(4096)
        if not chunk:
            break
        data.extend(chunk)
    first_line = bytes(data).split(b"\r\n", 1)[0]
    parts = first_line.decode("latin1", "replace").split(" ")
    if len(parts) < 2 or parts[0] != "CONNECT":
        raise ValueError("expected CONNECT request")
    return parts[1], bytes(data[data.find(b"\r\n\r\n") + 4 :])


def _read_first_record(conn: socket.socket, initial: bytes) -> bytes:
    data = bytearray(initial)
    while len(data) < 5:
        chunk = conn.recv(4096)
        if not chunk:
            break
        data.extend(chunk)
    if len(data) < 5:
        raise ValueError("connection closed before TLS record header")
    content_type, _version, length = struct.unpack_from(">BHH", data, 0)
    if content_type != parser.TLS_HANDSHAKE:
        raise ValueError(f"first TLS record is content type {content_type}, expected 22")
    total = 5 + length
    while len(data) < total:
        chunk = conn.recv(min(8192, total - len(data)))
        if not chunk:
            break
        data.extend(chunk)
    if len(data) < total:
        raise ValueError(f"truncated TLS record: got {len(data)}, expected {total}")
    return bytes(data[:total])


def serve(args: argparse.Namespace) -> int:
    out_dir = args.out_dir
    out_dir.mkdir(parents=True, exist_ok=True)
    index_path = out_dir / "connections.jsonl"
    server = socket.socket()
    server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    server.bind((args.host, args.port))
    server.listen(16)
    server.settimeout(1.0)
    started = time.monotonic()
    count = 0
    print(f"listening={args.host}:{server.getsockname()[1]} out_dir={out_dir}", flush=True)
    try:
        while count < args.max_connections and time.monotonic() - started < args.duration:
            try:
                conn, _addr = server.accept()
            except socket.timeout:
                continue
            count += 1
            record_path = out_dir / f"clienthello-{count:04d}.bin"
            event: dict[str, object] = {
                "connection": count,
                "captured_at": _utc_now(),
                "record": str(record_path),
            }
            with conn:
                conn.settimeout(args.timeout)
                try:
                    target, initial = _read_until_headers(conn)
                    event["target"] = target
                    conn.sendall(b"HTTP/1.1 200 Connection Established\r\n\r\n")
                    record = _read_first_record(conn, initial)
                    record_path.write_bytes(record)
                    event["bytes"] = len(record)
                    event["status"] = "captured"
                    print(f"captured={record_path} target={target} bytes={len(record)}", flush=True)
                except (OSError, ValueError) as exc:
                    event["status"] = "error"
                    event["error"] = str(exc)
                    print(f"capture_error={exc}", file=sys.stderr, flush=True)
            with index_path.open("a", encoding="utf-8") as index:
                index.write(json.dumps(event, sort_keys=True) + "\n")
    finally:
        server.close()
    print(f"connections={count} index={index_path}", flush=True)
    return 0


def report(args: argparse.Namespace) -> int:
    records = sorted(args.capture_dir.glob("clienthello-*.bin"))
    if not records:
        print("ERROR: no clienthello-*.bin files", file=sys.stderr)
        return 2
    samples: list[dict[str, object]] = []
    raw_records: list[bytes] = []
    errors: list[str] = []
    for path in records:
        try:
            raw = path.read_bytes()
            raw_records.append(raw)
            samples.extend(parser.parse_tls_records(raw))
        except (OSError, parser.ParseError) as exc:
            errors.append(f"{path.name}: {exc}")
    if not samples:
        print("ERROR: no parseable ClientHello records", file=sys.stderr)
        for error in errors:
            print(f"- {error}", file=sys.stderr)
        return 2
    report_data = parser.build_report(b"".join(raw_records), args.source)
    report_data["note"] = "Records are per-connection sink captures; randoms and key-share payloads are omitted."
    if errors:
        report_data["parse_errors"] = errors
    encoded = json.dumps(report_data, indent=2, sort_keys=True) + "\n"
    if args.out:
        args.out.write_text(encoded, encoding="utf-8")
        print(f"report={args.out}")
    else:
        print(encoded, end="")
    return 0


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    sub = ap.add_subparsers(dest="command", required=True)
    srv = sub.add_parser("serve")
    srv.add_argument("--host", default="127.0.0.1")
    srv.add_argument("--port", type=int, default=18080)
    srv.add_argument("--out-dir", type=Path, required=True)
    srv.add_argument("--max-connections", type=int, default=10)
    srv.add_argument("--duration", type=float, default=120.0)
    srv.add_argument("--timeout", type=float, default=8.0)
    srv.set_defaults(func=serve)
    rep = sub.add_parser("report")
    rep.add_argument("--capture-dir", type=Path, required=True)
    rep.add_argument("--out", type=Path)
    rep.add_argument("--source", default="official-antigravity-language-server-local-connect")
    rep.set_defaults(func=report)
    args = ap.parse_args(argv)
    try:
        return args.func(args)
    except OSError as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
