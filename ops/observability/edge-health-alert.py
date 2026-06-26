#!/usr/bin/env python3
"""edge-health-alert.py — turn scan-edge-health.sh --json output into an alert
decision + a Feishu message, with cross-run dedup via a state key.

This is the *decision* half of the edge-health-watch loop; the *transport* halves
are scan-edge-health.sh --json (read-only SSM fleet sweep) and the
.github/workflows/edge-health-watch.yml step that signs + POSTs to Feishu. Keeping
the decision here (pure Python, no HTTP/AWS) makes it unit-testable with fixtures
(--selftest) and registerable in preflight, mirroring data_layer_capacity_verdict.py
and edge_health_verdict.py.

WHY (2026-06-07 incident): 4 edges went to 0 schedulable accounts and the one healthy
multi-account edge (us5) buckled under concentrated failover load; ~3445 clients ate
429s across two spikes — and ZERO alerts fired, because the truth tool
(scan-edge-health.sh, #640) only runs when a human remembers to run it. This turns
"someone has to go look" into "Feishu shouts the moment an edge dies".

Trigger model — three severities, all deduped by one state key. The severity answers
the only question that matters: IS A REAL CLIENT BEING HURT RIGHT NOW?
  🔴 critical (ACTIVE HARM): unreachable / parse-error / degraded, OR a `down` edge that
    is ACTIVELY rejecting (no_available_429>0). Someone is eating 429s this minute.
  🟠 warning (LEADING INDICATOR): a `down` edge whose pool just emptied (sched=0, accounts
    exist) but has rejected ZERO clients yet (no_available_429==0). Nobody is hurt yet —
    this is the step BEFORE an outage, not an outage. The 2026-06-09 alert read exactly
    this shape (`sched=0 served200=639 noavail429=0 ratio=1.0`) and still shouted 🔴 宕机;
    calling a not-yet-outage by the outage word spends the operator's trust. So it now
    gets its own colour and the word 即将掉单, reserving 🔴/宕机 for active harm.
  🟡 notice (POSTURE / provisioning risk): verdict thin / idle-thin (single-account SPOF)
    and no-accounts (zero accounts provisioned, no rejected demand — e.g. us3/us4 awaiting
    the add-accounts vs decommission decision). Chronic states that must not pin the fleet
    to critical, but whose SET CHANGES are exactly the early warnings the 2026-06-07
    incident lacked (an edge slipping 2→1 accounts is the step BEFORE down). Newly-appeared
    posture edges are tagged 新 so "what changed" reads at a glance vs the chronic backdrop.
The state key digests BOTH sets (`a:` / `p:` prefixed); the workflow alerts ONLY when
the key changes vs the previous run — new breakage, escalation, posture drift, OR
recovery — so a multi-hour incident (or a chronically thin fleet) does not re-spam
every cycle, and a recovery posts a green all-clear.

Input (stdin): one verdict JSON object per line (scan-edge-health.sh --json).
Args:
  --prev-key-file <path>   file holding the previous run's state key (may be missing/empty)
  --window <str>           traffic window label to show in the message (default "2h")
  --selftest               run fixtures (exit 1 on failure)

Output (stdout): one JSON object:
  {"key","actionable_count","changed","should_alert","severity","message"}
The workflow writes `key` back to the cache, and POSTs `message` to Feishu iff
`should_alert` is true.
"""
from __future__ import annotations

import argparse
import json
import sys

ACTIONABLE = ("down", "degraded", "unreachable", "parse-error")
POSTURE = ("thin", "idle-thin", "no-accounts")


def _num(v, default=0):
    try:
        return int(v)
    except (TypeError, ValueError):
        return default


def _down_kind(r: dict):
    """Split the single word `down` into the two states it hides — the distinction that
    decides whether a real client is being hurt right now:
      'leading' — pool empty (sched=0, accounts exist) but ZERO clients rejected yet
                  (no_available_429==0). The step BEFORE an outage; nobody hurt yet.
      'active'  — clients ARE eating empty-pool 429s now (no_available_429>0), e.g. the
                  ratio collapsed with real traffic or an unprovisioned edge took demand.
    Returns None for non-down rows. (`down` always has sched=0 by construction in the
    verdict, so the noavail split is the whole story.)"""
    if r.get("verdict") != "down":
        return None
    return "leading" if _num(r.get("no_available_429")) == 0 else "active"


def _minutes_from_window(window: str) -> float:
    """Parse a `docker logs --since` style window ('2h' / '90m' / '15h' / '45' / '1d')
    into minutes, for the leading-indicator request-rate estimate. Bare number = minutes.
    Returns 0.0 on anything unparseable so the caller falls back to 'rate unknown' rather
    than fabricating a time-to-impact."""
    if not window:
        return 0.0
    w = str(window).strip().lower()
    unit, mult = w[-1:], {"h": 60.0, "m": 1.0, "d": 1440.0, "s": 1.0 / 60.0}
    try:
        if unit in mult:
            return float(w[:-1]) * mult[unit]
        return float(w)  # bare number => minutes
    except ValueError:
        return 0.0


def _time_to_impact(r: dict, window: str) -> str:
    """The number behind '下一批即将掉单'. Without it the leading-indicator claim is a
    hollow 'trust me' — Jobs Q7. Estimate request arrival rate from the window's own
    throughput so the operator knows if the next drop is seconds or hours away. Honest
    about ignorance: zero traffic in the window => we SAY the arrival time is unknown
    rather than imply urgency that may not exist."""
    minutes = _minutes_from_window(window)
    reqs = _num(r.get("total_completed")) or (_num(r.get("served_200")) + _num(r.get("no_available_429")))
    if minutes <= 0 or reqs <= 0:
        return f"近{window} 无流量经过 → 下一单何时到达未知，掉单尚不紧迫"
    per_min = reqs / minutes
    if per_min >= 1:
        gap = 60.0 / per_min
        when = f"约 {gap:.0f}s 内下一单将掉" if gap >= 1 else "下一单几乎立刻掉"
        return f"近{window} ~{per_min:.0f} 单/分 → {when}"
    # < 1/min: report the inter-arrival gap in minutes so sub-rate edges aren't rounded to 0
    gap_min = 1.0 / per_min
    return f"近{window} ~{reqs} 单/{window}（<1 单/分）→ 约 {gap_min:.0f} 分钟内下一单将掉"


def build_decision(rows: list, prev_key: str, window: str) -> dict:
    """Pure: verdict rows + previous state key -> alert decision + message."""
    actionable = [r for r in rows if r.get("verdict") in ACTIONABLE]
    posture = [r for r in rows if r.get("verdict") in POSTURE]
    # Stable key: sorted "a:verdict:edge" (incident) + "p:verdict:edge" (posture).
    # Same incident AND same posture => same key => no re-alert; any change (new edge
    # down, an edge slipping to single-account, posture resolved, recovery) flips it.
    key_parts = sorted(f"a:{r.get('verdict')}:{r.get('edge')}" for r in actionable) + \
        sorted(f"p:{r.get('verdict')}:{r.get('edge')}" for r in posture)
    key = "|".join(key_parts)  # "" when the fleet is fully clean

    changed = key != (prev_key or "")
    should_alert = changed  # breakage, escalation, posture drift, recovery all flip the key

    # `idle-thin` (a single-account edge with no traffic) is a SPOF too — surface it
    # in the thin context line, not silently dropped.
    thin = sorted(r.get("edge") for r in rows if r.get("verdict") in ("thin", "idle-thin"))
    no_accounts = sorted(r.get("edge") for r in rows if r.get("verdict") == "no-accounts")
    healthy = sorted(
        r.get("edge") for r in rows if r.get("verdict") in ("healthy", "idle")
    )

    # Legacy keys (pre-posture) carried unprefixed actionable entries — treat any
    # non-`p:` part as actionable so the first run after the format change still
    # reads "there WAS an incident" for recovery semantics.
    prev_parts = (prev_key or "").split("|")
    prev_had_actionable = any(part and not part.startswith("p:") for part in prev_parts)
    # Edges that were ALREADY in some posture bucket last run — used to tag only the
    # newly-appeared posture edges with 新, so an actionable alert's posture footer reads
    # "what changed" instead of re-dumping the same chronic SPOF/limbo list every cycle.
    prev_posture_edges = {
        part.split(":", 2)[2] for part in prev_parts
        if part.startswith("p:") and part.count(":") >= 2
    }

    # Severity is gated on ACTIVE HARM, not on the bare verdict word: a `down` edge that
    # has rejected nobody yet (leading indicator) is a 🟠 warning, not a 🔴 outage.
    active_harm = any(
        r.get("verdict") in ("unreachable", "parse-error", "degraded") for r in actionable
    ) or any(_down_kind(r) == "active" for r in actionable)
    if not actionable:
        if posture:
            # Incident over but provisioning risk remains => green recovery head if an
            # incident just cleared, otherwise a posture-drift notice.
            severity = "recovery" if prev_had_actionable else "notice"
        else:
            severity = "recovery" if (prev_key or "") else "ok"
    elif active_harm:
        severity = "critical"
    else:
        severity = "warning"  # only leading-indicator down(s): pool empty, nobody rejected yet

    message = _format_message(actionable, thin, no_accounts, healthy, window, severity,
                              prev_posture_edges)
    return {
        "key": key,
        "actionable_count": len(actionable),
        "changed": changed,
        "should_alert": should_alert,
        "severity": severity,
        "message": message,
    }


def _classify(r: dict) -> tuple:
    """Deterministic (cause_tag, action) for one actionable verdict row, derived ONLY
    from fields already in the verdict payload (no extra I/O). The tags are the operator
    judgment the raw `sched=…/ratio=…` line cannot convey on its own:

      - The 2026-06-09 alert read `sched=0 served200=3 noavail429=0 ratio=1.0` — which
        looks self-contradictory (down, yet ratio 1.0, zero 429). The truth is "the pool
        just emptied and HASN'T started rejecting yet" — a LEADING indicator, not an
        active outage. That distinction (noavail429==0 vs >0) changes the urgency.
      - `sched=0 total=0` (no accounts provisioned) vs `sched=0 total>0` (accounts exist
        but every one is unschedulable: cooldown / schedulable-flag / OAuth) point at
        completely different fixes — the raw line never showed `total`.
    """
    verdict = r.get("verdict")
    if verdict in ("unreachable", "parse-error"):
        return ("探测不可达", "SSM/probe 打不通——查实例是否存活、安全组/SSM agent、run-probe 是否超时")
    sched = _num(r.get("schedulable_accounts"))
    total = _num(r.get("total_accounts"))
    noavail = _num(r.get("no_available_429"))
    if verdict == "down":
        if sched == 0 and total == 0:
            # The quiet (no-demand) zero-account state is verdict no-accounts (posture)
            # and never reaches here; down+total=0 means clients ARE eating 429s.
            return ("未配置账号·流量被拒", "该 edge 没有任何账号且仍有请求打到这——紧急补号，或把 prod 镜像/DNS 流量从该 edge 摘除")
        if sched == 0 and noavail == 0:
            return ("池刚空·尚未拒单(领先告警)",
                    "账号已全部不可调度但还没开始 429——下一批请求即将掉单，见下方按账号定位")
        if sched == 0:
            return ("活跃掉单·账号全不可调度",
                    "正在大量 429、客户端已受影响（紧急）——见下方按账号定位")
        return ("在调度但几乎全 429",
                "账号可调度却几乎不出 200——多为上游限流/会话拒绝；查 cooldown 原因，勿清冷却、勿盲目扩容")
    if verdict == "degraded":
        return ("在服务但漏 429·池不足",
                "有 200 也有空池 429——账号数撑不住当前流量；补健康账号或分流，单边 ≥2 账号")
    return ("", "")


def _needs_triage_runbook(actionable: list) -> bool:
    """Show the sched=0 排查三连 runbook only when an edge is down with accounts that
    EXIST but are all unschedulable (total>0) — that is the case where "what do I do" is
    acute and the answer is the cooldown/flag/OAuth decision tree. An unreachable-only or
    未配置账号-only alert gets its per-edge action instead, no runbook."""
    return any(
        r.get("verdict") == "down"
        and _num(r.get("schedulable_accounts")) == 0
        and _num(r.get("total_accounts")) > 0
        for r in actionable
    )


# Display kinds in urgency order. `down` is split by _down_kind so "正在掉单" (active
# harm) and "即将掉单·预警" (leading indicator, nobody hurt yet) never share a heading —
# the whole point of the severity refactor.
_KIND_ORDER = ("down-active", "degraded", "unreachable", "parse-error", "down-leading")
_KIND_ZH = {
    "down-active": "宕机·正在掉单（客户端已受影响）",
    "degraded": "降级·漏拒（在服务但池不足）",
    "unreachable": "探测不可达",
    "parse-error": "解析失败",
    "down-leading": "即将掉单·预警（池已空，尚无客户端被拒）",
}


def _row_kind(r: dict) -> str:
    if r.get("verdict") == "down":
        return "down-active" if _down_kind(r) == "active" else "down-leading"
    return r.get("verdict")


def _triage_block(actionable: list) -> list:
    """Per-account 'which of the 3 steps applies, and the one-line fix' — DERIVED from the
    verdict's `unschedulable` diagnosis, not dumped as a generic runbook for the operator to
    work through by hand (Jobs Q8/Q9). Renders only for `down` edges whose accounts EXIST
    but are all unschedulable, and only when the verdict carried per-account data (else the
    caller falls back to the generic 排查三连)."""
    rows = [r for r in actionable
            if r.get("verdict") == "down" and _num(r.get("total_accounts")) > 0 and r.get("unschedulable")]
    if not rows:
        return []
    out = ["按账号定位（已派生，无需逐个翻 admin UI）:"]
    for r in sorted(rows, key=lambda r: r.get("edge") or ""):
        for a in r.get("unschedulable") or []:
            name = a.get("name") or "?"
            aid = a.get("id")
            who = f'"{name}"(#{aid})' if aid is not None else f'"{name}"'
            out.append(f"  • {r.get('edge')} {who}: {a.get('hint')}")
    return out


def _mark_new(edges: list, prev_posture_edges: set) -> str:
    """Tag edges that newly entered a posture bucket with 新, so the posture footer reads
    'what changed' against the chronic backdrop instead of an undifferentiated list."""
    return ", ".join(f"{e}(新)" if e not in (prev_posture_edges or set()) else e for e in edges)


def _format_message(actionable: list, thin: list, no_accounts: list, healthy: list,
                    window: str, severity: str, prev_posture_edges: set = frozenset()) -> str:
    if actionable:
        n = len(actionable)
        if severity == "critical":
            head = f"🔴 TokenKey 边缘健康 — {n} 个 edge 正在掉单/异常（客户端已受影响）"
        else:  # warning: leading-indicator only — pool empty, nobody rejected yet
            head = f"🟠 TokenKey 边缘健康 — {n} 个 edge 即将掉单（预警·尚无客户端被拒）"
    elif severity == "notice":
        head = "🟡 TokenKey 边缘健康 — 账号配备风险变化（无宕机/降级）"
    else:
        head = "✅ TokenKey 边缘健康 — 全部恢复（无宕机/降级）"
    lines = [head, ""]

    grouped: dict = {}
    for r in actionable:
        grouped.setdefault(_row_kind(r), []).append(r)

    for kind in _KIND_ORDER:
        rs = grouped.get(kind)
        if not rs:
            continue
        lines.append(f"{_KIND_ZH[kind]}:")
        for r in sorted(rs, key=lambda r: r.get("edge") or ""):
            cause, action = _classify(r)
            if kind in ("unreachable", "parse-error"):
                lines.append(f"  • {r.get('edge')}  ← {cause}")
            else:
                sched = _num(r.get("schedulable_accounts"))
                total = _num(r.get("total_accounts"))
                served = _num(r.get("served_200"))
                noavail = _num(r.get("no_available_429"))
                ratio = r.get("served_ratio")
                wait = _num(r.get("wait_timeout"))
                # "现在" (instantaneous pool) vs "近{window}" (trailing traffic) are labelled
                # so `可调度=0/1 … 服务200=639 ratio=1.0` no longer reads as a contradiction
                # (it's NOW empty, but served 639 over the LAST window) — Jobs Q1.
                tail = f"现在 可调度={sched}/{total} · 近{window} 服务200={served} 空池429={noavail}"
                if ratio is not None:
                    tail += f" ratio={ratio}"
                if wait:
                    tail += f" 等待超时={wait}"
                lines.append(f"  • {r.get('edge')}  {tail}  ← {cause}")
            if action:
                lines.append(f"      ↳ {action}")
            # Leading indicator => quantify "下一批即将掉单" so urgency is a number, not a vibe.
            if kind == "down-leading":
                lines.append(f"      ⏱ {_time_to_impact(r, window)}")

    triage = _triage_block(actionable)
    if triage:
        lines.append("")
        lines.extend(triage)
    elif _needs_triage_runbook(actionable):
        # Legacy fallback: a verdict payload without per-account `unschedulable` (older scan
        # output). Keep the generic decision tree so an old producer still gets guidance.
        lines.append("")
        lines.append("排查三连（sched=0 但账号存在时，逐一定位 N/M 中不可调度的账号）:")
        lines.append("  1. 冷却/窗口烧穿: 账号 active 但 5h/7d 窗口或上游限流冷却中 → 等恢复或按窗口剩余重排 priority；勿清冷却、勿扩容")
        lines.append("  2. schedulable=false 被钉死: OAuth 健康+无冷却却调度关闭 → 一键启用 remediate-schedulable-pool.sh MODE=edge-oauth-pool")
        lines.append("  3. OAuth 吊销: refresh 报成功仍 401 → 重新授权/换号，重刷无效")

    if (thin or no_accounts) and lines[-1] != "":
        lines.append("")
    if thin:
        lines.append(f"薄边/SPOF（单账号，应补到 ≥2）: {_mark_new(thin, prev_posture_edges)}")
    if no_accounts:
        lines.append(f"未配置账号（0 账号、无被拒流量——补号或下线，决策前不按宕机告警）: {_mark_new(no_accounts, prev_posture_edges)}")
    if healthy:
        lines.append(f"健康: {', '.join(healthy)}")
    lines.append("")
    lines.append(f"窗口={window} • 逐账号明细(status/cooldown/schedulable/refresh): ops/observability/scan-edge-health.sh --with-prod")
    return "\n".join(lines)


# --- selftest fixtures: the 2026-06-07 incident shapes + dedup behavior ----------
_SELFTEST = []


def _case(name, rows, prev_key, expect):
    _SELFTEST.append((name, rows, prev_key, expect))


_case(
    "incident: 3 down + 1 degraded => critical, alert from clean",
    [
        {"edge": "uk2", "verdict": "down", "schedulable_accounts": 0, "served_200": 712, "no_available_429": 0},
        {"edge": "uk3", "verdict": "down", "schedulable_accounts": 0, "served_200": 882, "no_available_429": 0},
        {"edge": "us3", "verdict": "down", "schedulable_accounts": 0, "served_200": 170, "no_available_429": 0},
        {"edge": "us5", "verdict": "degraded", "schedulable_accounts": 3, "served_200": 11102, "no_available_429": 4841, "served_ratio": 0.696, "wait_timeout": 5656},
        {"edge": "us2", "verdict": "thin", "schedulable_accounts": 1},
        {"edge": "prod", "verdict": "healthy", "schedulable_accounts": 14},
    ],
    "",
    {"should_alert": True, "severity": "critical", "actionable_count": 4},
)
_case(
    "same incident set unchanged => NO re-alert",
    [
        {"edge": "uk2", "verdict": "down"},
        {"edge": "uk3", "verdict": "down"},
        {"edge": "us3", "verdict": "down"},
        {"edge": "us5", "verdict": "degraded"},
    ],
    "a:degraded:us5|a:down:uk2|a:down:uk3|a:down:us3",
    {"should_alert": False, "severity": "critical"},
)
_case(
    "escalation: a new edge goes down => alert (key changed)",
    [
        {"edge": "uk2", "verdict": "down"},
        {"edge": "uk3", "verdict": "down"},
        {"edge": "us3", "verdict": "down"},
        {"edge": "us6", "verdict": "down"},
        {"edge": "us5", "verdict": "degraded"},
    ],
    "a:degraded:us5|a:down:uk2|a:down:uk3|a:down:us3",
    {"should_alert": True, "severity": "critical"},
)
_case(
    "full recovery => green all-clear alert",
    [
        {"edge": "uk2", "verdict": "healthy", "schedulable_accounts": 2},
        {"edge": "us5", "verdict": "healthy", "schedulable_accounts": 3},
    ],
    "a:degraded:us5|a:down:uk2|a:down:uk3|a:down:us3",
    {"should_alert": True, "severity": "recovery", "actionable_count": 0},
)
_case(
    "legacy unprefixed prev key (pre-posture format) still reads as incident => recovery",
    [
        {"edge": "uk2", "verdict": "healthy", "schedulable_accounts": 2},
        {"edge": "us5", "verdict": "healthy", "schedulable_accounts": 3},
    ],
    "degraded:us5|down:uk2",
    {"should_alert": True, "severity": "recovery", "actionable_count": 0},
)
_case(
    "incident recovers but SPOF posture remains => green recovery head, key keeps posture",
    [
        {"edge": "uk2", "verdict": "healthy", "schedulable_accounts": 2},
        {"edge": "us2", "verdict": "thin", "schedulable_accounts": 1},
    ],
    "a:down:uk2|p:thin:us2",
    {"should_alert": True, "severity": "recovery", "actionable_count": 0},
)
_case(
    "steady healthy => no alert, no key",
    [
        {"edge": "uk2", "verdict": "healthy"},
        {"edge": "us5", "verdict": "healthy"},
    ],
    "",
    {"should_alert": False, "severity": "ok", "actionable_count": 0},
)
_case(
    "posture drift: thin set first seen => ONE notice alert",
    [
        {"edge": "uk1", "verdict": "thin", "schedulable_accounts": 1},
        {"edge": "us2", "verdict": "thin", "schedulable_accounts": 1},
        {"edge": "prod", "verdict": "healthy"},
    ],
    "",
    {"should_alert": True, "severity": "notice", "actionable_count": 0},
)
_case(
    "chronic thin unchanged => silence (deduped via p: key)",
    [
        {"edge": "uk1", "verdict": "thin", "schedulable_accounts": 1},
        {"edge": "us2", "verdict": "thin", "schedulable_accounts": 1},
        {"edge": "prod", "verdict": "healthy"},
    ],
    "p:thin:uk1|p:thin:us2",
    {"should_alert": False, "severity": "notice", "actionable_count": 0},
)
_case(
    "uk2/uk3 zero-account posture unchanged => silence, NOT chronic critical",
    [
        {"edge": "uk2", "verdict": "no-accounts", "schedulable_accounts": 0, "total_accounts": 0},
        {"edge": "uk3", "verdict": "no-accounts", "schedulable_accounts": 0, "total_accounts": 0},
        {"edge": "prod", "verdict": "healthy"},
    ],
    "p:no-accounts:uk2|p:no-accounts:uk3",
    {"should_alert": False, "severity": "notice", "actionable_count": 0},
)
_case(
    "posture resolved (account added, SPOF gone) => notice alert announces the change",
    [
        {"edge": "uk1", "verdict": "healthy", "schedulable_accounts": 2},
        {"edge": "us2", "verdict": "thin", "schedulable_accounts": 1},
    ],
    "p:thin:uk1|p:thin:us2",
    {"should_alert": True, "severity": "notice", "actionable_count": 0},
)
_case(
    "unreachable edge => critical actionable",
    [
        {"edge": "fra1", "verdict": "unreachable"},
        {"edge": "prod", "verdict": "healthy"},
    ],
    "",
    {"should_alert": True, "severity": "critical", "actionable_count": 1},
)
_case(
    "pure leading-indicator down (pool empty, ZERO clients rejected) => 🟠 warning, NOT 🔴 critical",
    [
        {"edge": "us6", "verdict": "down", "schedulable_accounts": 0, "total_accounts": 1,
         "served_200": 639, "no_available_429": 0, "served_ratio": 1.0},
        {"edge": "prod", "verdict": "healthy", "schedulable_accounts": 14},
    ],
    "",
    {"should_alert": True, "severity": "warning", "actionable_count": 1},
)
_case(
    "active-outage down (clients eating empty-pool 429s) => 🔴 critical",
    [
        {"edge": "us3", "verdict": "down", "schedulable_accounts": 0, "total_accounts": 1,
         "served_200": 0, "no_available_429": 33748, "served_ratio": 0.0},
    ],
    "",
    {"should_alert": True, "severity": "critical", "actionable_count": 1},
)
_case(
    "leading down + an active down together => critical (the active one rules)",
    [
        {"edge": "us6", "verdict": "down", "schedulable_accounts": 0, "total_accounts": 1,
         "served_200": 639, "no_available_429": 0},
        {"edge": "us3", "verdict": "down", "schedulable_accounts": 0, "total_accounts": 1,
         "served_200": 0, "no_available_429": 33748},
    ],
    "",
    {"should_alert": True, "severity": "critical", "actionable_count": 2},
)


# --- message-content fixtures: lock the operator-value enrichment (cause tags, sched=N/M,
# the 排查三连 runbook) so a regression that strips guidance fails preflight, not prod ----
_MESSAGE_CASES = []


def _msg_case(name, rows, must_contain, must_not_contain=(), prev_key=""):
    _MESSAGE_CASES.append((name, rows, must_contain, must_not_contain, prev_key))


_msg_case(
    "leading-indicator down, legacy payload (no per-account data) => 🟠 head, now/window split, ⏱, generic runbook fallback",
    [
        {"edge": "uk1", "verdict": "down", "schedulable_accounts": 0, "total_accounts": 2,
         "served_200": 3, "no_available_429": 0, "served_ratio": 1.0, "total_completed": 3},
        {"edge": "prod", "verdict": "healthy", "schedulable_accounts": 14},
    ],
    ["🟠", "即将掉单", "池刚空·尚未拒单(领先告警)", "现在 可调度=0/2", "近2h 服务200=3", "⏱",
     "排查三连", "schedulable=false 被钉死", "OAuth 吊销"],
    ["🔴", "sched=0/2"],  # not red (nobody rejected yet); old bare-sched format gone
)
_msg_case(
    "leading-indicator down WITH per-account diagnosis => 按账号定位 block replaces the generic runbook",
    [
        {"edge": "us6", "verdict": "down", "schedulable_accounts": 0, "total_accounts": 2,
         "served_200": 639, "no_available_429": 0, "served_ratio": 1.0, "total_completed": 639,
         "unschedulable": [
             {"id": 5, "name": "cc-us6a", "bucket": "flag",
              "hint": "schedulable=false 被钉死（账号健康却调度关闭）→ 一键启用：remediate-schedulable-pool.sh MODE=edge-oauth-pool"},
             {"id": 6, "name": "cc-us6b", "bucket": "window",
              "hint": "5h/7d 窗口烧穿（上游 rejected）→ 等窗口重置，勿清冷却、勿扩容"},
         ]},
    ],
    ["🟠", "按账号定位（已派生", 'us6 "cc-us6a"(#5)', "一键启用：remediate-schedulable-pool.sh MODE=edge-oauth-pool",
     'us6 "cc-us6b"(#6)', "窗口烧穿"],
    ["排查三连"],  # generic runbook is suppressed once per-account data is present
)
_msg_case(
    "active-outage down (sched=0, 429 flooding) => 🔴 head, urgent tag, not the leading tag",
    [{"edge": "us3", "verdict": "down", "schedulable_accounts": 0, "total_accounts": 1,
      "served_200": 0, "no_available_429": 33748, "served_ratio": 0.0, "total_completed": 33748}],
    ["🔴", "正在掉单", "活跃掉单·账号全不可调度", "现在 可调度=0/1"],
    ["池刚空·尚未拒单", "🟠"],
)
_msg_case(
    "down with total=0 AND rejected demand => 未配置账号·流量被拒, NO 排查三连 runbook",
    [{"edge": "fra1", "verdict": "down", "schedulable_accounts": 0, "total_accounts": 0,
      "served_200": 0, "no_available_429": 412}],
    ["未配置账号·流量被拒", "紧急补号"],
    ["排查三连"],
)
_msg_case(
    "posture-only (thin + no-accounts) => 🟡 notice head with both posture lines",
    [
        {"edge": "uk1", "verdict": "thin", "schedulable_accounts": 1},
        {"edge": "uk2", "verdict": "no-accounts", "schedulable_accounts": 0, "total_accounts": 0},
        {"edge": "prod", "verdict": "healthy", "schedulable_accounts": 14},
    ],
    ["🟡", "账号配备风险变化", "薄边/SPOF（单账号，应补到 ≥2）: uk1", "未配置账号（0 账号", "uk2"],
    ["🔴", "全部恢复", "宕机（down）"],
)
_msg_case(
    "incident cleared with posture residue => ✅ recovery head + posture lines",
    [
        {"edge": "uk2", "verdict": "healthy", "schedulable_accounts": 2},
        {"edge": "us2", "verdict": "thin", "schedulable_accounts": 1},
    ],
    ["✅", "全部恢复", "薄边/SPOF（单账号，应补到 ≥2）: us2"],
    ["🔴", "🟡"],
    prev_key="a:down:uk2|p:thin:us2",
)
_msg_case(
    "unreachable => 探测不可达 action, no runbook",
    [{"edge": "sg1", "verdict": "unreachable"}],
    ["探测不可达", "实例是否存活"],
    ["排查三连"],
)
_msg_case(
    "degraded => 🔴 (active harm, clients bleeding 429s) + 池不足 tag + labelled window traffic",
    [{"edge": "us5", "verdict": "degraded", "schedulable_accounts": 3, "total_accounts": 3,
      "served_200": 11102, "no_available_429": 4841, "served_ratio": 0.696, "wait_timeout": 5656}],
    ["🔴", "降级·漏拒", "在服务但漏 429·池不足", "等待超时=5656", "近2h 服务200=11102"],
    ["🟠"],  # degraded is active harm, never the leading-indicator amber
)
_msg_case(
    "leading-indicator with measurable rate => ⏱ shows a concrete time-to-next-drop",
    [{"edge": "us6", "verdict": "down", "schedulable_accounts": 0, "total_accounts": 1,
      "served_200": 600, "no_available_429": 0, "served_ratio": 1.0, "total_completed": 600}],
    ["⏱", "单/分", "下一单"],
)
_msg_case(
    "posture marks newly-appeared edges with 新, leaves chronic ones bare",
    [
        {"edge": "uk9", "verdict": "thin", "schedulable_accounts": 1},   # new this run
        {"edge": "us2", "verdict": "thin", "schedulable_accounts": 1},   # already in prev key
        {"edge": "prod", "verdict": "healthy", "schedulable_accounts": 14},
    ],
    ["uk9(新)", "us2"],
    ["us2(新)"],  # chronic edge is NOT re-flagged as new
    prev_key="p:thin:us2",
)


def _run_selftest() -> int:
    failures = 0
    for name, rows, prev_key, expect in _SELFTEST:
        got = build_decision(rows, prev_key, "2h")
        for k, v in expect.items():
            if got.get(k) != v:
                failures += 1
                print(f"  [FAIL] {name}: {k} expected={v} got={got.get(k)}", file=sys.stderr)
                break
        else:
            print(f"  [ok] {name}", file=sys.stderr)
    for name, rows, must_contain, must_not_contain, prev_key in _MESSAGE_CASES:
        msg = build_decision(rows, prev_key, "2h")["message"]
        missing = [s for s in must_contain if s not in msg]
        present = [s for s in must_not_contain if s in msg]
        if missing or present:
            failures += 1
            if missing:
                print(f"  [FAIL] msg/{name}: missing {missing}", file=sys.stderr)
            if present:
                print(f"  [FAIL] msg/{name}: should not contain {present}", file=sys.stderr)
        else:
            print(f"  [ok] msg/{name}", file=sys.stderr)
    total = len(_SELFTEST) + len(_MESSAGE_CASES)
    if failures:
        print(f"edge-health-alert selftest: {failures} FAILED", file=sys.stderr)
        return 1
    print(f"edge-health-alert selftest: all {total} cases passed", file=sys.stderr)
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description="Edge-health alert decision from scan-edge-health.sh --json.")
    ap.add_argument("--prev-key-file", default=None)
    ap.add_argument("--window", default="2h")
    ap.add_argument("--selftest", action="store_true")
    args = ap.parse_args()

    if args.selftest:
        return _run_selftest()

    prev_key = ""
    if args.prev_key_file:
        try:
            with open(args.prev_key_file, encoding="utf-8") as fh:
                prev_key = fh.read().strip()
        except FileNotFoundError:
            prev_key = ""

    rows = []
    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            rows.append(json.loads(line))
        except json.JSONDecodeError:
            continue

    print(json.dumps(build_decision(rows, prev_key, args.window), ensure_ascii=False))
    return 0


if __name__ == "__main__":
    sys.exit(main())
