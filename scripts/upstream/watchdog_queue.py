"""One tracking Issue per platform; daily comment deltas are durable publication state."""
import hashlib
import html
import json
import re
from datetime import datetime, timezone
from urllib.parse import urlencode

BEGIN = "<!-- issue-watchdog:begin -->"
END = "<!-- issue-watchdog:end -->"
PLATFORMS = {
    "upstream": {"label": "upstream-tracking", "title": "[upstream] 上游网关调度、计费与账号问题跟踪", "description": "Daily upstream gateway, billing and account tracking"},
    "kiro": {"label": "kiro-tracking", "title": "[kiro] Kiro 中转网关、计费与账号问题跟踪", "description": "Daily Kiro relay gateway, billing and account tracking"},
    "openai": {"label": "openai-tracking", "title": "[openai] Codex 中转网关、计费与账号问题跟踪", "description": "Daily OpenAI/Codex relay gateway, billing and account tracking"},
    "google": {"label": "google-tracking", "title": "[google] Gemini/Antigravity 中转网关、计费与账号问题跟踪", "description": "Daily Google relay gateway, billing and account tracking"},
}
MARKER = "<!-- watchdog-tracking:v1 -->"
# Intel lives in its own namespace, never under `watchdog-tracking`: the queue's
# discovery scan matches that prefix and raises on a second hit, so sharing it
# would make every run die on `duplicate upstream tracking Issues`.
# Discovery prefix, deliberately unterminated: every intel Issue's marker carries
# a platform and period after it, so matching must be on the prefix.
INTEL_MARKER = "<!-- watchdog-intel:v1 "
INTEL_RECORD = re.compile(r"^- \[([^\]]+)\].*?<!-- watchdog-intel-item:(\S+) ([0-9a-f]+) -->$",
                          re.MULTILINE)


def platform_marker(platform):
    if platform not in PLATFORMS:
        raise ValueError(f"unknown tracking platform: {platform}")
    return f"<!-- watchdog-tracking:v1 platform:{platform} -->"


def intel_marker(platform, period):
    """Identity of one period's intel Issue. Period scoping is what bounds size:
    each Issue holds a single period's leads, not the historical sum."""
    if platform not in PLATFORMS:
        raise ValueError(f"unknown tracking platform: {platform}")
    if not re.fullmatch(r"\d{4}-W\d{2}", period or ""):
        raise ValueError(f"intel period must look like 2026-W43: {period!r}")
    return f"<!-- watchdog-intel:v1 platform:{platform} period:{period} -->"


def intel_period(when=None):
    year, week, _ = (when or datetime.now(timezone.utc).date()).isocalendar()
    return f"{year}-W{week:02d}"


RECORD = re.compile(r"^- \[([ xX])\] (.*?) <!-- watchdog-item:([^ ]+) ([0-9a-f]+) (pending|fixed) ([^ ]+) -->$", re.MULTILINE)
COMMENT_LIMIT = 55000

TOPICS = {
    "scheduling": r"gateway|schedul|routing|route|failover|load.?balanc|concurren|capacity|saturation|retry|retries|timeout|网关|调度|路由|故障转移|负载均衡|并发|重试|超时",
    "billing": r"billing|metering|usage|quota|credit|balance|payment|refund|计费|用量|额度|配额|余额|支付|充值|扣费|退款",
    "account": r"oauth|api.?key|key.?rotat|session|credential|refresh.?token|access.?token|account|authentication|authorization|\bauth\b|401|403|429|rate.?limit|cooldown|账号|账户|凭证|鉴权|封号|限流|冷却",
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


def trusted_authors(target, api):
    """Markers in public Issues/comments are data until their author is trusted."""
    publisher = api("api", "user")
    permissions = {}

    def trusted(row):
        author = row.get("user") or {}
        author_id, login = author.get("id"), author.get("login")
        if not author_id or not login:
            return False
        if author_id == publisher["id"]:
            return True
        if author_id not in permissions:
            permission = api("api", f"repos/{target}/collaborators/{login}/permission")
            permissions[author_id] = permission.get("permission") in {"admin", "maintain", "write"}
        return permissions[author_id]

    return trusted


def sync_intel(repo, items, target, api, platform="upstream", period=None, published=None):
    """Publish keyword leads to a rotating, per-period Issue.

    Leads are not tasks: the classifier states it has not verified them, so they
    are plain bullets, never checkboxes. Ticking one here must not retire anything
    -- the promotion path is a judgment in the git ledger, which moves the ref into
    the queue on the next run.

    Rotation is what keeps this readable: a new period opens a fresh Issue and
    closes the previous one. Closed, never deleted, so a lead stays searchable
    long after the report artifact has expired -- which is the whole reason intel
    needs an Issue carrier and not just a CI artifact.

    Delivery state comes from the caller's `published` map rather than this Issue's
    own history, so rotating to a new Issue cannot re-publish an earlier period.
    """
    period = period or intel_period()
    marker = intel_marker(platform, period)
    config = PLATFORMS[platform]
    pages = api("api", "--paginate", "--slurp",
                f"repos/{target}/issues?{urlencode({'state': 'all', 'per_page': 100})}")
    trusted = trusted_authors(target, api)
    rows = [row for page in pages for row in page
            if "pull_request" not in row and INTEL_MARKER in (row.get("body") or "") and trusted(row)]
    current = [row for row in rows if marker in (row.get("body") or "")]
    if len(current) > 1:
        raise ValueError("duplicate intel Issues for one period")
    issue = current[0] if current else None

    items = [{**item, "upstream": item.get("upstream") or f"{repo}#{item['number']}"} for item in items]
    delivered = dict(published or {})
    fresh = []
    for item in sorted(items, key=lambda item: item["upstream"]):
        ref = item["upstream"]
        signature = hashlib.sha256(json.dumps({k: item.get(k) for k in
            ("title", "updated_at", "state", "merged_at")}, sort_keys=True).encode()).hexdigest()[:16]
        # Dedup on content alone, never on the period: including the period would
        # republish every still-open lead each week, which is exactly the
        # accumulation this rotation exists to stop. A lead is published once, in
        # the period it was found; the closed Issue keeps it searchable after that.
        # A changed signature is new information and does publish again.
        record = delivered.get(ref)
        if isinstance(record, (list, tuple)) and record and record[0] == signature:
            continue
        source, number = ref.rsplit("#", 1)
        route = "pull" if item.get("kind") == "pr" else "issues"
        state = "merged" if item.get("merged_at") else item.get("state", "open")
        fresh.append(f"- [{ref}](https://github.com/{source}/{route}/{number}) "
                     f"{safe_title(item.get('title'))} — {state}; "
                     f"{item.get('updated_at') or 'unknown'} "
                     f"<!-- watchdog-intel-item:{ref} {signature} -->")
        delivered[ref] = [signature, period]

    created = 0
    if not issue and not fresh:
        return {"created": 0, "commented": 0, "leads": 0, "closed": 0, "published": delivered}
    if not issue:
        issue = api("api", "--method", "POST", f"repos/{target}/issues", payload={
            "title": f"{config['title']} — 线索 {period}",
            "body": marker + "\n" + managed_body("", f"{platform} 来源本期关键词线索；仅供排查参考。\n"
                "这些条目**未经人工核验**，不是待办：勾选在此无效。\n"
                "确认要做的请在 ops/issue-watchdog 账本登记判断，下一轮它会进入跟踪队列。\n"
                f"下一周期开启时本单自动关闭（不删除），历史可检索。"),
            "labels": [config["label"]]})
        created = 1
    endpoint = f"repos/{target}/issues/{issue['number']}"
    commented = 0
    while fresh:
        body = f"<!-- watchdog-intel-day:{datetime.now(timezone.utc).date().isoformat()} -->\n"
        chunk = []
        while fresh and len((body + "\n".join(chunk + [fresh[0]])).encode()) < COMMENT_LIMIT:
            chunk.append(fresh.pop(0))
        if not chunk:
            raise ValueError("intel lead exceeds comment limit")
        api("api", "--method", "POST", endpoint + "/comments",
            payload={"body": body + "\n".join(chunk) + "\n"})
        commented += 1

    # Close prior periods only after this one exists, so a lead is never carried by
    # nothing. Closing is not deleting: the Issue stays searchable.
    closed = 0
    for row in rows:
        if row["number"] != issue["number"] and row.get("state") == "open":
            api("api", "--method", "PATCH", f"repos/{target}/issues/{row['number']}",
                payload={"state": "closed", "state_reason": "completed"})
            closed += 1
    return {"created": created, "commented": commented, "leads": len(items),
            "closed": closed, "published": delivered}


def sync_queue(repo, items, target, api, resolved=(), day=None, platform="upstream",
               published=None):
    """Publish relevant findings immediately, regardless of risk/manual judgment.

    A checked task is an explicit TokenKey resolution. Ledger resolutions also
    check every historical task for that ref. Absence from an incremental scan
    or upstream closure is never treated as a TokenKey fix.
    """
    day = day or datetime.now(timezone.utc).date().isoformat()
    marker = platform_marker(platform)
    config = PLATFORMS[platform]
    resolved = set(resolved)
    # The hidden platform marker owns identity. Labels are presentation metadata
    # and may be removed by an operator; filtering by them would create a second
    # aggregate Issue after that harmless edit.
    pages = api("api", "--paginate", "--slurp",
                f"repos/{target}/issues?{urlencode({'state': 'all', 'per_page': 100})}")
    trusted = trusted_authors(target, api)
    matches = [row for page in pages for row in page
               if "pull_request" not in row and (marker in (row.get("body") or "") or
                    (platform == "upstream" and MARKER in (row.get("body") or ""))) and trusted(row)]
    if len(matches) > 1:
        raise ValueError("duplicate upstream tracking Issues")
    issue = matches[0] if matches else None
    items = [{**item, "upstream": item.get("upstream") or f"{repo}#{item['number']}"} for item in items]
    items = [item for item in items if item["upstream"] not in resolved]
    if not issue and not items:
        # Nothing to publish: hand back the delivery state unchanged rather than an
        # empty map, which the caller would persist and then re-publish everything.
        return {"created": 0, "commented": 0, "pending": 0,
                "published": dict(published or {})}
    created = 0
    if not issue:
        labels = {row["name"] for page in api("api", "--paginate", "--slurp",
                  f"repos/{target}/labels?per_page=100") for row in page}
        if config["label"] not in labels:
            api("api", "--method", "POST", f"repos/{target}/labels", payload={
                "name": config["label"], "color": "BFD4F2", "description": config["description"]})
        issue = api("api", "--method", "POST", f"repos/{target}/issues", payload={
            "title": config["title"],
            "body": marker + "\n" + managed_body("", f"每日追加新增及更新评论；{platform} 来源共用此 Issue。\n"
                "修复后勾选对应任务或更新修复账本；已完成条目不再进入待处理集合。\n"
                "上游关闭或 PR 合并不代表 TokenKey 已解决。"), "labels": [config["label"]]})
        created = 1
    endpoint = f"repos/{target}/issues/{issue['number']}"
    comments = [row for page in api("api", "--paginate", "--slurp", endpoint + "/comments?per_page=100") for row in page]
    # Only repository writers may supply state; other public comments are prose.
    owned = [row for row in comments if re.search(r"<!-- watchdog-day:\d{4}-\d{2}-\d{2}:\d+ -->", row.get("body") or "") and trusted(row)]
    # Delivery state belongs to the checkpoint, human intent to the Issue. Seeding
    # `latest` from the checkpoint first means a signature survives a line being
    # deleted or the Issue being replaced; the comment scrape then overrides it for
    # any ref the Issue still carries, so the Issue stays a recovery source when the
    # Actions cache is evicted. Ticks are only ever read from the comments: a human
    # verdict must not be inventable by whatever wrote the checkpoint.
    latest, manual = {}, set()
    for ref, record in (published or {}).items():
        if not isinstance(record, (list, tuple)) or len(record) < 2:
            continue
        signature, status = record[0], record[1]
        updated = record[2] if len(record) >= 3 else "unknown"
        if not isinstance(signature, str) or status not in ("pending", "fixed"):
            continue
        if updated != "unknown":
            try:
                datetime.fromisoformat(str(updated).replace("Z", "+00:00"))
            except ValueError:
                updated = "unknown"
        # Never seed a checkbox: a verdict is read from the Issue, never from state.
        latest[ref] = (signature, status, " ", updated)
    for comment in owned:
        for checked, text, ref, signature, status, updated in RECORD.findall(comment.get("body") or ""):
            latest[ref] = (signature, status, checked, updated)
            if checked.lower() == "x" and status == "pending":
                manual.add(ref)
    # The queue holds what WE still have to act on, so a task leaves it only on our
    # own verdict: a checkbox, or a recorded fix judgment. Upstream closing or
    # merging never retires one -- an upstream fix is exactly the case where
    # TokenKey may still be missing that behavior. Git keeps the audit trail; an
    # Issue that also kept every retired task grows without bound and stops being read.
    retired = resolved | manual
    # Retire before appending anything; retries remain idempotent.
    # Delete only ledger-resolved lines: git already holds that audit trail. A
    # human checkbox must KEEP its line, because that line is the only durable
    # carrier of the tick -- `manual` is rebuilt from live comments on every run,
    # so deleting the line discards the verdict and the task returns next round.
    for comment in owned:
        body = RECORD.sub(lambda m: "" if m[3] in resolved else m[0], comment.get("body") or "")
        body = re.sub(r"\n{3,}", "\n\n", body).rstrip() + "\n"
        if body != comment.get("body"):
            api("api", "--method", "PATCH", f"repos/{target}/issues/comments/{comment['id']}", payload={"body": body})
            comment["body"] = body
    additions = []
    for item in sorted(items, key=lambda item: item["upstream"]):
        ref = item["upstream"]
        if ref in retired:
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
        # New findings are appended as a dated batch, preserving human text.
        # Retired tasks are removed rather than struck through, so the Issue stays
        # a readable queue of open work instead of an ever-growing mirror.
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
                   if ref not in retired and status != "fixed" and checked.lower() != "x"})
    body = managed_body(issue.get("body") or "", f"每日追加新增及更新评论；{platform} 来源共用此 Issue。\n"
                        "勾选即从队列移除；判断理由与修复证据记在 ledger 与 commit，不留在本单。\n"
                        "上游关闭或 PR 合并不代表 TokenKey 已解决，条目仍留在队列里。\n\n"
                        f"当前待处理：{pending} 条。完成全部条目后自动关闭；新问题复用同一 Issue。")
    changes = {}
    if body != issue.get("body"):
        changes["body"] = body
    state = "open" if pending else "closed"
    if issue.get("state") != state:
        changes["state"] = state
    if changes:
        api("api", "--method", "PATCH", endpoint, payload=changes)
    # Hand the delivery state back so the caller can persist it in the checkpoint.
    # Retired refs stay in the map: forgetting them would re-publish on the next run.
    delivered = {ref: [signature, status, updated]
                 for ref, (signature, status, _, updated) in latest.items()}
    return {"created": created, "commented": commented, "pending": pending,
            "published": delivered}
