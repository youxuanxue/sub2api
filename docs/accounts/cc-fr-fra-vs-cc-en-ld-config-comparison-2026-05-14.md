# edge-fra1 vs edge-uk1 Anthropic OAuth 账号配置对比

> 日期：2026-05-14
> 范围：仅对比 `edge-fra1/cc-fr-fra-ec2-5-1-a` 与 `edge-uk1/cc-en-ld-ec2-16-1-a` 两个真正持有 Anthropic OAuth 凭据的 edge 账号。
> 视角：**Anthropic OAuth 账号本身的存活率 + 转发流量的稳定性**。不涉及 prod 这一跳的 API Key 透传账号。
> 数据来源：
>   - `cc-fr-fra-ec2-5-1-a`：[`docs/ops/edge-fra1-400-investigation-facts-2026-05-13.md`](../ops/edge-fra1-400-investigation-facts-2026-05-13.md) 中的 DB snapshot（事实层）。
>   - `cc-en-ld-ec2-16-1-a`：[`docs/ops/cc-uk1-oauth-edge-uk1-config-2026-05-12.md`](../ops/cc-uk1-oauth-edge-uk1-config-2026-05-12.md) 中的**推荐基线**（运营手册推荐值；实际 DB 当前值未在本次会话中读取，需要 ops 复核）。
>   - 字段语义来自 `backend/ent/schema/account.go`、`backend/internal/service/account.go`、`backend/internal/handler/dto/types.go`。

## 1. 账号在拓扑中的位置

两个账号都挂在 **edge** 这一跳的 `default` 分组里，类型都是 **真 Anthropic OAuth**——是流量到达 `api.anthropic.com` 前的最后一道身份。它们的稳定性直接决定：
- 该 edge 上对应组（cc-fr-fra-oauth / cc-uk1-oauth）能否被调度池选中；
- 账号被 Anthropic 风控的概率（速率/会话/指纹形态会被上游观测）；
- prod 这一跳 sticky 表的命中率（账号死了 sticky 失效，会话切换）。

```
claude code CLI → prod (api.tokenkey.dev) ── APIKey 透传 ──┐
                                                          ├─→ edge-fra1 ─→ cc-fr-fra-ec2-5-1-a ─→ api.anthropic.com
                                                          └─→ edge-uk1  ─→ cc-en-ld-ec2-16-1-a ─→ api.anthropic.com
```

## 2. 配置快照（事实层）

### 2.1 cc-fr-fra-ec2-5-1-a（edge-fra1）

| 字段 | 当前值 | 来源 |
|---|---|---|
| `platform` | `anthropic` | DB |
| `type` | `oauth` | DB |
| `status` | `error` | DB |
| `error_message` | `Organization disabled (400): This organization has been disabled.` | DB |
| `concurrency` | `3` | DB |
| `base_rpm` | `8` | DB.extra |
| `rpm_strategy` | `tiered` | DB.extra |
| `rpm_sticky_buffer` | `3` | DB.extra |
| `max_sessions` | `8` | DB.extra |
| `session_idle_timeout_minutes` | `8` | DB.extra |
| `user_msg_queue_mode` | 未观测 | — |
| `enable_tls_fingerprint` | 未观测 | — |
| `tls_fingerprint_profile_id` | 未观测 | — |
| `session_id_masking_enabled` | 未观测 | — |
| `org_uuid` / `account_uuid` | `12fee4ed…` / `58d0fc8c…` | DB.extra |
| 同组其他账号 | **0**（`default` 仅此一个账号） | DB |
| 上游 400 时刻 | `2026-05-13T10:13:12Z` | logs |
| 400 前累计成功 | `107` 条（全部 `/v1/messages`） | usage_logs |
| RPM 峰值（每分钟） | `8`（`09:48`） | usage_logs |
| 流量形态 | 09:20–10:00 突发，5 分钟桶最高 26 | usage_logs |
| 模型分布 | opus-4-7 81% / haiku-4-5 13% / sonnet-4-6 6% | usage_logs |
| UA 分布 | claude-cli 93% / curl 6% | usage_logs |

### 2.2 cc-en-ld-ec2-16-1-a（edge-uk1）—— 推荐基线

> 实际 DB 当前值未在本次会话读取，下表为运营手册推荐值；如果本机 prod admin 已偏离，应按手册回归。

| 字段 | 推荐值 | 上限 |
|---|---|---|
| `platform` | `anthropic` | — |
| `type` | `oauth` | — |
| `concurrency` | **1** | 2 |
| `base_rpm` | **3～4** | — |
| `rpm_strategy` | `tiered`（默认） | — |
| `rpm_sticky_buffer` | **1** | — |
| `max_sessions` | **1** | 2 |
| `session_idle_timeout_minutes` | `8` | — |
| `user_msg_queue_mode` | **`serialize`** | — |
| `enable_tls_fingerprint` | **`true`** | — |
| `tls_fingerprint_profile_id` | **固定模板**（`claude_cli_v2` 或内置默认） | ❌ 不选随机 |
| `session_id_masking_enabled` | **`true`** | — |
| 同组其他账号 | 多账号同优先级，LRU 自然均衡 | — |

## 3. 每个字段做什么 · 对 OAuth 账号存活与流量稳定性的作用

下面只解释**在 edge 这一跳的 OAuth 路径上生效**的字段。代码分支 `if account.IsOAuth()`（`gateway_service.go:4462 / 6072`）。

### 3.1 `concurrency`（账号最大并发）

- **作用**：同一时刻可向该账号派发的请求数；超出排队或被调度器跳过。
- **OAuth 视角**：Anthropic Console 真账号不像 API Key 那样有明确「企业级 RPS 预算」。**并发越高，上游越像「机器人在压测」**，触发风控（rate-limit / overload / disable）的概率上升。
- **流量稳定性**：单账号并发高 → 失败时影响面大；并发低 + 多账号 LRU 均摊 → 失败粒度小，sticky 切换成本低。
- **fra1 vs uk1**：fra1=3，uk1=1。**fra1 是 uk1 的 3 倍**，配合 base_rpm=8 形成「单账号长时间满载」形态。

### 3.2 `base_rpm`（账号基准 RPM）

- **作用**：账号每分钟可接受的请求数门槛；超过后 `CheckRPMSchedulability`（`account.go:2094`）返回 `StickyOnly` 或 `NotSchedulable`。
- **OAuth 视角**：Anthropic 真账号合理使用强度大致等同于「一个程序员在 IDE 里跑 Claude Code」，**人类 IDE 峰值 RPM ≈ 3–6**。把 base_rpm 配到 8 已经接近 Claude Code CLI 自然峰值的上限。
- **触发的失败模式**：long-tail 失败模式不是 429，而是 Console 侧风控直接把账号 `disabled`（fra1 已实测）。
- **fra1 vs uk1**：fra1=8，uk1=3–4。fra1 实测 09:48 命中 RPM=8 峰值（**正好等于 base_rpm**），账号在持续高强度下运行了约 23 分钟（09:20→10:13）后被风控 disable。

### 3.3 `rpm_strategy`（RPM 触顶策略）

- **作用**：超过 `base_rpm` 后行为：
  - `tiered`（默认）：`base_rpm` 内绿区；`base_rpm ~ base_rpm+sticky_buffer` 黄区（仅服务现有 sticky session）；之上红区（直接踢出）。
  - `sticky_exempt`：超过 base_rpm 后**所有非 sticky 请求阻塞**，已有 sticky 不受限。
- **OAuth 视角**：`tiered` 让账号在触顶时仍能消化 sticky 流量，避免 sticky session 中断；`sticky_exempt` 更激进保护已有会话但拒绝新会话。
- **fra1 vs uk1**：都是 `tiered`。这条没差异。

### 3.4 `rpm_sticky_buffer`（黄区宽度）

- **作用**：`tiered` 模式下黄区上限 = `base_rpm + buffer`。代码实现：未手动设置时 `buffer = concurrency + max_sessions`，并有 floor = `base_rpm / 5`（`account.go:2050`）。
- **OAuth 视角**：buffer 越大，账号在「假死」前能多撑几条 sticky 请求；buffer 太大反而把账号推向更高的真实 RPM，被风控的概率更高。
- **fra1 vs uk1**：
  - fra1: `base_rpm=8` + `buffer=3` → 黄区上限 11。
  - uk1: `base_rpm=3` + `buffer=1` → 黄区上限 4。
  - **fra1 黄区上限差不多是 uk1 的 3 倍**。

### 3.5 `max_sessions`（账号可承载的活跃 sticky session 数）

- **作用**：边界——超出后新 sticky session 拿不到这个账号，被调度到其他账号。
- **OAuth 视角**：**Anthropic 一个真实程序员账号正常一次只跑 1–2 个 Claude Code 会话**。max_sessions 设高了相当于强行让一个真账号同时扮演多个程序员，是「机器人特征」最强的信号之一。
- **fra1 vs uk1**：fra1=8（同时挂 8 个 sticky 会话），uk1=1。fra1 等于在告诉 Anthropic：「我这个个人开发者同时开了 8 个 IDE 在写代码」——风控会注意到。

### 3.6 `session_idle_timeout_minutes`（sticky session 空闲过期）

- **作用**：edge 侧 sticky 表 TTL；超过这个时长没新请求，sticky 失效，下次请求重新调度。
- **OAuth 视角**：和 prod 侧 sticky TTL 协同。**TTL 太长**：账号 disable 后陈旧 sticky 还在引导流量到 dead account → 大量瞬时失败；**TTL 太短**：会话频繁切换账号 → 上游每次都看到新 session_id，看起来更像爬虫。
- **fra1 vs uk1**：都是 8 分钟，**这条配置一致**。

### 3.7 `user_msg_queue_mode`（用户消息排队模式）

- **作用**：`account.go:1434`，两种值：
  - `serialize`：严格串行——同账号同会话同一时刻只处理一条 user message。
  - `throttle`：软限速——并发允许，但用 token bucket 节流。
  - 留空：用全局配置。
- **OAuth 视角**：`serialize` **强制让账号看起来像「一个程序员一次只输入一条消息」**，避免并发对话造成机器人嫌疑；`throttle` 更适合多账号已经分摊压力的场景。
- **fra1 vs uk1**：
  - fra1：未观测（DB snapshot 没读这字段；运营手册没给 fra1 推荐值）。
  - uk1：手册推荐 `serialize`。
  - **fra1 如果是空 → 用全局配置 → 大概率不串行 → 配合 concurrency=3 一起放大「同账号同时多请求」的风控信号**。

### 3.8 `enable_tls_fingerprint` + `tls_fingerprint_profile_id`（TLS 指纹伪装）

- **作用**：edge 出站到 `api.anthropic.com` 时的 ClientHello / JA3 形态。**默认 Go HTTP client 的 JA3 是 Anthropic 已知的机器人形态**，开启后伪装成 Claude Code CLI 自己的 JA3。
- **OAuth 视角**：这是「让真账号看起来像真客户端」的核心机制。**关闭等同把脸贴上 Anthropic 风控雷达**。代码分支：`gateway_service.go:6072 if account.IsOAuth() && s.identityService != nil`。
- **profile 选择**：
  - 固定模板（`claude_cli_v2` / 内置默认）：每次请求 JA3 一致 → 像稳定客户端。
  - 随机模板：每次请求 JA3 不同 → **比固定模板更像爬虫**（真客户端不会每次握手都变 JA3）。
- **fra1 vs uk1**：
  - fra1：未观测；DB 没读，运营手册没强调。
  - uk1：手册推荐 **开启 + 固定模板**。
  - **fra1 如果没开 TLS 伪装，这是被风控 disable 最直接的解释之一**（在 23 分钟内累计 107 条请求 + 默认 Go JA3，几乎必触发）。

### 3.9 `session_id_masking_enabled`（session_id 伪装）

- **作用**：`account.go:1454 IsSessionIDMaskingEnabled` + `identity_service.go:269 RewriteUserIDWithMasking`。打开后，每 15 分钟轮换一次 `metadata.user_id` 中的 session_id；多个 sticky session 看起来是同一个用户的连续操作。
- **OAuth 视角**：让 Anthropic 看到的 user_id 形态接近真实 Claude Code CLI 行为（CLI 自己也会刷 session_id）。
- **同时的副作用**：和 sticky 路径联动。**勾选时务必同步检查 sticky_routing_mode / prod 这一跳的 type=APIKey** 才不会击穿 edge sticky。本仓库 `docs/ops/cc-uk1-oauth-edge-uk1-sticky-audit-2026-05-12.md §4 隐患 C` 描述过这个雷区。
- **fra1 vs uk1**：
  - fra1：未观测。
  - uk1：手册推荐开启。

### 3.10 `extra.model_rate_limits`（模型粒度配额，仅历史快照）

- **作用**：保存上游返回的 model-level rate-limit 元数据（如 `claude-3-5-sonnet-20240620` 的小时上限），由网关动态写入，主要用于观测与回压决策。
- **OAuth 视角**：不直接影响调度，但 model_rate_limits 频繁更新意味着账号在不断触模型级配额——是「快烧到 disable」的先兆指标。
- **fra1 vs uk1**：fra1 在 disable 前已有 `claude-3-5-sonnet-20240620` 的 model_rate_limits 历史；uk1 未观测。

### 3.11 `status` + `error_message` + `temp_unschedulable_until`（健康状态）

- **作用**：调度器在 `gateway.select_account_*` 路径上读 `status=active` 才会选；其他 `error / disabled` 跳过；`temp_unschedulable_until` 是临时退避窗口。
- **OAuth 视角**：上游 4xx/5xx 自动写入 `status`；429 → `rate_limited_at`；529 → `overload_until`；**特殊 400「Organization disabled」直接 status=error 且不会自动恢复**（人工 / Console 解封后才能改回）。
- **fra1 vs uk1**：fra1 已 `status=error`，组内 0 个 active 账号 → 任何流量到此组都返回 `no available accounts` 502。uk1 状态未观测。

## 4. 关键差异一览

| 维度 | cc-fr-fra-ec2-5-1-a | cc-en-ld-ec2-16-1-a（推荐） | 倍数差异 | OAuth 风控含义 |
|---|---|---|---|---|
| 并发 | 3 | 1 | 3× | fra1 单账号同时被 3 路压 |
| RPM 基准 | 8 | 3–4 | 2–2.7× | fra1 持续触顶 |
| 黄区上限（RPM+buffer） | 11 | 4 | 2.75× | fra1 极限峰值更高 |
| 最大 sticky session 数 | 8 | 1 | 8× | **最显著的差异**——fra1 装作 8 个程序员 |
| 串行队列 | 未观测/未启用 | `serialize` | — | fra1 没限制并发对话节奏 |
| TLS 指纹伪装 | 未观测/疑似未启用 | 开启+固定模板 | — | fra1 可能用裸 Go JA3 直连 Anthropic |
| session_id 伪装 | 未观测 | 启用 | — | fra1 user_id 可能持续不变（也是风控信号） |
| 同组冗余账号 | 0 | 多账号 LRU | — | fra1 单点 → disable 直接整组 502 |
| 当前状态 | `error: Organization disabled` | （未观测，应为 active） | — | fra1 已被 Console 封禁 |

## 5. 优化建议

### 5.1 fra1 必改项（短期）

| 优先级 | 字段 | 现状 | 建议值 | 理由 |
|---|---|---|---|---|
| P0 | `concurrency` | 3 | **1**（最多 2） | 与 uk1 推荐对齐；削单账号压测形态 |
| P0 | `max_sessions` | 8 | **1**（最多 2） | **最关键**——8 个 sticky session 等于 8 个程序员同时在线，强机器人特征 |
| P0 | `base_rpm` | 8 | **3–4** | 控制在人类 IDE 自然峰值附近 |
| P0 | `rpm_sticky_buffer` | 3 | **1** | 与 base_rpm 降幅匹配 |
| P0 | `enable_tls_fingerprint` | 未观测 | **`true`** | OAuth 路径强制要求；不开 ≈ 裸 JA3 直连 |
| P0 | `tls_fingerprint_profile_id` | 未观测 | **固定模板**（首选 `claude_cli_v2`） | 随机模板比不伪装还危险 |
| P0 | `user_msg_queue_mode` | 未观测 | **`serialize`** | 让上游看到「单线程对话节奏」 |
| P0 | `session_id_masking_enabled` | 未观测 | **`true`** | 配合 prod APIKey 透传，不会击穿 sticky |
| P1 | 组内补冗余账号 | 仅 1 个 | **≥ 3 个同优先级** | 单账号 disable 不再让整组 502 |
| P2 | `session_idle_timeout_minutes` | 8 | 保持 8 | 已合理，不动 |

### 5.2 uk1 复核项（确认手册值确实落地）

由于本会话**没有读到 cc-en-ld-ec2-16-1-a 的 DB snapshot**，建议 ops：

1. 在 edge-uk1 admin UI 上打开 `cc-en-ld-ec2-16-1-a` 编辑页，逐字段对照 §2.2 推荐值。
2. 若 `enable_tls_fingerprint` 或 `tls_fingerprint_profile_id` 是空 / 随机模板 / 未观测 → 立刻按手册回归；这两条不到位 uk1 复制 fra1 翻车只是时间问题。
3. 若 `user_msg_queue_mode` 不是 `serialize` → 改为 `serialize`，记录改动时间。
4. 抓一份当前 `extra_json` 存档，未来对比基线。

### 5.3 通用建议（两个 edge 都该做）

| 项 | 做法 |
|---|---|
| **冗余** | 每个 edge `default` 分组至少挂 3 个 OAuth 账号；fra1 当前 1 个是结构性单点 |
| **预警** | 添加监控：单账号每 10 分钟成功请求 > 50 → 告警，提示「接近 fra1 disable 当时的 23 分钟/107 条强度」 |
| **滚动隔离** | 当 `status=error`（尤其 `Organization disabled` 文案）→ 自动从可调度池移除，不依赖 schedulable 字段手动改 |
| **TLS 模板审计** | 每周一次脚本对比所有 OAuth 账号的 `tls_fingerprint_profile_id` 是否落在白名单集合，捕获手动改成随机模板的情况 |
| **流量画像探针** | 用 curl 模拟人类节奏（每分钟 ≤4 条，间隔抖动）作为最小压测基线；若 curl 都触发 disable，说明是组织层风控，账号已无救 |

## 6. 风险红线

以下任意一条触发 = 该 OAuth 账号已进入「随时 disable」高危区，应该立即下线并隔离：

1. **单账号** `model_rate_limits` 在 24h 内更新 ≥ 3 次（说明持续触模型级配额）。
2. **单账号** 持续 30 分钟以上 RPM ≥ base_rpm 的 80%。
3. **同账号 sticky session 数** 持续触达 `max_sessions` 上限。
4. **TLS 指纹伪装** 被运维或代码改动意外关闭（看 `enable_tls_fingerprint` 历史变更）。
5. **多账号同时刷新 access_token** 集中在同一时间窗口（被风控关联可能性）。

## 7. fra1 当前事故的归因排序（基于事实 + 上述配置）

按可能性高低（不是单一根因，而是几条叠加致 disable）：

1. **过激限额**（`base_rpm=8` × `max_sessions=8` × `concurrency=3`）—— 单账号被持续 23 分钟压到接近上限，且呈现「8 个程序员同时在线」形态。
2. **TLS 指纹** 是否启用未观测——若关闭则裸 JA3 直连 Anthropic，加速触发。
3. **零冗余** —— 单账号承担整组流量，没机会被 LRU 分散；行为画像很集中。
4. **流量形态突变**（09:20 开始 5 分钟内 20 条）—— 与人类 IDE 行为不符。
5. **环境变量缺失**（`ANTHROPIC_API_KEY` 未在容器观测到）—— 与本次 disable 无直接因果，但说明 ops 验证手段不完整。

→ 解封后**必须先按 §5.1 改配置**再恢复调度，否则会复现。

## 8. 数据空洞与后续追查

本次报告未覆盖以下事实，建议补：

- `cc-en-ld-ec2-16-1-a` 当前 DB `extra_json` 实际值（uk1 admin UI 截图或 SQL dump）。
- 两个账号的 `tls_fingerprint_profile_id` 实际 ID 与 profile 名称映射。
- fra1 在 09:20 起的突发是否对应某个真实客户端事件（人 vs 脚本调用）。
- Anthropic Console 侧 `12fee4ed-e049-4559-8027-7b22d0f52889` 组织被 disable 的官方原因（需邮件/Console 联系上游确认）。

补全后再补一轮 §7 归因。

## 附录 A：字段→代码索引

| 字段 | Schema/DTO | 运行时读取点 |
|---|---|---|
| `concurrency` | `ent/schema/account.go:97` | 调度器 LRU |
| `base_rpm` | `dto.types.go:207` | `account.go:2020 GetBaseRPM` |
| `rpm_strategy` | `dto.types.go:208` | `account.go:2035 GetRPMStrategy` |
| `rpm_sticky_buffer` | `dto.types.go:209` | `account.go:2050 GetRPMStickyBuffer` |
| `max_sessions` | `dto.types.go:202` | `account.go GetMaxSessions` |
| `session_idle_timeout_minutes` | `dto.types.go:203` | `account.go:2005 GetSessionIdleTimeoutMinutes` |
| `user_msg_queue_mode` | `dto.types.go:210` | `account.go:1436 GetUserMsgQueueMode` |
| `enable_tls_fingerprint` | `dto.types.go:214` | `gateway_service.go:6072 (OAuth path)` |
| `tls_fingerprint_profile_id` | `dto.types.go:215` | 同上 |
| `session_id_masking_enabled` | `dto.types.go:220` | `account.go:1458 IsSessionIDMaskingEnabled` + `identity_service.go:269` |
| `status` / `error_message` | `ent/schema/account.go:114-122` | `gateway.select_account_*` |

## 附录 B：参考文档

- 链路代码细节：[`docs/ops/cc-uk1-oauth-edge-uk1-sticky-audit-2026-05-12.md`](../ops/cc-uk1-oauth-edge-uk1-sticky-audit-2026-05-12.md)
- 配置运营手册（uk1 推荐基线）：[`docs/ops/cc-uk1-oauth-edge-uk1-config-2026-05-12.md`](../ops/cc-uk1-oauth-edge-uk1-config-2026-05-12.md)
- fra1 事故事实清单：[`docs/ops/edge-fra1-400-investigation-facts-2026-05-13.md`](../ops/edge-fra1-400-investigation-facts-2026-05-13.md)
- Edge 部署拓扑：[`deploy/aws/stage0/edge-targets.json`](../../deploy/aws/stage0/edge-targets.json)
