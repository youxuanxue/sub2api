#!/usr/bin/env python3
"""Collect and render TokenKey plan-account usage reports for a calendar period."""

from __future__ import annotations

import argparse
import datetime as dt
import json
import re
import subprocess
import sys
from collections import defaultdict
from pathlib import Path
from typing import Any, Sequence
from zoneinfo import ZoneInfo

SHANGHAI = ZoneInfo("Asia/Shanghai")
KIND_CN = {
    "volcengine_agent_plan": "VolcEngine Agent Plan",
    "ali_token_plan": "Ali Token Plan",
    "qianfan_token_plan": "Qianfan Token Plan",
    "nvidia_build": "NVIDIA Build",
}
KIND_ORDER = [
    "volcengine_agent_plan",
    "ali_token_plan",
    "qianfan_token_plan",
    "nvidia_build",
]
PROBE_SCRIPT = "ops/observability/probe-plan-account-usage.sh"
DEFAULT_RAW_DIR = ".cache/plan-account-usage-raw"


class PlanUsageReportError(RuntimeError):
    """Deterministic failure while collecting or rendering the report."""


def _repo_root() -> Path:
    return Path(__file__).resolve().parents[2]


def parse_month(month: str) -> tuple[dt.datetime, dt.datetime]:
    """Return [start, end) Asia/Shanghai bounds for YYYY-MM."""
    if not re.fullmatch(r"\d{4}-\d{2}", month):
        raise PlanUsageReportError(f"month must be YYYY-MM, got {month!r}")
    year, mon = (int(part) for part in month.split("-"))
    start = dt.datetime(year, mon, 1, 0, 0, 0, tzinfo=SHANGHAI)
    if mon == 12:
        end = dt.datetime(year + 1, 1, 1, 0, 0, 0, tzinfo=SHANGHAI)
    else:
        end = dt.datetime(year, mon + 1, 1, 0, 0, 0, tzinfo=SHANGHAI)
    return start, end


def parse_bound(value: str, *, label: str) -> dt.datetime:
    """Parse an explicit bound as Asia/Shanghai unless offset is present."""
    text = value.strip().replace("T", " ")
    if re.fullmatch(r"\d{4}-\d{2}-\d{2}", text):
        return dt.datetime.fromisoformat(text).replace(tzinfo=SHANGHAI)
    try:
        parsed = dt.datetime.fromisoformat(text)
    except ValueError as exc:
        raise PlanUsageReportError(f"invalid {label}: {value!r}") from exc
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=SHANGHAI)
    return parsed


def format_psql_timestamptz(moment: dt.datetime) -> str:
    local = moment.astimezone(SHANGHAI)
    offset = local.utcoffset() or dt.timedelta(0)
    hours = int(offset.total_seconds() // 3600)
    return f"{local.strftime('%Y-%m-%d %H:%M:%S')}{hours:+03d}"


def default_output_path(period_start: dt.datetime) -> Path:
    stamp = period_start.astimezone(SHANGHAI).strftime("%Y%m")
    return Path(f"docs/ops/plan-account-usage-{stamp}.md")


def list_deployable_edges(repo_root: Path | None = None) -> list[str]:
    root = repo_root or _repo_root()
    cmd = [
        sys.executable,
        str(root / "deploy/aws/stage0/resolve-edge-target.py"),
        "--list-deployable",
    ]
    proc = subprocess.run(cmd, cwd=root, capture_output=True, text=True, check=False)
    if proc.returncode != 0:
        raise PlanUsageReportError(
            f"resolve-edge-target failed: {proc.stderr.strip() or proc.stdout.strip()}"
        )
    edges = [line.strip() for line in proc.stdout.splitlines() if line.strip()]
    if not edges:
        raise PlanUsageReportError("no deployable edges returned")
    return edges


def parse_probe_rows(stdout: str, *, target: str) -> list[dict[str, Any]]:
    rows: list[dict[str, Any]] = []
    for line in stdout.splitlines():
        line = line.strip()
        if not (line.startswith("{") and '"account_id"' in line and '"plan_kind"' in line):
            continue
        row = json.loads(line)
        row["target"] = target
        rows.append(row)
    return rows


def run_probe(
    *,
    target: str,
    period_start: dt.datetime,
    period_end: dt.datetime,
    repo_root: Path | None = None,
    timeout_seconds: int = 600,
) -> list[dict[str, Any]]:
    root = repo_root or _repo_root()
    start_lit = format_psql_timestamptz(period_start)
    end_lit = format_psql_timestamptz(period_end)
    cmd = [
        "bash",
        str(root / "ops/observability/run-probe.sh"),
        "--target",
        target,
        "--script",
        PROBE_SCRIPT,
        "--env",
        f"PERIOD_START={start_lit}",
        "--env",
        f"PERIOD_END={end_lit}",
        "--comment",
        f"plan account usage {target} {start_lit}..{end_lit}",
        "--compressed-output",
        "--timeout-seconds",
        str(timeout_seconds),
    ]
    proc = subprocess.run(cmd, cwd=root, capture_output=True, text=True, check=False)
    if proc.returncode != 0:
        err = (proc.stderr or proc.stdout or "").strip()
        raise PlanUsageReportError(f"run-probe failed target={target}: {err[-2000:]}")
    return parse_probe_rows(proc.stdout, target=target)


def collect(
    *,
    period_start: dt.datetime,
    period_end: dt.datetime,
    raw_dir: Path,
    include_prod: bool = True,
    edges: Sequence[str] | None = None,
    repo_root: Path | None = None,
    timeout_seconds: int = 600,
) -> dict[str, Any]:
    if period_end <= period_start:
        raise PlanUsageReportError("period_end must be after period_start")
    root = repo_root or _repo_root()
    edge_ids = list(edges) if edges is not None else list_deployable_edges(root)
    targets = (["prod"] if include_prod else []) + [f"edge:{edge}" for edge in edge_ids]

    raw_dir.mkdir(parents=True, exist_ok=True)
    documents: list[dict[str, Any]] = []
    for target in targets:
        rows = run_probe(
            target=target,
            period_start=period_start,
            period_end=period_end,
            repo_root=root,
            timeout_seconds=timeout_seconds,
        )
        doc = {
            "target": target,
            "period_start": period_start.isoformat(),
            "period_end": period_end.isoformat(),
            "account_count": len(rows),
            "accounts": rows,
        }
        documents.append(doc)
        out = raw_dir / f"{target.replace(':', '_')}.json"
        out.write_text(json.dumps(doc, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")

    manifest = {
        "period_start": period_start.isoformat(),
        "period_end": period_end.isoformat(),
        "targets": targets,
        "sampled_at_utc": dt.datetime.now(dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
    }
    (raw_dir / "manifest.json").write_text(
        json.dumps(manifest, indent=2, ensure_ascii=False) + "\n", encoding="utf-8"
    )
    return {"manifest": manifest, "documents": documents}


def load_raw_dir(raw_dir: Path) -> tuple[dict[str, Any], list[dict[str, Any]]]:
    manifest_path = raw_dir / "manifest.json"
    if not manifest_path.exists():
        raise PlanUsageReportError(f"missing {manifest_path}")
    manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    documents: list[dict[str, Any]] = []
    for target in manifest["targets"]:
        path = raw_dir / f"{target.replace(':', '_')}.json"
        if not path.exists():
            raise PlanUsageReportError(f"missing raw document for target={target}: {path}")
        documents.append(json.loads(path.read_text(encoding="utf-8")))
    return manifest, documents


def _account_created_at(row: dict[str, Any]) -> dt.datetime | None:
    value = row.get("created_at_utc") or row.get("created_at")
    if not value:
        return None
    text = str(value).replace("Z", "+00:00")
    if "+" not in text[10:] and not text.endswith("Z"):
        # Naive UTC from probe (created_at AT TIME ZONE 'UTC').
        text = f"{text}+00:00"
    try:
        moment = dt.datetime.fromisoformat(text)
    except ValueError:
        return None
    if moment.tzinfo is None:
        moment = moment.replace(tzinfo=dt.timezone.utc)
    return moment


def accounts_in_period(
    rows: Sequence[dict[str, Any]], *, period_end: dt.datetime
) -> list[dict[str, Any]]:
    """Drop accounts created at/after period_end (defense for cached raw JSON)."""
    end = period_end.astimezone(dt.timezone.utc)
    kept: list[dict[str, Any]] = []
    for row in rows:
        created = _account_created_at(row)
        if created is not None and created >= end:
            continue
        kept.append(row)
    return kept


def flatten_accounts(
    documents: Sequence[dict[str, Any]], *, period_end: dt.datetime | None = None
) -> list[dict[str, Any]]:
    rows: list[dict[str, Any]] = []
    for document in documents:
        for account in document.get("accounts", []):
            row = dict(account)
            row["target"] = document["target"]
            rows.append(row)
    if period_end is not None:
        return accounts_in_period(rows, period_end=period_end)
    return rows


def _fmt_ts(value: Any) -> str:
    if not value:
        return "-"
    text = str(value).replace("Z", "+00:00")
    try:
        moment = dt.datetime.fromisoformat(text)
    except ValueError:
        return text[:16]
    if moment.tzinfo is None:
        moment = moment.replace(tzinfo=dt.timezone.utc)
    return moment.astimezone(SHANGHAI).strftime("%Y-%m-%d %H:%M")


def _money(value: Any) -> str:
    return f"{float(value or 0):,.2f}"


def _num(value: Any) -> str:
    return f"{int(value or 0):,}"


def _tpm_m(value: Any) -> str:
    return f"{float(value or 0) / 1_000_000:.2f}"


def _sort_key(row: dict[str, Any]) -> tuple[int, str, int]:
    target = str(row.get("target") or "")
    return (0 if target == "prod" else 1, target, int(row.get("account_id") or 0))


def _window(cell: dict[str, Any] | None) -> tuple[str, str]:
    cell = cell or {}
    start = cell.get("window_start")
    end = cell.get("window_end")
    if not start or not end:
        return _money(cell.get("total_cost")), "-"
    return _money(cell.get("total_cost")), f"{_fmt_ts(start)} 至 {_fmt_ts(end)}"


def render_markdown(
    *,
    manifest: dict[str, Any],
    documents: Sequence[dict[str, Any]],
    sampled_at: str | None = None,
) -> str:
    period_start = dt.datetime.fromisoformat(manifest["period_start"]).astimezone(SHANGHAI)
    period_end = dt.datetime.fromisoformat(manifest["period_end"]).astimezone(SHANGHAI)
    rows = flatten_accounts(documents, period_end=period_end)
    sampled = sampled_at or str(manifest.get("sampled_at_utc") or "-")
    stamp = period_start.strftime("%Y-%m")

    lines: list[str] = []
    a = lines.append
    a(f"# Plan 账号计量报告（{stamp}）")
    a("")
    a(
        f"- **统计周期**：{period_start.strftime('%Y-%m-%d %H:%M')} 至 "
        f"{period_end.strftime('%Y-%m-%d %H:%M')}（Asia/Shanghai，含头不含尾）"
    )
    a(f"- **采样时间**：{sampled}")
    a("- **覆盖环境**：prod + deployable edges（由 `resolve-edge-target.py --list-deployable` 解析）")
    a(
        "- **账号类型**：VolcEngine Agent Plan / Ali Token Plan / "
        "Qianfan Token Plan / NVIDIA Build"
    )
    a("- **金额单位**：USD（表内不写美元符号，避免预览把金额当成数学公式）")
    a("- **TPM 单位**：million tokens / minute（表内 `peak_tpm_m`）")
    a("- **数据来源**：各环境本地 PostgreSQL `usage_logs`")
    a("- **复算入口**：`ops/observability/plan_account_usage_report.py` / skill `tokenkey-plan-account-usage-report`")
    a("")
    a("## 一、大白话口径说明")
    a("")
    a("这份表回答两件事：")
    a("")
    a(
        "1. **钱花在哪**：每个账号在本周期一共结算了多少；以及按 5 小时 / 7 天切出来的"
        "**非重叠周期**里，烧得最猛的那一档分别是多少。"
    )
    a("2. **峰值有多高**：周期内按「每分钟」统计的请求峰值（RPM）和 token 峰值（TPM，单位 million）。")
    a("")
    a("### 月度总量（month）")
    a("")
    a("- 周期内该账号所有 `usage_logs` 行的 `total_cost` / `actual_cost` 合计（不做额外 status 过滤）。")
    a("- 当统计窗是一个自然月时，**month 最大值 = 月度总量**。")
    a("")
    a("### 5h / 7d 最大值（非滚动、步进切片）")
    a("")
    a("- **不是**滚动窗口（不是每小时/每天挪一下）。")
    a("- 从周期起点起，按固定步长切成**互不重叠**的完整格子：")
    a("  - **5h**：步进 5 小时，只保留完整 5h 格；")
    a("  - **7d**：步进 7 天，月末不足 7 天的尾巴不进候选。")
    a("- 对每个账号，在这些格子里取 **total_cost 最大** 的一格，并给出该格起止时间。")
    a("- 这样更贴近上游「5 小时额度 / 周额度」一类周期（对齐到本报告周期起点；上游真实 reset 时刻可能仍有偏移）。")
    a("")
    a("### 峰值 RPM / TPM")
    a("")
    a("- 只看周期内 `usage_logs`。")
    a("- 按分钟桶：该分钟请求数 = RPM；input+output+cache token 合计 = TPM。")
    a("- **TPM 以 million 计**：原始 token 数 / 1,000,000，保留两位小数。")
    a("- 取周期内最大分钟值，并标注出现时间（北京时间）。")
    a("")
    a("### 其他注意")
    a("")
    a("- Edge 与 Prod 是各自本地账本，同名账号不能直接加总（ID 也不共用）。")
    a("- 某环境无匹配账号时明细为空，汇总计 0。")
    a("- 只纳入 **创建时间早于 period_end** 的账号；周期结束后新建的账号不进表。")
    a("")
    a("## 二、汇总")
    a("")
    a("| 环境 | 类型 | 账号数 | 月度 total_cost 合计 |")
    a("| --- | --- | ---: | ---: |")

    agg: dict[tuple[str, str], dict[str, float]] = defaultdict(
        lambda: {"n": 0.0, "month": 0.0}
    )
    for row in rows:
        env = "prod" if row.get("target") == "prod" else "edge"
        kind = str(row.get("plan_kind") or "")
        bucket = agg[(env, kind)]
        bucket["n"] += 1
        bucket["month"] += float((row.get("month_period") or {}).get("total_cost") or 0)

    for env in ("prod", "edge"):
        for kind in KIND_ORDER:
            bucket = agg.get((env, kind))
            if not bucket:
                continue
            a(
                f"| {env} | {KIND_CN[kind]} | {int(bucket['n'])} | {_money(bucket['month'])} |"
            )

    a("")
    a("## 三、明细表")
    a("")
    a("- **月度total / 月度actual / 请求数 / tokens**：整窗合计（tokens 为原始计数）")
    a("- **max5h / max7d**：非重叠步进格子里的最大 total_cost，及格子起止")
    a("- **peak_rpm**：周期内分钟请求峰值")
    a("- **peak_tpm_m**：周期内分钟 token 峰值，单位 million")
    a("")

    def section(title: str, kind: str) -> None:
        subset = [row for row in rows if row.get("plan_kind") == kind]
        if not subset:
            return
        a(f"### {title}")
        a("")
        a(
            "| 环境 | ID | 名称 | 创建CST | 月度total | 月度actual | 请求数 | tokens | "
            "max5h | max5h窗口 | max7d | max7d窗口 | peak_rpm | rpm时刻 | peak_tpm_m | tpm时刻 |"
        )
        a(
            "| --- | ---: | --- | --- | ---: | ---: | ---: | ---: | ---: | --- | "
            "---: | --- | ---: | --- | ---: | --- |"
        )
        for row in sorted(subset, key=_sort_key):
            month = row.get("month_period") or {}
            max5h = row.get("max_5h") or {}
            max7d = row.get("max_7d") or {}
            peaks = row.get("peaks") or {}
            cost5, win5 = _window(max5h)
            cost7, win7 = _window(max7d)
            created = row.get("created_at_utc")
            if created and "+" not in str(created) and not str(created).endswith("Z"):
                created = f"{created}+00:00"
            a(
                f"| {row.get('target')} | {row.get('account_id')} | {row.get('name')} | "
                f"{_fmt_ts(created)} | {_money(month.get('total_cost'))} | "
                f"{_money(month.get('actual_cost'))} | {_num(month.get('reqs'))} | "
                f"{_num(month.get('tokens'))} | {cost5} | {win5} | {cost7} | {win7} | "
                f"{_num(peaks.get('peak_rpm'))} | {_fmt_ts(peaks.get('peak_rpm_at'))} | "
                f"{_tpm_m(peaks.get('peak_tpm'))} | {_fmt_ts(peaks.get('peak_tpm_at'))} |"
            )
        a("")

    section("3.1 VolcEngine Agent Plan", "volcengine_agent_plan")
    section("3.2 Ali Token Plan", "ali_token_plan")
    section("3.3 Qianfan Token Plan", "qianfan_token_plan")
    section("3.4 NVIDIA Build", "nvidia_build")

    a("## 四、复算")
    a("")
    a("```bash")
    a(
        f"python3 ops/observability/plan_account_usage_report.py collect "
        f"--month {stamp} --output docs/ops/plan-account-usage-{period_start.strftime('%Y%m')}.md"
    )
    a(
        "python3 ops/observability/plan_account_usage_report.py render "
        "--raw-dir .cache/plan-account-usage-raw "
        f"--output docs/ops/plan-account-usage-{period_start.strftime('%Y%m')}.md"
    )
    a("```")
    a("")
    a("- 5h / 7d 为**非重叠步进切片**（步长分别 5h / 7d），对齐周期起点；不是 1 小时/1 天滚动。")
    a("- 周期末不足一整格的尾巴不参与 max5h/max7d 候选。")
    a("- TPM 展示为 million（原始值 / 1e6）。")
    a("- 未做跨环境去重或汇率换算。")
    a("")
    body = "\n".join(lines) + "\n"
    if "$" in body:
        raise PlanUsageReportError("rendered markdown must not contain dollar signs")
    return body


def write_report(path: Path, markdown: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(markdown, encoding="utf-8")


def _resolve_period(args: argparse.Namespace) -> tuple[dt.datetime, dt.datetime]:
    if args.month:
        if args.period_start or args.period_end:
            raise PlanUsageReportError("use either --month or --period-start/--period-end")
        return parse_month(args.month)
    if not args.period_start or not args.period_end:
        raise PlanUsageReportError("require --month or both --period-start and --period-end")
    return parse_bound(args.period_start, label="period-start"), parse_bound(
        args.period_end, label="period-end"
    )


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)

    collect_p = sub.add_parser("collect", help="Probe prod+edges and write markdown")
    collect_p.add_argument("--month", help="YYYY-MM Asia/Shanghai calendar month")
    collect_p.add_argument("--period-start", help="Inclusive start (Asia/Shanghai default)")
    collect_p.add_argument("--period-end", help="Exclusive end (Asia/Shanghai default)")
    collect_p.add_argument("--raw-dir", type=Path, default=Path(DEFAULT_RAW_DIR))
    collect_p.add_argument("--output", type=Path, help="Markdown output path")
    collect_p.add_argument(
        "--edges",
        default="auto",
        help="Comma-separated edge ids, or 'auto' (default)",
    )
    collect_p.add_argument("--skip-prod", action="store_true")
    collect_p.add_argument("--timeout-seconds", type=int, default=600)

    render_p = sub.add_parser("render", help="Render markdown from cached raw JSON")
    render_p.add_argument("--raw-dir", type=Path, default=Path(DEFAULT_RAW_DIR))
    render_p.add_argument("--output", type=Path, required=True)

    return parser


def main(argv: Sequence[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    root = _repo_root()

    if args.command == "collect":
        period_start, period_end = _resolve_period(args)
        edges: list[str] | None
        if args.edges == "auto":
            edges = None
        else:
            edges = [part.strip() for part in args.edges.split(",") if part.strip()]
        result = collect(
            period_start=period_start,
            period_end=period_end,
            raw_dir=(root / args.raw_dir).resolve()
            if not args.raw_dir.is_absolute()
            else args.raw_dir,
            include_prod=not args.skip_prod,
            edges=edges,
            repo_root=root,
            timeout_seconds=args.timeout_seconds,
        )
        output = args.output or default_output_path(period_start)
        if not output.is_absolute():
            output = root / output
        markdown = render_markdown(
            manifest=result["manifest"], documents=result["documents"]
        )
        write_report(output, markdown)
        kept = flatten_accounts(result["documents"], period_end=period_end)
        print(f"wrote {output} accounts={len(kept)}")
        return 0

    if args.command == "render":
        raw_dir = args.raw_dir if args.raw_dir.is_absolute() else root / args.raw_dir
        manifest, documents = load_raw_dir(raw_dir)
        output = args.output if args.output.is_absolute() else root / args.output
        markdown = render_markdown(manifest=manifest, documents=documents)
        write_report(output, markdown)
        period_end = dt.datetime.fromisoformat(manifest["period_end"])
        kept = flatten_accounts(documents, period_end=period_end)
        print(f"wrote {output} accounts={len(kept)}")
        return 0

    parser.error(f"unknown command {args.command}")
    return 2


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except PlanUsageReportError as exc:
        print(f"error: {exc}", file=sys.stderr)
        raise SystemExit(2) from exc
