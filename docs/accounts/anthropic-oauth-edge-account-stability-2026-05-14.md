# Anthropic OAuth Edge 账号稳定性配置对比（线上事实，2026-05-14）

> **TokenKey 现行运维标准（Anthropic OAuth edge，与快照日期无关）**：每条 edge Anthropic OAuth 账号必须在 DB 绑定显式 TLS 模板；canonical **`tls_fingerprint_profiles.name`** 一律为 **`claude_cli_2_1_142_node24_20260515`**（字段体以 `deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json` 的 `shared_baseline.tls_profile` 为唯一真值源；对照副本见 `deploy/aws/stage0/claude_cli_2_1_142_node24_20260515.json`）。`ops/anthropic/manage-anthropic-config.py` 写入面 **(A)** 的 tier apply SQL 会按该名 `ON CONFLICT (name)` upsert 并写 `accounts.extra.tls_fingerprint_profile_id`。
>
> **与本文史实的关系**：下文表格与 §5 中出现的 **`claude_cli_nodejs24_fixed`** 是 **2026-05-14 当日** fra1 数据库里 **`tls_fingerprint_profiles.name` 的字面快照**，保留用于对照当时线上事实；运维与新账号回填应以文首 canonical 为准，避免继续手建旧名。
>
> 范围：只对比线上 `edge-fra1` 的 `cc-fr-fra-ec2-5-1-a` 与线上 `edge-uk1` 的 `cc-en-ld-ec2-16-1-a`。  
> 原则：只使用本次从线上 AWS SSM + PostgreSQL + 容器日志读取到的事实；不引用历史文档、后台截图或本地猜测。  
> 脱敏：OAuth token、refresh token、token_type、内部 token version、邮箱、account/org UUID 在本文中脱敏或局部描述，避免把线上身份与凭据写入仓库。

## 证据来源

| Edge | 读取方式 | 线上实例 | 线上域名 | 镜像 | 关键证据 |
|---|---|---|---|---|---|
| fra1 | AWS SSM + `tokenkey-postgres` 只读 SQL + `docker logs` | `i-05350d8cc7c838355` | `api-fra1.tokenkey.dev` | `ghcr.io/youxuanxue/sub2api:1.7.31` | SSM command `7552ff9a-4115-4a65-8794-42dcc6a90ec5`, `4bb4aee9-d801-44ee-8f03-8e4da3a116c0`, `67bd3596-17b0-4ed3-80fc-dd2e05c28217` |
| uk1 | AWS SSM + `tokenkey-postgres` 只读 SQL + `docker logs` | `i-0f6ece892c918ea9a` | `api-uk1.tokenkey.dev` | `ghcr.io/youxuanxue/sub2api:1.7.27` | SSM command `9e1d3f80-c9b0-408a-ba86-16ec99945fe9`, `84da0215-e2b6-42dc-8b2d-a492dd3f651c`, `a7b98dd7-9a7b-4c51-8c57-932aad590693` |

## 总结结论

两个账号当前都不是“TLS 参数没调好”的问题，而是已经进入同一种上游失败状态：

| Edge | 账号 | 当前状态 | 线上错误 | 稳定性含义 |
|---|---|---|---|---|
| fra1 | `cc-fr-fra-ec2-5-1-a` | `error` | `Organization disabled (400): This organization has been disabled.` | 组织已被上游禁用；TLS/profile/RPM/session 参数不能恢复这个组织。 |
| uk1 | `cc-en-ld-ec2-16-1-a` | `error` | `Organization disabled (400): This organization has been disabled.` | 同上；该账号不应继续作为稳定性优化验证对象。 |

稳定性配置层面：

- 两边都已经是固定区域出口、无代理、Anthropic OAuth、默认分组、`concurrency=3`、`base_rpm=8`、串行用户消息队列、session masking、cache TTL override 到 `1h`。
- fra1 已显式绑定 TLS profile **`claude_cli_nodejs24_fixed`**（2026-05-14 DB **字面名**；现行 TokenKey 统一为 **`claude_cli_2_1_142_node24_20260515`**，见文首），但绑定发生在账号最后一次成功流量之后，因此尚无线上请求证明“绑定后 profile 被实际用于上游请求”。
- uk1 开启了 `enable_tls_fingerprint=true`，但没有显式 `tls_fingerprint_profile_id`，线上 `tls_fingerprint_profiles` 表为空；按当前产品语义这会退回内置默认 profile，但可审计性弱于 fra1。
- uk1 镜像 `1.7.27` 落后 fra1 的 `1.7.31`，跨 edge 行为对比会混入版本差异。

## 1. Edge 与部署层对比

| 项目 | fra1 | uk1 | 稳定性解读 | 建议 |
|---|---|---|---|---|
| Region | `eu-west-3` | `eu-west-2` | 固定区域出口有利于 OAuth 流量稳定；不要同一账号跨区域漂移。 | 保持每个 OAuth 账号固定绑定一个 edge 出口。 |
| Domain | `api-fra1.tokenkey.dev` | `api-uk1.tokenkey.dev` | 两边都是独立 edge 入口。 | 正常。 |
| Instance | `i-05350d8cc7c838355` | `i-0f6ece892c918ea9a` | 单 EC2 edge。 | 正常。 |
| Image | `sub2api:1.7.31` | `sub2api:1.7.27` | 版本不同会影响 TLS profile、调度、错误处理、缓存行为的可比性。 | 优先把 uk1 升级到与 fra1 相同的已验证版本，再做策略对比。 |
| Proxy | `NULL` | `NULL` | 不经二级代理，出口身份更稳定。 | 对 OAuth 稳定性是正确方向，继续保持。 |

## 2. 账号主表配置对比

| 字段 | fra1：`cc-fr-fra-ec2-5-1-a` | uk1：`cc-en-ld-ec2-16-1-a` | 作用 | 稳定性评价与建议 |
|---|---:|---:|---|---|
| `id` | `1` | `2` | DB 主键。 | 仅用于排障定位。 |
| `platform` | `anthropic` | `anthropic` | 决定走 Anthropic 网关链路。 | 正确。 |
| `type` | `oauth` | `oauth` | 使用 OAuth access/refresh token。 | 正确。 |
| `status` | `error` | `error` | 账号调度状态。 | P0：两个账号都已不可作为健康流量源。 |
| `error_message` | `Organization disabled (400)` | `Organization disabled (400)` | 上游明确返回组织禁用。 | P0：换健康组织/账号；不要通过 TLS/RPM 参数试图恢复。 |
| `schedulable` | `true` | `true` | 管理侧是否允许参与调度。 | 虽为 true，但 `status=error` 会阻断实际健康调度；不要只看 schedulable。 |
| `priority` | `1` | `1` | 调度优先级，数值越小越优先。 | 单账号/主力账号可接受；多账号池建议按健康度分层。 |
| `concurrency` | `3` | `3` | 最大并发。 | 稳定优先下合理，不建议升高。 |
| `load_factor` | `NULL` | `NULL` | 空值时通常按 concurrency 作为负载因子。 | 正常。 |
| `rate_multiplier` | `1.0000` | `1.0000` | 账号计费倍率。 | 正常。 |
| `proxy_id` | `NULL` | `NULL` | 是否绑定代理。 | 正确：edge 自身即出口，不要叠加不稳定代理。 |
| `channel_type` | `0` | `0` | New API channel 类型。 | Anthropic 原生 OAuth 应为 0。 |
| `auto_pause_on_expired` | `true` | `true` | 账号过期后自动暂停。 | 正常。 |
| `last_used_at` | `2026-05-13 10:03:35 UTC` | `2026-05-12 10:27:09 UTC` | 最后使用时间。 | 两者均已停止有效业务流量；uk1 更早停止。 |
| `session_window_status` | `allowed` | `allowed` | 5h 窗口状态。 | 不是当前阻断点；阻断点是 organization disabled。 |
| `session_window_start/end` | `2026-05-13 07:50 → 12:50 UTC` | `2026-05-12 07:10 → 12:10 UTC` | Anthropic OAuth 窗口跟踪。 | 历史窗口，账号 error 后不代表当前健康。 |

## 3. OAuth credentials 对比（脱敏）

| 字段 | fra1 | uk1 | 作用 | 稳定性评价与建议 |
|---|---|---|---|---|
| `access_token` | `<redacted>` | `<redacted>` | 上游访问 token。 | 不写入文档；如账号 disabled，刷新 token 也不一定能恢复组织。 |
| `refresh_token` | `<redacted>` | `<redacted>` | 刷新 access token。 | 同上。 |
| `token_type` | `<redacted>` | `<redacted>` | 通常是 Bearer。 | 正常但脱敏。 |
| `scope` | `user:file_upload user:inference user:mcp_servers user:profile user:sessions:claude_code` | 同 fra1 | OAuth 权限范围。 | 两边 scope 都完整，权限范围不是当前问题。 |
| `account_uuid` | 存在，已脱敏 | 存在，已脱敏 | Anthropic 账号身份。 | 两边都存在。 |
| `org_uuid` | 存在，已脱敏 | 存在，已脱敏 | Anthropic 组织身份。 | 两边错误都指向组织 disabled。 |
| `email_address` | 存在，已脱敏 | 存在，已脱敏 | 登录邮箱。 | 不写入仓库。 |
| `expires_at` | JSON number：`1778670058` | JSON string：`"1778592851"` | access token 过期时间。 | 类型不一致但代码通常兼容；建议新账号导入时统一为 number，减少边界差异。 |
| `expires_in` | JSON number：`28800` | JSON string：`"28800"` | token TTL。 | 同上，建议统一为 number。 |
| `_token_version` | 无 | 有，已脱敏 | token 缓存/刷新版本。 | uk1 有额外 token 版本字段；不是当前 disabled 根因。 |
| `intercept_warmup_requests` | `true` | `true` | 拦截 warmup，减少无效上游调用。 | 正确，建议保留。 |
| `temp_unschedulable_enabled` | `true` | 未见 | 临时不可调度规则开关。 | fra1 更完整；uk1/新账号建议补齐。 |
| `temp_unschedulable_rules` | 429 + `rate limit`/`too many requests` → 暂停 30 分钟 | 未见 | 命中可恢复限流时暂停账号。 | 建议新健康账号都启用，避免 429 后继续打流。 |

## 4. extra 稳定性配置对比

| 字段 | fra1 | uk1 | 作用 | 稳定性评价与建议 |
|---|---:|---:|---|---|
| `enable_tls_fingerprint` | `true` | `true` | 开启出站 TLS ClientHello 模拟。 | 正确。 |
| `tls_fingerprint_profile_id` | `1` | 未见 | 绑定显式 TLS 模板。 | fra1 可审计性更好；uk1 建议按 **`claude_cli_2_1_142_node24_20260515`**（tier baseline）创建/同步 profile 并绑定（见文首）；勿长期停留在“仅启用、无 profile 行”。 |
| `base_rpm` | `8` | `8` | 账号基础 RPM 绿区上限。 | 稳定优先下合理。 |
| `rpm_strategy` | `tiered` | `tiered` | RPM 三段：绿区、sticky-only、红区。 | 正确；比硬切更平滑。 |
| `rpm_sticky_buffer` | `3` | `3` | 超过 base_rpm 后 sticky 请求缓冲。 | 偏紧；若目标是会话连续性，健康账号可试 `5`。若严格控流，保留 `3`。 |
| `user_msg_queue_mode` | `serialize` | `serialize` | 同一用户消息串行。 | 强烈建议保留，OAuth 稳定性优先。 |
| `max_sessions` | `8` | `10` | 最大会话数。 | uk1 更激进；建议健康新账号以 `8` 作为基线，再按成功率上调。 |
| `session_idle_timeout_minutes` | `8` | `5` | 会话空闲释放时间。 | uk1 更容易 session churn；建议统一到 `8`，减少会话频繁变化。 |
| `session_id_masking_enabled` | `true` | `true` | 稳定 `metadata.user_id` 的 session 部分。 | 正确，建议保留。 |
| `cache_ttl_override_enabled` | `true` | `true` | 强制 cache creation TTL 分类。 | 正确。 |
| `cache_ttl_override_target` | `1h` | `1h` | 将 cache creation 归入 1h。 | 对 Claude Code 长上下文稳定性有利；成本更高但当前目标是稳定。 |
| `window_cost_limit` | `200` | `200` | 5h 窗口成本阈值。 | 对单账号偏宽；健康账号建议先用 `100–150` 更早降速。 |
| `window_cost_sticky_reserve` | `5` | `5` | 窗口超过阈值后的 sticky reserve。 | 偏小；若降低 limit，reserve 建议到 `10`。 |
| `session_window_utilization` | `0.07` | `0.24` | 最近 5h 用量观测。 | uk1 使用强度高于 fra1。 |
| `passive_usage_7d_utilization` | `0.02` | `0.13` | 7d 用量观测。 | uk1 使用强度高于 fra1。 |
| `passive_usage_sampled_at` | `2026-05-13T10:03:33Z` | `2026-05-12T10:25:22Z` | 用量采样时间。 | 均为账号进入 error 前后的历史样本。 |
| `model_rate_limits` | `claude-3-5-sonnet-20240620`，reset `2026-05-14T03:06:25Z` | `gpt-5.5` 与 `claude-3-7-sonnet-20250219`，reset `2026-05-13` | 模型级限流记忆。 | 均为历史/过期风险字段；uk1 的 `gpt-5.5` 出现在 Anthropic 账号上尤其可疑，建议健康账号初始化时不要继承 stale model limits。 |

## 5. TLS profile 对比

| 项目 | fra1 | uk1 | 稳定性解读 | 建议 |
|---|---|---|---|---|
| `enable_tls_fingerprint` | `true` | `true` | 两边都启用 TLS 指纹模拟。 | 正确。 |
| 显式 `tls_fingerprint_profile_id` | `1` | 无 | fra1 已绑定固定模板；uk1 只启用但无显式模板。 | uk1 应补齐显式模板绑定（canonical：**`claude_cli_2_1_142_node24_20260515`**）。 |
| `tls_fingerprint_profiles` 表 | 1 条：`claude_cli_nodejs24_fixed`（快照日 DB 字面名） | 0 条 | fra1 当时可审计；uk1 不可在后台看到具体模板。 | 新工作与跨 edge 对齐一律使用 **`claude_cli_2_1_142_node24_20260515`**（文首）；历史名仅存于本条快照语义。 |
| profile 特征 | Node.js 24.x 风格，`enable_grease=false`，ALPN `http/1.1`，TLS 1.3/1.2，X25519 | 无显式 profile | 固定 ClientHello 有助于减少账号流量形态漂移。 | 健康账号一律绑定 **`claude_cli_2_1_142_node24_20260515`**（文首）；不使用随机或仅内置默认且无 DB 模板。 |
| 线上转发日志 | 最后成功流量仍显示 `Built-in Default (Node.js 24.x)`，发生在绑定显式 profile 之前 | 未捕获到目标账号最近转发日志 | fra1 显式绑定后账号已 error，尚无 post-bind 流量证据。 | 需要健康账号产生请求后再验证日志。 |

fra1 **2026-05-14 采样** profile 线上事实（`name` 为当时 DB 字面；现行标准名为文首 **`claude_cli_2_1_142_node24_20260515`**）：

```text
id = 1
name = claude_cli_nodejs24_fixed
enable_grease = false
alpn_protocols = ["http/1.1"]
supported_versions = [772, 771]
key_share_groups = [29]
extensions = [0,65037,23,65281,10,11,35,16,5,13,18,51,45,43]
created_at = 2026-05-14 01:57:37 UTC
```

## 6. 分组与代理对比

| 项目 | fra1 | uk1 | 作用 | 建议 |
|---|---|---|---|---|
| Group | `default` / `anthropic` / `active` | `default` / `anthropic` / `active` | 账号所在调度池。 | 正常。 |
| Group rate_multiplier | `1.0000` | `1.0000` | 分组计费倍率。 | 正常。 |
| Proxy | `NULL` | `NULL` | 是否经额外代理。 | 对 edge 模式正确；不要引入代理漂移。 |

## 7. usage_logs 线上流量事实

| 指标 | fra1 | uk1 | 解读 |
|---|---:|---:|---|
| usage_count | `107` | `1138` | uk1 历史请求量明显更大。 |
| first_usage | `2026-05-13 07:50:52 UTC` | `2026-05-11 00:24:38 UTC` | uk1 运行更早更久。 |
| last_usage | `2026-05-13 10:03:35 UTC` | `2026-05-12 10:27:09 UTC` | 两者之后均无成功 usage log。 |
| total_cost | `33.935008` | `159.929572` | uk1 消耗更高。 |
| cache_ttl_overridden_count | `99 / 107` | `944 / 1138` | 两边多数请求都应用了 cache TTL override。 |

### 模型分布

| Edge | 模型 | 请求数 | 最后出现 | 平均 duration_ms | 平均 first_token_ms |
|---|---|---:|---|---:|---:|
| fra1 | `claude-opus-4-7` | `87` | `2026-05-13 10:03:35 UTC` | `13959.0` | `4380.1` |
| fra1 | `claude-haiku-4-5-20251001` | `14` | `2026-05-13 09:58:16 UTC` | `10725.6` | `1528.7` |
| fra1 | `claude-sonnet-4-6` | `6` | `2026-05-13 08:17:23 UTC` | `1121.3` | `959.7` |
| uk1 | `claude-opus-4-7` | `653` | `2026-05-12 10:07:55 UTC` | `11077.6` | `3078.2` |
| uk1 | `claude-sonnet-4-6` | `238` | `2026-05-12 10:27:09 UTC` | `11528.8` | `3287.5` |
| uk1 | `claude-haiku-4-5-20251001` | `121` | `2026-05-12 09:55:28 UTC` | `3740.5` | `1556.4` |
| uk1 | `claude-opus-4-6` | `114` | `2026-05-12 10:24:07 UTC` | `83829.4` | `1542.4` |
| uk1 | `claude-haiku-4-5` | `12` | `2026-05-12 02:14:03 UTC` | `2505.8` | `992.8` |

线上事实显示：uk1 的历史负载更高、模型更混杂、存在长时非流式 `claude-opus-4-6` 请求；fra1 负载较低但也在较短时间内进入同类 organization disabled 错误。因此当前不能把“哪个配置更稳”归因到单个参数，优先结论是：两个组织均已不可用。

## 8. 逐项优化建议

### P0：不要继续优化这两个 disabled organization 账号

| 事实 | 判断 | 建议 |
|---|---|---|
| 两个账号 `status=error`，错误均为 `Organization disabled (400)` | 上游组织禁用是硬失败，不是 TLS、RPM、session 参数能修复的问题。 | 新建健康 Anthropic OAuth 账号；旧账号保留为事故样本，不再作为流量源。 |
| fra1 最近日志明确出现 `account_disabled_auth_error` | 继续重试只会产生失败请求。 | 不要简单把 `status` 改回 `active` 继续跑。 |

### P1：标准化健康新账号模板

建议健康新账号采用以下稳定性基线：

| 配置 | 建议值 | 理由 |
|---|---:|---|
| 固定 edge | 每个账号固定一个 edge，如 fra1 或 uk1 | 避免同一 OAuth 账号跨地区出口漂移。 |
| `proxy_id` | `NULL` | edge 本身就是出口，减少代理变量。 |
| `concurrency` | `3` | 当前两边一致，稳定优先。 |
| `enable_tls_fingerprint` | `true` | 开启 TLS ClientHello 模拟。 |
| `tls_fingerprint_profile_id` | 显式绑定 **`tls_fingerprint_profiles.name = claude_cli_2_1_142_node24_20260515`** | 与 tier baseline / 运维 skill 对齐；可审计、跨 edge 一致；避免「仅启用、无模板行」。 |
| `user_msg_queue_mode` | `serialize` | 降低同一用户并发乱序和突刺。 |
| `session_id_masking_enabled` | `true` | 降低 `metadata.user_id` session 部分频繁变化。 |
| `cache_ttl_override_enabled` | `true` | 统一 cache token TTL 行为。 |
| `cache_ttl_override_target` | `1h` | 对 Claude Code 长上下文更稳定。 |
| `base_rpm` | `8` | 两边已有基线，保守。 |
| `rpm_strategy` | `tiered` | 比硬切断更平滑。 |
| `rpm_sticky_buffer` | `3` 起步；若重视会话连续性可试 `5` | `3` 更保守，`5` 更有利于 sticky 会话不断流。 |
| `max_sessions` | `8` 起步 | uk1 的 `10` 更激进；建议先按 fra1 的 `8` 起步。 |
| `session_idle_timeout_minutes` | `8` | 比 uk1 的 `5` 更少 session churn。 |
| `window_cost_limit` | `100–150` 起步 | 当前两边 `200` 对单账号偏宽，较晚降速。 |
| `window_cost_sticky_reserve` | `10` | 当前 `5` 偏小；配合 lower limit 更平滑。 |
| `intercept_warmup_requests` | `true` | 减少 warmup 对上游账号的噪音。 |
| `temp_unschedulable_enabled/rules` | 开启 429 暂停规则 | fra1 有，uk1 缺；建议新账号统一配置。 |

### P1：先消除 edge 版本差异

| 事实 | 风险 | 建议 |
|---|---|---|
| fra1 镜像 `1.7.31`，uk1 镜像 `1.7.27` | 行为差异可能来自版本，而非账号配置。 | 在继续做 A/B 之前，把 uk1 升级到同一版本。 |

### P1：为 uk1 补齐显式 TLS profile

| 事实 | 风险 | 建议 |
|---|---|---|
| uk1 `enable_tls_fingerprint=true` 但 `tls_fingerprint_profiles` 表为空，且账号无 `tls_fingerprint_profile_id` | 只能依赖内置默认；后台不可审计，不利于跨 edge 对齐。 | 在 uk1 通过 tier baseline **`claude_cli_2_1_142_node24_20260515`** upsert profile 并绑定新健康账号（`manage-anthropic-config` 写入面 **(A)** 或等价流程）。旧 disabled 账号不建议继续修。 |

### P2：清理不要继承的历史状态

| 字段 | 事实 | 建议 |
|---|---|---|
| `model_rate_limits` | fra1/uk1 都有历史限流条目，uk1 甚至有 `gpt-5.5` 这种与 Anthropic 账号不匹配的键 | 新账号初始化时不要复制旧账号的 `model_rate_limits`。 |
| `session_window_utilization` / `passive_usage_*` | 都是账号停止前的历史观测值 | 新账号应从空观测开始，让系统重新采样。 |
| `expires_at` / `expires_in` 类型 | fra1 是 number，uk1 是 string | 新导入统一用 number，减少解析分支。 |

## 9. 推荐执行顺序

1. 不再将这两个 `Organization disabled` 账号作为稳定性优化对象。
2. 先把 uk1 升级到与 fra1 相同的线上版本，减少变量。
3. 为每个 edge 新建健康 Anthropic OAuth 账号。
4. 套用同一套稳定性基线：固定 edge、无代理、显式 TLS profile（**`claude_cli_2_1_142_node24_20260515`**）、serialize 队列、session masking、1h cache TTL、保守 RPM/session/window 限制。
5. 小流量 smoke：先 Haiku/Sonnet，再 Opus；观察 400/401/403/429/529 分类。
6. 只有在账号 `status=active` 且产生新请求后，才验证 TLS profile 是否在转发日志中实际生效。

## 10. 本次不建议立即修改的项

| 项 | 原因 |
|---|---|
| 直接把两个账号 `status` 从 `error` 改回 `active` | 上游已经返回 organization disabled；强行恢复只会继续失败。 |
| 继续提高 concurrency / max_sessions | 当前目标是稳定，不是吞吐；两个账号已经 disabled，不能证明高并发安全。 |
| 给 edge 账号叠加代理 | 会引入出口身份漂移，与 edge 固定出口目标冲突。 |
| 用旧账号配置完整复制到新账号 | 会复制 stale `model_rate_limits`、usage utilization、历史窗口状态。 |
