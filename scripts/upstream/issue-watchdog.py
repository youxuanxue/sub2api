#!/usr/bin/env python3
from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import subprocess
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

RISK_ORDER = {
    "critical": 0,
    "high": 1,
    "needs_review": 2,
    "needs_prod_validation": 3,
    "medium": 4,
    "fixed": 5,
    "not_applicable": 6,
    "low": 7,
    "unknown_low_signal": 8,
}
UNRESOLVED_STATUSES = {
    "unresolved_in_tokenkey",
    "candidate_unverified",
    "needs_tokenkey_review",
}
FIXED_STATUS = "fixed_in_tokenkey"


def load_json(path: Path) -> dict[str, Any]:
    return json.loads(path.read_text(encoding="utf-8"))


def write_json(path: Path, data: dict[str, Any]) -> None:
    path.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def load_jsonl(path: Path) -> list[dict[str, Any]]:
    rows: list[dict[str, Any]] = []
    if not path.exists():
        return rows
    # Split only on "\n" (the producer separator), NOT str.splitlines(): the
    # latter also breaks on Unicode line boundaries (U+2028/U+2029/U+0085).
    # json.dumps(ensure_ascii=False) keeps those raw inside string values, so an
    # upstream issue title/body containing U+2028 would split one valid JSON
    # record across "lines" and raise JSONDecodeError: Unterminated string.
    for line in path.read_text(encoding="utf-8").split("\n"):
        if line.strip():
            rows.append(json.loads(line))
    return rows


def now_utc() -> str:
    return datetime.now(timezone.utc).replace(microsecond=0).isoformat().replace("+00:00", "Z")


def issue_number(upstream: str) -> int:
    match = re.search(r"#([0-9]+)$", upstream)
    if not match:
        raise ValueError(f"invalid upstream issue reference: {upstream}")
    return int(match.group(1))


def issue_url(upstream: str) -> str:
    # The repo is carried in the ref itself ("owner/name#NNN"), so this works for
    # any watched repo (Wei-Shaw/sub2api, anthropics/claude-code, …) with no flag.
    repo, _, number = upstream.rpartition("#")
    return f"https://github.com/{repo}/issues/{number}"


def issue_numbers_in_upstream(upstream: str) -> set[int]:
    """All `#NNNN` issue numbers referenced in an upstream field.

    Handles multi-issue fact-check rows such as
    `anthropics/claude-code#60168, #63885, anthropics/claude-code#64777`.
    """
    nums = [int(m) for m in re.findall(r"#([0-9]+)", upstream or "")]
    return set(nums)


def upstream_check_covers_entry(check_upstream: str, entry_upstream: str) -> bool:
    """True when a fact-check/fix `upstream` field covers a triage row."""
    if check_upstream == entry_upstream:
        return True
    try:
        entry_num = issue_number(entry_upstream)
    except ValueError:
        return False
    return entry_num in issue_numbers_in_upstream(check_upstream)


def sh(args: list[str], *, check: bool = True) -> subprocess.CompletedProcess[str]:
    return subprocess.run(args, text=True, capture_output=True, check=check)


def git_sha() -> str:
    try:
        return sh(["git", "rev-parse", "HEAD"]).stdout.strip()
    except (subprocess.CalledProcessError, OSError):
        return ""


def check_fact(spec: str) -> dict[str, Any]:
    if ":" not in spec:
        return {"spec": spec, "ok": False, "reason": "expected path:needle"}
    path_text, needle = spec.split(":", 1)
    path = Path(path_text)
    if not path.exists():
        return {"spec": spec, "ok": False, "path": path_text, "needle": needle, "reason": "path_missing"}
    text = path.read_text(encoding="utf-8", errors="replace")
    ok = needle in text
    return {
        "spec": spec,
        "ok": ok,
        "path": path_text,
        "needle": needle,
        "reason": "found" if ok else "needle_missing",
    }


def ensure_fixed_entry(fixes: dict[str, Any], check: dict[str, Any]) -> bool:
    upstream = check["upstream"]
    entry = {
        "upstream": upstream,
        "tokenkey_pr": check.get("tokenkey_pr"),
        "status": FIXED_STATUS,
        "fixed_by": check.get("fixed_by", []),
        "summary": check.get("summary", "Fixed in TokenKey."),
    }
    entry = {k: v for k, v in entry.items() if v not in (None, "", [])}

    issues = fixes.setdefault("issues", [])
    for idx, existing in enumerate(issues):
        if existing.get("upstream") == upstream:
            if existing != {**existing, **entry}:
                issues[idx] = {**existing, **entry}
                return True
            return False
    issues.append(entry)
    issues.sort(key=lambda item: issue_number(item["upstream"]))
    return True


def update_triage_fixed(triage: dict[str, Any], check: dict[str, Any]) -> bool:
    upstream = check["upstream"]
    changed = False
    for entry in triage.get("issues", []):
        if not upstream_check_covers_entry(upstream, entry.get("upstream", "")):
            continue
        if entry.get("impact") != "fixed":
            entry["impact"] = "fixed"
            changed = True
        if entry.get("tokenkey_status") != FIXED_STATUS:
            entry["tokenkey_status"] = FIXED_STATUS
            changed = True
        rationale = check.get("summary") or entry.get("rationale") or "Fixed in TokenKey."
        pr = check.get("tokenkey_pr")
        if pr and pr not in rationale:
            rationale = f"{rationale} Fixed by {pr}."
        if entry.get("rationale") != rationale:
            entry["rationale"] = rationale
            changed = True
    if changed:
        recalc_triage_counts(triage)
    return changed


def recalc_triage_counts(triage: dict[str, Any]) -> None:
    counts: dict[str, int] = {}
    for entry in triage.get("issues", []):
        impact = entry.get("impact", "unknown_low_signal")
        counts[impact] = counts.get(impact, 0) + 1
    triage["counts"] = counts
    triage["issues"].sort(key=lambda e: (RISK_ORDER.get(e.get("impact", ""), 99), e.get("upstream", "")))


def upstream_issue_map(rows: list[dict[str, Any]]) -> dict[int, dict[str, Any]]:
    out: dict[int, dict[str, Any]] = {}
    for row in rows:
        if "pull_request" in row:
            continue
        out[int(row["number"])] = row
    return out


def is_unresolved_high(entry: dict[str, Any], upstream_by_number: dict[int, dict[str, Any]], force_issue: str) -> bool:
    upstream = entry.get("upstream", "")
    try:
        num = issue_number(upstream)
    except ValueError:
        return False
    if force_issue and str(num) == force_issue:
        return entry.get("tokenkey_status") != FIXED_STATUS
    upstream_state = (upstream_by_number.get(num) or {}).get("state", "open")
    if upstream_state != "open":
        return False
    impact = entry.get("impact")
    status = entry.get("tokenkey_status")
    return impact in {"critical", "high"} and status in UNRESOLVED_STATUSES


def issue_signature(upstream: str) -> str:
    return hashlib.sha256(upstream.encode()).hexdigest()[:12]


def updated_desc_key(value: str) -> float:
    if not value:
        return 0
    try:
        return -datetime.fromisoformat(value.replace("Z", "+00:00")).timestamp()
    except ValueError:
        return 0


def agent_input(report: dict[str, Any]) -> dict[str, Any]:
    selected = report.get("selected_issue")
    if not selected:
        return {"schema_version": 1, "selected_issue": None, "anchors_present": []}
    return {
        "schema_version": 1,
        "selected_issue": {
            "upstream": selected.get("upstream"),
            "number": selected.get("number"),
            "url": selected.get("url"),
            "title": selected.get("title"),
            "impact": selected.get("impact"),
            "tokenkey_status": selected.get("tokenkey_status"),
            "rationale": selected.get("rationale"),
        },
        "anchors_present": [
            {
                "upstream": item.get("upstream"),
                "tokenkey_pr": item.get("tokenkey_pr"),
                "severity": item.get("severity"),
            }
            for item in report.get("anchors_present", [])
        ],
    }


def report_markdown(report: dict[str, Any], title: str = "# Issue Watchdog") -> str:
    pending = report.get("needs_review", [])
    high = report.get("high_unresolved", [])
    missing = report.get("fact_check_missing", [])
    lines = [title, "",
             f"待核实：{len(pending)} · 已判定高风险：{len(high)} · 锚点需复核：{len(missing)}", "",
             "关键词候选尚未核实；锚点存在不等于行为测试通过。", ""]
    for heading, items in [("待核实（最近更新优先）", pending), ("已判定高风险", high)]:
        if items:
            lines += [f"## {heading}", ""]
            for item in items[:20]:
                title_text = str(item.get("title") or item["upstream"]).replace("\n", " ")
                lines.append(f"- {item['upstream']} — {title_text} ({item['url']})")
            lines.append("")
    if missing:
        lines += ["## 修复证据需复核", ""]
        for item in missing:
            lines.append(f"- {item['upstream']}：锚点缺失，请核对重构或行为回归。")
        lines.append("")
    lines += ["完整队列与检查详情见本次运行工件中的 report.json。",
              f"扫描：{report.get('generated_at')} · main：`{report.get('repo_sha')}`",
              f"运行：{report.get('run_url') or 'local'}", ""]
    return "\n".join(lines)


def set_output(name: str, value: str) -> None:
    output = os.environ.get("GITHUB_OUTPUT")
    if output:
        with open(output, "a", encoding="utf-8") as f:
            f.write(f"{name}={value}\n")
    else:
        print(f"{name}={value}")


def build_report(upstream_rows: list[dict[str, Any]], triage: dict[str, Any],
                 fixes: dict[str, Any], fact_checks: list[dict[str, Any]],
                 force_issue: str = "") -> dict[str, Any]:
    upstream_by_num = upstream_issue_map(upstream_rows)

    anchors_present: list[dict[str, Any]] = []
    fact_check_missing: list[dict[str, Any]] = []

    for check in fact_checks:
        facts = [check_fact(spec) for spec in check.get("fixed_if_all_present", [])]
        if facts and all(fact.get("ok") for fact in facts):
            anchors_present.append({
                "upstream": check.get("upstream"),
                "tokenkey_pr": check.get("tokenkey_pr"),
                "severity": check.get("severity"),
                "facts": facts,
            })
            ensure_fixed_entry(fixes, check)
            update_triage_fixed(triage, check)
        else:
            fact_check_missing.append({
                "upstream": check.get("upstream"),
                "severity": check.get("severity"),
                "failed": [fact for fact in facts if not fact.get("ok")],
            })

    for entry in triage.get("issues", []):
        if any(upstream_check_covers_entry(item["upstream"], entry["upstream"])
               for item in fact_check_missing):
            entry.update(impact="needs_review", tokenkey_status="needs_tokenkey_review",
                         rationale="Recorded fix anchors are missing; verify refactor or regression.")
    recalc_triage_counts(triage)

    high_unresolved: list[dict[str, Any]] = []
    forced = None
    for entry in triage.get("issues", []):
        is_high = is_unresolved_high(entry, upstream_by_num, "")
        is_forced = bool(force_issue) and str(issue_number(entry["upstream"])) == force_issue
        if not is_high and not is_forced:
            continue
        num = issue_number(entry["upstream"])
        upstream_issue = upstream_by_num.get(num, {})
        item = {
            "upstream": entry["upstream"],
            "number": num,
            "url": entry.get("url") or issue_url(entry["upstream"]),
            "title": entry.get("title") or upstream_issue.get("title") or "",
            "impact": entry.get("impact"),
            "tokenkey_status": entry.get("tokenkey_status"),
            "rationale": entry.get("rationale", ""),
            "updated_at": upstream_issue.get("updated_at") or entry.get("updated_at") or "",
            "signature": issue_signature(entry["upstream"]),
        }
        if is_high:
            high_unresolved.append(item)
        if is_forced:
            forced = item

    high_unresolved.sort(key=lambda item: (RISK_ORDER.get(item.get("impact", ""), 99), updated_desc_key(item.get("updated_at", ""))))
    selected = forced if force_issue else (high_unresolved[0] if high_unresolved else None)

    report = {
        "schema_version": 1,
        "generated_at": now_utc(),
        "run_url": os.environ.get("GITHUB_SERVER_URL", "") + "/" + os.environ.get("GITHUB_REPOSITORY", "") + "/actions/runs/" + os.environ.get("GITHUB_RUN_ID", "") if os.environ.get("GITHUB_RUN_ID") else "",
        "repo_sha": git_sha(),
        "upstream_issue_count": len(upstream_by_num),
        "triage_issue_count": len(triage.get("issues", [])),
        "triage_counts": triage.get("counts", {}),
        "fact_check_count": len(fact_checks),
        "anchors_present": anchors_present,
        "fact_check_missing": fact_check_missing,
        "high_unresolved": high_unresolved,
        "needs_review": sorted(
            [entry for entry in triage.get("issues", []) if entry.get("impact") == "needs_review"],
            key=lambda entry: updated_desc_key(entry.get("updated_at", ""))),
        "selected_issue": selected,
    }
    return report


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--upstream-issues", required=True, type=Path)
    parser.add_argument("--triage", required=True, type=Path)
    parser.add_argument("--fixes", required=True, type=Path)
    parser.add_argument("--fact-checks", required=True, type=Path)
    parser.add_argument("--report-json", required=True, type=Path)
    parser.add_argument("--report-md", required=True, type=Path)
    parser.add_argument("--agent-input-json", type=Path)
    parser.add_argument("--force-upstream-issue", default="")
    # Watched-repo display title for the markdown report. The repo for issue URLs is
    # derived from each ref, so this is the only repo-specific knob — defaults keep
    # the upstream (Wei-Shaw) watchdog behavior unchanged.
    parser.add_argument("--report-title", default="# Upstream Issue Watchdog Report")
    args = parser.parse_args()

    triage = load_json(args.triage)
    fixes = load_json(args.fixes)
    report = build_report(load_jsonl(args.upstream_issues), triage, fixes,
                          load_json(args.fact_checks).get("checks", []), args.force_upstream_issue)
    write_json(args.triage, triage)
    write_json(args.fixes, fixes)
    selected = report["selected_issue"]
    high_unresolved = report["high_unresolved"]
    write_json(args.report_json, report)
    args.report_md.write_text(report_markdown(report, args.report_title), encoding="utf-8")
    if args.agent_input_json:
        write_json(args.agent_input_json, agent_input(report))

    set_output("has_high_unresolved", "true" if high_unresolved else "false")
    set_output("selected_issue", str(selected["number"]) if selected else "")
    set_output("selected_upstream", selected["upstream"] if selected else "")
    set_output("high_unresolved_count", str(len(high_unresolved)))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
