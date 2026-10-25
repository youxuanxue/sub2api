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


PLATFORM_TERMS = {
    "kiro": r"\bkiro\b|codewhisperer|amazon.?q",
    "openai": r"\bopenai\b|\bcodex\b|chatgpt|\bgpt[- .]?\d",
    "google": r"\bgoogle\b|\bgemini\b|\bantigravity\b|cloudcode|generativelanguage",
}


def platform_hints(row, comments=()):
    content = " ".join([row.get("title") or "", row.get("body") or "",
                        *[label["name"] for label in row.get("labels", [])],
                        *[comment.get("body") or "" for comment in comments]])
    return [platform for platform, pattern in PLATFORM_TERMS.items()
            if re.search(pattern, content, re.I)]


def platform_findings(findings, platforms):
    """Single-provider repos belong to that provider even for cross-protocol work.

    Multi-provider repos use explicit platform hints. Unspecified shared account,
    scheduling or billing fixes go to every supported platform so none are lost.
    """
    if not platforms or len(set(platforms)) != len(platforms):
        raise ValueError("source needs unique tracking platforms")
    for platform in platforms:
        watchdog.queue.platform_marker(platform)
    grouped = {platform: [] for platform in platforms}
    for item in findings:
        matched = [platform for platform in platforms if platform in item.get("platform_hints", [])]
        for platform in matched or platforms:
            grouped[platform].append(item)
    return grouped


def collect(repo, previous, started, lookback_days=30, api=watchdog.gh):
    if previous and (previous.get("version") != 1 or previous.get("repo") != repo):
        raise ValueError("relay checkpoint source/version mismatch")
    cursor = datetime.fromisoformat(previous["cursor"]) if previous else started - timedelta(days=lookback_days)
    since = (cursor - timedelta(minutes=5)).isoformat()
    params = {"state": "all", "sort": "updated", "direction": "desc", "per_page": 100, "since": since}
    pages = api("api", "--paginate", "--slurp", f"repos/{repo}/issues?{urlencode(params)}")
    # No full-backlog bootstrap: mirroring every open upstream issue once turned the
    # tracking Issue into a thousands-strong mirror nobody reads. The lookback window
    # is the only entry point, so the queue only ever holds recent, real leads.
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
    context = dict((previous or {}).get("context", {}))
    findings = []
    for number, row in sorted(rows.items()):
        prior = context.get(str(number), {})
        matched = sorted(set(prior.get("topics", []))
                         | set(topics(row, comments.get(number, []), source_repo=repo)))
        hints = sorted(set(prior.get("platform_hints", [])) | set(platform_hints(row, comments.get(number, []))))
        if not matched:
            continue
        is_pr = "pull_request" in row
        # REST issues includes pull_request.merged_at. Use authoritative merge
        # metadata, never infer a merge from closed state. Older API responses
        # lacking that field fall back to PR detail without a search-result cap.
        pr = row.get("pull_request", {})
        if is_pr and "merged_at" not in pr:
            pr = api("api", f"repos/{repo}/pulls/{number}")
        context[str(number)] = {"topics": matched, "platform_hints": hints}
        updated = max([row["updated_at"], *[c["updated_at"] for c in comments.get(number, [])]])
        findings.append({"number": number, "title": row["title"], "kind": "pr" if is_pr else "issue",
                         "state": row["state"], "updated_at": updated, "topics": matched,
                         "url": f"https://github.com/{repo}/{'pull' if is_pr else 'issues'}/{number}",
                         "merged_at": pr.get("merged_at"), "merge_commit_sha": pr.get("merge_commit_sha"),
                         "platform_hints": hints,
                         "discussion_urls": [c["html_url"] for c in comments.get(number, [])]})
    return {"version": 1, "repo": repo, "cursor": started.isoformat(), "context": context}, findings


def scan_source(repo, work, checkpoint, target="", lookback_days=30, api=watchdog.gh, platforms=("upstream",)):
    previous = json.loads(checkpoint.read_text()) if checkpoint.exists() else None
    # Changing platform assignment needs a replay; the former cursor only
    # acknowledges delivery to the previous destination set.
    if previous and (previous.get("platforms") != list(platforms) or "context" not in previous):
        previous = None
    state, findings = collect(repo, previous, datetime.now(timezone.utc), lookback_days, api)
    state["platforms"] = list(platforms)
    work.mkdir(parents=True, exist_ok=True)
    grouped = platform_findings(findings, platforms)
    # An upstream merge is a parity lead, not a resolution: it is exactly where
    # TokenKey may be missing that behavior. It stays in the queue AND is listed
    # here so a reviewer can check our side without reading the whole report.
    upstream_fixes = [item for item in findings if item.get("merged_at")]
    report = {"repo": repo, "platforms": list(platforms), "scanned_at": state["cursor"], "findings": findings,
              "upstream_fixes": upstream_fixes,
              "status": "candidates_unverified", "sync": None}
    (work / "report.json").write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    if target:
        sync = {}
        resolved = watchdog.resolved_refs()
        # Delivery state is keyed per platform: the same ref can be queued in two
        # platform Issues and each has its own published signature.
        delivered = dict((previous or {}).get("published", {}))
        for platform, items in grouped.items():
            sync[platform] = watchdog.queue.sync_queue(
                repo, items, target, api, resolved=resolved, platform=platform,
                published=delivered.get(platform, {}))
            # Keep any prior map when a result carries none, so a caller that does
            # not report delivery state cannot silently erase it.
            if "published" in sync[platform]:
                delivered[platform] = sync[platform].pop("published")
        state["published"] = delivered
        report["sync"] = sync
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
        platforms = tuple(source["platforms"])
        if not re.fullmatch(r"[\w.-]+/[\w.-]+", repo):
            raise ValueError("invalid repository in manifest")
        key = repo.replace("/", "--")
        try:
            report = scan_source(repo, args.work / key, args.checkpoint_dir / f"{key}.json",
                                 args.sync_repo, args.lookback_days, platforms=platforms)
            print(f"{repo}: candidates={len(report['findings'])}; "
                  f"upstream_fixes={len(report['upstream_fixes'])}; sync={report['sync']}")
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
