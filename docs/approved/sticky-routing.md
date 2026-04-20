---
title: Sticky Routing & Prompt Cache Optimization
status: shipped
approved_by: xuejiao (post-hoc 2026-04-19)
approved_at: 2026-04-19
authors: [agent]
created: 2026-04-17
related_prs: []
related_commits: [a68dee5b]
related_stories: [US-006]
---

# Sticky Routing & Prompt Cache Optimization

## 0. TL;DR

让来自同一个客户端会话的请求**尽可能命中上游同一个 prompt cache 块**，把 token 成本压下去（社区数据：长程 Agent 任务可降 60% 上行 token，Anthropic OAuth cache TTL 可保 1 小时）。

> 范围：本设计**仅**补齐"客户端没送 sticky key 时网关自动派生并注入"的能力，以及把现有分散的注入逻辑收口到统一抽象后面，并加全局/分组级开关 + 命中率可视化。**不**改账号粘性调度（已成熟）、**不**修改真 Claude Code 客户端 → Anthropic OAuth 路径（有意识保留客户端原行为）。

## 1. 现状盘点

### 1.1 已有基础设施（不动）

| 模块 | 文件:行 | 作用 |
|---|---|---|
| `DeriveSessionHashFromSeed` | `backend/internal/service/openai_sticky_compat.go:32-48` | xxHash64 16-hex；网关侧账号粘性调度键 |
| `BindStickySession` / `GetSessionAccountID` | `backend/internal/service/openai_gateway_service.go:1190-1199` | Redis `openai:` + sessionHash → account_id，TTL 默认 1h |
| `deriveCompatPromptCacheKey` | `backend/internal/service/openai_compat_prompt_cache_key.go:21-65` | Chat Completions compat 路径，只对 gpt-5.4 / gpt-5.3-codex 自动注入 |
| `ExtractSessionID` / `GenerateSessionHash` | `backend/internal/service/openai_gateway_service.go:1128-1156` | header > prompt_cache_key > 内容种子 三级回退 |
| `ensureClaudeOAuthMetadataUserID` | `backend/internal/service/gateway_service.go:1014-1041` | Anthropic OAuth mimic 路径自动注入 metadata.user_id |
| `Antigravity SessionID` | `backend/internal/pkg/antigravity/request_transformer.go:146-163` | 基于 contents 的稳定 session id |
| `usage_logs.cache_read_tokens` | `backend/ent/schema/usage_log.go:72-79` | dashboard 已聚合 `total_cache_read_tokens` |
| `gateway.openai_ws.sticky_session_ttl_seconds` | `backend/internal/config/config.go:543-556` | 全局 TTL（无 kill-switch） |

### 1.2 真正的 Gap

| Gap | 当前行为 | 用户可感知影响 |
|---|---|---|
| **G1** Codex Responses 路径不自动注入 body `prompt_cache_key` | 客户端不送时仅做账号粘性，不送给上游 → 上游 LB 不知道这是同会话 | Cursor/Codex CLI 长任务命中率低 |
| **G2** OpenAI passthrough 模式下完全不补 sticky 头 | 完全裸透 | passthrough 用户拿不到优化 |
| **G3** Compat 自动注入白名单太窄 | 仅 `gpt-5.4` / `gpt-5.3-codex` | 其它 GPT 系列同样能受益但被排除 |
| **G4** 真 Claude Code → OpenAI 翻译路径 | handler 仅在客户端送了 `metadata.user_id` 时才合成 cache key | 多数 Claude Code 用户没显式送 metadata.user_id |
| **G5** 无统一注入器抽象 | 注入逻辑分散在 5+ 个文件 | 后续加 Anthropic 后端时易漏 |
| **G6** 无全局 kill-switch | 出问题只能改代码回滚 | 运维不安全 |
| **G7** 无分组级策略 | 不同账号源（OpenAI 官方 vs Anthropic vs GLM）只能同一策略 | 灵活性不足 |
| **G8** 命中率不可视 | 后端有数据但 dashboard 无独立卡片 | 调优盲飞 |

### 1.3 显式不做（避免 scope creep）

- ❌ 真 Claude Code 客户端 → Anthropic OAuth 直连：不动 `ensureClaudeOAuthMetadataUserID` 在 `isClaudeCode==true` 时跳过的逻辑（社区已知会和客户端策略打架）。
- ❌ NewAPI 渠道亲和：已有 `GetPreferredChannelByAffinity`，不重复造。
- ❌ Antigravity 内部 SessionID：内部协议，与 prompt cache 无直接对应。
- ❌ 改 `usage_logs` schema：现有 `cache_read_tokens` 足够。

---

## 2. 架构

### 2.1 统一注入器抽象

新增 `backend/internal/service/sticky_session_injector.go`：

```go
package service

// StickyKey 是网关从客户端请求派生出的统一 sticky 标识。
// Source 标明派生来源，便于日志归因。
type StickyKey struct {
    Value  string  // 派生后的字符串（已脱敏 / hash）
    Source string  // "client_session_id" | "client_conversation_id" | "client_metadata_user_id" | "client_prompt_cache_key" | "derived_content_hash" | "derived_first_user_hash"
}

// StickySessionInjector 把 StickyKey 翻译成各个上游协议要求的字段并写回请求。
type StickySessionInjector interface {
    // Derive 优先从客户端 header/body 提取已有 sticky id；失败则按策略派生兜底。
    Derive(ctx context.Context, req *InjectionRequest) StickyKey

    // InjectOpenAIResponses 把 sticky 信息写入 Codex/Responses 上游 body + header。
    InjectOpenAIResponses(req *InjectionRequest, body []byte, key StickyKey) ([]byte, error)

    // InjectOpenAIChatCompletions 同上，针对 /v1/chat/completions 上游。
    InjectOpenAIChatCompletions(req *InjectionRequest, body []byte, key StickyKey) ([]byte, error)

    // InjectAnthropicMessages 写入 metadata.user_id（仅在策略允许时）。
    InjectAnthropicMessages(req *InjectionRequest, body []byte, key StickyKey) ([]byte, error)
}

// InjectionRequest 收口注入决策需要的所有上下文，避免函数签名爆炸。
type InjectionRequest struct {
    APIKeyID       int64
    GroupID        int64
    UpstreamModel  string
    AccountKind    string  // "openai_oauth" | "openai_apikey" | "anthropic_oauth" | "gemini" | "antigravity" | "newapi"
    IsClaudeCodeUA bool    // true 时按"客户端自带策略"放行，禁止 metadata 注入
    Strategy       StickyStrategy  // 来自 group + global 合成
    Headers        http.Header
}

// StickyStrategy 来自 group + global setting 合成。
type StickyStrategy struct {
    GlobalEnabled bool       // 来自 setting `sticky_routing.enabled`，默认 true
    Mode          StickyMode // 来自 group.sticky_routing_mode
}

type StickyMode string
const (
    StickyModeAuto        StickyMode = "auto"        // 默认：派生 + 注入
    StickyModePassthrough StickyMode = "passthrough" // 仅透传客户端已送的，不派生不补
    StickyModeOff         StickyMode = "off"         // 全关
)
```

### 2.2 派生算法（`Derive`）

按优先级回退：

```
1. headers["session_id"] / headers["conversation_id"]                    → source = client_session_id
2. body.metadata.user_id (parsed)                                        → source = client_metadata_user_id
3. body.prompt_cache_key                                                 → source = client_prompt_cache_key
4. (兜底) hash( api_key_id || system_message_前 2KB || tools_signature ) → source = derived_content_hash
5. (空) 不注入                                                            → source = ""
```

**关键设计点**：
- 兜底 hash 使用 **api_key_id**（不是 group_id）作为命名空间种子 → 同 group 不同 key 不互踩；同 key 不同 system prompt 自然分桶。
- system + tools 截断到前 2KB / tools 仅取 name+description hash → 既稳定又不被超长 prompt 拖慢。
- 输出统一格式：`tk_{16hex}` (xxHash64)，方便日志识别 + 与现有 `compat_cc_` / `compat_cs_` 区分。

### 2.3 注入策略矩阵

| AccountKind | 上游协议 | sticky 字段 | 是否注入 body | 是否设 header |
|---|---|---|---|---|
| `openai_oauth` (Codex/Responses) | OpenAI Responses | `prompt_cache_key` (body) + `session_id` / `conversation_id` (header, isolated) | ✅ 当 mode=auto | ✅ |
| `openai_apikey` (Platform API) | OpenAI Responses | `prompt_cache_key` (body) | ✅ | ❌ |
| `openai_oauth` (Chat Completions compat) | OpenAI Chat Completions | `prompt_cache_key` (body, `compat_cc_*` 前缀沿用) | ✅ | 同上 |
| `anthropic_oauth` | Anthropic Messages | `metadata.user_id` | ✅ 当 `IsClaudeCodeUA=false` & mode=auto | ❌ |
| `anthropic_oauth` (真 Claude Code) | Anthropic Messages | — | ❌ | ❌ |
| `newapi` (channel_type>0) | 取决于上游 | header `X-Session-Id` (适配 GLM 等) | ❌ | ✅ 当 mode=auto |
| `gemini` / `antigravity` | 已有内部机制 | — | 不动 | 不动 |

### 2.4 接入点

**最少侵入**原则：每个调用点只新增 1-3 行。

| 接入点 | 文件:行（现状） | 改动 |
|---|---|---|
| Codex Responses 主路径 | `openai_gateway_service.go:1880-1884` | `if pck=="" && strategy.allows() { inject(...) }` |
| OpenAI passthrough | `openai_gateway_service.go:2700-2740` | 同上 |
| Chat Completions compat | `openai_gateway_chat_completions.go:69-74` | 把 `shouldAutoInjectPromptCacheKeyForCompat` 替换为 strategy 判定 |
| Messages → OpenAI | `openai_gateway_handler.go:597-611` | 把 if 条件从"有 metadata.user_id 才合成"扩展为"按 strategy 决定" |
| Anthropic OAuth mimic | `gateway_service.go:1077-1081` | 注入逻辑改用统一 injector，但 `IsClaudeCodeUA` 时 short-circuit |
| NewAPI 透传 | `gateway_handler_tk_affinity.go` | 新增 header 注入步骤（仅 `X-Session-Id`） |

---

## 3. Schema 变更（P2）

### 3.1 新增 Group 字段

`backend/ent/schema/group.go` 增加：

```go
field.Enum("sticky_routing_mode").
    Values("auto", "passthrough", "off").
    Default("auto").
    Comment("Sticky routing strategy for upstream prompt cache hits"),
```

迁移 SQL（`backend/migrations/093_add_group_sticky_routing.sql`）：

```sql
ALTER TABLE groups
ADD COLUMN sticky_routing_mode VARCHAR(16) NOT NULL DEFAULT 'auto';
COMMENT ON COLUMN groups.sticky_routing_mode IS 'Sticky routing strategy: auto (derive + inject) | passthrough (forward only) | off';
```

### 3.2 新增全局 setting

复用现有 settings 表，key 名常量加到 `domain_constants.go`：

```go
SettingKeyStickyRoutingEnabled = "gateway.sticky_routing.enabled"  // bool, default true
```

读取入口：`GetGatewayForwardingSettings` 同址扩展，返回值结构体加 `StickyRoutingEnabled bool`。

合成规则：`final.enabled = globalEnabled && group.mode != "off"`，`final.mode = group.mode`。

---

## 4. UI 变更（P2）

### 4.1 全局设置页

`frontend/src/views/admin/SettingsView.vue`（或对应 GatewaySettings 子页）加一个 toggle："启用 Prompt Cache 粘性路由（全局）"，绑定 `gateway.sticky_routing.enabled`。

### 4.2 分组编辑

`frontend/src/views/admin/GroupsView.vue` 在分组编辑表单的"高级"区块加一个下拉：

| 选项值 | 中文标签 | Tooltip |
|---|---|---|
| `auto` | 自动派生 + 注入（推荐） | 客户端没送 sticky 标识时网关自动按 (api_key + system + tools) 派生并写入上游，最大化命中率 |
| `passthrough` | 仅透传客户端 | 只有客户端自己带 prompt_cache_key / metadata.user_id 时才转发，不补 |
| `off` | 关闭 | 完全不处理 sticky 字段 |

---

## 5. 命中率可视化（P3）

### 5.1 后端聚合

`backend/internal/handler/admin/dashboard_handler.go` 已有 `total_cache_read_tokens`。新增按时间窗 + 按 group/api_key 的命中率字段：

```go
type CacheStats struct {
    InputTokens     int64   `json:"input_tokens"`
    CacheReadTokens int64   `json:"cache_read_tokens"`
    HitRate         float64 `json:"hit_rate"`  // CacheReadTokens / (InputTokens + CacheReadTokens)
}
```

新接口（小步加，不动既有）：
- `GET /admin/dashboard/cache-stats?window=24h&group_by=api_key`
- 复用现有 `usage_logs` 查询，加 `GROUP BY api_key_id`。

### 5.2 前端卡片

`frontend/src/views/admin/DashboardView.vue` 加一个 "Prompt Cache 命中率" 卡片：
- 顶部数字：过去 24h 整体命中率（百分比 + 绝对节省的 input tokens）
- 下方 mini-table：top 5 API key by 命中率（升序，凸显需要优化的）
- 颜色：>50% 绿、20-50% 黄、<20% 红

无需 chart 库新依赖（用现有 stat-card 组件 + 简单 v-for 列表）。

---

## 6. 测试计划

### 6.1 User Story

`.testing/user-stories/stories/US-201-sticky-routing.md`（编号待定）覆盖：
- AC-001 (正向, OpenAI Codex)：Cursor 客户端连续 5 个请求未送 prompt_cache_key + 同 system → 全部上游 body 注入相同的 `tk_*` key
- AC-002 (正向, Anthropic mimic)：非 Claude Code UA + Anthropic OAuth 账号 + 客户端无 metadata → 网关注入 metadata.user_id
- AC-003 (负向, 真 Claude Code)：UA=Claude Code + Anthropic OAuth → 不注入 metadata（不打架）
- AC-004 (负向, group=off)：分组 mode=off → 不论客户端送什么都不派生
- AC-005 (负向, global=off)：全局开关关闭 → 所有分组都不派生
- AC-006 (回归)：客户端已送 prompt_cache_key → 网关不覆盖（passthrough 优先）
- AC-007 (回归, compat 路径)：`gpt-5.4` 现有自动注入行为不变

### 6.2 测试文件

| 文件 | 覆盖 |
|---|---|
| `backend/internal/service/sticky_session_injector_test.go` | Derive 五级回退；各 Inject* 方法 |
| `backend/internal/service/sticky_session_strategy_test.go` | global/group 合成；StickyMode 三档行为 |
| `backend/internal/service/openai_gateway_service_sticky_test.go` | 既有 hot-path 测试加 sticky 注入用例 |
| `backend/internal/handler/openai_gateway_handler_messages_sticky_test.go` | Messages→OpenAI 路径 |
| `backend/internal/service/gateway_service_sticky_test.go` | Anthropic mimic / Claude Code skip |
| `frontend/src/components/keys/__tests__/UseKeyModal.spec.ts` | 已加（P0） |
| `frontend/src/views/admin/__tests__/GroupsViewSticky.spec.ts` | 分组下拉 |
| `frontend/src/views/admin/__tests__/DashboardCacheCard.spec.ts` | 命中率卡片 |

### 6.3 风险覆盖

| 风险类型 | 场景 | 测试 |
|---|---|---|
| 逻辑错误 | 派生算法稳定性（同输入得同输出） | `TestUS201_DeriveStable` |
| 行为回归 | 既有 compat 自动注入 | `TestUS201_CompatAutoInjectUnchanged` |
| 安全问题 | 跨 api_key 不互踩；hash 不泄露 system 内容 | `TestUS201_NoCrossAPIKeyCollision`, `TestUS201_HashNotReversible` |
| 运行时问题 | strategy 计算的 hot-path 性能；并发 derive 一致 | benchmark + `-race` |

---

## 7. 实施顺序与 PR 切分

> **实施实情（2026-04-19 事后盘点）**：本表是设计当时拟定的"理想 8-PR 切分"。
> 代码实际以**单提交 `a68dee5b`**（2026-04-18）一次性落地（schema + injector + 6 处接入点 + 单测 + UI），未拆 PR。
> 这违反了 `product-dev.mdc` §阶段 2 → 审批 → §阶段 3 顺序，详见 §11 实施情况与 `docs/preflight-debt.md`。
> 下次同等规模特性必须按本表切分。

| 顺序 | 内容 | PR 标题 | 可独立 review |
|---|---|---|---|
| 1 | P0 已完成 | `feat: ship anti-down-grading defaults for Claude Code env template` | ✅ |
| 2 | Schema + setting + 注入器骨架（无接入） | `feat: add sticky-routing schema and injector skeleton` | ✅ |
| 3 | 接入 OpenAI Codex Responses 主路径 | `feat: auto-inject prompt_cache_key in Codex Responses path` | ✅ |
| 4 | 接入 OpenAI passthrough + chat completions compat 改造 | `feat: unify sticky injection across OpenAI paths` | ✅ |
| 5 | 接入 Anthropic mimic + Messages→OpenAI | `feat: unify sticky injection across Anthropic paths` | ✅ |
| 6 | NewAPI X-Session-Id | `feat: forward sticky session header to newapi channels` | ✅ |
| 7 | 全局 + 分组 UI | `feat: ui controls for sticky routing strategy` | ✅ |
| 8 | Dashboard cache 命中率卡片 | `feat: cache hit rate dashboard card` | ✅ |

> 本设计文档（pending）merge 后才能进入 PR 2-8。

---

## 8. 兼容性 & 回滚

### 8.1 默认开关
- 全局 `sticky_routing.enabled = true`
- 分组 `sticky_routing_mode = "auto"`
- → 升级即生效，但任何用户都能改 group → `passthrough` 或 `off` 立即降级，无需回滚镜像。

### 8.2 回滚路径
1. 一键关：管理员全局开关关掉 → 走"仅透传"。
2. 单分组关：分组下拉切 `off`。
3. 代码回滚：本设计的 schema 字段保留为 `auto`，不影响读旧版。

### 8.3 数据迁移
- 新增列默认 `auto`，零数据迁移成本。
- 旧版本不识别该列，PG 自动忽略 → 灰度可双跑。

---

## 9. 已确认决策（事后回填，2026-04-19）

> 实施时（2026-04-18，先于本文档审批）已做出以下决策。本节做事后回填，方便审计与回看。

1. ✅ **字段命名 `sticky_routing_mode`** — 已采用。理由：覆盖更广（含 NewAPI X-Session-Id 等非 cache 场景），与 `prompt_cache_*` 解耦。
   schema：`backend/ent/schema/group.go` enum；migration：`backend/migrations/tk_002_add_groups_sticky_routing_mode.sql`。
2. ✅ **Dashboard 卡片只读最近 24h** — 已采用。`frontend/src/views/admin/DashboardView.vue` 仅暴露 `promptCacheHitRateToday/Total`，不引入时间窗切换。
3. ✅ **derive 兜底用 system 前 2KB** — 已采用。常量：`backend/internal/service/sticky_session_injector.go::stickyDerivedSystemPromptCap = 2 * 1024`。极少数超长 system prompt 撞桶问题观察后再优化。
4. ✅ **NewAPI X-Session-Id 与 OpenAI/Anthropic 注入同 PR** — 已采用（实际是 `a68dee5b` 一次提交全部落地）。访问点：`backend/internal/service/openai_gateway_bridge_dispatch.go::applyStickyToNewAPIBridge`。

---

## 10. 文档同步责任

合入时同步：
- `CLAUDE.md`：在 "Current Gateway Flow" 段加一行 sticky routing 说明
- `docs/agent_integration.md`：由 `scripts/export_agent_contract.py` 自动生成
- `docs/flows/openai-gateway-flow.md`：补充 sticky key 派生节点

---

## 11. 实施情况（Post-hoc / 2026-04-19 盘点）

> 本节为事后归档：代码于 2026-04-18 经单提交 `a68dee5b` 落地 main，并已上线 test/prod。
> 但本设计文档自 2026-04-17 起草后一直处于 `pending` 状态、未走 §3 阶段 2 审批门禁。
> 本节用于把"已发生事实"对齐到本文，方便审计与后续维护。
> 同时已新增 `scripts/preflight.sh` § 1 段 + `scripts/check_approved_docs.py` 机械门禁，避免再发生（见 §11.3）。

### 11.1 已落地事实（与 §3-§5 设计对应）

| 设计章节 | 代码落点 | 状态 |
|---|---|---|
| §3 注入器抽象 | `backend/internal/service/sticky_session_injector.go` + `_test.go` | ✅ |
| §3 schema 字段 | `backend/ent/schema/group.go` (`sticky_routing_mode` enum) + migration `tk_002_add_groups_sticky_routing_mode.sql` | ✅ |
| §3 全局开关 | `backend/internal/service/domain_constants.go::SettingKeyStickyRoutingEnabled` (`gateway.sticky_routing.enabled`) | ✅ |
| §4 6 处接入点 | OpenAI Responses（`openai_gateway_service.go`）/ Chat Compat / passthrough / Anthropic Messages（`gateway_service.go`）/ Messages-to-OpenAI / NewAPI bridge（`openai_gateway_bridge_dispatch.go::applyStickyToNewAPIBridge`） | ✅ |
| §5.6 Dashboard 卡片 | `frontend/src/views/admin/DashboardView.vue` (`promptCacheHitRateToday/Total`) | ✅ |
| §6 测试 | `.testing/user-stories/stories/US-006-sticky-routing-prompt-cache.md` + `sticky_session_injector_test.go` | ✅ |

### 11.2 已知漂移（process debt，登记不修）

1. **测试函数命名**：本文 §6 写 `TestUS201_*`，实际故事是 `US-006`，测试函数为 `TestUS006_*` / `TestStickySessionInjector_*`。已登记到 `docs/preflight-debt.md`。
2. **migration 编号**：本文未指定。实际为 `tk_002_add_groups_sticky_routing_mode.sql`（TK 私有命名空间，不与上游 `0XX_*.sql` 冲突，符合 §5 fork 隔离）。
3. **PR 切分背离**：§7 拟定 8-PR 顺序，实际单提交 `a68dee5b` 落地。代价：发版回滚粒度变粗。
4. **审批门禁缺位**：本文 status=pending 状态下代码 merge，违反 `product-dev.mdc` §阶段 2→审批→§阶段 3 顺序。

### 11.3 后续不做（聚焦 / Jobs 原则）

- **不补回 8-PR 拆分**：代码已上线、回滚链路验证过，补拆纯增 churn。
- **不重命名 `TestUS201_*` → `TestUS006_*`**：rename ~10 函数 + 跑全套测试，与"消除真实风险"的 ROI 不匹配；登记在 `docs/preflight-debt.md` 即可，新增测试遵循 US-006 命名。

### 11.4 已落地的 OPC 机械门禁（防再发）

为把"靠自觉"升级为"靠脚本"（dev-rules `dev-rules-convention.mdc` 强约束）：

- 新增 `scripts/check_approved_docs.py`：扫描 `docs/approved/*.md` frontmatter
  - R1: 必须包含 `status` 字段，且取值在 `{draft, pending, shipped, archived}`
  - R2: `status: pending` 但 `related_prs` / `related_commits` 非空 → **失败**（即"shipped 但 doc 没改"的同款）
  - R3: `status: shipped` 但 `related_prs` 与 `related_commits` 都为空 → **失败**
- 新增 `scripts/preflight.sh` § 1 段调用上述脚本；`pre-commit` hook 与 CI 同步运行。
- 历史事件登记：`docs/preflight-debt.md`。
