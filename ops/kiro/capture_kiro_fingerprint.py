#!/usr/bin/env python3
"""Deterministic Kiro (sixth platform) fingerprint capture diff for TokenKey alignment.

Sibling of ops/anthropic/capture_cc_fingerprint.py, but for the AWS Kiro /
CodeWhisperer client. The capture method differs by necessity: Claude Code can be
redirected to a self-hosted TLS collector via ANTHROPIC_BASE_URL, but the real
Kiro IDE hard-codes codewhisperer.us-east-1.amazonaws.com and cannot be
redirected. So the real ClientHello is obtained by **passive pcap** (the
handshake is plaintext) and this engine only parses + diffs — it never fabricates
a JA3.

Subcommands:
  bundle-from-pcap  Build a capture bundle from a tshark TSV (one ClientHello).
                    Computes ja3_raw / ja3_hash and an upstream-shaped TLS profile
                    object. (HTTP-protocol verification lives in
                    probe_runtime_gateway.py; the mitm path was non-viable.)
  diff              Compare --bundle against repo baseline; human report on stdout.
  check             Same as diff but exits 1 when actionable mismatches exist.
  check-tls         Exit 1 when bundle TLS ja3 fields mismatch the committed profile.
  show-baseline     Print the repo baseline (expected UA + committed ja3, if any).
  emit-profile      Write deploy/aws/stage0/tk_canonical_kiro_ide.json from a bundle.

stdlib-only. No network. Pure functions (compute_ja3 / build_canonical_profile /
expected_user_agent) are unit-tested by test_capture_kiro_fingerprint.py.
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

SCHEMA_VERSION = 2
REPO_ROOT = Path(__file__).resolve().parents[2]
KIRO_CONSTANTS_GO = REPO_ROOT / "backend/internal/pkg/kiro/constants.go"
KIRO_TLS_PROFILE_JSON = REPO_ROOT / "deploy/aws/stage0/tk_canonical_kiro_ide.json"
KIRO_PROFILE_NAME = "tk_canonical_kiro_ide"

# RFC 8701 GREASE values. Stripped from ciphers / extensions / curves before the
# JA3 string is built (JA3 spec) and from the stored profile lists (utls re-adds
# GREASE only when enable_grease=true).
GREASE_VALUES = frozenset(
    {
        0x0A0A, 0x1A1A, 0x2A2A, 0x3A3A, 0x4A4A, 0x5A5A, 0x6A6A, 0x7A7A,
        0x8A8A, 0x9A9A, 0xAAAA, 0xBABA, 0xCACA, 0xDADA, 0xEAEA, 0xFAFA,
    }
)

# tshark -T fields column order. The capture shell MUST emit these in this exact
# order with `-E header=y -E separator='\t' -E aggregator=,`.
TSHARK_FIELDS = (
    "tls.handshake.version",
    "tls.handshake.ciphersuite",
    "tls.handshake.extension.type",
    "tls.handshake.extensions_supported_group",
    "tls.handshake.extensions_ec_point_format",
    "tls.handshake.sig_hash_alg",
    "tls.handshake.extensions_alpn_str",
    "tls.handshake.extensions.supported_version",
    "tls.handshake.extensions_key_share_group",
    "tls.extension.psk_ke_mode",
    "tls.handshake.extensions_server_name",
)

# Fields that block merge / signal real drift when mismatched. Kiro capture is
# TLS/JA3-only: HTTP-protocol verification lives in probe_runtime_gateway.py (the
# mitm path was empirically non-viable — Kiro direct-dials, bypassing proxies).
CRITICAL_FIELDS = frozenset({"tls.ja3_hash"})


@dataclass(frozen=True)
class DiffRow:
    field: str
    tokenkey: str
    captured: str
    status: str  # match | mismatch | missing_capture | missing_tokenkey
    critical: bool
    note: str = ""


# --------------------------------------------------------------------------- #
# Repo constant extraction (mirror of the cc engine's _extract_const).
# --------------------------------------------------------------------------- #
def _extract_const(go_src: str, name: str) -> str:
    m = re.search(rf"\b{re.escape(name)}\s*=\s*\"([^\"]*)\"", go_src)
    if not m:
        raise ValueError(f"const {name} not found in kiro/constants.go")
    return m.group(1)


def load_kiro_constants(constants_go: Path = KIRO_CONSTANTS_GO) -> dict[str, str]:
    src = constants_go.read_text(encoding="utf-8")
    return {
        "streaming_sdk_version": _extract_const(src, "StreamingSDKVersion"),
        "runtime_sdk_version": _extract_const(src, "RuntimeSDKVersion"),
        "kiro_ide_version": _extract_const(src, "DefaultKiroIDEVersion"),
        "system_version": _extract_const(src, "DefaultSystemVersion"),
        "node_version": _extract_const(src, "DefaultNodeVersion"),
    }


def expected_user_agent(consts: dict[str, str]) -> str:
    """Rebuild the streaming User-Agent exactly as kiro.BuildUserAgent renders it
    (apiName=codewhispererstreaming, sdkVersion=StreamingSDKVersion, mode=m/E,
    no machineID — the canonical fingerprint without the per-account suffix)."""
    sdk = consts["streaming_sdk_version"]
    return (
        f"aws-sdk-js/{sdk} ua/2.1 os/{consts['system_version']} lang/js "
        f"md/nodejs#{consts['node_version']} api/codewhispererstreaming#{sdk} "
        f"m/E KiroIDE-{consts['kiro_ide_version']}"
    )


def expected_amz_user_agent(consts: dict[str, str]) -> str:
    """Rebuild the x-amz-user-agent exactly as kiro.BuildAmzUserAgent renders it."""
    return f"aws-sdk-js/{consts['streaming_sdk_version']} KiroIDE-{consts['kiro_ide_version']}"


# --------------------------------------------------------------------------- #
# JA3 + profile construction (pure, unit-tested).
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
    """Return (ja3_raw, ja3_md5). GREASE is stripped from ciphers/extensions/curves
    per the JA3 spec; point_formats are emitted as-is (never GREASE)."""

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
    ja3_md5 = hashlib.md5(ja3_raw.encode("ascii")).hexdigest()
    return ja3_raw, ja3_md5


def build_canonical_profile(
    fields: dict[str, Any],
    expected_http: dict[str, Any],
    tls_source: str,
) -> dict[str, Any]:
    """Assemble an upstream-shaped TLS profile (same schema as
    tk_canonical_cc_oauth.json) from parsed ClientHello fields. GREASE is stripped
    from the stored lists; enable_grease records whether GREASE was observed so the
    utls dialer can re-add it faithfully. HTTP fields are rendered from repository
    constants and kept separate so passive pcap is never claimed as HTTP evidence."""
    ciphers = fields.get("ciphers", [])
    extensions = fields.get("extensions", [])
    curves = fields.get("curves", [])
    point_formats = fields.get("point_formats", [])
    version = fields.get("version", 771)

    grease_seen = any(v in GREASE_VALUES for v in ciphers + extensions + curves)
    ja3_raw, ja3_hash = compute_ja3(version, ciphers, extensions, curves, point_formats)

    observed = {
        "ja3_raw": ja3_raw,
        "ja3_hash": ja3_hash,
        "server_name": fields.get("server_name", ""),
        "source": tls_source,
    }
    return {
        "name": KIRO_PROFILE_NAME,
        "description": (
            "TokenKey canonical Kiro IDE (AWS CodeWhisperer) TLS profile. Captured "
            "by passive pcap from a real Kiro IDE ClientHello (the AWS endpoint is "
            "hard-coded and cannot be redirected to a collector). Only observed.* "
            "is pcap evidence. expected_http is a deterministic repository-constant "
            "snapshot, not an HTTP capture; the live wire UA uses the same "
            "kiro.ResolveClientIdentity and builders."
        ),
        "enable_grease": grease_seen,
        "cipher_suites": _strip_grease(ciphers),
        "curves": _strip_grease(curves),
        "point_formats": point_formats,
        "signature_algorithms": fields.get("signature_algorithms", []),
        "alpn_protocols": fields.get("alpn_protocols", []),
        "supported_versions": _strip_grease(fields.get("supported_versions", [])),
        "key_share_groups": _strip_grease(fields.get("key_share_groups", [])),
        "psk_modes": fields.get("psk_modes", []),
        "extensions": _strip_grease(extensions),
        "observed": observed,
        "expected_http": {
            **expected_http,
            "source": "repo-constants",
        },
    }


def validate_profile_provenance(profile: dict[str, Any]) -> str | None:
    """Reject legacy profiles that label repository-derived HTTP fields as pcap
    observations. Historical bundles remain valid for TLS diff/check, but must be
    rebuilt with the current tool before emit-profile can write the baseline."""
    observed = profile.get("observed")
    expected_http = profile.get("expected_http")
    if not isinstance(observed, dict):
        return "observed must be an object"
    if not isinstance(expected_http, dict):
        return "expected_http is missing"

    missing_observed = sorted({"ja3_raw", "ja3_hash", "server_name", "source"} - observed.keys())
    if missing_observed:
        return f"observed is missing fields: {', '.join(missing_observed)}"
    missing_expected = sorted(
        {
            "kiro_ide_version",
            "streaming_sdk_version",
            "node_version",
            "system_version",
            "user_agent",
            "x_amz_user_agent",
            "source",
        }
        - expected_http.keys()
    )
    if missing_expected:
        return f"expected_http is missing fields: {', '.join(missing_expected)}"

    leaked = sorted(
        field
        for field in ("node_version", "system_version", "user_agent", "x_amz_user_agent")
        if field in observed
    )
    if leaked:
        return f"observed contains non-pcap HTTP fields: {', '.join(leaked)}"
    if not str(observed.get("source", "")).startswith("passive-pcap"):
        return "observed.source must identify passive-pcap evidence"
    if expected_http.get("source") != "repo-constants":
        return "expected_http.source must be repo-constants"
    return None


# --------------------------------------------------------------------------- #
# tshark TSV parsing.
# --------------------------------------------------------------------------- #
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


def _parse_str_list(cell: str) -> list[str]:
    return [tok.strip() for tok in (cell or "").split(",") if tok.strip()]


def parse_tshark_tsv(tsv_text: str) -> dict[str, Any]:
    """Parse a tshark `-T fields -E header=y -E separator=\\t -E aggregator=,`
    dump (header + >=1 ClientHello rows). Uses the first data row. Raises if no
    ClientHello row is present."""
    lines = [ln for ln in tsv_text.splitlines() if ln.strip()]
    if len(lines) < 2:
        raise ValueError("tshark TSV has no ClientHello data row (need header + >=1 row)")
    header = lines[0].split("\t")
    row = lines[1].split("\t")
    # Pad row to header width (trailing empty fields are dropped by tshark).
    row += [""] * (len(header) - len(row))
    cell = {header[i]: row[i] for i in range(len(header))}

    return {
        "version": (_parse_int_list(cell.get("tls.handshake.version", "")) or [771])[0],
        "ciphers": _parse_int_list(cell.get("tls.handshake.ciphersuite", "")),
        "extensions": _parse_int_list(cell.get("tls.handshake.extension.type", "")),
        "curves": _parse_int_list(cell.get("tls.handshake.extensions_supported_group", "")),
        "point_formats": _parse_int_list(cell.get("tls.handshake.extensions_ec_point_format", "")),
        "signature_algorithms": _parse_int_list(cell.get("tls.handshake.sig_hash_alg", "")),
        "alpn_protocols": _parse_str_list(cell.get("tls.handshake.extensions_alpn_str", "")),
        "supported_versions": _parse_int_list(cell.get("tls.handshake.extensions.supported_version", "")),
        "key_share_groups": _parse_int_list(cell.get("tls.handshake.extensions_key_share_group", "")),
        "psk_modes": _parse_int_list(cell.get("tls.extension.psk_ke_mode", "")),
        "server_name": (_parse_str_list(cell.get("tls.handshake.extensions_server_name", "")) or [""])[0],
    }


# --------------------------------------------------------------------------- #
# Baseline + diff.
# --------------------------------------------------------------------------- #
def load_committed_profile(path: Path = KIRO_TLS_PROFILE_JSON) -> dict[str, Any] | None:
    if not path.exists():
        return None
    return json.loads(path.read_text(encoding="utf-8"))


def diff_bundle(bundle: dict[str, Any], committed: dict[str, Any] | None) -> list[DiffRow]:
    rows: list[DiffRow] = []
    tls = bundle.get("tls", {})

    cap_ja3 = str(tls.get("ja3_hash", ""))
    if committed is None:
        rows.append(
            DiffRow(
                "tls.ja3_hash",
                "(none committed)",
                cap_ja3,
                "missing_tokenkey",
                critical=False,
                note="first capture — run `emit-profile` to commit tk_canonical_kiro_ide.json",
            )
        )
    else:
        base_ja3 = str(committed.get("observed", {}).get("ja3_hash", ""))
        status = "match" if base_ja3 == cap_ja3 else "mismatch"
        rows.append(DiffRow("tls.ja3_hash", base_ja3, cap_ja3, status, critical=True))

    return rows


def has_actionable_mismatch(rows: list[DiffRow]) -> bool:
    return any(r.status == "mismatch" and r.critical for r in rows)


# --------------------------------------------------------------------------- #
# Rendering.
# --------------------------------------------------------------------------- #
def _render(rows: list[DiffRow]) -> str:
    width = max((len(r.field) for r in rows), default=10)
    out = []
    sym = {"match": "✓", "mismatch": "✗", "missing_capture": "·", "missing_tokenkey": "+"}
    for r in rows:
        line = f"  {sym.get(r.status, '?')} {r.field.ljust(width)}  {r.status}"
        if r.status == "mismatch":
            line += f"\n      repo:     {r.tokenkey}\n      captured: {r.captured}"
        if r.note:
            line += f"\n      note: {r.note}"
        out.append(line)
    return "\n".join(out)


# --------------------------------------------------------------------------- #
# CLI.
# --------------------------------------------------------------------------- #
def cmd_bundle_from_pcap(args: argparse.Namespace) -> int:
    tsv_text = Path(args.tshark_tsv).read_text(encoding="utf-8")
    fields = parse_tshark_tsv(tsv_text)

    consts = load_kiro_constants()
    expected_http: dict[str, Any] = {
        "kiro_ide_version": consts["kiro_ide_version"],
        "streaming_sdk_version": consts["streaming_sdk_version"],
        "node_version": consts["node_version"],
        "system_version": consts["system_version"],
        "user_agent": expected_user_agent(consts),
        "x_amz_user_agent": expected_amz_user_agent(consts),
    }
    profile = build_canonical_profile(fields, expected_http, args.source or "passive-pcap")

    bundle = {
        "schema_version": SCHEMA_VERSION,
        "captured_at": args.captured_at or datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "source": args.source or "passive-pcap",
        "kiro_constants": consts,
        "tls": {
            "ja3_hash": profile["observed"]["ja3_hash"],
            "ja3_raw": profile["observed"]["ja3_raw"],
            "enable_grease": profile["enable_grease"],
            "profile": profile,
        },
    }
    out = Path(args.out)
    out.write_text(json.dumps(bundle, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    print(f"bundle={out}")
    print(f"ja3_hash={bundle['tls']['ja3_hash']}")
    return 0


def _load_bundle(path: str) -> dict[str, Any]:
    return json.loads(Path(path).read_text(encoding="utf-8"))


def cmd_diff(args: argparse.Namespace) -> int:
    bundle = _load_bundle(args.bundle)
    committed = load_committed_profile()
    rows = diff_bundle(bundle, committed)
    print(_render(rows))
    actionable = has_actionable_mismatch(rows)
    if actionable:
        print("\nRESULT: drift detected (actionable mismatch).")
    elif committed is None:
        print("\nRESULT: first capture — no committed profile yet. Run `emit-profile`.")
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
    committed = load_committed_profile()
    if committed is None:
        print("check-tls: no committed tk_canonical_kiro_ide.json — nothing to compare (first capture).")
        return 0
    base = str(committed.get("observed", {}).get("ja3_hash", ""))
    cap = str(bundle.get("tls", {}).get("ja3_hash", ""))
    if base == cap:
        print(f"check-tls: ja3_hash match ({cap})")
        return 0
    print(f"check-tls: ja3_hash MISMATCH\n  committed: {base}\n  captured:  {cap}")
    return 1


def cmd_show_baseline(args: argparse.Namespace) -> int:
    consts = load_kiro_constants()
    committed = load_committed_profile()
    print(json.dumps(
        {
            "constants": consts,
            "expected_user_agent": expected_user_agent(consts),
            "expected_x_amz_user_agent": expected_amz_user_agent(consts),
            "committed_ja3_hash": (committed or {}).get("observed", {}).get("ja3_hash") if committed else None,
            "committed_profile_present": committed is not None,
        },
        indent=2,
        ensure_ascii=False,
    ))
    return 0


def cmd_emit_profile(args: argparse.Namespace) -> int:
    bundle = _load_bundle(args.bundle)
    profile = bundle.get("tls", {}).get("profile")
    if not profile:
        print("emit-profile: bundle has no tls.profile", file=sys.stderr)
        return 1
    provenance_error = validate_profile_provenance(profile)
    if provenance_error:
        print(
            "emit-profile: unsafe legacy provenance: "
            f"{provenance_error}; rebuild the bundle with current bundle-from-pcap",
            file=sys.stderr,
        )
        return 1
    out = Path(args.out or KIRO_TLS_PROFILE_JSON)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(profile, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    print(f"wrote {out}")
    return 0


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(description=__doc__)
    sub = p.add_subparsers(dest="cmd", required=True)

    b = sub.add_parser("bundle-from-pcap", help="assemble TLS/JA3 bundle from tshark TSV")
    b.add_argument("--tshark-tsv", required=True)
    b.add_argument("--out", required=True)
    b.add_argument("--source", default="")
    b.add_argument("--captured-at", default="")
    b.set_defaults(func=cmd_bundle_from_pcap)

    d = sub.add_parser("diff", help="compare bundle to repo baseline")
    d.add_argument("--bundle", required=True)
    d.add_argument("--check", action="store_true")
    d.set_defaults(func=cmd_diff)

    c = sub.add_parser("check", help="diff + exit 1 on actionable mismatch")
    c.add_argument("--bundle", required=True)
    c.set_defaults(func=cmd_check)

    ct = sub.add_parser("check-tls", help="exit 1 when ja3_hash mismatches committed profile")
    ct.add_argument("--bundle", required=True)
    ct.set_defaults(func=cmd_check_tls)

    sb = sub.add_parser("show-baseline", help="print expected UA + committed ja3")
    sb.set_defaults(func=cmd_show_baseline)

    e = sub.add_parser("emit-profile", help="write deploy/aws/stage0/tk_canonical_kiro_ide.json from bundle")
    e.add_argument("--bundle", required=True)
    e.add_argument("--out", default="")
    e.set_defaults(func=cmd_emit_profile)

    return p


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    raise SystemExit(main())
