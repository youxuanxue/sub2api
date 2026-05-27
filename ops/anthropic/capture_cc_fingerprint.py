#!/usr/bin/env python3
"""Deterministic Claude Code fingerprint capture diff for TokenKey alignment.

Reads a capture bundle (TLS collector + optional HTTP mitm log) and compares it
against live repo constants (constants.go, identity_service*, tk_canonical JSON).

Subcommands:
  diff    Compare --bundle to TokenKey baseline; human report on stdout.
  check   Same as diff but exits 1 when actionable mismatches exist.
  check-env  Verify cc0-here / claude0-here launchers and proxy stack are up.
  check-tls  Exit 1 when bundle TLS ja3 fields mismatch TokenKey baseline.
  write-drift-spec  Write docs/spec-delta-cc-tls-drift-*.md from a drift bundle.
  bundle-from-artifacts  Build bundle JSON from TLS capture + HTTP log files.

stdlib-only except when invoked as __main__ with no network.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import socket
import subprocess
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


@dataclass(frozen=True)
class EnvCheckRow:
    component: str
    status: str  # ok | fail | warn | skip
    detail: str = ""


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
    tls_profile_path = root / TLS_PROFILE_JSON.relative_to(REPO_ROOT)
    tls_profile = json.loads(_read_text(tls_profile_path))

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


def has_tls_mismatch(rows: list[DiffRow]) -> bool:
    return any(
        r.field.startswith("tls.") and r.status == "mismatch" for r in rows
    )


def _tcp_open(host: str, port: int, *, timeout: float = 2.0) -> bool:
    try:
        with socket.create_connection((host, port), timeout=timeout):
            return True
    except OSError:
        return False


def _parse_host_port(endpoint: str, *, default_host: str) -> tuple[str, int]:
    if ":" not in endpoint:
        raise ValueError(f"expected host:port, got {endpoint!r}")
    host, port_s = endpoint.rsplit(":", 1)
    host = host.strip() or default_host
    return host, int(port_s)


def _launcher_path(name: str) -> Path:
    local = Path.home() / ".local" / "bin" / name
    if local.is_file() and os.access(local, os.X_OK):
        return local
    found = shutil.which(name)
    if found:
        return Path(found)
    return local


def _curl_ipify_via_proxy(proxy_url: str, *, timeout: int = 8) -> str:
    proc = subprocess.run(
        [
            "curl",
            "-fsS",
            "--max-time",
            str(timeout),
            "--proxy",
            proxy_url,
            "https://api.ipify.org",
        ],
        check=False,
        capture_output=True,
        text=True,
    )
    if proc.returncode != 0:
        raise RuntimeError((proc.stderr or proc.stdout or "curl failed").strip())
    return proc.stdout.strip()


def _claude_desktop_proxy_argv() -> tuple[bool, str]:
    if sys.platform != "darwin":
        return False, "Claude Desktop check is macOS-only"
    proc = subprocess.run(
        ["pgrep", "-x", "Claude"],
        check=False,
        capture_output=True,
        text=True,
    )
    if proc.returncode != 0:
        return False, "Claude.app is not running (start with claude0-here)"
    pid = (proc.stdout or "").strip().splitlines()[0]
    ps = subprocess.run(
        ["ps", "-p", pid, "-ww", "-o", "command="],
        check=False,
        capture_output=True,
        text=True,
    )
    cmd = (ps.stdout or "").strip()
    if "--proxy-server" not in cmd or "--disable-quic" not in cmd:
        return (
            False,
            "Claude running without claude0-here proxy flags (--proxy-server, --disable-quic)",
        )
    return True, f"PID {pid} uses claude0-here Chromium proxy flags"


def run_check_env(
    *,
    relax_desktop: bool = False,
    skip_egress: bool = False,
) -> list[EnvCheckRow]:
    """Verify cc0-here (CLI proxy stack) and claude0-here (Desktop) readiness."""
    rows: list[EnvCheckRow] = []
    env_file = Path.home() / ".config" / "cc0" / "env"
    if env_file.is_file():
        rows.append(EnvCheckRow("cc0.env", "ok", str(env_file)))
    else:
        rows.append(
            EnvCheckRow(
                "cc0.env",
                "warn",
                f"missing {env_file} (using defaults)",
            )
        )

    socks = os.environ.get("CC0_SOCKS5", "127.0.0.1:1093")
    gost_host = os.environ.get("CC0_GOST_HTTP_HOST", "127.0.0.1")
    gost_port = int(os.environ.get("CC0_GOST_HTTP_PORT", "11800"))
    expect_ip = os.environ.get("CC0_EXPECT_EGRESS_IP", "13.134.80.182")
    try:
        socks_host, socks_port = _parse_host_port(socks, default_host="127.0.0.1")
    except ValueError as exc:
        rows.append(EnvCheckRow("cc0.socks", "fail", str(exc)))
        socks_host, socks_port = "127.0.0.1", 1093

    for label, name in (("cc0-here", "cc0-here"), ("claude0-here", "claude0-here")):
        path = _launcher_path(name)
        if path.is_file() and os.access(path, os.X_OK):
            rows.append(EnvCheckRow(label, "ok", str(path)))
        else:
            rows.append(EnvCheckRow(label, "fail", f"not executable: {path}"))

    if _tcp_open(socks_host, socks_port):
        rows.append(EnvCheckRow("cc0.socks", "ok", f"listening {socks_host}:{socks_port}"))
    else:
        rows.append(
            EnvCheckRow(
                "cc0.socks",
                "fail",
                f"no listener on {socks_host}:{socks_port} (fingerprint browser SOCKS)",
            )
        )

    gost_url = f"http://{gost_host}:{gost_port}"
    if _tcp_open(gost_host, gost_port):
        rows.append(EnvCheckRow("cc0.gost", "ok", f"listening {gost_url}"))
    else:
        rows.append(
            EnvCheckRow(
                "cc0.gost",
                "fail",
                f"no listener on {gost_url} (run cc0-here once or cc0-gost)",
            )
        )

    if shutil.which("curl") and _tcp_open(gost_host, gost_port):
        try:
            observed = _curl_ipify_via_proxy(gost_url)
            if skip_egress:
                rows.append(
                    EnvCheckRow(
                        "cc0.egress",
                        "skip",
                        f"observed {observed} (egress check skipped)",
                    )
                )
            elif observed == expect_ip:
                rows.append(
                    EnvCheckRow("cc0.egress", "ok", f"egress {observed} via gost")
                )
            else:
                rows.append(
                    EnvCheckRow(
                        "cc0.egress",
                        "fail",
                        f"egress {observed} != expected {expect_ip}",
                    )
                )
        except RuntimeError as exc:
            rows.append(EnvCheckRow("cc0.egress", "fail", str(exc)))
    elif not shutil.which("curl"):
        rows.append(EnvCheckRow("cc0.egress", "skip", "curl not installed"))
    else:
        rows.append(EnvCheckRow("cc0.egress", "skip", "gost not listening"))

    ok_desktop, detail = _claude_desktop_proxy_argv()
    if ok_desktop:
        rows.append(EnvCheckRow("claude0-here.desktop", "ok", detail))
    elif relax_desktop:
        rows.append(EnvCheckRow("claude0-here.desktop", "warn", detail))
    else:
        rows.append(EnvCheckRow("claude0-here.desktop", "fail", detail))

    return rows


def format_check_env_report(rows: list[EnvCheckRow]) -> str:
    lines = ["TokenKey cc fingerprint check-env"]
    fail = sum(1 for r in rows if r.status == "fail")
    warn = sum(1 for r in rows if r.status == "warn")
    ok = sum(1 for r in rows if r.status == "ok")
    lines.append(f"ok={ok} warn={warn} fail={fail}")
    lines.append("")
    for r in rows:
        flag = {"ok": "OK", "fail": "FAIL", "warn": "WARN", "skip": "SKIP"}.get(
            r.status, r.status.upper()
        )
        lines.append(f"{flag} {r.component}")
        if r.detail:
            lines.append(f"  {r.detail}")
    return "\n".join(lines)


def check_env_failed(rows: list[EnvCheckRow]) -> bool:
    return any(r.status == "fail" for r in rows)


def write_tls_drift_spec(
    *,
    bundle_path: Path,
    repo_root: Path | None = None,
    out_path: Path | None = None,
) -> Path:
    root = repo_root or REPO_ROOT
    baseline = load_tokenkey_baseline(REPO_ROOT)
    bundle = load_capture_bundle(bundle_path)
    rows = diff_baseline_vs_capture(baseline, bundle)
    report = format_diff_report(rows, capture_path=str(bundle_path))
    stamp = datetime.now(timezone.utc).strftime("%Y%m%d")
    cc_ver = bundle.get("cc_version") or "unknown"
    out = out_path or (
        repo_root / f"docs/spec-delta-cc-tls-drift-{stamp}.md"
    )
    cap_tls = bundle.get("tls") or {}
    body = "\n".join(
        [
            "---",
            "title: cc TLS drift (automated daily capture)",
            f"cc_version: {cc_ver}",
            f"bundle: {bundle_path.name}",
            "status: draft",
            "---",
            "",
            "# spec-delta: cc TLS drift (automated)",
            "",
            "## Background",
            "",
            "Daily `sessionStart` hook captured real cc TLS ClientHello and found",
            "ja3 drift vs `deploy/aws/stage0/tk_canonical_cc_oauth.json`.",
            "",
            "## Delta",
            "",
            "- MODIFIED: TLS profile `tk_canonical_cc_oauth` (ja3 / cipher material)",
            "- MODIFIED: HTTP constants if `canonical.user_agent_version` also drifted",
            "",
            "## Evidence (capture)",
            "",
            f"- ja3_hash (captured): `{cap_tls.get('ja3_hash', '')}`",
            f"- ja3_hash (tokenkey): `{baseline['tls']['ja3_hash']}`",
            f"- ja3_raw (captured): `{cap_tls.get('ja3_raw', '')}`",
            "",
            "## Diff report",
            "",
            "```text",
            report,
            "```",
            "",
            "## Validation",
            "",
            "- `bash ops/anthropic/capture-cc-fingerprint.sh capture`",
            "- `ops/anthropic/manage-anthropic-config.py plan/apply/verify` on deployable edges",
            "- `./scripts/preflight.sh`",
            "",
        ]
    )
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(body, encoding="utf-8")
    return out


def cmd_check_env(args: argparse.Namespace) -> int:
    rows = run_check_env(
        relax_desktop=args.relax_desktop,
        skip_egress=args.skip_egress,
    )
    if args.json:
        payload = {
            "ok": not check_env_failed(rows),
            "rows": [
                {"component": r.component, "status": r.status, "detail": r.detail}
                for r in rows
            ],
        }
        print(json.dumps(payload, indent=2, sort_keys=True))
    else:
        print(format_check_env_report(rows))
    return 1 if check_env_failed(rows) else 0


def cmd_check_tls(args: argparse.Namespace) -> int:
    baseline = load_tokenkey_baseline(Path(args.repo_root) if args.repo_root else None)
    bundle = load_capture_bundle(Path(args.bundle))
    rows = diff_baseline_vs_capture(baseline, bundle)
    if args.json:
        tls_rows = [r for r in rows if r.field.startswith("tls.")]
        print(
            json.dumps(
                {
                    "tls_mismatch": has_tls_mismatch(rows),
                    "rows": [
                        {
                            "field": r.field,
                            "status": r.status,
                            "tokenkey": r.tokenkey,
                            "captured": r.captured,
                        }
                        for r in tls_rows
                    ],
                },
                indent=2,
                sort_keys=True,
            )
        )
    else:
        print(format_diff_report(rows, capture_path=str(args.bundle)))
    return 1 if has_tls_mismatch(rows) else 0


def cmd_write_drift_spec(args: argparse.Namespace) -> int:
    root = Path(args.repo_root) if args.repo_root else REPO_ROOT
    out = (
        Path(args.out)
        if args.out
        else None
    )
    path = write_tls_drift_spec(
        bundle_path=Path(args.bundle),
        repo_root=root,
        out_path=out,
    )
    print(path)
    return 0


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

    env = sub.add_parser(
        "check-env",
        help="Verify cc0-here / claude0-here launchers and proxy stack",
    )
    env.add_argument(
        "--relax-desktop",
        action="store_true",
        help="Claude.app not running is WARN, not FAIL (hook / headless)",
    )
    env.add_argument(
        "--skip-egress",
        action="store_true",
        help="Skip CC0_EXPECT_EGRESS_IP check",
    )
    env.add_argument("--json", action="store_true", help="Machine-readable report")
    env.set_defaults(func=cmd_check_env)

    tls = sub.add_parser(
        "check-tls",
        help="Exit 1 when bundle TLS ja3 mismatches TokenKey baseline",
    )
    tls.add_argument("--bundle", required=True)
    tls.add_argument("--repo-root", default="")
    tls.add_argument("--json", action="store_true")
    tls.set_defaults(func=cmd_check_tls)

    drift = sub.add_parser(
        "write-drift-spec",
        help="Write docs/spec-delta-cc-tls-drift-*.md from capture bundle",
    )
    drift.add_argument("--bundle", required=True)
    drift.add_argument("--repo-root", default="")
    drift.add_argument("--out", default="")
    drift.set_defaults(func=cmd_write_drift_spec)

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
