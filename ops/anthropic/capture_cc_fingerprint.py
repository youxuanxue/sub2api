#!/usr/bin/env python3
"""Deterministic Claude Code fingerprint capture diff for TokenKey alignment.

Reads a capture bundle (TLS collector + optional HTTP mitm log) and compares it
against live repo constants (constants.go, identity_service*, tk_canonical JSON).

Subcommands:
  diff    Compare --bundle to TokenKey baseline; human report on stdout.
  check   Same as diff but exits 1 when actionable mismatches exist.
  bundle-from-artifacts  Build bundle JSON from TLS capture + HTTP log files.

stdlib-only except when invoked as __main__ with no network.
"""
from __future__ import annotations

import argparse
import json
import re
import sys
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

SCHEMA_VERSION = 1
REPO_ROOT = Path(__file__).resolve().parents[2]
_BETA_CONST_MAP: dict[str, str] | None = None
CONSTANTS_GO = REPO_ROOT / "backend/internal/pkg/claude/constants.go"
IDENTITY_CANONICAL_GO = REPO_ROOT / "backend/internal/service/identity_service_tk_canonical_http.go"
IDENTITY_GO = REPO_ROOT / "backend/internal/service/identity_service.go"
TLS_PROFILE_JSON = REPO_ROOT / "deploy/aws/stage0/tk_canonical_cc_oauth.json"

# Fields that block merge / require code fix when mismatched.
CRITICAL_HTTP_FIELDS = frozenset(
    {
        "canonical.user_agent_version",
        "mimic.cli_version",
        "mimic.stainless_package_version",
        "canonical.stainless_package_version",
        "betas.sonnet_mimicry",
        "betas.haiku_mimicry",
    }
)


@dataclass(frozen=True)
class DiffRow:
    field: str
    tokenkey: str
    captured: str
    status: str  # match | mismatch | missing_capture | missing_tokenkey
    critical: bool
    note: str = ""


def _read_text(path: Path) -> str:
    return path.read_text(encoding="utf-8")


def _extract_const(go_src: str, name: str) -> str:
    m = re.search(rf"\b{re.escape(name)}\s*=\s*\"([^\"]*)\"", go_src)
    if not m:
        raise ValueError(f"const {name} not found")
    return m.group(1)


def _extract_map_string(go_src: str, key: str) -> str:
    m = re.search(
        rf"\"{re.escape(key)}\"\s*:\s*\"([^\"]*)\"",
        go_src,
    )
    if not m:
        raise ValueError(f"DefaultHeaders key {key!r} not found")
    return m.group(1)


def _extract_go_string_var(go_src: str, field: str) -> str:
    m = re.search(rf"{re.escape(field)}:\s*\"([^\"]*)\"", go_src)
    if not m:
        raise ValueError(f"field {field} not found")
    return m.group(1)


def _extract_beta_slice(go_src: str, func_name: str) -> list[str]:
    marker = f"func {func_name}() []string {{"
    start = go_src.find(marker)
    if start < 0:
        raise ValueError(f"function {func_name} not found")
    start += len(marker)
    end = go_src.find("}", start)
    body = go_src[start:end]
    return re.findall(r"Beta[A-Za-z0-9]+", body)


def _beta_token_map(constants_src: str) -> dict[str, str]:
    global _BETA_CONST_MAP
    if _BETA_CONST_MAP is not None:
        return _BETA_CONST_MAP
    mapping: dict[str, str] = {}
    for m in re.finditer(r"(Beta[A-Za-z0-9]+)\s*=\s*\"([^\"]+)\"", constants_src):
        mapping[m.group(1)] = m.group(2)
    _BETA_CONST_MAP = mapping
    return mapping


def _resolve_betas(constants_src: str, func_name: str) -> list[str]:
    names = _extract_beta_slice(constants_src, func_name)
    mapping = _beta_token_map(constants_src)
    return [mapping[n] for n in names]


def _ua_version(ua: str) -> str:
    m = re.search(r"claude-cli/(\d+\.\d+\.\d+)", ua or "")
    return m.group(1) if m else ""


def load_tokenkey_baseline(repo_root: Path | None = None) -> dict[str, Any]:
    root = repo_root or REPO_ROOT
    constants_src = _read_text(root / CONSTANTS_GO.relative_to(REPO_ROOT))
    canonical_src = _read_text(root / IDENTITY_CANONICAL_GO.relative_to(REPO_ROOT))
    identity_src = _read_text(root / IDENTITY_GO.relative_to(REPO_ROOT))
    tls_profile = json.loads(_read_text(root / TLS_PROFILE_JSON.relative_to(REPO_ROOT)))

    mimic_ua = _extract_map_string(constants_src, "User-Agent")
    observed = tls_profile.get("observed") or {}

    return {
        "tls": {
            "profile_name": tls_profile.get("name", ""),
            "ja3_hash": observed.get("ja3_hash", ""),
            "ja3_raw": observed.get("ja3_raw", ""),
        },
        "canonical_http": {
            "default_version": _extract_const(canonical_src, "DefaultClaudeCodeUserAgentVersion"),
            "user_agent_shape": "claude-cli/<version> (external, sdk-cli)",
            "stainless_package_version": _extract_go_string_var(
                canonical_src, "StainlessPackageVersion"
            ),
            "stainless_os": _extract_go_string_var(canonical_src, "StainlessOS"),
            "stainless_arch": _extract_go_string_var(canonical_src, "StainlessArch"),
            "stainless_runtime_version": _extract_go_string_var(
                canonical_src, "StainlessRuntimeVersion"
            ),
        },
        "mimic_http": {
            "cli_version": _extract_const(constants_src, "CLICurrentVersion"),
            "user_agent": mimic_ua,
            "stainless_package_version": _extract_map_string(
                constants_src, "X-Stainless-Package-Version"
            ),
            "stainless_os": _extract_map_string(constants_src, "X-Stainless-OS"),
        },
        "mimic_default_fingerprint": {
            "user_agent": _extract_go_string_var(identity_src, "UserAgent"),
            "stainless_package_version": _extract_go_string_var(
                identity_src, "StainlessPackageVersion"
            ),
        },
        "betas": {
            "sonnet_mimicry": _resolve_betas(constants_src, "FullClaudeCodeMimicryBetas"),
            "haiku_mimicry": _resolve_betas(constants_src, "FullClaudeCodeHaikuMimicryBetas"),
        },
    }


def _parse_http_log_line(line: str) -> dict[str, Any] | None:
    line = line.strip()
    if not line:
        return None
    if line.startswith("CC_CAPTURE "):
        line = line[len("CC_CAPTURE ") :]
    try:
        return json.loads(line)
    except json.JSONDecodeError:
        return None


def _http_variant(model: str) -> str | None:
    model_l = (model or "").lower()
    if "haiku" in model_l:
        return "haiku"
    if model_l:
        return "sonnet"
    return None


def _pick_http_by_model(records: list[dict[str, Any]]) -> dict[str, dict[str, Any]]:
    out: dict[str, dict[str, Any]] = {}
    for rec in records:
        variant = _http_variant(str(rec.get("model") or ""))
        if variant and variant not in out:
            out[variant] = rec
    return out


def load_http_log(path: Path) -> dict[str, dict[str, Any]]:
    records: list[dict[str, Any]] = []
    for line in path.read_text(encoding="utf-8").splitlines():
        rec = _parse_http_log_line(line)
        if rec:
            records.append(rec)
    return _pick_http_by_model(records)


def bundle_from_artifacts(
    *,
    cc_version: str,
    tls_observed: dict[str, Any],
    http_by_variant: dict[str, dict[str, Any]] | None = None,
    collector_url: str = "",
) -> dict[str, Any]:
    return {
        "schema_version": SCHEMA_VERSION,
        "captured_at": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
        "cc_version": cc_version,
        "collector": collector_url,
        "tls": {
            "ja3_hash": tls_observed.get("ja3_hash", ""),
            "ja3_raw": tls_observed.get("ja3_raw", ""),
            "user_agent": tls_observed.get("user_agent", ""),
            "stainless_package_version": tls_observed.get(
                "stainless_package_version", ""
            ),
        },
        "http": http_by_variant or {},
    }


def load_capture_bundle(path: Path) -> dict[str, Any]:
    data = json.loads(path.read_text(encoding="utf-8"))
    if data.get("schema_version") != SCHEMA_VERSION:
        raise ValueError(f"unsupported bundle schema_version: {data.get('schema_version')}")
    return data


def _beta_list(header: str) -> list[str]:
    return [p.strip() for p in (header or "").split(",") if p.strip()]


def diff_baseline_vs_capture(
    baseline: dict[str, Any], capture: dict[str, Any]
) -> list[DiffRow]:
    rows: list[DiffRow] = []

    cap_tls = capture.get("tls") or {}
    base_tls = baseline.get("tls") or {}
    for key in ("ja3_hash", "ja3_raw"):
        tk = str(base_tls.get(key) or "")
        cap = str(cap_tls.get(key) or "")
        if not cap:
            rows.append(
                DiffRow(
                    f"tls.{key}",
                    tk,
                    cap,
                    "missing_capture",
                    critical=False,
                    note="Run TLS collector capture",
                )
            )
        elif tk == cap:
            rows.append(DiffRow(f"tls.{key}", tk, cap, "match", critical=False))
        else:
            rows.append(
                DiffRow(
                    f"tls.{key}",
                    tk,
                    cap,
                    "mismatch",
                    critical=True,
                    note="Update tk_canonical_cc_oauth + manage-anthropic-config apply",
                )
            )

    cap_cc = capture.get("cc_version") or _ua_version(cap_tls.get("user_agent", ""))
    canon_ver = baseline["canonical_http"]["default_version"]
    mimic_ver = baseline["mimic_http"]["cli_version"]
    rows.append(
        DiffRow(
            "canonical.user_agent_version",
            canon_ver,
            cap_cc,
            "match" if canon_ver == cap_cc else "mismatch",
            critical="canonical.user_agent_version" in CRITICAL_HTTP_FIELDS,
        )
    )
    rows.append(
        DiffRow(
            "mimic.cli_version",
            mimic_ver,
            cap_cc,
            "match" if mimic_ver == cap_cc else "mismatch",
            critical="mimic.cli_version" in CRITICAL_HTTP_FIELDS,
        )
    )

    cap_stainless = str(
        cap_tls.get("stainless_package_version")
        or (capture.get("http") or {}).get("haiku", {}).get("x_stainless", {}).get(
            "X-Stainless-Package-Version", ""
        )
        or ""
    )
    canon_stainless = baseline["canonical_http"]["stainless_package_version"]
    mimic_stainless = baseline["mimic_http"]["stainless_package_version"]
    rows.append(
        DiffRow(
            "canonical.stainless_package_version",
            canon_stainless,
            cap_stainless,
            "match" if canon_stainless == cap_stainless else "mismatch",
            critical="canonical.stainless_package_version" in CRITICAL_HTTP_FIELDS,
        )
    )
    rows.append(
        DiffRow(
            "mimic.stainless_package_version",
            mimic_stainless,
            cap_stainless,
            "match" if mimic_stainless == cap_stainless else "mismatch",
            critical="mimic.stainless_package_version" in CRITICAL_HTTP_FIELDS,
        )
    )

    http = capture.get("http") or {}
    for variant, beta_key in (("haiku", "haiku_mimicry"), ("sonnet", "sonnet_mimicry")):
        rec = http.get(variant)
        if not rec:
            rows.append(
                DiffRow(
                    f"betas.{beta_key}",
                    ",".join(baseline["betas"][beta_key]),
                    "",
                    "missing_capture",
                    critical=f"betas.{beta_key}" in CRITICAL_HTTP_FIELDS,
                    note=f"Run HTTP mitm capture with --http for {variant}",
                )
            )
            continue
        tk_betas = baseline["betas"][beta_key]
        cap_betas = _beta_list(rec.get("anthropic_beta", ""))
        status = "match" if tk_betas == cap_betas else "mismatch"
        rows.append(
            DiffRow(
                f"betas.{beta_key}",
                ",".join(tk_betas),
                ",".join(cap_betas),
                status,
                critical=f"betas.{beta_key}" in CRITICAL_HTTP_FIELDS,
            )
        )

    return rows


def format_diff_report(rows: list[DiffRow], *, capture_path: str = "") -> str:
    lines = ["TokenKey cc fingerprint diff"]
    if capture_path:
        lines.append(f"capture: {capture_path}")
    mismatches = [r for r in rows if r.status == "mismatch" and r.critical]
    missing = [r for r in rows if r.status == "missing_capture" and r.critical]
    matches = [r for r in rows if r.status == "match"]

    lines.append(f"match={len(matches)} mismatch={len(mismatches)} missing_capture={len(missing)}")
    lines.append("")
    for r in rows:
        flag = {"match": "OK", "mismatch": "FAIL", "missing_capture": "SKIP"}.get(
            r.status, r.status
        )
        crit = " [critical]" if r.critical and r.status != "match" else ""
        lines.append(f"{flag}{crit} {r.field}")
        if r.status != "match":
            lines.append(f"  tokenkey: {r.tokenkey[:200]}")
            lines.append(f"  captured: {r.captured[:200]}")
            if r.note:
                lines.append(f"  note: {r.note}")
    if mismatches:
        lines.append("")
        lines.append("action: update backend/internal/pkg/claude/constants.go,")
        lines.append("  identity_service*.go, gateway_service.go mimic path; add constants_test;")
        lines.append("  run preflight; open docs/spec-delta-cc-<patch>.md PR.")
    tls_mismatch = any(r.field.startswith("tls.") and r.status == "mismatch" for r in rows)
    if tls_mismatch:
        lines.append("")
        lines.append("action_tls: ja3 changed — update deploy/aws/stage0/tk_canonical_cc_oauth.json")
        lines.append("  then ops/anthropic/manage-anthropic-config.py plan/apply on edges.")
    return "\n".join(lines)


def has_actionable_mismatch(rows: list[DiffRow]) -> bool:
    return any(r.status == "mismatch" and r.critical for r in rows)


def cmd_diff(args: argparse.Namespace) -> int:
    baseline = load_tokenkey_baseline(Path(args.repo_root) if args.repo_root else None)
    bundle = load_capture_bundle(Path(args.bundle))
    rows = diff_baseline_vs_capture(baseline, bundle)
    print(format_diff_report(rows, capture_path=str(args.bundle)))
    if args.check and has_actionable_mismatch(rows):
        return 1
    return 0


def cmd_bundle_from_artifacts(args: argparse.Namespace) -> int:
    tls_path = Path(args.tls_json)
    tls_data = json.loads(tls_path.read_text(encoding="utf-8"))
    observed = tls_data.get("observed") or tls_data
    if "fingerprints" in tls_data:
        observed = (tls_data.get("fingerprints") or [None])[0] or observed
    http_by_variant: dict[str, dict[str, Any]] = {}
    if args.http_log:
        http_by_variant = load_http_log(Path(args.http_log))
    bundle = bundle_from_artifacts(
        cc_version=args.cc_version or _ua_version(observed.get("user_agent", "")),
        tls_observed=observed,
        http_by_variant=http_by_variant,
        collector_url=args.collector or "",
    )
    out = Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(bundle, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(out)
    return 0


def cmd_show_baseline(_args: argparse.Namespace) -> int:
    baseline = load_tokenkey_baseline()
    print(json.dumps(baseline, indent=2, sort_keys=True))
    return 0


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(description=__doc__)
    sub = p.add_subparsers(dest="cmd", required=True)

    diff = sub.add_parser("diff", help="Compare capture bundle to TokenKey baseline")
    diff.add_argument("--bundle", required=True, help="Capture bundle JSON path")
    diff.add_argument("--repo-root", default="", help="Repo root override")
    diff.add_argument(
        "--check",
        action="store_true",
        help="Exit 1 when critical mismatches exist",
    )
    diff.set_defaults(func=cmd_diff)

    check = sub.add_parser("check", help="diff --check shorthand")
    check.add_argument("--bundle", required=True)
    check.add_argument("--repo-root", default="")
    check.set_defaults(func=lambda a: cmd_diff(argparse.Namespace(**{**vars(a), "check": True})))

    bfa = sub.add_parser(
        "bundle-from-artifacts",
        help="Build bundle JSON from TLS capture file + optional HTTP log",
    )
    bfa.add_argument("--tls-json", required=True)
    bfa.add_argument("--http-log", default="")
    bfa.add_argument("--cc-version", default="")
    bfa.add_argument("--collector", default="")
    bfa.add_argument("--out", required=True)
    bfa.set_defaults(func=cmd_bundle_from_artifacts)

    show = sub.add_parser("show-baseline", help="Print TokenKey baseline JSON")
    show.set_defaults(func=cmd_show_baseline)
    return p


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())
