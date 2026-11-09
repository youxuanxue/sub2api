#!/usr/bin/env python3
"""Redact credentials from probe stdout/stderr on the remote host.

run-probe.sh wraps every probe with this filter so secrets never reach SSM
command history, SSH logs or agent transcripts. A 2026-11-08 inventory probe
printed pgbackrest.conf's ``repo1-s3-token`` through a hand-written filter that
forgot the word ``token``; this owner removes that per-probe responsibility.

Two rule families, both value-shaped so numeric usage fields such as
``input_tokens: 123`` or ``api_key_id: 5`` stay intact:
  * well-known credential formats anywhere in a line (AWS key ids, STS session
    tokens, JWTs, provider API keys, Bearer values, PEM private keys, URL
    passwords);
  * ``name=value`` / ``name: value`` / JSON pairs whose name looks like a
    secret and whose value is a non-numeric string of 8+ characters.

stdlib-only. Exit code of the wrapped command is preserved.
"""
from __future__ import annotations

import re
import subprocess
import sys
import threading

MASK = "[REDACTED]"

_VALUE_PATTERNS = [
    re.compile(r"-----BEGIN [A-Z ]*PRIVATE KEY-----"),
    re.compile(r"\b(?:AKIA|ASIA)[0-9A-Z]{16}\b"),
    # STS / session tokens (IQoJ… current, FwoG… legacy) are long base64.
    re.compile(r"\b(?:IQoJ|FwoG)[A-Za-z0-9+/=]{40,}"),
    re.compile(r"\beyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}"),
    re.compile(r"\bsk-(?:ant-|proj-)?[A-Za-z0-9_-]{20,}"),
    re.compile(r"\bgh[pousr]_[A-Za-z0-9]{30,}"),
    re.compile(r"\bya29\.[A-Za-z0-9._-]{20,}"),
    re.compile(r"\b1//0[A-Za-z0-9_-]{20,}"),
    re.compile(r"\bAIza[0-9A-Za-z_-]{35}\b"),
]
_BEARER = re.compile(r"(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}")
# Scheme and userinfo are length-bounded so long word runs cannot backtrack.
_URL_PASSWORD = re.compile(
    r"((?<![A-Za-z0-9+.-])[a-z][a-z0-9+.-]{0,31}://[^\s:/@]{1,128}:)[^\s@/]{1,256}@", re.IGNORECASE
)

# Anchored at the start of an identifier run and length-bounded so long base64
# or JSON lines stay linear.
_SECRET_NAME = (
    r"(?<![A-Za-z0-9_.-])[A-Za-z0-9_.-]{0,48}?(?:secret|token(?!key)|passw(?:or)?d|credential|api[_-]?key|apikey"
    r"|access[_-]?key|private[_-]?key|session[_-]?key|signing[_-]?key|totp[_-]?key"
    r"|authorization|cookie)[A-Za-z0-9_.-]{0,48}"
)
# Names that describe a secret rather than hold one (snake, kebab or camelCase).
_DESCRIPTOR = (
    r"hash|sha256|fingerprint|id|ids|count|counts|at|type|types|name|names|len|length"
    r"|status|source|present|set|configured|enabled|expires?|expiry|ttl|kind|file|path|dir"
    r"|url|env|ref|mode|prefix|header|field|version"
)
_NON_SECRET_SUFFIX = re.compile(
    r"(?i:[_.-](?:" + _DESCRIPTOR + r"))$|(?<=[a-z0-9])(?:[A-Z](?i:" + _DESCRIPTOR + r")|ID|IDs)$"
    r"|(?i:tokens)$"
)
_PAIR = re.compile(
    r"""(?P<lead>(?P<q>["']?)(?P<name>""" + _SECRET_NAME + r""")(?P=q)\s*(?:=|:)\s*)"""
    r"""(?P<vq>["']?)(?P<value>[^\s"',;}&]+)""",
    re.IGNORECASE,
)
# Numbers, booleans, placeholders, shell expansions and code references.
_NOT_A_SECRET_VALUE = re.compile(
    r"(?i)^(?:-?[0-9][0-9_.,]*|true|false|null|none|nil|\[redacted\]|\*+|[-$<{(\[].*"
    r"|[a-z_][a-z0-9_]*(?:\.[a-z_][a-z0-9_]*)+\(?|map\[.*)$"
)


def _pair(match: re.Match[str]) -> str:
    name, value = match.group("name"), match.group("value")
    if _NON_SECRET_SUFFIX.search(name) or len(value) < 8 or _NOT_A_SECRET_VALUE.match(value):
        return match.group(0)
    return match.group("lead") + match.group("vq") + MASK


def redact_line(line: str) -> tuple[str, int]:
    count = 0
    for pattern in _VALUE_PATTERNS:
        line, n = pattern.subn(MASK, line)
        count += n
    line, n = _BEARER.subn(lambda m: m.group(1) + " " + MASK, line)
    count += n
    line, n = _URL_PASSWORD.subn(lambda m: m.group(1) + MASK + "@", line)
    count += n
    redacted = _PAIR.sub(_pair, line)
    if redacted != line:
        count += redacted.count(MASK) - line.count(MASK)
    return redacted, count


def _pump(source, sink, totals: list[int]) -> None:
    for raw in iter(source.readline, b""):
        text, n = redact_line(raw.decode("utf-8", errors="surrogateescape"))
        totals.append(n)
        sink.write(text.encode("utf-8", errors="surrogateescape"))
        sink.flush()
    source.close()


def main(argv: list[str]) -> int:
    command = argv[1:]
    if command and command[0] == "--":
        command = command[1:]
    if not command:
        print("usage: probe_output_redact.py -- <command...>", file=sys.stderr)
        return 2
    proc = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    totals: list[int] = []
    pumps = [
        threading.Thread(target=_pump, args=(proc.stdout, sys.stdout.buffer, totals)),
        threading.Thread(target=_pump, args=(proc.stderr, sys.stderr.buffer, totals)),
    ]
    for pump in pumps:
        pump.start()
    for pump in pumps:
        pump.join()
    code = proc.wait()
    if sum(totals):
        print(f"[probe-redact] redacted {sum(totals)} credential value(s)", file=sys.stderr)
    return code if code >= 0 else 128 - code


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
