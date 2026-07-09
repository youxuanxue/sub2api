#!/usr/bin/env python3
"""edge-health-alert.py — turn scan-edge-health.sh --json output into an alert
decision + a Feishu message, with cross-run dedup via a state key.

This is the *decision* half of manual edge-health triage; the *transport* halves
are scan-edge-health.sh --json (read-only SSM fleet sweep) and optional Feishu post
by an operator. Scheduled edge-health-watch GHA was retired 2026-07 — see
docs/spec-delta-cc-oauth-mimicry-fingerprint-scope.md. Keeping
the decision here (pure Python, no HTTP/AWS) makes it unit-testable with fixtures
(--selftest) and registerable in preflight, mirroring data_layer_capacity_verdict.py
and edge_health_verdict.py.

WHY (2026-06-07 incident): 4 edges went to 0 schedulable accounts and the one healthy
multi-account edge (us5) buckled under concentrated failover load; ~3445 clients ate
429s across two spikes — and ZERO alerts fired, because the truth tool
(scan-edge-health.sh, #640) only runs when a human remembers to run it. This turns
"someone has to go look" into "Feishu shouts the moment an edge dies".

Trigger model — Feishu pages on actionable-set changes only (🔴/✅):
  🔴 critical: confirmed active harm — degraded, or a `down` edge ACTIVELY rejecting
    (no_available_429>0) — OR unreachable / parse-error (cannot rule harm out).
  Leading `down` (sched=0, accounts exist, no_available_429==0) is NOT paged: multi-
  platform edges (e.g. us5 kiro/cc/openai) churn schedulable counts too often, and
  client harm is already covered by separate alerts (e.g. 无可用账号拒绝激增).
  ✅ recovery: actionable set cleared since the previous run.
Posture (thin / idle-thin / no-accounts) is NOT paged and NOT in the state key — see
`scan-edge-health.sh` for provisioning/SPOF backlog; 🔴/✅ footers still list thin
and no-accounts edges for context when an incident fires.
The persisted key is actionable-only (`a:down-active:<edge>` for active down,
`a:<verdict>:<edge>` otherwise; legacy `a:down:` leading entries ignored on read).
Same actionable set => no re-alert across 15-minute cycles.

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


def _is_actionable_for_alert(r: dict) -> bool:
    """Rows that may page Feishu. Leading `down` (pool empty, zero 429 yet) is excluded."""
    if r.get("verdict") not in ACTIONABLE:
        return False
    if r.get("verdict") == "down" and _down_kind(r) == "leading":
        return False
    return True


def _actionable_key_part(r: dict) -> str:
    """Stable dedup token for one alertable row. Active `down` uses down-active prefix."""
    verdict = r.get("verdict")
    edge = r.get("edge")
    if verdict == "down":
        return f"a:down-active:{edge}"
    return f"a:{verdict}:{edge}"


def _normalize_actionable_key(key: str) -> str:
    """Actionable-only state key; strip legacy `p:` posture and untracked `a:down:` entries."""
    parts = []
    for part in (key or "").split("|"):
        if not part or part.startswith("p:"):
            continue
        # Legacy caches used `a:down:` for leading indicators we no longer page; drop them.
        if part.startswith("a:down:") and not part.startswith("a:down-active:"):
            continue
        parts.append(part)
    return "|".join(sorted(parts))


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
    actionable = [r for r in rows if _is_actionable_for_alert(r)]
    key = "|".join(sorted(_actionable_key_part(r) for r in actionable))

    prev_actionable = _normalize_actionable_key(prev_key)
    changed = key != prev_actionable
    should_alert = changed

    thin = sorted(r.get("edge") for r in rows if r.get("verdict") in ("thin", "idle-thin"))
    no_accounts = sorted(r.get("edge") for r in rows if r.get("verdict") == "no-accounts")
    healthy = sorted(
        r.get("edge") for r in rows if r.get("verdict") in ("healthy", "idle")
    )

    prev_had_actionable = bool(prev_actionable)

    if not actionable:
        severity = "recovery" if prev_had_actionable else "ok"
    else:
        severity = "critical"

    leading_only = [r for r in rows if r.get("verdict") == "down" and _down_kind(r) == "leading"]

    if severity == "ok":
        message = ""
    else:
        message = _format_message(
            actionable, thin, no_accounts, healthy, window, severity, leading_only=leading_only
        )
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
# Short label per kind for the critical head's "(kinds present)" hint — derived from the
# actual set so the head never names a kind that isn't there.
_KIND_BRIEF = {
    "down-active": "宕机",
    "degraded": "降级",
    "unreachable": "不可达",
    "parse-error": "解析失败",
    "down-leading": "即将掉单",
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


def _format_message(actionable: list, thin: list, no_accounts: list, healthy: list,
                    window: str, severity: str, *, leading_only: list | None = None) -> str:
    grouped: dict = {}
    for r in actionable:
        grouped.setdefault(_row_kind(r), []).append(r)
    if leading_only:
        grouped.setdefault("down-leading", []).extend(leading_only)
    kinds_present = [k for k in _KIND_ORDER if k in grouped]

    if actionable:
        n = len(actionable)
        # Honest head: the critical set can mix confirmed harm (active-down / degraded)
        # with UNKNOWN states (unreachable / parse-error). The head must NOT blanket-claim
        # "客户端已受影响" / "正在掉单" across all N — that is the very overclaim this
        # refactor exists to kill (it would read as a confirmed outage on a probe flake).
        # The head names only the kinds ACTUALLY present (derived from `grouped`, not a
        # fixed legend); the precise per-edge truth lives in each section header.
        present = "/".join(_KIND_BRIEF[k] for k in kinds_present if k != "down-leading")
        head = f"🔴 TokenKey 边缘健康 — {n} 个 edge 需立即处理（{present}）"
    else:
        head = "✅ TokenKey 边缘健康 — 全部恢复（无宕机/降级）"
    lines = [head, ""]

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
        lines.append(f"薄边/SPOF（单账号，应补到 ≥2）: {', '.join(thin)}")
    if no_accounts:
        lines.append(f"未配置账号（0 账号、无被拒流量——补号或下线，决策前不按宕机告警）: {', '.join(no_accounts)}")
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
    "incident: 3 leading down + 1 degraded => critical (degraded only), alert from clean",
    [
        {"edge": "uk2", "verdict": "down", "schedulable_accounts": 0, "served_200": 712, "no_available_429": 0},
        {"edge": "uk3", "verdict": "down", "schedulable_accounts": 0, "served_200": 882, "no_available_429": 0},
        {"edge": "us3", "verdict": "down", "schedulable_accounts": 0, "served_200": 170, "no_available_429": 0},
        {"edge": "us5", "verdict": "degraded", "schedulable_accounts": 3, "served_200": 11102, "no_available_429": 4841, "served_ratio": 0.696, "wait_timeout": 5656},
        {"edge": "us2", "verdict": "thin", "schedulable_accounts": 1},
        {"edge": "prod", "verdict": "healthy", "schedulable_accounts": 14},
    ],
    "",
    {"should_alert": True, "severity": "critical", "actionable_count": 1},
)
_case(
    "same incident set unchanged => NO re-alert (legacy down keys ignored)",
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
    "escalation: a new active-down edge => alert (key changed)",
    [
        {"edge": "uk2", "verdict": "down"},
        {"edge": "uk3", "verdict": "down"},
        {"edge": "us3", "verdict": "down"},
        {"edge": "us6", "verdict": "down", "no_available_429": 12},
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
    "incident recovers but SPOF posture remains => green recovery, actionable key clears",
    [
        {"edge": "uk2", "verdict": "healthy", "schedulable_accounts": 2},
        {"edge": "us2", "verdict": "thin", "schedulable_accounts": 1},
    ],
    "a:down-active:uk2",
    {"should_alert": True, "severity": "recovery", "actionable_count": 0},
)
_case(
    "legacy prev key with p: posture suffix => same as actionable-only prev",
    [
        {"edge": "uk2", "verdict": "healthy", "schedulable_accounts": 2},
        {"edge": "us2", "verdict": "thin", "schedulable_accounts": 1},
    ],
    "a:down-active:uk2|p:thin:us2",
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
    "posture drift: thin set first seen => silent (ok, empty message)",
    [
        {"edge": "uk1", "verdict": "thin", "schedulable_accounts": 1},
        {"edge": "us2", "verdict": "thin", "schedulable_accounts": 1},
        {"edge": "prod", "verdict": "healthy"},
    ],
    "",
    {"should_alert": False, "severity": "ok", "actionable_count": 0},
)
_case(
    "chronic thin unchanged => silence",
    [
        {"edge": "uk1", "verdict": "thin", "schedulable_accounts": 1},
        {"edge": "us2", "verdict": "thin", "schedulable_accounts": 1},
        {"edge": "prod", "verdict": "healthy"},
    ],
    "",
    {"should_alert": False, "severity": "ok", "actionable_count": 0},
)
_case(
    "legacy p:-only prev key migrates silently => no false recovery page",
    [
        {"edge": "uk1", "verdict": "thin", "schedulable_accounts": 1},
        {"edge": "us2", "verdict": "thin", "schedulable_accounts": 1},
        {"edge": "prod", "verdict": "healthy"},
    ],
    "p:thin:uk1|p:thin:us2",
    {"should_alert": False, "severity": "ok", "actionable_count": 0},
)
_case(
    "uk2/uk3 zero-account posture unchanged => silence, NOT chronic critical",
    [
        {"edge": "uk2", "verdict": "no-accounts", "schedulable_accounts": 0, "total_accounts": 0},
        {"edge": "uk3", "verdict": "no-accounts", "schedulable_accounts": 0, "total_accounts": 0},
        {"edge": "prod", "verdict": "healthy"},
    ],
    "",
    {"should_alert": False, "severity": "ok", "actionable_count": 0},
)
_case(
    "posture resolved (account added) => silent when no actionable change",
    [
        {"edge": "uk1", "verdict": "healthy", "schedulable_accounts": 2},
        {"edge": "us2", "verdict": "thin", "schedulable_accounts": 1},
    ],
    "",
    {"should_alert": False, "severity": "ok", "actionable_count": 0},
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
    "pure leading-indicator down (pool empty, ZERO clients rejected) => silent, NOT paged",
    [
        {"edge": "us6", "verdict": "down", "schedulable_accounts": 0, "total_accounts": 1,
         "served_200": 639, "no_available_429": 0, "served_ratio": 1.0},
        {"edge": "prod", "verdict": "healthy", "schedulable_accounts": 14},
    ],
    "",
    {"should_alert": False, "severity": "ok", "actionable_count": 0},
)
_case(
    "leading down unchanged from legacy cache => silent migration, no false recovery",
    [
        {"edge": "us6", "verdict": "down", "schedulable_accounts": 0, "total_accounts": 1,
         "served_200": 639, "no_available_429": 0, "served_ratio": 1.0},
    ],
    "a:down:us6",
    {"should_alert": False, "severity": "ok", "actionable_count": 0},
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
    "leading down + an active down together => critical (active only in actionable set)",
    [
        {"edge": "us6", "verdict": "down", "schedulable_accounts": 0, "total_accounts": 1,
         "served_200": 639, "no_available_429": 0},
        {"edge": "us3", "verdict": "down", "schedulable_accounts": 0, "total_accounts": 1,
         "served_200": 0, "no_available_429": 33748},
    ],
    "",
    {"should_alert": True, "severity": "critical", "actionable_count": 1},
)


# --- message-content fixtures: lock the operator-value enrichment (cause tags, sched=N/M,
# the 排查三连 runbook) so a regression that strips guidance fails preflight, not prod ----
_MESSAGE_CASES = []


def _msg_case(name, rows, must_contain, must_not_contain=(), prev_key=""):
    _MESSAGE_CASES.append((name, rows, must_contain, must_not_contain, prev_key))


_msg_case(
    "leading-indicator only => no Feishu message (not paged)",
    [
        {"edge": "uk1", "verdict": "down", "schedulable_accounts": 0, "total_accounts": 2,
         "served_200": 3, "no_available_429": 0, "served_ratio": 1.0, "total_completed": 3},
        {"edge": "prod", "verdict": "healthy", "schedulable_accounts": 14},
    ],
    [],
    ["🟠", "即将掉单", "🔴", "池刚空"],
)
_msg_case(
    "mixed degraded + leading => critical head; leading still listed in body for context",
    [
        {"edge": "us6", "verdict": "down", "schedulable_accounts": 0, "total_accounts": 1,
         "served_200": 639, "no_available_429": 0, "served_ratio": 1.0, "total_completed": 639},
        {"edge": "us5", "verdict": "degraded", "schedulable_accounts": 3, "total_accounts": 3,
         "served_200": 11102, "no_available_429": 4841, "served_ratio": 0.696},
    ],
    ["🔴", "需立即处理", "降级·漏拒", "尚无客户端被拒", "即将掉单·预警"],
    ["2 个 edge 正在掉单", "🟠 TokenKey"],
)
_msg_case(
    "leading-indicator down WITH per-account diagnosis => silent when alone",
    [
        {"edge": "us6", "verdict": "down", "schedulable_accounts": 0, "total_accounts": 2,
         "served_200": 639, "no_available_429": 0, "served_ratio": 1.0, "total_completed": 639,
         "unschedulable": [
             {"id": 5, "name": "cc-us6a", "bucket": "flag",
              "hint": "schedulable=false 被钉死（账号健康却调度关闭）→ 一键启用：remediate-schedulable-pool.sh MODE=edge-oauth-pool"},
         ]},
    ],
    [],
    ["🟠", "按账号定位", "即将掉单"],
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
    "incident cleared with posture residue => ✅ recovery head + posture lines",
    [
        {"edge": "uk2", "verdict": "healthy", "schedulable_accounts": 2},
        {"edge": "us2", "verdict": "thin", "schedulable_accounts": 1},
    ],
    ["✅", "全部恢复", "薄边/SPOF（单账号，应补到 ≥2）: us2"],
    ["🔴", "🟡"],
    prev_key="a:down-active:uk2",
)
_msg_case(
    "actionable alert footer lists posture without (新) tags",
    [
        {"edge": "us3", "verdict": "down", "schedulable_accounts": 0, "total_accounts": 0,
         "served_200": 0, "no_available_429": 32},
        {"edge": "us5", "verdict": "thin", "schedulable_accounts": 1},
        {"edge": "us6", "verdict": "no-accounts", "schedulable_accounts": 0, "total_accounts": 0},
    ],
    ["🔴", "薄边/SPOF（单账号，应补到 ≥2）: us5", "未配置账号（0 账号", "us6"],
    ["(新)", "🟡"],
)
_msg_case(
    "unreachable => 探测不可达 action, no runbook",
    [{"edge": "sg1", "verdict": "unreachable"}],
    ["探测不可达", "实例是否存活"],
    ["排查三连"],
)
_msg_case(
    "unreachable-only head must NOT claim confirmed client impact (probe flake ≠ outage)",
    [{"edge": "sg1", "verdict": "unreachable"}, {"edge": "prod", "verdict": "healthy"}],
    ["🔴", "需立即处理"],
    ["客户端已受影响", "正在掉单"],  # unknown state — no blanket harm claim in the head
)
_msg_case(
    "degraded => 🔴 (active harm, clients bleeding 429s) + 池不足 tag + labelled window traffic",
    [{"edge": "us5", "verdict": "degraded", "schedulable_accounts": 3, "total_accounts": 3,
      "served_200": 11102, "no_available_429": 4841, "served_ratio": 0.696, "wait_timeout": 5656}],
    ["🔴", "降级·漏拒", "在服务但漏 429·池不足", "等待超时=5656", "近2h 服务200=11102"],
    ["🟠 TokenKey"],
)
_msg_case(
    "leading-indicator with measurable rate => silent when not bundled with critical",
    [{"edge": "us6", "verdict": "down", "schedulable_accounts": 0, "total_accounts": 1,
      "served_200": 600, "no_available_429": 0, "served_ratio": 1.0, "total_completed": 600}],
    [],
    ["⏱", "单/分", "下一单"],
)
_msg_case(
    "leading-indicator with ZERO traffic in window => silent when alone",
    [{"edge": "us6", "verdict": "down", "schedulable_accounts": 0, "total_accounts": 1,
      "served_200": 0, "no_available_429": 0, "total_completed": 0}],
    [],
    ["⏱", "无流量经过", "单/分"],
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
