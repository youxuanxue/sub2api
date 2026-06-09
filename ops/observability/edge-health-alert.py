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

Trigger model: the ACTIONABLE set is the edges whose verdict is down / degraded /
unreachable / parse-error (a `thin` single-account edge is shown for context but is
chronic for most of the fleet, so it does NOT trigger on its own). The state key is a
stable digest of that set; the workflow alerts ONLY when the key changes vs the
previous run — new breakage, escalation, OR full recovery — so a multi-hour incident
does not re-spam every cycle, and a recovery posts a green all-clear.

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
_SEV_ORDER = {"down": 0, "unreachable": 0, "parse-error": 0, "degraded": 1}


def _num(v, default=0):
    try:
        return int(v)
    except (TypeError, ValueError):
        return default


def build_decision(rows: list, prev_key: str, window: str) -> dict:
    """Pure: verdict rows + previous state key -> alert decision + message."""
    actionable = [r for r in rows if r.get("verdict") in ACTIONABLE]
    # Stable key: sorted "verdict:edge" of the actionable set. Same incident => same
    # key => no re-alert; any change (new edge, recovery) flips it.
    key_parts = sorted(f"{r.get('verdict')}:{r.get('edge')}" for r in actionable)
    key = "|".join(key_parts)  # "" when the fleet is clean

    changed = key != (prev_key or "")
    should_alert = changed  # breakage, escalation, AND recovery all flip the key

    # `idle-thin` (a single-account edge with no traffic) is a SPOF too — surface it
    # in the thin context line, not silently dropped.
    thin = sorted(r.get("edge") for r in rows if r.get("verdict") in ("thin", "idle-thin"))
    healthy = sorted(
        r.get("edge") for r in rows if r.get("verdict") in ("healthy", "idle")
    )

    if not actionable:
        severity = "recovery" if (prev_key or "") else "ok"
    elif any(r.get("verdict") in ("down", "unreachable", "parse-error") for r in actionable):
        severity = "critical"
    else:
        severity = "warning"

    message = _format_message(actionable, thin, healthy, window, severity)
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
            return ("未配置账号", "该 edge 没有任何账号——需新增账号，或确认是否应下线该 edge")
        if sched == 0 and noavail == 0:
            return ("池刚空·尚未拒单(领先告警)",
                    "账号已全部不可调度但还没开始 429——下一批请求即将掉单，按下方排查三连逐账号定位")
        if sched == 0:
            return ("活跃掉单·账号全不可调度",
                    "正在大量 429、客户端已受影响（紧急）——按下方排查三连逐账号定位")
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


_VERDICT_ZH = {"down": "宕机", "unreachable": "不可达", "parse-error": "解析失败", "degraded": "降级"}


def _format_message(actionable: list, thin: list, healthy: list, window: str, severity: str) -> str:
    if not actionable:
        head = "✅ TokenKey 边缘健康 — 全部恢复（无 宕机/降级）"
    else:
        head = f"🔴 TokenKey 边缘健康 — {len(actionable)} 个 edge 需要处理"
    lines = [head, ""]

    grouped: dict = {}
    for r in sorted(actionable, key=lambda r: (_SEV_ORDER.get(r.get("verdict"), 9), r.get("edge") or "")):
        grouped.setdefault(r.get("verdict"), []).append(r)

    for verdict in ("down", "unreachable", "parse-error", "degraded"):
        rs = grouped.get(verdict)
        if not rs:
            continue
        lines.append(f"{_VERDICT_ZH.get(verdict, verdict).upper()}（{verdict}）:")
        for r in rs:
            cause, action = _classify(r)
            if verdict in ("unreachable", "parse-error"):
                lines.append(f"  • {r.get('edge')}  ← {cause}")
            else:
                sched = _num(r.get("schedulable_accounts"))
                total = _num(r.get("total_accounts"))
                served = _num(r.get("served_200"))
                noavail = _num(r.get("no_available_429"))
                ratio = r.get("served_ratio")
                wait = _num(r.get("wait_timeout"))
                # sched=N/M makes "no accounts" vs "accounts exist but unschedulable"
                # readable at a glance — the single most actionable missing field.
                tail = f"sched={sched}/{total} served200={served} noavail429={noavail}"
                if ratio is not None:
                    tail += f" ratio={ratio}"
                if wait:
                    tail += f" wait_to={wait}"
                lines.append(f"  • {r.get('edge')}  {tail}  ← {cause}")
            if action:
                lines.append(f"      ↳ {action}")

    if _needs_triage_runbook(actionable):
        lines.append("")
        lines.append("排查三连（sched=0 但账号存在时，逐一定位 N/M 中不可调度的账号）:")
        lines.append("  1. 冷却/窗口烧穿: 账号 active 但 5h/7d 窗口或上游限流冷却中 → 等恢复或按窗口剩余重排 priority；勿清冷却、勿扩容")
        lines.append("  2. schedulable=false 被钉死: OAuth 健康+无冷却却调度关闭 → admin UI 手动启用（无自愈）")
        lines.append("  3. OAuth 吊销: refresh 报成功仍 401 → 重新授权/换号，重刷无效")

    if thin:
        lines.append("")
        lines.append(f"薄边/SPOF（单账号，应补到 ≥2）: {', '.join(thin)}")
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
    "degraded:us5|down:uk2|down:uk3|down:us3",
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
    "degraded:us5|down:uk2|down:uk3|down:us3",
    {"should_alert": True, "severity": "critical"},
)
_case(
    "full recovery => green all-clear alert",
    [
        {"edge": "uk2", "verdict": "healthy", "schedulable_accounts": 2},
        {"edge": "us5", "verdict": "healthy", "schedulable_accounts": 3},
    ],
    "degraded:us5|down:uk2|down:uk3|down:us3",
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
    "chronic thin alone does NOT trigger",
    [
        {"edge": "uk1", "verdict": "thin", "schedulable_accounts": 1},
        {"edge": "us2", "verdict": "thin", "schedulable_accounts": 1},
        {"edge": "prod", "verdict": "healthy"},
    ],
    "",
    {"should_alert": False, "actionable_count": 0},
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


# --- message-content fixtures: lock the operator-value enrichment (cause tags, sched=N/M,
# the 排查三连 runbook) so a regression that strips guidance fails preflight, not prod ----
_MESSAGE_CASES = []


def _msg_case(name, rows, must_contain, must_not_contain=()):
    _MESSAGE_CASES.append((name, rows, must_contain, must_not_contain))


_msg_case(
    "leading-indicator down (sched=0, accounts exist, no 429 yet) => leading tag + N/M + runbook",
    [
        {"edge": "uk1", "verdict": "down", "schedulable_accounts": 0, "total_accounts": 2,
         "served_200": 3, "no_available_429": 0, "served_ratio": 1.0},
        {"edge": "prod", "verdict": "healthy", "schedulable_accounts": 14},
    ],
    ["池刚空·尚未拒单(领先告警)", "sched=0/2", "排查三连", "schedulable=false 被钉死", "OAuth 吊销"],
)
_msg_case(
    "active-outage down (sched=0, 429 flooding) => urgent tag, not the leading tag",
    [{"edge": "us3", "verdict": "down", "schedulable_accounts": 0, "total_accounts": 1,
      "served_200": 0, "no_available_429": 33748, "served_ratio": 0.0}],
    ["活跃掉单·账号全不可调度", "sched=0/1"],
    ["池刚空·尚未拒单"],
)
_msg_case(
    "no-accounts down (total=0) => 未配置账号, NO 排查三连 runbook",
    [{"edge": "fra1", "verdict": "down", "schedulable_accounts": 0, "total_accounts": 0,
      "served_200": 0, "no_available_429": 0}],
    ["未配置账号"],
    ["排查三连"],
)
_msg_case(
    "unreachable => 探测不可达 action, no runbook",
    [{"edge": "sg1", "verdict": "unreachable"}],
    ["探测不可达", "实例是否存活"],
    ["排查三连"],
)
_msg_case(
    "degraded => 池不足 tag",
    [{"edge": "us5", "verdict": "degraded", "schedulable_accounts": 3, "total_accounts": 3,
      "served_200": 11102, "no_available_429": 4841, "served_ratio": 0.696, "wait_timeout": 5656}],
    ["在服务但漏 429·池不足", "wait_to=5656"],
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
    for name, rows, must_contain, must_not_contain in _MESSAGE_CASES:
        msg = build_decision(rows, "", "2h")["message"]
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
