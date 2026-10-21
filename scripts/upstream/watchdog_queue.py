"""Durable, bounded review queues shared by issue and relay discovery.

Each source owns bounded, append-only issue pages. Entries never disappear when a source
closes an issue or an Actions cache expires. Human text outside the managed region
is preserved, and closing a queue is an operator decision, never auto-reopened.
"""
import html
import re
import subprocess
from datetime import datetime
from urllib.parse import urlencode

BEGIN = "<!-- issue-watchdog:begin -->"
END = "<!-- issue-watchdog:end -->"
LABEL = "watchdog-review"
PAGE_SIZE = 100


def managed_body(body, content):
    block = f"{BEGIN}\n{content.strip()}\n{END}"
    if BEGIN in body or END in body:
        if body.count(BEGIN) != 1 or body.count(END) != 1 or body.index(BEGIN) > body.index(END):
            raise ValueError("malformed watchdog region; refusing to overwrite issue body")
        before, remainder = body.split(BEGIN, 1)
        _, after = remainder.split(END, 1)
        return before + block + after
    return body.rstrip() + ("\n\n" if body.strip() else "") + block + "\n"


def safe_title(title):
    # Render upstream text as data: suppress HTML, links, mentions and newlines.
    title = html.escape(" ".join((title or "").split())[:80]).replace("@", "＠")
    return re.sub(r"([\\`*_\[\]@])", r"\\\1", title)


def sync_queue(repo, items, target, api):
    if not items:
        return {"created": 0, "updated": 0, "closed": 0}
    pages = api("api", "--paginate", "--slurp",
                f"repos/{target}/issues?{urlencode({'state': 'all', 'labels': LABEL, 'per_page': 100})}")
    existing = [row for page in pages for row in page if "pull_request" not in row]
    prefix = f"<!-- watchdog-queue:{repo}:"
    pages_by_id = {}
    owners = {}
    records_by_page = {}
    for row in existing:
        marker = re.search(re.escape(prefix) + r"(\d+) -->", row.get("body") or "")
        if not marker:
            continue
        page = int(marker.group(1))
        if page in pages_by_id:
            raise ValueError(f"duplicate review queues: {repo}:{page}")
        pages_by_id[page] = row
        region = (row.get("body") or "").split(BEGIN, 1)[-1].split(END, 1)[0]
        records = {int(number): line for number, line in re.findall(
            r"^<!-- candidate:(\d+) --> (.*)$", region, re.MULTILINE)}
        records_by_page[page] = records
        for number in records:
            if number in owners:
                raise ValueError(f"duplicate review candidate: {repo}#{number}")
            owners[number] = page
    counts = {"created": 0, "updated": 0, "closed": 0}
    groups = {}
    for item in sorted(items, key=lambda item: int(item["number"])):
        number = int(item["number"])
        page = owners.get(number)
        if page is not None and pages_by_id.get(page, {}).get("state") == "closed":
            counts["closed"] += 1
            continue
        if page is None:
            available = [p for p, records in records_by_page.items()
                         if pages_by_id.get(p, {}).get("state", "open") == "open"
                         and len(records) < PAGE_SIZE]
            page = max(available) if available else max(records_by_page, default=0) + 1
            records_by_page.setdefault(page, {})[number] = ""
            owners[number] = page
        groups.setdefault(page, []).append(item)
    labels_ready = False
    for page, entries in sorted(groups.items()):
        row = pages_by_id.get(page)
        marker = f"{prefix}{page} -->"
        previous_body = (row.get("body") or "") if row else ""
        records = records_by_page[page]
        for item in entries:
            number = int(item["number"])
            old_updated = re.search(r"; updated (\d{4}-[^; ]+)", records.get(number, ""))
            if old_updated and item.get("updated_at"):
                if datetime.fromisoformat(item["updated_at"]) < datetime.fromisoformat(old_updated.group(1)):
                    continue
            kind = item.get("kind", "issue")
            route = "pull" if kind == "pr" else "issues"
            state = "merged" if item.get("merged_at") else item.get("state", "open")
            detail = f"{kind}/{state}; updated {item.get('updated_at') or 'unknown'}"
            if item.get("merged_at"):
                detail += f"; merged {item['merged_at']}"
            records[number] = (f"- [{repo}#{number}](https://github.com/{repo}/{route}/{number}) "
                               f"{safe_title(item.get('title'))} — {detail}")
        content = "\n".join([
            marker,
            "待核实线索：上游讨论/合并不等于 TokenKey 受影响或已修复。",
            "逐项核对当前实现 → 复现/回归测试 → 确认影响后关联独立修复 Issue/PR。",
            "判断、复现证据和 TokenKey PR 链接写在自动区域之外；关闭本队列表示人工处理决定。",
            "历史条目持续保留；源头关闭不会自动删除。队列分页避免 GitHub 正文长度上限。",
            "", *[f"<!-- candidate:{n} --> {line}" for n, line in sorted(records.items(), reverse=True)]])
        body = managed_body(previous_body, content)
        if len(body.encode("utf-8")) > 60000:
            raise ValueError(f"review queue too large: {repo}:{page}; preserve checkpoint and split manually")
        if row:
            if body != previous_body:
                api("api", "--method", "PATCH", f"repos/{target}/issues/{row['number']}", payload={"body": body})
                counts["updated"] += 1
        else:
            if not labels_ready:
                labels = {label["name"] for page in api("api", "--paginate", "--slurp",
                    f"repos/{target}/labels?per_page=100") for label in page}
                if LABEL not in labels:
                    try:
                        api("api", "--method", "POST", f"repos/{target}/labels", payload={
                            "name": LABEL, "color": "BFD4F2", "description": "Upstream candidates awaiting TokenKey verification"})
                    except subprocess.CalledProcessError:
                        # The existing source matrix can create the shared label
                        # concurrently. Only a verified existing label is benign.
                        api("api", f"repos/{target}/labels/{LABEL}")
                labels_ready = True
            api("api", "--method", "POST", f"repos/{target}/issues", payload={
                "title": f"[watchdog-review] {repo} · {page}",
                "body": body, "labels": [LABEL]})
            counts["created"] += 1
    return counts
