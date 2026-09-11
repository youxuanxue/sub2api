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
    int(entry["upstream"].rsplit("#", 1)[1]): tuple(entry["judgment"][key]
        for key in ("impact", "tokenkey_status", "rationale"))
    for entry in json.loads((Path(__file__).resolve().parents[2] / "ops/issue-watchdog/upstream.json").read_text(encoding="utf-8"))["entries"]
    if "judgment" in entry
}


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
    # Split only on "\n" (the producer separator), NOT str.splitlines(): the
    # latter also breaks on Unicode line boundaries (U+2028/U+2029/U+0085),
    # which json.dumps(ensure_ascii=False) keeps raw inside string values — an
    # upstream issue title/body containing U+2028 would otherwise split one
    # valid JSON record across "lines" and raise "Unterminated string".
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
    data = {
        "version": 1,
        "source": "Wei-Shaw/sub2api open issues",
        "generated_from": "GitHub REST API issues?state=open; pull requests excluded",
        "rationale": "TokenKey-local triage cache for upstream open issues. Generated report only; curated judgments live in ops/issue-watchdog/upstream.json.",
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
