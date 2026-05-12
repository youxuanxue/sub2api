# cc-uk1-oauth / edge-uk1 推荐配置与注意事项

> 查询日期：2026-05-12
>
> 范围：prod (`api.tokenkey.dev`) `cc-uk1-oauth` 分组及其下挂账号、edge-uk1 (`api-uk1.tokenkey.dev`) 分组与节点配置
>
> 前置：本文档假定读者已读过同期的链路审计报告 → [`cc-uk1-oauth-edge-uk1-sticky-audit-2026-05-12.md`](./cc-uk1-oauth-edge-uk1-sticky-audit-2026-05-12.md)
>
> 适用代码版本：base commit `4be7dd9c` + PR #190（sticky TTL 一致性 + Vertex 多跳 sticky 修复）

## 1. prod `cc-uk1-oauth` 分组

| 字段 | 推荐值 | 理由 |
|---|---|---|
| `platform` | `anthropic` | 走 Anthropic 单平台调度池 |
| `sticky_routing_mode` | `auto`（默认） | gateway 自动派生+注入 sticky key |
| `claude_code_only` | `true`，配 `fallback_group_id_on_invalid_request` 兜底 | 这条链路是给 Claude Code CLI 服务的 |
| `model_routing_enabled` | `false` | 关掉 routing 即可避开 [审计文档 §4 A](./cc-uk1-oauth-edge-uk1-sticky-audit-2026-05-12.md#4-已识别的错误隐患按严重度) 的 latent bug，全走 Layer 1.5 路径；PR #190 已修复 A，但保持关闭仍是最稳路径 |
| `require_oauth_only` | `true` | 阻止把 APIKey 账号混入 OAuth 池 |
| `rpm_limit` | 实测峰值 × 0.8 | 给 Anthropic 上游 429 留余量 |

## 2. prod cc-uk1-oauth 下挂的账号（即指向 edge-uk1 的 upstream 账号）

| 字段 | 推荐值 | 理由 |
|---|---|---|
| `Type` | 与 token 形态匹配，**二选一并锁定** | edge 的 `api_key_auth.go` 同时接受 `Authorization: Bearer` 与 `x-api-key`；推荐用 `apikey` + edge 的 `tk_xxx`，避免与真 Anthropic OAuth 语义混淆 |
| `custom_base_url_enabled` | `true`（OAuth 类型时） | 触发 `buildUpstreamRequest` 的 custom_base_url 分支 |
| `custom_base_url` / `base_url` | `https://api-uk1.tokenkey.dev` | **必须** 在 prod 的 `security.url_allowlist.upstream_hosts`（若开启）内，见 §6 |
| `session_id_masking_enabled` | **`false`** | 见 §5 的多跳 masking 约束（**这是最容易踩的坑**） |
| `enable_fingerprint`（`gateway.fingerprint_unification`） | prod 全局可关 | 真伪装在 edge → Anthropic 做，prod → edge 已经在私网链路 |
| `Concurrency` | 1～4，跟 edge 这一跳能消化的 QPS 匹配 |  |
| `Priority` | 多账号时同优先级 + LRU 自动均衡 | 默认即可 |

## 3. edge-uk1 分组与账号

| 项 | 推荐值 | 理由 |
|---|---|---|
| edge 上的分组 | `platform=anthropic`，`sticky_routing_mode=auto`，`require_oauth_only=true`，`model_routing_enabled=false` | 与 prod 对称 |
| edge 账号（`cc-en-ld-ec2-16-1-a` 等） | `Type=oauth`（真 Anthropic OAuth），`session_id_masking_enabled=true`，`enable_fingerprint=true` | masking 留在这一跳，见 §5 |
| `security.url_allowlist.enabled` | `true`，`upstream_hosts=["api.anthropic.com"]` | edge 只允许出站到 Anthropic |
| Caddy `MAIN_GATEWAY_ALLOWED_CIDR` | 严格写 prod EIP（当前 `34.194.234.88/32`） | 阻断公网直访 `/v1/*` |
| Redis 持久化 | `appendonly yes`、`appendfsync everysec`（已是） | sticky 表丢了重新绑、不致命 |
| `gateway.metadata_passthrough` | `false`（默认） | 让 edge 在最后一跳把 metadata.user_id 改写成 Anthropic 期望的指纹形态 |

## 4. sticky TTL 注意事项

- 当前 anthropic 路径的 sticky TTL 硬编码 `stickySessionTTL = time.Hour`（`backend/internal/service/gateway_service.go:47`）。
- PR #190 之后，**5 处 sticky-hit 路径** 都会在命中时刷新 TTL，所以「不停拿同 session 持续请求」的场景不会过期。
- 但「请求间隔 > 1h」时仍会过期；如果你常见单 session 跨 1h 完全空闲再回头，需要把这个常量调大、或抽成配置项（不在本次范围）。
- `gateway.openai_ws.sticky_session_ttl_seconds`（默认 3600）只影响 OpenAI-compat 池，**不影响 anthropic OAuth 路径**。

## 5. ⚠️ 核心注意事项：多跳路径上的 `session_id_masking_enabled`

**规则：开 masking 的只能是「最后一跳到真 Anthropic」的账号；中间任何一跳（prod → edge）开 masking 都会破坏下游 sticky。**

原理：

- `RewriteUserIDWithMasking`（`backend/internal/service/identity_service.go:269`）在该账号开 masking 时，每 15 min 轮换一次假 session_id。
- prod 上的账号如果开 masking → prod 出站给 edge 的 body 里 `metadata.user_id.session_id` 每 15 min 变一次。
- edge 的 sessionHash 派生 priority 1 是 `metadata.user_id.session_id`，跟着每 15 min 变 → edge 的 sticky 绑定每 15 min 失效一次 → upstream prompt cache 击穿。

正确分工：

| 跳 | `session_id_masking_enabled` | 目的 |
|---|---|---|
| client → prod | 不适用（client 自己出 session_id） | — |
| prod → edge-uk1 | **`false`** | 让 edge 看到的 session_id 稳定，edge 的 sticky 才能跨多次请求复用同一 OAuth 账号 |
| edge-uk1 → api.anthropic.com | **`true`** | 防止 Anthropic 根据稳定 session_id 关联多账号活动 |

排查思路：如果 edge 上看到「同一 client session 在 15 min 边界后切到了不同账号」，第一件事就是检查 prod 的 `cc-uk1-oauth` 账号是不是误开了 masking。

## 6. ⚠️ prod 出站 URL 白名单核对

如果 prod 的 `backend/config.yaml`（或 SSM 注入的 config）启用了 URL 白名单：

```yaml
security:
  url_allowlist:
    enabled: true
    upstream_hosts:
      - api.anthropic.com
      - api-uk1.tokenkey.dev    # ← 必须有
```

**漏掉 `api-uk1.tokenkey.dev` 时的症状**：`buildUpstreamRequest` 内部的 `validateUpstreamBaseURL`（`gateway_service.go:9419` 一带）直接返回错误，所有 cc-uk1-oauth 的请求 502。

排查命令（在 prod EC2 上）：

```bash
# 查 SSM 注入的 config（如果用 SSM 参数）
aws ssm get-parameter --name /tokenkey/prod/config.yaml --with-decryption \
  --query 'Parameter.Value' --output text | yq '.security.url_allowlist'

# 或直接看容器内
docker exec tokenkey cat /app/config.yaml | yq '.security.url_allowlist'
```

`security.url_allowlist.enabled=false` 时该校验不生效（不推荐生产环境关掉，但作为快速排查手段可以临时关）。

## 7. 配置完成后的验证步骤

按 [审计文档 §6](./cc-uk1-oauth-edge-uk1-sticky-audit-2026-05-12.md#6-链路-invariant-验证运维用) 跑一次真实 claude code CLI 请求，确认四条 invariant 全过：

1. prod ops `sticky.hash_source.source` = `metadata_user_id`
2. edge ops `sticky.hash_source.source` = `metadata_user_id`
3. prod ↔ edge 端的 `request_id`/`client_request_id` 能在 `ops_system_logs` 串联
4. prod 出站 `X-Claude-Code-Session-Id` 与 edge 入站同名 header 一致

任何一条失败：

- 1 / 2 不是 `metadata_user_id` → 检查客户端是否带了 `metadata.user_id`、prod 是否误清掉了 body 中该字段
- 3 拉不到 → 检查 8396d36b 后 ops 日志接入是否生效（`gh workflow run prod-ops.yml` 是否正常）
- 4 不一致 → 回到 §5 masking 排查；或检查 [审计文档 §4 D](./cc-uk1-oauth-edge-uk1-sticky-audit-2026-05-12.md#4-已识别的错误隐患按严重度)（RewriteUserID 改写带来的预期差）

## 8. 不在本文档范围

- 链路结构与代码层面的 sticky preservation 机制 → 审计文档
- new-api / OpenAI-compat 池的配置 → 见 `docs/approved/sticky-routing.md`
- 跨区域多 edge（us1 / sg1 / fra1）配置 → 这些 edge 当前 `deployable=false`，未来开通时再扩展本文档
