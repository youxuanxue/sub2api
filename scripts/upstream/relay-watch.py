#!/usr/bin/env python3
"""Incremental relay issue/PR discovery. Dry-run unless --sync-repo is explicit."""
from __future__ import annotations

import argparse
import json
import re
import subprocess
from datetime import datetime, timedelta, timezone
from pathlib import Path
from urllib.parse import urlencode
import importlib.util

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("watchdog_run", ROOT / "scripts/upstream/watchdog-run.py")
watchdog = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(watchdog)

topics = watchdog.queue.topics


def collect(repo, previous, started, lookback_days=30, api=watchdog.gh):
    if previous and (previous.get("version") != 1 or previous.get("repo") != repo):
        raise ValueError("relay checkpoint source/version mismatch")
    cursor = datetime.fromisoformat(previous["cursor"]) if previous else started - timedelta(days=lookback_days)
    since = (cursor - timedelta(minutes=5)).isoformat()
    params = {"state": "all", "sort": "updated", "direction": "desc", "per_page": 100, "since": since}
    pages = api("api", "--paginate", "--slurp", f"repos/{repo}/issues?{urlencode(params)}")
    # Issue comments have their own updated cursor: edited comments can contain the
    # first relevant evidence even when the issue title/body has no matching terms.
    comment_pages = api("api", "--paginate", "--slurp",
                        f"repos/{repo}/issues/comments?{urlencode({'since': since, 'per_page': 100})}")
    comments = {}
    for page in comment_pages:
        for comment in page:
            number = int(comment["issue_url"].rsplit("/", 1)[1])
            comments.setdefault(number, []).append(comment)
    rows = {int(row["number"]): row for page in pages for row in page}
    for number in comments.keys() - rows.keys():
        rows[number] = api("api", f"repos/{repo}/issues/{number}")
    findings = []
    for number, row in sorted(rows.items()):
        matched = topics(row, comments.get(number, []))
        if not matched:
            continue
        is_pr = "pull_request" in row
        # REST issues includes pull_request.merged_at. Use authoritative merge
        # metadata, never infer a merge from closed state. Older API responses
        # lacking that field fall back to PR detail without a search-result cap.
        pr = row.get("pull_request", {})
        if is_pr and "merged_at" not in pr:
            pr = api("api", f"repos/{repo}/pulls/{number}")
        updated = max([row["updated_at"], *[c["updated_at"] for c in comments.get(number, [])]])
        findings.append({"number": number, "title": row["title"], "kind": "pr" if is_pr else "issue",
                         "state": row["state"], "updated_at": updated, "topics": matched,
                         "url": f"https://github.com/{repo}/{'pull' if is_pr else 'issues'}/{number}",
                         "merged_at": pr.get("merged_at"), "merge_commit_sha": pr.get("merge_commit_sha"),
                         "discussion_urls": [c["html_url"] for c in comments.get(number, [])]})
    return {"version": 1, "repo": repo, "cursor": started.isoformat()}, findings


def scan_source(repo, work, checkpoint, target="", lookback_days=30, api=watchdog.gh):
    previous = json.loads(checkpoint.read_text()) if checkpoint.exists() else None
    state, findings = collect(repo, previous, datetime.now(timezone.utc), lookback_days, api)
    work.mkdir(parents=True, exist_ok=True)
    report = {"repo": repo, "scanned_at": state["cursor"], "findings": findings,
              "status": "candidates_unverified", "sync": None}
    (work / "report.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    if target:
        report["sync"] = watchdog.queue.sync_queue(repo, findings, target, api, resolved=watchdog.resolved_refs())
        (work / "report.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
        checkpoint.parent.mkdir(parents=True, exist_ok=True)
        temporary = checkpoint.with_suffix(".tmp")
        temporary.write_text(json.dumps(state) + "\n")
        temporary.replace(checkpoint)
    # A read-only run cannot acknowledge findings that were never published.
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manifest", type=Path, default=ROOT / "ops/issue-watchdog/relays.json")
    parser.add_argument("--repo", default="", help="Select one repository from the manifest")
    parser.add_argument("--sync-repo", default="")
    parser.add_argument("--work", type=Path, default=Path(".cache/relay-watch"))
    parser.add_argument("--checkpoint-dir", type=Path, default=Path(".cache/relay-watch-state"))
    parser.add_argument("--lookback-days", type=int, default=30)
    args = parser.parse_args()
    sources = json.loads(args.manifest.read_text())["sources"]
    if args.repo:
        sources = [source for source in sources if source["repo"] == args.repo]
        if not sources:
            parser.error("--repo must be listed in the manifest")
    if not 1 <= args.lookback_days <= 365:
        parser.error("--lookback-days must be between 1 and 365")
    failures = []
    for source in sources:
        repo = source["repo"]
        if not re.fullmatch(r"[\w.-]+/[\w.-]+", repo):
            raise ValueError("invalid repository in manifest")
        key = repo.replace("/", "--")
        try:
            report = scan_source(repo, args.work / key, args.checkpoint_dir / f"{key}.json",
                                 args.sync_repo, args.lookback_days)
            print(f"{repo}: candidates={len(report['findings'])}; sync={report['sync']}")
        except Exception as error:
            failures.append(repo)
            args.work.mkdir(parents=True, exist_ok=True)
            detail = str(error)
            if isinstance(error, subprocess.CalledProcessError):
                detail += "\n" + (error.stderr or "")
            (args.work / f"{key}-error.txt").write_text(detail + "\n")
            print(f"::error::{repo}: scan/sync failed; checkpoint preserved")
    if failures:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
