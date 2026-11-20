---
title: Sticky Routing & Prompt Cache Optimization
status: approved
approved_by: "xuejiao (design revision, this session, 2026-07-16)"
approved_at: 2026-07-16
previous_version_shipped_at: 2026-04-19
authors: [agent]
created: 2026-04-17
related_prs: []
related_commits: [a68dee5b]
related_stories: [US-006]
revised: 2026-11-20（再压 schema/UI/事后回填；保留策略矩阵、命中率规则与 §11.5/11.6）
---

# Sticky Routing & Prompt Cache Optimization

## 0. TL;DR

让来自同一个客户端会话的请求**尽可能命中上游同一个 prompt cache 块**，把 token 成本压下去（社区数据：长程 Agent 任务可降 60% 上行 token，Anthropic OAuth cache TTL 可保 1 小时）。

> 范围：本设计**仅**补齐"客户端没送 sticky key 时网关自动派生并注入"的能力，以及把现有分散的注入逻辑收口到统一抽象后面，并加全局/分组级开关 + 命中率可视化。**不**修改真 Claude Code 客户端 → Anthropic OAuth 路径（有意识保留客户端原行为）。账号粘性调度主体仍复用既有 Redis session→account 绑定；NewAPI 分组的 session **种子派生**在 §11.6 对 prompt-cache 友好路径做了最小 override（稳定前缀，不改粘账号机制本身）。

## 1. 显式不做

- 真 Claude Code → Anthropic OAuth 直连：`isClaudeCode==true` 时不强制改客户端 sticky。
- 不重复 NewAPI 渠道亲和；不动 Antigravity 内部 SessionID；不改 `usage_logs` schema。

## 2. 架构

### 2.1 统一注入器

Owner：`sticky_session_injector.go`。`Derive` + 各协议 `Inject*`；策略来自
`gateway.sticky_routing.enabled` 与 `group.sticky_routing_mode`
（`auto` / `passthrough` / `off`）。真 Claude Code UA 不强制 metadata 注入。

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

Responses / passthrough / Chat compat / Messages→OpenAI / Anthropic mimic /
NewAPI（`X-Session-Id`）均消费同一 injector；行号以代码为准。

---

## 3. Schema / settings

- Group：`sticky_routing_mode` enum `auto|passthrough|off`（默认 `auto`）；见
  `backend/ent/schema/group.go` / `tk_002_add_groups_sticky_routing_mode.sql`。
- 全局：`gateway.sticky_routing.enabled`（默认 true）。
- 合成：`enabled = global && mode != off`；`mode = group.mode`。
- Derive 兜底 system 前 2KB（`stickyDerivedSystemPromptCap`）。

## 4. UI

Settings：全局 sticky toggle。Groups 高级区：`auto` / `passthrough` / `off` 下拉。

## 5. 命中率可视化

复用 `GET /admin/dashboard/snapshot-v2?include_group_stats=true`，不另开 cache-stats
endpoint。`GroupStat` 含 input/output/cache_creation/cache_read 与
`cache_telemetry_unavailable_input_tokens`（SSOT：`billing_tier='kiro-estimated'`；
不得当 cache miss）。

可观测分母：
`(input - unavailable) + cache_creation + cache_read`。命中率前端按整数聚合；
Dashboard 卡片：当前筛选窗可观测命中率 + 影响量 Top 5 分组钻取；分母为 0 显示「不可观测」，
禁止假 `0%`。无跨平台统一健康阈值色。

验收：US-006 Linked Tests（AC-009…011）。

## 8. 兼容性 & 回滚

默认全局 on + group `auto`。关全局或 group `off`/`passthrough` 即可降级；列默认
`auto`，旧版忽略未知列。

## 11.5 槽满逃逸（upstream #2859，2026-06-03 增补）

sticky 账号并发槽满时，upstream 让用户**原地排队**（`StickySessionMaxWaiting` 深度，默认 3），
即使池里有空账号也不切换。#2859 改为 **OpenAI/newapi 高级调度器：槽满先试全池、有空账号即
逃逸、全池满才回退 sticky `WaitPlan`**（缓存仍热，不比原行为差）。开关
`gateway.sticky_routing.slot_full_escape_enabled`（默认 true / fail-open，可即时回滚）。
anthropic 主网关路径**不动**（上游已在 `wait_queue_full` 时逃逸）。代码：
`openai_account_scheduler.go` 的 `Select` 按 `result.Acquired` 分流 + `IsStickySlotFullEscapeEnabled`。

**决策记录（勿重蹈，A/B/C vs upstream）**：

- 队列机制（sticky + `MaxWaiting` + `WaitPlan`）是 **upstream Wei-Shaw/sub2api 的设计**，非 TK 自造。
  TK 用最小 override（**A 方案**）止血，不删上游分支。
- **不要删 `MaxWaiting` 队列（B）或把 sticky 重写成 load-balance 软加分（C）**：二者局部更简，但在 fork 里
  净删 / 重写上游 = 永久 merge 债（CLAUDE.md §5.x / §5.y）。其简化价值应作为 PR 推 upstream，让 TK 零冲突继承。
- **不要把 `StickySessionMaxWaiting` 设为 0** 来"关掉队列"：(1) `config.go` 校验 `<=0` 直接 fatal，app 起不来；
  (2) 两条 sticky 路径结构不同——OpenAI `selectBySessionHash` 槽满时**无条件**返回 `WaitPlan`（0 → 用户被拒、
  非逃逸），anthropic gateway 才有 `waitingCount < MaxWaiting` 门（0 → 逃逸）。同一配置值在两路径行为相反，是埋雷。
  逃逸决策必须放在"知道池里有没有空账号"的调度代码点，一个全局整数表达不了。
- 逃逸 / `#656` 谓词的 load-bearing 锚点见 `scripts/sentinels/gateway-tk.json`（防上游 merge 静默回退）。

## 11.6 NewAPI / Volc prompt-cache 账号粘性（2026-09-18）

目标：同 session 优先粘同一上游账号，抬高 Volc Plan（含 GLM）隐式 prefix cache 命中率。

背景（`rt@tk.com` / Acceptance-Test 实证）：NewAPI 账号 sticky 机制已存在，但默认
`GenerateSessionHash` 内容种子含 **first user turn**。共享长 system/tools、独立 user
问句的「重复组」因此每次换 hash → 打散到池内多个账号，prefix cache 失效。同账号连续
打可 99%+ 命中。

落地（最小 override，OpenAI OAuth 路径不变）：

| 行为 | Owner |
|---|---|
| NewAPI 分组无显式 session 时，用 **stable prefix**（system/tools/instructions）+ `api_key_id`+model 派生 sticky seed | `openai_gateway_scheduling_tk_newapi_prompt_cache.go` + `GenerateSessionHash` |

万能 key / 非 NewAPI 分组语义不变。槽满逃逸（§11.5）仍适用。
