"""Minimal stdlib HPACK (RFC 7541) decoder plus a tiny literal encoder.

Used only by the local Codex TLS capture endpoint to read the pseudo-header
order and header names of the first HTTP/2 request on a connection, and to
send a fixed response. The Huffman table is the RFC 7541 Appendix B table
(copied from Go's golang.org/x/net/http2/hpack tables.go).
"""
from __future__ import annotations

STATIC_TABLE: tuple[tuple[str, str], ...] = (
    (":authority", ""), (":method", "GET"), (":method", "POST"), (":path", "/"),
    (":path", "/index.html"), (":scheme", "http"), (":scheme", "https"),
    (":status", "200"), (":status", "204"), (":status", "206"), (":status", "304"),
    (":status", "400"), (":status", "404"), (":status", "500"), ("accept-charset", ""),
    ("accept-encoding", "gzip, deflate"), ("accept-language", ""), ("accept-ranges", ""),
    ("accept", ""), ("access-control-allow-origin", ""), ("age", ""), ("allow", ""),
    ("authorization", ""), ("cache-control", ""), ("content-disposition", ""),
    ("content-encoding", ""), ("content-language", ""), ("content-length", ""),
    ("content-location", ""), ("content-range", ""), ("content-type", ""), ("cookie", ""),
    ("date", ""), ("etag", ""), ("expect", ""), ("expires", ""), ("from", ""), ("host", ""),
    ("if-match", ""), ("if-modified-since", ""), ("if-none-match", ""), ("if-range", ""),
    ("if-unmodified-since", ""), ("last-modified", ""), ("link", ""), ("location", ""),
    ("max-forwards", ""), ("proxy-authenticate", ""), ("proxy-authorization", ""),
    ("range", ""), ("referer", ""), ("refresh", ""), ("retry-after", ""), ("server", ""),
    ("set-cookie", ""), ("strict-transport-security", ""), ("transfer-encoding", ""),
    ("user-agent", ""), ("vary", ""), ("via", ""), ("www-authenticate", ""),
)

# (code, bit length) for symbols 0..255; EOS (256) is never a valid decoded symbol.
HUFFMAN_CODES: tuple[tuple[int, int], ...] = (
    (0x1ff8, 13), (0x7fffd8, 23), (0xfffffe2, 28), (0xfffffe3, 28), (0xfffffe4, 28), (0xfffffe5, 28),
    (0xfffffe6, 28), (0xfffffe7, 28), (0xfffffe8, 28), (0xffffea, 24), (0x3ffffffc, 30), (0xfffffe9, 28),
    (0xfffffea, 28), (0x3ffffffd, 30), (0xfffffeb, 28), (0xfffffec, 28), (0xfffffed, 28), (0xfffffee, 28),
    (0xfffffef, 28), (0xffffff0, 28), (0xffffff1, 28), (0xffffff2, 28), (0x3ffffffe, 30), (0xffffff3, 28),
    (0xffffff4, 28), (0xffffff5, 28), (0xffffff6, 28), (0xffffff7, 28), (0xffffff8, 28), (0xffffff9, 28),
    (0xffffffa, 28), (0xffffffb, 28), (0x14, 6), (0x3f8, 10), (0x3f9, 10), (0xffa, 12), (0x1ff9, 13),
    (0x15, 6), (0xf8, 8), (0x7fa, 11), (0x3fa, 10), (0x3fb, 10), (0xf9, 8), (0x7fb, 11), (0xfa, 8),
    (0x16, 6), (0x17, 6), (0x18, 6), (0x0, 5), (0x1, 5), (0x2, 5), (0x19, 6), (0x1a, 6), (0x1b, 6),
    (0x1c, 6), (0x1d, 6), (0x1e, 6), (0x1f, 6), (0x5c, 7), (0xfb, 8), (0x7ffc, 15), (0x20, 6),
    (0xffb, 12), (0x3fc, 10), (0x1ffa, 13), (0x21, 6), (0x5d, 7), (0x5e, 7), (0x5f, 7), (0x60, 7),
    (0x61, 7), (0x62, 7), (0x63, 7), (0x64, 7), (0x65, 7), (0x66, 7), (0x67, 7), (0x68, 7), (0x69, 7),
    (0x6a, 7), (0x6b, 7), (0x6c, 7), (0x6d, 7), (0x6e, 7), (0x6f, 7), (0x70, 7), (0x71, 7), (0x72, 7),
    (0xfc, 8), (0x73, 7), (0xfd, 8), (0x1ffb, 13), (0x7fff0, 19), (0x1ffc, 13), (0x3ffc, 14), (0x22, 6),
    (0x7ffd, 15), (0x3, 5), (0x23, 6), (0x4, 5), (0x24, 6), (0x5, 5), (0x25, 6), (0x26, 6), (0x27, 6),
    (0x6, 5), (0x74, 7), (0x75, 7), (0x28, 6), (0x29, 6), (0x2a, 6), (0x7, 5), (0x2b, 6), (0x76, 7),
    (0x2c, 6), (0x8, 5), (0x9, 5), (0x2d, 6), (0x77, 7), (0x78, 7), (0x79, 7), (0x7a, 7), (0x7b, 7),
    (0x7ffe, 15), (0x7fc, 11), (0x3ffd, 14), (0x1ffd, 13), (0xffffffc, 28), (0xfffe6, 20), (0x3fffd2, 22),
    (0xfffe7, 20), (0xfffe8, 20), (0x3fffd3, 22), (0x3fffd4, 22), (0x3fffd5, 22), (0x7fffd9, 23),
    (0x3fffd6, 22), (0x7fffda, 23), (0x7fffdb, 23), (0x7fffdc, 23), (0x7fffdd, 23), (0x7fffde, 23),
    (0xffffeb, 24), (0x7fffdf, 23), (0xffffec, 24), (0xffffed, 24), (0x3fffd7, 22), (0x7fffe0, 23),
    (0xffffee, 24), (0x7fffe1, 23), (0x7fffe2, 23), (0x7fffe3, 23), (0x7fffe4, 23), (0x1fffdc, 21),
    (0x3fffd8, 22), (0x7fffe5, 23), (0x3fffd9, 22), (0x7fffe6, 23), (0x7fffe7, 23), (0xffffef, 24),
    (0x3fffda, 22), (0x1fffdd, 21), (0xfffe9, 20), (0x3fffdb, 22), (0x3fffdc, 22), (0x7fffe8, 23),
    (0x7fffe9, 23), (0x1fffde, 21), (0x7fffea, 23), (0x3fffdd, 22), (0x3fffde, 22), (0xfffff0, 24),
    (0x1fffdf, 21), (0x3fffdf, 22), (0x7fffeb, 23), (0x7fffec, 23), (0x1fffe0, 21), (0x1fffe1, 21),
    (0x3fffe0, 22), (0x1fffe2, 21), (0x7fffed, 23), (0x3fffe1, 22), (0x7fffee, 23), (0x7fffef, 23),
    (0xfffea, 20), (0x3fffe2, 22), (0x3fffe3, 22), (0x3fffe4, 22), (0x7ffff0, 23), (0x3fffe5, 22),
    (0x3fffe6, 22), (0x7ffff1, 23), (0x3ffffe0, 26), (0x3ffffe1, 26), (0xfffeb, 20), (0x7fff1, 19),
    (0x3fffe7, 22), (0x7ffff2, 23), (0x3fffe8, 22), (0x1ffffec, 25), (0x3ffffe2, 26), (0x3ffffe3, 26),
    (0x3ffffe4, 26), (0x7ffffde, 27), (0x7ffffdf, 27), (0x3ffffe5, 26), (0xfffff1, 24), (0x1ffffed, 25),
    (0x7fff2, 19), (0x1fffe3, 21), (0x3ffffe6, 26), (0x7ffffe0, 27), (0x7ffffe1, 27), (0x3ffffe7, 26),
    (0x7ffffe2, 27), (0xfffff2, 24), (0x1fffe4, 21), (0x1fffe5, 21), (0x3ffffe8, 26), (0x3ffffe9, 26),
    (0xffffffd, 28), (0x7ffffe3, 27), (0x7ffffe4, 27), (0x7ffffe5, 27), (0xfffec, 20), (0xfffff3, 24),
    (0xfffed, 20), (0x1fffe6, 21), (0x3fffe9, 22), (0x1fffe7, 21), (0x1fffe8, 21), (0x7ffff3, 23),
    (0x3fffea, 22), (0x3fffeb, 22), (0x1ffffee, 25), (0x1ffffef, 25), (0xfffff4, 24), (0xfffff5, 24),
    (0x3ffffea, 26), (0x7ffff4, 23), (0x3ffffeb, 26), (0x7ffffe6, 27), (0x3ffffec, 26), (0x3ffffed, 26),
    (0x7ffffe7, 27), (0x7ffffe8, 27), (0x7ffffe9, 27), (0x7ffffea, 27), (0x7ffffeb, 27), (0xffffffe, 28),
    (0x7ffffec, 27), (0x7ffffed, 27), (0x7ffffee, 27), (0x7ffffef, 27), (0x7fffff0, 27), (0x3ffffee, 26),
)
_HUFFMAN_DECODE = {(length, code): symbol for symbol, (code, length) in enumerate(HUFFMAN_CODES)}


class HPACKError(ValueError):
    pass


def huffman_decode(data: bytes) -> bytes:
    out = bytearray()
    code = 0
    length = 0
    for byte in data:
        for shift in range(7, -1, -1):
            code = (code << 1) | ((byte >> shift) & 1)
            length += 1
            symbol = _HUFFMAN_DECODE.get((length, code))
            if symbol is not None:
                out.append(symbol)
                code = 0
                length = 0
            elif length > 30:
                raise HPACKError("invalid huffman code")
    # Padding must be < 8 bits of the EOS prefix (all ones).
    if length > 7 or code != (1 << length) - 1:
        raise HPACKError("invalid huffman padding")
    return bytes(out)


def _decode_int(data: bytes, pos: int, prefix_bits: int) -> tuple[int, int]:
    if pos >= len(data):
        raise HPACKError("truncated integer")
    limit = (1 << prefix_bits) - 1
    value = data[pos] & limit
    pos += 1
    if value < limit:
        return value, pos
    shift = 0
    while True:
        if pos >= len(data):
            raise HPACKError("truncated integer continuation")
        byte = data[pos]
        pos += 1
        value += (byte & 0x7F) << shift
        shift += 7
        if not byte & 0x80:
            return value, pos
        if shift > 28:
            raise HPACKError("integer overflow")


def _decode_str(data: bytes, pos: int) -> tuple[str, int]:
    if pos >= len(data):
        raise HPACKError("truncated string")
    huffman = bool(data[pos] & 0x80)
    size, pos = _decode_int(data, pos, 7)
    if pos + size > len(data):
        raise HPACKError("truncated string body")
    raw = data[pos : pos + size]
    pos += size
    if huffman:
        raw = huffman_decode(raw)
    return raw.decode("latin-1"), pos


class Decoder:
    """Stateful HPACK decoder (one per HTTP/2 connection direction)."""

    def __init__(self, max_table_size: int = 4096) -> None:
        self.dynamic: list[tuple[str, str]] = []
        self.max_size = max_table_size

    def _size(self) -> int:
        return sum(len(n) + len(v) + 32 for n, v in self.dynamic)

    def _add(self, name: str, value: str) -> None:
        self.dynamic.insert(0, (name, value))
        while self.dynamic and self._size() > self.max_size:
            self.dynamic.pop()

    def _lookup(self, index: int) -> tuple[str, str]:
        if index <= 0:
            raise HPACKError("index 0")
        if index <= len(STATIC_TABLE):
            return STATIC_TABLE[index - 1]
        dyn = index - len(STATIC_TABLE) - 1
        if dyn >= len(self.dynamic):
            raise HPACKError("dynamic index out of range")
        return self.dynamic[dyn]

    def decode(self, block: bytes) -> list[tuple[str, str]]:
        headers: list[tuple[str, str]] = []
        pos = 0
        while pos < len(block):
            byte = block[pos]
            if byte & 0x80:  # indexed field
                index, pos = _decode_int(block, pos, 7)
                headers.append(self._lookup(index))
            elif byte & 0x40:  # literal with incremental indexing
                index, pos = _decode_int(block, pos, 6)
                name = self._lookup(index)[0] if index else None
                if name is None:
                    name, pos = _decode_str(block, pos)
                value, pos = _decode_str(block, pos)
                self._add(name, value)
                headers.append((name, value))
            elif byte & 0x20:  # dynamic table size update
                size, pos = _decode_int(block, pos, 5)
                self.max_size = size
                while self.dynamic and self._size() > self.max_size:
                    self.dynamic.pop()
            else:  # literal without indexing / never indexed (4-bit prefix)
                index, pos = _decode_int(block, pos, 4)
                name = self._lookup(index)[0] if index else None
                if name is None:
                    name, pos = _decode_str(block, pos)
                value, pos = _decode_str(block, pos)
                headers.append((name, value))
        return headers


def _encode_int(value: int, prefix_bits: int, first: int) -> bytes:
    limit = (1 << prefix_bits) - 1
    if value < limit:
        return bytes([first | value])
    out = bytearray([first | limit])
    value -= limit
    while value >= 0x80:
        out.append((value & 0x7F) | 0x80)
        value >>= 7
    out.append(value)
    return bytes(out)


def encode_response(status: int, headers: list[tuple[str, str]] = ()) -> bytes:  # type: ignore[assignment]
    """Encode :status plus literal-without-indexing headers (no Huffman)."""
    out = bytearray()
    status_index = {200: 8, 204: 9, 206: 10, 304: 11, 400: 12, 404: 13, 500: 14}.get(status)
    if status_index is None:
        raise HPACKError(f"unsupported status {status}")
    out += _encode_int(status_index, 7, 0x80)
    names = {name: i + 1 for i, (name, _) in reversed(list(enumerate(STATIC_TABLE)))}
    for name, value in headers:
        index = names.get(name, 0)
        out += _encode_int(index, 4, 0x00)
        if not index:
            raw_name = name.encode("latin-1")
            out += _encode_int(len(raw_name), 7, 0x00) + raw_name
        raw = value.encode("latin-1")
        out += _encode_int(len(raw), 7, 0x00) + raw
    return bytes(out)
