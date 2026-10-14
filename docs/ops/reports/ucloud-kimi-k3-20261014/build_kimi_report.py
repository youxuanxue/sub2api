from pathlib import Path
import collections, datetime as dt, html, json, math, shutil, sys
import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt
from matplotlib import font_manager
import numpy as np

ROOT = (
    Path(sys.argv[1]).resolve()
    if len(sys.argv) > 1
    else Path(__file__).resolve().parent
)
SRC = (
    Path(sys.argv[2]).resolve()
    if len(sys.argv) > 2
    else Path(__file__).resolve().parent
)
ROOT.mkdir(parents=True, exist_ok=True)
entries = [
    json.loads(x)
    for x in (SRC / "run-output.jsonl").read_text().splitlines()
    if x.startswith("{")
]
meta = next(x for x in entries if "run_id" in x)
evidence = json.loads((SRC / "evidence.json").read_text())
usage = {x["request_id"].removeprefix("local:"): x for x in evidence["usage_logs"]}
rows = [x for x in entries if "phase" in x]
error_by_client = {
    e["client_request_id"]: e
    for e in evidence["error_logs"]
    if e.get("client_request_id")
}
for r in rows:
    r["server_error"] = error_by_client.get(r["client_request_id"])
    rid = r.get("server_request_id") or (r["server_error"] or {}).get("request_id")
    r["server_usage"] = usage.get(rid.removeprefix("local:") if rid else None)
formal = [r for r in rows if r["phase"] in ["warmup", "measured"]]
measured = [r for r in rows if r["phase"] == "measured"]
ok = [r for r in measured if r["success"]]
events = [x for x in entries if "phase" not in x and "usage_logs" not in x]
(ROOT / "raw.json").write_text(
    json.dumps(
        {
            "metadata": meta,
            "requests": rows,
            "execution_events": events,
            "server_evidence": evidence,
        },
        ensure_ascii=False,
        indent=2,
    )
)


def pct(xs, p):
    xs = sorted(x for x in xs if x is not None)
    if not xs or (len(xs) < 2 and p > 50):
        return None
    idx = (len(xs) - 1) * p / 100
    lo = int(idx)
    hi = math.ceil(idx)
    return xs[lo] + (xs[hi] - xs[lo]) * (idx - lo)


def fmt(v, d=2, suffix=""):
    return "—" if v is None else f"{v:.{d}f}{suffix}"


def seconds(v):
    return fmt(v / 1000 if v is not None else None, 2, "s")


def bounds(vs):
    return [min(vs), max(vs)] if vs else None


stages = []
for target, c in [
    (4096, 10),
    (12288, 8),
    (16384, 6),
    (32768, 6),
    (65536, 4),
    (131072, 2),
    (262144, 1),
    (524288, 1),
]:
    stage = [r for r in formal if r["target_tokens"] == target]
    samples = [r for r in stage if r["phase"] == "measured"]
    successful = [r for r in samples if r["success"]]
    known = [r for r in successful if "prompt_tokens" in r["usage"]]
    cacheknown = [
        r
        for r in known
        if "cached_tokens" in (r["usage"].get("prompt_tokens_details") or {})
    ]
    token_total = sum(r["usage"]["prompt_tokens"] for r in cacheknown)
    cached = sum(
        r["usage"]["prompt_tokens_details"]["cached_tokens"] for r in cacheknown
    )
    server = [r["server_usage"] for r in successful if r["server_usage"]]
    lat50 = pct([r["latency_ms"] for r in successful], 50)
    lat99 = pct([r["latency_ms"] for r in successful], 99)
    skipped = [e for e in events if e.get("skipped_target") == target]
    s = {
        "target": target,
        "label": f"{target // 1024}K @{c}",
        "concurrency": c,
        "n": len(samples),
        "success_n": len(successful),
        "warm_n": len(stage) - len(samples),
        "warm_ok": sum(r["success"] for r in stage if r["phase"] == "warmup"),
        "success_pct": 100 * len(successful) / len(samples) if samples else None,
        "ttft50": pct([r["ttft_ms"] for r in successful], 50),
        "ttft95": pct([r["ttft_ms"] for r in successful], 95),
        "first_any50": pct([r["first_any_ms"] for r in successful], 50),
        "tpot95": pct([r["tpot_ms"] for r in successful], 95),
        "tps50": pct([r["tokens_per_second"] for r in successful], 50),
        "lat95": pct([r["latency_ms"] for r in successful], 95),
        "lat_ratio": lat99 / lat50 if lat99 is not None and lat50 else None,
        "gateway50": pct([u["gateway_latency_ms"] for u in server], 50),
        "cache_token_pct": 100 * cached / token_total if token_total else None,
        "cache_request_pct": 100
        * sum(
            r["usage"]["prompt_tokens_details"]["cached_tokens"] > 0 for r in cacheknown
        )
        / len(cacheknown)
        if cacheknown
        else None,
        "cache_coverage": len(cacheknown),
        "tokens_range": bounds([r["usage"]["prompt_tokens"] for r in known]),
        "visible_tokens_range": bounds(
            [
                r["visible_tokens"]
                for r in successful
                if r.get("visible_tokens") is not None
            ]
        ),
        "errors": dict(
            collections.Counter(
                ("未收到响应头" if r["http_status"] is None else str(r["http_status"]))
                + (" 无正文/未完成" if r["http_status"] == 200 else "")
                for r in samples
                if not r["success"]
            )
        ),
        "accounts": dict(collections.Counter(str(u["account_id"]) for u in server)),
        "finish_reasons": dict(
            collections.Counter(r["finish_reason"] for r in successful)
        ),
        "skip": skipped,
    }
    s["result"] = (
        "未测量"
        if not samples
        else ("存在失败" if len(successful) < len(samples) else "样本全成功")
    )
    stages.append(s)
start = min(r["started_utc"] for r in formal)
end = max(
    dt.datetime.fromisoformat(r["started_utc"])
    + dt.timedelta(milliseconds=r["latency_ms"])
    for r in formal
)
runusage = [r["server_usage"] for r in formal if r["server_usage"]]
summary = {
    "metadata": meta,
    "start_utc": start,
    "end_utc": end.isoformat(),
    "measured_requests": len(measured),
    "measured_successes": len(ok),
    "formal_requests": len(formal),
    "formal_successes": sum(r["success"] for r in formal),
    "attributed_formal_requests": len(runusage),
    "formal_actual_cost_usd": sum(u["actual_cost"] for u in runusage),
    "formal_list_cost_usd": sum(u["total_cost"] for u in runusage),
    "all_key_actual_cost_usd": sum(u["actual_cost"] for u in usage.values()),
    "key_state": evidence["key_state"],
    "gateway_image": evidence.get("gateway_image"),
    "gateway_revision": evidence.get("gateway_revision"),
    "cleanup_verification": evidence.get("cleanup_verification"),
    "stages": stages,
}
(ROOT / "summary.json").write_text(json.dumps(summary, ensure_ascii=False, indent=2))
font_manager.fontManager.addfont("/System/Library/Fonts/Supplemental/Arial Unicode.ttf")
plt.rcParams.update(
    {"font.family": "Arial Unicode MS", "axes.unicode_minus": False, "font.size": 11}
)
labels = [s["label"] for s in stages]
x = np.arange(len(stages))
width = 0.35
fig, ax = plt.subplots(figsize=(15, 4.3), dpi=160)
for delta, key, color, label in [
    (-width / 2, "ttft95", "#8254ef", "正文 TTFT P95"),
    (width / 2, "lat95", "#f04452", "完整响应 P95"),
]:
    vals = [s[key] / 1000 if s[key] is not None else np.nan for s in stages]
    bars = ax.bar(x + delta, vals, width, color=color, label=label)
    for b, v in zip(bars, vals):
        if not np.isnan(v):
            ax.annotate(
                f"{v:.1f}",
                (b.get_x() + b.get_width() / 2, v),
                ha="center",
                va="bottom",
                fontsize=9,
                xytext=(0, 4),
                textcoords="offset points",
            )
ax.set_xticks(x, labels)
ax.set_ylabel("秒（仅成功样本）")
ax.set_title("延迟对比", loc="left", fontweight="bold", pad=16)
ax.legend(frameon=False, loc="upper left")
ax.grid(axis="y", linestyle="--", alpha=0.18)
ax.set_axisbelow(True)
for spine in ["top", "right"]:
    ax.spines[spine].set_visible(False)
ax.margins(y=0.2)
fig.tight_layout()
fig.savefig(ROOT / "latency.png", bbox_inches="tight")
plt.close(fig)
headers = [
    "测试项 / 并发",
    "成功 / 请求",
    "网关 P50",
    "成功率",
    "TTFT P50",
    "TTFT P95",
    "TPOT P95",
    "tok/s P50",
    "延迟 P99/P50",
    "缓存 Token / 请求",
    "观察",
]


def table_row(s):
    return [
        s["label"],
        f"{s['success_n']} / {s['n']}",
        fmt(s["gateway50"], 0, "ms"),
        fmt(s["success_pct"], 1, "%"),
        seconds(s["ttft50"]),
        seconds(s["ttft95"]),
        fmt(s["tpot95"], 1, "ms"),
        fmt(s["tps50"], 1),
        fmt(s["lat_ratio"], 2, "×"),
        fmt(s["cache_token_pct"], 1, "%") + " / " + fmt(s["cache_request_pct"], 1, "%"),
        s["result"],
    ]


def tbody(data):
    return "".join(
        "<tr>" + "".join(f"<td>{html.escape(str(v))}</td>" for v in row) + "</tr>"
        for row in data
    )


def thead(headers):
    return (
        "<thead><tr>" + "".join("<th>" + h + "</th>" for h in headers) + "</tr></thead>"
    )


headline = (
    f"正式测量 {len(ok)}/{len(measured)} 成功 · 分组 china_deployed · direct 路由"
)
window = f"{dt.datetime.fromisoformat(start).strftime('%Y-%m-%d %H:%M:%S')}–{end.strftime('%H:%M:%S')} UTC / {dt.datetime.fromisoformat(start).astimezone(dt.timezone(dt.timedelta(hours=8))).strftime('%H:%M:%S')}–{end.astimezone(dt.timezone(dt.timedelta(hours=8))).strftime('%H:%M:%S')} 北京时间"
notes = [
    "发起端：UCloud 北京二 2 核 4 GiB / 5 Mbps prod 主机 → https://api-cn.tokenkey.dev/v1/chat/completions → china_deployed 真实账号池。key 的 routing_mode=direct；包含 Caddy、TokenKey 网关与供应商路径。不是绕过网关调用供应商原始 key，也不代表中国终端用户的网络体验。",
    "生产环境并非隔离压测环境；同一时段可能有用户流量与其他模型测试，未扣除共享资源干扰。结果代表本次窗口的 china_deployed 路径。",
    "每档 1 次预热，再跑 2 波闭环并发（10/8/6/6/4/2/1/1）；每波结束后启动下一波，无客户端重试，每请求独立 HTTP 连接，包含建连/TLS。预热与校准均不计主表；不是持续到达率或最大容量测试。网关内部重试可能影响耗时。",
    "固定随机种子英文工程词汇语料；前缀在各请求和档位之间复用，尾部请求编号不同。校准 3000 个词的供应商 token 用量，再调节词数接近各目标；K=1024，实际输入 token 以响应 usage 为准。缓存自然混合，预热请求不保证已在所有供应账号命中。",
    "流式 Chat Completions；max_tokens=2048，stream_options.include_usage=true，未指定 temperature 或 thinking，保留模型/网关默认行为。任务为忽略参考语料、逐行复述固定英文句子 20 次；这是合成性能测试，不评估问答质量或长上下文检索能力。",
    "成功须 HTTP 200、非空正文、finish_reason 为 stop/length 且收到 [DONE]；正常 length 截断也计成功，不代表完整遵循了任务。HTTP 200 但思考耗尽输出额度而无正文计失败。",
    "正文 TTFT=请求发起至首个非空正文 delta；首个输出包含 reasoning。TPOT=(末正文时间−首正文时间)/(completion_tokens−reasoning_tokens−1)，tok/s 为其倒数；TPOT P95 是每请求平均生成耗时的分位数，不是逐 token 间隔分位数。若出现 reasoning 但未返回 reasoning token 数，不报告正文 TPOT。",
    "网关 P50 来自按响应 X-Request-ID 关联的 usage_logs.gateway_latency_ms（接受数据库 local: 前缀），表示鉴权与路由阶段耗时并扣除队列等待；不包含上游等待、流传输与响应尾处理。未实测独立连通基线，不填推算值。",
    "延迟、TPOT、速度均只统计成功测量样本；成功率分母包含所有正式测量请求。失败耗时与原因保存在原始数据。分位数采用线性插值，少于 2 个成功样本不报告高分位；各档样本较少，P95/P99 仅描述本轮，不是稳定尾延迟或 SLA 证明。",
    "缓存 Token 命中率=Σcached_tokens/Σprompt_tokens；请求命中率=缓存 token>0 的成功请求数/提供缓存字段的成功请求数。缺字段显示 —，不当成 0；网关归因与缓存字段覆盖另表列出。",
    "没有提供客户准入阈值，“观察”只描述本轮样本是否全成功，不作达标判定。未知/未测/预算跳过档位保留为空，不能由失败推断模型上下文硬上限。",
    "测试设置 4.5 美元管理员独立 key 记账额度及逐档保守费用检查，正式测量累计运行超过 20 分钟不再启动新档；单次连接/读取超时 150 秒，收到事件时另检查 180 秒墙钟，因此阻塞读取可能使实际耗时超过 180 秒。测试完成后停用独立 key。实际成本按本测试用户的倍率记账，不是公开报价，也不代表供应商套餐现金结算。",
]
supplements = [e for e in events if e.get("supplemental_target")]
if supplements:
    notes.append(
        "512K 最初被未计折扣的预算预留跳过；核对所有已入账请求的实际倍率为 0.3 后，在原 4.5 美元额度内补测，仍为 1 次预热 + 2 次正式请求，参数与测量方法不变。原始跳过及补测事件均保留。"
    )
extra_headers = [
    "目标 / 并发",
    "实际输入 tokens",
    "正文 tokens",
    "首个输出 P50",
    "完整响应 P95",
    "成功归因账号",
    "失败 HTTP",
    "结束原因",
    "缓存覆盖",
]
extra_rows = []
for s in stages:
    extra_rows.append(
        [
            s["label"],
            "—" if not s["tokens_range"] else "–".join(map(str, s["tokens_range"])),
            "—"
            if not s["visible_tokens_range"]
            else "–".join(map(str, s["visible_tokens_range"])),
            seconds(s["first_any50"]),
            seconds(s["lat95"]),
            json.dumps(s["accounts"]),
            json.dumps(s["errors"], ensure_ascii=False),
            json.dumps(s["finish_reasons"]),
            f"{s['cache_coverage']}/{s['success_n']}",
        ]
    )
failures = [r for r in formal if not r["success"]]
error_summary = collections.Counter(r.get("error", "unknown") for r in failures)
error_lines = [f"{n} 次（含预热）：{msg}" for msg, n in error_summary.items()]
for e in supplements:
    error_lines.append(
        f"{e['supplemental_target'] // 1024}K 档已完成预算校准后的补测，原 budget_guard 跳过记录已补齐。"
    )
server_errors = collections.Counter(
    (e["status_code"], e["upstream_status_code"]) for e in evidence["error_logs"]
)
error_lines += [
    f"服务端日志 {n} 条：status_code={status}，upstream_status_code={upstream}。该记录不等同于客户端实际收到这个 HTTP 状态，客户端超时可能参与最终状态归因。"
    for (status, upstream), n in server_errors.items()
]
accounts = "；".join(f"{a['id']}={a['name']}" for a in evidence["accounts"])
key_state = evidence["key_state"][0]
complete_stages = [s for s in stages if s["n"]]
slow = max(
    (s for s in stages if s["ttft50"] is not None),
    key=lambda s: s["ttft50"],
    default=None,
)
no_content = sum(
    r.get("error") == "no_visible_content_before_output_limit" for r in measured
)
truncated = sum(r["success"] and r["finish_reason"] == "length" for r in measured)
findings = [
    f"正式测量 {len(ok)}/{len(measured)} 成功（{100 * len(ok) / len(measured):.1f}%）；成功要求有正文，不只看 HTTP 状态。",
    f"4K @10 正文 TTFT P50 {seconds(stages[0]['ttft50'])}，P95 {seconds(stages[0]['ttft95'])}；正文速度 P50 {fmt(stages[0]['tps50'], 1)} tok/s。",
]
if slow:
    findings.append(
        f"本轮正文 TTFT 中位数最高的档位为 {slow['label']}：{seconds(slow['ttft50'])}。各档并发不同，不是控制并发不变的纯上下文对比。"
    )
if no_content or truncated:
    findings.append(
        f"{no_content} 次测量因输出额度在思考阶段耗尽而没有正文；另 {truncated} 次有正文但以 length 截断，按本轮流式可用性定义计成功。上述结果受 2048-token 总输出上限影响。"
    )
timeouts = sum("timed out" in r.get("error", "").lower() for r in measured)
if timeouts:
    findings.append(
        f"{timeouts} 次测量发生客户端读取超时；这表示在本轮超时预算内没有取得有效响应，不能据此判定模型不支持该上下文长度。"
    )
findings.append(
    "缓存为共享前缀预热后的混合状态；长上下文档位样本很少，结果不能外推为冷缓存性能、持续吞吐或 SLA。"
)
summary["findings"] = findings
(ROOT / "summary.json").write_text(json.dumps(summary, ensure_ascii=False, indent=2))
foot = f"预热 {sum(s['warm_n'] for s in stages)} 次；正式轮总成功 {summary['formal_successes']}/{len(formal)}；正式轮 usage 归因 {len(runusage)} 条。正式轮标准成本 ${summary['formal_list_cost_usd']:.6f}，实际记账 ${summary['formal_actual_cost_usd']:.6f}；含校准实际记账 ${summary['all_key_actual_cost_usd']:.6f}。独立 key #{meta['api_key_id']} 状态 {key_state['key_status']}，停用后鉴权实测 HTTP {evidence['cleanup_verification']['auth_http_status']}；凭据未导出。"
css = """*{box-sizing:border-box}body{margin:0;background:#f3f6fb;color:#263449;font-family:-apple-system,BlinkMacSystemFont,"PingFang SC",Arial,sans-serif}main{max-width:1740px;margin:32px auto;background:white;border-radius:16px;padding:42px 42px;box-shadow:0 4px 22px #24355408}h1{font-size:30px;margin:8px 0 14px}h2{font-size:20px;margin-top:32px}.eyebrow{color:#7555da;font-weight:600;letter-spacing:2px;font-size:12px}.muted{color:#7b8aa3;font-size:14px;line-height:1.8}.cards{display:flex;gap:18px;margin:25px 0}.card{flex:1;background:#f7f8fc;padding:18px 22px;border-radius:10px}.card strong{display:block;font-size:25px;margin-top:8px}.scroll{overflow-x:auto}table{width:100%;border-collapse:collapse;font-size:14px;white-space:nowrap}th{text-align:left;color:#8998b0;font-weight:500;padding:15px 9px;border-bottom:1px solid #e7edf5}td{padding:19px 9px;border-bottom:1px solid #edf1f7}td:nth-child(5),td:nth-child(6){color:#7150d2;font-weight:650}td:last-child{color:#66758c}img{max-width:100%;height:auto}li{line-height:1.8;margin:7px 0;font-size:14px;overflow-wrap:anywhere}a{color:#7150d2}.note{border-left:3px solid #af95f1;padding:12px 18px;background:#faf8ff;line-height:1.8;font-size:14px}.foot{margin-top:28px;border-top:1px solid #eee;padding-top:18px} @media(max-width:800px){main{margin:8px;padding:20px}.cards{flex-wrap:wrap}.card{min-width:40%}h1{font-size:25px}}"""
page = f"""<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="icon" href="data:,"><title>Kimi K3 · UCloud 国内 prod 性能实测</title><style>{css}</style></head><body><main><div class="eyebrow">TOKENKEY / PRODUCTION PERFORMANCE</div><h1>Kimi K3 · 国内 prod 性能实测报告</h1><div class="muted">{headline}<br>{window}</div><div class="cards"><div class="card">正式测量成功率<strong>{100 * len(ok) / len(measured):.1f}%</strong></div><div class="card">测量请求 / 成功<strong>{len(measured)} / {len(ok)}</strong></div><div class="card">目标上下文 / 最大并发<strong>4K–512K / 10</strong></div><div class="card">正式轮实际记账<strong>${summary["formal_actual_cost_usd"]:.4f}</strong></div></div><div class="note">小样本并发实测；延迟只统计成功请求。未设客户准入阈值，不作“达标”判定。实际输入、原始数据及方法见下。</div><h2>实测发现</h2><ul>{"".join("<li>" + html.escape(f) + "</li>" for f in findings)}</ul><h2>对比总览</h2><div class="scroll"><table>{thead(headers)}<tbody>{tbody([table_row(s) for s in stages])}</tbody></table></div><img src="latency.png" alt="各上下文档位正文首字 P95 与完整响应 P95 延迟对比"><h2>计量与归因</h2><div class="scroll"><table>{thead(extra_headers)}<tbody>{tbody(extra_rows)}</tbody></table></div><h2>失败与未测档位</h2><ul>{"".join("<li>" + html.escape(e) + "</li>" for e in error_lines) or "<li>本轮无失败请求。</li>"}{"".join("<li>" + html.escape(json.dumps(e, ensure_ascii=False)) + "</li>" for e in events if "skipped_target" in e)}</ul><h2>方法与边界</h2><ol>{"".join("<li>" + html.escape(n) + "</li>" for n in notes)}</ol><div class="muted">{html.escape(foot)}<br>{html.escape(accounts)}<br>运行镜像：{html.escape(evidence.get("gateway_image", "未采集"))}</div><div class="foot muted"><a href="overview.png">报告总览 PNG</a> · <a href="raw.json">逐请求原始指标</a> · <a href="summary.json">聚合 JSON</a> · <a href="report.md">Markdown 报告</a></div></main></body></html>"""
(ROOT / "report.html").write_text(page)
md = (
    ["# Kimi K3 · UCloud 国内 prod 性能实测", headline, window, ""]
    + ["- " + f for f in findings]
    + [
        "",
        "| " + " | ".join(headers) + " |",
        "|" + "|".join(["---"] * len(headers)) + "|",
    ]
)
md += ["| " + " | ".join(table_row(s)) + " |" for s in stages]
md += [
    "",
    "![延迟对比](latency.png)",
    "",
    "## 计量与归因",
    "",
    "| " + " | ".join(extra_headers) + " |",
    "|" + "|".join(["---"] * len(extra_headers)) + "|",
]
md += ["| " + " | ".join(r) + " |" for r in extra_rows]
md += (
    ["", "## 失败与未测档位", ""]
    + (error_lines or ["本轮无失败请求。"])
    + [json.dumps(e, ensure_ascii=False) for e in events if "skipped_target" in e]
)
md += ["", "## 方法与边界", ""] + [f"{i + 1}. {n}" for i, n in enumerate(notes)]
md += [
    "",
    foot,
    "",
    accounts,
    "",
    "运行镜像：" + evidence.get("gateway_image", "未采集"),
    "",
    "[完整网页](report.html) · [总览 PNG](overview.png) · [原始指标](raw.json) · [汇总 JSON](summary.json)",
]
(ROOT / "report.md").write_text("\n".join(md) + "\n")
fig = plt.figure(figsize=(20, 12), dpi=150, facecolor="#f4f6fb")
fig.text(
    0.045,
    0.955,
    "Kimi K3 · UCloud 国内 prod 性能实测",
    fontsize=24,
    color="#263449",
    weight="bold",
)
fig.text(0.045, 0.917, headline + "  |  " + window, fontsize=12, color="#718198")
fig.text(0.045, 0.877, "对比总览", fontsize=15, weight="bold", color="#263449")
ax = fig.add_axes([0.035, 0.40, 0.93, 0.45])
ax.axis("off")
table = ax.table(
    cellText=[table_row(s) for s in stages],
    colLabels=headers,
    cellLoc="center",
    colWidths=[0.10, 0.08, 0.075, 0.07, 0.08, 0.08, 0.08, 0.075, 0.10, 0.16, 0.10],
    bbox=[0, 0, 1, 1],
)
table.auto_set_font_size(False)
table.set_fontsize(10)
for (ri, ci), cell in table.get_celld().items():
    cell.set_edgecolor("#e8edf5")
    cell.set_linewidth(0.5)
    cell.set_facecolor("white")
    cell.get_text().set_color("#263449" if ri else "#8192ad")
    if ri and ci in (4, 5):
        cell.get_text().set_color("#7852dd")
        cell.get_text().set_weight("bold")
    if (
        ri
        and ci == 3
        and stages[ri - 1]["success_pct"] is not None
        and stages[ri - 1]["success_pct"] < 100
    ):
        cell.get_text().set_color("#dc3b4a")
ax = fig.add_axes([0.07, 0.12, 0.88, 0.23], facecolor="white")
for delta, key, col, lab in [
    (-0.18, "ttft95", "#8254ef", "正文 TTFT P95"),
    (0.18, "lat95", "#f04452", "完整响应 P95"),
]:
    ax.bar(
        x + delta,
        [s[key] / 1000 if s[key] is not None else np.nan for s in stages],
        0.36,
        label=lab,
        color=col,
    )
ax.set_xticks(x, labels)
ax.set_ylabel("秒")
ax.set_title("延迟对比（仅成功样本）", loc="left", pad=10)
ax.grid(axis="y", linestyle="--", alpha=0.2)
ax.set_axisbelow(True)
ax.legend(frameon=False, loc="upper left")
ax.spines[["top", "right"]].set_visible(False)
fig.text(
    0.045,
    0.055,
    "2 波并发 / 档；每档另 1 次预热，排除主表。默认思考，输出上限 2048 tokens。小样本分位数，未设 SLA 阈值。",
    fontsize=11,
    color="#718198",
)
fig.text(
    0.045,
    0.029,
    "TTFT 为首个正文；TPOT 扣除 reasoning tokens；网关耗时来自 usage_logs。— 表示无有效样本或字段缺失。",
    fontsize=11,
    color="#718198",
)
fig.savefig(ROOT / "overview.png", bbox_inches="tight")
plt.close(fig)
for name in [
    "kimi-perf.py",
    "build_kimi_report.py",
    "run-output.jsonl",
    "evidence.json",
]:
    if (SRC / name).resolve() != (ROOT / name).resolve():
        shutil.copy(SRC / name, ROOT / name)
print(json.dumps(summary, ensure_ascii=False, indent=2))
