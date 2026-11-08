#!/usr/bin/env python3
"""Codex CLI TLS / HTTP-2 wire fingerprint capture and deterministic check.

Ground truth = the locally installed ``codex`` binary talking to a LOCAL
CONNECT + TLS capture endpoint (this file). The run uses a throwaway
``CODEX_HOME``, a dummy API key and a per-run local CA, and the endpoint never
dials upstream: CONNECT is accepted only for ``chatgpt.com`` and answered
locally, so no real token is used and no request leaves the machine.

Two lanes are recorded per ClientHello:

* ``https`` — ALPN ``h2,http/1.1``; the endpoint speaks HTTP/2 and records the
  client SETTINGS, connection WINDOW_UPDATE and pseudo-header order.
* ``wss``   — the Responses WebSocket upgrade (no ALPN, HTTP/1.1 Upgrade). The
  endpoint answers 426 so codex falls back to HTTPS in the same run.

Committed baseline = ``deploy/aws/stage0/tk_canonical_codex_cli.json`` (the
runtime fields are seeded into ``tls_fingerprint_profiles`` by the latest
``tk_canonical_codex_cli`` migration). ClientHello parsing, GREASE handling,
JA3 and the runtime projection/digest are shared with the Kiro engine
(``ops/kiro``); only Codex-specific lanes, JA4 and HTTP/2 live here.

Subcommands:
  capture       run real codex N times against the local endpoint → bundle
  check         bundle vs committed profile:
                  aligned       (exit 0)
                  version_only  codex version moved, TLS digest + wire unchanged (exit 1)
                  tls_drift     runtime digest or HTTP/2 / WSS wire changed (exit 1)
                  invalid       bundle/env unusable (exit 2)
  emit-profile  write the canonical JSON from a valid bundle

stdlib-only (Python 3.9+). Exit codes: 0 aligned, 1 drift, 2 usage/env/invalid.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import shutil
import socket
import ssl
import subprocess
import sys
import tempfile
import threading
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

HERE = Path(__file__).resolve().parent
REPO_ROOT = HERE.parents[1]
sys.path.insert(0, str(HERE))
sys.path.insert(0, str(REPO_ROOT / "ops/kiro"))

import capture_kiro_fingerprint as kiro  # noqa: E402  (shared TLS semantics / JA3 / digest)
import hpack_lite  # noqa: E402
import mitm_kiro_http_logger as kiro_hello  # noqa: E402  (shared ClientHello extension parser)

SCHEMA_VERSION = 1
PROFILE_NAME = "tk_canonical_codex_cli"
CODEX_TLS_PROFILE_JSON = REPO_ROOT / "deploy/aws/stage0/tk_canonical_codex_cli.json"
MIGRATIONS_DIR = REPO_ROOT / "backend/migrations"
DEFAULT_OUT_DIR = REPO_ROOT / ".codex_tls"
CAPTURE_HOST = "chatgpt.com"
CAPTURE_SOURCE = "real-cli-local-connect-capture"
MIN_LANE_SAMPLES = 3
EXIT_ALIGNED, EXIT_DRIFT, EXIT_INVALID = 0, 1, 2
RUNTIME_FIELDS = kiro.RUNTIME_PROFILE_FIELDS
# Header VALUES kept from the wire; every other header keeps only its name.
HEADER_VALUE_ALLOWLIST = frozenset({"user-agent", "originator", "openai-beta", "version"})
# (bundle path, committed observed path) pairs compared verbatim besides the runtime digest.
WIRE_FIELDS = (
    ("https.ja4", ("https", "ja4")),
    ("https.ja3_hash_sorted_extensions", ("https", "ja3_hash_sorted_extensions")),
    ("https.negotiated_alpn", ("https", "negotiated_alpn")),
    ("https.http2_akamai", ("https", "http2_akamai")),
    ("websocket.ja4", ("websocket", "ja4")),
    ("websocket.ja3_hash_sorted_extensions", ("websocket", "ja3_hash_sorted_extensions")),
    ("websocket.alpn_extension", ("websocket", "alpn_extension")),
    ("websocket.http_version", ("websocket", "http_version")),
    ("websocket.url_path", ("websocket", "url_path")),
    ("session_id_length", ("session_id_length",)),
)
_SSE = (
    'event: response.created\ndata: {"type":"response.created","response":{"id":"resp_cap",'
    '"status":"in_progress","output":[]}}\n\n'
    'event: response.completed\ndata: {"type":"response.completed","response":{"id":"resp_cap",'
    '"status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}\n\n'
).encode()


class InvalidEvidence(ValueError):
    pass


# --------------------------------------------------------------------------- ClientHello


class _Hello:
    """Shim matching the mitmproxy ClientHello attributes the Kiro parser reads."""

    def __init__(self, sni: str, cipher_suites: list[int], extensions: list[tuple[int, bytes]]) -> None:
        self.sni = sni
        self.cipher_suites = cipher_suites
        self.extensions = extensions


def client_hello_length(buf: bytes) -> int | None:
    """Bytes of TLS records needed to hold the full ClientHello, or None if unknown yet."""
    if len(buf) < 9:
        return None
    if buf[0] != 22:
        raise InvalidEvidence("first record is not a TLS handshake")
    need = int.from_bytes(buf[6:9], "big") + 4
    total = pos = got = 0
    while got < need:
        if pos + 5 > len(buf):
            return None
        rec_len = int.from_bytes(buf[pos + 3 : pos + 5], "big")
        pos += 5 + rec_len
        got += rec_len
        total = pos
    return total


def parse_client_hello_bytes(raw: bytes) -> dict[str, Any]:
    """Raw TLS records → the Kiro record shape plus session_id_length."""
    handshake = bytearray()
    pos = 0
    while pos + 5 <= len(raw) and raw[pos] == 22:
        rec_len = int.from_bytes(raw[pos + 3 : pos + 5], "big")
        handshake += raw[pos + 5 : pos + 5 + rec_len]
        pos += 5 + rec_len
    if len(handshake) < 4 or handshake[0] != 1:
        raise InvalidEvidence("no ClientHello")
    body = bytes(handshake[4 : 4 + int.from_bytes(handshake[1:4], "big")])
    try:
        legacy_version = int.from_bytes(body[0:2], "big")
        p = 34
        session_id_length = body[p]
        p += 1 + session_id_length
        cipher_len = int.from_bytes(body[p : p + 2], "big")
        ciphers = [int.from_bytes(body[i : i + 2], "big") for i in range(p + 2, p + 2 + cipher_len, 2)]
        p += 2 + cipher_len
        p += 1 + body[p]  # compression methods
        ext_end = p + 2 + int.from_bytes(body[p : p + 2], "big")
        p += 2
        extensions: list[tuple[int, bytes]] = []
        sni = ""
        while p + 4 <= ext_end:
            ext_type = int.from_bytes(body[p : p + 2], "big")
            ext_len = int.from_bytes(body[p + 2 : p + 4], "big")
            data = body[p + 4 : p + 4 + ext_len]
            p += 4 + ext_len
            extensions.append((ext_type, data))
            if ext_type == 0 and len(data) > 5 and data[2] == 0:
                sni = data[5 : 5 + int.from_bytes(data[3:5], "big")].decode("ascii", "strict")
    except (IndexError, UnicodeDecodeError) as exc:
        raise InvalidEvidence(f"truncated ClientHello: {exc}") from exc
    record = kiro_hello.parse_client_hello(_Hello(sni, ciphers, extensions))
    record["version"] = legacy_version
    record["session_id_length"] = session_id_length
    return record


def _hex4(values: list[int]) -> str:
    return ",".join(f"{value:04x}" for value in values)


def _sha12(text: str) -> str:
    return hashlib.sha256(text.encode()).hexdigest()[:12] if text else "000000000000"


def compute_ja4(record: dict[str, Any]) -> str:
    """JA4 (TCP) from a ClientHello record; GREASE ignored, SNI/ALPN excluded from part c."""
    grease = kiro.GREASE_VALUES
    versions = [v for v in record.get("supported_versions", []) if v not in grease]
    tls = "13" if 0x0304 in versions else {0x0303: "12", 0x0302: "11", 0x0301: "10"}.get(record.get("version", 0x0303), "00")
    ciphers = [v for v in record["cipher_suites"] if v not in grease]
    extensions = [v for v in record["extensions"] if v not in grease]
    alpn = record.get("alpn_protocols") or []
    alpn_code = (alpn[0][0] + alpn[0][-1]) if alpn and alpn[0] else "00"
    sni = "d" if record.get("server_name") else "i"
    part_c = _hex4(sorted(v for v in extensions if v not in (0, 16)))
    sigalgs = record.get("signature_algorithms") or []
    if sigalgs:
        part_c += "_" + _hex4(sigalgs)
    head = f"t{tls}{sni}{min(len(ciphers), 99):02d}{min(len(extensions), 99):02d}{alpn_code}"
    return f"{head}_{_sha12(_hex4(sorted(ciphers)))}_{_sha12(part_c)}"


def ja3_sorted(record: dict[str, Any]) -> tuple[str, str]:
    """JA3 with the extension list sorted — stable under rustls extension shuffling."""
    return kiro.compute_ja3(
        int(record.get("version", 771)),
        record["cipher_suites"],
        sorted(record["extensions"]),
        record["curves"],
        record["point_formats"],
    )


# --------------------------------------------------------------------------- local endpoint


def _headers_projection(pairs: list[tuple[str, str]]) -> dict[str, Any]:
    names = [name.lower() for name, _ in pairs]
    values = {name.lower(): value for name, value in pairs if name.lower() in HEADER_VALUE_ALLOWLIST}
    return {"header_names": names, "header_values": values}


def _frame(ftype: int, flags: int, stream_id: int, payload: bytes = b"") -> bytes:
    return len(payload).to_bytes(3, "big") + bytes([ftype, flags]) + stream_id.to_bytes(4, "big") + payload


def _recv_exact(conn: Any, size: int) -> bytes:
    out = bytearray()
    while len(out) < size:
        chunk = conn.recv(size - len(out))
        if not chunk:
            raise EOFError("connection closed")
        out += chunk
    return bytes(out)


def serve_http2(conn: Any) -> dict[str, Any]:
    """Minimal HTTP/2 server: record client SETTINGS / WINDOW_UPDATE / request headers.

    Answers ``.../responses`` POSTs with a two-event SSE stream and anything
    else with 404 so codex completes without upstream contact.
    """
    result: dict[str, Any] = {"settings": None, "connection_window_update": None, "requests": []}
    if _recv_exact(conn, 24) != b"PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n":
        result["error"] = "bad preface"
        return result
    conn.sendall(_frame(4, 0, 0))
    decoder = hpack_lite.Decoder()
    pending: dict[int, bytearray] = {}
    paths: dict[int, str] = {}

    def respond(stream_id: int) -> None:
        if paths.get(stream_id, "").endswith("/responses"):
            block = hpack_lite.encode_response(200, [("content-type", "text/event-stream")])
            conn.sendall(_frame(1, 0x4, stream_id, block) + _frame(0, 0x1, stream_id, _SSE))
        else:
            conn.sendall(_frame(1, 0x5, stream_id, hpack_lite.encode_response(404)))

    try:
        while True:
            header = _recv_exact(conn, 9)
            length = int.from_bytes(header[0:3], "big")
            ftype, flags = header[3], header[4]
            stream_id = int.from_bytes(header[5:9], "big") & 0x7FFFFFFF
            payload = _recv_exact(conn, length) if length else b""
            if ftype == 4 and not flags & 0x1:  # SETTINGS
                if result["settings"] is None:
                    result["settings"] = [
                        [int.from_bytes(payload[i : i + 2], "big"), int.from_bytes(payload[i + 2 : i + 6], "big")]
                        for i in range(0, len(payload) - 5, 6)
                    ]
                conn.sendall(_frame(4, 0x1, 0))
            elif ftype == 8 and stream_id == 0 and result["connection_window_update"] is None:
                result["connection_window_update"] = int.from_bytes(payload[0:4], "big") & 0x7FFFFFFF
            elif ftype in (1, 9):  # HEADERS / CONTINUATION
                block = payload
                if ftype == 1:
                    if flags & 0x8:
                        block = block[1 : len(block) - block[0]]
                    if flags & 0x20:
                        block = block[5:]
                    pending[stream_id] = bytearray()
                    pending[stream_id + (1 << 31)] = bytearray([flags & 0x1])  # remember END_STREAM
                pending[stream_id] += block
                if flags & 0x4:
                    pairs = decoder.decode(bytes(pending.pop(stream_id)))
                    end_stream = bool(pending.pop(stream_id + (1 << 31), b"\x00")[0])
                    pseudo = {name: value for name, value in pairs if name.startswith(":")}
                    path = pseudo.get(":path", "").split("?", 1)[0]
                    paths[stream_id] = path
                    result["requests"].append({
                        "method": pseudo.get(":method", ""),
                        "path": path,
                        "pseudo_order": ",".join(name[1] for name, _ in pairs if name.startswith(":")),
                        **_headers_projection([(n, v) for n, v in pairs if not n.startswith(":")]),
                    })
                    if end_stream:
                        respond(stream_id)
            elif ftype == 0:  # DATA
                if length:
                    inc = length.to_bytes(4, "big")
                    conn.sendall(_frame(8, 0, 0, inc) + _frame(8, 0, stream_id, inc))
                if flags & 0x1:
                    respond(stream_id)
            elif ftype == 6 and not flags & 0x1:  # PING
                conn.sendall(_frame(6, 0x1, 0, payload))
            elif ftype == 7:  # GOAWAY
                break
    except (EOFError, OSError, hpack_lite.HPACKError) as exc:
        result.setdefault("end", type(exc).__name__)
    return result


def serve_http1(conn: Any) -> dict[str, Any]:
    raw = bytearray()
    while b"\r\n\r\n" not in raw and len(raw) < 65536:
        chunk = conn.recv(4096)
        if not chunk:
            break
        raw += chunk
    lines = bytes(raw).split(b"\r\n\r\n", 1)[0].decode("latin-1").split("\r\n")
    parts = lines[0].split(" ") if lines and lines[0] else []
    pairs = [(ln.split(":", 1)[0].strip(), ln.split(":", 1)[1].strip()) for ln in lines[1:] if ":" in ln]
    projection = _headers_projection(pairs)
    upgrade = {name.lower(): value for name, value in pairs}.get("upgrade", "").lower()
    if upgrade == "websocket":
        conn.sendall(b"HTTP/1.1 426 Upgrade Required\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
    else:
        conn.sendall(b"HTTP/1.1 404 Not Found\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
    return {
        "method": parts[0] if parts else "",
        "path": parts[1].split("?", 1)[0] if len(parts) > 1 else "",
        "http_version": parts[2] if len(parts) > 2 else "",
        "upgrade": upgrade,
        **projection,
    }


class CaptureEndpoint:
    """Local CONNECT proxy that terminates TLS itself; never connects upstream."""

    def __init__(self, cert: Path, key: Path, port: int = 0) -> None:
        self.ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        self.ctx.load_cert_chain(str(cert), str(key))
        self.ctx.set_alpn_protocols(["h2", "http/1.1"])
        # No session tickets: every ClientHello is a first flight, so a resumed
        # pre_shared_key extension can never masquerade as an extension change.
        if hasattr(self.ctx, "num_tickets"):
            self.ctx.num_tickets = 0
        self.sock = socket.create_server(("127.0.0.1", port))
        self.port = self.sock.getsockname()[1]
        self.records: list[dict[str, Any]] = []
        self.rejected_hosts: list[str] = []
        self.errors: list[str] = []
        self._lock = threading.Lock()
        self._stop = threading.Event()
        self._thread = threading.Thread(target=self._accept_loop, daemon=True)

    def start(self) -> "CaptureEndpoint":
        self._thread.start()
        return self

    def stop(self) -> None:
        self._stop.set()
        self.sock.close()

    def _accept_loop(self) -> None:
        while not self._stop.is_set():
            try:
                conn, _ = self.sock.accept()
            except OSError:
                return
            threading.Thread(target=self._handle, args=(conn,), daemon=True).start()

    def _handle(self, conn: socket.socket) -> None:
        conn.settimeout(15)
        try:
            head = bytearray()
            while b"\r\n\r\n" not in head and len(head) < 8192:
                chunk = conn.recv(1024)
                if not chunk:
                    return
                head += chunk
            request_line = bytes(head).split(b"\r\n", 1)[0].decode("latin-1").split(" ")
            target = request_line[1] if len(request_line) > 1 else ""
            host = target.rsplit(":", 1)[0]
            if request_line[0] != "CONNECT" or host != CAPTURE_HOST:
                with self._lock:
                    self.rejected_hosts.append(host)
                conn.sendall(b"HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n")
                return
            conn.sendall(b"HTTP/1.1 200 Connection Established\r\n\r\n")
            raw = self._peek_client_hello(conn)
            record = parse_client_hello_bytes(raw)
            tls_conn = self.ctx.wrap_socket(conn, server_side=True)
            negotiated = tls_conn.selected_alpn_protocol() or ""
            record["negotiated_alpn"] = negotiated
            record["tls_version"] = tls_conn.version() or ""
            if negotiated == "h2":
                record["http2"] = serve_http2(tls_conn)
            else:
                record["http1"] = serve_http1(tls_conn)
            with self._lock:
                self.records.append(record)
        except (OSError, ssl.SSLError, InvalidEvidence, EOFError, ValueError) as exc:
            with self._lock:
                self.errors.append(f"{type(exc).__name__}: {exc}")
        finally:
            conn.close()

    @staticmethod
    def _peek_client_hello(conn: socket.socket) -> bytes:
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            buf = conn.recv(65536, socket.MSG_PEEK)
            if not buf:
                raise EOFError("closed before ClientHello")
            need = client_hello_length(buf)
            if need is not None and len(buf) >= need:
                return buf[:need]
            time.sleep(0.01)
        raise InvalidEvidence("ClientHello not received")


# --------------------------------------------------------------------------- capture driver


def _openssl() -> str:
    for candidate in (os.environ.get("TOKENKEY_CODEX_OPENSSL", ""), "/opt/homebrew/bin/openssl", "openssl"):
        if candidate and shutil.which(candidate):
            return candidate
    raise InvalidEvidence("openssl not found (set TOKENKEY_CODEX_OPENSSL)")


def make_local_ca(work: Path) -> tuple[Path, Path, Path]:
    """Per-run CA + chatgpt.com leaf. Lives only in the run's temp dir."""
    openssl = _openssl()
    ext = work / "leaf.ext"
    ext.write_text(
        "basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature\n"
        f"extendedKeyUsage=serverAuth\nsubjectAltName=DNS:{CAPTURE_HOST}\n",
        encoding="utf-8",
    )
    ca_ext = work / "ca.ext"
    ca_ext.write_text("basicConstraints=critical,CA:TRUE\nkeyUsage=critical,keyCertSign,cRLSign\n", encoding="utf-8")
    run = lambda *args: subprocess.run([openssl, *args], check=True, capture_output=True)  # noqa: E731
    run("ecparam", "-name", "prime256v1", "-genkey", "-noout", "-out", str(work / "ca.key"))
    run("req", "-new", "-key", str(work / "ca.key"), "-subj", "/CN=tokenkey codex tls capture local CA", "-out", str(work / "ca.csr"))
    run("x509", "-req", "-in", str(work / "ca.csr"), "-signkey", str(work / "ca.key"), "-days", "1",
        "-extfile", str(ca_ext), "-out", str(work / "ca.pem"))
    run("ecparam", "-name", "prime256v1", "-genkey", "-noout", "-out", str(work / "leaf.key"))
    run("req", "-new", "-key", str(work / "leaf.key"), "-subj", f"/CN={CAPTURE_HOST}", "-out", str(work / "leaf.csr"))
    run("x509", "-req", "-in", str(work / "leaf.csr"), "-CA", str(work / "ca.pem"), "-CAkey", str(work / "ca.key"),
        "-set_serial", str(int(time.time())), "-days", "1", "-extfile", str(ext), "-out", str(work / "leaf.pem"))
    return work / "ca.pem", work / "leaf.pem", work / "leaf.key"


CODEX_CONFIG = """\
model = "gpt-5.5"
model_provider = "tkcap"

[model_providers.tkcap]
name = "tkcap"
base_url = "https://{host}/backend-api/codex"
wire_api = "responses"
env_key = "TK_CODEX_TLS_CAPTURE_DUMMY_KEY"
supports_websockets = true
"""


def codex_child_env(codex_home: Path, ca: Path, port: int) -> dict[str, str]:
    """Child env with every real credential/proxy variable removed."""
    drop = re.compile(r"^(OPENAI_|CODEX_|AZURE_OPENAI_|HTTPS?_PROXY$|ALL_PROXY$|NO_PROXY$)", re.I)
    env = {key: value for key, value in os.environ.items() if not drop.match(key)}
    proxy = f"http://127.0.0.1:{port}"
    env.update({
        "CODEX_HOME": str(codex_home),
        "TK_CODEX_TLS_CAPTURE_DUMMY_KEY": "sk-tk-local-capture-dummy",
        "CODEX_CA_CERTIFICATE": str(ca),
        "SSL_CERT_FILE": str(ca),
        "HTTPS_PROXY": proxy, "https_proxy": proxy,
        "HTTP_PROXY": proxy, "http_proxy": proxy,
        "ALL_PROXY": proxy, "all_proxy": proxy,
    })
    return env


def run_capture(codex: str, runs: int, timeout: int, out_dir: Path) -> tuple[Path, dict[str, Any]]:
    sys.path.insert(0, str(HERE))
    import capture_codex_fingerprint as static  # version owner parse

    version = static.parse_codex_version(
        subprocess.run([codex, "--version"], capture_output=True, text=True, check=True).stdout
    )
    if not version:
        raise InvalidEvidence("could not parse `codex --version`")
    with tempfile.TemporaryDirectory(prefix="tk-codex-tls-") as tmp:
        work = Path(tmp)
        ca, leaf, key = make_local_ca(work)
        home = work / "codex-home"
        home.mkdir()
        (home / "config.toml").write_text(CODEX_CONFIG.format(host=CAPTURE_HOST), encoding="utf-8")
        cwd = work / "cwd"
        cwd.mkdir()
        endpoint = CaptureEndpoint(leaf, key).start()
        env = codex_child_env(home, ca, endpoint.port)
        run_codes: list[int] = []
        try:
            for _ in range(runs):
                try:
                    proc = subprocess.run(
                        [codex, "exec", "--skip-git-repo-check", "--ephemeral", "-s", "read-only",
                         "-C", str(cwd), "Reply with the single word pong"],
                        env=env, cwd=cwd, stdin=subprocess.DEVNULL, capture_output=True, timeout=timeout,
                    )
                    run_codes.append(proc.returncode)
                except subprocess.TimeoutExpired:
                    run_codes.append(-1)
            time.sleep(0.5)
        finally:
            endpoint.stop()
        records = list(endpoint.records)
        meta = {"codex_runs": runs, "codex_exit_codes": run_codes,
                "endpoint_errors": sorted(set(endpoint.errors)), "rejected_connect_hosts": sorted(set(endpoint.rejected_hosts))}
    bundle = assemble_bundle(records, codex_cli_version=version, meta=meta)
    out_dir.mkdir(parents=True, exist_ok=True)
    stamp = datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ")
    path = out_dir / f"{stamp}-codex-cli-tls.bundle.json"
    path.write_text(json.dumps(bundle, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    return path, bundle


# --------------------------------------------------------------------------- bundle (pure)


def classify_record(record: dict[str, Any]) -> str:
    if record.get("negotiated_alpn") == "h2" and (record.get("http2") or {}).get("requests"):
        return "https"
    http1 = record.get("http1") or {}
    if http1.get("upgrade") == "websocket":
        return "wss"
    return "unclassified"


def assemble_bundle(records: list[dict[str, Any]], *, codex_cli_version: str, captured_at: str = "",
                    meta: dict[str, Any] | None = None) -> dict[str, Any]:
    bundle = {
        "schema_version": SCHEMA_VERSION,
        "profile_name": PROFILE_NAME,
        "source": CAPTURE_SOURCE,
        "captured_at": captured_at or datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "codex_cli_version": codex_cli_version,
        "capture_meta": meta or {},
        "records": [{**record, "lane": classify_record(record)} for record in records],
    }
    errors = kiro._secret_errors(bundle)
    if errors:
        raise InvalidEvidence(f"bundle carries secret-bearing evidence: {errors[0]}")
    return bundle


def _uniform(values: list[Any], what: str) -> Any:
    if not values:
        raise InvalidEvidence(f"{what}: not observed")
    if any(value != values[0] for value in values[1:]):
        raise InvalidEvidence(f"{what} varies across samples: {sorted({json.dumps(v) for v in values})}")
    return values[0]


def _lane_semantics(records: list[dict[str, Any]], lane: str) -> dict[str, Any]:
    if len(records) < MIN_LANE_SAMPLES:
        raise InvalidEvidence(f"{lane}: need at least {MIN_LANE_SAMPLES} ClientHellos, got {len(records)}")
    for record in records:
        if record.get("server_name") != CAPTURE_HOST:
            raise InvalidEvidence(f"{lane}: unexpected SNI {record.get('server_name')!r}")
    semantics = _uniform([kiro._tls_semantics(record) for record in records], f"{lane} TLS semantic fields")
    orders = {tuple(kiro._strip_grease(record["extensions"])) for record in records}
    ja3_raw, ja3_hash = ja3_sorted(records[0])
    return {
        **semantics,
        "shuffle_extensions": len(orders) > 1,
        "sample_count": len(records),
        "distinct_extension_orders": len(orders),
        "ja4": _uniform([compute_ja4(record) for record in records], f"{lane} JA4"),
        "ja3_raw_sorted_extensions": ja3_raw,
        "ja3_hash_sorted_extensions": ja3_hash,
        "session_id_length": _uniform([record.get("session_id_length") for record in records], f"{lane} session id length"),
    }


def akamai_h2(http2: dict[str, Any]) -> str:
    settings = ";".join(f"{key}:{value}" for key, value in http2.get("settings") or [])
    first = (http2.get("requests") or [{}])[0]
    return f"{settings}|{http2.get('connection_window_update')}|0|{first.get('pseudo_order', '')}"


def observe(bundle: dict[str, Any]) -> dict[str, Any]:
    """Deterministic projection of a bundle; raises InvalidEvidence when unusable."""
    if bundle.get("schema_version") != SCHEMA_VERSION or bundle.get("source") != CAPTURE_SOURCE:
        raise InvalidEvidence("not a codex TLS capture bundle")
    version = str(bundle.get("codex_cli_version") or "")
    if not re.fullmatch(r"\d+\.\d+\.\d+(?:-[0-9A-Za-z.]+)?", version):
        raise InvalidEvidence("codex_cli_version missing")
    records = bundle.get("records")
    if not isinstance(records, list):
        raise InvalidEvidence("records missing")
    try:
        https = [r for r in records if classify_record(r) == "https"]
        wss = [r for r in records if classify_record(r) == "wss"]
        h = _lane_semantics(https, "https")
        w = _lane_semantics(wss, "wss")
        http2 = [r["http2"] for r in https]
        pseudo = _uniform([req.get("pseudo_order") for item in http2 for req in item.get("requests", [])], "HTTP/2 pseudo-header order")
        akamai = _uniform([akamai_h2(item) for item in http2], "HTTP/2 SETTINGS/WINDOW_UPDATE")
        https_paths = sorted({req.get("path", "") for item in http2 for req in item.get("requests", [])})
        if not any(path.endswith("/responses") for path in https_paths):
            raise InvalidEvidence("https: no Responses request observed")
        wss_paths = sorted({r["http1"].get("path", "") for r in wss})
    except (KeyError, TypeError) as exc:
        raise InvalidEvidence(f"malformed record: {exc}") from exc
    strip_alpn = lambda s: {**{k: s[k] for k in RUNTIME_FIELDS if k != "shuffle_extensions"},  # noqa: E731
                            "alpn_protocols": [], "extensions": sorted(set(s["extensions"]) - {16})}
    return {
        "codex_cli_version": version,
        "captured_at": str(bundle.get("captured_at") or ""),
        "runtime": kiro.runtime_profile_projection(h),
        "runtime_digest": kiro.runtime_profile_digest(h),
        "sample_count": len(https) + len(wss),
        "server_names": sorted({r["server_name"] for r in https + wss}),
        "https": {
            "sample_count": h["sample_count"],
            "distinct_extension_orders": h["distinct_extension_orders"],
            "ja4": h["ja4"],
            "ja3_raw_sorted_extensions": h["ja3_raw_sorted_extensions"],
            "ja3_hash_sorted_extensions": h["ja3_hash_sorted_extensions"],
            "negotiated_alpn": _uniform([r["negotiated_alpn"] for r in https], "negotiated ALPN"),
            "http2_akamai": akamai,
            "pseudo_order": pseudo,
            "paths": https_paths,
        },
        "websocket": {
            "sample_count": w["sample_count"],
            "distinct_extension_orders": w["distinct_extension_orders"],
            "url_path": _uniform(wss_paths, "WSS path") if len(wss_paths) == 1 else ",".join(wss_paths),
            "ja4": w["ja4"],
            "ja3_hash_sorted_extensions": w["ja3_hash_sorted_extensions"],
            "alpn_extension": "present" if 16 in w["extensions"] else "absent",
            "http_version": "HTTP/1.1 Upgrade" if all(r["http1"].get("http_version") == "HTTP/1.1" for r in wss) else "other",
            "same_hello_as_https_without_alpn": strip_alpn(w) == strip_alpn(h),
        },
        "session_id_length": _uniform([h["session_id_length"], w["session_id_length"]], "session id length"),
        "grease": bool(h["enable_grease"]),
    }


# --------------------------------------------------------------------------- decision (pure)


def _dig(value: Any, path: tuple[str, ...]) -> Any:
    for key in path:
        value = value.get(key) if isinstance(value, dict) else None
    return value


def decide(bundle: dict[str, Any], committed: dict[str, Any] | None) -> dict[str, Any]:
    """aligned / version_only / tls_drift / invalid with field-level reasons."""
    try:
        if committed is None:
            raise InvalidEvidence(f"committed profile missing: {CODEX_TLS_PROFILE_JSON}")
        obs = observe(bundle)
        committed_runtime = kiro.runtime_profile_projection(committed)
        committed_digest = kiro.runtime_profile_digest(committed)
    except (InvalidEvidence, ValueError) as exc:
        return {"verdict": "invalid", "exit_code": EXIT_INVALID, "error": str(exc)}
    committed_obs = committed.get("observed") or {}
    runtime_diffs = [
        {"field": field, "committed": committed_runtime[field], "observed": obs["runtime"][field]}
        for field in RUNTIME_FIELDS if committed_runtime[field] != obs["runtime"][field]
    ]
    wire_diffs = [
        {"field": label, "committed": _dig(committed_obs, path), "observed": _dig(obs, path)}
        for label, path in WIRE_FIELDS if _dig(committed_obs, path) != _dig(obs, path)
    ]
    if not obs["websocket"]["same_hello_as_https_without_alpn"]:
        wire_diffs.append({"field": "websocket.same_hello_as_https_without_alpn", "committed": True, "observed": False})
    committed_version = str(committed_obs.get("codex_cli_version") or "")
    result: dict[str, Any] = {
        "codex_cli_version": {"committed": committed_version, "observed": obs["codex_cli_version"]},
        "runtime_digest": {"committed": committed_digest, "observed": obs["runtime_digest"]},
        "runtime_diffs": runtime_diffs,
        "wire_diffs": wire_diffs,
        "observed": obs,
    }
    if runtime_diffs or committed_digest != obs["runtime_digest"]:
        result.update(verdict="tls_drift", exit_code=EXIT_DRIFT, migration_required=True, actions=[
            "emit-profile --bundle <bundle> to refresh deploy/aws/stage0/tk_canonical_codex_cli.json",
            "add a NEW forward migration updating tls_fingerprint_profiles row tk_canonical_codex_cli (never edit a published migration)",
            "append a docs/ops/codex-fingerprint-changelog.md row with the new runtime digest",
        ])
    elif wire_diffs:
        result.update(verdict="tls_drift", exit_code=EXIT_DRIFT, migration_required=False, actions=[
            "runtime digest unchanged: no migration; fix the Go wire owner (repository/http_upstream_tk_codex_h2.go for HTTP/2, service/openai_ws_client_tk_tls.go for WSS)",
            "emit-profile --bundle <bundle>, then append a changelog row",
        ])
    elif committed_version != obs["codex_cli_version"]:
        result.update(verdict="version_only", exit_code=EXIT_DRIFT, migration_required=False, actions=[
            "emit-profile --bundle <bundle> (observed version only), append a changelog row with the unchanged digest; no migration",
        ])
    else:
        result.update(verdict="aligned", exit_code=EXIT_ALIGNED, migration_required=False, actions=[])
    return result


# --------------------------------------------------------------------------- canonical profile


DEFAULT_DESCRIPTION = (
    "TokenKey canonical Codex CLI TLS profile captured from real codex ClientHello; rustls randomizes "
    "extension order per connection, so shuffle_extensions=true and JA4 (not JA3) is the stable identity."
)
DEFAULT_NOTE = (
    "Captured by pointing codex at a local CONNECT capture endpoint with a throwaway CODEX_HOME, dummy key "
    "and a local CA (CODEX_CA_CERTIFICATE); no request left the machine. ClientHello semantics are host-independent."
)


def build_canonical_profile(bundle: dict[str, Any], template: dict[str, Any] | None = None) -> dict[str, Any]:
    obs = observe(bundle)
    if not obs["runtime"]["shuffle_extensions"]:
        raise InvalidEvidence("extension permutation not observed; refusing to emit shuffle_extensions=false blindly")
    template = template or {}
    t_obs = template.get("observed") or {}
    https, wss = obs["https"], obs["websocket"]
    profile = {
        "name": PROFILE_NAME,
        "description": template.get("description") or DEFAULT_DESCRIPTION,
        **obs["runtime"],
        "observed": {
            "source": CAPTURE_SOURCE,
            "codex_cli_version": obs["codex_cli_version"],
            "captured_at": obs["captured_at"][:10],
            "sample_count": obs["sample_count"],
            "server_names": obs["server_names"],
            "https": {
                "ja4": https["ja4"],
                "ja3_raw_sorted_extensions": https["ja3_raw_sorted_extensions"],
                "ja3_hash_sorted_extensions": https["ja3_hash_sorted_extensions"],
                "ja3_hash_note": (
                    f"wire JA3 differs per connection ({https['distinct_extension_orders']}/{https['sample_count']} "
                    "distinct) because rustls shuffles extension order"
                ),
                "negotiated_alpn": https["negotiated_alpn"],
                "http2_akamai": https["http2_akamai"],
            },
            "websocket": {
                "url_path": wss["url_path"],
                "ja4": wss["ja4"],
                "ja3_hash_sorted_extensions": wss["ja3_hash_sorted_extensions"],
                "alpn_extension": wss["alpn_extension"],
                "http_version": wss["http_version"],
            },
            "session_id_length": obs["session_id_length"],
            "grease": obs["grease"],
            "runtime_digest": obs["runtime_digest"],
            "note": t_obs.get("note") or DEFAULT_NOTE,
        },
    }
    # Prose (description / note) is operator-authored and may say "dummy bearer";
    # only the observed evidence is screened.
    evidence = {k: v for k, v in profile["observed"].items() if k != "note"}
    if kiro._secret_errors(evidence):
        raise InvalidEvidence("candidate profile contains secret-bearing evidence")
    return profile


# --------------------------------------------------------------------------- migration parity


_SQL_FIELDS = ("name", "description", "enable_grease", "shuffle_extensions") + tuple(
    f for f in RUNTIME_FIELDS if f not in ("enable_grease", "shuffle_extensions")
)


def latest_seed_migration(directory: Path = MIGRATIONS_DIR) -> Path | None:
    """Highest-numbered tk_* migration that writes the tk_canonical_codex_cli row."""
    found = []
    for path in directory.glob("tk_*.sql"):
        text = path.read_text(encoding="utf-8")
        if f"'{PROFILE_NAME}'" in text and "tls_fingerprint_profiles" in text:
            match = re.match(r"tk_(\d+)_", path.name)
            if match:
                found.append((int(match.group(1)), path))
    return max(found)[1] if found else None


def parse_seed_migration(sql: str) -> dict[str, Any]:
    """Parse the INSERT (columns) VALUES (...) of a tls_fingerprint_profiles seed."""
    body = "\n".join(line for line in sql.splitlines() if not line.lstrip().startswith("--"))
    match = re.search(r"INSERT INTO tls_fingerprint_profiles\s*\((.*?)\)\s*VALUES\s*\((.*?)\)\s*ON CONFLICT", body, re.S)
    if not match:
        raise InvalidEvidence("seed INSERT not found")
    columns = [c.strip() for c in match.group(1).split(",")]
    values: list[Any] = []
    for token in re.finditer(r"'((?:[^']|'')*)'(::jsonb)?|\b(true|false)\b", match.group(2)):
        if token.group(3):
            values.append(token.group(3) == "true")
        elif token.group(2):
            values.append(json.loads(token.group(1)))
        else:
            values.append(token.group(1).replace("''", "'"))
    if len(columns) != len(values):
        raise InvalidEvidence(f"{len(columns)} columns but {len(values)} values")
    return dict(zip(columns, values))


# --------------------------------------------------------------------------- CLI


def load_committed(path: Path = CODEX_TLS_PROFILE_JSON) -> dict[str, Any] | None:
    return json.loads(path.read_text(encoding="utf-8")) if path.is_file() else None


def print_decision(result: dict[str, Any], stream=sys.stdout) -> None:
    print(f"verdict={result['verdict']}", file=stream)
    if result["verdict"] == "invalid":
        print(f"  error: {result['error']}", file=stream)
        return
    obs = result["observed"]
    ver, dig = result["codex_cli_version"], result["runtime_digest"]
    print(f"  codex_cli_version: committed={ver['committed']} observed={ver['observed']}", file=stream)
    state = "unchanged" if dig["committed"] == dig["observed"] else "CHANGED"
    print(f"  runtime_digest: committed={dig['committed'][:16]}… observed={dig['observed'][:16]}… ({state})", file=stream)
    print(f"  https: samples={obs['https']['sample_count']} distinct_extension_orders={obs['https']['distinct_extension_orders']} "
          f"ja4={obs['https']['ja4']} alpn={obs['https']['negotiated_alpn']} h2={obs['https']['http2_akamai']}", file=stream)
    print(f"  wss:   samples={obs['websocket']['sample_count']} distinct_extension_orders={obs['websocket']['distinct_extension_orders']} "
          f"ja4={obs['websocket']['ja4']} alpn_extension={obs['websocket']['alpn_extension']} path={obs['websocket']['url_path']}", file=stream)
    for diff in result["runtime_diffs"]:
        print(f"  runtime drift {diff['field']}: committed={diff['committed']} observed={diff['observed']}", file=stream)
    for diff in result["wire_diffs"]:
        print(f"  wire drift {diff['field']}: committed={diff['committed']} observed={diff['observed']}", file=stream)
    print(f"  migration_required={str(result['migration_required']).lower()}", file=stream)
    for action in result["actions"] or ["none"]:
        print(f"  action: {action}", file=stream)


def _load_bundle(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(value, dict):
        raise InvalidEvidence("bundle root must be an object")
    return value


def cmd_capture(args: argparse.Namespace) -> int:
    if args.samples < 1:
        print("error: --samples must be >= 1", file=sys.stderr)
        return EXIT_INVALID
    codex = shutil.which(args.codex)
    if not codex:
        print(f"error: codex CLI not found: {args.codex}", file=sys.stderr)
        return EXIT_INVALID
    path, bundle = run_capture(codex, args.samples, args.timeout, args.out_dir)
    lanes: dict[str, int] = {}
    for record in bundle["records"]:
        lanes[record["lane"]] = lanes.get(record["lane"], 0) + 1
    meta = bundle["capture_meta"]
    print(f"bundle={path}")
    print(f"codex_cli_version={bundle['codex_cli_version']} codex_exit_codes={meta['codex_exit_codes']}")
    print(f"clienthellos={json.dumps(lanes, sort_keys=True)} rejected_connect_hosts={meta['rejected_connect_hosts']}")
    for error in meta["endpoint_errors"]:
        print(f"endpoint: {error}")
    result = decide(bundle, load_committed())
    print_decision(result)
    return result["exit_code"]


def cmd_check(args: argparse.Namespace) -> int:
    result = decide(_load_bundle(args.bundle), load_committed(args.profile))
    if args.json:
        print(json.dumps({k: v for k, v in result.items()}, ensure_ascii=False, indent=2, sort_keys=True))
    else:
        print_decision(result)
    return result["exit_code"]


def cmd_emit_profile(args: argparse.Namespace) -> int:
    profile = build_canonical_profile(_load_bundle(args.bundle), load_committed())
    out = args.out or CODEX_TLS_PROFILE_JSON
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(profile, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"wrote {out}")
    print(f"runtime_digest={profile['observed']['runtime_digest']}")
    return EXIT_ALIGNED


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = parser.add_subparsers(dest="command", required=True)
    cap = sub.add_parser("capture")
    cap.add_argument("--samples", type=int, default=3, help="number of real `codex exec` runs")
    cap.add_argument("--timeout", type=int, default=120)
    cap.add_argument("--codex", default=os.environ.get("TOKENKEY_CODEX_CLI", "codex"))
    cap.add_argument("--out-dir", type=Path, default=DEFAULT_OUT_DIR)
    cap.set_defaults(func=cmd_capture)
    check = sub.add_parser("check")
    check.add_argument("--bundle", type=Path, required=True)
    check.add_argument("--profile", type=Path, default=CODEX_TLS_PROFILE_JSON)
    check.add_argument("--json", action="store_true")
    check.set_defaults(func=cmd_check)
    emit = sub.add_parser("emit-profile")
    emit.add_argument("--bundle", type=Path, required=True)
    emit.add_argument("--out", type=Path)
    emit.set_defaults(func=cmd_emit_profile)
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        return args.func(args)
    except (OSError, json.JSONDecodeError, subprocess.CalledProcessError, InvalidEvidence, ValueError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return EXIT_INVALID


if __name__ == "__main__":
    raise SystemExit(main())
