---
title: OpenAI-compat Messages 自动压缩策略（账号/分组）
status: shipped
approved_by: xuejiao (release gate approval 2026-05-07)
approved_at: 2026-05-07
authors: [agent]
created: 2026-05-07
related_prs: []
related_commits: [aa68310a, 08d2ebc8, f9cfe5c5]
related_stories: [US-034]
---

# OpenAI-compat Messages 自动压缩策略（账号/分组）

## 0. TL;DR

为缓解 OpenAI-compat `/v1/messages` 在长会话下的输入膨胀，本变更引入**按账号优先、分组兜底**的自动压缩策略：

- 优先级：`account.extra > group`；
- 仅当策略显式启用且 `input_tokens` 达阈值时触发；
- 无账号/分组配置时保持现状（不压缩）；
- 在 `previous_response_id` 回退路径新增结构化观测，区分 `not_found` 与 `unsupported`。

## 1. 背景与问题

现网日志显示 `input_tokens` 在长轮次场景可达 20 万+，并伴随 `previous_response_id` 回退。用户体感表现为：轮次变长后响应完成度下降，手动 compact 后显著改善。需要在兼容链路内提供受控的自动压缩能力，并可观测回退行为。

## 2. 设计目标

1. 在不引入全局配置项的前提下，提供最小可控压缩开关；
2. 保持 upstream 主文件最小侵入：策略逻辑下沉到 TK companion；
3. 压缩逻辑复用现有 guard/trim，不新增第二套裁剪算法；
4. 回退路径产出结构化指标，便于区分兼容问题类型；
5. 行为默认不变：未配置即不生效。

## 3. 范围与非目标

### 3.1 范围

- OpenAI-compat `/v1/messages` 路径；
- 分组 schema 新增策略字段；
- 账号 `extra` 可覆盖分组策略；
- 回退路径结构化日志增强。

### 3.2 非目标

- 不新增全局 config 字段；
- 不改变 OAuth 路径语义；
- 不引入会话级持久化 continuation 映射；
- 不改变公共 API 路由或状态码契约。

## 4. 数据模型 / Schema（高风险项）

### 4.1 变更

`groups` 表新增两列：

- `messages_compaction_enabled`（boolean，可空）
- `messages_compaction_input_tokens_threshold`（bigint，可空）

对应迁移：

- `backend/migrations/135_add_messages_compaction_policy_to_groups.sql`

### 4.2 兼容性

- 字段可空，旧数据无需回填；
- 未配置时逻辑等价于“关闭策略”；
- 回滚可通过回退代码 + 回滚迁移处理。

## 5. 策略决策与优先级

### 5.1 解析规则

1. 先读 `account.extra`：
   - `messages_compaction_enabled`
   - `messages_compaction_input_tokens_threshold`
2. 若账号显式关闭，直接关闭；
3. 若账号未给出有效策略，再读 `group` 字段；
4. 阈值 `< 1` 视为无效；
5. 最终未得到“启用 + 有效阈值”则不触发。

### 5.2 触发条件

- 仅在 compat replay guard 场景命中时评估；
- `input_tokens >= threshold` 才执行压缩；
- 压缩执行复用已有 replay guard 裁剪路径。

## 6. 回退路径可观测性

在 `previous_response_id` 失败回退时记录：

- `compat_previous_response_fallback_reason`: `not_found | unsupported`
- `compat_continuation_disabled_after_fallback`: `true | false`
- `compat_previous_response_retry_without_continuation`: `true`

语义：

- `not_found`：删除 response_id 后重试；
- `unsupported`：禁用 continuation 后重试。

## 7. 实现映射（代码锚点）

- 主注入点：`backend/internal/service/openai_gateway_messages.go`
- TK companion：`backend/internal/service/openai_messages_compaction_tk.go`
- 分组模型：`backend/internal/service/group.go`
- admin/service/repo/cache/dto 透传链路：
  - `backend/internal/service/admin_service.go`
  - `backend/internal/repository/group_repo.go`
  - `backend/internal/repository/api_key_repo.go`
  - `backend/internal/service/api_key_auth_cache.go`
  - `backend/internal/service/api_key_auth_cache_impl.go`
  - `backend/internal/handler/dto/types.go`
  - `backend/internal/handler/dto/mappers.go`
- 覆写防护门禁：`scripts/engine-facade-sentinels.json`

## 8. 风险与控制

1. 压缩过度造成上下文丢失
   - 控制：复用既有 guard/trim，保证 tool 边界不破坏。
2. continuation 回退行为回归
   - 控制：仅增强观测字段，不改状态机语义。
3. 配置透传断链
   - 控制：补齐 admin/repo/cache/DTO 回归测试。

## 9. 验证矩阵

### 9.1 单元/服务测试

- `backend/internal/service/openai_compat_model_test.go`
  - 回退 `not_found` 与 `unsupported` 分类日志语义；
- `backend/internal/service/openai_messages_compaction_tk_test.go`
  - 账号/分组优先级、显式关闭覆盖、阈值判定；
- `backend/internal/service/openai_messages_replay_guard_test.go`
  - 压缩触发时不破坏 replay 边界。

### 9.2 透传链路

- `backend/internal/service/admin_service_group_test.go`
- `backend/internal/service/api_key_service_cache_test.go`
- `backend/internal/server/api_contract_test.go`

目标：确认新增字段在 create/update/snapshot/contract 路径完整透传。

### 9.3 命令

```bash
cd backend
go test -tags=unit ./internal/service/... -run "TestForwardAsAnthropic|TestOpenAICompatContinuation|TestOpenAICompatMessagesCompaction|TestAdminService.*Group|TestAPIKeyService"
go test -tags=unit ./internal/server/... -run "TestAPIContract"
go build ./...

cd ..
bash scripts/preflight.sh
```

## 10. 回滚策略

1. 先回滚业务逻辑注入与 companion 调用；
2. 保留列但关闭策略（兼容回退窗口）；
3. 如需彻底回滚 schema，再执行迁移回退并重建相关快照断言。

## 11. 审批门禁

本文件为高风险变更审批锚点。`approved_by: pending` 期间，PR 不应合并。需人工完成设计审批后进入合并确认阶段。
