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
    for line in path.read_text(encoding="utf-8").splitlines():
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
    number = issue_number(upstream)
    return f"https://github.com/Wei-Shaw/sub2api/issues/{number}"


def sh(args: list[str], *, check: bool = True) -> subprocess.CompletedProcess[str]:
    return subprocess.run(args, text=True, capture_output=True, check=check)


def git_sha() -> str:
    try:
        return sh(["git", "rev-parse", "HEAD"]).stdout.strip()
    except Exception:
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
        if entry.get("upstream") != upstream:
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
        try:
            out[int(row["number"])] = row
        except Exception:
            continue
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
    if impact in {"critical", "high"} and status in UNRESOLVED_STATUSES:
        return True
    return False


def issue_signature(upstream: str) -> str:
    return hashlib.sha256(upstream.encode()).hexdigest()[:12]


def updated_desc_key(value: str) -> float:
    if not value:
        return 0
    try:
        return -datetime.fromisoformat(value.replace("Z", "+00:00")).timestamp()
    except ValueError:
        return 0


def report_markdown(report: dict[str, Any]) -> str:
    lines = [
        "# Upstream Issue Watchdog Report",
        "",
        f"- Run URL: {report.get('run_url') or 'n/a'}",
        f"- Repository SHA: `{report.get('repo_sha') or 'unknown'}`",
        f"- Generated at: `{report.get('generated_at')}`",
        f"- Upstream issues scanned: `{report.get('upstream_issue_count')}`",
        f"- Fact checks: `{report.get('fact_check_count')}`",
        f"- Fixed facts verified: `{len(report.get('fixed_verified', []))}`",
        f"- High/critical unresolved: `{len(report.get('high_unresolved', []))}`",
        "",
    ]
    selected = report.get("selected_issue")
    if selected:
        lines += [
            "## Selected issue for fix PR",
            "",
            f"- {selected.get('upstream')}: {selected.get('title')}",
            f"- Impact: `{selected.get('impact')}`",
            f"- TokenKey status: `{selected.get('tokenkey_status')}`",
            f"- URL: {selected.get('url')}",
            "",
        ]
    high = report.get("high_unresolved", [])
    if high:
        lines += ["## High/Critical unresolved", ""]
        for item in high:
            lines.append(f"- `{item.get('impact')}` {item.get('upstream')} — {item.get('title')} ({item.get('url')})")
        lines.append("")
    fixed = report.get("fixed_verified", [])
    if fixed:
        lines += ["## Fixed facts verified", ""]
        for item in fixed:
            lines.append(f"- {item.get('upstream')} via {item.get('tokenkey_pr', 'n/a')}")
        lines.append("")
    missing = report.get("fact_check_missing", [])
    if missing:
        lines += ["## Fact checks missing", ""]
        for item in missing:
            failed = ", ".join(f.get("spec", "") for f in item.get("failed", []))
            lines.append(f"- {item.get('upstream')}: {failed}")
        lines.append("")
    return "\n".join(lines) + "\n"


def set_output(name: str, value: str) -> None:
    output = os.environ.get("GITHUB_OUTPUT")
    if output:
        with open(output, "a", encoding="utf-8") as f:
            f.write(f"{name}={value}\n")
    else:
        print(f"{name}={value}")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--upstream-issues", required=True, type=Path)
    parser.add_argument("--triage", required=True, type=Path)
    parser.add_argument("--fixes", required=True, type=Path)
    parser.add_argument("--fact-checks", required=True, type=Path)
    parser.add_argument("--report-json", required=True, type=Path)
    parser.add_argument("--report-md", required=True, type=Path)
    parser.add_argument("--force-upstream-issue", default="")
    args = parser.parse_args()

    upstream_rows = load_jsonl(args.upstream_issues)
    upstream_by_num = upstream_issue_map(upstream_rows)
    triage = load_json(args.triage)
    fixes = load_json(args.fixes)
    fact_checks = load_json(args.fact_checks).get("checks", [])

    fixed_verified: list[dict[str, Any]] = []
    fact_check_missing: list[dict[str, Any]] = []
    changed = False

    for check in fact_checks:
        facts = [check_fact(spec) for spec in check.get("fixed_if_all_present", [])]
        if all(fact.get("ok") for fact in facts):
            fixed_verified.append({
                "upstream": check.get("upstream"),
                "tokenkey_pr": check.get("tokenkey_pr"),
                "severity": check.get("severity"),
                "facts": facts,
            })
            changed = ensure_fixed_entry(fixes, check) or changed
            changed = update_triage_fixed(triage, check) or changed
        else:
            fact_check_missing.append({
                "upstream": check.get("upstream"),
                "severity": check.get("severity"),
                "failed": [fact for fact in facts if not fact.get("ok")],
            })

    if changed:
        write_json(args.triage, triage)
        write_json(args.fixes, fixes)

    high_unresolved: list[dict[str, Any]] = []
    for entry in triage.get("issues", []):
        if not is_unresolved_high(entry, upstream_by_num, args.force_upstream_issue):
            continue
        num = issue_number(entry["upstream"])
        upstream_issue = upstream_by_num.get(num, {})
        high_unresolved.append({
            "upstream": entry["upstream"],
            "number": num,
            "url": entry.get("url") or issue_url(entry["upstream"]),
            "title": entry.get("title") or upstream_issue.get("title") or "",
            "impact": entry.get("impact"),
            "tokenkey_status": entry.get("tokenkey_status"),
            "rationale": entry.get("rationale", ""),
            "updated_at": upstream_issue.get("updated_at") or entry.get("updated_at") or "",
            "signature": issue_signature(entry["upstream"]),
        })

    high_unresolved.sort(key=lambda item: (RISK_ORDER.get(item.get("impact", ""), 99), updated_desc_key(item.get("updated_at", ""))))
    selected = high_unresolved[0] if high_unresolved else None

    report = {
        "schema_version": 1,
        "generated_at": now_utc(),
        "run_url": os.environ.get("GITHUB_SERVER_URL", "") + "/" + os.environ.get("GITHUB_REPOSITORY", "") + "/actions/runs/" + os.environ.get("GITHUB_RUN_ID", "") if os.environ.get("GITHUB_RUN_ID") else "",
        "repo_sha": git_sha(),
        "upstream_issue_count": len(upstream_by_num),
        "triage_issue_count": len(triage.get("issues", [])),
        "triage_counts": triage.get("counts", {}),
        "fact_check_count": len(fact_checks),
        "fixed_verified": fixed_verified,
        "fact_check_missing": fact_check_missing,
        "high_unresolved": high_unresolved,
        "selected_issue": selected,
    }
    write_json(args.report_json, report)
    args.report_md.write_text(report_markdown(report), encoding="utf-8")

    set_output("has_high_unresolved", "true" if high_unresolved else "false")
    set_output("selected_issue", str(selected["number"]) if selected else "")
    set_output("selected_upstream", selected["upstream"] if selected else "")
    set_output("high_unresolved_count", str(len(high_unresolved)))
    set_output("cache_changed", "true" if changed else "false")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
