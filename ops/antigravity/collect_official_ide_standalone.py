#!/usr/bin/env python3
"""Single-machine, stdlib-only Antigravity IDE/LS evidence collector.

The TLS listener is a non-forwarding HTTP CONNECT sink. It records the first
TLS record and closes the connection; it never contacts Google. Raw TLS records
are retained locally because they are parser input; derived JSON omits randoms
and key-share payloads. The HTTP sink records redacted request metadata only.
This file intentionally has no pip,
mitmproxy, tshark, Go, or Node dependency (``go`` is used only when available
to enrich the package snapshot).

Examples:
  python3 collect_official_ide_standalone.py collect --out ./capture --target app
  python3 collect_official_ide_standalone.py collect --out ./capture --target both
  python3 collect_official_ide_standalone.py snapshot --out ./capture

``app`` launches the installed App with a local non-forwarding proxy and is
expected to capture TLS only. ``ls`` launches a standalone LS with both API
endpoints overridden to the local HTTP sink and is expected to capture HTTP
identity metadata only. The latter reads the user's existing LS state but never
writes Authorization values to disk; use it only on a machine whose operator
has authorized the experiment.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import plistlib
import signal
import socket
import struct
import subprocess
import sys
import tempfile
import threading
import time
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any

TLS_HANDSHAKE = 22
GREASE = frozenset(0x0A0A + 0x1010 * i for i in range(16))


class ParseError(ValueError):
    pass


def need(b: bytes, p: int, n: int, label: str) -> None:
    if p + n > len(b):
        raise ParseError(f"truncated {label}")


def u8(b: bytes, p: int, label: str) -> tuple[int, int]:
    need(b, p, 1, label)
    return b[p], p + 1


def u16(b: bytes, p: int, label: str) -> tuple[int, int]:
    need(b, p, 2, label)
    return struct.unpack_from(">H", b, p)[0], p + 2


def vec(b: bytes, p: int, width: int, label: str) -> tuple[bytes, int]:
    n = 0
    for _ in range(width):
        x, p = u8(b, p, label + " length")
        n = (n << 8) | x
    need(b, p, n, label)
    return b[p:p + n], p + n


def u16s(b: bytes, label: str) -> list[int]:
    if len(b) % 2:
        raise ParseError(f"odd {label} length")
    return [struct.unpack_from(">H", b, i)[0] for i in range(0, len(b), 2)]


def ja3(version: int, ciphers: list[int], extensions: list[int], groups: list[int], points: list[int]) -> str:
    strip = lambda xs: [x for x in xs if x not in GREASE]
    raw = ",".join((str(version), "-".join(map(str, strip(ciphers))),
                     "-".join(map(str, strip(extensions))),
                     "-".join(map(str, strip(groups))), "-".join(map(str, points))))
    return raw + "|" + hashlib.md5(raw.encode()).hexdigest()


def parse_hello(body: bytes, record_version: int) -> dict[str, Any]:
    p = 0
    version, p = u16(body, p, "legacy version")
    need(body, p, 32, "random")
    p += 32
    _, p = vec(body, p, 1, "session id")
    cbytes, p = vec(body, p, 2, "ciphers")
    ciphers = u16s(cbytes, "ciphers")
    _, p = vec(body, p, 1, "compression")
    ebytes, p = vec(body, p, 2, "extensions")
    if p != len(body):
        raise ParseError("trailing ClientHello bytes")
    extensions: list[int] = []
    result: dict[str, Any] = {"record_version": record_version, "legacy_version": version,
                              "cipher_suites": ciphers}
    p = 0
    while p < len(ebytes):
        et, p = u16(ebytes, p, "extension type")
        eb, p = vec(ebytes, p, 2, "extension body")
        extensions.append(et)
        try:
            if et == 0:
                names, q = vec(eb, 0, 2, "SNI list")
                typ, q = u8(names, 0, "SNI type")
                name, q = vec(names, q, 2, "SNI name")
                if typ == 0:
                    result["server_name"] = name.decode("ascii", "replace")
            elif et == 10:
                x, _ = vec(eb, 0, 2, "groups")
                result["supported_groups"] = u16s(x, "groups")
            elif et == 11:
                x, _ = vec(eb, 0, 1, "point formats")
                result["point_formats"] = list(x)
            elif et == 13:
                x, _ = vec(eb, 0, 2, "signature algorithms")
                result["signature_algorithms"] = u16s(x, "signature algorithms")
            elif et == 16:
                x, _ = vec(eb, 0, 2, "ALPN list")
                alpn = []
                q = 0
                while q < len(x):
                    y, q = vec(x, q, 1, "ALPN item")
                    alpn.append(y.decode("ascii", "replace"))
                result["alpn_protocols"] = alpn
            elif et == 43:
                x, _ = vec(eb, 0, 1, "supported versions")
                result["supported_versions"] = u16s(x, "supported versions")
            elif et == 51:
                x, _ = vec(eb, 0, 2, "key shares")
                groups = []
                q = 0
                while q < len(x):
                    g, q = u16(x, q, "key share group")
                    _, q = vec(x, q, 2, "key share payload")
                    groups.append(g)
                result["key_share_groups"] = groups
        except ParseError:
            # Keep the extension number even when an optional payload is new.
            result.setdefault("unparsed_extensions", []).append(et)
    result["extensions"] = extensions
    raw_hash = ja3(version, ciphers, extensions, result.get("supported_groups", []), result.get("point_formats", []))
    result["ja3_raw"], result["ja3_hash"] = raw_hash.split("|", 1)
    result.setdefault("supported_groups", [])
    result.setdefault("point_formats", [])
    result.setdefault("signature_algorithms", [])
    result.setdefault("alpn_protocols", [])
    result.setdefault("supported_versions", [])
    result.setdefault("key_share_groups", [])
    return result


def parse_records(data: bytes) -> list[dict[str, Any]]:
    out = []
    p = 0
    while p < len(data):
        need(data, p, 5, "TLS record header")
        typ, ver, n = struct.unpack_from(">BHH", data, p)
        p += 5
        need(data, p, n, "TLS record")
        payload = data[p:p + n]
        p += n
        if typ != TLS_HANDSHAKE:
            continue
        q = 0
        while q < len(payload):
            mt, q = u8(payload, q, "handshake type")
            hb, q = vec(payload, q, 3, "handshake body")
            if mt == 1:
                out.append(parse_hello(hb, ver))
    if not out:
        raise ParseError("no ClientHello")
    return out


def asar_member(path: Path, member: str) -> bytes:
    with path.open("rb") as f:
        h = f.read(16)
        if len(h) != 16:
            raise ValueError("bad ASAR header")
        _, header_size, _, json_size = struct.unpack("<4I", h)
        if not 0 < json_size <= header_size <= 32 * 1024 * 1024:
            raise ValueError("invalid ASAR header")
        node: Any = json.loads(f.read(json_size))
        for part in member.split("/"):
            node = node["files"][part]
        f.seek(8 + header_size + int(node["offset"]))
        data = f.read(int(node["size"]))
        if len(data) != int(node["size"]):
            raise ValueError("truncated ASAR member")
        return data


def now() -> str:
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def package_snapshot(app: Path) -> dict[str, Any]:
    c = app / "Contents"
    info = plistlib.loads((c / "Info.plist").read_bytes())
    ep = c / "Frameworks/Electron Framework.framework/Resources/Info.plist"
    electron = plistlib.loads(ep.read_bytes()) if ep.exists() else {}
    ls = c / "Resources/bin/language_server"
    source = asar_member(c / "Resources/app.asar", "dist/languageServer.js")
    result: dict[str, Any] = {
        "app_version": info.get("CFBundleShortVersionString"),
        "bundle_id": info.get("CFBundleIdentifier"),
        "electron_version": electron.get("CFBundleVersion"),
        "ls_sha256": hashlib.sha256(ls.read_bytes()).hexdigest(),
        "asar_language_server_sha256": hashlib.sha256(source).hexdigest(),
        "launch_identity_excerpt": source.decode("utf-8", "replace").split("const args = [", 1)[-1].split("]", 1)[0].strip(),
    }
    for command, key in [(["go", "version", "-m", str(ls)], "ls_build_info"), ([str(ls), "--stamp"], "ls_stamp")]:
        try:
            result[key] = subprocess.check_output(command, text=True, stderr=subprocess.STDOUT, timeout=15).strip()
        except (OSError, subprocess.SubprocessError):
            result[key] = "unavailable"
    return result


class TlsSink:
    def __init__(self, out: Path, host: str, port: int):
        self.out = out
        self.host = host
        self.port = port
        self.server = socket.socket()
        self.server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        self.server.bind((host, port))
        self.server.listen(32)
        self.server.settimeout(0.5)
        self.stop = threading.Event()
        self.count = 0

    def start(self) -> None:
        self.out.mkdir(parents=True, exist_ok=True)
        (self.out / "connections.jsonl").write_text("", encoding="utf-8")
        self.thread = threading.Thread(target=self.run, daemon=True)
        self.thread.start()

    def run(self) -> None:
        while not self.stop.is_set():
            try:
                c, _ = self.server.accept()
            except socket.timeout:
                continue
            self.count += 1
            threading.Thread(target=self.handle, args=(c, self.count), daemon=True).start()

    @staticmethod
    def read_headers(c: socket.socket) -> tuple[str, bytes]:
        data = bytearray()
        while b"\r\n\r\n" not in data and len(data) < 8192:
            x = c.recv(4096)
            if not x:
                break
            data.extend(x)
        line = bytes(data).split(b"\r\n", 1)[0].decode("latin1", "replace").split(" ")
        if len(line) < 2 or line[0] != "CONNECT":
            raise ValueError("expected CONNECT")
        marker = data.find(b"\r\n\r\n")
        return line[1], bytes(data[marker + 4:])

    @staticmethod
    def read_record(c: socket.socket, initial: bytes) -> bytes:
        data = bytearray(initial)
        records = bytearray()
        # A ClientHello may span multiple TLS records. Read complete records
        # until the redacted parser can decode a ClientHello, then stop before
        # consuming application data. The 256 KiB bound prevents an accidental
        # non-TLS peer from turning the sink into an unbounded reader.
        while len(records) < 256 * 1024:
            while len(data) < 5:
                x = c.recv(8192)
                if not x:
                    raise ValueError("short TLS header")
                data.extend(x)
            typ, _, n = struct.unpack_from(">BHH", data, 0)
            while len(data) < 5 + n:
                x = c.recv(min(8192, 5 + n - len(data)))
                if not x:
                    raise ValueError("truncated TLS record")
                data.extend(x)
            if typ != TLS_HANDSHAKE:
                raise ValueError(f"first record type {typ}")
            records.extend(data[:5 + n])
            try:
                parse_records(bytes(records))
                return bytes(records)
            except ParseError:
                # The handshake body may continue in the next record.
                pass
            del data[:5 + n]
        raise ValueError("ClientHello exceeds 256 KiB or is not parseable")

    def handle(self, c: socket.socket, number: int) -> None:
        event: dict[str, Any] = {"connection": number, "captured_at": now()}
        path = self.out / f"clienthello-{number:04d}.bin"
        try:
            c.settimeout(8)
            target, initial = self.read_headers(c)
            event["target"] = target
            c.sendall(b"HTTP/1.1 200 Connection Established\r\n\r\n")
            record = self.read_record(c, initial)
            path.write_bytes(record)
            event.update(status="captured", record=path.name, bytes=len(record), sha256=hashlib.sha256(record).hexdigest())
        except (OSError, ValueError) as exc:
            event.update(status="error", error=str(exc))
        finally:
            c.close()
            with (self.out / "connections.jsonl").open("a", encoding="utf-8") as f:
                f.write(json.dumps(event, sort_keys=True) + "\n")

    def close(self) -> None:
        self.stop.set()
        self.server.close()
        self.thread.join(timeout=2)


class HttpHandler(BaseHTTPRequestHandler):
    def log_message(self, *_: Any) -> None:
        pass

    def do_POST(self) -> None:  # noqa: N802
        try:
            length = max(0, min(int(self.headers.get("Content-Length", "0")), 2 * 1024 * 1024))
        except ValueError:
            length = 0
        raw = self.rfile.read(length)
        event = summarize_http_request(self.headers, raw, self.path)
        output: Path = self.server.output_path  # type: ignore[attr-defined]
        with output.open("a", encoding="utf-8") as f:
            f.write(json.dumps(event, ensure_ascii=False, sort_keys=True) + "\n")
        payload = b"{}"
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)


def summarize_http_request(headers: Any, raw: bytes, path: str) -> dict[str, Any]:
    """Summarize an HTTP request without retaining credentials or body values."""
    event: dict[str, Any] = {"captured_at": now(), "method": "POST", "path": path.split("?", 1)[0],
                             "headers": {k: headers.get(k) for k in (
                                 "User-Agent", "X-Goog-Api-Client", "X-Client-Name", "X-Client-Version",
                                 "X-Machine-Id", "X-Vscode-Sessionid", "Content-Type") if headers.get(k)},
                             "authorization_present": bool(headers.get("Authorization")),
                             "body": {"bytes": len(raw)}}
    try:
        value = json.loads(raw)
        event["body"]["json"] = True
        if isinstance(value, dict):
            event["body"]["top_level_keys"] = sorted(map(str, value))
            metadata = value.get("metadata")
            if isinstance(metadata, dict):
                event["body"]["metadata_keys"] = sorted(map(str, metadata))
                for key in ("ideType", "ideVersion", "ideName", "platform", "userAgent"):
                    if isinstance(metadata.get(key), (str, int, float, bool)):
                        event["body"][key] = metadata[key]
    except (UnicodeDecodeError, json.JSONDecodeError):
        event["body"]["json"] = False
    return event


def http_sink(out: Path, port: int, seconds: float) -> None:
    out.parent.mkdir(parents=True, exist_ok=True)
    server = ThreadingHTTPServer(("127.0.0.1", port), HttpHandler)
    server.output_path = out  # type: ignore[attr-defined]
    timer = threading.Timer(seconds, server.shutdown)
    timer.start()
    try:
        server.serve_forever()
    finally:
        timer.cancel()
        server.server_close()


def run_process(argv: list[str], env: dict[str, str], seconds: float) -> int:
    p = subprocess.Popen(argv, env=env, start_new_session=True)
    try:
        time.sleep(seconds)
    finally:
        if p.poll() is None:
            try:
                os.killpg(p.pid, signal.SIGTERM)
                p.wait(timeout=8)
            except (ProcessLookupError, subprocess.TimeoutExpired):
                try:
                    os.killpg(p.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
    return p.returncode or 0


def report_tls(directory: Path) -> dict[str, Any]:
    records = sorted(directory.glob("clienthello-*.bin"))
    by_sni: dict[str, list[dict[str, Any]]] = {}
    for path in records:
        for sample in parse_records(path.read_bytes()):
            by_sni.setdefault(sample.get("server_name", "(no-sni)"), []).append(sample)
    return {"records": [{"file": p.name, "sha256": hashlib.sha256(p.read_bytes()).hexdigest()} for p in records],
            "samples_by_sni": by_sni}


def collect(args: argparse.Namespace) -> int:
    out = args.out
    out.mkdir(parents=True, exist_ok=True)
    app = args.app
    result: dict[str, Any] = {"schema_version": 1, "collection_kind": "full-local-collector",
                              "collected_at": now(), "package": package_snapshot(app),
                              "privacy": {"tokens_saved": False, "cookies_saved": False, "authorization_values_saved": False,
                                           "raw_tls_records_saved": True},
                              "phases": []}
    env = os.environ.copy()
    env.update(HTTP_PROXY=f"http://127.0.0.1:{args.tls_port}", HTTPS_PROXY=f"http://127.0.0.1:{args.tls_port}",
               http_proxy=f"http://127.0.0.1:{args.tls_port}", https_proxy=f"http://127.0.0.1:{args.tls_port}",
               NO_PROXY="127.0.0.1,localhost", no_proxy="127.0.0.1,localhost")
    if args.target in ("app", "both"):
        tls_dir = out / "tls"
        sink = TlsSink(tls_dir, "127.0.0.1", args.tls_port)
        sink.start()
        phase: dict[str, Any] = {"type": "official-app-startup", "seconds": args.seconds, "status": "started"}
        try:
            if args.launch:
                if subprocess.call(["pgrep", "-x", "Antigravity"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL) == 0:
                    raise RuntimeError("Antigravity is already running; quit it before --launch")
                run_process([str(app / "Contents/MacOS/Antigravity"), "--proxy-server=http://127.0.0.1:" + str(args.tls_port), "--disable-quic"], env, args.seconds)
            else:
                print(f"Launch the logged-in App with HTTPS_PROXY=http://127.0.0.1:{args.tls_port}; waiting {args.seconds}s", flush=True)
                time.sleep(args.seconds)
            phase["tls"] = report_tls(tls_dir)
        except Exception as exc:
            phase.update(status="error", error=str(exc))
        finally:
            sink.close()
        result["phases"].append(phase)
    if args.target in ("ls", "both"):
        http_path = out / "http.jsonl"
        server = threading.Thread(target=http_sink, args=(http_path, args.http_port, args.seconds), daemon=True)
        server.start()
        time.sleep(0.2)
        phase = {"type": "official-ls-local-http", "seconds": args.seconds, "status": "started", "http_events": 0}
        ls = app / "Contents/Resources/bin/language_server"
        # The endpoint overrides keep this phase offline. Real user auth state is
        # read by LS, but the sink stores only Authorization presence.
        ls_args = [str(ls), "--standalone", "--override_ide_name", "antigravity", "--subclient_type", "hub",
                   "--override_ide_version", result["package"]["app_version"], "--override_user_agent_name", "antigravity",
                   "--app_data_dir", "antigravity", "--gemini_dir", ".gemini", "--api_server_url", f"http://127.0.0.1:{args.http_port}",
                   "--cloud_code_endpoint", f"http://127.0.0.1:{args.http_port}", "--disable_telemetry"]
        try:
            run_process(ls_args, env, args.seconds)
        except Exception as exc:
            phase.update(status="error", error=str(exc))
        finally:
            server.join(timeout=3)
        if http_path.exists():
            phase["http_events"] = len(http_path.read_text(encoding="utf-8").splitlines())
        result["phases"].append(phase)
    result["limitations"] = ["A non-forwarding sink makes requests fail; this is fingerprint capture, not service validation.",
                              "HTTP capture is available only in the local-endpoint LS phase and does not prove the same request path as production.",
                              "Run the App phase with a logged-in official profile; an unauthenticated LS cannot produce cloudcode image evidence."]
    (out / "report.json").write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"report={out / 'report.json'}")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    sub = ap.add_subparsers(dest="command", required=True)
    snap = sub.add_parser("snapshot")
    snap.add_argument("--app", type=Path, default=Path("/Applications/Antigravity.app"))
    snap.add_argument("--out", type=Path, required=True)
    col = sub.add_parser("collect")
    col.add_argument("--app", type=Path, default=Path("/Applications/Antigravity.app"))
    col.add_argument("--out", type=Path, required=True)
    col.add_argument("--target", choices=("app", "ls", "both"), default="both")
    col.add_argument("--seconds", type=float, default=90)
    col.add_argument("--tls-port", type=int, default=18080)
    col.add_argument("--http-port", type=int, default=18081)
    col.add_argument("--launch", action="store_true", help="launch and terminate the App during the TLS phase")
    args = ap.parse_args()
    try:
        if args.command == "snapshot":
            args.out.mkdir(parents=True, exist_ok=True)
            data = {"schema_version": 1, "collection_kind": "package-only",
                    "collected_at": now(), "package": package_snapshot(args.app),
                    "privacy": {"tokens_saved": False, "cookies_saved": False}}
            (args.out / "snapshot.json").write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n")
            print(f"snapshot={args.out / 'snapshot.json'}")
            return 0
        return collect(args)
    except (OSError, ValueError, ParseError, KeyError, subprocess.SubprocessError) as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
