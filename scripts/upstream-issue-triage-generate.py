#!/usr/bin/env python3
from __future__ import annotations

import json
import re
import sys
from pathlib import Path

HIGH_PATTERNS = [
    ("claude_mimicry", ["claude code", "x-stainless", "user-agent", "authorized for use with claude code", "第三方", "风控"]),
    ("rate_limit_cooldown", ["429", "cooldown", "冷却", "限流", "503", "pst", "midnight"]),
    ("billing_usage", ["计费", "多收", "usage", "usage_logs", "input_tokens=0", "重复记录"]),
    ("image_oauth", ["images", "图片", "生图", "gpt-image", "context canceled", "502", "oauth"]),
    ("account_pool_health", ["测试账号", "调度池", "stream disconnected", "提前 eof", "response.completed"]),
    ("security", ["泄露", "secret", "token", "越权", "漏洞", "安全"]),
]

MEDIUM_PATTERNS = [
    ("ops_observability", ["运维", "监控", "归因", "日志", "sla", "request_id"]),
    ("compatibility", ["兼容", "compact", "透传", "header"]),
    ("admin_ui", ["页面", "展示", "统计", "按钮", "前端"]),
]

LOW_PATTERNS = [
    ("enhancement", ["是否可以", "希望", "建议", "feature", "优化"]),
    ("docs", ["文档", "readme", "教程"]),
]

MANUAL_TRIAGE = {
    580: ("fixed", "fixed_in_tokenkey", "Claude Code mimicry UA downgrade fixed by TokenKey PR #223."),
    641: ("fixed", "fixed_in_tokenkey", "Gemini 429 with no quotaResetDelay/retryDelay now uses tier cooldown for all Gemini OAuth accounts (google_one + aistudio OAuth + code_assist), not just Code Assist; PST-midnight fallback is reserved for API Key accounts. Fixed in TokenKey gemini_messages_compat_service.go handleGeminiUpstreamError."),
    1824: ("needs_prod_validation", "partially_mitigated", "OpenAI 403 now starts with temporary unschedulable state, but repeated CF/Arkose challenges can still cool down or error accounts."),
    1925: ("fixed", "fixed_in_tokenkey", "OpenAI /responses account test requires response.completed/response.done; early EOF fails tests."),
    2055: ("fixed", "fixed_in_tokenkey", "Rate-limit reset clears account/model rate-limit, temp-unschedulable, and OpenAI 403 counters."),
    2107: ("fixed", "fixed_in_tokenkey", "Channel interval pricing now applies flat channel defaults as the out-of-range fallback; fixed by TokenKey PR #232."),
    2159: ("fixed", "fixed_in_tokenkey", "Disabled proxies are filtered out of account preload and WithProxy edge loads; fixed by TokenKey PR #232."),
    2168: ("fixed", "fixed_in_tokenkey", "Streaming billing uses detached/drain paths and tests cover client-disconnect billing."),
    2211: ("fixed", "fixed_in_tokenkey", "Account scheduling now queries the account_groups join table for the requested group; balance-group requests should not schedule ungrouped accounts."),
    2232: ("needs_prod_validation", "needs_tokenkey_review", "OpenAI OAuth images/edits gpt-image-2 remains a user-facing image path; validate against current image/edit bridge behavior."),
    2245: ("needs_prod_validation", "known_protocol_limitation", "Responses stream terminal-event detection exists, but downstream may already have received HTTP 200 before an incomplete SSE is detected."),
    2258: ("fixed", "fixed_in_tokenkey", "OpenAI 429 burst below usage-window limits now falls through to short fallback cooldown instead of multi-hour/day reset headers; fixed by TokenKey PR #232."),
    2291: ("medium", "unresolved_observability", "Cache hit rate has inconsistent UI formulas between dashboard/trend views; observability issue, not direct production outage."),
    2293: ("fixed", "fixed_in_tokenkey", "Long-context billing includes cache_read tokens in the threshold and applies long-context multiplier to cache_read cost."),
    2310: ("fixed", "fixed_in_tokenkey", "OpenAI OAuth image generation uses a detached upstream context instead of binding long jobs to the client connection."),
    2332: ("fixed", "fixed_in_tokenkey", "TokenKey writes exactly one usage_logs row per request (single RecordUsage in worker pool, see gateway_handler.go), and parseSSEUsagePassthrough ignores zero-value input_tokens from message_delta. No duplicate input=0/output>0 rows. Test TestGatewayService_ParseSSEUsagePassthrough_MessageDeltaSelectiveOverwrite pins this."),
    2337: ("fixed", "fixed_in_tokenkey", "Anthropic-to-OpenAI multi-turn tool call mapping is covered by tool-continuation code and tests."),
    2363: ("needs_prod_validation", "partially_mitigated", "Cache-read price overrides exist in resolver and billing paths; verify production channel-pricing source selection."),
    2383: ("fixed", "fixed_in_tokenkey", "OpenAI usage recording has billing model candidates/source tracking and tests for mapped/unmapped models."),
    2410: ("medium", "needs_tokenkey_review", "Ops attribution gaps reduce incident actionability; TokenKey has added ops context but needs separate production log audit."),
    2411: ("fixed", "fixed_in_tokenkey", "Sticky-session scheduling has dedicated hash, mode, and invalidation logic."),
    2413: ("needs_prod_validation", "partially_mitigated", "OpenAI image 403 still triggers temporary unschedulable cooldown; decide whether CF/Arkose image challenges should be ignored or scoped."),
    2453: ("medium", "intentional_policy", "Default OpenAI fast policy still filters priority/fast globally by design; important product-policy tradeoff, not an accidental outage bug."),
    2465: ("not_applicable", "outside_tokenkey_runtime", "Issue is about upstream compat-proxy/proxy.py; this TokenKey worktree has no compat-proxy implementation to patch."),
    2478: ("fixed", "fixed_in_tokenkey", "Expired or non-active subscriptions no longer satisfy ExistsByUserIDAndGroupID for reassignment conflicts; fixed by TokenKey PR #232."),
    2486: ("needs_prod_validation", "needs_tokenkey_review", "Gemini pro-agent billing appears to flow through RecordUsageWithLongContext, but no gemini-pro-agent-specific test was found."),
    2487: ("fixed", "fixed_in_tokenkey", "Codex OAuth transform strips temperature and other unsupported fields before forwarding to ChatGPT internal endpoints."),
    2489: ("fixed", "fixed_in_tokenkey", "Claude Code mimicry helper-method header risk fixed by TokenKey PR #223."),
    2490: ("fixed", "fixed_in_tokenkey", "OpenAI /compact route and request-body normalization are implemented, including compact_not_supported errors when no account is available."),
    1311: ("fixed", "fixed_in_tokenkey", "Non-stream /v1/chat/completions and /v1/messages now explicitly set Content-Type: application/json after WriteFilteredHeaders, so the upstream Responses SSE Content-Type no longer leaks onto JSON bodies."),
    2500: ("fixed", "fixed_in_tokenkey", "Codex OAuth fixCallIDPrefix now emits fc_<id> (with underscore) for call_<id> inputs, matching the codex backend's id validator and preventing 502 on multi-hop tool turns."),
    2506: ("fixed", "fixed_in_tokenkey", "normalizeClaudeOAuthRequestBody skips context_management auto-injection for Haiku models (mirroring the existing Haiku exemption in FullClaudeCodeMimicryBetas), so claude-haiku-4-5-* + thinking.type=enabled no longer triggers Anthropic 400."),
    2515: ("fixed", "fixed_in_tokenkey", "ChatCompletions→Responses transform no longer emits content:null when the source content array is empty or every part was filtered out — falls back to empty string per upstream Responses contract."),
    1471: ("fixed", "fixed_in_tokenkey", "OpenAI /v1/responses sendErrorEvent prepends a blank line before the synthetic error event so an in-flight upstream SSE event (data: line without terminating blank line) does not merge with the injected error event into a single event carrying two JSON objects; downstream SDK JSON parsing no longer fails."),
    2538: ("fixed", "fixed_in_tokenkey", "SchedulerRateLimitReaper closes the deadlock where an account's 429 cooldown expires but no event triggers a scheduler snapshot rebuild; reaper ticks every 5s, atomically enqueues outbox account_changed events for accounts whose rate_limit_reset_at just elapsed, and the existing outbox worker rebuilds the bucket. Reaper is fully independent of upstream SchedulerSnapshotService and degrades to a no-op when disabled via config."),
}

FIXED_IDS = {num for num, (impact, _, _) in MANUAL_TRIAGE.items() if impact == "fixed"}


def norm(s: str) -> str:
    return re.sub(r"\s+", " ", s or "").strip()


def matches(text: str, groups: list[tuple[str, list[str]]]) -> list[str]:
    out = []
    lower = text.lower()
    for name, pats in groups:
        if any(p.lower() in lower for p in pats):
            out.append(name)
    return out


def classify(issue: dict) -> dict:
    num = int(issue["number"])
    title = norm(issue.get("title", ""))
    body = norm(issue.get("body", ""))
    text = f"{title} {body}"
    manual = MANUAL_TRIAGE.get(num)
    if manual:
        impact, status, note = manual
        cats = matches(text, HIGH_PATTERNS + MEDIUM_PATTERNS + LOW_PATTERNS)
        return {
            "upstream": f"Wei-Shaw/sub2api#{num}",
            "url": issue.get("html_url"),
            "title": title,
            "impact": impact,
            "categories": cats or ["manual"],
            "tokenkey_status": status,
            "rationale": note,
            "updated_at": issue.get("updated_at"),
        }

    high = matches(text, HIGH_PATTERNS)
    medium = matches(text, MEDIUM_PATTERNS)
    low = matches(text, LOW_PATTERNS)
    if high:
        impact = "needs_review"
        status = "candidate_unverified"
        rationale = "Keyword match for production-sensitive area; not yet manually verified against TokenKey code."
        cats = high
    elif medium:
        impact = "medium"
        status = "not_prioritized"
        rationale = "Potential compatibility/ops/admin impact, but not selected as severe TokenKey production risk in this pass."
        cats = medium
    elif low:
        impact = "low"
        status = "not_prioritized"
        rationale = "Appears to be enhancement/docs/UI request rather than severe online service risk."
        cats = low
    else:
        impact = "unknown_low_signal"
        status = "not_prioritized"
        rationale = "No high-signal production-risk keywords in this pass."
        cats = []
    return {
        "upstream": f"Wei-Shaw/sub2api#{num}",
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
        print("usage: upstream_issue_triage_generate.py <input.jsonl> <output.json>", file=sys.stderr)
        return 2
    src = Path(sys.argv[1])
    dst = Path(sys.argv[2])
    entries = []
    for line in src.read_text(encoding="utf-8").splitlines():
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
    data = {
        "version": 1,
        "source": "Wei-Shaw/sub2api open issues",
        "generated_from": "GitHub REST API issues?state=open; pull requests excluded",
        "rationale": "TokenKey-local triage cache for upstream open issues. Use this file before re-triaging upstream issues; update entries when TokenKey fixes, dismisses, or reclassifies an issue.",
        "counts": {},
        "issues": entries,
    }
    counts = {}
    for e in entries:
        counts[e["impact"]] = counts.get(e["impact"], 0) + 1
    data["counts"] = counts
    dst.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
