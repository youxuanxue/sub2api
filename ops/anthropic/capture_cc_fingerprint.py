#!/usr/bin/env python3
"""Deterministic Claude Code fingerprint capture diff for TokenKey alignment.

Reads a capture bundle (TLS collector + optional HTTP mitm log) and compares it
against live repo constants (constants.go, identity_service*, tk_canonical JSON).

Subcommands:
  diff    Compare --bundle to TokenKey baseline; human report on stdout.
  check   Same as diff but exits 1 when actionable mismatches exist.
  check-env  Verify cc0-here / claude0-here launchers and proxy stack are up.
  check-tls  Exit 1 when bundle TLS ja3 fields mismatch TokenKey baseline.
  write-drift-spec  Write docs/spec-delta/cc-tls-drift-*.md from a drift bundle.
  bundle-from-artifacts  Build bundle JSON from TLS capture + HTTP log files.
  tls-observed-from-pcap  Build tls-observed.json from a passive-pcap tshark TSV.

HTTP betas are recorded as a full per-model-family distribution (not last-wins):
cc is bimodal on Haiku — two beta sets alternate across requests in one session
(youxuanxue/sub2api#429). A bimodal field whose baseline matches one observed
variant is reported as ``needs_investigation`` (non-blocking), never a hard
mismatch against one arbitrary sample.

stdlib-only except when invoked as __main__ with no network.
"""
from __future__ import annotations

import argparse
import importlib.util
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
# Single declared source for the CC system-prompt anchors (shared with the
# guard scripts/sentinels/check-cc-system-prompt.py). Only the stable identity
# anchors + billing prefix are tracked — the full prompt is dynamic.
SYSTEM_PROMPT_REGISTRY = REPO_ROOT / "scripts/sentinels/cc-system-prompt.json"

# Fields that block merge / require code fix when mismatched.
CRITICAL_HTTP_FIELDS = frozenset(
    {
        "canonical.user_agent_version",
        "mimic.cli_version",
        "mimic.stainless_package_version",
        "canonical.stainless_package_version",
        "mimic.stainless_runtime_version",
        "canonical.stainless_runtime_version",
        "betas.sonnet_mimicry",
        "betas.haiku_mimicry",
        # Identity banner drift means spoofed clients no longer look like real
        # CC to upstream Anthropic (client_validation_error 403). Hard fail.
        "system.identity_anchor",
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


def _load_system_prompt_anchors(repo_root: Path | None = None) -> dict[str, Any]:
    """Read the canonical CC system-prompt anchors from the sentinel registry.

    Returns ``{"identity_prefixes": [...], "billing_prefix": "..."}``. The same
    file is the source of truth for the Go-copy guard, so the capture diff and
    the guard can never disagree on what "canonical" means.
    """
    root = repo_root or REPO_ROOT
    path = root / SYSTEM_PROMPT_REGISTRY.relative_to(REPO_ROOT)
    data = json.loads(_read_text(path))
    anchors = data.get("capture_anchors") or {}
    return {
        "identity_prefixes": list(anchors.get("identity_prefixes") or []),
        "billing_prefix": str(anchors.get("billing_prefix") or ""),
    }


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
            "user_agent_shape": "claude-cli/<version> (external, cli)",
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
            "stainless_runtime_version": _extract_map_string(
                constants_src, "X-Stainless-Runtime-Version"
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
        "system": _load_system_prompt_anchors(root),
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
    if "opus" in model_l:
        return "opus"
    if "sonnet" in model_l:
        return "sonnet"
    if model_l:
        return "sonnet"
    return None


def aggregate_http_records(
    records: list[dict[str, Any]],
) -> dict[str, dict[str, Any]]:
    """Group HTTP records by model family, keeping the FULL per-variant
    distribution of ``anthropic-beta`` headers (not last-wins).

    cc fires more than one request per model family per session (the main
    response plus background tasks such as title generation), and the Haiku beta
    set is *bimodal* — two sets alternate across requests on the same model (see
    youxuanxue/sub2api#429). Last-wins collapses that to a single arbitrary
    sample, so a single capture can "prove" either set. This keeps every
    observed variant with its occurrence count so the diff can flag a bimodal
    field for investigation instead of hard-failing against one half of real
    traffic.

    Returns ``{variant: {"total_requests": N, "unique": [
        {"anthropic_beta": header, "count": C, "record": rec}, ...]}}`` with
    ``unique`` ordered by descending count, ties broken by first appearance
    (deterministic — no reliance on dict insertion luck or wall-clock).
    """
    grouped: dict[str, list[dict[str, Any]]] = {}
    for rec in records:
        variant = _http_variant(str(rec.get("model") or ""))
        if variant:
            grouped.setdefault(variant, []).append(rec)
    out: dict[str, dict[str, Any]] = {}
    for variant, recs in grouped.items():
        # header -> [count, first_index, representative_record]
        counts: dict[str, list[Any]] = {}
        for idx, rec in enumerate(recs):
            beta = str(rec.get("anthropic_beta", "") or "")
            entry = counts.get(beta)
            if entry is None:
                counts[beta] = [1, idx, rec]
            else:
                entry[0] += 1
        unique = sorted(counts.items(), key=lambda kv: (-kv[1][0], kv[1][1]))
        out[variant] = {
            "total_requests": len(recs),
            "unique": [
                {"anthropic_beta": beta, "count": meta[0], "record": meta[2]}
                for beta, meta in unique
            ],
        }
    return out


def _dominant_record(dist_variant: dict[str, Any]) -> dict[str, Any]:
    """Most-frequent beta-set record for a variant (deterministic tie-break)."""
    unique = dist_variant.get("unique") or []
    return unique[0]["record"] if unique else {}


def serialize_http_variants(
    dist: dict[str, dict[str, Any]],
) -> dict[str, dict[str, Any]]:
    """Bundle-friendly distribution: drop the raw record, keep header + count."""
    return {
        variant: {
            "total_requests": v.get("total_requests", 0),
            "unique": [
                {
                    "anthropic_beta": u.get("anthropic_beta", ""),
                    "count": u.get("count", 0),
                }
                for u in (v.get("unique") or [])
            ],
        }
        for variant, v in dist.items()
    }


def load_http_records(path: Path) -> list[dict[str, Any]]:
    """Parse all HTTP mitm log lines into raw records (order preserved)."""
    records: list[dict[str, Any]] = []
    for line in path.read_text(encoding="utf-8").splitlines():
        rec = _parse_http_log_line(line)
        if rec:
            records.append(rec)
    return records


INTERACTIVE_UA_SUFFIX = " (external, cli)"
REPL_IDENTITY_BANNER = "You are Claude Code, Anthropic's official CLI for Claude"


def validate_interactive_http_log(path: Path) -> dict[str, Any]:
    """Validate mitm log from interactive REPL capture (prod-dominant cli cohort)."""
    records = load_http_records(path)
    if not records:
        raise ValueError("empty interactive HTTP log")
    uas = {rec.get("user_agent", "") for rec in records}
    bad_uas = sorted(u for u in uas if INTERACTIVE_UA_SUFFIX not in u)
    if bad_uas:
        raise ValueError(f"expected UA suffix {INTERACTIVE_UA_SUFFIX!r}, got: {bad_uas}")
    has_banner = any(
        REPL_IDENTITY_BANNER in (anchor.get("text_head") or "")
        for rec in records
        for anchor in (rec.get("system_anchors") or [])
    )
    if not has_banner:
        raise ValueError("missing Claude Code REPL identity banner in system_anchors")
    return {"request_count": len(records), "user_agent": next(iter(uas))}


def load_http_log(path: Path) -> dict[str, dict[str, Any]]:
    """Dominant-variant representative record per model family.

    Distribution-aware replacement for the old last-wins picker: when a family
    is bimodal the *most frequent* beta set wins (ties broken by first
    appearance), a deterministic strict improvement over the arbitrary last
    sample. Use :func:`aggregate_http_records` / :func:`serialize_http_variants`
    to retain the full distribution for the bundle.
    """
    dist = aggregate_http_records(load_http_records(path))
    return {variant: _dominant_record(v) for variant, v in dist.items()}


def aggregate_system_anchors(records: list[dict[str, Any]]) -> list[str]:
    """Union of every observed system-block head across all HTTP records.

    Different CC requests carry different system blocks (main response vs
    background tasks, billing block vs identity banner), so collect the
    deduped set of ``text_head`` strings — order-preserving for determinism.
    """
    seen: dict[str, None] = {}
    for rec in records:
        for anchor in rec.get("system_anchors") or []:
            if isinstance(anchor, dict):
                head = str(anchor.get("text_head") or "").strip()
                if head:
                    seen.setdefault(head, None)
    return list(seen.keys())


def bundle_from_artifacts(
    *,
    cc_version: str,
    tls_observed: dict[str, Any],
    http_by_variant: dict[str, dict[str, Any]] | None = None,
    http_variants: dict[str, dict[str, Any]] | None = None,
    system_anchors: list[str] | None = None,
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
        # ``http`` keeps one representative (dominant) record per family for the
        # legacy single-sample diff path; ``http_variants`` keeps the full
        # per-family beta distribution so bimodal fields stay visible (#429).
        "http": http_by_variant or {},
        "http_variants": http_variants or {},
        # Deduped heads of every observed system block (identity banner + billing
        # block). Optional — TLS-only bundles omit it; the diff then SKIPs.
        "system": {"anchors": list(system_anchors or [])},
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

    def _cap_stainless_header(field: str) -> str:
        http = capture.get("http") or {}
        for variant in ("haiku", "sonnet", "opus"):
            rec = http.get(variant) or {}
            xs = rec.get("x_stainless") or {}
            if isinstance(xs, dict):
                val = str(xs.get(field) or "").strip()
                if val:
                    return val
        return ""

    cap_runtime = _cap_stainless_header("X-Stainless-Runtime-Version")
    canon_runtime = baseline["canonical_http"]["stainless_runtime_version"]
    mimic_runtime = baseline["mimic_http"]["stainless_runtime_version"]
    if not cap_runtime:
        rows.append(
            DiffRow(
                "canonical.stainless_runtime_version",
                canon_runtime,
                "",
                "missing_capture",
                critical="canonical.stainless_runtime_version" in CRITICAL_HTTP_FIELDS,
                note="Run HTTP mitm capture with --http for runtime version",
            )
        )
        rows.append(
            DiffRow(
                "mimic.stainless_runtime_version",
                mimic_runtime,
                "",
                "missing_capture",
                critical="mimic.stainless_runtime_version" in CRITICAL_HTTP_FIELDS,
                note="Run HTTP mitm capture with --http for runtime version",
            )
        )
    else:
        rows.append(
            DiffRow(
                "canonical.stainless_runtime_version",
                canon_runtime,
                cap_runtime,
                "match" if canon_runtime == cap_runtime else "mismatch",
                critical="canonical.stainless_runtime_version" in CRITICAL_HTTP_FIELDS,
            )
        )
        rows.append(
            DiffRow(
                "mimic.stainless_runtime_version",
                mimic_runtime,
                cap_runtime,
                "match" if mimic_runtime == cap_runtime else "mismatch",
                critical="mimic.stainless_runtime_version" in CRITICAL_HTTP_FIELDS,
            )
        )

    http = capture.get("http") or {}
    variants = capture.get("http_variants") or {}
    for variant, beta_key in (("haiku", "haiku_mimicry"), ("sonnet", "sonnet_mimicry")):
        rec = http.get(variant)
        dist = variants.get(variant) or {}
        unique = dist.get("unique") or []
        tk_betas = baseline["betas"][beta_key]
        is_critical = f"betas.{beta_key}" in CRITICAL_HTTP_FIELDS

        # Resolve the observed beta-set distribution. Prefer the full per-family
        # distribution (http_variants); fall back to the single representative
        # record for legacy bundles that predate #429.
        if unique:
            observed_sets = [_beta_list(u.get("anthropic_beta", "")) for u in unique]
            counts = [int(u.get("count", 0)) for u in unique]
            total = int(dist.get("total_requests", sum(counts)))
        elif rec is not None:
            observed_sets = [_beta_list(rec.get("anthropic_beta", ""))]
            counts = [1]
            total = 1
        else:
            rows.append(
                DiffRow(
                    f"betas.{beta_key}",
                    ",".join(tk_betas),
                    "",
                    "missing_capture",
                    critical=is_critical,
                    note=f"Run HTTP mitm capture with --http for {variant}",
                )
            )
            continue

        matches = [tk_betas == s for s in observed_sets]
        if len(observed_sets) == 1:
            # Unimodal — the classic hard match/mismatch.
            rows.append(
                DiffRow(
                    f"betas.{beta_key}",
                    ",".join(tk_betas),
                    ",".join(observed_sets[0]),
                    "match" if matches[0] else "mismatch",
                    critical=is_critical,
                )
            )
            continue

        # Bimodal (or worse): cc alternates >1 beta set across requests on the
        # same model. Do NOT hard-fail against one arbitrary half of traffic —
        # that is exactly the #429 sampling artifact. If the baseline matches
        # ANY observed variant it is a deliberate decision point, not a drift.
        summary = f"{total} requests, {len(observed_sets)} unique beta header(s)"
        captured = " | ".join(
            f"[{c}x] {','.join(s)}" for c, s in zip(counts, observed_sets)
        )
        if any(matches):
            idx = matches.index(True)
            rows.append(
                DiffRow(
                    f"betas.{beta_key}",
                    ",".join(tk_betas),
                    captured,
                    "needs_investigation",
                    critical=is_critical,
                    note=(
                        f"bimodal — {summary}; baseline matches variant "
                        f"{idx + 1}/{len(observed_sets)} ({counts[idx]}x). Decide the "
                        f"canonical {variant} target deliberately "
                        f"(youxuanxue/sub2api#429); do not hard-align to one sample."
                    ),
                )
            )
        else:
            rows.append(
                DiffRow(
                    f"betas.{beta_key}",
                    ",".join(tk_betas),
                    captured,
                    "mismatch",
                    critical=is_critical,
                    note=(
                        f"bimodal — {summary}; baseline matches NONE of the observed "
                        f"variants. Re-capture and realign (youxuanxue/sub2api#429)."
                    ),
                )
            )

    rows.extend(_system_prompt_rows(baseline, capture))
    return rows


def _system_prompt_rows(
    baseline: dict[str, Any], capture: dict[str, Any]
) -> list[DiffRow]:
    """System-prompt anchor rows: identity banner (hard) + billing prefix (soft).

    Only the stable anchors are compared (prefix/substring match) — the full CC
    system prompt is dynamic (cwd/git/date/env) and is never byte-aligned.
    """
    base_system = baseline.get("system") or {}
    identity_prefixes = [p for p in (base_system.get("identity_prefixes") or []) if p]
    billing_prefix = str(base_system.get("billing_prefix") or "")
    cap_anchors = [
        str(a) for a in ((capture.get("system") or {}).get("anchors") or []) if a
    ]

    rows: list[DiffRow] = []
    tk_identity = " | ".join(identity_prefixes)

    if not cap_anchors:
        rows.append(
            DiffRow(
                "system.identity_anchor",
                tk_identity,
                "",
                "missing_capture",
                critical="system.identity_anchor" in CRITICAL_HTTP_FIELDS,
                note="Run HTTP mitm capture with --http (no system blocks recorded)",
            )
        )
        return rows

    matched_head = next(
        (h for h in cap_anchors for p in identity_prefixes if h.startswith(p)),
        "",
    )
    rows.append(
        DiffRow(
            "system.identity_anchor",
            tk_identity,
            matched_head or " | ".join(cap_anchors),
            "match" if matched_head else "mismatch",
            critical="system.identity_anchor" in CRITICAL_HTTP_FIELDS,
            note=(
                ""
                if matched_head
                else "No captured system block matches any canonical CC identity "
                "anchor — real CC banner drifted. Update "
                "scripts/sentinels/cc-system-prompt.json + the Go copies with "
                "capture evidence (docs/spec-delta/cc-system-prompt.md)."
            ),
        )
    )

    billing_present = bool(billing_prefix) and any(
        billing_prefix in h for h in cap_anchors
    )
    rows.append(
        DiffRow(
            "system.billing_prefix",
            billing_prefix,
            "present" if billing_present else "absent",
            "match" if billing_present else "needs_investigation",
            critical=False,
            note=(
                ""
                if billing_present
                else "Billing-attribution block prefix not seen in this capture. "
                "Expected for count_tokens / some sub-requests; INVESTIGATE only "
                "if missing from a normal /v1/messages call."
            ),
        )
    )
    return rows


def format_diff_report(rows: list[DiffRow], *, capture_path: str = "") -> str:
    lines = ["TokenKey cc fingerprint diff"]
    if capture_path:
        lines.append(f"capture: {capture_path}")
    mismatches = [r for r in rows if r.status == "mismatch" and r.critical]
    missing = [r for r in rows if r.status == "missing_capture" and r.critical]
    investigate = [r for r in rows if r.status == "needs_investigation"]
    matches = [r for r in rows if r.status == "match"]

    lines.append(
        f"match={len(matches)} mismatch={len(mismatches)} "
        f"needs_investigation={len(investigate)} missing_capture={len(missing)}"
    )
    lines.append("")
    for r in rows:
        flag = {
            "match": "OK",
            "mismatch": "FAIL",
            "needs_investigation": "INVESTIGATE",
            "missing_capture": "SKIP",
        }.get(r.status, r.status)
        crit = (
            " [critical]"
            if r.critical and r.status in ("mismatch", "missing_capture")
            else ""
        )
        lines.append(f"{flag}{crit} {r.field}")
        if r.status != "match":
            lines.append(f"  tokenkey: {r.tokenkey[:200]}")
            lines.append(f"  captured: {r.captured[:200]}")
            if r.note:
                lines.append(f"  note: {r.note}")
    if investigate:
        lines.append("")
        lines.append(
            "note: bimodal beta field(s) observed — NOT a hard mismatch (exit 0)."
        )
        lines.append(
            "  Characterize the A/B variants (request purpose / tool presence /"
        )
        lines.append(
            "  server gating) before changing any beta constant"
            " (youxuanxue/sub2api#429)."
        )
    if mismatches:
        lines.append("")
        lines.append("action: update backend/internal/pkg/claude/constants.go,")
        lines.append("  identity_service*.go, gateway_service.go mimic path; add constants_test;")
        lines.append("  run preflight; open docs/spec-delta/cc-<patch>.md PR.")
    tls_mismatch = any(r.field.startswith("tls.") and r.status == "mismatch" for r in rows)
    if tls_mismatch:
        lines.append("")
        lines.append("action_tls: ja3 changed — update deploy/aws/stage0/tk_canonical_cc_oauth.json")
        lines.append("  then ops/anthropic/manage-anthropic-config.py plan/apply on edges.")
    return "\n".join(lines)


def has_actionable_mismatch(rows: list[DiffRow]) -> bool:
    # needs_investigation (bimodal beta field, #429) is deliberately NOT
    # actionable — it must not fail check/diff against one arbitrary sample.
    return any(r.status == "mismatch" and r.critical for r in rows)


def has_needs_investigation(rows: list[DiffRow]) -> bool:
    return any(r.status == "needs_investigation" for r in rows)


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
    # Fallback only — the operator's ~/.config/cc0/env CC0_EXPECT_EGRESS_IP wins.
    # Current canonical cc0 SOCKS-chain egress (see docs/spec-delta/cc-2.1.16x.md).
    # 16.147.170.3 (retired EC2 us1 EIP) was decommissioned 2026-06-07; egress moved to
    # edge-ls-us-oh-3 (StaticIp-oh-3). Its public IP rotated 52.15.35.197 → 3.148.79.145
    # on 2026-06-08 (old IP was an ephemeral AWS re-adopted to *.coverahealth.com).
    expect_ip = os.environ.get("CC0_EXPECT_EGRESS_IP", "3.148.79.145")
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
    repo_root: Path,
    out_path: Path | None = None,
) -> Path:
    baseline = load_tokenkey_baseline(repo_root)
    bundle = load_capture_bundle(bundle_path)
    rows = diff_baseline_vs_capture(baseline, bundle)
    report = format_diff_report(rows, capture_path=str(bundle_path))
    stamp = datetime.now(timezone.utc).strftime("%Y%m%d")
    cc_ver = bundle.get("cc_version") or "unknown"
    out = out_path or (repo_root / f"docs/spec-delta/cc-tls-drift-{stamp}.md")
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
    http_variants: dict[str, dict[str, Any]] = {}
    system_anchors: list[str] = []
    if args.http_log:
        records = load_http_records(Path(args.http_log))
        dist = aggregate_http_records(records)
        http_by_variant = {v: _dominant_record(d) for v, d in dist.items()}
        http_variants = serialize_http_variants(dist)
        system_anchors = aggregate_system_anchors(records)
    bundle = bundle_from_artifacts(
        cc_version=args.cc_version or _ua_version(observed.get("user_agent", "")),
        tls_observed=observed,
        http_by_variant=http_by_variant,
        http_variants=http_variants,
        system_anchors=system_anchors,
        collector_url=args.collector or "",
    )
    out = Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(bundle, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(out)
    return 0


def _load_kiro_ja3_engine() -> Any:
    """Reuse the Kiro engine's tshark/JA3 helpers (same wire format, no network)."""
    kiro_py = REPO_ROOT / "ops/kiro/capture_kiro_fingerprint.py"
    spec = importlib.util.spec_from_file_location("capture_kiro_fingerprint", kiro_py)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load kiro JA3 engine from {kiro_py}")
    mod = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = mod
    spec.loader.exec_module(mod)
    return mod


def tls_observed_from_tshark_tsv(
    tsv_text: str,
    *,
    cc_version: str = "",
    source: str = "passive-pcap",
) -> dict[str, Any]:
    """Parse one ClientHello row and return collector-shaped tls-observed JSON."""
    kiro = _load_kiro_ja3_engine()
    fields = kiro.parse_tshark_tsv(tsv_text)
    ja3_raw, ja3_hash = kiro.compute_ja3(
        fields["version"],
        fields["ciphers"],
        fields["extensions"],
        fields["curves"],
        fields["point_formats"],
    )
    ua = f"claude-cli/{cc_version} (external, cli)" if cc_version else ""
    return {
        "ja3_hash": ja3_hash,
        "ja3_raw": ja3_raw,
        "user_agent": ua,
        "server_name": fields.get("server_name", ""),
        "source": source,
    }


def cmd_tls_observed_from_pcap(args: argparse.Namespace) -> int:
    tsv_text = Path(args.tshark_tsv).read_text(encoding="utf-8")
    observed = tls_observed_from_tshark_tsv(
        tsv_text,
        cc_version=args.cc_version or "",
        source=args.source or "passive-pcap",
    )
    out = Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(observed, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    print(out)
    print(f"ja3_hash={observed['ja3_hash']}")
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
        help="Write docs/spec-delta/cc-tls-drift-*.md from capture bundle",
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

    pcap = sub.add_parser(
        "tls-observed-from-pcap",
        help="Build tls-observed.json from passive-pcap tshark TSV (JA3 ground truth)",
    )
    pcap.add_argument("--tshark-tsv", required=True)
    pcap.add_argument("--out", required=True)
    pcap.add_argument("--cc-version", default="")
    pcap.add_argument("--source", default="passive-pcap")
    pcap.set_defaults(func=cmd_tls_observed_from_pcap)

    show = sub.add_parser("show-baseline", help="Print TokenKey baseline JSON")
    show.set_defaults(func=cmd_show_baseline)
    return p


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    sys.exit(main())
