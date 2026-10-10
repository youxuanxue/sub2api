#!/usr/bin/env python3
"""Build Feishu weekly payment reports and attack-pattern alert decisions.

Consumes the JSON document emitted by probe-payment-billing-watch.sh.
Delivery reuses edge_health_delivery.post_feishu (signed webhook).
"""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import pathlib
import sys
from typing import Any

if __package__:
    from .edge_health_delivery import DeliveryError, _atomic_write, post_feishu
else:
    from edge_health_delivery import DeliveryError, _atomic_write, post_feishu

SCHEMA_VERSION = 1
PERIOD_LABELS = {
    "last_7d": "近7天",
    "prev_calendar_month": "上自然月",
    "current_calendar_month": "本自然月",
    "all_time": "全部累计",
}
CREDIT_LABELS = {
    "payment_fulfillment": "支付履约(外部实收对应入账)",
    "admin_opening": "管理员开户",
    "admin_adjust": "管理员加款",
    "admin_other": "管理员其他",
    "signup_gift": "注册赠额(不计外部实收)",
}


class PaymentWatchError(ValueError):
    """Probe payload or decision contract is invalid."""


def parse_snapshot(raw: str | dict[str, Any]) -> dict[str, Any]:
    if isinstance(raw, dict):
        data = raw
    else:
        text = raw.strip()
        if not text:
            raise PaymentWatchError("empty probe snapshot")
        try:
            data = json.loads(text)
        except json.JSONDecodeError as exc:
            raise PaymentWatchError(f"probe snapshot is not JSON: {exc}") from exc
    if not isinstance(data, dict):
        raise PaymentWatchError("probe snapshot must be an object")
    if data.get("schema_version") != SCHEMA_VERSION:
        raise PaymentWatchError(
            f"unsupported schema_version {data.get('schema_version')!r}, want {SCHEMA_VERSION}"
        )
    for key in (
        "period_totals",
        "completed_by_provider",
        "admin_credits",
        "completed_detail_7d",
        "anomaly_cancel_storms",
        "anomaly_rapid_creates",
        "anomaly_suspicious_completed",
        "providers",
    ):
        if key not in data or not isinstance(data[key], list):
            raise PaymentWatchError(f"missing or invalid list field: {key}")
    return data


def _money(value: Any) -> str:
    try:
        return f"${float(value):,.2f}"
    except (TypeError, ValueError):
        return "$?"


def _period_map(rows: list[dict[str, Any]]) -> dict[str, dict[str, Any]]:
    out: dict[str, dict[str, Any]] = {}
    for row in rows:
        if isinstance(row, dict) and isinstance(row.get("period"), str):
            out[row["period"]] = row
    return out


def build_weekly_report(snapshot: dict[str, Any], *, now: dt.datetime | None = None) -> str:
    now = now or dt.datetime.now(dt.timezone.utc)
    db_now = snapshot.get("db_now_utc") or now.strftime("%Y-%m-%dT%H:%M:%SZ")
    lines = [
        "TokenKey 支付/充值周报",
        f"窗口基准(UTC): {db_now}",
        f"生成(UTC): {now.strftime('%Y-%m-%dT%H:%M:%SZ')}",
        "",
        "【外部支付 COMPLETED】",
    ]
    periods = _period_map(snapshot["period_totals"])
    for key in ("last_7d", "prev_calendar_month", "current_calendar_month", "all_time"):
        row = periods.get(key, {})
        lines.append(
            f"- {PERIOD_LABELS[key]}: {int(row.get('completed_n') or 0)} 笔 / "
            f"{_money(row.get('completed_amount') or 0)} / "
            f"{int(row.get('completed_users') or 0)} 用户"
            f"（未成交 {int(row.get('non_completed_n') or 0)} 笔 {_money(row.get('non_completed_amount') or 0)}）"
        )

    lines.append("")
    lines.append("【COMPLETED 按通道】")
    for period in ("last_7d", "prev_calendar_month", "current_calendar_month", "all_time"):
        provider_rows = [
            r
            for r in snapshot["completed_by_provider"]
            if isinstance(r, dict) and r.get("period") == period
        ]
        label = PERIOD_LABELS[period]
        if not provider_rows:
            lines.append(f"- {label}：（无）")
            continue
        for row in provider_rows:
            lines.append(
                f"- {label} {row.get('provider_key')}/{row.get('payment_type')}: "
                f"{int(row.get('n') or 0)} 笔 {_money(row.get('amount') or 0)}"
            )

    lines.append("")
    lines.append("【站内入账 redeem_codes 正额】")
    for period in ("last_7d", "prev_calendar_month", "current_calendar_month", "all_time"):
        credit_rows = [
            r for r in snapshot["admin_credits"] if isinstance(r, dict) and r.get("period") == period
        ]
        label = PERIOD_LABELS[period]
        if not credit_rows:
            lines.append(f"- {label}：（无）")
            continue
        for row in credit_rows:
            kind = str(row.get("notes_kind") or "admin_other")
            lines.append(
                f"- {label} {CREDIT_LABELS.get(kind, kind)}: "
                f"{int(row.get('n') or 0)} 笔 {_money(row.get('amount') or 0)} / "
                f"{int(row.get('users') or 0)} 用户"
            )

    lines.append("")
    lines.append("【近7天 COMPLETED 明细】")
    details = [r for r in snapshot["completed_detail_7d"] if isinstance(r, dict)]
    if not details:
        lines.append("- （无）")
    else:
        for row in details[:30]:
            trade = row.get("payment_trade_no") or "(empty)"
            paid_after = row.get("paid_after_seconds")
            lag = f"{paid_after}s" if paid_after is not None else "?"
            lines.append(
                f"- #{row.get('id')} user={row.get('user_id')} {row.get('user_email')} "
                f"{row.get('provider_key')}/{row.get('payment_type')} {_money(row.get('amount') or 0)} "
                f"lag={lag} trade={trade}"
            )

    lines.append("")
    lines.append("【已启用支付通道】")
    providers = [r for r in snapshot["providers"] if isinstance(r, dict)]
    if not providers:
        lines.append("- （无）")
    else:
        for row in providers:
            flag = "ON" if row.get("enabled") else "OFF"
            lines.append(
                f"- #{row.get('id')} {row.get('provider_key')} {row.get('name')} "
                f"[{flag}] types={row.get('supported_types')} mode={row.get('payment_mode') or '-'}"
            )
    return "\n".join(lines)


def _fingerprint_anomalies(snapshot: dict[str, Any]) -> list[dict[str, Any]]:
    findings: list[dict[str, Any]] = []
    window = int(snapshot.get("anomaly_window_minutes") or 15)

    for row in snapshot["anomaly_cancel_storms"]:
        if not isinstance(row, dict):
            continue
        uid = row.get("user_id")
        findings.append(
            {
                "kind": "cancel_storm",
                "key": f"payment:cancel_storm:user:{uid}",
                "summary": (
                    f"用户 {uid} ({row.get('user_email')}) 在 {window} 分钟内 "
                    f"{int(row.get('n') or 0)} 笔取消/过期/失败，合计 {_money(row.get('sum_amount') or 0)}"
                ),
                "detail": row,
            }
        )

    for row in snapshot["anomaly_rapid_creates"]:
        if not isinstance(row, dict):
            continue
        uid = row.get("user_id")
        findings.append(
            {
                "kind": "rapid_create",
                "key": f"payment:rapid_create:user:{uid}",
                "summary": (
                    f"用户 {uid} ({row.get('user_email')}) "
                    f"{row.get('span_seconds')}s 内连续建单 {int(row.get('n') or 0)} 笔 "
                    f"（状态 {row.get('statuses')}）"
                ),
                "detail": row,
            }
        )

    for row in snapshot["anomaly_suspicious_completed"]:
        if not isinstance(row, dict):
            continue
        oid = row.get("id")
        findings.append(
            {
                "kind": "suspicious_completed",
                "key": f"payment:suspicious_completed:order:{oid}",
                "summary": (
                    f"订单 #{oid} user={row.get('user_id')} {row.get('user_email')} "
                    f"{row.get('provider_key')}/{row.get('payment_type')} {_money(row.get('amount') or 0)} "
                    f"入账间隔 {row.get('paid_after_seconds')}s，"
                    f"trade_no={'empty' if not row.get('payment_trade_no') else 'present'}"
                ),
                "detail": row,
            }
        )
    return findings


def build_alert_decision(
    snapshot: dict[str, Any],
    *,
    prev_keys: set[str],
) -> dict[str, Any]:
    findings = _fingerprint_anomalies(snapshot)
    active_keys = {f["key"] for f in findings}
    new_findings = [f for f in findings if f["key"] not in prev_keys]

    if not new_findings:
        return {
            "schema_version": SCHEMA_VERSION,
            "should_alert": False,
            "message": "",
            "active_keys": sorted(active_keys),
            "new_keys": [],
        }

    window = int(snapshot.get("anomaly_window_minutes") or 15)
    lines = [
        "TokenKey 支付异常告警",
        f"窗口: 近 {window} 分钟",
        f"新发现: {len(new_findings)} / 当前活跃: {len(findings)}",
        "",
    ]
    for finding in new_findings[:15]:
        lines.append(f"- [{finding['kind']}] {finding['summary']}")
    if len(new_findings) > 15:
        lines.append(f"- …另有 {len(new_findings) - 15} 条")
    lines.append("")
    lines.append("建议: 核对 EasyPay/Stripe 商户侧是否有真实收款；可疑账号冻结余额；确认 webhook 验签版本。")

    return {
        "schema_version": SCHEMA_VERSION,
        "should_alert": True,
        "message": "\n".join(lines),
        "active_keys": sorted(active_keys),
        "new_keys": [f["key"] for f in new_findings],
    }


def load_prev_keys(path: pathlib.Path | None) -> set[str]:
    if path is None or not path.is_file():
        return set()
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return set()
    keys = data.get("active_keys") if isinstance(data, dict) else None
    if not isinstance(keys, list):
        return set()
    return {str(k) for k in keys if isinstance(k, str) and k}


def save_keys(path: pathlib.Path, keys: list[str]) -> None:
    payload = json.dumps(
        {"schema_version": SCHEMA_VERSION, "active_keys": keys},
        ensure_ascii=False,
        separators=(",", ":"),
        sort_keys=True,
    )
    _atomic_write(path, payload + "\n")


def is_monday_shanghai(now: dt.datetime | None = None) -> bool:
    now = now or dt.datetime.now(dt.timezone.utc)
    shanghai = now.astimezone(dt.timezone(dt.timedelta(hours=8)))
    return shanghai.weekday() == 0


def deliver(
    *,
    snapshot: dict[str, Any],
    mode: str,
    state_file: pathlib.Path,
    dry_run: bool,
    webhook_url: str,
    signing_secret: str,
    force_weekly: bool = False,
    now: dt.datetime | None = None,
) -> dict[str, Any]:
    now = now or dt.datetime.now(dt.timezone.utc)
    results: dict[str, Any] = {"mode": mode, "actions": []}

    if mode in ("alert", "all"):
        prev = load_prev_keys(state_file)
        decision = build_alert_decision(snapshot, prev_keys=prev)
        results["alert"] = {
            "should_alert": decision["should_alert"],
            "new_keys": decision["new_keys"],
            "active_keys": decision["active_keys"],
        }
        if decision["should_alert"]:
            if dry_run:
                print(decision["message"])
                results["actions"].append("alert-dry-run")
            else:
                post_feishu(
                    decision["message"],
                    webhook_url=webhook_url,
                    signing_secret=signing_secret,
                )
                results["actions"].append("alert-delivered")
        if not dry_run:
            save_keys(state_file, decision["active_keys"])
            results["actions"].append("alert-state-saved")

    want_weekly = mode == "weekly" or (mode == "all" and (force_weekly or is_monday_shanghai(now)))
    if want_weekly:
        report = build_weekly_report(snapshot, now=now)
        results["weekly"] = {"chars": len(report)}
        if dry_run:
            print(report)
            results["actions"].append("weekly-dry-run")
        else:
            post_feishu(report, webhook_url=webhook_url, signing_secret=signing_secret)
            results["actions"].append("weekly-delivered")

    return results


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--snapshot",
        type=pathlib.Path,
        required=True,
        help="JSON file from probe-payment-billing-watch.sh",
    )
    parser.add_argument(
        "--mode",
        choices=("alert", "weekly", "all"),
        default="all",
        help="alert=attack patterns only; weekly=report only; all=both (weekly gated to Monday CST unless --force-weekly)",
    )
    parser.add_argument("--state-file", type=pathlib.Path, default=pathlib.Path(".payment-billing-watch-state/keys.json"))
    parser.add_argument("--dry-run", action="store_true")
    parser.add_argument(
        "--force-weekly",
        action="store_true",
        help="With --mode all/weekly, always emit the weekly report (ignore Monday gate)",
    )
    parser.add_argument("--print-report", action="store_true", help="Print weekly report text and exit")
    parser.add_argument("--print-decision", action="store_true", help="Print alert decision JSON and exit")
    args = parser.parse_args(argv)

    try:
        snapshot = parse_snapshot(args.snapshot.read_text(encoding="utf-8"))
    except (OSError, PaymentWatchError) as exc:
        print(f"payment-billing-watch: {exc}", file=sys.stderr)
        return 1

    if args.print_report:
        print(build_weekly_report(snapshot))
        return 0
    if args.print_decision:
        decision = build_alert_decision(snapshot, prev_keys=load_prev_keys(args.state_file))
        print(json.dumps(decision, ensure_ascii=False, indent=2))
        return 0

    try:
        result = deliver(
            snapshot=snapshot,
            mode=args.mode,
            state_file=args.state_file,
            dry_run=args.dry_run,
            webhook_url=os.environ.get("FEISHU_WEBHOOK_URL", ""),
            signing_secret=os.environ.get("FEISHU_SIGNING_SECRET", ""),
            force_weekly=args.force_weekly,
        )
    except DeliveryError as exc:
        print(f"payment-billing-watch delivery failed: {exc}", file=sys.stderr)
        return 1

    print(json.dumps(result, ensure_ascii=False, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
