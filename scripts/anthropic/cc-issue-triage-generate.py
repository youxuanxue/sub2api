#!/usr/bin/env python3
"""Relevance-filtered triage for anthropics/claude-code issues.

Sibling of scripts/upstream/issue-triage-generate.py, but the target is the
public anthropics/claude-code tracker (~62k issues) which — unlike Wei-Shaw/sub2api
(a same-shaped fork) — is MOSTLY client-side noise (IDE/terminal/MCP/plugin/install)
irrelevant to a gateway/relay. So this script adds, on top of the upstream
vocabulary, an EXCLUDE pre-pass plus gateway-relevance keyword groups.

Safety posture (intentional, do NOT "fix"): a keyword match only ever yields
impact="needs_review" / status="candidate_unverified". The watchdog's
is_unresolved_high() gate (the shared scripts/upstream/issue-watchdog.py) promotes an
issue to a fix candidate ONLY when impact ∈ {critical, high} AND status is an
unresolved status — which the automated keyword path never emits. Promotion to a
fix target therefore requires a human-pinned MANUAL_TRIAGE entry. Daily cron is
scan-only and cannot auto-open a code PR from a keyword match.
"""
from __future__ import annotations

import json
import re
import sys
from pathlib import Path

# --- INCLUDE: gateway/relay-relevant signal (re.search, IGNORECASE) ---
GATEWAY_HIGH = [
    ("beta_header_drift", [r"anthropic-beta", r"\bbeta[- ]?header", r"beta=true", r"oauth.*beta"]),
    ("request_shape_400", [r"invalid_request", r"\b400\b", r"thinking\.type", r"thinking\.budget",
                           r"\btool_use\b", r"\btool_result\b", r"count_tokens", r"output_config\.effort",
                           r"context_management", r"cache_control"]),
    ("rate_limit", [r"\b429\b", r"\b529\b", r"overloaded", r"rate[- ]?limit", r"rate_limit_error", r"retry-?after"]),
    ("auth_token", [r"\b401\b", r"\boauth\b", r"token[- ]?refresh", r"refresh[- ]?token", r"unauthorized",
                    r"authentication_error", r"expired.*token"]),
    ("model_lifecycle", [r"deprecat", r"model.*not[- ]?found", r"404.*model", r"claude-opus-4-[789]",
                         r"claude-opus-[5-9]", r"claude-sonnet-4-[6-9]", r"enabled.*adaptive", r"\bsunset\b", r"\bretire"]),
    ("prompt_caching", [r"prompt[- ]?cach", r"cache_creation", r"cache_read", r"\bephemeral\b"]),
    ("new_endpoints", [r"/v1/messages/batches", r"\bbatch(es)?\b.*\bapi\b", r"/v1/files", r"\bfiles api\b"]),
    ("fingerprint_ua", [r"x-stainless", r"\bstainless\b", r"user-agent", r"\bfingerprint\b",
                        r"403.*block", r"cloudflare", r"\bWAF\b"]),
]
GATEWAY_MEDIUM = [
    ("streaming_sse", [r"\bSSE\b", r"event[- ]?stream", r"idle[- ]?timeout", r"stream.*disconnect",
                       r"incomplete.*chunk", r"\[DONE\]"]),
    ("token_limits", [r"\bstop_reason\b", r"\bmax_tokens\b", r"\b1m\b.*context", r"\b200k\b", r"context[- ]?window"]),
    ("effort_structured", [r"\beffort\b", r"structured[- ]?output", r"json_schema", r"response_format"]),
]
# --- EXCLUDE: client-side markers (drop unless a GATEWAY_HIGH signal co-occurs) ---
EXCLUDE = [
    ("ide", [r"\bvs ?code\b", r"jetbrains", r"intellij", r"\bextension\b", r"\bIDE\b"]),
    ("terminal", [r"\btui\b", r"\bterminal\b", r"\brender", r"flicker", r"\bscroll", r"\bspinner\b",
                  r"\bansi\b", r"colou?r"]),
    ("mcp", [r"\bmcp\b", r"model context protocol"]),
    ("plugin", [r"\bplugin", r"marketplace", r"\bsubagent", r"output style"]),
    ("slash_hook", [r"slash command", r"\bhook\b", r"settings\.json", r"keybind", r"shortcut"]),
    ("install", [r"\bnpm\b", r"installer", r"\binstall\b", r"node_modules", r"\bnpx\b"]),
    ("platform", [r"\bwindows\b", r"\bwsl\b", r"powershell", r"devcontainer"]),
]

# Human-pinned overrides. impact "high"/"critical" here (and only here) can promote
# an issue to a fix candidate via the watchdog's is_unresolved_high() gate.
MANUAL_TRIAGE = {
    61348: ("fixed", "fixed_in_tokenkey",
            "Opus 4.7/4.8 reject manual thinking (thinking.type=enabled+budget_tokens) with a 400 "
            "'thinking.type.enabled is not supported … Use thinking.type.adaptive'; TokenKey reactively "
            "self-heals enabled→adaptive on both the Forward and APIKey passthrough paths, hard-gated by "
            "isOpus47OrNewer. Fixed in youxuanxue/sub2api#514 + #518."),
}

FIXED_IDS = {num for num, (impact, _, _) in MANUAL_TRIAGE.items() if impact == "fixed"}


def norm(s: str) -> str:
    return re.sub(r"\s+", " ", s or "").strip()


def matches_re(text: str, groups: list[tuple[str, list[str]]]) -> list[str]:
    out = []
    for name, pats in groups:
        if any(re.search(p, text, re.IGNORECASE) for p in pats):
            out.append(name)
    return out


def classify(issue: dict) -> dict:
    num = int(issue["number"])
    title = norm(issue.get("title", ""))
    body = norm(issue.get("body", ""))
    text = f"{title} {body}"
    upstream = f"anthropics/claude-code#{num}"

    manual = MANUAL_TRIAGE.get(num)
    if manual:
        impact, status, note = manual
        cats = matches_re(text, GATEWAY_HIGH + GATEWAY_MEDIUM)
        return {
            "upstream": upstream,
            "url": issue.get("html_url"),
            "title": title,
            "impact": impact,
            "categories": cats or ["manual"],
            "tokenkey_status": status,
            "rationale": note,
            "updated_at": issue.get("updated_at"),
        }

    high = matches_re(text, GATEWAY_HIGH)
    medium = matches_re(text, GATEWAY_MEDIUM)
    excluded = matches_re(text, EXCLUDE)

    if excluded and not high:
        # Client-side claude-code concern with no strong gateway signal → not our surface.
        impact = "not_applicable"
        status = "not_applicable"
        rationale = f"Client-side claude-code concern ({', '.join(excluded)}); not a gateway/relay relevance signal."
        cats = excluded
    elif high:
        # A genuine gateway signal (429 / thinking.type 400 / OAuth 401 …) survives a
        # co-occurring client marker — e.g. "VSCode: 429 overloaded on opus-4.8" is a
        # real relay signal that merely mentions the IDE. This asymmetry is the core
        # false-negative guard.
        impact = "needs_review"
        status = "candidate_unverified"
        rationale = "Gateway/relay-relevant keyword match; not yet manually verified against TokenKey code."
        cats = high
    elif medium:
        impact = "medium"
        status = "not_prioritized"
        rationale = "Possible gateway compatibility/streaming impact, but not a high-severity relay signal in this pass."
        cats = medium
    else:
        impact = "unknown_low_signal"
        status = "not_applicable"
        rationale = "No gateway/relay-relevant keywords; treated as not-our-surface."
        cats = []

    return {
        "upstream": upstream,
        "url": issue.get("html_url"),
        "title": title,
        "impact": impact,
        "categories": cats,
        "tokenkey_status": status,
        "rationale": rationale,
        "updated_at": issue.get("updated_at"),
    }


def main() -> int:
    if len(sys.argv) != 3:
        print("usage: cc-issue-triage-generate.py <input.jsonl> <output.json>", file=sys.stderr)
        return 2
    src = Path(sys.argv[1])
    dst = Path(sys.argv[2])
    entries = []
    # Split only on "\n" (the producer separator), NOT str.splitlines(): the latter
    # also breaks on Unicode line boundaries (U+2028/U+2029/U+0085), which
    # json.dumps(ensure_ascii=False) keeps raw inside string values — an issue
    # title/body containing U+2028 would otherwise split one valid JSON record
    # across "lines" and raise "Unterminated string".
    for line in src.read_text(encoding="utf-8").split("\n"):
        if not line.strip():
            continue
        entries.append(classify(json.loads(line)))

    priority = {
        "high": 0,
        "needs_review": 1,
        "needs_prod_validation": 2,
        "medium": 3,
        "fixed": 4,
        "not_applicable": 5,
        "low": 6,
        "unknown_low_signal": 7,
    }
    entries.sort(key=lambda e: (priority.get(e["impact"], 9), e["upstream"]))
    counts: dict[str, int] = {}
    for e in entries:
        counts[e["impact"]] = counts.get(e["impact"], 0) + 1
    data = {
        "version": 1,
        "source": "anthropics/claude-code open issues",
        "generated_from": "GitHub REST API issues?state=open; pull requests excluded; gateway-relevance filtered",
        "rationale": "TokenKey-local triage cache for anthropics/claude-code issues, filtered for gateway/relay "
                     "relevance. Most claude-code issues are client-side (IDE/terminal/MCP) and are marked "
                     "not_applicable. Only human-pinned MANUAL_TRIAGE high/critical entries become fix candidates.",
        "counts": counts,
        "issues": entries,
    }
    dst.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
