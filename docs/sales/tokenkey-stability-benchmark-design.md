# TokenKey 稳定性实测宣传 · 可执行实验设计

> 目标：做一个"TokenKey 比大厂/比同行更稳"的实测，**经得起社区质疑**。
> 原则：用社区已信任的同口径方法对比（不自创标准），再在三个行业空白维度上做到别人做不到，并用**第三方中立监测**破解"既当运动员又当裁判"陷阱。
> 本文基于 2026-06-08 深度调研（5 路搜索 → 22 源 → 对抗式验证 22 条结论）+ TokenKey 代码事实核对。

---

## 0. 一句话策略

> **同口径站上社区的擂台（用它们的中立监测站和数学题验真口径），再亮出三张同行没有的牌：P99 尾延迟、failover 量化切换时间、长任务不中断率。**

---

## 1. 调研结论：同行怎么测的（决定我们怎么打）

### 1.1 软文层（反面教材，不参与对比）
- `LMU-AI/claude-api-relay-review`、`zzsting88/relayAPI` 等：只有**价格表**可信，稳定性是"没遇到过 502"式定性体感 + 星级打分。
- 典型陷阱（我们必须全部规避）：
  - **单一粗糙指标**——把稳定性等同于"一个月内 502/400 报错频率"。
  - **测试设计自相矛盾**——号称测一周，却用一个月窗口；不具名工具、不公开脚本/时长。
  - **运动员兼裁判**——带 aff 返利链接、把自家/主用平台排第一、附录只给那家配置教程。
  - **只报均值不报尾部**——均值掩盖卡顿，用户投诉全来自尾延迟。

### 1.2 社区事实标准层（我们要同口径对齐的对象）⭐
社区已有被信任的工程化方法论，**这是我们的对标基线**：

| 来源 | 方法论要点 |
|---|---|
| **`check-cx`**（BingZi-233，开源，Next.js+Supabase） | 后台轮询健康检查；默认 60s 间隔（15–600s 可调）；支持 OpenAI/Gemini/Anthropic 的 Chat Completions + Responses 端点；记录实时延迟 + Ping + 7/15/30 天可用性；只读状态 API `/api/v1/status` |
| **Help AIO 监测站**（基于同类方法） | **真实 CLI 请求非模拟**；**用数学题验证答案防缓存作假/防降智**（答错/超时/异常=失败）；首字 **<6s 正常 ≥6s 判延迟**；24h 不间断（~235 次/期）；呈现综合可用率% + 每模型可用率 + **首字时间趋势(最快/最慢/均值)** + 走势图 |
| **`check.linux.do`** | L 站官方模型中转状态检测，测 OpenAI/Gemini/Anthropic 可用性 + 延迟（社区默认裁判） |
| **降智/保真口径** | 标准测试集 AIME 2025 / GPQA（实测有中转站数学准确率掉 40%）；保真检测脚本用结构化输出验真 |
| **`new-api`** | 内置渠道健康检测（二元模式）+ 多渠道容错切换（但**只切不计时**，issue #3454/#1729） |
| **自建监控** | Upptime（GitHub Actions 无服务器）、Uptime Kuma、UptimeRobot |

### 1.3 全行业空白（我们的碾压点）
- **failover/容灾人人吹（"zero downtime""automatic failover"），无一家给量化切换时间/MTTR。**
- 几乎无人报 **P99 尾延迟**（社区监测站也只报均值）。
- 几乎无人做 **≥4h soak 长任务不中断**测试（10 分钟测不出连接池耗尽）。

---

## 2. 业界成熟方法论（武装我们的指标定义）

经对抗式验证（一手学术源 OSDI'24 DistServe/Sarathi-Serve、Google SRE Book、The Tail at Scale 交叉印证）：

- **流式专属指标**，不是 RPS：
  - **TTFT**（首 token 时间）——用户感知的"响应性"。
  - **ITL**（token 间延迟）——>100ms 肉眼可见卡顿；过载时 ITL 比 TTFT 先飙升，可作早期预警。
  - **TPS**（tokens/sec）——响应长度可变，RPS 是误导性代理。
  - **goodput**——同时满足所有 SLO 的请求占比；返回 200 但超时的**不计入**。
- **一律按 P99 报，不报均值**（200ms 均值的系统 P99 可能 4s；SLA 常写 P95，P99 告警）。
- **soak ≥ 4 小时**才能暴露内存泄漏/KV cache 碎片/连接池耗尽。
- 通用工具（k6/Locust）**默认不懂流式**——k6 需自定义 SSE chunk 解析，Locust 受 GIL 影响会人为夸大 ITL；需改造或自写 SSE 探针。

---

## 3. TokenKey 已具备的能力（实验直接复用，无需新建）

| 能力 | 代码/脚本位置 | 用途 |
|---|---|---|
| **P99 延迟统计** | `ops/observability/parse-access-log.py`（输出 p50/p90/p95/p99 latency_ms） | 直接产出尾延迟，社区监测站没有 |
| **edge 健康比值** | `ops/observability/scan-edge-health.sh` + `edge_health_verdict.py` | served_200 : no_available_429 + 可调度账号数 |
| **count_tokens failover** | `backend/internal/service/gateway_service_tk_count_tokens_failover.go` | failover 演示的代码依据 |
| **空池快速失败 429（非 503）** | `ratelimit_service_tk_downstream_no_available_test.go` | 不挂死连接的工程细节 |
| **429 分类探针** | `ops/observability/probe-429-classify.sh` | 区分限流 vs 空池 vs 不支持模型名 |
| **流量窗口探针** | `ops/observability/probe-fleet-traffic-window.sh` | 重建每分钟 RPM/会话/并发 |

> 注：以上为代码事实核对所得（2026-06-08）。failover 切换时间实验需在此基础上新增一个"主动打死节点 + 计时"的混沌步骤（见 §5.2）。

---

## 4. 实验对象与口径

### 4.1 对比组
- **TokenKey**（被测主体）
- **官方 API**（Anthropic 直连，作为"天花板"基线——证明我们贴近官方）
- **2–3 家主流同行中转**（用其公开可购买的同档位套餐；记录购买时间/套餐/单价以备质疑）

> ⚠️ 不点名抹黑：同行可匿名为"中转 A/B/C"，只公开方法和数据，让结论自己说话。

### 4.2 测试模型（与社区监测站同款，保证可对比）
- `claude-opus-4-8`、`claude-sonnet-4-6`（与 Help AIO 同款）
- 可选 `gpt-5.5` 做跨平台对照

### 4.3 验真口径（直接沿用社区标准，杜绝"你自己说的"）
- **真实客户端请求**（Claude Code 形 `/v1/messages`），非 Request 模拟。
- **数学题验证答案**：答错/超时/异常 = 失败（防缓存作假 + 防降智，与 Help AIO 同口径）。
- 降智单独跑一轮 **AIME 2025 子集**，报准确率（证明我们不掉点）。

---

## 5. 测什么 + 怎么测

### 5.1 基础可用性（同口径，站社区擂台）
| 指标 | 方法 | 判定 |
|---|---|---|
| 综合可用率% | 24×7 真实请求轮询（60s 间隔，对齐 check-cx） | 答对且未超时=成功 |
| 每模型可用率% | 按模型分桶 | 同上 |
| 首字时间趋势 | 记录每次 TTFT | <6s 正常 / ≥6s 延迟（对齐 Help AIO 阈值） |
| 429 率 | 统计限流响应占比 | 用 `probe-429-classify.sh` 区分真限流 vs 空池 |

### 5.2 🎯 failover 量化切换时间（行业空白，我们的杀手锏）
**混沌实验，全程录屏 + 数据曲线：**
1. 稳态打流（恒定并发，记录成功率基线）。
2. **人为打死一个边缘节点 / 拔掉一个上游账号**（记录 T0）。
3. 观测成功率曲线的下陷与恢复，记录**恢复到基线成功率的时间 = 切换时间（MTTR）**。
4. 对照组：同行/单机网关在同样操作下的表现（多数会持续 5xx 直到人工介入）。

> 输出："拔掉一个节点后，TokenKey 在 N 秒内自愈，成功率曲线几乎无感；对照组持续报错 X 分钟。" —— 这是同行根本给不出数字的演示。

### 5.3 P99 尾延迟（社区只报均值，我们报尾部）
- 用 `parse-access-log.py` 直接产出 TTFT 与端到端的 p50/p95/p99。
- 对照明确 SLO（如 P99 TTFT < 2.5s），公开是否达标。

### 5.4 长任务 / soak（≥4h，打脸短窗口）
- 连续 4h（可延至 24h）跑 **extended thinking + 1M context** 长任务流。
- 指标：**SSE 流式中断率 / 长连接保持成功率 / 内存与连接池是否稳定**。
- 这一项很多同行直接跑不通（原生保真缺失），是 0/1 的冲击力。

### 5.5 原生保真 0/1（布尔值，最有冲击力）
- extended thinking + 1M context + prompt caching + count_tokens 组合能否全跑通。
- 同行走 OpenAI 兼容协议大多丢这些特性——结果是"能/不能"，不是百分比。

---

## 6. 用什么工具

| 层 | 工具 | 说明 |
|---|---|---|
| 持续可用性 | `check-cx`（自部署一份）或接入 Help AIO | **用社区同款工具**，数字天然可比、可复现 |
| 压测/soak | 改造版 k6（自定义 SSE 解析 + 手动时间戳）或自写 Python SSE 探针 | 通用工具默认不懂流式，必须改造 |
| 指标计算 | `parse-access-log.py`（P99）+ Prometheus | 复用现有能力 |
| failover 计时 | 自写混沌脚本（打死节点 + 秒级采样成功率） | 行业无现成，自建 |
| 呈现 | 状态页 + 原始数据 CSV + 走势图 | 见 §7 |

> 全部脚本与并发剖面、测试时长、原始数据**公开可复现**——这是与软文层的根本区别。

---

## 7. 怎么呈现才可信（破解"运动员兼裁判"）

按可信度从高到低，**优先做上面的**：

1. **进社区中立监测站**（`check.linux.do` / Help AIO）——让第三方的可用率% 和首字趋势替我们说话。这是破解"裁判"质疑的最强一招。
2. **公开全部脚本 + 原始数据表 + 测试时间窗 + 套餐购买凭证**——让任何人能自己跑一遍复现。
3. **公开对自己不利的尾部数据**（P99、偶发失败、某时段抖动）——主动暴露弱点反而建立信任。
4. **同口径可对比**——所有指标定义与社区监测站一致（数学题验真、<6s 阈值、可用率%），不自创有利于自己的标准。
5. **failover 录屏 + 曲线**——动态演示比静态数字更有说服力。

### 呈现模板（一页纸结论）
```
TokenKey 稳定性实测（2026-XX-XX，连续 7×24h + 4h soak）
────────────────────────────────────────────
综合可用率      TokenKey 99.9X%  |  官方 99.9X%  |  中转A 9X.X%  |  中转B 9X.X%
首字 P99        TokenKey X.Xs    |  官方 X.Xs    |  中转A X.Xs   |  中转B X.Xs
failover 切换   TokenKey N 秒自愈 |  中转普遍：人工介入前持续报错
长任务不中断率  TokenKey 9X.X%   |  中转A 跑不通(原生保真缺失)
不降智(AIME)    TokenKey 与官方持平 | 中转A 准确率 -XX%
────────────────────────────────────────────
方法/脚本/原始数据全部公开可复现 → <链接>
第三方监测站交叉印证 → check.linux.do / Help AIO
```

---

## 7.1 社区监测站怎么接入（"同口径站擂台"的落地路径）

三条路径，**可信度与可控性正好相反**——越中立的越难控，越好控的越像"自己人"。按"先做哪个"排序：

### ① Help AIO —— 最实际、够中立、马上能推进 ✅ 首选
- **接入方式**：无表单，**直接通过官方 Telegram `@HelpAIO`（https://t.me/HelpAIO）联系站方申请收录**，提供 base_url + 一个测试 key。
- **中立性**：站方声明"排行/可用率/跑分均自费持续采集，无任何中转赞助或广告；含返佣入口可关闭且不影响排名"——数据是第三方采集，非我们自报。
- **它的方法论恰是我们要的同口径**：CLI 真实请求探可用性 + 标准化题库测能力，输出**通过率 / 平均花费 / TPS / 可用率 / 首字趋势**，有 7 天数据表与综合排名（`helpaio.com/transit/availability`、`helpaio.com/transit/table`）。
- **动作**：发 TG 申请 → 等它采集出可用率 → 在宣传中引用其页面。

### ② check.linux.do —— 最权威（L 站官方），但无公开入口 ⚠️ 中长期
- **现状**：查无"提交/添加站点"的公开表单或教程，更像作者/社区驱动收录。
- **推进方式**：去 L 站找该工具作者的**发布帖**，帖内联系作者申请加入；或在 L 站相关板块自然形成讨论被收录。最值钱（社区默认裁判）但不可控、靠社区关系，当中长期目标。

### ③ 自部署 check-cx —— 完全可控，但有"运动员兼裁判"风险 🔧 并行
- **部署**：开源（Next.js + Supabase，默认分支 `master`），Vercel/Docker 均可。
- **添加一个被测站**：往 Supabase `check_configs` 表插一行：
  ```sql
  INSERT INTO check_configs (name, type, model_id, endpoint, api_key, enabled)
  VALUES ('TokenKey opus-4-8', 'anthropic', <model_id>,
          'https://api.tokenkey.dev/v1/messages', '<test_key>', true);
  ```
  - `type` 支持 `openai` / `gemini` / `anthropic`；`endpoint` 填完整端点（`/v1/messages`、`/v1/chat/completions` 或 `/v1/responses`）；`model_id` 关联 `check_models`。
  - 轮询间隔 `CHECK_POLL_INTERVAL_SECONDS` 默认 60s（15–600s）；`is_maintenance=true` 保留卡片停轮询，`enabled=false` 完全不检测。
  - 必填环境变量：`SUPABASE_URL` / `SUPABASE_PUBLISHABLE_OR_ANON_KEY` / `SUPABASE_SERVICE_ROLE_KEY`。
- **正确用法**：自部署数字**别单独自夸**（会被质疑运动员兼裁判）。把 **TokenKey + 官方 + 2 家同行同口径一起挂上去**，公开全部配置和原始数据让人复现——透明性换可信度。

### 推荐执行顺序
1. **立刻**：发 TG 给 `@HelpAIO` 申请收录（①），最快拿到第三方背书。
2. **并行**：自部署 check-cx（③），把 TokenKey + 官方 + 同行同口径长期采集，数据自有且全公开可复现。
3. **中长期**：通过 L 站作者关系争取进 check.linux.do（②）。

→ 形成三层证据：**第三方背书（Help AIO）+ 自有透明对比盘（check-cx）+ 社区权威（check.linux.do）**，逐层加固。

---

## 8. 必须规避的可信度陷阱（自检清单）

- [ ] 没有用单一报错率代替多维指标。
- [ ] 一律报 P99，不只报均值。
- [ ] 测试窗 ≥4h soak + ≥7 天连续，不是短窗口体感。
- [ ] 工具具名、脚本公开、并发剖面与时长公开。
- [ ] 同行匿名化，不点名抹黑；套餐与购买时间留证。
- [ ] 指标口径与社区监测站一致（同口径可对比）。
- [ ] 有第三方/中立监测交叉印证，不只自报。
- [ ] 主动公开对自己不利的数据。
- [ ] 标注测试时的具体模型版本与时间窗（TTFT SLO 随模型代际变化快）。

---

## 9. 已知局限 / 待办

- **failover 量化方法需自建**：业界无公开的网关切换时间测量标准，§5.2 是我们自拟的混沌实验，首次执行需校准采样粒度。
- **接入路径已明确（见 §7.1）**，剩余待确认：Help AIO 经 TG 申请后的收录时效与数据可引用授权；check.linux.do 无公开入口，需找 L 站作者发布帖建立联系。
- 通用 LLM 推理方法论（queue depth 等）部分对"API 网关路由"意义较弱，落地时按网关场景裁剪。

---

## 附:核心信息源

- check-cx（社区开源监测工具）: https://github.com/BingZi-233/check-cx
- Help AIO 可用性监测（同口径标杆）: https://www.helpaio.com/transit/availability
- L 站官方状态检测: https://check.linux.do/
- Help AIO 接入联系: https://t.me/HelpAIO ; 7天数据表 https://www.helpaio.com/transit/table
- LLM 负载测试方法论: https://tianpan.co/blog/2026-03-19-load-testing-llm-applications ; https://blog.premai.io/load-testing-llms-tools-metrics-realistic-traffic-simulation-2026/
- P99/尾延迟（一手）: Google SRE Book; Dean & Barroso《The Tail at Scale》; OSDI'24 DistServe (arXiv 2401.09670)
- 软文反面教材: https://github.com/LMU-AI/claude-api-relay-review ; https://developer.aliyun.com/article/1728443
