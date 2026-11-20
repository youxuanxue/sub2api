---
title: "NewAPI 第五平台 — Bug 修复 + UI 暴露 3 个真实影响转发的字段（PR #19 之后）"
status: shipped
approved_by: xuejiao (PR #29 squash-merge)
authors: [agent]
created: 2026-04-22
related_prs: ["#29"]
related_stories: [US-019, US-020, US-021, US-022, US-023, US-024, US-025, US-026]
parent_design: docs/approved/admin-ui-newapi-platform-end-to-end.md
revised: 2026-11-20（删 audit round 散文；保留字段契约与已合入修复边界）
---

# NewAPI follow-up（shipped）

父设计：[admin-ui-newapi-platform-end-to-end](admin-ui-newapi-platform-end-to-end.md)。平台契约：[newapi-as-fifth-platform](newapi-as-fifth-platform.md)。本文件只保留仍有效的字段与调度边界；逐轮 audit 叙事已删。

## 转发字段（credentials / bridge）

均在 `accounts.credentials` jsonb；不改 schema。空值/空对象 = 不传。

| key | 类型 | 行为 |
| --- | --- | --- |
| `model_mapping` | JSON object | 与 `Account.GetModelMapping()` 兼容 |
| `status_code_mapping` | JSON-string of object | bridge → Gin `status_code_mapping`；`PopulateContextKeys` 跳过 `""` / `"{}"` |
| `openai_organization` | string | 出站 `OpenAI-Organization`；空 = 不设 |

Owner：UI `AccountNewApiPlatformFields.vue`；装载 `newapi_bridge_usage.go` / `ChannelContextInput.StatusCodeMappingJSON`。

## 已合入修复边界（勿回退）

- 调度平台列表以 `AllSchedulingPlatforms()` 为 SSOT；snapshot rebuild/defaultBuckets 不得再硬编码「四平台」。
- Moonshot newapi：`CreateAccount` / `UpdateAccount` 经 `MaybeResolveMoonshotBaseURLForNewAPI`；探测失败上抛，禁止静默回退。
- Admin/runtime 路径须识别 `PlatformNewAPI`（分组 oneof、默认组 seed、连接测试、可用模型、relay 错误 JSON、429 reset 解析、ops retry 分类、批量 Create 的 `ChannelType`/`LoadFactor`、Token Stats 过滤等）。细节与回归见 US-019…026 Linked Tests。

## 仍明确推迟

- 完整 HTTP+PG e2e：US-008/009/010（Draft）。
- `BulkEditAccountModal` 批量改 `channel_type` 守卫。
- 平台徽章 i18n 统一 pass。
