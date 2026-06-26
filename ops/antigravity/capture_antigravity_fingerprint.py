#!/usr/bin/env python3
"""Deterministic Antigravity (Google cloudcode-pa) fingerprint capture diff for
TokenKey alignment.

Sibling of ops/anthropic/capture_cc_fingerprint.py and ops/kiro/
capture_kiro_fingerprint.py, but **inverted relative to kiro**: for Antigravity the
load-bearing fingerprint is the HTTP layer (the impersonated client User-Agent
*version* in `antigravity/hub/<ver> windows/amd64`, the body `userAgent` literal,
and the loadCodeAssist/onboardUser ideType metadata), NOT the TLS JA3. Note the
privacy endpoints (setUserSettings/fetchUserInfo) deliberately send NO
`X-Goog-Api-Client: gl-node/<ver>` header — #756 + the 2026-06-13 real-IDE capture
confirmed the IDE does not send it, so its ABSENCE is the aligned state, and a
captured gl-node is treated as drift. TokenKey and the real Antigravity IDE both
speak from native Go/Node TLS
stacks, so their ClientHello is same-origin and JA3 carries no signal — TLS is
captured (optional) for completeness only and never gates.

Capture method also differs from both siblings: the Antigravity endpoint
`cloudcode-pa.googleapis.com` is hard-coded (cannot be redirected like cc's
ANTHROPIC_BASE_URL), so the on-wire HTTP is obtained by **mitmproxy** — the real
IDE must be configured to egress through the proxy and trust its CA. This engine
only parses + diffs; it never fabricates values.

The align target is read live from the Go constants (single source of truth — no
committed baseline JSON):
  - backend/internal/pkg/antigravity/oauth.go             (UA version/format, ClientID, scopes, redirect)
  - backend/internal/pkg/antigravity/client.go            (ideType/ideName/platform/pluginType; X-Goog-Api-Client expected ABSENT post-#756)
  - backend/internal/pkg/antigravity/request_transformer.go (body userAgent literal)

Subcommands:
  bundle-from-artifacts  Build a capture bundle from a mitm HTTP log (+ optional
                         tshark TSV for the non-gating TLS dimension).
  diff                   Compare --bundle against the Go-constant baseline.
  check                  Same as diff but exits 1 on actionable (HTTP) mismatch.
  check-tls              Print captured ja3 (informational; always exits 0 —
                         antigravity JA3 is non-load-bearing).
  show-baseline          Print the repo baseline rebuilt from the Go constants.

stdlib-only. No network. Pure functions are unit-tested by
test_capture_antigravity_fingerprint.py.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

SCHEMA_VERSION = 1
REPO_ROOT = Path(__file__).resolve().parents[2]
AG_DIR = REPO_ROOT / "backend/internal/pkg/antigravity"
OAUTH_GO = AG_DIR / "oauth.go"
CLIENT_GO = AG_DIR / "client.go"
REQUEST_TRANSFORMER_GO = AG_DIR / "request_transformer.go"

# RFC 8701 GREASE values (stripped before the JA3 string is built). Only used for
# the optional, non-gating TLS dimension.
GREASE_VALUES = frozenset(
    {
        0x0A0A, 0x1A1A, 0x2A2A, 0x3A3A, 0x4A4A, 0x5A5A, 0x6A6A, 0x7A7A,
        0x8A8A, 0x9A9A, 0xAAAA, 0xBABA, 0xCACA, 0xDADA, 0xEAEA, 0xFAFA,
    }
)

# tshark -T fields column order for the optional TLS pcap path. The capture shell
# MUST emit these in this exact order.
TSHARK_FIELDS = (
    "tls.handshake.version",
    "tls.handshake.ciphersuite",
    "tls.handshake.extension.type",
    "tls.handshake.extensions_supported_group",
    "tls.handshake.extensions_ec_point_format",
    "tls.handshake.extensions_server_name",
)


@dataclass(frozen=True)
class DiffRow:
    field: str
    tokenkey: str
    captured: str
    status: str  # match | mismatch | missing_capture | info
    critical: bool
    note: str = ""


# --------------------------------------------------------------------------- #
# Repo constant extraction (mirror of the cc / kiro engines' _extract_const).
# --------------------------------------------------------------------------- #
def _extract_const(go_src: str, name: str, where: str) -> str:
    m = re.search(rf"\b{re.escape(name)}\s*=\s*\"([^\"]*)\"", go_src)
    if not m:
        raise ValueError(f"const {name} not found in {where}")
    return m.group(1)


def _extract_field_assign(go_src: str, field: str, where: str) -> str:
    """Extract the literal from a Go field assignment like `.IDEType = "ANTIGRAVITY"`
    (first match)."""
    m = re.search(rf"\.{re.escape(field)}\s*=\s*\"([^\"]*)\"", go_src)
    if not m:
        raise ValueError(f"assignment .{field} = \"...\" not found in {where}")
    return m.group(1)


def _extract_struct_field(go_src: str, field: str, where: str) -> str:
    """Extract the literal from a Go struct-literal field like `UserAgent: "antigravity"`
    (first match)."""
    m = re.search(rf"\b{re.escape(field)}:\s*\"([^\"]*)\"", go_src)
    if not m:
        raise ValueError(f"struct field {field}: \"...\" not found in {where}")
    return m.group(1)


def _extract_ua_format(oauth_src: str) -> str:
    """Pull the `antigravity/hub/%s windows/amd64` format string out of BuildUserAgent.
    The `hub/` subclient_type segment was added in #756 to match the real IDE 2.0.11
    on-wire UA; the pattern allows any literal between `antigravity/` and the `%s`
    version placeholder so a future subclient rename does not silently fail to parse."""
    m = re.search(r"fmt\.Sprintf\(\"(antigravity/[^\"]*%s[^\"]*)\"", oauth_src)
    if not m:
        raise ValueError("BuildUserAgent fmt.Sprintf(\"antigravity/...%s... windows/amd64\") not found in oauth.go")
    return m.group(1)


def _extract_scopes(oauth_src: str) -> list[str]:
    """Extract every google auth scope listed in the Scopes const concatenation."""
    block = re.search(r"\bScopes\s*=\s*(.+?)\n\s*\n", oauth_src, re.DOTALL)
    region = block.group(1) if block else oauth_src
    return re.findall(r"https://www\.googleapis\.com/auth/[A-Za-z0-9._-]+", region)


def _extract_const_header(go_src: str, header: str) -> str:
    """Extract a Header.Set("<header>", "<val>") literal from client.go, or "" when
    the header is no longer set. #756 removed X-Goog-Api-Client(gl-node) from the
    privacy endpoints, so its ABSENCE is the aligned state — not a parse error."""
    m = re.search(rf"\"{re.escape(header)}\",\s*\"([^\"]*)\"", go_src)
    return m.group(1) if m else ""


def load_antigravity_baseline() -> dict[str, Any]:
    oauth = OAUTH_GO.read_text(encoding="utf-8")
    client = CLIENT_GO.read_text(encoding="utf-8")
    transformer = REQUEST_TRANSFORMER_GO.read_text(encoding="utf-8")
    return {
        "ua_version": _extract_const(oauth, "DefaultUserAgentVersion", "oauth.go"),
        "ua_format": _extract_ua_format(oauth),
        "client_id": _extract_const(oauth, "ClientID", "oauth.go"),
        "redirect_uri": _extract_const(oauth, "RedirectURI", "oauth.go"),
        "scopes": _extract_scopes(oauth),
        "body_user_agent": _extract_struct_field(transformer, "UserAgent", "request_transformer.go"),
        "ide_type": _extract_field_assign(client, "IDEType", "client.go"),
        "ide_name": _extract_field_assign(client, "IDEName", "client.go"),
        "platform": _extract_field_assign(client, "Platform", "client.go"),
        "plugin_type": _extract_field_assign(client, "PluginType", "client.go"),
        "x_goog_api_client": _extract_const_header(client, "X-Goog-Api-Client"),
    }


def expected_user_agent(baseline: dict[str, Any]) -> str:
    """Render the HTTP User-Agent exactly as antigravity.BuildUserAgent does for the
    default version (e.g. `antigravity/hub/2.0.11 windows/amd64`)."""
    return baseline["ua_format"].replace("%s", baseline["ua_version"], 1)


# --------------------------------------------------------------------------- #
# Captured-UA parsing.
# --------------------------------------------------------------------------- #
_UA_RE = re.compile(r"antigravity/(?:hub/)?(\d+\.\d+\.\d+)\s*(\S+)?")


def parse_ua(ua: str) -> tuple[str, str]:
    """Return (version, os_arch) parsed from an `antigravity/hub/<ver> <os>/<arch>` UA
    (the `hub/` subclient segment is optional for backward compatibility).
    Empty strings when not parseable."""
    m = _UA_RE.search(ua or "")
    if not m:
        return "", ""
    return m.group(1), (m.group(2) or "")


# --------------------------------------------------------------------------- #
# JA3 (optional, non-gating).
# --------------------------------------------------------------------------- #
def _strip_grease(values: list[int]) -> list[int]:
    return [v for v in values if v not in GREASE_VALUES]


def compute_ja3(
    version: int,
    ciphers: list[int],
    extensions: list[int],
    curves: list[int],
    point_formats: list[int],
) -> tuple[str, str]:
    def join(vals: list[int]) -> str:
        return "-".join(str(v) for v in vals)

    ja3_raw = ",".join(
        [
            str(version),
            join(_strip_grease(ciphers)),
            join(_strip_grease(extensions)),
            join(_strip_grease(curves)),
            join(point_formats),
        ]
    )
    return ja3_raw, hashlib.md5(ja3_raw.encode("ascii")).hexdigest()


def _parse_int_token(tok: str) -> int | None:
    tok = tok.strip()
    if not tok:
        return None
    try:
        return int(tok, 16) if tok.lower().startswith("0x") else int(tok)
    except ValueError:
        return None


def _parse_int_list(cell: str) -> list[int]:
    out: list[int] = []
    for tok in (cell or "").split(","):
        v = _parse_int_token(tok)
        if v is not None:
            out.append(v)
    return out


def parse_tshark_tsv(tsv_text: str) -> dict[str, Any] | None:
    """Parse a tshark TSV (header + >=1 ClientHello rows); use the first data row.
    Returns None when no data row is present (TLS is optional, never fatal)."""
    lines = [ln for ln in tsv_text.splitlines() if ln.strip()]
    if len(lines) < 2:
        return None
    header = lines[0].split("\t")
    row = lines[1].split("\t")
    row += [""] * (len(header) - len(row))
    cell = {header[i]: row[i] for i in range(len(header))}
    return {
        "version": (_parse_int_list(cell.get("tls.handshake.version", "")) or [771])[0],
        "ciphers": _parse_int_list(cell.get("tls.handshake.ciphersuite", "")),
        "extensions": _parse_int_list(cell.get("tls.handshake.extension.type", "")),
        "curves": _parse_int_list(cell.get("tls.handshake.extensions_supported_group", "")),
        "point_formats": _parse_int_list(cell.get("tls.handshake.extensions_ec_point_format", "")),
        "server_name": ((cell.get("tls.handshake.extensions_server_name", "") or "").split(",") or [""])[0].strip(),
    }


# --------------------------------------------------------------------------- #
# HTTP header log (mitm) parsing — primary signal.
# --------------------------------------------------------------------------- #
# Per-field "last non-empty wins" merge across all log lines, because the load-
# bearing values are spread across endpoints: streamGenerateContent carries the
# UA header + body userAgent + project/model; loadCodeAssist carries ideType/
# ideName; onboardUser carries platform/pluginType. The privacy endpoints are
# watched for X-Goog-Api-Client only to detect a gl-node regression (#756 removed
# it; it should stay absent). A single chat session typically hits several of these.
_MERGE_FIELDS = (
    "user_agent",
    "body_user_agent",
    "ide_type",
    "ide_name",
    "platform",
    "plugin_type",
    "x_goog_api_client",
    "client_metadata",
    "project",
    "model",
    "request_id",
)


def parse_http_log(path: Path) -> dict[str, Any]:
    merged: dict[str, Any] = {}
    seen_paths: list[str] = []
    for line in path.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if not line.startswith("{"):
            continue
        try:
            rec = json.loads(line)
        except json.JSONDecodeError:
            continue
        if rec.get("path"):
            seen_paths.append(str(rec["path"]))
        for f in _MERGE_FIELDS:
            v = rec.get(f)
            if v:
                merged[f] = v
    merged["seen_paths"] = seen_paths
    return merged


# --------------------------------------------------------------------------- #
# Diff.
# --------------------------------------------------------------------------- #
def diff_bundle(bundle: dict[str, Any], baseline: dict[str, Any]) -> list[DiffRow]:
    rows: list[DiffRow] = []
    http = bundle.get("http", {})
    tls = bundle.get("tls", {})

    # 1. HTTP UA *version* — the primary, load-bearing drift axis.
    cap_ua = str(http.get("user_agent", "")).strip()
    cap_ver, cap_os_arch = parse_ua(cap_ua)
    base_ver = baseline["ua_version"]
    if not cap_ver:
        rows.append(DiffRow("http.ua_version", base_ver, "(no http capture)", "missing_capture", critical=False))
    else:
        rows.append(DiffRow(
            "http.ua_version", base_ver, cap_ver,
            "match" if cap_ver == base_ver else "mismatch", critical=True,
        ))
        # os/arch is informational only: TokenKey deliberately pins windows/amd64
        # regardless of the host OS, so a darwin/arm64 capture on a Mac is expected
        # and is NOT drift.
        _, base_os_arch = parse_ua(expected_user_agent(baseline))
        if cap_os_arch and cap_os_arch != base_os_arch:
            rows.append(DiffRow(
                "http.ua_os_arch", base_os_arch, cap_os_arch, "info", critical=False,
                note="TokenKey pins windows/amd64 by design; captured host OS differs — not drift.",
            ))

    # 2. body userAgent literal.
    _http_row(rows, "http.body_user_agent", baseline["body_user_agent"], http.get("body_user_agent"))
    # 3. loadCodeAssist / onboardUser metadata.
    _http_row(rows, "http.ide_type", baseline["ide_type"], http.get("ide_type"))
    _http_row(rows, "http.ide_name", baseline["ide_name"], http.get("ide_name"))
    _http_row(rows, "http.platform", baseline["platform"], http.get("platform"))
    _http_row(rows, "http.plugin_type", baseline["plugin_type"], http.get("plugin_type"))
    # 4. privacy-endpoint X-Goog-Api-Client (gl-node). #756 + the 2026-06-13 real-IDE
    #    capture confirmed the privacy endpoints send NO such header; TokenKey removed
    #    it to align, so the aligned state is ABSENCE on both sides. A non-empty
    #    capture means the real IDE sends gl-node again (we'd need to re-add it) —
    #    surface as actionable drift.
    base_xgoog = str(baseline.get("x_goog_api_client", "")).strip()
    cap_xgoog = str(http.get("x_goog_api_client", "")).strip()
    captured_any = bool(str(http.get("user_agent", "")).strip())
    if cap_xgoog:
        rows.append(DiffRow(
            "http.x_goog_api_client", base_xgoog or "(not sent)", cap_xgoog,
            "match" if cap_xgoog == base_xgoog else "mismatch", critical=True,
            note="" if cap_xgoog == base_xgoog else "real IDE sent gl-node; TokenKey removed it in #756 — re-evaluate.",
        ))
    elif captured_any:
        rows.append(DiffRow(
            "http.x_goog_api_client", base_xgoog or "(not sent)", "(absent — aligned)",
            "match" if base_xgoog == "" else "mismatch", critical=True,
        ))
    else:
        rows.append(DiffRow(
            "http.x_goog_api_client", base_xgoog or "(not sent)", "(no http capture)",
            "missing_capture", critical=False,
        ))

    # 5. Serving-path Client-Metadata header presence — TokenKey does NOT send this
    #    on streamGenerateContent today; if the real IDE does, surface it as a
    #    non-gating finding (a potential header-alignment follow-up, separate PR).
    cap_cm = str(http.get("client_metadata", "")).strip()
    if cap_cm:
        rows.append(DiffRow(
            "http.client_metadata", "(not sent by TokenKey)", cap_cm, "info", critical=False,
            note="real IDE sends Client-Metadata; TokenKey serving path omits it — possible header-alignment follow-up.",
        ))

    # 6. TLS JA3 — informational only (non-load-bearing for antigravity).
    cap_ja3 = str(tls.get("ja3_hash", "")).strip()
    if cap_ja3:
        rows.append(DiffRow(
            "tls.ja3_hash", "(not gated)", cap_ja3, "info", critical=False,
            note="antigravity JA3 is non-load-bearing (Go/Node native, same-origin); recorded only.",
        ))

    return rows


def _http_row(rows: list[DiffRow], field: str, base: str, captured: Any) -> None:
    cap = str(captured or "").strip()
    if not cap:
        rows.append(DiffRow(field, base, "(no http capture)", "missing_capture", critical=False))
    else:
        rows.append(DiffRow(field, base, cap, "match" if cap == base else "mismatch", critical=True))


def has_actionable_mismatch(rows: list[DiffRow]) -> bool:
    return any(r.status == "mismatch" and r.critical for r in rows)


# --------------------------------------------------------------------------- #
# Rendering.
# --------------------------------------------------------------------------- #
def _render(rows: list[DiffRow]) -> str:
    width = max((len(r.field) for r in rows), default=10)
    out = []
    sym = {"match": "✓", "mismatch": "✗", "missing_capture": "·", "info": "i"}
    for r in rows:
        line = f"  {sym.get(r.status, '?')} {r.field.ljust(width)}  {r.status}"
        if r.status in ("mismatch", "info"):
            line += f"\n      repo:     {r.tokenkey}\n      captured: {r.captured}"
        if r.note:
            line += f"\n      note: {r.note}"
        out.append(line)
    return "\n".join(out)


# --------------------------------------------------------------------------- #
# CLI.
# --------------------------------------------------------------------------- #
def cmd_bundle_from_artifacts(args: argparse.Namespace) -> int:
    http: dict[str, Any] = {}
    if args.http_log and Path(args.http_log).exists():
        http = parse_http_log(Path(args.http_log))

    tls: dict[str, Any] = {}
    if args.tshark_tsv and Path(args.tshark_tsv).exists():
        fields = parse_tshark_tsv(Path(args.tshark_tsv).read_text(encoding="utf-8"))
        if fields:
            ja3_raw, ja3_hash = compute_ja3(
                fields["version"], fields["ciphers"], fields["extensions"],
                fields["curves"], fields["point_formats"],
            )
            tls = {"ja3_hash": ja3_hash, "ja3_raw": ja3_raw, "server_name": fields.get("server_name", "")}

    bundle = {
        "schema_version": SCHEMA_VERSION,
        "captured_at": args.captured_at or datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "source": args.source or "mitmproxy",
        "antigravity_baseline": load_antigravity_baseline(),
        "http": http,
        "tls": tls,
    }
    out = Path(args.out)
    out.write_text(json.dumps(bundle, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    print(f"bundle={out}")
    if http:
        print(f"captured_ua={http.get('user_agent', '(none)')}")
    else:
        print("captured_ua=(no http capture — confirm the IDE egresses through the mitm proxy)")
    return 0


def _load_bundle(path: str) -> dict[str, Any]:
    return json.loads(Path(path).read_text(encoding="utf-8"))


def cmd_diff(args: argparse.Namespace) -> int:
    bundle = _load_bundle(args.bundle)
    baseline = load_antigravity_baseline()
    rows = diff_bundle(bundle, baseline)
    print(_render(rows))
    actionable = has_actionable_mismatch(rows)
    captured_any = any(r.status in ("match", "mismatch") for r in rows)
    if actionable:
        print("\nRESULT: drift detected (actionable HTTP mismatch).")
    elif not captured_any:
        print("\nRESULT: no HTTP capture — confirm the IDE egresses through the mitm proxy + trusts its CA.")
    else:
        print("\nRESULT: aligned.")
    if args.check and actionable:
        return 1
    return 0


def cmd_check(args: argparse.Namespace) -> int:
    args.check = True
    return cmd_diff(args)


def cmd_check_tls(args: argparse.Namespace) -> int:
    bundle = _load_bundle(args.bundle)
    cap = str(bundle.get("tls", {}).get("ja3_hash", "")).strip()
    if cap:
        print(f"check-tls: captured ja3_hash={cap} (informational — antigravity JA3 is non-load-bearing, not gated)")
    else:
        print("check-tls: no TLS capture in bundle (optional dimension).")
    return 0


def cmd_show_baseline(args: argparse.Namespace) -> int:
    baseline = load_antigravity_baseline()
    print(json.dumps(
        {
            "baseline": baseline,
            "expected_user_agent": expected_user_agent(baseline),
        },
        indent=2,
        ensure_ascii=False,
    ))
    return 0


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(description=__doc__)
    sub = p.add_subparsers(dest="cmd", required=True)

    b = sub.add_parser("bundle-from-artifacts", help="assemble bundle from mitm http log (+ optional tshark TSV)")
    b.add_argument("--http-log", default="")
    b.add_argument("--tshark-tsv", default="")
    b.add_argument("--out", required=True)
    b.add_argument("--source", default="")
    b.add_argument("--captured-at", default="")
    b.set_defaults(func=cmd_bundle_from_artifacts)

    d = sub.add_parser("diff", help="compare bundle to the Go-constant baseline")
    d.add_argument("--bundle", required=True)
    d.add_argument("--check", action="store_true")
    d.set_defaults(func=cmd_diff)

    c = sub.add_parser("check", help="diff + exit 1 on actionable HTTP mismatch")
    c.add_argument("--bundle", required=True)
    c.set_defaults(func=cmd_check)

    ct = sub.add_parser("check-tls", help="print captured ja3 (informational; always exits 0)")
    ct.add_argument("--bundle", required=True)
    ct.set_defaults(func=cmd_check_tls)

    sb = sub.add_parser("show-baseline", help="print the baseline rebuilt from the Go constants")
    sb.set_defaults(func=cmd_show_baseline)

    return p


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    raise SystemExit(main())
