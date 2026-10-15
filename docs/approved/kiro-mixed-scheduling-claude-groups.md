---
title: Kiro OAuth mixed_scheduling into Claude (anthropic) groups
status: approved
approved_by: "feng (2026-10-16 会话明确批准：先万能钥路径，再 mixed_scheduling；不动 edge 账号)"
approved_at: 2026-10-16
created: 2026-10-16
owners: [tk-platform]
scope: allow platform=kiro OAuth with Extra.mixed_scheduling into platform=anthropic groups for same-host Claude /v1/messages
risk: high
---

# Kiro mixed_scheduling → Claude 组

## Background

Prod 已有原生 `platform=kiro` OAuth（挂在 `claude-2` / 原名 `kiro`，gid=1368）。
现有 `kiro-uk*` 等是 `platform=anthropic` + `mirror_platform=kiro` 的边侧中继 stub，
与原生 OAuth **不得混为一类**。本变更不改 edge 及其账号。

Claude 组（`platform=anthropic`）直钥此前不能挂、也不能调度 `platform=kiro`。
万能钥路径通过 `user_allowed_groups` 含 1368 + `nativePlatformHintMatchesGroup` 已可落到
kiro 组；本决策补齐 **Claude 组直钥** 同机选到 prod Kiro OAuth。

## Decision

对标 antigravity `mixed_scheduling`，但 **仅 anthropic 组**（不进 gemini 组）：

1. `Account.IsMixedSchedulingEnabled()`：`platform=kiro` 且 `Extra.mixed_scheduling=true` 时为真。
2. 挂组：`accountCompatibleWithGroupPlatform` 允许 anthropic 组绑定上述 kiro 账号。
3. 选号：anthropic 混合调度候选平台追加 `kiro`；未开 flag 的 kiro 账号被过滤。
4. 转发：选中 `IsKiro()` 后走现有 `tkTryForwardKiro` / `kiroGateway`（同机，无 edge hop）。
5. 模型门控：与 mirror stub 共用 `kiroMirrorStubSupportsModel` 降权/清粘性，避免 Fable 等
   非 Kiro catalog 模型粘在 kiro OAuth 上。
6. 调度快照：kiro mixed 变更重建 anthropic mixed bucket；mixed load 含 kiro 并按 flag 过滤。

## Out of scope

- 不改 edge、不改 `IsKiroMirrorStub` 语义、不强制下线 `kiro-uk*`。
- 不把 kiro 混入 gemini 组。
- 不默认把所有 kiro 账号打开 mixed；须显式 `Extra.mixed_scheduling=true` 并挂入 Claude 组。
- 部署后运营绑定账号到 Claude 组（gid=1 等）属发布后运维，不在本代码 PR 内写生产账号。

## Validation

- 单元：membership 允许/拒绝；`IsMixedSchedulingEnabled` kiro 分支；anthropic mixed 选号含/排除 kiro；
  非 catalog 模型对 kiro OAuth 降权或清粘性。
- 回归：antigravity mixed 既有测试保持通过。
- 预发/prod：部署后对试点账号设 `mixed_scheduling=true` 并绑定 Claude 组，用 **direct** Claude 组 key
  打 `/v1/messages`，确认 `account_id` 为 prod `platform=kiro` OAuth（非 stub）。
