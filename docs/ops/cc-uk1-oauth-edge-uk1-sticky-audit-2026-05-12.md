# claude code CLI → prod cc-uk1-oauth → edge-uk1 sticky 链路审计

> 查询日期：2026-05-12
>
> 范围：prod (`api.tokenkey.dev`) 上的 `cc-uk1-oauth` 分组，以及 edge-uk1 (`api-uk1.tokenkey.dev`) 的代码路径
>
> 触发：排查 claude code CLI 经 prod cc-uk1-oauth 转发到 edge-uk1 账号 `cc-en-ld-ec2-16-1-a` 的 session sticky 现状与隐患
>
> 同期：base commit `4be7dd9c`；关键依赖 `42f79b3f`（Preserve edge sticky session identity）、`8396d36b`（sticky source observability）
>
> 配套文档：账号 / 分组 / 部署侧的推荐配置和注意事项见 [`cc-uk1-oauth-edge-uk1-config-2026-05-12.md`](./cc-uk1-oauth-edge-uk1-config-2026-05-12.md)

## 1. 调用链结构

```
claude code CLI
   │  UA: claude-cli/x.y.z
   │  Hdr: X-Claude-Code-Session-Id: <client-session>
   │  Body.metadata.user_id: user_<uid>_account__session_<client-session>
   ▼
prod  api.tokenkey.dev (CloudFront-less, Caddy → tokenkey:8080)
   │  分组 cc-uk1-oauth · platform=anthropic
   │  GenerateSessionHash(parsed) → priority 1 = inner session_id
   │  GatewayService.SelectAccountWithLoadAwareness → Anthropic OAuth account
   │  account.custom_base_url = https://api-uk1.tokenkey.dev
   │  buildUpstreamRequest →
   │     - OAuth mimic（非 claude-cli UA）：丢客户端 header，但 tkEnsureClaudeCodeSessionHeader
   │       从 body.metadata.user_id 重新塞 X-Claude-Code-Session-Id
   │     - 真 claude-cli UA：白名单透传 X-Claude-Code-Session-Id
   │     - RewriteUserID（enableMPT=false）将 metadata.user_id.session 改写为
   │       hash(prod_account_id::原session)
   │  Redis 本机 key: sticky_session:{prod_group_id}:{sessionHash}
   ▼
edge-uk1  api-uk1.tokenkey.dev (Caddy 仅放行 prod EIP，转 tokenkey:8080)
   │  本机分组（platform=anthropic）
   │  GenerateSessionHash → priority 1 = 改写后的 session_id
   │  GatewayService.SelectAccountWithLoadAwareness → edge OAuth account
   │     例如 cc-en-ld-ec2-16-1-a
   │  buildUpstreamRequest → 真 Claude API
   │  Redis 本机 key: sticky_session:{edge_group_id}:{sessionHash}
   ▼
api.anthropic.com
```

两点结构要点：

- **两端各持本机 Redis**（`deploy/aws/stage0/docker-compose.yml`），sticky 表不互通。靠 `metadata.user_id.session_id` 在 body 内的稳定值在两跳间“对齐”到同一逻辑 session。
- **prod → edge 是私网信任链**：`Caddyfile.edge` 的 `@allowed_relay` 用 `remote_ip ${MAIN_GATEWAY_ALLOWED_CIDR}` 锁死 prod EIP；公网直访 `/v1/*` 一律 403。

## 2. sessionHash 派生优先级（两端同代码）

`backend/internal/service/gateway_service.go:660 GenerateSessionHash`：

| 优先级 | 来源 | 输出 |
|---|---|---|
| 1 | `parsed.MetadataUserID` 解析出 inner `session_id` | 原值，不哈希 |
| 2 | `parsed.ExplicitStickyKey`（`X-Claude-Code-Session-Id` / `X-Session-Id` 等） | xxhash 16hex |
| 3 | `parsed.PromptCacheKey` | xxhash 16hex |
| 4 | cacheable_content（Anthropic ephemeral 块） | xxhash 16hex |
| 5 | `ClientIP + UA + APIKeyID + system + 全部消息` | xxhash 16hex |

只要 `metadata.user_id` 不被中途清掉，两端的 sessionHash 都来自 priority 1。

`StickyKeyFromClientHeaders` 的 header walk 顺序：`session_id` → `conversation_id` → `X-Claude-Code-Session-Id` → `X-Session-Id`。

## 3. 多跳 sticky 保护点（commit 42f79b3f 之后）

`backend/internal/service/gateway_service_tk_sticky.go`：`tkEnsureClaudeCodeSessionHeader` 在四个出站构造函数被调用（含本 PR #190 新增的 Vertex 一处）：

| Callsite | 函数 | 用途 |
|---|---|---|
| `gateway_service.go:5333` | `buildUpstreamRequestAnthropicAPIKeyPassthrough` | Anthropic API Key 透传 |
| `gateway_service.go:6193` | `buildUpstreamRequest`（OAuth + APIKey 直转） | 主路径，cc-uk1-oauth 命中这里 |
| `gateway_service.go:6264` | `buildUpstreamRequestAnthropicVertex` | Vertex Anthropic（PR #190 新增） |
| `gateway_service.go:9392` | count_tokens 路径 | tokens 计费回退 |

策略：先从出站 body 的 `metadata.user_id` 同步 `X-Claude-Code-Session-Id`；body 没有时回退 ingress `ParsedRequest` 快照（`ClaudeCodeParsedRequestGinKey` Gin context）。即使 OAuth mimic 路径丢掉了客户端原始 header，下一跳照样能从 body 中拿到稳定 session。

## 4. 已识别的错误隐患（按严重度）

| ID | 隐患 | 文件 / 行 | 影响 cc-uk1-oauth？ | 状态 |
|---|---|---|---|---|
| **A** | `SelectAccountWithLoadAwareness` 在「有 model routing 配置」命中 sticky 时直接 return，未刷 TTL。无 routing 路径（line 1910）有刷。`selectAccountForModelWithPlatform`（3192）与 `selectAccountWithMixedScheduling`（3457）两条 legacy 路径同样不刷。1 h 后 sticky 静默过期、跨账号丢 prompt cache。 | `gateway_service.go:1711 / 3074 / 3192 / 3337 / 3457` | **不直接影响**（cc-uk1-oauth 默认无 routing） | ✅ PR #190 修复，5 处统一刷 TTL |
| **B** | `buildUpstreamRequestAnthropicVertex` 漏接多跳 sticky preservation，没有调用 `tkEnsureClaudeCodeSessionHeader`。Vertex Anthropic 多跳时 `X-Claude-Code-Session-Id` 不传。 | `gateway_service.go:6264`（修复后） | 不影响（cc-uk1-oauth 不走 Vertex） | ✅ PR #190 修复，加 sentinel + 回归测试 |
| **C** | `session_id_masking_enabled` 与多跳互动：prod 的 OAuth 账号若开 masking，则每 15 min 轮换“伪 session”，edge 看到的 sessionHash 也跟着换 → edge 这一跳 sticky 失效、prompt cache 击穿。 | 账号 `Extra.session_id_masking_enabled` | 取决于配置 | ⚠️ 配置约束，见 [config 文档](./cc-uk1-oauth-edge-uk1-config-2026-05-12.md) §5 |
| **D** | `gateway.metadata_passthrough = false`（默认）：prod 会把 client 的 `metadata.user_id` 改写为 `hash(prod_account_id::原session)`。两端 sessionHash 不是同一个值（prod 用原值、edge 用改写值）。本身不是 bug，日志排查时要注意。 | `setting_service.go:1719` | 仅影响日志关联 | 📘 已知非阻塞 |
| **E** | edge Caddy `header_up X-Forwarded-For {remote_host}`：edge 看到的 ClientIP 永远是 prod EIP。sticky 优先级 1 已绕过 XFF，因此对 sticky 本身无影响，但 ops 日志容易让人误判客户端。 | `deploy/aws/stage0/Caddyfile.edge:45` | 仅影响日志 | 📘 已知非阻塞 |
| **F** | prod `security.url_allowlist.upstream_hosts` 若启用了，必须包含 `api-uk1.tokenkey.dev`，否则 `validateUpstreamBaseURL` 直接拒绝。 | prod `config.yaml` | 取决于配置 | ⚠️ 配置核对项，见 [config 文档](./cc-uk1-oauth-edge-uk1-config-2026-05-12.md) §6 |

## 5. 配套代码修复（PR #190）

- **A：sticky hit TTL 一致性** — `gateway_service.go` 5 处 sticky 命中 return 之前补 `RefreshSessionTTL`，全部对齐 line 1910 / 1701 已有模式。
- **B：Vertex 多跳 sticky preservation** — `buildUpstreamRequestAnthropicVertex` (line 6264) 补一行 `tkEnsureClaudeCodeSessionHeader`；同步更新 `scripts/gateway-tk-sentinels.json` 防止 upstream merge 静默回退；加 `TestGatewayService_VertexAnthropicPreservesClaudeCodeSessionID` 回归测试。

修复后 invariant：

- 所有命中 sticky 的路径都会刷 TTL；
- 所有 Anthropic 出站构造函数（含 Vertex）都会走多跳 sticky 透传。

## 6. 链路 invariant 验证（运维用）

跑一次真实 claude code CLI 请求后，验证：

1. prod ops 日志：`sticky.hash_source` 的 `source` 字段应为 `metadata_user_id`。
2. edge-uk1 ops 日志：同一 `request_id` 串联，`sticky.hash_source` 也应为 `metadata_user_id`。
3. 用 `gh workflow run prod-ops.yml -f log_grep="<base64 of session_id>"` 在 prod 端 grep，对应 `client_request_id` 应能在 edge 端 ops_system_logs 里找到匹配项（8396d36b 之后已具备）。
4. prod 出站请求的 `X-Claude-Code-Session-Id` 与 edge 入站的同名 header 值应一致（来源于 `metadata.user_id.session_id` 解析，跨 hop 稳定）。

不满足任一条，回到 §4 排查对应 ID。

## 7. 不在本文档范围

- new-api 第五平台（`platform=newapi`）的 sticky：见 `docs/approved/sticky-routing.md` §3、`openai_sticky_compat.go`。
- OpenAI / Codex / GLM 类 OpenAI-compat 池的 sticky：同上。
- 跨区域多 edge 的 sticky 一致性：edge-us1 / sg1 / fra1 尚未部署（`deploy/aws/stage0/edge-targets.json` 中 `deployable=false`）。
- 账号 / 分组 / Caddy / `url_allowlist` 等可配置面的推荐值：见 [config 文档](./cc-uk1-oauth-edge-uk1-config-2026-05-12.md)。
