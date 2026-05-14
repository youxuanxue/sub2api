# Edge Anthropic OAuth 稳定配置基线

本清单把 `docs/accounts/anthropic-oauth-edge-account-stability-2026-05-14.md` 中的线上事实固化成可复用基线，用于新增或巡检 Edge Stage0 Anthropic OAuth 账号。

目标不是复制旧账号状态，而是复制稳定行为：固定 edge 出口、无二级代理、固定 TLS ClientHello、串行用户消息、稳定 session/cache 行为、保守 RPM 与窗口成本保护。

机器可读源：`deploy/aws/stage0/anthropic-oauth-stability-baseline.json`。

## 快速命令

只读检查线上账号与稳定基线的差异：

```bash
python3 scripts/check-edge-anthropic-oauth-stability.py \
  --edge-id fra1 \
  --account-name cc-fr-fra-ec2-5-1-a
```

输出 JSON：

```bash
python3 scripts/check-edge-anthropic-oauth-stability.py \
  --edge-id fra1 \
  --account-name cc-fr-fra-ec2-5-1-a \
  --json
```

只生成修正 SQL，不执行线上更新：

```bash
python3 scripts/check-edge-anthropic-oauth-stability.py \
  --edge-id uk1 \
  --account-name cc-en-ld-ec2-16-1-a \
  --emit-sql /tmp/uk1-oauth-stability.sql
```

登记一个账号到稳定名单，仅修改本仓库 JSON，不访问线上：

```bash
python3 scripts/check-edge-anthropic-oauth-stability.py \
  --edge-id fra1 \
  --account-name cc-fr-fra-ec2-5-1-a \
  --update-stable-list \
  --confirm yes-update-anthropic-stable-list
```

## 稳定基线字段

### Account 顶层字段

| 字段 | 基线 | 作用 |
|---|---:|---|
| `platform` | `anthropic` | 固定 Anthropic 原生网关链路。 |
| `type` | `oauth` | 固定 OAuth 账号类型。 |
| `proxy_id` | `null` | Edge 本身作为出口，不叠加二级代理。 |
| `concurrency` | `2` | 更保守并发上限，优先保护账号。 |
| `load_factor` | `null` | 空值时按并发作为负载因子。 |
| `priority` | `1` | 单 edge 主账号优先级。 |
| `rate_multiplier` | `1.0` | 默认账号倍率。 |
| `auto_pause_on_expired` | `true` | 到期自动暂停。 |
| `channel_type` | `0` | Anthropic 原生账号不走 New API channel。 |

### Credentials 配置字段

| 字段 | 基线 | 作用 |
|---|---:|---|
| `intercept_warmup_requests` | `true` | 减少 warmup 对上游账号的噪音。 |
| `temp_unschedulable_enabled` | `true` | 启用可恢复错误的临时暂停。 |
| `temp_unschedulable_rules` | 429(`rate limit`/`too many requests`) 30 分钟；529(`overloaded`/`capacity`) 15 分钟；401(`invalid_token`/`expired`) 30 分钟；403(`account_disabled_auth_error`/`organization disabled`) 6 小时 | 对可恢复过载、凭据异常、账号禁用做分层熔断，避免持续打流。 |

### Extra 配置字段

| 字段 | 基线 | 作用 |
|---|---:|---|
| `enable_tls_fingerprint` | `true` | 启用出站 TLS ClientHello 模拟。 |
| `base_rpm` | `5` | 更早进入控流区，优先保护账号。 |
| `rpm_strategy` | `tiered` | 绿区、sticky-only、红区三段保护。 |
| `rpm_sticky_buffer` | `3` | 超过 base_rpm 后的 sticky 缓冲，保持当前会话连续性与保护平衡。 |
| `user_msg_queue_mode` | `serialize` | 同一用户消息串行，减少突刺和乱序。 |
| `max_sessions` | `4` | 收紧会话数上限，降低账号压力。 |
| `session_idle_timeout_minutes` | `8` | 降低过快 session churn。 |
| `session_id_masking_enabled` | `true` | 稳定 `metadata.user_id` 的 session 部分。 |
| `cache_ttl_override_enabled` | `true` | 统一 cache creation TTL。 |
| `cache_ttl_override_target` | `1h` | 对 Claude Code 长上下文更稳定。 |
| `window_cost_limit` | `150` | 保持当前阈值，兼顾保护与可用性。 |
| `window_cost_sticky_reserve` | `3` | 收紧窗口超阈值后的 sticky 保留配额。 |
| `custom_base_url_enabled` | `false` | 禁止 Anthropic OAuth 账号绕过本 Edge 出口改走自定义 relay。 |
| `custom_base_url` | 不存在 / 空 | 关闭自定义 relay 后不保留出站 URL 残留。 |

### TLS profile

基线要求显式 profile，而不是只开启 `enable_tls_fingerprint` 后依赖内置默认。脚本会单独 diff `tls_fingerprint_profiles` 内容。

| 字段 | 基线 |
|---|---|
| `name` | `claude_cli_nodejs24_fixed` |
| `enable_grease` | `false` |
| `alpn_protocols` | `["http/1.1"]` |
| `supported_versions` | `[772, 771]` |
| `key_share_groups` | `[29]` |

完整 cipher suites、curves、signature algorithms 和 extensions 以 `deploy/aws/stage0/anthropic-oauth-stability-baseline.json` 为准。

## 明确排除字段

以下字段是运行态、观测值、身份值或敏感值，不应复制到稳定基线，也不参与 diff：

- `status`
- `error_message`
- `last_used_at`
- `created_at`
- `updated_at`
- `schedulable`
- `rate_limited_at`
- `rate_limit_reset_at`
- `overload_until`
- `temp_unschedulable_until`
- `temp_unschedulable_reason`
- `session_window_start`
- `session_window_end`
- `session_window_status`
- `model_rate_limits`
- `session_window_utilization`
- `passive_usage_7d_utilization`
- `passive_usage_7d_reset`
- `passive_usage_sampled_at`
- `access_token`
- `refresh_token`
- `token_type`
- `account_uuid`
- `org_uuid`
- `email_address`

## 新 Edge 接入流程

1. 在 `deploy/aws/stage0/edge-targets.json` 中确认 edge 已 `deployable=true`。
2. 在目标 edge 创建健康 Anthropic OAuth 账号。
3. 不绑定二级代理，让账号固定使用该 edge 出口。
4. 确认或创建 `claude_cli_nodejs24_fixed` TLS profile。
5. 运行只读 diff：

   ```bash
   python3 scripts/check-edge-anthropic-oauth-stability.py \
     --edge-id <edge-id> \
     --account-name <account-name>
   ```

6. 如有 drift，先生成 SQL 审阅：

   ```bash
   python3 scripts/check-edge-anthropic-oauth-stability.py \
     --edge-id <edge-id> \
     --account-name <account-name> \
     --emit-sql /tmp/<edge-id>-oauth-stability.sql
   ```

7. 人工审阅 SQL，确认不含 token、邮箱、UUID 后再决定是否手动执行。
8. 小流量 smoke：先 Haiku/Sonnet，再 Opus，观察 400/401/403/429/529 分类。
9. 账号保持 `active` 且请求成功后，登记到稳定名单：

   ```bash
   python3 scripts/check-edge-anthropic-oauth-stability.py \
     --edge-id <edge-id> \
     --account-name <account-name> \
     --update-stable-list \
     --confirm yes-update-anthropic-stable-list
   ```

## 脚本安全边界

`scripts/check-edge-anthropic-oauth-stability.py` 的默认行为是只读：

- 只接受 `edge-targets.json` 中存在且 `deployable=true` 的 edge。
- 线上查询只读取 `platform='anthropic' AND type='oauth' AND deleted_at IS NULL` 的账号。
- 会把 `custom_base_url_enabled=true` 或残留 `custom_base_url` 判为 drift，避免账号改走非 Edge 出站路径。
- 不回显 OAuth token、邮箱、account UUID 或 org UUID。
- `--emit-sql` 只写本地 SQL 文件，不执行；生成内容会关闭自定义 base URL 并移除残留 URL。
- `--update-stable-list` 只更新 repo 内 JSON，必须显式确认。
- 首版没有线上 `--apply`，避免把生产数据库写入和清单维护混在一个低门槛命令里。
