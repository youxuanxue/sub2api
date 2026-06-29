---
title: newapi 分组图片生成开关与 tk_049 存量回填
status: pending
approved_by: pending
authors: [agent]
created: 2026-06-29
related_prs: []
related_commits: []
parent_design: docs/approved/admin-ui-newapi-platform-end-to-end.md
---

# newapi 分组图片生成开关与 tk_049 存量回填

## 背景

Migration 134 只为 `openai` / `gemini` / `antigravity` 回填了 `groups.allow_image_generation`。
含 Vertex Imagen（channel_type 41）或 VolcEngine Seedream（45）的 `newapi` 分组在 prod 仍为
默认 `false`，导致 `/v1/images/generations` 与全能 Key imagen 路由报「该分组未开通此类生成」。

Admin 后端字段与 API 已支持 `allow_image_generation`，但 `GroupsView` 的「图片生成计费」区块
原先未包含 `platform=newapi`。

## 变更

1. **Admin UI**：`supportsGroupImagePricing()` 将 `newapi` 纳入图片生成计费开关（与 openai/gemini/antigravity 一致）。
2. **一次性迁移 `tk_049`**：为已有 Vertex/Seedream 账号绑定的 `newapi` 分组 `UPDATE allow_image_generation=true`。
3. **全能 Key 路由**：`/v1/images/generations` 形状在 pick 前过滤 `allow_image_generation=false` 的分组。

## 不在范围

- gemini-native 经 `/v1/chat/completions` 的生图不走 `allow_image_generation` 列（沿用 chat 计费路径）。
- 不新增独立图片协议入口。

## 验证

- 单元/前端测试覆盖 resolver 过滤、inlineData→markdown、BakeOff 路由分流、Admin 平台 gate。
- prod BakeOff / ImageStudio 实机复现：PR 验证节标注为待人工确认。
