#!/usr/bin/env python3
"""Collect and render TokenKey edge capacity evidence."""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import re
import subprocess
import sys
import tempfile
from collections import Counter, defaultdict
from pathlib import Path
from typing import Any, Sequence

if __package__:
    from . import edge_capacity_probe as probe
else:
    import edge_capacity_probe as probe


DEFAULT_REPORT_DATE = "20260720"
CapacityReportError = probe.CapacityReportError


def _repo_root() -> Path:
    return Path(__file__).resolve().parents[2]


def _flatten_accounts(edge_documents: Sequence[dict[str, Any]]) -> list[dict[str, Any]]:
    flattened: list[dict[str, Any]] = []
    seen: set[tuple[str, int]] = set()
    for document in edge_documents:
        edge = str(document["edge"])
        for account in document["accounts"]:
            key = (edge, int(account["account_id"]))
            if key in seen:
                raise CapacityReportError(
                    f"duplicate account document: edge={edge} account_id={key[1]}"
                )
            seen.add(key)
            flattened.append({**account, "edge": edge})
    return flattened


def aggregate_type_groups(
    edge_documents: Sequence[dict[str, Any]],
) -> list[dict[str, Any]]:
    grouped: dict[tuple[str, int], list[dict[str, Any]]] = defaultdict(list)
    for account in _flatten_accounts(edge_documents):
        grouped[(account["platform"], int(account["channel_type"]))].append(account)

    output: list[dict[str, Any]] = []
    for (platform, channel_type), accounts in sorted(grouped.items()):
        values = [
            int(account["sources"]["F"]["pristine"]["cross_day"])
            for account in accounts
        ]
        required_accounts = min(3, len(accounts))
        all_edges = {account["edge"] for account in accounts}
        required_edges = 1 if len(accounts) == 1 else 2
        recommendation = 0
        supporters: list[dict[str, Any]] = []
        for candidate in sorted({value for value in values if value > 0}, reverse=True):
            candidate_supporters = [
                account
                for account, value in zip(accounts, values)
                if value >= candidate
            ]
            if len(candidate_supporters) < required_accounts:
                continue
            if len({account["edge"] for account in candidate_supporters}) < required_edges:
                continue
            recommendation = candidate
            supporters = candidate_supporters
            break

        supporter_edges = {account["edge"] for account in supporters}
        if recommendation and len(accounts) >= 3 and len(supporter_edges) >= 2:
            confidence = "高"
        elif recommendation and len(accounts) == 2 and len(supporter_edges) >= 2:
            confidence = "中"
        else:
            confidence = "暂定"

        output.append(
            {
                "platform": platform,
                "channel_type": channel_type,
                "account_count": len(accounts),
                "edge_count": len(all_edges),
                "values": sorted(values),
                "recommended": recommendation or None,
                "supporter_count": len(supporters),
                "supporter_edge_count": len(supporter_edges),
                "confidence": confidence,
            }
        )
    return output


def _metric_label(min_seconds: int) -> str:
    if min_seconds % 60 == 0:
        return f"C{min_seconds // 60}"
    return f"C{min_seconds}s"


def _default_report_path(min_seconds: int) -> str:
    return f"docs/ops/edge-capacity-report-{DEFAULT_REPORT_DATE}-{_metric_label(min_seconds).lower()}.md"


def _fmt_value(value: Any) -> str:
    return str(value) if value not in (None, "") else "-"


def _fmt_pct(value: Any) -> str:
    if value in (None, ""):
        return "-"
    numeric = float(value)
    return f"{numeric:.2f}%".replace(".00%", "%")


def render_report(edge_documents: Sequence[dict[str, Any]]) -> str:
    if not edge_documents:
        raise CapacityReportError("cannot render an empty edge document set")
    edge_ids = [str(document.get("edge", "")) for document in edge_documents]
    duplicate_edges = sorted(
        edge for edge, count in Counter(edge_ids).items() if count > 1
    )
    if duplicate_edges:
        raise CapacityReportError(f"duplicate edge documents: {duplicate_edges}")
    for document in edge_documents:
        probe.validate_document(document)

    edge_documents = sorted(edge_documents, key=lambda item: item["edge"])
    accounts = sorted(
        _flatten_accounts(edge_documents),
        key=lambda item: (item["edge"], int(item["account_id"])),
    )
    if not accounts:
        raise CapacityReportError("edge documents contain no active accounts")
    groups = aggregate_type_groups(edge_documents)
    min_seconds_values = {
        int(document["meta"]["min_sustain_seconds"])
        for document in edge_documents
    }
    if len(min_seconds_values) != 1:
        raise CapacityReportError("edge documents use different min_sustain_seconds")
    min_seconds = min_seconds_values.pop()
    requested_days_values = {
        int(document["meta"]["requested_days"]) for document in edge_documents
    }
    if len(requested_days_values) != 1:
        raise CapacityReportError("edge documents use different requested_days")
    analysis_days_values = {
        int(document["meta"]["analysis_days"]) for document in edge_documents
    }
    if len(analysis_days_values) != 1:
        raise CapacityReportError("edge documents use different analysis_days")
    snapshot_values = {
        str(document["meta"]["snapshot_at"]) for document in edge_documents
    }
    if len(snapshot_values) != 1:
        raise CapacityReportError("edge documents use different absolute windows")
    label = _metric_label(min_seconds)
    requested_days = requested_days_values.pop()
    analysis_days = analysis_days_values.pop()
    settlement_seconds = probe.SETTLEMENT_LAG_SECONDS
    snapshot_at = snapshot_values.pop()
    generated_utc = max(str(doc["meta"]["db_now_utc"]) for doc in edge_documents)

    lines = [
        "<!-- 由 ops/observability/edge_capacity_report.py 生成；请勿手工修改。 -->",
        "",
        f"# Edge 同类型账号持续 {min_seconds} 秒安全并发评估",
        "",
        f"生成时间（UTC）：`{generated_utc}`。共同证据截止（UTC）：`{snapshot_at}`。请求回看目标 `{requested_days}` 天；安全并发只使用 access/error 均完整的 `{analysis_days}` 天窗口，截止时间至少预留 `{settlement_seconds}` 秒等待异步日志落稳。",
        "",
        "## 同类型建议",
        "",
        "账号按 `(platform, channel_type)` 合并。建议值表示该类型**单个账号**的默认并发起点，不是把多个账号容量相加。",
        "",
        f"| 平台 | channel_type | 样本 | 账号级 F 跨天 {label}（升序） | 独立支持 | 同类型建议 | 置信度 |",
        "|---|---:|---:|---|---|---:|---|",
    ]
    for group in groups:
        values = ", ".join(str(value) for value in group["values"])
        recommended = _fmt_value(group["recommended"])
        support = (
            f"{group['supporter_count']} 个账号 / "
            f"{group['supporter_edge_count']} 个 Edge"
        )
        confidence = group["confidence"]
        if group["account_count"] == 2:
            confidence += "：仅 2 个账号"
        lines.append(
            f"| {group['platform']} | {group['channel_type']} | "
            f"{group['account_count']} 个账号 / {group['edge_count']} 个 Edge | "
            f"`{values}` | {support} | **{recommended}** | {confidence} |"
        )

    lines.extend(
        [
            "",
            "合并规则：样本不少于 3 个时，取至少 3 个独立账号证明过的最高 `F pristine Cross-day` 值，并要求支持账号覆盖至少 2 个 Edge；只有 2 个账号时，必须分属 2 个 Edge 并取两者共同证明值；只有 1 个账号时仅给暂定值。当前 cap 是本地调度快照，不作为上游能力上限。这个规则避免低流量或低 cap 账号把结果直接拖到最小值，也避免单个高样本代表整类能力。",
            "",
            "## F/H 解释",
            "",
            "- `F`（Forward）区间为 `[access 完成时间 - usage.duration_ms, access 完成时间)`，近似请求真正转发给上游、占用最终账号执行槽的时间。usage 只通过稳定 request id 精确关联到 access，关联失败不做最近时间猜配。F 是保守下界，也是推荐依据。",
            "- `H`（HTTP lifecycle）区间为 `[access 完成时间 - access.latency_ms, access 完成时间)`，还包含鉴权、路由、排队、重试、failover 和响应收尾，是上界参考，不能直接当上游执行并发。",
            "",
        ]
    )

    example = max(
        accounts,
        key=lambda account: (
            int(account["sources"]["H"]["pristine"]["observed"])
            - int(account["sources"]["F"]["pristine"]["observed"]),
            int(account["sources"]["H"]["pristine"]["observed"]),
        ),
    )
    example_f = example["sources"]["F"]["pristine"]["observed"]
    example_h = example["sources"]["H"]["pristine"]["observed"]
    lines.extend(
        [
            f"示例：`{example['edge']}/id={example['account_id']}` 的单次 {label} 为 `F/H = {example_f}/{example_h}`。这表示历史上分别至少有一个 {min_seconds} 秒区间保持 F={example_f}，以及至少有一个 {min_seconds} 秒区间保持 H={example_h}；两个最大值不保证发生在同一时段，不能直接相减。推荐仍从 F 取值。",
            "",
            "## 指标口径",
            "",
            f"- `Peak`：pristine 区间内的瞬时重建峰值，可能只持续毫秒或数秒。",
            f"- `Observed {label}`：至少 1 个 pristine 区间在并发 `>=N` 下连续保持 {min_seconds} 秒。",
            f"- `Repeated {label}`：至少 3 个独立最大连续区间分别保持 {min_seconds} 秒；一段长区间仍只算 1 次。",
            f"- `Cross-day {label}`：满足 Repeated，且区间开始日期覆盖至少 2 个 UTC 日期。",
            "- `Pristine`：既无归属于该账号的最终对客失败，也无被 retry/failover 隐藏的上游或账号鉴权失败。",
            "- 错误先用稳定 request id 关联最终 access；关联后仍无法归属账号的池级错误不摊给任何账号，也不打断账号 pristine 区间。",
            "",
            "## 账号级证据",
            "",
            f"| Edge | 账号 | 平台 / channel_type | 当前 cap | Peak F/H | 单次 {label} F/H | 三次复现 F/H | 跨天 F/H | F 关联率 |",
            "|---|---|---|---:|---:|---:|---:|---:|---:|",
        ]
    )
    for account in accounts:
        f_metric = account["sources"]["F"]["pristine"]
        h_metric = account["sources"]["H"]["pristine"]
        lines.append(
            f"| {account['edge']} | id={account['account_id']} | "
            f"{account['platform']} / {account['channel_type']} | "
            f"{account['configured_concurrency']} | "
            f"{f_metric['peak']} / {h_metric['peak']} | "
            f"{f_metric['observed']} / {h_metric['observed']} | "
            f"{f_metric['repeated']} / {h_metric['repeated']} | "
            f"{f_metric['cross_day']} / {h_metric['cross_day']} | "
            f"{_fmt_pct(account['coverage']['usage_match_pct'])} |"
        )

    lines.extend(
        [
            "",
            "## 覆盖与限制",
            "",
            "| Edge | access 留存起点（UTC） | F 关联率范围 | 日志采样 | 无效 access / usage 行 |",
            "|---|---|---:|---|---:|",
        ]
    )
    for document in edge_documents:
        account_coverages = [account["coverage"] for account in document["accounts"]]
        match_values = [
            float(coverage["usage_match_pct"])
            for coverage in account_coverages
            if coverage.get("usage_match_pct") is not None
        ]
        match_range = (
            f"{_fmt_pct(min(match_values))}-{_fmt_pct(max(match_values))}"
            if match_values
            else "-"
        )
        invalid_access = sum(int(item["invalid_access_rows"]) for item in account_coverages)
        invalid_usage = sum(int(item["invalid_usage_rows"]) for item in account_coverages)
        sampling = "开启" if document["meta"]["runtime_sampling_enabled"] else "关闭"
        lines.append(
            f"| {document['edge']} | {document['meta']['access_min_utc']} | "
            f"{match_range} | {sampling} | {invalid_access} / {invalid_usage} |"
        )

    lines.extend(
        [
            "",
            "- 无错结论只覆盖各 Edge 完全一致的 access/error 留存窗口，窗口不一致时拒绝合并。",
            "- access 只记录最终选中账号；更早失败 hop 有时间和账号归属，但没有完整尝试时长。",
            "- 当前 cap、channel_type 和可调度状态是生成时快照，不是历史版本。",
            "- probe 不采集账号名、邮箱或 credentials；报告和本地 raw JSON 都只用 `Edge + account_id` 标识账号。",
            "- 证据上界已预留日志落稳时间；异步 sink 极端积压或丢行仍可能超出该保护，且没有历史 drop 计数证明绝对完整。",
            "- 同一类型仍可能有 tier、配额、地区、模型组合和请求时长差异；类型建议是默认起点，不替代真实请求分档升压。",
            "",
            "本报告只读生成，没有修改任何线上账号或并发配置。",
            "",
        ]
    )
    return "\n".join(lines)


def _resolve_edges(repo_root: Path, raw_edges: str) -> list[str]:
    if raw_edges != "auto":
        edges = [item.strip() for item in raw_edges.split(",") if item.strip()]
    else:
        proc = subprocess.run(
            [
                "python3",
                str(repo_root / "deploy/aws/stage0/resolve-edge-target.py"),
                "--list-deployable",
            ],
            cwd=repo_root,
            check=True,
            capture_output=True,
            text=True,
        )
        edges = [line.strip() for line in proc.stdout.splitlines() if line.strip()]
    if not edges:
        raise CapacityReportError("no deployable edges resolved")
    invalid = [edge for edge in edges if not re.fullmatch(r"[a-z]{2,4}[0-9]+", edge)]
    if invalid:
        raise CapacityReportError(f"invalid edge ids: {invalid}")
    return sorted(set(edges))


def _require_complete_edge_set(
    documents: Sequence[dict[str, Any]], expected_edges: Sequence[str]
) -> None:
    actual = sorted(str(document.get("edge", "")) for document in documents)
    expected = sorted(expected_edges)
    if actual != expected:
        raise CapacityReportError(
            f"edge document set differs from deployable SSOT: expected={expected} actual={actual}"
        )


def _collect_edge(
    repo_root: Path,
    edge: str,
    days: int,
    min_seconds: int,
    timeout_seconds: int,
    snapshot_at: str,
) -> dict[str, Any]:
    run_probe = repo_root / "ops/observability/run-probe.sh"
    probe_script = repo_root / "ops/observability/probe-edge-capacity.sh"
    analyzer = repo_root / "ops/observability/edge_capacity_probe.py"
    cmd = [
        "bash",
        str(run_probe),
        "--target",
        f"edge:{edge}",
        "--script",
        str(probe_script),
        "--with",
        str(analyzer),
        "--env",
        f"EDGE_ID={edge}",
        "--env",
        f"DAYS={days}",
        "--env",
        f"MIN_SECONDS={min_seconds}",
        "--env",
        f"SNAPSHOT_AT={snapshot_at}",
        "--timeout-seconds",
        str(timeout_seconds),
        "--comment",
        f"read-only edge capacity report {edge}",
    ]
    proc = subprocess.run(
        cmd,
        cwd=repo_root,
        capture_output=True,
        text=True,
        timeout=max(180, timeout_seconds + 30),
    )
    if proc.returncode != 0:
        raise CapacityReportError(
            f"edge collection failed edge={edge} rc={proc.returncode}: "
            f"{proc.stderr[-4000:]}"
        )
    try:
        document = json.loads(proc.stdout)
    except json.JSONDecodeError as exc:
        raise CapacityReportError(
            f"edge={edge} returned invalid JSON: {proc.stdout[-2000:]}"
        ) from exc
    probe.validate_document(document, edge)
    return document


def _write_text_atomic(path: Path, content: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(
        "w", encoding="utf-8", dir=path.parent, delete=False
    ) as handle:
        handle.write(content)
        temporary = Path(handle.name)
    os.replace(temporary, path)


def _command_collect(args: argparse.Namespace) -> int:
    repo_root = _repo_root()
    edges = _resolve_edges(repo_root, args.edges)
    if args.edges != "auto":
        deployable_edges = _resolve_edges(repo_root, "auto")
        if edges != deployable_edges:
            raise CapacityReportError(
                f"collect requires all deployable edges: expected={deployable_edges} actual={edges}"
            )
    documents: list[dict[str, Any]] = []
    raw_dir = Path(args.raw_dir).resolve() if args.raw_dir else None
    snapshot_at = (
        dt.datetime.now(tz=dt.timezone.utc)
        - dt.timedelta(seconds=probe.SETTLEMENT_LAG_SECONDS)
    ).isoformat(timespec="microseconds")
    for edge in edges:
        print(f"collect edge={edge} status=starting", file=sys.stderr)
        document = _collect_edge(
            repo_root,
            edge,
            args.days,
            args.min_seconds,
            args.timeout_seconds,
            snapshot_at,
        )
        documents.append(document)
        print(
            f"collect edge={edge} status=success accounts={len(document['accounts'])}",
            file=sys.stderr,
        )

    report = render_report(documents)
    if raw_dir:
        raw_dir.mkdir(parents=True, exist_ok=True)
        for document in documents:
            _write_text_atomic(
                raw_dir / f"{document['edge']}.json",
                json.dumps(document, ensure_ascii=False, indent=2) + "\n",
            )
    output = Path(args.output or _default_report_path(args.min_seconds))
    if not output.is_absolute():
        output = repo_root / output
    _write_text_atomic(output, report)
    for group in aggregate_type_groups(documents):
        print(
            "type_recommendation "
            f"platform={group['platform']} channel_type={group['channel_type']} "
            f"recommended={_fmt_value(group['recommended'])} "
            f"supporters={group['supporter_count']} edges={group['supporter_edge_count']}"
        )
    print(f"report_path={output}")
    return 0


def _parse_inputs(values: Sequence[str]) -> list[dict[str, Any]]:
    documents: list[dict[str, Any]] = []
    for value in values:
        if "=" not in value:
            raise CapacityReportError(f"--input must be EDGE=PATH, got: {value}")
        edge, raw_path = value.split("=", 1)
        with Path(raw_path).open(encoding="utf-8") as handle:
            document = json.load(handle)
        probe.validate_document(document, edge)
        documents.append(document)
    return documents


def _command_render(args: argparse.Namespace) -> int:
    repo_root = _repo_root()
    expected_edges = _resolve_edges(repo_root, "auto")
    if args.raw_dir:
        raw_dir = Path(args.raw_dir)
        inputs = [f"{edge}={raw_dir / f'{edge}.json'}" for edge in expected_edges]
    else:
        inputs = args.input
    documents = _parse_inputs(inputs)
    _require_complete_edge_set(documents, expected_edges)
    min_seconds = int(documents[0]["meta"]["min_sustain_seconds"])
    output = Path(args.output or _default_report_path(min_seconds))
    if not output.is_absolute():
        output = repo_root / output
    _write_text_atomic(output, render_report(documents))
    print(f"report_path={output}")
    return 0


def _positive_int(raw: str) -> int:
    value = int(raw)
    if value <= 0:
        raise argparse.ArgumentTypeError("must be positive")
    return value


def _ssm_timeout(raw: str) -> int:
    value = _positive_int(raw)
    if not 30 <= value <= 2_592_000:
        raise argparse.ArgumentTypeError("must be between 30 and 2592000")
    return value


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)

    collect = subparsers.add_parser(
        "collect", help="collect all selected edges and update the Markdown report"
    )
    collect.add_argument(
        "--edges",
        default="auto",
        help="comma-separated edge ids or auto for all deployable edges",
    )
    collect.add_argument("--days", type=_positive_int, default=probe.DEFAULT_DAYS)
    collect.add_argument(
        "--min-seconds", type=_positive_int, default=probe.DEFAULT_MIN_SECONDS
    )
    collect.add_argument("--output")
    collect.add_argument("--raw-dir")
    collect.add_argument("--timeout-seconds", type=_ssm_timeout, default=600)
    collect.set_defaults(func=_command_collect)

    render = subparsers.add_parser(
        "render", help="render a report from previously collected edge JSON"
    )
    render_source = render.add_mutually_exclusive_group(required=True)
    render_source.add_argument("--input", action="append", metavar="EDGE=PATH")
    render_source.add_argument("--raw-dir")
    render.add_argument("--output")
    render.set_defaults(func=_command_render)
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    args = _build_parser().parse_args(argv)
    try:
        return int(args.func(args))
    except CapacityReportError as exc:
        print(f"edge_capacity_report: ERROR: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
