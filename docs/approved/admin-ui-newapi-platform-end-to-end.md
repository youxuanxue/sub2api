---
title: Admin UI — 第五平台 `newapi` 端到端可见性与可操作性
status: shipped
approved_by: xuejiao (PR #19 squash-merge)
approved_at: 2026-04-21
shipped_at: 2026-04-21
authors: [agent]
created: 2026-04-20
related_prs: ["#19"]
related_commits: [3a43a109]
related_stories: [US-018]
parent_design: docs/approved/newapi-as-fifth-platform.md
revised: 2026-11-20（删原型清单/stage 散文；保留平台 SSOT、§1.5 与 web_search 契约）
---

# Admin UI — 第五平台 `newapi` 端到端

`status: shipped`（#19）。父设计：[newapi-as-fifth-platform](newapi-as-fifth-platform.md)。
US-018 仍可为 Draft（stage-4 vitest 范围未关）；本文是已合入 UI 契约。

## 0. TL;DR

Admin UI 必须把 `newapi` 当作一等平台：创建分组/账号、列表徽章、筛选、Channels
round-trip。平台选项单一事实源，禁止再散落 4 元素字面量。

## 3.1 单一事实源 — `usePlatformOptions()`

- `frontend/src/constants/gatewayPlatforms.ts` 的 `GATEWAY_PLATFORMS`（含 `newapi`）
  驱动选项。
- `usePlatformOptions()` 供 Groups / Accounts 等消费；不得在视图内硬编码平台列表。

## 3.2–3.3 创建与展示

- `CreateAccountModal` 含 `newapi` segment，接线 `AccountNewApiPlatformFields` +
  `listChannelTypes` / `fetchUpstreamModels`。
- `PlatformTypeBadge` / 颜色 / i18n 须显式支持 `newapi`（cyan / 「New API」）；
  禁止回退成 Gemini 或默认灰。

### 1.5 ChannelsView round-trip（数据丢失护栏）

`ChannelsView` 不得使用缺少 `newapi` 的本地 `platformOrder`。`apiToForm` /
`formToAPI` 必须经 `channelFormConversion.ts`（`apiToFormSections` /
`formSectionsToApi`），canonical order 默认 `GATEWAY_PLATFORMS`。否则打开并保存
含 `newapi` 行的 channel 会静默丢掉 mapping/pricing/分组关联。

`platformColors.ts` 的 `Platform` 联合类型与 variant map 须含 `newapi`。

## Non-goals

不在本文恢复：独立 ops-dashboard 筛选精修、批量改 `channel_type` 破坏性 UX、
品牌名全量 i18n pass（见 follow-up 文推迟项）。

## 渠道 Web Search 平台维度保留（PR #2973）

`features_config.web_search_emulation` 按平台合并：请求内的值覆盖旧值，遗漏的平台
继承；省略整个键时继承原映射，显式空对象或 null 清空。其他 feature 键仍整块替换。
页面与提交共用 `channelFormConversion.ts` 平台白名单；已禁用平台 section 显式提交
false。页面不表示的 API 维度由后端保留。

防覆盖：`gateway-tk.json` / `frontend-tk.json`。验收：US-018 AC-007；
`frontend/e2e/channel-websearch.e2e.ts`。
