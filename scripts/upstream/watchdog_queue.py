"""One upstream tracking Issue; daily comment deltas are durable publication state."""
import hashlib
import html
import json
import re
from datetime import datetime, timezone
from urllib.parse import urlencode

BEGIN = "<!-- issue-watchdog:begin -->"
END = "<!-- issue-watchdog:end -->"
LABEL = "upstream-tracking"
MARKER = "<!-- watchdog-tracking:v1 -->"
RECORD = re.compile(r"^- \[([ xX])\] (.*?) <!-- watchdog-item:([^ ]+) ([0-9a-f]+) (pending|fixed) ([^ ]+) -->$", re.MULTILINE)
COMMENT_LIMIT = 55000

TOPICS = {
    "scheduling": r"gateway|schedul|routing|route|failover|load.?balanc|concurren|capacity|saturation|retry|retries|timeout|网关|调度|路由|故障转移|负载均衡|并发|重试|超时",
    "billing": r"billing|metering|usage|quota|credit|balance|payment|refund|计费|用量|额度|配额|余额|支付|充值|扣费|退款",
    "account": r"oauth|credential|refresh.?token|access.?token|account|authentication|authorization|\bauth\b|401|403|429|rate.?limit|cooldown|账号|账户|凭证|鉴权|封号|限流|冷却",
    "conversion": r"convert|translat|(?<![a-z])stream|eventstream|\bsse\b|websocket|tool.?result|tool.?use|function.?call|thinking|reasoning|response\.|image|转换|工具调用|图片|拒答|截断",
}


def topics(row, comments=()):
    content = " ".join([row.get("title") or "", row.get("body") or "",
                        *[(comment.get("body") or "") for comment in comments]])
    return [name for name, pattern in TOPICS.items() if re.search(pattern, content, re.I)]


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
    value = html.escape(" ".join((title or "").split())[:120]).replace("@", "＠")
    return re.sub(r"([\\`*_\[\]])", r"\\\1", value)


def sync_queue(repo, items, target, api, resolved=(), day=None):
    """Publish relevant findings immediately, regardless of risk/manual judgment.

    A checked task is an explicit TokenKey resolution. Ledger resolutions also
    check every historical task for that ref. Absence from an incremental scan
    or upstream closure is never treated as a TokenKey fix.
    """
    day = day or datetime.now(timezone.utc).date().isoformat()
    resolved = set(resolved)
    pages = api("api", "--paginate", "--slurp",
                f"repos/{target}/issues?{urlencode({'state': 'all', 'labels': LABEL, 'per_page': 100})}")
    matches = [row for page in pages for row in page
               if "pull_request" not in row and MARKER in (row.get("body") or "")]
    if len(matches) > 1:
        raise ValueError("duplicate upstream tracking Issues")
    issue = matches[0] if matches else None
    items = [{**item, "upstream": item.get("upstream") or f"{repo}#{item['number']}"} for item in items]
    items = [item for item in items if item["upstream"] not in resolved]
    if not issue and not items:
        return {"created": 0, "commented": 0, "pending": 0}
    created = 0
    if not issue:
        labels = {row["name"] for page in api("api", "--paginate", "--slurp",
                  f"repos/{target}/labels?per_page=100") for row in page}
        if LABEL not in labels:
            api("api", "--method", "POST", f"repos/{target}/labels", payload={
                "name": LABEL, "color": "BFD4F2", "description": "Daily upstream gateway, billing and account tracking"})
        issue = api("api", "--method", "POST", f"repos/{target}/issues", payload={
            "title": "[upstream] 网关调度、计费与上游账号问题跟踪",
            "body": MARKER + "\n" + managed_body("", "每日追加新增及更新评论；所有来源共用此 Issue。\n"
                "修复后勾选对应任务或更新修复账本；已完成条目不再进入待处理集合。\n"
                "上游关闭或 PR 合并不代表 TokenKey 已解决。"), "labels": [LABEL]})
        created = 1
    endpoint = f"repos/{target}/issues/{issue['number']}"
    comments = [row for page in api("api", "--paginate", "--slurp", endpoint + "/comments?per_page=100") for row in page]
    # Only our marked daily comments contain machine-owned task lines.
    owned = [row for row in comments if re.search(r"<!-- watchdog-day:\d{4}-\d{2}-\d{2}:\d+ -->", row.get("body") or "")]
    latest, manual = {}, set()
    for comment in owned:
        for checked, text, ref, signature, status, updated in RECORD.findall(comment.get("body") or ""):
            latest[ref] = (signature, status, checked, updated)
            if checked.lower() == "x" and status == "pending":
                manual.add(ref)
    fixed = resolved | manual
    # Update task state before appending anything; retries remain idempotent.
    for comment in owned:
        def finish(match):
            if match[3] not in fixed:
                return match[0]
            status = "fixed" if match[3] in resolved else "pending"
            return f"- [x] {match[2]} <!-- watchdog-item:{match[3]} {match[4]} {status} {match[6]} -->"
        body = RECORD.sub(finish, comment.get("body") or "")
        if body != comment.get("body"):
            api("api", "--method", "PATCH", f"repos/{target}/issues/comments/{comment['id']}", payload={"body": body})
            comment["body"] = body
    additions = []
    for item in sorted(items, key=lambda item: item["upstream"]):
        ref = item["upstream"]
        if ref in fixed:
            continue
        signature = hashlib.sha256(json.dumps({k: item.get(k) for k in
            ("title", "updated_at", "state", "merged_at")}, sort_keys=True).encode()).hexdigest()[:16]
        old = latest.get(ref)
        updated = item.get("updated_at") or "unknown"
        # The main scanner and relay discussion scanner overlap.  A stale
        # snapshot must not overwrite newer discussion/merge evidence.
        if old and old[1] != "fixed":
            if old[0] == signature:
                continue
            if old[3] != "unknown" and (updated == "unknown" or
                    datetime.fromisoformat(updated.replace("Z", "+00:00")) <
                    datetime.fromisoformat(old[3].replace("Z", "+00:00"))):
                continue
        source, number = ref.rsplit("#", 1)
        route = "pull" if item.get("kind") == "pr" else "issues"
        state = "merged" if item.get("merged_at") else item.get("state", "open")
        additions.append(f"- [ ] "
                         f"[{ref}](https://github.com/{source}/{route}/{number}) "
                         f"{safe_title(item.get('title'))} — {state}; {item.get('updated_at') or 'unknown'} "
                         f"<!-- watchdog-item:{ref} {signature} pending {updated} -->")
        latest[ref] = (signature, "pending", " ", updated)
    today = [c for c in owned if f"<!-- watchdog-day:{day}:" in c["body"]]
    commented = 0
    while additions:
        # New findings are appended as publication records.  This preserves the
        # human text and gives every daily refresh a durable audit trail.
        body = f"<!-- watchdog-day:{day}:{len(today) + 1} -->\n{day} 新增及更新\n\n"
        chunk = []
        while additions and len((body + "\n" + "\n".join(chunk + [additions[0]])).encode()) < COMMENT_LIMIT:
            chunk.append(additions.pop(0))
        if not chunk:
            raise ValueError("tracking item exceeds comment limit")
        body = body.rstrip() + "\n" + "\n".join(chunk) + "\n"
        current = api("api", "--method", "POST", endpoint + "/comments", payload={"body": body})
        today.append(current)
        commented += 1
    pending = len({ref for ref, (_, status, checked, _) in latest.items()
                   if ref not in fixed and status != "fixed" and checked.lower() != "x"})
    body = managed_body(issue.get("body") or "", "每日追加新增及更新评论；所有来源共用此 Issue。\n"
                        "修复后勾选对应任务或更新修复账本；已完成条目不再进入待处理集合。\n"
                        "上游关闭或 PR 合并不代表 TokenKey 已解决。\n\n"
                        f"当前待处理：{pending} 条。完成全部条目后自动关闭；新问题复用同一 Issue。")
    changes = {}
    if body != issue.get("body"):
        changes["body"] = body
    state = "open" if pending else "closed"
    if issue.get("state") != state:
        changes["state"] = state
    if changes:
        api("api", "--method", "PATCH", endpoint, payload=changes)
    return {"created": created, "commented": commented, "pending": pending}
